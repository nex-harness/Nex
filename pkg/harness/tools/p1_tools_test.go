package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func TestFileStateTrackerReadBeforeEditAndStaleDetection(t *testing.T) {
	dir := t.TempDir()
	tracker := NewFileStateTracker()
	rt := NewReadToolWithTracker(tracker)
	wt := NewWriteToolWithTracker(tracker)
	et := NewEditToolWithTracker(tracker)

	existing := filepath.Join(dir, "app.go")
	if err := os.WriteFile(existing, []byte("package app\n\nconst Version = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Edit before read must fail
	res, err := et.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"app.go","old_string":"Version = 1","new_string":"Version = 2"}`), nil)
	if err != nil || !res.IsError || !strings.Contains(res.Output, "has not been read") {
		t.Fatalf("expected read-before-edit rejection, got res=%+v err=%v", res, err)
	}

	// 2. Write overwriting existing unread file must fail
	res, err = wt.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"app.go","content":"overwrite"}`), nil)
	if err != nil || !res.IsError || !strings.Contains(res.Output, "has not been read") {
		t.Fatalf("expected read-before-overwrite rejection, got res=%+v err=%v", res, err)
	}

	// 3. Write creating a BRAND NEW file must succeed without prior read
	res, err = wt.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"new.txt","content":"fresh"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("expected new file write to succeed, got res=%+v err=%v", res, err)
	}

	// 4. Read app.go -> then edit succeeds
	res, err = rt.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"app.go"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("read failed: %+v err=%v", res, err)
	}
	res, err = et.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"app.go","old_string":"Version = 1","new_string":"Version = 2"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("edit after read failed: %+v err=%v", res, err)
	}

	// 5. Simulate external modification (different size + future mtime) -> edit must reject as stale!
	if err := os.WriteFile(existing, []byte("package app\n\nconst Version = 2 // external edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(5 * time.Second)
	_ = os.Chtimes(existing, future, future)

	res, err = et.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"app.go","old_string":"Version = 2","new_string":"Version = 3"}`), nil)
	if err != nil || !res.IsError || !strings.Contains(res.Output, "modified externally") {
		t.Fatalf("expected stale-file rejection, got res=%+v err=%v", res, err)
	}

	// 6. Re-read -> edit succeeds again
	_, _ = rt.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"app.go"}`), nil)
	res, err = et.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"app.go","old_string":"Version = 2","new_string":"Version = 3"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("edit after re-read failed: %+v err=%v", res, err)
	}
}

func TestBashReadOnlyCommandChainPolicy(t *testing.T) {
	readOnlyCases := []string{
		"git status",
		"git -C /tmp diff --stat",
		"git log -n 10 --oneline",
		"git branch -a",
		"git tag -l",
		"LC_ALL=C ls -la && cat README.md | head -n 5",
		"rg 'TODO' pkg/ | wc -l",
		"go list ./...",
		"find . -name '*.go' | sort | uniq",
		"gh pr list",
	}
	for _, cmd := range readOnlyCases {
		if !IsReadOnlyBashCommand(cmd) {
			t.Errorf("expected read-only=true for %q", cmd)
		}
	}

	mutatingCases := []string{
		"rm -rf build/",
		"git status && rm foo.txt",
		"git commit -m 'msg'",
		"git push origin main",
		"git branch new-feature",
		"git branch -D old-branch",
		"cat a.txt > b.txt",
		"echo $(rm -rf /tmp/x)",
		"echo `whoami`",
		"sed -i 's/a/b/g' file.txt",
		"find . -name '*.tmp' -delete",
		"go test ./...",
		"go env -w GOPATH=/tmp",
		"sort -o out.txt in.txt",
		"sleep 10 &",
	}
	for _, cmd := range mutatingCases {
		if IsReadOnlyBashCommand(cmd) {
			t.Errorf("expected read-only=false for %q", cmd)
		}
	}

	bt := NewBashTool()
	if !bt.IsReadOnly(json.RawMessage(`{"command":"git status"}`)) {
		t.Fatal("BashTool.IsReadOnly should be true for git status")
	}
	if bt.IsReadOnly(json.RawMessage(`{"command":"git status","run_in_background":true}`)) {
		t.Fatal("BashTool.IsReadOnly should be false when run_in_background=true")
	}
}

func TestBashBackgroundTaskLifecycle(t *testing.T) {
	dir := t.TempDir()
	mgr := NewBackgroundTaskManager()
	defer mgr.StopAll()

	bt := NewBashToolWithBackground(mgr)
	outTool := NewTaskOutputTool(mgr)
	stopTool := NewTaskStopTool(mgr)

	// 1. Start a fast background task and collect its output
	res, err := bt.Execute(context.Background(), dir, json.RawMessage(`{"command":"printf 'bg-hello\\n'","run_in_background":true}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("start bg task failed: %+v err=%v", res, err)
	}
	taskID, _ := res.Metadata["task_id"].(string)
	if taskID == "" {
		t.Fatalf("missing task_id in metadata: %+v", res.Metadata)
	}

	outArgs, _ := json.Marshal(map[string]any{"task_id": taskID, "block": true, "timeout_ms": 5000})
	oRes, err := outTool.Execute(context.Background(), dir, outArgs, nil)
	if err != nil || oRes.IsError || !strings.Contains(oRes.Output, "bg-hello") || !strings.Contains(oRes.Output, "[completed]") {
		t.Fatalf("unexpected task_output: %+v err=%v", oRes, err)
	}

	// 2. Start a long-running background task and terminate it with task_stop
	res2, err := bt.Execute(context.Background(), dir, json.RawMessage(`{"command":"sleep 30","run_in_background":true}`), nil)
	if err != nil || res2.IsError {
		t.Fatalf("start long bg task failed: %+v err=%v", res2, err)
	}
	taskID2, _ := res2.Metadata["task_id"].(string)
	stopArgs, _ := json.Marshal(map[string]any{"task_id": taskID2})
	sRes, err := stopTool.Execute(context.Background(), dir, stopArgs, nil)
	if err != nil || sRes.IsError || !strings.Contains(sRes.Output, "Stopped") {
		t.Fatalf("task_stop failed: %+v err=%v", sRes, err)
	}
}

func TestWebFetchSSRFGuardHTMLStrippingAndCache(t *testing.T) {
	wf := NewWebFetchTool()

	// 1. SSRF protection blocks private/loopback/metadata addresses by default
	for _, blockedURL := range []string{
		"http://127.0.0.1:8080/admin",
		"http://localhost/secret",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.1/internal",
		"file:///etc/passwd",
	} {
		res, err := wf.Execute(context.Background(), "", json.RawMessage(`{"url":"`+blockedURL+`"}`), nil)
		if err != nil || !res.IsError {
			t.Fatalf("expected SSRF/scheme block for %s, got %+v err=%v", blockedURL, res, err)
		}
	}

	// 2. HTML extraction & caching with test server
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><style>body{color:red}</style><script>alert(1)</script></head><body><h1>Nex Docs</h1><p>Hello &amp; welcome!</p></body></html>`))
	}))
	defer srv.Close()

	wf.allowPrivateHosts = true
	args, _ := json.Marshal(map[string]any{"url": srv.URL})
	res1, err := wf.Execute(context.Background(), "", args, nil)
	if err != nil || res1.IsError {
		t.Fatalf("webfetch failed: %+v err=%v", res1, err)
	}
	if !strings.Contains(res1.Output, "Nex Docs") || !strings.Contains(res1.Output, "Hello & welcome!") || strings.Contains(res1.Output, "alert(1)") || strings.Contains(res1.Output, "color:red") {
		t.Fatalf("unexpected cleaned HTML output:\n%s", res1.Output)
	}

	// Second fetch should hit in-memory TTL cache
	res2, err := wf.Execute(context.Background(), "", args, nil)
	if err != nil || res2.IsError || res2.Metadata["cached"] != true {
		t.Fatalf("expected cached response on second fetch, got %+v err=%v", res2, err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected server to be hit once (cached second time), got %d", hits)
	}
}

type mockUIPrompter struct {
	hasUI bool
	reply protocol.UIResolvePayload
}

func (m *mockUIPrompter) HasUI() bool { return m.hasUI }
func (m *mockUIPrompter) Prompt(_ context.Context, _ protocol.UIPromptRequestPayload) (protocol.UIResolvePayload, error) {
	return m.reply, nil
}

func TestAskUserQuestionTool(t *testing.T) {
	// Interactive select
	ui := &mockUIPrompter{hasUI: true, reply: protocol.UIResolvePayload{Value: "PostgreSQL"}}
	tool := NewAskUserQuestionTool(ui)

	res, err := tool.Execute(context.Background(), "", json.RawMessage(`{"question":"Which DB?","options":["SQLite","PostgreSQL"]}`), nil)
	if err != nil || res.IsError || !strings.Contains(res.Output, "PostgreSQL") {
		t.Fatalf("unexpected select result: %+v err=%v", res, err)
	}

	// Interactive confirm
	ui.reply = protocol.UIResolvePayload{Confirmed: true}
	res, err = tool.Execute(context.Background(), "", json.RawMessage(`{"question":"Proceed?","kind":"confirm"}`), nil)
	if err != nil || res.IsError || !strings.Contains(res.Output, "yes") {
		t.Fatalf("unexpected confirm result: %+v err=%v", res, err)
	}

	// Headless fallback with default_value
	ui.hasUI = false
	res, err = tool.Execute(context.Background(), "", json.RawMessage(`{"question":"Region?","options":["us","eu"],"default_value":"us"}`), nil)
	if err != nil || res.IsError || !strings.Contains(res.Output, "us") {
		t.Fatalf("unexpected headless default result: %+v err=%v", res, err)
	}

	// Headless without default_value returns clear error
	res, err = tool.Execute(context.Background(), "", json.RawMessage(`{"question":"API token?"}`), nil)
	if err != nil || !res.IsError || !strings.Contains(res.Output, "headless") {
		t.Fatalf("expected headless error without default, got %+v err=%v", res, err)
	}
}

type mockSessionLookup struct {
	recs map[string]*store.SessionRecord
	evs  map[string][]store.SessionEvent
}

func (m *mockSessionLookup) GetSession(_ context.Context, id string) (*store.SessionRecord, error) {
	if r, ok := m.recs[id]; ok {
		return r, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockSessionLookup) ListSessionEventsAfter(_ context.Context, sessionID string, _ uint64) ([]store.SessionEvent, error) {
	return m.evs[sessionID], nil
}

func TestReadSessionContextAndReferences(t *testing.T) {
	refs := ExtractSessionReferences("Compare #sess_alpha-1 with #session_beta.2 and #sess_alpha-1 again")
	if len(refs) != 2 || refs[0] != "sess_alpha-1" || refs[1] != "session_beta.2" {
		t.Fatalf("unexpected refs: %+v", refs)
	}

	lookup := &mockSessionLookup{
		recs: map[string]*store.SessionRecord{
			"sess_alpha-1": {ID: "sess_alpha-1", Title: "Weather Discussion", WorkspacePath: "/tmp/ws", ModelID: "grok-4.7"},
		},
		evs: map[string][]store.SessionEvent{
			"sess_alpha-1": {
				{Seq: 1, EventType: "message_end", Payload: json.RawMessage(`{"message":{"role":"user","content":[{"type":"text","text":"What is Beijing weather tomorrow?"}]}}`)},
				{Seq: 2, EventType: "message_end", Payload: json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"text","text":"Beijing tomorrow will be sunny, 0 to 5C."}]}}`)},
			},
		},
	}

	reminder := BuildReferencedSessionReminder(context.Background(), lookup, "Please check #sess_alpha-1")
	if !strings.Contains(reminder, "sess_alpha-1 (Weather Discussion)") || !strings.Contains(reminder, "read_session_context") {
		t.Fatalf("unexpected reminder:\n%s", reminder)
	}

	tool := NewReadSessionContextTool(lookup, "sess_current")
	if !tool.IsReadOnly(nil) || tool.Name() != "read_session_context" {
		t.Fatalf("expected read-only read_session_context tool")
	}

	res, err := tool.Execute(context.Background(), "", json.RawMessage(`{"session_id":"#sess_alpha-1","query":"Beijing"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("read_session_context failed: %+v err=%v", res, err)
	}
	if !strings.Contains(res.Output, "Weather Discussion") || !strings.Contains(res.Output, "sunny, 0 to 5C") {
		t.Fatalf("unexpected transcript output:\n%s", res.Output)
	}

	missing, err := tool.Execute(context.Background(), "", json.RawMessage(`{"session_id":"sess_nope"}`), nil)
	if err != nil || !missing.IsError {
		t.Fatalf("expected missing session error, got %+v err=%v", missing, err)
	}
}
