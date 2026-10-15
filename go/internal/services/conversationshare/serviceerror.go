// serviceerror.go ports the ConversationShareServiceError half of
// conversationShare.ts: the safe details payload the Renderer receives.
//
// In Go the error is a struct implementing error; the RPC channel copies the
// passthrough fields (kind, code, status, requestId, details) onto an
// *rpc.Error, mirroring how the TS Error subclass's own properties become
// PromiseError passthrough keys.
package conversationshare

import (
	"fmt"
	"regexp"
)

// serviceError is the Go ConversationShareServiceError.
type serviceError struct {
	kind       string
	message    string
	requestID  string
	status     *int64
	code       *int64
	reasonCode string
	issues     []Issue
	// details is the pre-sanitized safe payload (nil when empty).
	details map[string]any
}

var safeRequestIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// errorDetails carries the fields serviceError.New accepts.
type errorDetails struct {
	RequestID   string
	Status      *int64
	Code        *int64
	ReasonCode  string
	Diagnostics map[string]any
	Issues      []Issue
}

// newServiceError ports the ConversationShareServiceError constructor: every
// piece that crosses the RPC boundary is sanitized here, once.
func newServiceError(kind, message string, details errorDetails) *serviceError {
	err := &serviceError{kind: kind, message: message}
	if trimmed := regexpSpaceTrim(details.RequestID); trimmed != "" && safeRequestIDRe.MatchString(trimmed) {
		err.requestID = trimmed
	}
	if details.Status != nil {
		err.status = details.Status
	}
	if details.Code != nil {
		err.code = details.Code
	}
	if details.ReasonCode != "" {
		err.reasonCode = details.ReasonCode
	} else {
		err.reasonCode = inferFailureReasonCode(message)
	}
	if diagnostics := sanitizeFailureDiagnostics(details.Diagnostics); len(diagnostics) > 0 {
		err.details = map[string]any{"diagnostics": diagnostics}
	}
	sanitized, issueCount, omittedCount := SanitizedIssues(details.Issues)
	if len(sanitized) > 0 {
		err.issues = sanitized
		if err.details == nil {
			err.details = map[string]any{}
		}
		issuesAsAny := make([]any, len(sanitized))
		for index, issue := range sanitized {
			issuesAsAny[index] = issue
		}
		err.details["issues"] = issuesAsAny
		if issueCount > 0 {
			err.details["issueCount"] = issueCount
			err.details["omittedIssueCount"] = omittedCount
		}
	}
	if err.requestID != "" {
		if err.details == nil {
			err.details = map[string]any{}
		}
		err.details["requestId"] = err.requestID
	}
	if err.status != nil {
		if err.details == nil {
			err.details = map[string]any{}
		}
		err.details["status"] = *err.status
	}
	if err.code != nil {
		if err.details == nil {
			err.details = map[string]any{}
		}
		err.details["code"] = *err.code
	}
	if err.details == nil {
		err.details = map[string]any{"reasonCode": err.reasonCode}
	} else {
		err.details["reasonCode"] = err.reasonCode
	}
	return err
}

func regexpSpaceTrim(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t' || value[start] == '\n' || value[start] == '\r') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\n' || value[end-1] == '\r') {
		end--
	}
	return value[start:end]
}

// sanitizeFailureDiagnostics ports sanitizeFailureDiagnostics: only the
// whitelisted keys survive, string values must not look like paths/URLs.
func sanitizeFailureDiagnostics(diagnostics map[string]any) map[string]any {
	if len(diagnostics) == 0 {
		return nil
	}
	safe := map[string]any{}
	for key, value := range diagnostics {
		if !safeFailureDiagnosticKeys[key] {
			continue
		}
		if text, ok := value.(string); ok {
			if _, isSafe := safeIssueString(text); !isSafe {
				continue
			}
		}
		safe[key] = value
	}
	if len(safe) == 0 {
		return nil
	}
	return safe
}

// Error implements error.
func (e *serviceError) Error() string { return e.message }

// Kind returns the error kind (the TS `kind` member).
func (e *serviceError) Kind() string { return e.kind }

// ReasonCode returns the inferred or explicit reason code.
func (e *serviceError) ReasonCode() string { return e.reasonCode }

// Errno returns the sanitized diagnostics errno, if present (the callers in
// selection.go branch on it exactly like isMissingArtifactReadError).
func (e *serviceError) Errno() (string, bool) {
	if e.details == nil {
		return "", false
	}
	diagnostics, ok := e.details["diagnostics"].(map[string]any)
	if !ok {
		return "", false
	}
	errno, ok := diagnostics["errno"].(string)
	return errno, ok
}

// isServiceError reports whether err is a *serviceError.
func isServiceError(err error) (*serviceError, bool) {
	if typed, ok := err.(*serviceError); ok {
		return typed, true
	}
	return nil, false
}

// errKind extracts the TS `kind` from any error shape (serviceError or a
// client error carrying one).
func errKind(err error) string {
	if typed, ok := isServiceError(err); ok {
		return typed.kind
	}
	if kinded, ok := err.(interface{ Kind() string }); ok {
		return kinded.Kind()
	}
	return ""
}

// fmt helper kept local so the file list stays import-light.
func errorf(format string, args ...any) *serviceError {
	return newServiceError("invalid_conversation", fmt.Sprintf(format, args...), errorDetails{})
}
