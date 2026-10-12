package modelclient

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// ErrorAttrs is the normalized shape the transport adapter fills in for a failed
// model call. It mirrors the fields the Node failure-inspection walks over:
// APICallError's statusCode/responseBody/responseHeaders/data, plus generic
// code/name/cause/isRetryable. A chain of AttrError values reproduces the
// cause-walking of the JS inspectors.
type ErrorAttrs struct {
	// Name is the JS error name ("AbortError", "TimeoutError", "ApiCallError", ...).
	Name string
	// Code is the low-level error code (ECONNRESET, ETIMEDOUT, MODEL_STREAM_IDLE_TIMEOUT, ...).
	Code string
	// Message is the human-readable message.
	Message string
	// StatusCode is an HTTP status if known.
	StatusCode *int
	// ResponseStatus is the transport status retained by ProviderBusinessError.
	ResponseStatus *int
	// ResponseBody is the parsed JSON body (if any) or the raw text.
	ResponseBody any
	// ResponseBodyText is the raw response body text.
	ResponseBodyText string
	// ResponseHeaders are the response headers (case preserved).
	ResponseHeaders map[string]string
	// IsRetryable is the SDK's retry flag (APICallError.isRetryable).
	IsRetryable *bool
	// Data is APICallError.data (parsed error body).
	Data any
	// ProviderCode/providerMessage/requestId are set on ProviderBusinessError.
	ProviderCode      string
	HasProviderCode   bool
	ProviderMessage   string
	ProviderRequestID string
	// ResponseBodySummary is the ProviderBusinessError nested summary.
	ResponseBodySummary any
	// IsProviderBusiness marks a ProviderBusinessError.
	IsProviderBusiness bool
	// Cause is the wrapped error.
	Cause error
}

// AttrError wraps an ErrorAttrs as an error.
type AttrError struct {
	Attrs ErrorAttrs
}

func (e *AttrError) Error() string {
	if e.Attrs.Message != "" {
		return e.Attrs.Message
	}
	return "model error"
}

// NewAttrError builds an error carrying the given attributes.
func NewAttrError(a ErrorAttrs) *AttrError { return &AttrError{Attrs: a} }

// attrsOf returns the attributes of err, following the cause chain to the first
// AttrError. Non-AttrError errors get a minimal shape.
func attrsOf(err error) ErrorAttrs {
	var ae *AttrError
	if errors.As(err, &ae) {
		return ae.Attrs
	}
	if err == nil {
		return ErrorAttrs{}
	}
	return ErrorAttrs{Message: err.Error()}
}

func walkAttrs(err error, seen map[error]bool, fn func(ErrorAttrs) (stop bool)) {
	for err != nil {
		if seen[err] {
			return
		}
		seen[err] = true
		var ae *AttrError
		if errors.As(err, &ae) {
			if fn(ae.Attrs) {
				return
			}
			err = ae.Attrs.Cause
			continue
		}
		return
	}
}

func getStatusCode(attrs ErrorAttrs) *int {
	var result *int
	cur := attrs
	seen := map[error]bool{}
	for {
		if cur.StatusCode != nil {
			return cur.StatusCode
		}
		if cur.Cause == nil || seen[cur.Cause] {
			return result
		}
		seen[cur.Cause] = true
		cur = attrsOf(cur.Cause)
	}
}

func getHTTPResponseStatus(attrs ErrorAttrs) *int {
	cur := attrs
	seen := map[error]bool{}
	for {
		if cur.ResponseStatus != nil {
			return cur.ResponseStatus
		}
		if cur.Cause == nil || seen[cur.Cause] {
			return nil
		}
		seen[cur.Cause] = true
		cur = attrsOf(cur.Cause)
	}
}

func getErrorCode(attrs ErrorAttrs) string {
	cur := attrs
	seen := map[error]bool{}
	for {
		if cur.Code != "" {
			return cur.Code
		}
		if cur.Cause == nil || seen[cur.Cause] {
			return ""
		}
		seen[cur.Cause] = true
		cur = attrsOf(cur.Cause)
	}
}

func getResponseHeaders(attrs ErrorAttrs) map[string]string {
	cur := attrs
	seen := map[error]bool{}
	for {
		if len(cur.ResponseHeaders) > 0 {
			return cur.ResponseHeaders
		}
		if cur.Cause == nil || seen[cur.Cause] {
			return nil
		}
		seen[cur.Cause] = true
		cur = attrsOf(cur.Cause)
	}
}

// getAPICallResponseBody returns the parsed body for an ApiCallError-shaped
// error. In the Go port the transport already parses JSON, so this returns the
// stored ResponseBody.
func getAPICallResponseBody(attrs ErrorAttrs) any {
	return attrs.ResponseBody
}

func isAbortFailure(attrs ErrorAttrs, aborted bool) bool {
	if aborted {
		return true
	}
	cur := attrs
	seen := map[error]bool{}
	for {
		if cur.Name == "AbortError" || cur.Code == "ABORT_ERR" {
			return true
		}
		if cur.Cause == nil || seen[cur.Cause] {
			return false
		}
		seen[cur.Cause] = true
		cur = attrsOf(cur.Cause)
	}
}

func isTimeoutFailure(attrs ErrorAttrs, code string, statusCode *int) bool {
	if statusCode != nil && *statusCode == 408 {
		return true
	}
	switch strings.ToUpper(code) {
	case "ETIMEDOUT", "ETIMEOUT", "UND_ERR_CONNECT_TIMEOUT", "UND_ERR_HEADERS_TIMEOUT", "UND_ERR_BODY_TIMEOUT":
		return true
	}
	cur := attrs
	seen := map[error]bool{}
	for {
		name := strings.ToLower(cur.Name)
		if name == "timeouterror" || name == "timeout_error" {
			return true
		}
		if cur.Cause == nil || seen[cur.Cause] {
			return false
		}
		seen[cur.Cause] = true
		cur = attrsOf(cur.Cause)
	}
}

func isTLSFailure(code string) bool {
	up := strings.ToUpper(code)
	return strings.Contains(up, "CERT") ||
		strings.Contains(up, "TLS") ||
		up == "UNABLE_TO_VERIFY_LEAF_SIGNATURE"
}

func isProxyFailure(code string) bool {
	return strings.Contains(strings.ToUpper(code), "PROXY")
}

func isNetworkFailure(code string) bool {
	switch strings.ToUpper(code) {
	case "ECONNRESET", "ECONNREFUSED", "EAI_AGAIN", "ENOTFOUND", "ENETUNREACH",
		"EHOSTUNREACH", "UND_ERR_SOCKET", "UND_ERR_CONNECT_TIMEOUT":
		return true
	}
	return false
}

func isProviderMarkedRetryable(attrs ErrorAttrs) bool {
	return attrs.IsRetryable != nil && *attrs.IsRetryable
}

func IsModelStreamIdleTimeoutError(err error) bool {
	var idle *StreamIdleTimeoutError
	if errors.As(err, &idle) {
		return true
	}
	attrs := attrsOf(err)
	return attrs.Code == "MODEL_STREAM_IDLE_TIMEOUT" || attrs.Name == "ModelStreamIdleTimeoutError"
}

// StreamIdleTimeoutError reproduces ModelStreamIdleTimeoutError.
type StreamIdleTimeoutError struct {
	IdleMs    int64
	TimeoutMs int64
}

func (e *StreamIdleTimeoutError) Error() string {
	return "Model stream stalled: no event received for " + strconv.FormatInt(e.TimeoutMs, 10) + "ms."
}

// IsDeadlineExceeded reports whether err is a context deadline error, used by the
// transport adapter to map to a timeout.
func IsDeadlineExceeded(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}
