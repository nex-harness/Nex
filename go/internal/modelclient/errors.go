package modelclient

// Model error codes (ModelErrorCode in @nex/contracts).
const (
	CodeInvalidModelSelection = "invalid_model_selection"
	CodeModelConfigMissing    = "model_config_missing"
	CodeProviderNotFound      = "provider_not_found"
	CodeProviderNotConfigured = "provider_not_configured"
	CodeModelNotFound         = "model_not_found"
	CodeInvalidModelRequest   = "invalid_model_request"
	CodeInvalidModelResponse  = "invalid_model_response"
	CodeModelRequestFailed    = "model_request_failed"
	CodeModelRequestAuthMiss  = "model_request_auth_missing"
	CodeModelRequestCancelled = "model_request_cancelled"
	CodeModelRequestTimeout   = "model_request_timeout"
	CodeModelRateLimited      = "model_rate_limited"
	CodeModelContextExceeded  = "model_context_exceeded"
)

// Model failure / retry reasons (ModelFailureReason / ModelRetryReason).
const (
	ReasonRateLimited           = "rate_limited"
	ReasonProviderOverloaded    = "provider_overloaded"
	ReasonServerError           = "server_error"
	ReasonNetworkError          = "network_error"
	ReasonTimeout               = "timeout"
	ReasonStreamIdleTimeout     = "stream_idle_timeout"
	ReasonStaleConnection       = "stale_connection"
	ReasonAuthRefresh           = "auth_refresh"
	ReasonReasoningSigRepair    = "reasoning_signature_repair"
	ReasonOffpeakQueued         = "offpeak_queued"
	ReasonAuthFailed            = "auth_failed"
	ReasonCancelled             = "cancelled"
	ReasonContextExceeded       = "context_exceeded"
	ReasonInvalidRequest        = "invalid_request"
	ReasonProviderNotConfigured = "provider_not_configured"
	ReasonProxyError            = "proxy_error"
	ReasonTlsError              = "tls_error"
	ReasonUnknown               = "unknown"
)

// Failure is the classified model failure (ClassifiedModelFailure).
type Failure struct {
	Code         string
	Message      string
	Reason       string
	RetryReason  string
	Retryable    bool
	RetryAfterMs *int64
	StatusCode   *int
}

// ClassifyFailure ports classifyModelFailure. The error is a Go error whose shape
// the transport adapter fills in (see ErrorAttrs). abortSignal is represented by
// the aborted flag.
func ClassifyFailure(err error, aborted bool) Failure {
	attrs := attrsOf(err)
	statusCode := getStatusCode(attrs)
	code := getErrorCode(attrs)
	headers := getResponseHeaders(attrs)
	retryAfterMs := ParseRetryAfterMs(headers)

	if code == CodeInvalidModelResponse {
		return Failure{
			Code:        CodeInvalidModelResponse,
			Message:     errMessage(err, "Model returned an invalid response."),
			Reason:      ReasonInvalidRequest,
			RetryReason: ReasonNetworkError,
			Retryable:   false,
			StatusCode:  statusCode,
		}
	}
	if code == CodeInvalidModelRequest {
		return Failure{
			Code:        CodeInvalidModelRequest,
			Message:     errMessage(err, "Model request configuration is invalid."),
			Reason:      ReasonInvalidRequest,
			RetryReason: ReasonNetworkError,
			Retryable:   false,
			StatusCode:  statusCode,
		}
	}
	if isAbortFailure(attrs, aborted) {
		return Failure{
			Code:        CodeModelRequestCancelled,
			Message:     "Model request was cancelled.",
			Reason:      ReasonCancelled,
			RetryReason: ReasonNetworkError,
			Retryable:   false,
			StatusCode:  statusCode,
		}
	}
	if IsModelStreamIdleTimeoutError(err) {
		return Failure{
			Code:         CodeModelRequestTimeout,
			Message:      errMessage(err, "Model stream stalled."),
			Reason:       ReasonStreamIdleTimeout,
			RetryReason:  ReasonStreamIdleTimeout,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   statusCode,
		}
	}

	if biz, ok := findProviderBusinessError(attrs); ok {
		if f, ok := classifyProviderBusinessFailure(biz, statusCode, retryAfterMs); ok {
			return f
		}
	}
	if biz, ok := readMappedAiSdkProviderBusinessError(attrs); ok {
		if f, ok := classifyProviderBusinessFailure(biz, statusCode, retryAfterMs); ok {
			return f
		}
	}
	if f, ok := classifyProviderBusinessFailureFromAPICallBody(attrs, statusCode, retryAfterMs); ok {
		return f
	}

	if isTimeoutFailure(attrs, code, statusCode) {
		return Failure{
			Code:         CodeModelRequestTimeout,
			Message:      "Model request timed out.",
			Reason:       ReasonTimeout,
			RetryReason:  ReasonTimeout,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   statusCode,
		}
	}
	if statusCode != nil && *statusCode == 429 {
		return Failure{
			Code:         CodeModelRateLimited,
			Message:      "Provider rate limited the model request.",
			Reason:       ReasonRateLimited,
			RetryReason:  ReasonRateLimited,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   statusCode,
		}
	}
	if statusCode != nil && *statusCode == 529 {
		return Failure{
			Code:         CodeModelRequestFailed,
			Message:      "Provider is overloaded.",
			Reason:       ReasonProviderOverloaded,
			RetryReason:  ReasonProviderOverloaded,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   statusCode,
		}
	}
	if statusCode != nil && (*statusCode == 401 || *statusCode == 403) {
		return Failure{
			Code:        CodeProviderNotConfigured,
			Message:     "Provider authentication failed.",
			Reason:      ReasonAuthFailed,
			RetryReason: ReasonAuthRefresh,
			Retryable:   false,
			StatusCode:  statusCode,
		}
	}
	if statusCode != nil && (*statusCode == 400 || *statusCode == 422) {
		contextExceeded := isContextExceededFailure(attrs)
		if contextExceeded {
			return Failure{
				Code:        CodeModelContextExceeded,
				Message:     "Model request exceeded the provider context window.",
				Reason:      ReasonContextExceeded,
				RetryReason: ReasonNetworkError,
				Retryable:   false,
				StatusCode:  statusCode,
			}
		}
		return Failure{
			Code:        CodeInvalidModelRequest,
			Message:     "Provider rejected the model request.",
			Reason:      ReasonInvalidRequest,
			RetryReason: ReasonNetworkError,
			Retryable:   false,
			StatusCode:  statusCode,
		}
	}
	if isContextExceededFailure(attrs) {
		return Failure{
			Code:        CodeModelContextExceeded,
			Message:     "Model request exceeded the provider context window.",
			Reason:      ReasonContextExceeded,
			RetryReason: ReasonNetworkError,
			Retryable:   false,
			StatusCode:  statusCode,
		}
	}
	if statusCode != nil && *statusCode >= 500 {
		return Failure{
			Code:         CodeModelRequestFailed,
			Message:      "Provider returned a server error.",
			Reason:       ReasonServerError,
			RetryReason:  ReasonServerError,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   statusCode,
		}
	}
	if isTLSFailure(code) {
		return Failure{
			Code:        CodeModelRequestFailed,
			Message:     "TLS validation failed for the provider request.",
			Reason:      ReasonTlsError,
			RetryReason: ReasonNetworkError,
			Retryable:   false,
			StatusCode:  statusCode,
		}
	}
	if isProxyFailure(code) {
		return Failure{
			Code:         CodeModelRequestFailed,
			Message:      "Proxy connection failed for the provider request.",
			Reason:       ReasonProxyError,
			RetryReason:  ReasonNetworkError,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   statusCode,
		}
	}
	if isNetworkFailure(code) {
		return Failure{
			Code:         CodeModelRequestFailed,
			Message:      "Network connection failed for the provider request.",
			Reason:       ReasonNetworkError,
			RetryReason:  ReasonNetworkError,
			Retryable:    true,
			RetryAfterMs: retryAfterMs,
			StatusCode:   statusCode,
		}
	}

	retryable := isProviderMarkedRetryable(attrs)
	message := "Model request failed."
	reason := ReasonUnknown
	retryReason := ReasonNetworkError
	if retryable {
		message = "Provider marked the model request as retryable."
		reason = ReasonServerError
		retryReason = ReasonServerError
	}
	return Failure{
		Code:         CodeModelRequestFailed,
		Message:      message,
		Reason:       reason,
		RetryReason:  retryReason,
		Retryable:    retryable,
		RetryAfterMs: retryAfterMs,
		StatusCode:   statusCode,
	}
}

// IsRetryableFailure ports isRetryableFailure.
func IsRetryableFailure(f Failure) bool {
	return f.Retryable && f.Reason != ReasonCancelled
}

func errMessage(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	if msg := err.Error(); msg != "" {
		return msg
	}
	return fallback
}
