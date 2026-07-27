// Shared helpers for detecting encrypted audit-log content.
//
// When the logencryption plugin seals a conversation, the whole bundle is
// stored as one disguised message whose content is an envelope JSON carrying a
// very long base64 ciphertext (see plugins/logencryption/store.go). That
// ciphertext is meaningless to the browser (it cannot be decrypted client-side)
// and rendering it verbatim is both useless and janky. Detect the marker so the
// UI can render a compact badge instead of the raw envelope.

export const ENCRYPTED_LOG_MARKER = "__jq_log_encryption_v1";

// encryptedContentLabel returns a short human label when `content` is an
// encrypted-log envelope JSON string, or null for ordinary plaintext content.
export function encryptedContentLabel(content: string): string | null {
	// Cheap prefilter: the marker only appears in JSON objects, so anything that
	// doesn't even contain the marker substring can skip the parse entirely. This
	// keeps the hot path (plaintext messages) from paying a JSON.parse on every
	// render.
	if (!content || !content.includes(ENCRYPTED_LOG_MARKER)) {
		return null;
	}
	try {
		const parsed = JSON.parse(content);
		if (parsed?.[ENCRYPTED_LOG_MARKER] === true) {
			return parsed.status === "content_not_recorded" ? "Content not recorded" : "Encrypted log content";
		}
	} catch {
		// Plain text content that merely happens to contain the marker substring.
	}
	return null;
}