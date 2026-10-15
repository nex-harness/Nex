// httpclient_test.go (step 3.8): the transport contract. Every endpoint is a
// loopback httptest server — no real share service is contacted from a test.
// The error-kind mapping and the auth/request-id behaviour are UI contract.
package conversationshare

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCapabilitiesRequiresAuthenticationBeforeAnyRequest(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewClient(ClientOptions{
		BaseURL: server.URL, Doer: server.Client(),
		Token: func(context.Context) (string, error) { return "   ", nil },
	})
	if _, err := client.GetCapabilities(context.Background()); err == nil {
		t.Fatal("expected an authentication error")
	}
	if hits != 0 {
		t.Fatalf("an unauthenticated request must not reach the network (%d hits)", hits)
	}
}

func TestRequestCarriesAuthAndRequestIDHeaders(t *testing.T) {
	var seenAuth, seenRequestID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenRequestID = r.Header.Get(requestIDHeaderName)
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":`+string(mustJSON(defaultCapabilities()))+`}`)
	}))
	defer server.Close()
	client := NewClient(ClientOptions{
		BaseURL: server.URL, Doer: server.Client(),
		Token: func(context.Context) (string, error) { return "tok", nil },
	})
	if _, err := client.GetCapabilities(context.Background()); err != nil {
		t.Fatalf("getCapabilities: %v", err)
	}
	if seenAuth != "Bearer tok" {
		t.Fatalf("authorization = %q", seenAuth)
	}
	if !safeRequestIDRe.MatchString(seenRequestID) {
		t.Fatalf("request id %q is not a safe header value", seenRequestID)
	}
}

func TestErrorEnvelopeMapsCodesToKinds(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		wantKind string
		wantMsg  string
		wantCode int64
	}{
		{400, `{"code":3206,"msg":"disclosure required"}`, KindDisclosureRequired, "disclosure required", 3206},
		{403, `{"code":3214,"msg":"import not allowed"}`, KindImportNotAllowed, "import not allowed", 3214},
		{404, `{"code":3211,"msg":"not found"}`, KindNotFound, "not found", 3211},
		{410, `{"code":3212,"msg":"expired"}`, KindExpired, "expired", 3212},
		{413, `{"code":3209,"msg":"limit exceeded"}`, KindLimitExceeded, "limit exceeded", 3209},
		// An unrecognised business code keeps msg+status but lands on unknown.
		{400, `{"code":9999,"msg":"brand new failure"}`, KindUnknown, "brand new failure", 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.wantKind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = io.WriteString(w, testCase.body)
			}))
			defer server.Close()
			client := NewClient(ClientOptions{
				BaseURL: server.URL, Doer: server.Client(),
				Token: func(context.Context) (string, error) { return "tok", nil },
			})
			_, err := client.GetCapabilities(context.Background())
			typed, ok := isClientError(err)
			if !ok || typed.kind != testCase.wantKind {
				t.Fatalf("kind = %v (%v), want %s", typed, err, testCase.wantKind)
			}
			if typed.status != int64(testCase.status) {
				t.Fatalf("status = %d", typed.status)
			}
			if testCase.wantCode != 0 && (typed.code != testCase.wantCode || !typed.hasCode) {
				t.Fatalf("code = %d/%v, want %d", typed.code, typed.hasCode, testCase.wantCode)
			}
			if typed.message != testCase.wantMsg {
				t.Fatalf("server msg must be preserved verbatim: %q", typed.message)
			}
		})
	}
}

func TestErrorEnvelopeFallbacks(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantKind string
	}{
		{"401 empty body", 401, "", KindAuthenticationRequired},
		{"502 html", 502, "<html>bad gateway</html>", KindNetwork},
		{"400 invalid json", 400, "not json at all", KindInvalidContract},
		{"429 empty body", 429, "", KindRateLimited},
		{"503 empty body", 503, "", KindNetwork},
		{"404 empty body", 404, "", KindUnknown},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = io.WriteString(w, testCase.body)
			}))
			defer server.Close()
			client := NewClient(ClientOptions{
				BaseURL: server.URL, Doer: server.Client(),
				Token: func(context.Context) (string, error) { return "tok", nil },
			})
			_, err := client.GetCapabilities(context.Background())
			typed, ok := isClientError(err)
			if !ok || typed.kind != testCase.wantKind {
				t.Fatalf("kind = %v (%v), want %s", typed, err, testCase.wantKind)
			}
		})
	}
}

func TestTransportFailureBecomesANetworkErrorWithoutHostLeakage(t *testing.T) {
	// Port 1 on loopback refuses connections; the message must stay generic.
	client := NewClient(ClientOptions{
		BaseURL: "http://127.0.0.1:1", Doer: &http.Client{Timeout: time.Second},
		Token: func(context.Context) (string, error) { return "tok", nil },
	})
	_, err := client.GetCapabilities(context.Background())
	typed, ok := isClientError(err)
	if !ok || typed.kind != KindNetwork {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(typed.message, "127.0.0.1") {
		t.Fatalf("the network message leaked the host: %q", typed.message)
	}
}

func TestNewerSchemaVersionAsksForAnUpgrade(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"schema_version":99,`+
			`"share":{"title":"t","access_mode":"public_readonly","created_at":1,"expires_at":2},`+
			`"rows":[],"artifacts":[],`+
			`"integrity":{"projection_sha256":"x","artifact_set_sha256":"y"}}}`)
	}))
	defer server.Close()
	client := NewClient(ClientOptions{
		BaseURL: server.URL, Doer: server.Client(),
		Token: func(context.Context) (string, error) { return "tok", nil },
	})
	_, err := client.GetPreview(context.Background(), "CODE")
	typed, ok := isClientError(err)
	if !ok || typed.kind != KindUnsupportedSchema {
		t.Fatalf("a newer schema version must ask for an upgrade, got %v", err)
	}
}

func TestMalformedSuccessEnvelopeIsAContractError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":17,"msg":"weird","data":{}}`)
	}))
	defer server.Close()
	client := NewClient(ClientOptions{
		BaseURL: server.URL, Doer: server.Client(),
		Token: func(context.Context) (string, error) { return "tok", nil },
	})
	_, err := client.GetCapabilities(context.Background())
	typed, ok := isClientError(err)
	if !ok || typed.kind != KindInvalidContract {
		t.Fatalf("error = %v", err)
	}
}

func TestParseRetryAfterMs(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  int64
		ok    bool
	}{
		{"delta seconds", "42", 42_000, true},
		{"delta seconds padded", " 7 ", 7_000, true},
		{"zero", "0", 0, false},
		{"imf-fixdate future", "Thu, 15 Oct 2026 12:00:30 GMT", 30_000, true},
		{"imf-fixdate past", "Thu, 15 Oct 2026 11:59:00 GMT", 0, false},
		{"rfc850", "Thursday, 15-Oct-26 12:00:30 GMT", 30_000, true},
		{"asctime", "Thu Oct 15 12:00:30 2026", 30_000, true},
		{"iso is not an http date", "2026-10-15T12:00:30Z", 0, false},
		{"garbage", "soon", 0, false},
		{"empty", "", 0, false},
		{"overflowing digits", "99999999999999999999", 0, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := parseRetryAfterMs(testCase.value, now)
			if got != testCase.want || ok != testCase.ok {
				t.Fatalf("parseRetryAfterMs(%q) = %d/%v, want %d/%v",
					testCase.value, got, ok, testCase.want, testCase.ok)
			}
		})
	}
}

func TestSafetyCheckPendingCarriesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("retry-after", "3")
		w.Header().Set(requestIDHeaderName, "req-abc")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":3215,"msg":"safety check pending"}`)
	}))
	defer server.Close()
	client := NewClient(ClientOptions{
		BaseURL: server.URL, Doer: server.Client(),
		Token: func(context.Context) (string, error) { return "tok", nil },
	})
	_, err := client.Confirm(context.Background(), "prep-1", &ConfirmRequest{})
	typed, ok := isClientError(err)
	if !ok || typed.kind != KindSafetyCheckPending {
		t.Fatalf("error = %v", err)
	}
	if !typed.hasRetry || typed.retryAfterMs != 3000 {
		t.Fatalf("retryAfterMs = %d/%v", typed.retryAfterMs, typed.hasRetry)
	}
	if typed.requestID != "req-abc" {
		t.Fatalf("requestID = %q", typed.requestID)
	}
}

func TestUploadTimeoutScalesWithPayloadAndFloorsAtTheDefault(t *testing.T) {
	// Ceiling division means even a tiny upload adds one second of slack, and
	// a caller-supplied floor above that still wins.
	if got := computeUploadTimeoutMs(0, defaultTimeoutMs); got != defaultTimeoutMs {
		t.Fatalf("empty upload timeout = %d, want the floor %d", got, defaultTimeoutMs)
	}
	if got := computeUploadTimeoutMs(1024, defaultTimeoutMs); got != defaultTimeoutMs+1000 {
		t.Fatalf("small upload timeout = %d", got)
	}
	// 10MB at the 128KB/s floor needs 80s on top of the 30s base.
	want := int64(30_000 + (10<<20)/uploadMinThroughputBytesPerSec*1000)
	if got := computeUploadTimeoutMs(10<<20, defaultTimeoutMs); got != want {
		t.Fatalf("large upload timeout = %d, want %d", got, want)
	}
	// A caller-supplied higher floor is respected.
	if got := computeUploadTimeoutMs(1<<20, 90_000); got != 90_000 {
		t.Fatalf("floor not respected: %d", got)
	}
}

func TestUploadSendsDescriptorAndFileParts(t *testing.T) {
	var fields map[string]string
	var fileName, fileContent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		fields = map[string]string{}
		for key := range r.MultipartForm.Value {
			fields[key] = r.FormValue(key)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("file part: %v", err)
		} else {
			defer file.Close()
			payload, _ := io.ReadAll(file)
			fileName, fileContent = header.Filename, string(payload)
		}
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"artifact_id":"share-artifact-1",`+
			`"size_bytes":5,"sha256":"aa","status":"received","safety_status":"passed"}}`)
	}))
	defer server.Close()
	client := NewClient(ClientOptions{
		BaseURL: server.URL, Doer: server.Client(),
		Token: func(context.Context) (string, error) { return "tok", nil },
	})
	upload, err := client.UploadArtifact(context.Background(), "prep-1", ArtifactDescriptor{
		ArtifactID: "share-artifact-1", DisplayName: "report.pdf",
		MimeType: "application/pdf", Sha256: "aa", SizeBytes: 5,
	}, "report.pdf", []byte("hello"))
	if err != nil {
		t.Fatalf("uploadArtifact: %v", err)
	}
	if upload.ArtifactID != "share-artifact-1" || upload.SafetyStatus != "passed" {
		t.Fatalf("upload = %#v", upload)
	}
	if !strings.Contains(fields["descriptor"], `"artifact_id":"share-artifact-1"`) {
		t.Fatalf("descriptor part = %q", fields["descriptor"])
	}
	if fileName != "report.pdf" || fileContent != "hello" {
		t.Fatalf("file part = %q / %q", fileName, fileContent)
	}
}

func TestNormalizeRequestIDRejectsUnsafeValues(t *testing.T) {
	safe := []string{"req-1", "a.b:c_d", strings.Repeat("x", 128)}
	for _, value := range safe {
		if normalizeRequestID(" "+value+" ") != value {
			t.Errorf("normalizeRequestID(%q) should keep it", value)
		}
	}
	for _, value := range []string{"", "   ", "has space", "has/slash", strings.Repeat("x", 129)} {
		if normalizeRequestID(value) != "" {
			t.Errorf("normalizeRequestID(%q) should drop it", value)
		}
	}
}
