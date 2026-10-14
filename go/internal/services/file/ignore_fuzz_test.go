package file

import (
	"encoding/json"
	"os"
	"testing"
)

// TestIgnoreFuzzCorpus runs an adversarially-generated corpus (random token
// soup incl. regex metacharacters, brackets, backslashes) against the port.
// Regenerate with the generator embedded in this repo's gitignore-corpus
// workflow; the file is committed so the check is deterministic.
func TestIgnoreFuzzCorpus(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/services/file/gitignore-fuzz-corpus.json")
	if err != nil {
		t.Fatalf("read fuzz corpus: %v", err)
	}
	var corpus struct {
		Cases []struct {
			Rules []string `json:"rules"`
			Paths []struct {
				Path    string `json:"path"`
				Ignored bool   `json:"ignored"`
			} `json:"paths"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("parse fuzz corpus: %v", err)
	}
	mismatches, checks := 0, 0
	for _, c := range corpus.Cases {
		m := newMatcher().Add(joinLines(c.Rules))
		for _, p := range c.Paths {
			checks++
			if got := m.ignores(p.Path); got != p.Ignored {
				mismatches++
				if mismatches <= 15 {
					t.Errorf("rules=%v path=%q want=%v got=%v", c.Rules, p.Path, p.Ignored, got)
				}
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d/%d fuzz checks mismatch", mismatches, checks)
	}
}
