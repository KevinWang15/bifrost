// Shared helpers for detecting encrypted audit-log content.
//
// When the logencryption plugin seals a conversation, the whole bundle is
// stored as one disguised message whose content is an envelope JSON carrying a
// very long base64 ciphertext (see plugins/logencryption/store.go). That
// ciphertext is meaningless to the browser (it cannot be decrypted client-side)
// and rendering it verbatim is both useless and janky. Detect the marker so the
// UI can render a compact badge instead of the raw envelope.

// Markers for every envelope generation the plugin has persisted. v1 rows stay
// readable as a badge even though v2 readers cannot decrypt them.
export const ENCRYPTED_LOG_MARKERS = ["__jq_log_encryption_v1", "__jq_log_encryption_v2"] as const;

// Backwards-compatible alias for the current marker.
export const ENCRYPTED_LOG_MARKER = "__jq_log_encryption_v2";

// Sentinels the plugin writes into content_summary for encrypted teams, one per
// envelope generation (see plugins/logencryption/store.go summarySentinel).
const ENCRYPTED_SUMMARIES = new Set(["[encrypted]", "[encrypted:v2]"]);

// encryptedContentLabel returns a short human label when `value` is an
// encrypted-log envelope (JSON string or already-parsed object) or an encrypted
// content_summary sentinel, or null for ordinary plaintext content.
export function encryptedContentLabel(value: unknown): string | null {
	if (typeof value === "string") {
		if (ENCRYPTED_SUMMARIES.has(value)) {
			return "Encrypted log content";
		}
		// Cheap prefilter: the marker only appears in JSON objects, so anything that
		// doesn't even contain a marker substring can skip the parse entirely. This
		// keeps the hot path (plaintext messages) from paying a JSON.parse on every
		// render.
		if (!ENCRYPTED_LOG_MARKERS.some((marker) => value.includes(marker))) {
			return null;
		}
	}

	let parsed: unknown = value;
	if (typeof value === "string") {
		try {
			parsed = JSON.parse(value);
		} catch {
			// Plain text content that merely happens to contain the marker substring.
			return null;
		}
	}
	if (typeof parsed !== "object" || parsed === null) {
		return null;
	}
	const envelope = parsed as Record<string, unknown>;
	if (!ENCRYPTED_LOG_MARKERS.some((marker) => envelope[marker] === true)) {
		return null;
	}
	return envelope.status === "content_not_recorded" ? "Content not recorded" : "Encrypted log content";
}
