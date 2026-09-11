package governance

import (
	"testing"

	"github.com/maximhq/bifrost/core/routingstrategy"
	"github.com/maximhq/bifrost/core/schemas"
	tables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/stretchr/testify/require"
)

func TestLoadBalanceProviderSessionResilience(t *testing.T) {
	a, b := buildProviderConfig("openai", []string{"*"}), buildProviderConfig("groq", []string{"*"})
	a.Weight, b.Weight = schemas.Ptr(.5), schemas.Ptr(.5)
	vk := buildVirtualKeyWithProviders("vk-sticky", "sk-bf-sticky", "sticky", []tables.TableVirtualKeyProviderConfig{a, b})
	p := newLoadBalanceTestPlugin(t, vk)
	m, err := routingstrategy.New(schemas.RoutingResilienceConfig{SessionStickiness: true, OutageDetection: true, FailureThreshold: 1}, resilienceKV(t))
	require.NoError(t, err)
	choose := func(id string) schemas.ModelProvider {
		ctx := presentCtx("sk-bf-sticky")
		ctx.SetValue(schemas.BifrostContextKeySessionID, id)
		req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Model: "shared"}}
		m.Attach(ctx, req)
		require.NoError(t, p.LoadBalanceProvider(ctx, req))
		return req.ChatRequest.Provider
	}
	first := choose("s")
	require.NotEmpty(t, first)
	for range 40 {
		require.Equal(t, first, choose("s"))
	}
	r := routingstrategy.Route{Provider: first, Model: "shared", API: schemas.ChatCompletionRequest}
	e, _ := m.Begin(r)
	m.Observe(r, e, &schemas.BifrostError{StatusCode: schemas.Ptr(503)})
	require.NotEqual(t, first, choose("s"))
	require.NotEqual(t, first, choose("another-session"))
}

func TestLoadBalanceProviderRetainsExplicitCrossModelFallback(t *testing.T) {
	a, b := buildProviderConfig("openai", []string{"gpt"}), buildProviderConfig("groq", []string{"glm"})
	a.Weight = schemas.Ptr(1.0)
	vk := buildVirtualKeyWithProviders("vk-cross", "sk-bf-cross", "cross", []tables.TableVirtualKeyProviderConfig{a, b})
	p := newLoadBalanceTestPlugin(t, vk)
	m, err := routingstrategy.New(schemas.RoutingResilienceConfig{SessionStickiness: true}, resilienceKV(t))
	require.NoError(t, err)
	request := func() (*schemas.BifrostContext, *schemas.BifrostRequest) {
		ctx := presentCtx("sk-bf-cross")
		ctx.SetValue(schemas.BifrostContextKeySessionID, "session")
		req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Model: "gpt", Fallbacks: []schemas.Fallback{{Provider: schemas.Groq, Model: "glm"}}}}
		m.Attach(ctx, req)
		require.NoError(t, p.LoadBalanceProvider(ctx, req))
		return ctx, req
	}
	ctx, req := request()
	require.Equal(t, schemas.OpenAI, req.ChatRequest.Provider)
	m.BindSuccess(ctx, routingstrategy.Route{Provider: schemas.Groq, Model: "glm", API: schemas.ChatCompletionRequest})
	_, req = request()
	require.Equal(t, schemas.Groq, req.ChatRequest.Provider)
	require.Equal(t, "glm", req.ChatRequest.Model)
	require.Contains(t, req.ChatRequest.Fallbacks, schemas.Fallback{Provider: schemas.OpenAI, Model: "gpt"})
}

func resilienceKV(t *testing.T) *kvstore.Store {
	t.Helper()
	store, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}
