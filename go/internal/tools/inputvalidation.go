package tools

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// RuntimeInputValidationIssue mirrors the zod issue object recorded by
// prepareInitialToolExecutionInput. Only the fields the projection reads are kept.
type RuntimeInputValidationIssue struct {
	Code        string         `json:"code"`
	Path        []any          `json:"path"`
	Message     string         `json:"message"`
	Expected    string         `json:"expected"`
	Received    string         `json:"received"`
	Type        string         `json:"type"`
	Validation  string         `json:"validation"`
	Minimum     *float64       `json:"minimum"`
	Maximum     *float64       `json:"maximum"`
	Inclusive   *bool          `json:"inclusive"`
	Exact       *bool          `json:"exact,omitempty"`
	UnionErrors []runtimeUnion `json:"unionErrors"`
}

type runtimeUnion struct {
	Issues []RuntimeInputValidationIssue `json:"issues"`
}

// MarshalJSON emits only the keys the TypeScript zod issue actually carries, so
// the recorded projection compares equal instead of carrying Go zero values.
func (i RuntimeInputValidationIssue) MarshalJSON() ([]byte, error) {
	out := map[string]any{"code": i.Code, "path": i.Path, "message": i.Message}
	if i.Expected != "" {
		out["expected"] = i.Expected
	}
	if i.Received != "" {
		out["received"] = i.Received
	}
	if i.Type != "" {
		out["type"] = i.Type
	}
	if i.Validation != "" {
		out["validation"] = i.Validation
	}
	if i.Minimum != nil {
		out["minimum"] = *i.Minimum
	}
	if i.Maximum != nil {
		out["maximum"] = *i.Maximum
	}
	if i.Inclusive != nil {
		out["inclusive"] = *i.Inclusive
	}
	// too_small/too_big always carry `exact` in the recorded zod issue.
	if i.Exact != nil {
		out["exact"] = *i.Exact
	}
	if len(i.UnionErrors) > 0 {
		out["unionErrors"] = i.UnionErrors
	}
	return json.Marshal(out)
}

// CreateInitialInputValidationModelContent mirrors
// createInitialInputValidationModelContent(): the <tool_use_error> text the model
// receives for a first-time input-schema failure.
func CreateInitialInputValidationModelContent(toolName string, inputSchema map[string]any, jsonIssues []ValidationIssue, runtimeIssues []RuntimeInputValidationIssue) string {
	issues := projectInitialModelValidationIssues(inputSchema, jsonIssues, runtimeIssues)
	return "<tool_use_error>InputValidationError: " + formatToolInputValidationError(toolName, issues) + "</tool_use_error>"
}

func formatToolInputValidationError(toolName string, issues []ValidationIssue) string {
	var missing, unexpected, wrongTypes []string
	for _, issue := range issues {
		if issue.Code == "invalid_type" && strings.Contains(issue.Message, "received undefined") {
			missing = append(missing, formatValidationPath(issue.Path))
			continue
		}
		if issue.Code == "unrecognized_keys" {
			unexpected = append(unexpected, issue.Keys...)
			continue
		}
		if issue.Code == "invalid_type" {
			expected, _ := expectedTypeForIssue(issue)
			wrongTypes = append(wrongTypes, "The parameter `"+formatValidationPath(issue.Path)+"` type is expected as `"+expected+"` but provided as `"+receivedFromMessage(issue)+"`")
		}
	}
	var lines []string
	for _, p := range missing {
		lines = append(lines, "The required parameter `"+p+"` is missing")
	}
	for _, p := range unexpected {
		lines = append(lines, "An unexpected parameter `"+p+"` was provided")
	}
	lines = append(lines, wrongTypes...)
	if len(lines) > 0 {
		label := "issue"
		if len(lines) > 1 {
			label = "issues"
		}
		return toolName + " failed due to the following " + label + ":\n" + strings.Join(lines, "\n")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(issues); err != nil {
		return "[]"
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func expectedTypeForIssue(issue ValidationIssue) (string, string) {
	if issue.Expected == "int" {
		return "int", "safeint"
	}
	if issue.Expected != "" {
		return issue.Expected, ""
	}
	return "unknown", ""
}

var receivedPattern = regexp.MustCompile(`received (\w+)`)

// receivedFromMessage mirrors the formatter's `issue.message.match(/received (\w+)/)`.
func receivedFromMessage(issue ValidationIssue) string {
	if m := receivedPattern.FindStringSubmatch(issue.Message); m != nil {
		return m[1]
	}
	return "unknown"
}

// formatValidationPath mirrors formatValidationPath().
func formatValidationPath(path []any) string {
	if len(path) == 0 {
		return ""
	}
	var b strings.Builder
	for i, seg := range path {
		switch s := seg.(type) {
		case float64:
			b.WriteString("[" + formatNumber(s) + "]")
		case int:
			b.WriteString("[" + formatNumber(float64(s)) + "]")
		case string:
			if i == 0 {
				b.WriteString(s)
			} else {
				b.WriteString("." + s)
			}
		}
	}
	return b.String()
}
