package routingstrategy

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(schemas.RoutingResilienceConfig{SessionStickiness: true, OutageDetection: true, FailureThreshold: 1}, newTestKV())
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func session(m *Manager, id string) *schemas.BifrostContext {
	c := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	c.SetValue(schemas.BifrostContextKeySessionID, id)
	m.Attach(c, &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Model: "alias"}})
	return c
}

var options = []schemas.RoutingTarget{{Provider: "a", Model: "gpt", Weight: 1}, {Provider: "b", Model: "glm", Weight: 1}}

func TestSessionSelectionConcurrentAndRemoval(t *testing.T) {
	m := testManager(t)
	first, _ := m.Select(session(m, "s"), "rule", options)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, ok := m.Select(session(m, "s"), "rule", options)
			if !ok || !same(got, first) {
				t.Errorf("session moved: %v -> %v", first, got)
			}
		}()
	}
	wg.Wait()
	other := options[0]
	if same(other, first) {
		other = options[1]
	}
	got, ok := m.Select(session(m, "s"), "rule", []schemas.RoutingTarget{other})
	if !ok || !same(got, other) {
		t.Fatal("removed target was retained", got)
	}
}
func TestFallbackBindingSurvivesRecovery(t *testing.T) {
	m := testManager(t)
	ctx := session(m, "s")
	a := options[0]
	b := options[1]
	b.FallbackOnly = true
	m.Select(ctx, "rule", []schemas.RoutingTarget{a, b})
	r := Route{a.Provider, a.Model, schemas.ChatCompletionRequest}
	epoch, _ := m.Begin(r)
	m.Observe(r, epoch, &schemas.BifrostError{StatusCode: schemas.Ptr(503)})
	if _, allowed := m.Begin(r); allowed {
		t.Fatal("open route admitted")
	}
	selected, ok := m.Select(session(m, "new"), "rule", []schemas.RoutingTarget{a, b})
	if !ok || selected.Provider != "b" {
		t.Fatal("healthy fallback not selected", selected)
	}
	m.BindSuccess(ctx, Route{b.Provider, b.Model, schemas.ChatCompletionRequest})
	m.mu.Lock()
	m.health[r].until = time.Time{}
	m.mu.Unlock()
	got, _ := m.Select(session(m, "s"), "rule", []schemas.RoutingTarget{a, b})
	if got.Provider != "b" {
		t.Fatal("recovery moved session back to primary", got)
	}
}
func TestOutageClassificationAndHalfOpen(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 429} {
		m := testManager(t)
		r := Route{"a", "gpt", schemas.ChatCompletionRequest}
		e, _ := m.Begin(r)
		m.Observe(r, e, &schemas.BifrostError{StatusCode: &status})
		if _, ok := m.Begin(r); !ok {
			t.Fatalf("%d incorrectly tripped model route", status)
		}
	}
	m := testManager(t)
	now := time.Now()
	m.now = func() time.Time { return now }
	r := Route{"a", "gpt", schemas.ChatCompletionRequest}
	old, _ := m.Begin(r)
	m.Observe(r, old, &schemas.BifrostError{StatusCode: schemas.Ptr(503)})
	m.Observe(r, old, nil)
	if _, ok := m.Begin(r); ok {
		t.Fatal("old success erased outage")
	}
	now = now.Add(11 * time.Minute)
	e, ok := m.Begin(r)
	if !ok {
		t.Fatal("half-open trial denied")
	}
	if _, ok := m.Begin(r); ok {
		t.Fatal("multiple half-open trials admitted")
	}
	m.Observe(r, e, nil)
	if _, ok := m.Begin(r); !ok {
		t.Fatal("successful trial did not recover")
	}
	if _, ok := m.Begin(Route{"a", "other", schemas.ChatCompletionRequest}); !ok {
		t.Fatal("other model blocked")
	}
}
func TestSessionKVExpiryAndTTLOverride(t *testing.T) {
	m := testManager(t)
	kv := m.kv.(*testKV)
	ctx := session(m, "s")
	ctx.SetValue(schemas.BifrostContextKeySessionTTL, 2*time.Second)
	m.Select(ctx, "rule", options[:1])
	key := m.sessionKey(ctx, "rule")
	if kv.ttls[key] != 2*time.Second {
		t.Fatal("session TTL override ignored")
	}
	kv.now = kv.now.Add(time.Second)
	m.Select(ctx, "rule", options)
	kv.now = kv.now.Add(1500 * time.Millisecond)
	if _, err := kv.Get(key); err != nil {
		t.Fatal("active binding TTL not refreshed")
	}
	kv.now = kv.now.Add(time.Second)
	a, b := options[0], options[1]
	a.Weight = 0
	got, _ := m.Select(session(m, "s"), "rule", []schemas.RoutingTarget{a, b})
	if got.Provider != b.Provider {
		t.Fatal("expired binding retained", got)
	}
}

func TestManagersShareSessionKV(t *testing.T) {
	kv := newTestKV()
	m1, _ := New(schemas.RoutingResilienceConfig{SessionStickiness: true}, kv)
	m2, _ := New(schemas.RoutingResilienceConfig{SessionStickiness: true}, kv)
	start := make(chan struct{})
	results := make(chan schemas.RoutingTarget, 100)
	for i := range 100 {
		go func() {
			<-start
			m := m1
			if i%2 == 0 {
				m = m2
			}
			got, _ := m.Select(session(m, "shared"), "rule", options)
			results <- got
		}()
	}
	close(start)
	first := <-results
	for range 99 {
		if got := <-results; !same(got, first) {
			t.Fatal("shared KV chose conflicting bindings", got, first)
		}
	}
	// Losing a Manager must not lose bindings: its caller owns the KV lifetime.
	m1.Close()
	got, _ := m2.Select(session(m2, "shared"), "rule", options)
	if !same(got, first) {
		t.Fatal("binding did not survive manager replacement")
	}
}

func TestBackgroundProbeRecoversBeforeCooldown(t *testing.T) {
	m := testManager(t)
	m.config.ProbeIntervalSeconds = 1
	r := Route{"a", "gpt", schemas.ChatCompletionRequest}
	e, _ := m.Begin(r)
	m.Observe(r, e, &schemas.BifrostError{StatusCode: schemas.Ptr(503)})
	probed := make(chan Route, 1)
	m.Start(context.Background(), func(_ context.Context, route Route) bool { probed <- route; return true })
	defer m.Close()
	select {
	case got := <-probed:
		if got != r {
			t.Fatal(got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("no background probe")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.Begin(r); ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("probe did not restore route")
}

func TestLateStreamErrorQuarantinesWithoutReplay(t *testing.T) {
	m := testManager(t)
	ctx := session(m, "stream")
	r := Route{"a", "gpt", schemas.ChatCompletionRequest}
	epoch, _ := m.Begin(r)
	source := make(chan *schemas.BifrostStreamChunk, 2)
	first := &schemas.BifrostStreamChunk{}
	failure := &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{StatusCode: schemas.Ptr(503)}}
	source <- first
	source <- failure
	close(source)
	stream := m.WatchStream(ctx, r, epoch, source)
	if <-stream != first || <-stream != failure {
		t.Fatal("stream chunks were changed")
	}
	for range stream {
	}
	if _, ok := m.Begin(r); ok {
		t.Fatal("late stream failure was not remembered")
	}
}

func TestConcurrentOldSuccessDoesNotUndoFallbackBinding(t *testing.T) {
	m := testManager(t)
	a := options[0]
	b := options[1]
	b.FallbackOnly = true
	c1, c2 := session(m, "s"), session(m, "s")
	m.Select(c1, "rule", []schemas.RoutingTarget{a, b})
	m.Select(c2, "rule", []schemas.RoutingTarget{a, b})
	m.BindSuccess(c2, Route{b.Provider, b.Model, schemas.ChatCompletionRequest})
	m.BindSuccess(c1, Route{a.Provider, a.Model, schemas.ChatCompletionRequest})
	got, _ := m.Select(session(m, "s"), "rule", []schemas.RoutingTarget{a, b})
	if got.Provider != "b" {
		t.Fatal("older success undid fallback binding")
	}
}

func TestSessionIdentityIsolation(t *testing.T) {
	m := testManager(t)
	c1 := session(m, "same-id")
	c2 := session(m, "same-id")
	c1.SetValue(schemas.BifrostContextKeyVirtualKey, "tenant-a")
	c2.SetValue(schemas.BifrostContextKeyVirtualKey, "tenant-b")
	if m.sessionKey(c1, "rule") == m.sessionKey(c2, "rule") {
		t.Fatal("tenant session namespaces overlap")
	}
}

func TestNilKVDisablesAffinity(t *testing.T) {
	m, err := New(schemas.RoutingResilienceConfig{SessionStickiness: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.Select(session(m, "s"), "rule", options[:1])
	a, b := options[0], options[1]
	a.Weight = 0
	got, _ := m.Select(session(m, "s"), "rule", []schemas.RoutingTarget{a, b})
	if got.Provider != b.Provider {
		t.Fatal("affinity retained without a KV store")
	}
}

// testKV exercises the injected storage contract with a controllable expiry clock.
type testKV struct {
	mu      sync.Mutex
	values  map[string]any
	expires map[string]time.Time
	ttls    map[string]time.Duration
	now     time.Time
	fail    bool
}

func newTestKV() *testKV {
	return &testKV{values: map[string]any{}, expires: map[string]time.Time{}, ttls: map[string]time.Duration{}, now: time.Now()}
}
func (s *testKV) Get(key string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail || !s.now.Before(s.expires[key]) {
		return nil, fmt.Errorf("unavailable or expired")
	}
	return s.values[key], nil
}
func (s *testKV) SetWithTTL(key string, value any, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return fmt.Errorf("unavailable")
	}
	s.values[key], s.expires[key], s.ttls[key] = value, s.now.Add(ttl), ttl
	return nil
}
func (s *testKV) SetNXWithTTL(key string, value any, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return false, fmt.Errorf("unavailable")
	}
	if s.now.Before(s.expires[key]) {
		return false, nil
	}
	s.values[key], s.expires[key], s.ttls[key] = value, s.now.Add(ttl), ttl
	return true, nil
}
func (s *testKV) Delete(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return false, fmt.Errorf("unavailable")
	}
	_, found := s.values[key]
	delete(s.values, key)
	delete(s.expires, key)
	return found, nil
}

func TestKVFailureStillSelectsEligibleTarget(t *testing.T) {
	m := testManager(t)
	m.kv.(*testKV).fail = true
	got, ok := m.Select(session(m, "s"), "rule", options[:1])
	if !ok || !same(got, options[0]) {
		t.Fatal("KV failure blocked request")
	}
}

func TestReplicatedBindingValueAndDefaultTTL(t *testing.T) {
	m := testManager(t)
	ctx := session(m, "replicated")
	m.Select(ctx, "rule", options[:1])
	kv := m.kv.(*testKV)
	key := m.sessionKey(ctx, "rule")
	if kv.ttls[key] != schemas.DefaultSessionStickyTTL {
		t.Fatal("wrong default TTL")
	}
	// The existing KV replication mechanism JSON-encodes string values.
	raw, _ := json.Marshal(kv.values[key])
	kv.values[key] = raw
	got, ok := m.Select(session(m, "replicated"), "rule", options)
	if !ok || !same(got, options[0]) {
		t.Fatal("replicated KV binding was not decoded")
	}
}
