package schemas

import (
	"fmt"
	"time"
)

// RoutingResilienceConfig is startup-only. Session affinity uses the injected
// KVStore; route health remains local to one Bifrost instance.
type RoutingResilienceConfig struct {
	SessionStickiness    bool `json:"session_stickiness"`
	OutageDetection      bool `json:"outage_detection"`
	SessionTTLSeconds    int  `json:"session_ttl_seconds"`
	FailureThreshold     int  `json:"failure_threshold"`
	CooldownSeconds      int  `json:"cooldown_seconds"`
	ProbeIntervalSeconds int  `json:"probe_interval_seconds"`
	ProbeTimeoutSeconds  int  `json:"probe_timeout_seconds"`
}

func (c RoutingResilienceConfig) WithDefaults() (RoutingResilienceConfig, error) {
	fields := []struct {
		value         *int
		fallback, max int
		name          string
	}{
		{&c.SessionTTLSeconds, int(DefaultSessionStickyTTL / time.Second), 2592000, "session_ttl_seconds"},
		{&c.FailureThreshold, 3, 100, "failure_threshold"},
		{&c.CooldownSeconds, 600, 86400, "cooldown_seconds"},
		{&c.ProbeIntervalSeconds, 30, 3600, "probe_interval_seconds"},
		{&c.ProbeTimeoutSeconds, 10, 120, "probe_timeout_seconds"},
	}
	for _, f := range fields {
		if *f.value == 0 {
			*f.value = f.fallback
		}
		if *f.value < 1 || *f.value > f.max {
			return c, fmt.Errorf("routing_resilience.%s must be between 1 and %d", f.name, f.max)
		}
	}
	return c, nil
}

// RoutingTarget is an eligible destination. FallbackOnly targets participate only
// when primaries are unavailable or an existing session is bound to them.
type RoutingTarget struct {
	Provider     ModelProvider
	Model        string
	KeyID        string
	Weight       float64
	FallbackOnly bool
}

type RoutingSelector interface {
	Select(ctx *BifrostContext, scope string, targets []RoutingTarget) (RoutingTarget, bool)
}

type routingSelectorContextKey struct{}

func SetRoutingSelector(ctx *BifrostContext, selector RoutingSelector) {
	ctx.SetValue(routingSelectorContextKey{}, selector)
}

func RoutingSelectorFromContext(ctx *BifrostContext) RoutingSelector {
	if ctx == nil {
		return nil
	}
	selector, _ := ctx.Value(routingSelectorContextKey{}).(RoutingSelector)
	return selector
}
