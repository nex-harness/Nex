package tools

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ---- projectInitialModelValidationIssues (subset sufficient for these tools) ----

func projectInitialModelValidationIssues(inputSchema map[string]any, jsonIssues []ValidationIssue, runtimeIssues []RuntimeInputValidationIssue) []ValidationIssue {
	if len(runtimeIssues) == 0 {
		return append([]ValidationIssue(nil), jsonIssues...)
	}
	expandedRuntime := expandRuntimeIssues(runtimeIssues, jsonIssues)
	matchedRuntime := map[int]bool{}
	matchedJSON := map[int]ValidationIssue{}
	var jsonProjection []ValidationIssue
	seenIdentity := map[string]bool{}
	for _, jsonIssue := range jsonIssues {
		matchingIndex := -1
		for i, ri := range expandedRuntime {
			if matchedRuntime[i] {
				continue
			}
			if runtimeIssueMatchesJSONIssue(ri, jsonIssue) {
				matchingIndex = i
				break
			}
		}
		if matchingIndex < 0 && !isProviderOnlyStructuralIssue(jsonIssue, inputSchema) {
			continue
		}
		if matchingIndex >= 0 {
			matchedRuntime[matchingIndex] = true
			matchedJSON[matchingIndex] = jsonIssue
		}
		id := toolInputIssueIdentity(jsonIssue)
		if seenIdentity[id] {
			continue
		}
		seenIdentity[id] = true
		jsonProjection = append(jsonProjection, jsonIssue)
	}
	for _, issue := range jsonProjection {
		if isParameterFormatterIssue(issue) {
			return jsonProjection
		}
	}
	var runtimeProjection []ValidationIssue
	seenRuntime := map[string]bool{}
	for i, ri := range expandedRuntime {
		var issue ValidationIssue
		if ji, ok := matchedJSON[i]; ok {
			issue = ji
		} else {
			canonical, ok := canonicalizeRuntimeOnlyIssue(ri, inputSchema)
			if !ok {
				continue
			}
			issue = canonical
		}
		id := toolInputIssueIdentity(issue)
		if seenRuntime[id] {
			continue
		}
		seenRuntime[id] = true
		runtimeProjection = append(runtimeProjection, issue)
	}
	if len(runtimeProjection) > 0 {
		return orderRuntimeFallbackIssues(runtimeProjection)
	}
	return append([]ValidationIssue(nil), jsonIssues...)
}

func isParameterFormatterIssue(issue ValidationIssue) bool {
	return issue.Code == "invalid_type" || issue.Code == "unrecognized_keys"
}

func orderRuntimeFallbackIssues(issues []ValidationIssue) []ValidationIssue {
	ordered := append([]ValidationIssue(nil), issues...)
	for i := len(ordered) - 1; i >= 0; i-- {
		issue := ordered[i]
		if !isArrayBoundIssue(issue) {
			continue
		}
		lastDescendant := -1
		for j := len(ordered) - 1; j > i; j-- {
			if isStrictDescendantPath(ordered[j].Path, issue.Path) {
				lastDescendant = j
				break
			}
		}
		if lastDescendant < 0 {
			continue
		}
		ordered = append(ordered[:i], ordered[i+1:]...)
		ordered = append(ordered[:lastDescendant], append([]ValidationIssue{issue}, ordered[lastDescendant:]...)...)
	}
	return ordered
}

func isArrayBoundIssue(issue ValidationIssue) bool {
	return (issue.Code == "too_big" || issue.Code == "too_small") && issue.Origin == "array"
}

func isStrictDescendantPath(candidate, parent []any) bool {
	if len(candidate) <= len(parent) {
		return false
	}
	for i, seg := range parent {
		if !pathSegmentEqual(candidate[i], seg) {
			return false
		}
	}
	return true
}

func pathSegmentEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func expandRuntimeIssues(runtimeIssues []RuntimeInputValidationIssue, jsonIssues []ValidationIssue) []RuntimeInputValidationIssue {
	var expanded []RuntimeInputValidationIssue
	for _, issue := range runtimeIssues {
		hasJSONUnion := false
		for _, ji := range jsonIssues {
			if runtimeIssueMatchesJSONIssue(issue, ji) {
				hasJSONUnion = true
				break
			}
		}
		var unionIssues []RuntimeInputValidationIssue
		if !hasJSONUnion {
			unionIssues = readRuntimeUnionIssues(issue)
		}
		if len(unionIssues) > 0 {
			expanded = append(expanded, expandRuntimeIssues(unionIssues, jsonIssues)...)
		} else {
			expanded = append(expanded, issue)
		}
	}
	return expanded
}

func readRuntimeUnionIssues(issue RuntimeInputValidationIssue) []RuntimeInputValidationIssue {
	if issue.Code != "invalid_union" {
		return nil
	}
	var out []RuntimeInputValidationIssue
	for _, ue := range issue.UnionErrors {
		out = append(out, ue.Issues...)
	}
	return out
}

func runtimeIssueMatchesJSONIssue(ri RuntimeInputValidationIssue, ji ValidationIssue) bool {
	if !sameValidationPath(ri.Path, ji.Path) {
		return false
	}
	code := normalizeRuntimeIssueCode(ri.Code)
	if code == ji.Code {
		return true
	}
	return ri.Code == "invalid_type" && ri.Received == "undefined" && ji.Code == "invalid_value"
}

func canonicalizeRuntimeOnlyIssue(issue RuntimeInputValidationIssue, inputSchema map[string]any) (ValidationIssue, bool) {
	path := issue.Path
	if path == nil {
		return ValidationIssue{}, false
	}
	switch issue.Code {
	case "custom":
		return createCustomIssue(issue.Message, path), true
	case "invalid_string":
		return canonicalizeRuntimeStringIssue(issue, path, inputSchema)
	case "too_big":
		if origin := runtimeIssueOrigin(issue); origin != "" && issue.Maximum != nil {
			inclusive := issue.Inclusive == nil || *issue.Inclusive
			return tooBigWithOptions(origin, int(*issue.Maximum), path, exactValue(issue.Exact), inclusive), true
		}
	case "too_small":
		if origin := runtimeIssueOrigin(issue); origin != "" && issue.Minimum != nil {
			inclusive := issue.Inclusive == nil || *issue.Inclusive
			return tooSmallWithOptions(origin, int(*issue.Minimum), path, exactValue(issue.Exact), inclusive), true
		}
	}
	return ValidationIssue{}, false
}

func canonicalizeRuntimeStringIssue(issue RuntimeInputValidationIssue, path []any, inputSchema map[string]any) (ValidationIssue, bool) {
	if issue.Validation == "url" {
		message := issue.Message
		if message == "Invalid url" {
			message = "Invalid URL"
		}
		return ValidationIssue{Code: "invalid_format", Origin: "string", Format: "url", Path: clonePath(path), Message: message}, true
	}
	if issue.Validation != "regex" {
		return ValidationIssue{}, false
	}
	schemaNode := readSchemaAtPath(inputSchema, path)
	pattern := ""
	if schemaNode != nil {
		if p, ok := schemaNode["pattern"].(string); ok {
			pattern = "/" + p + "/"
		}
	}
	message := issue.Message
	if message == "Invalid" && pattern != "" {
		message = "Invalid string: must match pattern " + pattern
	}
	out := ValidationIssue{Code: "invalid_format", Origin: "string", Format: "regex", Path: clonePath(path), Message: message}
	if pattern != "" {
		out.Received = pattern
	}
	return out, true
}

func tooSmallWithOptions(origin string, minimum int, path []any, exact, inclusive bool) ValidationIssue {
	m := float64(minimum)
	return ValidationIssue{Code: "too_small", Origin: origin, Minimum: &m, Inclusive: &inclusive, Exact: exact, Path: clonePath(path), Message: formatSizeIssueMessage("too_small", origin, minimum, inclusive)}
}

func tooBigWithOptions(origin string, maximum int, path []any, exact, inclusive bool) ValidationIssue {
	m := float64(maximum)
	return ValidationIssue{Code: "too_big", Origin: origin, Maximum: &m, Inclusive: &inclusive, Exact: exact, Path: clonePath(path), Message: formatSizeIssueMessage("too_big", origin, maximum, inclusive)}
}

func isProviderOnlyStructuralIssue(issue ValidationIssue, inputSchema map[string]any) bool {
	if issue.Code == "unrecognized_keys" {
		return true
	}
	if !isMissingTypeIssue(issue) {
		return false
	}
	schemaNode := readSchemaAtPath(inputSchema, issue.Path)
	if schemaNode == nil {
		return true
	}
	_, hasDefault := schemaNode["default"]
	return !hasDefault
}

func isMissingTypeIssue(issue ValidationIssue) bool {
	return issue.Code == "invalid_type" && strings.Contains(issue.Message, "received undefined")
}

func runtimeIssueOrigin(issue RuntimeInputValidationIssue) string {
	switch issue.Type {
	case "array", "number", "string":
		return issue.Type
	}
	return ""
}

func normalizeRuntimeIssueCode(code string) string {
	switch code {
	case "invalid_enum_value", "invalid_literal":
		return "invalid_value"
	case "invalid_string":
		return "invalid_format"
	}
	return code
}

func sameValidationPath(a []any, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !pathSegmentEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func toolInputIssueIdentity(issue ValidationIssue) string {
	p, _ := json.Marshal(issue.Path)
	return issue.Code + ":" + string(p) + ":" + issue.Message
}

func readSchemaAtPath(schema map[string]any, path []any) map[string]any {
	var current any = schema
	for _, seg := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		if _, isNum := seg.(float64); isNum {
			current = m["items"]
			continue
		}
		props, ok := m["properties"].(map[string]any)
		if !ok {
			return nil
		}
		current = props[fmtSprint(seg)]
	}
	if m, ok := current.(map[string]any); ok {
		return m
	}
	return nil
}

var _ = regexp.MustCompile

func fmtSprint(v any) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(sprint(v)), `"`))
}

func sprint(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func exactValue(e *bool) bool {
	return e != nil && *e
}
