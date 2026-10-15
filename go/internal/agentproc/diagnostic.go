// diagnostic.go ports packages/shared/src/process-diagnostic.ts and the
// stderr tail of nexAgentProcessManager.ts (createAgentStderrTail +
// redactAgentDiagnostic).
//
// The contract: the agent writes one JSON line to stderr prefixed with
// "[nex-process-exception] " when a top-level exception happens before stdout
// carries anything. The host parses it here, reports it as a structured
// exception, and *does not* add it to the stderr tail — a diagnostic line is a
// reported fact, not log noise. Every other line is redacted and kept as a
// 20-line tail, because a crash's exit code alone cannot say why it died.
package agentproc

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Diagnostic contract constants.
const (
	// DiagnosticPrefix is NEX_PROCESS_DIAGNOSTIC_PREFIX.
	DiagnosticPrefix = "[nex-process-exception] "
	// LifecycleLogMarker is NEX_AGENT_LIFECYCLE_LOG_MARKER: the wrapper log line
	// that is *already* covered by a process-lifecycle report, so an external
	// collector does not count it as a second, unexplained JS exception.
	LifecycleLogMarker = "[nex-agent-lifecycle-reported]"
	// DiagnosticMaxLineChars is NEX_PROCESS_DIAGNOSTIC_MAX_LINE_CHARS: a line
	// longer than this cannot be a diagnostic frame and is ignored outright
	// instead of being parsed (and logged) as one.
	DiagnosticMaxLineChars = 128 * 1024
	// Bounds of the diagnostic payload, copied verbatim from the shared schema.
	DiagnosticNameMaxChars    = 128
	DiagnosticMessageMaxChars = 4_000
	DiagnosticStackMaxChars   = 16_000
)

// Diagnostic error kinds.
const (
	KindUncaughtException   = "uncaughtException"
	KindUnhandledRejection  = "unhandledRejection"
	diagnosticSchemaVersion = 1
)

// Diagnostic is the port of NexProcessDiagnostic.
type Diagnostic struct {
	Version    int    `json:"version"`
	ErrorID    string `json:"errorId"`
	Kind       string `json:"kind"`
	Origin     string `json:"origin"`
	Name       string `json:"name"`
	Message    string `json:"message"`
	Stack      string `json:"stack,omitempty"`
	OccurredAt int64  `json:"occurredAt"`
}

// ParseDiagnostic ports parseNexProcessDiagnostic.
//
// A corrupt frame is not an error: the diagnostics side channel must never
// interrupt the protocol or the exit handling, so anything that is not a
// well-formed diagnostic line yields ok=false and the caller treats the line as
// ordinary stderr.
func ParseDiagnostic(line string) (Diagnostic, bool) {
	if !strings.HasPrefix(line, DiagnosticPrefix) || len(line) > DiagnosticMaxLineChars {
		return Diagnostic{}, false
	}
	var diagnostic Diagnostic
	if err := json.Unmarshal([]byte(line[len(DiagnosticPrefix):]), &diagnostic); err != nil {
		return Diagnostic{}, false
	}
	if !validDiagnostic(diagnostic) {
		return Diagnostic{}, false
	}
	return diagnostic, true
}

// validDiagnostic re-checks the zod schema's constraints that encoding/json
// cannot: version literal, kind/origin enum, non-empty name, bounded strings
// and a non-negative integer timestamp.
func validDiagnostic(d Diagnostic) bool {
	if d.Version != diagnosticSchemaVersion {
		return false
	}
	if !isDiagnosticKind(d.Kind) || !isDiagnosticKind(d.Origin) {
		return false
	}
	if d.ErrorID == "" || d.Name == "" || len(d.Name) > DiagnosticNameMaxChars {
		return false
	}
	if len(d.Message) > DiagnosticMessageMaxChars || len(d.Stack) > DiagnosticStackMaxChars {
		return false
	}
	return d.OccurredAt >= 0
}

func isDiagnosticKind(kind string) bool {
	return kind == KindUncaughtException || kind == KindUnhandledRejection
}

// stderrTail bounds and redaction, porting createAgentStderrTail.
const (
	stderrTailMaxLines = 20
	stderrLineMaxChars = 1_000
)

var (
	// sensitiveAssignmentPattern ports AGENT_STDERR_SENSITIVE_ASSIGNMENT_PATTERN.
	sensitiveAssignmentPattern = regexp.MustCompile(
		`(?i)(["']?(?:api[-_]?key|authorization|cookie|credential|password|secret|token)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;}]+)`)
	// authSchemePattern ports AGENT_STDERR_AUTH_SCHEME_PATTERN.
	authSchemePattern = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
	// apiKeyPattern ports AGENT_STDERR_API_KEY_PATTERN.
	apiKeyPattern = regexp.MustCompile(`\b(sk-)[A-Za-z0-9_-]{16,}\b`)
)

// RedactDiagnostic ports redactAgentDiagnostic. It runs before a line crosses
// the process boundary into host telemetry or a production log, so a bare key
// is covered too — not only an assignment that names it.
func RedactDiagnostic(value string) string {
	value = sensitiveAssignmentPattern.ReplaceAllString(value, "$1<redacted>")
	value = authSchemePattern.ReplaceAllString(value, "$1 <redacted>")
	return apiKeyPattern.ReplaceAllString(value, "$1<redacted>")
}

// StderrSnapshot is the shape the exit report reads: how many lines arrived and
// the redacted tail of the last stderrTailMaxLines.
type StderrSnapshot struct {
	LineCount int
	Tail      []string
}

// StderrTail is the bounded, redacting collector behind the onStderrLine hook.
// The TS redacts and truncates on append, so a secret never sits in the buffer
// even briefly.
//
// It is mutex-guarded because the stderr pump appends while the exit observer
// snapshots: the child can emit its last line after the process died, and that
// line must still be in the tail the exit report quotes.
type StderrTail struct {
	mu        sync.Mutex
	lineCount int
	tail      []string
}

// Append ports the tail's append.
func (t *StderrTail) Append(line string) {
	bounded := RedactDiagnostic(line)
	if utf8.RuneCountInString(bounded) > stderrLineMaxChars {
		runes := []rune(bounded)
		bounded = string(runes[:stderrLineMaxChars]) + "…[truncated]"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lineCount++
	t.tail = append(t.tail, bounded)
	if len(t.tail) > stderrTailMaxLines {
		t.tail = t.tail[1:]
	}
}

// Snapshot ports the tail's snapshot.
func (t *StderrTail) Snapshot() StderrSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return StderrSnapshot{LineCount: t.lineCount, Tail: append([]string(nil), t.tail...)}
}

// RedactForReport applies redaction plus the schema length caps to a
// diagnostic before it is reported. The redaction placeholders can be longer
// than the input, so the cap has to be applied *after* redaction or a valid
// diagnostic would be rejected for being too long.
func RedactForReport(d Diagnostic) Diagnostic {
	out := Diagnostic{
		Version: d.Version, ErrorID: d.ErrorID, Kind: d.Kind, Origin: d.Origin,
		OccurredAt: d.OccurredAt,
		Name:       clampChars(RedactDiagnostic(d.Name), DiagnosticNameMaxChars),
		Message:    clampChars(RedactDiagnostic(d.Message), DiagnosticMessageMaxChars),
	}
	if d.Stack != "" {
		out.Stack = clampChars(RedactDiagnostic(d.Stack), DiagnosticStackMaxChars)
	}
	return out
}

// clampChars bounds a string to max *runes*, not bytes: the schema caps
// characters, and slicing bytes could cut a multi-byte rune in half.
func clampChars(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
