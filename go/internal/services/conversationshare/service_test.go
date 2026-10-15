// service_test.go (step 3.8): the preflight + publish flow end to end
// against an httptest server and a fixture agent.
//
// PRIVACY: every endpoint in this file is a loopback httptest server. No real
// share service is ever contacted — the upload target is injected, and the
// assertions check that local paths never leave the process.
package conversationshare

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- fixture agent ----------

// fixtureAgent answers the four agent V4 reads the service needs. Every hook
// is overridable per test; the defaults describe one completed turn.
type fixtureAgent struct {
	mu sync.Mutex

	rows     []row
	revision int
	logEpoch string
	pages    int

	// statResults / readResults are keyed by the attachment ref.
	statResults map[string]error
	readResults map[string][]byte
	statCalls   int
	readCalls   int

	fileChanges []fileChange
}

func (a *fixtureAgent) ConversationRowsRangeV4(_ context.Context, request RowsRangeRequest) (RowsRangeResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if request.BeforeRowID == nil {
		return RowsRangeResult{Rows: a.rows, AtRevision: a.revision, AtLogEpoch: a.logEpoch}, nil
	}
	return RowsRangeResult{AtRevision: a.revision, AtLogEpoch: a.logEpoch}, nil
}

func (a *fixtureAgent) ConversationFileChangesV4(_ context.Context, _ FileChangesRequest) (FileChangesResult, error) {
	result := FileChangesResult{State: "active"}
	for _, change := range a.fileChanges {
		result.Items = append(result.Items, struct {
			Path string `json:"path"`
		}{Path: change.Path})
	}
	return result, nil
}

func (a *fixtureAgent) ConversationAttachmentStatV4(_ context.Context, request AttachmentStatRequest) (AttachmentStatResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statCalls++
	if err, ok := a.statResults[request.Ref]; ok && err != nil {
		return AttachmentStatResult{}, err
	}
	return AttachmentStatResult{TotalBytes: int64(len(a.readResults[request.Ref]))}, nil
}

func (a *fixtureAgent) ConversationAttachmentReadV4(_ context.Context, request AttachmentReadRequest) (AttachmentReadResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.readCalls++
	payload, ok := a.readResults[request.Ref]
	if !ok {
		return AttachmentReadResult{}, newServiceError(KindInvalidConversation, "attachment source ended", errorDetails{})
	}
	return AttachmentReadResult{DataBase64: base64Of(payload), MediaType: "application/pdf", TotalBytes: int64(len(payload))}, nil
}

func base64Of(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// ---------- fixture rows ----------

// completedTurn builds the rows of one finished product turn with an
// assistantText body, matching what the agent V4 rows/range returns.
func completedTurn(productTurnID string, startRowID int64) []row {
	turnID := productTurnID + "-turn"
	return []row{
		{
			"rowId": float64(startRowID), "kind": "turnHeader", "productTurnId": productTurnID,
			"turnId": turnID, "entityId": productTurnID + "-entity", "createdAt": float64(1700000000000),
			"createdAtSeq": float64(0), "origin": "local", "executionKind": "main",
			"state": "completedSuccess", "startedAt": float64(1700000000000),
			"endedAt": float64(1700000001000), "activeMs": float64(1000),
		},
		{
			"rowId": float64(startRowID + 1), "kind": "assistantText", "productTurnId": productTurnID,
			"turnId": turnID, "createdAt": float64(1700000000500), "createdAtSeq": float64(1),
			"text": "Done.", "visibility": "normal",
		},
	}
}

func userInputRow(productTurnID string, turnID string, rowID int64, attachments []any, withEntity bool) row {
	current := row{
		"rowId": float64(rowID), "kind": "userInput", "productTurnId": productTurnID,
		"turnId": turnID, "createdAt": float64(1700000000000), "createdAtSeq": float64(2),
		"text": "see attached",
	}
	if withEntity {
		current["entityId"] = productTurnID + "-input-entity"
	}
	if attachments != nil {
		current["attachments"] = attachments
	}
	return current
}

func attachment(fileName, mimeType, ref string, bytes int64) map[string]any {
	return map[string]any{
		"fileName": fileName, "mime": mimeType, "ref": ref, "bytes": float64(bytes),
	}
}

// ---------- fixture share API ----------

// shareAPI is a loopback stand-in for the Nex share service. It records every
// request so the test can assert the wire shape.
type shareAPI struct {
	mu sync.Mutex

	t *testing.T
	// capabilities is returned by /shares/capabilities.
	capabilities map[string]any
	// confirmResponses are popped in order by /confirm.
	confirmResponses []shareAPIResponse
	// pendingConfirmResponses is how many times confirm answers 3215 first.
	pendingConfirmResponses int
	uploadStatuses          []string

	preparations  []PreparationRequest
	uploads       []uploadRecord
	confirmBodies []map[string]any
	authHeaders   []string
}

type shareAPIResponse struct {
	status int
	body   string
}

type uploadRecord struct {
	path        string
	displayName string
	descriptor  ArtifactDescriptor
	bytes       []byte
}

func defaultCapabilities() map[string]any {
	return map[string]any{
		"schema_version": 1, "ttl_ms": 604800000, "max_rows": 5000,
		"max_payload_bytes": 8 << 20, "max_artifact_count": 20,
		"max_artifact_bytes": 16 << 20, "max_total_artifact_bytes": 32 << 20,
		"access_modes": []string{AccessPrivate, AccessPublicReadonly, AccessPublicImport},
		"allowed_artifacts": []map[string]any{
			{"type": "pdf", "extensions": []string{"pdf"}, "mime_types": []string{"application/pdf"}},
			{"type": "image", "extensions": []string{"png"}, "mime_types": []string{"image/png"}},
			{"type": "md", "extensions": []string{"md"}, "mime_types": []string{"text/markdown"}},
			// Deliberately unsupported by this client: must be narrowed away.
			{"type": "archive", "extensions": []string{"zip"}, "mime_types": []string{"application/zip"}},
		},
	}
}

func (api *shareAPI) handler(t *testing.T) http.Handler {
	api.t = t
	mux := http.NewServeMux()
	mux.HandleFunc("/shares/capabilities", func(w http.ResponseWriter, r *http.Request) {
		api.recordAuth(r)
		api.writeData(w, api.capabilities)
	})
	mux.HandleFunc("/shares/preparations", func(w http.ResponseWriter, r *http.Request) {
		api.recordAuth(r)
		var request PreparationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode preparation request: %v", err)
		}
		api.mu.Lock()
		api.preparations = append(api.preparations, request)
		api.mu.Unlock()
		api.writeData(w, map[string]any{
			"status": "preparing", "preparation_id": "prep-1",
			"access_mode": request.AccessMode, "expires_at": 1700608000000,
		})
	})
	mux.HandleFunc("/shares/preparations/prep-1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		api.recordAuth(r)
		record := uploadRecord{path: r.URL.Path}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("read multipart file: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer file.Close()
		payload, _ := io.ReadAll(file)
		record.bytes = payload
		record.displayName = header.Filename
		if raw := r.FormValue("descriptor"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &record.descriptor); err != nil {
				t.Errorf("decode descriptor part: %v", err)
			}
		}
		api.mu.Lock()
		api.uploads = append(api.uploads, record)
		api.mu.Unlock()
		safetyStatus := "passed"
		if len(api.uploadStatuses) > 0 {
			safetyStatus = api.uploadStatuses[0]
			api.uploadStatuses = api.uploadStatuses[1:]
		}
		api.writeData(w, map[string]any{
			"artifact_id": record.descriptor.ArtifactID, "size_bytes": len(payload),
			"sha256": record.descriptor.Sha256, "status": "received",
			"safety_status": safetyStatus,
		})
	})
	mux.HandleFunc("/shares/preparations/prep-1/confirm", func(w http.ResponseWriter, r *http.Request) {
		api.recordAuth(r)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode confirm body: %v", err)
		}
		api.mu.Lock()
		api.confirmBodies = append(api.confirmBodies, body)
		pending := api.pendingConfirmResponses > 0
		if pending {
			api.pendingConfirmResponses--
		}
		response := shareAPIResponse{}
		if len(api.confirmResponses) > 0 {
			response = api.confirmResponses[0]
			api.confirmResponses = api.confirmResponses[1:]
		}
		api.mu.Unlock()
		if pending {
			w.Header().Set("retry-after", "0")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":3215,"msg":"safety check pending"}`)
			return
		}
		if response.status != 0 {
			w.WriteHeader(response.status)
			_, _ = io.WriteString(w, response.body)
			return
		}
		api.writeData(w, map[string]any{
			"share_code": "SHARE123", "share_url": "https://share.invalid/SHARE123",
			"access_mode": "public_readonly", "expires_at": 1700608000000,
		})
	})
	return mux
}

func (api *shareAPI) recordAuth(r *http.Request) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.authHeaders = append(api.authHeaders, r.Header.Get("Authorization"))
}

func (api *shareAPI) writeData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, fmt.Sprintf(`{"code":0,"msg":"ok","data":%s}`, mustJSON(data)))
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

// newTestService wires the service against the loopback API and a temp
// workspace. No real endpoint is involved: server.URL is 127.0.0.1.
func newTestService(t *testing.T, api *shareAPI, agent *fixtureAgent) (*Service, string) {
	t.Helper()
	if api.capabilities == nil {
		api.capabilities = defaultCapabilities()
	}
	server := httptest.NewServer(api.handler(t))
	t.Cleanup(server.Close)
	workspace := t.TempDir()
	client := NewClient(ClientOptions{
		BaseURL: server.URL,
		Doer:    &http.Client{Timeout: 5 * time.Second},
		Token:   func(context.Context) (string, error) { return "test-token", nil },
		Now:     func() time.Time { return time.UnixMilli(1700000000000) },
	})
	service := NewService(ServiceOptions{
		Agent:          agent,
		Client:         client,
		ArtifactSource: NewLocalArtifactSource(),
		Now:            func() time.Time { return time.UnixMilli(1700000000000) },
		// No real waiting: the confirm-retry loop advances the fake clock.
		Sleep: func(ctx context.Context, delayMs int64) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
				return nil
			}
		},
		ConfirmPollIntervalMs:     1,
		ConfirmPollTimeoutMs:      5_000,
		ConversationWorkspaceRoot: workspace,
		ShareWebURL:               "https://share.invalid",
	})
	return service, workspace
}

// ---------- preflight ----------

func TestPreflightReportsCleanSelection(t *testing.T) {
	agent := &fixtureAgent{rows: completedTurn("pt-1", 1), revision: 7, logEpoch: "epoch-1"}
	service, _ := newTestService(t, &shareAPI{}, agent)

	result, err := service.Preflight(context.Background(), PreflightInput{
		WorkspaceRef: WorkspaceRef{WorkspacePath: "/w", SessionID: "s1"},
		Selection:    Selection{Kind: "all"},
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if result.Revision != 7 || result.LogEpoch != "epoch-1" {
		t.Fatalf("watermark lost: %#v", result)
	}
	if len(result.BlockingIssues) != 0 {
		t.Fatalf("unexpected blocking issues: %v", issueCodes(result.BlockingIssues))
	}
	if len(result.TurnResults) != 1 || result.TurnResults[0].ProductTurnID != "pt-1" {
		t.Fatalf("turn results wrong: %#v", result.TurnResults)
	}
	// The unsupported "archive" type must be narrowed away.
	for _, allowed := range result.SupportedArtifactTypes {
		if allowed.Type == "archive" {
			t.Fatalf("unsupported artifact type survived narrowing: %#v", result.SupportedArtifactTypes)
		}
	}
	if result.CapabilitiesFingerprint == "" {
		t.Fatal("capabilities fingerprint must be reported")
	}
}

func TestPreflightBlocksOnInvalidSelection(t *testing.T) {
	agent := &fixtureAgent{rows: completedTurn("pt-1", 1), revision: 1, logEpoch: "e"}
	service, _ := newTestService(t, &shareAPI{}, agent)

	result, err := service.Preflight(context.Background(), PreflightInput{
		WorkspaceRef: WorkspaceRef{WorkspacePath: "/w", SessionID: "s1"},
		Selection:    Selection{Kind: "rowAnchors", RowIDs: []int64{999}},
	})
	if err != nil {
		t.Fatalf("preflight must not throw for an invalid selection: %v", err)
	}
	if len(result.BlockingIssues) != 1 || result.BlockingIssues[0].Code != ReasonInvalidSelection {
		t.Fatalf("blocking issues = %v", issueCodes(result.BlockingIssues))
	}
}

func TestPreflightBlocksUnresolvablePagination(t *testing.T) {
	agent := &fixtureAgent{rows: nil, revision: 1, logEpoch: ""}
	service, _ := newTestService(t, &shareAPI{}, agent)
	if _, err := service.Preflight(context.Background(), PreflightInput{
		WorkspaceRef: WorkspaceRef{WorkspacePath: "/w", SessionID: "s1"},
		Selection:    Selection{Kind: "all"},
	}); err == nil {
		t.Fatal("a missing read watermark must fail the load")
	}
}

func TestPreflightClassifiesAttachmentOutcomes(t *testing.T) {
	turn := completedTurn("pt-1", 1)
	turn = append(turn, userInputRow("pt-1", "pt-1-turn", 10, []any{
		attachment("ok.pdf", "application/pdf", "ref-ok", 4),
		attachment("too-big.pdf", "application/pdf", "ref-big", 1<<40),
		attachment("missing.pdf", "application/pdf", "ref-missing", 1),
		attachment("unsupported.zip", "application/zip", "ref-unsupported", 1),
		attachment("unclassifiable.pdf", "application/pdf", "ref-unknown", 1),
	}, true))
	agent := &fixtureAgent{
		rows: turn, revision: 3, logEpoch: "e3",
		statResults: map[string]error{
			"ref-big":     &faultError{code: faultShareStatTooLarge},
			"ref-missing": &faultError{code: faultShareStatNotFound},
			// A stat failure with no fault code is not classifiable.
			"ref-unknown": &faultError{code: ""},
		},
		readResults: map[string][]byte{
			"ref-ok": []byte("data"), "ref-unsupported": []byte("z"), "ref-unknown": []byte("u"),
		},
	}
	service, _ := newTestService(t, &shareAPI{}, agent)

	result, err := service.Preflight(context.Background(), PreflightInput{
		WorkspaceRef: WorkspaceRef{WorkspacePath: "/w", SessionID: "s1"},
		Selection:    Selection{Kind: "all"},
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	blocking := issueCodes(result.BlockingIssues)
	if !containsCode(blocking, ReasonArtifactSizeLimit) {
		t.Fatalf("an oversized attachment must block: %v", blocking)
	}
	warnings := issueCodes(result.SkippableWarnings)
	if !containsCode(warnings, ReasonInputAttachmentUnavail) {
		t.Fatalf("a missing attachment is skippable: %v", warnings)
	}
	if !containsCode(warnings, ReasonArtifactTypeNotAllowed) {
		t.Fatalf("an unsupported type is a skippable warning: %v", warnings)
	}
	deferred := issueCodes(result.DeferredIssues)
	if !containsCode(deferred, ReasonArtifactReadFailed) {
		t.Fatalf("an unclassifiable read failure is deferred: %v", deferred)
	}
	// Every artifact issue must carry the turn identity, not just an ordinal.
	for _, issue := range append(result.BlockingIssues, result.SkippableWarnings...) {
		if issue.Scope == ScopeArtifact && (issue.ProductTurnID == nil || *issue.ProductTurnID != "pt-1") {
			t.Fatalf("artifact issue without product turn identity: %#v", issue)
		}
	}
}

func TestPreflightBlocksInputAttachmentWithoutRowIdentity(t *testing.T) {
	turn := completedTurn("pt-1", 1)
	turn = append(turn, userInputRow("pt-1", "pt-1-turn", 10,
		[]any{attachment("a.pdf", "application/pdf", "ref", 1)}, false))
	agent := &fixtureAgent{rows: turn, revision: 1, logEpoch: "e"}
	service, _ := newTestService(t, &shareAPI{}, agent)

	result, err := service.Preflight(context.Background(), PreflightInput{
		WorkspaceRef: WorkspaceRef{WorkspacePath: "/w", SessionID: "s1"},
		Selection:    Selection{Kind: "all"},
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !containsCode(issueCodes(result.BlockingIssues), ReasonInputAttachment) {
		t.Fatalf("an attachment without a row entity must block: %v", issueCodes(result.BlockingIssues))
	}
}

// faultError is an agent error carrying a NEX_ATTACHMENT_FAULT_CODES string.
type faultError struct{ code string }

func (e *faultError) Error() string {
	if e.code == "" {
		return "attachment stat failed for an unknown reason"
	}
	return e.code
}

func (e *faultError) FaultCode() string { return e.code }

func containsCode(codes []string, want string) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}

// ---------- publish ----------

func TestPublishUploadsArtifactsAndConfirms(t *testing.T) {
	agent := &fixtureAgent{rows: completedTurn("pt-1", 1), revision: 5, logEpoch: "e5"}
	api := &shareAPI{}
	service, workspace := newTestService(t, api, agent)

	input := PublishInput{
		WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
		Title:                "  My share  ",
		AccessMode:           AccessPublicReadonly,
		Selection:            Selection{Kind: "all"},
		ClientRequestID:      "req-1",
		DisclosureAcceptedAt: 1700000000000,
	}
	var progress []PublishProgress
	cancel := service.OnPublishProgress("op-1", func(update PublishProgress) {
		progress = append(progress, update)
	})
	defer cancel()

	record, err := service.Publish(context.Background(), input, "op-1")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if record.ShareCode != "SHARE123" || record.ShareURL == "" {
		t.Fatalf("share record wrong: %#v", record)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.preparations) != 1 {
		t.Fatalf("preparations = %d", len(api.preparations))
	}
	preparation := api.preparations[0]
	if preparation.Title != "My share" {
		t.Fatalf("title not trimmed: %q", preparation.Title)
	}
	if preparation.ClientRequestID != "req-1" || preparation.SchemaVersion != SchemaVersion {
		t.Fatalf("preparation request wrong: %#v", preparation)
	}
	if len(preparation.PayloadSha256) != 64 {
		t.Fatalf("payload digest missing: %#v", preparation)
	}
	if len(api.confirmBodies) != 1 {
		t.Fatalf("confirm calls = %d", len(api.confirmBodies))
	}
	confirm := api.confirmBodies[0]
	integrity, ok := confirm["integrity"].(map[string]any)
	if !ok || integrity["projection_sha256"] == "" || integrity["artifact_set_sha256"] == "" {
		t.Fatalf("confirm integrity missing: %#v", confirm["integrity"])
	}
	disclosure, ok := confirm["disclosure_confirmation"].(map[string]any)
	if !ok || disclosure["acknowledged_no_secret_detection"] != true {
		t.Fatalf("disclosure confirmation missing: %#v", confirm["disclosure_confirmation"])
	}
	for _, header := range api.authHeaders {
		if header != "Bearer test-token" {
			t.Fatalf("authorization header = %q", header)
		}
	}
	// Progress must walk collecting → uploading → checking → complete.
	phases := make([]string, 0, len(progress))
	for _, update := range progress {
		phases = append(phases, update.Phase)
	}
	joined := strings.Join(phases, ",")
	for _, phase := range []string{"collecting", "uploading", "checking", "complete"} {
		if !strings.Contains(joined, phase) {
			t.Fatalf("phase %q missing from progress: %v", phase, phases)
		}
	}
	if phases[len(phases)-1] != "complete" {
		t.Fatalf("publish must end in complete: %v", phases)
	}
}

func TestPublishUploadsTheInputAttachment(t *testing.T) {
	turn := completedTurn("pt-1", 1)
	turn = append(turn, userInputRow("pt-1", "pt-1-turn", 10,
		[]any{attachment("report.pdf", "application/pdf", "ref-1", 5)}, true))
	agent := &fixtureAgent{
		rows: turn, revision: 5, logEpoch: "e5",
		readResults: map[string][]byte{"ref-1": []byte("hello")},
	}
	api := &shareAPI{}
	service, workspace := newTestService(t, api, agent)

	_, err := service.Publish(context.Background(), PublishInput{
		WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
		Title:                "Share",
		AccessMode:           AccessPrivate,
		Selection:            Selection{Kind: "all"},
		ClientRequestID:      "req-1",
		DisclosureAcceptedAt: 1700000000000,
	}, "op-1")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.uploads) != 1 {
		t.Fatalf("uploads = %d, want 1", len(api.uploads))
	}
	upload := api.uploads[0]
	if upload.displayName != "report.pdf" {
		t.Fatalf("upload filename = %q", upload.displayName)
	}
	if string(upload.bytes) != "hello" {
		t.Fatalf("upload bytes = %q", upload.bytes)
	}
	if upload.descriptor.MimeType != "application/pdf" || upload.descriptor.ArtifactType != "pdf" {
		t.Fatalf("descriptor wrong: %#v", upload.descriptor)
	}
	// The local ref must be replaced by a public nex-artifact:// reference.
	if upload.descriptor.Ref == "ref-1" || !strings.HasPrefix(upload.descriptor.Ref, "nex-artifact://share/") {
		t.Fatalf("ref was not re-minted: %q", upload.descriptor.Ref)
	}
}

func TestPublishRetriesSafetyCheckPendingThenSucceeds(t *testing.T) {
	agent := &fixtureAgent{rows: completedTurn("pt-1", 1), revision: 5, logEpoch: "e5"}
	api := &shareAPI{pendingConfirmResponses: 2}
	service, workspace := newTestService(t, api, agent)

	record, err := service.Publish(context.Background(), PublishInput{
		WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
		Title:                "Share",
		AccessMode:           AccessPrivate,
		Selection:            Selection{Kind: "all"},
		ClientRequestID:      "req-1",
		DisclosureAcceptedAt: 1700000000000,
	}, "op-1")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if record.ShareCode != "SHARE123" {
		t.Fatalf("share record = %#v", record)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	// 2 pending + 1 success, against a SINGLE preparation.
	if len(api.confirmBodies) != 3 {
		t.Fatalf("confirm calls = %d, want 3", len(api.confirmBodies))
	}
	if len(api.preparations) != 1 {
		t.Fatalf("prepare must not be repeated: %d", len(api.preparations))
	}
}

func TestPublishFailsWhenSafetyCheckNeverClears(t *testing.T) {
	agent := &fixtureAgent{rows: completedTurn("pt-1", 1), revision: 5, logEpoch: "e5"}
	api := &shareAPI{pendingConfirmResponses: 1_000_000}
	service, workspace := newTestService(t, api, agent)
	// A clock that jumps past the poll deadline on the first retry.
	service.now = func() time.Time { return time.UnixMilli(1700000000000) }
	service.sleep = func(ctx context.Context, delayMs int64) error {
		service.now = func() time.Time { return time.UnixMilli(1700000000000 + 60_000) }
		return nil
	}

	_, err := service.Publish(context.Background(), PublishInput{
		WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
		Title:                "Share",
		AccessMode:           AccessPrivate,
		Selection:            Selection{Kind: "all"},
		ClientRequestID:      "req-1",
		DisclosureAcceptedAt: 1700000000000,
	}, "op-1")
	if err == nil {
		t.Fatal("expected a safety-check timeout")
	}
	typed, ok := isServiceError(err)
	if !ok || typed.Kind() != KindSafetyCheckTimeout {
		t.Fatalf("error = %v (%T)", err, err)
	}
}

func TestPublishRejectsMissingDisclosureConfirmation(t *testing.T) {
	agent := &fixtureAgent{rows: completedTurn("pt-1", 1), revision: 1, logEpoch: "e"}
	api := &shareAPI{}
	service, workspace := newTestService(t, api, agent)

	_, err := service.Publish(context.Background(), PublishInput{
		WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
		Title:                "Share",
		AccessMode:           AccessPrivate,
		Selection:            Selection{Kind: "all"},
		ClientRequestID:      "req-1",
		DisclosureAcceptedAt: 0,
	}, "op-1")
	typed, ok := isServiceError(err)
	if !ok || typed.Kind() != KindDisclosureRequired {
		t.Fatalf("error = %v", err)
	}
	if len(api.preparations) != 0 {
		t.Fatal("nothing must be sent before the disclosure check")
	}
}

func TestPublishRejectsUnavailableAccessMode(t *testing.T) {
	agent := &fixtureAgent{rows: completedTurn("pt-1", 1), revision: 1, logEpoch: "e"}
	api := &shareAPI{}
	service, workspace := newTestService(t, api, agent)

	_, err := service.Publish(context.Background(), PublishInput{
		WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
		Title:                "Share",
		AccessMode:           "public_everyone",
		Selection:            Selection{Kind: "all"},
		ClientRequestID:      "req-1",
		DisclosureAcceptedAt: 1700000000000,
	}, "op-1")
	typed, ok := isServiceError(err)
	if !ok || typed.Kind() != KindFeatureDisabled {
		t.Fatalf("error = %v", err)
	}
}

func TestPublishSurfacesUploadAcknowledgementMismatch(t *testing.T) {
	// The API answers the upload with a sha256 that does not match the manifest.
	turn := completedTurn("pt-1", 1)
	turn[1]["text"] = "See `report.pdf`"
	agent := &fixtureAgent{rows: turn, revision: 5, logEpoch: "e5"}
	api := &shareAPI{capabilities: defaultCapabilities()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/artifacts") {
			api.recordAuth(r)
			api.writeData(w, map[string]any{
				"artifact_id": "share-artifact-1", "size_bytes": 1, "sha256": "tampered",
				"status": "received", "safety_status": "passed",
			})
			return
		}
		api.handler(t).ServeHTTP(w, r)
	}))
	defer server.Close()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "report.pdf"), []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceOptions{
		Agent: agent,
		Client: NewClient(ClientOptions{
			BaseURL: server.URL, Doer: &http.Client{Timeout: 5 * time.Second},
			Token: func(context.Context) (string, error) { return "t", nil },
		}),
		ArtifactSource: NewLocalArtifactSource(),
		Now:            func() time.Time { return time.UnixMilli(1700000000000) },
		Sleep:          func(context.Context, int64) error { return nil },
	})

	_, err := service.Publish(context.Background(), PublishInput{
		WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
		Title:                "Share",
		AccessMode:           AccessPrivate,
		Selection:            Selection{Kind: "all"},
		ClientRequestID:      "req-1",
		DisclosureAcceptedAt: 1700000000000,
	}, "op-1")
	typed, ok := isServiceError(err)
	if !ok || typed.Kind() != KindUploadIncomplete {
		t.Fatalf("error = %v (%T), want upload_incomplete", err, err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.confirmBodies) != 0 {
		t.Fatal("a mismatched upload acknowledgement must stop before confirm")
	}
}

func TestPublishRejectsRowsWithLocalPathsInTheProjection(t *testing.T) {
	turn := completedTurn("pt-1", 1)
	// The sweep is anchored per VALUE (ports assertSafeString): a toolCall
	// output that IS a bare file: URL is what blocks the share.
	turn = append(turn, row{
		"rowId": float64(10), "kind": "toolCall", "productTurnId": "pt-1",
		"turnId": "pt-1-turn", "entityId": "pt-1-tool", "toolCallId": "tc-1",
		"createdAt": float64(1700000000000), "createdAtSeq": float64(2),
		"status": "completed", "toolName": "read",
		"output": map[string]any{"text": "file:///Users/alice/secret.txt"},
	})
	agent := &fixtureAgent{rows: turn, revision: 1, logEpoch: "e"}
	api := &shareAPI{}
	service, workspace := newTestService(t, api, agent)

	_, err := service.Publish(context.Background(), PublishInput{
		WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
		Title:                "Share",
		AccessMode:           AccessPrivate,
		Selection:            Selection{Kind: "all"},
		ClientRequestID:      "req-1",
		DisclosureAcceptedAt: 1700000000000,
	}, "op-1")
	if err == nil {
		t.Fatal("a file: URL in the selected rows must block the publish")
	}
	typed, ok := isServiceError(err)
	if !ok || typed.Kind() != KindUnsafeStructure {
		t.Fatalf("error = %v", err)
	}
	if len(api.confirmBodies) != 0 {
		t.Fatal("nothing may be confirmed when the projection is unsafe")
	}
}

func TestPublishWarnsAboutUnshareableFilesWithoutFailing(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	turn := completedTurn("pt-1", 1)
	turn[1]["text"] = "See `missing.pdf` and `notes.md`"
	agent := &fixtureAgent{rows: turn, revision: 1, logEpoch: "e"}
	api := &shareAPI{}
	service, _ := newTestService(t, api, agent)

	var warnings []Issue
	cancel := service.OnPublishProgress("op-1", func(update PublishProgress) {
		warnings = append(warnings, update.Warnings...)
	})
	defer cancel()

	if _, err := service.Publish(context.Background(), PublishInput{
		WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
		Title:                "Share",
		AccessMode:           AccessPrivate,
		Selection:            Selection{Kind: "all"},
		ClientRequestID:      "req-1",
		DisclosureAcceptedAt: 1700000000000,
	}, "op-1"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !containsCode(issueCodes(warnings), ReasonArtifactReadFailed) {
		t.Fatalf("a vanished preview file must be reported as a warning: %v", issueCodes(warnings))
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	// notes.md is allowed as an md artifact; the missing file must not upload.
	for _, upload := range api.uploads {
		if upload.displayName == "missing.pdf" {
			t.Fatalf("a vanished file was uploaded: %#v", upload)
		}
	}
}

func TestConcurrentPublishesDoNotShareArtifactBytes(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("# "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	turn := completedTurn("pt-1", 1)
	turn[1]["text"] = "See `a.md` and `b.md`"
	// markdown/html references need an active file-change authorization.
	agent := &fixtureAgent{
		rows: turn, revision: 1, logEpoch: "e",
		fileChanges: []fileChange{
			{Path: workspace + "/a.md", State: "active"},
			{Path: workspace + "/b.md", State: "active"},
		},
	}
	api := &shareAPI{}
	service, _ := newTestService(t, api, agent)
	// The fixture workspace must be the one the publish reads through.
	service.conversationWorkspaceRoot = workspace

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Publish(context.Background(), PublishInput{
				WorkspaceRef:         WorkspaceRef{WorkspacePath: workspace, SessionID: "s1"},
				Title:                "Share",
				AccessMode:           AccessPrivate,
				Selection:            Selection{Kind: "all"},
				ClientRequestID:      "req-1",
				DisclosureAcceptedAt: 1700000000000,
			}, "op-1")
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent publish failed: %v", err)
		}
	}
	t.Logf("uploads=%d prepares=%d", len(api.uploads), len(api.preparations))
	api.mu.Lock()
	defer api.mu.Unlock()
	// Each publish uploads its own copy: two uploads per file name, never a
	// payload from the other publish.
	seen := map[string]int{}
	for _, upload := range api.uploads {
		seen[upload.displayName]++
		if !strings.HasPrefix(string(upload.bytes), "# ") {
			t.Fatalf("upload %q carried foreign bytes: %q", upload.displayName, upload.bytes)
		}
	}
	if seen["a.md"] != 2 || seen["b.md"] != 2 {
		t.Fatalf("upload counts = %#v", seen)
	}
}

func TestPublishCancelStopsProgressDelivery(t *testing.T) {
	agent := &fixtureAgent{rows: completedTurn("pt-1", 1), revision: 1, logEpoch: "e"}
	service, _ := newTestService(t, &shareAPI{}, agent)
	received := 0
	cancel := service.OnPublishProgress("op-1", func(PublishProgress) { received++ })
	service.reportPublishProgress("op-1", "collecting", 0, 0, nil, nil)
	if received != 1 {
		t.Fatalf("handler received %d updates", received)
	}
	cancel()
	service.reportPublishProgress("op-1", "collecting", 1, 1, nil, nil)
	if received != 1 {
		t.Fatalf("cancel did not detach the handler: %d", received)
	}
}

func TestGetContinuationRejectsTamperedIntegrity(t *testing.T) {
	rows := []json.RawMessage{json.RawMessage(`{"rowId":1,"kind":"assistantText"}`)}
	projectionDigest := mustDigest(t, rawRowsValue(rows))
	api := &shareAPI{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.writeData(w, map[string]any{
			"schema_version": 1, "import_grant_id": "grant-1", "import_grant_expires_at": 1.0,
			"share":     map[string]any{"share_id": "s1", "title": "t", "access_mode": "public_importable", "created_at": 1.0, "expires_at": 2.0},
			"rows":      []json.RawMessage{json.RawMessage(`{"rowId":1,"kind":"assistantText","injected":true}`)},
			"artifacts": []any{},
			"integrity": map[string]any{
				"projection_sha256": projectionDigest, "artifact_set_sha256": mustDigest(t, []any{}),
			},
		})
	}))
	defer server.Close()
	service := NewService(ServiceOptions{
		Client:         NewClient(ClientOptions{BaseURL: server.URL, Doer: server.Client(), Token: func(context.Context) (string, error) { return "t", nil }}),
		ArtifactSource: NewLocalArtifactSource(),
	})
	if _, err := service.GetContinuation(context.Background(), "CODE", "req-1"); err == nil {
		t.Fatal("a tampered continuation must be rejected")
	}
}

func TestGetContinuationAcceptsVerifiedPayload(t *testing.T) {
	rows := []json.RawMessage{json.RawMessage(`{"rowId":1,"kind":"assistantText"}`)}
	api := &shareAPI{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.writeData(w, map[string]any{
			"schema_version": 1, "import_grant_id": "grant-1", "import_grant_expires_at": 1.0,
			"share":     map[string]any{"share_id": "s1", "title": "t", "access_mode": "public_importable", "created_at": 1.0, "expires_at": 2.0},
			"rows":      rows,
			"artifacts": []any{},
			"integrity": map[string]any{
				"projection_sha256": mustDigest(t, rawRowsValue(rows)), "artifact_set_sha256": mustDigest(t, []any{}),
			},
		})
	}))
	defer server.Close()
	service := NewService(ServiceOptions{
		Client:         NewClient(ClientOptions{BaseURL: server.URL, Doer: server.Client(), Token: func(context.Context) (string, error) { return "t", nil }}),
		ArtifactSource: NewLocalArtifactSource(),
	})
	continuation, err := service.GetContinuation(context.Background(), "CODE", "req-1")
	if err != nil {
		t.Fatalf("getContinuation: %v", err)
	}
	if continuation.ImportGrantID != "grant-1" {
		t.Fatalf("continuation = %#v", continuation)
	}
}

func TestAuthenticationRequiredWithoutToken(t *testing.T) {
	api := &shareAPI{}
	server := httptest.NewServer(api.handler(t))
	defer server.Close()
	service := NewService(ServiceOptions{
		Client: NewClient(ClientOptions{
			BaseURL: server.URL, Doer: server.Client(),
			Token: func(context.Context) (string, error) { return "", nil },
		}),
		ArtifactSource: NewLocalArtifactSource(),
	})
	_, err := service.GetCapabilities(context.Background())
	typed, ok := isClientError(err)
	if !ok || typed.Kind() != KindAuthenticationRequired {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeConnectionErrorMapsFaults(t *testing.T) {
	for _, fault := range []string{
		"fault.conversation.rowsRangeConnectionUntrusted",
		"fault.connection.handshakeRequired",
		"fault.connection.closed",
	} {
		converted := normalizeConnectionError(errors.New(fault))
		typed, ok := isServiceError(converted)
		if !ok || typed.Kind() != KindConnectionUnavailable {
			t.Fatalf("%s was not mapped: %v", fault, converted)
		}
	}
	other := errors.New("boom")
	if normalizeConnectionError(other) != other {
		t.Fatal("unrelated errors must pass through unchanged")
	}
}

func TestAttachmentFaultClassifiers(t *testing.T) {
	if !isDefiniteMissingAttachment(&faultError{code: faultShareStatNotFound}) ||
		!isDefiniteMissingAttachment(&faultError{code: faultStatNotFile}) {
		t.Fatal("stat not-found/not-a-file are definite misses")
	}
	if isDefiniteMissingAttachment(&faultError{code: faultShareStatNotAuthorized}) {
		t.Fatal("an authorization error is not a definite miss")
	}
	if !isAttachmentAuthorizationError(&faultError{code: faultShareReadNotAuthorized}) ||
		!isAttachmentAuthorizationError(&faultError{code: faultShareStatConnectionUntrusted}) {
		t.Fatal("authorization faults are not classified")
	}
	if !isAttachmentTooLargeError(&faultError{code: faultShareStatTooLarge}) ||
		!isAttachmentTooLargeError(&faultError{code: faultPreviewTooLarge}) {
		t.Fatal("too-large faults are not classified")
	}
}
