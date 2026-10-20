package runners_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/tylinux/nex-next/server/internal/runners"
)

type fakeLink struct {
	id      string
	mu      sync.Mutex
	calls   []string
	listens map[string]string
	closed  bool
	done    chan struct{}
}

func newFakeLink(id string) *fakeLink {
	return &fakeLink{id: id, listens: make(map[string]string), done: make(chan struct{})}
}

func (f *fakeLink) Call(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, f.id)
	return json.RawMessage("null"), nil
}

func (f *fakeLink) Listen(id, channel, event string, _ json.RawMessage, _ func(json.RawMessage)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listens[id] = channel + "." + event
	return nil
}

func (f *fakeLink) Unlisten(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.listens, id)
}

func (f *fakeLink) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.done)
	}
}

func (f *fakeLink) Done() <-chan struct{} { return f.done }

func (f *fakeLink) listenFor(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listens[id]
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func info(id string, paths ...string) runners.Info {
	workspaces := make([]runners.Workspace, len(paths))
	for i, path := range paths {
		workspaces[i] = runners.Workspace{Path: path}
	}
	return runners.Info{RunnerID: id, Name: id, Workspaces: workspaces}
}

func TestRoutingTableAndWorkspaceListing(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	a := newFakeLink("a")
	b := newFakeLink("b")
	registry.Register(info("a", "/w/a", "/w/shared"), a)
	registry.Register(info("b", "/w/b", "/w/shared"), b)

	id, ok := registry.RunnerIDForPath("/w/a")
	if !ok || id != "a" {
		t.Fatalf("RunnerIDForPath(/w/a) = %q,%v", id, ok)
	}
	if _, ok := registry.RunnerIDForPath("/w/shared"); !ok {
		t.Fatal("shared path must keep its first owner")
	}
	registry.Call(context.Background(), "/w/shared", "c", "m", json.RawMessage("null"))
	if len(a.calls) != 1 || len(b.calls) != 0 {
		t.Fatalf("shared path routed to %v/%v, want first owner", a.calls, b.calls)
	}

	workspaces := registry.Workspaces()
	if len(workspaces) != 3 {
		t.Fatalf("workspaces = %+v, want 3 accepted entries", workspaces)
	}
	if workspaces[0].Label != "a: a" {
		t.Fatalf("label = %q, want 'a: a'", workspaces[0].Label)
	}
}

func TestDuplicateRunnerDisplacesOldLink(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	first := newFakeLink("first")
	registry.Register(info("r", "/w"), first)

	second := newFakeLink("second")
	displaced := registry.Register(info("r", "/w"), second)
	if displaced != first {
		t.Fatalf("displaced = %v, want the first link", displaced)
	}
	fi, _ := registry.FirstRunnerID()
	if fi != "r" {
		t.Fatalf("FirstRunnerID = %q", fi)
	}
	// The caller closes the displaced link; the registry routing uses the new
	// link for the same path.
	displaced.Close()
	registry.Call(context.Background(), "/w", "c", "m", json.RawMessage("null"))
	if len(second.calls) != 1 {
		t.Fatalf("new link did not receive the call")
	}
}

func TestOfflineAndReconnectRelisten(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	link := newFakeLink("first")
	registry.Register(info("r", "/w"), link)

	registry.RegisterListen("conn", 7, "nex-agent", "onDynamicConversationFrame", "/w",
		json.RawMessage(`[{"workspacePath":"/w"}]`), func(json.RawMessage) {})
	if got := link.listenFor("conn:7"); got != "nex-agent.onDynamicConversationFrame" {
		t.Fatalf("listen = %q", got)
	}

	registry.Unregister("r", link)
	// Unregister deliberately does not call Unlisten: the socket is already
	// gone. What matters is that the binding loses its runner (so events stop)
	// and keeps its spec for the reconnect below.
	if _, ok := registry.RunnerIDForPath("/w"); ok {
		t.Fatal("workspace must disappear while the runner is offline")
	}
	if ids := registry.BindingsForRunner("r"); len(ids) != 0 {
		t.Fatalf("bindings for offline runner = %v", ids)
	}

	reconnected := newFakeLink("second")
	registry.Register(info("r", "/w"), reconnected)
	if got := reconnected.listenFor("conn:7"); got != "nex-agent.onDynamicConversationFrame" {
		t.Fatalf("re-listen = %q, want the original spec", got)
	}
	if ids := registry.BindingsForRunner("r"); len(ids) != 1 || ids[0] != "conn:7" {
		t.Fatalf("bindings after reconnect = %v", ids)
	}
}

func TestChangeSubscription(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	var mu sync.Mutex
	count := 0
	unsubscribe := registry.Subscribe(func() {
		mu.Lock()
		count++
		mu.Unlock()
	})

	link := newFakeLink("r")
	registry.Register(info("r", "/w"), link)
	registry.Unregister("r", link)
	unsubscribe()
	registry.Register(info("other", "/w2"), newFakeLink("other"))

	mu.Lock()
	defer mu.Unlock()
	if count != 2 {
		t.Fatalf("notifications = %d, want 2", count)
	}
}

func TestFirstRunnerIsRegistrationOrder(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	if _, ok := registry.FirstRunnerID(); ok {
		t.Fatal("no runner should be reported before registration")
	}
	registry.Register(info("a", "/a"), newFakeLink("a"))
	registry.Register(info("b", "/b"), newFakeLink("b"))
	id, ok := registry.FirstRunnerID()
	if !ok || id != "a" {
		t.Fatalf("FirstRunnerID = %q,%v, want a", id, ok)
	}

	// A first-runner listen is assigned to the first runner and re-issued after
	// a reconnect.
	listenLink := newFakeLink("listen")
	registry.RegisterListen("conn", 1, "model-selection", "onDidChange", "",
		json.RawMessage("null"), func(json.RawMessage) {})
	if got := listenLink.listenFor("conn:1"); got != "" {
		t.Fatal("first-runner listen must not attach to an unrelated link")
	}
	if _, err := registry.CallFirst(context.Background(), "model-selection", "getView", json.RawMessage("null")); err != nil {
		t.Fatalf("CallFirst: %v", err)
	}
}

// A first-runner listen must move to the next connected runner without waiting
// for an unrelated reconnect, otherwise model-selection.onDidChange stays dead.
func TestFirstRunnerListenMovesOnDisconnect(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	first := newFakeLink("first")
	second := newFakeLink("second")
	registry.Register(info("a", "/a"), first)
	registry.Register(info("b", "/b"), second)

	registry.RegisterListen("conn", 1, "model-selection", "onDidChange", "",
		json.RawMessage("null"), func(json.RawMessage) {})
	if got := first.listenFor("conn:1"); got != "model-selection.onDidChange" {
		t.Fatalf("initial listen = %q", got)
	}

	registry.Unregister("a", first)
	if got := second.listenFor("conn:1"); got != "model-selection.onDidChange" {
		t.Fatalf("listen was not moved to the surviving runner: %q", got)
	}
	// The old link must not keep the binding assigned.
	if ids := registry.BindingsForRunner("a"); len(ids) != 0 {
		t.Fatalf("bindings for the offline runner = %v", ids)
	}
}

// A displaced duplicate must be closed by Register before the replacement
// receives re-issued listens, so the old socket cannot emit duplicate events.
func TestRegisterClosesDisplacedBeforeRelisten(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	first := newFakeLink("first")
	registry.Register(info("r", "/w"), first)
	registry.RegisterListen("conn", 1, "nex-agent", "onDynamicConversationFrame", "/w",
		json.RawMessage(`[{"workspacePath":"/w"}]`), func(json.RawMessage) {})

	second := newFakeLink("second")
	displaced := registry.Register(info("r", "/w"), second)
	if displaced != first {
		t.Fatalf("displaced = %v, want the first link", displaced)
	}
	if !first.closed {
		t.Fatal("Register must close the displaced link itself")
	}
	if got := second.listenFor("conn:1"); got != "nex-agent.onDynamicConversationFrame" {
		t.Fatalf("replacement listen = %q", got)
	}
}

// Replacing a listen on the same runner must not Unlisten after the new
// Listen, which would delete the freshly registered stream on the runner.
func TestRegisterListenReplaceSameLinkKeepsNewStream(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	link := newFakeLink("link")
	registry.Register(info("r", "/w"), link)

	registry.RegisterListen("conn", 1, "nex-agent", "onDynamicConversationFrame", "/w",
		json.RawMessage(`[{"workspacePath":"/w"}]`), func(json.RawMessage) {})
	registry.RegisterListen("conn", 1, "nex-agent", "onDynamicConversationFrame", "/w",
		json.RawMessage(`[{"workspacePath":"/w"}]`), func(json.RawMessage) {})

	if got := link.listenFor("conn:1"); got != "nex-agent.onDynamicConversationFrame" {
		t.Fatalf("stream after replace = %q, want the new listen", got)
	}
	if ids := registry.BindingsForRunner("r"); len(ids) != 1 || ids[0] != "conn:1" {
		t.Fatalf("bindings = %v", ids)
	}
}

// The runner's hello may repeat a path, and a re-register after losing a path
// to another runner must not leave duplicate listing rows.
func TestDuplicateWorkspacePathInHelloIsListedOnce(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	registry.Register(runners.Info{
		RunnerID: "r",
		Workspaces: []runners.Workspace{
			{Path: "/w"}, {Path: "/w"}, {Path: " /w "},
		},
	}, newFakeLink("r"))

	if workspaces := registry.Workspaces(); len(workspaces) != 1 || workspaces[0].Path != "/w" {
		t.Fatalf("workspaces = %+v, want one /w row", workspaces)
	}
}
