package compat

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestNamespaceCompactAliasCollidesWithOrdinaryTool(t *testing.T) {
	id := namespaceToolID{namespace: "mcp__bifrost", name: "probe_get_marker"}
	alias := compactNamespaceToolName(id)
	req := &schemas.BifrostResponsesRequest{Provider: schemas.OpenRouter, Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(id.namespace + "__" + id.name)},
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(alias)},
		{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr(id.namespace), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(id.name)}}}},
	}}}
	if _, err := flattenNamespaceTools(req); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range req.Params.Tools {
		if seen[*tool.Name] {
			t.Fatalf("duplicate provider function name: %s", *tool.Name)
		}
		seen[*tool.Name] = true
	}
}

func TestNamespaceOrdinaryToolChoiceSurvivesCollision(t *testing.T) {
	ns, name := "mcp__bifrost", "probe_get_marker"
	ordinary := ns + "__" + name
	req := &schemas.BifrostResponsesRequest{Provider: schemas.OpenRouter, Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(ordinary)},
		{Type: schemas.ResponsesToolTypeNamespace, Name: &ns, ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: &name}}}},
	}, ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr(ordinary)}}}}
	if _, err := flattenNamespaceTools(req); err != nil {
		t.Fatal(err)
	}
	if *req.Params.ToolChoice.ResponsesToolChoiceStruct.Name != ordinary {
		t.Fatalf("choice for ordinary function %s redirected to namespace alias %s", ordinary, *req.Params.ToolChoice.ResponsesToolChoiceStruct.Name)
	}
}

func TestNamespaceExplicitChoiceAndAllowedTools(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.OpenRouter} {
		t.Run(string(provider), func(t *testing.T) {
			var req schemas.BifrostResponsesRequest
			if err := schemas.Unmarshal([]byte(`{"params":{"tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"child"}]}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"function","namespace":"ns","name":"child"}]}}}`), &req); err != nil {
				t.Fatal(err)
			}
			req.Provider = provider
			if _, err := flattenNamespaceTools(&req); err != nil {
				t.Fatal(err)
			}
			choice := req.Params.ToolChoice.ResponsesToolChoiceStruct.Tools[0]
			if provider == schemas.OpenAI {
				if choice.Namespace == nil || *choice.Namespace != "ns" || *choice.Name != "child" {
					t.Fatalf("native choice changed: %#v", choice)
				}
			} else if choice.Namespace != nil || *choice.Name != "ns__child" {
				t.Fatalf("choice not flattened: %#v", choice)
			}
		})
	}
}

func TestNamespaceUnknownChoiceRejected(t *testing.T) {
	for _, withTools := range []bool{false, true} {
		req := &schemas.BifrostResponsesRequest{Provider: schemas.OpenRouter, Params: &schemas.ResponsesParameters{ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeFunction, Namespace: schemas.Ptr("missing"), Name: schemas.Ptr("child")}}}}
		if withTools {
			req.Params.Tools = []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("ns"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("child")}}}}}
		}
		if _, err := flattenNamespaceTools(req); err == nil {
			t.Fatalf("undeclared namespace accepted (with tools: %v)", withTools)
		}
	}
}

func TestNamespaceInvalidChoiceShortCircuits(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(t.Context())
	defer cancel()
	plugin := &CompatPlugin{config: Config{ShouldConvertParams: true}}
	req := &schemas.BifrostRequest{ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenRouter, Params: &schemas.ResponsesParameters{ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeFunction, Namespace: schemas.Ptr("missing"), Name: schemas.Ptr("child")}}}}}
	_, stop, err := plugin.PreLLMHook(ctx, req)
	if err != nil || stop == nil || stop.Error == nil || *stop.Error.StatusCode != 400 || *stop.Error.AllowFallbacks {
		t.Fatalf("invalid choice must block upstream: stop=%#v err=%v", stop, err)
	}
}
