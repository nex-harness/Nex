package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

func TestReadToolTextImageAndBinary(t *testing.T) {
	dir := t.TempDir()

	txtFile := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(txtFile, []byte("alpha\nbeta\ngamma\ndelta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := NewReadTool()
	res, err := rt.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"sample.txt","offset":2,"limit":2}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("read text failed: %+v, err=%v", res, err)
	}
	if !strings.Contains(res.Output, "2\tbeta") || !strings.Contains(res.Output, "3\tgamma") || strings.Contains(res.Output, "alpha") {
		t.Fatalf("unexpected sliced output:\n%s", res.Output)
	}

	// Minimal valid PNG header
	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d}
	imgFile := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(imgFile, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	imgRes, err := rt.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"logo.png"}`), nil)
	if err != nil || imgRes.IsError {
		t.Fatalf("read image failed: %+v, err=%v", imgRes, err)
	}
	if len(imgRes.ExtraBlocks) != 1 || imgRes.ExtraBlocks[0].Type != ai.BlockImage || imgRes.ExtraBlocks[0].MimeType != "image/png" {
		t.Fatalf("expected image block, got %+v", imgRes.ExtraBlocks)
	}

	// Non-image binary file must be rejected cleanly
	binFile := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(binFile, []byte{0x01, 0x02, 0x00, 0xFF, 0xFE}, 0o644); err != nil {
		t.Fatal(err)
	}
	binRes, _ := rt.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"blob.bin"}`), nil)
	if !binRes.IsError || !strings.Contains(binRes.Output, "binary") {
		t.Fatalf("expected binary file rejection, got %+v", binRes)
	}
}

func TestWriteAndEditPreserveCRLFAndBOMWithUnifiedDiff(t *testing.T) {
	dir := t.TempDir()
	wt := NewWriteTool()
	et := NewEditTool()

	wRes, err := wt.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"sub/hello.go","content":"package sub\n\nfunc Greet() string {\n\treturn \"hi\"\n}\n"}`), nil)
	if err != nil || wRes.IsError {
		t.Fatalf("write failed: %+v err=%v", wRes, err)
	}
	if wRes.Additions != 5 {
		t.Fatalf("expected 5 additions on new file, got +%d -%d", wRes.Additions, wRes.Deletions)
	}

	// Create Windows CRLF + UTF-8 BOM file with trailing spaces on a line
	crlfPath := filepath.Join(dir, "win.ps1")
	origCRLF := append([]byte{0xEF, 0xBB, 0xBF}, []byte("$a = 1   \r\n$b = 2\r\nWrite-Host $a\r\n")...)
	if err := os.WriteFile(crlfPath, origCRLF, 0o644); err != nil {
		t.Fatal(err)
	}

	// Edit using LF old_string WITHOUT trailing spaces -> exercises tolerant whitespace match + CRLF & BOM preservation!
	eRes, err := et.Execute(context.Background(), dir, json.RawMessage(`{
		"file_path": "win.ps1",
		"old_string": "$a = 1\n$b = 2",
		"new_string": "$a = 10\n$b = 20\n$c = 30"
	}`), nil)
	if err != nil || eRes.IsError {
		t.Fatalf("edit failed: %+v err=%v", eRes, err)
	}
	if eRes.Additions != 3 || eRes.Deletions != 2 || !strings.Contains(eRes.Diff, "+$c = 30") {
		t.Fatalf("unexpected diff stats (+%d -%d):\n%s", eRes.Additions, eRes.Deletions, eRes.Diff)
	}

	updatedRaw, err := os.ReadFile(crlfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(updatedRaw, utf8BOM) {
		t.Fatalf("UTF-8 BOM was lost!")
	}
	if !bytes.Contains(updatedRaw, []byte("$a = 10\r\n$b = 20\r\n$c = 30\r\n")) {
		t.Fatalf("CRLF line endings were not preserved: %q", string(updatedRaw))
	}
}

func TestBashStreamingAndProcessGroupKillOnTimeout(t *testing.T) {
	dir := t.TempDir()
	bt := NewBashTool()

	var mu sync.Mutex
	var streamed strings.Builder
	res, err := bt.Execute(context.Background(), dir, json.RawMessage(`{"command":"printf 'step1\\n'; printf 'step2\\n'"}`), func(delta string) {
		mu.Lock()
		streamed.WriteString(delta)
		mu.Unlock()
	})
	if err != nil || res.IsError {
		t.Fatalf("bash failed: %+v err=%v", res, err)
	}
	if !strings.Contains(streamed.String(), "step1") || !strings.Contains(res.Output, "step2") {
		t.Fatalf("missing streamed output: streamed=%q res=%q", streamed.String(), res.Output)
	}

	// Spawn a background grandchild holding stdout open for 30s, with a 150ms timeout.
	// Process group / Job Object kill must terminate everything and return promptly (< 2s), NOT hang 30s!
	start := time.Now()
	toRes, _ := bt.Execute(context.Background(), dir, json.RawMessage(`{"command":"sleep 30 & echo child_spawned; wait","timeout_ms":150}`), nil)
	elapsed := time.Since(start)
	if !toRes.IsError || !strings.Contains(toRes.Output, "timed out") {
		t.Fatalf("expected timeout error, got %+v", toRes)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("process group kill leaked/hung for %v!", elapsed)
	}
}

func TestGlobAndGrepRespectGitignore(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n*.secret\n!keep.secret\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)

	_ = os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("const NEEDLE = 1;\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "hidden.secret"), []byte("NEEDLE_SECRET\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "keep.secret"), []byte("NEEDLE_ALLOWED\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "src", "app.go"), []byte("package src\n// NEEDLE_CODE\n"), 0o644)

	globTool := NewGlobTool()
	gRes, err := globTool.Execute(context.Background(), dir, json.RawMessage(`{"pattern":"**/*"}`), nil)
	if err != nil || gRes.IsError {
		t.Fatalf("glob failed: %+v err=%v", gRes, err)
	}
	if strings.Contains(gRes.Output, "node_modules") || strings.Contains(gRes.Output, "hidden.secret") {
		t.Fatalf("gitignore failed in glob:\n%s", gRes.Output)
	}
	if !strings.Contains(gRes.Output, "keep.secret") || !strings.Contains(gRes.Output, "src/app.go") {
		t.Fatalf("expected unignored files in glob:\n%s", gRes.Output)
	}

	grepTool := NewGrepTool()
	sRes, err := grepTool.Execute(context.Background(), dir, json.RawMessage(`{"pattern":"NEEDLE"}`), nil)
	if err != nil || sRes.IsError {
		t.Fatalf("grep failed: %+v err=%v", sRes, err)
	}
	if strings.Contains(sRes.Output, "node_modules") || strings.Contains(sRes.Output, "hidden.secret") {
		t.Fatalf("gitignore failed in grep:\n%s", sRes.Output)
	}
	if !strings.Contains(sRes.Output, "src/app.go:2:") || !strings.Contains(sRes.Output, "keep.secret:1:") {
		t.Fatalf("expected matches in grep:\n%s", sRes.Output)
	}
}

func TestTodoWriteAndReadRoundTrip(t *testing.T) {
	store := NewTodoStore()
	wt := NewTodoWriteTool(store)
	rt := NewTodoReadTool(store)

	// Fresh store reads as an empty list, never null or an error.
	emptyRes, err := rt.Execute(context.Background(), "", nil, nil)
	if err != nil || emptyRes.IsError {
		t.Fatalf("todo_read on empty store failed: %+v err=%v", emptyRes, err)
	}
	if strings.TrimSpace(emptyRes.Output) != "[]" {
		t.Fatalf("expected empty JSON array, got %q", emptyRes.Output)
	}

	writeRes, err := wt.Execute(context.Background(), "", json.RawMessage(`{
		"todos": [
			{"content": "  read the spec  ", "status": "completed", "priority": "high"},
			{"content": "implement builder", "status": "in_progress", "priority": "high"},
			{"content": "add tests", "status": "pending", "priority": "low"},
			{"content": "   ", "status": "pending", "priority": "low"}
		]
	}`), nil)
	if err != nil || writeRes.IsError {
		t.Fatalf("todo_write failed: %+v err=%v", writeRes, err)
	}

	items := store.Get()
	if len(items) != 3 {
		t.Fatalf("expected blank entries to be dropped, got %d: %+v", len(items), items)
	}
	if items[0].Content != "read the spec" {
		t.Fatalf("expected content to be trimmed, got %q", items[0].Content)
	}
	if got := writeRes.Metadata["completed"]; got != 1 {
		t.Fatalf("expected 1 completed, got %v", got)
	}
	if got := writeRes.Metadata["in_progress"]; got != 1 {
		t.Fatalf("expected 1 in_progress, got %v", got)
	}
	if got := writeRes.Metadata["pending"]; got != 1 {
		t.Fatalf("expected 1 pending, got %v", got)
	}

	// Normalization: unknown status becomes pending, unknown priority becomes medium.
	_, _ = wt.Execute(context.Background(), "", json.RawMessage(`{
		"todos": [{"content": "unknowns", "status": "blocked", "priority": "urgent"}]
	}`), nil)
	items = store.Get()
	if len(items) != 1 || items[0].Status != "pending" || items[0].Priority != "medium" {
		t.Fatalf("expected status/priority normalization, got %+v", items)
	}

	// Each call replaces the whole list rather than appending.
	readRes, err := rt.Execute(context.Background(), "", nil, nil)
	if err != nil || readRes.IsError {
		t.Fatalf("todo_read failed: %+v err=%v", readRes, err)
	}
	if !strings.Contains(readRes.Output, "unknowns") || strings.Contains(readRes.Output, "implement builder") {
		t.Fatalf("expected list replacement semantics, got:\n%s", readRes.Output)
	}
}

func TestTodoWriteRejectsInvalidArguments(t *testing.T) {
	wt := NewTodoWriteTool(NewTodoStore())
	res, err := wt.Execute(context.Background(), "", json.RawMessage(`{"todos": "not-an-array"}`), nil)
	if err != nil {
		t.Fatalf("expected soft error result, got hard error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Output, "invalid todo_write arguments") {
		t.Fatalf("expected invalid-arguments error, got %+v", res)
	}
}

func TestToolDefinitionsKeepNamesAndSchemas(t *testing.T) {
	// Tool names and JSON schema property names are part of the model-facing
	// contract; other subsystems and clients depend on them staying stable.
	want := map[string][]string{
		"read":       {"file_path", "offset", "limit"},
		"write":      {"file_path", "content"},
		"edit":       {"file_path", "old_string", "new_string", "replace_all"},
		"bash":       {"command", "description", "timeout_ms", "cwd"},
		"grep":       {"pattern", "path", "include", "case_insensitive", "output_mode", "limit"},
		"glob":       {"pattern", "path", "limit"},
		"todo_write": {"todos"},
		"todo_read":  {},
	}
	todoStore := NewTodoStore()
	all := []Tool{
		NewReadTool(), NewWriteTool(), NewEditTool(), NewBashTool(),
		NewGrepTool(), NewGlobTool(), NewTodoWriteTool(todoStore), NewTodoReadTool(todoStore),
	}
	for _, tool := range all {
		def := tool.Definition()
		if def.Name != tool.Name() {
			t.Fatalf("Definition().Name %q != Name() %q", def.Name, tool.Name())
		}
		props, ok := want[def.Name]
		if !ok {
			t.Fatalf("unexpected tool %q", def.Name)
		}
		if strings.TrimSpace(def.Description) == "" {
			t.Fatalf("tool %q has an empty description", def.Name)
		}
		if !json.Valid(def.InputSchema) {
			t.Fatalf("tool %q has an invalid input schema: %s", def.Name, def.InputSchema)
		}
		for _, p := range props {
			if !strings.Contains(string(def.InputSchema), `"`+p+`"`) {
				t.Fatalf("tool %q schema is missing property %q: %s", def.Name, p, def.InputSchema)
			}
		}
	}
}

func TestGrepOutputModesAndLineTruncation(t *testing.T) {
	dir := t.TempDir()
	longLine := "MATCH_LONG_" + strings.Repeat("x", 500)
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nMATCH_HERE 1\nMATCH_HERE 2\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte(longLine+"\nMATCH_HERE 3\n"), 0o644)

	grepTool := NewGrepTool()

	runSuite := func(label string) {
		// 1. files_with_matches
		res, err := grepTool.Execute(context.Background(), dir, json.RawMessage(`{"pattern":"MATCH_HERE","output_mode":"files_with_matches"}`), nil)
		if err != nil || res.IsError {
			t.Fatalf("[%s] files_with_matches failed: %+v err=%v", label, res, err)
		}
		if !strings.Contains(res.Output, "a.go") || !strings.Contains(res.Output, "b.txt") || strings.Contains(res.Output, "MATCH_HERE 1") {
			t.Fatalf("[%s] unexpected files_with_matches output: %q", label, res.Output)
		}

		// 2. count
		resCount, err := grepTool.Execute(context.Background(), dir, json.RawMessage(`{"pattern":"MATCH_HERE","output_mode":"count"}`), nil)
		if err != nil || resCount.IsError {
			t.Fatalf("[%s] count failed: %+v err=%v", label, resCount, err)
		}
		if !strings.Contains(resCount.Output, "a.go:2") || !strings.Contains(resCount.Output, "b.txt:1") {
			t.Fatalf("[%s] unexpected count output: %q", label, resCount.Output)
		}

		// 3. content line truncation at 240 chars
		resLong, err := grepTool.Execute(context.Background(), dir, json.RawMessage(`{"pattern":"MATCH_LONG_"}`), nil)
		if err != nil || resLong.IsError {
			t.Fatalf("[%s] content long line failed: %+v err=%v", label, resLong, err)
		}
		if !strings.Contains(resLong.Output, "...") || len(resLong.Output) >= 320 {
			t.Fatalf("[%s] expected matched line truncated around 240 chars, got len=%d: %q", label, len(resLong.Output), resLong.Output)
		}
	}

	// Test default path (uses rg if installed)
	runSuite("default")

	// Force pure-Go fallback by clearing PATH
	t.Setenv("PATH", "")
	runSuite("pure-go-fallback")
}
