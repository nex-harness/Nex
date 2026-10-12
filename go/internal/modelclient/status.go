package modelclient

import (
	"encoding/json"
	"time"
)

// transportErrorFromResponse builds an AttrError from a non-2xx response, mirroring
// the AI SDK's createJsonErrorResponseHandler: status, response headers and the
// (JSON-parsed when possible) response body.
func transportErrorFromResponse(resp *Response) error {
	attrs := ErrorAttrs{
		Name:             "APICallError",
		Message:          statusMessage(resp),
		StatusCode:       intPtr(resp.StatusCode),
		ResponseHeaders:  resp.Headers,
		ResponseBodyText: resp.BodyText,
	}
	if parsed := parseJSONLoose(resp.BodyText); parsed != nil {
		attrs.ResponseBody = parsed
		// APICallError.data is the parsed error body; the mapped provider error
		// path reads it.
		attrs.Data = parsed
	}
	return NewAttrError(attrs)
}

func statusMessage(resp *Response) string {
	if resp.StatusText != "" {
		return resp.StatusText
	}
	return "Provider returned a non-2xx response."
}

func publishStarted(cfg Config, status StatusSink, attempt, maxAttempts int, requestID string, startedAt time.Time) {
	if status == nil {
		return
	}
	status.Publish(baseStatus(cfg, StatusEvent{
		Status:      "model_request_started",
		Attempt:     attempt,
		MaxAttempts: effectiveMaxAttempts(cfg, maxAttempts),
		RequestID:   requestID,
	}))
}

func publishCompleted(cfg Config, status StatusSink, attempt, maxAttempts int, requestID string, startedAt time.Time, outcome streamOutcome) {
	if status == nil {
		return
	}
	ev := baseStatus(cfg, StatusEvent{
		Status:       "model_request_completed",
		Attempt:      attempt,
		MaxAttempts:  effectiveMaxAttempts(cfg, maxAttempts),
		RequestID:    requestID,
		DurationMs:   durationMs(startedAt, cfg.Clock),
		FinishReason: outcome.finishReason,
		Usage:        outcome.usage,
	})
	status.Publish(ev)
}

func publishFailed(cfg Config, status StatusSink, attempt, maxAttempts int, requestID string, startedAt time.Time, f Failure, committed bool) {
	if status == nil {
		return
	}
	status.Publish(baseStatus(cfg, StatusEvent{
		Status:                "model_request_failed",
		Attempt:               attempt,
		MaxAttempts:           effectiveMaxAttempts(cfg, maxAttempts),
		RequestID:             requestID,
		DurationMs:            durationMs(startedAt, cfg.Clock),
		Reason:                f.Reason,
		Message:               f.Message,
		ErrorCode:             f.Code,
		StatusCode:            f.StatusCode,
		Retryable:             f.Retryable,
		RetryAfterMs:          f.RetryAfterMs,
		StreamOutputCommitted: committed,
	}))
}

func publishRetryScheduled(cfg Config, status StatusSink, attempt, maxAttempts int, requestID string, f Failure, delayMs int64) {
	if status == nil {
		return
	}
	d := delayMs
	status.Publish(baseStatus(cfg, StatusEvent{
		Status:       "model_retry_scheduled",
		Attempt:      attempt,
		MaxAttempts:  effectiveMaxAttempts(cfg, maxAttempts),
		RequestID:    requestID,
		Reason:       f.RetryReason,
		Message:      f.Message,
		ErrorCode:    f.Code,
		StatusCode:   f.StatusCode,
		RetryAfterMs: f.RetryAfterMs,
		DelayMs:      &d,
		NextAttempt:  attempt + 1,
	}))
}

func baseStatus(cfg Config, ev StatusEvent) StatusEvent {
	ev.ProviderID = cfg.ProviderID
	ev.ModelID = cfg.ModelID
	ev.ProviderKind = cfg.ProviderKind
	ev.Transport = cfg.Transport
	return ev
}

func effectiveMaxAttempts(cfg Config, maxAttempts int) int {
	return RetryBudgetMaxAttempts(cfg.RetryBudget, maxAttempts)
}

func intPtr(v int) *int { return &v }

// parseJSONLoose parses text as JSON, returning nil for empty or invalid input.
func parseJSONLoose(text string) any {
	if len(text) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return nil
	}
	return v
}
