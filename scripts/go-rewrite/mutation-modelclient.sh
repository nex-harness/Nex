#!/usr/bin/env bash
# Mutation check for the Go model client failure classifier: applies each mutation,
# runs the modelclient tests, expects a failure, then restores the file. Prints
# CAUGHT / NOT-CAUGHT per mutation.
#
# Every mutation below breaks one classifier rule; the failure table is only
# meaningful if flipping a rule makes it fail.
set -u
cd "$(dirname "$0")/../../go" || exit 2
PKG=./internal/modelclient/
OUT="${MUTATION_OUT:-/tmp/mut-modelclient.out}"

mutate() {
  local desc="$1" file="$2" from="$3" to="$4"
  cp "$file" "$file.bak"
  python3 - "$file" "$from" "$to" <<'PY'
import sys
path, frm, to = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(path).read()
if frm not in s:
    print("MUTATION-PATTERN-MISSING", file=sys.stderr)
    sys.exit(3)
open(path, "w").write(s.replace(frm, to, 1))
PY
  if [ $? -ne 0 ]; then
    mv "$file.bak" "$file"
    echo "SKIP (pattern missing): $desc"
    return
  fi
  if go test "$PKG" >"$OUT" 2>&1; then
    echo "NOT-CAUGHT: $desc"
  else
    echo "CAUGHT:     $desc"
  fi
  mv "$file.bak" "$file"
}

# ── status-code branches (errors.go) ─────────────────────────────────────────
mutate "429 no longer classified as rate limited" \
  internal/modelclient/errors.go 'if statusCode != nil && *statusCode == 429 {' \
  'if statusCode != nil && *statusCode == 439 {'

mutate "529 no longer classified as overloaded" \
  internal/modelclient/errors.go 'if statusCode != nil && *statusCode == 529 {' \
  'if statusCode != nil && *statusCode == 528 {'

mutate "401 falls through to the generic branch" \
  internal/modelclient/errors.go 'if statusCode != nil && (*statusCode == 401 || *statusCode == 403) {' \
  'if statusCode != nil && (*statusCode == 401 || *statusCode == 405) {'

mutate "400 loses the context-exceeded split" \
  internal/modelclient/errors.go 'if statusCode != nil && (*statusCode == 400 || *statusCode == 422) {
		contextExceeded := isContextExceededFailure(attrs)' \
  'if statusCode != nil && (*statusCode == 400 || *statusCode == 422) {
		contextExceeded := false'

mutate "5xx stops being retryable" \
  internal/modelclient/errors.go '			Code:         CodeModelRequestFailed,
			Message:      "Provider returned a server error.",
			Reason:       ReasonServerError,
			RetryReason:  ReasonServerError,
			Retryable:    true,' \
  '			Code:         CodeModelRequestFailed,
			Message:      "Provider returned a server error.",
			Reason:       ReasonServerError,
			RetryReason:  ReasonServerError,
			Retryable:    false,'

mutate "abort stops winning over the status code" \
  internal/modelclient/errors.go '	if isAbortFailure(attrs, aborted) {' \
  '	if false {'

# ── inspector predicates (inspect.go) ───────────────────────────────────────
mutate "network: ECONNRESET removed from the retryable set" \
  internal/modelclient/inspect.go 'case "ECONNRESET", "ECONNREFUSED", "EAI_AGAIN", "ENOTFOUND", "ENETUNREACH",
		"EHOSTUNREACH", "UND_ERR_SOCKET", "UND_ERR_CONNECT_TIMEOUT":' \
  'case "ECONNREFUSED", "EAI_AGAIN", "ENOTFOUND", "ENETUNREACH",
		"EHOSTUNREACH", "UND_ERR_SOCKET", "UND_ERR_CONNECT_TIMEOUT":'

mutate "timeout: UND_ERR_BODY_TIMEOUT removed" \
  internal/modelclient/inspect.go 'case "ETIMEDOUT", "ETIMEOUT", "UND_ERR_CONNECT_TIMEOUT", "UND_ERR_HEADERS_TIMEOUT", "UND_ERR_BODY_TIMEOUT":' \
  'case "ETIMEDOUT", "ETIMEOUT", "UND_ERR_CONNECT_TIMEOUT", "UND_ERR_HEADERS_TIMEOUT":'

mutate "timeout: 408 no longer counts as a timeout" \
  internal/modelclient/inspect.go '	if statusCode != nil && *statusCode == 408 {' \
  '	if statusCode != nil && *statusCode == 409 {'

mutate "tls: CERT codes stop matching" \
  internal/modelclient/inspect.go '	return strings.Contains(up, "CERT") ||' \
  '	return false ||'

mutate "proxy: PROXY codes stop matching" \
  internal/modelclient/inspect.go 'func isProxyFailure(code string) bool {
	return strings.Contains(strings.ToUpper(code), "PROXY")' \
  'func isProxyFailure(code string) bool {
	return strings.Contains(strings.ToUpper(code), "PROXYX")'

# ── provider business codes (businesscodes.go) ──────────────────────────────
mutate "1312 stops being a retryable overload" \
  internal/modelclient/businesscodes.go '"1312": retryableOverloadMapping,' \
  '"1312": terminalRateLimitMapping,'

mutate "1304 becomes retryable" \
  internal/modelclient/businesscodes.go '"1304": terminalRateLimitMapping,' \
  '"1304": retryableRateLimitMapping,'

mutate "3006 no longer maps to model_not_found" \
  internal/modelclient/businesscodes.go '"3006": {code: CodeModelNotFound, reason: ReasonInvalidRequest, retryReason: ReasonNetworkError, retryable: false},' \
  '"3006": {code: CodeInvalidModelRequest, reason: ReasonInvalidRequest, retryReason: ReasonNetworkError, retryable: false},'

mutate "the PROVIDER_BUSINESS_ERROR wrapper code stops being ignored" \
  internal/modelclient/business.go '		if strings.ToUpper(normalized) == providerBusinessErrorWrapperCode {
			return ""
		}' \
  '		if false {
			return ""
		}'

mutate "nested provider code no longer reads error_code" \
  internal/modelclient/business_nested.go '	if code := firstNormalizedRecordValue(records, "error_code"); code != "" {' \
  '	if false {'

# ── context-exceeded phrases (contextlimit.go) ──────────────────────────────
# Each clause is chosen so no sibling clause re-covers the same phrase: the
# "context"+"exceed" rule subsumes the model_context_window_exceeded literal,
# so mutating that one alone would prove nothing.
mutate "context: the range-of-input-length phrase is gone" \
  internal/modelclient/contextlimit.go '		strings.Contains(message, "range of input length should be") ||' \
  '		false ||'

mutate "context: the prompt-is-too-long phrase is gone" \
  internal/modelclient/contextlimit.go '		strings.Contains(message, "prompt is too long") ||' \
  '		false ||'

mutate "context: the maximum-context-length phrase is gone" \
  internal/modelclient/contextlimit.go '		(strings.Contains(message, "maximum context length") &&' \
  '		(false &&'

mutate "context: context_window_exceeded code removed" \
  internal/modelclient/contextlimit.go '	case "context_length_exceeded", "context_window_exceeded", "model_context_exceeded", "model_context_window_exceeded":' \
  '	case "context_length_exceeded", "model_context_exceeded":'

# ── Retry-After parsing (retryafter.go) ─────────────────────────────────────
mutate "retry-after: x-should-retry=false no longer blocks the wait" \
  internal/modelclient/retryafter.go '	if isShouldRetryHeaderFalse(headers) {
		return nil
	}' \
  '	if false {
		return nil
	}'

mutate "retry-after: retry-after-ms no longer wins over retry-after" \
  internal/modelclient/retryafter.go '	if v, ok := findHeaderValue(headers, "retry-after-ms"); ok {' \
  '	if v, ok := findHeaderValue(headers, "retry-after-millis"); ok {'

# ── retry policy / budget (retrypolicy.go, retrydelay.go) ────────────────────
mutate "retry: maxAttempts default drops the +1" \
  internal/modelclient/retrypolicy.go '		MaxAttempts:   DefaultMaxRetries + 1,' \
  '		MaxAttempts:   DefaultMaxRetries,'

mutate "retry: the unbounded budget stops allowing another attempt" \
  internal/modelclient/retrypolicy.go 'func RetryBudgetAllows(budget string, retryBudgetAttempt, maxAttempts int) bool {
	return IsUnboundedRetryBudget(budget) || retryBudgetAttempt < maxAttempts' \
  'func RetryBudgetAllows(budget string, retryBudgetAttempt, maxAttempts int) bool {
	return retryBudgetAttempt < maxAttempts'

mutate "backoff: the exponent is off by one" \
  internal/modelclient/retrydelay.go '	exponent := attempt - 1' \
  '	exponent := attempt'

mutate "backoff: Retry-After stops overriding the curve" \
  internal/modelclient/retrydelay.go '	if isReasonableRetryAfterMs(retryAfterMs, uncapped) {
		return *retryAfterMs
	}' \
  '	if false {
		return *retryAfterMs
	}'

mutate "idle timeout: the per-retry increment is dropped" \
  internal/modelclient/retrydelay.go '	return base + retry*modelStreamIdleTimeoutRetryIncrementMs' \
  '	return base'

mutate "idle timeout: empty completion becomes non-retryable" \
  internal/modelclient/retrydelay.go '		Reason:      ReasonUnknown,
		RetryReason: ReasonServerError,
		Retryable:   true,' \
  '		Reason:      ReasonUnknown,
		RetryReason: ReasonServerError,
		Retryable:   false,'

# ── retry boundary (streamutil.go, stream.go) ───────────────────────────────
mutate "retry boundary: a visible text delta no longer commits the stream" \
  internal/modelclient/streamutil.go '	case EventTextDelta, EventReasoningDelta:
		return ev.Text == ""' \
  '	case EventTextDelta, EventReasoningDelta:
		return true'

mutate "retry: setup failures are no longer gated by retryability" \
  internal/modelclient/stream.go '	return RetryBudgetAllows(h.cfg.RetryBudget, attempt, maxAttempts) && IsRetryableFailure(f)' \
  '	return RetryBudgetAllows(h.cfg.RetryBudget, attempt, maxAttempts)'

echo "----"
echo "log: $OUT"