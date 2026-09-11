package handlers

import (
	"github.com/maximhq/bifrost/core/schemas"
	"testing"
)

func TestWebSocketHarnessSessionIdentity(t *testing.T) {
	for _, header := range []string{"session-id", "session_id", "x-claude-code-session-id", "x-bf-session-id"} {
		t.Run(header, func(t *testing.T) {
			ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{headers: map[string][]string{header: {"conversation-1"}}})
			defer cancel()
			if got := ctx.Value(schemas.BifrostContextKeySessionID); got != "conversation-1" {
				t.Fatalf("session identity = %v, want conversation-1", got)
			}
		})
	}
}
