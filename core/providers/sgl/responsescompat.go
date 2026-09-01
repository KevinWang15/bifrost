package sgl

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const sglStructuredOutputToolBaseName = "bifrost_structured_output"

// toCompatibleResponsesChatRequest converts a Responses request to SGLang's Chat
// Completions shape. SGLang can constrain either tool calls or response_format,
// but not both at once, so represent the structured final response as one more
// function tool when the request needs both capabilities.
func (provider *SGLProvider) toCompatibleResponsesChatRequest(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) (*schemas.BifrostChatRequest, *schemas.BifrostError) {
	chatRequest := request.ToChatRequest()
	if request == nil || chatRequest.Params == nil || len(chatRequest.Params.Tools) == 0 || request.Params == nil || request.Params.Text == nil || request.Params.Text.Format == nil || !sglStructuredOutputCompatibilityApplies(request.Params.ToolChoice) {
		return chatRequest, nil
	}

	format := request.Params.Text.Format
	var parameters *schemas.ToolFunctionParameters
	var err error
	switch format.Type {
	case "json_schema":
		if format.JSONSchema == nil {
			return chatRequest, nil
		}
		parameters, err = sglStructuredOutputToolParameters(format.JSONSchema)
	case "json_object":
		parameters = &schemas.ToolFunctionParameters{Type: "object"}
	default:
		return chatRequest, nil
	}
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert structured output schema to an SGLang tool", err)
	}

	toolName := uniqueSGLStructuredOutputToolName(chatRequest.Params.Tools)
	const finalResponseInstruction = "Return the final response matching the requested JSON format. Use this only when no further tool calls are needed."
	description := finalResponseInstruction
	if format.Description != nil && strings.TrimSpace(*format.Description) != "" {
		description = strings.TrimSpace(*format.Description) + " " + finalResponseInstruction
	}

	chatRequest.Params.Tools = append(chatRequest.Params.Tools, schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        toolName,
			Description: &description,
			Parameters:  parameters,
			Strict:      format.Strict,
		},
	})
	chatRequest.Params.ResponseFormat = nil
	chatRequest.Params.ToolChoice = &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr(string(schemas.ChatToolChoiceTypeRequired))}
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, toolName)
	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	provider.logger.Debug("[sgl] activated Responses structured-output tool compatibility: request_id=%q format_type=%s user_tool_count=%d internal_tool=%q removed_response_format=true downstream_tool_choice=required", requestID, format.Type, len(chatRequest.Params.Tools)-1, toolName)

	return chatRequest, nil
}

// sglStructuredOutputCompatibilityApplies returns true only when the caller
// allows the model to choose between using a tool and returning a final answer.
// Forced, disabled, named, and restricted tool choices keep their original
// semantics instead of silently broadening the set of callable tools.
func sglStructuredOutputCompatibilityApplies(toolChoice *schemas.ResponsesToolChoice) bool {
	if toolChoice == nil || (toolChoice.ResponsesToolChoiceStr == nil && toolChoice.ResponsesToolChoiceStruct == nil) {
		return true
	}
	if toolChoice.ResponsesToolChoiceStr != nil {
		return *toolChoice.ResponsesToolChoiceStr == string(schemas.ResponsesToolChoiceTypeAuto)
	}

	choice := toolChoice.ResponsesToolChoiceStruct
	if choice == nil || len(choice.Tools) > 0 {
		return false
	}
	return choice.Type == schemas.ResponsesToolChoiceTypeAuto ||
		(choice.Type == "" && choice.Mode != nil && *choice.Mode == string(schemas.ResponsesToolChoiceTypeAuto))
}

func sglStructuredOutputToolParameters(schema *schemas.ResponsesTextConfigFormatJSONSchema) (*schemas.ToolFunctionParameters, error) {
	composite, acceptAll, err := schema.CompositeSchema()
	if err != nil {
		return nil, err
	}

	if acceptAll {
		return &schemas.ToolFunctionParameters{Type: "object"}, nil
	}

	var schemaValue interface{} = schema.ToMap()
	if composite != nil {
		schemaValue = composite
	}
	if schemaValue == nil {
		return nil, fmt.Errorf("json_schema format has no schema")
	}

	data, err := schemas.Marshal(schemaValue)
	if err != nil {
		return nil, err
	}

	var parameters schemas.ToolFunctionParameters
	if err := schemas.Unmarshal(data, &parameters); err != nil {
		return nil, err
	}
	if parameters.Type == "" {
		parameters.Type = "object"
	}

	return &parameters, nil
}

func uniqueSGLStructuredOutputToolName(tools []schemas.ChatTool) string {
	used := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if tool.Function != nil {
			used[tool.Function.Name] = struct{}{}
		}
	}

	if _, exists := used[sglStructuredOutputToolBaseName]; !exists {
		return sglStructuredOutputToolBaseName
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s_%d", sglStructuredOutputToolBaseName, suffix)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

// restoreSGLStructuredOutputResponse hides the internal synthetic tool from the
// Responses caller and exposes its JSON arguments as ordinary output text.
func (provider *SGLProvider) restoreSGLStructuredOutputResponse(ctx *schemas.BifrostContext, response *schemas.BifrostChatResponse) {
	toolName, _ := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string)
	if toolName == "" || response == nil {
		return
	}

	for i := range response.Choices {
		choice := &response.Choices[i]
		if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
			continue
		}
		message := choice.ChatNonStreamResponseChoice.Message
		if message.ChatAssistantMessage == nil || len(message.ToolCalls) == 0 {
			continue
		}

		remaining := make([]schemas.ChatAssistantMessageToolCall, 0, len(message.ToolCalls))
		restoredCalls := 0
		for _, toolCall := range message.ToolCalls {
			if toolCall.Function.Name == nil || *toolCall.Function.Name != toolName {
				remaining = append(remaining, toolCall)
				continue
			}

			appendChatMessageText(message, toolCall.Function.Arguments)
			restoredCalls++
		}
		message.ToolCalls = remaining

		if restoredCalls > 0 && len(remaining) == 0 && choice.FinishReason != nil && *choice.FinishReason == string(schemas.BifrostFinishReasonToolCalls) {
			choice.FinishReason = schemas.Ptr(string(schemas.BifrostFinishReasonStop))
		}
		if restoredCalls > 0 {
			requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
			provider.logger.Debug("[sgl] restored internal structured-output tool call as Responses output text: request_id=%q stream=false internal_tool=%q restored_call_count=%d preserved_user_tool_call_count=%d", requestID, toolName, restoredCalls, len(remaining))
		}
	}
}

func appendChatMessageText(message *schemas.ChatMessage, text string) {
	if message.Content == nil {
		message.Content = &schemas.ChatMessageContent{ContentStr: &text}
		return
	}
	if message.Content.ContentStr != nil {
		combined := *message.Content.ContentStr + text
		message.Content.ContentStr = &combined
		return
	}
	message.Content.ContentBlocks = append(message.Content.ContentBlocks, schemas.ChatContentBlock{
		Type: schemas.ChatContentBlockTypeText,
		Text: &text,
	})
}
