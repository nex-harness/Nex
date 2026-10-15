// Package memory is the IMemoryService port (step 3.7): the read-only
// Project Memory catalog and the guarded single-file read.
//
// Source: packages/services/src/memory/{memoryService,projectMemoryStableRead}.ts.
// Guards preserved: the projects root must be a plain directory (a symlinked
// root would follow reads outside the local profile); workspace and memory
// dirs must be plain directories; the file must match its requested name
// exactly (case-aliasing FS would otherwise bypass the catalog allowlist);
// the read is bounded to 5 MiB and verified against the file's identity
// (dev/inode/size/mtimes) before and after, so an atomic replace mid-read
// fails closed instead of serving spliced content.
package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/services/paths"
)

// ChannelName is the rpc channel name (ServiceChannels.Memory).
const ChannelName = "memory"

// Error codes (the UI branches on these).
const (
	PreviewLimitExceededErrorCode = "PROJECT_MEMORY_PREVIEW_LIMIT_EXCEEDED"
	FileChangedErrorCode          = "PROJECT_MEMORY_FILE_CHANGED"
)

// previewMaxBytes ports PROJECT_MEMORY_PREVIEW_MAX_BYTES.
const previewMaxBytes = 5 * 1024 * 1024

const (
	projectMemoryIndexFileName = "MEMORY.md"
	projectMemoryDirectoryName = "memory"
)

var projectKeySuffixRe = regexp.MustCompile(`^(.*)-[a-f0-9]{16}$`)

// FileSummary ports ProjectMemoryFileSummary.
type FileSummary struct {
	Name      string  `json:"name"`
	Path      string  `json:"path"`
	Kind      string  `json:"kind"` // index | item
	Size      int64   `json:"size"`
	UpdatedAt float64 `json:"updatedAt"`
}

// WorkspaceSummary ports ProjectMemoryWorkspaceSummary.
type WorkspaceSummary struct {
	ID        string        `json:"id"`
	Label     string        `json:"label"`
	UpdatedAt float64       `json:"updatedAt"`
	Files     []FileSummary `json:"files"`
}

// ReadResult is the readProjectMemoryFile result.
type ReadResult struct {
	Content   string  `json:"content"`
	UpdatedAt float64 `json:"updatedAt"`
}

// Options carry the seams.
type Options struct {
	// Paths supplies the data root (~/.nex via the step-3.1 paths port).
	Paths *paths.Paths
}

// Service is the IMemoryService implementation.
type Service struct {
	paths *paths.Paths
}

// New builds the service.
func New(options Options) *Service {
	return &Service{paths: options.Paths}
}

func (s *Service) projectsRoot() string {
	// getProjectMemoriesRoot: {nexDataRoot}/cli/memories/projects.
	return filepath.Join(s.paths.NexDataRootDir(), "cli", "memories", "projects")
}

// isValidPathSegment ports isValidPathSegment.
func isValidPathSegment(value string) bool {
	return value != "" &&
		value != "." &&
		value != ".." &&
		filepath.Base(value) == value &&
		!strings.Contains(value, "/") &&
		!strings.Contains(value, "\\")
}

// isProjectMemoryFileName ports isProjectMemoryFileName.
func isProjectMemoryFileName(fileName string) bool {
	return fileName == projectMemoryIndexFileName ||
		(strings.HasSuffix(fileName, ".md") && fileName != projectMemoryIndexFileName)
}

func resolveWorkspaceLabel(workspaceID string) string {
	match := projectKeySuffixRe.FindStringSubmatch(workspaceID)
	if match == nil {
		return workspaceID
	}
	return strings.TrimSpace(match[1])
}

// isPlainDirectory: a directory that is not itself a symlink.
func isPlainDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir() && info.Mode()&os.ModeSymlink == 0, nil
}

// ListProjectMemories ports listProjectMemories: workspaces that have at
// least one memory file; sorted by updatedAt desc then id.
func (s *Service) ListProjectMemories(ctx context.Context) ([]WorkspaceSummary, error) {
	projectsRoot := s.projectsRoot()
	plain, err := isPlainDirectory(projectsRoot)
	if err != nil || !plain {
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return []WorkspaceSummary{}, nil
	}
	projectEntries, err := os.ReadDir(projectsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []WorkspaceSummary{}, nil
		}
		return nil, err
	}

	workspaces := []WorkspaceSummary{}
	for _, projectEntry := range projectEntries {
		if !projectEntry.IsDir() || projectEntry.Type()&os.ModeSymlink != 0 {
			continue
		}
		workspaceID := projectEntry.Name()
		workspaceRoot := filepath.Join(projectsRoot, workspaceID)
		memoryRoot := filepath.Join(workspaceRoot, projectMemoryDirectoryName)
		workspacePlain, err := isPlainDirectory(workspaceRoot)
		if err != nil || !workspacePlain {
			continue
		}
		memoryPlain, err := isPlainDirectory(memoryRoot)
		if err != nil || !memoryPlain {
			continue
		}
		memoryEntries, err := os.ReadDir(memoryRoot)
		if err != nil {
			// The Memory Agent may delete the dir between checks; the
			// catalog snapshot just skips the vanished workspace.
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		files := []FileSummary{}
		for _, memoryEntry := range memoryEntries {
			if !memoryEntry.Type().IsRegular() ||
				memoryEntry.Type()&os.ModeSymlink != 0 ||
				!isProjectMemoryFileName(memoryEntry.Name()) {
				continue
			}
			filePath := filepath.Join(memoryRoot, memoryEntry.Name())
			info, err := os.Lstat(filePath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			kind := "item"
			if memoryEntry.Name() == projectMemoryIndexFileName {
				kind = "index"
			}
			files = append(files, FileSummary{
				Name:      memoryEntry.Name(),
				Path:      filePath,
				Kind:      kind,
				Size:      info.Size(),
				UpdatedAt: mtimeMs(info),
			})
		}
		if len(files) == 0 {
			continue
		}
		sort.Slice(files, func(i, j int) bool {
			if files[i].Kind != files[j].Kind {
				return files[i].Kind == "index"
			}
			return files[i].Name < files[j].Name
		})
		var updatedAt float64
		for _, file := range files {
			if file.UpdatedAt > updatedAt {
				updatedAt = file.UpdatedAt
			}
		}
		workspaces = append(workspaces, WorkspaceSummary{
			ID:        workspaceID,
			Label:     resolveWorkspaceLabel(workspaceID),
			UpdatedAt: updatedAt,
			Files:     files,
		})
	}
	sort.Slice(workspaces, func(i, j int) bool {
		if workspaces[i].UpdatedAt != workspaces[j].UpdatedAt {
			return workspaces[i].UpdatedAt > workspaces[j].UpdatedAt
		}
		return workspaces[i].ID < workspaces[j].ID
	})
	return workspaces, nil
}

// ReadProjectMemoryFile ports readProjectMemoryFile with all guards.
func (s *Service) ReadProjectMemoryFile(ctx context.Context, workspaceID, fileName string) (*ReadResult, error) {
	if !isValidPathSegment(workspaceID) ||
		!isValidPathSegment(fileName) ||
		!isProjectMemoryFileName(fileName) {
		return nil, fmt.Errorf("Invalid Project Memory path")
	}
	projectsRoot := s.projectsRoot()
	workspaceRoot := filepath.Join(projectsRoot, workspaceID)
	memoryRoot := filepath.Join(workspaceRoot, projectMemoryDirectoryName)
	if err := requirePlainDirectories(projectsRoot, workspaceRoot, memoryRoot); err != nil {
		return nil, err
	}
	filePath, err := requireExactProjectMemoryFile(memoryRoot, fileName)
	if err != nil {
		return nil, err
	}
	return readProjectMemoryFileStable(fileName, filePath, func() error {
		if err := requirePlainDirectories(projectsRoot, workspaceRoot, memoryRoot); err != nil {
			return err
		}
		nextPath, err := requireExactProjectMemoryFile(memoryRoot, fileName)
		if err != nil {
			return err
		}
		if nextPath != filePath {
			return fmt.Errorf("Project Memory file name does not match exactly: %s", fileName)
		}
		return assertContainedProjectMemoryPath(projectsRoot, filePath)
	})
}

func requirePlainDirectories(projectsRoot, workspaceRoot, memoryRoot string) error {
	// Only the workspace subdir is validated here; a symlinked projectsRoot
	// would let list/read follow outside the local data dir.
	plain, err := isPlainDirectory(projectsRoot)
	if err != nil {
		return err
	}
	if !plain {
		return fmt.Errorf("Project Memory directory is not a regular directory: %s", projectsRoot)
	}
	for _, dir := range []string{workspaceRoot, memoryRoot} {
		plain, err := isPlainDirectory(dir)
		if err != nil {
			return err
		}
		if !plain {
			return fmt.Errorf("Project Memory directory is not a regular directory: %s", dir)
		}
	}
	return nil
}

// requireExactProjectMemoryFile: the readdir entry must carry exactly the
// requested name and be a regular non-symlink file (case-aliasing guard).
func requireExactProjectMemoryFile(memoryRoot, fileName string) (string, error) {
	memoryEntries, err := os.ReadDir(memoryRoot)
	if err != nil {
		return "", err
	}
	var fileEntry os.DirEntry
	for _, entry := range memoryEntries {
		if entry.Name() == fileName {
			fileEntry = entry
			break
		}
	}
	requestedFilePath := filepath.Join(memoryRoot, fileName)
	if fileEntry == nil {
		if _, err := os.Lstat(requestedFilePath); err != nil {
			return "", err // the raw ENOENT passes through
		}
		return "", fmt.Errorf("Project Memory file name does not match exactly: %s", fileName)
	}
	if !fileEntry.Type().IsRegular() || fileEntry.Type()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("Project Memory file is not a regular file: %s", fileName)
	}
	return requestedFilePath, nil
}

func assertContainedProjectMemoryPath(projectsRoot, targetPath string) error {
	projectsRootRealPath, err := filepath.EvalSymlinks(projectsRoot)
	if err != nil {
		return err
	}
	targetRealPath, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(projectsRootRealPath, targetRealPath)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("Project Memory path is outside the local profile: %s", targetPath)
	}
	return nil
}
