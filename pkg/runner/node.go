package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Tags             []string
	HTTPClient       *http.Client

	// Modular capabilities (optional, wired across milestones)
	ProviderOverride ai.Provider // Optional override for deterministic unit/e2e tests
	WorkspaceRPC     WorkspaceRPCHandler
	PTY              PTYHandler
	Extensions       SessionExtensionBuilder
}

type activeSession struct {
	record store.SessionRecord
	loop   *agent.Loop
	cancel context.CancelFunc
}

// Node is an autonomous Runner capable of both Server-connected execution and offline standalone execution.
type Node struct {
	cfg   Config
	store *store.Store
	hc    *http.Client

	wsMu sync.RWMutex
	ws   *websocket.Conn

	mu              sync.Mutex
	activeSessions  map[string]*activeSession
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
		cfg.Version = "0.3.0"
	}
	if cfg.WorkspaceDefault == "" {
		cfg.WorkspaceDefault, _ = os.Getwd()
	}

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

	return &Node{
		cfg:             cfg,
		store:           st,
		hc:              hc,
		activeSessions:  make(map[string]*activeSession),
		pendingApproval: make(map[string]chan protocol.ApprovalResolvePayload),
		pendingUI:       make(map[string]chan protocol.UIResolvePayload),
		pendingRPC:      make(map[string]chan protocol.Envelope),
	}, nil
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
	return n.store.Close()
}

// StartConnectLoop maintains an outbound reverse WebSocket connection to nex-server with auto-reconnect.
func (n *Node) StartConnectLoop(ctx context.Context) {
	if strings.TrimSpace(n.cfg.ServerWSURL) == "" {
		return
	}
	backoff := 200 * time.Millisecond
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
		}
		if err != nil && backoff < 5*time.Second {
			backoff *= 2
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
	helloEnv, _ := protocol.NewEnvelope(protocol.TypeRunnerHello, protocol.RunnerHelloPayload{
		RunnerID:         n.cfg.RunnerID,
		Name:             n.cfg.Name,
		AuthToken:        n.cfg.AuthToken,
		Hostname:         host,
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		Shell:            tools.DefaultShellName(),
		Version:          n.cfg.Version,
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
	if json.Unmarshal(welcomeEnv.Payload, &welcome) == nil && welcome.Snapshot != nil {
		n.ApplyConfigSnapshot(ctx, welcome.Snapshot)
	}

	n.wsMu.Lock()
	n.ws = ws
	n.wsMu.Unlock()
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

	case protocol.TypeSessionPrompt:
		var p protocol.PromptPayload
		if json.Unmarshal(env.Payload, &p) == nil {
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
				ce, _ := protocol.NewEnvelope(protocol.TypePTYClosed, map[string]string{"pty_id": closedID})
				_ = n.sendEnvelope(context.Background(), ce)
			})
			if err != nil {
				errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
				errEnv.ReplyTo = env.ID
				_ = n.sendEnvelope(ctx, errEnv)
				return
			}
			reply.ReplyTo = env.ID
			_ = n.sendEnvelope(ctx, reply)
		}()
	}
}
