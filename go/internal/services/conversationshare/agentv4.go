// agentv4.go holds the agent V4 seam types: the four conversation reads the
// share path needs, their request/result shapes and the fault-code classifiers.
// Split out of service.go so the service itself stays short; symbols are
// unchanged.
package conversationshare

import "context"

// AgentService is the ConversationShareAgentService pick.
type AgentService interface {
	// ConversationRowsRangeV4 pages the conversation rows (beforeRowId
	// pagination, PROTOCOL_V4_LIMITS.rowsRangeMaxLimit).
	ConversationRowsRangeV4(ctx context.Context, request RowsRangeRequest) (RowsRangeResult, error)
	// ConversationFileChangesV4 returns the turn's file-change set.
	ConversationFileChangesV4(ctx context.Context, request FileChangesRequest) (FileChangesResult, error)
	// ConversationAttachmentStatV4 stats one input attachment.
	ConversationAttachmentStatV4(ctx context.Context, request AttachmentStatRequest) (AttachmentStatResult, error)
	// ConversationAttachmentReadV4 pages one input attachment's bytes.
	ConversationAttachmentReadV4(ctx context.Context, request AttachmentReadRequest) (AttachmentReadResult, error)
}

// WorkspaceRef carries the identity triple every agent request forwards
// (workspaceIdentity / workspacePath rules from AGENTS.md).
type WorkspaceRef struct {
	WorkspacePath     string
	WorkspaceIdentity string
	RemoteSessionID   string
	SessionID         string
}

// RowsRangeRequest ports the conversationRowsRangeV4 params.
type RowsRangeRequest struct {
	WorkspaceRef
	BeforeRowID *int64
	Limit       int
}

// RowsRangeResult ports the result.
type RowsRangeResult struct {
	Rows       []row
	AtRevision int
	AtLogEpoch string
	HasMore    bool
}

// FileChangesRequest ports conversationFileChangesV4 params.
type FileChangesRequest struct {
	WorkspaceRef
	Target       RowTarget
	BaseRevision int
	BaseLogEpoch string
}

// FileChangesResult ports the result.
type FileChangesResult struct {
	Items []struct {
		Path string `json:"path"`
	}
	State string
}

// RowTarget is {rowId, entityId}.
type RowTarget struct {
	RowID    int64  `json:"rowId"`
	EntityID string `json:"entityId"`
}

// AttachmentStatRequest ports conversationAttachmentStatV4 params.
type AttachmentStatRequest struct {
	WorkspaceRef
	Target          RowTarget
	AttachmentIndex int
	Ref             string
}

// AttachmentStatResult ports the result.
type AttachmentStatResult struct {
	TotalBytes int64
}

// AttachmentReadRequest ports conversationAttachmentReadV4 params.
type AttachmentReadRequest struct {
	WorkspaceRef
	Ref             string
	Target          RowTarget
	AttachmentIndex int
	Offset          int64
	Limit           int64
}

// AttachmentReadResult ports the result.
type AttachmentReadResult struct {
	DataBase64 string
	MediaType  string
	TotalBytes int64
	NextOffset *int64
}

// protocolV4 limits used here (PROTOCOL_V4_LIMITS).
const (
	rowsRangeMaxLimit      = 200
	attachmentChunkMaxByte = 512 * 1024
)

// Attachment fault codes (NEX_ATTACHMENT_FAULT_CODES) — the stable-string
// classification seam. The Go agent bridge reports them in error messages or
// wrapped codes; the reader accepts both.
const (
	faultShareStatNotFound            = "fault.attachment.shareStatNotFound"
	faultStatNotFile                  = "fault.attachment.statNotFile"
	faultShareStatNotAuthorized       = "fault.attachment.shareStatNotAuthorized"
	faultShareReadNotAuthorized       = "fault.attachment.shareReadNotAuthorized"
	faultShareStatConnectionUntrusted = "fault.attachment.shareStatConnectionUntrusted"
	faultShareReadConnectionUntrusted = "fault.attachment.shareReadConnectionUntrusted"
	faultShareStatTooLarge            = "fault.attachment.shareStatTooLarge"
	faultPreviewTooLarge              = "fault.attachment.previewTooLarge"
)

var faultCodes = []string{
	faultShareStatNotFound, faultStatNotFile, faultShareStatNotAuthorized,
	faultShareReadNotAuthorized, faultShareStatConnectionUntrusted,
	faultShareReadConnectionUntrusted, faultShareStatTooLarge, faultPreviewTooLarge,
}

// readFaultCode ports readNexAttachmentFaultCode (message-equality fallback).
func readFaultCode(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, code := range faultCodes {
		if message == code {
			return code
		}
		if typed, ok := err.(interface{ FaultCode() string }); ok {
			if typed.FaultCode() == code {
				return code
			}
		}
	}
	return ""
}

// isDefiniteMissingAttachment / isAttachmentAuthorizationError /
// isAttachmentTooLargeError port the three TS classifiers.
func isDefiniteMissingAttachment(err error) bool {
	switch readFaultCode(err) {
	case faultShareStatNotFound, faultStatNotFile:
		return true
	}
	return false
}

func isAttachmentAuthorizationError(err error) bool {
	switch readFaultCode(err) {
	case faultShareStatNotAuthorized, faultShareReadNotAuthorized,
		faultShareStatConnectionUntrusted, faultShareReadConnectionUntrusted:
		return true
	}
	return false
}

func isAttachmentTooLargeError(err error) bool {
	return readFaultCode(err) == faultShareStatTooLarge || readFaultCode(err) == faultPreviewTooLarge
}

// connectionUnavailableErrors ports CONNECTION_UNAVAILABLE_ERRORS.
var connectionUnavailableErrors = map[string]bool{
	"fault.conversation.rowsRangeConnectionUntrusted": true,
	"fault.connection.handshakeRequired":              true,
	"fault.connection.closed":                         true,
}

// normalizeConnectionError ports normalizeConversationShareConnectionError.
func normalizeConnectionError(err error) error {
	if err != nil && connectionUnavailableErrors[err.Error()] {
		return newServiceError(KindConnectionUnavailable, "Conversation share connection is not ready", errorDetails{})
	}
	return err
}

// PublishProgress ports ConversationSharePublishProgress.
type PublishProgress struct {
	OperationID         string  `json:"operationId"`
	Phase               string  `json:"phase"` // collecting | uploading | checking | complete
	CompletedArtifacts  int     `json:"completedArtifacts"`
	TotalArtifacts      int     `json:"totalArtifacts"`
	Warnings            []Issue `json:"warnings,omitempty"`
	OmittedWarningCount *int    `json:"omittedWarningCount,omitempty"`
}
