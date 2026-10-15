// permission.go ports packages/shared/src/permission-request-preview.ts
// (getPermissionRequestPreview and its collectors) as far as the bot reply
// formatter needs it.
//
// It is declared here rather than in a shared package because the bots contract
// is the only consumer in this step; when the UI needs the same preview (step
// 3.9's siblings in other tiers) the file moves to internal/ unchanged.
//
// The traversal is defensive by construction: it walks arbitrary JSON produced
// by a model or a provider, with a `seen` set against cycles, a 6-path cap and
// no assumption that a key exists.
package bots

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// PermissionFileChange is PermissionRequestFileChange.
type PermissionFileChange struct {
	Path string `json:"path"`
	Type string `json:"type"` // "add" | "update"
}

// PermissionScope is PermissionRequestScope.
type PermissionScope string

const (
	ScopeCommand PermissionScope = "command"
	ScopeFile    PermissionScope = "file"
	ScopeGeneric PermissionScope = "generic"
)

// PermissionPreview is PermissionRequestPreview.
type PermissionPreview struct {
	Title       string                 `json:"title"`
	Command     *string                `json:"command"`
	FilePaths   []string               `json:"filePaths"`
	Scope       PermissionScope        `json:"scope"`
	FileChange  *PermissionFileChange  `json:"fileChange"`
	FileChanges []PermissionFileChange `json:"fileChanges"`
}

var (
	commandKeys = map[string]bool{
		"command": true, "cmd": true, "script": true, "shellcommand": true,
	}
	argumentKeys = map[string]bool{
		"args": true, "argv": true, "arguments": true,
	}
	filePathKeys = map[string]bool{
		"path": true, "paths": true, "file": true,
		// Nex Agent edit 权限常把目标文件放在 file_path/filePath，漏掉会让权限预览只剩标题。
		"file_path": true, "filepath": true, "files": true, "filename": true,
		"filenames": true, "target": true, "targets": true,
		"location": true, "locations": true,
	}
	ignoredDirectoryKeys = map[string]bool{
		"cwd": true, "directory": true, "workingdirectory": true,
	}
)

const maxPermissionFilePaths = 6

func isJSONRecord(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func normalizeInlineText(value string) string {
	return collapseWhitespace(strings.TrimSpace(value))
}

func normalizeBlockText(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}

// getStringArray ports getStringArray: strings pass through, numbers/booleans
// are stringified, everything else becomes "".
func getStringArray(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			out = append(out, strings.TrimSpace(typed))
		case float64:
			out = append(out, jsonNumberString(typed))
		case bool:
			if typed {
				out = append(out, "true")
			} else {
				out = append(out, "false")
			}
		}
	}
	// TS: `.filter(item => item.length > 0)` after mapping "" stays.
	filtered := out[:0]
	for _, item := range out {
		if item != "" {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// readPermissionInputSource ports readPermissionInputSource: rawInput wins, then
// input (the nex protocol schema), else the record itself.
func readPermissionInputSource(raw any) any {
	record, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	if value, has := record["rawInput"]; has && value != nil {
		return value
	}
	// Bugfix (TS): Nex protocol 的 requestPermission schema 使用 input 承载工具参数。
	if value, has := record["input"]; has {
		return value
	}
	return raw
}

// getCommandFromRecord ports getCommandFromRecord.
func getCommandFromRecord(record map[string]any) string {
	for key, value := range record {
		if !commandKeys[strings.ToLower(key)] {
			continue
		}
		text, ok := value.(string)
		if !ok {
			continue
		}
		command := normalizeBlockText(text)
		if command == "" {
			continue
		}
		for argsKey, argsValue := range record {
			if !argumentKeys[strings.ToLower(argsKey)] {
				continue
			}
			args := getStringArray(argsValue)
			if len(args) > 0 {
				return command + " " + strings.Join(args, " ")
			}
		}
		return command
	}
	return ""
}

// visitedSet is the Go stand-in for the TS `Set<object>` cycle guard: a map or
// a slice is unhashable in Go, so identity is tracked by the container's own
// address (reflect's Pointer), which is exactly the reference identity
// `seen.has(value)` asks about. Two structurally equal but distinct objects have
// distinct addresses and are both walked, as in the TS.
type visitedSet map[uintptr]bool

// markVisited reports whether value was already visited, and records it.
func markVisited(seen visitedSet, value any) bool {
	ptr, ok := identityPointer(value)
	if !ok {
		return false
	}
	if seen[ptr] {
		return true
	}
	seen[ptr] = true
	return false
}

// identityPointer returns the address of the map header or slice data of value.
func identityPointer(value any) (uintptr, bool) {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Map, reflect.Slice:
		return reflected.Pointer(), true
	}
	return 0, false
}

func findFirstCommand(value any, seen visitedSet, allowBareString bool) string {
	if text, ok := value.(string); ok {
		if !allowBareString {
			return ""
		}
		command := normalizeBlockText(text)
		if command != "" {
			return command
		}
		return ""
	}
	if items, ok := value.([]any); ok {
		if markVisited(seen, items) {
			return ""
		}
		for _, item := range items {
			if nested := findFirstCommand(item, seen, false); nested != "" {
				return nested
			}
		}
		return ""
	}
	record, ok := value.(map[string]any)
	if !ok || markVisited(seen, record) {
		return ""
	}
	if direct := getCommandFromRecord(record); direct != "" {
		return direct
	}
	for _, key := range []string{"rawInput", "input", "params", "toolCall"} {
		nested, has := record[key]
		if !has {
			continue
		}
		allowNested := key == "rawInput" || key == "input" || key == "params"
		if command := findFirstCommand(nested, seen, allowNested); command != "" {
			return command
		}
	}
	for _, nested := range record {
		if isJSONRecord(nested) {
			if command := findFirstCommand(nested, seen, false); command != "" {
				return command
			}
			continue
		}
		if _, ok := nested.([]any); ok {
			if command := findFirstCommand(nested, seen, false); command != "" {
				return command
			}
		}
	}
	return ""
}

func pushUniquePath(paths []string, value string) []string {
	normalized := normalizeInlineText(value)
	if normalized == "" {
		return paths
	}
	for _, existing := range paths {
		if existing == normalized {
			return paths
		}
	}
	return append(paths, normalized)
}

// extractPathsFromCandidate ports extractPathsFromCandidate.
func extractPathsFromCandidate(value any, paths []string, seen visitedSet) []string {
	if len(paths) >= maxPermissionFilePaths {
		return paths
	}
	if text, ok := value.(string); ok {
		return pushUniquePath(paths, text)
	}
	if items, ok := value.([]any); ok {
		if markVisited(seen, items) {
			return paths
		}
		for _, item := range items {
			paths = extractPathsFromCandidate(item, paths, seen)
			if len(paths) >= maxPermissionFilePaths {
				return paths
			}
		}
		return paths
	}
	record, ok := value.(map[string]any)
	if !ok || markVisited(seen, record) {
		return paths
	}
	if text, ok := record["path"].(string); ok {
		paths = pushUniquePath(paths, text)
		if len(paths) >= maxPermissionFilePaths {
			return paths
		}
	}
	for _, nested := range record {
		paths = extractPathsFromCandidate(nested, paths, seen)
		if len(paths) >= maxPermissionFilePaths {
			return paths
		}
	}
	return paths
}

// PermissionRequest is the subset of NexPermissionRequest the preview reads.
type PermissionRequest struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Raw         any    `json:"raw,omitempty"`
}

// GetPermissionRequestPreview ports getPermissionRequestPreview. raw accepts a
// decoded JSON value; callers holding RawMessage decode it first.
func GetPermissionRequestPreview(request PermissionRequest) PermissionPreview {
	filePaths := collectFilePaths(request.Raw, nil, visitedSet{})
	title := normalizeInlineText(firstNonEmptyString(request.Title, request.Description, request.Kind))
	if title == "" {
		title = "permission"
	}
	command := findFirstCommand(readPermissionInputSource(request.Raw), visitedSet{}, true)
	var commandPtr *string
	if command != "" {
		commandPtr = &command
	}
	fileChanges := collectFileChanges(request.Raw, nil, visitedSet{})
	if fileChanges == nil {
		fileChanges = []PermissionFileChange{}
	}
	var fileChange *PermissionFileChange
	if len(fileChanges) == 1 {
		only := fileChanges[0]
		fileChange = &only
	}
	scope := ScopeGeneric
	switch {
	case command != "":
		scope = ScopeCommand
	case len(filePaths) > 0:
		scope = ScopeFile
	}
	if filePaths == nil {
		filePaths = []string{}
	}
	return PermissionPreview{
		Title:       title,
		Command:     commandPtr,
		FilePaths:   filePaths,
		Scope:       scope,
		FileChange:  fileChange,
		FileChanges: fileChanges,
	}
}

// DecodeJSONValue turns a decoded-any tree that arrived as RawMessage into the
// map[string]any the collectors expect.
func DecodeJSONValue(raw json.RawMessage) (any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// jsonNumberString is String(number) for the values encoding/json decodes into
// float64: integers print without a fraction.
func jsonNumberString(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e21 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}
