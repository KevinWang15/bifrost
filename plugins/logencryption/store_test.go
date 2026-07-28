package logencryption

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"sort"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logencryption/envelope"
)

// ---- test doubles ----

type fakeResolver struct {
	policy *Policy
	err    error
}

func (f *fakeResolver) Resolve(_ context.Context, _, _ string) (*Policy, error) {
	return f.policy, f.err
}

// captureStore records the entries it received (after decoration).
type captureStore struct {
	logstore.LogStore // nil embedded; only write methods are called in tests
	batch             []*logstore.Log
}

func (c *captureStore) BatchCreateIfNotExists(_ context.Context, entries []*logstore.Log) error {
	c.batch = append(c.batch, entries...)
	return nil
}

type captureUpdateStore struct {
	logstore.LogStore
	lastUpdate any
}

func (c *captureUpdateStore) Update(_ context.Context, _ string, entry any) error {
	c.lastUpdate = entry
	return nil
}

// ---- helpers ----

func genPub(t *testing.T) (*rsa.PrivateKey, *PublicKeyInfo, string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return k, &PublicKeyInfo{KID: "kid", Alg: envelope.RecipientAlgRSAOAEPSHA256, PublicKeyPEM: pemStr}, pemStr
}

func strp(s string) *string { return &s }

func ptrRole(r schemas.ResponsesMessageRoleType) *schemas.ResponsesMessageRoleType { return &r }

func newEntry() *logstore.Log {
	return &logstore.Log{
		ID:             "req_1",
		VirtualKeyID:   strp("vk_1"),
		TeamID:         strp("team_1"),
		InputHistory:   `[{"role":"user","content":"secret input"}]`,
		OutputMessage:  `{"role":"assistant","content":"secret output"}`,
		ContentSummary: "secret input",
	}
}

// ---- tests ----

func TestEncryptedTeamWithOwnerKey(t *testing.T) {
	bossPriv, bossInfo, _ := genPub(t)
	ownerPriv, ownerInfo, _ := genPub(t)
	bossInfo.KID = "boss-v1"
	ownerInfo.KID = "user-alice-v1"

	inner := &captureStore{}
	s := NewEncryptingLogStore(inner, &fakeResolver{policy: &Policy{
		TeamID: "team_1", UserID: "alice", Encrypt: true,
		BossPublicKey: bossInfo, OwnerPublicKey: ownerInfo,
	}}, nil)

	e := newEntry()
	if err := s.BatchCreateIfNotExists(context.Background(), []*logstore.Log{e}); err != nil {
		t.Fatal(err)
	}
	got := inner.batch[0]

	// Carrier: exactly one disguised message in InputHistoryParsed whose content
	// is the bundle envelope. Other content fields cleared.
	env := carrierEnvelope(t, got)
	if got.ContentSummary != summarySentinel {
		t.Fatalf("content summary must be sentinel, got %q", got.ContentSummary)
	}
	if got.PayloadStoragePolicy != logstore.PayloadStorageObjectOnly {
		t.Fatalf("encrypted payload must be object-only in hybrid mode, got %d", got.PayloadStoragePolicy)
	}
	if got.OutputMessage != "" || got.OutputMessageParsed != nil {
		t.Fatal("output must be folded into the carrier, not left in its own field")
	}
	if len(env.Recipients) != 2 {
		t.Fatalf("want 2 recipients, got %d", len(env.Recipients))
	}

	// Owner opens the bundle; it contains both input and output.
	ownerBundle := openBundle(t, env, "user-alice-v1", ownerPriv)
	if string(ownerBundle.InputHistory) != `[{"role":"user","content":"secret input"}]` {
		t.Fatalf("owner bundle input mismatch: %s", ownerBundle.InputHistory)
	}
	if string(ownerBundle.OutputMessage) != `{"role":"assistant","content":"secret output"}` {
		t.Fatalf("owner bundle output mismatch: %s", ownerBundle.OutputMessage)
	}
	// Boss can also open the same bundle.
	if _, err := envelope.Open(env, "boss-v1", bossPriv); err != nil {
		t.Fatalf("boss open bundle: %v", err)
	}
}

func TestEncryptedWriterRetryIsIdempotent(t *testing.T) {
	bossPriv, bossInfo, _ := genPub(t)
	bossInfo.KID = "boss-v1"

	inner := &captureStore{}
	store := NewEncryptingLogStore(inner, &fakeResolver{policy: &Policy{
		TeamID: "team_1", UserID: "alice", Encrypt: true,
		BossPublicKey: bossInfo,
	}}, nil)
	entry := newEntry()

	if err := store.BatchCreateIfNotExists(context.Background(), []*logstore.Log{entry}); err != nil {
		t.Fatal(err)
	}
	firstCarrier := carrierContent(t, entry)

	// The logging writer retries the same pointer after a failed batch.
	if err := store.BatchCreateIfNotExists(context.Background(), []*logstore.Log{entry}); err != nil {
		t.Fatal(err)
	}
	secondCarrier := carrierContent(t, entry)
	if secondCarrier != firstCarrier {
		t.Fatal("retry must reuse the installed carrier instead of nesting a new envelope")
	}

	bundle := openBundle(t, carrierEnvelope(t, entry), "boss-v1", bossPriv)
	if string(bundle.InputHistory) != `[{"role":"user","content":"secret input"}]` {
		t.Fatalf("retry changed decrypted input: %s", bundle.InputHistory)
	}
	if string(bundle.OutputMessage) != `{"role":"assistant","content":"secret output"}` {
		t.Fatalf("retry changed decrypted output: %s", bundle.OutputMessage)
	}
}

func TestPayloadFieldEncryptionClassificationIsExhaustive(t *testing.T) {
	// Auxiliary payload fields are intentionally allowed to remain cleartext
	// under the accepted deployment policy. Keeping every field explicit makes
	// future Bifrost additions fail this test until they receive a deliberate
	// encryption classification.
	classification := map[string]string{
		"input_history":             "encrypted",
		"responses_input_history":   "encrypted",
		"output_message":            "encrypted",
		"responses_output":          "encrypted",
		"raw_request":               "cleared",
		"raw_response":              "cleared",
		"embedding_output":          "allowed-cleartext",
		"rerank_output":             "allowed-cleartext",
		"ocr_input":                 "allowed-cleartext",
		"ocr_output":                "allowed-cleartext",
		"params":                    "allowed-cleartext",
		"tools":                     "allowed-cleartext",
		"tool_calls":                "allowed-cleartext",
		"speech_input":              "allowed-cleartext",
		"transcription_input":       "allowed-cleartext",
		"image_generation_input":    "allowed-cleartext",
		"image_edit_input":          "allowed-cleartext",
		"image_variation_input":     "allowed-cleartext",
		"video_generation_input":    "allowed-cleartext",
		"video_edit_input":          "allowed-cleartext", // Same modality policy as video generation.
		"speech_output":             "allowed-cleartext",
		"transcription_output":      "allowed-cleartext",
		"image_generation_output":   "allowed-cleartext",
		"list_models_output":        "allowed-cleartext",
		"video_generation_output":   "allowed-cleartext",
		"video_retrieve_output":     "allowed-cleartext",
		"video_download_output":     "allowed-cleartext",
		"video_list_output":         "allowed-cleartext",
		"video_delete_output":       "allowed-cleartext",
		"cache_debug":               "allowed-cleartext",
		"guardrail_debug":           "allowed-cleartext", // Judge metadata, matching error/cache diagnostics policy.
		"token_usage":               "allowed-cleartext",
		"error_details":             "allowed-cleartext",
		"passthrough_request_body":  "allowed-cleartext",
		"passthrough_response_body": "allowed-cleartext",
		"routing_engine_logs":       "allowed-cleartext",
	}

	seen := make(map[string]struct{})
	for _, field := range logstore.PayloadFieldNames() {
		if _, duplicate := seen[field]; duplicate {
			t.Errorf("payload field %q is duplicated", field)
		}
		seen[field] = struct{}{}
		if _, classified := classification[field]; !classified {
			t.Errorf("payload field %q has no encryption classification", field)
		}
		delete(classification, field)
	}
	if len(classification) != 0 {
		leftover := make([]string, 0, len(classification))
		for field := range classification {
			leftover = append(leftover, field)
		}
		sort.Strings(leftover)
		t.Fatalf("classification contains fields no longer present in logstore: %v", leftover)
	}
}

func TestEncryptedTeamBossOnlyWhenNoOwnerKey(t *testing.T) {
	bossPriv, bossInfo, _ := genPub(t)
	bossInfo.KID = "boss-v1"

	inner := &captureStore{}
	s := NewEncryptingLogStore(inner, &fakeResolver{policy: &Policy{
		TeamID: "team_1", UserID: "alice", Encrypt: true,
		BossPublicKey: bossInfo, OwnerPublicKey: nil,
	}}, nil)

	e := newEntry()
	_ = s.BatchCreateIfNotExists(context.Background(), []*logstore.Log{e})
	got := inner.batch[0]

	env := carrierEnvelope(t, got)
	if len(env.Recipients) != 1 || env.Recipients[0].Type != envelope.RecipientBoss {
		t.Fatalf("want boss-only recipient, got %+v", env.Recipients)
	}
	if _, err := envelope.Open(env, "boss-v1", bossPriv); err != nil {
		t.Fatalf("boss open: %v", err)
	}
}

func TestExemptTeamPassthrough(t *testing.T) {
	inner := &captureStore{}
	s := NewEncryptingLogStore(inner, &fakeResolver{policy: &Policy{
		TeamID: "team_1", Exempt: true, Encrypt: false,
	}}, nil)

	e := newEntry()
	_ = s.BatchCreateIfNotExists(context.Background(), []*logstore.Log{e})
	got := inner.batch[0]

	if got.InputHistory != `[{"role":"user","content":"secret input"}]` {
		t.Fatalf("exempt input must stay plaintext, got %q", got.InputHistory)
	}
	if got.ContentSummary != "secret input" {
		t.Fatalf("exempt summary must be preserved, got %q", got.ContentSummary)
	}
	if got.PayloadStoragePolicy != logstore.PayloadStorageDefault {
		t.Fatalf("exempt payload must keep the default storage policy, got %d", got.PayloadStoragePolicy)
	}
	if envelope.IsEnvelope([]byte(got.InputHistory)) {
		t.Fatal("exempt input must not be an envelope")
	}
}

func TestPolicyErrorFailsSecure(t *testing.T) {
	inner := &captureStore{}
	s := NewEncryptingLogStore(inner, &fakeResolver{err: errors.New("portal down")}, nil)

	e := newEntry()
	_ = s.BatchCreateIfNotExists(context.Background(), []*logstore.Log{e})
	got := inner.batch[0]

	env := carrierEnvelope(t, got)
	if env.Status != envelope.StatusNotRecorded || env.Reason != envelope.ReasonPolicyUnavailable {
		t.Fatalf("want not-recorded placeholder, got %+v", env)
	}
	if got.ContentSummary != summarySentinel {
		t.Fatalf("summary must be sentinel on fail-secure, got %q", got.ContentSummary)
	}
	if got.PayloadStoragePolicy != logstore.PayloadStorageObjectOnly {
		t.Fatalf("fail-secure payload must be object-only in hybrid mode, got %d", got.PayloadStoragePolicy)
	}
	// Absolutely no plaintext may survive anywhere.
	if wantAbsent := "secret"; contains(got.InputHistory, wantAbsent) || contains(got.OutputMessage, wantAbsent) ||
		contains(carrierContent(t, got), wantAbsent) {
		t.Fatalf("plaintext leaked into fail-secure entry: in=%q out=%q", got.InputHistory, got.OutputMessage)
	}
}

// TestResponsesFieldsEncrypted covers the OpenAI Responses API content fields:
// they are folded into the carrier bundle, not left in their own columns.
func TestResponsesFieldsEncrypted(t *testing.T) {
	ownerPriv, ownerInfo, _ := genPub(t)
	_, bossInfo, _ := genPub(t)
	bossInfo.KID = "boss-v1"
	ownerInfo.KID = "user-alice-v1"

	inner := &captureStore{}
	s := NewEncryptingLogStore(inner, &fakeResolver{policy: &Policy{
		TeamID: "team_1", UserID: "alice", Encrypt: true,
		BossPublicKey: bossInfo, OwnerPublicKey: ownerInfo,
	}}, nil)

	e := &logstore.Log{
		ID:                    "req_r",
		VirtualKeyID:          strp("vk_1"),
		TeamID:                strp("team_1"),
		ResponsesInputHistory: `[{"type":"message","role":"user","content":"responses secret in"}]`,
		ResponsesOutput:       `[{"type":"message","role":"assistant","content":"responses secret out"}]`,
	}
	_ = s.BatchCreateIfNotExists(context.Background(), []*logstore.Log{e})
	got := inner.batch[0]

	// Own columns cleared; content lives (encrypted) inside the carrier.
	if got.ResponsesInputHistory != "" || got.ResponsesOutput != "" {
		t.Fatalf("responses columns must be cleared, got in=%q out=%q", got.ResponsesInputHistory, got.ResponsesOutput)
	}
	if contains(carrierContent(t, got), "responses secret") {
		t.Fatal("plaintext leaked in carrier")
	}
	env := carrierEnvelope(t, got)
	bundle := openBundle(t, env, "user-alice-v1", ownerPriv)
	if !contains(string(bundle.ResponsesInputHistory), "responses secret in") ||
		!contains(string(bundle.ResponsesOutput), "responses secret out") {
		t.Fatalf("responses content missing from decrypted bundle: %+v", bundle)
	}
}

// TestNoPlaintextAfterSerializeFields is the regression test for the summary
// regeneration leak: it runs the real SerializeFields()/BuildContentSummary()
// and asserts no plaintext survives in any persisted column.
func TestNoPlaintextAfterSerializeFields(t *testing.T) {
	_, bossInfo, _ := genPub(t)
	bossInfo.KID = "boss-v1"

	inner := &captureStore{}
	s := NewEncryptingLogStore(inner, &fakeResolver{policy: &Policy{
		TeamID: "team_1", UserID: "alice", Encrypt: true, BossPublicKey: bossInfo,
	}}, nil)

	const secret = "TOPSECRETCONTENT"
	e := &logstore.Log{
		ID:           "req_s",
		VirtualKeyID: strp("vk_1"),
		TeamID:       strp("team_1"),
		InputHistoryParsed: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: strp(secret + "-in")},
		}},
		OutputMessageParsed: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: strp(secret + "-out")},
		},
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{{
			Role: ptrRole(schemas.ResponsesInputMessageRoleUser),
		}},
		ResponsesOutputParsed: []schemas.ResponsesMessage{{
			Role: ptrRole(schemas.ResponsesInputMessageRoleAssistant),
		}},
		RawRequest:  secret + "-rawreq",
		RawResponse: secret + "-rawresp",
	}
	if err := s.BatchCreateIfNotExists(context.Background(), []*logstore.Log{e}); err != nil {
		t.Fatal(err)
	}
	got := inner.batch[0]

	// Run the real serialization the DB layer performs before persistence.
	if err := got.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields: %v", err)
	}

	cols := map[string]string{
		"input_history":           got.InputHistory,
		"output_message":          got.OutputMessage,
		"responses_input_history": got.ResponsesInputHistory,
		"responses_output":        got.ResponsesOutput,
		"content_summary":         got.ContentSummary,
		"raw_request":             got.RawRequest,
		"raw_response":            got.RawResponse,
	}
	for name, val := range cols {
		if contains(val, secret) {
			t.Fatalf("plaintext leaked into column %s after SerializeFields: %q", name, val)
		}
	}
	if got.ContentSummary != summarySentinel {
		t.Fatalf("content_summary should be sentinel %q, got %q", summarySentinel, got.ContentSummary)
	}
	if got.RawRequest != "" || got.RawResponse != "" {
		t.Fatalf("raw fields must be blanked, got req=%q resp=%q", got.RawRequest, got.RawResponse)
	}
}

func TestMetadataUpdatePassesThroughUnchanged(t *testing.T) {
	inner := &captureUpdateStore{}
	store := NewEncryptingLogStore(inner, &fakeResolver{}, nil)
	updates := map[string]interface{}{
		"total_tokens": 42,
		"cost":         0.25,
		"has_object":   true,
	}

	if err := store.Update(context.Background(), "req_meta", updates); err != nil {
		t.Fatalf("metadata update: %v", err)
	}
	got, ok := inner.lastUpdate.(map[string]interface{})
	if !ok ||
		got["total_tokens"] != 42 ||
		got["cost"] != 0.25 ||
		got["has_object"] != true {
		t.Fatalf("metadata update was not passed through unchanged: %+v", inner.lastUpdate)
	}
}

func TestSerializedContentUpdateRejectedWithoutMutation(t *testing.T) {
	inner := &captureUpdateStore{}
	store := NewEncryptingLogStore(inner, &fakeResolver{}, nil)
	const plaintext = `{"role":"assistant","content":"exempt plaintext"}`
	updates := map[string]interface{}{
		"output_message":  plaintext,
		"content_summary": "exempt plaintext",
		"total_tokens":    42,
	}

	err := store.Update(context.Background(), "req_content", updates)
	if err == nil {
		t.Fatal("content-bearing update map must be rejected")
	}
	if inner.lastUpdate != nil {
		t.Fatal("rejected content update reached the inner store")
	}
	if updates["output_message"] != plaintext ||
		updates["content_summary"] != "exempt plaintext" ||
		updates["total_tokens"] != 42 {
		t.Fatalf("rejected update was mutated: %+v", updates)
	}
}

func mustUnmarshal(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("unmarshal %q: %v", s, err)
	}
}

// carrierContent extracts the single carrier message's content string from a
// decorated entry (InputHistoryParsed = one disguised message).
func carrierContent(t *testing.T, l *logstore.Log) string {
	t.Helper()
	if len(l.InputHistoryParsed) != 1 {
		t.Fatalf("expected exactly one carrier message, got %d", len(l.InputHistoryParsed))
	}
	c := l.InputHistoryParsed[0].Content
	if c == nil || c.ContentStr == nil {
		t.Fatalf("carrier message has no string content: %+v", l.InputHistoryParsed[0])
	}
	return *c.ContentStr
}

// carrierEnvelope extracts and parses the envelope carried inside the entry.
func carrierEnvelope(t *testing.T, l *logstore.Log) *envelope.Envelope {
	t.Helper()
	content := carrierContent(t, l)
	if !envelope.IsEnvelope([]byte(content)) {
		t.Fatalf("carrier content is not an envelope: %s", content)
	}
	var env envelope.Envelope
	mustUnmarshal(t, content, &env)
	return &env
}

// openBundle opens the envelope with the given recipient key and unpacks the bundle.
func openBundle(t *testing.T, env *envelope.Envelope, kid string, priv *rsa.PrivateKey) envelope.Bundle {
	t.Helper()
	pt, err := envelope.Open(env, kid, priv)
	if err != nil {
		t.Fatalf("open bundle (%s): %v", kid, err)
	}
	var b envelope.Bundle
	mustUnmarshal(t, string(pt), &b)
	return b
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestWrapFailsSecureWhenEnabledButMisconfigured proves the anti-fail-open fix:
// with BF_LOG_ENCRYPTION_ENABLED=true but no URL/secret, the wrap does NOT return
// the plaintext store — it returns a fail-secure decorator that writes only
// not-recorded placeholders (never plaintext).
func TestWrapFailsSecureWhenEnabledButMisconfigured(t *testing.T) {
	t.Setenv(envEnabled, "true")
	t.Setenv(envPortalBaseURL, "")
	t.Setenv(envInternalSecret, "")

	inner := &captureStore{}
	wrapped := WrapLogStoreFromEnv(inner, nil)
	if wrapped == logstore.LogStore(inner) {
		t.Fatal("misconfigured-enabled must NOT return the plaintext inner store")
	}
	e := newEntry()
	if err := wrapped.BatchCreateIfNotExists(context.Background(), []*logstore.Log{e}); err != nil {
		t.Fatal(err)
	}
	got := inner.batch[0]
	env := carrierEnvelope(t, got)
	if env.Status != envelope.StatusNotRecorded {
		t.Fatalf("want not-recorded placeholder on misconfig, got status %q", env.Status)
	}
	if contains(carrierContent(t, got), "secret") {
		t.Fatal("plaintext leaked despite misconfig fail-secure")
	}
}

// Disabling new encryption must preserve plaintext writes while retaining the
// billing policy for previously encrypted rows.
func TestWrapDisabledPreservesWrites(t *testing.T) {
	t.Setenv(envEnabled, "")
	inner := &captureStore{}
	entry := newEntry()
	if err := WrapLogStoreFromEnv(inner, nil).BatchCreateIfNotExists(context.Background(), []*logstore.Log{entry}); err != nil {
		t.Fatal(err)
	}
	if inner.batch[0].ContentSummary != "secret input" || inner.batch[0].InputHistory != entry.InputHistory {
		t.Fatal("disabled encryption changed the plaintext write")
	}
}

type billingCaptureStore struct{ logstore.LogStore }

func (s *billingCaptureStore) HydrateBillingChunk(_ context.Context, rows []*logstore.Log) (logstore.BillingHydrationResult, error) {
	return logstore.BillingHydrationResult{}, nil
}

func TestBillingRestoresHistoricalObjectOnlyPolicy(t *testing.T) {
	for _, enabled := range []string{"true", "false"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv(envEnabled, enabled)
			t.Setenv(envPortalBaseURL, "")
			t.Setenv(envInternalSecret, "")
			rows := []*logstore.Log{nil,
				{ContentSummary: summarySentinel, HasObject: true},
				{ContentSummary: "ordinary summary", HasObject: true},
				{ContentSummary: "[encrypted]", HasObject: true},
			}
			store := WrapLogStoreFromEnv(&billingCaptureStore{}, nil)
			if _, err := store.HydrateBillingChunk(context.Background(), rows); err != nil {
				t.Fatal(err)
			}
			if rows[1].PayloadStoragePolicy != logstore.PayloadStorageObjectOnly {
				t.Fatal("historical v2 policy lost")
			}
			for _, row := range rows[2:] {
				if row.PayloadStoragePolicy != logstore.PayloadStorageDefault {
					t.Fatal("ordinary or v1 storage policy changed")
				}
			}
		})
	}
}
