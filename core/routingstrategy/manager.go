// Package routingstrategy selects eligible destinations using KV-backed affinity
// and bounded, node-local route health.
package routingstrategy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

type Route struct {
	Provider schemas.ModelProvider
	Model    string
	API      schemas.RequestType
}
type health struct {
	failures                  int
	until, nextProbe, touched time.Time
	probing                   bool
	epoch                     uint64
}
type bindingKey struct{}
type selectedTargetKey struct{}
type requestScopeKey struct{}
type requestAPIKey struct{}
type selectionMadeKey struct{}

// Manager stores only route identifiers, never prompts, request bodies or credentials.
type Manager struct {
	mu           sync.Mutex
	config       schemas.RoutingResilienceConfig
	kv           schemas.KVStore
	sessionLocks [64]sync.Mutex
	health       map[Route]*health
	epoch        uint64
	now          func() time.Time
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

func New(config schemas.RoutingResilienceConfig, kv schemas.KVStore) (*Manager, error) {
	c, err := config.WithDefaults()
	if err != nil {
		return nil, err
	}
	return &Manager{config: c, kv: kv, health: make(map[Route]*health), now: time.Now}, nil
}

func API(kind schemas.RequestType) schemas.RequestType {
	switch kind {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		return schemas.ChatCompletionRequest
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		return schemas.ResponsesRequest
	}
	return ""
}

func (m *Manager) Attach(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) {
	if m == nil {
		return
	}
	schemas.SetRoutingSelector(ctx, nil)
	if API(req.RequestType) == "" || ctx.Value(schemas.BifrostContextKeyDirectKey) != nil || ctx.Value(schemas.BifrostContextKeyLargePayloadMetadata) != nil {
		return
	}
	if skip, _ := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); skip {
		return
	}
	provider, model, _ := req.GetRequestFields()
	ctx.SetValue(requestScopeKey{}, fmt.Sprintf("%q/%q", provider, model))
	ctx.SetValue(requestAPIKey{}, API(req.RequestType))
	ctx.ClearValue(bindingKey{})
	ctx.ClearValue(selectionMadeKey{})
	schemas.SetRoutingSelector(ctx, m)
}

// Finalize also gives explicitly addressed requests with a fallback chain a
// stable successful destination. Weighted selectors already made their decision.
func (m *Manager) Finalize(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) {
	if m == nil || schemas.RoutingSelectorFromContext(ctx) == nil || API(req.RequestType) == "" || ctx.Value(selectionMadeKey{}) != nil {
		return
	}
	p, model, fallbacks := req.GetRequestFields()
	if p == "" || len(fallbacks) == 0 {
		return
	}
	options := []schemas.RoutingTarget{{Provider: p, Model: model, Weight: 1}}
	for _, f := range fallbacks {
		options = append(options, schemas.RoutingTarget{Provider: f.Provider, Model: f.Model, FallbackOnly: true})
	}
	selected, ok := m.Select(ctx, "explicit-fallbacks", options)
	if !ok || (selected.Provider == p && selected.Model == model) {
		return
	}
	remaining := []schemas.Fallback{{Provider: p, Model: model}}
	for _, f := range fallbacks {
		if f.Provider != selected.Provider || f.Model != selected.Model {
			remaining = append(remaining, f)
		}
	}
	req.SetProvider(selected.Provider)
	req.SetModel(selected.Model)
	req.SetFallbacks(remaining)
	ctx.ClearValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID)
	ctx.ClearValue(schemas.BifrostContextKeyAPIKeyID)
	ctx.ClearValue(schemas.BifrostContextKeyAPIKeyName)
}

func (m *Manager) sessionKey(ctx *schemas.BifrostContext, scope string) string {
	id, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	if !m.config.SessionStickiness || m.kv == nil || id == "" {
		return ""
	}
	// Delimit with quoted values: session IDs are caller-controlled. Separate
	// identities and incoming aliases, not only the eventual physical model.
	parts := []string{scope, id}
	for _, key := range []any{requestScopeKey{}, requestAPIKey{}, schemas.BifrostContextKeyVirtualKey,
		schemas.BifrostContextKeyGovernanceVirtualKeyID, schemas.BifrostContextKeyUserID,
		schemas.BifrostContextKeyGovernanceTeamID, schemas.BifrostContextKeyGovernanceCustomerID} {
		parts = append(parts, fmt.Sprint(ctx.Value(key)))
	}
	return fmt.Sprintf("routing:session:%x", sha256.Sum256([]byte(fmt.Sprintf("%q", parts))))
}

func same(a, b schemas.RoutingTarget) bool {
	return a.Provider == b.Provider && a.Model == b.Model && a.KeyID == b.KeyID
}

// sessionLock serializes updates within this manager without holding the health
// lock during KV I/O. Cross-manager first writes use KVStore.SetNXWithTTL.
func (m *Manager) sessionLock(key string) *sync.Mutex {
	sum := sha256.Sum256([]byte(key))
	return &m.sessionLocks[int(sum[0])%len(m.sessionLocks)]
}

func (m *Manager) ttl(ctx *schemas.BifrostContext) time.Duration {
	if ttl, _ := ctx.Value(schemas.BifrostContextKeySessionTTL).(time.Duration); ttl > 0 {
		return ttl
	}
	return time.Duration(m.config.SessionTTLSeconds) * time.Second
}

func (m *Manager) readBinding(key string) (schemas.RoutingTarget, bool) {
	raw, err := m.kv.Get(key)
	if err != nil {
		return schemas.RoutingTarget{}, false
	}
	var data string
	switch value := raw.(type) {
	case string:
		data = value
	case []byte:
		// Replicated KV entries can arrive as the JSON encoding of the stored string.
		if sonic.Unmarshal(value, &data) != nil {
			data = string(value)
		}
	}
	var target schemas.RoutingTarget
	err = sonic.UnmarshalString(data, &target)
	return target, err == nil
}

func bindingValue(target schemas.RoutingTarget) string {
	// A struct has stable field ordering and round-trips through string-valued KV backends.
	value, _ := sonic.MarshalString(target)
	return value
}

func bindingError(ctx *schemas.BifrostContext, err error) {
	if err != nil {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineCore, schemas.LogLevelWarn, "Session routing KV operation failed; continuing without guaranteed affinity")
	}
}

func (m *Manager) Select(ctx *schemas.BifrostContext, scope string, targets []schemas.RoutingTarget) (schemas.RoutingTarget, bool) {
	ctx.SetValue(selectionMadeKey{}, true)
	key := m.sessionKey(ctx, scope)
	if key != "" {
		lock := m.sessionLock(key)
		lock.Lock()
		defer lock.Unlock()
		ctx.SetValue(bindingKey{}, key)
	}
	api, _ := ctx.Value(requestAPIKey{}).(schemas.RequestType)
	m.mu.Lock()
	now := m.now()
	eligible := make([]schemas.RoutingTarget, 0, len(targets))
	for _, t := range targets {
		h := m.health[Route{t.Provider, t.Model, api}]
		if t.Weight >= 0 && (h == nil || h.until.IsZero() || (!h.probing && !now.Before(h.until))) {
			eligible = append(eligible, t)
		}
	}
	m.mu.Unlock()
	match := func(cached schemas.RoutingTarget) (schemas.RoutingTarget, bool) {
		for _, t := range eligible {
			if same(t, cached) {
				return t, true
			}
		}
		return schemas.RoutingTarget{}, false
	}
	if key != "" {
		if cached, found := m.readBinding(key); found {
			if t, valid := match(cached); valid {
				bindingError(ctx, m.kv.SetWithTTL(key, bindingValue(t), m.ttl(ctx)))
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineCore, schemas.LogLevelInfo, fmt.Sprintf("Reusing session target %s/%s", t.Provider, t.Model))
				ctx.SetValue(selectedTargetKey{}, t)
				return t, true
			}
			_, err := m.kv.Delete(key)
			bindingError(ctx, err)
		}
	}
	selected, ok := weightedTarget(eligible)
	if !ok {
		return selected, false
	}
	if key != "" {
		written, err := m.kv.SetNXWithTTL(key, bindingValue(selected), m.ttl(ctx))
		bindingError(ctx, err)
		if err == nil && !written {
			if cached, found := m.readBinding(key); found {
				if winner, valid := match(cached); valid {
					selected = winner
				}
			}
		}
	}
	ctx.SetValue(selectedTargetKey{}, selected)
	return selected, true
}

func (m *Manager) BindSuccess(ctx *schemas.BifrostContext, route Route) {
	if m == nil || m.kv == nil {
		return
	}
	key, _ := ctx.Value(bindingKey{}).(string)
	if key == "" {
		return
	}
	lock := m.sessionLock(key)
	lock.Lock()
	defer lock.Unlock()
	target, ok := m.readBinding(key)
	if !ok {
		return
	}
	selected, _ := ctx.Value(selectedTargetKey{}).(schemas.RoutingTarget)
	if !same(target, selected) {
		return
	} // a newer request already rebound this session
	m.mu.Lock()
	h := m.health[route]
	healthy := h == nil || h.until.IsZero()
	m.mu.Unlock()
	if !healthy {
		return
	}
	// Preserve a routing-rule key pin only while staying on that same target.
	if target.Provider != route.Provider || target.Model != route.Model {
		target = schemas.RoutingTarget{Provider: route.Provider, Model: route.Model}
	}
	bindingError(ctx, m.kv.SetWithTTL(key, bindingValue(target), m.ttl(ctx)))
}

// Begin gates every actual upstream attempt, including explicit primaries and
// configured fallbacks. After cooldown only one business request may probe.
func (m *Manager) Begin(route Route) (uint64, bool) {
	if m == nil || !m.config.OutageDetection || route.API == "" {
		return 0, true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	h := m.health[route]
	if h == nil {
		if len(m.health) >= 10000 {
			return 0, true
		}
		m.epoch++
		h = &health{epoch: m.epoch}
		m.health[route] = h
	}
	h.touched = now
	if !h.until.IsZero() {
		if h.probing || now.Before(h.until) {
			return 0, false
		}
		h.probing = true
	}
	return h.epoch, true
}

// Observe counts only upstream network/5xx failures. Caller cancellation, local
// governance errors, malformed requests, and per-key 4xx failures do not trip a route.
func (m *Manager) Observe(route Route, epoch uint64, failure *schemas.BifrostError) {
	if m == nil || epoch == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.health[route]
	if h == nil || h.epoch != epoch {
		return
	} // an older in-flight success cannot close a newer outage
	now := m.now()
	h.touched = now
	bad := failure != nil && !failure.IsBifrostError && ((failure.StatusCode != nil && *failure.StatusCode >= 500 && *failure.StatusCode <= 599) ||
		(failure.Error != nil && (failure.Error.Message == schemas.ErrProviderDoRequest || failure.Error.Message == schemas.ErrProviderNetworkError)))
	if failure == nil {
		h.failures = 0
		h.until = time.Time{}
		h.probing = false
		return
	}
	if !bad {
		if h.probing {
			h.probing = false
			h.until = now.Add(time.Duration(m.config.CooldownSeconds) * time.Second)
			h.nextProbe = now.Add(time.Duration(m.config.ProbeIntervalSeconds) * time.Second)
		}
		return
	}
	h.failures++
	if h.failures >= m.config.FailureThreshold || h.probing {
		m.epoch++
		h.epoch = m.epoch
		h.probing = false
		h.until = now.Add(time.Duration(m.config.CooldownSeconds) * time.Second)
		h.nextProbe = now.Add(time.Duration(m.config.ProbeIntervalSeconds) * time.Second)
	}
}

// Start runs bounded background probes. The executor must contact the exact
// route with a synthetic prompt, without routing rules or fallbacks.
func (m *Manager) Start(parent context.Context, probe func(context.Context, Route) bool) {
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := m.now()
				m.mu.Lock()
				type job struct {
					r     Route
					epoch uint64
				}
				var jobs []job
				for r, h := range m.health {
					if now.Sub(h.touched) > max(time.Hour, 2*time.Duration(m.config.CooldownSeconds)*time.Second) {
						delete(m.health, r)
						continue
					}
					if len(jobs) < 4 && !h.until.IsZero() && !h.probing && !now.Before(h.nextProbe) {
						h.probing = true
						jobs = append(jobs, job{r, h.epoch})
					}
				}
				m.mu.Unlock()
				var workers sync.WaitGroup
				for _, j := range jobs {
					workers.Add(1)
					go func() {
						defer workers.Done()
						pc, done := context.WithTimeout(ctx, time.Duration(m.config.ProbeTimeoutSeconds)*time.Second)
						ok := probe(pc, j.r)
						done()
						m.mu.Lock()
						defer m.mu.Unlock()
						if h := m.health[j.r]; h != nil && h.epoch == j.epoch {
							h.probing = false
							h.nextProbe = m.now().Add(time.Duration(m.config.ProbeIntervalSeconds) * time.Second)
							if ok {
								h.failures = 0
								h.until = time.Time{}
							} else {
								h.until = m.now().Add(time.Duration(m.config.CooldownSeconds) * time.Second)
							}
						}
					}()
				}
				workers.Wait()
			}
		}
	}()
}
func (m *Manager) Close() {
	if m != nil && m.cancel != nil {
		m.cancel()
		m.wg.Wait()
	}
}

// WatchStream observes failures after the startup boundary without replaying any
// output. It keeps no chunk history and updates health before closing the stream.
func (m *Manager) WatchStream(ctx *schemas.BifrostContext, route Route, epoch uint64, source chan *schemas.BifrostStreamChunk) chan *schemas.BifrostStreamChunk {
	out := make(chan *schemas.BifrostStreamChunk)
	go func() {
		defer close(out)
		failed := false
		defer func() {
			if ctx.Err() != nil {
				m.Observe(route, epoch, &schemas.BifrostError{IsBifrostError: true})
				return
			}
			if !failed {
				m.Observe(route, epoch, nil)
				m.BindSuccess(ctx, route)
			}
		}()
		for chunk := range source {
			if chunk != nil && chunk.BifrostError != nil && !failed && ctx.Err() == nil {
				m.Observe(route, epoch, chunk.BifrostError)
				failed = true
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				for range source {
				}
				return
			}
		}
	}()
	return out
}
