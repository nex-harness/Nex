package modelclient

import (
	"encoding/json"
	"strconv"
	"strings"
)

const providerBusinessErrorWrapperCode = "PROVIDER_BUSINESS_ERROR"

// findProviderBusinessError ports findProviderBusinessError: it walks the cause
// chain and returns the first AttrError marked IsProviderBusiness.
func findProviderBusinessError(attrs ErrorAttrs) (ErrorAttrs, bool) {
	cur := attrs
	seen := map[error]bool{}
	for {
		if cur.IsProviderBusiness {
			return cur, true
		}
		if cur.Cause == nil || seen[cur.Cause] {
			return ErrorAttrs{}, false
		}
		seen[cur.Cause] = true
		cur = attrsOf(cur.Cause)
	}
}

// readMappedAiSdkProviderBusinessError ports the same-named function: it reads
// APICallError.data.error (or the error itself), picks the code/type that has a
// business-code mapping, and rebuilds a ProviderBusinessError from it.
func readMappedAiSdkProviderBusinessError(attrs ErrorAttrs) (ErrorAttrs, bool) {
	data, _ := asRecord(attrs.Data)
	errorData, ok := asRecord(data["error"])
	if !ok {
		errorData, _ = asRecord(attrs.Data)
	}
	if errorData == nil {
		// fall back to reading code/type straight off the outer attrs
		errorData = map[string]any{}
		if attrs.Code != "" {
			errorData["code"] = attrs.Code
		}
		if len(errorData) == 0 {
			return ErrorAttrs{}, false
		}
	}

	providerCode := ""
	for _, candidate := range []string{
		normalizeProviderCode(errorData["code"]),
		normalizeProviderCode(errorData["type"]),
	} {
		if candidate != "" {
			if _, ok := providerBusinessCodeMapping(candidate); ok {
				providerCode = candidate
				break
			}
		}
	}
	if providerCode == "" {
		return ErrorAttrs{}, false
	}

	statusCode := getStatusCode(attrs)
	var msg string
	if s, ok := errorData["message"].(string); ok {
		msg = s
	}
	out := ErrorAttrs{
		IsProviderBusiness:  true,
		ProviderCode:        providerCode,
		HasProviderCode:     true,
		ProviderMessage:     msg,
		ProviderRequestID:   readProviderRequestID(data, errorData),
		ResponseBodySummary: summarizeAiSdkErrorData(data, errorData),
		ResponseHeaders:     getResponseHeaders(attrs),
		StatusCode:          statusCode,
	}
	if statusCode != nil {
		out.ResponseStatus = statusCode
	}
	return out, true
}

func readProviderRequestID(data, errorData map[string]any) string {
	for _, rec := range []map[string]any{data, errorData} {
		if rec == nil {
			continue
		}
		for _, key := range []string{"request_id", "requestId", "id"} {
			if v := stringTrimmed(rec[key]); v != "" {
				if len(v) > 1000 {
					v = v[:1000]
				}
				return v
			}
		}
	}
	return ""
}

func summarizeAiSdkErrorData(data, errorData map[string]any) map[string]any {
	summary := map[string]any{}
	if data != nil {
		copyScalar(data, summary, "request_id")
		copyScalar(data, summary, "requestId")
	}
	errorSummary := map[string]any{}
	copyScalar(errorData, errorSummary, "code")
	copyScalar(errorData, errorSummary, "type")
	copyScalar(errorData, errorSummary, "request_id")
	copyScalar(errorData, errorSummary, "requestId")
	summary["error"] = errorSummary
	return summary
}

func copyScalar(source, target map[string]any, key string) {
	v := source[key]
	switch v.(type) {
	case string, float64, bool, int64, json.Number:
		target[key] = v
	}
}

func normalizeProviderCode(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	case string:
		return stringTrimmed(value)
	}
	return ""
}

// classifyProviderBusinessFailureFromAPICallBody ports the same-named function:
// when an APICallError body contains a provider business signal, rebuild it.
func classifyProviderBusinessFailureFromAPICallBody(attrs ErrorAttrs, statusCode *int, retryAfterMs *int64) (Failure, bool) {
	body := getAPICallResponseBody(attrs)
	detected := ReadProviderBusinessFailureFromBody(body)
	if !detected.Found {
		return Failure{}, false
	}
	status := statusCode
	if status == nil && detected.StatusCode != nil {
		status = detected.StatusCode
	}
	built := ErrorAttrs{
		IsProviderBusiness:  true,
		ProviderCode:        detected.ProviderCode,
		HasProviderCode:     detected.ProviderCode != "",
		ProviderMessage:     detected.ProviderMessage,
		ProviderRequestID:   detected.ProviderRequestID,
		ResponseBodySummary: detected.ResponseBodySummary,
	}
	if status != nil {
		built.StatusCode = status
		built.ResponseStatus = status
	}
	return classifyProviderBusinessFailure(built, status, retryAfterMs)
}

func classifyProviderBusinessFailure(attrs ErrorAttrs, statusCode *int, retryAfterMs *int64) (Failure, bool) {
	if !attrs.IsProviderBusiness {
		return Failure{}, false
	}
	message := providerBusinessMessage(attrs)
	providerCode := resolveProviderBusinessCode(attrs)
	effectiveStatus := resolveProviderBusinessStatusCode(attrs, statusCode)

	if mapped, ok := providerBusinessCodeMapping(providerCode); ok {
		msg := message
		if mapped.message != "" {
			msg = mapped.message
		}
		return Failure{
			Code:         mapped.code,
			Message:      msg,
			Reason:       mapped.reason,
			RetryReason:  mapped.retryReason,
			Retryable:    mapped.retryable,
			RetryAfterMs: retryAfterMs,
			StatusCode:   effectiveStatus,
		}, true
	}

	if isContextExceededFailure(ErrorAttrs{Message: attrs.ProviderMessage, ProviderCode: attrs.ProviderCode, Code: attrs.Code, ResponseBody: attrs.ResponseBodySummary}) {
		return Failure{
			Code:         CodeModelContextExceeded,
			Message:      "Model request exceeded the provider context window.",
			Reason:       ReasonContextExceeded,
			RetryReason:  ReasonNetworkError,
			Retryable:    false,
			RetryAfterMs: retryAfterMs,
			StatusCode:   effectiveStatus,
		}, true
	}
	if isRetryableProviderBusinessTimeoutFailure(attrs, providerCode, effectiveStatus) {
		return Failure{
			Code:         CodeModelRequestTimeout,
			Message:      message,
			Reason:       ReasonTimeout,
			RetryReason:  ReasonTimeout,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   effectiveStatus,
		}, true
	}
	retryReason := ReasonNetworkError
	if effectiveStatus != nil && *effectiveStatus >= 500 {
		retryReason = ReasonServerError
	}

	if effectiveStatus != nil && (*effectiveStatus == 401 || *effectiveStatus == 403) {
		return Failure{
			Code:         CodeProviderNotConfigured,
			Message:      message,
			Reason:       ReasonAuthFailed,
			RetryReason:  ReasonAuthRefresh,
			Retryable:    false,
			RetryAfterMs: retryAfterMs,
			StatusCode:   effectiveStatus,
		}, true
	}
	if effectiveStatus != nil && *effectiveStatus == 404 {
		return Failure{
			Code:         CodeModelNotFound,
			Message:      message,
			Reason:       ReasonInvalidRequest,
			RetryReason:  ReasonNetworkError,
			Retryable:    false,
			RetryAfterMs: retryAfterMs,
			StatusCode:   effectiveStatus,
		}, true
	}
	if effectiveStatus != nil && (*effectiveStatus == 400 || *effectiveStatus == 422) {
		return Failure{
			Code:         CodeInvalidModelRequest,
			Message:      message,
			Reason:       ReasonInvalidRequest,
			RetryReason:  ReasonNetworkError,
			Retryable:    false,
			RetryAfterMs: retryAfterMs,
			StatusCode:   effectiveStatus,
		}, true
	}
	if effectiveStatus != nil && *effectiveStatus == 429 {
		return Failure{
			Code:         CodeModelRateLimited,
			Message:      message,
			Reason:       ReasonRateLimited,
			RetryReason:  ReasonRateLimited,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   effectiveStatus,
		}, true
	}
	if isRetryableProviderBusinessNetworkFailure(attrs, providerCode) {
		return Failure{
			Code:         CodeModelRequestFailed,
			Message:      message,
			Reason:       ReasonNetworkError,
			RetryReason:  ReasonNetworkError,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   effectiveStatus,
		}, true
	}
	if effectiveStatus != nil && *effectiveStatus >= 500 {
		return Failure{
			Code:         CodeModelRequestFailed,
			Message:      message,
			Reason:       ReasonServerError,
			RetryReason:  retryReason,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   effectiveStatus,
		}, true
	}
	return Failure{
		Code:         CodeModelRequestFailed,
		Message:      message,
		Reason:       ReasonUnknown,
		RetryReason:  retryReason,
		Retryable:    false,
		RetryAfterMs: retryAfterMs,
		StatusCode:   effectiveStatus,
	}, true
}

func providerBusinessMessage(attrs ErrorAttrs) string {
	if attrs.ProviderMessage != "" {
		return attrs.ProviderMessage
	}
	if attrs.Message != "" {
		return attrs.Message
	}
	return "Provider returned a business error."
}

func resolveProviderBusinessCode(attrs ErrorAttrs) string {
	if direct := normalizeProviderCodeForError(attrs.ProviderCode); direct != "" {
		return direct
	}
	return readNestedProviderCode(attrs.ResponseBodySummary)
}

func normalizeProviderCodeForError(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		normalized := stringTrimmed(v)
		if normalized == "" {
			return ""
		}
		if strings.ToUpper(normalized) == providerBusinessErrorWrapperCode {
			return ""
		}
		return normalized
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return ""
}

func resolveProviderBusinessStatusCode(attrs ErrorAttrs, statusCode *int) *int {
	if s := normalizeHTTPFailureStatus(statusCode); s != nil {
		return s
	}
	if s := normalizeHTTPFailureStatus(attrs.StatusCode); s != nil {
		return s
	}
	if s := normalizeHTTPFailureStatus(attrs.ResponseStatus); s != nil {
		return s
	}
	if s, ok := readNestedStatusCode(attrs.ResponseBodySummary); ok {
		return s
	}
	return nil
}

func normalizeHTTPFailureStatus(value *int) *int {
	if value != nil && *value >= 400 && *value <= 599 {
		return value
	}
	return nil
}
