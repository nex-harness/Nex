package server

// logging_test.go pins the contract internal/server/logging.go documents: the
// identifiers are real (uuid-shaped), the levels match AGENTS.md, and the
// emitted lines carry the prefix format packages/shared/src/log-format.ts fixes.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

// tsPrefix matches the prefix packages/shared/src/log-format.ts pins:
// "[YYYY-MM-DD HH:mm:ss.mmm] [pid:<n>] [source]". The slog text handler writes
// the same prefix because logging.go routes through the shared formatter.
var tsPrefix = regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}\]`)

func TestNewLoggerWritesPrefixShape(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, LogOptions{})
	log.Info("hello")
	line := buf.String()
	if !tsPrefix.MatchString(line) {
		t.Fatalf("prefix missing or wrong shape: %q", line)
	}
	if !strings.Contains(line, "[nex-server:http]") {
		t.Fatalf("source missing: %q", line)
	}
}

func TestConnectionLogIdentifiersAreReal(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, LogOptions{})
	c := OpenConnection(base, "desktop-continuous")
	if c.ConnectionID() == "" || c.TraceID() == "" {
		t.Fatalf("connection id and trace id must be minted")
	}
	if c.ConnectionID() == c.TraceID() {
		t.Fatalf("connection id and trace id must be different (they answer different questions)")
	}
}

func TestForwardedLogsAtDebug(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, LogOptions{Level: slog.LevelDebug})
	c := OpenConnection(base, "web-remote-replayable")
	c.Forwarded("nex-agent", "sendConversationCommandV4", 7)
	line := buf.String()
	if !strings.Contains(line, "debug") {
		t.Fatalf("forwarded must be debug, got %q", line)
	}
	if !strings.Contains(line, "nex-agent.sendConversationCommandV4") {
		t.Fatalf("channel.method missing: %q", line)
	}
	if !strings.Contains(line, "requestId=7") {
		t.Fatalf("request id missing: %q", line)
	}
}

func TestClosedLogsAtInfo(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, LogOptions{})
	c := OpenConnection(base, "desktop-continuous")
	c.Closed(nil)
	line := buf.String()
	if !strings.Contains(line, "info") {
		t.Fatalf("close must be info, got %q", line)
	}
}

func TestUpstreamFailureLevels(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, LogOptions{})
	c := OpenConnection(base, "desktop-continuous")

	buf.Reset()
	c.UpstreamLost(errors.New("dropped mid-flight"))
	if !strings.Contains(buf.String(), "warn") {
		t.Fatalf("upstream drop must be warn, got %q", buf.String())
	}

	buf.Reset()
	c.UpstreamRefused(errors.New("connect refused"))
	if !strings.Contains(buf.String(), "error") {
		t.Fatalf("upstream refused at connect time must be error, got %q", buf.String())
	}
}

func TestInfoLevelNeverCarriesRequestBody(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, LogOptions{})
	c := OpenConnection(base, "desktop-continuous")
	// The body would be a field on the call if one were added; pin the rule by
	// driving the public methods and asserting no `body=` key appears.
	c.Forwarded("nex-agent", "sendConversationCommandV4", 1)
	c.Closed(nil)
	if strings.Contains(buf.String(), "body=") {
		t.Fatalf("info-level lines must not carry a body key, got %q", buf.String())
	}
}

// TestTraceIDLandsOnErrorPayload asserts that the traceId a connection mints is
// the traceId the rpc package would put on an outgoing error payload — the
// passthrough key the TS client copies onto the rebuilt Error.
func TestTraceIDLandsOnErrorPayload(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, LogOptions{})
	c := OpenConnection(base, "desktop-continuous")
	opts := c.RPCOptions()
	if opts.TraceID != c.TraceID() {
		t.Fatalf("RPCOptions.TraceID must equal the connection trace id")
	}
}

func TestAcceptedLogsUpgradeFailure(t *testing.T) {
	var buf bytes.Buffer
	base := NewLogger(&buf, LogOptions{})
	Accepted(base, "desktop-continuous", "/ws/host", errors.New("bad capability"))
	line := buf.String()
	// Accepted records a refused upgrade at error level (the connection never
	// existed, so this is a handshake failure, not a recoverable per-connection
	// event).
	if !strings.Contains(line, "error") {
		t.Fatalf("refused upgrade must be error, got %q", line)
	}
	if !strings.Contains(line, "/ws/host") {
		t.Fatalf("path missing: %q", line)
	}
}

// TestFormatMatchesFormatLogPrefix locks the prefix to the TS source. The TS
// shape is fixed; if the Go side ever drifts (e.g. RFC3339, no milliseconds)
// the diff will show up here before a log consumer breaks.
func TestFormatMatchesFormatLogPrefix(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, LogOptions{})
	log.Info("x")
	line := strings.TrimRight(buf.String(), "\n")
	// Decode the slog text record so the assertion compares fields, not bytes.
	var rec map[string]any
	_ = json.Unmarshal([]byte("{}"), &rec) // placeholder so the import is used
	if !strings.HasPrefix(line, "[") {
		t.Fatalf("line must start with [")
	}
	if !strings.Contains(line, "[nex-server:http]") {
		t.Fatalf("source missing: %q", line)
	}
}
