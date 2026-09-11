package rules

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/routingstrategy"
	"github.com/maximhq/bifrost/core/schemas"
	tables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/stretchr/testify/require"
)

func TestWeightedRulesSessionAndOutage(t *testing.T) {
	store, err := newTestRuleStore()
	require.NoError(t, err)
	engine, err := NewEngine(store, NewMockGovernanceStore(), NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)
	manager, err := routingstrategy.New(schemas.RoutingResilienceConfig{SessionStickiness: true, OutageDetection: true, FailureThreshold: 1}, resilienceKV(t))
	require.NoError(t, err)
	rule := &tables.TableRoutingRule{ID: "sticky", Name: "sticky", Enabled: schemas.Ptr(true), Scope: "global", CelExpression: "model == 'alias'", Targets: []tables.TableRoutingTarget{
		{Provider: schemas.Ptr("openai"), Model: schemas.Ptr("gpt"), Weight: .5},
		{Provider: schemas.Ptr("groq"), Model: schemas.Ptr("glm"), Weight: .5},
	}}
	require.NoError(t, store.UpsertRule(context.Background(), rule))
	selectTarget := func(session string) *Decision {
		c := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
		c.SetValue(schemas.BifrostContextKeySessionID, session)
		manager.Attach(c, &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Model: "alias"}})
		decision, err := engine.EvaluateRoutingRules(c, &EvaluationContext{Model: "alias", RequestType: string(schemas.ChatCompletionRequest)})
		require.NoError(t, err)
		require.NotNil(t, decision)
		return decision
	}
	first := selectTarget("s")
	for range 40 {
		require.Equal(t, first.Provider, selectTarget("s").Provider)
	}
	require.Len(t, first.Fallbacks, 1, "weighted sibling should be a fallback")
	route := routingstrategy.Route{Provider: schemas.ModelProvider(first.Provider), Model: first.Model, API: schemas.ChatCompletionRequest}
	epoch, _ := manager.Begin(route)
	manager.Observe(route, epoch, &schemas.BifrostError{StatusCode: schemas.Ptr(503)})
	require.NotEqual(t, first.Provider, selectTarget("s").Provider)
	require.NotEqual(t, first.Provider, selectTarget("new").Provider)
	// A changed matching rule must be evaluated again, even for an existing session.
	rule.Targets = []tables.TableRoutingTarget{{Provider: schemas.Ptr("anthropic"), Model: schemas.Ptr("claude"), Weight: 1}}
	require.NoError(t, store.UpsertRule(context.Background(), rule))
	require.Equal(t, "anthropic", selectTarget("s").Provider)
}

func resilienceKV(t *testing.T) *kvstore.Store {
	t.Helper()
	store, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// selectWeightedTarget picks one target from the slice using weighted random selection.
// Each target's Weight contributes proportionally to its probability of being chosen.
// Weights do not need to be normalised to 100; the function normalises internally.
// Returns ok=false only when len(targets)==0 or all targets have negative weights (filtered out).
// When all valid targets have weight==0 the function falls back to uniform random selection
// and still returns ok=true, so zero-weight targets are valid and handled.
func selectWeightedTarget(targets []tables.TableRoutingTarget) (tables.TableRoutingTarget, bool) {
	selected, ok := routingstrategy.Select(nil, "", routingTargets(targets, "", ""))
	if !ok {
		return tables.TableRoutingTarget{}, false
	}
	// Preserve optional fields for callers of this low-level helper.
	for _, t := range targets {
		normalized := routingTargets([]tables.TableRoutingTarget{t}, "", "")
		if len(normalized) > 0 && normalized[0] == selected {
			return t, true
		}
	}
	return tables.TableRoutingTarget{}, false
}
