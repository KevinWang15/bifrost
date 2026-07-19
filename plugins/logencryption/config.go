package logencryption

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Environment variables controlling log encryption. Boss key material itself is
// NOT read here: the boss public key is returned by the Portal policy API
// (Portal owns it as mandatory deployment config). Bifrost only needs to know
// where Portal is and the shared internal secret.
const (
	envEnabled        = "BF_LOG_ENCRYPTION_ENABLED"
	envPortalBaseURL  = "BF_LOG_ENCRYPTION_PORTAL_URL"
	envInternalSecret = "BF_LOG_ENCRYPTION_INTERNAL_SECRET"
	envPolicyTTL      = "BF_LOG_ENCRYPTION_POLICY_TTL_SECONDS"
)

// failSecureResolver is installed when encryption is enabled but misconfigured.
// It never resolves a policy, so the decorator writes a not-recorded placeholder
// for every row: no plaintext is ever persisted. This is the deliberate anti-
// fail-open behavior — a fat-fingered deploy (ENABLED=true but URL/secret
// missing) degrades to "content not recorded", NOT to plaintext logging.
type failSecureResolver struct{ reason string }

func (r failSecureResolver) Resolve(context.Context, string, string) (*Policy, error) {
	return nil, fmt.Errorf("log-encryption misconfigured: %s", r.reason)
}

// WrapLogStoreFromEnv returns an encrypting decorator around inner when log
// encryption is enabled via environment configuration; otherwise it returns
// inner unchanged. This keeps the upstream call site a single conditional wrap.
//
// Fail-secure invariant: when encryption is ENABLED but incompletely configured,
// this does NOT return the plaintext store. It returns a decorator backed by a
// fail-secure resolver so every row becomes a not-recorded placeholder — the
// "never plaintext at rest" guarantee holds even on misconfiguration. The
// condition is logged at ERROR and every row visibly becomes a placeholder, so
// the misconfig is loud rather than silent.
func WrapLogStoreFromEnv(inner logstore.LogStore, logger schemas.Logger) logstore.LogStore {
	if inner == nil {
		return inner
	}
	if !boolEnv(envEnabled) {
		return inner
	}
	baseURL := os.Getenv(envPortalBaseURL)
	secret := os.Getenv(envInternalSecret)
	if baseURL == "" || secret == "" {
		reason := fmt.Sprintf("%s set but %s/%s missing", envEnabled, envPortalBaseURL, envInternalSecret)
		if logger != nil {
			logger.Error("log-encryption: %s; failing secure — all content will be stored as not-recorded placeholders (no plaintext, no decryptable audit) until fixed", reason)
		}
		return NewEncryptingLogStore(inner, failSecureResolver{reason: reason}, logger)
	}
	ttl := 60 * time.Second
	if v := os.Getenv(envPolicyTTL); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			ttl = time.Duration(secs) * time.Second
		}
	}
	client := NewPortalPolicyClient(PortalPolicyConfig{
		BaseURL:        baseURL,
		InternalSecret: secret,
		TTL:            ttl,
	})
	if logger != nil {
		logger.Info("log-encryption: enabled; policy source %s (ttl %s)", baseURL, ttl)
	}
	return NewEncryptingLogStore(inner, client, logger)
}

func boolEnv(name string) bool {
	v := os.Getenv(name)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}
