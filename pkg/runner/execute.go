package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/prompts"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

type runnerApprover struct {
	node      *Node
	sessionID string
	hasUI     bool
}

func (ra *runnerApprover) RequestApproval(ctx context.Context, req agent.ApprovalRequest) (bool, string, error) {
	if !ra.hasUI || !ra.node.IsOnline() {
		return false, "permission denied: runner is in headless/offline mode", nil
	}
	ch := make(chan protocol.ApprovalResolvePayload, 1)
	ra.node.mu.Lock()
	ra.node.pendingApproval[req.ApprovalID] = ch
	ra.node.mu.Unlock()
	defer func() {
		ra.node.mu.Lock()
		delete(ra.node.pendingApproval, req.ApprovalID)
		ra.node.mu.Unlock()
	}()

	select {
	case res := <-ch:
		return res.Approved, res.Reason, nil
	case <-ctx.Done():
		return false, "approval canceled", ctx.Err()
	}
}

// RequestUIPrompt sends a remote declarative ctx.ui prompt (select/confirm/input) to connected clients.
func (n *Node) RequestUIPrompt(ctx context.Context, sessionID string, req protocol.UIPromptRequestPayload) (protocol.UIResolvePayload, error) {
	if !n.IsOnline() {
		return protocol.UIResolvePayload{Canceled: true, Value: req.DefaultValue}, nil
	}
	if req.RequestID == "" {
		req.RequestID = "uip_" + uuid.NewString()
	}
	ch := make(chan protocol.UIResolvePayload, 1)
	n.mu.Lock()
	n.pendingUI[req.RequestID] = ch
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.pendingUI, req.RequestID)
		n.mu.Unlock()
	}()

	payloadBytes, _ := json.Marshal(map[string]any{
		"type":      agent.EventUIPromptRequested,
		"ui_prompt": req,
	})
	n.recordAndEmitSessionEvent(ctx, sessionID, 0, string(agent.EventUIPromptRequested), payloadBytes, nil)

	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		return protocol.UIResolvePayload{Canceled: true}, ctx.Err()
	}
}

// EmitUIStatus publishes a plugin ctx.ui.setStatus or setWidget update to the session stream.
func (n *Node) EmitUIStatus(ctx context.Context, sessionID, key, badge string, widget any) {
	payloadBytes, _ := json.Marshal(map[string]any{
		"type":   agent.EventUIStatusUpdated,
		"key":    key,
		"badge":  badge,
		"widget": widget,
	})
	n.recordAndEmitSessionEvent(ctx, sessionID, 0, string(agent.EventUIStatusUpdated), payloadBytes, nil)
}

func (n *Node) recordAndEmitSessionEvent(ctx context.Context, sessionID string, turnIdx int, evType string, payload json.RawMessage, blobHashes []string) {
	// ZCode Persistence Rule: transient streaming deltas are emitted live over WS only, never stored in SQLite/Outbox!
	if evType == string(agent.EventMessageUpdate) || evType == string(agent.EventToolExecutionUpdate) {
		if !n.IsOnline() {
			return
		}
		transient := store.SessionEvent{
			SessionID: sessionID,
			TurnIdx:   turnIdx,
			EventType: evType,
			Payload:   payload,
			CreatedAt: time.Now().UnixMilli(),
		}
		env, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, transient)
		env.SessionID = sessionID
		env.RunnerID = n.cfg.RunnerID
		_ = n.sendEnvelope(ctx, env)
		return
	}

	online := n.IsOnline()
	saved, err := n.store.AppendSessionEvent(ctx, store.SessionEvent{
		SessionID:  sessionID,
		TurnIdx:    turnIdx,
		EventType:  evType,
		Payload:    payload,
		BlobHashes: blobHashes,
	}, !online)
	if err != nil {
		return
	}

	if online {
		env, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, saved)
		env.SessionID = saved.SessionID
		env.RunnerID = n.cfg.RunnerID
		env.Seq = saved.Seq
		if sendErr := n.sendEnvelope(ctx, env); sendErr != nil {
			// Connection dropped right before write: store into Outbox for later replay!
			_, _ = n.store.AppendSessionEvent(ctx, saved, true)
		}
	}
}

func (n *Node) resolveProviderAndModel(ctx context.Context, provID, modelID string) (ai.Provider, string, string, ai.ModelMeta, error) {
	if n.cfg.ProviderOverride != nil {
		if provID == "" {
			provID = "anthropic"
		}
		if modelID == "" {
			modelID = "claude-sonnet-4-5"
		}
		m, err := n.store.GetModel(ctx, provID, modelID)
		meta := ai.ModelMeta{ID: modelID, ProviderID: provID, ContextWindow: 200000}
		if err == nil && m != nil {
			meta = *m
		}
		return n.cfg.ProviderOverride, provID, modelID, meta, nil
	}

	provs, _ := n.store.ListProviders(ctx)
	var chosenProv *store.ProviderRecord
	for i := range provs {
		if provID != "" && provs[i].ID == provID {
			chosenProv = &provs[i]
			break
		}
		if provID == "" && provs[i].Enabled {
			chosenProv = &provs[i]
			if provs[i].APIKey != "" {
				break
			}
		}
	}
	if chosenProv == nil {
		return nil, "", "", ai.ModelMeta{}, fmt.Errorf("runner: no enabled LLM provider found (requested %q)", provID)
	}

	apiKey := chosenProv.APIKey
	if apiKey == "" {
		if chosenProv.Driver == ai.DriverAnthropic {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		} else {
			apiKey = os.Getenv("OPENAI_API_KEY")
		}
	}

	if modelID == "" {
		mods, _ := n.store.ListModels(ctx, chosenProv.ID)
		if len(mods) > 0 {
			modelID = mods[0].ID
		} else if chosenProv.Driver == ai.DriverAnthropic {
			modelID = "claude-sonnet-4-5"
		} else {
			modelID = "gpt-5-codex"
		}
	}

	var meta ai.ModelMeta
	if m, err := n.store.GetModel(ctx, chosenProv.ID, modelID); err == nil && m != nil {
		meta = *m
	} else {
		meta = ai.ModelMeta{ID: modelID, ProviderID: chosenProv.ID, ContextWindow: 200000, MaxOutputTokens: 16384}
	}
	if len(meta.ReasoningLevels) == 0 {
		meta.ReasoningLevels = ai.DefaultReasoningLevels(meta)
	}

	p, err := ai.NewProvider(chosenProv.Driver, chosenProv.BaseURL, apiKey, n.hc)
	if err != nil {
		return nil, "", "", ai.ModelMeta{}, err
	}
	return p, chosenProv.ID, modelID, meta, nil
}

func (n *Node) loadSessionMessages(ctx context.Context, sessionID string) []ai.Message {
	evs, err := n.store.ListSessionEventsAfter(ctx, sessionID, 0)
	if err != nil {
		return nil
	}
	var msgs []ai.Message
	for _, ev := range evs {
		switch ev.EventType {
		case string(agent.EventCompaction):
			var parsed struct {
				CompactedMessages []ai.Message `json:"compacted_messages"`
			}
			if json.Unmarshal(ev.Payload, &parsed) == nil && len(parsed.CompactedMessages) > 0 {
				msgs = make([]ai.Message, len(parsed.CompactedMessages))
				copy(msgs, parsed.CompactedMessages)
			}
		case string(agent.EventMessageEnd):
			var parsed struct {
				Message *ai.Message `json:"message"`
			}
			if json.Unmarshal(ev.Payload, &parsed) == nil && parsed.Message != nil {
				msgs = append(msgs, *parsed.Message)
			}
		case string(agent.EventToolExecutionEnd):
			var parsed struct {
				ToolCallID string        `json:"tool_call_id"`
				ToolName   string        `json:"tool_name"`
				ToolResult *tools.Result `json:"tool_result"`
			}
			if json.Unmarshal(ev.Payload, &parsed) == nil && parsed.ToolCallID != "" && parsed.ToolResult != nil {
				msgs = append(msgs, ai.NewToolResultMessage(
					parsed.ToolCallID,
					parsed.ToolName,
					parsed.ToolResult.Output,
					parsed.ToolResult.IsError,
					parsed.ToolResult.ExtraBlocks...,
				))
			}
		}
	}
	return msgs
}

type slashCommandExecutor interface {
	HasCommand(name string) bool
	ExecuteCommand(ctx context.Context, name, args string) error
}

func parseSlashCommand(raw string) (name, args string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "/") || len(trimmed) < 2 {
		return "", "", false
	}
	rest := trimmed[1:]
	idx := strings.IndexAny(rest, " \t\n\r")
	if idx < 0 {
		return strings.TrimSpace(rest), "", true
	}
	return strings.TrimSpace(rest[:idx]), strings.TrimSpace(rest[idx+1:]), true
}

// ExecutePrompt runs a full Agent Loop turn cycle for a chat prompt, subagent task, or autonomous cron job.
func (n *Node) ExecutePrompt(
	parentCtx context.Context,
	p protocol.PromptPayload,
	triggerSource string,
	hasUI bool,
) (*ai.Message, error) {
	if p.SessionID == "" {
		p.SessionID = "sess_" + uuid.NewString()
	}
	workDir := p.WorkspacePath
	if workDir == "" {
		workDir = n.cfg.WorkspaceDefault
	}
	permMode := agent.PermissionMode(p.PermissionMode)
	if permMode == "" {
		if hasUI {
			permMode = agent.PermissionInteractive
		} else {
			permMode = agent.PermissionAutonomous
		}
	}

	prov, provID, modelID, modelMeta, err := n.resolveProviderAndModel(parentCtx, p.ProviderID, p.ModelID)
	if err != nil {
		return nil, err
	}

	title, modelText := buildUserTurnText(p.Text)

	sessRec, err := n.store.GetSession(parentCtx, p.SessionID)
	if err != nil {
		created, cErr := n.store.CreateOrUpdateSession(parentCtx, store.SessionRecord{
			ID:                 p.SessionID,
			RunnerID:           n.cfg.RunnerID,
			Title:              title,
			WorkspacePath:      workDir,
			AgentProfileSlug:   p.AgentProfileSlug,
			ProviderID:         provID,
			ModelID:            modelID,
			PermissionMode:     string(permMode),
			Status:             "running",
			ContextWindowLimit: modelMeta.ContextWindow,
		})
		if cErr != nil {
			return nil, cErr
		}
		sessRec = &created
	} else {
		sessRec.Status = "running"
		sessRec.ProviderID = provID
		sessRec.ModelID = modelID
		sessRec.PermissionMode = string(permMode)
		updated, _ := n.store.CreateOrUpdateSession(parentCtx, *sessRec)
		sessRec = &updated
	}

	history := n.loadSessionMessages(parentCtx, sessRec.ID)

	// Build System Sections (stable cached core + Skills + MCP + volatile meta context)
	sections := buildPromptSections(parentCtx, n, sessRec, workDir, triggerSource)

	sessionLookup := &runnerSessionLookup{node: n}
	if refReminder := tools.BuildReferencedSessionReminder(parentCtx, sessionLookup, p.Text); refReminder != "" {
		sections = append(sections, ai.SystemSection{
			Category: agent.CatMetaContext,
			Name:     "referenced_sessions",
			Content:  refReminder,
		})
	}

	sessionTools, bgCleanup := tools.DefaultCodingToolsWithCleanup()
	defer bgCleanup()
	sessionTools = append(sessionTools,
		tools.NewAskUserQuestionTool(&sessionUIBridge{
			node:      n,
			sessionID: sessRec.ID,
			hasUI:     hasUI,
		}),
		tools.NewReadSessionContextTool(sessionLookup, sessRec.ID),
	)

	var hooks agent.Hooks
	var extCleanup func()
	if n.cfg.Extensions != nil {
		extraTools, extraSecs, h, cleanupFn, extErr := n.cfg.Extensions.PrepareSession(parentCtx, *sessRec, n, hasUI)
		if extErr == nil {
			sessionTools = append(sessionTools, extraTools...)
			sections = append(sections, extraSecs...)
			hooks = h
			extCleanup = cleanupFn
		}
	}
	if extCleanup != nil {
		defer extCleanup()
	}

	runCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// ZCode 30ms Frame Coalescer: first streaming delta flushes eagerly for snappy TTFT;
	// subsequent adjacent deltas coalesce within 30ms windows; any lifecycle/control event flushes pending first!
	var coalMu sync.Mutex
	var pendingEv *agent.Event
	var flushTimer *time.Timer
	firstDeltaSent := false

	emitRaw := func(ev agent.Event) {
		type enrichedEvent struct {
			agent.Event
			SessionMeta *store.SessionRecord `json:"session_meta,omitempty"`
		}
		var metaPtr *store.SessionRecord
		if ev.Type == agent.EventAgentStart {
			cp := *sessRec
			metaPtr = &cp
		}
		var hashes []string
		if ev.Telemetry != nil {
			if ev.Telemetry.SystemPromptBlobHash != "" {
				hashes = append(hashes, ev.Telemetry.SystemPromptBlobHash)
			}
			if ev.Telemetry.RawTraceBlobHash != "" {
				hashes = append(hashes, ev.Telemetry.RawTraceBlobHash)
			}
		}
		raw, _ := json.Marshal(enrichedEvent{Event: ev, SessionMeta: metaPtr})
		n.recordAndEmitSessionEvent(context.Background(), sessRec.ID, ev.TurnIdx, string(ev.Type), raw, hashes)
	}

	// Handle built-in `/compact [focus]` command immediately without invoking a normal coding turn
	if focus, isCompact := agent.ParseCompactCommand(p.Text); isCompact {
		defer func() {
			sessRec.Status = "idle"
			_, _ = n.store.CreateOrUpdateSession(context.Background(), *sessRec)
		}()
		emitRaw(agent.Event{Type: agent.EventAgentStart})
		userMsg := ai.NewUserTextMessage(p.Text)
		emitRaw(agent.Event{Type: agent.EventMessageEnd, Message: &userMsg})

		var replyText string
		if len(history) <= 2 {
			replyText = "Context is already compact (fewer than 3 messages in history)."
		} else {
			beforeTokens := agent.EstimateContextTokens(nil, "", nil, history)
			compacted, summary, cErr := agent.CompactMessages(runCtx, prov, modelID, history, focus)
			if cErr != nil {
				replyText = fmt.Sprintf("Context compaction failed: %v", cErr)
			} else {
				afterTokens := agent.EstimateContextTokens(nil, "", nil, compacted)
				var summaryMsg *ai.Message
				if len(compacted) > 0 {
					m := compacted[0]
					summaryMsg = &m
				}
				emitRaw(agent.Event{
					Type:              agent.EventCompaction,
					Reason:            "manual",
					Delta:             summary,
					Message:           summaryMsg,
					CompactedMessages: compacted,
				})
				replyText = fmt.Sprintf("Compacted session context (%d -> %d messages, ~%d -> ~%d tokens).\n\n%s", len(history), len(compacted), beforeTokens, afterTokens, summary)
			}
		}
		replyMsg := ai.Message{
			Role:      ai.RoleAssistant,
			Content:   []ai.ContentBlock{ai.TextBlock(replyText)},
			Timestamp: time.Now().UnixMilli(),
		}
		emitRaw(agent.Event{Type: agent.EventMessageEnd, Message: &replyMsg})
		emitRaw(agent.Event{Type: agent.EventAgentEnd})
		return &replyMsg, nil
	}

	// Dispatch plugin-registered `/slash` commands (e.g., `/deploy-check`) when not `/goal`
	if _, isGoal := prompts.ParseGoalCommand(p.Text); !isGoal {
		if cmdName, cmdArgs, isSlash := parseSlashCommand(p.Text); isSlash {
			if cmdExec, ok := hooks.(slashCommandExecutor); ok && cmdExec.HasCommand(cmdName) {
				defer func() {
					sessRec.Status = "idle"
					_, _ = n.store.CreateOrUpdateSession(context.Background(), *sessRec)
				}()
				emitRaw(agent.Event{Type: agent.EventAgentStart})
				userMsg := ai.NewUserTextMessage(p.Text)
				emitRaw(agent.Event{Type: agent.EventMessageEnd, Message: &userMsg})

				cmdErr := cmdExec.ExecuteCommand(runCtx, cmdName, cmdArgs)
				replyText := fmt.Sprintf("Executed command `/%s`.", cmdName)
				if cmdErr != nil {
					replyText = fmt.Sprintf("Command `/%s` failed: %v", cmdName, cmdErr)
				}
				replyMsg := ai.Message{
					Role:      ai.RoleAssistant,
					Content:   []ai.ContentBlock{ai.TextBlock(replyText)},
					Timestamp: time.Now().UnixMilli(),
				}
				emitRaw(agent.Event{Type: agent.EventMessageEnd, Message: &replyMsg})
				emitRaw(agent.Event{Type: agent.EventAgentEnd})
				return &replyMsg, cmdErr
			}
		}
	}

	flushPendingLocked := func() {
		if flushTimer != nil {
			flushTimer.Stop()
			flushTimer = nil
		}
		if pendingEv != nil {
			evToEmit := *pendingEv
			pendingEv = nil
			emitRaw(evToEmit)
		}
	}

	defer func() {
		coalMu.Lock()
		flushPendingLocked()
		coalMu.Unlock()
	}()

	loop := agent.NewLoop(agent.LoopConfig{
		SessionID:       sessRec.ID,
		RunnerID:        n.cfg.RunnerID,
		ProviderID:      provID,
		Model:           modelID,
		Provider:        prov,
		WorkDir:         workDir,
		SystemSections:  sections,
		ThinkingEffort:  p.ThinkingEffort,
		ReasoningLevels: modelMeta.ReasoningLevels,
		ReasoningMap:    modelMeta.ReasoningMap,
		MaxOutputTokens: modelMeta.MaxOutputTokens,
		ContextWindow:   modelMeta.ContextWindow,
		Pricing:         modelMeta.Pricing,
		PermissionMode:  permMode,
		TriggerSource:   triggerSource,
		Tools:           sessionTools,
		Approver:        &runnerApprover{node: n, sessionID: sessRec.ID, hasUI: hasUI && n.IsOnline()},
		Hooks:           hooks,
		Store:           n.store,
		OnEvent: func(ev agent.Event) {
			coalMu.Lock()
			defer coalMu.Unlock()

			isCoalescible := ev.Type == agent.EventMessageUpdate || ev.Type == agent.EventToolExecutionUpdate
			if !isCoalescible {
				flushPendingLocked()
				if ev.Type == agent.EventMessageStart {
					firstDeltaSent = false
				}
				emitRaw(ev)
				return
			}

			// Eager first delta for instant TTFT feel
			if !firstDeltaSent {
				firstDeltaSent = true
				emitRaw(ev)
				return
			}

			if pendingEv != nil &&
				pendingEv.Type == ev.Type &&
				pendingEv.TurnIdx == ev.TurnIdx &&
				pendingEv.DeltaKind == ev.DeltaKind &&
				pendingEv.ToolCallID == ev.ToolCallID &&
				len(pendingEv.Delta)+len(ev.Delta) <= 8192 {
				pendingEv.Delta += ev.Delta
				return
			}

			flushPendingLocked()
			evCopy := ev
			pendingEv = &evCopy
			flushTimer = time.AfterFunc(30*time.Millisecond, func() {
				coalMu.Lock()
				defer coalMu.Unlock()
				flushPendingLocked()
			})
		},
	}, history)

	n.mu.Lock()
	n.activeSessions[sessRec.ID] = &activeSession{record: *sessRec, loop: loop, cancel: cancel}
	n.mu.Unlock()

	defer func() {
		n.mu.Lock()
		delete(n.activeSessions, sessRec.ID)
		n.mu.Unlock()
		sessRec.Status = "idle"
		_, _ = n.store.CreateOrUpdateSession(context.Background(), *sessRec)
	}()

	var blocks []ai.ContentBlock
	if modelText != "" {
		blocks = append(blocks, ai.TextBlock(modelText))
	}
	blocks = append(blocks, p.Images...)
	userMsg := ai.Message{Role: ai.RoleUser, Content: blocks}

	reply, runErr := loop.Run(runCtx, &userMsg)
	if runErr == nil && len(history) == 0 && sessRec.Depth == 0 && triggerSource == "chat" && n.cfg.ProviderOverride == nil && len([]rune(strings.TrimSpace(p.Text))) > 16 {
		assistantSummary := ""
		if reply != nil {
			assistantSummary = reply.PlainText()
		}
		go n.generateSessionTitleAsync(sessRec.ID, prov, modelID, p.Text, assistantSummary)
	}
	return reply, runErr
}

// generateSessionTitleAsync runs a lightweight low-cost sidecar completion (thinking off, no tools)
// to replace the initial truncated prompt title with a concise session title.
func (n *Node) generateSessionTitleAsync(sessionID string, prov ai.Provider, modelID, userText, assistantText string) {
	if prov == nil || sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	uClip := strings.TrimSpace(userText)
	if runes := []rune(uClip); len(runes) > 400 {
		uClip = string(runes[:400])
	}
	aClip := strings.TrimSpace(assistantText)
	if runes := []rune(aClip); len(runes) > 300 {
		aClip = string(runes[:300])
	}

	req := &ai.StreamRequest{
		Model:           modelID,
		SystemPrompt:    "Generate a concise conversation title (max 14 Chinese characters or 7 English words) matching the user's language. Return ONLY the plain title text without quotes, markdown, prefixes, or trailing punctuation.",
		Messages:        []ai.Message{ai.NewUserTextMessage(fmt.Sprintf("User: %s\nAssistant: %s", uClip, aClip))},
		MaxOutputTokens: 48,
		ThinkingEffort:  "",
	}
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		return
	}
	var sb strings.Builder
	for ev := range ch {
		if ev.Type == ai.StreamEventTextDelta {
			sb.WriteString(ev.TextDelta)
		} else if ev.Type == ai.StreamEventDone && ev.Message != nil && sb.Len() == 0 {
			sb.WriteString(ev.Message.PlainText())
		}
	}
	clean := cleanGeneratedTitle(sb.String())
	if clean == "" {
		return
	}
	if rec, gErr := n.store.GetSession(ctx, sessionID); gErr == nil && rec != nil {
		rec.Title = clean
		_, _ = n.store.CreateOrUpdateSession(ctx, *rec)
	}
	payloadBytes, _ := json.Marshal(map[string]any{
		"type":  "session_title_updated",
		"title": clean,
	})
	if n.IsOnline() {
		ev := store.SessionEvent{
			SessionID: sessionID,
			EventType: "session_title_updated",
			Payload:   payloadBytes,
			CreatedAt: time.Now().UnixMilli(),
		}
		env, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, ev)
		env.SessionID = sessionID
		env.RunnerID = n.cfg.RunnerID
		_ = n.sendEnvelope(ctx, env)
	}
}

func cleanGeneratedTitle(raw string) string {
	s := strings.TrimSpace(raw)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	s = strings.Trim(s, "\"'`“”‘’《》【】[]#*-:： ")
	s = strings.TrimRight(s, ".。!！?？")
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > 40 {
		s = string(runes[:40])
	}
	return s
}

// buildUserTurnText splits a raw prompt into a clean display/title label and the
// model-facing text. A leading `/goal ` is stripped for the title and the
// objective is wrapped in structured goal framing for the model.
func buildUserTurnText(raw string) (title, modelText string) {
	display := raw
	modelText = raw

	if objective, isGoal := prompts.ParseGoalCommand(raw); isGoal {
		display = prompts.GoalDisplaySummary(objective)
		modelText = prompts.FormatGoalTurnPrompt(objective)
	}
	if strings.TrimSpace(display) == "" {
		display = "Session"
	}

	title = display
	if runes := []rune(title); len(runes) > 48 {
		title = string(runes[:48]) + "..."
	}
	return title, modelText
}

// buildPromptSections assembles the ordered system sections for a session. Child
// sessions use the subagent prompt builder; everything else uses the main agent.
// Skills and MCP namespaces are collected here so the cached core stays stable.
func buildPromptSections(ctx context.Context, n *Node, sessRec *store.SessionRecord, workDir, triggerSource string) []ai.SystemSection {
	opts := prompts.SessionOptions{
		RunnerID:       n.cfg.RunnerID,
		WorkDir:        workDir,
		PermissionMode: sessRec.PermissionMode,
		Now:            time.Now(),
	}

	isSubagent := sessRec.Depth > 0 || triggerSource == "subagent"
	if isSubagent {
		opts.SubagentType = subagentRoleSlug(sessRec.AgentProfileSlug)
	}
	opts.CustomPrompt = agentProfilePrompt(ctx, n, sessRec.AgentProfileSlug)

	opts.Skills = loadSkillInfos(ctx, n)
	opts.MCPNamespaces = loadMCPNamespaceSummary(ctx, n)

	return prompts.BuildSessionSections(opts)
}

// subagentRoleSlug maps a profile slug to a built-in role. Custom slugs are not
// built-in roles, so they fall back to the generic worker prompt; the profile's
// own instructions are still appended via the CustomPrompt option.
func subagentRoleSlug(slug string) string {
	if prompts.IsSubagentRole(slug) {
		return slug
	}
	return prompts.SubagentWorker
}

func agentProfilePrompt(ctx context.Context, n *Node, slug string) string {
	if slug == "" {
		return ""
	}
	profiles, err := n.store.ListAgentProfiles(ctx)
	if err != nil {
		return ""
	}
	for _, ap := range profiles {
		if ap.Slug == slug {
			return ap.SystemPrompt
		}
	}
	return ""
}

func loadSkillInfos(ctx context.Context, n *Node) []prompts.SkillInfo {
	skills, err := n.store.ListSkills(ctx)
	if err != nil {
		return nil
	}
	out := make([]prompts.SkillInfo, 0, len(skills))
	for _, sk := range skills {
		if !sk.Enabled {
			continue
		}
		out = append(out, prompts.SkillInfo{
			Name:        sk.Name,
			Description: sk.Description,
			Path:        filepath.Join(n.cfg.DataDir, "skills", sk.Name, "SKILL.md"),
		})
	}
	return out
}

// loadMCPNamespaceSummary asks the extension builder for its live MCP namespace
// summary when it exposes one. When extensions are attached but expose no
// summary, it returns nothing because PrepareSession already injects the
// mcp_tools section itself; only standalone runners fall back to the store.
type runnerSessionLookup struct {
	node *Node
}

func (l *runnerSessionLookup) serverHTTPBase() string {
	wsURL := strings.TrimSpace(l.node.cfg.ServerWSURL)
	if wsURL == "" {
		return ""
	}
	u, err := url.Parse(wsURL)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := "http"
	if u.Scheme == "wss" || u.Scheme == "https" {
		scheme = "https"
	}
	return scheme + "://" + u.Host
}

func (l *runnerSessionLookup) GetSession(ctx context.Context, id string) (*store.SessionRecord, error) {
	if rec, err := l.node.store.GetSession(ctx, id); err == nil && rec != nil {
		return rec, nil
	}
	base := l.serverHTTPBase()
	if base == "" {
		return nil, fmt.Errorf("session %q not found", id)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/sessions", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ai.DefaultUserAgent)
	if l.node.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+l.node.cfg.AuthToken)
	}
	resp, err := l.node.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server session lookup HTTP %d", resp.StatusCode)
	}
	var list []store.SessionRecord
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("session %q not found", id)
}

func (l *runnerSessionLookup) ListSessionEventsAfter(ctx context.Context, sessionID string, afterSeq uint64) ([]store.SessionEvent, error) {
	if evs, err := l.node.store.ListSessionEventsAfter(ctx, sessionID, afterSeq); err == nil && len(evs) > 0 {
		return evs, nil
	}
	base := l.serverHTTPBase()
	if base == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/sessions/%s/events?after_seq=%d", base, url.PathEscape(sessionID), afterSeq), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ai.DefaultUserAgent)
	if l.node.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+l.node.cfg.AuthToken)
	}
	resp, err := l.node.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server events lookup HTTP %d", resp.StatusCode)
	}
	var evs []store.SessionEvent
	if err := json.NewDecoder(resp.Body).Decode(&evs); err != nil {
		return nil, err
	}
	return evs, nil
}

func loadMCPNamespaceSummary(ctx context.Context, n *Node) string {
	type namespaceSummarizer interface {
		MCPNamespaceSummary() string
	}
	if s, ok := n.cfg.Extensions.(namespaceSummarizer); ok {
		return strings.TrimSpace(s.MCPNamespaceSummary())
	}
	if n.cfg.Extensions != nil {
		return ""
	}

	servers, err := n.store.ListMCPServers(ctx)
	if err != nil || len(servers) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Enabled MCP namespaces are reachable lazily through the `codemode` tool via `searchTools`, `describeTool`, and `tools.<namespace>.<tool>`:\n")
	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		mode := srv.HostMode
		if mode == "" {
			mode = "local"
		}
		fmt.Fprintf(&sb, "- %s (%s)\n", srv.Name, mode)
	}
	return strings.TrimSpace(sb.String())
}
