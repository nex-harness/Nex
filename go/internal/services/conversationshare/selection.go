// selection.go ports the row-tree half of conversationShareService.ts:
// selectRows, sanitizeUnsupportedShareStructures, sanitizeUnsupportedShareArtifacts,
// collectShareStructureIssues, and the turn-ordinal bookkeeping.
//
// Rows are JSON trees (map[string]any), matching what the agent V4 read
// returns over the wire. Every accessor is defensive: a missing or
// wrong-typed field is "absent", never a panic.
package conversationshare

import "regexp"

// Selection ports ConversationShareSelection (discriminated on kind).
type Selection struct {
	Kind           string   `json:"kind"` // all | productTurns | rowAnchors
	ProductTurnIDs []string `json:"productTurns,omitempty"`
	RowIDs         []int64  `json:"rowIds,omitempty"`
}

// row is one conversation row as a JSON object.
type row map[string]any

func (r row) kind() string {
	text, _ := r["kind"].(string)
	return text
}

func (r row) stringField(key string) (string, bool) {
	text, ok := r[key].(string)
	return text, ok && text != ""
}

func (r row) productTurnID() (string, bool) { return r.stringField("productTurnId") }

func (r row) turnID() (string, bool) { return r.stringField("turnId") }

func (r row) rowID() (int64, bool) {
	number, ok := r["rowId"].(float64)
	if !ok || number != float64(int64(number)) {
		return 0, false
	}
	return int64(number), true
}

func (r row) entityID() (string, bool) { return r.stringField("entityId") }

// turnOrdinalByProductTurn ports turnOrdinalByProductTurn: 服务端按全部
// turnHeader 编号的序号；轮次定位必须按身份，turnOrdinal 只用于展示文案。
func turnOrdinalByProductTurn(rows []row) map[string]int64 {
	result := map[string]int64{}
	for _, current := range rows {
		if current.kind() != "turnHeader" {
			continue
		}
		productTurnID, ok := current.productTurnID()
		if !ok {
			continue
		}
		if _, exists := result[productTurnID]; exists {
			continue
		}
		result[productTurnID] = int64(len(result) + 1)
	}
	return result
}

// hasUnsafeShareString ports hasUnsafeShareString: data:/file: anywhere.
func hasUnsafeShareString(value any) bool {
	switch typed := value.(type) {
	case string:
		return unsafeStringRe.MatchString(typed)
	case []any:
		for _, entry := range typed {
			if hasUnsafeShareString(entry) {
				return true
			}
		}
	// `row` 是 map[string]any 的具名类型：type switch 的具名分支不会命中它，
	// 必须单独列出，否则对整行的扫描会静默判定为安全。
	case row:
		for key, entry := range typed {
			if key == "ref" {
				continue
			}
			if hasUnsafeShareString(entry) {
				return true
			}
		}
	case map[string]any:
		for key, entry := range typed {
			if key == "ref" {
				continue
			}
			if hasUnsafeShareString(entry) {
				return true
			}
		}
	}
	return false
}

var unsafeStringRe = regexp.MustCompile(`(?i)^(?:data|file):`)

// hasUnsupportedArtifactReference ports hasUnsupportedArtifactReference.
func hasUnsupportedArtifactReference(value any) bool {
	switch typed := value.(type) {
	case string:
		return artifactRefRe.MatchString(typed)
	case []any:
		for _, entry := range typed {
			if hasUnsupportedArtifactReference(entry) {
				return true
			}
		}
	case row:
		for key, entry := range typed {
			if key == "ref" {
				continue
			}
			if hasUnsupportedArtifactReference(entry) {
				return true
			}
		}
	case map[string]any:
		for key, entry := range typed {
			if key == "ref" {
				continue
			}
			if hasUnsupportedArtifactReference(entry) {
				return true
			}
		}
	}
	return false
}

var artifactRefRe = regexp.MustCompile(`(?i)^nex-artifact://`)

// sanitizeUnsupportedShareStructures ports the same-named TS function: V1
// 公开投影承载不了的已定稿内部结构静默移除（inline tool images、内部 timeline
// marker、EnterPlanMode），运行中的轮次/行/工具/子代理仍由
// collectShareStructureIssues 阻断。
func sanitizeUnsupportedShareStructures(rows []row) []row {
	sanitized := make([]row, 0, len(rows))
	for _, current := range rows {
		switch current.kind() {
		case "toolCall":
			if display, ok := current["display"].(map[string]any); ok {
				if displayKind, _ := display["kind"].(string); displayKind == "node_repl_images" {
					// 内嵌图片就在 display.images 的 base64 里，删掉 display 即移除全部图片字节。
					stripped := copyRow(current)
					delete(stripped, "display")
					sanitized = append(sanitized, stripped)
					continue
				}
			}
			if toolName, _ := current["toolName"].(string); toolName == "EnterPlanMode" {
				// 内部模式切换边界，渲染层本来就过滤，白吃配额；直接移除。
				continue
			}
		case "timelineMarker":
			marker, _ := current["marker"].(map[string]any)
			markerType, _ := marker["type"].(string)
			markerRunning :=
				(markerType == "compact" && statusOf(marker) == "running") ||
					(markerType == "goalVerify" && outcomeOf(marker) == "running")
			if markerRunning {
				sanitized = append(sanitized, current)
				continue
			}
			// fork/checkpoint marker 只携带本地会话关系，其余 marker 纯记账，
			// 公开页不渲染——全部移除，不打扰分享者。
			continue
		}
		sanitized = append(sanitized, current)
	}
	return sanitized
}

func statusOf(marker map[string]any) string {
	status, _ := marker["status"].(string)
	return status
}

func outcomeOf(marker map[string]any) string {
	outcome, _ := marker["outcome"].(string)
	return outcome
}

func copyRow(source row) row {
	dest := make(row, len(source))
	for key, value := range source {
		dest[key] = value
	}
	return dest
}

// removeIndependentArtifactRows ports removeIndependentArtifactRows.
func removeIndependentArtifactRows(rows []row) []row {
	out := rows[:0:0]
	for _, current := range rows {
		if current.kind() == "artifact" {
			continue
		}
		out = append(out, current)
	}
	return out
}

// sanitizeUnsupportedShareArtifacts ports the same-named function: rows 里的
// artifact 类型/扩展/mime 不在能力表内时移除该行并给出可跳过 warning。
func sanitizeUnsupportedShareArtifacts(rows []row, capabilities Capabilities, ordinalRows []row) ([]row, []Issue) {
	ordinals := turnOrdinalByProductTurn(ordinalRows)
	var warnings []Issue
	retained := make([]row, 0, len(rows))
	for _, current := range rows {
		if current.kind() != "artifact" {
			retained = append(retained, current)
			continue
		}
		displayName, _ := current["displayName"].(string)
		mimeType, _ := current["mimeType"].(string)
		extension, _ := fileExtension(displayName)
		normalizedMime := normalizedMimeType(mimeType)
		artifactType, _ := current["artifactType"].(string)
		if allowedArtifactMatch(capabilities, artifactType, extension, normalizedMime) {
			retained = append(retained, current)
			continue
		}
		issue := Issue{Code: ReasonArtifactTypeNotAllowed, Scope: ScopeArtifact}
		if rowID, ok := current.rowID(); ok {
			value := rowID
			issue.RowID = &value
		}
		attachTurnIdentity(&issue, current, ordinals)
		name := displayName
		issue.ArtifactDisplayName = &name
		issue.ArtifactType = &artifactType
		if extension != "" {
			issue.Extension = &extension
		}
		issue.MimeType = &normalizedMime
		issue.AllowedFormats = allowedFormatLabels(capabilities)
		issue.AllowedArtifacts = allowedArtifactSummaries(capabilities)
		warnings = append(warnings, issue)
	}
	return retained, warnings
}

func attachTurnIdentity(issue *Issue, current row, ordinals map[string]int64) {
	if productTurnID, ok := current.productTurnID(); ok {
		issue.ProductTurnID = &productTurnID
		if ordinal, exists := ordinals[productTurnID]; exists {
			issue.TurnOrdinal = &ordinal
		}
	}
}

// collectShareStructureIssues ports collectShareStructureIssues.
func collectShareStructureIssues(rows []row, ordinalRows []row) []Issue {
	var issues []Issue
	ordinals := turnOrdinalByProductTurn(ordinalRows)
	appendIssue := func(current row, code, scope string) {
		issue := Issue{Code: code, Scope: scope}
		if rowID, ok := current.rowID(); ok {
			value := rowID
			issue.RowID = &value
		}
		if productTurnID, ok := current.productTurnID(); ok {
			issue.ProductTurnID = &productTurnID
			if ordinal, exists := ordinals[productTurnID]; exists {
				issue.TurnOrdinal = &ordinal
			}
		}
		issues = append(issues, issue)
	}
	for _, current := range rows {
		if _, ok := current.productTurnID(); !ok {
			appendIssue(current, ReasonMissingProductTurn, ScopeConversation)
		}
		switch current.kind() {
		case "turnHeader":
			if state, _ := current["state"].(string); state == "running" {
				appendIssue(current, ReasonRunningTurn, ScopeTurn)
			}
		case "assistantText", "reasoning":
			if state, _ := current["state"].(string); state == "streaming" {
				appendIssue(current, ReasonStreamingRow, ScopeTurn)
			}
		case "toolCall":
			status, _ := current["status"].(string)
			if status == "inputStreaming" || status == "pendingApproval" || status == "running" {
				appendIssue(current, ReasonActiveToolCall, ScopeTurn)
			}
		case "subagent":
			if status, _ := current["status"].(string); status == "running" {
				appendIssue(current, ReasonActiveSubagent, ScopeTurn)
			}
		case "timelineMarker":
			marker, _ := current["marker"].(map[string]any)
			markerType, _ := marker["type"].(string)
			if (markerType == "compact" && statusOf(marker) == "running") ||
				(markerType == "goalVerify" && outcomeOf(marker) == "running") {
				appendIssue(current, ReasonUnsupportedTimeline, ScopeTurn)
			}
		}
		if hasUnsafeShareString(current) {
			appendIssue(current, ReasonUnsafeURL, ScopeTurn)
		}
		if hasUnsupportedArtifactReference(current) {
			appendIssue(current, ReasonArtifactProtocol, ScopeTurn)
		}
	}
	return issues
}
