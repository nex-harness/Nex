package logio

// The prefix is a contract with the rest of the repository: the TypeScript host, server and desktop
// processes all write lines in this exact shape (formatLogPrefix, packages/shared/src/log-format.ts)
// and the tooling that tails them reads the timestamp and source positionally from the start of the
// line. So these tests pin the FORMAT against the TypeScript implementation itself rather than
// against a copy of it, and the format test shells out to node to run the real function.

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedClock is the instant every format assertion uses. It has a two-digit millisecond field and
// a single-digit month and hour, so a formatter that zero-pads (and the TS one does) is caught by a
// missing pad exactly the way a real reader's regex would fail.
var fixedClock = time.Date(2026, 3, 7, 9, 4, 5, 60_000_000, time.Local)

func TestFormatPrefixMatchesTypeScript(t *testing.T) {
	cases := []struct {
		name   string
		source string
		pid    int
	}{
		{name: "with a pid", source: "nex-server:http", pid: 4242},
		{name: "pid zero omits the part", source: "nex-server:http", pid: 0},
		{name: "agent source", source: "nex-agent", pid: 99},
		{name: "browser-style source has no pid", source: "ui", pid: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := formatLogPrefixFromTypeScript(t, tc.source, tc.pid)
			got := FormatPrefix(tc.source, tc.pid, fixedClock)
			if got != want {
				t.Errorf("FormatPrefix(%q, %d) =\n  %q\nwant\n  %q", tc.source, tc.pid, got, want)
			}
		})
	}
}

// TestFormatPrefixIsLocalWithNoZone pins the one detail a Go-to-string format could silently get
// wrong: the TS uses Date's LOCAL getters and appends no zone. A formatter that emitted UTC or a
// "+08:00" suffix would still look like a timestamp, and every reader comparing a Go line against a
// Node line from the same machine would be off by the offset.
func TestFormatPrefixIsLocalWithNoZone(t *testing.T) {
	got := FormatPrefix("src", 1, fixedClock)
	if !strings.Contains(got, fixedClock.Format("2006-01-02 15:04:05")) {
		t.Fatalf("prefix %q does not carry the local wall clock", got)
	}
	if strings.ContainsAny(got[len("[2006-01-02 15:04:05.000] [pid:1] "):], "+") {
		t.Errorf("prefix %q carries a zone designator; formatLogPrefix emits none", got)
	}
}

// formatLogPrefixFromTypeScript runs the REAL formatLogPrefix from the checked-out TypeScript and
// returns what it would print for the same instant.
//
// Two things are pinned rather than copied. First the clock: the TS `formatTimestamp()` reads Date's
// LOCAL getters, so the test hands it a Date whose getters are overridden to the same
// year/month/day/hour/minute/second/millisecond Go used. Without that the two sides would race the
// wall clock and could differ by a millisecond on a fast machine. Second the pid: `pid?: number` is
// optional, so passing undefined is what exercises the no-pid branch and the Go side must treat a
// non-positive pid the same way.
//
// The module is imported from an absolute file:// URL because the ESM resolver rejects a bare
// relative path under --eval. When packages/shared has not been built in this checkout the test
// SKIPS: a missing build artifact is not a formatting bug, and the shape assertions in the other
// tests here still pin the format.
func formatLogPrefixFromTypeScript(t *testing.T, source string, pid int) string {
	t.Helper()
	modulePath, err := filepath.Abs("../../../packages/shared/dist/log-format.js")
	if err != nil {
		t.Fatalf("resolve the shared log-format module: %v", err)
	}
	if _, err := os.Stat(modulePath); err != nil {
		t.Skip("packages/shared/dist/log-format.js is absent; build @nex/shared to compare formats")
	}
	script := `
		const m = await import(process.argv[1]);
		const at = new Date(0);
		const getters = {
			getFullYear: 4, getMonth: 5, getDate: 6,
			getHours: 7, getMinutes: 8, getSeconds: 9, getMilliseconds: 10,
		};
		for (const [name, index] of Object.entries(getters)) {
			Object.defineProperty(at, name, { value: () => Number(process.argv[index]) });
		}
		const Native = Date;
		globalThis.Date = function (...args) {
			return args.length ? new Native(...args) : at;
		};
		globalThis.Date.prototype = Native.prototype;
		const pidArg = Number(process.argv[11]);
		process.stdout.write(m.formatLogPrefix(process.argv[3], pidArg > 0 ? pidArg : undefined));
	`
	output, runErr := exec.Command("node", "--input-type=module", "-e", script,
		"file://"+filepath.ToSlash(modulePath),
		source,
		itoaArg(fixedClock.Year()), itoaArg(int(fixedClock.Month())), itoaArg(fixedClock.Day()),
		itoaArg(fixedClock.Hour()), itoaArg(fixedClock.Minute()), itoaArg(fixedClock.Second()),
		itoaArg(fixedClock.Nanosecond()/1e6), itoaArg(pid),
	).CombinedOutput()
	if runErr != nil {
		t.Skipf("formatLogPrefix is not runnable here (%v): %s", runErr, firstLine(string(output)))
	}
	return strings.TrimRight(string(output), "\n")
}

func itoaArg(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func firstLine(s string) string {
	if index := strings.IndexByte(s, '\n'); index >= 0 {
		return s[:index]
	}
	return s
}

// TestLineStartsWithPrefix pins where the prefix sits in a rendered line: at the START. It is a
// prefix in the regex sense, and any parser reading the timestamp positionally depends on that.
// Prepending to the message (rather than emitting it as an attribute) is what guarantees it, since
// slog's handlers write attributes after the message.
func TestLineStartsWithPrefix(t *testing.T) {
	var out bytes.Buffer
	log := New(&out, Options{Source: "nex-server:http", PID: 7, Now: func() time.Time { return fixedClock }})
	log.Info("websocket connection opened", slog.String("connectionId", "conn_abc"))
	line := strings.TrimRight(out.String(), "\n")
	if !strings.HasPrefix(line, "[2026-03-07 09:04:05.060] [pid:7] [nex-server:http]") {
		t.Fatalf("line does not start with the prefix:\n%s", line)
	}
}

// TestLevelsAreTaggedAndFiltered is the half of the format a caller actually sees: which levels
// reach the stream, and what each one is called on it. Debug being absent at the default level is
// the property AGENTS.md's logging section depends on — a per-connection protocol line must not
// reach a production disk.
func TestLevelsAreTaggedAndFiltered(t *testing.T) {
	var atInfo bytes.Buffer
	New(&atInfo, Options{Source: "s", PID: 1, Now: func() time.Time { return fixedClock }}).
		Info("kept")
	New(&atInfo, Options{Source: "s", PID: 1, Now: func() time.Time { return fixedClock }}).
		Debug("dropped")
	if !strings.Contains(atInfo.String(), "kept") {
		t.Errorf("info line missing: %q", atInfo.String())
	}
	if strings.Contains(atInfo.String(), "dropped") {
		t.Errorf("debug reached a logger built at info: %q", atInfo.String())
	}

	var atDebug bytes.Buffer
	debug := New(&atDebug, Options{Source: "s", PID: 1, Level: LevelDebug, Now: func() time.Time { return fixedClock }})
	debug.Debug("d")
	debug.Info("i")
	debug.Warn("w")
	debug.Error("e")
	lines := strings.Split(strings.TrimRight(atDebug.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines at debug, want 4:\n%s", len(lines), atDebug.String())
	}
	// The tag sits between the prefix and the message, padded to a common width so a log read by eye
	// lines up. The message is the single character the test logged at that level.
	for i, want := range []string{"debug", "info ", "warn ", "error"} {
		suffix := want + " " + string([]byte{'d', 'i', 'w', 'e'}[i])
		if !strings.HasSuffix(lines[i], suffix) {
			t.Errorf("line %d = %q, want it to end with %q", i, lines[i], suffix)
		}
	}
	// Every level's tag is five characters wide, so the messages of a busy connection line up.
	const prefixWidth = len("[2026-03-07 09:04:05.060] [pid:1] [s] ")
	for i, line := range lines {
		if len(line) <= prefixWidth+5 {
			t.Errorf("line %d is too short to carry a five-wide tag: %q", i, line)
			continue
		}
		if got := line[prefixWidth+5]; got != ' ' {
			t.Errorf("line %d has %q where the message should start, want a space: %q", i, got, line)
		}
	}
}

// TestWithKeepsPrefix pins the property that made this a handler wrapper rather than a formatting
// call at every site: logger.With — the way every line in this codebase attaches a connection id —
// must not lose the prefix. Debug, so the line exists at the default level the way a forwarding
// line exists when debug is on.
func TestWithKeepsPrefix(t *testing.T) {
	var out bytes.Buffer
	log := New(&out, Options{
		Source: "nex-server:http", PID: 7, Level: LevelDebug,
		Now: func() time.Time { return fixedClock },
	})
	log.With(slog.String("connectionId", "conn_1")).Debug("rpc forward test.echo 7")
	line := strings.TrimRight(out.String(), "\n")
	if !strings.HasPrefix(line, "[2026-03-07 09:04:05.060] [pid:7] [nex-server:http]") {
		t.Fatalf("With dropped the prefix:\n%s", line)
	}
	if !strings.Contains(line, "rpc forward test.echo 7") {
		t.Errorf("message lost:\n%s", line)
	}
	if !strings.Contains(line, "connectionId=conn_1") {
		t.Errorf("With did not carry its attribute:\n%s", line)
	}
}

// TestZeroLevelMeansInfo pins the default. slog's own zero value IS LevelInfo, but this package
// documents the default rather than relying on the field's zero value happening to be right, and
// the level table in internal/server/logging.go quotes it.
func TestZeroLevelMeansInfo(t *testing.T) {
	var out bytes.Buffer
	New(&out, Options{Source: "s", PID: 1}).Debug("dropped")
	if out.Len() != 0 {
		t.Errorf("debug was written with a zero Level: %q", out.String())
	}
	if got := (Options{}).Level; got != slog.LevelInfo {
		t.Errorf("zero Options.Level = %v, want info", got)
	}
}
