package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// JSON configuration intentionally works against the old schema too: before
// resilience exists it is ignored and the second request hits the dead upstream.
func TestRoutingResilienceRemembersOutage(t *testing.T) {
	var hits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"message":"upstream unavailable"}}`)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"ok","object":"chat.completion","model":"backup","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
	}))
	defer backup.Close()
	account := NewMockAccount()
	for provider, url := range map[schemas.ModelProvider]string{schemas.OpenAI: primary.URL, schemas.Groq: backup.URL} {
		account.AddProviderWithBaseURL(provider, 1, 1, url)
		account.configs[provider].NetworkConfig.MaxRetries = 0
		account.SetKeysForProvider(provider, []schemas.Key{{ID: string(provider), Value: *schemas.NewSecretVar("fake"), Models: schemas.WhiteList{"*"}, Weight: 1}})
	}
	var config schemas.BifrostConfig
	if err := json.Unmarshal([]byte(`{"RoutingResilience":{"outage_detection":true,"failure_threshold":1,"cooldown_seconds":600,"probe_interval_seconds":30}}`), &config); err != nil {
		t.Fatal(err)
	}
	config.Account, config.Logger = account, NewDefaultLogger(schemas.LogLevelError)
	client, err := Init(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Shutdown()
	for range 2 {
		ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(5*time.Second))
		response, failure := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI, Model: "primary",
			Input:     []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
			Fallbacks: []schemas.Fallback{{Provider: schemas.Groq, Model: "backup"}},
		})
		if failure != nil || response == nil {
			t.Fatalf("fallback failed: %v", failure)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("dead upstream received %d requests, want 1", hits.Load())
	}
}
