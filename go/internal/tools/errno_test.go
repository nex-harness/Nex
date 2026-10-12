package tools_test

import (
	"os"
	"path/filepath"
	"testing"

	tools2 "github.com/tylinux/nex/go/internal/tools"
)

// TestUnmappedErrnoMessageMatchesNode pins the message Node surfaces for an errno it
// does not map to a specific FileSystemPortError code. toFileSystemError() forwards
// libuv's raw text, so the model sees "ENOTDIR: not a directory, stat '<path>'" —
// Go's own os.PathError text ("stat <path>: not a directory") is a different string
// and would diverge. Found by the write/parent-is-file differential vector.
func TestUnmappedErrnoMessageMatchesNode(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("i am a file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(blocker, "child.txt")

	c := newCtx(t, root)
	_, err := tools2.Write(map[string]any{"file_path": child, "content": "nope\n"}, c)
	if err == nil {
		t.Fatal("expected an error writing under a regular file")
	}
	want := "ENOTDIR: not a directory, stat '" + child + "'"
	if err.Error() != want {
		t.Fatalf("errno message mismatch\n got: %q\nwant: %q", err.Error(), want)
	}
	// It stays an unmapped I/O failure, not one of the codes the tools branch on.
	if code := tools2.ErrorCodeOf(err); code != "io_error" {
		t.Fatalf("code = %q, want io_error", code)
	}
	// The blocking file must be untouched.
	if data, readErr := os.ReadFile(blocker); readErr != nil || string(data) != "i am a file\n" {
		t.Fatalf("blocking file changed: %q (%v)", string(data), readErr)
	}
}
