// artifactsource.go ports conversationShareArtifactSource.ts: the local
// artifact reader/stat seam. The remote variant (SSH/WSL/Docker through
// IFileService) stays with the remote backends (Phase 5) — only the local
// source is owned by this package.
//
// Security: the ref comes from session data and is NOT trusted. Both stat and
// read resolve symlinks (realpath) and re-verify the workspace boundary AFTER
// the resolution, so a symlink planted inside the workspace cannot make the
// share read an arbitrary local file. Only ENOENT/ENOTDIR/EISDIR degrade to
// artifact_read_failed (skippable); permission errors stay blocking.
package conversationshare

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ArtifactStat ports ConversationShareArtifactStat.
type ArtifactStat struct {
	CanonicalPath string
	Size          int64
	MtimeMs       *int64
}

// MaterializedArtifact ports ConversationShareMaterializedArtifact.
type MaterializedArtifact struct {
	Bytes         []byte
	CanonicalPath string
}

// ArtifactSource is the read/stat seam (ConversationShareArtifactSource).
type ArtifactSource interface {
	Stat(ctx context.Context, workspacePath, ref string) (ArtifactStat, error)
	Read(ctx context.Context, workspacePath, ref string, maxBytes int64) (MaterializedArtifact, error)
}

// urlSchemeRe matches the "ref must be a path, not a URL" guard.
var urlSchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z\d+.-]*://`)

// LocalArtifactSource is createLocalConversationShareArtifactSource.
type LocalArtifactSource struct{}

// NewLocalArtifactSource builds the local source.
func NewLocalArtifactSource() *LocalArtifactSource { return &LocalArtifactSource{} }

func skippableReadError(err error) bool {
	var pathError *fs.PathError
	if errors.As(err, &pathError) {
		switch pathError.Err.Error() {
		case "no such file or directory", "not a directory", "is a directory":
			return true
		}
	}
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		switch syscallErr.Err.Error() {
		case "no such file or directory", "not a directory", "is a directory":
			return true
		}
	}
	return false
}

// errnoString extracts the stable errno-ish label for diagnostics.
func errnoString(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "ENOENT"
	}
	if errors.Is(err, errNotFile{}) {
		return "ENOTDIR"
	}
	if errors.Is(err, fs.ErrInvalid) {
		return "EINVAL"
	}
	return ""
}

// unreadableArtifactError ports unreadableArtifactError: 统一带上
// artifact_read_failed 与底层 errno，路径不进错误载荷。
func unreadableArtifactError(message string, cause error) *serviceError {
	diagnostics := map[string]any{}
	if errno := errnoString(cause); errno != "" {
		diagnostics["errno"] = errno
	}
	details := errorDetails{ReasonCode: ReasonArtifactReadFailed}
	if len(diagnostics) > 0 {
		details.Diagnostics = diagnostics
	}
	return newServiceError(KindInvalidConversation, message, details)
}

// Stat ports the local stat.
func (s *LocalArtifactSource) Stat(ctx context.Context, workspacePath, ref string) (ArtifactStat, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactStat{}, err
	}
	if urlSchemeRe.MatchString(ref) {
		return ArtifactStat{}, newServiceError(KindUnsafeStructure, "Conversation artifact source must be a local workspace path", errorDetails{})
	}
	stat, err := statArtifact(workspacePath, ref)
	if err != nil {
		if skippableReadError(err) {
			return ArtifactStat{}, unreadableArtifactError("Conversation artifact cannot be read", err)
		}
		return ArtifactStat{}, err
	}
	return stat, nil
}

// Read ports the local read.
func (s *LocalArtifactSource) Read(ctx context.Context, workspacePath, ref string, maxBytes int64) (MaterializedArtifact, error) {
	if err := ctx.Err(); err != nil {
		return MaterializedArtifact{}, err
	}
	if maxBytes <= 0 {
		return MaterializedArtifact{}, newServiceError(KindInvalidContract, "Conversation artifact byte limit is invalid", errorDetails{})
	}
	if urlSchemeRe.MatchString(ref) {
		return MaterializedArtifact{}, newServiceError(KindUnsafeStructure, "Conversation artifact source must be a local workspace path", errorDetails{})
	}
	info, err := resolveArtifact(workspacePath, ref)
	if err != nil {
		if skippableReadError(err) {
			return MaterializedArtifact{}, unreadableArtifactError("Conversation artifact cannot be read", err)
		}
		return MaterializedArtifact{}, err
	}
	if !info.mode.IsRegular() {
		return MaterializedArtifact{}, newServiceError(KindInvalidConversation, "Conversation artifact source is not a file", errorDetails{})
	}
	if info.size > maxBytes {
		return MaterializedArtifact{}, newServiceError(KindLimitExceeded, "Conversation artifact exceeds the byte limit", errorDetails{})
	}
	// 按 stat 大小 + 1 字节读：仍能识别 stat/read 间的文件增长。
	buffer := make([]byte, info.size+1)
	file, err := os.Open(info.canonicalPath)
	if err != nil {
		if skippableReadError(err) {
			return MaterializedArtifact{}, unreadableArtifactError("Conversation artifact cannot be read", err)
		}
		return MaterializedArtifact{}, err
	}
	defer file.Close()
	bytesRead := 0
	for bytesRead < len(buffer) {
		read, err := file.Read(buffer[bytesRead:])
		if read > 0 {
			bytesRead += read
		}
		if err != nil {
			break
		}
	}
	if int64(bytesRead) > maxBytes {
		return MaterializedArtifact{}, newServiceError(KindLimitExceeded, "Conversation artifact exceeds the byte limit", errorDetails{})
	}
	return MaterializedArtifact{Bytes: buffer[:bytesRead], CanonicalPath: info.canonicalPath}, nil
}

type resolvedArtifact struct {
	canonicalPath string
	size          int64
	mode          fs.FileMode
	mtimeMs       int64
}

func statArtifact(workspacePath, ref string) (ArtifactStat, error) {
	info, err := resolveArtifact(workspacePath, ref)
	if err != nil {
		return ArtifactStat{}, err
	}
	if !info.mode.IsRegular() {
		return ArtifactStat{}, unreadableArtifactError("Conversation artifact source is not a file", errNotFile{})
	}
	mtimeMs := info.mtimeMs
	return ArtifactStat{CanonicalPath: info.canonicalPath, Size: info.size, MtimeMs: &mtimeMs}, nil
}

type errNotFile struct{}

func (errNotFile) Error() string { return "not a file" }

// resolveArtifact ports the realpath + boundary check sequence.
func resolveArtifact(workspacePath, ref string) (resolvedArtifact, error) {
	workspaceRealPath, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return resolvedArtifact{}, err
	}
	requestedPath := ref
	if !filepath.IsAbs(requestedPath) {
		requestedPath = filepath.Join(workspacePath, ref)
	}
	artifactRealPath, err := filepath.EvalSymlinks(requestedPath)
	if err != nil {
		return resolvedArtifact{}, err
	}
	// artifact Row 的 ref 来自会话数据；必须在跟随符号链接后再次验证边界。
	if !isInsideWorkspace(workspaceRealPath, artifactRealPath) {
		return resolvedArtifact{}, newServiceError(KindUnsafeStructure, "Conversation artifact is outside the workspace", errorDetails{})
	}
	info, err := os.Stat(artifactRealPath)
	if err != nil {
		return resolvedArtifact{}, err
	}
	return resolvedArtifact{
		canonicalPath: artifactRealPath, size: info.Size(), mode: info.Mode(),
		mtimeMs: info.ModTime().UnixMilli(),
	}, nil
}

// isInsideWorkspace ports isInsideWorkspace.
func isInsideWorkspace(workspacePath, targetPath string) bool {
	relative, err := filepath.Rel(workspacePath, targetPath)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
