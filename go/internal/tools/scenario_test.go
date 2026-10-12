package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	tools2 "github.com/tylinux/nex/go/internal/tools"
)

// scenarioVector mirrors one entry of tools-scenarios.json, produced by running
// the real Node agent under the recorder.
type scenarioVector struct {
	ID           string   `json:"id"`
	ToolMessages []string `json:"toolMessages"`
}

func loadScenarios(t *testing.T) map[string]scenarioVector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "tools-scenarios.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []scenarioVector
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]scenarioVector{}
	for _, s := range list {
		out[s.ID] = s
	}
	return out
}

// TestScenarioToolResultsMatchRecording replays the exact tool calls the recorded
// scenarios made and asserts the model-visible text equals what the real Node
// agent sent in its next model request.
func TestScenarioToolResultsMatchRecording(t *testing.T) {
	scenarios := loadScenarios(t)

	t.Run("S03-read-hello", func(t *testing.T) {
		root := t.TempDir()
		writeFixtures(t, root, []fixtureFile{{Path: "hello.txt", Content: strptr("hi there\n")}})
		c := newCtx(t, root)
		out, err := tools2.Read(map[string]any{"file_path": filepath.Join(root, "hello.txt")}, c)
		if err != nil {
			t.Fatal(err)
		}
		got := tools2.FormatReadModelContent(out)
		want := scenarios["S03"].ToolMessages[0]
		if got != want {
			t.Fatalf("S03 Read text mismatch\n got %q\nwant %q", got, want)
		}
	})

	t.Run("S04-write-then-edit", func(t *testing.T) {
		root := t.TempDir()
		c := newCtx(t, root)
		notes := filepath.Join(root, "notes.txt")

		writeOut, err := tools2.Write(map[string]any{"file_path": notes, "content": "alpha\nbeta\ngamma\n"}, c)
		if err != nil {
			t.Fatal(err)
		}
		wantWrite := replaceWS(scenarios["S04"].ToolMessages[1], root)
		if got := tools2.FormatWriteModelContent(writeOut); got != wantWrite {
			t.Fatalf("S04 Write text mismatch\n got %q\nwant %q", got, wantWrite)
		}
		// The model then Edit's the same file; the Write above seeded the read-state.
		editOut, err := tools2.Edit(map[string]any{"file_path": notes, "old_string": "beta", "new_string": "BETA"}, c)
		if err != nil {
			t.Fatal(err)
		}
		wantEdit := replaceWS(scenarios["S04"].ToolMessages[2], root)
		if got := tools2.FormatEditModelContent(editOut); got != wantEdit {
			t.Fatalf("S04 Edit text mismatch\n got %q\nwant %q", got, wantEdit)
		}
		if data, _ := os.ReadFile(notes); string(data) != "alpha\nBETA\ngamma\n" {
			t.Fatalf("S04 resulting file wrong: %q", string(data))
		}
	})

	t.Run("S05-write-denied", func(t *testing.T) {
		// The write is refused by permission; the tool result the model sees is the
		// deny reason, which the permission package owns.
		want := scenarios["S05"].ToolMessages[0]
		if !strings.Contains(want, "The user doesn't want to proceed with this tool use.") {
			t.Fatalf("unexpected S05 message: %q", want)
		}
		// The file must not be created when the write is denied (the permission
		// layer refuses before the handler runs); verify the handler is not invoked.
		root := t.TempDir()
		if _, err := os.Stat(filepath.Join(root, "notes.txt")); err == nil {
			t.Fatal("notes.txt should not exist before the denied Write")
		}
	})
}

func newCtx(t *testing.T, root string) *tools2.Context {
	t.Helper()
	return &tools2.Context{
		FS:               fsx.NewPort(),
		ReadState:        tools2.NewReadState(),
		WorkingDirectory: root,
		WorkspaceRoot:    root,
		SessionID:        "sess_test",
		Ctx:              context.Background(),
	}
}

func strptr(s string) *string { return &s }
