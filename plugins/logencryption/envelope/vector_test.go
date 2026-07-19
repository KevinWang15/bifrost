package envelope

import (
	"crypto/rand"
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
	owner, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	// Seal a whole-conversation bundle (the current format), then carry it as the
	// content of a single disguised message — exactly what the decorator writes.
	bundle := Bundle{
		InputHistory:  json.RawMessage(`[{"role":"user","content":"cross-language vector"}]`),
		OutputMessage: json.RawMessage(`{"role":"assistant","content":"vector reply"}`),
	}
	plaintext, _ := json.Marshal(&bundle)
	aad := AAD{LogID: "req_vector", Field: FieldBundle, TeamID: "team_v", UserID: "vectoruser"}
	env, err := Seal(plaintext, FieldBundle, aad, []PublicRecipient{
		{Type: RecipientOwner, KID: "user-vector-v1", PublicKey: &owner.PublicKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(owner)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	envJSON, _ := json.Marshal(env)
	// The carrier is what the browser actually receives as `input_history`.
	carrier := []map[string]any{{"role": "user", "content": string(envJSON)}}

	vector := map[string]any{
		"expected_plaintext": string(plaintext),
		"recipient_kid":      "user-vector-v1",
		"private_key_pkcs8":  string(privPEM),
		"envelope":           json.RawMessage(envJSON),
		"carrier":            carrier,
	}
	out, _ := json.MarshalIndent(vector, "", "  ")
	if err := os.WriteFile("testdata/cross_lang_vector.json", out, 0o644); err != nil {
		t.Fatal(err)
	}
}
