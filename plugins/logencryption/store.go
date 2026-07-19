// Package logencryption provides an encrypting decorator around the Bifrost
// LogStore. It encrypts conversation content (InputHistory / OutputMessage) at
// the persistence seam so that the live request/response objects — and thus the
// provider call and the client response — remain plaintext, while the database
// and any hybrid S3 offload receive only ciphertext envelopes.
//
// Why a store decorator instead of a PreLLM/PostLLM plugin: the logging plugin
// keeps a live reference to req.ChatRequest.Input and serializes it only at
// write time. Mutating request/response content in a hook to inject ciphertext
// would also corrupt the provider call. Transforming the Log entry just before
// persistence is the only seam that gets ciphertext into storage without
// touching the live objects.
package logencryption

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logencryption/envelope"
)

// EncryptingLogStore wraps a logstore.LogStore and encrypts content fields of
// Log entries for non-exempt teams before delegating to the inner store.
type EncryptingLogStore struct {
	logstore.LogStore // embedded: all read methods and untouched writes pass through

	resolver PolicyResolver
	logger   schemas.Logger
}

// NewEncryptingLogStore wraps inner with encryption driven by resolver.
func NewEncryptingLogStore(inner logstore.LogStore, resolver PolicyResolver, logger schemas.Logger) *EncryptingLogStore {
	return &EncryptingLogStore{LogStore: inner, resolver: resolver, logger: logger}
}

// Create encrypts then delegates.
func (s *EncryptingLogStore) Create(ctx context.Context, entry *logstore.Log) error {
	s.encrypt(ctx, entry)
	return s.LogStore.Create(ctx, entry)
}

// CreateIfNotExists encrypts then delegates.
func (s *EncryptingLogStore) CreateIfNotExists(ctx context.Context, entry *logstore.Log) error {
	s.encrypt(ctx, entry)
	return s.LogStore.CreateIfNotExists(ctx, entry)
}

// BatchCreateIfNotExists encrypts each entry then delegates. This is the path
// the logging plugin's async writer actually uses.
func (s *EncryptingLogStore) BatchCreateIfNotExists(ctx context.Context, entries []*logstore.Log) error {
	for _, e := range entries {
		s.encrypt(ctx, e)
	}
	return s.LogStore.BatchCreateIfNotExists(ctx, entries)
}

// contentUpdateKeys are the GORM update-map keys that carry conversation content.
// If any appears in an Update, we must not let plaintext reach storage.
var contentUpdateKeys = []string{
	"input_history",
	"output_message",
	"responses_input_history",
	"responses_output",
	"content_summary",
	"raw_request",
	"raw_response",
}

// Update defends the content path. The live logging flow builds complete entries
// via BatchCreateIfNotExists and only uses Update for usage/metadata; but
// updateLogEntry can build a map containing content keys. Since an update map
// carries serialized strings (and may lack the vk/team needed to resolve policy
// or wrap recipients), we fail secure: any content-bearing key is replaced with
// a not-recorded placeholder rather than risk persisting plaintext. Non-content
// updates (token usage, cost, status, errors) pass through unchanged.
func (s *EncryptingLogStore) Update(ctx context.Context, id string, entry any) error {
	switch v := entry.(type) {
	case map[string]interface{}:
		s.scrubUpdateMap(id, v)
	case *logstore.Log:
		// Struct updates run SerializeFields in RDB (rdb.go serializeLogUpdateEntry),
		// which would serialize content-bearing parsed fields to plaintext columns.
		// Route through the same encrypt transform so content is sealed/fail-secure.
		s.encrypt(ctx, v)
	case logstore.Log:
		s.encrypt(ctx, &v)
		return s.LogStore.Update(ctx, id, v)
	}
	return s.LogStore.Update(ctx, id, entry)
}

// scrubUpdateMap fail-secures a content-bearing GORM update map. An update map
// carries serialized strings and may lack the vk/team needed to resolve policy
// or wrap recipients, so any content-bearing key is replaced with a not-recorded
// placeholder rather than risk persisting plaintext. Non-content updates (token
// usage, cost, status, errors) are left untouched.
func (s *EncryptingLogStore) scrubUpdateMap(id string, m map[string]interface{}) {
	scrubbed := false
	for _, k := range contentUpdateKeys {
		if _, present := m[k]; !present {
			continue
		}
		switch k {
		case "content_summary":
			m[k] = summarySentinel
		case "input_history":
			m[k] = mustJSON(envelope.NotRecorded(envelope.FieldInputHistory, envelope.ReasonPolicyUnavailable))
		case "output_message":
			m[k] = mustJSON(envelope.NotRecorded(envelope.FieldOutputMessage, envelope.ReasonPolicyUnavailable))
		case "responses_input_history":
			m[k] = mustJSON(envelope.NotRecorded(envelope.FieldResponsesInputHistory, envelope.ReasonPolicyUnavailable))
		case "responses_output":
			m[k] = mustJSON(envelope.NotRecorded(envelope.FieldResponsesOutput, envelope.ReasonPolicyUnavailable))
		case "raw_request", "raw_response":
			m[k] = ""
		}
		scrubbed = true
	}
	if scrubbed {
		s.warn("log-encryption: content-bearing Update scrubbed to fail-secure placeholders for id %s", id)
	}
}

// summarySentinel is written to ContentSummary for encrypted teams. It MUST be
// non-empty: SerializeFields regenerates ContentSummary from the parsed content
// fields whenever ContentSummary == "" (framework/logstore/tables.go:645), which
// would re-derive plaintext. A non-empty sentinel suppresses that regeneration.
const summarySentinel = "[encrypted]"

// encrypt transforms a single Log entry's content fields in place according to
// policy. It never returns an error: on any policy/crypto failure it applies a
// fail-secure placeholder so plaintext is never persisted.
func (s *EncryptingLogStore) encrypt(ctx context.Context, entry *logstore.Log) {
	if entry == nil {
		return
	}
	hasInput := entry.InputHistory != "" || entry.InputHistoryParsed != nil
	hasOutput := entry.OutputMessage != "" || entry.OutputMessageParsed != nil
	hasRespInput := entry.ResponsesInputHistory != "" || entry.ResponsesInputHistoryParsed != nil
	hasRespOutput := entry.ResponsesOutput != "" || entry.ResponsesOutputParsed != nil
	if !hasInput && !hasOutput && !hasRespInput && !hasRespOutput {
		return
	}

	vkID := deref(entry.VirtualKeyID)
	teamID := deref(entry.TeamID)

	policy, err := s.resolver.Resolve(ctx, vkID, teamID)
	if err != nil {
		s.warn("log-encryption: policy unavailable, applying fail-secure placeholder: %v", err)
		s.placeholder(entry, envelope.ReasonPolicyUnavailable)
		return
	}
	// Exempt team: leave plaintext untouched.
	if policy.Exempt || !policy.Encrypt {
		return
	}

	recipients, err := policy.Recipients()
	if err != nil {
		// Boss key missing/invalid → cannot guarantee auditability; fail secure.
		s.warn("log-encryption: recipients unavailable, applying fail-secure placeholder: %v", err)
		s.placeholder(entry, envelope.ReasonBossKeyMissing)
		return
	}

	userID := policy.UserID
	if userID == "" {
		userID = deref(entry.UserID)
	}

	// Bundle ALL conversation content into ONE envelope and carry it as a single
	// disguised message in InputHistoryParsed. This survives Bifrost's list read
	// path (which NULLs output_message and returns only the last input_history
	// element): a one-element array passes truncation unchanged, and the message
	// is well-formed so DeserializeFields does not empty it.
	plaintext, ok := marshalBundle(entry)
	if !ok || len(plaintext) == 0 {
		return
	}
	env, err := envelope.Seal(plaintext, envelope.FieldBundle, envelope.AAD{
		LogID: entry.ID, Field: envelope.FieldBundle, TeamID: teamID, UserID: userID,
	}, recipients)
	if err != nil {
		s.warn("log-encryption: seal bundle failed, applying placeholder: %v", err)
		s.placeholder(entry, envelope.ReasonPolicyUnavailable)
		return
	}
	installCarrier(entry, mustJSON(env))
}

// installCarrier is the single seam that turns a Log entry into an encrypted
// row: it replaces all content with one disguised carrier message (fixed
// content-free role; content = the given envelope/placeholder JSON string) in
// InputHistoryParsed, clears the sibling content string columns, nils every
// other content parsed field, scrubs raw columns, and sets the summary sentinel.
// Both the encrypted path (encrypt) and the fail-secure path (placeholder)
// go through here so they can never drift apart (e.g. forget to blank a column).
func installCarrier(entry *logstore.Log, envelopeJSON string) {
	content := envelopeJSON
	entry.InputHistory = ""
	entry.InputHistoryParsed = []schemas.ChatMessage{{
		Role:    schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{ContentStr: &content},
	}}
	// Clear the other content string columns so no stale plaintext remains; their
	// parsed fields are nilled by nilContentParsedExceptInput (InputHistoryParsed
	// is the carrier and intentionally preserved).
	entry.OutputMessage = ""
	entry.ResponsesInputHistory = ""
	entry.ResponsesOutput = ""
	nilContentParsedExceptInput(entry)
	scrubRaw(entry)
	entry.ContentSummary = summarySentinel
}

// scrubRaw blanks the raw provider request/response columns. These carry
// verbatim provider bytes (not normalized content) and are out of scope for the
// audit use case; blanking them makes the "no raw plaintext at rest" guarantee
// self-enforcing rather than dependent on external raw-capture config (R7).
func scrubRaw(entry *logstore.Log) {
	entry.RawRequest = ""
	entry.RawResponse = ""
}

// placeholder replaces all content with a single fail-secure carrier message
// (not-recorded envelope) so no plaintext survives.
func (s *EncryptingLogStore) placeholder(entry *logstore.Log, reason string) {
	installCarrier(entry, mustJSON(envelope.NotRecorded(envelope.FieldBundle, reason)))
}

// nilContentParsedExceptInput nils every content-bearing parsed field that
// BuildContentSummary (framework/logstore/tables.go:1183) reads, EXCEPT
// InputHistoryParsed which holds the carrier message. This prevents
// SerializeFields/BuildContentSummary from re-deriving or re-serializing
// plaintext for encrypted rows.
func nilContentParsedExceptInput(entry *logstore.Log) {
	entry.ResponsesInputHistoryParsed = nil
	entry.OutputMessageParsed = nil
	entry.ResponsesOutputParsed = nil
	entry.RerankOutputParsed = nil
	entry.OCROutputParsed = nil
	entry.SpeechInputParsed = nil
	entry.TranscriptionOutputParsed = nil
	entry.ImageGenerationInputParsed = nil
	entry.ImageEditInputParsed = nil
	entry.VideoGenerationInputParsed = nil
}

func (s *EncryptingLogStore) warn(format string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(format, args...)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
