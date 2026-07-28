//go:build pge2e

package logencryption

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/postgresconn"
	"github.com/maximhq/bifrost/plugins/logencryption/envelope"
)

// TestBundleCarrierSurvivesDatabaseOnlyListPathPostgres covers the no-S3
// PostgreSQL mode. It exercises the real bifrost_safe_jsonb list truncation and
// is gated behind the `pge2e` build tag + PG_E2E_DSN-style env.
//
// Run: PGE2E=1 with a live PG at localhost:5432 (user e2e / pass e2e / db logs):
//
//	CGO_ENABLED=1 go test -tags pge2e -run TestBundleCarrierSurvivesDatabaseOnlyListPathPostgres -v ./
func TestBundleCarrierSurvivesDatabaseOnlyListPathPostgres(t *testing.T) {
	if os.Getenv("PGE2E") == "" {
		t.Skip("set PGE2E=1 with a live Postgres to run")
	}
	ctx := context.Background()
	pgCfg := postgresconn.Config{
		Host:     schemas.NewSecretVar("localhost"),
		Port:     schemas.NewSecretVar("5432"),
		User:     schemas.NewSecretVar("e2e"),
		Password: schemas.NewSecretVar("e2e"),
		DBName:   schemas.NewSecretVar("logs"),
		SSLMode:  schemas.NewSecretVar("disable"),
	}
	inner, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypePostgres,
		Config:  &logstore.PostgresConfig{Config: pgCfg},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("new pg log store: %v", err)
	}
	defer inner.Close(ctx)

	ownerPriv, ownerInfo := genTestPub(t)
	_, bossInfo := genTestPub(t)
	bossInfo.KID = "boss-v1"
	ownerInfo.KID = "user-alice-v1"
	store := NewEncryptingLogStore(inner, &fakeResolver{policy: &Policy{
		TeamID: "team_pg", UserID: "alice", Encrypt: true,
		BossPublicKey: bossInfo, OwnerPublicKey: ownerInfo,
	}}, bifrost.NewDefaultLogger(schemas.LogLevelError))

	id := "pgrow-" + time.Now().Format("150405.000")
	row := &logstore.Log{
		ID: id, Timestamp: time.Now().UTC(), Object: "chat.completion",
		Provider: "openai", Model: "gpt-x", Status: "success",
		InputHistoryParsed:  mustChatMsgs(t, `[{"role":"user","content":"turn one"},{"role":"assistant","content":"reply one"},{"role":"user","content":"turn two SECRET"}]`),
		OutputMessageParsed: mustChatMsg(t, `{"role":"assistant","content":"final answer SECRET"}`),
	}
	if err := store.BatchCreateIfNotExists(ctx, []*logstore.Log{row}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	res, err := store.SearchLogs(ctx, logstore.SearchFilters{}, logstore.PaginationOptions{Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var got *logstore.Log
	for i := range res.Logs {
		if res.Logs[i].ID == id {
			got = &res.Logs[i]
		}
	}
	if got == nil {
		t.Fatalf("row %s not found in list results", id)
	}
	if len(got.InputHistoryParsed) != 1 {
		t.Fatalf("carrier must be a single message after PG list path, got %d", len(got.InputHistoryParsed))
	}
	c := got.InputHistoryParsed[0].Content
	if c == nil || c.ContentStr == nil || !envelope.IsEnvelope([]byte(*c.ContentStr)) {
		t.Fatalf("carrier content is not an envelope after PG list path")
	}
	if contains(*c.ContentStr, "SECRET") || contains(got.OutputMessage, "SECRET") {
		t.Fatalf("plaintext SECRET leaked on PG list path")
	}
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
	if !contains(string(b.InputHistory), "turn two SECRET") || !contains(string(b.OutputMessage), "final answer SECRET") {
		t.Fatalf("decrypted PG bundle missing full conversation: in=%s out=%s", b.InputHistory, b.OutputMessage)
	}
	t.Logf("PG list path: carrier survived; decrypted full 3-turn input + output")
}
