package file

// golden_test.go loads the Node behavior captures (svc-capture.mjs) so the
// table tests can assert against what the real RPC produced.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type goldenCase struct {
	Method string
	Args   map[string]any
	OK     bool
	Result any
	Error  map[string]any
	Post   []string `json:"post"`
}

func readGolden(t *testing.T, slug string) goldenCase {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "services", "file", slug, "out.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("golden %s not found: %v", slug, err)
	}
	var out goldenCase
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse golden %s: %v", slug, err)
	}
	return out
}

func TestGoldenFilesPresent(t *testing.T) {
	count := 0
	base := filepath.Join("..", "..", "..", "testdata", "services", "file")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Skip("no golden dir")
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "fixtures" {
			count++
		}
	}
	if count < 30 {
		t.Fatalf("expected the full file capture set (>=30 cases), found %d", count)
	}
	_ = fmt.Sprint
}
