// projection.go ports conversationSharePublicProjection.ts: the V1 public
// projection. It works on JSON row trees: every projected row is rebuilt by
// an allow-list (never spread-copied), every identity is re-minted as a
// share-* id, and the result is re-validated by the closure checks before it
// can leave the function.
//
// The allow-list discipline is the privacy core of the share path: local-only
// fields (workId, sourceCommandId, feedback, originMeta identities, the
// internal artifact ref) are structurally unable to appear in the payload
// because projectRow never copies them.
package conversationshare

import (
	"encoding/json"
	"regexp"
)

// PublicArtifact ports ConversationSharePublicArtifact.
type PublicArtifact struct {
	SourceRef  string
	Descriptor ArtifactDescriptor
}

// Projection ports ConversationSharePublicProjection.
type Projection struct {
	Rows                   []json.RawMessage
	SelectedProductTurnIDs []string
	Artifacts              []PublicArtifact
}

var (
	publicProductTurnRe = regexp.MustCompile(`^share-product-turn-[1-9][0-9]*$`)
	publicTurnRe        = regexp.MustCompile(`^share-turn-[1-9][0-9]*$`)
	publicEntityRe      = regexp.MustCompile(`^share-entity-[1-9][0-9]*$`)
	publicToolCallRe    = regexp.MustCompile(`^share-tool-call-[1-9][0-9]*$`)
	publicArtifactRe    = regexp.MustCompile(`^share-artifact-[1-9][0-9]*$`)
	publicArtifactRefRe = regexp.MustCompile(`^nex-artifact://share/[A-Za-z0-9._~-]+$`)
)

// projectionError mirrors throwProjectionError.
func projectionError(kind, message string) *serviceError {
	return newServiceError(kind, message, errorDetails{})
}

// assertSafeString ports assertSafeString.
func assertSafeString(value string) error {
	if regexpArtifactScheme.MatchString(value) {
		return projectionError(KindArtifactProtocolNotReady2, "Artifact references must be represented by formal conversation artifact rows")
	}
	if regexpUnsafeScheme.MatchString(value) {
		return projectionError(KindUnsafeStructure, "Local and inline URLs cannot be shared")
	}
	return nil
}

var (
	regexpArtifactScheme = regexp.MustCompile(`(?i)^nex-artifact://`)
	regexpUnsafeScheme   = regexp.MustCompile(`(?i)^(?:data|file):`)
)

// KindArtifactProtocolNotReady2 is the projection-site spelling of the same
// kind constant used by the service layer (kept local to avoid an import
// cycle in the const block ordering).
const KindArtifactProtocolNotReady2 = KindArtifactProtocolNotReady

// visitStrings ports visitStrings.
func visitStrings(value any, visitor func(string)) {
	switch typed := value.(type) {
	case string:
		visitor(typed)
	case []any:
		for _, entry := range typed {
			visitStrings(entry, visitor)
		}
	// `row` 是 map[string]any 的具名类型：type switch 的具名分支不会命中它，
	// 必须单独列出，否则对整行的遍历会静默返回空。
	case row:
		for _, entry := range typed {
			visitStrings(entry, visitor)
		}
	case map[string]any:
		for _, entry := range typed {
			visitStrings(entry, visitor)
		}
	}
}

// assertTerminalAndSafe ports assertTerminalAndSafe.
func assertTerminalAndSafe(rows []row) error {
	for _, current := range rows {
		if _, ok := current.productTurnID(); !ok {
			return projectionError(KindInvalidConversation, "Conversation row is missing its product turn identity")
		}
		kind := current.kind()
		switch kind {
		case "turnHeader":
			if state, _ := current["state"].(string); state == "running" {
				return projectionError(KindInvalidConversation, "Running turns cannot be shared")
			}
		case "assistantText", "reasoning":
			if state, _ := current["state"].(string); state == "streaming" {
				return projectionError(KindInvalidConversation, "Streaming rows cannot be shared")
			}
		case "toolCall":
			status, _ := current["status"].(string)
			if status == "inputStreaming" || status == "pendingApproval" || status == "running" {
				return projectionError(KindInvalidConversation, "Active tool calls cannot be shared")
			}
			if display, ok := current["display"].(map[string]any); ok {
				if displayKind, _ := display["kind"].(string); displayKind == "node_repl_images" {
					return projectionError(KindUnsafeStructure, "Inline tool images cannot be shared")
				}
			}
		case "subagent":
			if status, _ := current["status"].(string); status == "running" {
				return projectionError(KindInvalidConversation, "Active subagents cannot be shared")
			}
		case "timelineMarker":
			marker, _ := current["marker"].(map[string]any)
			markerType, _ := marker["type"].(string)
			if (markerType == "compact" && statusOf(marker) == "running") ||
				(markerType == "goalVerify" && outcomeOf(marker) == "running") {
				return projectionError(KindInvalidConversation, "Active timeline operations cannot be shared")
			}
			if markerType == "forkNotice" || markerType == "forkCreated" || markerType == "checkpointRestored" {
				return projectionError(KindInvalidConversation, "Timeline references outside the shared projection are not supported")
			}
			if markerType == "compact" {
				if _, hasSummary := marker["summaryRef"]; hasSummary {
					return projectionError(KindArtifactProtocolNotReady2, "Conversation summary references require the artifact row protocol")
				}
			}
		}
		if err := assertRowStringsSafe(current); err != nil {
			return err
		}
	}
	return nil
}

// assertRowStringsSafe applies the per-kind skip rules (artifact.ref,
// truncated.ref, userInput.attachments do not join the generic URL sweep).
func assertRowStringsSafe(current row) error {
	kind := current.kind()
	safeCopy := copyRow(current)
	switch kind {
	case "artifact":
		delete(safeCopy, "ref")
	case "toolCall":
		if output, ok := safeCopy["output"].(map[string]any); ok {
			if truncated, isTruncated := output["truncated"].(map[string]any); isTruncated {
				_ = truncated
				// 只保留 output.text（truncated.ref 是本地按需读取引用）。
				replacement := map[string]any{"text": output["text"]}
				safeCopy["output"] = replacement
			}
		}
	case "userInput":
		delete(safeCopy, "attachments")
	}
	var failure error
	visitStrings(safeCopy, func(value string) {
		if failure == nil {
			failure = assertSafeString(value)
		}
	})
	return failure
}
