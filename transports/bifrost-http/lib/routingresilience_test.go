package lib

import (
	"encoding/json"
	"testing"
)

func TestRoutingResilienceConfigRoundTrip(t *testing.T) {
	var config ConfigData
	if err := json.Unmarshal([]byte(`{"routing_resilience":{"session_stickiness":true,"outage_detection":true,"failure_threshold":1}}`), &config); err != nil {
		t.Fatal(err)
	}
	if config.RoutingResilience == nil || !config.RoutingResilience.SessionStickiness || !config.RoutingResilience.OutageDetection {
		t.Fatal("startup policy lost during config decode")
	}
	c, err := config.RoutingResilience.WithDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if c.CooldownSeconds != 600 || c.ProbeIntervalSeconds != 30 {
		t.Fatal(c)
	}
}
