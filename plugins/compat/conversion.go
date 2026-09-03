package compat

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	namespaceToolSeparator     = "__"
	maxProviderFunctionNameLen = 64
	compactNamespaceNamePrefix = "bfns_"
)

type namespaceToolID struct {
	namespace string
	name      string
}

// namespaceToolCodec owns the reversible wire-name mapping for one request.
// It deliberately lives in request context: provider-visible aliases can be
// shortened or disambiguated, so parsing a flat name by delimiter is unsafe.
type namespaceToolCodec struct {
	byFlat      map[string]namespaceToolID
	byLogical   map[namespaceToolID]string
	byCanonical map[string]string
}

type namespaceToolCodecContextKey struct{}

// providerSupportsNamespaceTools is the provider capability boundary for the
// OpenAI Responses namespace extension. Unknown and custom providers default
// to the portable function-tool representation.
func providerSupportsNamespaceTools(provider schemas.ModelProvider, model string) bool {
	switch provider {
	case schemas.OpenAI:
		return true
	case schemas.Azure:
		return !schemas.IsAnthropicModel(model)
	default:
		return false
	}
}

// applyParameterConversion rewrites request fields in place for provider compatibility.
func applyParameterConversion(req *schemas.BifrostRequest) *namespaceToolCodec {
	if req == nil || req.ResponsesRequest == nil {
		return nil
	}
	return flattenNamespaceTools(req.ResponsesRequest)
}

// flattenNamespaceTools expands namespace-scoped tools into ordinary function
// tools for Responses-compatible providers that do not implement OpenAI's
// namespace extension. All model-visible references are rewritten through the
// same request-local codec so responses and replayed history remain symmetric.
func flattenNamespaceTools(req *schemas.BifrostResponsesRequest) *namespaceToolCodec {
	if req == nil || req.Params == nil || providerSupportsNamespaceTools(req.Provider, req.Model) {
		return nil
	}

	codec := newNamespaceToolCodec(req.Params.Tools)
	if codec == nil {
		return nil
	}

	flattened := make([]schemas.ResponsesTool, 0, len(req.Params.Tools)+len(codec.byFlat))
	for _, tool := range req.Params.Tools {
		if tool.Type != schemas.ResponsesToolTypeNamespace || tool.ResponsesToolNamespace == nil {
			flattened = append(flattened, tool)
			continue
		}
		for _, nested := range tool.ResponsesToolNamespace.Tools {
			if tool.Name == nil || nested.Name == nil {
				continue
			}
			flatName, ok := codec.byLogical[namespaceToolID{namespace: *tool.Name, name: *nested.Name}]
			if !ok {
				continue
			}
			nested.Name = schemas.Ptr(flatName)
			flattened = append(flattened, nested)
		}
	}
	req.Params.Tools = flattened
	flattenNamespacedInput(req.Input, codec)
	flattenNamespacedToolChoice(req.Params.ToolChoice, codec)
	return codec
}

func newNamespaceToolCodec(tools []schemas.ResponsesTool) *namespaceToolCodec {
	used := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if tool.Type != schemas.ResponsesToolTypeNamespace && tool.Name != nil {
			used[*tool.Name] = struct{}{}
		}
	}

	codec := &namespaceToolCodec{
		byFlat:      make(map[string]namespaceToolID),
		byLogical:   make(map[namespaceToolID]string),
		byCanonical: make(map[string]string),
	}
	for _, namespaceTool := range tools {
		if namespaceTool.Type != schemas.ResponsesToolTypeNamespace ||
			namespaceTool.Name == nil || *namespaceTool.Name == "" ||
			namespaceTool.ResponsesToolNamespace == nil {
			continue
		}
		for _, nested := range namespaceTool.ResponsesToolNamespace.Tools {
			if nested.Name == nil || *nested.Name == "" {
				continue
			}
			id := namespaceToolID{namespace: *namespaceTool.Name, name: *nested.Name}
			canonical := id.namespace + namespaceToolSeparator + id.name
			flatName := canonical
			if len(flatName) > maxProviderFunctionNameLen {
				flatName = compactNamespaceToolName(id)
			}
			if _, collision := used[flatName]; collision {
				flatName = compactNamespaceToolName(id)
			}
			// A SHA-256-derived alias collision is not realistically reachable, but
			// fail closed rather than silently routing a call to the wrong tool.
			if existing, collision := codec.byFlat[flatName]; collision && existing != id {
				continue
			}
			used[flatName] = struct{}{}
			codec.byFlat[flatName] = id
			codec.byLogical[id] = flatName
			if previous, exists := codec.byCanonical[canonical]; !exists || previous == flatName {
				codec.byCanonical[canonical] = flatName
			} else {
				delete(codec.byCanonical, canonical)
			}
		}
	}
	if len(codec.byFlat) == 0 {
		return nil
	}
	return codec
}

func compactNamespaceToolName(id namespaceToolID) string {
	digest := sha256.Sum256([]byte(id.namespace + "\x00" + id.name))
	// 5-byte prefix + 28 digest bytes encoded as hex = 61 characters.
	return compactNamespaceNamePrefix + hex.EncodeToString(digest[:28])
}

// flattenNamespacedInput keeps replayed function_call history consistent with
// the flattened tool definitions sent to the upstream provider.
func flattenNamespacedInput(input []schemas.ResponsesMessage, codec *namespaceToolCodec) {
	if codec == nil {
		return
	}
	for i := range input {
		toolMessage := input[i].ResponsesToolMessage
		if toolMessage == nil || toolMessage.Name == nil || toolMessage.Namespace == nil {
			continue
		}
		flatName, ok := codec.byLogical[namespaceToolID{namespace: *toolMessage.Namespace, name: *toolMessage.Name}]
		if !ok {
			continue
		}
		messageCopy := *toolMessage
		messageCopy.Name = schemas.Ptr(flatName)
		messageCopy.Namespace = nil
		input[i].ResponsesToolMessage = &messageCopy
	}
}

func flattenNamespacedToolChoice(choice *schemas.ResponsesToolChoice, codec *namespaceToolCodec) {
	if choice == nil || choice.ResponsesToolChoiceStruct == nil || codec == nil {
		return
	}
	choiceStruct := choice.ResponsesToolChoiceStruct
	if choiceStruct.Name != nil {
		if flat, ok := codec.byCanonical[*choiceStruct.Name]; ok {
			choiceStruct.Name = schemas.Ptr(flat)
		}
	}
	for i := range choiceStruct.Tools {
		if choiceStruct.Tools[i].Name == nil {
			continue
		}
		if flat, ok := codec.byCanonical[*choiceStruct.Tools[i].Name]; ok {
			choiceStruct.Tools[i].Name = schemas.Ptr(flat)
		}
	}
}

func restoreNamespacedMessage(message *schemas.ResponsesMessage, codec *namespaceToolCodec) {
	if message == nil || message.ResponsesToolMessage == nil || message.ResponsesToolMessage.Name == nil || codec == nil {
		return
	}
	mapped, ok := codec.byFlat[*message.ResponsesToolMessage.Name]
	if !ok {
		return
	}
	messageCopy := *message.ResponsesToolMessage
	messageCopy.Name = schemas.Ptr(mapped.name)
	messageCopy.Namespace = schemas.Ptr(mapped.namespace)
	message.ResponsesToolMessage = &messageCopy
}

func restoreNamespacedResponse(response *schemas.BifrostResponsesResponse, codec *namespaceToolCodec) {
	if response == nil {
		return
	}
	for i := range response.Output {
		restoreNamespacedMessage(&response.Output[i], codec)
	}
}

func restoreNamespacedStreamResponse(response *schemas.BifrostResponsesStreamResponse, codec *namespaceToolCodec) {
	if response == nil {
		return
	}
	restoreNamespacedMessage(response.Item, codec)
	restoreNamespacedResponse(response.Response, codec)
}
