package modelcapabilityvalidator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

func testPlugin(t *testing.T, inputModalities []string) *Plugin {
	t.Helper()
	pricing := map[string]any{
		"glm-5.2": map[string]any{
			"provider": "zhipu",
			"mode":     "chat",
			"architecture": map[string]any{
				"input_modalities": inputModalities,
			},
		},
	}
	data, err := json.Marshal(pricing)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + path})
	if err := store.LoadFromURLIntoMemory(t.Context()); err != nil {
		t.Fatal(err)
	}
	return &Plugin{catalog: modelcatalog.NewTestCatalogWithDatasheet(store)}
}

func TestPreLLMHookRejectsUnsupportedChatImage(t *testing.T) {
	plugin := testPlugin(t, []string{"text"})
	text := "describe this image"
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: "zhipu",
			Model:    "glm-5.2",
			Input: []schemas.ChatMessage{{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: &text},
					{Type: schemas.ChatContentBlockTypeImage},
				}},
			}},
		},
	}

	_, shortCircuit, err := plugin.PreLLMHook(nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if shortCircuit == nil || shortCircuit.Error == nil {
		t.Fatal("expected unsupported image input to be rejected")
	}
	if shortCircuit.Error.StatusCode == nil || *shortCircuit.Error.StatusCode != 400 {
		t.Fatalf("expected status 400, got %#v", shortCircuit.Error.StatusCode)
	}
	if shortCircuit.Error.Type != nil {
		t.Fatalf("request validation must not use the governance error type, got %q", *shortCircuit.Error.Type)
	}
	if shortCircuit.Error.Error == nil || shortCircuit.Error.Error.Type == nil || *shortCircuit.Error.Error.Type != "invalid_request_error" {
		t.Fatalf("unexpected error: %#v", shortCircuit.Error.Error)
	}
	if shortCircuit.Error.Error.Code == nil || *shortCircuit.Error.Error.Code != "unsupported_input_modality" {
		t.Fatalf("unexpected error: %#v", shortCircuit.Error.Error)
	}
	if shortCircuit.Error.Error.Param != "messages" {
		t.Fatalf("expected messages parameter, got %#v", shortCircuit.Error.Error.Param)
	}
	if shortCircuit.Error.AllowFallbacks != nil {
		t.Fatal("capability validation must preserve Bifrost's default fallback behavior")
	}
}

func TestPreLLMHookRejectsUnsupportedResponsesImage(t *testing.T) {
	plugin := testPlugin(t, []string{"text"})
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: "zhipu",
			Model:    "glm-5.2",
			Input: []schemas.ResponsesMessage{{
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeImage,
				}}},
			}},
		},
	}

	_, shortCircuit, err := plugin.PreLLMHook(nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if shortCircuit == nil || shortCircuit.Error == nil {
		t.Fatal("expected unsupported image input to be rejected")
	}
	if shortCircuit.Error.Error == nil {
		t.Fatal("expected a structured error")
	}
	if shortCircuit.Error.Error.Param != "input" {
		t.Fatalf("expected input parameter, got %#v", shortCircuit.Error.Error.Param)
	}
}

func TestPreLLMHookAllowsSupportedUnknownAndTextOnlyInputs(t *testing.T) {
	imageRequest := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: "zhipu",
			Model:    "glm-5.2",
			Input: []schemas.ChatMessage{{
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
					Type: schemas.ChatContentBlockTypeImage,
				}}},
			}},
		},
	}
	textRequest := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: "zhipu",
			Model:    "glm-5.2",
			Input: []schemas.ChatMessage{{
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
					Type: schemas.ChatContentBlockTypeText,
				}}},
			}},
		},
	}

	tests := []struct {
		name   string
		plugin *Plugin
		req    *schemas.BifrostRequest
	}{
		{name: "supported image", plugin: testPlugin(t, []string{"text", "image"}), req: imageRequest},
		{name: "unknown capabilities", plugin: &Plugin{catalog: modelcatalog.NewTestCatalog(nil)}, req: imageRequest},
		{name: "unspecified modalities", plugin: testPlugin(t, nil), req: imageRequest},
		{name: "text-only input", plugin: testPlugin(t, []string{"text"}), req: textRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, shortCircuit, err := tt.plugin.PreLLMHook(nil, tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if shortCircuit != nil {
				t.Fatalf("expected request to pass, got %#v", shortCircuit.Error)
			}
		})
	}
}
