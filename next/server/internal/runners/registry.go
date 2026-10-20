// Package runners tracks connected agent runners, the workspacePath routing
// table and browser listen bindings that survive runner reconnects.
package runners

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// Workspace is one workspace advertised by a runner.
type Workspace struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
}

// Info is the identity a runner reports in its hello frame.
type Info struct {
	RunnerID   string      `json:"runnerId"`
	Name       string      `json:"name"`
	Version    string      `json:"version"`
	OS         string      `json:"os"`
	Arch       string      `json:"arch"`
	Workspaces []Workspace `json:"workspaces"`
}

// Link is one live runner connection. It is implemented by the runner
// WebSocket connection in internal/runnerws.
type Link interface {
	// Call forwards one channel call and waits for the runner result.
	Call(ctx context.Context, channel, method string, arg json.RawMessage) (json.RawMessage, error)
	// Listen starts a dynamic event stream on the runner. id is stable for the
	// lifetime of the browser listen, so a reconnect can re-issue the same id.
	Listen(id, channel, event string, arg json.RawMessage, emit func(json.RawMessage)) error
	// Unlisten stops a previously started stream.
	Unlisten(id string)
	// Close terminates the connection.
	Close()
	// Done is closed when the connection has shut down.
	Done() <-chan struct{}
}

// ErrNoRunnerForWorkspace means no connected runner serves the requested
// workspace path.
var ErrNoRunnerForWorkspace = errors.New("no runner for workspace")

// ErrNoRunnerConnected means no runner is connected at all.
var ErrNoRunnerConnected = errors.New("no runner connected")

// OfflineError reports a call that could not complete because its runner
// disconnected. Its message is the stable "runner offline" used by the UI.
type OfflineError struct{}

// Error implements error.
func (OfflineError) Error() string { return "runner offline" }

// RemoteError is an error reported by the runner itself.
type RemoteError struct {
	Message string
	Name    string
}

// Error implements error.
func (e *RemoteError) Error() string { return e.Message }

// WorkspaceInfo is the server-info projection of one routed workspace.
type WorkspaceInfo struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

type runner struct {
	info  Info
	link  Link
	order int64
}

type binding struct {
	id            string
	owner         string
	ownerListenID uint32
	channel       string
	event         string
	arg           json.RawMessage
	workspacePath string
	// firstRunner marks a channel with no workspace in its argument
	// (model-selection/provider-settings): it follows whichever runner is
	// connected first.
	firstRunner bool
	emit        func(json.RawMessage)
	runner      *runner
}

// listenRequest is one deferred Link.Listen call. The registry never calls
// Link methods while holding mu.
type listenRequest struct {
	id   string
	link Link
	bind *binding
}

// issueListens re-issues the collected listens and cleans up bindings that were
// disposed or replaced while a listen was in flight.
func (r *Registry) issueListens(requests []listenRequest) {
	for _, request := range requests {
		r.issueListen(request)
	}
}

// issueListen sends one listen and resolves overlap with a concurrent
// RegisterListen for the same id. The runner keeps one sink per listen id, so
// whichever Link.Listen lands last wins; after issuing we re-check the current
// binding and re-issue the newer one if it lost the race. The loop is bounded
// because replacements are user actions, not a free-running stream.
func (r *Registry) issueListen(initial listenRequest) {
	current := initial
	for attempt := 0; attempt < 4; attempt++ {
		if !r.bindingActive(current.bind, current.link) {
			return
		}
		if err := current.link.Listen(current.id, current.bind.channel, current.bind.event, current.bind.arg, current.bind.emit); err != nil {
			r.logger.Warn("re-issuing browser listen failed",
				"channel", current.bind.channel, "event", current.bind.event, "error", err)
			r.clearBindingRunner(current.bind, current.link)
			return
		}
		// The binding may have been disposed while the listen was in flight.
		// Without the cleanup the runner keeps an orphan stream that no later
		// unlisten can reach.
		if r.bindingRemoved(current.bind) {
			current.link.Unlisten(current.id)
			return
		}
		r.mu.Lock()
		latest, ok := r.bindings[current.bind.id]
		r.mu.Unlock()
		if !ok || latest == current.bind {
			return
		}
		// A newer binding for the same id exists. If it belongs to this link,
		// re-issue it so its emit function is the installed one; otherwise the
		// new owner already sent its own listen.
		if latest.runner == nil || latest.runner.link != current.link {
			return
		}
		current = listenRequest{id: latest.id, link: current.link, bind: latest}
	}
}

// bindingRemoved reports whether b was disposed: no binding is registered for
// its id. A replacement with the same id is not "removed"; issueListen
// re-issues the newer binding instead.
func (r *Registry) bindingRemoved(b *binding) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.bindings[b.id]
	return !ok
}

// planLocked reassigns unassigned bindings to a live runner and returns the
// listens that must be (re-)issued. It must be called with mu held and the
// returned requests issued after unlocking.
func (r *Registry) planLocked() []listenRequest {
	var requests []listenRequest
	first := r.firstLocked()
	for _, b := range r.bindings {
		if b.runner != nil {
			continue
		}
		if b.firstRunner {
			if first == nil {
				continue
			}
			b.runner = first
			requests = append(requests, listenRequest{id: b.id, link: first.link, bind: b})
			continue
		}
		if owner, ok := r.paths[b.workspacePath]; ok {
			b.runner = owner
			requests = append(requests, listenRequest{id: b.id, link: owner.link, bind: b})
		}
	}
	return requests
}

// Registry owns the connected runner set, the path routing table and the
// browser listen bindings.
//
// Lock ordering: Registry.mu is never held while calling Link methods that can
// block; Listen/Unlisten are non-blocking sends, but they are still invoked
// outside the lock to keep the invariant simple.
type Registry struct {
	mu        sync.Mutex
	logger    *slog.Logger
	runners   map[string]*runner
	paths     map[string]*runner
	bindings  map[string]*binding
	nextOrder int64
	subs      map[int64]func()
	nextSub   int64
}

// NewRegistry returns an empty registry.
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		logger:   logger,
		runners:  make(map[string]*runner),
		paths:    make(map[string]*runner),
		bindings: make(map[string]*binding),
		subs:     make(map[int64]func()),
	}
}

// Register adds a runner. The newest connection for a runner id wins and the
// returned link (if non-nil) has already been closed; it is returned so callers
// can log the displacement. Closing it before re-issuing listens guarantees the
// old socket cannot deliver duplicate events for the same bindings.
// Workspace paths already owned by another runner are rejected and logged; the
// first owner keeps them. Pending browser listens for now-routed paths are
// re-issued.
func (r *Registry) Register(info Info, link Link) (displaced Link) {
	info.RunnerID = strings.TrimSpace(info.RunnerID)
	if info.RunnerID == "" || link == nil {
		return nil
	}

	var requests []listenRequest

	r.mu.Lock()

	if existing, ok := r.runners[info.RunnerID]; ok {
		r.removeLocked(info.RunnerID, existing)
		displaced = existing.link
		r.logger.Warn("runner duplicate id, newest connection wins", "runnerId", info.RunnerID)
	}

	accepted := make([]Workspace, 0, len(info.Workspaces))
	seenPaths := make(map[string]struct{}, len(info.Workspaces))
	for _, workspace := range info.Workspaces {
		path := strings.TrimSpace(workspace.Path)
		if path == "" {
			continue
		}
		// A hello may repeat a path; listing it twice would duplicate the
		// workspace in /api/server-info and recentProjects.
		if _, duplicate := seenPaths[path]; duplicate {
			continue
		}
		seenPaths[path] = struct{}{}
		if owner, taken := r.paths[path]; taken {
			r.logger.Warn("workspace path already served by another runner, rejecting",
				"path", path, "owner", owner.info.RunnerID, "rejected", info.RunnerID)
			continue
		}
		accepted = append(accepted, Workspace{Path: path, Label: strings.TrimSpace(workspace.Label)})
	}

	r.nextOrder++
	entry := &runner{
		info: Info{
			RunnerID:   info.RunnerID,
			Name:       info.Name,
			Version:    info.Version,
			OS:         info.OS,
			Arch:       info.Arch,
			Workspaces: accepted,
		},
		link:  link,
		order: r.nextOrder,
	}
	r.runners[info.RunnerID] = entry
	for _, workspace := range accepted {
		r.paths[workspace.Path] = entry
	}

	requests = r.planLocked()

	r.mu.Unlock()

	// Close the displaced socket before sending listens to the replacement, so
	// the old read loop can no longer fan out events to the same browsers.
	if displaced != nil {
		displaced.Close()
	}

	r.issueListens(requests)
	r.notify()
	return displaced
}

// clearBindingRunner detaches link from b when b is still assigned to it, so a
// later Register retries the listen instead of assuming it is live.
func (r *Registry) clearBindingRunner(b *binding, link Link) {
	r.mu.Lock()
	if b.runner != nil && b.runner.link == link {
		b.runner = nil
	}
	r.mu.Unlock()
}

// bindingActive reports whether b is still registered and still assigned to
// link.
func (r *Registry) bindingActive(b *binding, link Link) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.bindings[b.id]
	return ok && current == b && b.runner != nil && b.runner.link == link
}

// Unregister removes id when link is still the current connection. Bindings
// assigned to it stay registered and are re-issued on the next Register, except
// first-runner bindings, which move to the next connected runner immediately.
func (r *Registry) Unregister(id string, link Link) {
	r.mu.Lock()
	entry, ok := r.runners[id]
	if !ok || entry.link != link {
		r.mu.Unlock()
		return
	}
	r.removeLocked(id, entry)
	// A remaining runner can take over workspace-less streams right away;
	// otherwise model-selection.onDidChange would stay dead until an unrelated
	// reconnect.
	requests := r.planLocked()
	r.mu.Unlock()
	r.logger.Info("runner disconnected", "runnerId", id)
	r.issueListens(requests)
	r.notify()
}

// removeLocked detaches a runner and its paths. Bindings keep their spec but
// lose the runner assignment.
func (r *Registry) removeLocked(id string, entry *runner) {
	delete(r.runners, id)
	for _, workspace := range entry.info.Workspaces {
		if owner, ok := r.paths[workspace.Path]; ok && owner == entry {
			delete(r.paths, workspace.Path)
		}
	}
	for _, b := range r.bindings {
		if b.runner == entry {
			b.runner = nil
		}
	}
}

// Runner returns the live runner for id.
func (r *Registry) Runner(id string) (Link, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.runners[id]
	if !ok {
		return nil, false
	}
	return entry.link, true
}

// RunnerIDForPath returns the runner currently serving workspacePath.
func (r *Registry) RunnerIDForPath(workspacePath string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.paths[strings.TrimSpace(workspacePath)]
	if !ok {
		return "", false
	}
	return entry.info.RunnerID, true
}

// Call forwards a call to the runner that owns workspacePath.
func (r *Registry) Call(ctx context.Context, workspacePath, channel, method string, arg json.RawMessage) (json.RawMessage, error) {
	r.mu.Lock()
	entry, ok := r.paths[strings.TrimSpace(workspacePath)]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNoRunnerForWorkspace, workspacePath)
	}
	return entry.link.Call(ctx, channel, method, arg)
}

// CallFirst forwards a call to the first connected runner (model catalog
// channels have no workspace in their args).
func (r *Registry) CallFirst(ctx context.Context, channel, method string, arg json.RawMessage) (json.RawMessage, error) {
	r.mu.Lock()
	entry := r.firstLocked()
	r.mu.Unlock()
	if entry == nil {
		return nil, ErrNoRunnerConnected
	}
	return entry.link.Call(ctx, channel, method, arg)
}

// FirstRunnerID reports whether a runner is connected and which one is first.
func (r *Registry) FirstRunnerID() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.firstLocked()
	if entry == nil {
		return "", false
	}
	return entry.info.RunnerID, true
}

func (r *Registry) firstLocked() *runner {
	var first *runner
	for _, entry := range r.runners {
		if first == nil || entry.order < first.order {
			first = entry
		}
	}
	return first
}

// RegisterListen stores a browser listen spec and starts it when the
// workspace currently has an owner. The binding outlives runner disconnects.
func (r *Registry) RegisterListen(owner string, ownerListenID uint32, channel, event, workspacePath string, arg json.RawMessage, emit func(json.RawMessage)) {
	id := fmt.Sprintf("%s:%d", owner, ownerListenID)
	first := workspacePath == ""
	b := &binding{
		id:            id,
		owner:         owner,
		ownerListenID: ownerListenID,
		channel:       channel,
		event:         event,
		arg:           arg,
		workspacePath: strings.TrimSpace(workspacePath),
		firstRunner:   first,
		emit:          emit,
	}

	r.mu.Lock()
	var previousLink Link
	if previous, ok := r.bindings[id]; ok && previous.runner != nil {
		previousLink = previous.runner.link
	}
	var link Link
	if first {
		if entry := r.firstLocked(); entry != nil {
			b.runner = entry
			link = entry.link
		}
	} else if entry, ok := r.paths[b.workspacePath]; ok {
		b.runner = entry
		link = entry.link
	}
	r.bindings[id] = b
	r.mu.Unlock()

	// Stop a stream left by a replaced binding on a different runner. When the
	// link is the same, the subsequent Listen overwrites the runner-side sink
	// for this id, so an Unlisten would race with it and could delete the new
	// stream instead.
	if previousLink != nil && previousLink != link {
		previousLink.Unlisten(id)
	}

	if link != nil {
		if err := link.Listen(id, channel, event, arg, emit); err != nil {
			r.logger.Warn("starting browser listen failed", "channel", channel, "event", event, "error", err)
			r.clearBindingRunner(b, link)
			return
		}
		// The browser may have disconnected (UnregisterOwner) or replaced this
		// listen while the runner call was in flight. Without the cleanup the
		// runner would keep an orphan stream that no later unlisten can reach.
		if r.bindingRemoved(b) {
			link.Unlisten(id)
			return
		}
		r.ensureLatest(b, link)
	}
}

// ensureLatest re-issues the listen when a newer binding for the same id was
// installed on this link while ours was in flight.
func (r *Registry) ensureLatest(b *binding, link Link) {
	r.mu.Lock()
	latest, ok := r.bindings[b.id]
	r.mu.Unlock()
	if !ok || latest == b || latest.runner == nil || latest.runner.link != link {
		return
	}
	r.issueListen(listenRequest{id: latest.id, link: link, bind: latest})
}

// UnregisterListen stops and forgets one browser listen.
func (r *Registry) UnregisterListen(owner string, ownerListenID uint32) {
	id := fmt.Sprintf("%s:%d", owner, ownerListenID)
	r.mu.Lock()
	b, ok := r.bindings[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.bindings, id)
	var link Link
	if b.runner != nil {
		link = b.runner.link
	}
	r.mu.Unlock()
	if link != nil {
		link.Unlisten(id)
	}
}

// UnregisterOwner removes every listen owned by a closed browser connection.
func (r *Registry) UnregisterOwner(owner string) {
	type unlisten struct {
		link Link
		id   string
	}
	var calls []unlisten
	r.mu.Lock()
	for id, b := range r.bindings {
		if b.owner != owner {
			continue
		}
		if b.runner != nil {
			calls = append(calls, unlisten{link: b.runner.link, id: id})
		}
		delete(r.bindings, id)
	}
	r.mu.Unlock()
	for _, call := range calls {
		call.link.Unlisten(call.id)
	}
}

// BindingsForRunner returns the stable listen ids currently assigned to id.
func (r *Registry) BindingsForRunner(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.runners[id]
	if !ok {
		return nil
	}
	var ids []string
	for listenID, b := range r.bindings {
		if b.runner == entry {
			ids = append(ids, listenID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Workspaces returns every accepted workspace ordered by runner registration
// order and then path.
func (r *Registry) Workspaces() []WorkspaceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	type ordered struct {
		order     int64
		runner    string
		workspace Workspace
	}
	var all []ordered
	for _, entry := range r.runners {
		for _, workspace := range entry.info.Workspaces {
			all = append(all, ordered{order: entry.order, runner: entry.info.Name, workspace: workspace})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].order != all[j].order {
			return all[i].order < all[j].order
		}
		return all[i].workspace.Path < all[j].workspace.Path
	})
	out := make([]WorkspaceInfo, 0, len(all))
	for _, item := range all {
		out = append(out, WorkspaceInfo{
			Path:  item.workspace.Path,
			Label: labelFor(item.runner, item.workspace),
		})
	}
	return out
}

// labelFor builds the server-info label per specs: "<runnerName>: <basename>".
// The runner-provided label is not used, so every workspace looks consistent
// in the UI's folder list.
func labelFor(runnerName string, workspace Workspace) string {
	base := workspace.Path
	if idx := strings.LastIndexAny(strings.TrimRight(base, "/\\"), "/\\"); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = workspace.Path
	}
	if strings.TrimSpace(runnerName) == "" {
		return base
	}
	return strings.TrimSpace(runnerName) + ": " + base
}

// Paths returns routed workspace paths in the same order as Workspaces.
func (r *Registry) Paths() []string {
	workspaces := r.Workspaces()
	out := make([]string, len(workspaces))
	for i, workspace := range workspaces {
		out[i] = workspace.Path
	}
	return out
}

// Subscribe registers a change callback. The callback runs outside the
// registry lock and must not call back into the registry synchronously.
func (r *Registry) Subscribe(fn func()) (unsubscribe func()) {
	if fn == nil {
		return func() {}
	}
	r.mu.Lock()
	r.nextSub++
	id := r.nextSub
	r.subs[id] = fn
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.subs, id)
		r.mu.Unlock()
	}
}

func (r *Registry) notify() {
	r.mu.Lock()
	callbacks := make([]func(), 0, len(r.subs))
	for _, fn := range r.subs {
		callbacks = append(callbacks, fn)
	}
	r.mu.Unlock()
	for _, fn := range callbacks {
		fn()
	}
}
