// importstore_read.go holds the read side of the imported-share store: the
// readonly copy lookup, the unsupported-row counter and the persisted index
// load. Split out of importstore.go; symbols are unchanged.
package conversationshare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

// GetImportedConversation ports getImportedConversation: 按 contextId 扫
// .nex-share/ 下各 importRoot 并比对文件内的 contextId。
func (s *Service) GetImportedConversation(ctx context.Context, workspacePath, contextID string) (*ImportedConversation, error) {
	shareRoot := filepath.Join(workspacePath, ".nex-share")
	entries, err := os.ReadDir(shareRoot)
	if err != nil {
		return nil, nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(shareRoot, entry.Name(), "shared-conversation.json"))
		if err != nil {
			continue
		}
		var parsed struct {
			FormatVersion int                `json:"formatVersion"`
			ShareID       string             `json:"shareId"`
			ContextID     string             `json:"contextId"`
			Title         string             `json:"title"`
			Rows          []json.RawMessage  `json:"rows"`
			Artifacts     []ImportedArtifact `json:"artifacts"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		if parsed.ContextID != contextID || parsed.ShareID == "" || parsed.ContextID == "" || parsed.FormatVersion <= 0 {
			continue
		}
		unsupportedRowCount := countUnsupportedRows(parsed.Rows)
		if parsed.FormatVersion > importedConversationShareFormatVersion {
			// formatVersion 比本端新时，认不出的行照样计入，让 UI 出软提示。
			unsupportedRowCount++
		}
		return &ImportedConversation{
			ShareID: parsed.ShareID, ContextID: parsed.ContextID, Title: parsed.Title,
			Rows: parsed.Rows, Artifacts: parsed.Artifacts, UnsupportedRowCount: unsupportedRowCount,
		}, nil
	}
	return nil, nil
}

// countUnsupportedRows ports decodeConversationShareRows's unsupported count
// for the readonly copy: rows that are not valid JSON objects with a known
// kind are unsupported.
var knownRowKinds = map[string]bool{
	"turnHeader": true, "userInput": true, "assistantText": true, "reasoning": true,
	"toolCall": true, "artifact": true, "timelineMarker": true, "subagent": true,
	"hookInvocation": true,
}

func countUnsupportedRows(rows []json.RawMessage) int {
	count := 0
	for _, raw := range rows {
		var probe struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil || !knownRowKinds[probe.Kind] {
			count++
		}
	}
	return count
}

// reportImportProgress fires the import progress event.
func (s *Service) reportImportProgress(operationID, phase string, completed, total int) {
	s.mu.Lock()
	handlers := append([]func(ImportProgress){}, s.importProgressHandlers[operationID]...)
	s.mu.Unlock()
	progress := ImportProgress{
		OperationID: operationID, Phase: phase,
		CompletedArtifacts: completed, TotalArtifacts: total,
	}
	for _, handler := range handlers {
		handler(progress)
	}
}

// OnImportProgress ports onDynamicImportProgress(operationId).
func (s *Service) OnImportProgress(operationID string, handler func(ImportProgress)) (cancel func()) {
	s.mu.Lock()
	s.importProgressHandlers[operationID] = append(s.importProgressHandlers[operationID], handler)
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			handlers := s.importProgressHandlers[operationID]
			for index, candidate := range handlers {
				// 函数值不可比较，按指针身份（同一 closure 变量）匹配。
				if reflect.ValueOf(candidate).Pointer() == reflect.ValueOf(handler).Pointer() {
					s.importProgressHandlers[operationID] = append(handlers[:index], handlers[index+1:]...)
					break
				}
			}
		})
	}
}

// persistCompletedImportIndex ports persistCompletedImportIndex + the
// .nex-share-imports.json file. The write is serialized through a mutex (the
// TS chains promises for the same guarantee).
func (s *Service) persistCompletedImportIndex() error {
	s.importIndexMu.Lock()
	defer s.importIndexMu.Unlock()
	if s.conversationWorkspaceRoot == "" {
		return nil
	}
	importIndexPath := filepath.Join(s.conversationWorkspaceRoot, ".nex-share-imports.json")
	s.importState.mu.Lock()
	index := map[string]ImportResult{}
	for key, result := range s.importState.completed {
		index[key] = result
	}
	s.importState.mu.Unlock()
	encoded, err := json.Marshal(index)
	if err != nil {
		return err
	}
	temporaryPath := importIndexPath + "." + newRequestID() + ".tmp"
	if err := os.WriteFile(temporaryPath, encoded, 0o644); err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	return os.Rename(temporaryPath, importIndexPath)
}

// loadCompletedImportIndex ports loadCompletedImportIndex.
func (s *Service) loadCompletedImportIndex() {
	if s.conversationWorkspaceRoot == "" {
		return
	}
	importIndexPath := filepath.Join(s.conversationWorkspaceRoot, ".nex-share-imports.json")
	data, err := os.ReadFile(importIndexPath)
	if err != nil {
		return
	}
	var raw map[string]ImportResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for key, record := range raw {
		if record.WorkspacePath == "" || record.SessionID == "" || record.Title == "" ||
			record.ContextID == "" || record.ShareURL == "" {
			continue
		}
		if info, err := os.Stat(record.WorkspacePath); err != nil || !info.IsDir() {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		shareCode := parts[0]
		workspaceKey := workspaceKeyOf(record.WorkspacePath, record.WorkspaceIdentity)
		if len(parts) > 1 {
			workspaceKey = parts[1]
		}
		record.Reused = true
		workspaceKeyedShare := importDedupeKey(shareCode, workspaceKey)
		s.importState.mu.Lock()
		s.importState.completed[key] = record
		s.importState.completedByWorkspace[workspaceKeyedShare] = record
		s.importState.mu.Unlock()
	}
}

// formatSharedContextV1 ports formatSharedContextV1: the markdown the
// imported session body carries, its sha256, and the unsupported row kinds.
func formatSharedContextV1(continuation Continuation, installed []installedArtifactRef) (string, string, []string) {
	paths := map[string]installedArtifactRef{}
	for _, artifact := range installed {
		paths[artifact.ArtifactID] = artifact
	}
	fenceFor := func(value string) string {
		fence := "```"
		if strings.Contains(value, "```") {
			fence = "````"
		}
		return fence + "\n" + value + "\n" + fence
	}
	sections := []string{"# Shared conversation: " + continuation.Share.Title}
	unsupportedKinds := map[string]bool{}
	var unsupportedOrder []string
	for _, raw := range continuation.Rows {
		var current row
		if err := json.Unmarshal(raw, &current); err != nil {
			continue
		}
		switch current.kind() {
		case "turnHeader":
			continue
		case "userInput":
			var attachmentLines []string
			if attachments, ok := current["attachments"].([]any); ok {
				for _, entry := range attachments {
					attachment, ok := entry.(map[string]any)
					if !ok {
						continue
					}
					ref, _ := attachment["ref"].(string)
					artifactID := strings.TrimPrefix(ref, "nex-artifact://share/")
					if installedArtifact, exists := paths[artifactID]; exists && ref != artifactID {
						attachmentLines = append(attachmentLines,
							"- "+installedArtifact.DisplayName,
							"  - Path: "+installedArtifact.WorkspaceRelativePath,
							"  - MIME: "+installedArtifact.MimeType,
							"  - SHA-256: "+installedArtifact.Sha256)
					}
				}
			}
			text, _ := current["text"].(string)
			section := "## User\n\n" + text
			if len(attachmentLines) > 0 {
				section += "\n\n### Attachments\n\n" + strings.Join(attachmentLines, "\n")
			}
			sections = append(sections, section)
		case "assistantText":
			text, _ := current["text"].(string)
			sections = append(sections, "## Assistant\n\n"+text)
		case "reasoning":
			text, _ := current["text"].(string)
			sections = append(sections, "## Reasoning\n\n"+text)
		case "toolCall":
			toolName, _ := current["toolName"].(string)
			inputText, _ := current["inputText"].(string)
			section := "## Tool: " + toolName + "\n\n### Input\n\n" + fenceFor(inputText)
			if output, ok := current["output"].(map[string]any); ok {
				if outputText, ok := output["text"].(string); ok && outputText != "" {
					section += "\n\n### Output\n\n" + fenceFor(outputText)
				}
			}
			sections = append(sections, section)
		case "timelineMarker":
			encoded, err := json.Marshal(current["marker"])
			if err != nil {
				continue
			}
			sections = append(sections, "## Timeline\n\n"+fenceFor(string(encoded)))
		case "artifact":
			artifactVersionID, _ := current["artifactVersionId"].(string)
			installedArtifact, exists := paths[artifactVersionID]
			if !exists {
				continue
			}
			sections = append(sections, strings.Join([]string{
				"## Artifact: " + installedArtifact.DisplayName,
				"- Path: " + installedArtifact.WorkspaceRelativePath,
				"- MIME: " + installedArtifact.MimeType,
				"- SHA-256: " + installedArtifact.Sha256,
			}, "\n"))
		case "subagent", "hookInvocation":
			continue
		default:
			// 未来新增的 row kind：不抛，但留痕。
			kind := current.kind()
			if kind == "" {
				kind = "unknown"
			}
			if !unsupportedKinds[kind] {
				unsupportedKinds[kind] = true
				unsupportedOrder = append(unsupportedOrder, kind)
			}
		}
	}
	markdown := strings.Join(sections, "\n\n") + "\n"
	digest := sha256.Sum256([]byte(markdown))
	return markdown, hex.EncodeToString(digest[:]), unsupportedOrder
}

// installedArtifactRef is the local installed-artifact record shape.
type installedArtifactRef struct {
	ArtifactID            string
	WorkspaceRelativePath string
	DisplayName           string
	MimeType              string
	Sha256                string
}
