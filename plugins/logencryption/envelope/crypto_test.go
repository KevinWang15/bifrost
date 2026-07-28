package envelope

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return k
}

func pubPEM(t *testing.T, k *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func testAAD() AAD {
	return AAD{LogID: "req_123", Field: FieldInputHistory, TeamID: "team_a", UserID: "alice"}
}

func TestSealOpenRoundTripBothRecipients(t *testing.T) {
	boss := genKey(t)
	owner := genKey(t)
	plaintext := []byte(`[{"role":"user","content":"summarize this contract"}]`)

	env, err := Seal(plaintext, FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-v1", PublicKey: &boss.PublicKey},
		{Type: RecipientOwner, KID: "user-alice-v1", PublicKey: &owner.PublicKey},
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !env.Marker || env.Version != Version || env.Status != StatusEncrypted {
		t.Fatalf("unexpected envelope header: %+v", env)
	}
	if env.ContentEncoding != ContentEncodingGZIP {
		t.Fatalf("content encoding = %q, want %q", env.ContentEncoding, ContentEncodingGZIP)
	}
	if len(env.Recipients) != 2 {
		t.Fatalf("want 2 recipients, got %d", len(env.Recipients))
	}

	// Owner can open with the owner kid.
	got, err := Open(env, "user-alice-v1", owner)
	if err != nil {
		t.Fatalf("owner open: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("owner plaintext mismatch: %q", got)
	}

	// Boss can open with the boss kid.
	got, err = Open(env, "boss-v1", boss)
	if err != nil {
		t.Fatalf("boss open: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("boss plaintext mismatch: %q", got)
	}
}

func TestSealUsesFreshDEKAndNonce(t *testing.T) {
	boss := genKey(t)
	recipients := []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-v2", PublicKey: &boss.PublicKey},
	}
	first, err := Seal([]byte("same plaintext"), FieldInputHistory, testAAD(), recipients)
	if err != nil {
		t.Fatalf("first seal: %v", err)
	}
	second, err := Seal([]byte("same plaintext"), FieldInputHistory, testAAD(), recipients)
	if err != nil {
		t.Fatalf("second seal: %v", err)
	}
	if first.ContentNonce == second.ContentNonce {
		t.Fatal("two envelopes reused the same GCM nonce")
	}
	if first.ContentCiphertext == second.ContentCiphertext {
		t.Fatal("two envelopes produced the same ciphertext")
	}
	if first.Recipients[0].WrappedDEK == second.Recipients[0].WrappedDEK {
		t.Fatal("two envelopes reused the same wrapped DEK")
	}
}

func TestSealRequiresBossRecipient(t *testing.T) {
	owner := genKey(t)
	_, err := Seal([]byte("hello"), FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientOwner, KID: "user-alice-v2", PublicKey: &owner.PublicKey},
	})
	if err == nil || !strings.Contains(err.Error(), "boss recipient is required") {
		t.Fatalf("owner-only seal error = %v, want boss requirement", err)
	}
}

func TestOpenRejectsEnvelopeWithoutBossRecipient(t *testing.T) {
	boss := genKey(t)
	owner := genKey(t)
	env, err := Seal([]byte("hello"), FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-v2", PublicKey: &boss.PublicKey},
		{Type: RecipientOwner, KID: "user-alice-v2", PublicKey: &owner.PublicKey},
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	env.Recipients = env.Recipients[1:]
	if _, err := Open(env, "user-alice-v2", owner); err == nil ||
		!strings.Contains(err.Error(), "boss recipient is required") {
		t.Fatalf("owner-only open error = %v, want boss requirement", err)
	}
}

func TestOpenRejectsEnvelopeWithUnusableBossRecipient(t *testing.T) {
	boss := genKey(t)
	owner := genKey(t)
	env, err := Seal([]byte("hello"), FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-v2", PublicKey: &boss.PublicKey},
		{Type: RecipientOwner, KID: "user-alice-v2", PublicKey: &owner.PublicKey},
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	env.Recipients[0].WrappedDEK = ""
	if _, err := Open(env, "user-alice-v2", owner); err == nil ||
		!strings.Contains(err.Error(), "invalid wrapped dek") {
		t.Fatalf("unusable boss open error = %v, want wrapped dek rejection", err)
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	boss := genKey(t)
	owner := genKey(t)
	other := genKey(t)
	aad := testAAD()
	aad.Field = FieldOutputMessage

	env, err := Seal([]byte("secret"), FieldOutputMessage, aad, []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-v1", PublicKey: &boss.PublicKey},
		{Type: RecipientOwner, KID: "user-alice-v1", PublicKey: &owner.PublicKey},
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Correct kid but wrong private key must fail to unwrap.
	if _, err := Open(env, "boss-v1", other); err == nil {
		t.Fatal("expected failure opening with wrong private key")
	}
	// Unknown kid must fail.
	if _, err := Open(env, "does-not-exist", owner); err == nil {
		t.Fatal("expected failure opening with unknown kid")
	}
}

func TestAADTamperingRejected(t *testing.T) {
	boss := genKey(t)
	env, err := Seal([]byte("hello"), FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-v2", PublicKey: &boss.PublicKey},
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Tamper with AAD (swap the owner) — GCM open must fail.
	env.AAD.UserID = "mallory"
	if _, err := Open(env, "boss-v2", boss); err == nil {
		t.Fatal("expected GCM open failure after AAD tampering")
	}
}

func TestVersionAndEncodingRejected(t *testing.T) {
	boss := genKey(t)
	env, err := Seal([]byte("hello"), FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-v2", PublicKey: &boss.PublicKey},
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	env.Version = 1
	if _, err := Open(env, "boss-v2", boss); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("version 1 error = %v, want unsupported version", err)
	}
	env.Version = Version
	env.ContentEncoding = "identity"
	if _, err := Open(env, "boss-v2", boss); err == nil || !strings.Contains(err.Error(), "unsupported content encoding") {
		t.Fatalf("identity encoding error = %v, want unsupported content encoding", err)
	}
}

func TestAuthenticationPrecedesDecompression(t *testing.T) {
	boss := genKey(t)
	env, err := Seal([]byte(strings.Repeat("authenticated content ", 100)), FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-v2", PublicKey: &boss.PublicKey},
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.ContentCiphertext)
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	ciphertext[len(ciphertext)-1] ^= 0xff
	env.ContentCiphertext = base64.StdEncoding.EncodeToString(ciphertext)

	if _, err := Open(env, "boss-v2", boss); err == nil || !strings.Contains(err.Error(), "gcm open") {
		t.Fatalf("tampered ciphertext error = %v, want gcm authentication failure", err)
	}
}

func TestGZIPDeterministicAndBounded(t *testing.T) {
	plaintext := []byte(strings.Repeat("repeatable audit content ", 100))
	first, err := compressGZIP(plaintext)
	if err != nil {
		t.Fatalf("first compress: %v", err)
	}
	second, err := compressGZIP(plaintext)
	if err != nil {
		t.Fatalf("second compress: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("gzip output is not deterministic")
	}
	got, err := decompressGZIP(first, int64(len(plaintext)))
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("gzip round trip changed plaintext")
	}
	if _, err := decompressGZIP(first, int64(len(plaintext)-1)); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("bounded decompress error = %v, want size-limit failure", err)
	}
}

func TestCompressedEnvelopeStorageSize(t *testing.T) {
	boss := genKey(t)
	cases := []struct {
		name             string
		content          string
		maxCompressedPct int
		maxEnvelopePct   int
	}{
		{
			name:             "representative repetitive bundle",
			content:          strings.Repeat("tool result and conversation history ", 4096),
			maxCompressedPct: 10,
			maxEnvelopePct:   15,
		},
		{
			name:             "incompressible bundle",
			content:          base64.StdEncoding.EncodeToString(deterministicNoise(128 << 10)),
			maxCompressedPct: 105,
			maxEnvelopePct:   150,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plaintext, err := json.Marshal(Bundle{
				InputHistory: json.RawMessage(`[{"role":"user","content":` +
					string(mustMarshal(t, tc.content)) + `}]`),
			})
			if err != nil {
				t.Fatalf("marshal bundle: %v", err)
			}
			compressed, err := compressGZIP(plaintext)
			if err != nil {
				t.Fatalf("compress: %v", err)
			}
			if got := len(compressed) * 100 / len(plaintext); got > tc.maxCompressedPct {
				t.Fatalf("compressed size = %d%% of plaintext, want <= %d%%", got, tc.maxCompressedPct)
			}

			env, err := Seal(plaintext, FieldBundle, AAD{
				LogID: "req_size", Field: FieldBundle, TeamID: "team_size", UserID: "user_size",
			}, []PublicRecipient{
				{Type: RecipientBoss, KID: "boss-size-v2", PublicKey: &boss.PublicKey},
			})
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			stored, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}
			if got := len(stored) * 100 / len(plaintext); got > tc.maxEnvelopePct {
				t.Fatalf("envelope size = %d%% of plaintext, want <= %d%%", got, tc.maxEnvelopePct)
			}
		})
	}
}

func deterministicNoise(size int) []byte {
	result := make([]byte, 0, size)
	var counter uint64
	for len(result) < size {
		var input [8]byte
		binary.BigEndian.PutUint64(input[:], counter)
		block := sha256.Sum256(input[:])
		result = append(result, block[:]...)
		counter++
	}
	return result[:size]
}

func TestParseRSAPublicKeyPEMRoundTrip(t *testing.T) {
	k := genKey(t)
	parsed, err := ParseRSAPublicKeyPEM(pubPEM(t, k))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.N.Cmp(k.PublicKey.N) != 0 || parsed.E != k.PublicKey.E {
		t.Fatal("parsed public key does not match original")
	}
}

func TestNotRecordedPlaceholder(t *testing.T) {
	env := NotRecorded(FieldInputHistory, ReasonPolicyUnavailable)
	if !env.Marker || env.Status != StatusNotRecorded || env.Reason != ReasonPolicyUnavailable {
		t.Fatalf("unexpected placeholder: %+v", env)
	}
	if env.Version != Version || env.ContentEncoding != ContentEncodingGZIP {
		t.Fatalf("unexpected placeholder wire metadata: %+v", env)
	}
	if _, err := Open(env, "any", genKey(t)); err == nil {
		t.Fatal("expected error opening a not-recorded placeholder")
	}
}

func TestIsEnvelope(t *testing.T) {
	boss := genKey(t)
	env, _ := Seal([]byte("x"), FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientBoss, KID: "boss-v2", PublicKey: &boss.PublicKey},
	})
	encJSON := mustMarshal(t, env)

	cases := map[string]struct {
		raw  string
		want bool
	}{
		"encrypted envelope": {string(encJSON), true},
		"placeholder":        {string(mustMarshal(t, NotRecorded(FieldInputHistory, ReasonBossKeyMissing))), true},
		"v1 envelope":        {`{"__jq_log_encryption_v1":true,"version":1}`, false},
		"wrong v2 version":   {`{"__jq_log_encryption_v2":true,"version":1}`, false},
		"plaintext messages": {`[{"role":"user","content":"hi"}]`, false},
		"plaintext object":   {`{"role":"assistant","content":"hi"}`, false},
		"garbage":            {`not json`, false},
	}
	for name, c := range cases {
		if got := IsEnvelope([]byte(c.raw)); got != c.want {
			t.Errorf("%s: IsEnvelope=%v want %v", name, got, c.want)
		}
	}
}
