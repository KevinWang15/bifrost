package sgl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

type captureSGLLogger struct {
	mu   sync.Mutex
	logs []string
}

func (logger *captureSGLLogger) Debug(message string, args ...any) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.logs = append(logger.logs, fmt.Sprintf(message, args...))
}
func (*captureSGLLogger) Info(string, ...any)                    {}
func (*captureSGLLogger) Warn(string, ...any)                    {}
func (*captureSGLLogger) Error(string, ...any)                   {}
func (*captureSGLLogger) Fatal(string, ...any)                   {}
func (*captureSGLLogger) SetLevel(schemas.LogLevel)              {}
func (*captureSGLLogger) SetOutputType(schemas.LoggerOutputType) {}
func (*captureSGLLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func (logger *captureSGLLogger) joined() string {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	return strings.Join(logger.logs, "\n")
}

func testSGLResponsesRequest() *schemas.BifrostResponsesRequest {
	weatherToolName := "lookup_weather"
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.SGL,
		Model:    "glm-test",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("What is the weather?"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeFunction,
					Name: &weatherToolName,
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Parameters: &schemas.ToolFunctionParameters{
							Type: "object",
							Properties: schemas.NewOrderedMapFromPairs(
								schemas.KV("city", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
							),
							Required: []string{"city"},
						},
					},
				},
			},
			ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("auto")},
			Text: &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_schema",
					Name: schemas.Ptr("weather_answer"),
					JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
						Type: schemas.Ptr("object"),
						Properties: schemas.NewOrderedMapFromPairs(
							schemas.KV("summary", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
						),
						Required: []string{"summary"},
					},
				},
			},
		},
	}
}

func TestToCompatibleResponsesChatRequestCombinesToolsAndStructuredOutput(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := testSGLResponsesRequest()
	provider := newTestSGLProvider()

	chatRequest, bifrostErr := provider.toCompatibleResponsesChatRequest(ctx, request)
	if bifrostErr != nil {
		t.Fatalf("unexpected compatibility error: %v", bifrostErr)
	}
	if chatRequest.Params.ResponseFormat != nil {
		t.Fatal("response_format must be removed when its schema is represented by the synthetic tool")
	}
	if len(chatRequest.Params.Tools) != 2 {
		t.Fatalf("expected the user tool and synthetic tool, got %d tools", len(chatRequest.Params.Tools))
	}

	syntheticTool := chatRequest.Params.Tools[1]
	if syntheticTool.Function == nil || syntheticTool.Function.Name != sglStructuredOutputToolBaseName {
		t.Fatalf("expected synthetic tool %q, got %+v", sglStructuredOutputToolBaseName, syntheticTool.Function)
	}
	if syntheticTool.Function.Parameters == nil || syntheticTool.Function.Parameters.Type != "object" {
		t.Fatalf("expected the response schema on the synthetic tool, got %+v", syntheticTool.Function.Parameters)
	}
	if syntheticTool.Function.Parameters.Properties == nil {
		t.Fatal("expected structured output properties on the synthetic tool")
	}
	if _, ok := syntheticTool.Function.Parameters.Properties.Get("summary"); !ok {
		t.Fatal("expected summary property on the synthetic tool schema")
	}

	if chatRequest.Params.ToolChoice == nil || chatRequest.Params.ToolChoice.ChatToolChoiceStr == nil || *chatRequest.Params.ToolChoice.ChatToolChoiceStr != "required" {
		t.Fatalf("expected required tool choice so the model chooses a user tool or the final-response tool, got %+v", chatRequest.Params.ToolChoice)
	}
	if got, _ := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); got != sglStructuredOutputToolBaseName {
		t.Fatalf("expected structured output tool name in context, got %q", got)
	}
	if len(request.Params.Tools) != 1 || request.Params.Text.Format == nil {
		t.Fatal("compatibility conversion mutated the caller's Responses request")
	}
}

func TestToCompatibleResponsesChatRequestLeavesResponseFormatWithoutUserTools(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := testSGLResponsesRequest()
	request.Params.Tools = nil
	provider := newTestSGLProvider()

	chatRequest, bifrostErr := provider.toCompatibleResponsesChatRequest(ctx, request)
	if bifrostErr != nil {
		t.Fatalf("unexpected compatibility error: %v", bifrostErr)
	}
	if chatRequest.Params.ResponseFormat == nil {
		t.Fatal("response_format-only requests should continue using SGLang's native constraint")
	}
	if len(chatRequest.Params.Tools) != 0 {
		t.Fatalf("did not expect a synthetic tool without user tools, got %d", len(chatRequest.Params.Tools))
	}
	if got := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName); got != nil {
		t.Fatalf("did not expect structured output tool context, got %v", got)
	}
}

func TestToCompatibleResponsesChatRequestSupportsJSONObject(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := testSGLResponsesRequest()
	request.Params.Text.Format.Type = "json_object"
	request.Params.Text.Format.JSONSchema = nil
	provider := newTestSGLProvider()

	chatRequest, bifrostErr := provider.toCompatibleResponsesChatRequest(ctx, request)
	if bifrostErr != nil {
		t.Fatalf("unexpected compatibility error: %v", bifrostErr)
	}
	if chatRequest.Params.ResponseFormat != nil {
		t.Fatal("json_object response_format must be represented by the synthetic tool")
	}
	if len(chatRequest.Params.Tools) != 2 || chatRequest.Params.Tools[1].Function == nil || chatRequest.Params.Tools[1].Function.Parameters == nil {
		t.Fatalf("expected a synthetic JSON-object tool, got %+v", chatRequest.Params.Tools)
	}
	if chatRequest.Params.Tools[1].Function.Parameters.Type != "object" {
		t.Fatalf("expected an object schema, got %+v", chatRequest.Params.Tools[1].Function.Parameters)
	}
}

func TestToCompatibleResponsesChatRequestTransformsEveryAutomaticChoiceShape(t *testing.T) {
	auto := "auto"
	tests := []struct {
		name   string
		choice *schemas.ResponsesToolChoice
	}{
		{name: "default"},
		{name: "string auto", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: &auto}},
		{name: "structured auto", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeAuto}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			request := testSGLResponsesRequest()
			request.Params.ToolChoice = test.choice
			provider := newTestSGLProvider()

			chatRequest, bifrostErr := provider.toCompatibleResponsesChatRequest(ctx, request)
			if bifrostErr != nil {
				t.Fatalf("unexpected compatibility error: %v", bifrostErr)
			}
			if chatRequest.Params.ResponseFormat != nil || len(chatRequest.Params.Tools) != 2 {
				t.Fatalf("automatic choice was not transformed: response_format=%v tools=%d", chatRequest.Params.ResponseFormat, len(chatRequest.Params.Tools))
			}
		})
	}
}

func TestSGLStructuredOutputCompatibilityAppliesOnlyToAutomaticChoice(t *testing.T) {
	auto := "auto"
	none := "none"
	required := "required"
	any := "any"
	name := "lookup_weather"
	tests := []struct {
		name   string
		choice *schemas.ResponsesToolChoice
		want   bool
	}{
		{name: "default", want: true},
		{name: "empty", choice: &schemas.ResponsesToolChoice{}, want: true},
		{name: "auto string", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: &auto}, want: true},
		{name: "auto struct type", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeAuto}}, want: true},
		{name: "auto struct mode without type", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Mode: &auto}}, want: true},
		{name: "none", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: &none}},
		{name: "required", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: &required}},
		{name: "any", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: &any}},
		{name: "named", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeFunction, Name: &name}}},
		{name: "allowed tools auto", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeAllowedTools, Mode: &auto, Tools: []schemas.ResponsesToolChoiceAllowedToolDef{{Type: string(schemas.ResponsesToolTypeFunction), Name: &name}}}}},
		{name: "contradictory required auto", choice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeRequired, Mode: &auto}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sglStructuredOutputCompatibilityApplies(test.choice); got != test.want {
				t.Fatalf("expected activation=%t, got %t", test.want, got)
			}
		})
	}
}

func TestToCompatibleResponsesChatRequestDoesNotTransformNonAutomaticChoices(t *testing.T) {
	choices := []string{"none", "required", "any"}
	for _, choice := range choices {
		t.Run(choice, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			request := testSGLResponsesRequest()
			request.Params.ToolChoice = &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: &choice}
			provider := newTestSGLProvider()
			logger := &captureSGLLogger{}
			provider.logger = logger

			chatRequest, bifrostErr := provider.toCompatibleResponsesChatRequest(ctx, request)
			if bifrostErr != nil {
				t.Fatalf("unexpected compatibility error: %v", bifrostErr)
			}
			if chatRequest.Params.ResponseFormat == nil || len(chatRequest.Params.Tools) != 1 {
				t.Fatalf("non-automatic choice was transformed: response_format=%v tools=%d", chatRequest.Params.ResponseFormat, len(chatRequest.Params.Tools))
			}
			if got := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName); got != nil {
				t.Fatalf("unexpected structured-output context: %v", got)
			}
			if logs := logger.joined(); logs != "" {
				t.Fatalf("non-automatic choice emitted compatibility logs: %q", logs)
			}
		})
	}
}

func TestToCompatibleResponsesChatRequestDoesNotTransformInapplicableRequests(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*schemas.BifrostResponsesRequest)
	}{
		{name: "no tools", mutate: func(request *schemas.BifrostResponsesRequest) { request.Params.Tools = nil }},
		{name: "unusable non-function tool", mutate: func(request *schemas.BifrostResponsesRequest) {
			request.Params.Tools = []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}}
		}},
		{name: "plain text", mutate: func(request *schemas.BifrostResponsesRequest) {
			request.Params.Text.Format.Type = "text"
			request.Params.Text.Format.JSONSchema = nil
		}},
		{name: "json schema without schema", mutate: func(request *schemas.BifrostResponsesRequest) {
			request.Params.Text.Format.JSONSchema = nil
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			request := testSGLResponsesRequest()
			test.mutate(request)
			provider := newTestSGLProvider()
			logger := &captureSGLLogger{}
			provider.logger = logger

			chatRequest, bifrostErr := provider.toCompatibleResponsesChatRequest(ctx, request)
			if bifrostErr != nil {
				t.Fatalf("unexpected compatibility error: %v", bifrostErr)
			}
			if got := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName); got != nil {
				t.Fatalf("inapplicable request activated compatibility: %v", got)
			}
			if len(chatRequest.Params.Tools) > 1 {
				t.Fatalf("inapplicable request gained an internal tool: %+v", chatRequest.Params.Tools)
			}
			if logs := logger.joined(); logs != "" {
				t.Fatalf("inapplicable request emitted compatibility logs: %q", logs)
			}
		})
	}
}

func TestToCompatibleResponsesChatRequestUsesCollisionSafeInternalName(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := testSGLResponsesRequest()
	request.Params.Tools[0].Name = schemas.Ptr(sglStructuredOutputToolBaseName)
	provider := newTestSGLProvider()

	chatRequest, bifrostErr := provider.toCompatibleResponsesChatRequest(ctx, request)
	if bifrostErr != nil {
		t.Fatalf("unexpected compatibility error: %v", bifrostErr)
	}
	if got := chatRequest.Params.Tools[1].Function.Name; got != sglStructuredOutputToolBaseName+"_2" {
		t.Fatalf("expected collision-safe internal name, got %q", got)
	}
}

func TestRestoreSGLStructuredOutputResponsePreservesUserToolCalls(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, sglStructuredOutputToolBaseName)
	finishReason := string(schemas.BifrostFinishReasonToolCalls)
	structuredName := sglStructuredOutputToolBaseName
	weatherName := "lookup_weather"
	response := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: &finishReason,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &structuredName, Arguments: `{"summary":"sunny"}`}},
								{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &weatherName, Arguments: `{"city":"Shanghai"}`}},
							},
						},
					},
				},
			},
		},
	}

	provider := newTestSGLProvider()
	logger := &captureSGLLogger{}
	provider.logger = logger
	provider.restoreSGLStructuredOutputResponse(ctx, response)

	message := response.Choices[0].ChatNonStreamResponseChoice.Message
	if message.Content == nil || message.Content.ContentStr == nil || *message.Content.ContentStr != `{"summary":"sunny"}` {
		t.Fatalf("expected synthetic tool arguments as output text, got %+v", message.Content)
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name == nil || *message.ToolCalls[0].Function.Name != weatherName {
		t.Fatalf("expected the user tool call to survive, got %+v", message.ToolCalls)
	}
	if response.Choices[0].FinishReason == nil || *response.Choices[0].FinishReason != string(schemas.BifrostFinishReasonToolCalls) {
		t.Fatalf("expected tool_calls finish reason while a user tool remains, got %v", response.Choices[0].FinishReason)
	}
}

func TestRestoreSGLStructuredOutputResponseMapsSyntheticOnlyFinishReason(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, sglStructuredOutputToolBaseName)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "request-unary")
	finishReason := string(schemas.BifrostFinishReasonToolCalls)
	structuredName := sglStructuredOutputToolBaseName
	response := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: &finishReason,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &structuredName, Arguments: `{"summary":"sunny"}`}},
							},
						},
					},
				},
			},
		},
	}

	provider := newTestSGLProvider()
	logger := &captureSGLLogger{}
	provider.logger = logger
	provider.restoreSGLStructuredOutputResponse(ctx, response)

	message := response.Choices[0].ChatNonStreamResponseChoice.Message
	if len(message.ToolCalls) != 0 {
		t.Fatalf("synthetic tool call leaked to the caller: %+v", message.ToolCalls)
	}
	if response.Choices[0].FinishReason == nil || *response.Choices[0].FinishReason != string(schemas.BifrostFinishReasonStop) {
		t.Fatalf("expected stop finish reason, got %v", response.Choices[0].FinishReason)
	}
	logs := logger.joined()
	if !strings.Contains(logs, `request_id="request-unary"`) || !strings.Contains(logs, "stream=false") || !strings.Contains(logs, "restored_call_count=1") {
		t.Fatalf("expected unary restoration log, got %q", logs)
	}
	for _, sensitive := range []string{"summary", "sunny"} {
		if strings.Contains(logs, sensitive) {
			t.Fatalf("logs contain response content %q: %q", sensitive, logs)
		}
	}
}

func TestResponsesStreamConvertsSyntheticToolArgumentsToOutputText(t *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &capturedBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"glm-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
			`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"glm-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-final","type":"function","function":{"name":"bifrost_structured_output","arguments":"{\"summary\":"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"glm-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"sunny\"}"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"glm-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	provider := newTestSGLProvider()
	logger := &captureSGLLogger{}
	provider.logger = logger
	key := schemas.Key{
		ID:    "test-key",
		Value: schemas.SecretVar{Val: "test-api-key"},
		SGLKeyConfig: &schemas.SGLKeyConfig{
			URL: schemas.SecretVar{Val: server.URL},
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "request-stream")

	stream, bifrostErr := provider.ResponsesStream(ctx, noopPostHookRunner, nil, key, testSGLResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error: %v", bifrostErr)
	}

	var outputText string
	var sawFunctionCallEvent bool
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %v", chunk.BifrostError)
		}
		response := chunk.BifrostResponsesStreamResponse
		if response == nil {
			continue
		}
		switch response.Type {
		case schemas.ResponsesStreamResponseTypeOutputTextDelta:
			if response.Delta != nil {
				outputText += *response.Delta
			}
		case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone:
			sawFunctionCallEvent = true
		case schemas.ResponsesStreamResponseTypeOutputItemAdded, schemas.ResponsesStreamResponseTypeOutputItemDone:
			if response.Item != nil && response.Item.Type != nil && *response.Item.Type == schemas.ResponsesMessageTypeFunctionCall {
				sawFunctionCallEvent = true
			}
		}
	}

	if outputText != `{"summary":"sunny"}` {
		t.Fatalf("expected synthetic tool arguments as output text, got %q", outputText)
	}
	if sawFunctionCallEvent {
		t.Fatal("synthetic structured-output tool leaked as a function-call event")
	}
	if capturedBody["response_format"] != nil {
		t.Fatalf("response_format leaked into SGLang request: %+v", capturedBody["response_format"])
	}
	if capturedBody["tool_choice"] != "required" {
		t.Fatalf("expected required tool_choice, got %+v", capturedBody["tool_choice"])
	}
	tools, ok := capturedBody["tools"].([]interface{})
	if !ok || len(tools) != 2 {
		t.Fatalf("expected user and synthetic tools downstream, got %+v", capturedBody["tools"])
	}
	logs := logger.joined()
	for _, expected := range []string{"activated Responses structured-output tool compatibility", `request_id="request-stream"`, "stream=true", "removed_response_format=true"} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("expected log marker %q, got %q", expected, logs)
		}
	}
	for _, sensitive := range []string{"What is the weather?", "summary", "sunny"} {
		if strings.Contains(logs, sensitive) {
			t.Fatalf("logs contain request or response content %q: %q", sensitive, logs)
		}
	}
}

func TestResponsesStreamPreservesUserToolCallsAlongsideStructuredOutput(t *testing.T) {
	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)
	state.ConfigureStructuredOutputToolCompatibility(sglStructuredOutputToolBaseName, "request-mixed-stream", &captureSGLLogger{})

	internalName := sglStructuredOutputToolBaseName
	internalID := "call-final"
	userToolName := "lookup_weather"
	userToolID := "call-weather"
	finishReason := string(schemas.BifrostFinishReasonToolCalls)
	chunks := []*schemas.BifrostChatResponse{
		{
			ID: "chatcmpl-mixed", Model: "glm-test",
			Choices: []schemas.BifrostResponseChoice{{ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{
					{Index: 0, ID: &internalID, Function: schemas.ChatAssistantMessageToolCallFunction{Name: &internalName, Arguments: `{"summary":`}},
					{Index: 1, ID: &userToolID, Function: schemas.ChatAssistantMessageToolCallFunction{Name: &userToolName, Arguments: `{"city":`}},
				},
			}}}},
		},
		{
			ID: "chatcmpl-mixed", Model: "glm-test",
			Choices: []schemas.BifrostResponseChoice{{ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{
					{Index: 0, Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `"sunny"}`}},
					{Index: 1, Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `"Shanghai"}`}},
				},
			}}}},
		},
		{
			ID: "chatcmpl-mixed", Model: "glm-test",
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason:             &finishReason,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{}},
			}},
		},
	}

	var outputText string
	var sawUserTool bool
	var completed *schemas.BifrostResponsesStreamResponse
	for _, chunk := range chunks {
		for _, event := range chunk.ToBifrostResponsesStreamResponse(state) {
			encoded, err := schemas.Marshal(event)
			if err != nil {
				t.Fatalf("failed to encode stream event: %v", err)
			}
			if strings.Contains(string(encoded), internalName) {
				t.Fatalf("internal tool leaked into stream event: %s", encoded)
			}
			if event.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && event.Delta != nil {
				outputText += *event.Delta
			}
			if event.Item != nil && event.Item.Type != nil && *event.Item.Type == schemas.ResponsesMessageTypeFunctionCall {
				if event.Item.Name == nil || *event.Item.Name != userToolName {
					t.Fatalf("unexpected client-visible tool: %+v", event.Item.Name)
				}
				sawUserTool = true
			}
			if event.Type == schemas.ResponsesStreamResponseTypeCompleted {
				completed = event
			}
		}
	}

	if outputText != `{"summary":"sunny"}` {
		t.Fatalf("expected structured output text, got %q", outputText)
	}
	if !sawUserTool {
		t.Fatal("genuine user tool call was lost")
	}
	if completed == nil || completed.Response == nil || completed.Response.StopReason == nil || *completed.Response.StopReason != finishReason {
		t.Fatalf("expected tool_calls stop reason while a genuine tool survives, got %+v", completed)
	}
}

func BenchmarkSGLResponsesCompatibility(b *testing.B) {
	provider := newTestSGLProvider()

	b.Run("responses_to_chat_baseline", func(b *testing.B) {
		request := testSGLResponsesRequest()
		b.ReportAllocs()
		for b.Loop() {
			_ = request.ToChatRequest()
		}
	})

	b.Run("not_applicable", func(b *testing.B) {
		request := testSGLResponsesRequest()
		request.Params.ToolChoice = &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("none")}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		b.ReportAllocs()
		for b.Loop() {
			if _, bifrostErr := provider.toCompatibleResponsesChatRequest(ctx, request); bifrostErr != nil {
				b.Fatal(bifrostErr)
			}
		}
	})

	b.Run("activated", func(b *testing.B) {
		request := testSGLResponsesRequest()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		b.ReportAllocs()
		for b.Loop() {
			if _, bifrostErr := provider.toCompatibleResponsesChatRequest(ctx, request); bifrostErr != nil {
				b.Fatal(bifrostErr)
			}
		}
	})
}
