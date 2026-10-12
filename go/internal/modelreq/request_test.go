package modelreq

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/tylinux/nex/go/internal/promptctx"
	"github.com/tylinux/nex/go/internal/tooldefs"
)

type requestVector struct {
	Scenario string `json:"scenario"`
	Env      struct {
		Cwd             string `json:"cwd"`
		Platform        string `json:"platform"`
		Shell           string `json:"shell"`
		OSVersion       string `json:"osVersion"`
		IsGitRepository bool   `json:"isGitRepository"`
	} `json:"env"`
	Date       string          `json:"date"`
	ProviderID string          `json:"providerId"`
	ModelID    string          `json:"modelId"`
	UserText   string          `json:"userText"`
	Body       json.RawMessage `json:"body"`
}

func TestRequestBodyIsByteIdenticalToWhatNodeSent(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/vectors/request.json")
	if err != nil {
		t.Fatal(err)
	}
	var vecs []requestVector
	if err := json.Unmarshal(raw, &vecs); err != nil {
		t.Fatal(err)
	}
	if len(vecs) == 0 {
		t.Fatal("no request vectors")
	}
	tools, err := tooldefs.Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vecs {
		got, err := Build(Input{
			ModelID: v.ModelID,
			Context: promptctx.Config{
				Env:   promptctx.EnvInfo{Cwd: v.Env.Cwd, Platform: v.Env.Platform, Shell: v.Env.Shell, OSVersion: v.Env.OSVersion, IsGitRepository: &v.Env.IsGitRepository},
				Model: &promptctx.Model{ProviderID: v.ProviderID, ModelID: v.ModelID}, CurrentDate: v.Date,
			},
			History:         []Message{{Role: "user", Content: v.UserText}},
			MaxOutputTokens: 8000,
			Tools:           tools,
		})
		if err != nil {
			t.Fatal(err)
		}
		var want bytes.Buffer
		if err := json.Compact(&want, v.Body); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want.Bytes()) {
			i := 0
			for i < len(got) && i < want.Len() && got[i] == want.Bytes()[i] {
				i++
			}
			lo := max(0, i-60)
			t.Fatalf("request differs from Node's at byte %d (go %d bytes, node %d)\n go:   %q\n node: %q", i, len(got), want.Len(),
				got[lo:min(len(got), i+80)], want.Bytes()[lo:min(want.Len(), i+80)])
		}
	}
}

func TestToolListIsTheTwentyNodeSendsInOrder(t *testing.T) {
	defs, err := tooldefs.Default()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Agent", "AskUserQuestion", "Bash", "CronCreate", "CronDelete", "CronList", "CronUpdate", "Edit", "EnterPlanMode",
		"ExitPlanMode", "Read", "Skill", "TaskOutput", "TaskStop", "TodoRead", "TodoWrite", "WebFetch", "Write", "SendMessage", "ReadSessionContext"}
	if len(defs) != len(want) {
		t.Fatalf("got %d tools", len(defs))
	}
	for i, d := range defs {
		if d.Name != want[i] {
			t.Errorf("tool %d: %s, want %s", i, d.Name, want[i])
		}
	}
}
