package routingstrategy

import (
	"github.com/maximhq/bifrost/core/schemas"
	"math/rand/v2"
)

// Select performs exactly one selection, using resilience when attached and
// ordinary weighted balancing otherwise. Callers own eligibility and model resolution.
func Select(ctx *schemas.BifrostContext, scope string, targets []schemas.RoutingTarget) (schemas.RoutingTarget, bool) {
	if selector := schemas.RoutingSelectorFromContext(ctx); selector != nil {
		return selector.Select(ctx, scope, targets)
	}
	return weightedTarget(targets)
}

func weightedTarget(targets []schemas.RoutingTarget) (schemas.RoutingTarget, bool) {
	primary := make([]schemas.RoutingTarget, 0, len(targets))
	fallback := make([]schemas.RoutingTarget, 0, len(targets))
	total := 0.0
	for _, t := range targets {
		if t.Weight < 0 {
			continue
		}
		if t.FallbackOnly {
			fallback = append(fallback, t)
		} else {
			primary = append(primary, t)
			total += t.Weight
		}
	}
	if len(primary) == 0 {
		primary = fallback
	}
	if len(primary) == 0 {
		return schemas.RoutingTarget{}, false
	}
	if total == 0 {
		return primary[rand.IntN(len(primary))], true
	}
	r := rand.Float64() * total
	for _, t := range primary {
		r -= t.Weight
		if r < 0 {
			return t, true
		}
	}
	return primary[len(primary)-1], true
}
