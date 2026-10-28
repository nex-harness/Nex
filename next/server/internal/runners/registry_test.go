package runners_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/nex-harness/nex/server/internal/runners"
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

func (f *fakeLink) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func info(id string, paths ...string) runners.Info {
	workspaces := make([]runners.Workspace, len(paths))
	for i, path := range paths {
		workspaces[i] = runners.Workspace{Path: path}
	}
	return runners.Info{RunnerID: id, Name: id, OS: "linux", Workspaces: workspaces}
}

func callTarget(t *testing.T, registry *runners.Registry, target runners.Target) error {
	t.Helper()
	_, err := registry.CallWorkspace(context.Background(), target, "nex-task", "listTasks", json.RawMessage("null"))
	return err
}

func mustCallTarget(t *testing.T, registry *runners.Registry, target runners.Target) {
	t.Helper()
	if err := callTarget(t, registry, target); err != nil {
		t.Fatalf("CallWorkspace(%+v): %v", target, err)
	}
}

func TestRoutingTableAndWorkspaceListing(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	a := newFakeLink("a")
	b := newFakeLink("b")
	registry.Register(info("a", "/w/a", "/w/shared"), a)
	registry.Register(info("b", "/w/shared"), b)

	// Unique paths still route by path; duplicate paths are ambiguous.
	id, ok := registry.RunnerIDForPath("/w/a")
	if !ok || id != "a" {
		t.Fatalf("RunnerIDForPath(/w/a) = %q,%v", id, ok)
	}
	if ids := registry.RunnerIDsForPath("/w/shared"); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("RunnerIDsForPath(/w/shared) = %v", ids)
	}
	if _, ok := registry.RunnerIDForPath("/w/shared"); ok {
		t.Fatal("ambiguous path must not report a single owner")
	}
	ambiguous := callTarget(t, registry, runners.Target{WorkspacePath: "/w/shared"})
	if !errors.Is(ambiguous, runners.ErrAmbiguousWorkspacePath) {
		t.Fatalf("ambiguous call error = %v", ambiguous)
	}

	// Identities route deterministically even when paths collide.
	mustCallTarget(t, registry, runners.Target{
		WorkspacePath:     "/w/shared",
		WorkspaceIdentity: runners.BuildIdentity("b", "/w/shared"),
	})
	if a.callCount() != 0 || b.callCount() != 1 {
		t.Fatalf("calls = a:%d b:%d, want only b", a.callCount(), b.callCount())
	}

	workspaces := registry.Workspaces()
	if len(workspaces) != 3 {
		t.Fatalf("workspaces = %+v, want 3 entries", workspaces)
	}
	if workspaces[0].Path != "/w/a" || workspaces[0].Label != "a: a" {
		t.Fatalf("workspaces[0] = %+v", workspaces[0])
	}
	shared := workspaces[1:]
	if shared[0].Path != "/w/shared" || shared[1].Path != "/w/shared" {
		t.Fatalf("shared rows = %+v", shared)
	}
	if shared[0].WorkspaceIdentity != "runner:a:/w/shared" || shared[1].WorkspaceIdentity != "runner:b:/w/shared" {
		t.Fatalf("shared identities = %q, %q", shared[0].WorkspaceIdentity, shared[1].WorkspaceIdentity)
	}
	if shared[0].RunnerID != "a" || shared[1].RunnerID != "b" {
		t.Fatalf("shared runner ids = %q, %q", shared[0].RunnerID, shared[1].RunnerID)
	}
	if shared[0].RunnerName != "a" || shared[0].RunnerOS != "linux" {
		t.Fatalf("shared runner metadata = %+v", shared[0])
	}

	// recentProjects stays path-only and deduplicated.
	if paths := registry.Paths(); len(paths) != 2 || paths[0] != "/w/a" || paths[1] != "/w/shared" {
		t.Fatalf("Paths() = %v, want [/w/a /w/shared]", paths)
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
	// link for the same identity.
	displaced.Close()
	mustCallTarget(t, registry, runners.Target{
		WorkspacePath:     "/w",
		WorkspaceIdentity: "runner:r:/w",
	})
	if second.callCount() != 1 || first.callCount() != 0 {
		t.Fatalf("calls = second:%d first:%d, want the new link only", second.callCount(), first.callCount())
	}
}

func TestDuplicateRunnerNewestWinsForIdentity(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	first := newFakeLink("first")
	registry.Register(info("r", "/w"), first)
	second := newFakeLink("second")
	registry.Register(info("r", "/w"), second)

	id, ok := registry.RunnerIDForIdentity("runner:r:/w")
	if !ok || id != "r" {
		t.Fatalf("RunnerIDForIdentity = %q,%v", id, ok)
	}
	mustCallTarget(t, registry, runners.Target{WorkspaceIdentity: "runner:r:/w"})
	if second.callCount() != 1 || first.callCount() != 0 {
		t.Fatalf("calls = second:%d first:%d", second.callCount(), first.callCount())
	}
}

func TestOfflineRunnerIdentityNeverFallsBack(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	alpha := newFakeLink("alpha")
	beta := newFakeLink("beta")
	registry.Register(info("alpha", "/w"), alpha)
	registry.Register(info("beta", "/w"), beta)

	registry.Unregister("beta", beta)

	// The identity of the offline runner must fail, not fall back to alpha or
	// to the equally served path.
	err := callTarget(t, registry, runners.Target{WorkspacePath: "/w", WorkspaceIdentity: "runner:beta:/w"})
	var offline runners.OfflineError
	if !errors.As(err, &offline) {
		t.Fatalf("offline identity error = %v, want runner offline", err)
	}
	if err.Error() != "runner offline" {
		t.Fatalf("offline message = %q", err.Error())
	}
	if alpha.callCount() != 0 {
		t.Fatal("offline identity fell back to another runner")
	}

	// An identity for a runner id that is not connected reports offline: the
	// caller cannot fall back to a different machine.
	if err := callTarget(t, registry, runners.Target{WorkspaceIdentity: "runner:gamma:/w"}); !errors.As(err, &offline) {
		t.Fatalf("unknown runner identity error = %v, want runner offline", err)
	}
	// A live runner id is authoritative even when the hello did not advertise
	// that exact path (the runner owns its filesystem).
	registry.Register(info("gamma", "/other"), newFakeLink("gamma"))
	mustCallTarget(t, registry, runners.Target{WorkspaceIdentity: "runner:gamma:/w"})
}

func TestOfflineAndReconnectRelisten(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	link := newFakeLink("first")
	registry.Register(info("r", "/w"), link)

	if err := registry.RegisterListen("conn", 7, runners.Target{WorkspacePath: "/w"},
		"nex-agent", "onDynamicConversationFrame",
		json.RawMessage(`[{"workspacePath":"/w"}]`), func(json.RawMessage) {}); err != nil {
		t.Fatalf("RegisterListen: %v", err)
	}
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

// Identity listens must rebind to the same runner id after a reconnect, and a
// different runner serving the same path must never receive them.
func TestIdentityListenRebindsToSameRunnerOnly(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	alpha := newFakeLink("alpha")
	beta := newFakeLink("beta")
	registry.Register(info("alpha", "/w"), alpha)
	registry.Register(info("beta", "/w"), beta)

	if err := registry.RegisterListen("conn", 3, runners.Target{
		WorkspacePath:     "/w",
		WorkspaceIdentity: "runner:beta:/w",
	}, "nex-agent", "onDynamicConversationFrame",
		json.RawMessage(`[{"workspacePath":"/w","workspaceIdentity":"runner:beta:/w"}]`),
		func(json.RawMessage) {}); err != nil {
		t.Fatalf("RegisterListen: %v", err)
	}
	if got := beta.listenFor("conn:3"); got != "nex-agent.onDynamicConversationFrame" {
		t.Fatalf("identity listen = %q, want beta", got)
	}
	if got := alpha.listenFor("conn:3"); got != "" {
		t.Fatalf("alpha received an identity listen for beta: %q", got)
	}

	registry.Unregister("beta", beta)
	// alpha is still connected and serves the same path, but must not inherit
	// the identity-bound stream.
	if got := alpha.listenFor("conn:3"); got != "" {
		t.Fatalf("alpha inherited beta's listen on disconnect: %q", got)
	}

	beta2 := newFakeLink("beta2")
	registry.Register(info("beta", "/w"), beta2)
	if got := beta2.listenFor("conn:3"); got != "nex-agent.onDynamicConversationFrame" {
		t.Fatalf("identity listen after reconnect = %q, want beta's replacement", got)
	}
	if got := alpha.listenFor("conn:3"); got != "" {
		t.Fatalf("alpha received beta's re-issued listen: %q", got)
	}

	// An identity listen for an offline runner is stored, and only attaches
	// when that exact runner reconnects.
	registry.Unregister("alpha", alpha)
	if err := registry.RegisterListen("conn", 4, runners.Target{WorkspaceIdentity: "runner:alpha:/w"},
		"nex-task", "onDynamicWorkspaceEvent", json.RawMessage("null"), func(json.RawMessage) {}); err != nil {
		t.Fatalf("RegisterListen while offline: %v", err)
	}
	if got := beta2.listenFor("conn:4"); got != "" {
		t.Fatalf("offline identity listen attached to beta: %q", got)
	}
	alpha2 := newFakeLink("alpha2")
	registry.Register(info("alpha", "/w"), alpha2)
	if got := alpha2.listenFor("conn:4"); got != "nex-task.onDynamicWorkspaceEvent" {
		t.Fatalf("offline identity listen did not attach on reconnect: %q", got)
	}
}

func TestAmbiguousPathListenRejected(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	registry.Register(info("a", "/w"), newFakeLink("a"))
	registry.Register(info("b", "/w"), newFakeLink("b"))

	err := registry.RegisterListen("conn", 1, runners.Target{WorkspacePath: "/w"},
		"nex-agent", "onDynamicConversationFrame", json.RawMessage("null"), func(json.RawMessage) {})
	if !errors.Is(err, runners.ErrAmbiguousWorkspacePath) {
		t.Fatalf("ambiguous listen error = %v", err)
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
	registry.RegisterListen("conn", 1, runners.Target{}, "model-selection", "onDidChange",
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

	registry.RegisterListen("conn", 1, runners.Target{}, "model-selection", "onDidChange",
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
	if err := registry.RegisterListen("conn", 1, runners.Target{WorkspacePath: "/w"},
		"nex-agent", "onDynamicConversationFrame",
		json.RawMessage(`[{"workspacePath":"/w"}]`), func(json.RawMessage) {}); err != nil {
		t.Fatalf("RegisterListen: %v", err)
	}

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

	for range 2 {
		if err := registry.RegisterListen("conn", 1, runners.Target{WorkspacePath: "/w"},
			"nex-agent", "onDynamicConversationFrame",
			json.RawMessage(`[{"workspacePath":"/w"}]`), func(json.RawMessage) {}); err != nil {
			t.Fatalf("RegisterListen: %v", err)
		}
	}

	if got := link.listenFor("conn:1"); got != "nex-agent.onDynamicConversationFrame" {
		t.Fatalf("stream after replace = %q, want the new listen", got)
	}
	if ids := registry.BindingsForRunner("r"); len(ids) != 1 || ids[0] != "conn:1" {
		t.Fatalf("bindings = %v", ids)
	}
}

// The runner's hello may repeat a path; it is listed once. Two runners may
// advertise the same path and each gets its own row and identity.
func TestDuplicateWorkspacePathInHelloIsListedOnce(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	registry.Register(runners.Info{
		RunnerID: "r",
		Workspaces: []runners.Workspace{
			{Path: "/w"}, {Path: "/w"}, {Path: " /w "},
		},
	}, newFakeLink("r"))
	registry.Register(info("other", "/w"), newFakeLink("other"))

	workspaces := registry.Workspaces()
	if len(workspaces) != 2 {
		t.Fatalf("workspaces = %+v, want one row per runner", workspaces)
	}
	if workspaces[0].WorkspaceIdentity != "runner:r:/w" || workspaces[1].WorkspaceIdentity != "runner:other:/w" {
		t.Fatalf("identities = %q, %q", workspaces[0].WorkspaceIdentity, workspaces[1].WorkspaceIdentity)
	}
}

func TestInvalidRunnerIDRejected(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	for _, id := range []string{"", "a:b", "a b"} {
		if displaced := registry.Register(info(id, "/w"), newFakeLink(id)); displaced != nil {
			t.Fatalf("Register(%q) displaced a link", id)
		}
	}
	if workspaces := registry.Workspaces(); len(workspaces) != 0 {
		t.Fatalf("invalid runner ids were registered: %+v", workspaces)
	}
}

// A `workspaces` frame replaces the runner's catalogue
// (docs/next/runner-folders.md decision 3): the old path stops resolving, the
// new one resolves by path and identity, and /api/server-info changes.
func TestUpdateWorkspacesReplacesCatalog(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	link := newFakeLink("r")
	registry.Register(info("r", "/old"), link)

	var mu sync.Mutex
	notifications := 0
	registry.Subscribe(func() {
		mu.Lock()
		notifications++
		mu.Unlock()
	})

	if applied := registry.UpdateWorkspaces("r", link, []runners.Workspace{{Path: "/new", Label: "ignored"}}); !applied {
		t.Fatal("UpdateWorkspaces from the current link was ignored")
	}

	if _, ok := registry.RunnerIDForPath("/old"); ok {
		t.Fatal("old path still resolves after the catalogue was replaced")
	}
	if id, ok := registry.RunnerIDForPath("/new"); !ok || id != "r" {
		t.Fatalf("RunnerIDForPath(/new) = %q,%v", id, ok)
	}
	if id, ok := registry.RunnerIDForIdentity("runner:r:/new"); !ok || id != "r" {
		t.Fatalf("RunnerIDForIdentity(runner:r:/new) = %q,%v", id, ok)
	}
	if _, ok := registry.RunnerIDForIdentity("runner:r:/old"); ok {
		t.Fatal("old identity still catalogued")
	}

	workspaces := registry.Workspaces()
	if len(workspaces) != 1 || workspaces[0].Path != "/new" || workspaces[0].WorkspaceIdentity != "runner:r:/new" {
		t.Fatalf("workspaces = %+v", workspaces)
	}
	// labelFor keeps the "<runnerName>: <basename>" format (decision 6).
	if workspaces[0].Label != "r: new" {
		t.Fatalf("label = %q", workspaces[0].Label)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifications != 1 {
		t.Fatalf("notifications = %d, want 1", notifications)
	}
}

// A frame from a link that is no longer current must be ignored: after a
// reconnect the old socket can still deliver one, and it must never overwrite
// the live catalogue.
func TestUpdateWorkspacesIgnoresStaleLink(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	oldLink := newFakeLink("old")
	registry.Register(info("r", "/old"), oldLink)
	newLink := newFakeLink("new")
	registry.Register(info("r", "/new"), newLink)

	if applied := registry.UpdateWorkspaces("r", oldLink, []runners.Workspace{{Path: "/stale"}}); applied {
		t.Fatal("a frame from the displaced link was applied")
	}
	if _, ok := registry.RunnerIDForPath("/stale"); ok {
		t.Fatal("stale frame overwrote the catalogue")
	}
	if id, ok := registry.RunnerIDForPath("/new"); !ok || id != "r" {
		t.Fatalf("live catalogue changed: %q,%v", id, ok)
	}

	// An unknown runner id is ignored as well.
	if applied := registry.UpdateWorkspaces("ghost", newFakeLink("ghost"), []runners.Workspace{{Path: "/x"}}); applied {
		t.Fatal("a frame for an unknown runner was applied")
	}
}

// A pending listen bound to a newly added path must start as soon as the
// workspaces frame lands, without a reconnect.
func TestUpdateWorkspacesReplansPendingListen(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	link := newFakeLink("r")
	registry.Register(info("r", "/old"), link)

	// Path-routed listen for a folder the runner does not serve yet: the spec is
	// stored unassigned (a unique path rule with zero owners), so it only starts
	// when the catalogue gains that path. Identity listens attach to the runner
	// id immediately and would not exercise the re-plan.
	if err := registry.RegisterListen("conn", 9, runners.Target{WorkspacePath: "/new"},
		"nex-agent", "onDynamicConversationFrame", json.RawMessage("null"), func(json.RawMessage) {}); err != nil {
		t.Fatalf("RegisterListen: %v", err)
	}
	if got := link.listenFor("conn:9"); got != "" {
		t.Fatalf("listen started before the path was served: %q", got)
	}

	registry.UpdateWorkspaces("r", link, []runners.Workspace{{Path: "/new"}})
	if got := link.listenFor("conn:9"); got != "nex-agent.onDynamicConversationFrame" {
		t.Fatalf("listen after the frame = %q, want it started", got)
	}

	// Removing the folder keeps the binding spec (a stale tab survives) but the
	// path no longer routes.
	registry.UpdateWorkspaces("r", link, []runners.Workspace{{Path: "/other"}})
	if _, ok := registry.RunnerIDForPath("/new"); ok {
		t.Fatal("removed path still routes")
	}
	if ids := registry.BindingsForRunner("r"); len(ids) != 1 {
		t.Fatalf("binding spec dropped on removal: %v", ids)
	}
}

// The machine browse scope is a routing identity, never a catalogued workspace.
func TestMachineScopeIdentityRoutesButIsNotListed(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	link := newFakeLink("r")
	registry.Register(info("r", "/w"), link)

	if err := callTarget(t, registry, runners.Target{
		WorkspacePath:     runners.MachineScopePath,
		WorkspaceIdentity: "runner:r:~",
	}); err != nil {
		t.Fatalf("machine scope call: %v", err)
	}
	if link.callCount() != 1 {
		t.Fatalf("calls = %d, want the machine scope call to reach the runner", link.callCount())
	}

	// A runner that (incorrectly) reports `~` must not put it in server-info.
	registry.UpdateWorkspaces("r", link, []runners.Workspace{{Path: "~"}, {Path: "/w"}})
	for _, workspace := range registry.Workspaces() {
		if workspace.Path == runners.MachineScopePath {
			t.Fatalf("machine scope listed as a workspace: %+v", workspace)
		}
	}
	for _, path := range registry.Paths() {
		if path == runners.MachineScopePath {
			t.Fatalf("machine scope listed in Paths(): %v", registry.Paths())
		}
	}
}
