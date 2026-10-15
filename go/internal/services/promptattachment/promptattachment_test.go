// promptattachment_test.go (step 3.8): the local transfer service — zero-copy
// stage (ref IS localPath, staged=false), size from params over stat, the
// missing-file size fallback, and the no-op lifecycle methods.
package promptattachment

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStageZeroCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	os.WriteFile(path, []byte(make([]byte, 1024)), 0o644)
	service := New()
	result, err := service.Stage(context.Background(), StageParams{
		OperationID: "op-1", SessionID: "s-1", WorkspacePath: dir,
		LocalPath: path, FileName: "doc.pdf", Mime: "application/pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ref != path || result.Staged != false || result.OperationID != "op-1" {
		t.Fatalf("result = %+v", result)
	}
	if result.Bytes != 1024 {
		t.Fatalf("bytes = %d, want 1024 from stat", result.Bytes)
	}
}

func TestStageExplicitSizeWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	os.WriteFile(path, []byte(make([]byte, 10)), 0o644)
	service := New()
	result, err := service.Stage(context.Background(), StageParams{
		OperationID: "op-1", LocalPath: path, SizeBytes: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != 5 {
		t.Fatalf("bytes = %d, want the explicit 5", result.Bytes)
	}
}

func TestStageMissingFileFallsBackToZero(t *testing.T) {
	service := New()
	result, err := service.Stage(context.Background(), StageParams{
		OperationID: "op-1", LocalPath: "/nonexistent/file.bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != 0 {
		t.Fatalf("bytes = %d, want 0 for a missing file", result.Bytes)
	}
}

func TestLifecycleNoOps(t *testing.T) {
	service := New()
	service.Adopt(context.Background(), "op-1")
	service.Cancel(context.Background(), "op-1")
	service.Cleanup(context.Background(), "op-1")
	// repeated calls stay safe
	service.Cleanup(context.Background(), "op-1")
}
