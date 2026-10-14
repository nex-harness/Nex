package file

// codec.go is the byte-exact Go port of packages/shared/src/
// workspaceFileEntriesCodec.ts — the columnar pack/unpack of workspace file
// entries used by listWorkspaceFilesLength/listWorkspaceFilesRange. The wire
// format is `type\trelativePath` lines with `\\`, `\t`, `\n` escapes; name is
// the last relativePath segment and path is rootPath + separator + parts.
//
// The TS escape order matters: backslashes are doubled FIRST, so a literal
// `\t` (backslash + t) becomes `\\t` and round-trips, while a real tab becomes
// `\t`. The unescape collapses every `\<char>` pair (`\\.` in the TS).
// Vectors: ignore_codec_test.go round-trips every entry shape plus the
// captures under testdata/services/file/*listWorkspaceFilesRange*.

import (
	"strings"
)

const (
	fieldSeparator = "\t"
	lineSeparator  = "\n"
)

// WorkspaceFileEntriesChunkSize is WORKSPACE_FILE_ENTRIES_CHUNK_SIZE: the
// caller-side chunk target in CHARACTERS (UTF-16 code units in the TS; the
// Go port counts bytes, which is safe because the chunk is sliced at a line
// boundary by the CALLER, not by this constant).
const WorkspaceFileEntriesChunkSize = 4_000_000

// workspaceEntry mirrors WorkspaceFileEntry (packages/shared/src/protocol.ts).
type workspaceEntry struct {
	Name         string
	Path         string
	RelativePath string
	Type         string // "file" | "directory"
}

// escapeField ports escapeField: backslashes doubled, then tab and newline
// given letter escapes.
func escapeField(value string) string {
	r := strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`)
	return r.Replace(value)
}

// unescapeField ports unescapeField: `\\.` -> the second byte, with t/n
// mapped back to tab/newline.
func unescapeField(value string) string {
	if !strings.ContainsRune(value, '\\') {
		return value
	}
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) {
			i++
			switch value[i] {
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			default:
				b.WriteByte(value[i])
			}
			continue
		}
		b.WriteByte(value[i])
	}
	return b.String()
}

// packWorkspaceFileEntries ports packWorkspaceFileEntries: one
// `type\trelativePath` line per entry, joined by newlines (no trailing
// newline; an empty list packs to "").
func packWorkspaceFileEntries(entries []workspaceEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString(lineSeparator)
		}
		b.WriteString(e.Type)
		b.WriteString(fieldSeparator)
		b.WriteString(escapeField(e.RelativePath))
	}
	return b.String()
}

// unpackWorkspaceFileEntries ports unpackWorkspaceFileEntries: name is the
// last relativePath segment; path joins rootPath with `\` when rootPath
// contains a backslash (Windows), `/` otherwise, tolerating a rootPath that
// already ends with a separator.
func unpackWorkspaceFileEntries(packed, rootPath string) []workspaceEntry {
	if packed == "" {
		return nil
	}
	separator := "/"
	if strings.Contains(rootPath, `\`) {
		separator = `\`
	}
	prefix := rootPath
	if !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, `\`) {
		prefix += separator
	}
	lines := strings.Split(packed, lineSeparator)
	entries := make([]workspaceEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		tabAt := strings.IndexByte(line, '\t')
		if tabAt == -1 {
			continue
		}
		relativePath := unescapeField(line[tabAt+1:])
		name := relativePath
		if lastSlash := strings.LastIndexByte(relativePath, '/'); lastSlash != -1 {
			name = relativePath[lastSlash+1:]
		}
		pathPart := relativePath
		if separator == `\` {
			pathPart = strings.ReplaceAll(relativePath, "/", `\`)
		}
		entries = append(entries, workspaceEntry{
			Name:         name,
			Path:         prefix + pathPart,
			RelativePath: relativePath,
			Type:         line[:tabAt],
		})
	}
	return entries
}
