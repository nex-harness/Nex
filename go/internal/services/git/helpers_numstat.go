// helpers_numstat.go ports the numstat parsing and untracked-stats building
// from gitCliHelpers.ts (step 3.3). Split out of helpers.go to stay under the
// 400-line archcheck limit.
package git

import (
	"strconv"
	"strings"
	"sync"
)

type GitLineStat struct {
	Added        int
	Removed      int
	Kind         GitChangeKind
	HasKind      bool
	OriginalPath *string
}

func parseNumstatValue(value string) int {
	if value == "-" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

// inferKindFromNumstat ports inferKindFromNumstat.
func inferKindFromNumstat(stat GitLineStat) GitChangeKind {
	if stat.HasKind {
		return stat.Kind
	}
	if stat.Added > 0 && stat.Removed == 0 {
		return ChangeKindAdded
	}
	if stat.Removed > 0 && stat.Added == 0 {
		return ChangeKindDeleted
	}
	return ChangeKindModified
}

// parseNumstatMap ports parseNumstat (the gitCliRepo variant: the renamed-path
// record keeps only the new path as the key).
func parseNumstatMap(stdout string) map[string]GitLineStat {
	records := strings.Split(stdout, "\x00")
	stats := make(map[string]GitLineStat)
	for index := 0; index < len(records); index++ {
		record := records[index]
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\t")
		if len(fields) < 3 {
			continue
		}
		added := parseNumstatValue(fields[0])
		removed := parseNumstatValue(fields[1])
		pathField := strings.Join(fields[2:], "\t")
		if pathField != "" {
			stats[NormalizeGitPath(pathField)] = GitLineStat{Added: added, Removed: removed}
			continue
		}
		originalPath, renamedPath := "", ""
		if index+1 < len(records) {
			originalPath = records[index+1]
		}
		if index+2 < len(records) {
			renamedPath = records[index+2]
		}
		index += 2
		if renamedPath == "" {
			continue
		}
		entry := GitLineStat{Added: added, Removed: removed, Kind: ChangeKindRenamed, HasKind: true}
		if originalPath != "" {
			entry.OriginalPath = ptr(NormalizeGitPath(originalPath))
		}
		stats[NormalizeGitPath(renamedPath)] = entry
	}
	return stats
}

// ---------------------------------------------------------------------------
// untracked line stats (buildUntrackedStats / countUntrackedFileLines)
// ---------------------------------------------------------------------------

// buildUntrackedStats ports buildUntrackedStats: 固定 worker 各复用一个小缓冲，
// 逐文件限量读取；大文件仍保留变更条目，只跳过行数统计。
func buildUntrackedStats(repoRoot string, entries []GitStatusEntry) map[string]GitLineStat {
	stats := make(map[string]GitLineStat, len(entries))
	var untracked []GitStatusEntry
	for _, entry := range entries {
		if entry.IsUntracked {
			untracked = append(untracked, entry)
		}
	}
	nextIndex := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := GitUntrackedStatConcurrency
	if workers > len(untracked) {
		workers = len(untracked)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buffer := make([]byte, GitUntrackedStatChunkBytes)
			for {
				mu.Lock()
				index := nextIndex
				if index >= len(untracked) {
					mu.Unlock()
					return
				}
				nextIndex++
				mu.Unlock()
				entry := untracked[index]
				absolutePath := joinRepoPath(repoRoot, entry.Path)
				lines, ok := countUntrackedFileLines(absolutePath, buffer)
				mu.Lock()
				if !ok {
					stats[entry.Path] = GitLineStat{}
				} else {
					stats[entry.Path] = GitLineStat{Added: lines}
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return stats
}

// joinRepoPath joins a repo-relative forward-slash path onto repoRoot.
