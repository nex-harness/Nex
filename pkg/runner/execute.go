package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
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
		if ev.EventType == string(agent.EventMessageEnd) {
			var parsed struct {
				Message *ai.Message `json:"message"`
			}
			if json.Unmarshal(ev.Payload, &parsed) == nil && parsed.Message != nil {
				msgs = append(msgs, *parsed.Message)
			}
		}
	}
	return msgs
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

	title := p.Text
	if len(title) > 60 {
		title = title[:60] + "..."
	}
	if title == "" {
		title = "Session"
	}

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

	// Build System Sections (Core Prompt + Skills Catalog + Meta Context + Optional Extensions)
	sysPrompt := "You are Nex, an autonomous software engineering agent running on a cross-platform Runner."
	if profiles, err := n.store.ListAgentProfiles(parentCtx); err == nil {
		for _, ap := range profiles {
			if ap.Slug == sessRec.AgentProfileSlug && ap.SystemPrompt != "" {
				sysPrompt = ap.SystemPrompt
				break
			}
		}
	}

	sections := []ai.SystemSection{
		{Category: agent.CatSystemPrompt, Content: sysPrompt, CacheControl: true},
	}

	// Inject synced Skills catalog summary
	if skills, err := n.store.ListSkills(parentCtx); err == nil && len(skills) > 0 {
		var sb string
		sb = "Available Skills (stored locally under " + filepath.Join(n.cfg.DataDir, "skills") + "):\n"
		for _, sk := range skills {
			if sk.Enabled {
				sb += fmt.Sprintf("- %s: %s (file: %s)\n", sk.Name, sk.Description, filepath.Join(n.cfg.DataDir, "skills", sk.Name, "SKILL.md"))
			}
		}
		sections = append(sections, ai.SystemSection{Category: agent.CatSkills, Content: sb})
	}

	// Inject Runner OS / Shell / Workspace Meta Context (and optional AGENTS.md)
	metaText := fmt.Sprintf("Runner ID: %s\nOS/Arch: %s/%s\nShell: %s\nWorkspace: %s\nDate: %s",
		n.cfg.RunnerID, runtime.GOOS, runtime.GOARCH, tools.DefaultShellName(), workDir, time.Now().Format("2006-01-02"))
	if agentsMD, err := os.ReadFile(filepath.Join(workDir, "AGENTS.md")); err == nil && len(agentsMD) > 0 {
		if len(agentsMD) > 16384 {
			agentsMD = agentsMD[:16384]
		}
		metaText += "\n\n# Workspace AGENTS.md\n" + string(agentsMD)
	}
	sections = append(sections, ai.SystemSection{Category: agent.CatMetaContext, Content: metaText})

	sessionTools := tools.DefaultCodingTools()
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

	loop := agent.NewLoop(agent.LoopConfig{
		SessionID:       sessRec.ID,
		RunnerID:        n.cfg.RunnerID,
		ProviderID:      provID,
		Model:           modelID,
		Provider:        prov,
		WorkDir:         workDir,
		SystemSections:  sections,
		ThinkingEffort:  p.ThinkingEffort,
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
	if p.Text != "" {
		blocks = append(blocks, ai.TextBlock(p.Text))
	}
	blocks = append(blocks, p.Images...)
	userMsg := ai.Message{Role: ai.RoleUser, Content: blocks}

	return loop.Run(runCtx, &userMsg)
}
