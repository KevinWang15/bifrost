package logencryption

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/maximhq/bifrost/plugins/logencryption/envelope"
)

// PublicKeyInfo describes a recipient public key returned by the policy API.
// The PEM is parsed lazily and cached alongside the policy entry.
type PublicKeyInfo struct {
	KID          string `json:"kid"`
	Alg          string `json:"alg"`
	PublicKeyPEM string `json:"public_key_pem"`
}

// Policy is the encryption policy for a single (virtual key, team) pair as
// resolved by Portal. Portal is the source of truth; Bifrost never decides
// exemptions on its own.
type Policy struct {
	TeamID  string `json:"team_id"`
	UserID  string `json:"user_id"` // owner user id resolved from the VK description
	Encrypt bool   `json:"encrypt"`
	Exempt  bool   `json:"exempt"`

	BossPublicKey  *PublicKeyInfo `json:"boss_public_key"`
	OwnerPublicKey *PublicKeyInfo `json:"owner_public_key"`

	// Parsed keys are filled on first use. Policies are shared by the TTL cache and
	// used concurrently by logging workers, so protect lazy initialization.
	keyMu    sync.Mutex
	bossKey  *rsa.PublicKey
	ownerKey *rsa.PublicKey
}

// Recipients builds the envelope recipient list from the policy. It always
// includes the boss recipient (required); the owner recipient is included only
// when an owner public key is configured. Returns an error if the boss key is
// missing/unparseable, which the caller treats as fail-secure.
func (p *Policy) Recipients() ([]envelope.PublicRecipient, error) {
	p.keyMu.Lock()
	defer p.keyMu.Unlock()

	if p.BossPublicKey == nil {
		return nil, fmt.Errorf("policy: boss public key missing")
	}
	if p.bossKey == nil {
		k, err := envelope.ParseRSAPublicKeyPEM(p.BossPublicKey.PublicKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("policy: parse boss key: %w", err)
		}
		p.bossKey = k
	}
	recipients := []envelope.PublicRecipient{
		{Type: envelope.RecipientBoss, KID: p.BossPublicKey.KID, PublicKey: p.bossKey},
	}
	if p.OwnerPublicKey != nil && p.OwnerPublicKey.PublicKeyPEM != "" {
		if p.ownerKey == nil {
			k, err := envelope.ParseRSAPublicKeyPEM(p.OwnerPublicKey.PublicKeyPEM)
			if err != nil {
				// Owner key is best-effort: a bad owner key must not block boss-only encryption.
				return recipients, nil
			}
			p.ownerKey = k
		}
		recipients = append(recipients, envelope.PublicRecipient{
			Type: envelope.RecipientOwner, KID: p.OwnerPublicKey.KID, PublicKey: p.ownerKey,
		})
	}
	return recipients, nil
}

// PolicyResolver resolves encryption policy for a request. It is an interface so
// the store decorator can be tested without a real Portal.
type PolicyResolver interface {
	// Resolve returns the policy for the given virtual key / team. A non-nil
	// error means the policy could not be determined (fail-secure applies).
	Resolve(ctx context.Context, virtualKeyID, teamID string) (*Policy, error)
}

type cacheEntry struct {
	policy    *Policy
	expiresAt time.Time
}

// PortalPolicyClient calls the Portal internal policy API and caches results
// per (vk,team) key with a short TTL.
type PortalPolicyClient struct {
	baseURL        string
	internalSecret string
	httpClient     *http.Client
	ttl            time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// PortalPolicyConfig configures the policy client.
type PortalPolicyConfig struct {
	BaseURL        string        // e.g. http://portal-backend:10076
	InternalSecret string        // shared secret sent as x-kms-identity-secret
	TTL            time.Duration // cache TTL; defaults to 60s if zero
	HTTPClient     *http.Client  // optional; a default client is used if nil
}

// NewPortalPolicyClient builds a policy client.
func NewPortalPolicyClient(cfg PortalPolicyConfig) *PortalPolicyClient {
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	return &PortalPolicyClient{
		baseURL:        cfg.BaseURL,
		internalSecret: cfg.InternalSecret,
		httpClient:     hc,
		ttl:            ttl,
		cache:          make(map[string]cacheEntry),
	}
}

func cacheKey(vk, team string) string { return vk + "\x00" + team }

// Resolve implements PolicyResolver with a TTL cache.
func (c *PortalPolicyClient) Resolve(ctx context.Context, virtualKeyID, teamID string) (*Policy, error) {
	key := cacheKey(virtualKeyID, teamID)

	c.mu.Lock()
	if e, ok := c.cache[key]; ok && time.Now().Before(e.expiresAt) {
		c.mu.Unlock()
		return e.policy, nil
	}
	c.mu.Unlock()

	policy, err := c.fetch(ctx, virtualKeyID, teamID)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[key] = cacheEntry{policy: policy, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return policy, nil
}

func (c *PortalPolicyClient) fetch(ctx context.Context, virtualKeyID, teamID string) (*Policy, error) {
	q := url.Values{}
	q.Set("virtual_key_id", virtualKeyID)
	q.Set("team_id", teamID)
	endpoint := fmt.Sprintf("%s/api/portal/internal/log-encryption/policy?%s", c.baseURL, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-kms-identity-secret", c.internalSecret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("policy fetch: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("policy fetch: status %d: %s", resp.StatusCode, string(body))
	}
	var policy Policy
	if err := json.Unmarshal(body, &policy); err != nil {
		return nil, fmt.Errorf("policy decode: %w", err)
	}
	return &policy, nil
}
