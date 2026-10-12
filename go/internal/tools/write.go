package tools

const (
	writeStorageSuffix  = " (file state is current in your context \u2014 no need to Read it back)"
	writeNotReadMessage = "File has not been read yet. Read it first before writing to it."
	writeStaleMessage   = "File has been modified since read, either by the user or by a linter. Read it again before attempting to write it."
)

// WriteOutput mirrors WriteOutput for the fields the Go port produces.
type WriteOutput struct {
	Type            string     `json:"type"` // create | update
	FilePath        string     `json:"filePath"`
	Content         string     `json:"content"`
	StructuredPatch []DiffHunk `json:"structuredPatch"`
	OriginalFile    *string    `json:"originalFile"`
	UserModified    bool       `json:"userModified"`
}

// Write mirrors handlers/write.ts: read the existing file, enforce the
// read-before-write freshness guard, then write atomically preserving encoding,
// line endings and mode.
func Write(input map[string]any, c *Context) (*WriteOutput, error) {
	filePath, err := stringField(input, "file_path")
	if err != nil {
		return nil, err
	}
	content, err := stringField(input, "content")
	if err != nil {
		return nil, err
	}

	resolved, err := ResolveWorkspacePath(filePath, c.WorkingDirectory, c.WorkspaceRoot, "write")
	if err != nil {
		return nil, err
	}

	var originalFile *string
	var originalEncoding string
	var originalLineEndings string
	read, readErr := c.FS.ReadTextFile(c.Ctx, resolved, 0)
	if readErr == nil {
		if failure := assertWritableExistingFileIsFresh(resolved, read, c.ReadState); failure != nil {
			return nil, failure
		}
		s := read.Content
		originalFile = &s
		originalEncoding = read.Encoding
		originalLineEndings = read.LineEndings
	} else if fsErrorCode(readErr) != "not_found" {
		return nil, readErr
	}

	contentToWrite := content
	writeResult, err := c.FS.WriteTextFile(c.Ctx, resolved, contentToWrite, originalEncoding, originalLineEndings)
	if err != nil {
		return nil, err
	}
	_ = originalEncoding

	_, version := readStateAfterWrite(c.ReadState, resolved, contentToWrite, writeResult.Revision)
	if c.RecordReadStateMetadata != nil && version != nil {
		c.RecordReadStateMetadata(*version, "Write")
	}

	out := &WriteOutput{FilePath: filePath, Content: contentToWrite, UserModified: false}
	if originalFile != nil {
		out.Type = "update"
		out.OriginalFile = originalFile
		out.StructuredPatch = CreateStructuredPatch(filePath, *originalFile, contentToWrite)
	} else {
		out.Type = "create"
		out.StructuredPatch = []DiffHunk{}
	}
	return out, nil
}

// FormatWriteModelContent mirrors formatWriteModelContent().
func FormatWriteModelContent(out *WriteOutput) string {
	filePath := out.FilePath
	if filePath == "" {
		filePath = "the file"
	}
	if out.Type == "create" {
		return "File created successfully at: " + filePath + writeStorageSuffix
	}
	return "The file " + filePath + " has been updated successfully." + writeStorageSuffix
}

// assertWritableExistingFileIsFresh mirrors assertWritableExistingFileIsFresh().
func assertWritableExistingFileIsFresh(filePath string, current ReadText, state *ReadState) *Failure {
	lastRead, ok := state.FindLatest(filePath)
	if !ok || lastRead.IsPartialView {
		return &Failure{ErrorCode: writeFileNotRead, Message: writeNotReadMessage}
	}
	if !hasReadStateChanged(lastRead, current) {
		return nil
	}
	if isStrictFullRead(lastRead) && lastRead.Content == current.Content {
		return nil
	}
	return &Failure{ErrorCode: writeFileStale, Message: writeStaleMessage}
}

const (
	writeFileNotRead = 1
	writeFileStale   = 2
)

// hasReadStateChanged mirrors the Write variant in write.ts (revision id first,
// then mtime/size).
func hasReadStateChanged(lastRead ReadStateEntry, current ReadText) bool {
	currentRevisionID := current.Revision.ID
	if lastRead.RevisionID != "" && currentRevisionID != "" && lastRead.RevisionID != currentRevisionID {
		return true
	}
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
	return isStrictFullRead(lastRead) && lastRead.Content != current.Content
}

func readStateAfterWrite(state *ReadState, filePath, content string, rev Revision) (*ReadStateEntry, *ReadStateEntry) {
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
		SourceTool:    "Write",
		RevisionID:    rev.ID,
		MtimeMs:       mtime,
		SizeBytes:     &size,
	}
	state.set(CreateReadStateKey(filePath, ToIntPtr(1), nil), entry)
	return &entry, &entry
}
