package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	tools2 "github.com/tylinux/nex/go/internal/tools"
)

// TestAtomicWriteLeavesNoTempFiles verifies the atomic write path renames a temp
// file into place and cleans up after itself.
func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	c := newCtx(t, root)
	target := filepath.Join(root, "sub", "f.txt")
	if _, err := tools2.Write(map[string]any{"file_path": target, "content": "hello\n"}, c); err != nil {
		t.Fatal(err)
	}
	// Rewrite an existing file too.
	if _, err := tools2.Write(map[string]any{"file_path": target, "content": "world\n"}, c); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "f.txt" {
			t.Fatalf("leftover temp file: %q", e.Name())
		}
	}
}

// TestWritePreservesMode verifies an executable bit survives an overwrite.
func TestWritePreservesMode(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "s.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	c := newCtx(t, root)
	if _, err := tools2.Read(map[string]any{"file_path": script}, c); err != nil {
		t.Fatal(err)
	}
	if _, err := tools2.Write(map[string]any{"file_path": script, "content": "#!/bin/sh\necho hi\n"}, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode not preserved: got %o want 755", info.Mode().Perm())
	}
}

// TestReadCancellation verifies a cancelled context reaches the adapter and is
// reported with the "cancelled" code, exactly like Node's throwIfAborted(signal)
// at the start of a range read — the tool must surface it, not read anyway.
func TestReadCancellation(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fxp := fsx.NewPort()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := fxp.ReadTextRange(ctx, f, nil, nil, 0); err == nil {
		t.Fatal("expected cancellation error, got nil")
	} else if tools2.ErrorCodeOf(err) != "cancelled" {
		t.Fatalf("expected code cancelled, got %q (%v)", tools2.ErrorCodeOf(err), err)
	}

	// The tool surfaces the cancellation as an error rather than an empty result.
	c := &tools2.Context{
		FS:               fxp,
		ReadState:        tools2.NewReadState(),
		WorkingDirectory: root,
		WorkspaceRoot:    root,
		SessionID:        "sess_test",
		Ctx:              ctx,
	}
	if _, err := tools2.Read(map[string]any{"file_path": f}, c); err == nil {
		t.Fatal("expected Read to fail on a cancelled context")
	}
}
