// preflight.go ports preflightWithAgent from conversationShareService.ts:
// selection → structure sanitize → attachment existence checks (with bounded
// concurrency semantics kept by collecting first, classifying in order) →
// manifest issues → preview-candidate checks.
//
// 简化范围说明：预览卡片候选发现（buildConversationPreviewArtifactCandidates，
// conversation-preview-artifacts.ts 的 5 个正则提取器）是 shared 层的独立纯函数，
// 属于公开投影/预览共享路径；本端口将其作为注入 seam（PreviewCandidates
// 函数）暴露，默认实现提供 citation/markdown-link/file-url 三种最常见的
// 引用形态。完整五正则提取器与 UI 预览共享，等 Phase 4 需要 UI 侧同源时
// 一并端口到 go/internal（决策记录在 decisions.md）。
//
// 引用提取与路径归一化在 previewref.go；本文件只保留预检的服务流程。

package conversationshare

import (
	"context"
	"strings"
)

// Preflight ports preflight.
func (s *Service) Preflight(ctx context.Context, input PreflightInput) (PreflightResult, error) {
	result, err := s.preflightWithAgent(ctx, input, s.agent)
	if err != nil {
		return PreflightResult{}, normalizeConnectionError(err)
	}
	return result, nil
}

func (s *Service) preflightWithAgent(ctx context.Context, input PreflightInput, agent AgentService) (PreflightResult, error) {
	capabilities, err := s.client.GetCapabilities(ctx)
	if err != nil {
		return PreflightResult{}, err
	}
	supportedArtifactTypes := allowedArtifactSummaries(capabilities)
	loaded, err := s.loadAllRows(ctx, input.WorkspaceRef)
	if err != nil {
		return PreflightResult{}, err
	}
	rows, revision, logEpoch := loaded.rows, loaded.revision, loaded.logEpoch

	var blockingIssues, skippableWarnings, deferredIssues []Issue
	selected, productTurnIDs, selectionErr := selectRows(rows, input.Selection)
	if selectionErr != nil {
		if _, isShareErr := isServiceError(selectionErr); isShareErr {
			blockingIssues = append(blockingIssues, Issue{Code: ReasonInvalidSelection, Scope: ScopeConversation})
		} else {
			return PreflightResult{}, selectionErr
		}
		return PreflightResult{
			Revision: revision, LogEpoch: logEpoch,
			CapabilitiesFingerprint: capabilitiesFingerprint(capabilities),
			BlockingIssues:          blockingIssues,
			SupportedArtifactTypes:  supportedArtifactTypes,
			TurnResults:             []TurnPreflightResult{},
		}, nil
	}

	structureSanitized := sanitizeUnsupportedShareStructures(selected)
	artifactSanitizedRows, artifactWarnings := sanitizeUnsupportedShareArtifacts(
		removeIndependentArtifactRows(structureSanitized), capabilities, rows)
	selectedRows := artifactSanitizedRows
	skippableWarnings = append(skippableWarnings, artifactWarnings...)
	blockingIssues = append(blockingIssues, collectShareStructureIssues(selectedRows, rows)...)
	hasShareableContent := anyShareableContent(selectedRows)

	ordinals := turnOrdinalByProductTurn(rows)
	// 收集需要 stat 的附件，串行分类保持 issue 顺序与串行实现一致。
	type attachmentCheck struct {
		rowIndex        int
		attachmentIndex int
		attachment      map[string]any
		allowed         bool
		baseIssue       Issue
		row             row
	}
	var checks []attachmentCheck
	for _, current := range selectedRows {
		if current.kind() != "userInput" {
			continue
		}
		attachments, ok := current["attachments"].([]any)
		if !ok {
			continue
		}
		for attachmentIndex, entry := range attachments {
			attachment, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			fileName, _ := attachment["fileName"].(string)
			mime, _ := attachment["mime"].(string)
			extension, _ := fileExtension(fileName)
			mimeType := normalizedMimeType(mime)
			baseIssue := Issue{Scope: ScopeArtifact}
			if rowID, ok := current.rowID(); ok {
				value := rowID
				baseIssue.RowID = &value
			}
			attachTurnIdentity(&baseIssue, current, ordinals)
			displayName := attachmentDisplayName(fileName)
			baseIssue.ArtifactDisplayName = &displayName
			if extension != "" {
				baseIssue.Extension = &extension
			}
			baseIssue.MimeType = &mimeType
			// 类型判定必须排在体积判定之前（TS 注释）。
			allowed := false
			if extension != "" {
				for _, candidate := range capabilities.AllowedArtifacts {
					extensionMatch := false
					for _, allowedExtension := range candidate.Extensions {
						if strings.ToLower(strings.TrimPrefix(allowedExtension, ".")) == extension {
							extensionMatch = true
							break
						}
					}
					if !extensionMatch {
						continue
					}
					for _, allowedMime := range candidate.MimeTypes {
						if strings.ToLower(allowedMime) == mimeType {
							allowed = true
							break
						}
					}
					if allowed {
						baseIssue.ArtifactType = &candidate.Type
						break
					}
				}
			}
			if !allowed {
				fallback := extension
				if fallback == "" {
					fallback = "unknown"
				}
				baseIssue.ArtifactType = &fallback
			}
			baseIssue.AllowedArtifacts = supportedArtifactTypes
			if _, hasEntity := current.entityID(); !hasEntity {
				issue := Issue{Code: ReasonInputAttachment, Scope: ScopeArtifact}
				if rowID, ok := current.rowID(); ok {
					value := rowID
					issue.RowID = &value
				}
				issue.ProductTurnID = baseIssue.ProductTurnID
				issue.TurnOrdinal = baseIssue.TurnOrdinal
				displayName := attachmentDisplayName(fileName)
				issue.ArtifactDisplayName = &displayName
				blockingIssues = append(blockingIssues, issue)
				continue
			}
			checks = append(checks, attachmentCheck{
				attachmentIndex: attachmentIndex, attachment: attachment,
				allowed: allowed, baseIssue: baseIssue, row: current,
			})
		}
	}
	for _, check := range checks {
		ref, _ := check.attachment["ref"].(string)
		entityID, _ := check.row.entityID()
		rowID, _ := check.row.rowID()
		stat, statErr := agent.ConversationAttachmentStatV4(ctx, AttachmentStatRequest{
			WorkspaceRef:    input.WorkspaceRef,
			Target:          RowTarget{RowID: rowID, EntityID: entityID},
			AttachmentIndex: check.attachmentIndex, Ref: ref,
		})
		if statErr == nil {
			if !check.allowed {
				// 即使类型不支持，也必须先完成存在性检查（TS 注释）。
				issue := check.baseIssue
				issue.Code = ReasonArtifactTypeNotAllowed
				skippableWarnings = append(skippableWarnings, issue)
			} else if stat.TotalBytes > capabilities.MaxArtifactBytes {
				issue := check.baseIssue
				issue.Code = ReasonArtifactSizeLimit
				actual := stat.TotalBytes
				limit := capabilities.MaxArtifactBytes
				issue.Actual = &actual
				issue.Limit = &limit
				blockingIssues = append(blockingIssues, issue)
			} else if declaredBytes, ok := numberField(check.attachment, "bytes"); ok && declaredBytes > 0 && stat.TotalBytes != int64(declaredBytes) {
				issue := check.baseIssue
				issue.Code = ReasonArtifactChanged
				availability := "changed"
				issue.Availability = &availability
				skippableWarnings = append(skippableWarnings, issue)
			} else {
				hasShareableContent = true
			}
			continue
		}
		if isAttachmentAuthorizationError(statErr) {
			issue := Issue{Code: ReasonArtifactProtocol, Scope: ScopeArtifact}
			if rowID, ok := check.row.rowID(); ok {
				value := rowID
				issue.RowID = &value
			}
			issue.ProductTurnID = check.baseIssue.ProductTurnID
			issue.TurnOrdinal = check.baseIssue.TurnOrdinal
			issue.ArtifactDisplayName = check.baseIssue.ArtifactDisplayName
			blockingIssues = append(blockingIssues, issue)
		} else if isAttachmentTooLargeError(statErr) {
			issue := check.baseIssue
			issue.Code = ReasonArtifactSizeLimit
			limit := capabilities.MaxArtifactBytes
			issue.Limit = &limit
			blockingIssues = append(blockingIssues, issue)
		} else if isDefiniteMissingAttachment(statErr) {
			issue := check.baseIssue
			issue.Code = ReasonInputAttachmentUnavail
			availability := "not_found"
			issue.Availability = &availability
			skippableWarnings = append(skippableWarnings, issue)
		} else {
			hasShareableContent = true
			issue := check.baseIssue
			issue.Code = ReasonArtifactReadFailed
			availability := "unknown"
			issue.Availability = &availability
			deferredIssues = append(deferredIssues, issue)
		}
	}

	if !hasShareableContent {
		blockingIssues = append(blockingIssues, Issue{Code: ReasonNoShareableContent, Scope: ScopeConversation})
	}

	if len(blockingIssues) > 0 {
		return s.finishPreflight(revision, logEpoch, capabilities, supportedArtifactTypes,
			productTurnIDs, ordinals, blockingIssues, skippableWarnings, deferredIssues), nil
	}

	registeredProjection, err := BuildPublicProjection(selectedRows, productTurnIDs, nil)
	if err != nil {
		return PreflightResult{}, err
	}
	manifestIssues := collectArtifactManifestIssues(capabilities, registeredProjection.Artifacts)
	for _, issue := range manifestIssues {
		if issue.Code != ReasonArtifactTypeNotAllowed {
			blockingIssues = append(blockingIssues, issue)
		} else {
			skippableWarnings = append(skippableWarnings, issue)
		}
	}

	// 预览卡片候选：header 完成的轮次 + assistantText 引用的文件。
	headersByProductTurnID := map[string]row{}
	for _, current := range selectedRows {
		if current.kind() != "turnHeader" {
			continue
		}
		productTurnID, ok := current.productTurnID()
		if !ok {
			continue
		}
		state, _ := current["state"].(string)
		if state == "completedSuccess" || state == "completedInterrupted" {
			headersByProductTurnID[productTurnID] = current
		}
	}
	assistantTextByProductTurnID := map[string][]string{}
	var assistantOrder []string
	for _, current := range selectedRows {
		if current.kind() != "assistantText" {
			continue
		}
		productTurnID, ok := current.productTurnID()
		if !ok {
			continue
		}
		if _, seen := assistantTextByProductTurnID[productTurnID]; !seen {
			assistantOrder = append(assistantOrder, productTurnID)
		}
		text, _ := current["text"].(string)
		assistantTextByProductTurnID[productTurnID] = append(assistantTextByProductTurnID[productTurnID], text)
	}
	for _, productTurnID := range assistantOrder {
		header, ok := headersByProductTurnID[productTurnID]
		if !ok {
			continue
		}
		assistantText := strings.Join(assistantTextByProductTurnID[productTurnID], "\n\n")
		// markdown/html 引用需要 fileChanges 授权；判定来自引用本身（见
		// referencesNeedFileChanges 的说明）。
		var changes []fileChange
		if referencesNeedFileChanges(assistantText, input.WorkspacePath) {
			fileChangesValue, fileChangesErr := s.agentFileChanges(ctx, agent, input.WorkspaceRef, header, revision, logEpoch)
			if fileChangesErr != nil {
				return PreflightResult{}, fileChangesErr
			}
			if fileChangesValue != nil {
				changes = fileChangesValue
			}
		}
		candidates := defaultPreviewCandidates(assistantText, productTurnID, input.WorkspacePath, changes)
		var visibleCandidates []previewCandidate
		previewCanonicalPaths := map[string]bool{}
		previewStatSettled := true
		for _, candidate := range candidates {
			stat, statErr := s.artifactSource.Stat(ctx, input.WorkspacePath, candidate.SourceRef)
			issue := previewCandidateIssue(header, ordinals, productTurnID, candidate, supportedArtifactTypes)
			if statErr == nil {
				if previewCanonicalPaths[stat.CanonicalPath] {
					continue
				}
				previewCanonicalPaths[stat.CanonicalPath] = true
			} else {
				if typed, ok := isServiceError(statErr); ok && typed.ReasonCode() == ReasonArtifactReadFailed {
					// Assistant 预览文件已不存在时 UI 也不显示这张卡片，不产生 warning。
					continue
				}
				previewStatSettled = false
				issue.Code = ReasonArtifactReadFailed
				availability := "unknown"
				issue.Availability = &availability
				deferredIssues = append(deferredIssues, issue)
				continue
			}
			if len(visibleCandidates) < conversationPreviewCardVisibleLimit {
				visibleCandidates = append(visibleCandidates, candidate)
			}
		}
		for _, candidate := range visibleCandidates {
			issue := previewCandidateIssue(header, ordinals, productTurnID, candidate, supportedArtifactTypes)
			allowed := candidate.PreviewKind != "video" && candidate.PreviewKind != "audio"
			if allowed {
				extension, _ := fileExtension(candidate.DisplayName)
				allowed = allowedArtifactMatch(capabilities, "", extension, candidate.MimeType)
			}
			if !allowed {
				issue.Code = ReasonArtifactTypeNotAllowed
				issue.AllowedFormats = allowedFormatLabels(capabilities)
				skippableWarnings = append(skippableWarnings, issue)
			}
		}
		snapshotKey := preflightKeyOf(input.WorkspaceRef, productTurnID)
		if previewStatSettled {
			s.putSnapshot(snapshotKey, previewPreflightSnapshot{
				revision: revision, logEpoch: logEpoch,
				capabilitiesFingerprint: capabilitiesFingerprint(capabilities),
				candidateFingerprint:    previewCandidateFingerprint(candidates),
				visibleSourceRefs:       sourceRefsOf(visibleCandidates),
			})
		} else {
			s.dropSnapshots(snapshotKey)
		}
	}

	return s.finishPreflight(revision, logEpoch, capabilities, supportedArtifactTypes,
		productTurnIDs, ordinals, blockingIssues, skippableWarnings, deferredIssues), nil
}

// conversationPreviewCardVisibleLimit ports CONVERSATION_PREVIEW_CARD_VISIBLE_LIMIT.
const conversationPreviewCardVisibleLimit = 10

// loadedRows bundles the loadAllRows triple.
type loadedRows struct {
	rows     []row
	revision int
	logEpoch string
}

func sourceRefsOf(candidates []previewCandidate) []string {
	refs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.SourceRef)
	}
	return refs
}

func previewCandidateIssue(header row, ordinals map[string]int64, productTurnID string, candidate previewCandidate, supported []AllowedArtifact) Issue {
	issue := Issue{Scope: ScopeArtifact}
	if rowID, ok := header.rowID(); ok {
		value := rowID
		issue.RowID = &value
	}
	if ordinal, exists := ordinals[productTurnID]; exists {
		issue.TurnOrdinal = &ordinal
	}
	issue.ProductTurnID = &productTurnID
	issue.ArtifactDisplayName = &candidate.DisplayName
	issue.ArtifactType = &candidate.ArtifactType
	if extension, ok := fileExtension(candidate.DisplayName); ok {
		issue.Extension = &extension
	}
	issue.MimeType = &candidate.MimeType
	issue.AllowedArtifacts = supported
	return issue
}
