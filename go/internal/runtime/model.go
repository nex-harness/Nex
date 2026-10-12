package runtime

import (
	"context"
	"strings"

	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/modelreq"
	"github.com/tylinux/nex/go/internal/tooldefs"
)

// runModelStep performs one assistant model request. The client publishes its
// attempt statuses from its own goroutine while the turn loop consumes the
// buffered event channel, so the loop is the only mutator: non-terminal
// statuses are handed to it and it applies them; model_request_completed is
// buffered and applied after the channel is drained (the client can publish it
// before the loop has consumed the trailing events).
//
// A tool call is scheduled and started from inside the loop, before the step's
// model_request_completed status, matching the recorded order.
func (s *session) runModelStep(ctx context.Context, turnID, inputID string, history []modelreq.Message) *modelStep {
	// model_request.
	s.appendEvent(sessionEvent{
		Type: evModelRequest, TurnID: turnID, HasTurnID: true,
		Payload: mustJSON(map[string]any{"querySource": "main_turn", "inputId": inputID}),
	})

	assistantMessageID := s.rt.deps.IDs.MessageID()
	s.lastAssistantMessageID = assistantMessageID
	sink := newStatusSink(s, turnID, inputID)

	body, err := s.buildBody(history)
	if err != nil {
		return nil
	}
	req := modelclient.Request{URL: s.rt.deps.Provider.chatURL(), Body: body}
	client := modelclient.NewClient(s.rt.deps.ModelConfig, s.rt.deps.Transport, sink)
	handle := client.Stream(ctx, req)

	events := handle.Events()
	step := &modelStep{}
	var sb strings.Builder
	for events != nil {
		select {
		case item, ok := <-sink.handoff:
			if ok {
				s.appendNetworkStatus(turnID, item.payload)
				close(item.ack)
			}
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			s.applyStreamEvent(ctx, turnID, assistantMessageID, ev, step, &sb)
		}
	}
	for {
		select {
		case item := <-sink.handoff:
			s.appendNetworkStatus(turnID, item.payload)
			close(item.ack)
		default:
			goto drained
		}
	}
drained:
	if err := handle.Wait(); err != nil {
		return nil
	}
	// The streaming calls' tool_call_started lands after the stream's finish
	// event but before the request's completed status (see startStreamingTool).
	s.drainStreamedTools(ctx)
	s.flushPendingStarts(false)
	sink.flushCompleted()
	step.text = sb.String()
	step.finishReason = handle.FinishReason()
	step.toolCount = len(step.toolCalls)
	if u := handle.Usage(); u != nil {
		step.usage = usageFromClient(u)
	}
	return step
}

func (s *session) applyStreamEvent(ctx context.Context, turnID, assistantMessageID string, ev modelclient.Event, step *modelStep, sb *strings.Builder) {
	switch ev.Kind {
	case modelclient.EventStart:
		s.appendStreamEvent(turnID, assistantMessageID, "start", "")
	case modelclient.EventTextStart:
		s.appendStreamEvent(turnID, assistantMessageID, "text_start", "")
	case modelclient.EventTextDelta:
		sb.WriteString(ev.Text)
		s.appendStreamEvent(turnID, assistantMessageID, "text_delta", ev.Text)
	case modelclient.EventTextEnd:
		s.appendStreamEvent(turnID, assistantMessageID, "text_end", "")
	case modelclient.EventReasoningStart:
		s.appendStreamEvent(turnID, assistantMessageID, "reasoning_start", "")
	case modelclient.EventReasoningDelta:
		s.appendStreamEvent(turnID, assistantMessageID, "reasoning_delta", ev.Text)
	case modelclient.EventReasoningEnd:
		s.appendStreamEvent(turnID, assistantMessageID, "reasoning_end", "")
	case modelclient.EventToolInputStart:
		s.appendStreamEventTool(turnID, assistantMessageID, "tool_input_start", "", ev.ID, ev.ToolName)
	case modelclient.EventToolInputDelta:
		step.appendToolInputDelta(s, turnID, assistantMessageID, ev.ID, ev.Text)
	case modelclient.EventToolInputEnd:
		step.flushToolInputDelta(s, turnID, assistantMessageID, ev.ID)
		// accept() 不 await：上一个调用的 closed ledger / scheduled 在解析器下一次让出时
		// 运行，也就是本调用的输入增量已落盘、tool_input_end 尚未追加之间
		// （heldout/read-two-files：Node scheduled(c1)=14，先于 c2 的 tool_input_end）。
		s.drainStreamedTools(ctx)
		s.appendStreamEventCall(turnID, assistantMessageID, "tool_input_end", "", ev.ID)
	case modelclient.EventToolCall:
		// accept() is not awaited in Node, so the *previous* accepted call runs at
		// this yield, after this call's tool_call streaming event was appended.
		step.toolCalls = append(step.toolCalls, ev.ToolCall)
		s.appendStreamEventToolInput(turnID, assistantMessageID, "tool_call", ev.ToolCall.ID, orderedRaw(ev.ToolCall.Arguments, ev.ToolCall.Input))
		s.drainStreamedTools(ctx)
		// The previous call's tool_call_started lands after this call's tool_call
		// event but before this call's inputText row is finalised.
		s.flushPendingStarts(true)
		if streamableTool(ev.ToolCall.Name) {
			s.pendingStreamed = append(s.pendingStreamed, streamedCall{
				turnID: turnID, assistantMessageID: assistantMessageID,
				call: ev.ToolCall, index: len(step.toolCalls) - 1,
			})
		} else {
			s.deferredCalls = append(s.deferredCalls, ev.ToolCall)
		}
	case modelclient.EventFinish:
		// Node's accepted call runs at the yield that precedes the finish
		// event: its scheduled/queued/started ledgers land first, then finish.
		s.drainStreamedTools(ctx)
		s.appendStreamEvent(turnID, assistantMessageID, "finish", "")
	}
}

// appendNetworkStatus appends one model_network_status event.
//
// Frame boundaries in Node are purely timer-driven (30 ms flush window). The
// runtime drives its own timer, so it additionally flushes where the recorded
// frames show the timer reliably fired, and only there.
//
// Bug: this used to flush after model_request_started AND
// model_retry_scheduled. Neither matches Node: a status only reaches the
// publisher, and the frame boundary comes from the timer. Flushing per retry
// split one retry lifecycle into N frames, so N apiRetry patches each survived
// on their own frame (heldout 429-twice: Node has one apiRetry delta carrying
// attempt 2, Go had attempt 1 and attempt 2).
//
// Why flush on the FIRST started status and not on retry attempts:
//   - the first request of a model step is the one the recorded traces show the
//     frame closing on (S02/S03/S13 all end a frame at the started status);
//   - a retry's started status arrives milliseconds after the preceding
//     model_retry_scheduled, so Node's timer cannot separate them and the
//     apiRetry patches coalesce (rule 2) into the last writer's value.
//
// When the backoff is long enough for the real timer to fire (heldout
// 500-then-ok), the timer flushes it anyway; frame boundaries are timing noise
// that the normalizer flattens away.
func (s *session) appendNetworkStatus(turnID string, payload map[string]any) {
	s.appendEvent(sessionEvent{
		Type: evModelNetworkStatus, TurnID: turnID, HasTurnID: true,
		Payload: mustJSON(payload),
	})
	if payload["type"] == "model_request_started" && networkStatusAttempt(payload) <= 1 {
		s.rt.flushSubscribers(s)
	}
}

// networkStatusAttempt reads a status payload's attempt counter; a missing or
// non-positive value is attempt 1 (the first physical request).
func networkStatusAttempt(payload map[string]any) int {
	switch v := payload["attempt"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 1
	}
}

// appendStreamEvent appends one model_streaming event.
func (s *session) appendStreamEvent(turnID, assistantMessageID, kind, delta string) {
	s.appendStreamEventCall(turnID, assistantMessageID, kind, delta, "")
}

// appendStreamEventCall appends one model_streaming event carrying a toolCallId
// (required for the tool-input lifecycle events the projection keys rows on).
func (s *session) appendStreamEventCall(turnID, assistantMessageID, kind, delta, toolCallID string) {
	s.appendStreamEventTool(turnID, assistantMessageID, kind, delta, toolCallID, "")
}

// appendStreamEventToolInput appends the tool_call model_streaming event, which
// carries the parsed tool input the projection stores on the row.
func (s *session) appendStreamEventToolInput(turnID, assistantMessageID, kind, toolCallID string, input any) {
	payload := map[string]any{
		"assistantMessageId": assistantMessageID,
		"kind":               kind,
		"delta":              "",
		"done":               false,
		"toolCallId":         toolCallID,
		"input":              input,
	}
	s.appendEvent(sessionEvent{Type: evModelStreaming, TurnID: turnID, HasTurnID: true, Payload: mustJSON(payload)})
}

// appendStreamEventTool appends a model_streaming event carrying a toolCallId
// and, for tool_input_start, the tool name.
func (s *session) appendStreamEventTool(turnID, assistantMessageID, kind, delta, toolCallID, toolName string) {
	payload := map[string]any{
		"assistantMessageId": assistantMessageID,
		"kind":               kind,
		"delta":              delta,
		"done":               false,
	}
	if toolCallID != "" {
		payload["toolCallId"] = toolCallID
	}
	if toolName != "" {
		payload["toolName"] = toolName
	}
	s.appendEvent(sessionEvent{Type: evModelStreaming, TurnID: turnID, HasTurnID: true, Payload: mustJSON(payload)})
}

func usageFromClient(u *modelclient.Usage) modelUsage {
	var out modelUsage
	if u.PromptTokens != nil {
		out.InputTokens = *u.PromptTokens
	}
	if u.CompletionTokens != nil {
		out.OutputTokens = *u.CompletionTokens
	}
	if u.ReasoningTokens != nil {
		out.ReasoningTokens = *u.ReasoningTokens
	}
	if u.CachedTokens != nil {
		out.CacheReadTokens = *u.CachedTokens
	}
	return out
}

// buildBody builds the request body: the prompt context, the history, the 20
// default tools and the max output tokens. The shape depends on the provider
// dialect: chat-completions and openai-responses carry the same conversation
// but in different envelopes (messages vs instructions+input), so the build
// switches on ProviderKind.
func (s *session) buildBody(history []modelreq.Message) ([]byte, error) {
	tools, err := tooldefs.Default()
	if err != nil {
		return nil, err
	}
	defs := make([]tooldefs.Def, len(tools))
	copy(defs, tools)
	if s.rt.deps.Provider.ProviderKind == "openai-responses" {
		return buildBodyResponses(s.rt.deps.Provider, s.promptConfig(), s.firstText, history, defs)
	}
	if len(history) == 0 {
		history = []modelreq.Message{{Role: "user", Content: s.firstText}}
	}
	return modelreq.Build(modelreq.Input{
		ModelID:         s.rt.deps.Provider.ModelID,
		Context:         s.promptConfig(),
		History:         history,
		MaxOutputTokens: int(s.rt.deps.Provider.MaxOutputToken),
		Tools:           defs,
	})
}

func boolPtr(b bool) *bool { return &b }

// chatURL is baseURL + "/chat/completions".
func (p Provider) chatURL() string {
	return strings.TrimRight(p.BaseURL, "/") + "/chat/completions"
}

// statusSink turns modelclient.StatusEvents into model_network_status events.
type statusSink struct {
	s           *session
	turnID      string
	queryID     string
	handoff     chan statusItem
	completd    map[string]any
	hasDone     bool
	querySource string
	transport   string
}

type statusItem struct {
	payload map[string]any
	ack     chan struct{}
}

func newStatusSink(s *session, turnID, queryID string) *statusSink {
	return &statusSink{s: s, turnID: turnID, queryID: queryID, handoff: make(chan statusItem, 8)}
}

func (k *statusSink) Publish(ev modelclient.StatusEvent) {
	payload := k.payloadFor(ev)
	if ev.Status == "model_request_completed" {
		k.completd = payload
		k.hasDone = true
		return
	}
	ack := make(chan struct{})
	k.handoff <- statusItem{payload: payload, ack: ack}
	<-ack
}

func (k *statusSink) flushCompleted() {
	if k.hasDone {
		k.s.appendNetworkStatus(k.turnID, k.completd)
		k.hasDone = false
	}
}

func (k *statusSink) payloadFor(ev modelclient.StatusEvent) map[string]any {
	providerID, modelID, providerKind := ev.ProviderID, ev.ModelID, ev.ProviderKind
	if providerID == "" {
		providerID = k.s.rt.deps.Provider.ProviderID
	}
	if modelID == "" {
		modelID = k.s.rt.deps.Provider.ModelID
	}
	if providerKind == "" {
		providerKind = k.s.rt.deps.Provider.ProviderKind
	}
	payload := map[string]any{
		"type":         ev.Status,
		"requestId":    ev.RequestID,
		"providerId":   providerID,
		"modelId":      modelID,
		"providerKind": providerKind,
		"baseURL":      k.s.rt.deps.Provider.BaseURL,
		"transport":    ev.Transport,
		"querySource":  "main_turn",
		"queryId":      k.queryID,
		"attempt":      ev.Attempt,
		"maxAttempts":  ev.MaxAttempts,
	}
	switch ev.Status {
	case "model_request_completed":
		payload["durationMs"] = derefInt(ev.DurationMs)
	case "model_request_failed":
		payload["durationMs"] = derefInt(ev.DurationMs)
		payload["reason"] = ev.Reason
		payload["retryable"] = ev.Retryable
		if ev.StatusCode != nil {
			payload["statusCode"] = *ev.StatusCode
		}
	case "model_retry_scheduled":
		payload["reason"] = ev.Reason
		payload["delayMs"] = derefInt(ev.DelayMs)
		payload["nextAttempt"] = ev.NextAttempt
		if ev.StatusCode != nil {
			payload["statusCode"] = *ev.StatusCode
		}
	}
	return payload
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// emittersContextUsage builds the contextUsageBreakdown for a turn.
func emittersContextUsage(s *session, history []modelreq.Message) []map[string]any {
	items := contextBreakdown(s, history)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{"source": it.Source, "chars": it.Chars})
	}
	return out
}
