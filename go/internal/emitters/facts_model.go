package emitters

import (
	"encoding/json"
	"net/url"

	"github.com/tylinux/nex/go/internal/projection"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// ── model.request.status ───────────────────────────────────────────────

func (t *Telemetry) factNetworkStatus(ev projection.Event, m map[string]json.RawMessage, turnID string, hasTurn bool, sourceCommandID string, hasSourceCommand bool) *objBuilder {
	status := mustString(m, "type")
	// 准入等待的两端不是 provider 请求状态，fact 枚举不收它们，显式跳过。
	if status == "model_request_queued" || status == "model_request_admitted" {
		return nil
	}
	providerID := mustString(m, "providerId")
	modelID := mustString(m, "modelId")
	t.modelBySession.set(t.sessionID, modelIdentity{modelName: modelID, modelProvider: providerID})

	o := t.base(ev, sourceCommandID, hasSourceCommand, turnID, hasTurn)
	o.set("kind", v4pub.Str("model.request.status"))
	requestID, _ := nonEmpty(m, "requestId")
	o.set("requestId", v4pub.Str(requestID))
	o.set("status", v4pub.Str(status))
	o.set("providerId", v4pub.Str(providerID))
	o.set("modelId", v4pub.Str(modelID))
	o.setStr(mustNonEmpty(m, "providerKind"), "providerKind", mustString(m, "providerKind"))
	if host := providerHostname(mustString(m, "baseURL")); host != "" {
		o.set("providerHostname", v4pub.Str(host))
	}
	o.setStr(mustNonEmpty(m, "transport"), "transport", mustString(m, "transport"))
	o.setStr(mustNonEmpty(m, "querySource"), "querySource", mustString(m, "querySource"))
	o.setStr(mustNonEmpty(m, "queryId"), "queryId", mustString(m, "queryId"))
	attempt, _ := integer(m, "attempt")
	o.set("attempt", v4pub.Int(attempt))
	maxAttempts, _ := integer(m, "maxAttempts")
	o.set("maxAttempts", v4pub.Int(maxAttempts))

	switch status {
	case "model_request_completed":
		o.number(true, "durationMs", mustNumber(m, "durationMs"))
	case "model_request_failed":
		o.setIf(hasNum(m, "durationMs"), "durationMs", v4pub.Float(mustNumber(m, "durationMs")))
		o.set("reason", v4pub.Str(mustString(m, "reason")))
		o.set("retryable", v4pub.Bool(mustBool(m, "retryable")))
		o.setIf(hasNum(m, "statusCode"), "statusCode", v4pub.Int(mustInt(m, "statusCode")))
	case "model_retry_scheduled":
		o.set("reason", v4pub.Str(mustString(m, "reason")))
		o.setIf(hasNum(m, "statusCode"), "statusCode", v4pub.Int(mustInt(m, "statusCode")))
		o.number(true, "delayMs", mustNumber(m, "delayMs"))
		o.integer(true, "nextAttempt", mustInt(m, "nextAttempt"))
	case "model_stream_stalled":
		o.number(true, "idleMs", mustNumber(m, "idleMs"))
		o.number(true, "timeoutMs", mustNumber(m, "timeoutMs"))
	}

	// 记录完成的请求身份，供后续 usage.delta 关联（只放行 step usage 来源）。
	if status == "model_request_completed" && isStepUsageQuerySourceOpt(querySourceOpt(m)) {
		key := modelRequestQueueKey(t.sessionID, querySourceOpt(m))
		queue := t.completedModelRequestsSlice(key)
		queue = append(queue, completedRequest{
			requestID:        requestID,
			providerID:       providerID,
			modelID:          modelID,
			providerKind:     mustString(m, "providerKind"),
			providerHostname: providerHostname(mustString(m, "baseURL")),
		})
		t.completedModelRequests.set(key, queue)
	}
	return o
}

// ── stream.chunk ───────────────────────────────────────────────────────

func (t *Telemetry) factStreamChunk(ev projection.Event, m map[string]json.RawMessage, turnID string, hasTurn bool, sourceCommandID string, hasSourceCommand bool) *objBuilder {
	kind := mustString(m, "kind")
	channel := ""
	switch kind {
	case "text_delta":
		channel = "text"
	case "reasoning_delta":
		channel = "thought"
	default:
		return nil
	}
	parent := streamingParentToolCallID(m)
	partID := mustString(m, "partId")
	streamKey := t.sessionID + "\x00" + turnID + "\x00" + channel + "\x00" + partID + "\x00" + parent

	o := t.base(ev, sourceCommandID, hasSourceCommand, turnID, hasTurn)
	o.set("kind", v4pub.Str("stream.chunk"))
	o.set("channel", v4pub.Str(channel))
	delta, _ := str(m, "delta")
	o.set("chunkLength", v4pub.Int(int64(jsLen(delta))))
	o.set("firstChunk", v4pub.Bool(t.firstStreamChunks.add(streamKey)))
	o.setStr(mustNonEmpty(m, "assistantMessageId"), "assistantMessageId", mustString(m, "assistantMessageId"))
	o.setStr(mustNonEmpty(m, "partId"), "partId", partID)
	o.setStr(parent != "", "parentToolCallId", parent)
	return o
}

// ── usage.delta (model_complete) ───────────────────────────────────────

func (t *Telemetry) factUsageDelta(ev projection.Event, m map[string]json.RawMessage, turnID string, hasTurn bool, sourceCommandID string, hasSourceCommand bool) *objBuilder {
	requestQueueKey := modelRequestQueueKey(t.sessionID, querySourceOpt(m))
	completed := t.dequeueCompletedRequest(requestQueueKey)

	qs := querySourceOpt(m)
	stopReason := mustString(m, "stopReason")
	if !isStepUsageModelComplete(qs, stopReason) {
		return nil
	}
	usage := record(m, "usage")
	o := t.base(ev, sourceCommandID, hasSourceCommand, turnID, hasTurn)
	o.set("kind", v4pub.Str("usage.delta"))
	if completed != nil {
		o.set("requestId", v4pub.Str(completed.requestID))
		o.set("providerId", v4pub.Str(completed.providerID))
		o.set("modelId", v4pub.Str(completed.modelID))
		o.setStr(completed.providerKind != "", "providerKind", completed.providerKind)
		o.setStr(completed.providerHostname != "", "providerHostname", completed.providerHostname)
	}
	inTok := nonNegOr0(usage, "inputTokens")
	outTok := nonNegOr0(usage, "outputTokens")
	o.set("inputTokens", numJSON(inTok))
	o.set("outputTokens", numJSON(outTok))
	o.set("totalTokens", numJSON(totalTokensOf(usage)))
	o.set("reasoningTokens", numJSON(nonNegOr0(usage, "reasoningTokens")))
	o.set("cacheReadTokens", numJSON(cacheReadTokensOf(usage)))
	o.set("cacheWriteTokens", numJSON(nonNegOr0(usage, "cacheWriteTokens")))
	return o
}

// ── turn.terminal (turn_complete) ──────────────────────────────────────

func (t *Telemetry) factTurnComplete(ev projection.Event, m map[string]json.RawMessage, turnID string, hasTurn bool, turnKey string, sourceCommandID string, hasSourceCommand bool) *objBuilder {
	directSourceCommandID := ""
	hasDirect := false
	if v, ok := nonEmpty(m, "inputId"); ok {
		directSourceCommandID = v
		hasDirect = true
	} else if hasSourceCommand {
		directSourceCommandID = sourceCommandID
		hasDirect = true
	}
	o := t.base(ev, directSourceCommandID, hasDirect, turnID, hasTurn)
	o.set("kind", v4pub.Str("turn.terminal"))
	resultType := mustString(m, "resultType")
	o.set("status", v4pub.Str(terminalStatus(resultType)))
	o.set("resultType", v4pub.Str(resultType))
	o.number(true, "durationMs", mustNumber(m, "durationMs"))
	o.number(true, "tokenCount", mustNumber(m, "tokenCount"))
	o.integer(true, "toolCallCount", mustInt(m, "toolCallCount"))
	if resultType == "cancelled" {
		o.set("errorCode", v4pub.Str("USER_INTERRUPT"))
		o.set("errorMessage", v4pub.Str("User stopped generation"))
	}
	if b, ok := boolField(m, "backgroundSubagentResultConsumed"); ok && b {
		o.set("backgroundSubagentResultConsumed", v4pub.Bool(true))
	}
	if b, ok := boolField(m, "workflowResultConsumed"); ok && b {
		o.set("workflowResultConsumed", v4pub.Bool(true))
	}
	t.clearTurn(turnKey)
	return o
}

func (t *Telemetry) clearTurn(turnKey string) {
	if turnKey == "" {
		return
	}
	t.sourceCommandByTurn.delete(turnKey)
	t.firstStreamChunks.deletePrefix(turnKey + "\x00")
	t.toolNameByCall.deletePrefix(turnKey + "\x00")
}

// ── shared helpers ─────────────────────────────────────────────────────

type modelIdentity struct {
	modelName     string
	modelProvider string
}

type completedRequest struct {
	requestID        string
	providerID       string
	modelID          string
	providerKind     string
	providerHostname string
}

func modelRequestQueueKey(sessionID, querySource string) string {
	return sessionID + "\x00" + querySource
}

func isStepUsageQuerySourceOpt(qs string) bool {
	// querySource === undefined || main_turn || subagent || workflow_child
	return qs == "" || qs == "main_turn" || qs == "subagent" || qs == "workflow_child"
}

func isStepUsageModelComplete(qs, stopReason string) bool {
	// isStepUsageQuerySource(qs) && (qs !== undefined || stopReason !== "tool_internal")
	if !isStepUsageQuerySourceOpt(qs) {
		return false
	}
	if qs != "" {
		return true
	}
	return stopReason != "tool_internal"
}

func terminalStatus(resultType string) string {
	switch resultType {
	case "success":
		return "success"
	case "cancelled":
		return "interrupted"
	default:
		return "failed"
	}
}

// providerHostname mirrors providerHostname(baseURL): the URL hostname, or "".
func providerHostname(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// streamingParentToolCallId mirrors streamingParentToolCallId(payload).
func streamingParentToolCallID(m map[string]json.RawMessage) string {
	meta := record(m, "_meta")
	nex := record(meta, "nex")
	for _, c := range []struct {
		src map[string]json.RawMessage
		key string
	}{
		{m, "parentToolCallId"},
		{m, "parentToolUseId"},
		{meta, "parentToolCallId"},
		{meta, "parentToolUseId"},
		{nex, "parentToolCallId"},
		{nex, "parentToolUseId"},
	} {
		if v, ok := nonEmpty(c.src, c.key); ok {
			return v
		}
	}
	return ""
}
