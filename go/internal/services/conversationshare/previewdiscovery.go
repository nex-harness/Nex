// previewdiscovery.go holds the preview-card discovery pass of the publish
// flow: the assistant-text references, the visibility cap, the type gating and
// the warning issues. Split out of discovery.go so the input-attachment staging
// stays readable; symbols are unchanged.
package conversationshare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// discoverPreviewArtifacts ports discoverPreviewArtifacts (publish side).
// discoverPreviewArtifacts ports discoverPreviewArtifacts. The bytes travel with
// the result (as in TS, where each DiscoveredShareArtifact carries them); they
// are never stashed on the service, so two concurrent publishes cannot see each
// other's bytes. existingCanonicalPaths carries the registered artifacts'
// canonical paths in, so one physical file is never published twice.
func (s *Service) discoverPreviewArtifacts(ctx context.Context, agent AgentService, input PublishInput, capabilities Capabilities, selectedRows []row, loaded loadedRows, existingCanonicalPaths map[string]bool) (discovered []row, bytesBySourceRef map[string][]byte, warnings []Issue) {
	bytesBySourceRef = map[string][]byte{}
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
	ordinals := turnOrdinalByProductTurn(loaded.rows)
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
	var maxSourceRowID int64
	for _, current := range selectedRows {
		if rowID, ok := current.rowID(); ok && rowID > maxSourceRowID {
			maxSourceRowID = rowID
		}
	}
	for _, productTurnID := range assistantOrder {
		header, ok := headersByProductTurnID[productTurnID]
		if !ok {
			continue
		}
		assistantText := strings.Join(assistantTextByProductTurnID[productTurnID], "\n\n")
		// needsFileChanges 必须由「引用」判定（TS: references.some(kind is
		// markdown|html)），不能由已过滤的候选反推：markdown/html 候选在拿到
		// fileChanges 之前就会被 defaultPreviewCandidates 丢掉，用候选反推会
		// 让 fileChanges 永远不被取，markdown 引用永远无法发布。
		needsFileChanges := referencesNeedFileChanges(assistantText, input.WorkspacePath)
		var candidates []previewCandidate
		var changes []fileChange
		if needsFileChanges {
			fileChanges, err := s.agentFileChanges(ctx, agent, input.WorkspaceRef, header, loaded.revision, loaded.logEpoch)
			if err != nil {
				return nil, nil, warnings
			}
			changes = fileChanges
		}
		candidates = defaultPreviewCandidates(assistantText, productTurnID, input.WorkspacePath, changes)
		// 预检快照匹配时只发布选择阶段可见的候选。
		if snapshot, ok := s.takeSnapshot(preflightKeyOf(input.WorkspaceRef, productTurnID)); ok &&
			snapshot.revision == loaded.revision && snapshot.logEpoch == loaded.logEpoch &&
			snapshot.capabilitiesFingerprint == capabilitiesFingerprint(capabilities) &&
			snapshot.candidateFingerprint == previewCandidateFingerprint(candidates) {
			visible := map[string]bool{}
			for _, ref := range snapshot.visibleSourceRefs {
				visible[ref] = true
			}
			// 不能就地过滤（candidates[:0] 会与 range 的读取位置重叠）。
			filtered := make([]previewCandidate, 0, len(candidates))
			for _, candidate := range candidates {
				if visible[candidate.SourceRef] {
					filtered = append(filtered, candidate)
				}
			}
			candidates = filtered
		}
		var visibleCandidates []previewCandidate
		previewCanonicalPaths := map[string]bool{}
		for _, candidate := range candidates {
			stat, statErr := s.artifactSource.Stat(ctx, input.WorkspacePath, candidate.SourceRef)
			if statErr == nil {
				if previewCanonicalPaths[stat.CanonicalPath] {
					continue
				}
				previewCanonicalPaths[stat.CanonicalPath] = true
			} else if typed, isShareErr := isServiceError(statErr); isShareErr && typed.ReasonCode() == ReasonArtifactReadFailed {
				availability := "unknown"
				if errno, hasErrno := typed.Errno(); hasErrno && errno == "ENOENT" {
					availability = "not_found"
				}
				warnings = append(warnings, previewReadWarning(header, ordinals, candidate, availability))
				continue
			} else {
				return nil, nil, warnings
			}
			if len(visibleCandidates) < conversationPreviewCardVisibleLimit {
				visibleCandidates = append(visibleCandidates, candidate)
			}
		}
		for _, candidate := range visibleCandidates {
			if candidate.PreviewKind == "video" || candidate.PreviewKind == "audio" {
				warnings = append(warnings, previewTypeWarning(header, ordinals, candidate, capabilities))
				continue
			}
			extension, _ := fileExtension(candidate.DisplayName)
			if !allowedArtifactMatch(capabilities, "", extension, candidate.MimeType) {
				warnings = append(warnings, previewTypeWarning(header, ordinals, candidate, capabilities))
				continue
			}
			materialized, readErr := s.artifactSource.Read(ctx, input.WorkspacePath, candidate.SourceRef, capabilities.MaxArtifactBytes)
			if readErr != nil {
				if typed, isShareErr := isServiceError(readErr); isShareErr && typed.ReasonCode() == ReasonArtifactReadFailed {
					availability := "unknown"
					if errno, hasErrno := typed.Errno(); hasErrno && errno == "ENOENT" {
						availability = "not_found"
					}
					warnings = append(warnings, previewReadWarning(header, ordinals, candidate, availability))
					continue
				}
				// limit_exceeded / unsafe_structure 仍然阻断。
				return nil, nil, warnings
			}
			// 与 TS 的 existingCanonicalPaths 同义：已登记结果物的物理文件不被二次发布。
			if existingCanonicalPaths[materialized.CanonicalPath] {
				continue
			}
			existingCanonicalPaths[materialized.CanonicalPath] = true
			localKey := "preview:" + productTurnID + ":" + candidate.SourceRef
			digest := sha256.Sum256(materialized.Bytes)
			allowedType := ""
			for _, allowed := range capabilities.AllowedArtifacts {
				if allowedArtifactMatch(capabilities, allowed.Type, extension, candidate.MimeType) {
					allowedType = allowed.Type
					break
				}
			}
			discovered = append(discovered, row{
				"rowId":  float64(maxSourceRowID + int64(len(discovered)) + 1),
				"turnId": header["turnId"], "productTurnId": header["productTurnId"],
				"createdAt": headerCreatedAt(header), "createdAtSeq": header["createdAtSeq"],
				"kind": "artifact", "artifactVersionId": localKey, "logicalArtifactKey": localKey,
				"displayName": candidate.DisplayName, "artifactType": allowedType,
				"mimeType": candidate.MimeType, "sizeBytes": float64(len(materialized.Bytes)),
				"sha256": hex.EncodeToString(digest[:]), "ref": candidate.SourceRef, "state": "current",
			})
			bytesBySourceRef[candidate.SourceRef] = materialized.Bytes
		}
	}
	return discovered, bytesBySourceRef, warnings
}

func headerCreatedAt(header row) any {
	if endedAt, ok := header["endedAt"]; ok && endedAt != nil {
		return endedAt
	}
	return header["createdAt"]
}

func previewReadWarning(header row, ordinals map[string]int64, candidate previewCandidate, availability string) Issue {
	issue := previewCandidateIssue(header, ordinals, candidate.ProductTurnID, candidate, nil)
	issue.Code = ReasonArtifactReadFailed
	issue.Availability = &availability
	return issue
}

func previewTypeWarning(header row, ordinals map[string]int64, candidate previewCandidate, capabilities Capabilities) Issue {
	issue := previewCandidateIssue(header, ordinals, candidate.ProductTurnID, candidate, nil)
	issue.Code = ReasonArtifactTypeNotAllowed
	issue.AllowedFormats = allowedFormatLabels(capabilities)
	issue.AllowedArtifacts = allowedArtifactSummaries(capabilities)
	return issue
}

// insertDiscoveredArtifacts ports insertDiscoveredArtifacts.
func insertDiscoveredArtifacts(selectedRows []row, artifacts []row) []row {
	if len(artifacts) == 0 {
		return selectedRows
	}
	artifactsByProductTurnID := map[string][]row{}
	for _, artifact := range artifacts {
		productTurnID, _ := artifact.productTurnID()
		artifactsByProductTurnID[productTurnID] = append(artifactsByProductTurnID[productTurnID], artifact)
	}
	lastRowIndexByProductTurnID := map[string]int{}
	for index, current := range selectedRows {
		if productTurnID, ok := current.productTurnID(); ok {
			lastRowIndexByProductTurnID[productTurnID] = index
		}
	}
	out := make([]row, 0, len(selectedRows)+len(artifacts))
	for index, current := range selectedRows {
		out = append(out, current)
		productTurnID, ok := current.productTurnID()
		if !ok || lastRowIndexByProductTurnID[productTurnID] != index {
			continue
		}
		out = append(out, artifactsByProductTurnID[productTurnID]...)
	}
	return out
}

func descriptorsOf(artifacts []PublicArtifact) []ArtifactDescriptor {
	descriptors := make([]ArtifactDescriptor, 0, len(artifacts))
	for _, artifact := range artifacts {
		descriptors = append(descriptors, artifact.Descriptor)
	}
	return descriptors
}

func marshalJSONBytes(value any) ([]byte, error) {
	return jsonMarshal(value)
}

func treeOfRaw(request *ConfirmRequest) any {
	// The payload digest in the TS is computed on the serialized confirm
	// request; Go marshals the same tree via the json seam.
	var tree any
	if err := jsonUnmarshalValue(mustMarshal(request), &tree); err != nil {
		return nil
	}
	return tree
}
