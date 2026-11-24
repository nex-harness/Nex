package subagent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubSubExecutor struct {
	mu          sync.Mutex
	bgCompleted chan string
}

func (s *stubSubExecutor) RunChildSession(_ context.Context, _ string, req SpawnRequest, readOnly bool, effectiveWorkDir string) (string, func(string), error) {
	if !readOnly && strings.Contains(req.Prompt, "write_isolated") {
		_ = os.WriteFile(filepath.Join(effectiveWorkDir, "isolated.txt"), []byte("worktree_data\n"), 0o644)
	}
	return "done:" + req.Prompt, func(_ string) {}, nil
}

func (s *stubSubExecutor) NotifyParentBackgroundDone(_, subID, summary string) {
	if s.bgCompleted != nil {
		s.bgCompleted <- subID + ":" + summary
	}
}

func TestSubagentMaxDepthEnforcementWorktreeIsolationAndBackground(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Init git repo for worktree isolation test
	gitRun := func(args ...string) {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, string(out))
		}
	}
	gitRun("git", "init")
	gitRun("git", "config", "user.name", "Test")
	gitRun("git", "config", "user.email", "test@example.com")
	_ = os.WriteFile(filepath.Join(dir, "base.txt"), []byte("root\n"), 0o644)
	gitRun("git", "add", "base.txt")
	gitRun("git", "commit", "--no-verify", "-m", "base")

	execStub := &stubSubExecutor{bgCompleted: make(chan string, 2)}
	mgr := NewManager(execStub, 1, 4)

	// 1. Dual max_depth=1 enforcement: depth=0 exposes [subagent, send_message]; depth=1 returns nil!
	if len(mgr.ToolsForDepth("sess_root", 0, nil)) != 2 {
		t.Fatalf("expected 2 subagent tools at depth=0")
	}
	if len(mgr.ToolsForDepth("sess_child", 1, nil)) != 0 {
		t.Fatalf("subagent tools MUST be physically stripped at depth >= 1!")
	}
	if _, err := mgr.Spawn(ctx, SpawnRequest{ParentDepth: 1, Prompt: "nested"}); err == nil || !strings.Contains(err.Error(), "max_depth=1") {
		t.Fatalf("expected kernel max_depth=1 rejection, got %v", err)
	}

	// 2. Parallel git worktree isolation: writes inside worktree without touching main workspace!
	out, err := mgr.Spawn(ctx, SpawnRequest{
		ParentSessionID: "sess_root",
		ParentDepth:     0,
		AgentType:       "worker",
		Prompt:          "write_isolated",
		Isolation:       "worktree",
		WorkDir:         dir,
	})
	if err != nil {
		t.Fatalf("worktree spawn failed: %v", err)
	}
	if !strings.Contains(out, "Worktree isolation preserved changes") {
		t.Fatalf("expected worktree preservation notice, got %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "isolated.txt")); !os.IsNotExist(err) {
		t.Fatalf("worktree isolation leaked isolated.txt into root workspace!")
	}

	// 3. Background async spawn + completion notification + send_message resume
	bgOut, err := mgr.Spawn(ctx, SpawnRequest{
		ParentSessionID: "sess_root",
		ParentDepth:     0,
		AgentType:       "explore",
		Prompt:          "scan repo",
		RunInBackground: true,
		WorkDir:         dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	var bgMeta struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal([]byte(bgOut), &bgMeta)

	select {
	case note := <-execStub.bgCompleted:
		if !strings.Contains(note, "done:scan repo") {
			t.Fatalf("unexpected background completion note: %q", note)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for background subagent")
	}

	resumed, err := mgr.SendMessage(ctx, bgMeta.AgentID, "follow-up question")
	if err != nil || !strings.Contains(resumed, "done:follow-up question") {
		t.Fatalf("SendMessage resume failed: %q err=%v", resumed, err)
	}
}

type blockingSubExecutor struct {
	started     chan struct{}
	canceled    chan struct{}
	bgCompleted chan string
}

func (b *blockingSubExecutor) RunChildSession(ctx context.Context, _ string, _ SpawnRequest, _ bool, _ string) (string, func(string), error) {
	close(b.started)
	<-ctx.Done()
	close(b.canceled)
	return "", nil, ctx.Err()
}

func (b *blockingSubExecutor) NotifyParentBackgroundDone(_, subID, summary string) {
	if b.bgCompleted != nil {
		b.bgCompleted <- subID + ":" + summary
	}
}

func TestManagerCancelByParentStopsBackgroundSubagents(t *testing.T) {
	exec := &blockingSubExecutor{
		started:     make(chan struct{}),
		canceled:    make(chan struct{}),
		bgCompleted: make(chan string, 1),
	}
	mgr := NewManager(exec, 1, 2)

	if _, err := mgr.Spawn(context.Background(), SpawnRequest{
		ParentSessionID: "sess_parent_abort",
		ParentDepth:     0,
		AgentType:       "explore",
		Prompt:          "long background scan",
		RunInBackground: true,
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-exec.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background subagent to start")
	}

	if cancelled := mgr.CancelByParent("sess_parent_abort"); cancelled != 1 {
		t.Fatalf("expected 1 cancelled subagent, got %d", cancelled)
	}

	select {
	case <-exec.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background subagent context cancellation")
	}

	// Ensure cancelled background subagent does NOT invoke NotifyParentBackgroundDone
	select {
	case msg := <-exec.bgCompleted:
		t.Fatalf("expected cancelled background subagent NOT to notify parent, got %q", msg)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestManagerCancelByIDAndListRunningByParent(t *testing.T) {
	exec := &blockingSubExecutor{
		started:     make(chan struct{}),
		canceled:    make(chan struct{}),
		bgCompleted: make(chan string, 1),
	}
	mgr := NewManager(exec, 1, 2)
	stateChanges := make(chan string, 4)
	mgr.SetOnStateChange(func(parentSessionID string) {
		stateChanges <- parentSessionID
	})

	out, err := mgr.Spawn(context.Background(), SpawnRequest{
		ParentSessionID: "sess_parent_list",
		ParentDepth:     0,
		AgentType:       "explore",
		Prompt:          "watch workflow run",
		RunInBackground: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal([]byte(out), &meta)

	select {
	case <-exec.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background subagent")
	}

	running := mgr.ListRunningByParent("sess_parent_list")
	if len(running) != 1 || running[0].SubagentID != meta.AgentID {
		t.Fatalf("expected 1 running subagent for parent, got %+v", running)
	}

	parentID, ok := mgr.CancelByID(meta.AgentID, "user")
	if !ok || parentID != "sess_parent_list" {
		t.Fatalf("expected CancelByID ok=true parent=sess_parent_list, got ok=%v parent=%q", ok, parentID)
	}
	select {
	case <-exec.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subagent cancel")
	}
	if len(mgr.ListRunningByParent("sess_parent_list")) != 0 {
		t.Fatalf("expected 0 running subagents after CancelByID, got %+v", mgr.ListRunningByParent("sess_parent_list"))
	}
	if len(stateChanges) == 0 {
		t.Fatal("expected onStateChange callbacks to fire")
	}
}
