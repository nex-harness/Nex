package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/extension"
	"github.com/nex-harness/nex/pkg/harness/codemode"
	"github.com/nex-harness/nex/pkg/harness/pty"
	"github.com/nex-harness/nex/pkg/harness/scheduler"
	"github.com/nex-harness/nex/pkg/harness/subagent"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/harness/workspace"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

// DefaultRuntimeWiring connects WorkspaceRPC, PTY, CodeMode MCP, TS/Go Extensions, Subagents, and Autonomous Cron onto a Node.
type DefaultRuntimeWiring struct {
	node    *Node
	mcpPool *codemode.Pool
	subMgr  *subagent.Manager
	cronEng *scheduler.Engine
}

// AttachDefaultSubsystems wires all M3, M4, and M5 subsystems onto n.
func AttachDefaultSubsystems(n *Node) *DefaultRuntimeWiring {
	if n.cfg.WorkspaceRPC == nil {
		n.cfg.WorkspaceRPC = workspace.NewService()
	}
	if n.cfg.PTY == nil {
		n.cfg.PTY = pty.NewManager(n.cfg.WorkspaceDefault)
	}

	w := &DefaultRuntimeWiring{node: n}
	w.mcpPool = codemode.NewPool(w)
	w.subMgr = subagent.NewManager(w, subagent.DefaultMaxDepth, subagent.DefaultMaxConcurrent)
	w.cronEng = scheduler.NewEngine(n.store, w)

	// Load initial MCP servers & Cron schedules from local SQLite snapshot (for immediate offline readiness!)
	ctx := context.Background()
	if mcps, err := n.store.ListMCPServers(ctx); err == nil {
		w.mcpPool.SyncServers(mcps)
	}
	_ = w.cronEng.Reload(ctx, n.cfg.RunnerID)

	n.cfg.Extensions = w
	return w
}

// Close releases background MCP subprocesses and Cron timers.
func (w *DefaultRuntimeWiring) Close() {
	if w.cronEng != nil {
		w.cronEng.Stop()
	}
	if w.mcpPool != nil {
		w.mcpPool.Close()
	}
}

// --- codemode.RemoteCaller ---

func (w *DefaultRuntimeWiring) IsOnline() bool {
	return w.node.IsOnline()
}

func (w *DefaultRuntimeWiring) CallRemoteMCP(ctx context.Context, namespace, toolName string, args json.RawMessage) (json.RawMessage, error) {
	reqEnv, _ := protocol.NewEnvelope(protocol.TypeMCPRemoteCall, protocol.MCPRemoteCallPayload{
		Namespace: namespace,
		ToolName:  toolName,
		Arguments: args,
	})
	resp, err := w.node.CallServerRPC(ctx, reqEnv, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var out protocol.MCPRemoteResultPayload
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s", out.Error)
	}
	return out.Result, nil
}

// --- scheduler.JobExecutor ---

func (w *DefaultRuntimeWiring) ExecuteCronPrompt(ctx context.Context, job store.CronJobRecord) error {
	p := scheduler.BuildPromptForCron(job)
	_, err := w.node.ExecutePrompt(ctx, p, "cron", false)
	return err
}

// --- subagent.LocalExecutor ---

func (w *DefaultRuntimeWiring) RunChildSession(
	ctx context.Context,
	childSessionID string,
	req subagent.SpawnRequest,
	readOnly bool,
	effectiveWorkDir string,
) (string, func(string), error) {
	permMode := agent.PermissionFullAccess
	if readOnly {
		permMode = agent.PermissionReadOnly
	} else if parentSess, err := w.node.store.GetSession(ctx, req.ParentSessionID); err == nil && parentSess.PermissionMode != "" {
		permMode = agent.PermissionMode(parentSess.PermissionMode)
	}

	// Persist child session with depth = ParentDepth + 1
	_, _ = w.node.store.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:               childSessionID,
		RunnerID:         w.node.cfg.RunnerID,
		ParentSessionID:  req.ParentSessionID,
		ParentToolCallID: req.ParentToolCallID,
		Depth:            req.ParentDepth + 1,
		Title:            fmt.Sprintf("[%s] %s", req.AgentType, truncateStr(req.Prompt, 40)),
		WorkspacePath:    effectiveWorkDir,
		AgentProfileSlug: req.AgentType,
		PermissionMode:   string(permMode),
		Status:           "running",
	})

	msg, err := w.node.ExecutePrompt(ctx, protocol.PromptPayload{
		SessionID:        childSessionID,
		RunnerID:         w.node.cfg.RunnerID,
		WorkspacePath:    effectiveWorkDir,
		AgentProfileSlug: req.AgentType,
		PermissionMode:   string(permMode),
		Text:             req.Prompt,
	}, "subagent", w.node.IsOnline())

	steerFn := func(text string) {
		w.node.mu.Lock()
		as := w.node.activeSessions[childSessionID]
		w.node.mu.Unlock()
		if as != nil {
			as.loop.Steer(ai.NewUserTextMessage(text))
		}
	}

	if err != nil {
		return "", steerFn, err
	}
	return msg.PlainText(), steerFn, nil
}

func (w *DefaultRuntimeWiring) NotifyParentBackgroundDone(parentSessionID, subagentID, summary string) {
	if parentSessionID == "" {
		return
	}
	noteText := fmt.Sprintf("[Background subagent %s completed]\n%s", subagentID, summary)
	w.node.mu.Lock()
	as := w.node.activeSessions[parentSessionID]
	w.node.mu.Unlock()
	if as != nil {
		as.loop.FollowUp(ai.NewUserTextMessage(noteText))
		return
	}
	// If parent session is currently idle, trigger a follow-up turn automatically
	go func() {
		_, _ = w.node.ExecutePrompt(context.Background(), protocol.PromptPayload{
			SessionID: parentSessionID,
			Text:      noteText,
		}, "chat", w.node.IsOnline())
	}()
}

// --- SessionExtensionBuilder ---

type sessionUIBridge struct {
	node      *Node
	sessionID string
	hasUI     bool
}

func (b *sessionUIBridge) HasUI() bool { return b.hasUI && b.node.IsOnline() }

func (b *sessionUIBridge) Prompt(ctx context.Context, req protocol.UIPromptRequestPayload) (protocol.UIResolvePayload, error) {
	return b.node.RequestUIPrompt(ctx, b.sessionID, req)
}

func (b *sessionUIBridge) SetStatus(ctx context.Context, key, badge string, widget any) {
	b.node.EmitUIStatus(ctx, b.sessionID, key, badge, widget)
}

func (b *sessionUIBridge) SendUserMessage(text string) {
	go func() {
		_, _ = b.node.ExecutePrompt(context.Background(), protocol.PromptPayload{
			SessionID: b.sessionID,
			Text:      text,
		}, "chat", b.hasUI)
	}()
}

func (w *DefaultRuntimeWiring) PrepareSession(
	ctx context.Context,
	sess store.SessionRecord,
	node *Node,
	hasUI bool,
) ([]tools.Tool, []ai.SystemSection, agent.Hooks, func(), error) {
	uiBridge := &sessionUIBridge{node: node, sessionID: sess.ID, hasUI: hasUI}
	reg := extension.NewRegistry(sess.WorkspacePath, uiBridge)

	// Load enabled TS/JS dynamic plugins from local SQLite snapshot
	if plugins, err := node.store.ListPlugins(ctx); err == nil {
		for _, pl := range plugins {
			if pl.Enabled {
				_ = reg.LoadTSPlugin(pl.Name, pl.SourceTS, pl.CompiledJS)
			}
		}
	}

	var extraTools []tools.Tool
	var extraSections []ai.SystemSection

	// 1. Mount MCP `codemode` sandbox tool (enforcing plugin hooks + interactive approvals on every inner call!)
	approver := &runnerApprover{node: node, sessionID: sess.ID, hasUI: hasUI}
	cmTool := codemode.NewTool(w.mcpPool, codemode.NewSessionKV(), func(callCtx context.Context, fullName string, readOnly bool, args json.RawMessage) error {
		dec, err := reg.OnToolCall(callCtx, fullName, args)
		if err != nil {
			return err
		}
		if dec.Block {
			return fmt.Errorf("blocked by plugin: %s", dec.Reason)
		}
		if !readOnly && sess.PermissionMode == string(agent.PermissionInteractive) {
			aprID := "apr_" + uuid.NewString()
			approved, reason, err := approver.RequestApproval(callCtx, agent.ApprovalRequest{
				ApprovalID: aprID,
				SessionID:  sess.ID,
				ToolCallID: "cm_" + aprID,
				ToolName:   "codemode -> " + fullName,
				Arguments:  args,
			})
			if err != nil {
				return err
			}
			if !approved {
				return fmt.Errorf("permission denied for %s: %s", fullName, reason)
			}
		}
		if !readOnly && sess.PermissionMode == string(agent.PermissionReadOnly) {
			return fmt.Errorf("session is read-only; cannot call write MCP tool %s", fullName)
		}
		return nil
	})
	extraTools = append(extraTools, cmTool)

	if mcpSummary := w.mcpPool.NamespaceSummaryPrompt(); mcpSummary != "" {
		extraSections = append(extraSections, ai.SystemSection{
			Category: agent.CatMCPTools,
			Content:  mcpSummary,
		})
	}

	// 2. Mount concurrent Subagent tools ONLY if sess.Depth < max_depth (physical stripping at depth >= 1!)
	subTools := w.subMgr.ToolsForDepth(sess.ID, sess.Depth, func(status string) {
		raw, _ := json.Marshal(agent.Event{
			Type:   agent.EventSubagentProgress,
			Reason: status,
		})
		node.recordAndEmitSessionEvent(ctx, sess.ID, 0, string(agent.EventSubagentProgress), raw, nil)
	})
	extraTools = append(extraTools, subTools...)

	// 3. Mount plugin-registered custom tools
	extraTools = append(extraTools, reg.Tools()...)

	return extraTools, extraSections, reg, func() {}, nil
}

func (w *DefaultRuntimeWiring) OnSnapshotApplied(ctx context.Context, snap *protocol.ConfigSnapshot) {
	if snap == nil {
		return
	}
	w.mcpPool.SyncServers(snap.MCPServers)
	_ = w.cronEng.Reload(ctx, w.node.cfg.RunnerID)
}

func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
