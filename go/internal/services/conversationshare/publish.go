// publish.go ports publishWithAgent/publishInternal from
// conversationShareService.ts plus buildConversationShareArtifactSnapshot
// from conversationShareArtifactDiscovery.ts: the input-attachment staging,
// registered-artifact re-verification, preview-artifact discovery, the
// preparation → upload → confirm-until-ready upload flow.
package conversationshare

import (
	"context"
	"encoding/json"
	"strings"
)

// jsonMarshal / jsonUnmarshalValue / mustMarshal are the tiny json seam.
func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }

func jsonUnmarshalValue(data []byte, target *any) error { return json.Unmarshal(data, target) }

func mustMarshal(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return encoded
}

// PublishInput ports PublishTextConversationInput.
type PublishInput struct {
	WorkspaceRef
	Title                string    `json:"title"`
	AccessMode           string    `json:"accessMode"`
	Selection            Selection `json:"selection"`
	ClientRequestID      string    `json:"clientRequestId"`
	DisclosureAcceptedAt int64     `json:"disclosureAcceptedAt"`
	Locale               string    `json:"locale,omitempty"`
}

// artifactSnapshot ports ConversationShareArtifactSnapshot.
type artifactSnapshot struct {
	rows                []row
	bytesBySourceRef    map[string][]byte
	additionalArtifacts []PublicArtifact
	issues              []Issue
	warnings            []Issue
}

// Publish ports publish.
func (s *Service) Publish(ctx context.Context, input PublishInput, operationID string) (ShareRecord, error) {
	record, err := s.publishInternal(ctx, input, operationID, s.agent)
	if err != nil {
		return ShareRecord{}, normalizeConnectionError(err)
	}
	// 发布成功即本次分享结束：回收该 session 的预检快照（长生命周期 host 防泄漏）。
	s.dropSnapshots(preflightKeyOf(input.WorkspaceRef, ""))
	return record, nil
}

func (s *Service) publishInternal(ctx context.Context, input PublishInput, operationID string, agent AgentService) (ShareRecord, error) {
	s.reportPublishProgress(operationID, "collecting", 0, 0, nil, nil)
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.ClientRequestID) == "" {
		return ShareRecord{}, newServiceError(KindInvalidContract, "Share title and request identity are required", errorDetails{})
	}
	if input.DisclosureAcceptedAt <= 0 {
		return ShareRecord{}, newServiceError(KindDisclosureRequired, "Explicit disclosure confirmation is required", errorDetails{})
	}
	if input.Selection.Kind == "rowAnchors" && len(input.Selection.RowIDs) == 0 {
		return ShareRecord{}, newServiceError(KindInvalidSelection, "At least one conversation row must be selected", errorDetails{})
	}

	capabilities, err := s.client.GetCapabilities(ctx)
	if err != nil {
		return ShareRecord{}, err
	}
	if !containsString(capabilities.AccessModes, input.AccessMode) {
		return ShareRecord{}, newServiceError(KindFeatureDisabled, "Requested share access mode is unavailable", errorDetails{})
	}

	loaded, err := s.loadAllRows(ctx, input.WorkspaceRef)
	if err != nil {
		return ShareRecord{}, err
	}
	selected, productTurnIDs, err := selectRows(loaded.rows, input.Selection)
	if err != nil {
		return ShareRecord{}, err
	}
	structureSanitized := sanitizeUnsupportedShareStructures(selected)
	artifactSanitizedRows, artifactWarnings := sanitizeUnsupportedShareArtifacts(
		removeIndependentArtifactRows(structureSanitized), capabilities, loaded.rows)
	selectedRows := artifactSanitizedRows
	publishWarnings := append([]Issue{}, artifactWarnings...)
	structureIssues := collectShareStructureIssues(selectedRows, loaded.rows)
	if len(structureIssues) > 0 {
		structureKind := KindInvalidConversation
		hasProtocol := false
		hasUnsafe := false
		for _, issue := range structureIssues {
			if issue.Code == ReasonArtifactProtocol {
				hasProtocol = true
			}
			if issue.Code == ReasonUnsafeURL {
				hasUnsafe = true
			}
		}
		if hasProtocol {
			structureKind = KindArtifactProtocolNotReady
		} else if hasUnsafe {
			structureKind = KindUnsafeStructure
		}
		return ShareRecord{}, newServiceError(structureKind, "Selected conversation contains unsupported structure", errorDetails{Issues: structureIssues})
	}
	// 本地运行投影包含 subagent 详情与写入态 ID；confirm 前必须先生成独立公开投影。
	registeredProjection, err := BuildPublicProjection(selectedRows, productTurnIDs, nil)
	if err != nil {
		return ShareRecord{}, err
	}
	if err := validateArtifactManifest(capabilities, registeredProjection.Artifacts); err != nil {
		return ShareRecord{}, err
	}

	snapshot, err := s.buildArtifactSnapshot(ctx, agent, input, capabilities, selectedRows, registeredProjection, loaded)
	if err != nil {
		return ShareRecord{}, err
	}
	if len(snapshot.issues) > 0 {
		return ShareRecord{}, newServiceError(KindArtifactNotAllowed, "Conversation result artifacts are not allowed by server capabilities", errorDetails{Issues: snapshot.issues})
	}
	if len(snapshot.warnings) > 0 {
		publishWarnings = append(publishWarnings, snapshot.warnings...)
	}
	if len(publishWarnings) > 0 {
		// 非阻断：让分享者知道哪些真实文件没有进入链接。
		sanitized, _, omitted := SanitizedIssues(publishWarnings)
		s.reportPublishProgress(operationID, "collecting", 0, 0, sanitized, &omitted)
	}
	publicProjection, err := BuildPublicProjection(snapshot.rows, productTurnIDs, snapshot.additionalArtifacts)
	if err != nil {
		return ShareRecord{}, err
	}
	if err := validateArtifactManifest(capabilities, publicProjection.Artifacts); err != nil {
		return ShareRecord{}, err
	}
	if len(publicProjection.Rows) > capabilities.MaxRows {
		actual := int64(len(publicProjection.Rows))
		limit := int64(capabilities.MaxRows)
		return ShareRecord{}, newServiceError(KindLimitExceeded, "Conversation contains too many rows to share", errorDetails{Issues: []Issue{{
			Code: "rows_limit", Scope: ScopeConversation, Actual: &actual, Limit: &limit,
		}}})
	}

	confirmRequest, err := buildConfirmRequest(publicProjection.SelectedProductTurnIDs, publicProjection.Rows, descriptorsOf(publicProjection.Artifacts), input.DisclosureAcceptedAt)
	if err != nil {
		return ShareRecord{}, err
	}
	confirmBytes, err := marshalJSONBytes(confirmRequest)
	if err != nil {
		return ShareRecord{}, err
	}
	if int64(len(confirmBytes)) > capabilities.MaxPayloadBytes {
		actual := int64(len(confirmBytes))
		limit := capabilities.MaxPayloadBytes
		return ShareRecord{}, newServiceError(KindLimitExceeded, "Conversation share payload is too large", errorDetails{Issues: []Issue{{
			Code: "payload_size_limit", Scope: ScopeConversation, Actual: &actual, Limit: &limit,
		}}})
	}

	payloadDigest, err := sha256CanonicalJSON(treeOfRaw(confirmRequest))
	if err != nil {
		return ShareRecord{}, err
	}
	preparation, err := s.client.CreatePreparation(ctx, PreparationRequest{
		ClientRequestID: input.ClientRequestID, Title: strings.TrimSpace(input.Title),
		SchemaVersion: SchemaVersion, AccessMode: input.AccessMode,
		PayloadSha256: payloadDigest, ArtifactCount: len(publicProjection.Artifacts),
	})
	if err != nil {
		return ShareRecord{}, err
	}
	if preparation.Status == "confirmed" && preparation.Share != nil {
		s.reportPublishProgress(operationID, "complete", len(publicProjection.Artifacts), len(publicProjection.Artifacts), nil, nil)
		return *preparation.Share, nil
	}
	s.reportPublishProgress(operationID, "uploading", 0, len(publicProjection.Artifacts), nil, nil)
	uploaded := 0
	for _, artifact := range publicProjection.Artifacts {
		bytes, ok := snapshot.bytesBySourceRef[artifact.SourceRef]
		if !ok {
			return ShareRecord{}, newServiceError(KindInvalidConversation, "Conversation artifact bytes are missing from the publication snapshot", errorDetails{})
		}
		upload, err := s.client.UploadArtifact(ctx, preparation.PreparationID, artifact.Descriptor, artifact.Descriptor.DisplayName, bytes)
		if err != nil {
			return ShareRecord{}, err
		}
		if upload.ArtifactID != artifact.Descriptor.ArtifactID ||
			upload.SizeBytes != artifact.Descriptor.SizeBytes ||
			upload.Sha256 != artifact.Descriptor.Sha256 {
			actual := upload.SizeBytes
			limit := artifact.Descriptor.SizeBytes
			return ShareRecord{}, newServiceError(KindUploadIncomplete, "Conversation artifact upload acknowledgement does not match the manifest", errorDetails{Issues: []Issue{{
				Code: KindUploadIncomplete, Scope: ScopeArtifact,
				ArtifactDisplayName: &artifact.Descriptor.DisplayName,
				ArtifactType:        &artifact.Descriptor.ArtifactType,
				Extension:           &artifact.Descriptor.Extension,
				MimeType:            &artifact.Descriptor.MimeType,
				Actual:              &actual, Limit: &limit,
			}}})
		}
		uploaded++
		s.reportPublishProgress(operationID, "uploading", uploaded, len(publicProjection.Artifacts), nil, nil)
	}
	s.reportPublishProgress(operationID, "checking", uploaded, len(publicProjection.Artifacts), nil, nil)
	share, err := s.confirmUntilReady(ctx, preparation.PreparationID, confirmRequest)
	if err != nil {
		return ShareRecord{}, err
	}
	s.reportPublishProgress(operationID, "complete", uploaded, len(publicProjection.Artifacts), nil, nil)
	return share, nil
}

// confirmUntilReady ports confirmUntilReady: 3215 是后端安全检查的非终态，
// 同一 preparation/DTO 必须串行重试；不能重新 prepare，也不能无限等待。
func (s *Service) confirmUntilReady(ctx context.Context, preparationID string, request *ConfirmRequest) (ShareRecord, error) {
	var deadlineMs int64
	var lastRequestID string
	var lastStatus, lastCode *int64
	for {
		share, err := s.client.Confirm(ctx, preparationID, request)
		if err == nil {
			return share, nil
		}
		clientErr, ok := isClientError(err)
		if !ok || clientErr.kind != KindSafetyCheckPending {
			return ShareRecord{}, err
		}
		if clientErr.requestID != "" {
			lastRequestID = clientErr.requestID
		}
		if clientErr.status != 0 {
			status := clientErr.status
			lastStatus = &status
		}
		if clientErr.hasCode {
			code := clientErr.code
			lastCode = &code
		}
		currentMs := s.now().UnixMilli()
		if deadlineMs == 0 {
			deadlineMs = currentMs + s.confirmPollTimeoutMs
		}
		remainingMs := deadlineMs - currentMs
		if remainingMs <= 0 {
			return ShareRecord{}, s.safetyCheckTimeout(lastRequestID, lastStatus, lastCode)
		}
		requestedDelayMs := s.confirmPollIntervalMs
		if clientErr.hasRetry && clientErr.retryAfterMs > 0 {
			requestedDelayMs = clientErr.retryAfterMs
		}
		if requestedDelayMs > remainingMs {
			requestedDelayMs = remainingMs
		}
		if sleepErr := s.sleep(ctx, requestedDelayMs); sleepErr != nil {
			return ShareRecord{}, sleepErr
		}
		if s.now().UnixMilli() >= deadlineMs {
			return ShareRecord{}, s.safetyCheckTimeout(lastRequestID, lastStatus, lastCode)
		}
	}
}

func (s *Service) safetyCheckTimeout(requestID string, status, code *int64) *serviceError {
	details := errorDetails{RequestID: requestID, Status: status, Code: code}
	return newServiceError(KindSafetyCheckTimeout, "Conversation share safety check timed out", details)
}
