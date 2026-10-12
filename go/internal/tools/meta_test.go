package tools_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tools2 "github.com/tylinux/nex/go/internal/tools"
)

type metaVectors struct {
	Metadata       map[string]map[string]any `json:"metadata"`
	PermissionSpec map[string]map[string]any `json:"permissionSpec"`
	ResultBudget   map[string]map[string]any `json:"resultBudget"`
	Timeout        map[string]map[string]any `json:"timeout"`
	ReadStateKeys  []struct {
		Path  string         `json:"path"`
		Input map[string]any `json:"input"`
		Key   string         `json:"key"`
	} `json:"readStateKeys"`
	Matcher []struct {
		Content    string `json:"content"`
		Search     string `json:"search"`
		ReplaceAll bool   `json:"replaceAll"`
		Result     struct {
			Status         string `json:"status"`
			ActualString   string `json:"actualString"`
			Strategy       string `json:"strategy"`
			CandidateCount int    `json:"candidateCount"`
		} `json:"result"`
	} `json:"matcher"`
	Diff []struct {
		Old   string           `json:"old"`
		New   string           `json:"new"`
		Hunks []map[string]any `json:"hunks"`
	} `json:"diff"`
}

func loadMeta(t *testing.T) metaVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "tools-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v metaVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestToolMetadataMatchesTypeScript asserts the readOnly/destructive/side-effect
// flags, permission specs, result budgets and timeouts are exactly what the
// TypeScript tool metadata declares.
func TestToolMetadataMatchesTypeScript(t *testing.T) {
	v := loadMeta(t)
	want := map[string]struct {
		readOnly      bool
		destructive   bool
		sideEffect    string
		riskLevel     string
		needsApproval bool
		permission    string
	}{
		"read":  {true, false, "none", "low", false, "read"},
		"write": {false, false, "workspace", "medium", true, "edit"},
		"edit":  {false, false, "workspace", "medium", true, "edit"},
	}
	for name, w := range want {
		md := v.Metadata[name]
		if md["readOnly"] != w.readOnly {
			t.Errorf("%s readOnly: got %v want %v", name, md["readOnly"], w.readOnly)
		}
		if md["destructive"] != w.destructive {
			t.Errorf("%s destructive: got %v want %v", name, md["destructive"], w.destructive)
		}
		if md["sideEffectScope"] != w.sideEffect {
			t.Errorf("%s sideEffectScope: got %v want %v", name, md["sideEffectScope"], w.sideEffect)
		}
		if md["riskLevel"] != w.riskLevel {
			t.Errorf("%s riskLevel: got %v want %v", name, md["riskLevel"], w.riskLevel)
		}
		if md["needsApproval"] != w.needsApproval {
			t.Errorf("%s needsApproval: got %v want %v", name, md["needsApproval"], w.needsApproval)
		}
		ps := v.PermissionSpec[name]
		if ps["permission"] != w.permission {
			t.Errorf("%s permission: got %v want %v", name, ps["permission"], w.permission)
		}
		if ps["needsApproval"] != w.needsApproval {
			t.Errorf("%s permission.needsApproval mismatch", name)
		}
		if ps["ruleContentSource"] != nil {
			t.Errorf("unexpected ruleContentSource")
		}
	}
	// The Go tool registry must expose the same flags.
	for _, entry := range tools2.Registry() {
		name := entry.Metadata.Name
		w, ok := want[lower(name)]
		if !ok {
			continue
		}
		if entry.Metadata.ReadOnly != w.readOnly || entry.Metadata.Destructive != w.destructive {
			t.Errorf("%s registry flags differ from metadata", name)
		}
	}
}

// TestResultBudgetMatchesTypeScript asserts the per-tool result budget head
// direction and truncate strategy.
func TestResultBudgetMatchesTypeScript(t *testing.T) {
	v := loadMeta(t)
	for name := range v.Metadata {
		budget := v.ResultBudget[name]
		if budget["strategy"] != "truncate" {
			t.Errorf("%s strategy: %v", name, budget["strategy"])
		}
		preview, _ := budget["preview"].(map[string]any)
		if preview["direction"] != "head" {
			t.Errorf("%s preview.direction: %v", name, preview["direction"])
		}
		got := tools2.ResultBudgetFor(title(name))
		if got.Strategy != budget["strategy"] {
			t.Errorf("%s Go budget strategy %q want %v", name, got.Strategy, budget["strategy"])
		}
		if float64(got.MaxModelBytes) != budget["maxModelBytes"].(float64) {
			t.Errorf("%s Go maxModelBytes %d want %v", name, got.MaxModelBytes, budget["maxModelBytes"])
		}
	}
}

// TestHeadTailLimitsMatchProtocol pins the 32 KiB head/tail rule.
func TestHeadTailLimitsMatchProtocol(t *testing.T) {
	if tools2.ToolOutputFinalHeadBytes != 32*1024 || tools2.ToolOutputFinalTailBytes != 32*1024 {
		t.Fatalf("head/tail limits drifted: %d/%d", tools2.ToolOutputFinalHeadBytes, tools2.ToolOutputFinalTailBytes)
	}
	small := "hello"
	if out, truncated := tools2.TruncateHeadTail(small); truncated || out != small {
		t.Fatalf("small text should not be truncated")
	}
	big := make([]byte, 100*1024)
	for i := range big {
		big[i] = 'x'
	}
	out, truncated := tools2.TruncateHeadTail(string(big))
	if !truncated {
		t.Fatalf("large text should be truncated")
	}
	if len(out) == 0 {
		t.Fatal("truncated output empty")
	}
}

// TestMatcherVectors replays findEditMatch cases generated from the real handler.
func TestMatcherVectors(t *testing.T) {
	v := loadMeta(t)
	if len(v.Matcher) < 10 {
		t.Fatalf("only %d matcher vectors", len(v.Matcher))
	}
	for _, c := range v.Matcher {
		got := tools2.FindEditMatch(c.Content, c.Search, c.ReplaceAll)
		if got.Status != c.Result.Status || got.Strategy != tools2.EditMatchStrategy(c.Result.Strategy) ||
			got.ActualString != c.Result.ActualString || got.CandidateCount != c.Result.CandidateCount {
			t.Fatalf("matcher %q/%q replaceAll=%v\n got %+v\nwant %+v", c.Content, c.Search, c.ReplaceAll, got, c.Result)
		}
	}
}

// TestDiffVectors replays createStructuredPatch cases from the real jsdiff.
func TestDiffVectors(t *testing.T) {
	v := loadMeta(t)
	if len(v.Diff) < 8 {
		t.Fatalf("only %d diff vectors", len(v.Diff))
	}
	for i, c := range v.Diff {
		hunks := tools2.CreateStructuredPatch("f.txt", c.Old, c.New)
		var got []map[string]any
		for _, h := range hunks {
			got = append(got, map[string]any{
				"oldStart": float64(h.OldStart),
				"oldLines": float64(h.OldLines),
				"newStart": float64(h.NewStart),
				"newLines": float64(h.NewLines),
				"lines":    toAnySlice(h.Lines),
			})
		}
		var want []map[string]any
		for _, h := range c.Hunks {
			want = append(want, map[string]any{
				"oldStart": h["oldStart"],
				"oldLines": h["oldLines"],
				"newStart": h["newStart"],
				"newLines": h["newLines"],
				"lines":    h["lines"],
			})
		}
		if !reflect.DeepEqual(got, want) {
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(want)
			t.Fatalf("diff[%d] mismatch\n got %s\nwant %s", i, gb, wb)
		}
	}
}

func lower(s string) string { return strings.ToLower(s) }

func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// TestReadStateKeyVectors pins createReadFileStateKey against the real TS helper.
func TestReadStateKeyVectors(t *testing.T) {
	v := loadMeta(t)
	if len(v.ReadStateKeys) == 0 {
		t.Fatal("no readStateKeys vectors")
	}
	for _, c := range v.ReadStateKeys {
		abs := filepath.Join("/tmp/nex-fixed/ws", filepath.FromSlash(c.Path))
		offset, limit, _ := readOffsetLimit(c.Input)
		got := tools2.CreateReadStateKey(abs, offset, limit)
		want := replaceWS(c.Key, "/tmp/nex-fixed/ws")
		if got != want {
			t.Fatalf("read-state key mismatch\n got: %q\nwant: %q", got, want)
		}
	}
}

func readOffsetLimit(input map[string]any) (*int, *int, bool) {
	var off, lim *int
	if v, ok := input["offset"].(float64); ok {
		i := int(v)
		off = &i
	}
	if v, ok := input["limit"].(float64); ok {
		i := int(v)
		lim = &i
	}
	return off, lim, true
}
