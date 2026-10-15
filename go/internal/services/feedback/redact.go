// redact.go wires the feedback text scrub to internal/feedbackprivacy. Kept
// as a separate file so feedback.go has no import of the redaction package
// while still exposing the single scrub entry point (redact) it calls.
package feedback

import (
	"github.com/tylinux/nex/go/internal/feedbackprivacy"
)

func init() {
	// 诊断归档与工单正文共用同一条完整脱敏路径（feedbackPrivacy.ts 的注释：
	// 字符串也可能包含日志前缀或多个 JSON 片段）。
	feedbackRedact = func(text string) string {
		return feedbackprivacy.RedactFeedbackText(text, false)
	}
}

// redactDiagnostic is the archive variant (diagnostic: true).
func redactDiagnostic(text string) string {
	return feedbackprivacy.RedactFeedbackText(text, true)
}
