// Package logx ports createServiceLogger from
// packages/services/src/logger/serviceLogger.ts (step 3.1).
//
// It exists separately from internal/adapters/logio for the reason the TS cites:
// 服务层日志过去常被复用到 Nex Agent 命名 logger，导致新 Nex 路径继续依赖 Nex Agent
// 目录。Here that means the *service* logging convention — scope + traceId in the
// shared prefix, level policy per AGENTS.md — is owned by the services layer and
// neither drags in the HTTP shell's logger nor the agent's.
//
// The prefix format is the same formatLogPrefix string the whole repository
// parses; logio.FormatPrefix is the byte-identical implementation, so this
// package reuses it rather than growing a second copy.
package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/logio"
)

// ServiceLogger is the port of the TS ServiceLogger interface: every method
// takes an optional traceId. The Go spelling uses variadic key/value pairs
// instead of `...args: unknown[]`; call sites pass strings and small values.
type ServiceLogger interface {
	Debug(traceID string, msg string, args ...any)
	Info(traceID string, msg string, args ...any)
	Warn(traceID string, msg string, args ...any)
	Error(traceID string, msg string, args ...any)
}

// Options ports the createServiceLogger options: pid, sink, isDebugEnabled.
type Options struct {
	// PID is the `[pid:N]` part. Zero selects the real pid (the TS default
	// process.pid); a negative value omits the pid part entirely.
	PID int
	// Sink receives the formatted lines. Nil uses stderr, which is what the
	// Node host's console maps to for a detached service process.
	Sink io.Writer
	// IsDebugEnabled gates Debug only, exactly like the TS: debug 日志默认只在
	// 本地开发运行时打印；测试/正式安装包都使用 production 构建。Nil resolves
	// once at construction from NEX_RUNTIME_ENV == "development"
	// (isEffectiveDevelopmentNodeEnv).
	IsDebugEnabled func() bool
	// Now lets a test pin the timestamp in the prefix. Nil uses time.Now.
	Now func() time.Time
}

// serviceLogger is the concrete implementation. Lines are written under a mutex
// because the sink is shared by every scope of the process.
type serviceLogger struct {
	scope          string
	pid            int
	sink           io.Writer
	isDebugEnabled func() bool
	mu             *sync.Mutex
	now            func() time.Time
}

// NewServiceLogger ports createServiceLogger(scope, options).
func NewServiceLogger(scope string, opts *Options) ServiceLogger {
	o := Options{}
	if opts != nil {
		o = *opts
	}
	pid := o.PID
	if pid == 0 {
		pid = os.Getpid() //nolint:forbidigo // this package is the sanctioned process-info adapter
	}
	sink := o.Sink
	if sink == nil {
		sink = os.Stderr //nolint:forbidigo // adapter package
	}
	debugEnabled := o.IsDebugEnabled
	if debugEnabled == nil {
		debugEnabled = func() bool { return isDevelopmentRuntimeEnv() }
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &serviceLogger{
		scope: scope, pid: pid, sink: sink,
		isDebugEnabled: debugEnabled,
		mu:             &sync.Mutex{},
		now:            now,
	}
}

// isDevelopmentRuntimeEnv ports isEffectiveDevelopmentNodeEnv: only the app/CLI
// injected NEX_RUNTIME_ENV counts (NODE_ENV is deliberately ignored — it leaks
// from user shells), and "test" is treated as production so high-frequency
// debug logs never reach a test run's disk either.
func isDevelopmentRuntimeEnv() bool {
	//nolint:forbidigo // this package is the sanctioned env adapter
	return strings.TrimSpace(os.Getenv("NEX_RUNTIME_ENV")) == "development"
}

// write renders one line: the shared prefix, the level tag, the message, then
// the key=value args. The source in the prefix is `scope` or
// `scope][trace:<traceId>` — the exact TS composition.
func (l *serviceLogger) write(level string, traceID, msg string, args []any) {
	if level == "debug" && !l.isDebugEnabled() {
		return
	}
	source := l.scope
	if traceID != "" {
		source = l.scope + "][trace:" + traceID
	}
	var line strings.Builder
	line.WriteString(logio.FormatPrefix(source, l.pid, l.now()))
	line.WriteByte(' ')
	// Level tags are padded to a common width (logio.levelTag's convention) so
	// messages line up when a log is read by eye.
	switch level {
	case "error":
		line.WriteString("error")
	case "warn ":
		line.WriteString("warn ")
	case "debug":
		line.WriteString("debug")
	default:
		line.WriteString("info ")
	}
	line.WriteByte(' ')
	line.WriteString(msg)
	for i := 0; i+1 < len(args); i += 2 {
		line.WriteByte(' ')
		line.WriteString(fmt.Sprint(args[i]))
		line.WriteByte('=')
		line.WriteString(fmt.Sprint(args[i+1]))
	}
	line.WriteByte('\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.sink, line.String())
}

func (l *serviceLogger) Debug(traceID, msg string, args ...any) { l.write("debug", traceID, msg, args) }
func (l *serviceLogger) Info(traceID, msg string, args ...any)  { l.write("info", traceID, msg, args) }
func (l *serviceLogger) Warn(traceID, msg string, args ...any)  { l.write("warn ", traceID, msg, args) }
func (l *serviceLogger) Error(traceID, msg string, args ...any) { l.write("error", traceID, msg, args) }
