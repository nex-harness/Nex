package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/extension"
	"github.com/nex-harness/nex/pkg/harness/browser"
	"github.com/nex-harness/nex/pkg/harness/codemode"
	"github.com/nex-harness/nex/pkg/harness/pty"
	"github.com/nex-harness/nex/pkg/harness/scheduler"
	"github.com/nex-harness/nex/pkg/harness/subagent"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/harness/workspace"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

// DefaultRuntimeWiring connects WorkspaceRPC, PTY, Browser, CodeMode MCP, TS/Go Extensions, Subagents, and Autonomous Cron onto a Node.
type DefaultRuntimeWiring struct {
	node       *Node
	mcpPool    *codemode.Pool
	subMgr     *subagent.Manager
	cronEng    *scheduler.Engine
	browserMgr *browser.Manager
}

// AttachDefaultSubsystems wires all M3, M4, M5, and Browser Use subsystems onto n.
func AttachDefaultSubsystems(n *Node) *DefaultRuntimeWiring {
	ctx := context.Background()
	if n.cfg.WorkspaceRPC == nil {
		n.cfg.WorkspaceRPC = workspace.NewService()
	}
	if n.cfg.PTY == nil {
		n.cfg.PTY = pty.NewManager(n.cfg.WorkspaceDefault)
	}
	var browserMgr *browser.Manager
	if n.cfg.Browser == nil {
		browserMgr = browser.NewManager(
			n.cfg.DataDir,
			n.cfg.RunnerID,
			n.hc,
			func(st browser.SessionStatePayload) {
				env, _ := protocol.NewEnvelope(protocol.TypeBrowserState, st)
				env.SessionID = st.SessionID
				env.RunnerID = n.cfg.RunnerID
				_ = n.sendEnvelope(context.Background(), env)
			},
			func(hst browser.HandoffState) {
				env, _ := protocol.NewEnvelope(protocol.TypeBrowserHandoff, hst)
				env.SessionID = hst.SessionID
				env.RunnerID = n.cfg.RunnerID
				_ = n.sendEnvelope(context.Background(), env)
			},
			n.sendBinaryFrame,
		)
		if prefs, err := n.store.GetUserPreferences(ctx); err == nil {
			browserMgr.UpdateConfig(browserConfigFromPrefs(prefs))
		}
		n.cfg.Browser = browserMgr
	}

	w := &DefaultRuntimeWiring{node: n, browserMgr: browserMgr}
	w.mcpPool = codemode.NewPool(w)
	w.subMgr = subagent.NewManager(w, subagent.DefaultMaxDepth, subagent.DefaultMaxConcurrent)
	w.subMgr.SetOnStateChange(func(parentSessionID string) {
		w.node.emitBackgroundTasksSnapshot(parentSessionID)
	})
	w.cronEng = scheduler.NewEngine(n.store, w)

	// Load initial MCP servers & Cron schedules from local SQLite snapshot (for immediate offline readiness!)
	if mcps, err := n.store.ListMCPServers(ctx); err == nil {
		w.mcpPool.SyncServers(mcps)
	}
	_ = w.cronEng.Reload(ctx, n.cfg.RunnerID)

	n.cfg.Extensions = w
	return w
}

func browserConfigFromPrefs(p store.UserPreferences) browser.Config {
	cfg := browser.DefaultConfig()
	cfg.Enabled = p.BrowserEnabled
	if p.BrowserMode != "" {
		cfg.Mode = browser.Mode(p.BrowserMode)
	}
	cfg.Headless = p.BrowserHeadless
	if p.BrowserProfileMode != "" {
		cfg.ProfileMode = browser.ProfileMode(p.BrowserProfileMode)
	}
	cfg.AttachURL = p.BrowserAttachURL
	cfg.ExecutablePath = p.BrowserExecutablePath
	cfg.DownloadMirror = p.BrowserDownloadMirror
	return cfg
}

// Close releases background MCP subprocesses, managed browser runtime, and Cron timers.
func (w *DefaultRuntimeWiring) Close() {
	if w.cronEng != nil {
		w.cronEng.Stop()
	}
	if w.mcpPool != nil {
		w.mcpPool.Close()
	}
	if w.browserMgr != nil {
		w.browserMgr.Close()
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

func parseModelRef(ref string) (providerID, modelID string) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" || strings.EqualFold(trimmed, "inherit") || strings.EqualFold(trimmed, "default") {
		return "", ""
	}
	if prov, mod, ok := strings.Cut(trimmed, "::"); ok {
		return strings.TrimSpace(prov), strings.TrimSpace(mod)
	}
	return "", trimmed
}

func (w *DefaultRuntimeWiring) resolveSubagentModel(
	ctx context.Context,
	parentSess *store.SessionRecord,
	agentType string,
	reqModelRef string,
) (providerID, modelID, thinkingEffort string) {
	if parentSess != nil {
		providerID = parentSess.ProviderID
		modelID = parentSess.ModelID
	}
	if w == nil || w.node == nil || w.node.store == nil {
		return providerID, modelID, ""
	}

	var matchedProfile *store.AgentProfile
	if profiles, err := w.node.store.ListAgentProfiles(ctx); err == nil {
		for i := range profiles {
			if strings.EqualFold(strings.TrimSpace(profiles[i].Slug), strings.TrimSpace(agentType)) {
				matchedProfile = &profiles[i]
				break
			}
		}
	}

	rawRef := strings.TrimSpace(reqModelRef)
	if rawRef == "" && matchedProfile != nil {
		rawRef = strings.TrimSpace(matchedProfile.ModelRef)
	}
	effort := ""
	if matchedProfile != nil {
		effort = strings.TrimSpace(matchedProfile.ThinkingEffort)
	}

	pID, mID := parseModelRef(rawRef)
	if pID == "" && mID == "" {
		// Default: inherit main conversation's provider and model identically.
		return providerID, modelID, effort
	}

	providers, _ := w.node.store.ListProviders(ctx)
	enabledProv := map[string]bool{}
	for _, pr := range providers {
		if pr.Enabled {
			enabledProv[pr.ID] = true
		}
	}
	models, _ := w.node.store.ListModels(ctx, "")
	if len(models) > 0 {
		for _, m := range models {
			if len(providers) > 0 && !enabledProv[m.ProviderID] {
				continue
			}
			if pID != "" && m.ProviderID == pID && m.ID == mID {
				return m.ProviderID, m.ID, effort
			}
			if pID == "" && m.ID == mID {
				return m.ProviderID, m.ID, effort
			}
		}
		if pID == "" && strings.Contains(mID, "/") {
			if slashProv, slashMod, ok := strings.Cut(mID, "/"); ok {
				for _, m := range models {
					if (len(providers) == 0 || enabledProv[m.ProviderID]) && m.ProviderID == slashProv && m.ID == slashMod {
						return m.ProviderID, m.ID, effort
					}
				}
			}
		}
		// Override model no longer exists or provider disabled: safely fall back to main session model.
		return providerID, modelID, ""
	}

	if pID != "" {
		return pID, mID, effort
	}
	return providerID, mID, effort
}

// --- subagent.LocalExecutor ---

func (w *DefaultRuntimeWiring) RunChildSession(
	ctx context.Context,
	childSessionID string,
	req subagent.SpawnRequest,
	readOnly bool,
	effectiveWorkDir string,
) (string, func(string), error) {
	parentSess, parentErr := w.node.store.GetSession(ctx, req.ParentSessionID)
	permMode := agent.PermissionFullAccess
	if readOnly {
		permMode = agent.PermissionReadOnly
	} else if parentErr == nil && parentSess.PermissionMode != "" {
		permMode = agent.PermissionMode(parentSess.PermissionMode)
	}

	var parentPtr *store.SessionRecord
	if parentErr == nil {
		parentPtr = parentSess
	}
	childProvID, childModelID, childEffort := w.resolveSubagentModel(ctx, parentPtr, req.AgentType, req.Model)

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
		ProviderID:       childProvID,
		ModelID:          childModelID,
		PermissionMode:   string(permMode),
		Status:           "running",
	})

	msg, err := w.node.ExecutePrompt(ctx, protocol.PromptPayload{
		SessionID:        childSessionID,
		RunnerID:         w.node.cfg.RunnerID,
		WorkspacePath:    effectiveWorkDir,
		AgentProfileSlug: req.AgentType,
		ProviderID:       childProvID,
		ModelID:          childModelID,
		ThinkingEffort:   ai.ThinkingEffort(childEffort),
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
	w.node.wakeSessionWithBackgroundNotification(parentSessionID, noteText)
}

func (w *DefaultRuntimeWiring) ListBackgroundSubagents(parentSessionID string) []protocol.BackgroundTaskInfo {
	if w.subMgr == nil {
		return nil
	}
	snaps := w.subMgr.ListRunningByParent(parentSessionID)
	out := make([]protocol.BackgroundTaskInfo, 0, len(snaps))
	for _, s := range snaps {
		title := strings.Join(strings.Fields(s.Prompt), " ")
		if runes := []rune(title); len(runes) > 80 {
			title = string(runes[:80]) + "..."
		}
		if title == "" {
			title = s.AgentType
		}
		out = append(out, protocol.BackgroundTaskInfo{
			TaskID:    s.SubagentID,
			Kind:      "local_agent",
			Title:     title,
			Status:    "running",
			StartedAt: s.StartedAt,
		})
	}
	return out
}

func (w *DefaultRuntimeWiring) CancelBackgroundSubagent(taskID, initiator string) (string, bool) {
	if w.subMgr == nil {
		return "", false
	}
	return w.subMgr.CancelByID(taskID, initiator)
}

func (w *DefaultRuntimeWiring) IsBackgroundSession(sessionID string) bool {
	if w.subMgr == nil {
		return false
	}
	return w.subMgr.IsBackgroundSession(sessionID)
}

func (w *DefaultRuntimeWiring) CancelAllBackgroundSubagents() {
	if w.subMgr != nil {
		w.subMgr.CancelAll()
	}
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

	// 0. Hot-discover workspace/user .mcp.json servers so newly written .mcp.json takes effect immediately
	if localDiscovered := codemode.DiscoverLocalMCPServers(sess.WorkspacePath, ""); len(localDiscovered) > 0 {
		w.mcpPool.MergeDiscoveredServers(localDiscovered)
	}

	// 1. Mount MCP `codemode` sandbox tool (enforcing plugin hooks + interactive approvals on every inner call!)
	approver := &runnerApprover{node: node, sessionID: sess.ID, hasUI: hasUI}
	cmTool := codemode.NewTool(w.mcpPool, codemode.NewSessionKV(), func(callCtx context.Context, fullName string, readOnly bool, args json.RawMessage) error {
		if ns, _, ok := strings.Cut(fullName, "."); ok && ns != "" {
			if !w.mcpPool.IsNamespaceVisibleInWorkspace(ns, sess.WorkspacePath) {
				return fmt.Errorf("MCP server %q is hidden or disabled in workspace %s", ns, sess.WorkspacePath)
			}
		}
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
	cmTool.SetModelBridge(&codemode.ModelBridge{
		ResolveClassifier: node.resolveClassifierProvider,
		ResolveImage:      node.resolveImageProvider,
	})
	extraTools = append(extraTools, cmTool)

	if mcpSummary := w.mcpPool.NamespaceSummaryPromptForWorkspace(sess.WorkspacePath); mcpSummary != "" {
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
