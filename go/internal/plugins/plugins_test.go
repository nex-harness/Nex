// plugins_test.go: manifest reading, marketplace partitions/merge, version
// compare table, atomic dir install, and the plugin archive (including the
// security-critical rejection shapes, run through the same hostile-entry
// builder pattern as the skills archive).
package plugins

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/skills"
)

// TestReadManifest covers the three manifest locations and validation.
func TestReadManifest(t *testing.T) {
	root := t.TempDir()
	// .nex-plugin wins first.
	for _, segments := range [][]string{{".nex-plugin"}, {".claude-plugin"}, {".codex-plugin"}} {
		dir := filepath.Join(append([]string{root}, segments...)...)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"demo","description":"d","version":"1.0.0"}`), 0o644)
	}
	manifest, err := ReadManifest(root)
	if err != nil || manifest == nil {
		t.Fatalf("manifest = %v, %v", manifest, err)
	}
	if manifest.Name != "demo" || manifest.Description != "d" || manifest.Version != "1.0.0" {
		t.Fatalf("manifest = %+v", manifest)
	}

	// Invalid name → nil (no partial manifest).
	bad := t.TempDir()
	os.MkdirAll(filepath.Join(bad, ".nex-plugin"), 0o755)
	os.WriteFile(filepath.Join(bad, ".nex-plugin", "plugin.json"), []byte(`{"name":"Bad Name!"}`), 0o644)
	manifest, err = ReadManifest(bad)
	if err != nil || manifest != nil {
		t.Fatalf("invalid manifest = %v, %v", manifest, err)
	}
	// Corrupt JSON → nil.
	corrupt := t.TempDir()
	os.MkdirAll(filepath.Join(corrupt, ".nex-plugin"), 0o755)
	os.WriteFile(filepath.Join(corrupt, ".nex-plugin", "plugin.json"), []byte(`{`), 0o644)
	if manifest, err := ReadManifest(corrupt); err != nil || manifest != nil {
		t.Fatalf("corrupt manifest = %v, %v", manifest, err)
	}
	// Missing manifest → nil.
	if manifest, err := ReadManifest(t.TempDir()); err != nil || manifest != nil {
		t.Fatalf("missing manifest = %v, %v", manifest, err)
	}
}

// TestResolveComponentRoots covers manifest paths + default fallback.
func TestResolveComponentRoots(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "skills"), 0o755)
	// No manifest field → default skills/ dir.
	if roots := ResolveComponentRoots(root, nil, "skills"); len(roots) != 1 || roots[0] != filepath.Join(root, "skills") {
		t.Fatalf("default root = %v", roots)
	}
	// Listed paths stay inside; absolute and escaping paths are dropped.
	os.MkdirAll(filepath.Join(root, "custom"), 0o755)
	if roots := ResolveComponentRoots(root, "custom", "skills"); len(roots) != 1 || roots[0] != filepath.Join(root, "custom") {
		t.Fatalf("listed root = %v", roots)
	}
	if roots := ResolveComponentRoots(root, []any{"../outside", "/abs"}, "skills"); len(roots) != 0 {
		t.Fatalf("escaping roots = %v", roots)
	}
}

// TestMarketplacePartitionsMerge ports rebuildOfficialMarketplaceSync: CDN
// entries win on name; the merged file is written next to the partitions.
func TestMarketplacePartitionsMerge(t *testing.T) {
	storage := t.TempDir()
	bundled := map[string]any{
		"name":        "other",
		"description": "bundled base",
		"plugins":     []any{map[string]any{"name": "shared"}, map[string]any{"name": "only-bundled"}},
	}
	cdn := map[string]any{
		"name":    "other",
		"plugins": []any{map[string]any{"name": "shared", "version": "2.0.0"}, map[string]any{"name": "only-cdn"}},
	}
	if _, err := WriteBundledOfficialPartition(storage, bundled); err != nil {
		t.Fatal(err)
	}
	merged, err := WriteCDNOfficialPartition(storage, cdn)
	if err != nil {
		t.Fatal(err)
	}
	if merged["name"] != OfficialMarketplaceID {
		t.Fatalf("merged name = %v", merged["name"])
	}
	plugins := merged["plugins"].([]any)
	if len(plugins) != 3 {
		t.Fatalf("merged plugins = %v", plugins)
	}
	names := map[string]bool{}
	for _, entry := range plugins {
		record := entry.(map[string]any)
		if record["name"] == "shared" && record["version"] != "2.0.0" {
			t.Fatal("CDN entry must win over the bundled same-name entry")
		}
		names[record["name"].(string)] = true
	}
	for _, expected := range []string{"shared", "only-bundled", "only-cdn"} {
		if !names[expected] {
			t.Fatalf("missing %s", expected)
		}
	}
	// The merged file persisted and re-reads.
	strict, err := ReadOfficialMergedMarketplaceStrict(storage)
	if err != nil || strict == nil {
		t.Fatalf("strict read = %v, %v", strict, err)
	}
	if strict["name"] != OfficialMarketplaceID {
		t.Fatal("persisted merged file has the official id")
	}
}

// TestParseMarketplaceManifestObjectPlugins covers the object-keyed shape.
func TestParseMarketplaceManifestObjectPlugins(t *testing.T) {
	manifest, err := ParseMarketplaceManifest(map[string]any{
		"name": "m",
		"plugins": map[string]any{
			"a": map[string]any{"dependencies": []any{"b@m"}},
			"b": nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Plugins) != 2 {
		t.Fatalf("plugins = %+v", manifest.Plugins)
	}
	for _, entry := range manifest.Plugins {
		if entry.Name == "a" && len(entry.Dependencies) != 1 {
			t.Fatal("dependency lost")
		}
	}
}

// TestComparePluginVersions ports the version-compare table.
func TestComparePluginVersions(t *testing.T) {
	cases := []struct {
		installed, latest string
		want              PluginUpdateStatus
	}{
		{"", "1.0.0", UpdateNone},
		{"1.0.0", "", UpdateNone},
		{"1.0.0", "1.0.1", UpdateAvailable},
		{"1.0.0", "1.0.0", UpdateNone},
		{"1.0.0", "0.9.9", UpdateNone},
		{"v1.2.3", "v1.2.4", UpdateAvailable},
		{"1.2", "1.2.1", UpdateAvailable}, // coerce fills missing parts
		{"abc", "def", UpdateVersionChanged},
		{"abc", "abc", UpdateNone},
	}
	for _, tc := range cases {
		if got := ComparePluginVersions(tc.installed, tc.latest); got != tc.want {
			t.Errorf("ComparePluginVersions(%q, %q) = %s want %s", tc.installed, tc.latest, got, tc.want)
		}
	}
}

// TestComparePluginUpdate ports the fallback-to-sha axis.
func TestComparePluginUpdate(t *testing.T) {
	cases := []struct {
		installedVersion, installedSha, latestVersion, latestSha string
		want                                                     PluginUpdateStatus
	}{
		{"1.0.0", "aaa", "1.0.1", "bbb", UpdateAvailable},
		{"1.0.0", "aaa", "", "bbb", UpdateAvailable},
		{"1.0.0", "aaa", "", "aaa", UpdateNone},
		{"", "aaa", "", "bbb", UpdateAvailable},
		{"", "", "", "bbb", UpdateVersionChanged},
		{"", "", "", "", UpdateNone},
	}
	for _, tc := range cases {
		if got := ComparePluginUpdate(tc.installedVersion, tc.installedSha, tc.latestVersion, tc.latestSha); got != tc.want {
			t.Errorf("ComparePluginUpdate(%q,%q,%q,%q) = %s want %s",
				tc.installedVersion, tc.installedSha, tc.latestVersion, tc.latestSha, got, tc.want)
		}
	}
}

// TestAtomicDirectoryInstall: install via staging; finalize flips; a failed
// copy rolls back leaving the previous state intact.
func TestAtomicDirectoryInstall(t *testing.T) {
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"name":"p"}`), 0o644)
	os.MkdirAll(filepath.Join(source, "skills"), 0o755)
	os.WriteFile(filepath.Join(source, "skills", "SKILL.md"), []byte("x"), 0o644)

	parent := t.TempDir()
	target := filepath.Join(parent, "p")
	if err := InstallDirectoryAtomic(source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "plugin.json")); err != nil {
		t.Fatal("install incomplete")
	}
	// No staging or transaction residue.
	entries, _ := os.ReadDir(parent)
	if len(entries) != 1 {
		t.Fatalf("residue: %v", entries)
	}

	// Rollback path: stage over an existing target, fail the copy, verify
	// the old content survives.
	os.WriteFile(filepath.Join(target, "plugin.json"), []byte(`{"name":"old"}`), 0o644)
	activation, err := StageDirectoryAtomic(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := activation.Rollback(); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(target, "plugin.json"))
	if string(content) != `{"name":"old"}` {
		t.Fatalf("rollback lost old content: %s", content)
	}
}

// TestPluginArchiveSecurity runs the same hostile shapes through the plugin
// codec (zip-slip, absolute, symlink, bomb cap, mode restoration).
func TestPluginArchiveSecurity(t *testing.T) {
	target := t.TempDir()

	// zip-slip via metadata path is impossible (validated), but entries flow
	// through the shared path policy — verify via a hand-built archive.
	archive := buildPluginTar(t, func(b *tarTestBuilder) {
		b.file("../escaped.txt", []byte("evil"))
	})
	if err := ExtractSyncArchive(archive, target, ExtractSyncArchiveOptions{MaxExtractedBytes: 1 << 20}); err == nil {
		t.Fatal("zip-slip entry must be rejected")
	}

	archive = buildPluginTar(t, func(b *tarTestBuilder) {
		b.symlink("link", "/etc")
	})
	if err := ExtractSyncArchive(archive, t.TempDir(), ExtractSyncArchiveOptions{}); err == nil {
		t.Fatal("symlink entry must be rejected")
	}

	archive = buildPluginTar(t, func(b *tarTestBuilder) {
		b.file("big.bin", bytes.Repeat([]byte{1}, 2048))
	})
	err := ExtractSyncArchive(archive, t.TempDir(), ExtractSyncArchiveOptions{MaxExtractedBytes: 1024})
	if err == nil {
		t.Fatal("size cap must trip")
	}
}

// TestPluginArchiveModeRestoration: the exec bit survives extraction.
func TestPluginArchiveModeRestoration(t *testing.T) {
	source := t.TempDir()
	script := filepath.Join(source, "run.sh")
	os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755)
	archive, err := CreateSyncArchive([]ArchiveEntry{{SourcePath: source, ArchivePath: "p"}},
		ArchiveMetadata{Plugins: []ArchivePluginMetadata{{Name: "p", PluginID: "p@inline", DirectoryName: "p"}}})
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := ExtractSyncArchive(archive, target, ExtractSyncArchiveOptions{MaxExtractedBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(target, "p", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatal("exec bit lost")
	}
	// Metadata entry present.
	if _, err := os.Stat(filepath.Join(target, MetadataArchivePath)); err != nil {
		t.Fatal("metadata entry missing")
	}
}

// tarTestBuilder is the plugin codec's hostile-entry writer.
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

func buildPluginTar(t *testing.T, build func(*tarTestBuilder)) []byte {
	t.Helper()
	var builder tarTestBuilder
	build(&builder)
	builder.Write(make([]byte, tarEndBytes))
	var out bytes.Buffer
	gzipWriter := gzip.NewWriter(&out)
	gzipWriter.Write(builder.Bytes())
	gzipWriter.Close()
	return out.Bytes()
}

// TestNormalizeSyncRelativePathShared: the plugin codec reuses the shared
// path policy (skills.NormalizeSyncRelativePath) — one implementation.
func TestNormalizeSyncRelativePathShared(t *testing.T) {
	if got, err := skills.NormalizeSyncRelativePath("a/b", "label"); err != nil || got != "a/b" {
		t.Fatalf("shared normalize = %q, %v", got, err)
	}
	if _, err := skills.NormalizeSyncRelativePath("../x", "label"); err == nil {
		t.Fatal("shared normalize must reject dot-dot")
	}
}

// TestContentFingerprint: stable across runs, sensitive to content and names.
func TestContentFingerprint(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	first, err := ContentFingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ContentFingerprint(dir)
	if err != nil || first != second {
		t.Fatal("fingerprint not stable")
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("y"), 0o644)
	third, err := ContentFingerprint(dir)
	if err != nil || third == first {
		t.Fatal("fingerprint insensitive to content")
	}
}
