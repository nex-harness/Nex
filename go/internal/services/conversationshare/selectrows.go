// selectrows.go holds the row-selection half of the share service: selectRows
// and the per-turn issue partitioning. Split out of selection.go; symbols are
// unchanged.
package conversationshare

import (
	"strings"
)

// allowedFormatLabels ports allowedFormatLabels.
func allowedFormatLabels(capabilities Capabilities) []string {
	var labels []string
	for _, allowed := range capabilities.AllowedArtifacts {
		for _, extension := range allowed.Extensions {
			labels = append(labels, strings.ToUpper(allowed.Type)+" (."+strings.TrimPrefix(extension, ".")+")")
		}
	}
	return labels
}

// allowedArtifactSummaries ports allowedArtifactSummaries.
func allowedArtifactSummaries(capabilities Capabilities) []AllowedArtifact {
	summaries := make([]AllowedArtifact, 0, len(capabilities.AllowedArtifacts))
	for _, allowed := range capabilities.AllowedArtifacts {
		summaries = append(summaries, AllowedArtifact{
			Type: allowed.Type, Extensions: allowed.Extensions, MimeTypes: allowed.MimeTypes,
		})
	}
	return summaries
}

// capabilitiesFingerprint ports capabilitiesFingerprint: sha256 over the
// canonical JSON of the capabilities tree.
func capabilitiesFingerprint(capabilities Capabilities) string {
	encoded, err := marshalToTree(capabilities)
	if err != nil {
		return ""
	}
	digest, err := sha256CanonicalJSON(encoded)
	if err != nil {
		return ""
	}
	return digest
}

// allowedArtifactFor ports allowedArtifactFor (extension + mime must both
// match one allowed entry).
func allowedArtifactFor(capabilities Capabilities, extension string, mimeType string) bool {
	if extension == "" {
		return false
	}
	return allowedArtifactMatch(capabilities, "", extension, mimeType)
}

func allowedArtifactMatch(capabilities Capabilities, artifactType, extension, mimeType string) bool {
	for _, allowed := range capabilities.AllowedArtifacts {
		if artifactType != "" && allowed.Type != artifactType {
			continue
		}
		extensionMatch := false
		for _, candidate := range allowed.Extensions {
			if strings.ToLower(strings.TrimPrefix(candidate, ".")) == extension {
				extensionMatch = true
				break
			}
		}
		if !extensionMatch {
			continue
		}
		for _, candidate := range allowed.MimeTypes {
			if strings.ToLower(candidate) == mimeType {
				return true
			}
		}
	}
	return false
}

// selectRows ports selectRows. On invalid selection it throws the exact TS
// service errors (invalid_selection with the three messages).
func selectRows(rows []row, selection Selection) (selected []row, productTurnIDs []string, err error) {
	var selectedIDs map[string]bool
	switch selection.Kind {
	case "all":
		selectedIDs = map[string]bool{}
		for _, current := range rows {
			if current.kind() != "turnHeader" {
				continue
			}
			if productTurnID, ok := current.productTurnID(); ok {
				selectedIDs[productTurnID] = true
			}
		}
	case "productTurns":
		if len(selection.ProductTurnIDs) == 0 {
			return nil, nil, newServiceError(KindInvalidSelection, "At least one product turn must be selected", errorDetails{})
		}
		selectedIDs = map[string]bool{}
		for _, productTurnID := range selection.ProductTurnIDs {
			selectedIDs[productTurnID] = true
		}
	default:
		if len(selection.RowIDs) == 0 {
			return nil, nil, newServiceError(KindInvalidSelection, "At least one conversation row must be selected", errorDetails{})
		}
		rowsByID := map[int64]row{}
		for _, current := range rows {
			if rowID, ok := current.rowID(); ok {
				rowsByID[rowID] = current
			}
		}
		selectedIDs = map[string]bool{}
		seenRowIDs := map[int64]bool{}
		for _, rowID := range selection.RowIDs {
			if seenRowIDs[rowID] {
				continue
			}
			seenRowIDs[rowID] = true
			current, exists := rowsByID[rowID]
			if !exists {
				return nil, nil, newServiceError(KindInvalidSelection, "A selected row no longer exists", errorDetails{})
			}
			productTurnID, ok := current.productTurnID()
			if !ok {
				return nil, nil, newServiceError(KindInvalidSelection, "A selected row no longer exists", errorDetails{})
			}
			selectedIDs[productTurnID] = true
		}
	}

	var orderedProductTurnIDs []string
	headerCounts := map[string]int64{}
	for _, current := range rows {
		if current.kind() != "turnHeader" {
			continue
		}
		productTurnID, ok := current.productTurnID()
		if !ok {
			continue
		}
		headerCounts[productTurnID]++
		if selectedIDs[productTurnID] {
			orderedProductTurnIDs = append(orderedProductTurnIDs, productTurnID)
		}
	}
	if len(orderedProductTurnIDs) == 0 || len(orderedProductTurnIDs) != len(selectedIDs) {
		return nil, nil, newServiceError(KindInvalidSelection, "Selected product turns are incomplete or ambiguous", errorDetails{})
	}
	for _, productTurnID := range orderedProductTurnIDs {
		if headerCounts[productTurnID] != 1 {
			return nil, nil, newServiceError(KindInvalidSelection, "Selected product turns are incomplete or ambiguous", errorDetails{})
		}
	}

	selectedTurnIDs := map[string]bool{}
	for _, current := range rows {
		if current.kind() != "turnHeader" {
			continue
		}
		productTurnID, ok := current.productTurnID()
		if !ok || !selectedIDs[productTurnID] {
			continue
		}
		if turnID, ok := current.turnID(); ok {
			selectedTurnIDs[turnID] = true
		}
	}
	// 旧投影可能缺 productTurnId；只要 turnId 落在已选轮次也必须保留，让校验显式拒绝。
	for _, current := range rows {
		productTurnID, hasProductTurn := current.productTurnID()
		if hasProductTurn && selectedIDs[productTurnID] {
			selected = append(selected, current)
			continue
		}
		if turnID, ok := current.turnID(); ok && selectedTurnIDs[turnID] {
			selected = append(selected, current)
		}
	}
	return selected, orderedProductTurnIDs, nil
}

// buildTurnPreflightResults ports buildTurnPreflightResults.
func buildTurnPreflightResults(productTurnIDs []string, ordinals map[string]int64, blocking, warnings, deferred []Issue) []TurnPreflightResult {
	results := make([]TurnPreflightResult, 0, len(productTurnIDs))
	for _, productTurnID := range productTurnIDs {
		ordinal, hasOrdinal := ordinals[productTurnID]
		results = append(results, TurnPreflightResult{
			ProductTurnID:     productTurnID,
			TurnFingerprint:   "",
			BlockingIssues:    issuesForTurn(blocking, ordinal, hasOrdinal),
			SkippableWarnings: issuesForTurn(warnings, ordinal, hasOrdinal),
			DeferredIssues:    issuesForTurn(deferred, ordinal, hasOrdinal),
		})
	}
	return results
}

func issuesForTurn(issues []Issue, ordinal int64, hasOrdinal bool) []Issue {
	var out []Issue
	for _, issue := range issues {
		if issue.TurnOrdinal != nil && hasOrdinal && *issue.TurnOrdinal == ordinal {
			out = append(out, issue)
			continue
		}
		if issue.TurnOrdinal == nil && issue.RowID == nil && issue.ArtifactDisplayName == nil &&
			(issue.Scope == ScopeConversation || issue.Scope == ScopeTransport) {
			out = append(out, issue)
		}
	}
	return out
}

// TurnPreflightResult ports ConversationShareTurnPreflightResult.
type TurnPreflightResult struct {
	ProductTurnID     string  `json:"productTurnId"`
	TurnFingerprint   string  `json:"turnFingerprint,omitempty"`
	BlockingIssues    []Issue `json:"blockingIssues"`
	SkippableWarnings []Issue `json:"skippableWarnings"`
	DeferredIssues    []Issue `json:"deferredIssues"`
}

// PreflightResult ports ConversationSharePreflightResult.
type PreflightResult struct {
	Revision                int                   `json:"revision"`
	LogEpoch                string                `json:"logEpoch"`
	CapabilitiesFingerprint string                `json:"capabilitiesFingerprint"`
	BlockingIssues          []Issue               `json:"blockingIssues"`
	SkippableWarnings       []Issue               `json:"skippableWarnings"`
	DeferredIssues          []Issue               `json:"deferredIssues"`
	SupportedArtifactTypes  []AllowedArtifact     `json:"supportedArtifactTypes"`
	TurnResults             []TurnPreflightResult `json:"turnResults"`
}
