// discovery.go holds the artifact-discovery half of the publish flow: the
// snapshot assembly, the input-attachment staging and the preview-card
// discovery. Split out of publish.go; every symbol stays package-private.
package conversationshare

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// buildArtifactSnapshot ports buildConversationShareArtifactSnapshot.
func (s *Service) buildArtifactSnapshot(ctx context.Context, agent AgentService, input PublishInput, capabilities Capabilities, selectedRows []row, registeredProjection *Projection, loaded loadedRows) (*artifactSnapshot, error) {
	var issues, warnings []Issue
	inputRows, inputArtifacts, inputBytes, inputWarnings := s.discoverInputAttachments(ctx, agent, input, capabilities, selectedRows)
	warnings = append(warnings, inputWarnings...)
	// registered artifacts: 复验当前字节（Row 在工具完成时记录，用户可能已改文件）。
	bytesBySourceRef := map[string][]byte{}
	for _, artifact := range registeredProjection.Artifacts {
		materialized, err := s.artifactSource.Read(ctx, input.WorkspacePath, artifact.SourceRef, capabilities.MaxArtifactBytes)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(materialized.Bytes)
		if int64(len(materialized.Bytes)) != artifact.Descriptor.SizeBytes ||
			hex.EncodeToString(digest[:]) != artifact.Descriptor.Sha256 {
			return nil, newServiceError(KindInvalidConversation, "Conversation artifact changed after it was registered", errorDetails{})
		}
		bytesBySourceRef[artifact.SourceRef] = materialized.Bytes
	}
	// existingCanonicalPaths 用已登记结果物的 realpath 播种：同一个物理文件被
	// assistant 正文再次引用时不再二次发布（TS discovery 的 Set 语义）。
	existingCanonicalPaths := map[string]bool{}
	for _, artifact := range registeredProjection.Artifacts {
		canonical, statErr := s.artifactSource.Stat(ctx, input.WorkspacePath, artifact.SourceRef)
		if statErr == nil {
			existingCanonicalPaths[canonical.CanonicalPath] = true
		}
	}
	discovered, discoveredBytes, discoveredWarnings := s.discoverPreviewArtifacts(
		ctx, agent, input, capabilities, inputRows, loaded, existingCanonicalPaths)
	warnings = append(warnings, discoveredWarnings...)
	for sourceRef, bytes := range inputBytes {
		bytesBySourceRef[sourceRef] = bytes
	}
	for sourceRef, bytes := range discoveredBytes {
		bytesBySourceRef[sourceRef] = bytes
	}
	return &artifactSnapshot{
		rows:                insertDiscoveredArtifacts(inputRows, discovered),
		bytesBySourceRef:    bytesBySourceRef,
		additionalArtifacts: inputArtifacts,
		issues:              issues,
		warnings:            warnings,
	}, nil
}

// discoverInputAttachments ports discoverInputAttachments: 读取用户输入附件，
// 替换 ref 为公开 nex-artifact://share/<id>。
func (s *Service) discoverInputAttachments(ctx context.Context, agent AgentService, input PublishInput, capabilities Capabilities, selectedRows []row) (rows []row, artifacts []PublicArtifact, bytesBySourceRef map[string][]byte, warnings []Issue) {
	rows = make([]row, 0, len(selectedRows))
	bytesBySourceRef = map[string][]byte{}
	ordinals := turnOrdinalByProductTurn(selectedRows)
	artifactIndex := 0
	for _, source := range selectedRows {
		if source.kind() != "userInput" {
			rows = append(rows, source)
			continue
		}
		current := copyRow(source)
		attachments, ok := current["attachments"].([]any)
		if !ok || len(attachments) == 0 {
			rows = append(rows, current)
			continue
		}
		var nextAttachments []any
		for attachmentIndex, entry := range attachments {
			attachment, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			fileName, _ := attachment["fileName"].(string)
			mime, _ := attachment["mime"].(string)
			extension, hasExtension := fileExtension(fileName)
			if !hasExtension {
				extension = "txt"
			}
			mimeType := strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
			if mimeType == "" {
				mimeType = "text/plain"
			}
			var allowedType string
			allowed := allowedArtifactMatch(capabilities, "", extension, mimeType)
			if allowed {
				for _, candidate := range capabilities.AllowedArtifacts {
					if allowedArtifactMatch(capabilities, candidate.Type, extension, mimeType) {
						allowedType = candidate.Type
						break
					}
				}
			}
			turnOrdinal := (*int64)(nil)
			if productTurnID, ok := current.productTurnID(); ok {
				if ordinal, exists := ordinals[productTurnID]; exists {
					turnOrdinal = &ordinal
				}
			}
			if !allowed {
				fallbackType := extension
				if extension == "txt" {
					fallbackType = "text"
				}
				warnings = append(warnings, Issue{
					Code: ReasonArtifactTypeNotAllowed, Scope: ScopeArtifact,
					TurnOrdinal: turnOrdinal, ArtifactDisplayName: &fileName,
					ArtifactType: &fallbackType, Extension: &extension, MimeType: &mimeType,
					AllowedArtifacts: allowedArtifactSummaries(capabilities),
				})
				continue
			}
			materialized, mediaType, readErr := readInputAttachment(ctx, agent, input.WorkspaceRef, current, attachmentIndex, capabilities.MaxArtifactBytes)
			if readErr != nil {
				switch readFaultCode(readErr) {
				case faultShareReadNotAuthorized, faultShareReadConnectionUntrusted:
					rows = append(rows, source)
					return rows, artifacts, bytesBySourceRef, warnings
				case faultPreviewTooLarge, faultShareStatTooLarge:
					// 容量超限是确定阻断，不能降级成 warning 后静默发布。
					rowID, _ := current.rowID()
					warnings = append(warnings, Issue{
						Code: ReasonInputAttachment, Scope: ScopeArtifact, RowID: &rowID,
						TurnOrdinal: turnOrdinal, ArtifactDisplayName: &fileName,
						ArtifactType: &allowedType, Extension: &extension, MimeType: &mimeType,
					})
					continue
				}
				availability := "unknown"
				warnings = append(warnings, Issue{
					Code: ReasonInputAttachmentUnavail, Scope: ScopeArtifact,
					TurnOrdinal: turnOrdinal, ArtifactDisplayName: &fileName,
					ArtifactType: &allowedType, Extension: &extension, MimeType: &mimeType,
					Availability: &availability,
				})
				continue
			}
			artifactIndex++
			sourceRef := inputSourceRef(current, attachmentIndex)
			artifactID := artifactIDOfIndex(artifactIndex)
			digest := sha256.Sum256(materialized)
			ref := "nex-artifact://share/" + artifactID
			artifacts = append(artifacts, PublicArtifact{
				SourceRef: sourceRef,
				Descriptor: ArtifactDescriptor{
					ArtifactID: artifactID, LogicalArtifactKey: "share-input-key-" + itoa(artifactIndex),
					ProducerProductTurn: producerTurnOf(current), ArtifactVersion: 1, State: "current",
					Ref: ref, ArtifactType: allowedType, DisplayName: fileName,
					Extension: extension, MimeType: mediaType,
					SizeBytes: int64(len(materialized)), Sha256: hex.EncodeToString(digest[:]),
				},
			})
			bytesBySourceRef[sourceRef] = materialized
			nextAttachments = append(nextAttachments, map[string]any{
				"ref": ref, "previewRef": ref, "fileName": attachment["fileName"],
				"mime": attachment["mime"], "bytes": float64(len(materialized)),
			})
		}
		if len(nextAttachments) > 0 {
			current["attachments"] = nextAttachments
		} else {
			delete(current, "attachments")
		}
		rows = append(rows, current)
	}
	return rows, artifacts, bytesBySourceRef, warnings
}

func producerTurnOf(current row) string {
	if productTurnID, ok := current.productTurnID(); ok {
		return productTurnID
	}
	if turnID, ok := current.turnID(); ok {
		return turnID
	}
	return ""
}

func inputSourceRef(current row, attachmentIndex int) string {
	rowID, _ := current.rowID()
	return "input:" + itoa64(rowID) + ":" + itoa(attachmentIndex)
}

func artifactIDOfIndex(artifactIndex int) string {
	return "share-input-artifact-" + itoa(artifactIndex)
}

func itoa(value int) string { return itoa64(int64(value)) }
func itoa64(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// readInputAttachment ports readInputAttachment: chunked attachment read.
func readInputAttachment(ctx context.Context, agent AgentService, ref WorkspaceRef, current row, attachmentIndex int, maxBytes int64) ([]byte, string, error) {
	attachments, _ := current["attachments"].([]any)
	if attachmentIndex >= len(attachments) {
		return nil, "", newServiceError(KindInvalidConversation, "Conversation input attachment is missing a stable row identity", errorDetails{})
	}
	attachment, ok := attachments[attachmentIndex].(map[string]any)
	if !ok {
		return nil, "", newServiceError(KindInvalidConversation, "Conversation input attachment is missing a stable row identity", errorDetails{})
	}
	entityID, hasEntity := current.entityID()
	rowID, _ := current.rowID()
	if !hasEntity {
		return nil, "", newServiceError(KindInvalidConversation, "Conversation input attachment is missing a stable row identity", errorDetails{})
	}
	attachmentRef, _ := attachment["ref"].(string)
	mediaType, _ := attachment["mime"].(string)
	var chunks [][]byte
	var offset int64
	var totalBytes int64
	for {
		limit := attachmentChunkMaxByte
		if int64(limit) > maxBytes {
			limit = int(maxBytes)
		}
		result, err := agent.ConversationAttachmentReadV4(ctx, AttachmentReadRequest{
			WorkspaceRef: ref, Ref: attachmentRef,
			Target:          RowTarget{RowID: rowID, EntityID: entityID},
			AttachmentIndex: attachmentIndex, Offset: offset, Limit: int64(limit),
		})
		if err != nil {
			return nil, "", err
		}
		chunk, err := base64.StdEncoding.DecodeString(result.DataBase64)
		if err != nil {
			return nil, "", newServiceError(KindInvalidContract, "Conversation input attachment returned invalid read progress", errorDetails{})
		}
		if totalBytes == 0 {
			totalBytes = result.TotalBytes
		}
		if result.MediaType != "" {
			mediaType = result.MediaType
		}
		if totalBytes > maxBytes || (len(chunk) == 0 && result.NextOffset != nil) {
			return nil, "", newServiceError(KindLimitExceeded, "Conversation input attachment exceeds the byte limit", errorDetails{})
		}
		chunks = append(chunks, chunk)
		offset += int64(len(chunk))
		if result.NextOffset == nil {
			break
		}
		if *result.NextOffset != offset || *result.NextOffset > totalBytes {
			return nil, "", newServiceError(KindInvalidConversation, "Conversation input attachment returned invalid read progress", errorDetails{})
		}
	}
	output := make([]byte, offset)
	cursor := 0
	for _, chunk := range chunks {
		copy(output[cursor:], chunk)
		cursor += len(chunk)
	}
	declaredBytes, hasDeclared := numberField(attachment, "bytes")
	if totalBytes != int64(len(output)) || (hasDeclared && declaredBytes > 0 && int64(declaredBytes) != int64(len(output))) {
		return nil, "", newServiceError(KindInvalidConversation, "Conversation input attachment changed before sharing", errorDetails{})
	}
	if len(output) == 0 {
		return nil, "", newServiceError(KindInvalidConversation, "Conversation input attachment is empty", errorDetails{})
	}
	return output, mediaType, nil
}
