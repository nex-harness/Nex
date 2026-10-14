package file

// service_index.go ports the workspace file index half of fileService.ts:
// ensureWorkspaceFileIndex (TTL + .nexignore fingerprint cache, in-flight
// scan dedup by workspace key, bounded-concurrency traversal), the RPC
// methods searchWorkspaceFiles / listWorkspaceFilesLength /
// listWorkspaceFilesRange, and the .nexignore read/transform/write methods.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type workspaceIndex struct {
	at              int64 // unix ms
	signature       string
	packed          string
	candidates      []searchCandidate
	candidatesBuilt bool
}

type scanState struct {
	signature string
	done      chan struct{}
	index     *workspaceIndex
	err       error
}

// workspaceKey is the AGENTS.md identity rule: workspaceIdentity?.trim() ||
// workspacePath — the single key for cache isolation.
func workspaceKey(identity, rootPath string) string {
	if t := trimmedOrEmpty(identity); t != "" {
		return t
	}
	return rootPath
}

func trimmedOrEmpty(s string) string {
	t := trimSpaces(s)
	if t == "" {
		return ""
	}
	return t
}

func trimSpaces(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// statIgnoreFingerprint ports statWorkspaceFileSearchIgnoreFingerprint.
func statIgnoreFingerprint(rootPath string) string {
	info, err := os.Stat(filepath.Join(rootPath, IgnoreFileName))
	if err != nil {
		return "none"
	}
	return fmt.Sprintf("%v:%d", float64(info.ModTime().UnixNano())/1e6, info.Size())
}

// ensureIndex ports ensureWorkspaceFileIndex.
func (s *Service) ensureIndex(ctx context.Context, rootPath, workspaceIdentity string, refresh bool) (*workspaceIndex, error) {
	key := workspaceKey(workspaceIdentity, rootPath)
	rules, err := loadIgnoreRules(rootPath, s.logger)
	if err != nil {
		return nil, err
	}
	cacheSignature := rootPath + "\n" + statIgnoreFingerprint(rootPath)
	now := s.now().UnixMilli()

	s.mu.Lock()
	// drop expired cache entries
	for k, v := range s.indexCache {
		if now-v.at >= int64(indexCacheTTL/time.Millisecond) {
			delete(s.indexCache, k)
		}
	}
	if st, ok := s.scanning[key]; ok && !refresh && st.signature == cacheSignature {
		s.mu.Unlock()
		select {
		case <-st.done:
			return st.index, st.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if cached, ok := s.indexCache[key]; !refresh && ok && cached.signature == cacheSignature {
		delete(s.indexCache, key)
		s.indexCache[key] = cached
		s.mu.Unlock()
		return cached, nil
	}
	st := &scanState{signature: cacheSignature, done: make(chan struct{})}
	s.scanning[key] = st
	s.mu.Unlock()

	go func() {
		defer close(st.done)
		index, err := s.scanWorkspace(rootPath, rules, cacheSignature)
		s.mu.Lock()
		if cur, ok := s.scanning[key]; ok && cur == st {
			delete(s.scanning, key)
			if err == nil {
				// 显式刷新/规则变化可替换在途扫描；旧扫描只服务旧请求。
				delete(s.indexCache, key)
				s.indexCache[key] = index
				for len(s.indexCache) > indexCacheMaxEntries {
					var oldestKey string
					var oldestAt int64
					for k, v := range s.indexCache {
						if oldestKey == "" || v.at < oldestAt {
							oldestKey, oldestAt = k, v.at
						}
					}
					delete(s.indexCache, oldestKey)
				}
			}
		}
		s.mu.Unlock()
		st.index, st.err = index, err
	}()

	select {
	case <-st.done:
		return st.index, st.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// scanWorkspace ports the traverseWorker scan: bounded concurrency over a
// shared directory queue, results sorted (dirs first, then relativePath).
func (s *Service) scanWorkspace(rootPath string, rules *IgnoreRules, cacheSignature string) (*workspaceIndex, error) {
	var mu sync.Mutex
	var entries []workspaceEntry
	var firstErr error
	pending := []string{rootPath}
	ignoreRulesActive := rules.Source == "file" || rules.Source == "created-from-gitignore" || rules.Source == "created-from-template"

	var wg sync.WaitGroup
	for w := 0; w < indexScanConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if len(pending) == 0 {
					mu.Unlock()
					return
				}
				current := pending[len(pending)-1]
				pending = pending[:len(pending)-1]
				mu.Unlock()

				dirEntries, err := os.ReadDir(current)
				if err != nil {
					if isSkippableWorkspaceListError(err) {
						continue
					}
					// the TS propagates the first non-skippable error by
					// rejecting the whole scan; capture via panic-free channel
					// by storing in the shared state
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					// wake the other workers so wg.Wait finishes promptly
					pending = nil
					mu.Unlock()
					return
				}
				for _, de := range dirEntries {
					entryPath := filepath.Join(current, de.Name())
					relativePath := normalizeRelativePath(rootPath, entryPath)
					if relativePath == IgnoreFileName {
						continue
					}
					isSymlink := de.Type()&os.ModeSymlink != 0
					entryType := resolveReaddirEntryType(context.Background(), entryPath, de.IsDir(), isSymlink)
					if rules.isPathIgnored(relativePath, entryType) {
						continue
					}
					decision := s.filter.evaluate(workspaceEntry{
						Name: de.Name(), Path: entryPath, RelativePath: relativePath, Type: entryType,
					}, ignoreRulesActive)
					mu.Lock()
					if decision.include {
						entries = append(entries, workspaceEntry{Name: de.Name(), Path: entryPath, RelativePath: relativePath, Type: entryType})
					}
					if entryType == "directory" && !isSymlink && decision.traverse {
						pending = append(pending, entryPath)
					}
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "directory"
		}
		return localeCompareOrdered(entries[i].RelativePath, entries[j].RelativePath) < 0
	})
	packed := packWorkspaceFileEntries(entries)
	return &workspaceIndex{at: s.now().UnixMilli(), signature: cacheSignature, packed: packed}, nil
}

// SearchWorkspaceFiles ports searchWorkspaceFiles.
func (s *Service) SearchWorkspaceFiles(ctx context.Context, rootPath, workspaceIdentity, query string, limit *int64, refresh bool) ([]workspaceEntry, error) {
	l := int64(displayCap)
	if limit != nil {
		l = *limit
	}
	if l < 0 {
		l = 0
	}
	if l == 0 {
		return []workspaceEntry{}, nil
	}
	index, err := s.ensureIndex(ctx, rootPath, workspaceIdentity, refresh)
	if err != nil {
		return nil, err
	}
	if !index.candidatesBuilt {
		cands, err := buildHostCandidates(ctx, index.packed, rootPath)
		if err != nil {
			return nil, err
		}
		index.candidates = cands
		index.candidatesBuilt = true
	}
	return searchHostCandidates(ctx, index.candidates, query, int(l))
}

// ListWorkspaceFilesLength ports listWorkspaceFilesLength.
func (s *Service) ListWorkspaceFilesLength(ctx context.Context, rootPath string) (int, error) {
	index, err := s.ensureIndex(ctx, rootPath, "", false)
	if err != nil {
		return 0, err
	}
	return len(index.packed), nil
}

// ListWorkspaceFilesRange ports listWorkspaceFilesRange.
func (s *Service) ListWorkspaceFilesRange(ctx context.Context, rootPath string, offset, length int64) (string, error) {
	index, err := s.ensureIndex(ctx, rootPath, "", false)
	if err != nil {
		return "", err
	}
	packed := index.packed
	if offset < 0 {
		offset = 0
	}
	if offset >= int64(len(packed)) {
		return "", nil
	}
	if length < 0 {
		length = 0
	}
	end := offset + length
	if end > int64(len(packed)) {
		end = int64(len(packed))
	}
	return packed[offset:end], nil
}

// ReadWorkspaceFileSearchIgnore ports readWorkspaceFileSearchIgnore.
func (s *Service) ReadWorkspaceFileSearchIgnore(ctx context.Context, rootPath string) (content string, source string, err error) {
	return readIgnoreFile(rootPath)
}

// ApplyWorkspaceFileSearchIgnoreTransform ports applyWorkspaceFileSearchIgnoreTransform.
func (s *Service) ApplyWorkspaceFileSearchIgnoreTransform(ctx context.Context, rootPath, transform string) (string, error) {
	return transformIgnoreFile(rootPath, transform)
}

// WriteWorkspaceFileSearchIgnore ports writeWorkspaceFileSearchIgnore.
func (s *Service) WriteWorkspaceFileSearchIgnore(ctx context.Context, rootPath, content string) error {
	return writeIgnoreFile(rootPath, content)
}
