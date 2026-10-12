// Package logio writes log lines in the prefix format the whole repository already parses.
//
// The format is `formatLogPrefix` from packages/shared/src/log-format.ts, ported exactly:
//
//	with pid:    "[2026-10-12 08:30:15.123] [pid:4242] [nex-server:http]"
//	without pid: "[2026-10-12 08:30:15.123] [nex-server:http]"
//
// and it stays byte-identical because the TypeScript host, server and desktop processes all write
// it (packages/server/src/http.ts, entry-stdio.ts, packages/desktop/src/host/index.ts, the UI's
// own logger.ts) and the tooling that tails those files reads the timestamp and source positionally
// from the START of the line. A Go server that emitted a different shape would make one process's
// lines unrecognisable to the tooling that already handles the others, so this port keeps the
// prefix at the line start and adds only a level tag after it.
//
// The prefix is built by a slog.Handler wrapper rather than by the call sites, for one reason:
// call sites are in internal/server, internal/gateway and internal/adapters/node, none of which
// should have to remember the format or read os.Getpid(). Wrapping the handler also means
// logger.With(...) — the way every line in this codebase attaches a connection id — keeps the
// prefix, which formatting at the call site would lose.
//
// This package is an adapter because it touches the process (the pid) and the stream. Nothing in
// it knows what the server logs; it only decides how a line looks.
package logio

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// formatLogPrefix is a verbatim port:
//
//	export function formatLogPrefix(source: string, pid?: number): string {
//	  const ts = formatTimestamp();
//	  const pidPart = pid != null ? ` [pid:${pid}]` : "";
//	  return `[${ts}]${pidPart} [${source}]`;
//	}
//
// The timestamp is LOCAL time with no zone suffix, because Date.getHours() and friends are local
// in JS too — a reader comparing Go lines against Node lines sees the same wall clock on both
// sides, which is the whole point of a shared format.
func FormatPrefix(source string, pid int, now time.Time) string {
	ts := now.Format("2006-01-02 15:04:05.000")
	var pidPart string
	if pid > 0 {
		pidPart = fmt.Sprintf(" [pid:%d]", pid)
	}
	return fmt.Sprintf("[%s]%s [%s]", ts, pidPart, source)
}

// levelTags are the level words written after the prefix. They are padded to a common width so the
// messages of a busy connection line up when a log is read by eye; the padding is INSIDE the level
// field, not part of the prefix, and loggers that parse the prefix keep working unchanged.
//
// "debug" and "info" are lower case and "warn"/"error" upper case because that is the case the
// rest of the repository uses (packages/ui/src/logger.ts, the AGENTS.md level names).
var levelTags = map[slog.Level]string{
	slog.LevelDebug: "debug",
	slog.LevelInfo:  "info ",
	slog.LevelWarn:  "warn ",
	slog.LevelError: "error",
}

// The levels, re-exported so an entry point configuring a logger names them the way the rest of
// the Go tree does instead of importing log/slog for one constant.
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// levelTag returns the fixed-width tag for a level. A level between the standard ones falls back
// to its own name plus a sign, which keeps an unusual level readable instead of blank.
func levelTag(level slog.Level) string {
	if tag, ok := levelTags[level]; ok {
		return tag
	}
	return fmt.Sprintf("%-5s", strings.ToLower(level.String()))
}

// Options configures the logger.
type Options struct {
	// Source is the `nex-server:http`-style token in the prefix. Required.
	Source string
	// Level is the minimum level that is written. Zero selects slog.LevelInfo, which is the
	// production default: AGENTS.md reserves debug for high-frequency diagnostics that must not
	// reach a production disk, and a per-connection protocol line is exactly that.
	Level slog.Level
	// PID is the process id in the prefix. Zero selects the real one; a test passes a fixed
	// value to pin the format. Only a non-positive value omits the pid part, which reproduces the
	// TS `pid?: number` optional argument.
	PID int
	// Now is the clock. Nil selects time.Now; a test injects a fixed clock so the timestamp is
	// assertable.
	Now func() time.Time
}

// New builds a logger that writes prefixed lines to w.
//
// The handler OWNS the whole line rather than wrapping slog's text handler, because the prefix has
// to be the first thing ON the line. A wrapper can only rewrite record.Message, and every stock
// handler writes `time=… level=… msg="…"` before the message — so a wrapped prefix lands in the
// middle of the line, where the positional parsers in the existing tooling cannot see it. Rendering
// the line here is the only way `[timestamp] [pid] [source]` is actually a prefix.
//
// That also means attribute rendering is reimplemented rather than inherited, which is a real cost:
// the three stock handlers exist precisely so nobody has to. What makes it worth paying here is
// that this process's lines have to be indistinguishable in shape from the TypeScript ones, which
// no stock handler produces.
func New(w io.Writer, opts Options) *slog.Logger {
	pid := opts.PID
	if pid == 0 {
		pid = os.Getpid() //nolint:forbidigo // this package is the sanctioned process-info adapter
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return slog.New(&lineHandler{
		writer: w,
		source: opts.Source,
		pid:    pid,
		now:    now,
		level:  opts.Level,
		mu:     &sync.Mutex{},
	})
}

// lineHandler renders one line per record: the formatLogPrefix prefix, the level tag, the message,
// then the attributes.
type lineHandler struct {
	writer io.Writer
	source string
	pid    int
	now    func() time.Time
	level  slog.Level

	// mu serializes writes. slog makes no write atomicity promise across records — a handler is
	// documented as needing to handle concurrency itself — and two connections logging at once
	// would otherwise interleave inside a line and corrupt the very format this package exists to
	// keep parseable.
	mu    *sync.Mutex
	attrs []slog.Attr
	// groups are the open WithGroup scopes, each with the attributes given inside it.
	groups []groupAttrs
}

// groupAttrs is one open group: the attributes bound under it. The name is needed because the
// qualified key of an attribute is group.key for each enclosing group.
type groupAttrs struct {
	name  string
	attrs []slog.Attr
}

// Enabled is the level filter, and the ONLY one: a Debug call on a logger built at info costs one
// comparison and produces nothing. That is what makes "a request body never reaches a production
// log" a property of the level rather than of every call site remembering to check it.
func (h *lineHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle renders and writes one record.
func (h *lineHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString(FormatPrefix(h.source, h.pid, h.now()))
	line.WriteByte(' ')
	line.WriteString(levelTag(record.Level))
	line.WriteByte(' ')
	line.WriteString(record.Message)
	for _, attr := range h.rendered() {
		line.WriteByte(' ')
		line.WriteString(attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		if text := h.renderOne(slog.Attr{Key: h.qualify(attr.Key), Value: attr.Value}); text != "" {
			line.WriteByte(' ')
			line.WriteString(text)
		}
		return true
	})
	line.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.writer, line.String())
	return err
}

// WithAttrs returns a handler carrying attrs on every line, still prefixed. This is the path that
// matters most: logger.With(connectionId, traceId, mode) is how a connection's identifiers reach
// its lines, and it must not cost the prefix.
func (h *lineHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := h.clone()
	next.attrs = append(next.attrs, attrs...)
	return next
}

// WithGroup opens a group scope. Attributes bound inside are prefixed with the group name, the same
// way slog's own handlers qualify keys, so `log.WithGroup("rpc").Info("…", "id", 7)` reads
// `rpc.id=7` here and reads the same in a stock handler.
func (h *lineHandler) WithGroup(name string) slog.Handler {
	next := h.clone()
	next.groups = append(next.groups, groupAttrs{name: name})
	return next
}

// clone copies the handler for WithAttrs/WithGroup. The slices are re-allocated so a derived logger
// cannot alias the parent's attribute storage: slog hands a derived handler the same slice it was
// given, and appending to it would write into the parent's backing array.
func (h *lineHandler) clone() *lineHandler {
	next := &lineHandler{
		writer: h.writer,
		source: h.source,
		pid:    h.pid,
		now:    h.now,
		level:  h.level,
		mu:     h.mu,
	}
	if h.attrs != nil {
		next.attrs = append([]slog.Attr(nil), h.attrs...)
	}
	next.groups = make([]groupAttrs, len(h.groups))
	copy(next.groups, h.groups)
	for i := range next.groups {
		next.groups[i].attrs = append([]slog.Attr(nil), h.groups[i].attrs...)
	}
	return next
}

// qualify resolves an attribute's key against the open group scopes, returning the key it should
// be written under. An attribute bound inside no group keeps its own key, which is the common case
// for this codebase: everything is log.Info(msg, "key", value).
func (h *lineHandler) qualify(key string) string {
	for _, group := range h.groups {
		key = group.name + "." + key
	}
	return key
}

// rendered returns the `key=value` text for every attribute bound with WithAttrs, in binding order,
// with the open groups applied.
func (h *lineHandler) rendered() []string {
	if len(h.attrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(h.attrs))
	for _, attr := range h.attrs {
		if text := h.renderOne(slog.Attr{Key: h.qualify(attr.Key), Value: attr.Value}); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// renderOne renders one attribute as `key=value`. The empty string is returned for a group with no
// members, which is what a stock handler does too — there is nothing to say about it.
func (h *lineHandler) renderOne(attr slog.Attr) string {
	switch attr.Value.Kind() {
	case slog.KindGroup:
		group := attr.Value.Group()
		if len(group) == 0 {
			return ""
		}
		parts := make([]string, 0, len(group))
		for _, member := range group {
			key := member.Key
			if attr.Key != "" {
				key = attr.Key + "." + key
			}
			if text := h.renderOne(slog.Attr{Key: key, Value: member.Value}); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case slog.KindString:
		return attr.Key + "=" + quoteIfNeeded(attr.Value.String())
	default:
		return attr.Key + "=" + attr.Value.String()
	}
}

// quoteIfNeeded quotes a string attribute value containing a space, a quote or an equals sign, the
// same rule slog's TextHandler applies — so a log line with an error message in it stays parseable
// as one attribute rather than reading as two.
func quoteIfNeeded(value string) string {
	if !strings.ContainsAny(value, " =\"") {
		return value
	}
	return strconv.Quote(value)
}

// Compile-time proof this is a slog handler.
var _ slog.Handler = (*lineHandler)(nil)
