// projectionassert.go holds the closure re-validation of the built public
// projection (assertConversationSharePublicProjection) and its uniqueness
// helper. Split out of projection.go; symbols are unchanged.
package conversationshare

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// assertUniqueValues ports assertUnique.
func assertUniqueValues(values []string, label string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return projectionError(KindInvalidConversation, fmt.Sprintf("Conversation public %s identities must be unique", label))
		}
		seen[value] = true
	}
	return nil
}

// assertPublicProjection ports assertConversationSharePublicProjection: the
// closure re-validation of the built projection.
func assertPublicProjection(rows []row, selectedProductTurnIDs []string) error {
	if len(rows) == 0 || len(selectedProductTurnIDs) == 0 {
		return projectionError(KindInvalidConversation, "Conversation public projection cannot be empty")
	}
	if err := assertTerminalAndSafe(rows); err != nil {
		return err
	}
	if err := assertUniqueValues(selectedProductTurnIDs, "product turn"); err != nil {
		return err
	}
	for _, productTurnID := range selectedProductTurnIDs {
		if !publicProductTurnRe.MatchString(productTurnID) {
			return projectionError(KindInvalidConversation, "Conversation public product turn identity is invalid")
		}
	}
	selected := map[string]bool{}
	for _, productTurnID := range selectedProductTurnIDs {
		selected[productTurnID] = true
	}
	headerCounts := map[string]int64{}
	entityIDs := map[string]bool{}
	var toolCallIDs, artifactIDs []string
	turnIDs := map[string]bool{}
	for index, current := range rows {
		if rowID, ok := current.rowID(); !ok || rowID != int64(index+1) {
			return projectionError(KindInvalidConversation, "Conversation public row identities must be contiguous")
		}
		turnID, _ := current.turnID()
		if !publicTurnRe.MatchString(turnID) {
			return projectionError(KindInvalidConversation, "Conversation public turn identity is invalid")
		}
		turnIDs[turnID] = true
		productTurnID, hasProductTurn := current.productTurnID()
		if !hasProductTurn || !publicProductTurnRe.MatchString(productTurnID) || !selected[productTurnID] {
			return projectionError(KindInvalidConversation, "Conversation row belongs to an unselected product turn")
		}
		if entityID, ok := current.entityID(); ok {
			if !publicEntityRe.MatchString(entityID) {
				return projectionError(KindInvalidConversation, "Conversation public entity identity is invalid")
			}
			entityIDs[entityID] = true
		}
		if _, hasActions := current["actions"]; hasActions {
			return projectionError(KindInvalidConversation, "Conversation public rows cannot contain local actions")
		}
		switch current.kind() {
		case "turnHeader":
			headerCounts[productTurnID]++
			if _, hasOriginMeta := current["originMeta"]; hasOriginMeta {
				return projectionError(KindInvalidConversation, "Conversation public turn headers cannot contain local work identity")
			}
		case "userInput":
			for _, key := range []string{"sourceCommandId", "rootSourceCommandId", "clientId"} {
				if _, present := current[key]; present {
					return projectionError(KindInvalidConversation, "Conversation public input contains local write identity")
				}
			}
			if originMeta, ok := current["originMeta"].(map[string]any); ok {
				if _, hasWorkID := originMeta["workId"]; hasWorkID {
					return projectionError(KindInvalidConversation, "Conversation public input contains local write identity")
				}
				if _, hasSender := originMeta["senderSessionId"]; hasSender {
					return projectionError(KindInvalidConversation, "Conversation public input contains local write identity")
				}
			}
			if attachments, ok := current["attachments"].([]any); ok {
				for _, entry := range attachments {
					attachment, ok := entry.(map[string]any)
					if !ok {
						continue
					}
					ref, _ := attachment["ref"].(string)
					if !publicArtifactRefRe.MatchString(ref) {
						return projectionError(KindInvalidConversation, "Conversation public input attachment reference is invalid")
					}
					if previewRef, ok := attachment["previewRef"].(string); ok && previewRef != "" &&
						!publicArtifactRefRe.MatchString(previewRef) {
						return projectionError(KindInvalidConversation, "Conversation public input preview reference is invalid")
					}
				}
			}
		case "assistantText":
			if _, hasFeedback := current["feedback"]; hasFeedback {
				return projectionError(KindInvalidConversation, "Conversation public assistant text contains local feedback")
			}
		case "toolCall":
			toolCallID, _ := current.stringField("toolCallId")
			if !publicToolCallRe.MatchString(toolCallID) {
				return projectionError(KindInvalidConversation, "Conversation public tool call identity is invalid")
			}
			toolCallIDs = append(toolCallIDs, toolCallID)
			for _, key := range []string{"progress", "approvalInteractionId", "workId"} {
				if _, present := current[key]; present {
					return projectionError(KindInvalidConversation, "Conversation public tool call contains local runtime identity")
				}
			}
		case "artifact":
			artifactVersionID, _ := current.stringField("artifactVersionId")
			ref, _ := current["ref"].(string)
			if !publicArtifactRe.MatchString(artifactVersionID) || ref != "nex-artifact://share/"+artifactVersionID {
				return projectionError(KindInvalidConversation, "Conversation public artifact identity is invalid")
			}
			artifactIDs = append(artifactIDs, artifactVersionID)
		case "timelineMarker":
			if _, hasSourceCommand := current["sourceCommandId"]; hasSourceCommand {
				return projectionError(KindInvalidConversation, "Conversation public timeline contains local command identity")
			}
		case "subagent":
			return projectionError(KindInvalidConversation, "Conversation public projection cannot contain subagent details")
		}
	}
	if err := assertUniqueValues(toolCallIDs, "tool call"); err != nil {
		return err
	}
	if err := assertUniqueValues(artifactIDs, "artifact"); err != nil {
		return err
	}
	if len(turnIDs) == 0 {
		return projectionError(KindInvalidConversation, "Conversation public projection has no turns")
	}
	for _, productTurnID := range selectedProductTurnIDs {
		if headerCounts[productTurnID] != 1 {
			return projectionError(KindInvalidConversation, "Conversation public product turns require exactly one header")
		}
	}
	for _, current := range rows {
		if current.kind() != "turnHeader" {
			continue
		}
		if segments, ok := current["workSegments"].([]any); ok {
			for _, entry := range segments {
				segment, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				if trigger, ok := segment["triggerEntityId"].(string); ok && trigger != "" && !entityIDs[trigger] {
					return projectionError(KindInvalidConversation, "Conversation work segment entity reference is unresolved")
				}
			}
		}
	}
	return nil
}

// BuildPublicProjection ports buildConversationSharePublicProjection.
func BuildPublicProjection(rows []row, selectedProductTurnIDs []string, additionalArtifacts []PublicArtifact) (*Projection, error) {
	if err := assertTerminalAndSafe(rows); err != nil {
		return nil, err
	}
	if err := assertUniqueValues(selectedProductTurnIDs, "source product turn"); err != nil {
		return nil, err
	}
	if len(selectedProductTurnIDs) == 0 {
		return nil, projectionError(KindInvalidConversation, "Conversation product turn selection cannot be empty")
	}
	selected := map[string]bool{}
	for _, productTurnID := range selectedProductTurnIDs {
		selected[productTurnID] = true
	}
	for _, current := range rows {
		productTurnID, ok := current.productTurnID()
		if !ok || !selected[productTurnID] {
			return nil, projectionError(KindInvalidConversation, "Conversation rows fall outside the selected product turns")
		}
	}
	// 本地运行投影包含 subagent 详情和写入态标识：先过滤非 V1 Row。
	retained := make([]row, 0, len(rows))
	for _, current := range rows {
		if current.kind() == "subagent" || current.kind() == "hookInvocation" {
			continue
		}
		retained = append(retained, current)
	}
	var sourceArtifactRows []row
	for _, current := range retained {
		if current.kind() == "artifact" {
			sourceArtifactRows = append(sourceArtifactRows, current)
		}
	}
	if err := assertUniqueValues(stringFields(sourceArtifactRows, "artifactVersionId"), "source artifact"); err != nil {
		return nil, err
	}
	if err := assertUniqueValues(stringFields(sourceArtifactRows, "logicalArtifactKey"), "source artifact logical key"); err != nil {
		return nil, err
	}
	ids := buildIDMaps(retained, selectedProductTurnIDs)
	projectedRows := make([]row, 0, len(retained))
	for index, current := range retained {
		projected, err := projectRow(current, index, ids)
		if err != nil {
			return nil, err
		}
		projectedRows = append(projectedRows, projected)
	}
	var artifacts []PublicArtifact
	for _, sourceRow := range sourceArtifactRows {
		sourceVersionID, _ := sourceRow.stringField("artifactVersionId")
		mappedVersion, _ := ids.artifactIDs.get(sourceVersionID)
		var projectedArtifact row
		for _, candidate := range projectedRows {
			if candidate.kind() != "artifact" {
				continue
			}
			if versionID, _ := candidate.stringField("artifactVersionId"); versionID == mappedVersion {
				projectedArtifact = candidate
				break
			}
		}
		if projectedArtifact == nil {
			return nil, projectionError(KindInvalidConversation, "Projected artifact row is missing")
		}
		productTurnID, _ := projectedArtifact.productTurnID()
		displayName, _ := projectedArtifact["displayName"].(string)
		extension, err := extensionOfStrict(displayName)
		if err != nil {
			return nil, err
		}
		state, _ := projectedArtifact["state"].(string)
		logicalKey, _ := projectedArtifact["logicalArtifactKey"].(string)
		ref, _ := projectedArtifact["ref"].(string)
		artifactType, _ := projectedArtifact["artifactType"].(string)
		mimeType, _ := projectedArtifact["mimeType"].(string)
		sizeBytes, _ := numberField(projectedArtifact, "sizeBytes")
		sha256Value, _ := projectedArtifact["sha256"].(string)
		artifacts = append(artifacts, PublicArtifact{
			SourceRef: sourceRefOf(sourceRow),
			Descriptor: ArtifactDescriptor{
				ArtifactID: mappedVersion, LogicalArtifactKey: logicalKey,
				ProducerProductTurn: productTurnID, ArtifactVersion: 1, State: state,
				Ref: ref, ArtifactType: artifactType, DisplayName: displayName,
				Extension: extension, MimeType: mimeType,
				SizeBytes: int64(sizeBytes), Sha256: sha256Value,
			},
		})
	}
	for index, artifact := range additionalArtifacts {
		mapped, err := ids.productTurnIDs.required(artifact.Descriptor.ProducerProductTurn, "input attachment product turn")
		if err != nil {
			return nil, err
		}
		artifact.Descriptor.ProducerProductTurn = mapped
		additionalArtifacts[index] = artifact
	}
	var allArtifactIDs []string
	for _, artifact := range artifacts {
		allArtifactIDs = append(allArtifactIDs, artifact.Descriptor.ArtifactID)
	}
	for _, artifact := range additionalArtifacts {
		allArtifactIDs = append(allArtifactIDs, artifact.Descriptor.ArtifactID)
	}
	if err := assertUniqueValues(allArtifactIDs, "artifact manifest"); err != nil {
		return nil, err
	}
	mappedSelected := make([]string, 0, len(selectedProductTurnIDs))
	for _, productTurnID := range selectedProductTurnIDs {
		mapped, err := ids.productTurnIDs.required(productTurnID, "product turn")
		if err != nil {
			return nil, err
		}
		mappedSelected = append(mappedSelected, mapped)
	}
	artifacts = append(artifacts, additionalArtifacts...)
	if err := assertPublicProjection(projectedRows, mappedSelected); err != nil {
		return nil, err
	}
	encodedRows := make([]json.RawMessage, 0, len(projectedRows))
	for _, current := range projectedRows {
		encoded, err := json.Marshal(current)
		if err != nil {
			return nil, err
		}
		encodedRows = append(encodedRows, encoded)
	}
	return &Projection{
		Rows: encodedRows, SelectedProductTurnIDs: mappedSelected, Artifacts: artifacts,
	}, nil
}

func stringFields(rows []row, key string) []string {
	values := make([]string, 0, len(rows))
	for _, current := range rows {
		if value, ok := current.stringField(key); ok {
			values = append(values, value)
		}
	}
	return values
}

func numberField(current row, key string) (float64, bool) {
	number, ok := current[key].(float64)
	return number, ok
}

func sourceRefOf(sourceRow row) string {
	ref, _ := sourceRow["ref"].(string)
	return ref
}

// extensionOfStrict ports extensionOf (projection variant): missing extension
// is a projection error.
func extensionOfStrict(displayName string) (string, error) {
	fileName := strings.TrimSpace(displayName)
	dot := strings.LastIndex(fileName, ".")
	if dot <= 0 || dot == len(fileName)-1 {
		return "", projectionError(KindInvalidConversation, "Conversation artifact extension is missing")
	}
	return strings.ToLower(fileName[dot+1:]), nil
}

// sortStrings is a tiny helper used by the channel for deterministic output.
func sortStrings(values []string) {
	sort.Strings(values)
}
