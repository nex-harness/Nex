package runtime

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/tooldefs"
	"github.com/tylinux/nex/go/internal/tools"
)

// This file ports the pre-handler half of executeToolCallImpl
// (tool/executor/call-runner.ts): the registry lookup and the initial
// input-schema validation, both of which run before ToolCallStarted and both of
// which end in a tool_call_error instead of a tool_call_result.

// toolInputSchema returns the JSON schema the model was shown for a tool, read
// from the same tooldefs the request body carries, or nil when the tool is not
// part of the default set.
func toolInputSchema(name string) map[string]any {
	defs, err := tooldefs.Default()
	if err != nil {
		return nil
	}
	for _, d := range defs {
		if d.Name != name {
			continue
		}
		var entry struct {
			Function struct {
				Parameters map[string]any `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(d.JSON, &entry); err != nil {
			return nil
		}
		return entry.Function.Parameters
	}
	return nil
}

// registeredTool reports whether the tool registry knows the name (call-runner.ts
// resolves `entry` from deps.registry before anything else happens).
func registeredTool(name string) bool {
	_, ok := registryEntry(name)
	return ok
}

// initialInputValidation ports prepareInitialToolExecutionInput +
// validateInitialModelToolInput. It returns a non-nil failure when the model's
// arguments do not satisfy the tool's input schema; the call never reaches
// ToolCallStarted.
//
// Order matters and mirrors Node: the runtime zod schema runs first (a top-level
// JSON string is parsed and unknown keys are stripped), and the JSON schema
// check then runs on the *normalized* input, with the runtime issues handed to
// the model-content projector so the <tool_use_error> text names the real cause.
func initialInputValidation(call modelclient.ToolCall) *toolFailure {
	if len(call.Arguments) > 0 || call.Input != nil {
		if f := validateAgainstInputSchema(call.Name, call.Input); f != nil {
			return f
		}
	}
	return nil
}

// validateAgainstInputSchema runs the JSON schema check and, on failure, builds
// the CoreError whose message is fixed and whose modelContent is the projected
// InputValidationError text.
func validateAgainstInputSchema(toolName string, input any) *toolFailure {
	schema := toolInputSchema(toolName)
	if schema == nil {
		return nil
	}
	prepared := tools.PrepareInitialInput(toolName, input)
	validation := tools.ValidateJSONSchemaValue(prepared.Input, schema)
	if validation.Valid {
		return nil
	}
	modelContent := tools.CreateInitialInputValidationModelContent(
		toolName, schema, validation.Issues, prepared.RuntimeValidationIssues)
	failure := inputValidationFailure(modelContent)
	return &failure
}
