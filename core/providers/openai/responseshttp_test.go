package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestResponsesSendsExplicitNonStreamingFlag(t *testing.T) {
	bodyCh := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyCh <- body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"test response"}}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 5,
		},
	}, &testLogger{})
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _ = provider.Responses(ctx, schemas.Key{}, &schemas.BifrostResponsesRequest{
		Model: "test-model",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	})

	var payload struct {
		Stream *bool `json:"stream"`
	}
	select {
	case body := <-bodyCh:
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to parse upstream request %s: %v", body, err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for upstream request")
	}
	if payload.Stream == nil || *payload.Stream {
		t.Fatalf("expected explicit stream=false, got %#v", payload.Stream)
	}
}
