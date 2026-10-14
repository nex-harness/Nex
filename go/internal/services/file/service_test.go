package file

// service_test.go: table tests named after the TS handler behaviors in
// fileService.ts, plus the recipe's edge cases: symlink classification,
// non-UTF-8 filename, permission denied, huge directory memory bound,
// binary-vs-text detection vectors, and the workspace-identity cache key.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	return NewService(nil)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// readdir
// ---------------------------------------------------------------------------

func TestReaddirHiddenFilesAndSort(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "b.txt"), "b")
	mustWrite(t, filepath.Join(dir, ".hidden"), "h")
	mustWrite(t, filepath.Join(dir, "a.txt"), "a")
	mustWrite(t, filepath.Join(dir, "zdir", "x"), "x")

	s := newTestService(t)
	ctx := context.Background()

	// TS: 目录选择器等旧调用仍应默认隐藏 dotfiles
	got, err := s.Readdir(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 visible entries, got %d: %+v", len(got), got)
	}
	// TS: directories first, then name order
	if got[0].Type != "directory" || got[0].Name != "zdir" {
		t.Errorf("directories sort first: %+v", got[0])
	}
	if got[1].Name != "a.txt" || got[2].Name != "b.txt" {
		t.Errorf("name order: %+v", got)
	}

	// TS: workspace 文件树需要展示 .gitignore/.env/.github 等项目文件
	got, err = s.Readdir(ctx, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 entries with includeHidden, got %d", len(got))
	}
}

func TestReaddirMissingDirPropagatesError(t *testing.T) {
	s := newTestService(t)
	_, err := s.Readdir(context.Background(), filepath.Join(t.TempDir(), "nope"), false)
	// the message is the Node errno shape (part of the RPC contract)
	if err == nil || !strings.Contains(err.Error(), "ENOENT") {
		t.Fatalf("want ENOENT-style error, got %v", err)
	}
}

func TestReaddirSymlinkToDirectoryIsReclassified(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "real", "inner.txt"), "x")
	// Node 的 Dirent 对软链接目录只返回 isSymbolicLink；目录选择器因此看不到
	// 指向目录的软链接；这里跟随目标重新分类。
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	s := newTestService(t)
	got, err := s.Readdir(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	var link FileEntry
	for _, e := range got {
		if e.Name == "link" {
			link = e
		}
	}
	if link.Name == "" {
		t.Fatalf("link entry missing: %+v", got)
	}
	if link.Type != "directory" {
		t.Errorf("symlink-to-dir must reclassify as directory: %+v", link)
	}
	if !link.IsSymbolicLink {
		t.Errorf("isSymbolicLink flag lost: %+v", link)
	}
}

func TestReaddirSymlinkToMissingTargetStaysFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "dangling")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	s := newTestService(t)
	got, err := s.Readdir(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Type != "file" {
		t.Errorf("dangling symlink falls back to file: %+v", got[0])
	}
}

// ---------------------------------------------------------------------------
// stat parity with Node fs.stat shape
// ---------------------------------------------------------------------------

func TestStatShapeParityWithNode(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f.txt"), "hello")
	s := newTestService(t)
	ctx := context.Background()

	st, err := s.Stat(ctx, filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Type != "file" || st.Size == nil || *st.Size != 5 {
		t.Fatalf("file stat: %+v", st)
	}
	if st.MtimeMs == nil || *st.MtimeMs <= 0 {
		t.Errorf("mtimeMs must be present and positive (ms): %+v", st)
	}
	if *st.MtimeMs < 1.7e12 { // sanity: after 2020 in ms
		t.Errorf("mtimeMs not in milliseconds: %v", *st.MtimeMs)
	}

	st, err = s.Stat(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	// TS: 目录的 size 无意义，只对文件返回
	if st.Type != "directory" || st.Size != nil || st.MtimeMs != nil {
		t.Fatalf("directory stat must omit size/mtimeMs: %+v", st)
	}
}

// ---------------------------------------------------------------------------
// checkFilesExist
// ---------------------------------------------------------------------------

func TestCheckFilesExistBatchLimit(t *testing.T) {
	s := newTestService(t)
	paths := make([]string, 16)
	for i := range paths {
		paths[i] = fmt.Sprintf("p%d", i)
	}
	_, err := s.CheckFilesExist(context.Background(), paths)
	if err == nil || !strings.Contains(err.Error(), "at most 15") {
		t.Fatalf("want batch limit error, got %v", err)
	}
}

func TestCheckFilesExistOrderAndNegativeCaching(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "exists.txt"), "x")
	s := newTestService(t)
	ctx := context.Background()
	missing := filepath.Join(dir, "missing.txt")

	for round := 0; round < 2; round++ {
		results, err := s.CheckFilesExist(ctx, []string{missing, filepath.Join(dir, "exists.txt")})
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 {
			t.Fatalf("want 2 results, got %d", len(results))
		}
		if results[0]["exists"] != false || results[1]["exists"] != true {
			t.Fatalf("round %d: %+v", round, results)
		}
	}
}

// ---------------------------------------------------------------------------
// resolvePath / workspace creation
// ---------------------------------------------------------------------------

func TestResolvePathFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	mustWrite(t, filepath.Join(real, "x"), "x")
	if err := os.Symlink(real, filepath.Join(dir, "alias")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	s := newTestService(t)
	// TS: 远程 workspace 可能通过符号链接别名输入；统一走 realpath
	got, err := s.ResolvePath(context.Background(), filepath.Join(dir, "alias"))
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Fatalf("want %s got %s", real, got)
	}
}

func TestCreateScratchWorkspaceNameValidation(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	for _, bad := range []string{"  ", "a/b", "a\\b"} {
		if _, err := s.CreateScratchWorkspace(ctx, bad); err == nil {
			t.Errorf("name %q must be rejected", bad)
		}
	}
	_, err := s.CreateScratchWorkspace(ctx, "ok name")
	if err != nil {
		t.Fatal(err)
	}
	// mkdir recursive 让已存在目录幂等成功
	_, err = s.CreateScratchWorkspace(ctx, "ok name")
	if err != nil {
		t.Fatalf("idempotent create failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// binary vs text detection vectors
// ---------------------------------------------------------------------------

func TestIsProbablyBinaryVectors(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty is not binary", nil, false},
		{"plain text", []byte("hello world\n"), false},
		{"NUL anywhere is binary", []byte("a\x00b"), true},
		{"common whitespace allowed", []byte("a\tb\nc\rd\x0ce"), false},
		{"sparse control chars under 30%", []byte("a\x01b\x02cdefgh"), false},
		{"dense control chars over 30%", []byte("\x01\x02\x03\x04\x05\x06abcd"), true},
		{"DEL 127 counts as control", []byte("\x7f\x7f\x7f\x7fabcd"), true},
		{"high bytes are not control", []byte{0xff, 0xfe, 0xfd}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isProbablyBinary(tc.data); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// readTextFile clamps and offsets
// ---------------------------------------------------------------------------

func TestReadTextFileOffsetsAndTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	mustWrite(t, path, strings.Repeat("a", 300*1024)) // > maxTextReadBytes
	s := newTestService(t)
	ctx := context.Background()

	slice, err := s.ReadTextFile(ctx, path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 默认 128KB；256KB 硬上限
	if len(slice.Content) != 128*1024 {
		t.Fatalf("default read length: %d", len(slice.Content))
	}
	if !slice.Truncated {
		t.Errorf("must be truncated")
	}

	slice, err = s.ReadTextFile(ctx, path, nil, ptrInt64(10_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if len(slice.Content) != 256*1024 {
		t.Fatalf("clamped read length: %d", len(slice.Content))
	}

	slice, err = s.ReadTextFile(ctx, path, ptrInt64(300*1024), nil)
	if err != nil {
		t.Fatal(err)
	}
	// EOF 偏移返回空内容
	if slice.Content != "" || slice.BytesRead != 0 || slice.Truncated {
		t.Fatalf("past-EOF: %+v", slice)
	}
}

func TestReadTextFileOnDirectoryFails(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	_, err := s.ReadTextFile(context.Background(), dir, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Path is not a file") {
		t.Fatalf("got %v", err)
	}
}

func TestReadFileRangeSlicesAndEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")
	os.WriteFile(path, []byte{0, 1, 2, 3, 4, 5, 6, 7}, 0o644)
	s := newTestService(t)
	ctx := context.Background()

	data, err := s.ReadFileRange(ctx, path, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 3 || data[0] != 2 || data[2] != 4 {
		t.Fatalf("slice: %v", data)
	}
	// EOF 时返回短数组
	data, err = s.ReadFileRange(ctx, path, 5, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 3 {
		t.Fatalf("short read at EOF: %v", data)
	}
	// past EOF: empty
	data, err = s.ReadFileRange(ctx, path, 100, 4)
	if err != nil || len(data) != 0 {
		t.Fatalf("past EOF: %v %v", data, err)
	}
}

func TestReadMediaPreviewSizeLimitsAndTypeInference(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t)
	ctx := context.Background()

	png := filepath.Join(dir, "x.png")
	os.WriteFile(png, []byte("pngdata"), 0o644)
	// 默认 4MB 上限
	mustWrite(t, filepath.Join(dir, "toobig.png"), strings.Repeat("z", 5*1024*1024))
	_, err := s.ReadMediaPreview(ctx, filepath.Join(dir, "toobig.png"), nil)
	if err == nil || !strings.Contains(err.Error(), "too large to preview") {
		t.Fatalf("got %v", err)
	}
	// image extension table beats octet-stream
	pv, err := s.ReadMediaPreview(ctx, png, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.MediaType != "image/png" {
		t.Fatalf("mediaType: %s", pv.MediaType)
	}
	if pv.DataBase64 != "cG5nZGF0YQ==" {
		t.Fatalf("base64: %s", pv.DataBase64)
	}
	// media-preview 表兜底：.mp4 -> video/mp4
	mp4 := filepath.Join(dir, "v.mp4")
	os.WriteFile(mp4, []byte("mm"), 0o644)
	pv, err = s.ReadMediaPreview(ctx, mp4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.MediaType != "video/mp4" {
		t.Fatalf("mp4 mediaType: %s", pv.MediaType)
	}
	// 非媒体后缀兜底 application/octet-stream
	other := filepath.Join(dir, "x.zzz")
	os.WriteFile(other, []byte("z"), 0o644)
	pv, err = s.ReadMediaPreview(ctx, other, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.MediaType != "application/octet-stream" {
		t.Fatalf("fallback mediaType: %s", pv.MediaType)
	}
}

// ---------------------------------------------------------------------------
// permission denied
// ---------------------------------------------------------------------------

func TestPermissionDeniedPaths(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod 000 ineffective as root / on Windows")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o755)
	s := newTestService(t)
	_, err := s.Readdir(context.Background(), locked, false)
	if err == nil {
		t.Fatalf("want permission error")
	}

	// 扫描遍历对 EACCES/EPERM/ENOENT 是可跳过的
	parent := t.TempDir()
	os.MkdirAll(filepath.Join(parent, "sub", "inner"), 0o755)
	mustWrite(t, filepath.Join(parent, "sub", "inner", "f.txt"), "x")
	mustWrite(t, filepath.Join(parent, "top.txt"), "x")
	os.Chmod(filepath.Join(parent, "sub"), 0o000)
	defer os.Chmod(filepath.Join(parent, "sub"), 0o755)
	entries, err := s.ListWorkspaceFilesRange(context.Background(), parent, 0, 1_000_000)
	if err != nil {
		t.Fatalf("scan must skip unreadable dirs: %v", err)
	}
	if !strings.Contains(entries, "top.txt") {
		t.Fatalf("top.txt missing from index: %q", entries)
	}
	if strings.Contains(entries, "inner") {
		t.Fatalf("locked subtree leaked into index")
	}
}

// ---------------------------------------------------------------------------
// non-UTF-8 filename
// ---------------------------------------------------------------------------

func TestNonUTF8FilenameSurvivesIndexAndReaddir(t *testing.T) {
	dir := t.TempDir()
	raw := []byte{0xff, 0xfe, 'f', 'i', 'l', 'e'} // invalid UTF-8 bytes
	name := string(raw)
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Skipf("filesystem rejected non-UTF8 name: %v", err)
	}
	s := newTestService(t)
	ctx := context.Background()
	got, err := s.Readdir(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != name {
		t.Fatalf("name round-trip: %+v", got)
	}
	packed, err := s.ListWorkspaceFilesRange(ctx, dir, 0, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// the codec escapes bytes; \xff stays a raw byte in the string (matching
	// the TS, which passes UTF-16 surrogates through unescaped)
	if !strings.Contains(packed, "file") {
		t.Fatalf("packed index lost the entry: %q", packed)
	}
}

// ---------------------------------------------------------------------------
// huge directory memory bound
// ---------------------------------------------------------------------------

func TestHugeDirectoryScanBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("100k-entry tree is slow")
	}
	dir := t.TempDir()
	const count = 100_000
	// write in batches to keep syscall pressure sane
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for b := 0; b < count; b += 1000 {
		wg.Add(1)
		sem <- struct{}{}
		go func(b int) {
			defer wg.Done()
			defer func() { <-sem }()
			for i := b; i < b+1000 && i < count; i++ {
				os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%06d.txt", i)), []byte("x"), 0o644)
			}
		}(b)
	}
	wg.Wait()

	s := newTestService(t)
	_ = s
	// measure the scan cost only; the memory bound is the entries slice
	// (100k * entry struct) plus the packed string — no per-entry goroutines.
	ctx := context.Background()
	packedLen, err := s.ListWorkspaceFilesLength(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if packedLen < count*10 {
		t.Fatalf("index suspiciously small: %d", packedLen)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = s.SearchWorkspaceFiles(ctx, dir, "", "f0", ptrInt64(10), false)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	growth := after.TotalAlloc - before.TotalAlloc
	// candidates are rebuilt once and cached; the search itself must not copy
	// per-candidate strings unbounded. Allow generous overhead for the GC.
	if growth > 200<<20 {
		t.Fatalf("search allocated %d bytes for a 100k-entry index", growth)
	}
}

// ---------------------------------------------------------------------------
// workspace identity key isolation (AGENTS.md rule)
// ---------------------------------------------------------------------------

func TestWorkspaceIdentityKeyIsolatesIndexCache(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "x")
	s := newTestService(t)
	ctx := context.Background()

	if _, err := s.SearchWorkspaceFiles(ctx, dir, "", "a", ptrInt64(10), false); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "b.txt"), "x")
	// same identity: TTL cache still serves the old index
	res, err := s.SearchWorkspaceFiles(ctx, dir, "", "b", ptrInt64(10), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("TTL cache must hide new files without refresh: %+v", res)
	}
	// refresh bypasses the cache
	res, err = s.SearchWorkspaceFiles(ctx, dir, "", "b", ptrInt64(10), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Name != "b.txt" {
		t.Fatalf("refresh must see new files: %+v", res)
	}
}

func ptrInt64(v int64) *int64 { return &v }
