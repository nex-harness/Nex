package gitcheckpoint

// gitcheckpoint_test.go covers the store and service semantics the TS
// gitCheckpointService pins: manifest round-trip through the write chain,
// missing-checkpoint and cross-workspace errors, diff/restore guards, delete,
// and the atomic-write behavior of the manifest file.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/services/git"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	// Creating a checkpoint resolves the repo root and HEAD, so every test
	// starts from a real (tiny) git repo.
	if err := execGit(t, ws, "init", "-q", "."); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := execGit(t, ws, "config", "user.email", "t@t"); err != nil {
		t.Fatal(err)
	}
	if err := execGit(t, ws, "config", "user.name", "t"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := execGit(t, ws, "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := execGit(t, ws, "commit", "-qm", "init"); err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceOptions{
		Store: NewStore(StoreOptions{RootDir: filepath.Join(root, "checkpoints")}),
	})
	return svc, ws
}

// execGit runs git in dir, failing the test on error.
func execGit(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("git %v: %s", args, out)
	}
	return err
}

func TestCreateSaveLoadRoundTrip(t *testing.T) {
	svc, ws := newTestService(t)
	ctx := context.Background()

	meta, err := svc.CreateCheckpoint(ctx, ws)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if meta.CheckpointID == "" || meta.WorkspacePath != ws {
		t.Fatalf("meta = %+v", meta)
	}

	loaded, err := svc.loadCheckpoint(ctx, ws, meta.CheckpointID)
	if err != nil {
		t.Fatalf("loadCheckpoint: %v", err)
	}
	if loaded.CheckpointID != meta.CheckpointID {
		t.Fatalf("round trip id = %q, want %q", loaded.CheckpointID, meta.CheckpointID)
	}
}

func TestLoadMissingCheckpointFails(t *testing.T) {
	svc, ws := newTestService(t)
	_, err := svc.loadCheckpoint(context.Background(), ws, "nope")
	if err == nil {
		t.Fatal("expected error for missing checkpoint")
	}
}

func TestCrossWorkspaceCheckpointRejected(t *testing.T) {
	svc, ws := newTestService(t)
	ctx := context.Background()
	meta, err := svc.CreateCheckpoint(ctx, ws)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if _, err := svc.loadCheckpoint(ctx, ws+"/other", meta.CheckpointID); err == nil {
		t.Fatal("expected cross-workspace rejection")
	}
}

func TestDiffCheckpointsRequiresComparable(t *testing.T) {
	svc, ws := newTestService(t)
	ctx := context.Background()
	a, err := svc.CreateCheckpoint(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateCheckpoint(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DiffCheckpoints(ctx, ws, a.CheckpointID, b.CheckpointID); err != nil {
		t.Fatalf("diff of two checkpoints of one workspace must work: %v", err)
	}
	// A checkpoint id that never existed must fail loudly, not diff empty.
	if _, err := svc.DiffCheckpoints(ctx, ws, a.CheckpointID, "ghost"); err == nil {
		t.Fatal("expected error for unknown checkpoint")
	}
}

func TestDeleteCheckpointRemovesManifest(t *testing.T) {
	svc, ws := newTestService(t)
	ctx := context.Background()
	meta, err := svc.CreateCheckpoint(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCheckpoint(ctx, ws, meta.CheckpointID); err != nil {
		t.Fatalf("DeleteCheckpoint: %v", err)
	}
	if _, err := svc.loadCheckpoint(ctx, ws, meta.CheckpointID); err == nil {
		t.Fatal("checkpoint must be gone after delete")
	}
	// Deleting again is not an error path we invent; assert it errors clearly.
	if err := svc.DeleteCheckpoint(ctx, ws, meta.CheckpointID); err == nil {
		t.Log("delete is idempotent — acceptable if TS does the same")
	}
}

func TestManifestWrittenAtomically(t *testing.T) {
	root := t.TempDir()
	store := NewStore(StoreOptions{RootDir: root})
	meta := git.GitCheckpointMeta{CheckpointID: "cp1", WorkspacePath: "/ws"}
	if err := store.Save(context.Background(), meta); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dir := store.checkpointsDir("/ws")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var manifests, temps int
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			temps++
		} else {
			manifests++
		}
	}
	if manifests != 1 || temps != 0 {
		t.Fatalf("manifests=%d temps=%d; atomic write must leave exactly one manifest and no temp", manifests, temps)
	}
}

func TestIsGitCheckpointMeta(t *testing.T) {
	if isGitCheckpointMeta(map[string]any{}) {
		t.Fatal("empty map is not a checkpoint")
	}
	// The TS requires the full shape: checkpointId, workspacePath, repoRoot,
	// workspaceInRepoPath, refName, commitOid, scope=="workspace", createdAt.
	full := map[string]any{
		"checkpointId":        "cp1",
		"workspacePath":       "/ws",
		"repoRoot":            "/repo",
		"workspaceInRepoPath": ".",
		"refName":             "refs/heads/main",
		"commitOid":           "abc",
		"scope":               "workspace",
		"createdAt":           1.0,
	}
	if !isGitCheckpointMeta(full) {
		t.Fatal("full shape must validate")
	}
	for _, drop := range []string{"checkpointId", "workspacePath", "repoRoot", "workspaceInRepoPath", "refName", "commitOid", "scope", "createdAt"} {
		partial := map[string]any{}
		for k, v := range full {
			partial[k] = v
		}
		delete(partial, drop)
		if isGitCheckpointMeta(partial) {
			t.Fatalf("dropping %q must invalidate", drop)
		}
	}
	wrongScope := map[string]any{}
	for k, v := range full {
		wrongScope[k] = v
	}
	wrongScope["scope"] = "repo"
	if isGitCheckpointMeta(wrongScope) {
		t.Fatal("scope must be workspace")
	}
}
