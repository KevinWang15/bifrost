package envelope

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
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

func TestOpenWithWrongKeyFails(t *testing.T) {
	boss := genKey(t)
	owner := genKey(t)
	other := genKey(t)

	env, err := Seal([]byte("secret"), FieldOutputMessage, testAAD(), []PublicRecipient{
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
	owner := genKey(t)
	env, err := Seal([]byte("hello"), FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientOwner, KID: "user-alice-v1", PublicKey: &owner.PublicKey},
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Tamper with AAD (swap the owner) — GCM open must fail.
	env.AAD.UserID = "mallory"
	if _, err := Open(env, "user-alice-v1", owner); err == nil {
		t.Fatal("expected GCM open failure after AAD tampering")
	}
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
	if _, err := Open(env, "any", genKey(t)); err == nil {
		t.Fatal("expected error opening a not-recorded placeholder")
	}
}

func TestIsEnvelope(t *testing.T) {
	owner := genKey(t)
	env, _ := Seal([]byte("x"), FieldInputHistory, testAAD(), []PublicRecipient{
		{Type: RecipientOwner, KID: "k", PublicKey: &owner.PublicKey},
	})
	encJSON := mustMarshal(t, env)

	cases := map[string]struct {
		raw  string
		want bool
	}{
		"encrypted envelope": {string(encJSON), true},
		"placeholder":        {string(mustMarshal(t, NotRecorded(FieldInputHistory, ReasonBossKeyMissing))), true},
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
