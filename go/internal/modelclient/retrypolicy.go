package modelclient

import (
	"math"
	"strconv"
	"strings"
)

// Retry option constants (retry-policy.ts).
const (
	DefaultMaxRetries         = 10
	DefaultRetryBaseDelayMs   = 2000
	DefaultRetryBackoffFactor = 2.0
	DefaultRetryMaxDelayMs    = 60000
	DefaultRetryJitter        = true
)

// Env keys (retry-policy.ts).
const (
	EnvModelRetryMaxRetries    = "NEX_MODEL_RETRY_MAX_RETRIES"
	EnvModelRetryBaseDelayMs   = "NEX_MODEL_RETRY_BASE_DELAY_MS"
	EnvModelRetryBackoffFactor = "NEX_MODEL_RETRY_BACKOFF_FACTOR"
	EnvModelRetryMaxDelayMs    = "NEX_MODEL_RETRY_MAX_DELAY_MS"
)

// RetryOptions are the raw (possibly partial) options.
type RetryOptions struct {
	MaxAttempts   *int
	BaseDelayMs   *int
	BackoffFactor *float64
	MaxDelayMs    *int
	Jitter        *bool
}

// ResolvedRetryOptions is the fully-resolved retry configuration.
type ResolvedRetryOptions struct {
	MaxAttempts   int
	BaseDelayMs   int
	BackoffFactor float64
	MaxDelayMs    int
	Jitter        bool
}

// DefaultRetryOptions returns the defaults with maxAttempts = retries + 1.
func DefaultRetryOptions() ResolvedRetryOptions {
	return ResolvedRetryOptions{
		BackoffFactor: DefaultRetryBackoffFactor,
		BaseDelayMs:   DefaultRetryBaseDelayMs,
		Jitter:        DefaultRetryJitter,
		MaxAttempts:   DefaultMaxRetries + 1,
		MaxDelayMs:    DefaultRetryMaxDelayMs,
	}
}

// ResolveRetryOptions ports resolveAiSdkModelRetryOptions.
func ResolveRetryOptions(options *RetryOptions, env map[string]string) ResolvedRetryOptions {
	def := DefaultRetryOptions()
	envOpts := readRetryOptionsFromEnv(env)
	return ResolvedRetryOptions{
		BackoffFactor: normalizePositiveNumber(
			options.backoffFactorPtr(),
			normalizePositiveNumber(envOpts.BackoffFactor, def.BackoffFactor),
		),
		BaseDelayMs: normalizeNonNegativeInteger(
			options.baseDelayPtr(),
			normalizeNonNegativeInteger(envOpts.BaseDelayMs, def.BaseDelayMs),
		),
		Jitter: options.jitterOr(def.Jitter),
		MaxAttempts: normalizePositiveInteger(
			options.maxAttemptsPtr(),
			normalizePositiveInteger(envOpts.MaxAttempts, def.MaxAttempts),
		),
		MaxDelayMs: normalizeNonNegativeInteger(
			options.maxDelayPtr(),
			normalizeNonNegativeInteger(envOpts.MaxDelayMs, def.MaxDelayMs),
		),
	}
}

func (o *RetryOptions) backoffFactorPtr() *float64 {
	if o == nil {
		return nil
	}
	return o.BackoffFactor
}
func (o *RetryOptions) baseDelayPtr() *int {
	if o == nil {
		return nil
	}
	return o.BaseDelayMs
}
func (o *RetryOptions) maxAttemptsPtr() *int {
	if o == nil {
		return nil
	}
	return o.MaxAttempts
}
func (o *RetryOptions) maxDelayPtr() *int {
	if o == nil {
		return nil
	}
	return o.MaxDelayMs
}
func (o *RetryOptions) jitterOr(fallback bool) bool {
	if o == nil || o.Jitter == nil {
		return fallback
	}
	return *o.Jitter
}

// retryOptionsFromEnv is the env-parsed partial options.
type retryOptionsFromEnv struct {
	MaxAttempts   *int
	BaseDelayMs   *int
	BackoffFactor *float64
	MaxDelayMs    *int
}

func readRetryOptionsFromEnv(env map[string]string) retryOptionsFromEnv {
	var out retryOptionsFromEnv
	if maxRetries, ok := parseNonNegativeIntegerEnv(env[EnvModelRetryMaxRetries]); ok {
		attempts := maxRetries + 1
		out.MaxAttempts = &attempts
	}
	if v, ok := parsePositiveNumberEnv(env[EnvModelRetryBackoffFactor]); ok {
		out.BackoffFactor = &v
	}
	if v, ok := parseNonNegativeIntegerEnv(env[EnvModelRetryBaseDelayMs]); ok {
		out.BaseDelayMs = &v
	}
	if v, ok := parseNonNegativeIntegerEnv(env[EnvModelRetryMaxDelayMs]); ok {
		out.MaxDelayMs = &v
	}
	return out
}

func parseNonNegativeIntegerEnv(value string) (int, bool) {
	if value == "" || strings.TrimSpace(value) == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return 0, false
	}
	return int(math.Floor(parsed)), true
}

func parsePositiveNumberEnv(value string) (float64, bool) {
	if value == "" || strings.TrimSpace(value) == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

func normalizePositiveInteger(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return maxInt(1, *value)
}

func normalizePositiveNumber(value *float64, fallback float64) float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value <= 0 {
		return fallback
	}
	return *value
}

func normalizeNonNegativeInteger(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return maxInt(0, *value)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ModelRetryBudget values.
const (
	RetryBudgetDefault   = "default"
	RetryBudgetUnbounded = "unbounded"
	// UnboundedRetryMaxAttempts is the sentinel written into status events when
	// the budget is unbounded (Infinity is not serializable).
	UnboundedRetryMaxAttempts = 0
)

// IsUnboundedRetryBudget reports whether the budget is unbounded.
func IsUnboundedRetryBudget(budget string) bool { return budget == RetryBudgetUnbounded }

// RetryBudgetAllows ports retryBudgetAllows.
func RetryBudgetAllows(budget string, retryBudgetAttempt, maxAttempts int) bool {
	return IsUnboundedRetryBudget(budget) || retryBudgetAttempt < maxAttempts
}

// RetryAttemptLoopContinues ports retryAttemptLoopContinues.
func RetryAttemptLoopContinues(budget string, attempt, loopMaxAttempts int) bool {
	return IsUnboundedRetryBudget(budget) || attempt <= loopMaxAttempts
}

// RetryBudgetMaxAttempts ports retryBudgetMaxAttempts.
func RetryBudgetMaxAttempts(budget string, maxAttempts int) int {
	if IsUnboundedRetryBudget(budget) {
		return UnboundedRetryMaxAttempts
	}
	return maxAttempts
}
