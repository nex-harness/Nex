package tools

import (
	"strings"
)

// EditErrorCode mirrors EditErrorCode in contracts/src/tools/edit.ts.
const (
	EditNoChange          = 1
	EditFileExistsNoOld   = 3
	EditFileNotExist      = 4
	EditNotebookFile      = 5
	EditFileNotRead       = 6
	EditStaleFile         = 7
	EditOldStringNotFound = 8
	EditAmbiguousReplace  = 9
	EditFileTooLarge      = 10
	EditInvalidPath       = 13
)

const (
	editNotReadMessage   = "File has not been read yet. Read it first before writing to it."
	editStaleMessage     = "File has been modified since read, either by the user or by a linter. Read it again before attempting to write it."
	editFreshnessSuffix  = " (file state is current in your context — no need to Read it back)"
	maxEditFileSizeBytes = 1024 * 1024 * 1024
)

// EditOutput mirrors EditOutput for the fields the Go port produces.
type EditOutput struct {
	FilePath            string     `json:"filePath"`
	OldString           string     `json:"oldString"`
	NewString           string     `json:"newString"`
	OriginalFile        string     `json:"originalFile"`
	StructuredPatch     []DiffHunk `json:"structuredPatch"`
	UserModified        bool       `json:"userModified"`
	ReplaceAll          bool       `json:"replaceAll"`
	MatchStrategy       *string    `json:"matchStrategy,omitempty"`
	MatchCandidateCount *int       `json:"matchCandidateCount,omitempty"`
}

// Edit mirrors handlers/edit.ts.
func Edit(input map[string]any, c *Context) (*EditOutput, error) {
	filePath, err := stringField(input, "file_path")
	if err != nil {
		return nil, err
	}
	oldString, err := stringField(input, "old_string")
	if err != nil {
		return nil, err
	}
	newString, err := stringField(input, "new_string")
	if err != nil {
		return nil, err
	}
	replaceAll := boolField(input, "replace_all")

	if oldString == newString {
		return nil, &Failure{ErrorCode: EditNoChange, Message: "No changes to make: old_string and new_string are exactly the same."}
	}
	if filePath == "" {
		return nil, &Failure{ErrorCode: EditInvalidPath, Message: "Tool path must not be empty"}
	}

	resolved, err := ResolveWorkspacePath(filePath, c.WorkingDirectory, c.WorkspaceRoot, "write")
	if err != nil {
		return nil, err
	}

	stat, statErr := c.FS.Stat(c.Ctx, resolved)
	if statErr != nil {
		if fsErrorCode(statErr) != "not_found" {
			return nil, statErr
		}
		// missing file
		if oldString == "" {
			return writeEditResult(c, writeEditInput{
				filePath:      filePath,
				resolved:      resolved,
				originalFile:  "",
				actualOld:     "",
				actualNew:     newString,
				newContent:    newString,
				replaceAll:    replaceAll,
				matchAttempts: 0,
			})
		}
		return nil, &Failure{ErrorCode: EditFileNotExist, Message: missingReadFileMessage(resolved, c)}
	}

	if stat.SizeBytes > maxEditFileSizeBytes {
		return nil, &Failure{ErrorCode: EditFileTooLarge, Message: "File is too large to edit (1GB). Maximum editable file size is 1GB."}
	}

	read, err := c.FS.ReadTextFile(c.Ctx, resolved, 0)
	if err != nil {
		return nil, err
	}
	content := strings.ReplaceAll(read.Content, "\r\n", "\n")
	oldNorm := strings.ReplaceAll(oldString, "\r\n", "\n")
	requestedNew := strings.ReplaceAll(newString, "\r\n", "\n")

	if oldNorm == "" {
		if strings.TrimSpace(content) != "" {
			return nil, &Failure{ErrorCode: EditFileExistsNoOld, Message: "Cannot create new file - file already exists."}
		}
		if failure := editableReadStateFailure(resolved, read, c.ReadState); failure != nil {
			return nil, failure
		}
		return writeEditResult(c, writeEditInput{
			filePath:      filePath,
			resolved:      resolved,
			originalFile:  content,
			actualOld:     "",
			actualNew:     requestedNew,
			newContent:    requestedNew,
			read:          &read,
			replaceAll:    replaceAll,
			matchAttempts: 0,
		})
	}

	if strings.HasSuffix(resolved, ".ipynb") {
		return nil, &Failure{ErrorCode: EditNotebookFile, Message: "File is a Jupyter Notebook. Use the NotebookEdit to edit this file."}
	}

	if failure := editableReadStateFailure(resolved, read, c.ReadState); failure != nil {
		return nil, failure
	}

	match := FindEditMatch(content, oldNorm, replaceAll)
	if match.Status == "not_found" {
		return nil, &Failure{ErrorCode: EditOldStringNotFound, Message: "String to replace not found in file.\nString: " + oldString}
	}
	if match.Status == "ambiguous" {
		return nil, &Failure{ErrorCode: EditAmbiguousReplace, Message: createAmbiguousEditMessage(match.CandidateCount, oldString)}
	}

	actualOld := match.ActualString
	matchCount := countOccurrences(content, actualOld)
	if !replaceAll && matchCount > 1 {
		return nil, &Failure{ErrorCode: EditAmbiguousReplace, Message: createAmbiguousEditMessage(matchCount, oldString)}
	}

	normalizedNew := NormalizeReplacementForMatch(match.Strategy, requestedNew)
	actualNew := PreserveQuoteStyle(oldNorm, actualOld, normalizedNew)
	newContent := applyEditToContent(content, actualOld, actualNew, replaceAll)

	strategy := string(match.Strategy)
	count := match.CandidateCount
	return writeEditResult(c, writeEditInput{
		filePath:      filePath,
		resolved:      resolved,
		originalFile:  content,
		actualOld:     actualOld,
		actualNew:     actualNew,
		newContent:    newContent,
		read:          &read,
		replaceAll:    replaceAll,
		matchStrategy: &strategy,
		matchCount:    &count,
		matchAttempts: 1,
	})
}

// FormatEditModelContent mirrors formatEditModelContent().
func FormatEditModelContent(out *EditOutput) string {
	filePath := out.FilePath
	if filePath == "" {
		filePath = "the file"
	}
	if out.ReplaceAll {
		return "The file " + filePath + " has been updated. All occurrences were successfully replaced." + editFreshnessSuffix
	}
	return "The file " + filePath + " has been updated successfully." + editFreshnessSuffix
}

type writeEditInput struct {
	filePath      string
	resolved      string
	originalFile  string
	actualOld     string
	actualNew     string
	newContent    string
	read          *ReadText
	replaceAll    bool
	matchStrategy *string
	matchCount    *int
	matchAttempts int
}

func writeEditResult(c *Context, in writeEditInput) (*EditOutput, error) {
	contentToWrite := in.newContent
	lineEndings := ""
	encoding := ""
	if in.read != nil {
		lineEndings = in.read.LineEndings
		encoding = in.read.Encoding
	} else {
		lineEndings = detectLineEndingsString(in.originalFile)
	}
	writeResult, err := c.FS.WriteTextFile(c.Ctx, in.resolved, contentToWrite, encoding, lineEndings)
	if err != nil {
		return nil, err
	}

	_, version := readStateAfterEdit(c.ReadState, in.resolved, contentToWrite, writeResult.Revision)
	if c.RecordReadStateMetadata != nil && version != nil {
		c.RecordReadStateMetadata(*version, "Edit")
	}

	return &EditOutput{
		FilePath:            in.filePath,
		OldString:           in.actualOld,
		NewString:           in.actualNew,
		OriginalFile:        in.originalFile,
		StructuredPatch:     CreateStructuredPatch(in.filePath, in.originalFile, contentToWrite),
		UserModified:        false,
		ReplaceAll:          in.replaceAll,
		MatchStrategy:       in.matchStrategy,
		MatchCandidateCount: in.matchCount,
	}, nil
}

func readStateAfterEdit(state *ReadState, filePath, content string, rev Revision) (*ReadStateEntry, *ReadStateEntry) {
	if state == nil {
		return nil, nil
	}
	mtime := NormalizeMtimeMs(&rev.MtimeMs)
	size := rev.SizeBytes
	entry := ReadStateEntry{
		Path:          filePath,
		Content:       content,
		IsPartialView: false,
		ReadAt:        nowUTC(),
		SourceTool:    "Edit",
		RevisionID:    rev.ID,
		MtimeMs:       mtime,
		SizeBytes:     &size,
	}
	state.set(CreateReadStateKey(filePath, ToIntPtr(1), nil), entry)
	return &entry, &entry
}

func editableReadStateFailure(filePath string, current ReadText, state *ReadState) *Failure {
	if state == nil {
		return nil
	}
	lastRead, ok := state.FindLatest(filePath)
	if !ok || lastRead.IsPartialView {
		return &Failure{ErrorCode: EditFileNotRead, Message: editNotReadMessage}
	}
	if !hasReadStateChangedEdit(lastRead, current) {
		return nil
	}
	if isStrictFullRead(lastRead) && lastRead.Content == current.Content {
		return nil
	}
	return &Failure{ErrorCode: EditStaleFile, Message: editStaleMessage}
}

// hasReadStateChangedEdit mirrors the Edit variant in edit.ts: mtime/size first,
// then revision id.
func hasReadStateChangedEdit(lastRead ReadStateEntry, current ReadText) bool {
	currentMtime := current.Revision.MtimeMs
	if lastRead.MtimeMs != nil {
		nc := NormalizeMtimeMs(&currentMtime)
		nl := lastRead.MtimeMs
		mtimeAdvanced := nc != nil && nl != nil && *nc > *nl
		sizeChanged := lastRead.SizeBytes == nil || *lastRead.SizeBytes != current.SizeBytes
		return mtimeAdvanced || sizeChanged
	}
	if lastRead.SizeBytes != nil && *lastRead.SizeBytes != current.SizeBytes {
		return true
	}
	return lastRead.RevisionID != "" && current.Revision.ID != "" && lastRead.RevisionID != current.Revision.ID
}

func countOccurrences(content, needle string) int {
	if needle == "" {
		return 0
	}
	count := 0
	position := 0
	for position < len(content) {
		index := strings.Index(content[position:], needle)
		if index == -1 {
			break
		}
		count++
		position = position + index + len(needle)
	}
	return count
}

func createAmbiguousEditMessage(matchCount int, oldString string) string {
	if matchCount > 0 {
		return "Found " + itoaInt(matchCount) + " matches of the string to replace, but replace_all is false. To replace all occurrences, set replace_all to true. To replace only one occurrence, please provide more context to uniquely identify the instance.\nString: " + oldString
	}
	return "old_string is not unique in the file. Provide more surrounding context or set replace_all to true."
}

// applyEditToContent mirrors applyEditToContent().
func applyEditToContent(content, oldString, newString string, replaceAll bool) string {
	replacement := newString
	if replacement != "" {
		return replaceLiteral(content, oldString, replacement, replaceAll)
	}
	search := oldString
	if !strings.HasSuffix(oldString, "\n") && strings.Contains(content, oldString+"\n") {
		search = oldString + "\n"
	}
	return replaceLiteral(content, search, replacement, replaceAll)
}

func replaceLiteral(content, search, replacement string, replaceAll bool) string {
	if search == "" {
		return content
	}
	if replaceAll {
		return strings.ReplaceAll(content, search, replacement)
	}
	index := strings.Index(content, search)
	if index == -1 {
		return content
	}
	return content[:index] + replacement + content[index+len(search):]
}

func detectLineEndingsString(content string) string {
	crlf, lf := 0, 0
	for i := 0; i < len(content); i++ {
		if content[i] != '\n' {
			continue
		}
		if i > 0 && content[i-1] == '\r' {
			crlf++
		} else {
			lf++
		}
	}
	if crlf > lf {
		return "CRLF"
	}
	return "LF"
}

// boolField mirrors the semanticBoolean() preprocessor in contracts/tools/edit.ts:
// booleans pass through, numbers 1/0 map, and the strings true/1/yes/y/on and
// false/0/no/n/off (trimmed, lowercased) coerce.
func boolField(input map[string]any, key string) bool {
	v, ok := input[key]
	if !ok || v == nil {
		return false
	}
	switch n := v.(type) {
	case bool:
		return n
	case float64:
		return n == 1
	case int:
		return n == 1
	case string:
		switch strings.ToLower(strings.TrimSpace(n)) {
		case "true", "1", "yes", "y", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}
