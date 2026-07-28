// Package envelope defines the versioned encryption-envelope format used to
// store LLM conversation content at rest.
//
// The same JSON shape is produced by Bifrost (Go) and consumed by the Portal
// browser (WebCrypto/TypeScript). Keep this file and the Portal TypeScript
// definition in lockstep. Version 2 intentionally does not decode version 1;
// retained v1 payloads are treated as legacy-unreadable by display clients.
//
// Cryptographic scheme (v2):
//   - content: deterministic gzip level 6, then AES-256-GCM with a random
//     per-envelope 32-byte data encryption key (DEK)
//   - DEK wrapping: RSA-OAEP-SHA256 to each recipient public key (boss + owner)
//   - AAD binds ciphertext to encoding / field / log id / team / user / version
package envelope

import "encoding/json"

// Marker is the JSON key whose presence identifies a field value as an encryption
// envelope (or placeholder) rather than plaintext messages. Readers MUST check for
// this key before attempting to parse the field as a message array/object.
const Marker = "__jq_log_encryption_v2"

// Version is the current envelope schema version.
const Version = 2

// GZIPLevel is the compression level used for every non-empty bundle.
const GZIPLevel = 6

// MaxDecompressedBytes matches Bifrost's default maximum request-body contract.
// Readers must enforce the same limit before allocating the complete plaintext.
const MaxDecompressedBytes int64 = 100 << 20

// Algorithm identifiers. Kept as string constants so the format is self-describing
// and future algorithms can be added without breaking old rows.
const (
	ContentAlgAES256GCM       = "AES-256-GCM"
	ContentEncodingGZIP       = "gzip"
	RecipientAlgRSAOAEPSHA256 = "RSA-OAEP-SHA256"
)

// Status values for a field envelope.
const (
	// StatusEncrypted means content_ciphertext holds the encrypted field and
	// recipients hold the wrapped DEKs.
	StatusEncrypted = "encrypted"
	// StatusNotRecorded is the fail-secure placeholder written when encryption
	// policy could not be resolved. No plaintext and no ciphertext is stored.
	StatusNotRecorded = "content_not_recorded"
)

// Reasons for StatusNotRecorded placeholders.
const (
	ReasonPolicyUnavailable = "policy_unavailable"
	ReasonBossKeyMissing    = "boss_key_missing"
)

// Field names carried inside the envelope (also used in AAD).
const (
	FieldInputHistory          = "input_history"
	FieldOutputMessage         = "output_message"
	FieldResponsesInputHistory = "responses_input_history"
	FieldResponsesOutput       = "responses_output"
	// FieldBundle is the AAD field name for a whole-conversation bundle: all
	// content fields are sealed into ONE envelope carried inside a single
	// disguised message in input_history, so the envelope survives Bifrost's
	// list read path (which NULLs output_message and returns only the last
	// input_history element).
	FieldBundle = "bundle"
)

// Bundle is the plaintext payload sealed for a whole-conversation envelope. Each
// field mirrors a Bifrost Log content column and is included only when present.
// The raw JSON of each original field is preserved verbatim so the browser can
// restore the exact structure the renderer expects.
type Bundle struct {
	InputHistory          json.RawMessage `json:"input_history,omitempty"`
	OutputMessage         json.RawMessage `json:"output_message,omitempty"`
	ResponsesInputHistory json.RawMessage `json:"responses_input_history,omitempty"`
	ResponsesOutput       json.RawMessage `json:"responses_output,omitempty"`
}

// RecipientType distinguishes who a wrapped DEK is for.
const (
	RecipientBoss  = "boss"
	RecipientOwner = "owner"
)

// Recipient is one wrapped copy of the content DEK for a single public key.
type Recipient struct {
	Type       string `json:"type"`        // "boss" | "owner"
	KID        string `json:"kid"`         // key id of the public key used
	Alg        string `json:"alg"`         // wrapping algorithm, e.g. RSA-OAEP-SHA256
	WrappedDEK string `json:"wrapped_dek"` // base64 (std) of RSA-OAEP(DEK)
}

// AAD is the additional authenticated data bound into the AES-GCM seal. It is
// stored in the clear (it is metadata, not secret) so a reader can reconstruct
// the exact AAD bytes before opening the ciphertext.
type AAD struct {
	LogID  string `json:"log_id"`
	Field  string `json:"field"`
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
}

// Envelope is the JSON object stored inside an otherwise-plaintext log content
// field. The Marker field is always true so readers can detect it.
//
// For StatusNotRecorded placeholders, ContentCiphertext/ContentNonce/Recipients
// are empty and Reason is set.
type Envelope struct {
	Marker  bool   `json:"__jq_log_encryption_v2"`
	Version int    `json:"version"`
	Status  string `json:"status"`
	Field   string `json:"field"`
	Reason  string `json:"reason,omitempty"`

	ContentAlg        string `json:"content_alg,omitempty"`
	ContentEncoding   string `json:"content_encoding"`
	ContentNonce      string `json:"content_nonce,omitempty"`      // base64 std
	ContentCiphertext string `json:"content_ciphertext,omitempty"` // base64 std

	AAD        *AAD        `json:"aad,omitempty"`
	Recipients []Recipient `json:"recipients,omitempty"`
}
