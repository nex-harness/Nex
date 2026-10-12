package modelclient

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolInput(t *testing.T) {
	cases := []struct {
		name    string
		input   any
		want    string // canonical JSON
		recover bool
	}{
		{"undefined", nil, "{}", false},
		{"explicit-null", NullInput{}, "{}", true},
		{"empty-string", "", "{}", false},
		{"whitespace-string", "   ", "{}", true},
		{"bad-json", "{not json}", "{}", true},
		{"json-null", "null", "{}", true},
		{"bom-prefixed", "\ufeff{\"a\":1}", `{"a":1}`, false},
		{"valid-object", `{"file_path":"/x"}`, `{"file_path":"/x"}`, false},
		{"valid-array", "[1,2,3]", "[1,2,3]", false},
		{"number", "42", "42", false},
		{"bool", "true", "true", false},
		{"trailing-garbage", `{"a":1} trailing`, "{}", true},
		{"already-parsed", map[string]any{"a": json.Number("1")}, `{"a":1}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, status := NormalizeToolInput(c.input)
			if status.Recovered != c.recover {
				t.Errorf("recovered: got %v want %v", status.Recovered, c.recover)
			}
			s, err := MarshalCanonicalInput(got)
			if err != nil {
				t.Fatal(err)
			}
			if !jsonEqualString([]byte(s), c.want) {
				t.Errorf("value: got %s want %s", s, c.want)
			}
		})
	}
}

func TestNormalizeToolName(t *testing.T) {
	cases := []struct {
		name             string
		value            any
		providerExecuted bool
		toolCallID       string
		want             string
		wantErr          bool
	}{
		{"named", "Read", false, "c1", "Read", false},
		{"trimmed", "  Read  ", false, "c1", "Read", false},
		{"empty-with-id", "", false, "c1", "", false},
		{"empty-no-id", "", false, "", "", true},
		{"empty-provider-executed", "", true, "c1", "", true},
		{"non-string", 42, false, "c1", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeToolName(c.value, c.providerExecuted, c.toolCallID)
			if (err != nil) != c.wantErr {
				t.Fatalf("err: got %v wantErr %v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

// TestProviderBusinessDetectionFromBody pins the body-detection rules used by the
// failure classifier for HTTP 200 business bodies and 403 JSON bodies.
func TestProviderBusinessDetectionFromBody(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		found  bool
		code   string
		status *int
	}{
		{"provider-code", `{"providerCode":"3007","message":"plan"}`, true, "3007", nil},
		{"nested-error-code", `{"error":{"code":"3002"}}`, true, "3002", nil},
		{"error-code-field", `{"error_code":"1006"}`, true, "1006", nil},
		{"success-false", `{"success":false,"message":"nope"}`, true, "", nil},
		{"empty", `{}`, false, "", nil},
		{"zero-code", `{"code":"0"}`, false, "", nil},
		{"http-status-code", `{"code":"500","message":"boom"}`, true, "500", intPtr(500)},
		{"wrapper-code", `{"code":"PROVIDER_BUSINESS_ERROR","providerCode":"1234"}`, true, "1234", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var body any
			if err := json.Unmarshal([]byte(c.body), &body); err != nil {
				t.Fatal(err)
			}
			got := ReadProviderBusinessFailureFromBody(body)
			if got.Found != c.found {
				t.Fatalf("found: got %v want %v", got.Found, c.found)
			}
			if !c.found {
				return
			}
			if got.ProviderCode != c.code {
				t.Errorf("code: got %q want %q", got.ProviderCode, c.code)
			}
			if !intPtrEqual(got.StatusCode, c.status) {
				t.Errorf("status: got %v want %v", derefInt(got.StatusCode), derefInt(c.status))
			}
		})
	}
}

// TestS13ScenarioRetryClassification reproduces the classification S13 records:
// an HTTP 429 with retry-after: 0 is a retryable rate limit with a zero delay.
func TestS13ScenarioRetryClassification(t *testing.T) {
	err := NewAttrError(ErrorAttrs{
		Name:            "APICallError",
		StatusCode:      intPtr(429),
		ResponseHeaders: map[string]string{"retry-after": "0"},
	})
	f := ClassifyFailure(err, false)
	if f.Code != CodeModelRateLimited {
		t.Fatalf("code: got %q", f.Code)
	}
	if f.Reason != ReasonRateLimited {
		t.Fatalf("reason: got %q", f.Reason)
	}
	if !f.Retryable {
		t.Fatal("expected retryable")
	}
	if f.RetryAfterMs == nil || *f.RetryAfterMs != 0 {
		t.Fatalf("retryAfterMs: got %v want 0", derefInt64(f.RetryAfterMs))
	}
	delay := CalculateRetryDelay(DefaultRetryOptions(), 1, f.RetryAfterMs, nil)
	if delay != 0 {
		t.Fatalf("delay: got %d want 0", delay)
	}
}

// TestMidStreamDisconnectClassification covers the dropAfterChunks cases: a
// mid-body transport reset classified by isNetworkFailure (ECONNRESET,
// UND_ERR_SOCKET) is a retryable network failure. EPIPE/CONNECTIONCLOSED are NOT
// in the generic network set — they are only retryable for compact streams via
// the separate stale-stream gate.
func TestMidStreamDisconnectClassification(t *testing.T) {
	for _, code := range []string{"ECONNRESET", "UND_ERR_SOCKET", "ECONNREFUSED"} {
		err := NewAttrError(ErrorAttrs{Name: "Error", Code: code, Message: "socket closed"})
		f := ClassifyFailure(err, false)
		if f.Reason != ReasonNetworkError {
			t.Errorf("%s: reason got %q want %q", code, f.Reason, ReasonNetworkError)
		}
		if !f.Retryable {
			t.Errorf("%s: expected retryable", code)
		}
	}
	for _, code := range []string{"EPIPE", "ConnectionClosed"} {
		err := NewAttrError(ErrorAttrs{Name: "Error", Code: code, Message: "closed"})
		f := ClassifyFailure(err, false)
		if f.Retryable {
			t.Errorf("%s: expected NOT retryable via generic classifier (reason %q)", code, f.Reason)
		}
	}
}
