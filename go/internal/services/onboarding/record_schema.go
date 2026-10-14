// record_schema.go: the onboardingRecordFileSchema port — v1|v2 union with
// the v1 transform (decisions: [] added at read), entry and decision parsing.

package onboarding

import (
	"errors"
	"fmt"
	"strings"
)

// parseRecordFile ports onboardingRecordFileSchema (v1 | v2 union + the v1
// transform adding decisions: []).
func parseRecordFile(obj map[string]any) (*RecordFile, error) {
	version, _ := obj["version"].(float64)
	deviceMid, _ := obj["deviceMid"].(string)
	if deviceMid == "" {
		return nil, errors.New("deviceMid must be a non-empty string")
	}
	entriesRaw, _ := obj["entries"].([]any)
	entries := make([]Entry, 0, len(entriesRaw))
	for _, item := range entriesRaw {
		entryObj, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("entry must be an object")
		}
		entry, err := parseEntry(entryObj)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	decisions := []Decision{}
	switch int64(version) {
	case 1:
		// v1 无需启动迁移；读取时补空 decisions.
	case 2:
		decisionsRaw, _ := obj["decisions"].([]any)
		for _, item := range decisionsRaw {
			decisionObj, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("decision must be an object")
			}
			decision, err := parseDecision(decisionObj)
			if err != nil {
				return nil, err
			}
			decisions = append(decisions, decision)
		}
	default:
		return nil, fmt.Errorf("invalid version %v", obj["version"])
	}
	return &RecordFile{Version: 2, DeviceMid: deviceMid, Entries: entries, Decisions: decisions}, nil
}

func parseEntry(obj map[string]any) (Entry, error) {
	completedAt, _ := obj["completedAt"].(string)
	if completedAt == "" {
		return Entry{}, errors.New("completedAt must be a non-empty string")
	}
	if uploadState, _ := obj["uploadState"].(string); uploadState != "pending" {
		return Entry{}, errors.New(`uploadState must be "pending"`)
	}
	entry := Entry{
		UserID:                      nullableField(obj, "userId", isNonEmptyString),
		Occupation:                  nullableField(obj, "occupation", isNonEmptyString),
		InterfaceMode:               nullableField(obj, "interfaceMode", isEnum("coding", "office")),
		MemoryEnabled:               nullableField(obj, "memoryEnabled", isBool),
		ProactiveSuggestionsEnabled: nullableField(obj, "proactiveSuggestionsEnabled", isBool),
		CompletedAt:                 completedAt,
		UploadState:                 "pending",
	}
	return entry, nil
}

func parseDecision(obj map[string]any) (Decision, error) {
	decidedAt, _ := obj["decidedAt"].(string)
	if decidedAt == "" {
		return Decision{}, errors.New("decidedAt must be a non-empty string")
	}
	status, _ := obj["status"].(string)
	if status != "dismissed" && status != "existing_local_user" {
		return Decision{}, errors.New("invalid decision status")
	}
	reason, _ := obj["reason"].(string)
	if reason != "user_closed" && reason != "existing_local_task" {
		return Decision{}, errors.New("invalid decision reason")
	}
	return Decision{
		UserID:    nullableField(obj, "userId", isNonEmptyString),
		Status:    status,
		Reason:    reason,
		DecidedAt: decidedAt,
	}, nil
}

// nullableField returns the value for a z.string().min(1).nullable() field:
// absent or null → nil; otherwise validated.
func nullableField(obj map[string]any, key string, validate func(any) bool) any {
	value, present := obj[key]
	if !present || value == nil {
		return nil
	}
	return value
}

func isNonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}

func isBool(v any) bool {
	_, ok := v.(bool)
	return ok
}

func isEnum(values ...string) func(any) bool {
	set := map[string]bool{}
	for _, v := range values {
		set[v] = true
	}
	return func(v any) bool {
		s, ok := v.(string)
		return ok && set[s]
	}
}
