// import.go: the skill-sync import pipeline (importArchive +
// collectExtractedSkillDirectories) and the recursive size computation.
package skillsync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/skills"
)

// importArchive ports importArchive: extract into a temp dir inside the
// user skill root, validate each extracted skill directory, copy the ones
// that are new (by folder and by frontmatter name), and always clean the
// temp root.
func (s *Service) importArchive(archive []byte) (*ImportResult, error) {
	targetRoot := s.userNexSkillRoot()
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return nil, err
	}
	tempRoot := filepath.Join(targetRoot, ".sync-tmp-"+randomHexID())
	if err := os.MkdirAll(tempRoot, 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempRoot)

	// The archive comes over the wire: extract to the temp dir first and
	// validate SKILL.md before copying anything into the user root, so a
	// path traversal or half-written directory cannot pollute it.
	if err := skills.ExtractSyncArchive(archive, tempRoot, skills.ExtractSyncArchiveOptions{
		MaxExtractedBytes: s.maxArchiveBytes,
	}); err != nil {
		return nil, err
	}
	extractedSkillDirectories, err := collectExtractedSkillDirectories(tempRoot)
	if err != nil {
		return nil, err
	}
	// The remote SkillsService reads both user roots; a same-named skill in
	// the compatibility root must also be skipped or the sync duplicates it.
	existingSkillPathByName, err := s.collectUserSkillDirectoryPathByName()
	if err != nil {
		return nil, err
	}
	result := &ImportResult{}
	for _, extracted := range extractedSkillDirectories {
		targetPath, err := skills.ResolveSyncPathWithin(targetRoot, extracted.directoryName, "")
		if err != nil {
			return nil, err
		}
		skillContent, err := os.ReadFile(filepath.Join(extracted.sourcePath, skills.FileName))
		if err != nil {
			result.Results = append(result.Results, ImportResultItem{
				Name:          filepath.Base(extracted.directoryName),
				DirectoryName: extracted.directoryName,
				Status:        "failed",
				Error:         "SKILL.md is missing",
			})
			continue
		}
		metadata := parseSkillMetadata(string(skillContent), extracted.directoryName)
		nameKey := skills.NormalizeSkillNameKey(metadata.name)
		if _, statErr := os.Stat(targetPath); statErr == nil {
			result.Results = append(result.Results, ImportResultItem{
				Name: metadata.name, DirectoryName: extracted.directoryName, Status: "skipped", Path: targetPath,
			})
			continue
		}
		if existingPath, ok := existingSkillPathByName[nameKey]; ok {
			result.Results = append(result.Results, ImportResultItem{
				Name: metadata.name, DirectoryName: extracted.directoryName, Status: "skipped", Path: existingPath,
			})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			result.Results = append(result.Results, ImportResultItem{
				Name: metadata.name, DirectoryName: extracted.directoryName, Status: "failed", Path: targetPath, Error: err.Error(),
			})
			continue
		}
		if err := copyNewTree(extracted.sourcePath, targetPath); err != nil {
			result.Results = append(result.Results, ImportResultItem{
				Name: metadata.name, DirectoryName: extracted.directoryName, Status: "failed", Path: targetPath, Error: err.Error(),
			})
			continue
		}
		result.Results = append(result.Results, ImportResultItem{
			Name: metadata.name, DirectoryName: extracted.directoryName, Status: "synced", Path: targetPath,
		})
		existingSkillPathByName[nameKey] = targetPath
	}
	return result, nil
}

// copyNewTree copies without overwriting (errorOnExist semantics).
func copyNewTree(source, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("target already exists: %s", target)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	}
	if !info.IsDir() {
		return nil
	}
	if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyNewTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

type extractedSkillDirectory struct {
	directoryName string
	sourcePath    string
}

// collectExtractedSkillDirectories ports collectExtractedSkillDirectories.
func collectExtractedSkillDirectories(root string) ([]extractedSkillDirectory, error) {
	var directories []extractedSkillDirectory
	type frame struct {
		directoryPath string
		relativePath  string
	}
	stack := []frame{{root, ""}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.relativePath != "" {
			if _, err := os.Stat(filepath.Join(current.directoryPath, skills.FileName)); err == nil {
				directories = append(directories, extractedSkillDirectory{
					directoryName: current.relativePath,
					sourcePath:    current.directoryPath,
				})
			}
		}
		entries, err := os.ReadDir(current.directoryPath)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !skills.ShouldWalkDirectoryEntry(entry.Name()) {
				continue
			}
			relative := current.relativePath
			if relative != "" {
				relative += "/" + entry.Name()
			} else {
				relative = entry.Name()
			}
			stack = append(stack, frame{filepath.Join(current.directoryPath, entry.Name()), relative})
		}
	}
	sort.Slice(directories, func(i, j int) bool { return directories[i].directoryName < directories[j].directoryName })
	return directories, nil
}

// computeRecursiveSize ports computeRecursiveSize: symlink targets are
// followed for size, directory cycles are cut by realpath.
func computeRecursiveSize(path string, visitedDirectories map[string]bool) (int64, error) {
	pathStat, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if pathStat.Mode()&os.ModeSymlink != 0 {
		targetStat, err := os.Stat(path)
		if err != nil {
			return 0, nil
		}
		if targetStat.Mode().IsRegular() {
			return targetStat.Size(), nil
		}
		if targetStat.IsDir() {
			return computeDirectorySize(path, visitedDirectories)
		}
		return 0, nil
	}
	if pathStat.Mode().IsRegular() {
		return pathStat.Size(), nil
	}
	if !pathStat.IsDir() {
		return 0, nil
	}
	return computeDirectorySize(path, visitedDirectories)
}

func computeDirectorySize(path string, visitedDirectories map[string]bool) (int64, error) {
	canonicalPath := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		canonicalPath = resolved
	}
	if visitedDirectories[canonicalPath] {
		return 0, nil
	}
	visitedDirectories[canonicalPath] = true
	children, err := os.ReadDir(path)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, child := range children {
		size, err := computeRecursiveSize(filepath.Join(path, child.Name()), visitedDirectories)
		if err != nil {
			return 0, err
		}
		total += size
	}
	return total, nil
}

// joinSkillRelativePath keeps the /-shaped relative paths.
func joinSkillRelativePath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "/" + child
}

var _ = strings.TrimSpace
