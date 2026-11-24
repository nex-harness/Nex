package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

const (
	defaultBashTimeoutMS = 120_000
	maxBashTimeoutMS     = 600_000
	maxInlineOutputBytes = 16 * 1024
)

type bashArgs struct {
	Command         string `json:"command"`
	Description     string `json:"description,omitempty"`
	TimeoutMS       int    `json:"timeout_ms,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

// BashTool executes shell commands with streaming output, scoped env injection, secret redaction,
// and cross-platform process-tree cleanup.
type BashTool struct {
	bgMgr    *BackgroundTaskManager
	extraEnv map[string]string
	redactor *SecretRedactor
}

func NewBashTool() *BashTool { return &BashTool{} }

func NewBashToolWithBackground(mgr *BackgroundTaskManager) *BashTool {
	return &BashTool{bgMgr: mgr}
}

// SetExtraEnv configures per-session injected environment variables and automatically enables output redaction.
func (t *BashTool) SetExtraEnv(env map[string]string) {
	t.extraEnv = env
	t.redactor = NewSecretRedactor(env)
	if t.bgMgr != nil {
		t.bgMgr.SetExtraEnv(env, t.redactor)
	}
}

func (t *BashTool) Name() string     { return "bash" }
func (t *BashTool) Category() string { return "system" }
func (t *BashTool) IsReadOnly(rawArgs json.RawMessage) bool {
	var args bashArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return false
	}
	if args.RunInBackground {
		return false
	}
	return IsReadOnlyBashCommand(args.Command)
}

func (t *BashTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "bash",
		Category:    "system",
		Description: "Execute a shell command in the workspace. Streams output, enforces a timeout, and terminates the entire process tree on timeout or cancellation.\n\n- Prefer the dedicated tools when one fits: use `read`/`write`/`edit` for files and `grep`/`glob` for searching, instead of `cat`, `head`, `tail`, `sed`, `awk`, `grep`, or `find`.\n- Prefer absolute paths and set `cwd` rather than relying on the shell's current directory.\n- Always provide a concise `description` of what the command does (5-10 words).\n- Set `run_in_background: true` for long-running servers/watchers, then inspect via `task_output` and terminate via `task_stop`.\n- Reserve `bash` for work the other tools cannot do: builds, tests, package managers, git, and process control.\n\n# Git\n- Never use interactive flags (`-i`, such as `git rebase -i` or `git add -i`); they are unsupported in this environment.\n- Use the `gh` CLI for GitHub operations such as pull requests and issues.\n- Commit or push only when the user asks. If you are on the default branch, create a branch first.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "Shell command to execute"},
				"description": {"type": "string", "description": "Concise human-readable description of what this command does (5-10 words)"},
				"timeout_ms": {"type": "integer", "description": "Timeout in milliseconds (default 120000, max 600000)"},
				"cwd": {"type": "string", "description": "Optional working directory relative to workspace"},
				"run_in_background": {"type": "boolean", "description": "Run command asynchronously in the background and return a task_id for task_output/task_stop"}
			},
			"required": ["command"]
		}`),
	}
}

func (t *BashTool) Execute(ctx context.Context, workDir string, rawArgs json.RawMessage, onUpdate UpdateFunc) (Result, error) {
	var args bashArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid bash arguments: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{Output: "command is required", IsError: true}, nil
	}

	timeoutMS := args.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = defaultBashTimeoutMS
	}
	if timeoutMS > maxBashTimeoutMS {
		timeoutMS = maxBashTimeoutMS
	}

	execDir := workDir
	if args.Cwd != "" {
		execDir = ResolvePath(workDir, args.Cwd)
	}
	if execDir != "" {
		if st, err := os.Stat(execDir); err != nil || !st.IsDir() {
			return Result{Output: fmt.Sprintf("working directory %s does not exist", execDir), IsError: true}, nil
		}
	}

	if args.RunInBackground {
		if t.bgMgr == nil {
			return Result{Output: "run_in_background is not enabled for this bash tool instance", IsError: true}, nil
		}
		var bgTimeout time.Duration
		if args.TimeoutMS > 0 {
			bgTimeout = time.Duration(timeoutMS) * time.Millisecond
		}
		taskID := t.bgMgr.Start(execDir, args.Command, bgTimeout)
		return Result{
			Output:   fmt.Sprintf("Started background task %s. Use `task_output` with task_id=%q to read output or `task_stop` to terminate.", taskID, taskID),
			Metadata: map[string]any{"task_id": taskID, "background": true},
		}, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	collector := newBoundedCollector(maxInlineOutputBytes, onUpdate, t.redactor)
	defer collector.Close()

	exitCode, runErr := runPlatformShellCommand(runCtx, execDir, args.Command, collector, t.extraEnv)
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	canceled := ctx.Err() != nil

	output, spillFile, totalBytes := collector.Finalize()
	var sb strings.Builder
	sb.WriteString(output)

	if spillFile != "" {
		fmt.Fprintf(&sb, "\n\n[Output exceeded %d bytes (total %d bytes); full log saved to %s]", maxInlineOutputBytes, totalBytes, spillFile)
	}

	if timedOut {
		fmt.Fprintf(&sb, "\n[Command timed out after %d ms and its process tree was terminated]", timeoutMS)
		return Result{
			Output:   strings.TrimSpace(sb.String()),
			IsError:  true,
			Metadata: map[string]any{"exit_code": -1, "timed_out": true},
		}, nil
	}
	if canceled {
		fmt.Fprintf(&sb, "\n[Command canceled]")
		return Result{
			Output:   strings.TrimSpace(sb.String()),
			IsError:  true,
			Metadata: map[string]any{"exit_code": -1, "canceled": true},
		}, runCtx.Err()
	}

	if runErr != nil && exitCode == 0 {
		return Result{Output: fmt.Sprintf("failed to execute command: %v", runErr), IsError: true}, nil
	}

	if exitCode != 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "Exit code %d", exitCode)
		return Result{
			Output:   strings.TrimSpace(sb.String()),
			IsError:  true,
			Metadata: map[string]any{"exit_code": exitCode},
		}, nil
	}

	finalOut := sb.String()
	if strings.TrimSpace(finalOut) == "" {
		finalOut = "(command completed with no output)"
	}
	return Result{
		Output:   strings.TrimRight(finalOut, "\n"),
		Metadata: map[string]any{"exit_code": 0},
	}, nil
}

// boundedCollector streams live chunks via onUpdate while keeping head+tail in RAM and spilling huge outputs to disk.
type boundedCollector struct {
	mu           sync.Mutex
	maxBytes     int
	headLimit    int
	tailLimit    int
	head         bytes.Buffer
	tail         []byte
	total        int
	spillFile    *os.File
	spillPath    string
	onUpdate     UpdateFunc
	redactor     *SecretRedactor
	streamFilter *StreamFilter
}

func newBoundedCollector(maxBytes int, onUpdate UpdateFunc, redactor *SecretRedactor) *boundedCollector {
	half := maxBytes / 2
	var sf *StreamFilter
	if redactor != nil {
		sf = NewStreamFilter(redactor)
	}
	return &boundedCollector{
		maxBytes:     maxBytes,
		headLimit:    half,
		tailLimit:    maxBytes - half,
		onUpdate:     onUpdate,
		redactor:     redactor,
		streamFilter: sf,
	}
}

func (c *boundedCollector) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	origLen := len(p)
	data := p
	var emitStr string

	c.mu.Lock()
	if c.streamFilter != nil {
		emitStr = c.streamFilter.Push(string(p))
		data = []byte(emitStr)
	} else {
		emitStr = string(p)
	}
	if len(data) > 0 {
		c.writeCleanLocked(data)
	}
	c.mu.Unlock()

	if c.onUpdate != nil && emitStr != "" {
		c.onUpdate(emitStr)
	}
	return origLen, nil
}

func (c *boundedCollector) writeCleanLocked(p []byte) {
	n := len(p)
	c.total += n

	if c.head.Len() < c.headLimit {
		rem := c.headLimit - c.head.Len()
		if n <= rem {
			c.head.Write(p)
		} else {
			c.head.Write(p[:rem])
			c.appendTailLocked(p[rem:])
		}
	} else {
		c.appendTailLocked(p)
	}

	if c.total > c.maxBytes && c.spillFile == nil {
		if f, err := os.CreateTemp("", "nex-bash-*.log"); err == nil {
			c.spillFile = f
			c.spillPath = f.Name()
			_, _ = c.spillFile.Write(c.head.Bytes())
			prevTailLen := len(c.tail) - n
			if prevTailLen > 0 {
				_, _ = c.spillFile.Write(c.tail[:prevTailLen])
			}
		}
	}
	if c.spillFile != nil {
		_, _ = c.spillFile.Write(p)
	}
}

func (c *boundedCollector) appendTailLocked(p []byte) {
	if len(p) >= c.tailLimit {
		c.tail = append(c.tail[:0], p[len(p)-c.tailLimit:]...)
		return
	}
	if len(c.tail)+len(p) <= c.tailLimit {
		c.tail = append(c.tail, p...)
		return
	}
	drop := len(c.tail) + len(p) - c.tailLimit
	copy(c.tail, c.tail[drop:])
	c.tail = append(c.tail[:len(c.tail)-drop], p...)
}

func (c *boundedCollector) Finalize() (output string, spillPath string, total int) {
	var flushed string
	c.mu.Lock()
	if c.streamFilter != nil {
		flushed = c.streamFilter.Flush()
		if flushed != "" {
			c.writeCleanLocked([]byte(flushed))
		}
	}
	if c.spillFile != nil {
		_ = c.spillFile.Sync()
		_ = c.spillFile.Close()
		c.spillFile = nil
	}
	var out string
	if c.total <= c.maxBytes {
		var b strings.Builder
		b.Write(c.head.Bytes())
		b.Write(c.tail)
		out = b.String()
		spillPath = ""
	} else {
		omitted := c.total - c.head.Len() - len(c.tail)
		var b strings.Builder
		b.Write(c.head.Bytes())
		fmt.Fprintf(&b, "\n... [%d bytes omitted] ...\n", omitted)
		b.Write(c.tail)
		out = b.String()
		spillPath = c.spillPath
	}
	total = c.total
	onUpd := c.onUpdate
	if c.redactor != nil {
		out = c.redactor.Redact(out)
	}
	c.mu.Unlock()
	if onUpd != nil && flushed != "" {
		onUpd(flushed)
	}
	return out, spillPath, total
}

func (c *boundedCollector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spillFile != nil {
		_ = c.spillFile.Close()
		c.spillFile = nil
	}
}

var _ io.Writer = (*boundedCollector)(nil)
