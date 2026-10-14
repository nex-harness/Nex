// record_json.go: the onboarding-record JSON renderer — key order is the TS
// shape (version, deviceMid, entries, decisions), two-space indent, no
// trailing newline (atomicWriteText adds none).

package onboarding

import (
	"encoding/json"
	"fmt"
	"strings"
)

// marshalRecordFile renders JSON.stringify(file, null, 2) with the TS key
// order (version, deviceMid, entries, decisions; entry: userId, occupation,
// interfaceMode, memoryEnabled, proactiveSuggestionsEnabled, completedAt,
// uploadState).
func marshalRecordFile(file *RecordFile) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("{\n")
	sb.WriteString("  \"version\": 2,\n")
	sb.WriteString(fmt.Sprintf("  \"deviceMid\": %s,\n", jsonQuote(file.DeviceMid)))
	sb.WriteString("  \"entries\": [")
	for i, entry := range file.Entries {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("\n    {\n")
		sb.WriteString(fmt.Sprintf("      \"userId\": %s,\n", jsonValue(entry.UserID)))
		sb.WriteString(fmt.Sprintf("      \"occupation\": %s,\n", jsonValue(entry.Occupation)))
		sb.WriteString(fmt.Sprintf("      \"interfaceMode\": %s,\n", jsonValue(entry.InterfaceMode)))
		sb.WriteString(fmt.Sprintf("      \"memoryEnabled\": %s,\n", jsonValue(entry.MemoryEnabled)))
		sb.WriteString(fmt.Sprintf("      \"proactiveSuggestionsEnabled\": %s,\n", jsonValue(entry.ProactiveSuggestionsEnabled)))
		sb.WriteString(fmt.Sprintf("      \"completedAt\": %s,\n", jsonQuote(entry.CompletedAt)))
		sb.WriteString("      \"uploadState\": \"pending\"\n")
		sb.WriteString("    }")
	}
	if len(file.Entries) > 0 {
		sb.WriteString("\n  ],\n")
	} else {
		sb.WriteString("],\n")
	}
	sb.WriteString("  \"decisions\": [")
	for i, decision := range file.Decisions {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("\n    {\n")
		sb.WriteString(fmt.Sprintf("      \"userId\": %s,\n", jsonValue(decision.UserID)))
		sb.WriteString(fmt.Sprintf("      \"status\": %s,\n", jsonQuote(decision.Status)))
		sb.WriteString(fmt.Sprintf("      \"reason\": %s,\n", jsonQuote(decision.Reason)))
		sb.WriteString(fmt.Sprintf("      \"decidedAt\": %s\n", jsonQuote(decision.DecidedAt)))
		sb.WriteString("    }")
	}
	if len(file.Decisions) > 0 {
		sb.WriteString("\n  ]\n")
	} else {
		sb.WriteString("]\n")
	}
	sb.WriteString("}")
	return []byte(sb.String()), nil
}

func entryToMap(entry Entry) map[string]any {
	return map[string]any{
		"userId":                      entry.UserID,
		"occupation":                  entry.Occupation,
		"interfaceMode":               entry.InterfaceMode,
		"memoryEnabled":               entry.MemoryEnabled,
		"proactiveSuggestionsEnabled": entry.ProactiveSuggestionsEnabled,
		"completedAt":                 entry.CompletedAt,
		"uploadState":                 entry.UploadState,
	}
}

func jsonValue(v any) string {
	if v == nil {
		return "null"
	}
	switch t := v.(type) {
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return jsonQuote(t)
	default:
		return jsonQuote(fmt.Sprint(t))
	}
}

func jsonQuote(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(data)
}

func (s *Service) warn(format string, args ...any) {
	if s.logger != nil {
		s.logger.Warn("", fmt.Sprintf(format, args...))
	}
}
