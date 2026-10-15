// service.go ports the ConversationShareService core (conversationShareService.ts):
// preflight (structure/attachment checks), publish (projection → preparation →
// upload → confirm-until-ready) and the shared progress emitters.
//
// The agent V4 reads (conversationRowsRangeV4 / conversationFileChangesV4 /
// conversationAttachmentStatV4 / conversationAttachmentReadV4) are an injected
// seam (AgentService) — the Go agent bridge (step 3.10) will supply the real
// one; tests supply a fixture agent.
package conversationshare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"time"
)

// ChannelName is the rpc channel name (ServiceChannels.ConversationShare).
const ChannelName = "conversation-share"

// ServiceOptions ports ConversationShareServiceOptions.
type ServiceOptions struct {
	Agent                 AgentService
	Client                *Client
	ArtifactSource        ArtifactSource
	Sessions              SessionService
	ConfirmPollIntervalMs int64
	ConfirmPollTimeoutMs  int64
	Now                   func() time.Time
	Sleep                 func(ctx context.Context, delayMs int64) error
	// Download fetches one artifact download URL (defaults to a 120s-bounded
	// http.Client; tests point it at an httptest server).
	Download                  downloader
	ConversationWorkspaceRoot string
	ShareWebURL               string
}

// Service is the ConversationShareService port.
type Service struct {
	agent                     AgentService
	client                    *Client
	artifactSource            ArtifactSource
	sessions                  SessionService
	confirmPollIntervalMs     int64
	confirmPollTimeoutMs      int64
	now                       func() time.Time
	sleep                     func(ctx context.Context, delayMs int64) error
	download                  downloader
	conversationWorkspaceRoot string
	shareWebURL               string

	mu                     sync.Mutex
	progressHandlers       map[string][]func(PublishProgress)
	importProgressHandlers map[string][]func(ImportProgress)
	publishPhases          map[string]string

	importState   *importState
	importIndexMu sync.Mutex

	// previewPreflightSnapshots is shared by preflight and publish (the TS keeps
	// it on the service too), so every access goes through snapshotMu.
	previewPreflightSnapshots *orderedSnapshots
	snapshotMu                sync.Mutex
}

// previewPreflightSnapshot ports ConversationSharePreviewPreflightSnapshot.
type previewPreflightSnapshot struct {
	revision                int
	logEpoch                string
	capabilitiesFingerprint string
	candidateFingerprint    string
	visibleSourceRefs       []string
}

const previewPreflightSnapshotMaxEntries = 200

// NewService builds the service.
func NewService(options ServiceOptions) *Service {
	interval := options.ConfirmPollIntervalMs
	if interval <= 0 {
		interval = 5_000
	}
	timeout := options.ConfirmPollTimeoutMs
	if timeout <= 0 {
		timeout = 120_000
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, delayMs int64) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(delayMs) * time.Millisecond):
				return nil
			}
		}
	}
	shareWebURL := strings_TrimRight(options.ShareWebURL, "/")
	download := options.Download
	if download == nil {
		httpClient := &http.Client{}
		download = func(ctx context.Context, url string) (*http.Response, error) {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			return httpClient.Do(request)
		}
	}
	service := &Service{
		agent: options.Agent, client: options.Client, artifactSource: options.ArtifactSource,
		sessions:              options.Sessions,
		confirmPollIntervalMs: interval, confirmPollTimeoutMs: timeout, now: now, sleep: sleep,
		download:                  download,
		conversationWorkspaceRoot: options.ConversationWorkspaceRoot,
		shareWebURL:               shareWebURL,
		progressHandlers:          map[string][]func(PublishProgress){},
		importProgressHandlers:    map[string][]func(ImportProgress){},
		publishPhases:             map[string]string{},
		importState: &importState{
			completed: map[string]ImportResult{}, completedByWorkspace: map[string]ImportResult{},
			inFlight: map[string]chan ImportResult{}, inFlightErrors: map[string]error{},
		},
		previewPreflightSnapshots: &orderedSnapshots{limit: previewPreflightSnapshotMaxEntries},
	}
	service.loadCompletedImportIndex()
	return service
}

// GetCapabilities ports getCapabilities.
func (s *Service) GetCapabilities(ctx context.Context) (Capabilities, error) {
	return s.client.GetCapabilities(ctx)
}

// GetPreview ports getPreview (raw pass-through of the decoded wire data).
func (s *Service) GetPreview(ctx context.Context, shareCode string) (PreviewData, error) {
	return s.client.GetPreview(ctx, shareCode)
}

// GetContinuation ports getContinuation.
func (s *Service) GetContinuation(ctx context.Context, shareCode, clientRequestID string) (Continuation, error) {
	continuation, err := s.client.GetContinuation(ctx, shareCode, clientRequestID)
	if err != nil {
		return Continuation{}, err
	}
	if !verifyIntegrity(rawRowsValue(continuation.Rows), artifactsValue(continuation.Artifacts),
		struct{ Projection, ArtifactSet string }{continuation.Integrity.ProjectionSha256, continuation.Integrity.ArtifactSetSha256}) {
		return Continuation{}, &clientError{kind: KindInvalidContract, message: "Conversation share integrity check failed"}
	}
	return continuation, nil
}

func rawRowsValue(rows []json.RawMessage) any {
	values := make([]any, len(rows))
	for index, raw := range rows {
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			values[index] = value
		} else {
			values[index] = nil
		}
	}
	return values
}

func artifactsValue(artifacts []continuationArtifact) any {
	values := make([]any, len(artifacts))
	for index, artifact := range artifacts {
		encoded, err := json.Marshal(artifact)
		if err != nil {
			continue
		}
		var value any
		if err := json.Unmarshal(encoded, &value); err == nil {
			values[index] = value
		}
	}
	return values
}

// loadAllRows ports loadAllRows: backwards pagination with the revision /
// log-epoch watermark and global rowId ordering check.
func (s *Service) loadAllRows(ctx context.Context, ref WorkspaceRef) (loadedRows, error) {
	var pages [][]row
	var beforeRowID *int64
	logEpoch := ""
	revision := 0
	for {
		result, err := s.agent.ConversationRowsRangeV4(ctx, RowsRangeRequest{
			WorkspaceRef: ref, BeforeRowID: beforeRowID, Limit: rowsRangeMaxLimit,
		})
		if err != nil {
			return loadedRows{}, normalizeConnectionError(err)
		}
		if (logEpoch != "" && result.AtLogEpoch != logEpoch) || (pages != nil && result.AtRevision != revision) {
			return loadedRows{}, newServiceError(KindInvalidConversation, "Conversation changed while preparing share", errorDetails{})
		}
		logEpoch = result.AtLogEpoch
		revision = result.AtRevision
		pages = append([][]row{result.Rows}, pages...)
		if !result.HasMore {
			break
		}
		if len(result.Rows) == 0 {
			return loadedRows{}, newServiceError(KindInvalidContract, "Conversation row pagination did not advance", errorDetails{})
		}
		firstRowID, ok := result.Rows[0].rowID()
		if !ok || (beforeRowID != nil && firstRowID == *beforeRowID) {
			return loadedRows{}, newServiceError(KindInvalidContract, "Conversation row pagination did not advance", errorDetails{})
		}
		beforeRowID = &firstRowID
	}
	var rows []row
	for _, page := range pages {
		rows = append(rows, page...)
	}
	for index := 1; index < len(rows); index++ {
		previous, _ := rows[index-1].rowID()
		current, _ := rows[index].rowID()
		if previous >= current {
			return loadedRows{}, newServiceError(KindInvalidContract, "Conversation rows are not globally ordered", errorDetails{})
		}
	}
	if logEpoch == "" {
		return loadedRows{}, newServiceError(KindInvalidContract, "Conversation rows are missing a read watermark", errorDetails{})
	}
	return loadedRows{rows: rows, revision: revision, logEpoch: logEpoch}, nil
}

// reportPublishProgress fires the progress event (bounded channel in the
// channel adapter; here the handler list).
func (s *Service) reportPublishProgress(operationID, phase string, completed, total int, warnings []Issue, omitted *int) {
	s.mu.Lock()
	s.publishPhases[operationID] = phase
	handlers := append([]func(PublishProgress){}, s.progressHandlers[operationID]...)
	s.mu.Unlock()
	progress := PublishProgress{
		OperationID: operationID, Phase: phase,
		CompletedArtifacts: completed, TotalArtifacts: total,
		Warnings: warnings, OmittedWarningCount: omitted,
	}
	for _, handler := range handlers {
		handler(progress)
	}
}

// OnPublishProgress ports onDynamicPublishProgress(operationId).
func (s *Service) OnPublishProgress(operationID string, handler func(PublishProgress)) (cancel func()) {
	s.mu.Lock()
	s.progressHandlers[operationID] = append(s.progressHandlers[operationID], handler)
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			handlers := s.progressHandlers[operationID]
			for index, candidate := range handlers {
				// 函数值不可比较，按指针身份（同一 closure 变量）匹配。
				if reflect.ValueOf(candidate).Pointer() == reflect.ValueOf(handler).Pointer() {
					s.progressHandlers[operationID] = append(handlers[:index], handlers[index+1:]...)
					break
				}
			}
		})
	}
}

// preflightKeyOf ports previewPreflightKey.
func preflightKeyOf(ref WorkspaceRef, productTurnID string) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s",
		workspaceKeyOf(ref.WorkspacePath, ref.WorkspaceIdentity),
		ref.RemoteSessionID, ref.SessionID, productTurnID)
}

// workspaceKeyOf ports workspaceKeyOf (AGENTS.md identity rule).
func workspaceKeyOf(path, identity string) string {
	trimmedIdentity := trimSpace(identity)
	if trimmedIdentity != "" {
		return trimmedIdentity
	}
	trimmedPath := trimSpace(path)
	if trimmedPath != "" {
		return trimmedPath
	}
	return "__default_conversation_workspace__"
}

func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end && isSpaceByte(value[start]) {
		start++
	}
	for end > start && isSpaceByte(value[end-1]) {
		end--
	}
	return value[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// marshalToTree is the json.Marshal→Unmarshal round trip used where a typed
// value must join a canonical digest.
func marshalToTree(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var tree any
	if err := json.Unmarshal(encoded, &tree); err != nil {
		return nil, err
	}
	return tree, nil
}

// strings_TrimRight is strings.TrimRight kept local to limit import noise.
func strings_TrimRight(value, cutset string) string {
	for len(value) > 0 && containsRune(cutset, rune(value[len(value)-1])) {
		value = value[:len(value)-1]
	}
	return value
}

func containsRune(set string, r rune) bool {
	for _, candidate := range set {
		if candidate == r {
			return true
		}
	}
	return false
}
