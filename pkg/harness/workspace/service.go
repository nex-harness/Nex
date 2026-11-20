package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nex-harness/nex/pkg/harness/tools"
)

const (
	defaultPreviewLimitBytes = 256 * 1024
	maxImagePreviewBytes     = 8 * 1024 * 1024
)

// Request dispatches workspace FS and Git Review actions on a Runner.
type Request struct {
	Action        string `json:"action"` // "fs_list" | "fs_browse_dirs" | "fs_search" | "fs_read_preview" | "git_summary" | "git_diff"
	WorkspacePath string `json:"workspace_path,omitempty"`
	RelPath       string `json:"rel_path,omitempty"`
	Query         string `json:"query,omitempty"`
	DiffMode      string `json:"diff_mode,omitempty"` // "working" | "staged" | "unstaged" | "head"
	Limit         int    `json:"limit,omitempty"`
	IncludeHidden bool   `json:"include_hidden,omitempty"`
}

// BrowseDirEntry describes a subdirectory in the host/runner directory browser.
type BrowseDirEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsSymlink bool   `json:"is_symlink,omitempty"`
}

// BrowseDirsResult returns the current browsed directory, parent, home dir, and subdirectories.
type BrowseDirsResult struct {
	CurrentPath string           `json:"current_path"`
	ParentPath  string           `json:"parent_path"`
	HomePath    string           `json:"home_path"`
	Entries     []BrowseDirEntry `json:"entries"`
}

// DirEntry describes a single node in the lazy-loaded Files tree.
type DirEntry struct {
	Name       string `json:"name"`
	RelPath    string `json:"rel_path"`
	Kind       string `json:"kind"` // "dir" | "file" | "symlink"
	Size       int64  `json:"size"`
	ModTimeMS  int64  `json:"mod_time_ms"`
	GitIgnored bool   `json:"git_ignored"`
	GitStatus  string `json:"git_status,omitempty"`
}

// FilePreview describes the content returned when opening a file in a new Preview Tab.
type FilePreview struct {
	RelPath   string `json:"rel_path"`
	Name      string `json:"name"`
	PreviewAs string `json:"preview_as"` // "text" | "markdown" | "image" | "binary"
	Language  string `json:"language,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	Content   string `json:"content,omitempty"`   // UTF-8 text or base64 image payload
	TotalSize int64  `json:"total_size"`
	Truncated bool   `json:"truncated"`
	SHA256    string `json:"sha256,omitempty"`
	ModTimeMS int64  `json:"mod_time_ms"`
}

// GitFileChange summarizes one changed file in the Review tab.
type GitFileChange struct {
	RelPath   string `json:"rel_path"`
	Status    string `json:"status"` // "M" | "A" | "D" | "R" | "?"
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Diff      string `json:"diff,omitempty"`
	Folded    bool   `json:"folded,omitempty"` // True for lockfiles / huge diffs loaded on demand
}

// GitSummary powers the [± 更改 +X -Y] capsule and Review tab header.
type GitSummary struct {
	IsGitRepo    bool            `json:"is_git_repo"`
	Branch       string          `json:"branch,omitempty"`
	ChangedFiles int             `json:"changed_files"`
	Additions    int             `json:"additions"`
	Deletions    int             `json:"deletions"`
	Files        []GitFileChange `json:"files,omitempty"`
}

// Service implements Runner-side workspace browsing, safe previewing, and Git diff inspection.
type Service struct{}

func NewService() *Service { return &Service{} }

func safeJoinWorkspace(rootDir, relPath string) (string, string, error) {
	if strings.TrimSpace(rootDir) == "" {
		return "", "", errors.New("workspace root directory is empty")
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return "", "", err
	}
	if realRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = realRoot
	}

	cleanedRel := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(relPath, "/")))
	if cleanedRel == "." {
		cleanedRel = ""
	}
	target := filepath.Join(absRoot, cleanedRel)
	// Resolve symlinks if target exists to block symlink path traversal outside workspace root!
	checkTarget := target
	if realTarget, err := filepath.EvalSymlinks(target); err == nil {
		checkTarget = realTarget
	}
	relCheck, err := filepath.Rel(absRoot, checkTarget)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes workspace root", relPath)
	}
	return absRoot, target, nil
}

// HandleRequest executes a workspace RPC request and returns marshaled JSON response bytes.
func (s *Service) HandleRequest(ctx context.Context, defaultWorkDir string, rawPayload json.RawMessage) (json.RawMessage, error) {
	var req Request
	if err := json.Unmarshal(rawPayload, &req); err != nil {
		return nil, err
	}
	root := req.WorkspacePath
	if root == "" {
		root = defaultWorkDir
	}

	var resp any
	var err error
	switch req.Action {
	case "fs_list":
		resp, err = s.ListDir(ctx, root, req.RelPath)
	case "fs_browse_dirs":
		resp, err = s.BrowseDirs(ctx, root, req.IncludeHidden)
	case "fs_search":
		resp, err = s.SearchFiles(ctx, root, req.Query, req.Limit)
	case "fs_read_preview":
		resp, err = s.ReadPreview(ctx, root, req.RelPath)
	case "git_summary":
		resp, err = s.GitSummary(ctx, root, false, "")
	case "git_diff":
		resp, err = s.GitSummary(ctx, root, true, req.RelPath)
	default:
		return nil, fmt.Errorf("unknown workspace action %q", req.Action)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(resp)
}

// BrowseDirs lists subdirectories of an arbitrary path on the Runner host so users can
// pick a project/workspace directory without typing paths manually.
func (s *Service) BrowseDirs(_ context.Context, targetPath string, includeHidden bool) (*BrowseDirsResult, error) {
	homeDir, _ := os.UserHomeDir()
	raw := strings.TrimSpace(targetPath)
	if raw == "" {
		raw = homeDir
	}
	if raw == "" {
		if wd, err := os.Getwd(); err == nil {
			raw = wd
		}
	}
	if homeDir != "" {
		if raw == "~" {
			raw = homeDir
		} else if strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, "~\\") {
			raw = filepath.Join(homeDir, raw[2:])
		}
	}
	absPath, err := filepath.Abs(raw)
	if err != nil {
		return nil, fmt.Errorf("resolve directory %q: %w", raw, err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("cannot access %q: %w", absPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path %q is not a directory", absPath)
	}

	dirEntries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("read directory %q: %w", absPath, err)
	}

	entries := make([]BrowseDirEntry, 0, len(dirEntries))
	const maxEntries = 500
	for _, d := range dirEntries {
		name := d.Name()
		if !includeHidden && strings.HasPrefix(name, ".") {
			continue
		}
		isSymlink := d.Type()&os.ModeSymlink != 0
		isDir := d.IsDir()
		fullPath := filepath.Join(absPath, name)
		if !isDir && isSymlink {
			if targetInfo, statErr := os.Stat(fullPath); statErr == nil && targetInfo.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			continue
		}
		entries = append(entries, BrowseDirEntry{
			Name:      name,
			Path:      fullPath,
			IsSymlink: isSymlink,
		})
		if len(entries) >= maxEntries {
			break
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	parent := filepath.Dir(absPath)
	if parent == absPath {
		parent = ""
	}

	return &BrowseDirsResult{
		CurrentPath: absPath,
		ParentPath:  parent,
		HomePath:    homeDir,
		Entries:     entries,
	}, nil
}

// ListDir lists a single directory level lazily, sorting folders before files and marking .gitignore status.
func (s *Service) ListDir(ctx context.Context, rootDir, relPath string) (map[string]any, error) {
	absRoot, targetDir, err := safeJoinWorkspace(rootDir, relPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return nil, err
	}

	ign := tools.LoadIgnoreMatcher(absRoot)
	gitStatuses := collectPorcelainMap(ctx, absRoot)

	out := make([]DirEntry, 0, len(entries))
	truncated := false
	const maxDirEntries = 500
	for i, d := range entries {
		if d.Name() == ".git" {
			continue
		}
		if i >= maxDirEntries {
			truncated = true
			break
		}
		entryAbs := filepath.Join(targetDir, d.Name())
		rel, _ := filepath.Rel(absRoot, entryAbs)
		relSlash := filepath.ToSlash(rel)

		kind := "file"
		if d.IsDir() {
			kind = "dir"
		} else if d.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		var size int64
		var modMS int64
		if fi, err := d.Info(); err == nil {
			size = fi.Size()
			modMS = fi.ModTime().UnixMilli()
		}

		out = append(out, DirEntry{
			Name:       d.Name(),
			RelPath:    relSlash,
			Kind:       kind,
			Size:       size,
			ModTimeMS:  modMS,
			GitIgnored: ign.IsIgnored(relSlash, d.IsDir()),
			GitStatus:  gitStatuses[relSlash],
		})
	}

	sort.Slice(out, func(i, j int) bool {
		iDir := out[i].Kind == "dir"
		jDir := out[j].Kind == "dir"
		if iDir != jDir {
			return iDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	return map[string]any{
		"entries":   out,
		"truncated": truncated,
	}, nil
}

// SearchFiles performs fast substring/fuzzy filename lookup across non-ignored workspace files.
func (s *Service) SearchFiles(ctx context.Context, rootDir, query string, limit int) (map[string]any, error) {
	absRoot, _, err := safeJoinWorkspace(rootDir, "")
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	qLower := strings.ToLower(strings.TrimSpace(query))
	ign := tools.LoadIgnoreMatcher(absRoot)
	var matches []string

	_ = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || ctx.Err() != nil {
			return walkErr
		}
		rel, err := filepath.Rel(absRoot, p)
		if err != nil || rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if ign.IsIgnored(relSlash, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if qLower == "" || strings.Contains(strings.ToLower(relSlash), qLower) {
			matches = append(matches, relSlash)
			if len(matches) >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	return map[string]any{"files": matches}, nil
}

// ReadPreview inspects file magic bytes and returns safe text, markdown, image, or binary metadata for a Preview Tab.
func (s *Service) ReadPreview(_ context.Context, rootDir, relPath string) (*FilePreview, error) {
	if strings.TrimSpace(relPath) == "" {
		return nil, errors.New("rel_path is required")
	}
	absRoot, absFile, err := safeJoinWorkspace(rootDir, relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absFile)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("cannot preview directory as file")
	}

	relClean, _ := filepath.Rel(absRoot, absFile)
	relSlash := filepath.ToSlash(relClean)
	ext := strings.ToLower(filepath.Ext(absFile))

	f, err := os.Open(absFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Check image formats first
	if isImageExt(ext) && info.Size() <= maxImagePreviewBytes {
		raw, err := os.ReadFile(absFile)
		if err != nil {
			return nil, err
		}
		return &FilePreview{
			RelPath:   relSlash,
			Name:      filepath.Base(absFile),
			PreviewAs: "image",
			MimeType:  imageMimeByExt(ext),
			Content:   base64.StdEncoding.EncodeToString(raw),
			TotalSize: info.Size(),
			ModTimeMS: info.ModTime().UnixMilli(),
		}, nil
	}

	buf := make([]byte, defaultPreviewLimitBytes)
	n, _ := f.Read(buf)
	sample := buf[:n]

	sniffLen := len(sample)
	if sniffLen > 8192 {
		sniffLen = 8192
	}
	if bytes.IndexByte(sample[:sniffLen], 0) >= 0 {
		sum := sha256.Sum256(sample)
		return &FilePreview{
			RelPath:   relSlash,
			Name:      filepath.Base(absFile),
			PreviewAs: "binary",
			TotalSize: info.Size(),
			SHA256:    hex.EncodeToString(sum[:]),
			ModTimeMS: info.ModTime().UnixMilli(),
		}, nil
	}

	sample = bytes.TrimPrefix(sample, []byte{0xEF, 0xBB, 0xBF})
	previewKind := "text"
	if ext == ".md" || ext == ".mdx" {
		previewKind = "markdown"
	}

	return &FilePreview{
		RelPath:   relSlash,
		Name:      filepath.Base(absFile),
		PreviewAs: previewKind,
		Language:  detectCodeLanguage(absFile),
		Content:   string(sample),
		TotalSize: info.Size(),
		Truncated: info.Size() > int64(defaultPreviewLimitBytes),
		ModTimeMS: info.ModTime().UnixMilli(),
	}, nil
}

// GitSummary computes branch, (+/-) totals for the capsule, and per-file diffs for the Review Tab.
func (s *Service) GitSummary(ctx context.Context, rootDir string, includeDiffs bool, singleRelPath string) (*GitSummary, error) {
	absRoot, _, err := safeJoinWorkspace(rootDir, "")
	if err != nil {
		return nil, err
	}

	branchOut, err := runGit(ctx, absRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		// Check if git init with no commits yet
		if _, stErr := os.Stat(filepath.Join(absRoot, ".git")); stErr != nil {
			return &GitSummary{IsGitRepo: false}, nil
		}
		branchOut = "HEAD"
	}

	porcelain, _ := runGit(ctx, absRoot, "status", "--porcelain", "-uall")
	if strings.TrimSpace(porcelain) == "" {
		return &GitSummary{
			IsGitRepo: true,
			Branch:    strings.TrimSpace(branchOut),
		}, nil
	}

	numstatMap := make(map[string][2]int)
	numstatOut, _ := runGit(ctx, absRoot, "diff", "HEAD", "--numstat")
	if numstatOut == "" {
		numstatOut, _ = runGit(ctx, absRoot, "diff", "--numstat")
	}
	for _, line := range strings.Split(numstatOut, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) >= 3 {
			a, _ := strconv.Atoi(parts[0])
			d, _ := strconv.Atoi(parts[1])
			numstatMap[parts[2]] = [2]int{a, d}
		}
	}

	var files []GitFileChange
	totalAdds, totalDels := 0, 0

	for _, line := range strings.Split(strings.TrimRight(porcelain, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		xy := strings.TrimSpace(line[:2])
		relP := strings.TrimSpace(line[3:])
		if idx := strings.Index(relP, " -> "); idx >= 0 {
			relP = relP[idx+4:]
		}
		status := "M"
		switch {
		case strings.Contains(xy, "?"):
			status = "?"
		case strings.Contains(xy, "A"):
			status = "A"
		case strings.Contains(xy, "D"):
			status = "D"
		case strings.Contains(xy, "R"):
			status = "R"
		}

		adds, dels := numstatMap[relP][0], numstatMap[relP][1]
		if status == "?" {
			if raw, err := os.ReadFile(filepath.Join(absRoot, filepath.FromSlash(relP))); err == nil && bytes.IndexByte(raw, 0) < 0 {
				adds = bytes.Count(raw, []byte("\n"))
				if len(raw) > 0 && raw[len(raw)-1] != '\n' {
					adds++
				}
			}
		}
		totalAdds += adds
		totalDels += dels

		fc := GitFileChange{
			RelPath:   relP,
			Status:    status,
			Additions: adds,
			Deletions: dels,
		}

		if includeDiffs && (singleRelPath == "" || singleRelPath == relP) {
			isLockFile := strings.HasSuffix(relP, "-lock.json") || strings.HasSuffix(relP, ".lock") || strings.HasSuffix(relP, "go.sum")
			if singleRelPath == "" && (isLockFile || adds+dels > 2000) {
				fc.Folded = true
			} else {
				fc.Diff = computeSingleFileDiff(ctx, absRoot, relP, status)
			}
		}
		files = append(files, fc)
	}

	return &GitSummary{
		IsGitRepo:    true,
		Branch:       strings.TrimSpace(branchOut),
		ChangedFiles: len(files),
		Additions:    totalAdds,
		Deletions:    totalDels,
		Files:        files,
	}, nil
}

func computeSingleFileDiff(ctx context.Context, absRoot, relPath, status string) string {
	if status == "?" {
		raw, err := os.ReadFile(filepath.Join(absRoot, filepath.FromSlash(relPath)))
		if err != nil || bytes.IndexByte(raw, 0) >= 0 {
			return "(binary or unreadable untracked file)"
		}
		if len(raw) > defaultPreviewLimitBytes {
			raw = raw[:defaultPreviewLimitBytes]
		}
		diff, _, _ := tools.GenerateUnifiedDiff(relPath, "", strings.ReplaceAll(string(raw), "\r\n", "\n"))
		return diff
	}
	out, err := runGit(ctx, absRoot, "diff", "HEAD", "--", relPath)
	if err != nil || out == "" {
		out, _ = runGit(ctx, absRoot, "diff", "--", relPath)
	}
	if len(out) > defaultPreviewLimitBytes {
		out = out[:defaultPreviewLimitBytes] + "\n... [diff truncated]"
	}
	return strings.TrimRight(out, "\n")
}

func collectPorcelainMap(ctx context.Context, absRoot string) map[string]string {
	out, err := runGit(ctx, absRoot, "status", "--porcelain")
	if err != nil || out == "" {
		return nil
	}
	m := make(map[string]string)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		st := strings.TrimSpace(line[:2])
		p := strings.TrimSpace(line[3:])
		m[p] = st
	}
	return m
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func isImageExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return true
	default:
		return false
	}
}

func imageMimeByExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

func detectCodeLanguage(path string) string {
	base := strings.ToLower(filepath.Base(path))
	if base == "dockerfile" {
		return "dockerfile"
	}
	if base == "makefile" {
		return "makefile"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "jsx"
	case ".json":
		return "json"
	case ".md", ".mdx":
		return "markdown"
	case ".css":
		return "css"
	case ".html":
		return "html"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".sh", ".zsh", ".bash":
		return "bash"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	default:
		return "plaintext"
	}
}
