package routingstrategy

import (
	"github.com/maximhq/bifrost/core/schemas"
	"testing"
)

func TestSelectionCandidateRoles(t *testing.T) {
	primary := schemas.RoutingTarget{Provider: "a", Model: "gpt", Weight: 1}
	fallback := schemas.RoutingTarget{Provider: "b", Model: "glm", Weight: 100, FallbackOnly: true}
	invalid := schemas.RoutingTarget{Provider: "invalid", Weight: -1}
	for range 100 {
		got, ok := Select(nil, "", []schemas.RoutingTarget{fallback, invalid, primary})
		if !ok || got != primary {
			t.Fatal("fallback or invalid weight participated in primary balancing", got)
		}
	}
	got, ok := Select(nil, "", []schemas.RoutingTarget{invalid, fallback})
	if !ok || got != fallback {
		t.Fatal("fallback unavailable without primaries")
	}
	if _, ok := Select(nil, "", []schemas.RoutingTarget{invalid}); ok {
		t.Fatal("negative weight accepted")
	}
	primary.Weight = 0
	if got, ok := Select(nil, "", []schemas.RoutingTarget{primary, fallback}); !ok || got != primary {
		t.Fatal("zero weight primary lost to fallback")
	}
}
