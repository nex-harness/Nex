package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

type jsonSchemaNode struct {
	Type       string                    `json:"type"`
	Required   []string                  `json:"required"`
	Properties map[string]jsonSchemaNode `json:"properties"`
	Enum       []any                     `json:"enum"`
}

// ValidateToolArguments performs lightweight JSON Schema validation of tool call arguments
// before executing a tool, matching pi-agent's pre-execution validation contract so the model
// receives clear, actionable error feedback on malformed arguments.
func ValidateToolArguments(schemaRaw json.RawMessage, argsRaw json.RawMessage) error {
	if len(schemaRaw) == 0 {
		return nil
	}
	var schema jsonSchemaNode
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil // Don't block execution if a custom plugin supplied an unconventional schema
	}
	if schema.Type != "" && schema.Type != "object" {
		return nil
	}

	raw := argsRaw
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("arguments must be a valid JSON object: %v", err)
	}
	if obj == nil {
		obj = map[string]any{}
	}

	var errs []string
	for _, reqKey := range schema.Required {
		val, exists := obj[reqKey]
		if !exists || val == nil {
			errs = append(errs, fmt.Sprintf("missing required parameter %q", reqKey))
		}
	}

	for propName, propSchema := range schema.Properties {
		val, exists := obj[propName]
		if !exists || val == nil {
			continue
		}
		if propSchema.Type != "" {
			if errMsg := checkJSONType(propName, val, propSchema.Type); errMsg != "" {
				errs = append(errs, errMsg)
			}
		}
		if len(propSchema.Enum) > 0 && !matchesEnum(val, propSchema.Enum) {
			errs = append(errs, fmt.Sprintf("parameter %q value %v is not one of allowed values %v", propName, val, propSchema.Enum))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid tool arguments: %s", strings.Join(errs, "; "))
	}
	return nil
}

func checkJSONType(name string, val any, expected string) string {
	switch expected {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Sprintf("parameter %q must be a string", name)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Sprintf("parameter %q must be a boolean", name)
		}
	case "integer":
		f, ok := val.(float64)
		if !ok || math.Trunc(f) != f {
			return fmt.Sprintf("parameter %q must be an integer", name)
		}
	case "number":
		if _, ok := val.(float64); !ok {
			return fmt.Sprintf("parameter %q must be a number", name)
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return fmt.Sprintf("parameter %q must be an array", name)
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Sprintf("parameter %q must be an object", name)
		}
	}
	return ""
}

func matchesEnum(val any, allowed []any) bool {
	vb, _ := json.Marshal(val)
	vs := string(vb)
	for _, item := range allowed {
		ib, _ := json.Marshal(item)
		if string(ib) == vs {
			return true
		}
	}
	return false
}
