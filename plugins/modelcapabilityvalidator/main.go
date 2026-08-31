// Package modelcapabilityvalidator provides a built-in plugin that validates
// request inputs against explicit model capability metadata before provider calls.
package modelcapabilityvalidator

import (
	"fmt"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const PluginName = "model-capability-validator"

// Plugin validates request inputs against the selected model's catalog capabilities.
type Plugin struct {
	catalog *modelcatalog.ModelCatalog
}

// Init returns a new capability validator. A model catalog is required because it is
// the authoritative source of capability metadata.
func Init(catalog *modelcatalog.ModelCatalog) (*Plugin, error) {
	if catalog == nil {
		return nil, fmt.Errorf("model-capability-validator: catalog is required")
	}
	return &Plugin{catalog: catalog}, nil
}

// GetName implements schemas.BasePlugin.
func (p *Plugin) GetName() string { return PluginName }

// Cleanup implements schemas.BasePlugin.
func (p *Plugin) Cleanup() error { return nil }

// PreRequestHook implements schemas.LLMPlugin (no-op).
func (p *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook rejects image inputs when the selected model explicitly does not support them.
// Missing capability metadata is treated as unknown and allowed through.
func (p *Plugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if req == nil {
		return req, nil, nil
	}
	param := imageInputParam(req)
	if param == "" {
		return req, nil, nil
	}

	provider, model, _ := req.GetRequestFields()
	capabilities := p.catalog.GetModelCapabilityEntryForModel(model, provider)
	if capabilities == nil || capabilities.Architecture == nil || len(capabilities.Architecture.InputModalities) == 0 {
		return req, nil, nil
	}
	if slices.Contains(capabilities.Architecture.InputModalities, "image") {
		return req, nil, nil
	}

	const (
		errorType = "invalid_request_error"
		errorCode = "unsupported_input_modality"
	)
	return req, &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
		StatusCode: schemas.Ptr(400),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr(errorType),
			Code:    schemas.Ptr(errorCode),
			Message: fmt.Sprintf("model %q does not support image input; supported input modalities: [%s]", model, strings.Join(capabilities.Architecture.InputModalities, ", ")),
			Param:   param,
		},
	}}, nil
}

func imageInputParam(req *schemas.BifrostRequest) string {
	if req.ChatRequest != nil {
		for _, message := range req.ChatRequest.Input {
			if message.Content == nil {
				continue
			}
			for _, block := range message.Content.ContentBlocks {
				if block.Type == schemas.ChatContentBlockTypeImage {
					return "messages"
				}
			}
		}
	}

	if req.ResponsesRequest != nil {
		for _, message := range req.ResponsesRequest.Input {
			if message.Content == nil {
				continue
			}
			for _, block := range message.Content.ContentBlocks {
				if block.Type == schemas.ResponsesInputMessageContentBlockTypeImage {
					return "input"
				}
			}
		}
	}

	return ""
}

// PostLLMHook implements schemas.LLMPlugin (no-op).
func (p *Plugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}
