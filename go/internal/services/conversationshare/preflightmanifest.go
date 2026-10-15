// preflightmanifest.go holds the preflight tail: the fileChanges RPC wrap,
// the shareable-content reduction, the manifest issues and the candidate
// fingerprint. Split out of preflight.go; symbols are unchanged.
package conversationshare

import (
	"context"
	"strings"
)

// agentFileChanges wraps the fileChanges V4 call with the stale-watermark
// translation.
func (s *Service) agentFileChanges(ctx context.Context, agent AgentService, ref WorkspaceRef, header row, revision int, logEpoch string) ([]fileChange, error) {
	entityID, hasEntity := header.entityID()
	rowID, _ := header.rowID()
	if !hasEntity {
		return nil, nil
	}
	result, err := agent.ConversationFileChangesV4(ctx, FileChangesRequest{
		WorkspaceRef: ref, Target: RowTarget{RowID: rowID, EntityID: entityID},
		BaseRevision: revision, BaseLogEpoch: logEpoch,
	})
	if err != nil {
		if strings.Contains(err.Error(), "proto.staleRevision") || strings.Contains(err.Error(), "proto.staleLogEpoch") {
			return nil, newServiceError(KindInvalidConversation, "Conversation changed while preparing share", errorDetails{})
		}
		return nil, err
	}
	changes := make([]fileChange, 0, len(result.Items))
	state := "active"
	if result.State == "reverted" {
		state = "reverted"
	}
	for _, item := range result.Items {
		changes = append(changes, fileChange{Path: item.Path, State: state})
	}
	return changes, nil
}

func (s *Service) finishPreflight(revision int, logEpoch string, capabilities Capabilities, supported []AllowedArtifact, productTurnIDs []string, ordinals map[string]int64, blocking, warnings, deferred []Issue) PreflightResult {
	sanitizedBlocking, _, _ := SanitizedIssues(blocking)
	sanitizedWarnings, _, _ := SanitizedIssues(warnings)
	sanitizedDeferred, _, _ := SanitizedIssues(deferred)
	return PreflightResult{
		Revision: revision, LogEpoch: logEpoch,
		CapabilitiesFingerprint: capabilitiesFingerprint(capabilities),
		BlockingIssues:          sanitizedBlocking,
		SkippableWarnings:       sanitizedWarnings,
		DeferredIssues:          sanitizedDeferred,
		SupportedArtifactTypes:  supported,
		TurnResults: buildTurnPreflightResults(productTurnIDs, ordinals,
			sanitizedBlocking, sanitizedWarnings, sanitizedDeferred),
	}
}

// anyShareableContent ports the hasShareableContent reduction.
func anyShareableContent(rows []row) bool {
	for _, current := range rows {
		switch current.kind() {
		case "turnHeader", "timelineMarker":
			continue
		case "assistantText", "reasoning", "userInput":
			if text, ok := current["text"].(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		case "toolCall":
			if text, ok := current["inputText"].(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
			if output, ok := current["output"].(map[string]any); ok {
				if text, ok := output["text"].(string); ok && strings.TrimSpace(text) != "" {
					return true
				}
			}
			if toolError, ok := current["error"].(map[string]any); ok {
				if text, ok := toolError["message"].(string); ok && strings.TrimSpace(text) != "" {
					return true
				}
			}
		default:
			return true
		}
	}
	return false
}

// collectArtifactManifestIssues ports collectArtifactManifestIssues.
func collectArtifactManifestIssues(capabilities Capabilities, artifacts []PublicArtifact) []Issue {
	var issues []Issue
	if int64(len(artifacts)) > int64(capabilities.MaxArtifactCount) {
		actual := int64(len(artifacts))
		limit := int64(capabilities.MaxArtifactCount)
		issues = append(issues, Issue{Code: "artifact_count_limit", Scope: ScopeConversation, Actual: &actual, Limit: &limit})
	}
	artifactIDs := map[string]bool{}
	var declaredTotalBytes int64
	for _, artifact := range artifacts {
		descriptor := artifact.Descriptor
		if artifactIDs[descriptor.ArtifactID] {
			issues = append(issues, Issue{Code: ReasonArtifactManifest, Scope: ScopeArtifact, ArtifactDisplayName: &descriptor.DisplayName})
			continue
		}
		artifactIDs[descriptor.ArtifactID] = true
		declaredTotalBytes += descriptor.SizeBytes
		if descriptor.SizeBytes > capabilities.MaxArtifactBytes {
			actual := descriptor.SizeBytes
			limit := capabilities.MaxArtifactBytes
			issue := Issue{Code: ReasonArtifactSizeLimit, Scope: ScopeArtifact,
				ArtifactDisplayName: &descriptor.DisplayName, ArtifactType: &descriptor.ArtifactType,
				Extension: &descriptor.Extension, MimeType: &descriptor.MimeType,
				Actual: &actual, Limit: &limit}
			issues = append(issues, issue)
		}
		if !allowedArtifactMatch(capabilities, descriptor.ArtifactType, descriptor.Extension, strings.ToLower(descriptor.MimeType)) {
			issue := Issue{Code: ReasonArtifactTypeNotAllowed, Scope: ScopeArtifact,
				ArtifactDisplayName: &descriptor.DisplayName, ArtifactType: &descriptor.ArtifactType,
				Extension: &descriptor.Extension, MimeType: &descriptor.MimeType,
				AllowedFormats:   allowedFormatLabels(capabilities),
				AllowedArtifacts: allowedArtifactSummaries(capabilities)}
			issues = append(issues, issue)
		}
	}
	if declaredTotalBytes > capabilities.MaxTotalArtifactBytes {
		actual := declaredTotalBytes
		limit := capabilities.MaxTotalArtifactBytes
		issues = append(issues, Issue{Code: "artifact_total_size_limit", Scope: ScopeConversation, Actual: &actual, Limit: &limit})
	}
	return issues
}

// validateArtifactManifest ports validateArtifactManifest.
func validateArtifactManifest(capabilities Capabilities, artifacts []PublicArtifact) error {
	issues := collectArtifactManifestIssues(capabilities, artifacts)
	if len(issues) == 0 {
		return nil
	}
	hasTypeIssue := false
	for _, issue := range issues {
		if issue.Code == ReasonArtifactTypeNotAllowed {
			hasTypeIssue = true
			break
		}
	}
	kind := KindLimitExceeded
	if hasTypeIssue {
		kind = KindArtifactNotAllowed
	}
	return newServiceError(kind, "Conversation artifacts cannot be shared", errorDetails{Issues: issues})
}

// previewCandidateFingerprint ports getConversationSharePreviewCandidateFingerprint.
func previewCandidateFingerprint(candidates []previewCandidate) string {
	var builder strings.Builder
	builder.WriteString("[")
	for index, candidate := range candidates {
		if index > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`{"sourceRef":`)
		canonicalJSONString(&builder, candidate.SourceRef)
		builder.WriteString(`,"displayName":`)
		canonicalJSONString(&builder, candidate.DisplayName)
		builder.WriteString(`,"previewKind":`)
		canonicalJSONString(&builder, candidate.PreviewKind)
		builder.WriteString(`,"artifactType":`)
		canonicalJSONString(&builder, candidate.ArtifactType)
		builder.WriteString(`,"mimeType":`)
		canonicalJSONString(&builder, candidate.MimeType)
		builder.WriteString(`,"productTurnId":`)
		canonicalJSONString(&builder, candidate.ProductTurnID)
		builder.WriteString(`,"requiresFileChanges":`)
		if candidate.RequiresFileChanges {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
		builder.WriteString("}")
	}
	builder.WriteString("]")
	return builder.String()
}
