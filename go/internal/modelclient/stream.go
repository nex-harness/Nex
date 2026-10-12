package modelclient

import (
	"context"
	"errors"
	"io"
	"time"
)

// Stream performs a streaming model request with the agent's retry policy and
// returns a handle whose channel carries the decoded events. The channel is
// closed when the stream ends; the terminal error is available from Wait/Err.
//
// The retry loop mirrors runStreamText:
//
//   - each attempt to acquire admission is out of scope here (the lead owns the
//     queue); each physical request publishes model_request_started;
//   - a setup failure (no response / non-2xx) is retryable per the classifier and
//     the budget, and is never blocked by a retry boundary (nothing was emitted);
//   - a stream failure is retryable only while no visible output has been
//     committed (the "retry boundary");
//   - an idle-timeout retry widens the window by 30s per retry.
//
// Visible events (text/reasoning/tool-* deltas and the final tool calls) are
// emitted as they are decoded; a partial attempt that is retried after only
// retry-safe prelude events is silently dropped, matching the Node buffering.
func (c *Client) Stream(ctx context.Context, req Request) *StreamHandle {
	h := &StreamHandle{
		cfg:    c.cfg,
		events: make(chan Event, 64),
		done:   make(chan struct{}),
	}
	go h.run(ctx, c.transport, c.status, req)
	return h
}

// StreamHandle is an in-flight streaming request.
type StreamHandle struct {
	cfg       Config
	events    chan Event
	done      chan struct{}
	err       error
	lastUsage *Usage
	finish    string
}

// Events returns the decoded event channel. It is closed when the stream ends.
func (h *StreamHandle) Events() <-chan Event { return h.events }

// Wait blocks until the stream ends and returns the terminal error (nil on a
// clean finish).
func (h *StreamHandle) Wait() error {
	<-h.done
	return h.err
}

// Err returns the terminal error if the stream has already ended.
func (h *StreamHandle) Err() error { return h.err }

// Usage returns the usage reported by the final finish event, if any.
func (h *StreamHandle) Usage() *Usage { return h.lastUsage }

// FinishReason returns the provider finish reason, if any.
func (h *StreamHandle) FinishReason() string { return h.finish }

func (h *StreamHandle) run(ctx context.Context, transport Transport, status StatusSink, req Request) {
	defer close(h.done)
	defer close(h.events)
	h.err = h.streamLoop(ctx, transport, status, req)
}

func (h *StreamHandle) streamLoop(ctx context.Context, transport Transport, status StatusSink, req Request) error {
	maxAttempts := h.cfg.Retry.MaxAttempts
	attempt := 1
	retryBoundaryCommitted := false

	for RetryAttemptLoopContinues(h.cfg.RetryBudget, attempt, maxAttempts) {
		startedAt := h.cfg.Clock.Now()
		attemptID := requestIDForAttempt(req, attempt)
		// The Node runner publishes model_request_started before the physical
		// request, so a failed attempt still has a matching started event (S13:
		// started, failed, retry_scheduled, started, completed).
		publishStarted(h.cfg, status, attempt, maxAttempts, attemptID, startedAt)

		resp, err := transport.Do(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			err = normalizeTransportError(err)
			failure := ClassifyFailure(err, false)
			delay := CalculateRetryDelay(h.cfg.Retry, attempt, failure.RetryAfterMs, h.cfg.Rand)
			publishFailed(h.cfg, status, attempt, maxAttempts, attemptID, startedAt, failure, false)
			if !h.canRetrySetup(attempt, maxAttempts, failure) {
				return &AttemptError{Failure: failure}
			}
			publishRetryScheduled(h.cfg, status, attempt, maxAttempts, attemptID, failure, delay)
			// 退避期间必须把 sleep 的真实错误抛出去：只回 ctx.Err() 会在 ctx 仍然
			// 有效时返回 nil，让一次失败的退避静默变成一次立即重试。
			if serr := h.cfg.Clock.Sleep(ctx, ms(delay)); serr != nil {
				return serr
			}
			attempt++
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err := transportErrorFromResponse(resp)
			closeResponse(resp)
			failure := ClassifyFailure(err, false)
			delay := CalculateRetryDelay(h.cfg.Retry, attempt, failure.RetryAfterMs, h.cfg.Rand)
			publishFailed(h.cfg, status, attempt, maxAttempts, attemptID, startedAt, failure, false)
			if !h.canRetrySetup(attempt, maxAttempts, failure) {
				return &AttemptError{Failure: failure}
			}
			publishRetryScheduled(h.cfg, status, attempt, maxAttempts, attemptID, failure, delay)
			if serr := h.cfg.Clock.Sleep(ctx, ms(delay)); serr != nil {
				return serr
			}
			attempt++
			continue
		}

		outcome := h.runOneStream(ctx, status, resp, attempt, maxAttempts, attemptID, startedAt)
		closeResponse(resp)
		if outcome.committed {
			retryBoundaryCommitted = true
		}
		if outcome.err == nil {
			publishCompleted(h.cfg, status, attempt, maxAttempts, attemptID, startedAt, outcome)
			h.lastUsage = outcome.usage
			h.finish = outcome.finishReason
			return nil
		}
		if errors.Is(outcome.err, context.Canceled) || ctx.Err() != nil {
			return ctx.Err()
		}
		if !outcome.retry || retryBoundaryCommitted {
			return &AttemptError{Failure: outcome.failure}
		}
		attempt++
	}
	return &AttemptError{Failure: Failure{
		Code:    CodeModelRequestFailed,
		Message: "Model request failed before an attempt could complete",
		Reason:  ReasonUnknown,
	}}
}

// streamOutcome is the result of one streaming attempt.
type streamOutcome struct {
	err          error
	retry        bool
	delayMs      int64
	failure      Failure
	committed    bool
	usage        *Usage
	finishReason string
}

func (h *StreamHandle) runOneStream(
	ctx context.Context,
	status StatusSink,
	resp *Response,
	attempt, maxAttempts int,
	attemptID string,
	startedAt time.Time,
) streamOutcome {
	decoder := decoderFactoryFor(h.cfg)()
	assembler := NewAssembler()
	var out []Event
	out = append(out, decoder.Start())
	outcome := streamOutcome{}

	idleMs := ResolveModelStreamIdleTimeoutMs(intPtrOrNil(h.cfg.IdleTimeout), h.retryNumber(attempt))

	buf := make([]byte, 32*1024)
	var readErr error
	for {
		if ctx.Err() != nil {
			outcome.err = context.Canceled
			return outcome
		}
		n, err := readWithIdle(resp.Stream, buf, h.cfg.Clock, idleMs)
		if n > 0 {
			var derr error
			out, derr = decoder.Decode(buf[:n], out)
			if derr != nil {
				readErr = derr
				break
			}
			// Emit anything decodable now (streaming latency).
			var emitErr error
			out, emitErr = h.drain(ctx, out, assembler, &outcome)
			if emitErr != nil {
				outcome.err = emitErr
				return outcome
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			readErr = normalizeTransportError(err)
			break
		}
	}

	if readErr == nil {
		var ferr error
		out, ferr = decoder.Finish(out)
		if ferr != nil {
			readErr = ferr
		} else {
			var emitErr error
			out, emitErr = h.drain(ctx, out, assembler, &outcome)
			if emitErr != nil {
				outcome.err = emitErr
				return outcome
			}
		}
	}
	if readErr == nil {
		// success
		return outcome
	}

	failure := ClassifyFailure(readErr, ctx.Err() != nil)
	outcome.failure = failure
	outcome.delayMs = CalculateRetryDelay(h.cfg.Retry, attempt, failure.RetryAfterMs, h.cfg.Rand)
	publishFailed(h.cfg, status, attempt, maxAttempts, attemptID, startedAt, failure, outcome.committed)
	outcome.retry = h.canRetryStream(attempt, maxAttempts, outcome.committed, failure)
	if outcome.retry {
		publishRetryScheduled(h.cfg, status, attempt, maxAttempts, attemptID, failure, outcome.delayMs)
		if serr := h.cfg.Clock.Sleep(ctx, ms(outcome.delayMs)); serr != nil {
			outcome.retry = false
			outcome.err = serr
			return outcome
		}
	}
	outcome.err = &AttemptError{Failure: failure}
	return outcome
}

// drain folds any complete events out of the accumulated slice through the
// assembler and emits them, returning the residual buffer (incomplete events are
// not buffered here because the decoder already buffers at the SSE level).
func (h *StreamHandle) drain(ctx context.Context, pending []Event, assembler *Assembler, outcome *streamOutcome) ([]Event, error) {
	for _, ev := range pending {
		assembled, err := assembler.Handle(ev)
		if err != nil {
			return nil, err
		}
		for _, ev2 := range assembled {
			if !isRetrySafePreludeEvent(ev2) {
				outcome.committed = true
			}
			observeOutcome(outcome, ev2)
			if !h.emit(ctx, ev2) {
				return nil, context.Canceled
			}
		}
	}
	return nil, nil
}

func (h *StreamHandle) emit(ctx context.Context, ev Event) bool {
	select {
	case <-ctx.Done():
		return false
	case h.events <- ev:
		return true
	}
}

func (h *StreamHandle) retryNumber(attempt int) *int {
	base := 0
	if h.cfg.StreamIdleTimeoutRetryNumber != nil {
		base = *h.cfg.StreamIdleTimeoutRetryNumber
	}
	n := base + attempt - 1
	return &n
}

// canRetrySetup mirrors the setup-failure gate: budget must allow and the failure
// must be retryable (a retry boundary cannot exist before any output).
func (h *StreamHandle) canRetrySetup(attempt, maxAttempts int, f Failure) bool {
	if f.Reason == ReasonCancelled {
		return false
	}
	return RetryBudgetAllows(h.cfg.RetryBudget, attempt, maxAttempts) && IsRetryableFailure(f)
}

// canRetryStream mirrors canRetryStreamFailure: visible output commits the stream
// and forbids replay; otherwise budget + retryable decide.
func (h *StreamHandle) canRetryStream(attempt, maxAttempts int, committed bool, f Failure) bool {
	if committed {
		return false
	}
	if !RetryBudgetAllows(h.cfg.RetryBudget, attempt, maxAttempts) {
		return false
	}
	if f.Reason == ReasonCancelled {
		return false
	}
	return IsRetryableFailure(f)
}

// AttemptError is the terminal error of a stream, carrying the classified failure.
type AttemptError struct {
	Failure Failure
}

func (e *AttemptError) Error() string { return e.Failure.Message }

// FailureOf extracts the classified failure from an error, if present.
func FailureOf(err error) (Failure, bool) {
	var ae *AttemptError
	if errors.As(err, &ae) {
		return ae.Failure, true
	}
	return Failure{}, false
}

func ms(delayMs int64) time.Duration { return time.Duration(delayMs) * time.Millisecond }

func intPtrOrNil(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
