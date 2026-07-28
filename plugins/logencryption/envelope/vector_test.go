package envelope

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"testing"
)

// TestGenerateCrossLangVector writes a fixed test vector to disk so the Portal
// WebCrypto/TypeScript decryptor can be validated against a real Go-produced
// envelope. Run with: go test -run TestGenerateCrossLangVector -tags vectors
//
// It is gated behind the WRITE_VECTORS env var so normal `go test` runs do not
// touch the filesystem or require determinism.
func TestGenerateCrossLangVector(t *testing.T) {
	if os.Getenv("WRITE_VECTORS") == "" {
		t.Skip("set WRITE_VECTORS=1 to (re)generate cross-language test vectors")
	}
	const vectorPath = "testdata/cross_lang_vector.json"
	current, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatalf("read existing vector key: %v", err)
	}
	var fixed struct {
		PrivateKeyPKCS8 string `json:"private_key_pkcs8"`
	}
	if err := json.Unmarshal(current, &fixed); err != nil {
		t.Fatalf("parse existing vector key: %v", err)
	}
	block, _ := pem.Decode([]byte(fixed.PrivateKeyPKCS8))
	if block == nil {
		t.Fatal("existing vector has no private-key PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse existing vector private key: %v", err)
	}
	owner, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("existing vector key is %T, not RSA", parsed)
	}
	// Seal a whole-conversation bundle (the current format), then carry it as the
	// content of a single disguised message — exactly what the decorator writes.
	bundle := Bundle{
		InputHistory:  json.RawMessage(`[{"role":"user","content":"cross-language vector"}]`),
		OutputMessage: json.RawMessage(`{"role":"assistant","content":"vector reply"}`),
	}
	plaintext, _ := json.Marshal(&bundle)
	aad := AAD{LogID: "req_vector", Field: FieldBundle, TeamID: "team_v", UserID: "vectoruser"}
	random := bytes.NewReader(bytes.Repeat([]byte("jq-bifrost-log-envelope-v2-vector"), 64))
	env, err := seal(plaintext, FieldBundle, aad, []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-vector-v2", PublicKey: &owner.PublicKey},
		{Type: RecipientOwner, KID: "user-vector-v2", PublicKey: &owner.PublicKey},
	}, random)
	if err != nil {
		t.Fatal(err)
	}
	envJSON, _ := json.Marshal(env)
	// The carrier is what the browser actually receives as `input_history`.
	carrier := []map[string]any{{"role": "user", "content": string(envJSON)}}

	vector := map[string]any{
		"expected_plaintext": string(plaintext),
		"recipient_kid":      "user-vector-v2",
		"private_key_pkcs8":  fixed.PrivateKeyPKCS8,
		"envelope":           json.RawMessage(envJSON),
		"carrier":            carrier,
	}
	out, _ := json.MarshalIndent(vector, "", "  ")
	if err := os.WriteFile(vectorPath, out, 0o644); err != nil {
		t.Fatal(err)
	}
}
