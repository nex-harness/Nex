// Package installer implements the SSH install channel for runners
// (docs/next/runner-install.md): the server dials the target over SSH,
// deploys the runner bundle, starts the supervisor, and waits for the
// runner's hello on the registry. SSH is an install channel only — after
// hello, the runner talks to the server directly and no SSH state remains.
package installer

import (
	"fmt"
	"sync"
	"time"
)

// Status of one install session.
type Status string

const (
	StatusRunning    Status = "running"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusTimedOut   Status = "timed-out"
	StatusCancelled  Status = "cancelled"
)

// LogLevel of one log line (mirrored to slog at the same level).
type LogLevel string

const (
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// LogLine is one timestamped install step record.
type LogLine struct {
	Ts      time.Time `json:"ts"`
	Level   LogLevel  `json:"level"`
	Message string    `json:"message"`
}

// maxLogLines bounds the session log ring buffer (spec decision 8).
const maxLogLines = 200

// Session is one async install run: status + ring-buffered logs. Safe for
// concurrent use from the HTTP handlers and the installer goroutine.
type Session struct {
	ID        string
	Target    string // username@host
	Status    Status
	Error     string
	RunnerID  string
	CreatedAt time.Time

	mu   sync.Mutex
	logs []LogLine
}

// Log appends one line (bounded) and mirrors it to the logger.
func (s *Session) Log(level LogLevel, format string, args ...any) {
	line := LogLine{
		Ts:      time.Now().UTC(),
		Level:   level,
		Message: fmt.Sprintf(format, args...),
	}
	s.mu.Lock()
	if len(s.logs) >= maxLogLines {
		s.logs = append(s.logs[len(s.logs)-maxLogLines+1:], line)
	} else {
		s.logs = append(s.logs, line)
	}
	s.mu.Unlock()
}

// Snapshot returns a copy of the current logs.
func (s *Session) Snapshot() []LogLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LogLine, len(s.logs))
	copy(out, s.logs)
	return out
}

// Finish records the terminal state exactly once.
func (s *Session) Finish(status Status, errMessage string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status == StatusRunning {
		s.Status = status
		s.Error = errMessage
	}
}

// view is the JSON shape of GET /api/runner-install/{id}.
type view struct {
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
	RunnerID string    `json:"runnerId,omitempty"`
	Logs     []LogLine `json:"logs"`
}

// View builds the API response (logs copied).
func (s *Session) View() view {
	s.mu.Lock()
	status, errText, runnerID := s.Status, s.Error, s.RunnerID
	s.mu.Unlock()
	return view{
		Status:   string(status),
		Error:    errText,
		RunnerID: runnerID,
		Logs:     s.Snapshot(),
	}
}
