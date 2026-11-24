package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
)

const (
	DefaultMaxDepth      = 1
	DefaultMaxConcurrent = 6
)

// SpawnRequest models a Subagent invocation (preserving TargetRunnerID for future cross-runner delegation).
type SpawnRequest struct {
	ParentSessionID  string `json:"parent_session_id"`
	ParentDepth      int    `json:"parent_depth"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	TargetRunnerID   string `json:"target_runner_id,omitempty"` // Phase 1: empty / local Runner; reserved for RemoteSpawner
	AgentType        string `json:"agent_type"`                 // "explore" (read-only) | "worker" (read-write) | custom slug
	Model            string `json:"model,omitempty"`            // Optional model override ("provider::model" or model_id); empty inherits main session model
	Prompt           string `json:"prompt"`
	Isolation        string `json:"isolation,omitempty"` // "shared" (default) | "worktree"
	RunInBackground  bool   `json:"run_in_background,omitempty"`
	WorkDir          string `json:"work_dir"`
}

// LocalExecutor runs a child Agent Loop session on the current Runner.
type LocalExecutor interface {
	RunChildSession(ctx context.Context, childSessionID string, req SpawnRequest, readOnly bool, effectiveWorkDir string) (summary string, steerFn func(msg string), err error)
	NotifyParentBackgroundDone(parentSessionID, subagentID, summary string)
}

// RunningSubagentSnapshot describes an active background subagent owned by a parent session.
type RunningSubagentSnapshot struct {
	SubagentID      string
	ParentSessionID string
	SessionID       string
	AgentType       string
	Prompt          string
	StartedAt       int64
}

type runningEntry struct {
	subagentID      string
	parentSessionID string
	sessionID       string
	agentType       string
	prompt          string
	workDir         string
	isWorktree      bool
	branchName      string
	isBackground    bool
	startedAt       int64
	stoppedBy       string
	done            bool
	summary         string
	steer           func(msg string)
	cancel          context.CancelFunc
}

// Manager coordinates local goroutine Subagents with max_depth=1 enforcement, concurrency semaphores, and git worktree isolation.
type Manager struct {
	maxDepth int
	sem      chan struct{}
	exec     LocalExecutor

	mu            sync.Mutex
	entries       map[string]*runningEntry
	onStateChange func(parentSessionID string)
}

// SetOnStateChange registers a callback invoked outside m.mu whenever a background subagent starts or ends.
func (m *Manager) SetOnStateChange(fn func(parentSessionID string)) {
	m.mu.Lock()
	m.onStateChange = fn
	m.mu.Unlock()
}

// NewManager creates a Subagent Manager enforcing maxDepth (default 1) and maxConcurrent (default 6).
func NewManager(exec LocalExecutor, maxDepth, maxConcurrent int) *Manager {
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	return &Manager{
		maxDepth: maxDepth,
		sem:      make(chan struct{}, maxConcurrent),
		exec:     exec,
		entries:  make(map[string]*runningEntry),
	}
}

// ToolsForDepth returns [subagent, send_message] tools ONLY when currentDepth < maxDepth!
// When depth >= 1 (default max_depth=1), subagent tools are physically omitted from the LLM's visible toolset.
func (m *Manager) ToolsForDepth(parentSessionID string, currentDepth int, notifyProgress func(status string)) []tools.Tool {
	if currentDepth >= m.maxDepth {
		return nil
	}
	return []tools.Tool{
		&subagentTool{mgr: m, parentSessionID: parentSessionID, parentDepth: currentDepth, onProgress: notifyProgress},
		&sendMessageTool{mgr: m, parentSessionID: parentSessionID},
	}
}

// Spawn validates depth/target constraints, prepares optional git worktree isolation, and runs synchronously or in background.
func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (string, error) {
	if req.ParentDepth+1 > m.maxDepth {
		return "", fmt.Errorf("subagent depth %d exceeds max_depth=%d (nested subagents are disabled)", req.ParentDepth+1, m.maxDepth)
	}
	if req.TargetRunnerID != "" {
		return "", fmt.Errorf("cross-runner delegation to %q is reserved for future phase; omit target_runner_id for local concurrency", req.TargetRunnerID)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return "", errors.New("prompt is required")
	}

	agentType := strings.ToLower(strings.TrimSpace(req.AgentType))
	if agentType == "" {
		agentType = "worker"
	}
	readOnly := agentType == "explore" || agentType == "reviewer" || agentType == "researcher"

	subID := "sub_" + uuid.NewString()[:8]
	childSessID := "sess_" + subID

	effectiveDir := req.WorkDir
	var branchName string
	isWorktree := false

	if !readOnly && req.Isolation == "worktree" && req.WorkDir != "" {
		wtDir, br, err := createGitWorktree(ctx, req.WorkDir, subID)
		if err != nil {
			return "", fmt.Errorf("git worktree isolation failed: %w", err)
		}
		effectiveDir = wtDir
		branchName = br
		isWorktree = true
	}

	childCtx, cancel := context.WithCancel(ctx)
	entry := &runningEntry{
		subagentID:      subID,
		parentSessionID: req.ParentSessionID,
		sessionID:       childSessID,
		agentType:       agentType,
		prompt:          req.Prompt,
		workDir:         effectiveDir,
		isWorktree:      isWorktree,
		branchName:      branchName,
		isBackground:    req.RunInBackground,
		startedAt:       time.Now().UnixMilli(),
		cancel:          cancel,
	}

	m.mu.Lock()
	m.entries[subID] = entry
	onChangeStart := m.onStateChange
	m.mu.Unlock()
	if req.RunInBackground && onChangeStart != nil {
		onChangeStart(req.ParentSessionID)
	}

	runFn := func(rCtx context.Context) (string, error) {
		select {
		case m.sem <- struct{}{}:
			defer func() { <-m.sem }()
		case <-rCtx.Done():
			return "", rCtx.Err()
		}

		summary, steerFn, err := m.exec.RunChildSession(rCtx, childSessID, req, readOnly, effectiveDir)
		if steerFn != nil {
			m.mu.Lock()
			entry.steer = steerFn
			m.mu.Unlock()
		}

		wtNote := ""
		if isWorktree {
			wtNote = finalizeGitWorktree(req.WorkDir, effectiveDir, branchName)
		}
		finalOut := summary
		if wtNote != "" {
			finalOut += "\n\n" + wtNote
		}

		m.mu.Lock()
		entry.done = true
		entry.summary = finalOut
		onChangeDone := m.onStateChange
		isBg := entry.isBackground
		parentID := entry.parentSessionID
		m.mu.Unlock()
		if isBg && onChangeDone != nil {
			onChangeDone(parentID)
		}
		return finalOut, err
	}

	if req.RunInBackground {
		bgCtx, bgCancel := context.WithCancel(context.Background())
		entry.cancel = bgCancel
		cancel() // Release temporary foreground childCtx
		go func() {
			defer bgCancel()
			out, err := runFn(bgCtx)
			if bgCtx.Err() != nil {
				return
			}
			if err != nil && out == "" {
				out = "Background subagent error: " + err.Error()
			}
			m.exec.NotifyParentBackgroundDone(req.ParentSessionID, subID, out)
		}()
		b, _ := json.Marshal(map[string]any{
			"agent_id":   subID,
			"session_id": childSessID,
			"status":     "running",
			"isolation":  req.Isolation,
		})
		return string(b), nil
	}

	defer cancel()
	return runFn(childCtx)
}

// CancelByParent cancels all non-completed subagents spawned by parentSessionID and returns how many were cancelled.
func (m *Manager) CancelByParent(parentSessionID string) int {
	if parentSessionID == "" {
		return 0
	}
	m.mu.Lock()
	var cancels []context.CancelFunc
	for _, entry := range m.entries {
		if entry != nil && entry.parentSessionID == parentSessionID && !entry.done {
			entry.done = true
			if entry.cancel != nil {
				cancels = append(cancels, entry.cancel)
			}
		}
	}
	m.mu.Unlock()

	for _, c := range cancels {
		c()
	}
	return len(cancels)
}

// ListRunningByParent returns active background subagents spawned by parentSessionID.
func (m *Manager) ListRunningByParent(parentSessionID string) []RunningSubagentSnapshot {
	if parentSessionID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []RunningSubagentSnapshot
	for _, entry := range m.entries {
		if entry != nil && entry.isBackground && !entry.done && entry.parentSessionID == parentSessionID {
			out = append(out, RunningSubagentSnapshot{
				SubagentID:      entry.subagentID,
				ParentSessionID: entry.parentSessionID,
				SessionID:       entry.sessionID,
				AgentType:       entry.agentType,
				Prompt:          entry.prompt,
				StartedAt:       entry.startedAt,
			})
		}
	}
	return out
}

// IsBackgroundSession reports whether sessionID belongs to an active detached background subagent.
func (m *Manager) IsBackgroundSession(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.entries {
		if entry != nil && entry.isBackground && !entry.done && entry.sessionID == sessionID {
			return true
		}
	}
	return false
}

// CancelByID terminates a single running subagent matching subagentID or child sessionID.
func (m *Manager) CancelByID(idOrSessionID, initiator string) (parentSessionID string, ok bool) {
	if idOrSessionID == "" {
		return "", false
	}
	m.mu.Lock()
	var cancel context.CancelFunc
	var onChange func(string)
	for _, entry := range m.entries {
		if entry != nil && !entry.done && (entry.subagentID == idOrSessionID || entry.sessionID == idOrSessionID) {
			entry.done = true
			entry.stoppedBy = initiator
			parentSessionID = entry.parentSessionID
			cancel = entry.cancel
			onChange = m.onStateChange
			ok = true
			break
		}
	}
	m.mu.Unlock()
	if !ok {
		return "", false
	}
	if cancel != nil {
		cancel()
	}
	if onChange != nil && parentSessionID != "" {
		onChange(parentSessionID)
	}
	return parentSessionID, true
}

// CancelAll terminates all running subagents (called during Node shutdown).
func (m *Manager) CancelAll() {
	m.mu.Lock()
	var cancels []context.CancelFunc
	for _, entry := range m.entries {
		if entry != nil && !entry.done {
			entry.done = true
			entry.stoppedBy = "system"
			if entry.cancel != nil {
				cancels = append(cancels, entry.cancel)
			}
		}
	}
	m.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// SendMessage steers a running subagent or resumes a completed subagent session in place.
func (m *Manager) SendMessage(ctx context.Context, subagentID, message string) (string, error) {
	m.mu.Lock()
	entry := m.entries[subagentID]
	m.mu.Unlock()
	if entry == nil {
		return "", fmt.Errorf("subagent %q not found", subagentID)
	}

	if !entry.done && entry.steer != nil {
		entry.steer(message)
		return fmt.Sprintf("Delivered steering message to running subagent %s", subagentID), nil
	}

	// Resume existing child session in place (preserving full SQLite history!)
	out, _, err := m.exec.RunChildSession(ctx, entry.sessionID, SpawnRequest{
		Prompt:  message,
		WorkDir: entry.workDir,
	}, false, entry.workDir)
	return out, err
}

func createGitWorktree(ctx context.Context, repoDir, subID string) (string, string, error) {
	wtRoot := filepath.Join(repoDir, ".nex", "worktrees", subID)
	if err := os.MkdirAll(filepath.Dir(wtRoot), 0o755); err != nil {
		return "", "", err
	}
	branch := "nex/" + subID
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branch, wtRoot, "HEAD")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return wtRoot, branch, nil
}

func finalizeGitWorktree(repoDir, wtDir, branch string) string {
	stCmd := exec.Command("git", "status", "--porcelain")
	stCmd.Dir = wtDir
	stOut, err := stCmd.Output()
	if err == nil && strings.TrimSpace(string(stOut)) == "" {
		// Clean worktree with zero changes: remove automatically!
		rmCmd := exec.Command("git", "worktree", "remove", "--force", wtDir)
		rmCmd.Dir = repoDir
		_ = rmCmd.Run()
		brCmd := exec.Command("git", "branch", "-D", branch)
		brCmd.Dir = repoDir
		_ = brCmd.Run()
		return ""
	}
	return fmt.Sprintf("[Worktree isolation preserved changes at %s on branch %s]", wtDir, branch)
}

// --- LLM Tools (`subagent` & `send_message`) ---

type subagentTool struct {
	mgr             *Manager
	parentSessionID string
	parentDepth     int
	onProgress      func(status string)
}

func (t *subagentTool) Name() string                      { return "subagent" }
func (t *subagentTool) Category() string                  { return "system" }
func (t *subagentTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (t *subagentTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "subagent",
		Category:    "system",
		Description: "Delegate a self-contained task to a concurrent subagent running in its own session.\n\n- Use `subagent` for broad, multi-file exploration, isolated browser automation (`agent_type: \"browser\"`), or parallel independent tasks; it is usually not worth it for a single file or a quick lookup you could do directly.\n- `agent_type: \"explore\"` is read-only and optimized for fast search. `agent_type: \"worker\"` can edit code; add `isolation: \"worktree\"` for parallel edits that must not conflict. `agent_type: \"browser\"` isolates multi-step browser tasks and visual inspection.\n- Give the subagent a self-contained prompt: it cannot see this conversation, so include the goal, relevant paths, and constraints.\n- The subagent's report is NOT shown to the user directly. Relay the key findings in your own final answer.\n- Use `send_message` to steer a running subagent or continue a finished one.\n- Never fabricate a subagent's background result; if it has not reported yet, say so.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"agent_type": {"type": "string", "description": "'explore' (read-only), 'worker' (read-write), or 'browser' (web UI & visual inspection)"},
				"prompt": {"type": "string", "description": "Self-contained task instructions for the subagent"},
				"isolation": {"type": "string", "enum": ["shared", "worktree"], "description": "Use 'worktree' for parallel code edits without file conflicts"},
				"run_in_background": {"type": "boolean", "description": "Run asynchronously in background and return agent_id immediately"},
				"target_runner_id": {"type": "string", "description": "Reserved for future cross-runner delegation (leave empty for local)"}
			},
			"required": ["prompt"]
		}`),
	}
}

func (t *subagentTool) Execute(ctx context.Context, workDir string, rawArgs json.RawMessage, onUpdate tools.UpdateFunc) (tools.Result, error) {
	var req SpawnRequest
	if err := json.Unmarshal(rawArgs, &req); err != nil {
		return tools.Result{Output: "invalid subagent arguments: " + err.Error(), IsError: true}, nil
	}
	req.ParentSessionID = t.parentSessionID
	req.ParentDepth = t.parentDepth
	req.WorkDir = workDir

	if t.onProgress != nil {
		t.onProgress(fmt.Sprintf("subagent (%s) started", req.AgentType))
	}
	if onUpdate != nil {
		onUpdate(fmt.Sprintf("[subagent %s running...]\n", req.AgentType))
	}

	out, err := t.mgr.Spawn(ctx, req)
	if err != nil {
		return tools.Result{Output: err.Error(), IsError: true}, nil
	}
	return tools.Result{Output: out}, nil
}

type sendMessageTool struct {
	mgr             *Manager
	parentSessionID string
}

func (t *sendMessageTool) Name() string                      { return "send_message" }
func (t *sendMessageTool) Category() string                  { return "system" }
func (t *sendMessageTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (t *sendMessageTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "send_message",
		Category:    "system",
		Description: "Send a follow-up or steering message to a subagent by `agent_id`.\n\n- If the subagent is still running, the message is delivered as steering that redirects its current work.\n- If it has finished, the message resumes that subagent's existing session with its full history, instead of spawning a new one.\n- Always relay the resulting findings to the user yourself; the subagent's output is not surfaced to them directly.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"to": {"type": "string", "description": "Target subagent agent_id (e.g. sub_...)"},
				"message": {"type": "string", "description": "Follow-up or steering instruction"}
			},
			"required": ["to", "message"]
		}`),
	}
}

func (t *sendMessageTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ tools.UpdateFunc) (tools.Result, error) {
	var args struct {
		To      string `json:"to"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return tools.Result{Output: err.Error(), IsError: true}, nil
	}
	out, err := t.mgr.SendMessage(ctx, args.To, args.Message)
	if err != nil {
		return tools.Result{Output: err.Error(), IsError: true}, nil
	}
	return tools.Result{Output: out}, nil
}
