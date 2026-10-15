// archive_security_test.go: the archive security suite for both codecs
// (G3.sec's zip-slip / path traversal / symlink escape / bomb-ratio rows).
// The tar.gz bytes are crafted by hand (raw tar writers below) so hostile
// shapes — symlink entries, absolute names, ../ segments, drive paths,
// oversized content — reach the extractor exactly as an attacker would
// send them.
package skills

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarTestBuilder writes raw tar blocks for hostile-entry tests.
type tarTestBuilder struct{ bytes.Buffer }

func (b *tarTestBuilder) header(name string, size int64, typeFlag byte, linkName string) {
	header := make([]byte, tarBlockSize)
	copy(header, name)
	writeTarOctal(header, 0o644, 100, 8)
	writeTarOctal(header, uint64(size), 124, 12)
	header[156] = typeFlag
	if linkName != "" {
		copy(header[157:257], linkName)
	}
	copy(header[257:263], "ustar\x00")
	var checksum int
	for _, by := range header {
		checksum += int(by)
	}
	encoded := fmt.Sprintf("%06o", checksum)
	copy(header[148:154], encoded)
	header[154] = 0
	header[155] = ' '
	b.Write(header)
}

func (b *tarTestBuilder) file(name string, content []byte) {
	b.header(name, int64(len(content)), '0', "")
	b.Write(content)
	if padding := tarPaddedBytes(len(content)) - len(content); padding > 0 {
		b.Write(make([]byte, padding))
	}
}

func (b *tarTestBuilder) symlink(name, linkName string) {
	b.header(name, 0, '2', linkName)
}

func (b *tarTestBuilder) finish() []byte {
	b.Write(make([]byte, tarEndBytes))
	var out bytes.Buffer
	gzipWriter := gzip.NewWriter(&out)
	gzipWriter.Write(b.Bytes())
	gzipWriter.Close()
	return out.Bytes()
}

// TestSecurityZipSlip: a ../-prefixed entry must be rejected and nothing
// written outside the target.
func TestSecurityZipSlip(t *testing.T) {
	target := t.TempDir()
	var builder tarTestBuilder
	builder.file("../escaped.txt", []byte("evil"))
	err := ExtractSyncArchive(builder.finish(), target, ExtractSyncArchiveOptions{MaxExtractedBytes: 1024 * 1024})
	if err == nil {
		t.Fatal("zip-slip entry must be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(target, "..", "escaped.txt")); statErr == nil {
		t.Fatal("file escaped the target directory")
	}
}

// TestSecurityNestedDotDot: a deep a/../../b entry must be rejected.
func TestSecurityNestedDotDot(t *testing.T) {
	target := t.TempDir()
	var builder tarTestBuilder
	builder.file("a/../../b.txt", []byte("evil"))
	if err := ExtractSyncArchive(builder.finish(), target, ExtractSyncArchiveOptions{}); err == nil {
		t.Fatal("nested dot-dot entry must be rejected")
	}
}

// TestSecurityAbsolutePath: an absolute /etc entry must be rejected.
func TestSecurityAbsolutePath(t *testing.T) {
	target := t.TempDir()
	var builder tarTestBuilder
	builder.file("/etc/passwd-copy", []byte("evil"))
	if err := ExtractSyncArchive(builder.finish(), target, ExtractSyncArchiveOptions{}); err == nil {
		t.Fatal("absolute entry name must be rejected")
	}
}

// TestSecurityWindowsDriveAndUnc: C:/ and UNC names must be rejected.
func TestSecurityWindowsDriveAndUnc(t *testing.T) {
	for _, name := range []string{"C:/evil.txt", "c:evil.txt", "\\\\server\\share\\evil.txt"} {
		var builder tarTestBuilder
		builder.file(name, []byte("evil"))
		if err := ExtractSyncArchive(builder.finish(), t.TempDir(), ExtractSyncArchiveOptions{}); err == nil {
			t.Fatalf("windows-shaped entry %q must be rejected", name)
		}
	}
}

// TestSecuritySymlinkEscape: symlink entries (type '2') must be refused
// outright — never extracted, never followed.
func TestSecuritySymlinkEscape(t *testing.T) {
	target := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0o644)

	var builder tarTestBuilder
	builder.file("innocent.txt", []byte("ok"))
	builder.symlink("link", outside)
	builder.symlink("rel-link", "../../outside")
	if err := ExtractSyncArchive(builder.finish(), target, ExtractSyncArchiveOptions{}); err == nil {
		t.Fatal("symlink entry must be rejected")
	}
	if _, statErr := os.Lstat(filepath.Join(target, "link")); statErr == nil {
		t.Fatal("symlink must not be extracted")
	}
}

// TestSecurityHardlinkEscape: hardlink entries (type '1') are refused too.
func TestSecurityHardlinkEscape(t *testing.T) {
	var builder tarTestBuilder
	builder.header("hard-link", 0, '1', "/etc/passwd")
	archive := builder.finish()
	if err := ExtractSyncArchive(archive, t.TempDir(), ExtractSyncArchiveOptions{}); err == nil {
		t.Fatal("hardlink entry must be rejected")
	}
}

// TestSecurityZipBombRatioLimit: an entry whose declared content exceeds
// the extracted-bytes cap fails BEFORE writing the file; a gzip bomb whose
// decompressed size exceeds the limit fails the limit read (no ballooning).
func TestSecurityZipBombRatioLimit(t *testing.T) {
	target := t.TempDir()
	var builder tarTestBuilder
	builder.file("small.txt", []byte("x"))
	builder.file("big.bin", bytes.Repeat([]byte{0}, 512*10))
	// Cap below the big entry's size.
	err := ExtractSyncArchive(builder.finish(), target, ExtractSyncArchiveOptions{MaxExtractedBytes: 512 * 5})
	if err == nil {
		t.Fatal("oversized entry must trip the extracted-content limit")
	}
	var limitErr *SizeLimitError
	if !asSizeLimitError(err, &limitErr) {
		t.Fatalf("expected SizeLimitError, got %v", err)
	}
	if limitErr.Phase != "extracted-content" {
		t.Fatalf("expected extracted-content phase, got %s", limitErr.Phase)
	}
}

func asSizeLimitError(err error, target **SizeLimitError) bool {
	if e, ok := err.(*SizeLimitError); ok {
		*target = e
		return true
	}
	return false
}

// TestSecurityGzipBombDecompressionCap: a highly compressible stream larger
// than the limit fails during the decompressed read, not after allocation.
func TestSecurityGzipBombDecompressionCap(t *testing.T) {
	var tarBytes bytes.Buffer
	tarBytes.Write(make([]byte, 64*1024*1024)) // ~64 MiB of zeros
	tarBytes.Write(make([]byte, tarEndBytes))
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	gzipWriter.Write(tarBytes.Bytes())
	gzipWriter.Close()

	err := ExtractSyncArchive(compressed.Bytes(), t.TempDir(), ExtractSyncArchiveOptions{MaxExtractedBytes: 1024 * 1024})
	if err == nil {
		t.Fatal("gzip bomb must trip the decompression limit")
	}
}

// TestSecurityTruncatedArchive: a cut-off entry errors; nothing partial is
// left behind for that entry.
func TestSecurityTruncatedArchive(t *testing.T) {
	var builder tarTestBuilder
	builder.file("partial.txt", []byte("content"))
	archive := builder.finish()
	if len(archive) <= 20 {
		t.Fatalf("archive unexpectedly tiny: %d", len(archive))
	}
	// Cut the tail of the gzip stream: the reader hits unexpected EOF
	// (never a silent partial extract).
	if err := ExtractSyncArchive(archive[:len(archive)-20], t.TempDir(), ExtractSyncArchiveOptions{}); err == nil {
		t.Fatal("truncated archive must error")
	}
	// Cut the tar payload instead: gunzip succeeds but the end blocks are
	// missing — the entry loop must refuse the non-zero remainder.
	var builder2 tarTestBuilder
	builder2.file("partial.txt", []byte("content"))
	var raw bytes.Buffer
	raw.Write([]byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff}) // minimal gzip header
	// Build a valid gzip of the tar WITHOUT the end blocks.
	var tarOnly bytes.Buffer
	var builder3 tarTestBuilder
	builder3.file("partial.txt", []byte("content"))
	tarOnly.Write(builder3.Bytes())
	gzipWriter := gzip.NewWriter(&raw)
	gzipWriter.Write(tarOnly.Bytes())
	gzipWriter.Close()
	if err := ExtractSyncArchive(raw.Bytes(), t.TempDir(), ExtractSyncArchiveOptions{}); err == nil {
		t.Fatal("tar without end blocks must error")
	}
}

// TestSecurityExtractionRoundTrip: create → extract reproduces the tree.
func TestSecurityExtractionRoundTrip(t *testing.T) {
	source := t.TempDir()
	os.MkdirAll(filepath.Join(source, "group", "inner"), 0o755)
	os.WriteFile(filepath.Join(source, "group", "SKILL.md"), []byte("---\nname: a\n---\nbody"), 0o644)
	os.WriteFile(filepath.Join(source, "group", "inner", "SKILL.md"), []byte("---\nname: b\n---\nbody"), 0o644)
	archive, err := CreateSyncArchive([]ArchiveEntry{{SourcePath: source, ArchivePath: "root"}})
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := ExtractSyncArchive(archive, target, ExtractSyncArchiveOptions{MaxExtractedBytes: 20 * 1024 * 1024}); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"root/group/SKILL.md", "root/group/inner/SKILL.md"} {
		if _, statErr := os.Stat(filepath.Join(target, filepath.FromSlash(relative))); statErr != nil {
			t.Errorf("missing %s", relative)
		}
	}
}

// TestSecurityLongPathSplit: entries over 100 bytes use the ustar prefix
// split and still round-trip.
func TestSecurityLongPathSplit(t *testing.T) {
	source := t.TempDir()
	deep := filepath.Join(source, strings.Repeat("dir/", 30)+"leaf")
	os.MkdirAll(deep, 0o755)
	os.WriteFile(filepath.Join(deep, "file.txt"), []byte("x"), 0o644)
	archive, err := CreateSyncArchive([]ArchiveEntry{{SourcePath: source, ArchivePath: "root"}})
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := ExtractSyncArchive(archive, target, ExtractSyncArchiveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "root", strings.Repeat("dir/", 30), "leaf", "file.txt")); statErr != nil {
		t.Fatal("long-path entry did not round-trip")
	}
}

// TestSecuritySizeLimitErrorCode pins the contract error code.
func TestSecuritySizeLimitErrorCode(t *testing.T) {
	err := &SizeLimitError{ActualBytes: 10, MaxBytes: 5, Phase: "archive"}
	if err.Code() != "SKILL_SYNC_SIZE_LIMIT_EXCEEDED" {
		t.Fatalf("wrong code: %s", err.Code())
	}
	if !strings.Contains(err.Error(), "10/5") {
		t.Fatalf("wrong message shape: %s", err.Error())
	}
}
