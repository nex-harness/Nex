package bots

import (
	"regexp"
	"strings"
)

var (
	// The four patterns below are the TS regexes verbatim (all case-insensitive).
	editPermissionDeletePattern = regexp.MustCompile(`(?i)\b(delete|deleted|remove|removed|erase|erased|unlink|rm)\b`)
	editPermissionWritePattern  = regexp.MustCompile(`(?i)\b(write|wrote|create|created|add|added|save|saved|new)\b`)
	editPermissionUpdatePattern = regexp.MustCompile(`(?i)\b(update|updating|updated)\b`)
	editTitlePrefixPattern      = regexp.MustCompile(`(?i)^edit\b[:：]?\s*`)
)

// formatEditPermissionKindLabel ports formatEditPermissionKindLabel. The
// general "Edit <path>" title cannot distinguish write/update/delete in a third
// party message, so the raw request text decides; falling through to "Editing"
// is the deliberate generic case (TS Bugfix comment kept).
func formatEditPermissionKindLabel(request PermissionRequest, preview PermissionPreview, locale string) string {
	rawTextParts := make([]string, 0, 4)
	if request.Title != "" {
		rawTextParts = append(rawTextParts, request.Title)
	}
	if request.Description != "" {
		rawTextParts = append(rawTextParts, request.Description)
	}
	if rawRecord, ok := request.Raw.(map[string]any); ok {
		if kind, ok := rawRecord["kind"].(string); ok {
			rawTextParts = append(rawTextParts, kind)
		}
		if title, ok := rawRecord["title"].(string); ok {
			rawTextParts = append(rawTextParts, title)
		}
	}
	normalizedText := strings.ToLower(strings.TrimSpace(strings.Join(rawTextParts, " ")))
	fileChangeType := ""
	if preview.FileChange != nil {
		fileChangeType = preview.FileChange.Type
	}

	switch {
	case editPermissionDeletePattern.MatchString(normalizedText):
		return formatterText(locale, fmtEditDeleting)
	case fileChangeType == "add" || editPermissionWritePattern.MatchString(normalizedText):
		return formatterText(locale, fmtEditWriting)
	case editPermissionUpdatePattern.MatchString(normalizedText):
		return formatterText(locale, fmtEditUpdating)
	}
	return formatterText(locale, fmtEditEditing)
}

// formatter_permission.go ports the permission-request half of
// replyFormatter.ts: the "which kind of edit is this" classification and the
// summary a chat shows above an approval prompt.
//
// The edit-kind classification exists because the Agent's own title is a generic
// "Edit <path>", which a third-party message cannot act on; the raw request text
// and the fileChange type decide between writing / updating / deleting.
// formatPermissionRequestHeader ports formatPermissionRequestHeader.
func formatPermissionRequestHeader(request PermissionRequest, options *FormatOptions) string {
	preview := GetPermissionRequestPreview(request)
	locale := ""
	if options != nil {
		locale = options.Locale
	}
	return formatterText(locale, fmtPermissionReq) + "\n" + formatPermissionRequestTitle(request, preview, options)
}

// FormatPermissionRequestSummary ports formatBotPermissionRequestSummary.
func FormatPermissionRequestSummary(request PermissionRequest, options *FormatOptions) string {
	preview := GetPermissionRequestPreview(request)
	header := formatPermissionRequestHeader(request, options)
	if preview.Command != nil {
		return header + "\n" + formatMarkdownInlineCode(truncateMiddleText(*preview.Command, maxCommandFieldLength))
	}
	previewFilePaths := preview.FilePaths
	if len(previewFilePaths) == 0 {
		for _, change := range preview.FileChanges {
			previewFilePaths = append(previewFilePaths, change.Path)
		}
	}
	if len(previewFilePaths) > 0 {
		workspacePath := ""
		if options != nil {
			workspacePath = options.WorkspacePath
		}
		limit := min(3, len(previewFilePaths))
		parts := make([]string, 0, limit)
		for _, path := range previewFilePaths[:limit] {
			parts = append(parts, formatMarkdownInlineCode(toWorkspaceRelativePath(path, workspacePath)))
		}
		return header + "\n" + truncateText(strings.Join(parts, ", "), maxFieldLength)
	}
	return header
}

// formatPermissionRequestTitle ports formatPermissionRequestTitle.
func formatPermissionRequestTitle(request PermissionRequest, preview PermissionPreview, options *FormatOptions) string {
	locale := ""
	workspacePath := ""
	if options != nil {
		locale = options.Locale
		workspacePath = options.WorkspacePath
	}
	if request.Kind != "edit" || (preview.Scope != ScopeFile && len(preview.FileChanges) == 0) {
		return preview.Title
	}
	label := formatEditPermissionKindLabel(request, preview, locale)
	titleWithoutEdit := strings.TrimSpace(editTitlePrefixPattern.ReplaceAllString(preview.Title, ""))
	targetText := ""
	switch {
	case titleWithoutEdit != "" && titleWithoutEdit != preview.Title:
		targetText = titleWithoutEdit
	case len(preview.FilePaths) == 1 || len(preview.FileChanges) == 1:
		var target string
		if len(preview.FilePaths) == 1 {
			target = preview.FilePaths[0]
		} else {
			target = preview.FileChanges[0].Path
		}
		targetText = toWorkspaceRelativePath(target, workspacePath)
	}
	if targetText != "" {
		return label + " " + targetText
	}
	return label
}
