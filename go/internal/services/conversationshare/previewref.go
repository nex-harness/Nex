// previewref.go holds the preview-reference half of the share preflight: the
// reference extractors, the file-type table and the path normalizers. Split out
// of preflight.go so the service flow stays readable; the symbols are the same
// package-private ones, no API changed.
package conversationshare

import (
	"regexp"
	"strings"
)

// PreflightInput ports ConversationSharePreflightInput.
type PreflightInput struct {
	WorkspaceRef
	Selection Selection `json:"selection"`
}

// previewCandidate ports ConversationPreviewArtifactCandidate.
type previewCandidate struct {
	ArtifactType        string
	DisplayName         string
	MimeType            string
	PreviewKind         string
	ProductTurnID       string
	SourceRef           string
	RequiresFileChanges bool
}

// PreviewCandidatesFn builds the preview-card candidates from assistant text
// (the shared extractConversationPreviewFileReferences +
// buildConversationPreviewArtifactCandidates pair). Injectable so tests pin
// exact candidates.
type PreviewCandidatesFn func(assistantText, productTurnID, workspacePath string, fileChanges []fileChange) []previewCandidate

// fileChange ports ConversationPreviewFileChange.
type fileChange struct {
	Path  string
	State string // active | reverted
}

// defaultPreviewCandidates is the default seam implementation: markdown
// links, file:// URLs, backtick-quoted and bare workspace-relative paths with
// the preview extension set. The extension table mirrors PREVIEW_FILE_TYPES.
func defaultPreviewCandidates(assistantText, productTurnID, workspacePath string, changes []fileChange) []previewCandidate {
	references := extractPreviewReferences(assistantText, workspacePath)
	active := map[string]bool{}
	reverted := map[string]bool{}
	for _, change := range changes {
		if change.State == "reverted" {
			reverted[change.Path] = true
		} else {
			active[change.Path] = true
		}
	}
	seen := map[string]bool{}
	var out []previewCandidate
	for _, reference := range references {
		definition, ok := previewFileTypeOf(reference.path)
		if !ok {
			continue
		}
		if !isInsideWorkspace(workspacePath, reference.path) {
			continue
		}
		requiresFileChanges := definition.kind == "markdown" || definition.kind == "html"
		if requiresFileChanges && (!active[reference.path] || reverted[reference.path]) {
			continue
		}
		if seen[reference.path] {
			continue
		}
		seen[reference.path] = true
		out = append(out, previewCandidate{
			ArtifactType: definition.artifactType, DisplayName: leafOf(reference.path),
			MimeType: definition.mimeType, PreviewKind: definition.kind,
			ProductTurnID: productTurnID, SourceRef: reference.path,
			RequiresFileChanges: requiresFileChanges,
		})
	}
	return out
}

type previewFileDefinition struct {
	extensions   []string
	kind         string
	mimeType     string
	artifactType string
}

var previewFileTypes = []previewFileDefinition{
	{[]string{".md"}, "markdown", "text/markdown", "md"},
	{[]string{".html", ".htm"}, "html", "text/html", "html"},
	{[]string{".docx"}, "docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx"},
	{[]string{".xlsx"}, "xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"},
	{[]string{".pptx"}, "pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "pptx"},
	{[]string{".pdf"}, "pdf", "application/pdf", "pdf"},
	{[]string{".mp4"}, "video", "video/mp4", "video"},
	{[]string{".mov"}, "video", "video/quicktime", "video"},
	{[]string{".webm"}, "video", "video/webm", "video"},
	{[]string{".m4v"}, "video", "video/x-m4v", "video"},
	{[]string{".mp3"}, "audio", "audio/mpeg", "audio"},
	{[]string{".wav"}, "audio", "audio/wav", "audio"},
	{[]string{".m4a"}, "audio", "audio/mp4", "audio"},
	{[]string{".ogg"}, "audio", "audio/ogg", "audio"},
	{[]string{".opus"}, "audio", "audio/ogg", "audio"},
	{[]string{".flac"}, "audio", "audio/flac", "audio"},
	{[]string{".weba"}, "audio", "audio/webm", "audio"},
}

func previewFileTypeOf(path string) (previewFileDefinition, bool) {
	normalized := strings.ToLower(path)
	for _, definition := range previewFileTypes {
		for _, extension := range definition.extensions {
			if strings.HasSuffix(normalized, extension) {
				return definition, true
			}
		}
	}
	return previewFileDefinition{}, false
}

func leafOf(path string) string {
	segments := strings.Split(strings.TrimRight(strings.ReplaceAll(path, "\\", "/"), "/"), "/")
	if len(segments) == 0 {
		return path
	}
	return segments[len(segments)-1]
}

type previewReference struct {
	path string
}

// previewExtGroup is the shared extension alternation of the delimited
// reference forms.
const previewExtGroup = `(?:md|html?|docx|xlsx|pptx|pdf|mp4|mov|webm|m4v|mp3|wav|m4a|ogg|opus|flac|weba)`

// previewDelimitedBody is the TS body class `[^` + "`" + `'"` + `\r\n]` — a
// delimited reference may not contain ANY of the three quote characters.
const previewDelimitedBody = "[^`\"'\r\n]"

var (
	previewMarkdownLinkRe = regexp.MustCompile(`\[([^\]\n]*)\]\(([^)\n]+)\)`)
	previewFileURLRe      = regexp.MustCompile(`(?i)\bfile://[^\s<>()\]` + "`" + `'*，。！？；：、]+`)
	// RE2 has no backreferences, so the TS `[delim] … \1` form becomes one
	// regex per delimiter. The body class already excludes all three quotes, so
	// the three patterns accept exactly the same text as the TS single pattern.
	previewDelimitedRegexes = []*regexp.Regexp{
		regexp.MustCompile("(" + previewDelimitedBody + "+?\\." + previewExtGroup + ")`"),
		regexp.MustCompile("(" + previewDelimitedBody + "+?\\." + previewExtGroup + ")\""),
		regexp.MustCompile("(" + previewDelimitedBody + "+?\\." + previewExtGroup + ")"),
	}
)

// referencesNeedFileChanges ports `references.some(r => r.kind is markdown|html)`:
// the decision must be made on the REFERENCES, before defaultPreviewCandidates
// drops the unauthorized markdown/html ones. Deriving it from the already
// filtered candidates can never fire, which would mean fileChanges is never
// fetched and markdown/html references are never shareable.
func referencesNeedFileChanges(assistantText, workspacePath string) bool {
	for _, reference := range extractPreviewReferences(assistantText, workspacePath) {
		if definition, ok := previewFileTypeOf(reference.path); ok &&
			(definition.kind == "markdown" || definition.kind == "html") {
			return true
		}
	}
	return false
}

// extractPreviewReferences extracts a de-duplicated, document-ordered list of
// resolvable preview file references.
func extractPreviewReferences(content, workspacePath string) []previewReference {
	type match struct{ raw string }
	var matches []match
	seenSpans := [][2]int{}
	collect := func(raw string, start, end int) {
		for _, span := range seenSpans {
			if start < span[1] && end > span[0] {
				return
			}
		}
		seenSpans = append(seenSpans, [2]int{start, end})
		matches = append(matches, match{raw: raw})
	}
	for _, match := range previewMarkdownLinkRe.FindAllStringSubmatchIndex(content, -1) {
		href := strings.Trim(content[match[4]:match[5]], " \t<>\r\n")
		collect(href, match[0], match[1])
	}
	for _, match := range previewFileURLRe.FindAllStringIndex(content, -1) {
		collect(strings.TrimSpace(content[match[0]:match[1]]), match[0], match[1])
	}
	for _, delimited := range previewDelimitedRegexes {
		for _, match := range delimited.FindAllStringSubmatchIndex(content, -1) {
			raw := strings.TrimSpace(content[match[2]:match[3]])
			offset := match[0] + strings.Index(content[match[0]:match[1]], raw)
			collect(raw, offset, offset+len(raw))
		}
	}
	var references []previewReference
	seen := map[string]bool{}
	for _, entry := range matches {
		path := resolvePreviewPath(workspacePath, entry.raw)
		if path == "" {
			continue
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		references = append(references, previewReference{path: path})
	}
	return references
}

// resolvePreviewPath ports resolveConversationPreviewPath for the shapes this
// default extractor produces (relative + absolute + file URL).
func resolvePreviewPath(workspacePath, rawPath string) string {
	cleaned := strings.TrimRight(rawPath, " \t\r\n")
	cleaned = strings.TrimRight(cleaned, ".,;!?，。！？；：、")
	if index := regexpLineNumberSuffix.FindStringIndex(cleaned); index != nil {
		cleaned = cleaned[:index[0]]
	}
	if cleaned == "" {
		return ""
	}
	if strings.HasPrefix(cleaned, "~/") || strings.HasPrefix(cleaned, "~\\") {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(cleaned), "file://") {
		cleaned = fileURLToPath(cleaned)
		if cleaned == "" {
			return ""
		}
	}
	if strings.HasPrefix(cleaned, "/") || regexpWindowsAbs.MatchString(cleaned) || strings.HasPrefix(cleaned, `\\`) {
		return normalizePreviewPath(cleaned)
	}
	normalized := normalizeRelativePreviewPath(cleaned)
	if normalized == "" {
		return ""
	}
	return strings.TrimRight(normalizePreviewPath(workspacePath), "/") + "/" + normalized
}

var (
	regexpLineNumberSuffix = regexp.MustCompile(`:\d+(?::\d+)?$`)
	regexpWindowsAbs       = regexp.MustCompile(`^[a-zA-Z]:[\\/]`)
)

func fileURLToPath(value string) string {
	trimmed := strings.TrimPrefix(value, "file://")
	trimmed = strings.TrimPrefix(trimmed, "file:/")
	trimmed = strings.TrimPrefix(trimmed, "file:")
	if trimmed == "" {
		return ""
	}
	if index := strings.Index(trimmed, "://"); index >= 0 && index < strings.Index(trimmed+"/", "/"+string(trimmed[0])) {
		_ = index
	}
	// hostnames other than localhost are not workspace files.
	if !strings.HasPrefix(value, "file:///") && !strings.HasPrefix(value, "file://localhost") && strings.HasPrefix(value, "file://") {
		rest := strings.TrimPrefix(value, "file://")
		slash := strings.Index(rest, "/")
		if slash > 0 && rest[:slash] != "localhost" {
			return ""
		}
	}
	return strings.TrimPrefix(trimmed, "localhost")
}

func normalizePreviewPath(path string) string {
	normalized := strings.ReplaceAll(path, "\\", "/")
	normalized = regexpMultiSlash.ReplaceAllString(normalized, "/")
	prefix := ""
	if strings.HasPrefix(normalized, "//") {
		prefix = "//"
		normalized = normalized[2:]
	} else if strings.HasPrefix(normalized, "/") {
		prefix = "/"
	}
	var segments []string
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".." {
			if len(segments) > 0 {
				segments = segments[:len(segments)-1]
			}
			continue
		}
		segments = append(segments, segment)
	}
	return prefix + strings.Join(segments, "/")
}

var regexpMultiSlash = regexp.MustCompile(`/{2,}`)

func normalizeRelativePreviewPath(path string) string {
	var segments []string
	for _, segment := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".." {
			if len(segments) == 0 {
				return ""
			}
			segments = segments[:len(segments)-1]
			continue
		}
		segments = append(segments, segment)
	}
	return strings.Join(segments, "/")
}

// attachmentDisplayName ports attachmentDisplayName.
func attachmentDisplayName(fileName string) string {
	leaf := leafOf(fileName)
	if leaf == "" {
		return "附件"
	}
	return leaf
}
