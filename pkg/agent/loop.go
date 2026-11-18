package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/store"
)

// LoopConfig configures an Agent Loop instance for a session.
type LoopConfig struct {
	SessionID       string
	RunnerID        string
	ProviderID      string
	Model           string
	Provider        ai.Provider
	WorkDir         string
	SystemPrompt    string
	SystemSections  []ai.SystemSection
	ThinkingEffort  ai.ThinkingEffort
	MaxOutputTokens int
	MaxTurns        int
	ContextWindow   int
	Pricing         ai.ModelPricing
	PermissionMode  PermissionMode
	TriggerSource   string // "chat" | "subagent" | "cron"
	Tools           []tools.Tool
	Approver        Approver
	Hooks           Hooks
	Store           *store.Store
	OnEvent         EventSink
}

// Loop implements the pi-agent-style stateful Agent Loop with Steer and FollowUp control queues.
type Loop struct {
	cfg      LoopConfig
	toolMap  map[string]tools.Tool
	toolDefs []ai.ToolDefinition

	mu        sync.Mutex
	messages  []ai.Message
	steerQ    []ai.Message
	followUpQ []ai.Message
	turnIdx   int
	running   bool
}

// NewLoop constructs an Agent Loop from cfg and optional initial conversation history.
func NewLoop(cfg LoopConfig, history []ai.Message) *Loop {
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 40
	}
	if cfg.PermissionMode == "" {
		cfg.PermissionMode = PermissionFullAccess
	}
	if cfg.TriggerSource == "" {
		cfg.TriggerSource = "chat"
	}

	tm := make(map[string]tools.Tool, len(cfg.Tools))
	defs := make([]ai.ToolDefinition, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		tm[t.Name()] = t
		td := t.Definition()
		if td.Category == "" {
			td.Category = t.Category()
		}
		defs = append(defs, td)
	}

	msgs := make([]ai.Message, len(history))
	copy(msgs, history)

	return &Loop{
		cfg:      cfg,
		toolMap:  tm,
		toolDefs: defs,
		messages: msgs,
	}
}

// Steer enqueues an immediate steering message that interrupts remaining queued tool calls in the current turn.
func (l *Loop) Steer(msg ai.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steerQ = append(l.steerQ, msg)
}

// FollowUp enqueues a follow-up user message processed automatically when the agent finishes its current response.
func (l *Loop) FollowUp(msg ai.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.followUpQ = append(l.followUpQ, msg)
}

// Messages returns a snapshot copy of the conversation history.
func (l *Loop) Messages() []ai.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := make([]ai.Message, len(l.messages))
	copy(cp, l.messages)
	return cp
}

func (l *Loop) popSteer() []ai.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.steerQ) == 0 {
		return nil
	}
	out := l.steerQ
	l.steerQ = nil
	return out
}

func (l *Loop) popFollowUp() []ai.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.followUpQ) == 0 {
		return nil
	}
	out := l.followUpQ
	l.followUpQ = nil
	return out
}

func (l *Loop) appendMessage(m ai.Message) {
	if m.Timestamp == 0 {
		m.Timestamp = time.Now().UnixMilli()
	}
	l.mu.Lock()
	l.messages = append(l.messages, m)
	l.mu.Unlock()
}

func (l *Loop) emit(ev Event) {
	if l.cfg.OnEvent != nil {
		l.cfg.OnEvent(ev)
	}
}

// Run appends promptMsg (if non-nil) and drives the multi-turn tool & queue state machine until completion or ctx cancel.
func (l *Loop) Run(ctx context.Context, promptMsg *ai.Message) (*ai.Message, error) {
	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		return nil, errors.New("agent: loop is already running")
	}
	l.running = true
	l.mu.Unlock()

	defer func() {
		l.mu.Lock()
		l.running = false
		l.mu.Unlock()
	}()

	l.emit(Event{Type: EventAgentStart})
	defer l.emit(Event{Type: EventAgentEnd})

	if promptMsg != nil {
		m := *promptMsg
		l.appendMessage(m)
		l.emit(Event{Type: EventMessageEnd, Message: &m})
	}

	var lastAssistant *ai.Message
	turnsThisRun := 0

	for {
		if err := ctx.Err(); err != nil {
			return lastAssistant, err
		}

		// Drain any pending Steer messages before starting next LLM turn
		for _, sm := range l.popSteer() {
			l.appendMessage(sm)
			smCopy := sm
			l.emit(Event{Type: EventMessageEnd, Message: &smCopy})
		}

		turnsThisRun++
		l.mu.Lock()
		l.turnIdx++
		turnNo := l.turnIdx
		reqMsgs := make([]ai.Message, len(l.messages))
		copy(reqMsgs, l.messages)
		l.mu.Unlock()

		if l.cfg.Hooks != nil {
			modified, err := l.cfg.Hooks.OnContextBuild(ctx, reqMsgs)
			if err != nil {
				return lastAssistant, err
			}
			reqMsgs = modified
		}

		// Inject final wrap-up notice if hitting MaxTurns ceiling
		activeTools := l.toolDefs
		if turnsThisRun >= l.cfg.MaxTurns {
			activeTools = nil
			reqMsgs = append(reqMsgs, ai.NewUserTextMessage("[System notice: maximum turn budget reached; summarize findings now without further tool calls.]"))
		}

		l.emit(Event{Type: EventTurnStart, TurnIdx: turnNo})

		req := &ai.StreamRequest{
			Model:           l.cfg.Model,
			SystemPrompt:    l.cfg.SystemPrompt,
			SystemSections:  l.cfg.SystemSections,
			Messages:        reqMsgs,
			Tools:           activeTools,
			MaxOutputTokens: l.cfg.MaxOutputTokens,
			ThinkingEffort:  l.cfg.ThinkingEffort,
			PromptCacheKey:  l.cfg.SessionID,
		}

		startTS := time.Now()
		var firstTokenTS time.Time

		streamCh, err := l.cfg.Provider.Stream(ctx, req)
		if err != nil {
			l.emit(Event{Type: EventTurnEnd, TurnIdx: turnNo, Error: err.Error()})
			return lastAssistant, err
		}

		l.emit(Event{Type: EventMessageStart, TurnIdx: turnNo})

		var assistantMsg *ai.Message
		var turnUsage ai.Usage
		var streamErr error

		for sev := range streamCh {
			if firstTokenTS.IsZero() && (sev.Type == ai.StreamEventTextDelta || sev.Type == ai.StreamEventThinkingDelta || sev.Type == ai.StreamEventToolCallStart) {
				firstTokenTS = time.Now()
			}
			switch sev.Type {
			case ai.StreamEventTextDelta:
				l.emit(Event{Type: EventMessageUpdate, TurnIdx: turnNo, DeltaKind: "text", Delta: sev.TextDelta})
			case ai.StreamEventThinkingDelta:
				l.emit(Event{Type: EventMessageUpdate, TurnIdx: turnNo, DeltaKind: "thinking", Delta: sev.ThinkingDelta})
			case ai.StreamEventToolCallDelta:
				l.emit(Event{Type: EventMessageUpdate, TurnIdx: turnNo, DeltaKind: "toolcall", ToolCallID: sev.ToolCallID, ToolName: sev.ToolName, Delta: sev.ArgsDelta})
			case ai.StreamEventDone:
				assistantMsg = sev.Message
				turnUsage = sev.Usage
			case ai.StreamEventError:
				streamErr = sev.Err
			}
		}

		if streamErr != nil {
			l.emit(Event{Type: EventTurnEnd, TurnIdx: turnNo, Error: streamErr.Error()})
			return lastAssistant, streamErr
		}
		if assistantMsg == nil {
			assistantMsg = &ai.Message{Role: ai.RoleAssistant}
		}

		l.appendMessage(*assistantMsg)
		lastAssistant = assistantMsg
		l.emit(Event{Type: EventMessageEnd, TurnIdx: turnNo, Message: assistantMsg})

		// Compute 6-category Context Windows breakdown & record telemetry
		var ttftMS int64
		if !firstTokenTS.IsZero() {
			ttftMS = firstTokenTS.Sub(startTS).Milliseconds()
		}
		durMS := time.Since(startTS).Milliseconds()

		tel := BuildTurnTelemetry(ctx, l.cfg.Store, TurnMeta{
			SessionID:     l.cfg.SessionID,
			TurnIdx:       turnNo,
			RunnerID:      l.cfg.RunnerID,
			ProviderID:    l.cfg.ProviderID,
			TriggerSource: l.cfg.TriggerSource,
			ContextWindow: l.cfg.ContextWindow,
			Pricing:       l.cfg.Pricing,
		}, req, assistantMsg, turnUsage, ttftMS, durMS)

		if l.cfg.Store != nil && l.cfg.SessionID != "" {
			_ = l.cfg.Store.RecordTurnTelemetry(ctx, tel)
		}

		// Extract requested tool calls from assistant message
		var toolCalls []ai.ContentBlock
		for _, b := range assistantMsg.Content {
			if b.Type == ai.BlockToolCall {
				toolCalls = append(toolCalls, b)
			}
		}

		if len(toolCalls) > 0 {
			steeredMsgs, execErr := l.executeToolBatch(ctx, turnNo, toolCalls)
			l.emit(Event{Type: EventTurnEnd, TurnIdx: turnNo, Telemetry: &tel})
			if execErr != nil {
				return lastAssistant, execErr
			}
			for _, sm := range steeredMsgs {
				l.appendMessage(sm)
				smCopy := sm
				l.emit(Event{Type: EventMessageEnd, TurnIdx: turnNo, Message: &smCopy})
			}
			// Continue loop so model processes tool results (and any steer messages)
			continue
		}

		l.emit(Event{Type: EventTurnEnd, TurnIdx: turnNo, Telemetry: &tel})

		// Check Steer queue first, then FollowUp queue before ending the run
		if steers := l.popSteer(); len(steers) > 0 {
			for _, sm := range steers {
				l.appendMessage(sm)
				smCopy := sm
				l.emit(Event{Type: EventMessageEnd, Message: &smCopy})
			}
			continue
		}
		if followUps := l.popFollowUp(); len(followUps) > 0 {
			for _, fm := range followUps {
				l.appendMessage(fm)
				fmCopy := fm
				l.emit(Event{Type: EventMessageEnd, Message: &fmCopy})
			}
			continue
		}

		return lastAssistant, nil
	}
}

func (l *Loop) executeToolBatch(ctx context.Context, turnNo int, calls []ai.ContentBlock) ([]ai.Message, error) {
	var collectedSteers []ai.Message

	for i, call := range calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Check Steer queue BEFORE starting subsequent tool in batch (pi-agent interrupt semantics)
		if i > 0 {
			if steers := l.popSteer(); len(steers) > 0 {
				collectedSteers = append(collectedSteers, steers...)
			}
		}
		if len(collectedSteers) > 0 {
			skipMsg := ai.NewToolResultMessage(call.ToolCallID, call.ToolName, "[Skipped: interrupted by user steering message]", true)
			l.appendMessage(skipMsg)
			skipRes := tools.Result{Output: skipMsg.PlainText(), IsError: true}
			l.emit(Event{
				Type:       EventToolExecutionEnd,
				TurnIdx:    turnNo,
				ToolCallID: call.ToolCallID,
				ToolName:   call.ToolName,
				ToolResult: &skipRes,
			})
			continue
		}

		res, err := l.executeSingleTool(ctx, turnNo, call)
		if err != nil && ctx.Err() != nil {
			return nil, err
		}
		if err != nil {
			res = tools.Result{Output: fmt.Sprintf("tool execution error: %v", err), IsError: true}
		}

		trMsg := ai.NewToolResultMessage(call.ToolCallID, call.ToolName, res.Output, res.IsError, res.ExtraBlocks...)
		l.appendMessage(trMsg)

		// Also check if a Steer arrived WHILE this tool was running
		if steers := l.popSteer(); len(steers) > 0 {
			collectedSteers = append(collectedSteers, steers...)
		}
	}

	return collectedSteers, nil
}

func (l *Loop) executeSingleTool(ctx context.Context, turnNo int, call ai.ContentBlock) (tools.Result, error) {
	toolImpl, exists := l.toolMap[call.ToolName]
	if !exists {
		res := tools.Result{Output: fmt.Sprintf("unknown tool %q", call.ToolName), IsError: true}
		l.emit(Event{Type: EventToolExecutionEnd, TurnIdx: turnNo, ToolCallID: call.ToolCallID, ToolName: call.ToolName, ToolResult: &res})
		return res, nil
	}

	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}

	// 1. Plugin / Extension Pre-Tool Hook
	if l.cfg.Hooks != nil {
		dec, err := l.cfg.Hooks.OnToolCall(ctx, call.ToolName, args)
		if err != nil {
			return tools.Result{Output: "hook error: " + err.Error(), IsError: true}, nil
		}
		if dec.Block {
			reason := dec.Reason
			if reason == "" {
				reason = "blocked by extension policy"
			}
			res := tools.Result{Output: "Tool execution blocked: " + reason, IsError: true}
			l.emit(Event{Type: EventToolExecutionEnd, TurnIdx: turnNo, ToolCallID: call.ToolCallID, ToolName: call.ToolName, ToolResult: &res})
			return res, nil
		}
		if len(dec.UpdatedArgs) > 0 {
			args = dec.UpdatedArgs
		}
	}

	// 2. Permission Guard Check (Online Interactive vs Headless Autonomous)
	if !toolImpl.IsReadOnly(args) {
		switch l.cfg.PermissionMode {
		case PermissionReadOnly:
			res := tools.Result{Output: "permission denied: session is in read-only mode", IsError: true}
			l.emit(Event{Type: EventToolExecutionEnd, TurnIdx: turnNo, ToolCallID: call.ToolCallID, ToolName: call.ToolName, ToolResult: &res})
			return res, nil

		case PermissionAutonomous:
			// Headless / offline mode: if no Approver is wired, non-allowlisted writes are denied without hanging forever!
			if l.cfg.Approver != nil {
				approved, reason, err := l.cfg.Approver.RequestApproval(ctx, ApprovalRequest{
					ApprovalID: "apr_" + uuid.NewString(),
					SessionID:  l.cfg.SessionID,
					ToolCallID: call.ToolCallID,
					ToolName:   call.ToolName,
					Arguments:  args,
				})
				if err != nil || !approved {
					if reason == "" {
						reason = "permission denied (headless mode)"
					}
					res := tools.Result{Output: reason, IsError: true}
					l.emit(Event{Type: EventToolExecutionEnd, TurnIdx: turnNo, ToolCallID: call.ToolCallID, ToolName: call.ToolName, ToolResult: &res})
					return res, nil
				}
			}

		case PermissionInteractive:
			if l.cfg.Approver == nil {
				res := tools.Result{Output: "permission denied: interactive approval required but no UI client connected", IsError: true}
				l.emit(Event{Type: EventToolExecutionEnd, TurnIdx: turnNo, ToolCallID: call.ToolCallID, ToolName: call.ToolName, ToolResult: &res})
				return res, nil
			}
			aprID := "apr_" + uuid.NewString()
			l.emit(Event{
				Type:       EventApprovalRequested,
				TurnIdx:    turnNo,
				ApprovalID: aprID,
				ToolCallID: call.ToolCallID,
				ToolName:   call.ToolName,
				ToolArgs:   args,
			})
			approved, reason, err := l.cfg.Approver.RequestApproval(ctx, ApprovalRequest{
				ApprovalID: aprID,
				SessionID:  l.cfg.SessionID,
				ToolCallID: call.ToolCallID,
				ToolName:   call.ToolName,
				Arguments:  args,
			})
			l.emit(Event{
				Type:       EventApprovalResolved,
				TurnIdx:    turnNo,
				ApprovalID: aprID,
				ToolCallID: call.ToolCallID,
				ToolName:   call.ToolName,
				Approved:   &approved,
				Reason:     reason,
			})
			if err != nil {
				return tools.Result{}, err
			}
			if !approved {
				if reason == "" {
					reason = "user rejected tool execution"
				}
				res := tools.Result{Output: "Permission denied: " + reason, IsError: true}
				l.emit(Event{Type: EventToolExecutionEnd, TurnIdx: turnNo, ToolCallID: call.ToolCallID, ToolName: call.ToolName, ToolResult: &res})
				return res, nil
			}
		}
	}

	l.emit(Event{
		Type:       EventToolExecutionStart,
		TurnIdx:    turnNo,
		ToolCallID: call.ToolCallID,
		ToolName:   call.ToolName,
		ToolArgs:   args,
	})

	res, err := toolImpl.Execute(ctx, l.cfg.WorkDir, args, func(delta string) {
		l.emit(Event{
			Type:       EventToolExecutionUpdate,
			TurnIdx:    turnNo,
			ToolCallID: call.ToolCallID,
			ToolName:   call.ToolName,
			Delta:      delta,
		})
	})

	// 3. Plugin / Extension Post-Tool Hook
	if err == nil && l.cfg.Hooks != nil {
		mut, hErr := l.cfg.Hooks.OnToolResult(ctx, call.ToolName, args, res)
		if hErr == nil {
			if mut.ReplaceOutput != nil {
				res.Output = *mut.ReplaceOutput
			}
			if mut.AppendText != "" {
				res.Output += mut.AppendText
			}
		}
	}

	resCopy := res
	l.emit(Event{
		Type:       EventToolExecutionEnd,
		TurnIdx:    turnNo,
		ToolCallID: call.ToolCallID,
		ToolName:   call.ToolName,
		ToolResult: &resCopy,
	})
	return res, err
}
