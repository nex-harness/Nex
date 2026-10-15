// projectionrows.go holds the per-row-kind projection: projectRow and its
// helpers, the id allocators and the id maps. Split out of projection.go;
// every symbol stays package-private and unchanged.
package conversationshare

import "fmt"

// idAllocator ports allocateIds.
type idAllocator struct {
	mapping map[string]string
	prefix  string
}

func allocateIDs(values []string, prefix string) *idAllocator {
	allocator := &idAllocator{mapping: map[string]string{}, prefix: prefix}
	for _, value := range values {
		if _, exists := allocator.mapping[value]; !exists {
			allocator.mapping[value] = fmt.Sprintf("%s-%d", prefix, len(allocator.mapping)+1)
		}
	}
	return allocator
}

func (a *idAllocator) get(sourceID string) (string, bool) {
	mapped, ok := a.mapping[sourceID]
	return mapped, ok
}

func (a *idAllocator) required(sourceID, label string) (string, error) {
	if mapped, ok := a.mapping[sourceID]; ok {
		return mapped, nil
	}
	return "", projectionError(KindInvalidConversation, fmt.Sprintf("Conversation %s reference is outside the shared projection", label))
}

// idMaps ports PublicIdMaps.
type idMaps struct {
	productTurnIDs *idAllocator
	turnIDs        *idAllocator
	entityIDs      *idAllocator
	toolCallIDs    *idAllocator
	artifactIDs    *idAllocator
	artifactKeys   *idAllocator
}

func buildIDMaps(rows []row, selectedProductTurnIDs []string) *idMaps {
	maps := &idMaps{
		productTurnIDs: allocateIDs(selectedProductTurnIDs, "share-product-turn"),
		turnIDs:        allocateIDs(nil, "share-turn"),
		entityIDs:      allocateIDs(nil, "share-entity"),
		toolCallIDs:    allocateIDs(nil, "share-tool-call"),
		artifactIDs:    allocateIDs(nil, "share-artifact"),
		artifactKeys:   allocateIDs(nil, "share-artifact-key"),
	}
	var turnValues, entityValues, toolValues, artifactValues, keyValues []string
	for _, current := range rows {
		if turnID, ok := current.turnID(); ok {
			turnValues = append(turnValues, turnID)
		}
		if entityID, ok := current.entityID(); ok {
			entityValues = append(entityValues, entityID)
		}
		switch current.kind() {
		case "toolCall":
			if toolCallID, ok := current.stringField("toolCallId"); ok {
				toolValues = append(toolValues, toolCallID)
			}
		case "artifact":
			if artifactVersionID, ok := current.stringField("artifactVersionId"); ok {
				artifactValues = append(artifactValues, artifactVersionID)
			}
			if logicalKey, ok := current.stringField("logicalArtifactKey"); ok {
				keyValues = append(keyValues, logicalKey)
			}
		}
	}
	maps.turnIDs = allocateIDs(turnValues, "share-turn")
	maps.entityIDs = allocateIDs(entityValues, "share-entity")
	maps.toolCallIDs = allocateIDs(toolValues, "share-tool-call")
	maps.artifactIDs = allocateIDs(artifactValues, "share-artifact")
	maps.artifactKeys = allocateIDs(keyValues, "share-artifact-key")
	return maps
}

// projectBase ports projectBase: the shared allow-listed prefix.
func projectBase(current row, index int, ids *idMaps) (map[string]any, error) {
	productTurnID, ok := current.productTurnID()
	if !ok {
		return nil, projectionError(KindInvalidConversation, "Conversation row is missing its product turn identity")
	}
	mappedProductTurn, err := ids.productTurnIDs.required(productTurnID, "product turn")
	if err != nil {
		return nil, err
	}
	base := map[string]any{
		// rowId 必须以 float64 落地：投影闭包校验（assertPublicProjection）用
		// row.rowID() 读回这些行，它与解析后的 JSON 行一样只认 float64。
		"rowId":         float64(index + 1),
		"productTurnId": mappedProductTurn,
		"createdAt":     current["createdAt"],
		"createdAtSeq":  current["createdAtSeq"],
	}
	if turnID, ok := current.turnID(); ok {
		mapped, err := ids.turnIDs.required(turnID, "turn")
		if err != nil {
			return nil, err
		}
		base["turnId"] = mapped
	}
	if entityID, ok := current.entityID(); ok {
		mapped, err := ids.entityIDs.required(entityID, "entity")
		if err != nil {
			return nil, err
		}
		base["entityId"] = mapped
	}
	if visibility, ok := current["visibility"]; ok && visibility != nil {
		base["visibility"] = visibility
	}
	return base, nil
}

// projectTimelineMarker ports projectTimelineMarker: compact keeps only its
// non-reference fields; other retained markers pass through whole.
func projectTimelineMarker(marker map[string]any) (map[string]any, error) {
	markerType, _ := marker["type"].(string)
	switch markerType {
	case "compact":
		out := map[string]any{"type": "compact", "origin": marker["origin"], "status": marker["status"]}
		if value, ok := marker["tokensBefore"]; ok {
			out["tokensBefore"] = value
		}
		if value, ok := marker["tokensAfter"]; ok {
			out["tokensAfter"] = value
		}
		return out, nil
	case "modelChange", "goalSet", "goalVerify", "retryNotice":
		out := make(map[string]any, len(marker))
		for key, value := range marker {
			out[key] = value
		}
		return out, nil
	default:
		return nil, projectionError(KindInvalidConversation, "Timeline references outside the shared projection are not supported")
	}
}

// projectRow ports projectRow: 每类 Row 用 allow-list 新建对象。新增 kind 必须
// 显式加分支（default 报错），不能悄悄放过去。
func projectRow(current row, index int, ids *idMaps) (map[string]any, error) {
	base, err := projectBase(current, index, ids)
	if err != nil {
		return nil, err
	}
	switch current.kind() {
	case "turnHeader":
		base["kind"] = "turnHeader"
		base["origin"] = current["origin"]
		if executionKind, ok := current["executionKind"]; ok && executionKind != nil {
			base["executionKind"] = executionKind
		}
		base["state"] = current["state"]
		base["startedAt"] = current["startedAt"]
		if endedAt, ok := current["endedAt"]; ok && endedAt != nil {
			base["endedAt"] = endedAt
		}
		if activeMs, ok := current["activeMs"]; ok && activeMs != nil {
			base["activeMs"] = activeMs
		}
		if segments, ok := current["workSegments"].([]any); ok {
			projected := make([]any, 0, len(segments))
			for _, entry := range segments {
				segment, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				out := map[string]any{
					"segmentId": segment["segmentId"], "startedAt": segment["startedAt"],
				}
				if trigger, ok := segment["triggerEntityId"].(string); ok && trigger != "" {
					mapped, err := ids.entityIDs.required(trigger, "work segment entity")
					if err != nil {
						return nil, err
					}
					out["triggerEntityId"] = mapped
				}
				if endedAt, ok := segment["endedAt"]; ok && endedAt != nil {
					out["endedAt"] = endedAt
				}
				if activeMs, ok := segment["activeMs"]; ok && activeMs != nil {
					out["activeMs"] = activeMs
				}
				projected = append(projected, out)
			}
			base["workSegments"] = projected
		}
		if fileChanges, ok := current["fileChanges"].(map[string]any); ok {
			out := make(map[string]any, len(fileChanges))
			for key, value := range fileChanges {
				out[key] = value
			}
			base["fileChanges"] = out
		}
		return base, nil
	case "userInput":
		base["kind"] = "userInput"
		base["text"] = current["text"]
		base["origin"] = current["origin"]
		if originMeta, ok := current["originMeta"].(map[string]any); ok {
			projected := map[string]any{}
			if value, ok := originMeta["backgroundSource"]; ok && value != nil {
				projected["backgroundSource"] = value
			}
			if value, ok := originMeta["senderLabel"]; ok && value != nil {
				projected["senderLabel"] = value
			}
			if len(projected) > 0 {
				base["originMeta"] = projected
			}
		}
		if guided, ok := current["guided"]; ok && guided != nil {
			base["guided"] = guided
		}
		if attachments, ok := current["attachments"].([]any); ok {
			projected := make([]any, 0, len(attachments))
			for _, entry := range attachments {
				attachment, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				ref, _ := attachment["ref"].(string)
				if !publicArtifactRefRe.MatchString(ref) {
					continue
				}
				if previewRef, ok := attachment["previewRef"].(string); ok && previewRef != "" &&
					!publicArtifactRefRe.MatchString(previewRef) {
					continue
				}
				out := map[string]any{
					"ref": ref, "fileName": attachment["fileName"],
					"mime": attachment["mime"], "bytes": attachment["bytes"],
				}
				if previewRef, ok := attachment["previewRef"].(string); ok && previewRef != "" {
					out["previewRef"] = previewRef
				}
				projected = append(projected, out)
			}
			if len(projected) > 0 {
				base["attachments"] = projected
			}
		}
		return base, nil
	case "assistantText":
		base["kind"] = "assistantText"
		base["text"] = current["text"]
		base["state"] = current["state"]
		if model, ok := current["model"]; ok && model != nil {
			base["model"] = model
		}
		return base, nil
	case "reasoning":
		base["kind"] = "reasoning"
		base["text"] = current["text"]
		base["state"] = current["state"]
		if durationMs, ok := current["durationMs"]; ok && durationMs != nil {
			base["durationMs"] = durationMs
		}
		return base, nil
	case "toolCall":
		toolCallID, _ := current.stringField("toolCallId")
		mapped, err := ids.toolCallIDs.required(toolCallID, "tool call")
		if err != nil {
			return nil, err
		}
		base["kind"] = "toolCall"
		base["toolCallId"] = mapped
		base["toolName"] = current["toolName"]
		base["status"] = current["status"]
		base["inputText"] = current["inputText"]
		if input, ok := current["input"]; ok && input != nil {
			base["input"] = input
		}
		if output, ok := current["output"].(map[string]any); ok {
			base["output"] = map[string]any{"text": output["text"]}
		}
		if display, ok := current["display"].(map[string]any); ok {
			out := make(map[string]any, len(display))
			for key, value := range display {
				out[key] = value
			}
			base["display"] = out
		}
		if toolError, ok := current["error"].(map[string]any); ok {
			out := make(map[string]any, len(toolError))
			for key, value := range toolError {
				out[key] = value
			}
			base["error"] = out
		}
		if backgrounded, ok := current["backgrounded"]; ok && backgrounded != nil {
			base["backgrounded"] = backgrounded
		}
		if startedAt, ok := current["startedAt"]; ok && startedAt != nil {
			base["startedAt"] = startedAt
		}
		if endedAt, ok := current["endedAt"]; ok && endedAt != nil {
			base["endedAt"] = endedAt
		}
		return base, nil
	case "artifact":
		artifactVersionID, _ := current.stringField("artifactVersionId")
		mappedVersion, err := ids.artifactIDs.required(artifactVersionID, "artifact")
		if err != nil {
			return nil, err
		}
		logicalKey, _ := current.stringField("logicalArtifactKey")
		mappedKey, err := ids.artifactKeys.required(logicalKey, "artifact key")
		if err != nil {
			return nil, err
		}
		base["kind"] = "artifact"
		base["artifactVersionId"] = mappedVersion
		base["logicalArtifactKey"] = mappedKey
		base["displayName"] = current["displayName"]
		base["artifactType"] = current["artifactType"]
		base["mimeType"] = current["mimeType"]
		base["sizeBytes"] = current["sizeBytes"]
		base["sha256"] = current["sha256"]
		base["ref"] = "nex-artifact://share/" + mappedVersion
		base["state"] = current["state"]
		return base, nil
	case "timelineMarker":
		marker, ok := current["marker"].(map[string]any)
		if !ok {
			return nil, projectionError(KindInvalidConversation, "Conversation row is missing its timeline marker")
		}
		projected, err := projectTimelineMarker(marker)
		if err != nil {
			return nil, err
		}
		base["kind"] = "timelineMarker"
		if lane, ok := current["lane"]; ok && lane != nil {
			base["lane"] = lane
		}
		base["marker"] = projected
		return base, nil
	case "subagent":
		return nil, projectionError(KindInvalidConversation, "Subagent detail rows must be filtered before public projection")
	case "hookInvocation":
		return nil, projectionError(KindInvalidConversation, "Hook invocation rows are not part of the V1 public projection")
	default:
		return nil, projectionError(KindInvalidConversation, "Conversation row kind is not part of the V1 public projection")
	}
}
