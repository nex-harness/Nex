package file

// search.go ports the workspace file search stack:
//
//   - workspaceFileMentionFilter.ts (defaultWorkspaceFileSearchFilter)
//   - shared workspaceFileSearch.ts (candidate mapping, fuzzy scoring, top-K)
//   - file/workspaceFileSearch.ts (batched decode + batched filter)
//
// The scoring is transcribed exactly: prefix score = len(text)-len(query),
// substring = 100+index, subsequence = 200 with gap costs, plus the +25
// relativePath and +300 keyword tiers; ties break by (score, index, name).

import (
	"context"
	"strings"
	"unicode/utf8"
)

// displayCap is WORKSPACE_FILE_SEARCH_DISPLAY_CAP.
const displayCap = 1000

// searchCandidate is WorkspaceFileSearchCandidate with the precomputed
// lowercase fields.
type searchCandidate struct {
	id                string
	name              string
	path              string
	relativePath      string
	entryType         string
	lowercaseName     string
	lowercaseRelative string
	lowercasePath     string
}

// hasQuery ports hasWorkspaceFileSearchQuery.
func hasQuery(query string) bool {
	return strings.TrimSpace(query) != ""
}

// mapEntriesToCandidates ports mapWorkspaceFileEntriesToSearchCandidates:
// every field is trim+lowercased at index-build time.
func mapEntriesToCandidates(entries []workspaceEntry) []searchCandidate {
	out := make([]searchCandidate, len(entries))
	for i, e := range entries {
		out[i] = searchCandidate{
			id:                e.RelativePath,
			name:              e.Name,
			path:              e.Path,
			relativePath:      e.RelativePath,
			entryType:         e.Type,
			lowercaseName:     strings.ToLower(strings.TrimSpace(e.Name)),
			lowercaseRelative: strings.ToLower(strings.TrimSpace(e.RelativePath)),
			lowercasePath:     strings.ToLower(strings.TrimSpace(e.Path)),
		}
	}
	return out
}

// scoreNormalized ports scoreNormalizedFuzzyMatch (and the public
// scoreWorkspaceFileFuzzyMatch's trim+lower normalization).
// Returns (score, ok).
func scoreNormalized(text, query string) (int, bool) {
	if text == "" {
		return 0, false
	}
	if query == "" {
		return 0, true
	}
	if strings.HasPrefix(text, query) {
		return len(text) - len(query), true
	}
	if idx := strings.Index(text, query); idx != -1 {
		return 100 + idx, true
	}
	score := 200
	searchStart := 0
	for _, q := range query {
		foundAt := -1
		for i, r := range text[searchStart:] {
			if r == q {
				foundAt = i
				break
			}
		}
		if foundAt == -1 {
			return 0, false
		}
		// rune-aware: the TS indexOf is by UTF-16 unit; Go strings iterate
		// runes. Score by byte distance — identical for ASCII, and both are
		// stable orderings for non-ASCII.
		score += len(text[searchStart : searchStart+foundAt])
		searchStart += foundAt + utf8.RuneLen(q)
	}
	return score + len(text) - len(query), true
}

// candidateScore ports getWorkspaceFileSearchCandidateScore. query must
// already be normalized (trim+lower) by the caller.
func candidateScore(c searchCandidate, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	nameScore, nameOK := scoreNormalized(c.lowercaseName, query)
	relScore, relOK := scoreNormalized(c.lowercaseRelative, query)
	pathScore, pathOK := scoreNormalized(c.lowercasePath, query)
	const inf = 1 << 30
	best := inf
	if nameOK {
		best = nameScore
	}
	if relOK && relScore+25 < best {
		best = relScore + 25
	}
	// keyword tier: min(relativePath+300, path+300); the +100 tier is dead
	// code in the TS (always dominated by the +25 tier).
	if relOK || pathOK {
		kw := inf
		if relOK && relScore+300 < kw {
			kw = relScore + 300
		}
		if pathOK && pathScore+300 < kw {
			kw = pathScore + 300
		}
		if kw < best {
			best = kw
		}
	}
	if best == inf {
		return 0, false
	}
	return best, true
}

// scoredCandidate pairs a candidate with its score and source index.
type scoredCandidate struct {
	candidate searchCandidate
	index     int
	score     int
}

// compareScored ports compareScoredWorkspaceFileSearchCandidates: (score,
// index, name) — index makes the order strict so the top-K insertion point is
// unambiguous.
func compareScored(a, b scoredCandidate) int {
	if a.score != b.score {
		if a.score < b.score {
			return -1
		}
		return 1
	}
	if a.index != b.index {
		if a.index < b.index {
			return -1
		}
		return 1
	}
	return strings.Compare(a.candidate.name, b.candidate.name)
}

// filterCandidates ports filterWorkspaceFileSearchCandidates: top-K via
// binary-search insertion (the TS's O(n log K) fix).
func filterCandidates(candidates []searchCandidate, query string, limit int) []searchCandidate {
	normalized := strings.TrimSpace(query)
	if normalized == "" {
		// the caller maps requireQuery; the empty-query default sort is
		// directories first in original order, capped.
		out := make([]searchCandidate, 0, len(candidates))
		out = append(out, candidates...)
		sortDefaultCandidates(out)
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}
	normLower := strings.ToLower(normalized)
	var best []scoredCandidate
	for idx, c := range candidates {
		score, ok := candidateScore(c, normLower)
		if !ok {
			continue
		}
		scored := scoredCandidate{candidate: c, index: idx, score: score}
		// fast path: full and not better than the current worst
		if len(best) >= limit && compareScored(scored, best[len(best)-1]) >= 0 {
			continue
		}
		pos := sortSearch(len(best), func(i int) bool { return compareScored(scored, best[i]) < 0 })
		if pos == len(best) {
			if len(best) < limit {
				best = append(best, scored)
			}
			continue
		}
		best = append(best, scoredCandidate{})
		copy(best[pos+1:], best[pos:])
		best[pos] = scored
		if len(best) > limit {
			best = best[:len(best)-1]
		}
	}
	out := make([]searchCandidate, len(best))
	for i, s := range best {
		out[i] = s.candidate
	}
	return out
}

// sortSearch is sort.Search.
func sortSearch(n int, f func(int) bool) int {
	lo, hi := 0, n
	for lo < hi {
		mid := (lo + hi) / 2
		if f(mid) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// sortDefaultCandidates ports sortDefaultWorkspaceFileSearchCandidates: a
// STABLE priority sort — files (priority 0) before directories (priority 1),
// original order preserved within each group.
func sortDefaultCandidates(candidates []searchCandidate) {
	files := make([]searchCandidate, 0, len(candidates))
	dirs := make([]searchCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.entryType == "directory" {
			dirs = append(dirs, c)
		} else {
			files = append(files, c)
		}
	}
	copy(candidates, files)
	copy(candidates[len(files):], dirs)
}

// buildHostCandidates ports buildHostFileSearchCandidates: decode the packed
// index in ~128k-char batches, yielding between batches (Go: check ctx).
func buildHostCandidates(ctx context.Context, packed, rootPath string) ([]searchCandidate, error) {
	var out []searchCandidate
	for offset := 0; offset < len(packed); {
		newline := strings.IndexByte(packed[min(offset+128_000, len(packed)):], '\n')
		end := len(packed)
		if newline >= 0 {
			end = min(offset+128_000, len(packed)) + newline + 1
		}
		entries := unpackWorkspaceFileEntries(packed[offset:end], rootPath)
		out = append(out, mapEntriesToCandidates(entries)...)
		offset = end
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// searchHostCandidates ports searchHostFileCandidates: batch the candidates
// in 2048-sized windows through the same top-K filter, merging with the
// previous best (the top-K of a concatenation equals the incremental top-K).
func searchHostCandidates(ctx context.Context, candidates []searchCandidate, query string, limit int) ([]workspaceEntry, error) {
	var best []searchCandidate
	for offset := 0; offset < len(candidates); offset += 2048 {
		end := min(offset+2048, len(candidates))
		merged := make([]searchCandidate, 0, len(best)+end-offset)
		merged = append(merged, best...)
		merged = append(merged, candidates[offset:end]...)
		best = filterCandidates(merged, query, limit)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]workspaceEntry, len(best))
	for i, c := range best {
		out[i] = workspaceEntry{Name: c.name, Path: c.path, RelativePath: c.relativePath, Type: c.entryType}
	}
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
