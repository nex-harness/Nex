package projection

import (
	"encoding/json"
)

// This file ports the model-complete and model-network handlers.

// onModelNetworkStatus ports onModelNetworkStatus.
func (p *Projection) onModelNetworkStatus(event Event, m map[string]json.RawMessage) []Delta {
	if !p.acceptsActiveModelEvent(event) {
		return nil
	}
	statusType, _ := payloadString(m, "type")
	switch statusType {
	case "model_retry_scheduled":
		attempt := positiveInteger(payloadInt64(m, "attempt"), 1)
		maxAttempts := payloadInt64(m, "maxAttempts")
		if maxAttempts < attempt+1 {
			maxAttempts = attempt + 1
		}
		reason, _ := payloadString(m, "reason")
		return p.setAPIRetry(newObj().
			set("attempt", attempt).
			set("maxAttempts", maxAttempts).
			set("nextRetryAt", event.TimestampMs+payloadInt64(m, "delayMs")).
			set("reasonCode", modelRetryReasonCode(reason)))
	case "model_request_completed":
		return p.setAPIRetry(nil)
	case "model_request_failed":
		if ok, _ := payloadBool(m, "retryable"); ok {
			return nil
		}
		return p.setAPIRetry(nil)
	case "model_request_started":
		if payloadInt64(m, "attempt") <= 1 {
			return p.setAPIRetry(nil)
		}
		return nil
	default:
		return nil
	}
}

// acceptsActiveModelEvent ports acceptsActiveModelEvent.
func (p *Projection) acceptsActiveModelEvent(event Event) bool {
	if !p.isRunning() {
		return false
	}
	if !p.hasCurrentTurn || !event.HasTurnID {
		return true
	}
	return event.TurnID == p.currentTurnID
}

// setAPIRetry ports setApiRetry: a no-op when apiRetry is unchanged.
func (p *Projection) setAPIRetry(apiRetry *obj) []Delta {
	set := func(current any) bool {
		currentObj, _ := current.(*obj)
		if currentObj == nil && apiRetry == nil {
			return true
		}
		if currentObj == nil || apiRetry == nil {
			return false
		}
		for _, key := range []string{"attempt", "maxAttempts", "nextRetryAt", "reasonCode"} {
			a, _ := marshalValue(currentObj.get(key))
			b, _ := marshalValue(apiRetry.get(key))
			if string(a) != string(b) {
				return false
			}
		}
		return true
	}
	if set(p.controlObj().get("apiRetry")) {
		return nil
	}
	var value any
	if apiRetry == nil {
		value = nil
	} else {
		value = apiRetry
	}
	return []Delta{newStateUpdated(p.controlPatch(newObj().set("apiRetry", value), nil, nil, false, false))}
}

// modelRetryReasonCode ports modelRetryReasonCode.
func modelRetryReasonCode(reason string) string {
	switch reason {
	case "rate_limited", "offpeak_queued":
		return "fault.provider.rateLimited"
	case "provider_overloaded", "server_error":
		return "fault.provider.serverError"
	case "timeout":
		return "fault.network.timeout"
	case "stream_idle_timeout":
		return "fault.network.sseStalled"
	case "stale_connection":
		return "fault.network.sseDisconnected"
	case "network_error":
		return "fault.network.unreachable"
	default:
		return "fault.provider.requestFailed"
	}
}

// onModelComplete ports onModelComplete (main-turn path).
func (p *Projection) onModelComplete(event Event, m map[string]json.RawMessage) []Delta {
	retryClear := []Delta{}
	if p.acceptsActiveModelEvent(event) {
		retryClear = p.setAPIRetry(nil)
	}
	querySource, hasQuerySource := payloadString(m, "querySource")
	stopReason, _ := payloadString(m, "stopReason")
	isMainTurn := true
	if hasQuerySource {
		isMainTurn = querySource == "main_turn"
	} else {
		isMainTurn = stopReason != "tool_internal"
	}
	if isMainTurn {
		p.outputContinuationRowID = nil
	}

	deltas := []Delta{}

	// fileChanges summary on the turn header (main turn or subagent).
	supportsFileChanges := isMainTurn || querySource == "subagent"
	if supportsFileChanges {
		if fc, ok := m["fileChanges"]; ok && len(fc) > 0 && string(fc) != "null" {
			var fileChanges struct {
				Additions int64 `json:"additions"`
				Deletions int64 `json:"deletions"`
				Files     int64 `json:"files"`
			}
			if err := json.Unmarshal(fc, &fileChanges); err == nil && fileChanges.Files > 0 {
				if header := p.turnHeaderForEvent(event); header != nil {
					next := shallowClone(header).put("fileChanges", newObj().
						set("additions", fileChanges.Additions).
						set("deletions", fileChanges.Deletions).
						set("files", fileChanges.Files).
						set("state", "active"))
					deltas = append(deltas, newRowUpserted(next))
				}
			}
		}
	}
	if !isMainTurn {
		return append(deltas, retryClear...)
	}

	usedTokens := getModelUsageContextTokens(m)
	p.contextUsedTokens = usedTokens
	maxTokens := p.contextMaxTokens
	if n, ok := payloadInt(m, "contextWindow"); ok {
		v := n
		maxTokens = &v
	}
	usage := m["usage"]
	var usageObj struct {
		InputTokens      int64 `json:"inputTokens"`
		OutputTokens     int64 `json:"outputTokens"`
		CacheReadTokens  int64 `json:"cacheReadTokens"`
		CacheWriteTokens int64 `json:"cacheWriteTokens"`
	}
	if len(usage) > 0 {
		_ = json.Unmarshal(usage, &usageObj)
	}
	prevUsage := p.snapshot.get("usage").(*obj)
	cumulative := prevUsage.get("cumulative").(*obj)

	var contextWindow any
	if maxTokens == nil {
		contextWindow = nil
	} else {
		cw := newObj().
			set("usedTokens", usedTokens).
			set("maxTokens", *maxTokens)
		autoCompact := any(nil)
		if prevCW, ok := prevUsage.get("contextWindow").(*obj); ok && prevCW != nil {
			autoCompact = prevCW.get("autoCompactThresholdTokens")
		}
		cw.set("autoCompactThresholdTokens", autoCompact)
		if cache, ok := m["cacheHit"]; ok && len(cache) > 0 && string(cache) != "null" {
			cw.set("cache", compactRaw(cache))
		}
		if bd, ok := m["contextUsageBreakdown"]; ok && len(bd) > 0 && string(bd) != "null" {
			var items []any
			if err := json.Unmarshal(bd, &items); err == nil && len(items) > 0 {
				cw.set("breakdown", compactRaw(bd))
			}
		}
		contextWindow = cw
	}
	deltas = append(deltas, newStateUpdated(newObj().set("usage", newObj().
		set("contextWindow", contextWindow).
		set("cumulative", newObj().
			set("inputTokens", int64Of(cumulative.get("inputTokens"))+usageObj.InputTokens).
			set("outputTokens", int64Of(cumulative.get("outputTokens"))+usageObj.OutputTokens).
			set("cacheReadTokens", int64Of(cumulative.get("cacheReadTokens"))+usageObj.CacheReadTokens).
			set("cacheWriteTokens", int64Of(cumulative.get("cacheWriteTokens"))+usageObj.CacheWriteTokens)))))
	deltas = append(deltas, retryClear...)
	return deltas
}

// getModelUsageContextTokens ports getModelUsageContextTokens: the model usage's
// context token count, defaulting to 0.
func getModelUsageContextTokens(m map[string]json.RawMessage) int64 {
	usage, ok := m["usage"]
	if !ok || len(usage) == 0 {
		return 0
	}
	var u struct {
		InputTokens      int64  `json:"inputTokens"`
		OutputTokens     int64  `json:"outputTokens"`
		CacheReadTokens  int64  `json:"cacheReadTokens"`
		CacheWriteTokens int64  `json:"cacheWriteTokens"`
		TotalTokens      *int64 `json:"totalTokens"`
	}
	if err := json.Unmarshal(usage, &u); err != nil {
		return 0
	}
	if u.TotalTokens != nil {
		return *u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens
}

func payloadInt64(m map[string]json.RawMessage, key string) int64 {
	n, _ := payloadInt(m, key)
	return n
}

func positiveInteger(n int64, fallback int64) int64 {
	if n > 0 {
		return n
	}
	return fallback
}
