package compat

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestFlattenAndRestoreNamespaceTools(t *testing.T) {
	namespace := "mcp__bifrost"
	toolName := "exa-web_search_exa"
	callType := schemas.ResponsesMessageTypeFunctionCall
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenRouter,
		Model:    "openai/gpt-5-mini",
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
			Type: schemas.ResponsesToolTypeNamespace,
			Name: &namespace,
			ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{{
				Type: schemas.ResponsesToolTypeFunction,
				Name: &toolName,
			}}},
		}}},
		Input: []schemas.ResponsesMessage{{
			Type: &callType,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Name:      &toolName,
				Namespace: &namespace,
			},
		}},
	}

	codec, err := flattenNamespaceTools(req)
	if err != nil {
		t.Fatal(err)
	}
	flatName := "mcp__bifrost__exa-web_search_exa"
	if codec == nil || len(codec.byFlat) != 1 {
		t.Fatalf("expected one namespace mapping, got %#v", codec)
	}
	if got := *req.Params.Tools[0].Name; got != flatName {
		t.Fatalf("flattened tool name = %q, want %q", got, flatName)
	}
	if got := *req.Input[0].Name; got != flatName {
		t.Fatalf("flattened history name = %q, want %q", got, flatName)
	}
	if req.Input[0].Namespace != nil {
		t.Fatalf("flattened history retained namespace %q", *req.Input[0].Namespace)
	}

	response := &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{{
		Type:                 &callType,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr(flatName)},
	}}}
	restoreNamespacedResponse(response, codec)
	if got := *response.Output[0].Name; got != toolName {
		t.Fatalf("restored tool name = %q, want %q", got, toolName)
	}
	if got := *response.Output[0].Namespace; got != namespace {
		t.Fatalf("restored namespace = %q, want %q", got, namespace)
	}
}

func TestFlattenNamespaceToolsPreservesNativeOpenAI(t *testing.T) {
	namespace := "mcp__bifrost"
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
			Type: schemas.ResponsesToolTypeNamespace,
			Name: &namespace,
		}}},
	}

	if codec, err := flattenNamespaceTools(req); err != nil || codec != nil {
		t.Fatalf("expected no codec for OpenAI, got %#v", codec)
	}
	if req.Params.Tools[0].Type != schemas.ResponsesToolTypeNamespace {
		t.Fatalf("OpenAI namespace tool was modified")
	}
}

func TestFlattenNamespaceToolsUsesCollisionSafeAlias(t *testing.T) {
	namespace := "mcp__bifrost"
	toolName := "exa-web_search_exa"
	canonical := namespace + namespaceToolSeparator + toolName
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenRouter,
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeFunction, Name: &canonical},
			{
				Type: schemas.ResponsesToolTypeNamespace,
				Name: &namespace,
				ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{{
					Type: schemas.ResponsesToolTypeFunction,
					Name: &toolName,
				}}},
			},
		}},
	}

	codec, err := flattenNamespaceTools(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Params.Tools) != 2 {
		t.Fatalf("expected the native and flattened tools, got %d", len(req.Params.Tools))
	}
	if got := *req.Params.Tools[1].Name; got == canonical || len(got) > maxProviderFunctionNameLen {
		t.Fatalf("collision alias = %q", got)
	}
	if got := codec.byFlat[*req.Params.Tools[1].Name]; got != (namespaceToolID{namespace: namespace, name: toolName}) {
		t.Fatalf("collision alias restored to %#v", got)
	}
}

func TestFlattenNamespaceToolsCompactsLongNamesAndToolChoice(t *testing.T) {
	namespace := "mcp__a_very_long_server_name_that_exceeds_the_portable_limit"
	toolName := "a_very_long_tool_name_that_also_exceeds_the_limit"
	choiceType := schemas.ResponsesToolChoiceTypeFunction
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenRouter,
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{
				Type: schemas.ResponsesToolTypeNamespace,
				Name: &namespace,
				ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{{
					Type: schemas.ResponsesToolTypeFunction,
					Name: &toolName,
				}}},
			}},
			ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type:      choiceType,
				Name:      &toolName,
				Namespace: &namespace,
			}},
		},
	}

	codec, err := flattenNamespaceTools(req)
	if err != nil {
		t.Fatal(err)
	}
	flatName := *req.Params.Tools[0].Name
	if len(flatName) > maxProviderFunctionNameLen || flatName[:len(compactNamespaceNamePrefix)] != compactNamespaceNamePrefix {
		t.Fatalf("long tool was not compacted safely: %q", flatName)
	}
	if got := *req.Params.ToolChoice.ResponsesToolChoiceStruct.Name; got != flatName {
		t.Fatalf("tool choice = %q, want %q", got, flatName)
	}
	if codec.byFlat[flatName] != (namespaceToolID{namespace: namespace, name: toolName}) {
		t.Fatalf("compacted name is not reversible")
	}
}

func TestRestoreNamespacedStreamResponse(t *testing.T) {
	flatName := "mcp__bifrost__exa-web_search_exa"
	stream := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
		Item: &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: &flatName},
		},
	}
	codec := &namespaceToolCodec{
		byFlat: map[string]namespaceToolID{
			flatName: {namespace: "mcp__bifrost", name: "exa-web_search_exa"},
		},
	}

	restoreNamespacedStreamResponse(stream, codec)
	if got := *stream.Item.Name; got != "exa-web_search_exa" {
		t.Fatalf("restored stream tool name = %q", got)
	}
	if got := *stream.Item.Namespace; got != "mcp__bifrost" {
		t.Fatalf("restored stream namespace = %q", got)
	}
}
