package logencryption

import (
	"encoding/json"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logencryption/envelope"
)

// marshalBundle assembles the whole-conversation plaintext bundle from whatever
// content fields are present on the entry. The second return is false when there
// is nothing to seal. Each field carries the original serialized JSON verbatim so
// the browser can restore the exact structure the renderer expects.
func marshalBundle(entry *logstore.Log) ([]byte, bool) {
	var b envelope.Bundle
	any := false
	if v, ok := marshalInput(entry); ok {
		b.InputHistory = json.RawMessage(v)
		any = true
	}
	if v, ok := marshalOutput(entry); ok {
		b.OutputMessage = json.RawMessage(v)
		any = true
	}
	if v, ok := marshalResponsesInput(entry); ok {
		b.ResponsesInputHistory = json.RawMessage(v)
		any = true
	}
	if v, ok := marshalResponsesOutput(entry); ok {
		b.ResponsesOutput = json.RawMessage(v)
		any = true
	}
	if !any {
		return nil, false
	}
	data, err := json.Marshal(&b)
	if err != nil {
		return nil, false
	}
	return data, true
}

// marshalInput returns the plaintext JSON bytes for the input history, preferring
// the parsed structs (fresh log entries) and falling back to the already-
// serialized string. The second return is false when there is nothing to seal.
func marshalInput(entry *logstore.Log) ([]byte, bool) {
	if entry.InputHistoryParsed != nil {
		if b, err := json.Marshal(entry.InputHistoryParsed); err == nil {
			return b, true
		}
	}
	if entry.InputHistory != "" {
		return []byte(entry.InputHistory), true
	}
	return nil, false
}

// marshalOutput returns the plaintext JSON bytes for the output message.
func marshalOutput(entry *logstore.Log) ([]byte, bool) {
	if entry.OutputMessageParsed != nil {
		if b, err := json.Marshal(entry.OutputMessageParsed); err == nil {
			return b, true
		}
	}
	if entry.OutputMessage != "" {
		return []byte(entry.OutputMessage), true
	}
	return nil, false
}

// marshalResponsesInput returns the plaintext JSON bytes for the Responses-API
// input history.
func marshalResponsesInput(entry *logstore.Log) ([]byte, bool) {
	if entry.ResponsesInputHistoryParsed != nil {
		if b, err := json.Marshal(entry.ResponsesInputHistoryParsed); err == nil {
			return b, true
		}
	}
	if entry.ResponsesInputHistory != "" {
		return []byte(entry.ResponsesInputHistory), true
	}
	return nil, false
}

// marshalResponsesOutput returns the plaintext JSON bytes for the Responses-API
// output.
func marshalResponsesOutput(entry *logstore.Log) ([]byte, bool) {
	if entry.ResponsesOutputParsed != nil {
		if b, err := json.Marshal(entry.ResponsesOutputParsed); err == nil {
			return b, true
		}
	}
	if entry.ResponsesOutput != "" {
		return []byte(entry.ResponsesOutput), true
	}
	return nil, false
}

// mustJSON marshals v to a compact JSON string. Envelope types are plain structs
// that always marshal successfully; on the impossible error path it returns a
// minimal placeholder so we never persist plaintext.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"__jq_log_encryption_v1":true,"version":1,"status":"content_not_recorded","reason":"policy_unavailable"}`
	}
	return string(b)
}
