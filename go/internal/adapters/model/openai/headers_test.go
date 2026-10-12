package openai

import (
	"strings"
	"testing"
)

// capturedHeaders is the exact header set the Node agent sent to the mock
// provider for S13 request-0 (scripts/go-rewrite/record, NEX_REC_BODY_DIR). The
// transport-level headers (host, connection, content-length, accept*,
// sec-fetch-mode) are excluded because they are set by undici/Go's http stack.
var capturedHeaders = map[string]string{
	"authorization":      "Bearer sk-test-fake",
	"content-type":       "application/json",
	"http-referer":       "https://zcode.z.ai",
	"user-agent":         "Nex/1.2.3 ai-sdk/provider-utils/4.0.39 runtime/node.js/24",
	"x-client-language":  "en-US",
	"x-client-timezone":  "UTC",
	"x-nex-agent":        "glm",
	"x-nex-app-version":  "1.2.3",
	"x-nex-session-type": "main",
	"x-nex-trace-id":     "90e1fb98-f5e7-453e-bce4-5e2bbd462060",
	"x-os-category":      "linux",
	"x-os-version":       "7.0.14-12-pve",
	"x-platform":         "linux-x64",
	"x-query-id":         "cmd-0001",
	"x-release-channel":  "production",
	"x-request-id":       "5aa181b9-444e-4a56-a8ce-8bcbdc3cd292",
	"x-session-id":       "778ef4f8-8ed9-4ec1-bf63-50a83b8573e2",
	"x-title":            "Z Code@electron",
}

func sampleHeaderInput() HeaderInput {
	return HeaderInput{
		Authorization:  "Bearer sk-test-fake",
		HTTPReferer:    "https://zcode.z.ai",
		UserAgent:      "Nex/1.2.3",
		AppVersion:     "1.2.3",
		Title:          "Z Code@electron",
		ReleaseChannel: "production",
		ClientLanguage: "en-US",
		ClientTimezone: "UTC",
		NexAgent:       "glm",
		Platform:       "linux-x64",
		OSCategory:     "linux",
		OSVersion:      "7.0.14-12-pve",
		RequestID:      "5aa181b9-444e-4a56-a8ce-8bcbdc3cd292",
		TraceID:        "90e1fb98-f5e7-453e-bce4-5e2bbd462060",
		SessionType:    "main",
		QueryID:        "cmd-0001",
		SessionID:      "778ef4f8-8ed9-4ec1-bf63-50a83b8573e2",
	}
}

func TestBuildHeadersMatchCapture(t *testing.T) {
	got := BuildHeaders(sampleHeaderInput())
	for name, want := range capturedHeaders {
		if want == "application/json" {
			continue // content-type is set at request time, not in the static map
		}
		if got[name] != want {
			t.Errorf("header %q: got %q want %q", name, got[name], want)
		}
	}
	// No unexpected Nex headers beyond the capture plus content-type.
	for name := range got {
		if _, ok := capturedHeaders[name]; !ok {
			t.Errorf("unexpected header %q = %q", name, got[name])
		}
	}
}

func TestBuildHeadersOmitsEmptyOptional(t *testing.T) {
	in := sampleHeaderInput()
	in.AppVersion = ""
	in.Platform = ""
	in.OSVersion = ""
	in.QueryID = ""
	in.SessionID = ""
	got := BuildHeaders(in)
	for _, name := range []string{"x-nex-app-version", "x-platform", "x-os-version", "x-query-id", "x-session-id"} {
		if _, ok := got[name]; ok {
			t.Errorf("expected %q to be omitted, got %q", name, got[name])
		}
	}
}

func TestBuildHeadersAttributionWinsOverExtra(t *testing.T) {
	in := sampleHeaderInput()
	in.Extra = map[string]string{
		"x-request-id": "should-not-win",
		"x-custom":     "kept",
	}
	got := BuildHeaders(in)
	if got["x-request-id"] != in.RequestID {
		t.Errorf("attribution header was overridden: got %q", got["x-request-id"])
	}
	if got["x-custom"] != "kept" {
		t.Errorf("extra header lost: %q", got["x-custom"])
	}
}

func TestUserAgentSuffix(t *testing.T) {
	got := userAgent(HeaderInput{UserAgent: "Nex/1.2.3"})
	if !strings.HasPrefix(got, "Nex/1.2.3 ai-sdk/provider-utils/") {
		t.Fatalf("unexpected user agent %q", got)
	}
}
