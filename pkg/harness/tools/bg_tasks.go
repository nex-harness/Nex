package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/ai"
)

type bgTask struct {
	id        string
	command   string
	workDir   string
	startedAt time.Time
	cancel    context.CancelFunc
	doneCh    chan struct{}

	mu           sync.Mutex
	buf          strings.Builder
	readPos      int
	finished     bool
	exitCode     int
	errMsg       string
	stoppedBy    string
	streamFilter *StreamFilter
}

// BackgroundTaskSnapshot describes the state and tail output of a managed background bash command.
type BackgroundTaskSnapshot struct {
	TaskID     string
	Command    string
	WorkDir    string
	Status     string // "running" | "completed" | "failed" | "killed"
	ExitCode   int
	StartedAt  int64
	StoppedBy  string
	OutputTail string
}

func (t *bgTask) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Cap in-memory buffer at 512KB (keep newest tail)
	const maxBuf = 512 * 1024
	if t.streamFilter != nil {
		if clean := t.streamFilter.Push(string(p)); clean != "" {
			t.buf.WriteString(clean)
		}
	} else {
		t.buf.Write(p)
	}
	if t.buf.Len() > maxBuf {
		s := t.buf.String()
		drop := len(s) - maxBuf
		t.buf.Reset()
		t.buf.WriteString(s[drop:])
		t.readPos -= drop
		if t.readPos < 0 {
			t.readPos = 0
		}
	}
	return len(p), nil
}

// BackgroundTaskManager supervises long-running background bash commands within a session.
type BackgroundTaskManager struct {
	mu         sync.Mutex
	tasks      map[string]*bgTask
	extraEnv   map[string]string
	redactor   *SecretRedactor
	onStart    func(BackgroundTaskSnapshot)
	onComplete func(BackgroundTaskSnapshot)
}

func NewBackgroundTaskManager() *BackgroundTaskManager {
	return &BackgroundTaskManager{
		tasks: make(map[string]*bgTask),
	}
}

// SetCallbacks installs optional lifecycle hooks invoked outside locks when a task starts or terminates.
func (m *BackgroundTaskManager) SetCallbacks(onStart, onComplete func(BackgroundTaskSnapshot)) {
	m.mu.Lock()
	m.onStart = onStart
	m.onComplete = onComplete
	m.mu.Unlock()
}

func (m *BackgroundTaskManager) SetExtraEnv(env map[string]string, redactor *SecretRedactor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.extraEnv = env
	m.redactor = redactor
}

// Start launches command asynchronously and returns its task ID.
func (m *BackgroundTaskManager) Start(workDir, command string, timeout time.Duration) string {
	id := "bg_" + uuid.NewString()[:8]
	var runCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		runCtx, cancel = context.WithCancel(context.Background())
	}

	m.mu.Lock()
	extraEnv := m.extraEnv
	redactor := m.redactor
	var sf *StreamFilter
	if redactor != nil {
		sf = NewStreamFilter(redactor)
	}
	task := &bgTask{
		id:           id,
		command:      command,
		workDir:      workDir,
		startedAt:    time.Now(),
		cancel:       cancel,
		doneCh:       make(chan struct{}),
		streamFilter: sf,
	}
	m.tasks[id] = task
	onStart := m.onStart
	onComplete := m.onComplete
	m.mu.Unlock()

	if onStart != nil {
		onStart(BackgroundTaskSnapshot{
			TaskID:    id,
			Command:   command,
			WorkDir:   workDir,
			Status:    "running",
			StartedAt: task.startedAt.UnixMilli(),
		})
	}

	go func() {
		defer close(task.doneCh)
		defer cancel()
		exitCode, err := runPlatformShellCommand(runCtx, workDir, command, task, extraEnv)
		task.mu.Lock()
		if task.streamFilter != nil {
			if rem := task.streamFilter.Flush(); rem != "" {
				task.buf.WriteString(rem)
			}
		}
		task.finished = true
		task.exitCode = exitCode
		if err != nil {
			task.errMsg = err.Error()
		}
		status := "completed"
		if task.stoppedBy != "" {
			status = "killed"
		} else if exitCode != 0 || err != nil {
			status = "failed"
		}
		tail := task.buf.String()
		const maxTailBytes = 4000
		if len(tail) > maxTailBytes {
			tail = tail[len(tail)-maxTailBytes:]
		}
		snap := BackgroundTaskSnapshot{
			TaskID:     id,
			Command:    command,
			WorkDir:    workDir,
			Status:     status,
			ExitCode:   exitCode,
			StartedAt:  task.startedAt.UnixMilli(),
			StoppedBy:  task.stoppedBy,
			OutputTail: tail,
		}
		task.mu.Unlock()

		if onComplete != nil {
			onComplete(snap)
		}
	}()

	return id
}

// ReadOutput reads incremental or full output from taskID, optionally waiting up to waitDuration.
func (m *BackgroundTaskManager) ReadOutput(ctx context.Context, taskID string, wait bool, waitDuration time.Duration) (output string, status string, exitCode int, err error) {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	m.mu.Unlock()
	if !ok {
		return "", "", 0, fmt.Errorf("background task %q not found", taskID)
	}

	if wait {
		if waitDuration <= 0 {
			waitDuration = 10 * time.Second
		}
		if waitDuration > 60*time.Second {
			waitDuration = 60 * time.Second
		}
		timer := time.NewTimer(waitDuration)
		select {
		case <-task.doneCh:
			timer.Stop()
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return "", "", 0, ctx.Err()
		}
	}

	task.mu.Lock()
	defer task.mu.Unlock()
	full := task.buf.String()
	delta := ""
	if task.readPos < len(full) {
		delta = full[task.readPos:]
		task.readPos = len(full)
	}
	if task.finished {
		status = "completed"
		if task.exitCode != 0 {
			status = "failed"
		}
	} else {
		status = "running"
	}
	return delta, status, task.exitCode, nil
}

// StopWithInitiator terminates a running background task and records who stopped it ("user" or "model").
func (m *BackgroundTaskManager) StopWithInitiator(taskID, initiator string) error {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("background task %q not found", taskID)
	}
	task.mu.Lock()
	if !task.finished && initiator != "" {
		task.stoppedBy = initiator
	}
	task.mu.Unlock()
	task.cancel()
	select {
	case <-task.doneCh:
	case <-time.After(3 * time.Second):
	}
	return nil
}

// Stop terminates a running background task and its process tree.
func (m *BackgroundTaskManager) Stop(taskID string) error {
	return m.StopWithInitiator(taskID, "model")
}

// ListRunning returns snapshots of all background bash tasks still executing.
func (m *BackgroundTaskManager) ListRunning() []BackgroundTaskSnapshot {
	m.mu.Lock()
	tasks := make([]*bgTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		tasks = append(tasks, t)
	}
	m.mu.Unlock()

	var out []BackgroundTaskSnapshot
	for _, t := range tasks {
		t.mu.Lock()
		finished := t.finished
		t.mu.Unlock()
		if !finished {
			out = append(out, BackgroundTaskSnapshot{
				TaskID:    t.id,
				Command:   t.command,
				WorkDir:   t.workDir,
				Status:    "running",
				StartedAt: t.startedAt.UnixMilli(),
			})
		}
	}
	return out
}

// ActiveCount reports how many background bash tasks are currently running.
func (m *BackgroundTaskManager) ActiveCount() int {
	return len(m.ListRunning())
}

// StopAll terminates every running background task managed by this manager.
func (m *BackgroundTaskManager) StopAll() {
	m.mu.Lock()
	list := make([]*bgTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		list = append(list, t)
	}
	m.mu.Unlock()
	for _, t := range list {
		t.mu.Lock()
		if !t.finished && t.stoppedBy == "" {
			t.stoppedBy = "system"
		}
		t.mu.Unlock()
		t.cancel()
	}
}

// TaskOutputTool retrieves incremental output and status of a background bash task.
type TaskOutputTool struct {
	mgr *BackgroundTaskManager
}

func NewTaskOutputTool(mgr *BackgroundTaskManager) *TaskOutputTool {
	return &TaskOutputTool{mgr: mgr}
}

func (t *TaskOutputTool) Name() string                      { return "task_output" }
func (t *TaskOutputTool) Category() string                  { return "system" }
func (t *TaskOutputTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (t *TaskOutputTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "task_output",
		Category:    "system",
		Description: "Read new stdout/stderr output and status from a background task started with `bash` (`run_in_background: true`).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"task_id": {"type": "string", "description": "Background task ID returned by bash"},
				"block": {"type": "boolean", "description": "Whether to wait for task completion up to timeout_ms (default true)"},
				"timeout_ms": {"type": "integer", "description": "Max milliseconds to wait when block=true (default 10000, max 60000)"}
			},
			"required": ["task_id"]
		}`),
	}
}

func (t *TaskOutputTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	if t.mgr == nil {
		return Result{Output: "background task manager not initialized", IsError: true}, nil
	}
	var args struct {
		TaskID    string `json:"task_id"`
		Block     *bool  `json:"block,omitempty"`
		TimeoutMS int    `json:"timeout_ms,omitempty"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid task_output arguments: " + err.Error(), IsError: true}, nil
	}
	block := true
	if args.Block != nil {
		block = *args.Block
	}
	waitDur := time.Duration(args.TimeoutMS) * time.Millisecond
	out, status, exitCode, err := t.mgr.ReadOutput(ctx, strings.TrimSpace(args.TaskID), block, waitDur)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Task %s [%s]", args.TaskID, status)
	if status != "running" {
		fmt.Fprintf(&sb, " (exit_code=%d)", exitCode)
	}
	if strings.TrimSpace(out) != "" {
		sb.WriteString("\n\n")
		sb.WriteString(strings.TrimRight(out, "\n"))
	} else {
		sb.WriteString("\n(no new output)")
	}
	return Result{
		Output:   sb.String(),
		IsError:  status == "failed",
		Metadata: map[string]any{"task_id": args.TaskID, "status": status, "exit_code": exitCode},
	}, nil
}

// TaskStopTool terminates a running background bash task (or fallback subagent task) by ID.
type TaskStopTool struct {
	mgr       *BackgroundTaskManager
	extraStop func(taskID string) (bool, error)
}

func NewTaskStopTool(mgr *BackgroundTaskManager) *TaskStopTool {
	return &TaskStopTool{mgr: mgr}
}

// NewTaskStopToolWithFallback creates a TaskStopTool that falls back to extraStop (e.g., background subagent cancellation).
func NewTaskStopToolWithFallback(mgr *BackgroundTaskManager, extraStop func(taskID string) (bool, error)) *TaskStopTool {
	return &TaskStopTool{mgr: mgr, extraStop: extraStop}
}

func (t *TaskStopTool) Name() string                      { return "task_stop" }
func (t *TaskStopTool) Category() string                  { return "system" }
func (t *TaskStopTool) IsReadOnly(_ json.RawMessage) bool { return false }

func (t *TaskStopTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "task_stop",
		Category:    "system",
		Description: "Terminate a running background command (`bash` `run_in_background: true`) or background subagent by task ID.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"task_id": {"type": "string", "description": "Background task ID or subagent ID to terminate"}
			},
			"required": ["task_id"]
		}`),
	}
}

func (t *TaskStopTool) Execute(_ context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	if t.mgr == nil && t.extraStop == nil {
		return Result{Output: "background task manager not initialized", IsError: true}, nil
	}
	var args struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid task_stop arguments: " + err.Error(), IsError: true}, nil
	}
	taskID := strings.TrimSpace(args.TaskID)
	if t.mgr != nil {
		if err := t.mgr.StopWithInitiator(taskID, "model"); err == nil {
			return Result{
				Output:   fmt.Sprintf("Stopped background task %s", taskID),
				Metadata: map[string]any{"task_id": taskID, "stopped": true},
			}, nil
		} else if t.extraStop == nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
	}
	if t.extraStop != nil {
		handled, err := t.extraStop(taskID)
		if handled {
			if err != nil {
				return Result{Output: err.Error(), IsError: true}, nil
			}
			return Result{
				Output:   fmt.Sprintf("Stopped background task %s", taskID),
				Metadata: map[string]any{"task_id": taskID, "stopped": true},
			}, nil
		}
	}
	return Result{Output: fmt.Sprintf("background task %q not found", taskID), IsError: true}, nil
}
