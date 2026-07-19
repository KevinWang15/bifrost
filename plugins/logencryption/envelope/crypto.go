package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
)

// dekSize is the AES-256 key length in bytes.
const dekSize = 32

// PublicRecipient couples a public key with the identity used to wrap a DEK for it.
type PublicRecipient struct {
	Type      string // RecipientBoss | RecipientOwner
	KID       string
	PublicKey *rsa.PublicKey
}

// b64 encodes with standard base64 (matches WebCrypto btoa on raw bytes decoded
// via atob on the browser side).
func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// ParseRSAPublicKeyPEM parses a PEM-encoded RSA public key. It accepts both
// PKIX ("BEGIN PUBLIC KEY") and PKCS1 ("BEGIN RSA PUBLIC KEY") encodings.
func ParseRSAPublicKeyPEM(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("envelope: no PEM block found in public key")
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("envelope: PKIX key is %T, not RSA", key)
		}
		return rsaKey, nil
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("envelope: unsupported RSA public key PEM encoding")
}

// canonicalAAD produces the deterministic byte string bound into AES-GCM. Both
// producer and consumer must derive AAD identically; we use compact JSON with
// sorted keys (Go's json.Marshal on a struct emits fields in declaration order,
// which is stable). The TypeScript side must reproduce these exact bytes.
func canonicalAAD(a AAD) ([]byte, error) {
	// Field order here defines the wire contract for AAD bytes. Do not reorder.
	ordered := struct {
		Field  string `json:"field"`
		LogID  string `json:"log_id"`
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
	}{Field: a.Field, LogID: a.LogID, TeamID: a.TeamID, UserID: a.UserID}
	return json.Marshal(ordered)
}

// Seal encrypts plaintext for the given recipients and returns an encrypted
// Envelope. A fresh random DEK and GCM nonce are generated per call. At least one
// recipient is required.
func Seal(plaintext []byte, field string, aad AAD, recipients []PublicRecipient) (*Envelope, error) {
	if len(recipients) == 0 {
		return nil, errors.New("envelope: at least one recipient required")
	}

	dek := make([]byte, dekSize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("envelope: generate DEK: %w", err)
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("envelope: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("envelope: new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("envelope: generate nonce: %w", err)
	}

	aadBytes, err := canonicalAAD(aad)
	if err != nil {
		return nil, fmt.Errorf("envelope: marshal aad: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, aadBytes)

	wrapped := make([]Recipient, 0, len(recipients))
	for _, r := range recipients {
		if r.PublicKey == nil {
			return nil, fmt.Errorf("envelope: nil public key for recipient %q", r.Type)
		}
		wrappedDEK, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, r.PublicKey, dek, nil)
		if err != nil {
			return nil, fmt.Errorf("envelope: wrap DEK for %q: %w", r.Type, err)
		}
		wrapped = append(wrapped, Recipient{
			Type:       r.Type,
			KID:        r.KID,
			Alg:        RecipientAlgRSAOAEPSHA256,
			WrappedDEK: b64(wrappedDEK),
		})
	}

	aadCopy := aad
	return &Envelope{
		Marker:            true,
		Version:           Version,
		Status:            StatusEncrypted,
		Field:             field,
		ContentAlg:        ContentAlgAES256GCM,
		ContentNonce:      b64(nonce),
		ContentCiphertext: b64(ciphertext),
		AAD:               &aadCopy,
		Recipients:        wrapped,
	}, nil
}

// NotRecorded builds a fail-secure placeholder envelope with no plaintext or
// ciphertext, used when encryption policy could not be resolved.
func NotRecorded(field, reason string) *Envelope {
	return &Envelope{
		Marker:  true,
		Version: Version,
		Status:  StatusNotRecorded,
		Field:   field,
		Reason:  reason,
	}
}

// Open decrypts an encrypted Envelope using privKey, matching by recipientKID.
// It is primarily used in tests and any Go-side decryption; the Portal browser
// performs the equivalent operation with WebCrypto.
func Open(env *Envelope, recipientKID string, privKey *rsa.PrivateKey) ([]byte, error) {
	if env == nil {
		return nil, errors.New("envelope: nil envelope")
	}
	if env.Status != StatusEncrypted {
		return nil, fmt.Errorf("envelope: status %q is not decryptable", env.Status)
	}
	var wrapped string
	found := false
	for _, r := range env.Recipients {
		if r.KID == recipientKID {
			wrapped = r.WrappedDEK
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("envelope: no recipient with kid %q", recipientKID)
	}
	wrappedDEK, err := unb64(wrapped)
	if err != nil {
		return nil, fmt.Errorf("envelope: decode wrapped dek: %w", err)
	}
	dek, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privKey, wrappedDEK, nil)
	if err != nil {
		return nil, fmt.Errorf("envelope: unwrap dek: %w", err)
	}
	nonce, err := unb64(env.ContentNonce)
	if err != nil {
		return nil, fmt.Errorf("envelope: decode nonce: %w", err)
	}
	ciphertext, err := unb64(env.ContentCiphertext)
	if err != nil {
		return nil, fmt.Errorf("envelope: decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("envelope: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("envelope: new gcm: %w", err)
	}
	var aadBytes []byte
	if env.AAD != nil {
		if aadBytes, err = canonicalAAD(*env.AAD); err != nil {
			return nil, fmt.Errorf("envelope: marshal aad: %w", err)
		}
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aadBytes)
	if err != nil {
		return nil, fmt.Errorf("envelope: gcm open: %w", err)
	}
	return plaintext, nil
}

// IsEnvelope reports whether a raw stored field value is an encryption envelope
// or placeholder (i.e. carries the marker), as opposed to plaintext messages.
func IsEnvelope(raw []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	_, ok := probe[Marker]
	return ok
}
