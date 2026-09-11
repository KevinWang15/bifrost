package bifrost

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/routingstrategy"
	"github.com/maximhq/bifrost/core/schemas"
)

// probeRoutingTarget uses a synthetic prompt and operator-configured credentials.
// It deliberately bypasses request routing, retry, fallback and user plugins:
// a successful fallback must never masquerade as recovery of the broken route.
// No user request, history, tool, header, or secret is retained by the manager.
func (bifrost *Bifrost) probeRoutingTarget(parent context.Context, route routingstrategy.Route) bool {
	provider := bifrost.GetProviderByKey(route.Provider)
	if provider == nil {
		return false
	}
	config, err := bifrost.account.GetConfigForProvider(route.Provider)
	if err != nil || config == nil {
		return false
	}
	if config.CustomProviderConfig != nil && config.CustomProviderConfig.AllowedRequests != nil && !config.CustomProviderConfig.AllowedRequests.IsOperationAllowed(route.API) {
		return false
	}
	deadline, _ := parent.Deadline()
	if deadline.IsZero() {
		deadline = time.Now().Add(10 * time.Second)
	}
	ctx := schemas.NewBifrostContext(parent, deadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "routing-health-probe")
	bifrost.setModelCatalogOnContext(ctx)
	base := route.Provider
	if config.CustomProviderConfig != nil {
		base = config.CustomProviderConfig.BaseProviderType
	}
	keys, _, err := bifrost.selectKeyFromProviderForModelWithPool(ctx, route.API, route.Provider, route.Model, base)
	if err != nil || len(keys) == 0 {
		return false
	}
	key, err := bifrost.keySelector(ctx, keys, route.Provider, route.Model)
	if err != nil {
		return false
	}
	model := key.Aliases.Resolve(route.Model)
	prompt := "Bifrost health probe. Reply OK."
	var failure *schemas.BifrostError
	if route.API == schemas.ResponsesRequest {
		_, failure = provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{Provider: route.Provider, Model: model,
			Input:  []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: &prompt}}},
			Params: &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(16)}})
	} else {
		_, failure = provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{Provider: route.Provider, Model: model,
			Input:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &prompt}}},
			Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(16)}})
	}
	bifrost.logger.Info("routing health probe provider=%s model=%s api=%s recovered=%t", route.Provider, route.Model, route.API, failure == nil)
	return failure == nil && parent.Err() == nil
}
