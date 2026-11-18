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
