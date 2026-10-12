package tools

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

var pdfRangePattern = regexp.MustCompile(`^(\d+)(?:-(\d+))?$`)

// PreparedInput mirrors PreparedInitialToolExecutionInput.
type PreparedInput struct {
	Input                   any
	RuntimeValidationIssues []RuntimeInputValidationIssue
}

// PrepareInitialInput mirrors prepareInitialToolExecutionInput() for Read, Write
// and Edit: normalize a top-level JSON string, then run the tool's runtime schema
// (a small zod-equivalent). On success the parsed data replaces the input; on
// failure the raw normalized input plus the runtime issues are returned.
func PrepareInitialInput(toolName string, input any) PreparedInput {
	normalized := normalizeTopLevelJSONString(input)
	parsed, issues, ok := parseRuntimeInput(toolName, normalized)
	if !ok {
		return PreparedInput{Input: normalized, RuntimeValidationIssues: issues}
	}
	return PreparedInput{Input: parsed}
}

func normalizeTopLevelJSONString(input any) any {
	s, ok := input.(string)
	if !ok {
		return input
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return input
	}
	return out
}

func parseRuntimeInput(toolName string, input any) (any, []RuntimeInputValidationIssue, bool) {
	record, ok := input.(map[string]any)
	if !ok {
		return nil, []RuntimeInputValidationIssue{{
			Code:     "invalid_type",
			Expected: "object",
			Received: runtimeReceivedName(input),
			Path:     []any{},
			Message:  "Expected object, received " + runtimeReceivedName(input),
		}}, false
	}

	switch toolName {
	case "Read":
		return parseReadRuntime(record)
	case "Write":
		return parseWriteRuntime(record)
	case "Edit":
		return parseEditRuntime(record)
	}
	return record, nil, true
}

func parseReadRuntime(record map[string]any) (any, []RuntimeInputValidationIssue, bool) {
	var issues []RuntimeInputValidationIssue
	fp, ok := record["file_path"].(string)
	if !ok {
		if record["file_path"] == nil {
			issues = append(issues, RuntimeInputValidationIssue{Code: "invalid_type", Expected: "string", Received: "undefined", Path: []any{"file_path"}, Message: "Required"})
		} else {
			issues = append(issues, RuntimeInputValidationIssue{Code: "invalid_type", Expected: "string", Received: runtimeReceivedName(record["file_path"]), Path: []any{"file_path"}, Message: "Expected string, received " + runtimeReceivedName(record["file_path"])})
		}
	}
	if v, ok := record["offset"]; ok && v != nil {
		issues = append(issues, readNumberIssues(v, "offset", false)...)
	}
	if v, ok := record["limit"]; ok && v != nil {
		issues = append(issues, readNumberIssues(v, "limit", true)...)
	}
	if len(issues) > 0 {
		return record, issues, false
	}
	// file_path-level superRefine issues (device/binary/pages).
	if preflight := readSuperRefineIssues(fp, record["pages"]); len(preflight) > 0 {
		return record, preflight, false
	}
	return stripKeys(record, "file_path", "offset", "limit", "pages"), nil, true
}

func readNumberIssues(v any, key string, positive bool) []RuntimeInputValidationIssue {
	f, isNum := asFloat(v)
	if !isNum {
		return []RuntimeInputValidationIssue{{Code: "invalid_type", Expected: "number", Received: runtimeReceivedName(v), Path: []any{key}, Message: "Expected number, received " + runtimeReceivedName(v)}}
	}
	if f != math.Trunc(f) {
		return []RuntimeInputValidationIssue{{Code: "invalid_type", Expected: "integer", Received: "float", Message: "Expected integer, received float", Path: []any{key}}}
	}
	if positive {
		if f <= 0 {
			zero := 0.0
			incl := false
			exact := false
			return []RuntimeInputValidationIssue{{Code: "too_small", Minimum: &zero, Type: "number", Inclusive: &incl, Exact: &exact, Message: "Number must be greater than 0", Path: []any{key}}}
		}
	} else if f < 0 {
		zero := 0.0
		incl := true
		exact := false
		return []RuntimeInputValidationIssue{{Code: "too_small", Minimum: &zero, Type: "number", Inclusive: &incl, Exact: &exact, Message: "Number must be greater than or equal to 0", Path: []any{key}}}
	}
	return nil
}

func readSuperRefineIssues(filePath string, pages any) []RuntimeInputValidationIssue {
	lower := strings.ToLower(filePath)
	if blockingDevicePaths[lower] {
		return []RuntimeInputValidationIssue{{Code: "custom", Path: []any{"file_path"}, Message: "Cannot read '" + filePath + "': this device file would block or produce infinite output."}}
	}
	if pagesStr, ok := pages.(string); ok {
		if msg := readPdfPagesValidationFailure(filePath, pagesStr); msg != "" {
			return []RuntimeInputValidationIssue{{Code: "custom", Path: []any{"pages"}, Message: msg}}
		}
		return nil
	}
	ext := readFileExtension(lower)
	if ext != "" && unsupportedReadBinaryExtensions[ext] {
		return []RuntimeInputValidationIssue{{Code: "custom", Path: []any{"file_path"}, Message: "This tool cannot read binary files. The file appears to be a binary " + ext + " file. Please use appropriate tools for binary file analysis."}}
	}
	return nil
}

func parseWriteRuntime(record map[string]any) (any, []RuntimeInputValidationIssue, bool) {
	var issues []RuntimeInputValidationIssue
	issues = append(issues, stringIssues(record, "file_path")...)
	issues = append(issues, stringIssues(record, "content")...)
	if len(issues) > 0 {
		return record, issues, false
	}
	return stripKeys(record, "file_path", "content"), nil, true
}

func parseEditRuntime(record map[string]any) (any, []RuntimeInputValidationIssue, bool) {
	var issues []RuntimeInputValidationIssue
	// replace_all is preprocessed before validation, so coercion applies even when
	// other fields fail (zod runs every field parser).
	coerced := coerceSemanticBool(record["replace_all"])
	issues = append(issues, stringIssues(record, "file_path")...)
	issues = append(issues, stringIssues(record, "old_string")...)
	issues = append(issues, stringIssues(record, "new_string")...)
	if len(issues) > 0 {
		return record, issues, false
	}
	stripped := stripKeys(record, "file_path", "old_string", "new_string", "replace_all")
	if coerced != nil {
		stripped["replace_all"] = *coerced
	} else if _, ok := stripped["replace_all"]; !ok {
		stripped["replace_all"] = false
	}
	return stripped, nil, true
}

// coerceSemanticBool mirrors the semanticBoolean() preprocessor.
func coerceSemanticBool(v any) *bool {
	switch n := v.(type) {
	case bool:
		return &n
	case float64:
		if n == 1 {
			b := true
			return &b
		}
		if n == 0 {
			b := false
			return &b
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(n)) {
		case "true", "1", "yes", "y", "on":
			b := true
			return &b
		case "false", "0", "no", "n", "off":
			b := false
			return &b
		}
	}
	return nil
}

// stripKeys mirrors zod's default object behavior: unknown keys are dropped.
func stripKeys(record map[string]any, allowed ...string) map[string]any {
	set := map[string]bool{}
	for _, k := range allowed {
		set[k] = true
	}
	out := map[string]any{}
	for k, v := range record {
		if set[k] {
			out[k] = v
		}
	}
	return out
}

func stringIssues(record map[string]any, key string) []RuntimeInputValidationIssue {
	v, present := record[key]
	if !present || v == nil {
		return []RuntimeInputValidationIssue{{Code: "invalid_type", Expected: "string", Received: "undefined", Path: []any{key}, Message: "Required"}}
	}
	if _, ok := v.(string); !ok {
		return []RuntimeInputValidationIssue{{Code: "invalid_type", Expected: "string", Received: runtimeReceivedName(v), Path: []any{key}, Message: "Expected string, received " + runtimeReceivedName(v)}}
	}
	return nil
}

func runtimeReceivedName(v any) string {
	switch v.(type) {
	case nil:
		return "undefined"
	case string:
		return "string"
	case bool:
		return "boolean"
	// json.Number is what a UseNumber decoder produces for the model's raw
	// argument JSON; JS has one number type, so it maps to "number" too.
	case json.Number, float64, int:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "object"
	}
}

// readPdfPagesValidationFailure mirrors getReadPdfPagesValidationFailure().
func readPdfPagesValidationFailure(filePath, pages string) string {
	if !strings.HasSuffix(strings.ToLower(filePath), ".pdf") {
		return ""
	}
	const maxPagesPerRequest = 20
	normalized := strings.TrimSpace(pages)
	first, last, ok := parseReadPdfPageRange(normalized)
	if !ok {
		return "Invalid pages parameter: \"" + pages + "\". Use formats like \"1-5\", \"3\", or \"10-20\". Pages are 1-indexed."
	}
	count := last - first + 1
	if last == math.MaxInt32 && first >= 1 {
		// Open range: parseReadPdfPageRange returns last=+Inf; count is maxProtected+1.
		count = maxPagesPerRequest + 1
	}
	if count > maxPagesPerRequest {
		return "Page range \"" + pages + "\" exceeds maximum of " + itoaInt(maxPagesPerRequest) + " pages per request. Please use a smaller range."
	}
	return ""
}

// parseReadPdfPageRange mirrors parseReadPdfPageRange(); last == math.MaxInt32
// represents Number.POSITIVE_INFINITY for the open "N-" form.
func parseReadPdfPageRange(value string) (first, last int, ok bool) {
	if strings.HasSuffix(value, "-") && isPositiveInt(strings.TrimSuffix(value, "-")) {
		n, _ := atoiSafe(strings.TrimSuffix(value, "-"))
		if n >= 1 {
			return n, math.MaxInt32, true
		}
		return 0, 0, false
	}
	m := pdfRangePattern.FindStringSubmatch(value)
	if m == nil {
		return 0, 0, false
	}
	first, ok = atoiSafe(m[1])
	if !ok {
		return 0, 0, false
	}
	last = first
	if m[2] != "" {
		last, ok = atoiSafe(m[2])
		if !ok {
			return 0, 0, false
		}
	}
	if first < 1 || last < first {
		return 0, 0, false
	}
	return first, last, true
}

func atoiSafe(s string) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		if n > (1<<53)/10 {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func isValidPdfPages(pages string) bool {
	_, _, ok := parseReadPdfPageRange(strings.TrimSpace(pages))
	return ok || strings.TrimSpace(pages) == ""
}

func isPositiveInt(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
