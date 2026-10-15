// toolsummary.go ports packages/shared/src/tool-call-summary.ts
// (getCompactToolCallSummary and its change-stat extractor) — the same
// single-implementation rule the bots contract relies on for tool lines.
//
// The status message id map and isCompact*State helpers stay in TS for now: no
// Go caller uses them in this step, and dropping the whole file's semantics into
// a half-used port would be noise. What the bot reply formatter needs is the
// summary triple and the change stat, which this file provides byte-identically.
package bots

import (
	"regexp"
	"strings"
)

// ToolCallChangeStat is ToolCallChangeStat.
type ToolCallChangeStat struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// ToolCallSummary is ToolCallSummary.
type ToolCallSummary struct {
	PrimaryText   string
	SecondaryText string
	ChangeStat    *ToolCallChangeStat
}

// ToolCallSummarySource is ToolCallSummarySource.
type ToolCallSummarySource struct {
	Title  string
	Kind   string
	Input  any
	Output any
	Raw    any
}

var editKindPattern = regexp.MustCompile(`(?i)(edit|patch|replace|multi.?edit)`)

func normalizeDisplayText(value string) string {
	return collapseWhitespace(strings.TrimSpace(value))
}

func countLines(value string) int {
	if len(value) == 0 {
		return 0
	}
	count := 1
	for i := 0; i < len(value); i++ {
		if value[i] == '\n' {
			count++
		}
	}
	if value[len(value)-1] == '\n' {
		count--
	}
	return count
}

func readFirstStringField(value map[string]any, keys []string) (string, bool) {
	for _, key := range keys {
		if candidate, ok := value[key].(string); ok {
			return candidate, true
		}
	}
	return "", false
}

var beforeKeys = []string{"old_string", "oldString", "oldText", "before"}
var afterKeys = []string{"new_string", "newString", "newText", "after", "content"}

type beforeAfter struct {
	before string
	after  string
}

// extractBeforeAfterText ports extractBeforeAfterText: input record, then a
// metadata.filediff record, then a content block array.
func extractBeforeAfterText(source any) (beforeAfter, bool) {
	record, ok := source.(map[string]any)
	if !ok {
		return beforeAfter{}, false
	}
	before, hasBefore := readFirstStringField(record, beforeKeys)
	after, hasAfter := readFirstStringField(record, afterKeys)
	if hasBefore && hasAfter {
		return beforeAfter{before: before, after: after}, true
	}

	if metadata, ok := record["metadata"].(map[string]any); ok {
		if fileDiff, ok := metadata["filediff"].(map[string]any); ok {
			before, hasBefore := readFirstStringField(fileDiff, beforeKeys)
			after, hasAfter := readFirstStringField(fileDiff, afterKeys)
			if hasBefore && hasAfter {
				return beforeAfter{before: before, after: after}, true
			}
		}
	}

	if blocks, ok := record["content"].([]any); ok {
		for _, block := range blocks {
			blockRecord, ok := block.(map[string]any)
			if !ok {
				continue
			}
			before, hasBefore := readFirstStringField(blockRecord, beforeKeys)
			after, hasAfter := readFirstStringField(blockRecord, afterKeys)
			if hasBefore && hasAfter {
				return beforeAfter{before: before, after: after}, true
			}
		}
	}
	return beforeAfter{}, false
}

// getChangeStat ports getChangeStat: only edit-like kinds produce a stat, and it
// is omitted when both counts are zero.
func getChangeStat(kind string, input, output, raw any) *ToolCallChangeStat {
	if !editKindPattern.MatchString(kind) {
		return nil
	}
	change, ok := extractBeforeAfterText(input)
	if !ok {
		change, ok = extractBeforeAfterText(output)
	}
	if !ok {
		change, ok = extractBeforeAfterText(raw)
	}
	if !ok {
		return nil
	}
	removed := countLines(change.before)
	added := countLines(change.after)
	if added == 0 && removed == 0 {
		return nil
	}
	return &ToolCallChangeStat{Added: added, Removed: removed}
}

// getInputSummary ports getInputSummary: a bare string input, else the first
// command/path/prompt-shaped field.
func getInputSummary(input any) string {
	if text, ok := input.(string); ok {
		summary := normalizeDisplayText(text)
		if summary != "" {
			return summary
		}
		return ""
	}
	record, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"command", "path", "file_path", "filePath", "prompt"} {
		candidate, ok := record[key].(string)
		if !ok {
			continue
		}
		summary := normalizeDisplayText(candidate)
		if summary != "" {
			return summary
		}
	}
	return ""
}

// GetCompactToolCallSummary ports getCompactToolCallSummary.
func GetCompactToolCallSummary(source ToolCallSummarySource) ToolCallSummary {
	summary := ToolCallSummary{}
	if source.Title != "" {
		summary.PrimaryText = normalizeDisplayText(source.Title)
	}
	if summary.PrimaryText == "" {
		summary.PrimaryText = "tool"
	}
	summary.SecondaryText = getInputSummary(source.Input)
	summary.ChangeStat = getChangeStat(source.Kind, source.Input, source.Output, source.Raw)
	return summary
}
