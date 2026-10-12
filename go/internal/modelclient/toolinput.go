package modelclient

import (
	"encoding/json"
	"strings"
)

// NullInput represents an explicit JSON null input (distinct from Go nil, which
// means the JS undefined case in NormalizeToolInput).
type NullInput struct{}

// ToolInputStatus describes how NormalizeToolInput recovered from bad input, for
// logging parity with the Node warnAndRecoverMalformedToolInput path.
type ToolInputStatus struct {
	// Recovered is true when the input was malformed (or null) and became {}.
	Recovered bool
	// ParseErrorType is the JS error name the Node code would have reported
	// ("TypeError" for null, "SyntaxError" for bad JSON, "" when not applicable).
	ParseErrorType string
	// InputType is "null" when the failure was a literal null.
	InputType string
	// InputLength is the raw string length when the failure was bad JSON.
	InputLength int
}

// NormalizeToolInput mirrors normalizeModelToolInput:
//
//   - undefined (Go nil) -> {} with no recovery;
//   - the JSON literal null (the string "null", or an explicit Null input) -> {}
//     with a TypeError recovery;
//   - non-string (already-parsed object/array/number/bool) -> returned unchanged;
//   - "" -> {};
//   - a string: parse the JSON (after stripping a leading U+FEFF). If it parses to
//     a non-null value, that value is returned. If it parses to null or fails to
//     parse, {} is returned and the recovery is reported.
//
// Because Go's nil cannot distinguish undefined from null, callers that mean the
// JSON literal null must pass NullInput.
func NormalizeToolInput(input any) (any, ToolInputStatus) {
	if input == nil {
		return map[string]any{}, ToolInputStatus{}
	}
	if _, ok := input.(NullInput); ok {
		return map[string]any{}, ToolInputStatus{Recovered: true, ParseErrorType: "TypeError", InputType: "null"}
	}
	s, ok := input.(string)
	if !ok {
		return input, ToolInputStatus{}
	}
	if s == "" {
		return map[string]any{}, ToolInputStatus{}
	}
	trimmed := strings.TrimPrefix(s, "\ufeff")
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return map[string]any{}, ToolInputStatus{
			Recovered:      true,
			ParseErrorType: "SyntaxError",
			InputLength:    len(s),
		}
	}
	// trailing garbage after the first JSON value is a parse error in JS too
	if dec.More() {
		return map[string]any{}, ToolInputStatus{
			Recovered:      true,
			ParseErrorType: "SyntaxError",
			InputLength:    len(s),
		}
	}
	if value == nil {
		return map[string]any{}, ToolInputStatus{
			Recovered:      true,
			ParseErrorType: "TypeError",
			InputType:      "null",
			InputLength:    len(s),
		}
	}
	return value, ToolInputStatus{}
}

// NormalizeToolName mirrors normalizeModelToolName:
//
//   - a non-empty trimmed string -> the trimmed name;
//   - a whitespace-only string that is client-executed (providerExecuted != true)
//     and has a closable tool-call id -> the original (untrimmed) value;
//   - otherwise -> an error (invalid_model_response).
func NormalizeToolName(value any, providerExecuted bool, toolCallID string) (string, error) {
	if s, ok := value.(string); ok {
		trimmed := strings.TrimSpace(s)
		if trimmed != "" {
			return trimmed, nil
		}
		if !providerExecuted && strings.TrimSpace(toolCallID) != "" {
			return s, nil
		}
	}
	return "", &DecodeError{
		Kind:    ErrInvalidResponse,
		Message: "Model returned an invalid tool call: tool name is empty.",
	}
}
