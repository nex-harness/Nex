// errors_test.go (step 3.8): the cross-RPC redaction boundary. Nothing that
// carries a local path, a URL or an over-long string may survive into the
// Renderer payload, and the issue list must be capped at five entries. The
// error kind / reason code strings are UI contract — they are asserted
// verbatim.
package conversationshare

import (
	"encoding/json"
	"strings"
	"testing"
)

// marshalIssues renders an issue list the way it crosses the RPC boundary.
func marshalIssues(t *testing.T, issues []Issue) string {
	t.Helper()
	encoded, err := json.Marshal(issues)
	if err != nil {
		t.Fatalf("marshal issues: %v", err)
	}
	return string(encoded)
}

func ptr[T any](value T) *T { return &value }

func issueCodes(issues []Issue) []string {
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

func TestSanitizedIssuesStripsPathsURLsAndOversizeStrings(t *testing.T) {
	issues := []Issue{
		{
			Code: ReasonArtifactReadFailed, Scope: ScopeArtifact,
			ArtifactDisplayName: ptr("/Users/alice/secret/report.pdf"),
			ArtifactType:        ptr("pdf"),
			Extension:           ptr("pdf"),
			MimeType:            ptr("application/pdf"),
			Actual:              ptr(int64(42)), Limit: ptr(int64(10)),
		},
		{
			Code: ReasonUnsafeURL, Scope: ScopeConversation,
			ArtifactDisplayName: ptr("https://example.invalid/report.pdf"),
		},
		{
			Code: ReasonArtifactTypeNotAllowed, Scope: ScopeArtifact,
			ArtifactType: ptr("zip"),
			MimeType:     ptr("not-a-mime-type"),
			// A >128 char display name loses only that field, never the issue.
			ArtifactDisplayName: ptr(strings.Repeat("n", 200)),
		},
		{
			Code: ReasonUnsafeURL, Scope: ScopeConversation,
			ArtifactType: ptr("C:\\Users\\alice\\report.pdf"),
		},
	}
	sanitized, issueCount, omitted := SanitizedIssues(issues)
	if issueCount != 4 || omitted != 0 {
		t.Fatalf("issueCount=%d omitted=%d, want 4/0", issueCount, omitted)
	}
	if len(sanitized) != 4 {
		t.Fatalf("all four issues must survive with the unsafe field stripped: %v", issueCodes(sanitized))
	}
	encoded := marshalIssues(t, sanitized)
	for _, forbidden := range []string{"/Users/alice", "example.invalid", "C:\\Users", "not-a-mime-type"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("redaction leaked %q: %s", forbidden, encoded)
		}
	}
	if sanitized[2].MimeType != nil {
		t.Fatalf("a malformed mime type must be dropped: %v", sanitized[2].MimeType)
	}
	if sanitized[2].ArtifactDisplayName != nil {
		t.Fatalf("an over-long display name must be dropped: %v", *sanitized[2].ArtifactDisplayName)
	}
	if sanitized[2].ArtifactType == nil || *sanitized[2].ArtifactType != "zip" {
		t.Fatalf("the safe fields must survive: %#v", sanitized[2])
	}
	if sanitized[0].ArtifactDisplayName != nil {
		t.Fatalf("a path-shaped display name must be dropped: %v", sanitized[0].ArtifactDisplayName)
	}
}

func TestSanitizedIssuesCapsAtFiveAndReportsOmitted(t *testing.T) {
	var issues []Issue
	for index := 0; index < 9; index++ {
		issues = append(issues, Issue{Code: ReasonArtifactReadFailed, Scope: ScopeArtifact, Actual: ptr(int64(index))})
	}
	sanitized, issueCount, omitted := SanitizedIssues(issues)
	if len(sanitized) != maxFailureIssues {
		t.Fatalf("kept %d issues, want %d", len(sanitized), maxFailureIssues)
	}
	if issueCount != 9 {
		t.Fatalf("issueCount must report the pre-truncation total: %d", issueCount)
	}
	if omitted != 4 {
		t.Fatalf("omittedIssueCount=%d, want 4", omitted)
	}
}

func TestSanitizedIssuesFiltersAllowedArtifactSummaries(t *testing.T) {
	sanitized, _, _ := SanitizedIssues([]Issue{{
		Code: ReasonArtifactTypeNotAllowed, Scope: ScopeArtifact,
		AllowedFormats: []string{"  PDF (.pdf) ", "https://evil.invalid/x", ""},
		AllowedArtifacts: []AllowedArtifact{
			{Type: "pdf", Extensions: []string{".pdf"}, MimeTypes: []string{"Application/PDF"}, DisplayName: "PDF"},
			{Type: "", Extensions: []string{"pdf"}},
			{Type: "zip", Extensions: []string{}},
			{Type: "/Users/alice", Extensions: []string{"pdf"}},
		},
	}})
	if len(sanitized) != 1 {
		t.Fatalf("issue dropped entirely: %#v", sanitized)
	}
	issue := sanitized[0]
	if len(issue.AllowedFormats) != 1 || issue.AllowedFormats[0] != "PDF (.pdf)" {
		t.Fatalf("allowedFormats not sanitized: %v", issue.AllowedFormats)
	}
	if len(issue.AllowedArtifacts) != 1 {
		t.Fatalf("allowedArtifacts not filtered: %#v", issue.AllowedArtifacts)
	}
	allowed := issue.AllowedArtifacts[0]
	if allowed.Type != "pdf" || len(allowed.Extensions) != 1 || allowed.Extensions[0] != "pdf" {
		t.Fatalf("allowed artifact not normalized: %#v", allowed)
	}
	if len(allowed.MimeTypes) != 1 || allowed.MimeTypes[0] != "application/pdf" {
		t.Fatalf("mime type not lowercased: %v", allowed.MimeTypes)
	}
}

func TestInferFailureReasonCodeCoversTheTSVocabulary(t *testing.T) {
	cases := []struct{ message, want string }{
		{"Running turns block share selection", ReasonRunningTurn},
		{"Streaming rows cannot be shared", ReasonStreamingRow},
		{"Active tool calls are in flight", ReasonActiveToolCall},
		{"Active subagents are still working", ReasonActiveSubagent},
		{"User input attachments are not shareable", ReasonInputAttachment},
		{"Inline tool images are not shareable", ReasonInlineToolImage},
		{"Timeline references outside the projection", ReasonUnsupportedTimeline},
		{"Local and inline URLs cannot be shared", ReasonUnsafeURL},
		{"Conversation row is missing its product turn identity", ReasonMissingProductTurn},
		{"Selected product turns are incomplete", ReasonInvalidSelection},
		{"pdf is not allowed by server capabilities", ReasonArtifactTypeNotAllowed},
		{"The artifact extension is missing", ReasonArtifactExtensionMissing},
		{"Conversation artifact is outside the workspace", ReasonArtifactOutsideWorkspace},
		{"Conversation artifact changed after it was registered", ReasonArtifactChanged},
		{"Conversation artifact cannot be read", ReasonArtifactReadFailed},
		{"Conversation artifact source ended early", ReasonArtifactReadFailed},
		{"Conversation artifact identifiers are unresolved", ReasonArtifactManifest},
		{"payload has too many rows", ReasonPayloadLimit},
		{"something entirely unexpected", ReasonInvalidConversation},
	}
	for _, testCase := range cases {
		t.Run(testCase.message, func(t *testing.T) {
			if got := inferFailureReasonCode(testCase.message); got != testCase.want {
				t.Fatalf("inferFailureReasonCode(%q) = %q, want %q", testCase.message, got, testCase.want)
			}
		})
	}
}

func TestServiceErrorSanitizesDiagnosticsAndUnsafeRequestID(t *testing.T) {
	err := newServiceError(KindInvalidConversation, "Conversation artifact cannot be read", errorDetails{
		RequestID: "req-1 with spaces",
		Diagnostics: map[string]any{
			"errno":         "ENOENT",
			"rowKind":       "artifact",
			"workspacePath": "/Users/alice/work",
			"token":         "sk-secret",
		},
		Issues: []Issue{{Code: ReasonArtifactReadFailed, Scope: ScopeArtifact}},
	})
	// A request id with spaces is not a safe header value and is dropped.
	if err.requestID != "" {
		t.Fatalf("unsafe request id survived: %q", err.requestID)
	}
	if got := err.ReasonCode(); got != ReasonArtifactReadFailed {
		t.Fatalf("reason code = %q, want %q", got, ReasonArtifactReadFailed)
	}
	diagnostics, ok := err.details["diagnostics"].(map[string]any)
	if !ok {
		t.Fatalf("diagnostics missing: %#v", err.details)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("only whitelisted diagnostics survive: %#v", diagnostics)
	}
	if diagnostics["errno"] != "ENOENT" || diagnostics["rowKind"] != "artifact" {
		t.Fatalf("safe diagnostics dropped: %#v", diagnostics)
	}
	if errno, ok := err.Errno(); !ok || errno != "ENOENT" {
		t.Fatalf("Errno() = %q/%v", errno, ok)
	}
	if err.details["reasonCode"] != ReasonArtifactReadFailed {
		t.Fatalf("reasonCode missing from details: %#v", err.details)
	}
}

func TestServiceErrorAlwaysCarriesAReasonCode(t *testing.T) {
	err := newServiceError(KindUnsafeStructure, "nothing recognisable here", errorDetails{})
	if err.details == nil || err.details["reasonCode"] == nil {
		t.Fatalf("details must always carry a reason code: %#v", err.details)
	}
	if err.details["reasonCode"] != ReasonInvalidConversation {
		t.Fatalf("unmatched messages fall back to invalid_conversation: %#v", err.details)
	}
}

func TestErrorKindReadsBothErrorShapes(t *testing.T) {
	if got := errKind(newServiceError(KindInvalidSelection, "x", errorDetails{})); got != KindInvalidSelection {
		t.Fatalf("service error kind = %q", got)
	}
	if got := errKind(&clientError{kind: KindNotFound, message: "x"}); got != KindNotFound {
		t.Fatalf("client error kind = %q", got)
	}
	if got := errKind(nil); got != "" {
		t.Fatalf("nil error kind = %q", got)
	}
}

func TestClientErrorMapsEveryKnownBusinessCode(t *testing.T) {
	for code := int64(3001); code <= 3215; code++ {
		_, known := errorKindByCode[code]
		if !known {
			// Only the documented codes are mapped; the gaps are intentional.
			continue
		}
		if errorKindByCode[code] == "" {
			t.Fatalf("code %d maps to an empty kind", code)
		}
	}
	// Spot-check the codes the UI branches on by name.
	spot := map[int64]string{
		3201: KindAuthenticationRequired, 3206: KindDisclosureRequired,
		3208: KindArtifactNotAllowed, 3211: KindNotFound, 3212: KindExpired,
		3215: KindSafetyCheckPending, 3002: KindRateLimited,
	}
	for code, kind := range spot {
		if got := errorKindByCode[code]; got != kind {
			t.Fatalf("code %d maps to %q, want %q", code, got, kind)
		}
	}
}

// TestSanitizedIssuesMatchesTheTSVector pins the Go redaction to the TS one
// over a vector generated from the TS source by
// scripts/go-rewrite/vectors/sanitize-issues.mts. Regenerate with:
//
//	npx tsx scripts/go-rewrite/vectors/sanitize-issues.mts
//
// The vector deliberately mixes a path-shaped displayName, a URL-shaped
// allowedFormat, an artifact summary with a path-shaped type and a bad mime,
// an over-long extension and an unknown code.
func TestSanitizedIssuesMatchesTheTSVector(t *testing.T) {
	long := strings.Repeat("x", 200)
	issues := []Issue{
		{
			Code: ReasonArtifactTypeNotAllowed, Scope: ScopeArtifact,
			ArtifactDisplayName: ptr("/etc/passwd"), ArtifactType: ptr("text"),
			Extension: ptr("txt"), MimeType: ptr("text/plain"),
			AllowedFormats:   []string{"/bad/path"},
			AllowedArtifacts: []AllowedArtifact{{Type: "/weird", Extensions: []string{".x"}, MimeTypes: []string{"a/b"}}},
		},
		{
			Code: ReasonArtifactChanged, Scope: ScopeArtifact,
			RowID: ptr(int64(3)), TurnOrdinal: ptr(int64(2)), ProductTurnID: ptr("pt-1"),
			ArtifactDisplayName: ptr("ok.txt"), ArtifactType: ptr("text"), Extension: ptr("txt"),
			MimeType: ptr("text/plain"), Actual: ptr(int64(5)), Limit: ptr(int64(4)),
			RetryAfterMs: ptr(int64(100)), Phase: ptr("downloading"), Availability: ptr("changed"),
		},
		{Code: "rows_limit", Scope: ScopeConversation, Actual: ptr(int64(999)), Limit: ptr(int64(500))},
		{Code: "unknown_code", Scope: ScopeTurn, MimeType: ptr("not a mime"), Extension: ptr(long)},
	}
	sanitized, issueCount, omitted := SanitizedIssues(issues)
	if issueCount != 4 || omitted != 0 {
		t.Fatalf("issueCount=%d omitted=%d, want 4/0", issueCount, omitted)
	}
	encoded := marshalIssues(t, sanitized)
	want := `[{"code":"artifact_type_not_allowed","scope":"artifact","artifactType":"text","extension":"txt","mimeType":"text/plain"},` +
		`{"code":"artifact_changed","scope":"artifact","rowId":3,"turnOrdinal":2,"productTurnId":"pt-1","artifactDisplayName":"ok.txt","artifactType":"text","extension":"txt","mimeType":"text/plain","actual":5,"limit":4,"retryAfterMs":100,"phase":"downloading","availability":"changed"},` +
		`{"code":"rows_limit","scope":"conversation","actual":999,"limit":500},` +
		`{"code":"unknown_code","scope":"turn"}]`
	if encoded != want {
		t.Fatalf("Go redaction diverges from the TS vector\n got %s\nwant %s", encoded, want)
	}
}
