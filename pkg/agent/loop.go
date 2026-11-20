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
	ContextWindow      int
	Pricing            ai.ModelPricing
	PermissionMode     PermissionMode
	TriggerSource      string // "chat" | "subagent" | "cron"
	MaxRetries         int    // 0 defaults to 3; <0 disables retries
	RetryBaseDelay     time.Duration
	DisableAutoCompact bool
	Tools              []tools.Tool
	Approver           Approver
	Hooks              Hooks
	Store              *store.Store
	OnEvent            EventSink
}

// Loop implements the pi-agent-style stateful Agent Loop with Steer and FollowUp control queues.
type Loop struct {
	cfg      LoopConfig
	toolMap  map[string]tools.Tool
	toolDefs []ai.ToolDefinition

	mu        sync.Mutex
	emitMu    sync.Mutex
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
		l.emitMu.Lock()
		l.cfg.OnEvent(ev)
		l.emitMu.Unlock()
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
	maxRetries := l.cfg.MaxRetries
	if maxRetries == 0 {
		maxRetries = 3
	} else if maxRetries < 0 {
		maxRetries = 0
	}

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
		l.mu.Unlock()

		// Resolve SystemSections (allowing optional plugin SystemPromptHook mutations)
		activeSections := l.cfg.SystemSections
		if sph, ok := l.cfg.Hooks.(SystemPromptHook); ok && sph != nil {
			if modSecs, sErr := sph.OnSystemPrompt(ctx, activeSections); sErr == nil {
				activeSections = modSecs
			}
		}

		// Pre-flight Context Compaction (Tier 1: Microcompact >=65%, Tier 2: Auto-Compact >=85%)
		if !l.cfg.DisableAutoCompact && l.cfg.ContextWindow > 0 {
			l.maybePreFlightCompact(ctx, turnNo, activeSections)
		}

		l.emit(Event{Type: EventTurnStart, TurnIdx: turnNo})

		var assistantMsg *ai.Message
		var turnUsage ai.Usage
		var streamErr error
		var startTS, firstTokenTS time.Time
		var req *ai.StreamRequest
		compactedOnOverflow := false

	attemptTurn:
		for retryAttempt := 0; ; {
			l.mu.Lock()
			reqMsgs := make([]ai.Message, len(l.messages))
			copy(reqMsgs, l.messages)
			l.mu.Unlock()

			if l.cfg.Hooks != nil {
				modified, err := l.cfg.Hooks.OnContextBuild(ctx, reqMsgs)
				if err != nil {
					l.emit(Event{Type: EventTurnEnd, TurnIdx: turnNo, Error: err.Error()})
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

			req = &ai.StreamRequest{
				Model:           l.cfg.Model,
				SystemPrompt:    l.cfg.SystemPrompt,
				SystemSections:  activeSections,
				Messages:        reqMsgs,
				Tools:           activeTools,
				MaxOutputTokens: l.cfg.MaxOutputTokens,
				ThinkingEffort:  l.cfg.ThinkingEffort,
				PromptCacheKey:  l.cfg.SessionID,
			}

			startTS = time.Now()
			firstTokenTS = time.Time{}
			assistantMsg = nil
			turnUsage = ai.Usage{}
			streamErr = nil
			emittedAnyDelta := false

			streamCh, err := l.cfg.Provider.Stream(ctx, req)
			if err != nil {
				streamErr = err
			} else {
				l.emit(Event{Type: EventMessageStart, TurnIdx: turnNo})
				for sev := range streamCh {
					if firstTokenTS.IsZero() && (sev.Type == ai.StreamEventTextDelta || sev.Type == ai.StreamEventThinkingDelta || sev.Type == ai.StreamEventToolCallStart) {
						firstTokenTS = time.Now()
					}
					switch sev.Type {
					case ai.StreamEventTextDelta:
						emittedAnyDelta = true
						l.emit(Event{Type: EventMessageUpdate, TurnIdx: turnNo, DeltaKind: "text", Delta: sev.TextDelta})
					case ai.StreamEventThinkingDelta:
						emittedAnyDelta = true
						l.emit(Event{Type: EventMessageUpdate, TurnIdx: turnNo, DeltaKind: "thinking", Delta: sev.ThinkingDelta})
					case ai.StreamEventToolCallDelta:
						emittedAnyDelta = true
						l.emit(Event{Type: EventMessageUpdate, TurnIdx: turnNo, DeltaKind: "toolcall", ToolCallID: sev.ToolCallID, ToolName: sev.ToolName, Delta: sev.ArgsDelta})
					case ai.StreamEventDone:
						assistantMsg = sev.Message
						turnUsage = sev.Usage
					case ai.StreamEventError:
						streamErr = sev.Err
					}
				}
			}

			if streamErr == nil {
				break attemptTurn
			}

			// Overflow auto-recovery: compact history once and retry immediately!
			if !compactedOnOverflow && !l.cfg.DisableAutoCompact && IsContextOverflowError(streamErr) {
				compactedOnOverflow = true
				if l.forceCompact(ctx, turnNo, "context_overflow") {
					continue attemptTurn
				}
			}

			// Transient 429/5xx/network retry with exponential backoff (only if no partial tokens were streamed yet)
			if !emittedAnyDelta && retryAttempt < maxRetries && IsRetryableProviderError(streamErr) {
				delay := RetryDelay(retryAttempt, l.cfg.RetryBaseDelay)
				retryAttempt++
				if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
					l.emit(Event{Type: EventTurnEnd, TurnIdx: turnNo, Error: sleepErr.Error()})
					return lastAssistant, sleepErr
				}
				continue attemptTurn
			}

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

func (l *Loop) maybePreFlightCompact(ctx context.Context, turnNo int, activeSections []ai.SystemSection) {
	if l.cfg.ContextWindow <= 0 {
		return
	}
	l.mu.Lock()
	snapshot := make([]ai.Message, len(l.messages))
	copy(snapshot, l.messages)
	l.mu.Unlock()

	estTokens := EstimateContextTokens(activeSections, l.cfg.SystemPrompt, l.toolDefs, snapshot)
	ratio := float64(estTokens) / float64(l.cfg.ContextWindow)
	if ratio < microcompactThresholdPct {
		return
	}

	// Tier 1 (>=65%): zero-LLM microcompaction of bulky old tool outputs
	pruned, count := MicrocompactMessages(snapshot, microcompactKeepRecent)
	if count > 0 {
		l.mu.Lock()
		l.messages = pruned
		l.mu.Unlock()
		snapshot = pruned
		estTokens = EstimateContextTokens(activeSections, l.cfg.SystemPrompt, l.toolDefs, snapshot)
		ratio = float64(estTokens) / float64(l.cfg.ContextWindow)
	}

	// Tier 2 (>=85%): full LLM-backed compaction with deterministic fallback
	if ratio >= autoCompactThresholdPct {
		l.forceCompact(ctx, turnNo, "auto_compact")
	}
}

func (l *Loop) forceCompact(ctx context.Context, turnNo int, reason string) bool {
	l.mu.Lock()
	snapshot := make([]ai.Message, len(l.messages))
	copy(snapshot, l.messages)
	l.mu.Unlock()

	if len(snapshot) <= 2 {
		return false
	}

	compacted, summary, err := CompactMessages(ctx, l.cfg.Provider, l.cfg.Model, snapshot, "")
	if err != nil || len(compacted) == 0 {
		return false
	}
	// Check whether anything actually shrank (either message count or byte size)
	beforeTokens := EstimateContextTokens(nil, "", nil, snapshot)
	afterTokens := EstimateContextTokens(nil, "", nil, compacted)
	if len(compacted) >= len(snapshot) && afterTokens >= beforeTokens {
		return false
	}

	l.mu.Lock()
	l.messages = compacted
	l.mu.Unlock()

	cp := make([]ai.Message, len(compacted))
	copy(cp, compacted)
	var summaryMsg *ai.Message
	if len(cp) > 0 {
		m := cp[0]
		summaryMsg = &m
	}
	l.emit(Event{
		Type:              EventCompaction,
		TurnIdx:           turnNo,
		Reason:            reason,
		Delta:             summary,
		Message:           summaryMsg,
		CompactedMessages: cp,
	})
	return true
}

func (l *Loop) isCallReadOnly(call ai.ContentBlock) bool {
	toolImpl, exists := l.toolMap[call.ToolName]
	if !exists {
		return false
	}
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return toolImpl.IsReadOnly(args)
}

func (l *Loop) executeToolBatch(ctx context.Context, turnNo int, calls []ai.ContentBlock) ([]ai.Message, error) {
	var collectedSteers []ai.Message

	skipRemaining := func(from int) {
		for j := from; j < len(calls); j++ {
			c := calls[j]
			skipMsg := ai.NewToolResultMessage(c.ToolCallID, c.ToolName, "[Skipped: interrupted by user steering message]", true)
			l.appendMessage(skipMsg)
			skipRes := tools.Result{Output: skipMsg.PlainText(), IsError: true}
			l.emit(Event{
				Type:       EventToolExecutionEnd,
				TurnIdx:    turnNo,
				ToolCallID: c.ToolCallID,
				ToolName:   c.ToolName,
				ToolResult: &skipRes,
			})
		}
	}

	for i := 0; i < len(calls); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Check Steer queue BEFORE starting subsequent tool or parallel read-only group
		if i > 0 {
			if steers := l.popSteer(); len(steers) > 0 {
				collectedSteers = append(collectedSteers, steers...)
			}
		}
		if len(collectedSteers) > 0 {
			skipRemaining(i)
			break
		}

		// Group consecutive read-only tool calls for parallel execution
		if l.isCallReadOnly(calls[i]) {
			end := i + 1
			for end < len(calls) && l.isCallReadOnly(calls[end]) {
				end++
			}
			group := calls[i:end]
			if len(group) == 1 {
				res, err := l.executeSingleTool(ctx, turnNo, group[0])
				if err != nil && ctx.Err() != nil {
					return nil, err
				}
				if err != nil {
					res = tools.Result{Output: fmt.Sprintf("tool execution error: %v", err), IsError: true}
				}
				trMsg := ai.NewToolResultMessage(group[0].ToolCallID, group[0].ToolName, res.Output, res.IsError, res.ExtraBlocks...)
				l.appendMessage(trMsg)
			} else {
				type batchOutcome struct {
					res tools.Result
					err error
				}
				outcomes := make([]batchOutcome, len(group))
				var wg sync.WaitGroup
				for idx, c := range group {
					wg.Add(1)
					go func(slot int, blk ai.ContentBlock) {
						defer wg.Done()
						r, e := l.executeSingleTool(ctx, turnNo, blk)
						outcomes[slot] = batchOutcome{res: r, err: e}
					}(idx, c)
				}
				wg.Wait()
				for idx, out := range outcomes {
					if out.err != nil && ctx.Err() != nil {
						return nil, out.err
					}
					res := out.res
					if out.err != nil {
						res = tools.Result{Output: fmt.Sprintf("tool execution error: %v", out.err), IsError: true}
					}
					trMsg := ai.NewToolResultMessage(group[idx].ToolCallID, group[idx].ToolName, res.Output, res.IsError, res.ExtraBlocks...)
					l.appendMessage(trMsg)
				}
			}

			if steers := l.popSteer(); len(steers) > 0 {
				collectedSteers = append(collectedSteers, steers...)
			}
			i = end
			continue
		}

		// Mutating or unknown tool: run serially
		call := calls[i]
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
		i++
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

	// 0. Pre-execution JSON Schema Validation (catches malformed/missing arguments before hooks or approval prompts)
	if err := ValidateToolArguments(toolImpl.Definition().InputSchema, args); err != nil {
		res := tools.Result{Output: fmt.Sprintf("invalid arguments for %s: %v", call.ToolName, err), IsError: true}
		l.emit(Event{Type: EventToolExecutionEnd, TurnIdx: turnNo, ToolCallID: call.ToolCallID, ToolName: call.ToolName, ToolResult: &res})
		return res, nil
	}

	// 1. Plugin / Extension Pre-Tool Hook
	if l.cfg.Hooks != nil {
		dec, err := l.cfg.Hooks.OnToolCall(ctx, call.ToolName, args)
		if err != nil {
			res := tools.Result{Output: "hook error: " + err.Error(), IsError: true}
			l.emit(Event{Type: EventToolExecutionEnd, TurnIdx: turnNo, ToolCallID: call.ToolCallID, ToolName: call.ToolName, ToolResult: &res})
			return res, nil
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

	// 4. Hard Output Size Guard (prevents any single tool output from overflowing context)
	res.Output = GuardToolOutput(res.Output, DefaultMaxToolOutputBytes)

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
