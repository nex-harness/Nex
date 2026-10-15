// Package conversationshare is the IConversationShareService port (step 3.8).
//
// Source: packages/services/src/conversation-share/*.ts (5.5k lines) plus the
// shared contracts in packages/shared/src/conversation-share.ts. This file
// ports conversationShare.ts: the error/reason-code vocabulary, the failure
// issue shape, and sanitizeConversationShareIssues — the cross-RPC redaction
// boundary. Everything that crosses to the Renderer goes through it: paths,
// URLs and over-long strings are stripped so a diagnostic issue can never
// leak a local file path.
//
// Privacy boundary of the whole package (AGENTS.md + phase-3 spec): the
// service uploads conversation data to an external endpoint. That endpoint is
// ONLY reachable through the injected HTTPClient seam; tests use httptest
// servers with loopback URLs and must never embed a real endpoint. The
// upload payload is the public projection built by projection.go, which
// re-mints every identity and drops local-only fields.
package conversationshare

import (
	"regexp"
	"strings"
)

// Error kinds (ConversationShareServiceErrorKind). The client error half
// comes from the HTTP client; the service adds the five below.
const (
	KindArtifactProtocolNotReady = "artifact_protocol_not_ready"
	KindConnectionUnavailable    = "connection_unavailable"
	KindInvalidSelection         = "invalid_selection"
	KindSafetyCheckTimeout       = "safety_check_timeout"
)

// Reason codes (ConversationShareFailureReasonCode). Error name/code/kind
// strings are part of the UI contract; copy them exactly.
const (
	ReasonRunningTurn              = "running_turn"
	ReasonStreamingRow             = "streaming_row"
	ReasonActiveToolCall           = "active_tool_call"
	ReasonActiveSubagent           = "active_subagent"
	ReasonInputAttachment          = "input_attachment"
	ReasonInputAttachmentUnavail   = "input_attachment_unavailable"
	ReasonInlineToolImage          = "inline_tool_image"
	ReasonUnsupportedTimeline      = "unsupported_timeline"
	ReasonUnsafeURL                = "unsafe_url"
	ReasonMissingProductTurn       = "missing_product_turn"
	ReasonInvalidSelection         = "invalid_selection"
	ReasonArtifactTypeNotAllowed   = "artifact_type_not_allowed"
	ReasonArtifactExtensionMissing = "artifact_extension_missing"
	ReasonArtifactOutsideWorkspace = "artifact_outside_workspace"
	ReasonArtifactChanged          = "artifact_changed"
	ReasonArtifactReadFailed       = "artifact_read_failed"
	// 已知容量超限：附件真实大小超过服务端 max_artifact_bytes 或通道可搬运上限。
	// 与 artifact_read_failed（不确定）不同，它在选择阶段就是确定阻断。
	ReasonArtifactSizeLimit   = "artifact_size_limit"
	ReasonArtifactManifest    = "artifact_manifest"
	ReasonPayloadLimit        = "payload_limit"
	ReasonNoShareableContent  = "no_shareable_content"
	ReasonStaleConversation   = "stale_conversation"
	ReasonArtifactProtocol    = "artifact_protocol_not_ready"
	ReasonInvalidConversation = "invalid_conversation"
)

// Issue scopes.
const (
	ScopeConversation = "conversation"
	ScopeTurn         = "turn"
	ScopeArtifact     = "artifact"
	ScopeTransport    = "transport"
)

// maxFailureIssues ports MAX_FAILURE_ISSUES.
const maxFailureIssues = 5

// Issue ports ConversationShareFailureIssue. Optional fields use pointers so
// the RPC payload can omit them exactly like the TS spread conditionals.
type Issue struct {
	Code                string            `json:"code"`
	Scope               string            `json:"scope"`
	RowID               *int64            `json:"rowId,omitempty"`
	TurnOrdinal         *int64            `json:"turnOrdinal,omitempty"`
	ProductTurnID       *string           `json:"productTurnId,omitempty"`
	ArtifactDisplayName *string           `json:"artifactDisplayName,omitempty"`
	ArtifactType        *string           `json:"artifactType,omitempty"`
	Extension           *string           `json:"extension,omitempty"`
	MimeType            *string           `json:"mimeType,omitempty"`
	Actual              *int64            `json:"actual,omitempty"`
	Limit               *int64            `json:"limit,omitempty"`
	RetryAfterMs        *int64            `json:"retryAfterMs,omitempty"`
	Phase               *string           `json:"phase,omitempty"`
	AllowedFormats      []string          `json:"allowedFormats,omitempty"`
	AllowedArtifacts    []AllowedArtifact `json:"allowedArtifacts,omitempty"`
	Availability        *string           `json:"availability,omitempty"`
}

// AllowedArtifact ports ConversationShareAllowedArtifact.
type AllowedArtifact struct {
	Type        string   `json:"type"`
	Extensions  []string `json:"extensions"`
	MimeTypes   []string `json:"mimeTypes"`
	DisplayName string   `json:"displayName,omitempty"`
}

// inferFailureReasonCode ports inferFailureReasonCode: message → reason code.
func inferFailureReasonCode(message string) string {
	for _, rule := range reasonCodeRules {
		if rule.pattern.MatchString(message) {
			return rule.code
		}
	}
	return ReasonInvalidConversation
}

type reasonCodeRule struct {
	pattern *regexp.Regexp
	code    string
}

var reasonCodeRules = []reasonCodeRule{
	{regexp.MustCompile(`Running turns`), ReasonRunningTurn},
	{regexp.MustCompile(`Streaming rows`), ReasonStreamingRow},
	{regexp.MustCompile(`Active tool calls`), ReasonActiveToolCall},
	{regexp.MustCompile(`Active subagents|subagent detail`), ReasonActiveSubagent},
	{regexp.MustCompile(`attachments?|public input contains local write identity`), ReasonInputAttachment},
	{regexp.MustCompile(`Inline tool images`), ReasonInlineToolImage},
	{regexp.MustCompile(`Timeline|fork|checkpoint|summary references`), ReasonUnsupportedTimeline},
	{regexp.MustCompile(`Local and inline URLs`), ReasonUnsafeURL},
	{regexp.MustCompile(`missing its product turn|product turn identity`), ReasonMissingProductTurn},
	{regexp.MustCompile(`Selected product turns|selected row|selection`), ReasonInvalidSelection},
	{regexp.MustCompile(`not allowed by server capabilities`), ReasonArtifactTypeNotAllowed},
	{regexp.MustCompile(`extension is missing`), ReasonArtifactExtensionMissing},
	{regexp.MustCompile(`outside the workspace`), ReasonArtifactOutsideWorkspace},
	{regexp.MustCompile(`changed after|file changed|size/mtime`), ReasonArtifactChanged},
	// 本地/远程 artifact source 的兜底消息是 "artifact cannot be read"，
	// 旧正则只覆盖 ended|source|chunk|readable，读失败因此被误判为 invalid_conversation。
	{regexp.MustCompile(`artifact (?:ended|source|chunk|readable|cannot be read)`), ReasonArtifactReadFailed},
	{regexp.MustCompile(`artifact (?:identifiers|manifest|acknowledgement|missing)`), ReasonArtifactManifest},
	{regexp.MustCompile(`payload|too many rows|too many artifacts`), ReasonPayloadLimit},
}

// safeFailureDiagnosticKeys ports SAFE_FAILURE_DIAGNOSTIC_KEYS.
var safeFailureDiagnosticKeys = map[string]bool{
	"rowKind": true, "rowId": true, "field": true, "artifactType": true,
	"extension": true, "phase": true, "expectedBytes": true, "actualBytes": true,
	"errno": true,
}

var (
	unsafeIssueStringRe = regexp.MustCompile(`[\\/]|://`)
	safeMimeTypeRe      = regexp.MustCompile(`^[A-Za-z0-9.+-]+/[A-Za-z0-9.+-]+$`)
)

// safeIssueString ports the safeString helper: non-empty, ≤128, no path/URL.
func safeIssueString(value string) (string, bool) {
	if value == "" || len(value) > 128 || unsafeIssueStringRe.MatchString(value) {
		return "", false
	}
	return value, true
}

// safeIssueMimeType ports safeMimeType.
func safeIssueMimeType(value string) (string, bool) {
	if value == "" || len(value) > 128 || !safeMimeTypeRe.MatchString(value) {
		return "", false
	}
	return value, true
}

// sanitizeFailureIssue ports sanitizeFailureIssue. displayName 含路径/URL 或超长时
// 只剥离该字段，保留 code/scope 等定位信息（TS 注释：脱敏目标是不泄露路径）。
func sanitizeFailureIssue(issue Issue) (Issue, bool) {
	out := Issue{Code: issue.Code, Scope: issue.Scope}
	if issue.RowID != nil {
		out.RowID = issue.RowID
	}
	if issue.TurnOrdinal != nil {
		out.TurnOrdinal = issue.TurnOrdinal
	}
	if issue.ProductTurnID != nil && *issue.ProductTurnID != "" {
		out.ProductTurnID = issue.ProductTurnID
	}
	if issue.ArtifactDisplayName != nil {
		trimmed := strings.TrimSpace(*issue.ArtifactDisplayName)
		if safe, ok := safeIssueString(trimmed); ok {
			out.ArtifactDisplayName = &safe
		}
	}
	copyStringField(&out.ArtifactType, issue.ArtifactType)
	copyStringField(&out.Extension, issue.Extension)
	if issue.MimeType != nil {
		if safe, ok := safeIssueMimeType(*issue.MimeType); ok {
			out.MimeType = &safe
		}
	}
	if issue.Actual != nil {
		out.Actual = issue.Actual
	}
	if issue.Limit != nil {
		out.Limit = issue.Limit
	}
	if issue.RetryAfterMs != nil {
		out.RetryAfterMs = issue.RetryAfterMs
	}
	if issue.Phase != nil {
		out.Phase = issue.Phase
	}
	if allowedFormats := sanitizeAllowedFormats(issue.AllowedFormats); len(allowedFormats) > 0 {
		out.AllowedFormats = allowedFormats
	}
	if allowedArtifacts := sanitizeAllowedArtifacts(issue.AllowedArtifacts); len(allowedArtifacts) > 0 {
		out.AllowedArtifacts = allowedArtifacts
	}
	if issue.Availability != nil {
		out.Availability = issue.Availability
	}
	return out, true
}

func copyStringField(target **string, source *string) {
	if source == nil {
		return
	}
	if safe, ok := safeIssueString(*source); ok {
		*target = &safe
	}
}

func sanitizeAllowedFormats(formats []string) []string {
	var out []string
	for _, value := range formats {
		trimmed := strings.TrimSpace(value)
		if safe, ok := safeIssueString(trimmed); ok {
			out = append(out, safe)
		}
	}
	return out
}

func sanitizeAllowedArtifacts(artifacts []AllowedArtifact) []AllowedArtifact {
	var out []AllowedArtifact
	for _, artifact := range artifacts {
		cleaned := AllowedArtifact{Type: strings.TrimSpace(artifact.Type)}
		for _, extension := range artifact.Extensions {
			trimmed := strings.TrimSpace(extension)
			trimmed = strings.TrimPrefix(trimmed, ".")
			if trimmed != "" && len(trimmed) <= 32 {
				cleaned.Extensions = append(cleaned.Extensions, trimmed)
			}
		}
		for _, mimeType := range artifact.MimeTypes {
			trimmed := strings.ToLower(strings.TrimSpace(mimeType))
			if safeMimeTypeRe.MatchString(trimmed) {
				cleaned.MimeTypes = append(cleaned.MimeTypes, trimmed)
			}
		}
		if artifact.DisplayName != "" && len(artifact.DisplayName) <= 64 &&
			!unsafeIssueStringRe.MatchString(artifact.DisplayName) {
			cleaned.DisplayName = strings.TrimSpace(artifact.DisplayName)
		}
		// 过滤规则：type 非空、无路径/URL、extensions 非空。
		if cleaned.Type == "" || unsafeIssueStringRe.MatchString(cleaned.Type) ||
			len(cleaned.Extensions) == 0 {
			continue
		}
		out = append(out, cleaned)
	}
	return out
}

// SanitizedIssues ports sanitizeConversationShareIssues: 裁剪到 5 条并清洗成
// 可安全跨 RPC 的载荷。返回清洗后的 issues 与被省略的条数。非阻断 warning
// 与阻断 issue 共用同一套脱敏规则。
func SanitizedIssues(issues []Issue) (sanitized []Issue, issueCount, omittedCount int) {
	limit := issues
	if len(limit) > maxFailureIssues {
		limit = limit[:maxFailureIssues]
	}
	for _, issue := range limit {
		if cleaned, ok := sanitizeFailureIssue(issue); ok {
			sanitized = append(sanitized, cleaned)
		}
	}
	return sanitized, len(issues), max(0, len(issues)-len(sanitized))
}
