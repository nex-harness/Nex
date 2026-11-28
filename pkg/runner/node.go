package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/browser"
	"github.com/nex-harness/nex/pkg/harness/codemode"
	"github.com/nex-harness/nex/pkg/harness/scheduler"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/skills"
	"github.com/nex-harness/nex/pkg/store"
)

// WorkspaceRPCHandler handles workspace.fs_* and workspace.git_* RPCs (wired in M3).
type WorkspaceRPCHandler interface {
	HandleRequest(ctx context.Context, defaultWorkDir string, rawPayload json.RawMessage) (json.RawMessage, error)
}

// PTYHandler handles remote interactive pseudo-terminal lifecycle & binary I/O (wired in M3).
type PTYHandler interface {
	HandleControl(ctx context.Context, env protocol.Envelope, sendBinary func(frame []byte), sendClosed func(ptyID string)) (protocol.Envelope, error)
	WriteStdin(ptyID string, data []byte) error
	CloseAll()
}

// BrowserHandler handles headless Chromium CDP automation, streaming, and provisioning.
type BrowserHandler interface {
	Enabled() bool
	UpdateConfig(cfg browser.Config)
	SessionTool(sessionID, workDir string) tools.Tool
	HandleControl(ctx context.Context, req browser.ControlRequest) (browser.SessionStatePayload, error)
	HandleInput(ctx context.Context, ev browser.InputEvent) error
	HandleHandoffResolution(sessionID string, resumed bool) bool
	Probe(ctx context.Context) browser.ProbeResult
	Install(ctx context.Context, mirrorOverride string) (browser.ProbeResult, error)
	ClearData(mode string) error
	Close()
}

// SessionExtensionBuilder builds extra session tools, system sections, and hooks (wired in M4 for Plugins, CodeMode MCP, Skills, Subagents).
type SessionExtensionBuilder interface {
	PrepareSession(ctx context.Context, sess store.SessionRecord, node *Node, hasUI bool) (extraTools []tools.Tool, extraSections []ai.SystemSection, hooks agent.Hooks, cleanup func(), err error)
	OnSnapshotApplied(ctx context.Context, snap *protocol.ConfigSnapshot)
}

// Config configures a headless autonomous Runner node.
type Config struct {
	RunnerID         string
	Name             string
	AuthToken        string
	ServerWSURL      string // e.g. "ws://127.0.0.1:8080/ws/runner" (empty = standalone offline mode)
	DataDir          string // e.g. "~/.nex-runner"
	WorkspaceDefault string
	Version          string
	BuildSHA         string
	ProtocolVersion  int
	Capabilities     []string
	Elevated         bool
	AutoUpdate       *bool
	Tags             []string
	HTTPClient       *http.Client

	// Modular capabilities (optional, wired across milestones)
	ProviderOverride ai.Provider // Optional override for deterministic unit/e2e tests
	WorkspaceRPC     WorkspaceRPCHandler
	PTY              PTYHandler
	Browser          BrowserHandler
	Extensions       SessionExtensionBuilder
	OnBeforeRestart  func()
}

type activeWorkCounter interface {
	ActiveCount() int
}

type backgroundSubagentController interface {
	ListBackgroundSubagents(parentSessionID string) []protocol.BackgroundTaskInfo
	CancelBackgroundSubagent(taskID, initiator string) (parentSessionID string, ok bool)
	IsBackgroundSession(sessionID string) bool
	CancelAllBackgroundSubagents()
}

type activeSession struct {
	record store.SessionRecord
	loop   *agent.Loop
	cancel context.CancelFunc
}

// Node is an autonomous Runner capable of both Server-connected execution and offline standalone execution.
type Node struct {
	cfg          Config
	store        *store.Store
	hc           *http.Client
	streamHC     *http.Client
	binarySHA256 string
	isDev        bool
	updater      *Updater

	wsMu sync.RWMutex
	ws   *websocket.Conn

	mu              sync.Mutex
	activeSessions  map[string]*activeSession
	sessionBgTasks  map[string]*tools.BackgroundTaskManager
	activeTurns     int
	activePTYs      map[string]bool
	draining        bool
	pendingApproval map[string]chan protocol.ApprovalResolvePayload
	pendingUI       map[string]chan protocol.UIResolvePayload
	pendingRPC      map[string]chan protocol.Envelope
	memoryCfg       protocol.MemoryConfigSync
	goalCfg         protocol.GoalConfigSync
}

// NewNode initializes an autonomous Runner and its local SQLite + CAS store.
func NewNode(cfg Config) (*Node, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("runner: DataDir is required")
	}
	if cfg.RunnerID == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "node"
		}
		cfg.RunnerID = fmt.Sprintf("run_%s_%s", strings.ToLower(host), runtime.GOOS)
	}
	if cfg.Name == "" {
		cfg.Name = cfg.RunnerID
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.ProtocolVersion <= 0 {
		cfg.ProtocolVersion = protocol.ProtocolVersion
	}
	if len(cfg.Capabilities) == 0 {
		cfg.Capabilities = append([]string(nil), protocol.DefaultRunnerCapabilities...)
	}
	if cfg.WorkspaceDefault == "" {
		cfg.WorkspaceDefault, _ = os.Getwd()
	}
	binHash := computeExecutableSHA256()
	isDev := protocol.IsDevVersion(cfg.Version)

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	var streamHC *http.Client
	if cfg.HTTPClient != nil {
		streamHC = &http.Client{
			Transport:     cfg.HTTPClient.Transport,
			CheckRedirect: cfg.HTTPClient.CheckRedirect,
			Jar:           cfg.HTTPClient.Jar,
			Timeout:       0,
		}
	} else if baseTr, ok := http.DefaultTransport.(*http.Transport); ok && baseTr != nil {
		tr := baseTr.Clone()
		tr.ResponseHeaderTimeout = 60 * time.Second
		streamHC = &http.Client{Transport: tr, Timeout: 0}
	} else {
		streamHC = &http.Client{Timeout: 0}
	}

	n := &Node{
		cfg:             cfg,
		store:           st,
		hc:              hc,
		streamHC:        streamHC,
		binarySHA256:    binHash,
		isDev:           isDev,
		activeSessions:  make(map[string]*activeSession),
		sessionBgTasks:  make(map[string]*tools.BackgroundTaskManager),
		activePTYs:      make(map[string]bool),
		pendingApproval: make(map[string]chan protocol.ApprovalResolvePayload),
		pendingUI:       make(map[string]chan protocol.UIResolvePayload),
		pendingRPC:      make(map[string]chan protocol.Envelope),
	}
	n.updater = newUpdater(n)
	n.reconcileOrphanedLocalSessions(context.Background())
	return n, nil
}

// Store exposes the Runner's local SQLite + CAS store.
func (n *Node) Store() *store.Store { return n.store }

// RunnerID returns the node identifier.
func (n *Node) RunnerID() string { return n.cfg.RunnerID }

// DataDir returns the local state directory (~/.nex-runner).
func (n *Node) DataDir() string { return n.cfg.DataDir }

// IsOnline reports whether the Runner currently holds an active WebSocket connection to nex-server.
func (n *Node) IsOnline() bool {
	n.wsMu.RLock()
	defer n.wsMu.RUnlock()
	return n.ws != nil
}

// IsDev reports whether this runner binary is flagged as a dev build.
func (n *Node) IsDev() bool { return n.isDev }

// Version reports the configured runner version string.
func (n *Node) Version() string { return n.cfg.Version }

// Elevated reports whether this runner process has elevated privileges.
func (n *Node) Elevated() bool { return n.cfg.Elevated }

// Updater exposes the self-update coordinator.
func (n *Node) Updater() *Updater { return n.updater }

// StartWatchdog starts pending update startup verification, if applicable.
func (n *Node) StartWatchdog(ctx context.Context) {
	if n.updater != nil {
		n.updater.StartWatchdog(ctx)
	}
}

func (n *Node) autoUpdateEnabled() bool {
	if n.cfg.AutoUpdate != nil {
		return *n.cfg.AutoUpdate
	}
	return true
}

func (n *Node) invokeRestartHook() {
	if n.cfg.OnBeforeRestart != nil {
		n.cfg.OnBeforeRestart()
	}
}

// ActiveSessionIDs returns a snapshot of session IDs currently running inside this Node.
func (n *Node) ActiveSessionIDs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.activeSessions))
	for id := range n.activeSessions {
		out = append(out, id)
	}
	return out
}

func (n *Node) reconcileOrphanedLocalSessions(ctx context.Context) {
	if n.store == nil {
		return
	}
	sessions, err := n.store.ListSessions(ctx, "", true)
	if err != nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"type":   string(agent.EventAgentEnd),
		"reason": "runner_restarted",
	})
	for _, sess := range sessions {
		if sess.Status != "running" {
			continue
		}
		sess.Status = "idle"
		_, _ = n.store.CreateOrUpdateSession(ctx, sess)
		_, _ = n.store.AppendSessionEvent(ctx, store.SessionEvent{
			SessionID: sess.ID,
			TurnIdx:   0,
			EventType: string(agent.EventAgentEnd),
			Payload:   payload,
		}, true)
	}
}

func (n *Node) getOrCreateSessionBgTasks(sessionID string) *tools.BackgroundTaskManager {
	n.mu.Lock()
	if n.sessionBgTasks == nil {
		n.sessionBgTasks = make(map[string]*tools.BackgroundTaskManager)
	}
	if existing := n.sessionBgTasks[sessionID]; existing != nil {
		n.mu.Unlock()
		return existing
	}
	mgr := tools.NewBackgroundTaskManager()
	mgr.SetCallbacks(
		func(_ tools.BackgroundTaskSnapshot) {
			n.emitBackgroundTasksSnapshot(sessionID)
		},
		func(snap tools.BackgroundTaskSnapshot) {
			n.emitBackgroundTasksSnapshot(sessionID)
			if snap.Status == "killed" {
				return
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "[Background command %s %s (exit_code=%d)]\n", snap.TaskID, snap.Status, snap.ExitCode)
			if strings.TrimSpace(snap.Command) != "" {
				fmt.Fprintf(&sb, "Command: %s\n", strings.TrimSpace(snap.Command))
			}
			if strings.TrimSpace(snap.OutputTail) != "" {
				sb.WriteString("Output:\n")
				sb.WriteString(strings.TrimRight(snap.OutputTail, "\n"))
			}
			n.wakeSessionWithBackgroundNotification(sessionID, sb.String())
		},
	)
	n.sessionBgTasks[sessionID] = mgr
	n.mu.Unlock()
	return mgr
}

func (n *Node) listSessionBackgroundTasks(sessionID string) []protocol.BackgroundTaskInfo {
	var tasks []protocol.BackgroundTaskInfo
	n.mu.Lock()
	bgMgr := n.sessionBgTasks[sessionID]
	n.mu.Unlock()

	if bgMgr != nil {
		for _, bt := range bgMgr.ListRunning() {
			title := strings.Join(strings.Fields(bt.Command), " ")
			if runes := []rune(title); len(runes) > 80 {
				title = string(runes[:80]) + "..."
			}
			tasks = append(tasks, protocol.BackgroundTaskInfo{
				TaskID:    bt.TaskID,
				Kind:      "local_bash",
				Title:     title,
				Status:    "running",
				StartedAt: bt.StartedAt,
			})
		}
	}
	if bgCtrl, ok := n.cfg.Extensions.(backgroundSubagentController); ok {
		tasks = append(tasks, bgCtrl.ListBackgroundSubagents(sessionID)...)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].StartedAt < tasks[j].StartedAt
	})
	if tasks == nil {
		tasks = []protocol.BackgroundTaskInfo{}
	}
	return tasks
}

func (n *Node) emitBackgroundTasksSnapshot(sessionID string) {
	if strings.TrimSpace(sessionID) == "" || n.store == nil {
		return
	}
	tasks := n.listSessionBackgroundTasks(sessionID)
	payload, err := json.Marshal(map[string]any{
		"type":  string(agent.EventBackgroundTasksUpdated),
		"tasks": tasks,
	})
	if err != nil {
		return
	}
	n.recordAndEmitSessionEvent(context.Background(), sessionID, 0, string(agent.EventBackgroundTasksUpdated), payload, nil)
}

func (n *Node) wakeSessionWithBackgroundNotification(parentSessionID, noteText string) {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(noteText) == "" {
		return
	}
	n.mu.Lock()
	as := n.activeSessions[parentSessionID]
	n.mu.Unlock()
	if as != nil {
		as.loop.FollowUp(ai.NewUserTextMessage(noteText))
		return
	}
	payload := protocol.PromptPayload{
		SessionID: parentSessionID,
		Text:      noteText,
	}
	if rec, err := n.store.GetSession(context.Background(), parentSessionID); err == nil && rec != nil {
		payload.WorkspacePath = rec.WorkspacePath
		payload.AgentProfileSlug = rec.AgentProfileSlug
		payload.ProviderID = rec.ProviderID
		payload.ModelID = rec.ModelID
		payload.ThinkingEffort = ai.ThinkingEffort(rec.ThinkingEffort)
		payload.PermissionMode = rec.PermissionMode
	}
	go func() {
		_, _ = n.ExecutePrompt(context.Background(), payload, "chat", n.IsOnline())
	}()
}

// StopBackgroundTask terminates a single detached background command or background subagent.
func (n *Node) StopBackgroundTask(ctx context.Context, sessionID, taskID, initiator string) (bool, error) {
	cleanID := strings.TrimSpace(taskID)
	if cleanID == "" {
		return false, errors.New("task_id is required")
	}
	n.mu.Lock()
	bgMgr := n.sessionBgTasks[sessionID]
	n.mu.Unlock()
	if bgMgr != nil {
		if err := bgMgr.StopWithInitiator(cleanID, initiator); err == nil {
			n.emitBackgroundTasksSnapshot(sessionID)
			return true, nil
		}
	}
	if bgCtrl, ok := n.cfg.Extensions.(backgroundSubagentController); ok {
		if parentID, found := bgCtrl.CancelBackgroundSubagent(cleanID, initiator); found {
			targetSess := parentID
			if targetSess == "" {
				targetSess = sessionID
			}
			n.emitBackgroundTasksSnapshot(targetSess)
			return true, nil
		}
	}
	return false, fmt.Errorf("background task %q not found", cleanID)
}

// AbortSession cancels an active foreground session (plus synchronous child sessions)
// without killing detached background bash commands or detached background subagents,
// or reconciles an orphaned running session by emitting an interrupted agent_end event.
func (n *Node) AbortSession(ctx context.Context, sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	bgCtrl, hasBgCtrl := n.cfg.Extensions.(backgroundSubagentController)
	n.mu.Lock()
	as := n.activeSessions[sessionID]
	var cancels []context.CancelFunc
	if as != nil && as.cancel != nil {
		cancels = append(cancels, as.cancel)
	}
	for id, child := range n.activeSessions {
		if id != sessionID && child != nil && child.record.ParentSessionID == sessionID && child.cancel != nil {
			if hasBgCtrl && bgCtrl.IsBackgroundSession(id) {
				continue
			}
			cancels = append(cancels, child.cancel)
		}
	}
	n.mu.Unlock()

	for _, c := range cancels {
		c()
	}
	if as == nil {
		if sess, err := n.store.GetSession(ctx, sessionID); err == nil && sess != nil && sess.Status == "running" {
			sess.Status = "idle"
			_, _ = n.store.CreateOrUpdateSession(ctx, *sess)
		}
		payload, _ := json.Marshal(map[string]any{
			"type":   string(agent.EventAgentEnd),
			"reason": "aborted_orphan",
		})
		n.recordAndEmitSessionEvent(ctx, sessionID, 0, string(agent.EventAgentEnd), payload, nil)
	}
}

// IsDraining reports whether the runner is draining workload prior to an update restart.
func (n *Node) IsDraining() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.draining
}

// SetDraining toggles draining mode (new prompts are rejected while draining).
func (n *Node) SetDraining(v bool) {
	n.mu.Lock()
	n.draining = v
	n.mu.Unlock()
}

// WorkloadSummary breaks down currently active work on a runner node.
type WorkloadSummary struct {
	Turns    int
	PTYs     int
	Browsers int
}

// Total returns the combined count of active turns, PTYs, and browser sessions.
func (w WorkloadSummary) Total() int {
	return w.Turns + w.PTYs + w.Browsers
}

// Describe formats non-zero active workload items for draining diagnostics.
func (w WorkloadSummary) Describe(includeAux bool) string {
	var parts []string
	if w.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d active turn(s)", w.Turns))
	}
	if includeAux {
		if w.PTYs > 0 {
			parts = append(parts, fmt.Sprintf("%d terminal(s)", w.PTYs))
		}
		if w.Browsers > 0 {
			parts = append(parts, fmt.Sprintf("%d browser session(s)", w.Browsers))
		}
	}
	return strings.Join(parts, ", ")
}

// ActiveWorkload returns the structured count of active turns, PTYs, and browser sessions.
func (n *Node) ActiveWorkload() WorkloadSummary {
	n.mu.Lock()
	turns := n.activeTurns
	if len(n.activeSessions) > turns {
		turns = len(n.activeSessions)
	}
	ptys := len(n.activePTYs)
	n.mu.Unlock()

	if c, ok := n.cfg.PTY.(activeWorkCounter); ok {
		if v := c.ActiveCount(); v > ptys {
			ptys = v
		}
	}
	browsers := 0
	if c, ok := n.cfg.Browser.(activeWorkCounter); ok {
		browsers = c.ActiveCount()
	}
	return WorkloadSummary{
		Turns:    turns,
		PTYs:     ptys,
		Browsers: browsers,
	}
}

// ActiveWorkCount returns the count of active turns, sessions, PTYs, and browser sessions.
func (n *Node) ActiveWorkCount() int {
	return n.ActiveWorkload().Total()
}

// Close shuts down active sessions, detached background tasks, PTYs, and the local store.
func (n *Node) Close() error {
	n.mu.Lock()
	for _, as := range n.activeSessions {
		as.cancel()
	}
	bgManagers := make([]*tools.BackgroundTaskManager, 0, len(n.sessionBgTasks))
	for _, m := range n.sessionBgTasks {
		if m != nil {
			bgManagers = append(bgManagers, m)
		}
	}
	n.mu.Unlock()
	for _, m := range bgManagers {
		m.StopAll()
	}
	if bgCtrl, ok := n.cfg.Extensions.(backgroundSubagentController); ok {
		bgCtrl.CancelAllBackgroundSubagents()
	}
	if n.cfg.PTY != nil {
		n.cfg.PTY.CloseAll()
	}
	if n.cfg.Browser != nil {
		n.cfg.Browser.Close()
	}
	return n.store.Close()
}

// StartConnectLoop maintains an outbound reverse WebSocket connection to nex-server with auto-reconnect.
func (n *Node) StartConnectLoop(ctx context.Context) {
	if n.updater != nil {
		n.updater.StartWatchdog(ctx)
	}
	if strings.TrimSpace(n.cfg.ServerWSURL) == "" {
		return
	}
	backoff := 200 * time.Millisecond
	var lastErrStr string
	var lastErrLog time.Time
	for {
		if ctx.Err() != nil {
			return
		}
		connectedTS := time.Now()
		err := n.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(connectedTS) > 5*time.Second {
			backoff = 200 * time.Millisecond
			lastErrStr = ""
		}
		if err != nil {
			msg := err.Error()
			if msg != lastErrStr || time.Since(lastErrLog) >= 30*time.Second {
				log.Printf("nex-runner (%s): connection to %s failed: %v (retrying in %s)", n.cfg.RunnerID, n.cfg.ServerWSURL, err, backoff)
				lastErrStr = msg
				lastErrLog = time.Now()
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
		} else {
			lastErrStr = ""
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
	}
}

func (n *Node) connectOnce(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	ws, _, err := websocket.Dial(dialCtx, n.cfg.ServerWSURL, nil)
	cancel()
	if err != nil {
		return err
	}
	ws.SetReadLimit(16 * 1024 * 1024)
	defer ws.Close(websocket.StatusNormalClosure, "runner closing")

	host, _ := os.Hostname()
	rev, _ := n.store.GetConfigRev(ctx)
	upState := n.updater.loadState()
	helloUpdateState := ""
	helloUpdateErr := ""
	if upState.Status == "rolled_back" {
		helloUpdateState = upState.Status
		helloUpdateErr = upState.LastError
	}
	helloEnv, _ := protocol.NewEnvelope(protocol.TypeRunnerHello, protocol.RunnerHelloPayload{
		RunnerID:         n.cfg.RunnerID,
		Name:             n.cfg.Name,
		AuthToken:        n.cfg.AuthToken,
		Hostname:         host,
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		Shell:            tools.DefaultShellName(),
		Version:          n.cfg.Version,
		BuildSHA:         n.cfg.BuildSHA,
		BinarySHA256:     n.binarySHA256,
		ProtocolVersion:  n.cfg.ProtocolVersion,
		Capabilities:     n.cfg.Capabilities,
		IsDev:            n.isDev,
		Elevated:         n.cfg.Elevated,
		FullDiskAccess:   detectFullDiskAccess(),
		AppPath:          resolveRunnerAppPath(),
		UpdateState:      helloUpdateState,
		UpdateError:      helloUpdateErr,
		Tags:                 n.cfg.Tags,
		WorkspaceDefault:     n.cfg.WorkspaceDefault,
		SyncedConfigRev:      rev,
		ActiveSessionsSynced: true,
		ActiveSessionIDs:     n.ActiveSessionIDs(),
	})
	helloBytes, _ := json.Marshal(helloEnv)
	if err := ws.Write(ctx, websocket.MessageText, helloBytes); err != nil {
		return err
	}

	// Read runner.welcome
	_, rawWelcome, err := ws.Read(ctx)
	if err != nil {
		return err
	}
	var welcomeEnv protocol.Envelope
	if err := json.Unmarshal(rawWelcome, &welcomeEnv); err != nil || welcomeEnv.Type != protocol.TypeRunnerWelcome {
		return errors.New("runner: unexpected welcome frame")
	}
	var welcome protocol.RunnerWelcomePayload
	welcomeDecoded := json.Unmarshal(welcomeEnv.Payload, &welcome) == nil
	if welcomeDecoded && welcome.Snapshot != nil {
		n.ApplyConfigSnapshot(ctx, welcome.Snapshot)
	}

	n.wsMu.Lock()
	n.ws = ws
	n.wsMu.Unlock()
	log.Printf("nex-runner (%s): connected to %s (workspace=%s)", n.cfg.RunnerID, n.cfg.ServerWSURL, n.cfg.WorkspaceDefault)
	if welcomeDecoded && n.updater != nil {
		n.updater.HandleWelcome(ctx, welcome)
	}
	defer func() {
		n.wsMu.Lock()
		if n.ws == ws {
			n.ws = nil
		}
		n.wsMu.Unlock()
	}()

	// Flush any offline Outbox events accumulated while disconnected!
	go n.flushOutbox(ctx)

	for {
		msgType, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		if msgType == websocket.MessageBinary {
			if n.cfg.PTY != nil {
				if op, ptyID, rawIn, err := protocol.DecodePTYBinaryFrame(data); err == nil && op == protocol.PTYBinaryStdin {
					_ = n.cfg.PTY.WriteStdin(ptyID, rawIn)
				}
			}
			continue
		}

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}

		if env.ReplyTo != "" {
			n.mu.Lock()
			ch := n.pendingRPC[env.ReplyTo]
			n.mu.Unlock()
			if ch != nil {
				select {
				case ch <- env:
				default:
				}
				continue
			}
		}

		n.handleServerFrame(ctx, env)
	}
}

func (n *Node) sendEnvelope(ctx context.Context, env protocol.Envelope) error {
	n.wsMu.RLock()
	ws := n.ws
	n.wsMu.RUnlock()
	if ws == nil {
		return errors.New("runner: offline")
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	wCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return ws.Write(wCtx, websocket.MessageText, b)
}

func (n *Node) sendBinaryFrame(frame []byte) {
	n.wsMu.RLock()
	ws := n.ws
	n.wsMu.RUnlock()
	if ws == nil {
		return
	}
	wCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ws.Write(wCtx, websocket.MessageBinary, frame)
}

// CallServerRPC sends a request over the reverse WebSocket (e.g. mcp.remote_call) and awaits the reply.
func (n *Node) CallServerRPC(ctx context.Context, env protocol.Envelope, timeout time.Duration) (protocol.Envelope, error) {
	if env.ID == "" {
		env.ID = "rrpc_" + uuid.NewString()
	}
	ch := make(chan protocol.Envelope, 1)
	n.mu.Lock()
	n.pendingRPC[env.ID] = ch
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.pendingRPC, env.ID)
		n.mu.Unlock()
	}()

	if err := n.sendEnvelope(ctx, env); err != nil {
		return protocol.Envelope{}, err
	}
	rCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case resp := <-ch:
		return resp, nil
	case <-rCtx.Done():
		return protocol.Envelope{}, rCtx.Err()
	}
}

// ApplyConfigSnapshot persists Server-pushed providers, models, skills, plugins, MCP, agents, and cron jobs locally.
func (n *Node) ApplyConfigSnapshot(ctx context.Context, snap *protocol.ConfigSnapshot) {
	if snap == nil {
		return
	}
	for _, p := range snap.Providers {
		_ = n.store.UpsertProvider(ctx, p)
	}
	if len(snap.Models) > 0 {
		_ = n.store.UpsertModelsBatch(ctx, snap.Models, true)
	}
	for _, ap := range snap.AgentProfiles {
		_ = n.store.UpsertAgentProfile(ctx, ap)
	}
	// Cache Skills & compiled Plugins to local disk (~/.nex-runner/skills & plugins) for offline execution!
	skillsDir := filepath.Join(n.cfg.DataDir, "skills")
	for _, sk := range snap.Skills {
		_ = n.store.UpsertSkill(ctx, sk)
		dir := filepath.Join(skillsDir, sk.Name)
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(sk.ContentMD), 0o600)
	}
	if snap.SkillVisibilityRules != nil {
		_ = n.store.SaveSkillVisibilityRules(ctx, snap.SkillVisibilityRules)
	}
	pluginsDir := filepath.Join(n.cfg.DataDir, "plugins")
	for _, pl := range snap.Plugins {
		_ = n.store.UpsertPlugin(ctx, pl)
		if pl.CompiledJS != "" {
			dir := filepath.Join(pluginsDir, pl.Name)
			_ = os.MkdirAll(dir, 0o700)
			_ = os.WriteFile(filepath.Join(dir, "bundle.js"), []byte(pl.CompiledJS), 0o600)
		}
	}
	for _, m := range snap.MCPServers {
		_ = n.store.UpsertMCPServer(ctx, m)
	}
	for _, cj := range snap.CronJobs {
		_, _ = n.store.UpsertCronJob(ctx, cj)
	}
	if snap.CredentialsSynced {
		_ = n.store.ReplaceSyncedCredentials(ctx, snap.Credentials)
	} else {
		for _, c := range snap.Credentials {
			_, _ = n.store.UpsertCredential(ctx, c)
		}
	}
	if snap.MemoriesSynced {
		_, _ = n.store.ReplaceAllMemories(ctx, snap.Memories)
	} else {
		for _, mem := range snap.Memories {
			_, _ = n.store.UpsertMemory(ctx, mem)
		}
	}
	if snap.MemorySettings != nil {
		n.mu.Lock()
		n.memoryCfg = *snap.MemorySettings
		n.mu.Unlock()
	}
	if snap.BrowserSettings != nil && n.cfg.Browser != nil {
		cfg := browser.DefaultConfig()
		cfg.Enabled = snap.BrowserSettings.Enabled
		if snap.BrowserSettings.Mode != "" {
			cfg.Mode = browser.Mode(snap.BrowserSettings.Mode)
		}
		cfg.Headless = snap.BrowserSettings.Headless
		if snap.BrowserSettings.ProfileMode != "" {
			cfg.ProfileMode = browser.ProfileMode(snap.BrowserSettings.ProfileMode)
		}
		cfg.AttachURL = snap.BrowserSettings.AttachURL
		cfg.ExecutablePath = snap.BrowserSettings.ExecutablePath
		cfg.DownloadMirror = snap.BrowserSettings.DownloadMirror
		n.cfg.Browser.UpdateConfig(cfg)
	}
	if snap.MultimodalSettings != nil && n.store != nil {
		prefs, err := n.store.GetUserPreferences(ctx)
		if err != nil {
			prefs = store.DefaultUserPreferences()
		}
		ms := snap.MultimodalSettings
		prefs.ImageMode = ms.ImageMode
		prefs.ImageDriver = ms.ImageDriver
		prefs.ImageBaseURL = ms.ImageBaseURL
		prefs.ImageAPIKey = ms.ImageAPIKey
		prefs.ImageProviderID = ms.ImageProviderID
		prefs.ImageModelID = ms.ImageModelID
		prefs.ImageProviders = ms.ImageProviders
		prefs.ActiveImageProviderID = ms.ActiveImageProviderID
		prefs.ActiveImageModelID = ms.ActiveImageModelID
		prefs.ClassifierMode = ms.ClassifierMode
		prefs.ClassifierDriver = ms.ClassifierDriver
		prefs.ClassifierProviderID = ms.ClassifierProviderID
		prefs.ClassifierModelID = ms.ClassifierModelID
		prefs.ClassifierBaseURL = ms.ClassifierBaseURL
		prefs.ClassifierAPIKey = ms.ClassifierAPIKey
		prefs.ClassifierProviders = ms.ClassifierProviders
		prefs.ActiveClassifierProviderID = ms.ActiveClassifierProviderID
		prefs.ActiveClassifierModelID = ms.ActiveClassifierModelID
		prefs.JevAutoApproveEnabled = ms.JevAutoApproveEnabled
		prefs.JevAutoRouterEnabled = ms.JevAutoRouterEnabled
		_ = n.store.SaveUserPreferences(ctx, prefs)
	}
	if snap.GoalSettings != nil {
		n.mu.Lock()
		n.goalCfg = *snap.GoalSettings
		n.mu.Unlock()
		if n.store != nil {
			prefs, err := n.store.GetUserPreferences(ctx)
			if err != nil {
				prefs = store.DefaultUserPreferences()
			}
			prefs.GoalMaxRounds = snap.GoalSettings.MaxRounds
			prefs.GoalMaxTurnsPerRound = snap.GoalSettings.MaxTurnsPerRound
			prefs.GoalMaxConsecutiveNoProgress = snap.GoalSettings.MaxConsecutiveNoProgress
			prefs.GoalVerifierProviderID = snap.GoalSettings.VerifierProviderID
			prefs.GoalVerifierModelID = snap.GoalSettings.VerifierModelID
			_ = n.store.SaveUserPreferences(ctx, prefs)
		}
	}
	if snap.GlobalInstructions != nil {
		prefs, err := n.store.GetUserPreferences(ctx)
		if err != nil {
			prefs = store.DefaultUserPreferences()
		}
		prefs.GlobalInstructions = *snap.GlobalInstructions
		_ = n.store.SaveUserPreferences(ctx, prefs)
		// Mirror to ~/.nex/AGENTS.md so prompts.BuildSystemSections picks it up.
		if path, pathErr := globalInstructionsPath(); pathErr == nil {
			if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr == nil {
				_ = os.WriteFile(path, []byte(*snap.GlobalInstructions), 0o600)
			}
		}
	}
	if n.cfg.Extensions != nil {
		n.cfg.Extensions.OnSnapshotApplied(ctx, snap)
	}
}

// globalInstructionsPath resolves ~/.nex/AGENTS.md, the file read by
// prompts.BuildSystemSections for user-global instructions.
func globalInstructionsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("runner: home directory unavailable")
	}
	return filepath.Join(home, ".nex", "AGENTS.md"), nil
}

func (n *Node) flushOutbox(ctx context.Context) {
	for {
		entries, err := n.store.ListUnackedOutbox(ctx, 100)
		if err != nil || len(entries) == 0 {
			return
		}
		var maxSentID int64
		for _, e := range entries {
			env, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, nil)
			env.SessionID = e.SessionID
			env.RunnerID = n.cfg.RunnerID
			env.Seq = e.Seq
			env.Payload = e.FrameJSON
			if err := n.sendEnvelope(ctx, env); err != nil {
				return
			}
			maxSentID = e.ID
		}
		if maxSentID > 0 {
			_ = n.store.AckOutboxUpTo(ctx, maxSentID)
		}
		if len(entries) < 100 {
			return
		}
	}
}

func (n *Node) handleServerFrame(ctx context.Context, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeConfigSync:
		var snap protocol.ConfigSnapshot
		if json.Unmarshal(env.Payload, &snap) == nil {
			n.ApplyConfigSnapshot(ctx, &snap)
		}

	case protocol.TypeOutboxAck:
		// Outbox batch ack handled on send; individual acks also supported cleanly.

	case protocol.TypeRunnerUpdate:
		var up protocol.RunnerUpdatePayload
		if json.Unmarshal(env.Payload, &up) == nil && n.updater != nil {
			n.updater.HandleUpdateCommand(ctx, up)
		}

	case protocol.TypeRunnerPermission:
		var req protocol.RunnerPermissionPayload
		_ = json.Unmarshal(env.Payload, &req)
		fda, appPath, pErr := handleRunnerPermissionAction(req.Action)
		res := protocol.RunnerPermissionResultPayload{
			RequestID:      req.RequestID,
			FullDiskAccess: fda,
			AppPath:        appPath,
		}
		if pErr != nil {
			res.Error = pErr.Error()
		}
		reply, _ := protocol.NewEnvelope(protocol.TypeRunnerPermissionResult, res)
		reply.ReplyTo = env.ID
		_ = n.sendEnvelope(ctx, reply)

	case protocol.TypeSessionPrompt:
		var p protocol.PromptPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			if n.IsDraining() {
				errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": "runner is draining for update"})
				errEnv.SessionID = p.SessionID
				errEnv.ReplyTo = env.ID
				_ = n.sendEnvelope(ctx, errEnv)
				return
			}
			go func() {
				_, _ = n.ExecutePrompt(context.Background(), p, "chat", true)
			}()
		}

	case protocol.TypeSessionSteer:
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(env.Payload, &body) == nil && body.Text != "" {
			n.mu.Lock()
			as := n.activeSessions[env.SessionID]
			n.mu.Unlock()
			if as != nil {
				as.loop.Steer(ai.NewUserTextMessage(body.Text))
			}
		}

	case protocol.TypeSessionFollowUp:
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(env.Payload, &body) == nil && body.Text != "" {
			n.mu.Lock()
			as := n.activeSessions[env.SessionID]
			n.mu.Unlock()
			if as != nil {
				as.loop.FollowUp(ai.NewUserTextMessage(body.Text))
			}
		}

	case protocol.TypeSessionAbort:
		n.AbortSession(ctx, env.SessionID)

	case protocol.TypeSessionBgTaskStop:
		var req protocol.BgTaskStopPayload
		if json.Unmarshal(env.Payload, &req) == nil {
			sid := strings.TrimSpace(req.SessionID)
			if sid == "" {
				sid = env.SessionID
			}
			_, _ = n.StopBackgroundTask(ctx, sid, req.TaskID, "user")
		}

	case protocol.TypeSessionForkSync:
		go func() {
			var p protocol.SessionForkSyncPayload
			var reply protocol.Envelope
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
			} else if err := n.store.ImportSessionSnapshot(ctx, p.Session, p.Events); err != nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
			} else {
				reply, _ = protocol.NewEnvelope(protocol.TypeSessionForkSync, map[string]any{"ok": true, "session_id": p.Session.ID})
			}
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeSessionRewind:
		go func() {
			var p protocol.SessionRewindPayload
			var reply protocol.Envelope
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
			} else {
				sessID := p.SessionID
				if sessID == "" {
					sessID = env.SessionID
				}
				switch p.Action {
				case "list":
					cps, err := n.store.ListFileCheckpoints(ctx, sessID)
					if err != nil {
						reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
					} else {
						if cps == nil {
							cps = []store.FileCheckpoint{}
						}
						reply, _ = protocol.NewEnvelope(protocol.TypeSessionRewind, map[string]any{"checkpoints": cps})
					}
				default:
					res, err := n.store.RewindSessionFiles(ctx, sessID, p.BeforeSeq)
					if err != nil {
						reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
					} else {
						reply, _ = protocol.NewEnvelope(protocol.TypeSessionRewind, res)
					}
				}
			}
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeApprovalResolve:
		var ar protocol.ApprovalResolvePayload
		if json.Unmarshal(env.Payload, &ar) == nil && ar.ApprovalID != "" {
			n.mu.Lock()
			ch := n.pendingApproval[ar.ApprovalID]
			n.mu.Unlock()
			if ch != nil {
				select {
				case ch <- ar:
				default:
				}
			}
		}

	case protocol.TypeUIResolve:
		var ur protocol.UIResolvePayload
		if json.Unmarshal(env.Payload, &ur) == nil && ur.RequestID != "" {
			n.mu.Lock()
			ch := n.pendingUI[ur.RequestID]
			n.mu.Unlock()
			if ch != nil {
				select {
				case ch <- ur:
				default:
				}
			}
		}

	case protocol.TypeBlobFetch:
		go func() {
			var req protocol.BlobFetchPayload
			_ = json.Unmarshal(env.Payload, &req)
			var respPayload protocol.BlobDataPayload
			respPayload.Hash = req.Hash
			data, mime, err := n.store.GetBlob(ctx, req.Hash)
			if err != nil {
				respPayload.Error = err.Error()
			} else {
				respPayload.Data = data
				respPayload.Mime = mime
			}
			reply, _ := protocol.NewEnvelope(protocol.TypeBlobData, respPayload)
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeSkillsDiscover:
		go func() {
			var req protocol.SkillsDiscoverPayload
			_ = json.Unmarshal(env.Payload, &req)
			ws := strings.TrimSpace(req.WorkspacePath)
			if ws == "" {
				ws = n.cfg.WorkspaceDefault
			}
			regSkills, _ := n.store.ListSkills(ctx)
			rules, _ := n.store.GetSkillVisibilityRules(ctx)
			found := skills.Discover(skills.DiscoverOptions{
				WorkspacePath:  ws,
				DataDir:        n.cfg.DataDir,
				RegistrySkills: regSkills,
				Rules:          rules,
				IncludeContent: true,
			})
			reply, _ := protocol.NewEnvelope(protocol.TypeSkillsDiscover, found)
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeMCPDiscover:
		go func() {
			var req protocol.MCPDiscoverPayload
			_ = json.Unmarshal(env.Payload, &req)
			ws := strings.TrimSpace(req.WorkspacePath)
			if ws == "" {
				ws = n.cfg.WorkspaceDefault
			}
			found := codemode.DiscoverLocalMCPServers(ws, "")
			reply, _ := protocol.NewEnvelope(protocol.TypeMCPDiscover, found)
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeRunnerRestart:
		go func() {
			reply, _ := protocol.NewEnvelope(protocol.TypeRunnerRestart, map[string]any{"ok": true})
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
			// Give the ACK frame a moment to flush, then cleanly exit so the OS service / Task Scheduler restarts the process.
			time.Sleep(350 * time.Millisecond)
			_ = n.Close()
			os.Exit(0)
		}()

	case protocol.TypeCronRunNow:
		go func() {
			var req protocol.CronRunNowPayload
			if err := json.Unmarshal(env.Payload, &req); err != nil {
				reply, _ := protocol.NewEnvelope(protocol.TypeError, map[string]any{"error": "invalid cron.run_now payload"})
				reply.ReplyTo = env.ID
				_ = n.sendEnvelope(ctx, reply)
				return
			}
			// Acknowledge immediately and execute the cron prompt asynchronously
			reply, _ := protocol.NewEnvelope(protocol.TypeCronRunNow, map[string]any{"ok": true, "job_id": req.Job.ID})
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
			go func() {
				p := scheduler.BuildPromptForCron(req.Job)
				_, _ = n.ExecutePrompt(context.Background(), p, "cron", false)
			}()
		}()

	case protocol.TypeMCPProbe:
		go func() {
			var req protocol.MCPProbePayload
			var reply protocol.Envelope
			if err := json.Unmarshal(env.Payload, &req); err != nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
			} else {
				res := codemode.ProbeServer(ctx, req.Server)
				reply, _ = protocol.NewEnvelope(protocol.TypeMCPProbe, res)
			}
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeCredentialLocalSet:
		go func() {
			var req protocol.CredentialLocalSetPayload
			var res protocol.CredentialLocalSetResult
			if err := json.Unmarshal(env.Payload, &req); err != nil {
				res = protocol.CredentialLocalSetResult{OK: false, Error: err.Error()}
			} else {
				hints := map[string]string{}
				var lastErr error
				for k, v := range req.Values {
					h, err := n.store.SetLocalCredentialValue(ctx, req.CredentialID, k, v)
					if err != nil {
						lastErr = err
						break
					}
					hints[k] = h
				}
				if lastErr != nil {
					res = protocol.CredentialLocalSetResult{OK: false, Error: lastErr.Error()}
				} else {
					res = protocol.CredentialLocalSetResult{OK: true, Hints: hints}
				}
			}
			reply, _ := protocol.NewEnvelope(protocol.TypeCredentialLocalSet, res)
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeCredentialProbe:
		go func() {
			var req protocol.CredentialProbePayload
			var res protocol.CredentialProbeResult
			start := time.Now()
			if err := json.Unmarshal(env.Payload, &req); err != nil {
				res = protocol.CredentialProbeResult{OK: false, ExitCode: -1, Error: err.Error()}
			} else if strings.TrimSpace(req.Command) == "" {
				res = protocol.CredentialProbeResult{OK: false, ExitCode: -1, Error: "probe command is required"}
			} else {
				ws := strings.TrimSpace(req.WorkspacePath)
				if ws == "" {
					ws = n.cfg.WorkspaceDefault
				}
				envMap, _ := n.store.ResolveEffectiveEnv(ctx, n.cfg.RunnerID, ws, "chat", []string{req.CredentialID})
				probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				code, out, runErr := tools.RunShellWithEnv(probeCtx, ws, req.Command, envMap)
				res = protocol.CredentialProbeResult{
					OK:        runErr == nil && code == 0,
					ExitCode:  code,
					Output:    out,
					LatencyMs: time.Since(start).Milliseconds(),
				}
				if runErr != nil {
					res.Error = runErr.Error()
				}
			}
			reply, _ := protocol.NewEnvelope(protocol.TypeCredentialProbe, res)
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeWorkspaceReq:
		go func() {
			if n.cfg.WorkspaceRPC == nil {
				errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": "workspace RPC not initialized"})
				errEnv.ReplyTo = env.ID
				_ = n.sendEnvelope(ctx, errEnv)
				return
			}
			raw, err := n.cfg.WorkspaceRPC.HandleRequest(ctx, n.cfg.WorkspaceDefault, env.Payload)
			if err != nil {
				errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
				errEnv.ReplyTo = env.ID
				_ = n.sendEnvelope(ctx, errEnv)
				return
			}
			reply, _ := protocol.NewEnvelope(protocol.TypeWorkspaceResp, nil)
			reply.ReplyTo = env.ID
			reply.Payload = raw
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypePTYOpen, protocol.TypePTYResize, protocol.TypePTYClose, protocol.TypePTYList:
		go func() {
			if n.cfg.PTY == nil {
				errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": "PTY subsystem not initialized"})
				errEnv.ReplyTo = env.ID
				_ = n.sendEnvelope(ctx, errEnv)
				return
			}
			reply, err := n.cfg.PTY.HandleControl(ctx, env, n.sendBinaryFrame, func(closedID string) {
				n.mu.Lock()
				delete(n.activePTYs, closedID)
				n.mu.Unlock()
				ce, _ := protocol.NewEnvelope(protocol.TypePTYClosed, map[string]string{"pty_id": closedID})
				_ = n.sendEnvelope(context.Background(), ce)
			})
			if err != nil {
				errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
				errEnv.ReplyTo = env.ID
				_ = n.sendEnvelope(ctx, errEnv)
				return
			}
			switch env.Type {
			case protocol.TypePTYOpen:
				var p struct {
					PTYID string `json:"pty_id"`
				}
				if json.Unmarshal(reply.Payload, &p) == nil && p.PTYID != "" {
					n.mu.Lock()
					n.activePTYs[p.PTYID] = true
					n.mu.Unlock()
				}
			case protocol.TypePTYClose:
				var p struct {
					PTYID string `json:"pty_id"`
				}
				if json.Unmarshal(env.Payload, &p) == nil && p.PTYID != "" {
					n.mu.Lock()
					delete(n.activePTYs, p.PTYID)
					n.mu.Unlock()
				}
			}
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeBrowserControl:
		go func() {
			var req browser.ControlRequest
			var reply protocol.Envelope
			if n.cfg.Browser == nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": "browser subsystem not initialized"})
			} else if err := json.Unmarshal(env.Payload, &req); err != nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
			} else {
				if req.SessionID == "" {
					req.SessionID = env.SessionID
				}
				st, err := n.cfg.Browser.HandleControl(ctx, req)
				if err != nil {
					reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
				} else {
					reply, _ = protocol.NewEnvelope(protocol.TypeBrowserState, st)
					reply.SessionID = st.SessionID
				}
			}
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeBrowserInput:
		if n.cfg.Browser != nil {
			var ev browser.InputEvent
			if json.Unmarshal(env.Payload, &ev) == nil {
				if ev.SessionID == "" {
					ev.SessionID = env.SessionID
				}
				go func() { _ = n.cfg.Browser.HandleInput(ctx, ev) }()
			}
		}

	case protocol.TypeBrowserHandoff:
		if n.cfg.Browser != nil {
			var p struct {
				SessionID string `json:"session_id"`
				State     string `json:"state"`
				Resumed   *bool  `json:"resumed"`
			}
			if json.Unmarshal(env.Payload, &p) == nil {
				sid := p.SessionID
				if sid == "" {
					sid = env.SessionID
				}
				resumed := p.State == "resumed"
				if p.Resumed != nil {
					resumed = *p.Resumed
				}
				n.cfg.Browser.HandleHandoffResolution(sid, resumed)
			}
		}

	case protocol.TypeBrowserProbe:
		go func() {
			var reply protocol.Envelope
			if n.cfg.Browser == nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": "browser subsystem not initialized"})
			} else {
				res := n.cfg.Browser.Probe(ctx)
				reply, _ = protocol.NewEnvelope(protocol.TypeBrowserProbe, res)
			}
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeBrowserInstall:
		go func() {
			var req struct {
				Mirror string `json:"mirror,omitempty"`
			}
			_ = json.Unmarshal(env.Payload, &req)
			var reply protocol.Envelope
			if n.cfg.Browser == nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": "browser subsystem not initialized"})
			} else {
				res, err := n.cfg.Browser.Install(ctx, req.Mirror)
				if err != nil {
					reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
				} else {
					reply, _ = protocol.NewEnvelope(protocol.TypeBrowserInstall, res)
				}
			}
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()

	case protocol.TypeBrowserClearData:
		go func() {
			var req struct {
				Mode string `json:"mode"`
			}
			_ = json.Unmarshal(env.Payload, &req)
			if req.Mode == "" {
				req.Mode = "cache"
			}
			var reply protocol.Envelope
			if n.cfg.Browser == nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": "browser subsystem not initialized"})
			} else if err := n.cfg.Browser.ClearData(req.Mode); err != nil {
				reply, _ = protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
			} else {
				reply, _ = protocol.NewEnvelope(protocol.TypeBrowserClearData, map[string]any{"ok": true, "mode": req.Mode})
			}
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()
	}
}

func computeExecutableSHA256() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
