// Package runners tracks connected agent runners, the workspace identity and
// path routing tables, and browser listen bindings that survive runner
// reconnects.
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

// Target identifies the runner a forwarded call or listen must reach.
//
// WorkspaceIdentity wins when it parses as `runner:<runnerId>:<path>`;
// otherwise WorkspacePath is the legacy (unique-path) routing key.
type Target struct {
	WorkspacePath     string
	WorkspaceIdentity string
	// RunnerID routes directly to one runner (tool id bindings: the listen of a
	// terminal/watcher id must reach the machine that owns the id, regardless
	// of path identities). Empty means identity/path resolution as before.
	RunnerID string
}

// ErrNoRunnerForWorkspace means no connected runner serves the requested
// workspace path.
var ErrNoRunnerForWorkspace = errors.New("no runner for workspace")

// ErrNoRunnerConnected means no runner is connected at all.
var ErrNoRunnerConnected = errors.New("no runner connected")

// ErrAmbiguousWorkspacePath means several connected runners serve the path and
// the caller did not supply a workspace identity.
var ErrAmbiguousWorkspacePath = errors.New("ambiguous workspace path; workspaceIdentity required")

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
	Path              string `json:"path"`
	Label             string `json:"label"`
	WorkspaceIdentity string `json:"workspaceIdentity"`
	RunnerID          string `json:"runnerId"`
	RunnerName        string `json:"runnerName"`
	RunnerOS          string `json:"runnerOs"`
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
	// workspaceIdentity is set for identity-routed listens. It is the
	// reconnect key: only the runner with this id may ever receive the stream.
	workspaceIdentity string
	// targetRunnerID routes an id-only listen (terminal data/exit, watcher
	// change) to the machine that owns the tool id. The binding dies with that
	// runner (the ids die with it).
	targetRunnerID string
	// firstRunner marks a channel with no workspace in its argument
	// (model-selection/provider-settings onDidChange): it follows whichever
	// runner is connected first.
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
		entry := r.bindingRunnerLocked(b)
		if entry == nil {
			continue
		}
		b.runner = entry
		requests = append(requests, listenRequest{id: b.id, link: entry.link, bind: b})
	}
	return requests
}

// bindingRunnerLocked returns the live runner a non-first-runner binding may
// attach to, or nil when it must wait.
//
// Identity bindings are pinned to the runner id parsed from the identity:
// another runner that happens to serve the same path must never receive the
// stream. Path bindings attach only while exactly one runner serves the path.
func (r *Registry) bindingRunnerLocked(b *binding) *runner {
	if b.targetRunnerID != "" {
		return r.runners[b.targetRunnerID]
	}
	if b.workspaceIdentity != "" {
		runnerID, _, ok := ParseIdentity(b.workspaceIdentity)
		if !ok {
			return nil
		}
		return r.runners[runnerID]
	}
	owners := r.paths[b.workspacePath]
	if len(owners) == 1 {
		return owners[0]
	}
	return nil
}

// Registry owns the connected runner set, the identity/path routing tables and
// the browser listen bindings.
//
// Lock ordering: Registry.mu is never held while calling Link methods that can
// block; Listen/Unlisten are non-blocking sends, but they are still invoked
// outside the lock to keep the invariant simple.
type Registry struct {
	mu      sync.Mutex
	logger  *slog.Logger
	runners map[string]*runner
	// identities maps server-built `runner:<runnerId>:<path>` identities to
	// their runner. This is the primary routing table.
	identities map[string]*runner
	// paths maps a runner-local path to every runner serving it. Duplicate
	// paths across runners are legal; legacy path routing requires uniqueness.
	paths     map[string][]*runner
	bindings  map[string]*binding
	nextOrder int64
	subs      map[int64]func()
	nextSub   int64
	// dropSubs observe runner disconnects (tool id bindings cleanup).
	dropSubs map[int64]func(string)
	nextDrop int64
}

// NewRegistry returns an empty registry.
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		logger:     logger,
		runners:    make(map[string]*runner),
		identities: make(map[string]*runner),
		paths:      make(map[string][]*runner),
		bindings:   make(map[string]*binding),
		subs:       make(map[int64]func()),
	}
}

// normalizeWorkspaces trims and de-duplicates a runner-provided workspace
// list. A runner may repeat a path (in hello or in a later workspaces frame);
// listing it twice would duplicate the workspace in /api/server-info and
// recentProjects. The machine browse scope `~` is a routing pseudo path and is
// never catalogued as a workspace (docs/next/runner-folders.md decision 2).
func normalizeWorkspaces(workspaces []Workspace) []Workspace {
	accepted := make([]Workspace, 0, len(workspaces))
	seenPaths := make(map[string]struct{}, len(workspaces))
	for _, workspace := range workspaces {
		path := strings.TrimSpace(workspace.Path)
		if path == "" || path == MachineScopePath {
			continue
		}
		if _, duplicate := seenPaths[path]; duplicate {
			continue
		}
		seenPaths[path] = struct{}{}
		accepted = append(accepted, Workspace{Path: path, Label: strings.TrimSpace(workspace.Label)})
	}
	return accepted
}

// Register adds a runner. The newest connection for a runner id wins and the
// returned link (if non-nil) has already been closed; it is returned so callers
// can log the displacement. Closing it before re-issuing listens guarantees the
// old socket cannot deliver duplicate events for the same bindings.
//
// Duplicate workspace paths across runners are accepted (each runner gets its
// own identity). A hello may repeat a path; the repeat is ignored. Pending
// browser listens whose identity or unique path now resolves are re-issued.
func (r *Registry) Register(info Info, link Link) (displaced Link) {
	info.RunnerID = strings.TrimSpace(info.RunnerID)
	if link == nil {
		return nil
	}
	if !ValidRunnerID(info.RunnerID) {
		r.logger.Warn("runner hello rejected: invalid runnerId", "runnerId", info.RunnerID)
		return nil
	}

	var requests []listenRequest

	r.mu.Lock()

	if existing, ok := r.runners[info.RunnerID]; ok {
		r.removeLocked(info.RunnerID, existing)
		displaced = existing.link
		r.logger.Warn("runner duplicate id, newest connection wins", "runnerId", info.RunnerID)
	}

	accepted := normalizeWorkspaces(info.Workspaces)

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
	r.addCatalogLocked(entry)

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

// UpdateWorkspaces replaces a runner's catalogued workspaces from a live
// `workspaces` frame (docs/next/runner-folders.md decision 3). The runner owns
// its served folders; the server only mirrors them for routing and
// /api/server-info.
//
// A frame from a stale link is ignored: after a reconnect the old socket may
// still deliver one, and it must never overwrite the current catalogue. The
// identity and path tables are swapped under one lock, then pending listens are
// re-planned so a listen for a newly added path starts immediately. It reports
// whether the frame was applied.
func (r *Registry) UpdateWorkspaces(runnerID string, link Link, workspaces []Workspace) bool {
	runnerID = strings.TrimSpace(runnerID)
	if link == nil || !ValidRunnerID(runnerID) {
		return false
	}
	accepted := normalizeWorkspaces(workspaces)

	r.mu.Lock()
	entry, ok := r.runners[runnerID]
	if !ok || entry.link != link {
		// 过期链路：该 runner 已重连或被替换，这一帧来自旧连接。若应用它，旧
		// 连接就能覆盖新连接的目录表，导致路由到 runner 不再服务的路径。
		r.mu.Unlock()
		return false
	}
	r.removeCatalogLocked(entry)
	entry.info.Workspaces = accepted
	r.addCatalogLocked(entry)
	requests := r.planLocked()
	r.mu.Unlock()

	r.issueListens(requests)
	r.logger.Info("runner workspaces updated", "runnerId", runnerID, "workspaces", len(accepted))
	r.notify()
	return true
}

// addCatalogLocked registers the identity and path rows of entry's workspaces.
// It must be called with mu held.
func (r *Registry) addCatalogLocked(entry *runner) {
	for _, workspace := range entry.info.Workspaces {
		r.identities[BuildIdentity(entry.info.RunnerID, workspace.Path)] = entry
		r.paths[workspace.Path] = append(r.paths[workspace.Path], entry)
	}
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
	dropFns := make([]func(string), 0, len(r.dropSubs))
	for _, fn := range r.dropSubs {
		dropFns = append(dropFns, fn)
	}
	r.mu.Unlock()
	r.logger.Info("runner disconnected", "runnerId", id)
	r.issueListens(requests)
	// Tool id bindings (terminals/watchers) die with the process.
	for _, fn := range dropFns {
		fn(id)
	}
	r.notify()
}

// OnRunnerDisconnected registers a callback invoked with the runner id after
// it disconnects (tool id bindings and other per-runner cleanup).
func (r *Registry) OnRunnerDisconnected(fn func(runnerID string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dropSubs == nil {
		r.dropSubs = map[int64]func(string){}
	}
	r.nextDrop++
	r.dropSubs[r.nextDrop] = fn
}

// removeCatalogLocked detaches entry's identity and path rows. It must be called
// with mu held; the runner entry itself stays registered.
func (r *Registry) removeCatalogLocked(entry *runner) {
	for _, workspace := range entry.info.Workspaces {
		identity := BuildIdentity(entry.info.RunnerID, workspace.Path)
		if owner, ok := r.identities[identity]; ok && owner == entry {
			delete(r.identities, identity)
		}
		owners := r.paths[workspace.Path]
		remaining := owners[:0]
		for _, owner := range owners {
			if owner != entry {
				remaining = append(remaining, owner)
			}
		}
		if len(remaining) == 0 {
			delete(r.paths, workspace.Path)
		} else {
			r.paths[workspace.Path] = remaining
		}
	}
}

// removeLocked detaches a runner and its identities/paths. Bindings keep their
// spec but lose the runner assignment.
func (r *Registry) removeLocked(id string, entry *runner) {
	delete(r.runners, id)
	r.removeCatalogLocked(entry)
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

// RunnerInfo returns the hello identity of a connected runner (the name the
// machine reported, its version and OS). A runner known only through recorded
// facts is offline and has no registry entry, so callers fall back to the id.
func (r *Registry) RunnerInfo(id string) (Info, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.runners[strings.TrimSpace(id)]
	if !ok {
		return Info{}, false
	}
	info := entry.info
	info.Workspaces = append([]Workspace(nil), entry.info.Workspaces...)
	return info, true
}

// RunnerIDForPath returns the runner serving workspacePath when exactly one
// runner does. It reports false for unknown and ambiguous paths.
func (r *Registry) RunnerIDForPath(workspacePath string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	owners := r.paths[strings.TrimSpace(workspacePath)]
	if len(owners) != 1 {
		return "", false
	}
	return owners[0].info.RunnerID, true
}

// RunnerForTarget resolves the runner of a workspace target using the same
// rules as resolveLocked (identity first, then a unique path). It reports the
// runner id for callers that only need routing (tool id bindings), without
// the link.
func (r *Registry) RunnerForTarget(target Target) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, err := r.resolveLocked(target)
	if err != nil || entry == nil {
		return "", false
	}
	return entry.info.RunnerID, true
}

// RunnerIDsForPath returns every runner serving workspacePath in registration
// order.
func (r *Registry) RunnerIDsForPath(workspacePath string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	owners := r.paths[strings.TrimSpace(workspacePath)]
	ids := make([]string, 0, len(owners))
	for _, owner := range owners {
		ids = append(ids, owner.info.RunnerID)
	}
	return ids
}

// RunnerIDForIdentity returns the live runner for a server-built identity.
func (r *Registry) RunnerIDForIdentity(identity string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.identities[strings.TrimSpace(identity)]
	if !ok {
		return "", false
	}
	return entry.info.RunnerID, true
}

// resolveLocked maps a target to a live runner.
//
// A valid runner identity is authoritative: if its runner is not connected the
// call fails with OfflineError and never falls back to another runner. Without
// a valid runner identity the legacy path rule applies and requires a unique
// owner.
func (r *Registry) resolveLocked(target Target) (*runner, error) {
	if id := strings.TrimSpace(target.RunnerID); id != "" {
		// Tool id bindings name the machine directly.
		if entry, live := r.runners[id]; live {
			return entry, nil
		}
		return nil, OfflineError{}
	}
	if identity := strings.TrimSpace(target.WorkspaceIdentity); identity != "" {
		if runnerID, _, ok := ParseIdentity(identity); ok {
			// The identity table is the fast path. A live runner id is still
			// authoritative for a path it no longer advertises: the runner owns
			// its filesystem, so the identity remains valid there (a stale tab
			// survives a hello whose workspace list changed).
			if entry, catalogued := r.identities[identity]; catalogued && r.runners[runnerID] == entry {
				return entry, nil
			}
			if entry, live := r.runners[runnerID]; live {
				return entry, nil
			}
			return nil, OfflineError{}
		}
		// Not a server-built runner identity (e.g. a desktop `remote:` one):
		// fall through to legacy path routing.
	}
	path := strings.TrimSpace(target.WorkspacePath)
	owners := r.paths[path]
	switch len(owners) {
	case 0:
		return nil, fmt.Errorf("%w: %q", ErrNoRunnerForWorkspace, target.WorkspacePath)
	case 1:
		return owners[0], nil
	default:
		return nil, ErrAmbiguousWorkspacePath
	}
}

// CallWorkspace forwards a call to the runner selected by target.
func (r *Registry) CallWorkspace(ctx context.Context, target Target, channel, method string, arg json.RawMessage) (json.RawMessage, error) {
	r.mu.Lock()
	entry, err := r.resolveLocked(target)
	r.mu.Unlock()
	if err != nil {
		return nil, err
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

// RegisterListen stores a browser listen spec and starts it when the target
// currently resolves to a runner. The binding outlives runner disconnects.
//
// An ambiguous legacy path is rejected: the browser cannot be told which runner
// its stream would come from. Identity targets are always stored so they can be
// rebound to the same runner id after a reconnect.
func (r *Registry) RegisterListen(owner string, ownerListenID uint32, target Target, channel, event string, arg json.RawMessage, emit func(json.RawMessage)) error {
	id := fmt.Sprintf("%s:%d", owner, ownerListenID)
	workspacePath := strings.TrimSpace(target.WorkspacePath)
	workspaceIdentity := strings.TrimSpace(target.WorkspaceIdentity)
	// A non-runner identity (desktop `remote:`) is not a server routing key; it
	// is dropped so the binding behaves like a legacy path listen.
	if _, _, ok := ParseIdentity(workspaceIdentity); !ok {
		workspaceIdentity = ""
	}
	targetRunnerID := strings.TrimSpace(target.RunnerID)
	first := workspacePath == "" && workspaceIdentity == "" && targetRunnerID == ""

	b := &binding{
		id:                id,
		owner:             owner,
		ownerListenID:     ownerListenID,
		channel:           channel,
		event:             event,
		arg:               arg,
		workspacePath:     workspacePath,
		workspaceIdentity: workspaceIdentity,
		targetRunnerID:    targetRunnerID,
		firstRunner:       first,
		emit:              emit,
	}

	r.mu.Lock()
	if !first && workspaceIdentity == "" && targetRunnerID == "" {
		// Legacy path listen: reject ambiguity up front instead of silently
		// attaching the stream to an arbitrary machine.
		if len(r.paths[workspacePath]) > 1 {
			r.mu.Unlock()
			return ErrAmbiguousWorkspacePath
		}
	}
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
	} else if entry := r.bindingRunnerLocked(b); entry != nil {
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
			return nil
		}
		// The browser may have disconnected (UnregisterOwner) or replaced this
		// listen while the runner call was in flight. Without the cleanup the
		// runner would keep an orphan stream that no later unlisten can reach.
		if r.bindingRemoved(b) {
			link.Unlisten(id)
			return nil
		}
		r.ensureLatest(b, link)
	}
	return nil
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
// order and then path. Duplicate paths on different runners are separate rows
// with distinct identities.
func (r *Registry) Workspaces() []WorkspaceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	type ordered struct {
		order      int64
		runnerID   string
		runnerName string
		runnerOS   string
		workspace  Workspace
	}
	var all []ordered
	for _, entry := range r.runners {
		for _, workspace := range entry.info.Workspaces {
			// 机器浏览 scope（`~`）只是路由身份，绝不能出现在工作区列表里
			// （docs/next/runner-folders.md decision 2）。
			if workspace.Path == MachineScopePath {
				continue
			}
			all = append(all, ordered{
				order:      entry.order,
				runnerID:   entry.info.RunnerID,
				runnerName: entry.info.Name,
				runnerOS:   entry.info.OS,
				workspace:  workspace,
			})
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
			Path:              item.workspace.Path,
			Label:             labelFor(item.runnerName, item.runnerID, item.workspace),
			WorkspaceIdentity: BuildIdentity(item.runnerID, item.workspace.Path),
			RunnerID:          item.runnerID,
			RunnerName:        item.runnerName,
			RunnerOS:          item.runnerOS,
		})
	}
	return out
}

// labelFor builds the server-info label per spec: "<runnerName>: <basename>".
// The runner-provided label is not used, so every workspace looks consistent
// in the UI's folder list. A runner that reports no name falls back to its id.
func labelFor(runnerName, runnerID string, workspace Workspace) string {
	base := workspace.Path
	if idx := strings.LastIndexAny(strings.TrimRight(base, "/\\"), "/\\"); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = workspace.Path
	}
	name := strings.TrimSpace(runnerName)
	if name == "" {
		name = runnerID
	}
	if name == "" {
		return base
	}
	return name + ": " + base
}

// Paths returns routed workspace paths in the same order as Workspaces,
// deduplicated. recentProjects stays path-only, so a path served by two
// machines appears once there.
func (r *Registry) Paths() []string {
	workspaces := r.Workspaces()
	out := make([]string, 0, len(workspaces))
	seen := make(map[string]struct{}, len(workspaces))
	for _, workspace := range workspaces {
		if _, duplicate := seen[workspace.Path]; duplicate {
			continue
		}
		seen[workspace.Path] = struct{}{}
		out = append(out, workspace.Path)
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
