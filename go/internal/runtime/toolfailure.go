package runtime

import (
	"encoding/json"
	"strings"
	"unicode/utf16"

	"github.com/tylinux/nex/go/internal/tools"
)

// This file ports the tool-failure lifecycle of the Node call runner:
// `createErrorResult` / `createToolHandlerFailureError` (tool/executor/errors.ts)
// plus the pre-handler early exits in `executeToolCallImpl` (registry miss and
// initial input-schema validation, call-runner.ts lines 124-205). Both exit
// paths publish a `tool_call_error` session event, never a `tool_call_result`.

const (
	// CoreErrorType values the failure payload can carry (contracts errors/index.ts).
	coreErrorToolExecutionFailed = "tool_execution_failed"
	coreErrorToolNotFound        = "tool_not_found"

	// inputValidationFailureMessage is the message of the CoreError
	// createInputValidationError builds (executor/validation.ts).
	inputValidationFailureMessage = "Tool input failed inputSchema validation"
)

// toolFailureKind selects which of createErrorResult's two branches produced the
// payload: a ToolHandlerFailure returned by the handler, or a thrown CoreError.
type toolFailureKind int

const (
	// failureHandler is a ToolHandlerFailure ({result:false, errorCode, message}):
	// the model sees `<tool_use_error>message</tool_use_error>` and the error
	// payload carries `code: String(errorCode)` next to the CoreError type.
	failureHandler toolFailureKind = iota
	// failureCore is a thrown CoreError: the model sees the bare projected
	// message and the payload carries `code` only when projectExecutionErrorPayload
	// found one (createCoreError always sets code = TYPE.UPPERCASE).
	failureCore
)

// toolFailure is the result of the failure half of a tool call: everything
// createErrorResult builds minus the parts the port does not model (stack,
// startedAt/completedAt, duration).
type toolFailure struct {
	kind    toolFailureKind
	coreTyp string
	// code is the payload's `code` field ("" ⇒ omitted).
	code string
	// message is the projected, whitespace-compacted message the row and the
	// telemetry fact carry.
	message string
	// modelContent is what the model sees as the tool result; "" ⇒ the message.
	modelContent string
}

// coreCode mirrors createCoreError: `error.code = type.toUpperCase()`. projectExecutionErrorPayload
// picks that code up for the payload, so a thrown CoreError without an explicit
// context code reports e.g. "TOOL_EXECUTION_FAILED" in telemetry while the row
// keeps the lower-case type.
func coreCode(coreType string) string { return strings.ToUpper(coreType) }

// toolNotFoundFailure ports the registry-miss early exit (call-runner.ts:124):
// CoreError(ToolNotFound, `Tool not found: <name>`) — the model sees the bare
// message, and the error never reaches ToolCallStarted.
func toolNotFoundFailure(name string) toolFailure {
	return toolFailure{
		kind:         failureCore,
		coreTyp:      coreErrorToolNotFound,
		code:         coreCode(coreErrorToolNotFound),
		message:      sanitizeErrorText("Tool not found: " + name),
		modelContent: "",
	}
}

// inputValidationFailure ports the initial input-schema early exit
// (call-runner.ts:175): CoreError(ToolExecutionFailed, "Tool input failed
// inputSchema validation") whose modelContent is the projected
// <tool_use_error>InputValidationError text.
func inputValidationFailure(modelContent string) toolFailure {
	return toolFailure{
		kind:         failureCore,
		coreTyp:      coreErrorToolExecutionFailed,
		code:         coreCode(coreErrorToolExecutionFailed),
		message:      sanitizeErrorText(inputValidationFailureMessage),
		modelContent: modelContent,
	}
}

// handlerFailure ports createToolHandlerFailureError + createErrorResult's
// handlerFailure branch: the CoreError type is ToolExecutionFailed, the payload
// code is the String()ed numeric errorCode (Edit's "8"/"9"), and the model
// receives `<tool_use_error>message</tool_use_error>`.
func handlerFailure(f *tools.Failure) toolFailure {
	return toolFailure{
		kind:         failureHandler,
		coreTyp:      coreErrorToolExecutionFailed,
		code:         itoa(f.ErrorCode),
		message:      sanitizeErrorText(f.Message),
		modelContent: "<tool_use_error>" + f.Message + "</tool_use_error>",
	}
}

// coreToolFailure is the thrown-CoreError branch used by the Read/Write handlers,
// which raise createCoreError(ToolExecutionFailed, message) instead of returning
// a ToolHandlerFailure: no modelContent wrapper, code = TOOL_EXECUTION_FAILED.
func coreToolFailure(f *tools.Failure) toolFailure {
	return toolFailure{
		kind:         failureCore,
		coreTyp:      coreErrorToolExecutionFailed,
		code:         coreCode(coreErrorToolExecutionFailed),
		message:      sanitizeErrorText(f.Message),
		modelContent: "",
	}
}

// modelText is what the next request carries as this tool's result:
// createErrorResult leaves modelContent undefined for thrown CoreErrors, and
// turn-tools then falls back to error.message (turn-tools.ts:259-262).
func (f toolFailure) modelText() string {
	if f.modelContent != "" {
		return f.modelContent
	}
	return f.message
}

// payload builds the session-event `error` object (ToolExecutionResult.error):
// {type, message, code?, stack} — `stack` is never modelled, so it is omitted.
func (f toolFailure) payload() json.RawMessage {
	return mustJSON(map[string]any{
		"type":    f.coreTyp,
		"message": f.message,
		"code":    f.code,
	})
}

// sanitizeErrorText ports sanitizeText (errors/error-payload.ts): collapse every
// whitespace run to one space, trim, and cap at 500 chars with a "..." tail
// (497 chars + "..."). This is the message the tool row and the telemetry fact
// carry — the model instead receives modelText, which keeps the raw newlines.
func sanitizeErrorText(text string) string {
	compact := strings.Join(strings.Fields(text), " ")
	if compact == "" {
		return ""
	}
	units := utf16.Encode([]rune(compact))
	if len(units) <= 500 {
		return compact
	}
	// String.prototype.slice(0, 497) counts UTF-16 code units, so a surrogate
	// pair straddling the cut is kept whole by the encoder round trip.
	return string(utf16.Decode(units[:497])) + "..."
}
