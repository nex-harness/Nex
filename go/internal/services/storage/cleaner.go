// cleaner.go ports storage/adapters/fsCleaner.ts: enumerates candidates
// (recursive / shallow scopes) and deletes with bounded concurrency. Empty
// directories are pruned bottom-up after deletion, but the category's top
// directories (keepDirectories) stay so writers need not re-mkdir.
package storage

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// deleteConcurrency ports DELETE_CONCURRENCY.
const deleteConcurrency = 8

// fsCleaner ports createFsStorageCleaner.
type fsCleaner struct{}

// NewFsCleaner returns the filesystem FsCleanerPort.
func NewFsCleaner() FsCleanerPort { return fsCleaner{} }

// ListCandidates ports listCandidates: enumerate each scope, dedupe by
// normalized path (recursive and shallow scopes can overlap: cli/db/backup
// vs cli/db).
func (fsCleaner) ListCandidates(_ context.Context, rootPath string, scopes []CleanScope) ([]CleanCandidate, error) {
	byPath := map[string]CleanCandidate{}
	var order []string
	for _, scope := range scopes {
		var candidates []CleanCandidate
		if scope.Recursive {
			candidates = listRecursive(rootPath, scope.Prefix)
		} else {
			candidates = listShallow(rootPath, scope.Prefix)
		}
		for _, candidate := range candidates {
			key := NormalizeRelativePath(candidate.RelativePath)
			if _, seen := byPath[key]; !seen {
				order = append(order, key)
			}
			byPath[key] = CleanCandidate{RelativePath: key, Bytes: candidate.Bytes, MtimeMs: candidate.MtimeMs}
		}
	}
	sort.Strings(order)
	result := make([]CleanCandidate, 0, len(order))
	for _, key := range order {
		result = append(result, byPath[key])
	}
	return result, nil
}

func listShallow(rootPath, prefix string) []CleanCandidate {
	directoryPath := filepath.Join(rootPath, filepath.FromSlash(prefix))
	names, err := os.ReadDir(directoryPath)
	if err != nil {
		return nil
	}
	var candidates []CleanCandidate
	for _, name := range names {
		info, err := os.Lstat(filepath.Join(directoryPath, name.Name()))
		if err != nil || !info.Mode().IsRegular() {
			// 枚举期间被删除的文件跳过即可
			continue
		}
		candidates = append(candidates, CleanCandidate{
			RelativePath: prefix + "/" + name.Name(),
			Bytes:        info.Size(),
			MtimeMs:      info.ModTime().UnixMilli(),
		})
	}
	return candidates
}

func listRecursive(rootPath, prefix string) []CleanCandidate {
	var mu sync.Mutex
	var candidates []CleanCandidate
	_, _ = walkStorageRoot(context.Background(), walkOptions{
		rootPath: filepath.Join(rootPath, filepath.FromSlash(prefix)),
		onEntry: func(entry ScanEntry) {
			mu.Lock()
			candidates = append(candidates, CleanCandidate{
				RelativePath: prefix + "/" + entry.RelativePath,
				Bytes:        entry.Bytes,
				MtimeMs:      entry.MtimeMs,
			})
			mu.Unlock()
		},
	})
	return candidates
}

// DeleteFiles ports deleteFiles: concurrent deletes with the outside-root
// guard (relative path containing .. is refused with EOUTSIDE, never deleted).
func (fsCleaner) DeleteFiles(_ context.Context, rootPath string, targets []CleanCandidate, keepDirectories []string) (DeleteResult, error) {
	result := DeleteResult{Failures: []PathError{}}
	var mu sync.Mutex
	var deleted []string
	queue := make([]CleanCandidate, len(targets))
	copy(queue, targets)
	var wg sync.WaitGroup
	workers := deleteConcurrency
	if workers > len(queue) {
		workers = len(queue)
	}
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if len(queue) == 0 {
					mu.Unlock()
					return
				}
				target := queue[0]
				queue = queue[1:]
				mu.Unlock()

				relativePath := NormalizeRelativePath(target.RelativePath)
				absolutePath := filepath.Join(rootPath, filepath.FromSlash(relativePath))
				back, err := filepath.Rel(rootPath, absolutePath)
				if err != nil || strings.HasPrefix(back, "..") || strings.Contains(back, ".."+string(filepath.Separator)) {
					mu.Lock()
					result.Failures = append(result.Failures, PathError{Path: relativePath, Code: "EOUTSIDE"})
					mu.Unlock()
					continue
				}
				if err := os.Remove(absolutePath); err != nil {
					mu.Lock()
					result.Failures = append(result.Failures, PathError{Path: relativePath, Code: errCode(err)})
					mu.Unlock()
					continue
				}
				mu.Lock()
				result.DeletedCount++
				result.FreedBytes += target.Bytes
				deleted = append(deleted, relativePath)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	pruneEmptyParents(rootPath, deleted, keepDirectories)
	return result, nil
}

// pruneEmptyParents ports pruneEmptyParents: deepest directories first so
// parents get their chance to be empty; keepDirectories are never removed.
func pruneEmptyParents(rootPath string, deletedRelativePaths, keepDirs []string) {
	keep := map[string]bool{}
	for _, dir := range keepDirs {
		keep[dir] = true
	}
	directories := map[string]bool{}
	for _, path := range deletedRelativePaths {
		current := parentDir(path)
		for current != "" && current != "." && !keep[current] {
			directories[current] = true
			current = parentDir(current)
		}
	}
	ordered := make([]string, 0, len(directories))
	for dir := range directories {
		ordered = append(ordered, dir)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return strings.Count(ordered[i], "/") > strings.Count(ordered[j], "/")
	})
	for _, dir := range ordered {
		_ = os.Remove(filepath.Join(rootPath, filepath.FromSlash(dir)))
	}
}

func parentDir(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return ""
	}
	return path[:idx]
}
