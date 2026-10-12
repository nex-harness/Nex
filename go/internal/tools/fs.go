// Package tools holds the logic of the Read, Write and Edit agent tools: input
// validation, path policy, the edit matcher cascade, unified-diff hunks, the
// read-before-write guard and the model-visible result text.
//
// It never imports os: real filesystem access goes through the FS interface,
// implemented by go/internal/adapters/fsx, so the logic is testable and the
// architecture rule (I/O only in adapters) holds.
package tools

import (
	"context"
	"errors"
)

// Kind mirrors fsx.Kind without importing the adapter.
type Kind string

const (
	KindFile      Kind = "file"
	KindDirectory Kind = "directory"
	KindSymlink   Kind = "symlink"
	KindOther     Kind = "other"
)

// Revision mirrors FileSystemRevision.
type Revision struct {
	ID        string
	MtimeMs   float64
	SizeBytes int64
}

// Stat mirrors FileSystemStatResult.
type Stat struct {
	Path      string
	Kind      Kind
	SizeBytes int64
	MtimeMs   float64
	Revision  *Revision
}

// ReadRange mirrors FileSystemReadTextRangeResult.
type ReadRange struct {
	Content     string
	Encoding    string
	LineEndings string
	BytesRead   int64
	SizeBytes   int64
	StartLine   int
	LineCount   int
	TotalLines  int
	Revision    Revision
}

// ReadText mirrors FileSystemReadTextResult.
type ReadText struct {
	Content     string
	Encoding    string
	LineEndings string
	BytesRead   int64
	SizeBytes   int64
	Truncated   bool
	Revision    Revision
}

// WriteResult mirrors FileSystemWriteTextResult.
type WriteResult struct {
	BytesWritten int64
	Revision     Revision
}

// Error mirrors FileSystemPortError.
type Error struct {
	Code    string
	Path    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// FS is the filesystem boundary the tools depend on. Every method takes a
// context so cancellation reaches the adapter, mirroring the AbortSignal the Node
// handlers pass to the FileSystemPort.
type FS interface {
	Stat(ctx context.Context, path string) (Stat, error)
	ReadTextRange(ctx context.Context, path string, offsetLine, limitLines *int, maxBytes int64) (ReadRange, error)
	ReadTextFile(ctx context.Context, path string, maxBytes int64) (ReadText, error)
	ListDirectory(ctx context.Context, path string) ([]string, error)
	// WriteTextFile writes atomically, encoding content with `encoding`
	// ("" means utf8) after applying lineEndings ("" leaves content untouched).
	// The adapter preserves the existing file mode.
	WriteTextFile(ctx context.Context, path, content, encoding, lineEndings string) (WriteResult, error)
}

// ErrorCodeOf returns the FileSystemPortError code for err, or "".
func ErrorCodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
