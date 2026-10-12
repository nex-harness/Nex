package modelclient

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

type failureVector struct {
	Name        string          `json:"name"`
	Error       json.RawMessage `json:"error"`
	AbortSignal bool            `json:"abortSignal"`
	Result      struct {
		Code         string `json:"code"`
		Message      string `json:"message"`
		Reason       string `json:"reason"`
		RetryReason  string `json:"retryReason"`
		Retryable    bool   `json:"retryable"`
		RetryAfterMs *int64 `json:"retryAfterMs"`
		StatusCode   *int   `json:"statusCode"`
	} `json:"result"`
}

type failureErrorDoc struct {
	Kind                string            `json:"kind"`
	StatusCode          *int              `json:"statusCode"`
	ResponseStatus      *int              `json:"responseStatus"`
	Code                string            `json:"code"`
	Name                string            `json:"name"`
	Message             string            `json:"message"`
	ResponseBody        string            `json:"responseBody"`
	ResponseHeaders     map[string]string `json:"responseHeaders"`
	IsRetryable         *bool             `json:"isRetryable"`
	Data                json.RawMessage   `json:"data"`
	ProviderCode        string            `json:"providerCode"`
	ProviderMessage     string            `json:"providerMessage"`
	ResponseBodySummary json.RawMessage   `json:"responseBodySummary"`
	IdleMs              *int64            `json:"idleMs"`
	TimeoutMs           *int64            `json:"timeoutMs"`
	Outer               *struct {
		Kind            string            `json:"kind"`
		StatusCode      *int              `json:"statusCode"`
		Message         string            `json:"message"`
		ResponseHeaders map[string]string `json:"responseHeaders"`
	} `json:"outer"`
}

const defaultIdleTimeoutMs = 600_000

// makeError builds a Go error matching the vector's error descriptor. The shapes
// mirror what the Node transport produces (APICallError, plain Error, cause,
// ProviderBusinessError, ModelStreamIdleTimeoutError).
func makeError(t *testing.T, raw json.RawMessage) error {
	t.Helper()
	var doc failureErrorDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse error doc: %v", err)
	}
	switch doc.Kind {
	case "apiCall":
		attrs := ErrorAttrs{
			Name:            "APICallError",
			Message:         doc.Message,
			StatusCode:      doc.StatusCode,
			ResponseHeaders: doc.ResponseHeaders,
			IsRetryable:     doc.IsRetryable,
		}
		if doc.ResponseBody != "" {
			var parsed any
			if err := json.Unmarshal([]byte(doc.ResponseBody), &parsed); err == nil {
				attrs.ResponseBody = parsed
				attrs.ResponseBodyText = doc.ResponseBody
			}
		}
		if len(doc.Data) > 0 && string(doc.Data) != "null" {
			var parsed any
			if err := json.Unmarshal(doc.Data, &parsed); err == nil {
				attrs.Data = parsed
			}
		}
		return NewAttrError(attrs)
	case "plain":
		name := doc.Name
		if name == "" {
			name = "Error"
		}
		return NewAttrError(ErrorAttrs{
			Name:            name,
			Code:            doc.Code,
			Message:         doc.Message,
			ResponseHeaders: doc.ResponseHeaders,
			IsRetryable:     doc.IsRetryable,
		})
	case "cause":
		var inner ErrorAttrs
		if doc.Outer != nil {
			inner = ErrorAttrs{
				Name:            "APICallError",
				Message:         doc.Outer.Message,
				StatusCode:      doc.Outer.StatusCode,
				ResponseHeaders: doc.Outer.ResponseHeaders,
			}
		}
		return NewAttrError(ErrorAttrs{
			Name:    doc.Name,
			Code:    doc.Code,
			Message: doc.Message,
			Cause:   NewAttrError(inner),
		})
	case "providerBusiness":
		// ProviderBusinessError（model-execution.ts）：外层 code 是包装码，真实业务码在
		// providerCode 或 responseBodySummary 的深层结构里。
		attrs := ErrorAttrs{
			Name:               "ProviderBusinessError",
			Code:               providerBusinessErrorWrapperCode,
			Message:            providerBusinessFallbackMessage(doc.ProviderMessage),
			StatusCode:         doc.StatusCode,
			ResponseStatus:     doc.ResponseStatus,
			ResponseHeaders:    doc.ResponseHeaders,
			ProviderCode:       doc.ProviderCode,
			HasProviderCode:    doc.ProviderCode != "",
			ProviderMessage:    normalizeProviderMessageField(doc.ProviderMessage),
			IsProviderBusiness: true,
		}
		if len(doc.ResponseBodySummary) > 0 && string(doc.ResponseBodySummary) != "null" {
			var parsed any
			if err := json.Unmarshal(doc.ResponseBodySummary, &parsed); err == nil {
				attrs.ResponseBodySummary = parsed
			}
		}
		return NewAttrError(attrs)
	case "idleTimeout":
		timeout := defaultIdleTimeoutMs
		if doc.TimeoutMs != nil {
			timeout = int(*doc.TimeoutMs)
		}
		idle := int64(timeout)
		if doc.IdleMs != nil {
			idle = *doc.IdleMs
		}
		return &StreamIdleTimeoutError{IdleMs: idle, TimeoutMs: int64(timeout)}
	}
	t.Fatalf("unknown error kind %q", doc.Kind)
	return nil
}

func providerBusinessFallbackMessage(providerMessage string) string {
	if normalized := normalizeProviderMessageField(providerMessage); normalized != "" {
		return normalized
	}
	return "Provider returned a business error."
}

// readFailureVectors loads one generated vector file. Missing files skip the
// caller rather than fail so the suite still runs before the vector step.
func readFailureVectors(t *testing.T, file string) []failureVector {
	t.Helper()
	var vectors []failureVector
	readJSONFile(t, filepath.Join(vectorDir(t), "vectors", file), &vectors)
	return vectors
}

func assertFailureVector(t *testing.T, v failureVector) {
	t.Helper()
	err := makeError(t, v.Error)
	got := ClassifyFailure(err, v.AbortSignal)
	if got.Code != v.Result.Code {
		t.Errorf("code: got %q want %q", got.Code, v.Result.Code)
	}
	if got.Message != v.Result.Message {
		t.Errorf("message: got %q want %q", got.Message, v.Result.Message)
	}
	if got.Reason != v.Result.Reason {
		t.Errorf("reason: got %q want %q", got.Reason, v.Result.Reason)
	}
	if got.RetryReason != v.Result.RetryReason {
		t.Errorf("retryReason: got %q want %q", got.RetryReason, v.Result.RetryReason)
	}
	if got.Retryable != v.Result.Retryable {
		t.Errorf("retryable: got %v want %v", got.Retryable, v.Result.Retryable)
	}
	if !int64PtrEqual(got.RetryAfterMs, v.Result.RetryAfterMs) {
		t.Errorf("retryAfterMs: got %v want %v", derefInt64(got.RetryAfterMs), derefInt64(v.Result.RetryAfterMs))
	}
	if !intPtrEqual(got.StatusCode, v.Result.StatusCode) {
		t.Errorf("statusCode: got %v want %v", derefInt(got.StatusCode), derefInt(v.Result.StatusCode))
	}
	if IsRetryableFailure(got) != (v.Result.Retryable && v.Result.Reason != ReasonCancelled) {
		t.Errorf("isRetryableFailure disagrees")
	}
}

func TestFailureClassifierMatchesVectors(t *testing.T) {
	base := readFailureVectors(t, "model-failure.json")
	extended := readFailureVectors(t, "model-failure-extended.json")
	all := append(append([]failureVector{}, base...), extended...)
	if len(base) < 40 {
		t.Fatalf("expected >= 40 base failure vectors, got %d", len(base))
	}
	if len(all) < 80 {
		t.Fatalf("expected >= 80 failure vectors in total, got %d", len(all))
	}
	seen := map[string]bool{}
	for _, v := range all {
		if seen[v.Name] {
			t.Fatalf("duplicate vector name %q", v.Name)
		}
		seen[v.Name] = true
		t.Run(v.Name, func(t *testing.T) {
			assertFailureVector(t, v)
		})
	}
}

// TestFailureClassifierCoversEveryClass keeps the table honest: it asserts the
// corpus reaches every failure reason the classifier can emit, so a future
// vector regeneration that silently drops a branch fails here.
func TestFailureClassifierCoversEveryClass(t *testing.T) {
	all := append(
		readFailureVectors(t, "model-failure.json"),
		readFailureVectors(t, "model-failure-extended.json")...,
	)
	// The classifier can emit exactly these reasons; auth_refresh and cancelled
	// only ever appear as retryReason, cancelled only via the abort branches.
	wantReasons := []string{
		ReasonRateLimited, ReasonProviderOverloaded, ReasonServerError, ReasonNetworkError,
		ReasonTimeout, ReasonStreamIdleTimeout, ReasonAuthFailed, ReasonContextExceeded,
		ReasonInvalidRequest, ReasonProxyError, ReasonTlsError, ReasonUnknown, ReasonCancelled,
	}
	wantRetryReasons := []string{
		ReasonRateLimited, ReasonProviderOverloaded, ReasonServerError, ReasonNetworkError,
		ReasonTimeout, ReasonStreamIdleTimeout, ReasonAuthRefresh,
	}
	gotReasons := map[string]bool{}
	gotRetryReasons := map[string]bool{}
	for _, v := range all {
		gotReasons[v.Result.Reason] = true
		gotRetryReasons[v.Result.RetryReason] = true
	}
	for _, r := range wantReasons {
		if !gotReasons[r] {
			t.Errorf("no vector covers reason %q", r)
		}
	}
	for _, r := range wantRetryReasons {
		if !gotRetryReasons[r] {
			t.Errorf("no vector covers retryReason %q", r)
		}
	}
	// Both retryable and terminal outcomes must be represented, or the table
	// proves nothing about the retry decision.
	var retryable, terminal int
	for _, v := range all {
		if v.Result.Retryable {
			retryable++
		} else {
			terminal++
		}
	}
	if retryable < 20 || terminal < 20 {
		t.Errorf("unbalanced corpus: %d retryable, %d terminal", retryable, terminal)
	}
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func derefInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func derefInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
