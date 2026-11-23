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
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/browser"
	"github.com/nex-harness/nex/pkg/harness/codemode"
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
	binarySHA256 string
	isDev        bool
	updater      *Updater

	wsMu sync.RWMutex
	ws   *websocket.Conn

	mu              sync.Mutex
	activeSessions  map[string]*activeSession
	activeTurns     int
	activePTYs      map[string]bool
	draining        bool
	pendingApproval map[string]chan protocol.ApprovalResolvePayload
	pendingUI       map[string]chan protocol.UIResolvePayload
	pendingRPC      map[string]chan protocol.Envelope
	memoryCfg       protocol.MemoryConfigSync
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

	// Ensure embedded model catalog exists locally for offline cost/window resolution
	ctx := context.Background()
	if existing, err := st.ListModels(ctx, ""); err == nil && len(existing) == 0 {
		_ = st.UpsertProvider(ctx, store.ProviderRecord{
			ID: "anthropic", Name: "Anthropic", Driver: ai.DriverAnthropic, BaseURL: "https://api.anthropic.com/v1", Enabled: true,
		})
		_ = st.UpsertProvider(ctx, store.ProviderRecord{
			ID: "openai", Name: "OpenAI Codex", Driver: ai.DriverOpenAIResponses, BaseURL: "https://api.openai.com/v1", Enabled: true,
		})
		if seed, err := ai.EmbeddedModelsCatalog(); err == nil {
			_ = st.UpsertModelsBatch(ctx, seed, false)
		}
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}

	n := &Node{
		cfg:             cfg,
		store:           st,
		hc:              hc,
		binarySHA256:    binHash,
		isDev:           isDev,
		activeSessions:  make(map[string]*activeSession),
		activePTYs:      make(map[string]bool),
		pendingApproval: make(map[string]chan protocol.ApprovalResolvePayload),
		pendingUI:       make(map[string]chan protocol.UIResolvePayload),
		pendingRPC:      make(map[string]chan protocol.Envelope),
	}
	n.updater = newUpdater(n)
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

// ActiveWorkCount returns the count of active turns, sessions, PTYs, and browser sessions.
func (n *Node) ActiveWorkCount() int {
	n.mu.Lock()
	count := n.activeTurns
	if len(n.activeSessions) > count {
		count = len(n.activeSessions)
	}
	ptyCount := len(n.activePTYs)
	n.mu.Unlock()

	if c, ok := n.cfg.PTY.(activeWorkCounter); ok {
		if v := c.ActiveCount(); v > ptyCount {
			ptyCount = v
		}
	}
	count += ptyCount
	if c, ok := n.cfg.Browser.(activeWorkCounter); ok {
		count += c.ActiveCount()
	}
	return count
}

// Close shuts down active sessions, PTYs, and the local store.
func (n *Node) Close() error {
	n.mu.Lock()
	for _, as := range n.activeSessions {
		as.cancel()
	}
	n.mu.Unlock()
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
		UpdateState:      helloUpdateState,
		UpdateError:      helloUpdateErr,
		Tags:             n.cfg.Tags,
		WorkspaceDefault: n.cfg.WorkspaceDefault,
		SyncedConfigRev:  rev,
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
	if n.cfg.Extensions != nil {
		n.cfg.Extensions.OnSnapshotApplied(ctx, snap)
	}
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
		n.mu.Lock()
		as := n.activeSessions[env.SessionID]
		n.mu.Unlock()
		if as != nil {
			as.cancel()
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
