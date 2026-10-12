package fsx

import (
	"context"

	"github.com/tylinux/nex/go/internal/tools"
)

// Port adapts *FS to the tools.FS interface (tools keeps its own value types so
// the logic package stays free of the adapter). Adapters depending on the
// business package's interfaces is the normal direction; tools never imports fsx.
type Port struct{ inner *FS }

// NewPort returns the tools.FS implementation backed by the real filesystem.
func NewPort() *Port { return &Port{inner: New()} }

func (p *Port) Stat(ctx context.Context, path string) (tools.Stat, error) {
	if err := ctx.Err(); err != nil {
		return tools.Stat{}, cancelledError(err)
	}
	s, err := p.inner.Stat(path)
	if err != nil {
		return tools.Stat{}, toToolsError(err)
	}
	out := tools.Stat{Path: s.Path, Kind: tools.Kind(s.Kind), SizeBytes: s.SizeBytes, MtimeMs: s.MtimeMs}
	if s.Revision != nil {
		out.Revision = &tools.Revision{ID: s.Revision.ID, MtimeMs: s.Revision.MtimeMs, SizeBytes: s.Revision.SizeBytes}
	}
	return out, nil
}

func (p *Port) ReadTextRange(ctx context.Context, path string, offsetLine, limitLines *int, maxBytes int64) (tools.ReadRange, error) {
	if err := ctx.Err(); err != nil {
		return tools.ReadRange{}, cancelledError(err)
	}
	r, err := p.inner.ReadTextRange(path, offsetLine, limitLines, maxBytes)
	if err != nil {
		return tools.ReadRange{}, toToolsError(err)
	}
	return tools.ReadRange{
		Content:     r.Content,
		Encoding:    r.Encoding,
		LineEndings: r.LineEndings,
		BytesRead:   r.BytesRead,
		SizeBytes:   r.SizeBytes,
		StartLine:   r.StartLine,
		LineCount:   r.LineCount,
		TotalLines:  r.TotalLines,
		Revision:    tools.Revision{ID: r.Revision.ID, MtimeMs: r.Revision.MtimeMs, SizeBytes: r.Revision.SizeBytes},
	}, nil
}

func (p *Port) ReadTextFile(ctx context.Context, path string, maxBytes int64) (tools.ReadText, error) {
	if err := ctx.Err(); err != nil {
		return tools.ReadText{}, cancelledError(err)
	}
	r, err := p.inner.ReadTextFile(path, maxBytes)
	if err != nil {
		return tools.ReadText{}, toToolsError(err)
	}
	return tools.ReadText{
		Content:     r.Content,
		Encoding:    r.Encoding,
		LineEndings: r.LineEndings,
		BytesRead:   r.BytesRead,
		SizeBytes:   r.SizeBytes,
		Truncated:   r.Truncated,
		Revision:    tools.Revision{ID: r.Revision.ID, MtimeMs: r.Revision.MtimeMs, SizeBytes: r.Revision.SizeBytes},
	}, nil
}

func (p *Port) ListDirectory(ctx context.Context, path string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, cancelledError(err)
	}
	names, err := p.inner.ListDirectory(path)
	if err != nil {
		return nil, toToolsError(err)
	}
	return names, nil
}

func (p *Port) WriteTextFile(ctx context.Context, path, content, encoding, lineEndings string) (tools.WriteResult, error) {
	if err := ctx.Err(); err != nil {
		return tools.WriteResult{}, cancelledError(err)
	}
	mode, err := p.inner.FileMode(path)
	if err != nil {
		return tools.WriteResult{}, toToolsError(err)
	}
	r, err := p.inner.WriteTextFile(path, content, encoding, lineEndings, mode)
	if err != nil {
		return tools.WriteResult{}, toToolsError(err)
	}
	return tools.WriteResult{
		BytesWritten: r.BytesWritten,
		Revision:     tools.Revision{ID: r.Revision.ID, MtimeMs: r.Revision.MtimeMs, SizeBytes: r.Revision.SizeBytes},
	}, nil
}

// cancelledError mirrors FileSystemPortError code "cancelled".
func cancelledError(err error) error {
	return &tools.Error{Code: "cancelled", Message: "File system operation was cancelled: " + err.Error()}
}

func toToolsError(err error) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return &tools.Error{Code: e.Code, Path: e.Path, Message: e.Message}
	}
	return err
}
