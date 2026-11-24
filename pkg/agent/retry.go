package agent

import (
	"context"
	"errors"
	"strings"
	"time"
)

// DefaultStreamIdleTimeout is how long a single provider stream turn can go without
// receiving any SSE/stream event before being aborted as an idle hang.
const DefaultStreamIdleTimeout = 90 * time.Second

// ErrStreamIdleTimeout indicates the upstream LLM provider accepted the request
// (or was mid-stream) but stopped sending any events within the idle timeout window.
var ErrStreamIdleTimeout = errors.New("upstream stream idle timeout")

// IsContextOverflowError reports whether an LLM provider error indicates the request
// exceeded the model's context window or input token limit.
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	indicators := []string{
		"context_length_exceeded",
		"maximum context length",
		"prompt is too long",
		"input is too long",
		"too many tokens",
		"exceeds the context window",
		"exceeds maximum context",
		"context window exceeded",
		"request too large",
		"token limit exceeded",
		"max_tokens_exceeded",
		"reduce the length of the messages",
	}
	for _, needle := range indicators {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// IsRetryableProviderError reports whether a provider stream error is transient
// (rate limits, server overload, 5xx, network hiccups) and safe to retry with backoff.
func IsRetryableProviderError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrStreamIdleTimeout) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if IsContextOverflowError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())

	// Non-retryable client auth/bad-request errors
	nonRetryable := []string{
		"http 400",
		"http 401",
		"http 403",
		"http 404",
		"http 422",
		"invalid_api_key",
		"authentication_error",
		"permission_error",
		"unsupported provider driver",
	}
	for _, nr := range nonRetryable {
		if strings.Contains(msg, nr) {
			return false
		}
	}

	retryable := []string{
		"http 408",
		"http 409",
		"http 425",
		"http 429",
		"http 500",
		"http 502",
		"http 503",
		"http 504",
		"http 529",
		"rate_limit",
		"rate limit",
		"too many requests",
		"overloaded",
		"server_error",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		"connection reset",
		"broken pipe",
		"unexpected eof",
		"eof",
		"timeout awaiting response headers",
		"i/o timeout",
		"timeout",
		"temporarily unavailable",
	}
	for _, r := range retryable {
		if strings.Contains(msg, r) {
			return true
		}
	}
	return false
}

// RetryDelay returns bounded exponential backoff duration for 0-based retry attempt.
func RetryDelay(attempt int, base time.Duration) time.Duration {
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	if attempt < 0 {
		attempt = 0
	}
	delay := base << uint(min(attempt, 5))
	const maxDelay = 12 * time.Second
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
