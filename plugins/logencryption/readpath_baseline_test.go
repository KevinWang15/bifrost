package logencryption

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logencryption/envelope"
)

func mustChatMsgs(t *testing.T, raw string) []schemas.ChatMessage {
	t.Helper()
	var m []schemas.ChatMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal chat msgs: %v", err)
	}
	return m
}

// TestReadPathBaseline is a diagnostic/regression test for database-only
// storage. It proves what Bifrost's real list path returns for plaintext and
// envelope-shaped fields without object-storage hydration.
func TestReadPathBaseline(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "logs.db")
	// newSqliteLogStore requires the file to exist.
	if f, err := os.Create(dbPath); err != nil {
		t.Fatalf("create db: %v", err)
	} else {
		_ = f.Close()
	}

	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: dbPath},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("new log store: %v", err)
	}
	defer store.Close(ctx)

	now := time.Now().UTC()

	// Row 1: plaintext chat log (baseline — what encryption OFF looks like).
	plain := &logstore.Log{
		ID:                 "plain1",
		Timestamp:          now,
		Object:             "chat.completion",
		Provider:           "openai",
		Model:              "gpt-x",
		Status:             "success",
		InputHistoryParsed: mustChatMsgs(t, `[{"role":"user","content":"hello plaintext"}]`),
		OutputMessage:      `{"role":"assistant","content":"hi there plaintext"}`,
	}
	// Row 2: envelope row (what our decorator writes for encrypted teams).
	env := &logstore.Log{
		ID:            "enc1",
		Timestamp:     now.Add(time.Second),
		Object:        "chat.completion",
		Provider:      "openai",
		Model:         "gpt-x",
		Status:        "success",
		InputHistory:  `{"__jq_log_encryption_v2":true,"version":2,"status":"encrypted","field":"input_history","content_encoding":"gzip","content_ciphertext":"Y2lwaGVy"}`,
		OutputMessage: `{"__jq_log_encryption_v2":true,"version":2,"status":"encrypted","field":"output_message","content_encoding":"gzip","content_ciphertext":"Y2lwaGVy"}`,
	}
	if err := store.BatchCreateIfNotExists(ctx, []*logstore.Log{plain, env}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	res, err := store.SearchLogs(ctx, logstore.SearchFilters{}, logstore.PaginationOptions{Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	got := map[string]*logstore.Log{}
	for i := range res.Logs {
		got[res.Logs[i].ID] = &res.Logs[i]
	}

	// Report exactly what the list path yields. These t.Logf lines are the
	// diagnostic output; the assertions capture the behavior for regression.
	p := got["plain1"]
	e := got["enc1"]
	if p == nil || e == nil {
		t.Fatalf("missing rows: plain=%v enc=%v", p != nil, e != nil)
	}

	t.Logf("PLAINTEXT row via list: InputHistoryParsed=%d msgs, OutputMessage=%q, OutputMessageParsed=%v",
		len(p.InputHistoryParsed), p.OutputMessage, p.OutputMessageParsed)
	t.Logf("ENVELOPE row via list: InputHistory raw=%q, InputHistoryParsed=%d msgs, OutputMessage=%q, OutputMessageParsed=%v",
		e.InputHistory, len(e.InputHistoryParsed), e.OutputMessage, e.OutputMessageParsed)

	// BASELINE FACT 1: list path NULLs output_message for non-realtime rows,
	// so even the plaintext row returns no assistant output.
	if p.OutputMessage != "" || p.OutputMessageParsed != nil {
		t.Errorf("expected list path to drop output for plaintext row, got OutputMessage=%q parsed=%v",
			p.OutputMessage, p.OutputMessageParsed)
	}

	// BASELINE FACT 2: the envelope's input_history is destroyed by
	// DeserializeFields (object cannot unmarshal into []ChatMessage -> emptied).
	if len(e.InputHistoryParsed) != 0 {
		t.Logf("NOTE: envelope InputHistoryParsed survived as %d msgs", len(e.InputHistoryParsed))
	}
}

// TestDisguisedEnvelopeSurvivesReadPath validates the database-only detail
// path: an envelope carried inside a well-formed ChatMessage survives
// DeserializeFields intact.
func TestDisguisedEnvelopeSurvivesReadPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "logs.db")
	if f, err := os.Create(dbPath); err != nil {
		t.Fatalf("create db: %v", err)
	} else {
		_ = f.Close()
	}
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: dbPath},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("new log store: %v", err)
	}
	defer store.Close(ctx)

	// Disguise: envelope JSON carried as the string content of a single message.
	envJSON := `{"__jq_log_encryption_v2":true,"version":2,"status":"encrypted","field":"input_history","content_encoding":"gzip","content_ciphertext":"Y2lwaGVy"}`
	inputDisguised := mustChatMsgs(t, `[{"role":"user","content":`+jsonString(envJSON)+`}]`)
	outputDisguised := mustChatMsg(t, `{"role":"assistant","content":`+jsonString(envJSON)+`}`)

	row := &logstore.Log{
		ID: "disg1", Timestamp: time.Now().UTC(), Object: "chat.completion",
		Provider: "openai", Model: "gpt-x", Status: "success",
		InputHistoryParsed:  inputDisguised,
		OutputMessageParsed: outputDisguised,
	}
	if err := store.BatchCreateIfNotExists(ctx, []*logstore.Log{row}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// DETAIL path (what route 甲 has Portal call): FindByID, no list truncation.
	got, err := store.FindByID(ctx, "disg1")
	if err != nil {
		t.Fatalf("findByID: %v", err)
	}

	// Input: disguised envelope must survive as 1 message with the envelope string.
	if len(got.InputHistoryParsed) != 1 {
		t.Fatalf("input disguise lost: got %d msgs", len(got.InputHistoryParsed))
	}
	inContent := got.InputHistoryParsed[0].Content
	if inContent == nil || inContent.ContentStr == nil || *inContent.ContentStr != envJSON {
		t.Fatalf("input envelope content mangled: %+v", inContent)
	}
	// Output: *ChatMessage must survive with the envelope string intact.
	if got.OutputMessageParsed == nil || got.OutputMessageParsed.Content == nil ||
		got.OutputMessageParsed.Content.ContentStr == nil ||
		*got.OutputMessageParsed.Content.ContentStr != envJSON {
		t.Fatalf("output envelope content lost/mangled: %+v", got.OutputMessageParsed)
	}
	t.Logf("DETAIL path: input+output disguised envelopes BOTH survived intact")
}

// jsonString returns s as a JSON-quoted string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func mustChatMsg(t *testing.T, raw string) *schemas.ChatMessage {
	t.Helper()
	var m schemas.ChatMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal chat msg: %v", err)
	}
	return &m
}

// TestBundleCarrierSurvivesDatabaseOnlyListPath covers the no-S3 storage mode.
// PayloadStorageObjectOnly is intentionally ignored by a database-only store,
// so the encrypted carrier remains the authoritative DB payload.
func TestBundleCarrierSurvivesDatabaseOnlyListPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "logs.db")
	if f, err := os.Create(dbPath); err != nil {
		t.Fatalf("create db: %v", err)
	} else {
		_ = f.Close()
	}
	ctx := context.Background()
	inner, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: dbPath},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("new log store: %v", err)
	}
	defer inner.Close(ctx)

	// Real decorator with a boss+owner policy.
	ownerPriv, ownerInfo := genTestPub(t)
	_, bossInfo := genTestPub(t)
	bossInfo.KID = "boss-v1"
	ownerInfo.KID = "user-alice-v1"
	store := NewEncryptingLogStore(inner, &fakeResolver{policy: &Policy{
		TeamID: "team_1", UserID: "alice", Encrypt: true,
		BossPublicKey: bossInfo, OwnerPublicKey: ownerInfo,
	}}, bifrost.NewDefaultLogger(schemas.LogLevelError))

	// A MULTI-TURN conversation + an assistant output — the hard case.
	row := &logstore.Log{
		ID: "listrow", Timestamp: time.Now().UTC(), Object: "chat.completion",
		Provider: "openai", Model: "gpt-x", Status: "success",
		InputHistoryParsed:  mustChatMsgs(t, `[{"role":"user","content":"turn one"},{"role":"assistant","content":"reply one"},{"role":"user","content":"turn two SECRET"}]`),
		OutputMessageParsed: mustChatMsg(t, `{"role":"assistant","content":"final answer SECRET"}`),
	}
	if err := store.BatchCreateIfNotExists(ctx, []*logstore.Log{row}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Read back via the LIST path (what Portal uses).
	res, err := store.SearchLogs(ctx, logstore.SearchFilters{}, logstore.PaginationOptions{Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Logs) != 1 {
		t.Fatalf("want 1 row, got %d", len(res.Logs))
	}
	got := &res.Logs[0]

	// Carrier reduced multi-turn to EXACTLY ONE surviving message (guards against
	// an accidental multi-element array that list truncation would cut to last only).
	if len(got.InputHistoryParsed) != 1 {
		t.Fatalf("carrier must be a single message after list path, got %d", len(got.InputHistoryParsed))
	}
	c := got.InputHistoryParsed[0].Content
	if c == nil || c.ContentStr == nil {
		t.Fatalf("carrier lost its content string: %+v", got.InputHistoryParsed[0])
	}
	if !envelope.IsEnvelope([]byte(*c.ContentStr)) {
		t.Fatalf("carrier content is not an envelope after list path: %s", *c.ContentStr)
	}
	// No plaintext anywhere on the list-path row.
	if contains(*c.ContentStr, "SECRET") || contains(got.OutputMessage, "SECRET") {
		t.Fatalf("plaintext SECRET leaked on list path")
	}

	// Owner decrypts the carrier → full input (all 3 turns) + output.
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(*c.ContentStr), &env); err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	pt, err := envelope.Open(&env, "user-alice-v1", ownerPriv)
	if err != nil {
		t.Fatalf("owner open: %v", err)
	}
	var b envelope.Bundle
	if err := json.Unmarshal(pt, &b); err != nil {
		t.Fatalf("unpack bundle: %v", err)
	}
	if !contains(string(b.InputHistory), "turn one") || !contains(string(b.InputHistory), "turn two SECRET") {
		t.Fatalf("decrypted bundle missing full multi-turn input: %s", b.InputHistory)
	}
	if !contains(string(b.OutputMessage), "final answer SECRET") {
		t.Fatalf("decrypted bundle missing output: %s", b.OutputMessage)
	}
}

// genTestPub generates an RSA keypair and a PublicKeyInfo for it (test helper
// local to this file; store_test.go has its own genPub with a *testing.T API).
func genTestPub(t *testing.T) (*rsa.PrivateKey, *PublicKeyInfo) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return k, &PublicKeyInfo{KID: "kid", Alg: envelope.RecipientAlgRSAOAEPSHA256, PublicKeyPEM: pemStr}
}
