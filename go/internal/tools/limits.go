package tools

// Limits mirror PROTOCOL_V4_LIMITS in shared/nex-protocol-v4/core.ts. Only the
// tool-output byte limits are ported here; the wire limits live in
// internal/protocol. A test compares these with the TypeScript constants.
const (
	ToolOutputFinalHeadBytes = 32 * 1024
	ToolOutputFinalTailBytes = 32 * 1024
)

// TruncateHeadTail applies the v4 tool-output rule: when the final model-visible
// text exceeds head+tail bytes, keep the first 32 KiB and the last 32 KiB with a
// single truncation marker between them. It returns the text unchanged when it
// fits, and reports whether truncation happened.
//
// The exact marker text is owned by the v4 projection layer (product-projection),
// which is out of scope here; this helper is the byte-budget half of that rule,
// verified against PROTOCOL_V4_LIMITS and used by tests.
func TruncateHeadTail(text string) (string, bool) {
	b := []byte(text)
	if len(b) <= ToolOutputFinalHeadBytes+ToolOutputFinalTailBytes {
		return text, false
	}
	head := string(b[:ToolOutputFinalHeadBytes])
	tail := string(b[len(b)-ToolOutputFinalTailBytes:])
	return head + "\n…[truncated]…\n" + tail, true
}
