package runtime

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/sessionstore"
	"github.com/tylinux/nex/go/internal/tools"
)

// ── fakes ──────────────────────────────────────────────────────────────

type fakeClock struct{ ms int64 }

func (c *fakeClock) NowMs() int64 { return c.ms }
func (c *fakeClock) NewTimer(time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time)
	return ch, func() {}
}

type fakeIDs struct{ n int }

func (i *fakeIDs) next(p string) string { i.n++; return p + itoa(i.n) }
func (i *fakeIDs) SessionID() string    { return i.next("sess_") }
func (i *fakeIDs) TurnID() string       { return i.next("turn_") }
func (i *fakeIDs) MessageID() string    { return i.next("msg_") }
func (i *fakeIDs) PartID() string       { return i.next("part_") }
func (i *fakeIDs) EventID() string      { return i.next("ev_") }
func (i *fakeIDs) UUID() string         { return i.next("uuid-") }
func (i *fakeIDs) LogEpoch() string     { return "epoch1" }

type fakeOutbox struct {
	mu     sync.Mutex
	frames []protocol.Message
}

func (o *fakeOutbox) WriteMessage(m protocol.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.frames = append(o.frames, m)
	return nil
}
func (o *fakeOutbox) snapshot() []protocol.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]protocol.Message(nil), o.frames...)
}

type fakeFS struct{ inner tools.FS }

func (f fakeFS) ToolFS() tools.FS { return f.inner }

// fakeStore is a no-op session store (the runtime only needs it not to error).
type fakeStore struct{}

func (fakeStore) CreateSession(sessionstore.CreateSessionInput) (sessionstore.SessionRow, error) {
	return sessionstore.SessionRow{}, nil
}
func (fakeStore) UpdateTitle(string, string, string, int64) (sessionstore.SessionRow, error) {
	return sessionstore.SessionRow{}, nil
}
func (fakeStore) GetSession(string) (*sessionstore.SessionRow, error) { return nil, nil }
func (fakeStore) UpsertSessionEntry(sessionstore.SessionEntry) error  { return nil }
func (fakeStore) AdmitInput(sessionstore.SessionInput) error          { return nil }
func (fakeStore) UpdateInputs(string, []sessionstore.SessionInputUpdate) error {
	return nil
}
func (fakeStore) PromoteInput(string, string, sessionstore.Message, []sessionstore.Part) error {
	return nil
}
func (fakeStore) MarkInputPromoted(string, string, string) error   { return nil }
func (fakeStore) SettleInput(string, string, string, string) error { return nil }
func (fakeStore) SaveMessage(sessionstore.Message) error           { return nil }
func (fakeStore) SavePart(sessionstore.Part) error                 { return nil }
func (fakeStore) ListMessages(string) ([]sessionstore.MessageWithParts, error) {
	return nil, nil
}
func (fakeStore) RecordModelUsage(sessionstore.ModelUsage) error { return nil }
func (fakeStore) UpsertTurnUsage(sessionstore.TurnUsage) error   { return nil }
func (fakeStore) UpsertToolUsage(sessionstore.ToolUsage) error   { return nil }
func (fakeStore) RecordInputHistory(sessionstore.InputHistory) (string, error) {
	return "", nil
}
func (fakeStore) UpdateTodos(string, []sessionstore.Todo) error { return nil }

// fakeTransport returns one SSE body per call.
type fakeTransport struct {
	mu     sync.Mutex
	bodies []string
}

func (t *fakeTransport) Do(_ context.Context, _ modelclient.Request) (*modelclient.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var body string
	if len(t.bodies) > 0 {
		body = t.bodies[0]
		t.bodies = t.bodies[1:]
	}
	return &modelclient.Response{StatusCode: 200, Stream: io.NopCloser(strings.NewReader(body))}, nil
}

// ── tests ──────────────────────────────────────────────────────────────

func newTestRuntime(t *testing.T, transport ModelTransport) (*Runtime, *fakeOutbox, *fakeClock) {
	t.Helper()
	clk := &fakeClock{ms: 1_700_000_000_000}
	ob := &fakeOutbox{}
	rt := New(Deps{
		Clock:       clk,
		IDs:         &fakeIDs{},
		Outbox:      ob,
		Store:       fakeStore{},
		FS:          fakeFS{},
		Transport:   transport,
		ModelConfig: modelclient.Config{ProviderID: "mock", ModelID: "mock", ProviderKind: "openai-compatible", Transport: "sse"},
		Provider:    Provider{ProviderID: "mock", ProviderName: "Mock", ProviderKind: "openai-compatible", ModelID: "mock", ModelName: "Mock Model", BaseURL: "http://127.0.0.1:1/v1", ContextWindow: 100000, MaxOutputToken: 8000, ReasoningLevels: []string{"none"}, DefaultReasoning: "none"},
		Platform:    "linux", OSVersion: "linux 1 x64", PlatformArch: "x64", Shell: "zsh",
	}, "trace-test", "1.0.0")
	return rt, ob, clk
}

func TestCreateSessionEmitsPrefsThenResponse(t *testing.T) {
	rt, ob, _ := newTestRuntime(t, &fakeTransport{})
	// Answer the two prefs requests as they appear.
	done := make(chan struct{})
	var answered sync.Map
	go func() {
		for {
			for _, f := range ob.snapshot() {
				if f.Kind == protocol.KindRequest && f.Method == "session/requestRuntimePreferences" {
					if _, loaded := answered.LoadOrStore(f.ID.String(), true); !loaded {
						rt.HandleResponse(protocol.Message{Kind: protocol.KindResponse, ID: f.ID,
							Result: json.RawMessage(`{"memoryEnabled":false,"askUserQuestionAutoResolutionEnabled":true}`)})
					}
				}
			}
			select {
			case <-done:
				return
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}()

	params, _ := json.Marshal(map[string]any{
		"commandId": "cmd-1", "clientId": "c", "sessionId": nil, "type": "createSession",
		"payload": map[string]any{"workspaceId": "/tmp/ws", "firstInput": map[string]any{"text": "hi there", "modelSelection": map[string]any{"providerId": "mock", "modelId": "mock", "options": map[string]any{"reasoningLevel": "none"}}}},
	})
	result, err := rt.HandleRequest(context.Background(), protocol.Message{Kind: protocol.KindRequest, Method: MethodV4Command, Params: params, HasParams: true})
	close(done)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	var res struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(result, &res); err != nil || res.Status != "accepted" {
		t.Fatalf("result = %s (%v), want accepted", result, err)
	}
	// The prefs requests must precede the command response in the frame order.
	frames := ob.snapshot()
	var prefs, mcp int
	order := []string{}
	for _, f := range frames {
		switch f.Method {
		case "session/requestRuntimePreferences":
			prefs++
			order = append(order, "prefs")
		case "process/mcpTelemetry":
			mcp++
			order = append(order, "mcp")
		}
	}
	if prefs != 2 || mcp != 1 {
		t.Fatalf("prefs=%d mcp=%d, want 2 and 1 (order %v)", prefs, mcp, order)
	}
	if order[0] != "prefs" || order[1] != "mcp" || order[2] != "prefs" {
		t.Errorf("frame order = %v, want prefs, mcp, prefs", order)
	}
}

func mustSession(t *testing.T, rt *Runtime) *session {
	t.Helper()
	s, err := rt.newSession("sess_1", "epoch1", Workspace{Path: "/tmp/ws", Key: "/tmp/ws"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPreparedInputJSONPreservesKeyOrder(t *testing.T) {
	got := preparedInputJSON("Edit", `{"file_path":"/a","old_string":"x","new_string":"y"}`, nil)
	want := `{"file_path":"/a","old_string":"x","new_string":"y","replace_all":false}`
	if got != want {
		t.Fatalf("preparedInputJSON = %s, want %s", got, want)
	}
	// replace_all present is kept in place.
	got2 := preparedInputJSON("Edit", `{"file_path":"/a","old_string":"x","new_string":"y","replace_all":true}`, nil)
	if got2 != `{"file_path":"/a","old_string":"x","new_string":"y","replace_all":true}` {
		t.Fatalf("preparedInputJSON kept = %s", got2)
	}
	// Unknown keys are stripped.
	got3 := preparedInputJSON("Write", `{"file_path":"/a","content":"c","extra":1}`, nil)
	if got3 != `{"file_path":"/a","content":"c"}` {
		t.Fatalf("preparedInputJSON stripped = %s", got3)
	}
}

func TestTitleAndSlugHelpers(t *testing.T) {
	if got := titleFromInput("  Say   hello  "); got != "Say hello" {
		t.Errorf("titleFromInput = %q", got)
	}
	if got := projectIDForDirectory("/work/proj"); got != "proj_work-proj" {
		t.Errorf("projectIDForDirectory = %q", got)
	}
	if got := countContentLines("alpha\nbeta\ngamma\n"); got != 3 {
		t.Errorf("countContentLines = %d, want 3", got)
	}
}

func TestHasToolInputLineBreak(t *testing.T) {
	if !hasToolInputLineBreak("a\\nb") || !hasToolInputLineBreak("a\nb") {
		t.Error("line break not detected")
	}
	if hasToolInputLineBreak("plain") {
		t.Error("plain text reported a line break")
	}
}
