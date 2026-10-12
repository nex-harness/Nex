package modelclient

import "math"

// MaxReasonableRetryAfterMs mirrors MAX_REASONABLE_RETRY_AFTER_MS (5 minutes).
const MaxReasonableRetryAfterMs = 5 * 60_000

// RandFunc returns a float64 in [0,1), matching Math.random(). It is injected so
// the jitter branch is deterministic in tests.
type RandFunc func() float64

// CalculateRetryDelay ports calculateRetryDelay. rand is used only for the jitter
// branch; pass nil to disable jitter replacement (tests inject a fixed value).
func CalculateRetryDelay(retry ResolvedRetryOptions, attempt int, retryAfterMs *int64, rand RandFunc) int64 {
	exponent := attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	uncapped := float64(retry.BaseDelayMs) * math.Pow(retry.BackoffFactor, float64(exponent))
	capped := math.Min(uncapped, float64(retry.MaxDelayMs))

	if isReasonableRetryAfterMs(retryAfterMs, uncapped) {
		return *retryAfterMs
	}
	if !retry.Jitter || capped == 0 {
		return int64(capped)
	}

	r := 0.0
	if rand != nil {
		r = rand()
	}
	return int64(math.Round(capped * (0.5 + r*0.5)))
}

func isReasonableRetryAfterMs(value *int64, exponentialDelayMs float64) bool {
	if value == nil {
		return false
	}
	v := float64(*value)
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return false
	}
	return v <= MaxReasonableRetryAfterMs || v < exponentialDelayMs
}

// EmptyCompletion retry constants (empty-completion-retry.ts).
const (
	EmptyCompletionMaxRetries = 1
	EmptyCompletionMessage    = "Model returned no text, no tool calls, and no usage before completing the turn."
)

// EmptyCompletionFailure returns the synthetic failure used for an empty
// completion (createEmptyCompletionFailure).
func EmptyCompletionFailure() Failure {
	return Failure{
		Code:        CodeInvalidModelResponse,
		Message:     EmptyCompletionMessage,
		Reason:      ReasonUnknown,
		RetryReason: ReasonServerError,
		Retryable:   true,
	}
}

// CanRetryEmptyCompletion ports canRetryEmptyCompletion.
func CanRetryEmptyCompletion(aborted bool, attempt, maxAttempts, retryCount int) bool {
	return !aborted && retryCount < EmptyCompletionMaxRetries && attempt < maxAttempts
}

// Stream idle timeout constants (stream-idle-timeout.ts / @nex/contracts).
const (
	// DefaultModelStreamIdleTimeoutMs is DEFAULT_MODEL_STREAM_IDLE_TIMEOUT_MS.
	DefaultModelStreamIdleTimeoutMs = 600_000
	// modelStreamIdleTimeoutRetryIncrementMs is the per-retry increment.
	modelStreamIdleTimeoutRetryIncrementMs = 30_000
)

// ResolveModelStreamIdleTimeoutMs ports resolveModelStreamIdleTimeoutMs.
func ResolveModelStreamIdleTimeoutMs(baseTimeoutMs *int, retryNumber *int) int {
	base := DefaultModelStreamIdleTimeoutMs
	if baseTimeoutMs != nil {
		base = *baseTimeoutMs
	}
	if base <= 0 {
		return base
	}
	retry := 0
	if retryNumber != nil && *retryNumber > 0 {
		retry = *retryNumber
	}
	return base + retry*modelStreamIdleTimeoutRetryIncrementMs
}
