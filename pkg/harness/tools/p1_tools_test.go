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

func TestBackgroundTaskManagerCallbacksAndInitiator(t *testing.T) {
	dir := t.TempDir()
	mgr := NewBackgroundTaskManager()
	defer mgr.StopAll()

	startedCh := make(chan BackgroundTaskSnapshot, 4)
	completedCh := make(chan BackgroundTaskSnapshot, 4)
	mgr.SetCallbacks(
		func(snap BackgroundTaskSnapshot) { startedCh <- snap },
		func(snap BackgroundTaskSnapshot) { completedCh <- snap },
	)

	// 1. Completing command triggers onStart -> completed with tail output
	idOK := mgr.Start(dir, "printf 'ci-green\\n'", 5*time.Second)
	s1 := <-startedCh
	if s1.TaskID != idOK || s1.Status != "running" {
		t.Fatalf("unexpected start snapshot: %+v", s1)
	}
	c1 := <-completedCh
	if c1.TaskID != idOK || c1.Status != "completed" || c1.ExitCode != 0 || !strings.Contains(c1.OutputTail, "ci-green") {
		t.Fatalf("unexpected completed snapshot: %+v", c1)
	}

	// 2. Failing command triggers onComplete with status=failed
	idFail := mgr.Start(dir, "printf 'ci-red\\n' >&2; exit 2", 5*time.Second)
	<-startedCh
	c2 := <-completedCh
	if c2.TaskID != idFail || c2.Status != "failed" || c2.ExitCode != 2 {
		t.Fatalf("unexpected failed snapshot: %+v", c2)
	}

	// 3. Long command shows up in ListRunning and StopWithInitiator records user
	idLong := mgr.Start(dir, "sleep 30", 30*time.Second)
	<-startedCh
	running := mgr.ListRunning()
	if len(running) != 1 || running[0].TaskID != idLong {
		t.Fatalf("expected %s in ListRunning, got %+v", idLong, running)
	}
	if err := mgr.StopWithInitiator(idLong, "user"); err != nil {
		t.Fatal(err)
	}
	c3 := <-completedCh
	if c3.TaskID != idLong || c3.Status != "killed" || c3.StoppedBy != "user" {
		t.Fatalf("unexpected killed snapshot: %+v", c3)
	}
	if len(mgr.ListRunning()) != 0 {
		t.Fatalf("expected 0 running tasks after stop, got %+v", mgr.ListRunning())
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
	hasUI   bool
	reply   protocol.UIResolvePayload
	lastReq protocol.UIPromptRequestPayload
}

func (m *mockUIPrompter) HasUI() bool { return m.hasUI }
func (m *mockUIPrompter) Prompt(_ context.Context, req protocol.UIPromptRequestPayload) (protocol.UIResolvePayload, error) {
	m.lastReq = req
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

	// Structured options + header
	ui.hasUI = true
	ui.reply = protocol.UIResolvePayload{Value: "先规划再动手"}
	res, err = tool.Execute(context.Background(), "", json.RawMessage(`{
		"header": "测试",
		"question": "你更喜欢哪种工作方式？",
		"options": [
			{"label": "先规划再动手", "description": "先给出方案和步骤，确认后再执行"},
			{"label": "直接开干", "description": "不需要确认，直接开始做事"}
		]
	}`), nil)
	if err != nil || res.IsError || !strings.Contains(res.Output, "先规划再动手") {
		t.Fatalf("unexpected structured select result: %+v err=%v", res, err)
	}
	if ui.lastReq.Header != "测试" || len(ui.lastReq.OptionItems) != 2 || ui.lastReq.OptionItems[0].Description == "" || len(ui.lastReq.Options) != 2 {
		t.Fatalf("expected structured OptionItems and legacy Options labels, got %+v", ui.lastReq)
	}

	// Multi-question paginated payload & JSON answers decoding
	ui.reply = protocol.UIResolvePayload{Value: `{"answers":[{"question":"DB?","answer":"SQLite"},{"question":"Auth?","answer":"JWT"}]}`}
	res, err = tool.Execute(context.Background(), "", json.RawMessage(`{
		"questions": [
			{"header": "Storage", "question": "DB?", "options": ["SQLite", "Postgres"]},
			{"header": "Security", "question": "Auth?", "options": ["JWT", "Session"]}
		]
	}`), nil)
	if err != nil || res.IsError || !strings.Contains(res.Output, "1. DB?: SQLite") || !strings.Contains(res.Output, "2. Auth?: JWT") {
		t.Fatalf("unexpected multi-question result: %+v err=%v", res, err)
	}
	if len(ui.lastReq.Questions) != 2 || ui.lastReq.Title != "DB?" {
		t.Fatalf("expected 2 questions in payload, got %+v", ui.lastReq)
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

func TestSpillLargeOutputToDisk(t *testing.T) {
	small, path := SpillLargeOutput("grep", "short output", 1000)
	if small != "short output" || path != "" {
		t.Fatalf("expected small output untouched, got %q path=%q", small, path)
	}

	big := strings.Repeat("match line 1234567890\n", 500)
	inline, spillPath := SpillLargeOutput("grep", big, 1024)
	if spillPath == "" {
		t.Fatalf("expected spillPath for oversized output")
	}
	defer os.Remove(spillPath)
	if !strings.Contains(inline, spillPath) || !strings.Contains(inline, "output truncated") {
		t.Fatalf("expected inline truncation notice with spill path, got:\n%s", inline)
	}
	raw, err := os.ReadFile(spillPath)
	if err != nil || string(raw) != big {
		t.Fatalf("expected full spilled output on disk, err=%v len=%d", err, len(raw))
	}
}

func TestWebSearchPDFExitPlanAndMemoryTools(t *testing.T) {
	// 1. WebSearch with custom mock JSON server + domain filter + cache
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Go Docs","url":"https://pkg.go.dev/net/http","content":"HTTP client and server"},
			{"title":"Noise","url":"https://spam.example.com/x","content":"ignore me"}
		]}`))
	}))
	defer srv.Close()

	ws := NewWebSearchTool()
	ws.customURL = srv.URL
	res, err := ws.Execute(context.Background(), "", json.RawMessage(`{"query":"golang http","allowed_domains":["pkg.go.dev"]}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("websearch failed: %+v err=%v", res, err)
	}
	if !strings.Contains(res.Output, "Go Docs") || strings.Contains(res.Output, "spam.example.com") {
		t.Fatalf("unexpected websearch output:\n%s", res.Output)
	}
	res2, _ := ws.Execute(context.Background(), "", json.RawMessage(`{"query":"golang http","allowed_domains":["pkg.go.dev"]}`), nil)
	if res2.Metadata["cached"] != true || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected cached websearch hit, got meta=%+v hits=%d", res2.Metadata, hits)
	}

	// 2. Pure-Go PDF extraction via ReadTool
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "sample.pdf")
	pdfBytes := []byte("%PDF-1.4\n1 0 obj\n<< /Length 44 >>\nstream\nBT /F1 12 Tf (Hello Nex PDF Reader) Tj ET\nendstream\nendobj\n%%EOF\n")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rt := NewReadTool()
	pdfRes, err := rt.Execute(context.Background(), dir, json.RawMessage(`{"file_path":"sample.pdf"}`), nil)
	if err != nil || pdfRes.IsError || !strings.Contains(pdfRes.Output, "Hello Nex PDF Reader") {
		t.Fatalf("unexpected PDF read result: %+v err=%v", pdfRes, err)
	}

	// 3. exit_plan_mode tool
	ep := NewExitPlanModeTool()
	epRes, err := ep.Execute(context.Background(), dir, json.RawMessage(`{"title":"Auth Plan","plan_markdown":"## Steps\n1. Add JWT"}`), nil)
	if err != nil || epRes.IsError || epRes.Metadata["kind"] != "plan_proposal" {
		t.Fatalf("unexpected exit_plan_mode result: %+v err=%v", epRes, err)
	}

	// 4. Memory save & list tools backed by real SQLite store
	st, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ms := NewMemorySaveTool(st, dir)
	ml := NewMemoryListTool(st, dir)
	mr := NewMemoryReadTool(st, dir)
	md := NewMemoryDeleteTool(st)

	// Rejects secret-like API key material
	secRes, err := ms.Execute(context.Background(), dir, json.RawMessage(`{"title":"OpenAI Key","content":"Use sk-proj-abcdefghijklmnopqrstuvwxyz123456"}`), nil)
	if err != nil || !secRes.IsError || !strings.Contains(secRes.Output, "secret") {
		t.Fatalf("expected secret refusal, got %+v err=%v", secRes, err)
	}

	mRes, err := ms.Execute(context.Background(), dir, json.RawMessage(`{"scope":"global","category":"preference","title":"Static Go","content":"Always use CGO_ENABLED=0"}`), nil)
	if err != nil || mRes.IsError {
		t.Fatalf("memory_save failed: %+v err=%v", mRes, err)
	}
	savedID, _ := mRes.Metadata["memory_id"].(string)
	lRes, err := ml.Execute(context.Background(), dir, json.RawMessage(`{}`), nil)
	if err != nil || lRes.IsError || !strings.Contains(lRes.Output, "Always use CGO_ENABLED=0") {
		t.Fatalf("memory_list failed: %+v err=%v", lRes, err)
	}
	rRes, err := mr.Execute(context.Background(), dir, json.RawMessage(`{"ids":["`+savedID+`"]}`), nil)
	if err != nil || rRes.IsError || !strings.Contains(rRes.Output, "Always use CGO_ENABLED=0") {
		t.Fatalf("memory_read by id failed: %+v err=%v", rRes, err)
	}
	rqRes, err := mr.Execute(context.Background(), dir, json.RawMessage(`{"query":"CGO_ENABLED"}`), nil)
	if err != nil || rqRes.IsError || !strings.Contains(rqRes.Output, "Static Go") {
		t.Fatalf("memory_read by query failed: %+v err=%v", rqRes, err)
	}
	dRes, err := md.Execute(context.Background(), dir, json.RawMessage(`{"id":"`+savedID+`"}`), nil)
	if err != nil || dRes.IsError {
		t.Fatalf("memory_delete failed: %+v err=%v", dRes, err)
	}
	afterDel, _ := ml.Execute(context.Background(), dir, json.RawMessage(`{}`), nil)
	if !strings.Contains(afterDel.Output, "No persistent memories") {
		t.Fatalf("expected empty memories after delete, got %q", afterDel.Output)
	}
}
