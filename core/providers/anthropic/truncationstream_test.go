package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestToAnthropicResponsesStreamResponse_IncompleteEmitsTerminalEvents(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	response := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeIncomplete,
		Response: &schemas.BifrostResponsesResponse{
			StopReason: schemas.Ptr(string(schemas.BifrostFinishReasonLength)),
			Status:     schemas.Ptr(schemas.ResponsesResponseStatusIncomplete),
			IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{
				Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens,
			},
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  10,
				OutputTokens: 8,
				TotalTokens:  18,
			},
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, response)
	if len(events) != 2 {
		t.Fatalf("expected message_delta and message_stop, got %d events", len(events))
	}
	if events[0].Type != AnthropicStreamEventTypeMessageDelta {
		t.Fatalf("event[0] type = %q, want %q", events[0].Type, AnthropicStreamEventTypeMessageDelta)
	}
	if events[0].Delta == nil || events[0].Delta.StopReason == nil {
		t.Fatal("message_delta is missing stop_reason")
	}
	if got := *events[0].Delta.StopReason; got != AnthropicStopReasonMaxTokens {
		t.Fatalf("message_delta stop_reason = %q, want %q", got, AnthropicStopReasonMaxTokens)
	}
	if events[0].Usage == nil || events[0].Usage.OutputTokens != 8 {
		t.Fatalf("message_delta usage = %+v, want output_tokens=8", events[0].Usage)
	}
	if events[1].Type != AnthropicStreamEventTypeMessageStop {
		t.Fatalf("event[1] type = %q, want %q", events[1].Type, AnthropicStreamEventTypeMessageStop)
	}
}
