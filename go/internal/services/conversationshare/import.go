// import.go ports importShareInternal + getImportedConversation +
// sharedContextFormatter.ts: the download → verify → stage → commit import
// flow with the import-owned-subdirectory cleanup boundary and the persisted
// readonly copy.
package conversationshare

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
)

// ImportInput ports ImportConversationShareInput.
type ImportInput struct {
	ShareCode               string
	ClientRequestID         string
	TargetWorkspacePath     string
	TargetWorkspaceIdentity string
	TargetWorkspaceKind     string // local | remote
	Locale                  string
}

// ImportResult ports ImportConversationShareResult.
type ImportResult struct {
	WorkspacePath     string `json:"workspacePath"`
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
	SessionID         string `json:"sessionId"`
	ContextID         string `json:"contextId"`
	ShareURL          string `json:"shareUrl"`
	Title             string `json:"title"`
	Reused            bool   `json:"reused"`
	FallbackReason    string `json:"fallbackReason,omitempty"`
}

// ImportProgress ports ConversationShareImportProgress.
type ImportProgress struct {
	OperationID        string `json:"operationId"`
	Phase              string `json:"phase"` // downloading | installing | committing | complete
	CompletedArtifacts int    `json:"completedArtifacts"`
	TotalArtifacts     int    `json:"totalArtifacts"`
}

// ImportedConversation ports ImportedConversationShare.
type ImportedConversation struct {
	ShareID             string             `json:"shareId"`
	ContextID           string             `json:"contextId"`
	Title               string             `json:"title"`
	Rows                []json.RawMessage  `json:"rows"`
	Artifacts           []ImportedArtifact `json:"artifacts"`
	UnsupportedRowCount int                `json:"unsupportedRowCount"`
}

// ImportedArtifact is one artifact entry of the readonly copy.
type ImportedArtifact struct {
	ArtifactID            string `json:"artifactId"`
	DisplayName           string `json:"displayName"`
	MimeType              string `json:"mimeType,omitempty"`
	WorkspaceRelativePath string `json:"workspaceRelativePath,omitempty"`
}

// importedConversationShareFormatVersion ports
// IMPORTED_CONVERSATION_SHARE_FORMAT_VERSION.
const importedConversationShareFormatVersion = 1

// downloadTimeoutMs ports DOWNLOAD_TIMEOUT_MS.
const downloadTimeoutMs = 120_000

// SessionService is the nexSessionService pick the import flow needs.
type SessionService interface {
	// CreateSession commits the imported-history session.
	CreateSession(ctx context.Context, request CreateSessionRequest) (CreateSessionResult, error)
	// ListSessions lists the workspace's sessions (limit 100).
	ListSessions(ctx context.Context, workspacePath string, limit int) ([]SessionSummary, error)
}

// CreateSessionRequest is the importedHistory subset of createSession.
type CreateSessionRequest struct {
	WorkspacePath     string
	WorkspaceIdentity string
	SessionID         string
	ImportedHistory   *ImportedHistory
}

// ImportedHistory ports the importedHistory member.
type ImportedHistory struct {
	Source     string // "sharedContext"
	Title      string
	Markdown   string
	Provenance map[string]any
}

// CreateSessionResult carries the persisted session identity.
type CreateSessionResult struct {
	SessionID string
}

// SessionSummary is the listSessions item shape used here.
type SessionSummary struct {
	SessionID string
	Title     string
}

// importedShareTitlePrefix ports IMPORTED_SHARE_TITLE_PREFIX.
var importedShareTitlePrefix = map[string]string{
	"zh-CN": "来自分享：",
	"en-US": "From Share: ",
}

// sanitizeFileSegment ports sanitizeFileSegment.
func sanitizeFileSegment(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			builder.WriteRune('-')
		} else {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// uniqueImportedFileName ports uniqueImportedFileName.
func uniqueImportedFileName(displayName, artifactID string, usedNames map[string]bool) string {
	normalized := strings.ReplaceAll(displayName, "\\", "/")
	leaf := normalized
	if index := strings.LastIndex(normalized, "/"); index >= 0 {
		leaf = normalized[index+1:]
	}
	base := strings.Trim(sanitizeFileSegment(leaf), ". ")
	if len(base) > 180 {
		base = base[:180]
	}
	if base == "" {
		base = "artifact"
	}
	candidate := base
	if usedNames[strings.ToLower(candidate)] {
		dot := strings.LastIndex(base, ".")
		suffix := regexpNonAlnum.ReplaceAllString(artifactID, "")
		if len(suffix) > 8 {
			suffix = suffix[len(suffix)-8:]
		}
		if suffix == "" {
			suffix = "artifact"
		}
		if dot > 0 {
			candidate = base[:dot] + "-" + suffix + base[dot:]
		} else {
			candidate = base + "-" + suffix
		}
	}
	usedNames[strings.ToLower(candidate)] = true
	return candidate
}

var regexpNonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// importDedupeKey ports importDedupeKey.
func importDedupeKey(shareCode, workspaceKey string) string {
	return shareCode + "\x00" + workspaceKey
}

// importState is the dedupe/persist bookkeeping.
type importState struct {
	mu                   sync.Mutex
	completed            map[string]ImportResult
	completedByWorkspace map[string]ImportResult
	inFlight             map[string]chan ImportResult
	inFlightErrors       map[string]error
}
