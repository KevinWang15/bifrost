package bifrost

import (
	"container/list"
	"crypto/sha256"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

const (
	reasoningRejectionCacheLimit = 16384
	reasoningRejectionCacheTTL   = 24 * time.Hour
	// A single very long history must not evict the entire cache or keep an
	// unbounded pending set while its retry streams.
	reasoningRecoveryTokenLimit = 1024
)

// reasoningRejectionCache remembers successful fail-soft rewrites, not a blanket
// ban on reasoning for a provider. Only SHA-256 fingerprints are retained. The
// cache belongs to one Bifrost instance; it needs neither a sweeper nor ctx buffers.
//
// TODO: Before deploying Bifrost in HA (multiple instances), update this cache
// to share rejection decisions across instances and avoid repeated recovery retries.
type reasoningRejectionCache struct {
	active  atomic.Bool
	mu      sync.Mutex
	entries map[[32]byte]*list.Element
	lru     list.List
	limit   int
	ttl     time.Duration
}

type reasoningRejectionEntry struct {
	key     [32]byte
	expires time.Time
}

func reasoningRejectionKey(scope, token [32]byte) [32]byte {
	var data [64]byte
	copy(data[:32], scope[:])
	copy(data[32:], token[:])
	return sha256.Sum256(data[:])
}

func (c *reasoningRejectionCache) contains(scope [32]byte, payload string) bool {
	if c == nil || !c.active.Load() || payload == "" {
		return false
	}
	key := reasoningRejectionKey(scope, sha256.Sum256([]byte(payload)))
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		return false
	}
	if !time.Now().Before(e.Value.(reasoningRejectionEntry).expires) {
		delete(c.entries, key)
		c.lru.Remove(e)
		return false
	}
	c.lru.MoveToFront(e)
	return true
}

func (c *reasoningRejectionCache) remember(scope [32]byte, tokens map[[32]byte]struct{}) {
	if c == nil || len(tokens) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[[32]byte]*list.Element)
	}
	limit, ttl := c.limit, c.ttl
	if limit == 0 {
		limit = reasoningRejectionCacheLimit
	}
	if ttl == 0 {
		ttl = reasoningRejectionCacheTTL
	}
	expires := time.Now().Add(ttl)
	for token := range tokens {
		key := reasoningRejectionKey(scope, token)
		entry := reasoningRejectionEntry{key: key, expires: expires}
		if e := c.entries[key]; e != nil {
			e.Value = entry
			c.lru.MoveToFront(e)
		} else {
			c.entries[key] = c.lru.PushFront(entry)
		}
		for len(c.entries) > limit {
			e := c.lru.Back()
			delete(c.entries, e.Value.(reasoningRejectionEntry).key)
			c.lru.Remove(e)
		}
	}
	c.active.Store(true)
}

// An item-id or prompt-prefix mismatch can be repaired by the client while
// keeping the same token. Do not remember it as an upstream incompatibility.
func canRememberReasoningRejection(err *schemas.BifrostError) bool {
	if err == nil || err.Error == nil {
		return false
	}
	message := strings.ToLower(err.Error.Message)
	return !strings.Contains(message, "item_id") && !strings.Contains(message, "item id") &&
		!strings.Contains(message, "prefix mismatch") && !strings.Contains(message, "prefix_mismatch")
}

// Keep authentication and routing separate from administrative key metadata.
// All forwarded headers remain part of the identity: even an unfamiliar header
// can select an account or endpoint on a custom upstream.
type reasoningRejectionIdentity struct {
	Provider              schemas.ModelProvider
	Model                 string
	KeyID                 string
	Credential            string
	Azure                 *schemas.AzureKeyConfig
	Vertex                *schemas.VertexKeyConfig
	Bedrock               *schemas.BedrockKeyConfig
	BedrockMantle         *schemas.BedrockMantleKeyConfig
	VLLM                  *schemas.VLLMKeyConfig
	Replicate             *schemas.ReplicateKeyConfig
	Ollama                *schemas.OllamaKeyConfig
	SGL                   *schemas.SGLKeyConfig
	Databricks            *schemas.DatabricksKeyConfig
	GithubCopilot         *schemas.GithubCopilotKeyConfig
	UseAnthropicEndpoints bool
	UseOpenAIEndpoints    bool
	Alias                 *schemas.AliasConfig
	BaseURL               string
	ProviderHeaders       map[string]string
	CustomProvider        *schemas.CustomProviderConfig
	VirtualKeyID          string
	UserID                string
	CallerHeaders         map[string][]string
	URLPath               string
	VirtualKeyToken       string
}

// Include credentials and provider-specific endpoint/region configuration, so
// key rotation and hot reload cannot reuse a rejection from a different upstream.
// MarshalSorted also stabilizes extra-header maps. None of these bytes are stored
// or logged; only the digest survives. Alias config includes per-key deployments.
func reasoningRejectionScope(ctx *schemas.BifrostContext, config *schemas.ProviderConfig, provider schemas.ModelProvider, model string, key schemas.Key) ([32]byte, bool) {
	virtualKey, user := sessionIdentityParts(ctx)
	identity := reasoningRejectionIdentity{
		Provider:              provider,
		Model:                 model,
		KeyID:                 key.ID,
		Credential:            key.Value.GetValue(),
		Azure:                 key.AzureKeyConfig,
		Vertex:                key.VertexKeyConfig,
		Bedrock:               key.BedrockKeyConfig,
		BedrockMantle:         key.BedrockMantleKeyConfig,
		VLLM:                  key.VLLMKeyConfig,
		Replicate:             key.ReplicateKeyConfig,
		Ollama:                key.OllamaKeyConfig,
		SGL:                   key.SGLKeyConfig,
		Databricks:            key.DatabricksKeyConfig,
		GithubCopilot:         key.GithubCopilotKeyConfig,
		UseAnthropicEndpoints: key.UseAnthropicEndpoints != nil && *key.UseAnthropicEndpoints,
		UseOpenAIEndpoints:    key.UseOpenAIEndpoints != nil && *key.UseOpenAIEndpoints,
		Alias:                 key.Aliases.ResolveConfig(model),
		BaseURL:               config.NetworkConfig.BaseURL,
		ProviderHeaders:       config.NetworkConfig.ExtraHeaders,
		CustomProvider:        config.CustomProviderConfig,
		VirtualKeyID:          virtualKey,
		UserID:                user,
	}
	if identity.Alias != nil {
		alias := *identity.Alias
		alias.Description = ""
		identity.Alias = &alias
	}
	if ctx != nil {
		identity.CallerHeaders, _ = ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
		identity.URLPath, _ = ctx.Value(schemas.BifrostContextKeyURLPath).(string)
		identity.VirtualKeyToken, _ = ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
	}
	body, err := providerUtils.MarshalSorted(identity)
	if err != nil {
		return [32]byte{}, false
	}
	return sha256.Sum256(body), true
}

type reasoningPayloadSelector func(string) bool

// Ordinary requests do not hash credentials or allocate detached carrier structs.
func hasReplayableReasoning(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) bool {
	if req == nil {
		return false
	}
	if ctx != nil {
		if large, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); large {
			return false
		}
		if raw, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); raw {
			var body []byte
			if req.ChatRequest != nil {
				body = req.ChatRequest.RawRequestBody
			} else if _, ref := encryptedReasoningCarriers(req); ref != nil {
				body = *ref
			}
			found := false
			var visit func(gjson.Result)
			visit = func(value gjson.Result) {
				if found {
					return
				}
				value.ForEach(func(key, child gjson.Result) bool {
					if key.Str == "encrypted_content" || key.Str == "signature" || key.Str == "data" && value.Get("type").Str == "redacted_thinking" {
						found = true
					}
					if key.Str == "call_id" && strings.Contains(child.Str, providerUtils.ThoughtSignatureSeparator) {
						found = true
					}
					if child.IsArray() || child.IsObject() {
						visit(child)
					}
					return !found
				})
			}
			visit(gjson.ParseBytes(body))
			return found
		}
	}
	if chat := req.ChatRequest; chat != nil {
		for _, message := range chat.Input {
			if a := message.ChatAssistantMessage; a != nil {
				for _, d := range a.ReasoningDetails {
					if d.Signature != nil || d.Data != nil {
						return true
					}
				}
				for _, call := range a.ToolCalls {
					if call.ID != nil && strings.Contains(*call.ID, providerUtils.ThoughtSignatureSeparator) {
						return true
					}
				}
			}
			if tool := message.ChatToolMessage; tool != nil && tool.ToolCallID != nil && strings.Contains(*tool.ToolCallID, providerUtils.ThoughtSignatureSeparator) {
				return true
			}
		}
		return false
	}
	if input, _ := encryptedReasoningCarriers(req); input != nil {
		for _, message := range *input {
			if r := message.ResponsesReasoning; r != nil && r.EncryptedContent != nil {
				return true
			}
			if c := message.Content; c != nil {
				for _, b := range c.ContentBlocks {
					if b.Signature != nil || b.EncryptedContent != nil {
						return true
					}
				}
			}
			if tool := message.ResponsesToolMessage; tool != nil && tool.CallID != nil && strings.Contains(*tool.CallID, providerUtils.ThoughtSignatureSeparator) {
				return true
			}
		}
	}
	return false
}

// Detach the carrier structs before assigning rewritten slices. The worker's
// request is a shallow copy of the caller's, and a later fallback must still see
// the original history. The payloads themselves are copied only when edited.
func detachReasoningCarriers(req *schemas.BifrostRequest) func() {
	if req == nil {
		return func() {}
	}
	if req.ChatRequest != nil {
		copy := *req.ChatRequest
		req.ChatRequest = &copy
		input, body := copy.Input, copy.RawRequestBody
		return func() { copy.Input, copy.RawRequestBody = input, body }
	}
	if req.ResponsesRequest != nil {
		copy := *req.ResponsesRequest
		req.ResponsesRequest = &copy
	}
	if req.CountTokensRequest != nil {
		copy := *req.CountTokensRequest
		req.CountTokensRequest = &copy
	}
	if req.CompactionRequest != nil {
		copy := *req.CompactionRequest
		req.CompactionRequest = &copy
	}
	input, body := encryptedReasoningCarriers(req)
	if input == nil {
		return func() {}
	}
	originalInput, originalBody := *input, *body
	return func() { *input, *body = originalInput, originalBody }
}

// A nil selector preserves the existing strip-all recovery. A selector enables
// both collecting the removed tokens and filtering only previously rejected ones.
func selectReasoningPayload(selector reasoningPayloadSelector, payloads ...*string) bool {
	matched := false
	for _, p := range payloads {
		if p != nil && (selector == nil || selector(*p)) {
			matched = true
		}
	}
	return matched
}

func stripReasoningCallID(id string, selector reasoningPayloadSelector) string {
	base := providerUtils.StripThoughtSignature(id)
	if base != id && selectReasoningPayload(selector, schemas.Ptr(id[len(base):])) {
		return base
	}
	return id
}

// Stream setup is not success: an HTTP 200 can still end in an error. Learn only
// after a successful terminal event. The pending set holds bounded hashes only.
func rememberReasoningStream(ctx *schemas.BifrostContext, input chan *schemas.BifrostStreamChunk, cache *reasoningRejectionCache, scope [32]byte, tokens map[[32]byte]struct{}) chan *schemas.BifrostStreamChunk {
	output := make(chan *schemas.BifrostStreamChunk)
	go func() {
		defer close(output)
		failed, completed, forward := false, false, true
		for chunk := range input {
			if chunk.BifrostError != nil {
				failed = true
			}
			if r := chunk.BifrostResponsesStreamResponse; r != nil {
				if r.Type == schemas.ResponsesStreamResponseTypeCompleted {
					completed = true
				}
				if r.Type == schemas.ResponsesStreamResponseTypeIncomplete || r.Type == schemas.ResponsesStreamResponseTypeFailed {
					failed = true
				}
			}
			if r := chunk.BifrostChatResponse; r != nil {
				for _, choice := range r.Choices {
					if choice.FinishReason != nil {
						completed = true
					}
				}
			}
			if forward {
				select {
				case output <- chunk:
				case <-ctx.Done():
					failed, forward = true, false
				}
			}
		}
		if completed && !failed {
			cache.remember(scope, tokens)
		}
	}()
	return output
}
