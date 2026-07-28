package envelope

import (
	"bytes"
	"compress/gzip"
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
	"time"
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

// canonicalAAD produces the deterministic byte string bound into AES-GCM. The
// field order is alphabetical and is part of the cross-language wire contract.
// The TypeScript side must reproduce these exact bytes.
func canonicalAAD(a AAD, version int, contentEncoding string) ([]byte, error) {
	// Field order here defines the wire contract for AAD bytes. Do not reorder.
	ordered := struct {
		ContentEncoding string `json:"content_encoding"`
		Field           string `json:"field"`
		LogID           string `json:"log_id"`
		TeamID          string `json:"team_id"`
		UserID          string `json:"user_id"`
		Version         int    `json:"version"`
	}{
		ContentEncoding: contentEncoding,
		Field:           a.Field,
		LogID:           a.LogID,
		TeamID:          a.TeamID,
		UserID:          a.UserID,
		Version:         version,
	}
	return json.Marshal(ordered)
}

// compressGZIP deterministically compresses plaintext using the v2 wire
// contract. Explicit header values prevent timestamps or platform identifiers
// from making otherwise identical bundles produce different gzip streams.
func compressGZIP(plaintext []byte) ([]byte, error) {
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, GZIPLevel)
	if err != nil {
		return nil, err
	}
	writer.Header.ModTime = time.Unix(0, 0).UTC()
	writer.Header.OS = 255
	if _, err := writer.Write(plaintext); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

// decompressGZIP expands a gzip stream while enforcing maxBytes. The limit is
// checked while streaming so a small malicious ciphertext cannot allocate an
// unbounded decompressed payload.
func decompressGZIP(compressed []byte, maxBytes int64) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}

	plaintext, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("decompress gzip stream: %w", err)
	}
	if int64(len(plaintext)) > maxBytes {
		_ = reader.Close()
		return nil, fmt.Errorf("decompressed content exceeds %d bytes", maxBytes)
	}
	if err := reader.Close(); err != nil {
		return nil, fmt.Errorf("close gzip stream: %w", err)
	}
	return plaintext, nil
}

// Seal encrypts plaintext for the given recipients and returns an encrypted
// Envelope. Every non-empty plaintext is gzip-compressed before encryption. A
// fresh random DEK and GCM nonce are generated per call.
func Seal(plaintext []byte, field string, aad AAD, recipients []PublicRecipient) (*Envelope, error) {
	return seal(plaintext, field, aad, recipients, rand.Reader)
}

// seal implements Seal with an injectable randomness source for reproducible
// cross-language vectors. Production callers use Seal and crypto/rand.Reader.
func seal(plaintext []byte, field string, aad AAD, recipients []PublicRecipient, random io.Reader) (*Envelope, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("envelope: plaintext must not be empty")
	}
	if len(recipients) == 0 {
		return nil, errors.New("envelope: at least one recipient required")
	}
	if field == "" || aad.Field != field {
		return nil, errors.New("envelope: field must be non-empty and match aad field")
	}
	hasBoss := false
	recipientKIDs := make(map[string]struct{}, len(recipients))
	for _, recipient := range recipients {
		switch recipient.Type {
		case RecipientBoss:
			hasBoss = true
		case RecipientOwner:
		default:
			return nil, fmt.Errorf("envelope: unsupported recipient type %q", recipient.Type)
		}
		if recipient.KID == "" {
			return nil, errors.New("envelope: recipient kid must not be empty")
		}
		if _, duplicate := recipientKIDs[recipient.KID]; duplicate {
			return nil, fmt.Errorf("envelope: duplicate recipient kid %q", recipient.KID)
		}
		recipientKIDs[recipient.KID] = struct{}{}
		if recipient.PublicKey == nil {
			return nil, fmt.Errorf("envelope: nil public key for recipient %q", recipient.Type)
		}
	}
	if !hasBoss {
		return nil, errors.New("envelope: boss recipient is required")
	}

	compressed, err := compressGZIP(plaintext)
	if err != nil {
		return nil, fmt.Errorf("envelope: compress plaintext: %w", err)
	}

	dek := make([]byte, dekSize)
	if _, err := io.ReadFull(random, dek); err != nil {
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
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, fmt.Errorf("envelope: generate nonce: %w", err)
	}

	aadBytes, err := canonicalAAD(aad, Version, ContentEncodingGZIP)
	if err != nil {
		return nil, fmt.Errorf("envelope: marshal aad: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, compressed, aadBytes)

	wrapped := make([]Recipient, 0, len(recipients))
	for _, r := range recipients {
		wrappedDEK, err := rsa.EncryptOAEP(sha256.New(), random, r.PublicKey, dek, nil)
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
		ContentEncoding:   ContentEncodingGZIP,
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
		Marker:          true,
		Version:         Version,
		Status:          StatusNotRecorded,
		Field:           field,
		Reason:          reason,
		ContentEncoding: ContentEncodingGZIP,
	}
}

// Open decrypts an encrypted Envelope using privKey, matching by recipientKID.
// It is primarily used in tests and any Go-side decryption; the Portal browser
// performs the equivalent operation with WebCrypto.
func Open(env *Envelope, recipientKID string, privKey *rsa.PrivateKey) ([]byte, error) {
	if env == nil {
		return nil, errors.New("envelope: nil envelope")
	}
	if !env.Marker || env.Version != Version {
		return nil, fmt.Errorf("envelope: unsupported version %d", env.Version)
	}
	if env.ContentEncoding != ContentEncodingGZIP {
		return nil, fmt.Errorf("envelope: unsupported content encoding %q", env.ContentEncoding)
	}
	if env.Status != StatusEncrypted {
		return nil, fmt.Errorf("envelope: status %q is not decryptable", env.Status)
	}
	if env.ContentAlg != ContentAlgAES256GCM {
		return nil, fmt.Errorf("envelope: unsupported content algorithm %q", env.ContentAlg)
	}
	if env.AAD == nil {
		return nil, errors.New("envelope: aad is required")
	}
	if env.Field == "" || env.AAD.Field != env.Field {
		return nil, errors.New("envelope: field must be non-empty and match aad field")
	}
	if privKey == nil {
		return nil, errors.New("envelope: private key is required")
	}
	hasBoss := false
	recipientKIDs := make(map[string]struct{}, len(env.Recipients))
	for _, candidate := range env.Recipients {
		switch candidate.Type {
		case RecipientBoss:
			hasBoss = true
		case RecipientOwner:
		default:
			return nil, fmt.Errorf("envelope: unsupported recipient type %q", candidate.Type)
		}
		if candidate.KID == "" {
			return nil, errors.New("envelope: recipient kid must not be empty")
		}
		if _, duplicate := recipientKIDs[candidate.KID]; duplicate {
			return nil, fmt.Errorf("envelope: duplicate recipient kid %q", candidate.KID)
		}
		recipientKIDs[candidate.KID] = struct{}{}
		if candidate.Alg != RecipientAlgRSAOAEPSHA256 {
			return nil, fmt.Errorf("envelope: unsupported recipient algorithm %q", candidate.Alg)
		}
		wrappedDEK, err := unb64(candidate.WrappedDEK)
		if err != nil || len(wrappedDEK) == 0 {
			return nil, fmt.Errorf(
				"envelope: invalid wrapped dek for recipient %q",
				candidate.KID,
			)
		}
	}
	if !hasBoss {
		return nil, errors.New("envelope: boss recipient is required")
	}
	var recipient *Recipient
	for i := range env.Recipients {
		r := &env.Recipients[i]
		if r.KID == recipientKID {
			recipient = r
			break
		}
	}
	if recipient == nil {
		return nil, fmt.Errorf("envelope: no recipient with kid %q", recipientKID)
	}
	wrappedDEK, err := unb64(recipient.WrappedDEK)
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
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("envelope: nonce length %d, want %d", len(nonce), gcm.NonceSize())
	}
	aadBytes, err := canonicalAAD(*env.AAD, env.Version, env.ContentEncoding)
	if err != nil {
		return nil, fmt.Errorf("envelope: marshal aad: %w", err)
	}
	compressed, err := gcm.Open(nil, nonce, ciphertext, aadBytes)
	if err != nil {
		return nil, fmt.Errorf("envelope: gcm open: %w", err)
	}
	plaintext, err := decompressGZIP(compressed, MaxDecompressedBytes)
	if err != nil {
		return nil, fmt.Errorf("envelope: decompress plaintext: %w", err)
	}
	return plaintext, nil
}

// IsEnvelope reports whether a raw stored field value is an encryption envelope
// or placeholder (i.e. carries the marker), as opposed to plaintext messages.
func IsEnvelope(raw []byte) bool {
	var probe struct {
		Marker  bool `json:"__jq_log_encryption_v2"`
		Version int  `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Marker && probe.Version == Version
}
