package file

import (
	"encoding/json"
	"os"
	"testing"
)

// TestIgnoreCorpus runs the generated gitignore corpus (3944 path checks from
// the TS `ignore` package — see scripts/go-rewrite/gen-gitignore-corpus.mjs)
// against the Go port. This is the gate the phase-3 plan requires before any
// matcher (library or port) may back the .nexignore feature.
func TestIgnoreCorpus(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/services/file/gitignore-corpus.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus struct {
		Cases []struct {
			Name  string   `json:"name"`
			Rules []string `json:"rules"`
			Paths []struct {
				Path        string `json:"path"`
				IsDirectory bool   `json:"isDirectory"`
				Ignored     bool   `json:"ignored"`
			} `json:"paths"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	checks, mismatches := 0, 0
	for _, c := range corpus.Cases {
		m := newMatcher().Add(joinLines(c.Rules))
		for _, p := range c.Paths {
			checks++
			probe := p.Path
			if p.IsDirectory {
				probe += "/"
			}
			got := m.ignores(probe)
			if got != p.Ignored {
				mismatches++
				if mismatches <= 20 {
					t.Errorf("case %s: path=%q isDir=%v want=%v got=%v rules=%v",
						c.Name, p.Path, p.IsDirectory, p.Ignored, got, c.Rules)
				}
			}
		}
	}
	if checks < 3000 {
		t.Fatalf("corpus unexpectedly small: %d checks", checks)
	}
	if mismatches > 0 {
		t.Fatalf("%d/%d corpus checks mismatch the TS ignore package", mismatches, checks)
	}
}

func joinLines(rules []string) string {
	out := ""
	for _, r := range rules {
		out += r + "\n"
	}
	return out
}
