package permission_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tylinux/nex/go/internal/permission"
)

// TestRequestPayloadMatchesS04Frame builds the interaction/requestPermission
// payload for the Write and Edit inputs the S04 recording used and compares it
// (ignoring the generated requestId) with the recorded frames. This ties the Go
// permission-request shape to the golden trace, not to a hand-written expectation.
func TestRequestPayloadMatchesS04Frame(t *testing.T) {
	frames := loadFrames(t, "S04")
	want := map[string]map[string]any{}
	for _, f := range frames {
		if f["method"] != "interaction/requestPermission" {
			continue
		}
		params, _ := f["params"].(map[string]any)
		tool, _ := params["toolName"].(string)
		want[tool] = params
	}
	if len(want) != 2 {
		t.Fatalf("expected Write and Edit requestPermission frames, got %d", len(want))
	}

	ws := "<WS>"
	cases := []struct {
		tool  string
		input map[string]any
	}{
		{"Write", map[string]any{"file_path": ws + "/notes.txt", "content": "alpha\nbeta\ngamma\n"}},
		{"Edit", map[string]any{"file_path": ws + "/notes.txt", "old_string": "beta", "new_string": "BETA", "replace_all": false}},
	}
	for _, c := range cases {
		w := want[c.tool]
		req := permission.BuildRequest(permission.RequestParams{
			Input:      c.input,
			Reason:     permission.PermissionAskReason,
			RiskLevel:  permission.RiskLevelMedium,
			SessionID:  w["sessionId"].(string),
			ToolCallID: w["toolCallId"].(string),
			ToolName:   c.tool,
			TurnID:     w["turnId"].(string),
		})
		reqJSON, _ := json.Marshal(req)
		var got map[string]any
		_ = json.Unmarshal(reqJSON, &got)

		if !strings.HasPrefix(got["requestId"].(string), "perm_") {
			t.Fatalf("%s requestId %q is not perm_<uuid>", c.tool, got["requestId"])
		}
		for _, key := range []string{"input", "reason", "riskLevel", "sessionId", "options", "toolCallId", "toolName", "turnId"} {
			if !reflect.DeepEqual(got[key], w[key]) {
				g, _ := json.MarshalIndent(got[key], "", " ")
				wb, _ := json.MarshalIndent(w[key], "", " ")
				t.Fatalf("%s params[%q] mismatch\n got %s\nwant %s", c.tool, key, g, wb)
			}
		}
	}
}

// TestDenialContentMatchesS05Request asserts the deny option reason equals the
// denial text the model receives in the S05 recording.
func TestDenialContentMatchesS05Request(t *testing.T) {
	frames := loadFrames(t, "S05")
	var denyReason string
	for _, f := range frames {
		if f["method"] != "interaction/requestPermission" {
			continue
		}
		params, _ := f["params"].(map[string]any)
		options, _ := params["options"].([]any)
		for _, o := range options {
			om, _ := o.(map[string]any)
			if om["optionId"] == "deny" {
				resp, _ := om["response"].(map[string]any)
				denyReason, _ = resp["reason"].(string)
			}
		}
	}
	if denyReason == "" {
		t.Fatal("no deny reason found in S05 frames")
	}
	if denyReason != permission.PermissionDeniedByUserContent {
		t.Fatalf("deny reason mismatch:\n got %q\nwant %q", permission.PermissionDeniedByUserContent, denyReason)
	}
}

func loadFrames(t *testing.T, scenario string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "traces", "agent", scenario, "out.normalized.json"))
	if err != nil {
		t.Fatal(err)
	}
	var frames []map[string]any
	if err := json.Unmarshal(raw, &frames); err != nil {
		t.Fatal(err)
	}
	return frames
}
