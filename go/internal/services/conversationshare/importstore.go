// importstore.go holds the import flow plus its on-disk state: the completed
// import index, the readonly copy lookup and the shared-context formatter.
// Split out of import.go so each file stays readable; symbols are unchanged.
package conversationshare

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ImportShare ports importShare (dedupe + single-flight per workspace).
func (s *Service) ImportShare(ctx context.Context, input ImportInput, operationID string) (ImportResult, error) {
	if s.sessions == nil {
		return ImportResult{}, newServiceError(KindFeatureDisabled, "Conversation share import is unavailable", errorDetails{})
	}
	workspaceKey := workspaceKeyOf(input.TargetWorkspacePath, input.TargetWorkspaceIdentity)
	workspaceKeyedShare := importDedupeKey(input.ShareCode, workspaceKey)
	key := workspaceKeyedShare + "\x00" + input.ClientRequestID

	s.importState.mu.Lock()
	if completed, ok := s.importState.completed[key]; ok {
		s.importState.mu.Unlock()
		completed.Reused = true
		return completed, nil
	}
	if completed, ok := s.importState.completedByWorkspace[workspaceKeyedShare]; ok {
		s.importState.mu.Unlock()
		completed.Reused = true
		return completed, nil
	}
	if flight, ok := s.importState.inFlight[key]; ok {
		s.importState.mu.Unlock()
		select {
		case result := <-flight:
			return result, nil
		case <-ctx.Done():
			return ImportResult{}, ctx.Err()
		}
	}
	flight := make(chan ImportResult, 1)
	s.importState.inFlight[key] = flight
	s.importState.mu.Unlock()

	result, err := s.importShareInternal(ctx, input, operationID)
	s.importState.mu.Lock()
	delete(s.importState.inFlight, key)
	if err == nil {
		s.importState.completed[key] = result
		s.importState.completedByWorkspace[workspaceKeyedShare] = result
	}
	s.importState.mu.Unlock()
	if err != nil {
		return ImportResult{}, err
	}
	select {
	case flight <- result:
	default:
	}
	// 持久化失败不影响返回值（TS：调用方自己决定是否吞掉本次失败）。
	_ = s.persistCompletedImportIndex()
	return result, nil
}

func (s *Service) importShareInternal(ctx context.Context, input ImportInput, operationID string) (ImportResult, error) {
	if s.sessions == nil {
		return ImportResult{}, newServiceError(KindFeatureDisabled, "Conversation share import is unavailable", errorDetails{})
	}
	continuation, err := s.GetContinuation(ctx, input.ShareCode, input.ClientRequestID)
	if err != nil {
		return ImportResult{}, err
	}
	remoteTarget := input.TargetWorkspaceKind == "remote" || input.TargetWorkspaceIdentity != ""
	workspacePath := s.conversationWorkspaceRoot
	if input.TargetWorkspacePath != "" && !remoteTarget {
		workspacePath = input.TargetWorkspacePath
	}
	workspaceIdentity := ""
	if input.TargetWorkspaceIdentity != "" && !remoteTarget {
		workspaceIdentity = input.TargetWorkspaceIdentity
	}
	shareRoot := filepath.Join(workspacePath, ".nex-share")
	importRoot := filepath.Join(shareRoot, sanitizeFileSegment(continuation.Share.ShareID))
	markerPath := filepath.Join(importRoot, ".nex-share-import.json")
	stagingPath := filepath.Join(importRoot, ".share-import-staging")
	finalArtifactsPath := filepath.Join(importRoot, "shared-artifacts")
	conversationPath := filepath.Join(importRoot, "shared-conversation.json")
	importID := newRequestID()
	contextID := "shared-context-" + newRequestID()
	sessionID := "share-import-" + newRequestID()
	// 这个 URL 会进持久化的 provenance：durable 记录不该存随界面语言变化的值。
	shareURL := s.shareWebURL + "/" + pathEscape(input.ShareCode)

	if err := os.MkdirAll(shareRoot, 0o755); err != nil {
		return ImportResult{}, err
	}
	// 已完成的 marker 复用既有 session；不完整的目录继续创建。
	if marker, err := readImportMarker(markerPath); err == nil &&
		marker["shareCode"] == input.ShareCode &&
		strings.HasPrefix(stringOf(marker["sessionId"]), "share-import-") {
		sessions, listErr := s.sessions.ListSessions(ctx, workspacePath, 100)
		if listErr == nil {
			for _, session := range sessions {
				if session.SessionID == stringOf(marker["sessionId"]) {
					contextIDValue, _ := marker["contextId"].(string)
					shareURLValue, _ := marker["shareUrl"].(string)
					if contextIDValue != "" && shareURLValue != "" {
						return ImportResult{
							WorkspacePath: workspacePath, WorkspaceIdentity: workspaceIdentity,
							SessionID: session.SessionID, ContextID: contextIDValue,
							ShareURL: shareURLValue, Title: session.Title, Reused: true,
						}, nil
					}
				}
			}
		}
		_ = os.RemoveAll(importRoot)
	}
	// 语义是「失败时是否还允许删 importRoot」：session 一旦提交就必须解除武装。
	importRootCleanupArmed := false
	if err := os.Mkdir(importRoot, 0o755); err != nil {
		if os.IsExist(err) {
			return ImportResult{}, newServiceError(KindUnknown, "Conversation share import is already in progress", errorDetails{})
		}
		return ImportResult{}, err
	}
	importRootCleanupArmed = true
	writeMarker := func(phase string) error {
		marker := map[string]any{
			"importId": importID, "shareCode": input.ShareCode,
			"clientRequestId": input.ClientRequestID, "sessionId": sessionID,
			"shareId": continuation.Share.ShareID, "contextId": contextID,
			"shareUrl": shareURL, "workspaceKey": workspaceKeyOf(workspacePath, workspaceIdentity),
			"phase": phase, "createdAt": s.now().UnixMilli(),
		}
		encoded, err := json.Marshal(marker)
		if err != nil {
			return err
		}
		return os.WriteFile(markerPath, encoded, 0o644)
	}
	if err := writeMarker("preparing"); err != nil {
		_ = os.RemoveAll(importRoot)
		return ImportResult{}, err
	}

	fail := func(err error) (ImportResult, error) {
		// 导入失败清理只能触及本次 import-owned 子目录，不能删 workspace 根。
		if importRootCleanupArmed {
			_ = os.RemoveAll(importRoot)
		}
		return ImportResult{}, err
	}
	if err := os.Mkdir(stagingPath, 0o755); err != nil && !os.IsExist(err) {
		return fail(err)
	}
	// installedArtifactRef lives at file scope (formatSharedContextV1 uses it).
	var installedArtifacts []installedArtifactRef
	usedNames := map[string]bool{}
	completedArtifacts := 0
	s.reportImportProgress(operationID, "downloading", 0, len(continuation.Artifacts))
	if err := writeMarker("downloading"); err != nil {
		return fail(err)
	}
	for _, artifact := range continuation.Artifacts {
		bytes, responseMimeType, downloadErr := s.downloadArtifactBytes(ctx, artifact)
		if downloadErr != nil {
			return fail(downloadErr)
		}
		digest := sha256.Sum256(bytes)
		if int64(len(bytes)) != artifact.SizeBytes || hex.EncodeToString(digest[:]) != artifact.Sha256 {
			actual := int64(len(bytes))
			limit := artifact.SizeBytes
			return fail(newServiceError(KindInvalidContract, "Conversation artifact integrity check failed", errorDetails{Issues: []Issue{{
				Code: ReasonArtifactChanged, Scope: ScopeArtifact,
				ArtifactDisplayName: &artifact.DisplayName, ArtifactType: &artifact.ArtifactType,
				Extension: &artifact.Extension, MimeType: &artifact.MimeType,
				Actual: &actual, Limit: &limit, Phase: phaseDownloading(),
			}}}))
		}
		// content-type 不一致不能判 invalid_contract：对象存储按自己的规则打标签；
		// 字节已通过 size + SHA-256 校验，artifact.mime_type 才是权威值。
		_ = responseMimeType
		fileName := uniqueImportedFileName(artifact.DisplayName, artifact.ArtifactID, usedNames)
		if err := os.WriteFile(filepath.Join(stagingPath, fileName), bytes, 0o644); err != nil {
			return fail(err)
		}
		relative := ".nex-share/" + sanitizeFileSegment(continuation.Share.ShareID) + "/shared-artifacts/" + fileName
		installedArtifacts = append(installedArtifacts, installedArtifactRef{
			ArtifactID: artifact.ArtifactID, WorkspaceRelativePath: relative,
			DisplayName: artifact.DisplayName, MimeType: artifact.MimeType, Sha256: artifact.Sha256,
		})
		completedArtifacts++
		s.reportImportProgress(operationID, "downloading", completedArtifacts, len(continuation.Artifacts))
	}
	s.reportImportProgress(operationID, "installing", completedArtifacts, len(continuation.Artifacts))
	if err := writeMarker("installing"); err != nil {
		return fail(err)
	}
	if err := os.Rename(stagingPath, finalArtifactsPath); err != nil {
		return fail(err)
	}
	// 只读块要能永久离线打开：把公开 rows 与结果物元数据一起落盘。
	readonlyArtifacts := make([]ImportedArtifact, 0, len(continuation.Artifacts))
	for _, artifact := range continuation.Artifacts {
		entry := ImportedArtifact{
			ArtifactID: artifact.ArtifactID, DisplayName: artifact.DisplayName,
			MimeType: artifact.MimeType,
		}
		for _, installed := range installedArtifacts {
			if installed.ArtifactID == artifact.ArtifactID {
				entry.WorkspaceRelativePath = installed.WorkspaceRelativePath
				break
			}
		}
		readonlyArtifacts = append(readonlyArtifacts, entry)
	}
	readonlyCopy := map[string]any{
		"formatVersion": importedConversationShareFormatVersion,
		"shareId":       continuation.Share.ShareID,
		"contextId":     contextID,
		"title":         continuation.Share.Title,
		"rows":          continuation.Rows,
		"artifacts":     readonlyArtifacts,
	}
	encodedCopy, err := json.Marshal(readonlyCopy)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(conversationPath, encodedCopy, 0o644); err != nil {
		return fail(err)
	}
	markdown, markdownSha256, unsupportedKinds := formatSharedContextV1(continuation, installedArtifacts)
	if len(unsupportedKinds) > 0 {
		// 模型侧 shared_context 少了内容：不阻断导入，但必须留痕（无 logger 时静默）。
		_ = unsupportedKinds
	}
	_ = base64.StdEncoding // reserved for attachment read plumbing parity
	s.reportImportProgress(operationID, "committing", completedArtifacts, len(continuation.Artifacts))
	if err := writeMarker("committing"); err != nil {
		return fail(err)
	}
	prefix := importedShareTitlePrefix["zh-CN"]
	if value, ok := importedShareTitlePrefix[input.Locale]; ok {
		prefix = value
	}
	provenanceArtifacts := make([]any, 0, len(installedArtifacts))
	for _, installed := range installedArtifacts {
		provenanceArtifacts = append(provenanceArtifacts, map[string]any{
			"artifactId": installed.ArtifactID, "workspaceRelativePath": installed.WorkspaceRelativePath,
		})
	}
	if _, err := s.sessions.CreateSession(ctx, CreateSessionRequest{
		WorkspacePath: workspacePath, WorkspaceIdentity: workspaceIdentity,
		SessionID: sessionID,
		ImportedHistory: &ImportedHistory{
			Source:   "sharedContext",
			Title:    prefix + strings.TrimSpace(continuation.Share.Title),
			Markdown: markdown,
			Provenance: map[string]any{
				"shareId": continuation.Share.ShareID, "contextId": contextID,
				"shareUrl": shareURL, "status": "pending",
				"projectionSha256":  continuation.Integrity.ProjectionSha256,
				"artifactSetSha256": continuation.Integrity.ArtifactSetSha256,
				"formatterVersion":  1, "markdownSha256": markdownSha256,
				"installedArtifacts": provenanceArtifacts,
			},
		},
	}); err != nil {
		return fail(err)
	}
	// session 已提交：失败半径必须止于 createSession 之前，立刻解除清理武装。
	importRootCleanupArmed = false
	_ = os.Remove(markerPath)
	s.reportImportProgress(operationID, "complete", completedArtifacts, len(continuation.Artifacts))
	result := ImportResult{
		WorkspacePath: workspacePath, WorkspaceIdentity: workspaceIdentity,
		SessionID: sessionID, ContextID: contextID, ShareURL: shareURL,
		Title: continuation.Share.Title, Reused: false,
	}
	if remoteTarget {
		result.FallbackReason = "remote_workspace"
	} else if input.TargetWorkspacePath == "" {
		result.FallbackReason = "default_workspace"
	}
	return result, nil
}

func phaseDownloading() *string {
	phase := "downloading"
	return &phase
}

func stringOf(value any) string {
	text, _ := value.(string)
	return text
}

// readImportMarker reads and parses the marker JSON.
func readImportMarker(markerPath string) (map[string]any, error) {
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return nil, err
	}
	var marker map[string]any
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, err
	}
	return marker, nil
}

// downloader is the injected download seam (defaults to http.Client with the
// 120s timeout + content-length precheck semantics of downloadArtifactBytes).
type downloader func(ctx context.Context, url string) (*http.Response, error)

// downloadArtifactBytes ports downloadArtifactBytes: 带单请求超时（含读
// body）与 Content-Length 预检——完整性校验放在无界下载之后只保证正确性，
// 不保护客户端资源。
func (s *Service) downloadArtifactBytes(ctx context.Context, artifact continuationArtifact) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeoutMs*time.Millisecond)
	defer cancel()
	response, err := s.download(ctx, artifact.DownloadURL)
	if err != nil {
		return nil, "", &clientError{kind: KindNetwork, message: "Conversation artifact download failed"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", &clientError{kind: KindNetwork, message: "Conversation artifact download failed"}
	}
	if declared := response.Header.Get("Content-Length"); declared != "" {
		var declaredBytes int64
		if _, err := fmt.Sscanf(declared, "%d", &declaredBytes); err == nil && declaredBytes > artifact.SizeBytes {
			// body 已确定不会通过校验：先中断连接再抛错，不把超大响应读进内存。
			return nil, "", newServiceError(KindInvalidContract, "Conversation artifact integrity check failed", errorDetails{Issues: []Issue{{
				Code: ReasonArtifactChanged, Scope: ScopeArtifact,
				ArtifactDisplayName: &artifact.DisplayName, ArtifactType: &artifact.ArtifactType,
				Extension: &artifact.Extension, MimeType: &artifact.MimeType,
				Actual: &declaredBytes, Limit: &artifact.SizeBytes, Phase: phaseDownloading(),
			}}})
		}
	}
	bytes, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, "", &clientError{kind: KindNetwork, message: "Conversation artifact download timed out"}
	}
	responseMimeType := strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0])
	return bytes, responseMimeType, nil
}
