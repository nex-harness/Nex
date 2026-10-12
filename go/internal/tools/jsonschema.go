package tools

import (
	"encoding/json"
	"fmt"
)

// ValidationIssue mirrors ToolInputValidationIssue. Fields are ordered so the
// JSON fallback matches the TypeScript insertion order (the model sees this JSON
// when no parameter-formatter issue applies).
type ValidationIssue struct {
	Code      string              `json:"code"`
	Origin    string              `json:"origin,omitempty"`
	Expected  string              `json:"expected,omitempty"`
	Format    string              `json:"format,omitempty"`
	Received  string              `json:"received,omitempty"`
	Values    []any               `json:"values,omitempty"`
	Keys      []string            `json:"keys,omitempty"`
	Minimum   *float64            `json:"minimum,omitempty"`
	Maximum   *float64            `json:"maximum,omitempty"`
	Inclusive *bool               `json:"inclusive,omitempty"`
	Exact     bool                `json:"exact,omitempty"`
	Errors    [][]ValidationIssue `json:"errors,omitempty"`
	Path      []any               `json:"path"`
	Message   string              `json:"message"`
}

// ValidationResult mirrors JsonSchemaValidationResult.
type ValidationResult struct {
	Valid  bool
	Errors []string
	Issues []ValidationIssue
}

type collector struct {
	errors []string
	issues []ValidationIssue
}

// ValidateJSONSchemaValue mirrors validateJsonSchemaValue().
func ValidateJSONSchemaValue(value any, schema map[string]any) ValidationResult {
	if schema == nil || len(schema) == 0 {
		return ValidationResult{Valid: true}
	}
	c := &collector{}
	validateNode(value, schema, "$", []any{}, c)
	return ValidationResult{Valid: len(c.errors) == 0, Errors: c.errors, Issues: c.issues}
}

func validateNode(value any, schema map[string]any, displayPath string, issuePath []any, c *collector) {
	if oneOf, ok := asSchemaArray(schema["oneOf"]); ok {
		candidates := make([]*collector, len(oneOf))
		for i, cand := range oneOf {
			cc := &collector{}
			validateNode(value, cand, "$", []any{}, cc)
			candidates[i] = cc
		}
		matches := 0
		unionErrors := make([][]ValidationIssue, 0, len(candidates))
		for _, cand := range candidates {
			if len(cand.errors) == 0 {
				matches++
			}
			unionErrors = append(unionErrors, cand.issues)
		}
		if matches != 1 {
			c.errors = append(c.errors, fmt.Sprintf("%s must match exactly one oneOf schema, matched %d", displayPath, matches))
			c.issues = append(c.issues, ValidationIssue{Code: "invalid_union", Errors: unionErrors, Path: clonePath(issuePath), Message: "Invalid input"})
		}
		return
	}

	valueConstraintFailed := false
	if constVal, ok := schema["const"]; ok && !jsonValueEqual(value, constVal) {
		c.errors = append(c.errors, displayPath+" must be "+jsonString(constVal))
		c.issues = append(c.issues, createInvalidValueIssue([]any{constVal}, issuePath))
		valueConstraintFailed = true
	}
	if enumValues, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enumValues {
			if jsonValueEqual(value, e) {
				found = true
				break
			}
		}
		if !found {
			parts := make([]string, len(enumValues))
			for i, e := range enumValues {
				parts[i] = jsonString(e)
			}
			c.errors = append(c.errors, displayPath+" must be one of "+joinComma(parts))
			c.issues = append(c.issues, createInvalidValueIssue(enumValues, issuePath))
			valueConstraintFailed = true
		}
	}

	if typeVal, ok := schema["type"]; ok {
		if !matchesType(value, typeVal) {
			c.errors = append(c.errors, displayPath+" must be "+formatSchemaType(typeVal))
			if !valueConstraintFailed {
				if arr, ok := typeVal.([]any); ok {
					union := make([][]ValidationIssue, len(arr))
					for i, cand := range arr {
						union[i] = []ValidationIssue{createInvalidTypeIssue(value, cand, []any{})}
					}
					c.issues = append(c.issues, ValidationIssue{Code: "invalid_union", Errors: union, Path: clonePath(issuePath), Message: "Invalid input"})
				} else {
					c.issues = append(c.issues, createInvalidTypeIssue(value, typeVal, issuePath))
				}
			}
			return
		}
	}

	switch v := value.(type) {
	case string:
		validateString(v, schema, displayPath, issuePath, c)
	case float64:
		validateNumber(v, schema, displayPath, issuePath, c)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			validateNumber(f, schema, displayPath, issuePath, c)
		}
	case []any:
		validateArray(v, schema, displayPath, issuePath, c)
	case map[string]any:
		validateObject(v, schema, displayPath, issuePath, c)
	}
}

func validateObject(value map[string]any, schema map[string]any, displayPath string, issuePath []any, c *collector) {
	required := stringSlice(schema["required"])
	properties, _ := schema["properties"].(map[string]any)
	requiredSet := map[string]bool{}
	for _, k := range required {
		requiredSet[k] = true
		if _, ok := value[k]; !ok {
			c.errors = append(c.errors, displayPath+"."+k+" is required")
		}
	}
	for key, propertySchema := range properties {
		val, present := value[key]
		if !present {
			if requiredSet[key] {
				ps, _ := propertySchema.(map[string]any)
				c.issues = append(c.issues, createMissingPropertyIssues(ps, appendPath(issuePath, key))...)
			}
			continue
		}
		ps, ok := propertySchema.(map[string]any)
		if !ok {
			continue
		}
		validateNode(val, ps, displayPath+"."+key, appendPath(issuePath, key), c)
	}
	for _, key := range required {
		if _, ok := properties[key]; ok {
			continue
		}
		if _, present := value[key]; !present {
			c.issues = append(c.issues, createInvalidTypeIssue(nil, "unknown", appendPath(issuePath, key)))
		}
	}
	if ap, ok := schema["additionalProperties"].(bool); ok && !ap {
		allowed := map[string]bool{}
		for k := range properties {
			allowed[k] = true
		}
		var unexpected []string
		for k := range value {
			if !allowed[k] {
				c.errors = append(c.errors, displayPath+"."+k+" is not allowed")
				unexpected = append(unexpected, k)
			}
		}
		if len(unexpected) > 0 {
			c.issues = append(c.issues, createUnrecognizedKeysIssue(unexpected, issuePath))
		}
	}
}

func validateArray(value []any, schema map[string]any, displayPath string, issuePath []any, c *collector) {
	var minimumIssue, maximumIssue *ValidationIssue
	if minItems, ok := schema["minItems"].(float64); ok && len(value) < int(minItems) {
		c.errors = append(c.errors, fmt.Sprintf("%s must contain at least %d items", displayPath, int(minItems)))
		issue := createTooSmallIssue("array", int(minItems), issuePath)
		minimumIssue = &issue
	}
	if maxItems, ok := schema["maxItems"].(float64); ok && len(value) > int(maxItems) {
		c.errors = append(c.errors, fmt.Sprintf("%s must contain at most %d items", displayPath, int(maxItems)))
		issue := createTooBigIssue("array", int(maxItems), issuePath)
		maximumIssue = &issue
	}
	if items, ok := schema["items"].(map[string]any); ok {
		for i, item := range value {
			validateNode(item, items, fmt.Sprintf("%s[%d]", displayPath, i), appendPath(issuePath, i), c)
		}
	}
	if minimumIssue != nil {
		c.issues = append(c.issues, *minimumIssue)
	}
	if maximumIssue != nil {
		c.issues = append(c.issues, *maximumIssue)
	}
}

func validateString(value string, schema map[string]any, displayPath string, issuePath []any, c *collector) {
	if minLength, ok := schema["minLength"].(float64); ok && jsStringLength(value) < int(minLength) {
		c.errors = append(c.errors, fmt.Sprintf("%s must be at least %d characters", displayPath, int(minLength)))
		c.issues = append(c.issues, createTooSmallIssue("string", int(minLength), issuePath))
	}
	if maxLength, ok := schema["maxLength"].(float64); ok && jsStringLength(value) > int(maxLength) {
		c.errors = append(c.errors, fmt.Sprintf("%s must be at most %d characters", displayPath, int(maxLength)))
		c.issues = append(c.issues, createTooBigIssue("string", int(maxLength), issuePath))
	}
}

func validateNumber(value float64, schema map[string]any, displayPath string, issuePath []any, c *collector) {
	if minimum, ok := schema["minimum"].(float64); ok && value < minimum {
		c.errors = append(c.errors, fmt.Sprintf("%s must be >= %s", displayPath, formatNumber(minimum)))
		c.issues = append(c.issues, createTooSmallIssue("number", int(minimum), issuePath))
	}
	if maximum, ok := schema["maximum"].(float64); ok && value > maximum {
		c.errors = append(c.errors, fmt.Sprintf("%s must be <= %s", displayPath, formatNumber(maximum)))
		c.issues = append(c.issues, createTooBigIssue("number", int(maximum), issuePath))
	}
}
