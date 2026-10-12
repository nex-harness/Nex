// Package fsx is the filesystem adapter: the only place in the tools stack that
// touches the real filesystem. Tools depend on the small FS interface in
// go/internal/tools, so their logic stays testable and free of os imports
// (enforced by internal/archcheck).
//
// Behaviour mirrors apps/nex-cli/packages/adapters/src/fs/{index,text-range-reader,
// text-metadata}.ts: revision ids are `mtime:<int ms>:size:<bytes>`, reads are
// bounded, writes are atomic (temp file + rename in the same directory) and
// preserve the existing file mode.
package fsx

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// Kind mirrors FileSystemNodeKind.
type Kind string

const (
	KindFile      Kind = "file"
	KindDirectory Kind = "directory"
	KindSymlink   Kind = "symlink"
	KindOther     Kind = "other"
)

// Revision mirrors FileSystemRevision (no hash: the tools only compare id/mtime/size).
type Revision struct {
	ID        string
	MtimeMs   float64
	SizeBytes int64
}

// StatResult mirrors FileSystemStatResult.
type StatResult struct {
	Path      string
	Kind      Kind
	SizeBytes int64
	MtimeMs   float64
	Revision  *Revision
}

// ReadTextRangeResult mirrors FileSystemReadTextRangeResult.
type ReadTextRangeResult struct {
	Content     string
	Encoding    string
	LineEndings string // "LF" | "CRLF" | "" (non-text encodings)
	BytesRead   int64
	SizeBytes   int64
	StartLine   int
	LineCount   int
	TotalLines  int
	Revision    Revision
}

// ReadTextResult mirrors FileSystemReadTextResult.
type ReadTextResult struct {
	Content     string
	Encoding    string
	LineEndings string
	BytesRead   int64
	SizeBytes   int64
	Truncated   bool
	Revision    Revision
}

// WriteTextResult mirrors FileSystemWriteTextResult.
type WriteTextResult struct {
	BytesWritten int64
	Revision     Revision
}

// Error mirrors FileSystemPortError: a stable Code the tools branch on.
type Error struct {
	Code    string
	Path    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// CodeOf returns the FileSystemPortError code for err, or "" when err is not one.
func CodeOf(err error) string {
	var fsErr *Error
	if errors.As(err, &fsErr) {
		return fsErr.Code
	}
	return ""
}

// FS is the adapter's concrete implementation.
type FS struct{}

// New returns the real-filesystem adapter.
func New() *FS { return &FS{} }

// RevisionID builds the id used by both Node and Go: `mtime:<trunc ms>:size:<bytes>`.
func revisionID(mtimeMs float64, sizeBytes int64) string {
	return "mtime:" + strconv.FormatInt(int64(mtimeMs), 10) + ":size:" + strconv.FormatInt(sizeBytes, 10)
}

// Stat mirrors NodeFileSystemAdapter.stat.
func (fs *FS) Stat(path string) (StatResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return StatResult{}, toError(err, path)
	}
	kind := kindOf(info)
	res := StatResult{Path: path, Kind: kind, SizeBytes: info.Size(), MtimeMs: mtimeMs(info)}
	if kind == KindFile {
		rev := Revision{ID: revisionID(res.MtimeMs, res.SizeBytes), MtimeMs: res.MtimeMs, SizeBytes: res.SizeBytes}
		res.Revision = &rev
	}
	return res, nil
}

func mtimeMs(info os.FileInfo) float64 {
	// Node reports fractional milliseconds; Go's ModTime has nanoseconds, so keep
	// the fraction to match Math.floor on the Node side exactly.
	return float64(info.ModTime().UnixNano()) / 1e6
}

func kindOf(info os.FileInfo) Kind {
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return KindSymlink
	case mode.IsDir():
		return KindDirectory
	case mode.IsRegular():
		return KindFile
	default:
		return KindOther
	}
}

func toError(err error, path string) error {
	if err == nil {
		return nil
	}
	var fsErr *Error
	if errors.As(err, &fsErr) {
		return fsErr
	}
	if errors.Is(err, os.ErrNotExist) {
		return &Error{Code: "not_found", Path: path, Message: "File not found: " + path}
	}
	if errors.Is(err, os.ErrPermission) {
		return &Error{Code: "permission_denied", Path: path, Message: "Permission denied: " + path}
	}
	return &Error{Code: "io_error", Path: path, Message: nodeErrnoMessage(err, path)}
}

// nodeErrnoMessage mirrors toFileSystemError() in adapters/src/fs/index.ts: an errno
// Node does not map to a specific code (here anything that is not ENOENT/EACCES/
// EISDIR/ENAMETOOLONG) is surfaced with libuv's raw `"<ERRNO>: <desc>, <syscall> '<path>'"`
// text, because that string is what the model sees. Go's os.PathError text has a
// different shape, so the errno message is rebuilt here from the syscall name Node
// would have used. Errors without a PathError fall back to err.Error().
func nodeErrnoMessage(err error, path string) string {
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		return err.Error()
	}
	errno, ok := errnoOf(pathErr.Err)
	if !ok {
		return err.Error()
	}
	code, desc, known := errnoMeta(errno)
	if !known {
		return err.Error()
	}
	op := pathErr.Op
	if op == "" {
		op = "stat"
	}
	return code + ": " + desc + ", " + op + " '" + pathErr.Path + "'"
}

// ReadTextFile reads a whole text file (bounded by maxBytes when > 0).
func (fs *FS) ReadTextFile(path string, maxBytes int64) (ReadTextResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ReadTextResult{}, toError(err, path)
	}
	if info.IsDir() {
		return ReadTextResult{}, &Error{Code: "is_directory", Path: path, Message: "Cannot read directory as text file: " + path}
	}
	if !info.Mode().IsRegular() {
		return ReadTextResult{}, &Error{Code: "not_file", Path: path, Message: "Cannot read non-file path as text: " + path}
	}
	truncated := maxBytes > 0 && info.Size() > maxBytes
	var buf []byte
	if truncated {
		f, openErr := os.Open(path)
		if openErr != nil {
			return ReadTextResult{}, toError(openErr, path)
		}
		defer f.Close()
		limited := io.LimitReader(f, maxBytes)
		buf, err = io.ReadAll(limited)
		if err != nil {
			return ReadTextResult{}, toError(err, path)
		}
	} else {
		buf, err = os.ReadFile(path)
		if err != nil {
			return ReadTextResult{}, toError(err, path)
		}
	}
	decoded, encoding, decErr := decodeTextBuffer(buf, path)
	if decErr != nil {
		return ReadTextResult{}, decErr
	}
	isText := shouldNormalizeLineEndings(encoding)
	rawContent := decoded
	content := rawContent
	lineEndings := ""
	if isText {
		content = normalizeLineEndings(rawContent)
		lineEndings = detectLineEndings(rawContent)
	}
	rev := Revision{ID: revisionID(mtimeMs(info), info.Size()), MtimeMs: mtimeMs(info), SizeBytes: info.Size()}
	return ReadTextResult{
		Content:     content,
		Encoding:    encoding,
		LineEndings: lineEndings,
		BytesRead:   int64(len(buf)),
		SizeBytes:   info.Size(),
		Truncated:   truncated,
		Revision:    rev,
	}, nil
}

// ListDirectory mirrors FileSystemPort.listDirectory for the Read/Edit
// "did you mean" suggestion (files and symlinks only, names sorted by the caller).
func (fs *FS) ListDirectory(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, toError(err, path)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		k := kindOf(info)
		if k == KindFile || k == KindSymlink {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// WriteTextFile mirrors NodeFileSystemAdapter.writeTextFile (atomic path).
// lineEndings "CRLF"/"LF" re-applies the requested endings; "" leaves content as
// given. existingMode, when non-nil, is the mode to preserve on the target file.
func (fs *FS) WriteTextFile(path, content, encoding, lineEndings string, existingMode *os.FileMode) (WriteTextResult, error) {
	if lineEndings != "" {
		content = applyRequestedLineEndings(content, lineEndings)
	}
	buf, err := encodeTextContent(content, encoding)
	if err != nil {
		return WriteTextResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return WriteTextResult{}, toError(err, path)
	}
	if err := atomicWrite(path, buf, existingMode); err != nil {
		return WriteTextResult{}, toError(err, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return WriteTextResult{}, toError(err, path)
	}
	rev := Revision{ID: revisionID(mtimeMs(info), info.Size()), MtimeMs: mtimeMs(info), SizeBytes: info.Size()}
	return WriteTextResult{BytesWritten: int64(len(buf)), Revision: rev}, nil
}

// FileMode returns the mode of an existing file, or nil when it does not exist.
func (fs *FS) FileMode(path string) (*os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, toError(err, path)
	}
	m := info.Mode()
	return &m, nil
}
