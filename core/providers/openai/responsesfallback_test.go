package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestShouldFallbackResponsesToChat(t *testing.T) {
	tests := []struct {
		name        string
		providerKey string
		model       string
		responsesOp schemas.RequestType
		chatOp      schemas.RequestType
		allowed     *schemas.AllowedRequests
		want        bool
	}{
		{"Kimi-K3", "local_v2", "Kimi-K3", schemas.ResponsesRequest, schemas.ChatCompletionRequest, nil, true},
		{"Kimi-K3 on local", "local", "Kimi-K3", schemas.ResponsesRequest, schemas.ChatCompletionRequest, nil, true},
		{"Kimi-K3 streaming", "local_v2", "Kimi-K3", schemas.ResponsesStreamRequest, schemas.ChatCompletionStreamRequest, nil, true},
		{"other local_v2 model", "local_v2", "GLM-5.3", schemas.ResponsesRequest, schemas.ChatCompletionRequest, nil, false},
		{"other local model", "local", "GLM-5.3", schemas.ResponsesRequest, schemas.ChatCompletionRequest, nil, false},
		{"Kimi-K3 on another provider", "other", "Kimi-K3", schemas.ResponsesRequest, schemas.ChatCompletionRequest, nil, false},
		{"Kimi-K3 with chat disabled", "local_v2", "Kimi-K3", schemas.ResponsesRequest, schemas.ChatCompletionRequest, &schemas.AllowedRequests{Responses: true}, false},
		{"configured fallback", "other", "other-model", schemas.ResponsesRequest, schemas.ChatCompletionRequest, &schemas.AllowedRequests{ChatCompletion: true}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &OpenAIProvider{customProviderConfig: &schemas.CustomProviderConfig{
				CustomProviderKey: tt.providerKey,
				AllowedRequests:   tt.allowed,
			}}
			if got := provider.shouldFallbackResponsesToChat(tt.responsesOp, tt.chatOp, tt.model); got != tt.want {
				t.Fatalf("shouldFallbackResponsesToChat() = %v, want %v", got, tt.want)
			}
		})
	}
}
