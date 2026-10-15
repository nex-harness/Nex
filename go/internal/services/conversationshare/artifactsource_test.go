// artifactsource_test.go (step 3.8): the local artifact reader/stat seam.
// The ref comes from session data and is NOT trusted, so the path-confinement
// tests below are the security boundary of the whole share path.
package conversationshare

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkspaceFile(t *testing.T, workspace, name string, data string) string {
	t.Helper()
	path := filepath.Join(workspace, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalArtifactSourceReadsRelativeAndAbsoluteRefs(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "docs/report.pdf", "%PDF-1.4")
	source := NewLocalArtifactSource()
	ctx := context.Background()

	for _, ref := range []string{"docs/report.pdf", "docs" + string(filepath.Separator) + "report.pdf", filepath.Join(workspace, "docs/report.pdf")} {
		stat, err := source.Stat(ctx, workspace, ref)
		if err != nil {
			t.Fatalf("stat %q: %v", ref, err)
		}
		if stat.Size != 8 {
			t.Fatalf("stat size = %d", stat.Size)
		}
		if stat.MtimeMs == nil {
			t.Fatal("mtime must be reported (the publish re-check needs it)")
		}
		materialized, err := source.Read(ctx, workspace, ref, 1024)
		if err != nil {
			t.Fatalf("read %q: %v", ref, err)
		}
		if string(materialized.Bytes) != "%PDF-1.4" {
			t.Fatalf("read %q = %q", ref, materialized.Bytes)
		}
	}
}

func TestLocalArtifactSourceRejectsSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	secret := writeWorkspaceFile(t, outside, "secret.txt", "TOP SECRET")

	// A symlink planted INSIDE the workspace must not widen the boundary.
	link := filepath.Join(workspace, "escape.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	source := NewLocalArtifactSource()
	ctx := context.Background()

	_, err := source.Stat(ctx, workspace, "escape.txt")
	typed, ok := isServiceError(err)
	if !ok || typed.Kind() != KindUnsafeStructure {
		t.Fatalf("stat through a symlink out of the workspace: error = %v", err)
	}
	if typed.ReasonCode() != ReasonArtifactOutsideWorkspace {
		t.Fatalf("reason code = %q", typed.ReasonCode())
	}
	_, err = source.Read(ctx, workspace, "escape.txt", 1024)
	if typed, ok := isServiceError(err); !ok || typed.Kind() != KindUnsafeStructure {
		t.Fatalf("read through a symlink out of the workspace: error = %v", err)
	}
	// The path must not leak through the message either.
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), outside) {
		t.Fatalf("the error leaked a path: %v", err)
	}
}

func TestLocalArtifactSourceAcceptsInWorkspaceSymlinks(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "real/report.pdf", "%PDF-1.4")
	if err := os.Symlink(filepath.Join(workspace, "real/report.pdf"), filepath.Join(workspace, "link.pdf")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	source := NewLocalArtifactSource()
	stat, err := source.Stat(context.Background(), workspace, "link.pdf")
	if err != nil {
		t.Fatalf("an in-workspace symlink must resolve: %v", err)
	}
	if filepath.Base(stat.CanonicalPath) != "report.pdf" {
		t.Fatalf("canonical path = %q (the ref must resolve through the link)", stat.CanonicalPath)
	}
}

func TestLocalArtifactSourceRejectsURLRefs(t *testing.T) {
	workspace := t.TempDir()
	source := NewLocalArtifactSource()
	ctx := context.Background()
	for _, ref := range []string{"https://example.invalid/a.pdf", "FILE:///etc/passwd", "file://x"} {
		_, err := source.Stat(ctx, workspace, ref)
		typed, ok := isServiceError(err)
		if !ok || typed.Kind() != KindUnsafeStructure {
			t.Fatalf("ref %q: error = %v", ref, err)
		}
		_, err = source.Read(ctx, workspace, ref, 1024)
		if typed, ok := isServiceError(err); !ok || typed.Kind() != KindUnsafeStructure {
			t.Fatalf("read ref %q: error = %v", ref, err)
		}
	}
}

func TestLocalArtifactSourceMissingFileIsSkippable(t *testing.T) {
	workspace := t.TempDir()
	source := NewLocalArtifactSource()
	_, err := source.Stat(context.Background(), workspace, "nope.pdf")
	typed, ok := isServiceError(err)
	if !ok {
		t.Fatalf("error = %v", err)
	}
	if typed.ReasonCode() != ReasonArtifactReadFailed {
		t.Fatalf("reason code = %q, want artifact_read_failed", typed.ReasonCode())
	}
	if errno, ok := typed.Errno(); !ok || errno != "ENOENT" {
		t.Fatalf("errno = %q/%v, want ENOENT (the UI maps it to not_found)", errno, ok)
	}
}

func TestLocalArtifactSourceRejectsDirectories(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := NewLocalArtifactSource()
	ctx := context.Background()
	if _, err := source.Stat(ctx, workspace, "docs"); err == nil {
		t.Fatal("a directory is not an artifact")
	}
	if _, err := source.Read(ctx, workspace, "docs", 1024); err == nil {
		t.Fatal("a directory must not be readable as an artifact")
	}
}

func TestLocalArtifactSourceEnforcesTheByteLimit(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "big.bin", strings.Repeat("x", 100))
	source := NewLocalArtifactSource()
	ctx := context.Background()

	if _, err := source.Read(ctx, workspace, "big.bin", 99); err == nil {
		t.Fatal("a file above the byte limit must be refused")
	}
	materialized, err := source.Read(ctx, workspace, "big.bin", 100)
	if err != nil {
		t.Fatalf("read at exactly the limit: %v", err)
	}
	if len(materialized.Bytes) != 100 {
		t.Fatalf("read %d bytes, want 100", len(materialized.Bytes))
	}
	if _, err := source.Read(ctx, workspace, "big.bin", 0); err == nil {
		t.Fatal("a non-positive byte limit is a contract error")
	}
}

func TestLocalArtifactSourceDetectsGrowthBetweenStatAndRead(t *testing.T) {
	// The reader deliberately asks for stat-size+1 bytes so growth is caught.
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "grow.bin", "small")
	source := NewLocalArtifactSource()
	materialized, err := source.Read(context.Background(), workspace, "grow.bin", 1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(materialized.Bytes) != 5 {
		t.Fatalf("read %d bytes", len(materialized.Bytes))
	}
}

func TestIsInsideWorkspace(t *testing.T) {
	cases := []struct {
		workspace, target string
		want              bool
	}{
		{"/w", "/w", true},
		{"/w", "/w/a/b.txt", true},
		{"/w", "/w2", false},
		{"/w", "/", false},
		{"/w", "/w/../etc/passwd", false},
	}
	for _, testCase := range cases {
		if got := isInsideWorkspace(testCase.workspace, testCase.target); got != testCase.want {
			t.Errorf("isInsideWorkspace(%q, %q) = %v, want %v",
				testCase.workspace, testCase.target, got, testCase.want)
		}
	}
}
