// cleanplan.go ports storage/domain/cleanPlan.ts: given a category and the
// enumerated candidate files, decide which can be deleted. Pure functions;
// deletion itself is the cleaner adapter's job.
package storage

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// CleanCandidate ports StorageCleanCandidate.
type CleanCandidate struct {
	RelativePath string `json:"relativePath"`
	Bytes        int64  `json:"bytes"`
	MtimeMs      int64  `json:"mtimeMs"`
}

// CleanPlan ports StorageCleanPlan.
type CleanPlan struct {
	Targets      []CleanCandidate
	SkippedCount int
}

// subagentActiveWindowMs ports SUBAGENT_ACTIVE_WINDOW_MS: a session dir with
// any write in the last 24h is skipped whole, so an in-flight subagent's
// transcript is never deleted.
const subagentActiveWindowMs = 24 * 60 * 60 * 1000

// PlanStorageClean ports planStorageClean.
func PlanStorageClean(categoryID string, candidates []CleanCandidate, context CatalogContext, nowMs int64) CleanPlan {
	if CleanabilityOf(categoryID) == "none" {
		return CleanPlan{Targets: nil, SkippedCount: len(candidates)}
	}
	// 候选来自按前缀枚举，可能混入其他类别（如 cli/plugins 下的 cache）；
	// 只保留分类一致且未受保护的。
	var owned []CleanCandidate
	for _, candidate := range candidates {
		if ClassifyStoragePath(candidate.RelativePath, context).CategoryID == categoryID &&
			!IsProtectedPath(candidate.RelativePath) {
			owned = append(owned, candidate)
		}
	}
	targets := owned
	switch categoryID {
	case "logs":
		targets = filterSameLocalDay(owned, nowMs)
	case "subagentTranscripts":
		// 活动判定如果只看 owned（已按类别过滤，只剩 transcript.jsonl），会漏掉同
		// 目录下刚写入的 metadata/output 文件，把进行中 subagent 的 transcript 判成
		// 不活跃。这里用全部候选算活动时间。
		targets = filterInactiveSessionDirs(owned, candidates, nowMs)
	}
	return CleanPlan{Targets: targets, SkippedCount: len(candidates) - len(targets)}
}

// sameLocalDay ports isSameLocalDay: local calendar day comparison
// (time.UnixMilli already renders in the process's local zone).
func sameLocalDay(a, b int64) bool {
	ay, am, ad := time.UnixMilli(a).Date()
	by, bm, bd := time.UnixMilli(b).Date()
	return ay == by && am == bm && ad == bd
}

// filterSameLocalDay keeps candidates NOT modified today (logs: today's logs
// stay).
func filterSameLocalDay(candidates []CleanCandidate, nowMs int64) []CleanCandidate {
	var kept []CleanCandidate
	for _, candidate := range candidates {
		if !sameLocalDay(candidate.MtimeMs, nowMs) {
			kept = append(kept, candidate)
		}
	}
	return kept
}

// sessionDirKey ports the sessionKey helper: first three path segments
// (cli/agents/sess_x).
func sessionDirKey(path string) string {
	parts := strings.SplitN(path, "/", 4)
	return strings.Join(parts[:3], "/")
}

// filterInactiveSessionDirs ports filterInactiveSessionDirs: a session
// directory is skipped whole when ANY file in it (across all candidates) was
// written inside the active window.
func filterInactiveSessionDirs(targets, allCandidates []CleanCandidate, nowMs int64) []CleanCandidate {
	latestBySession := map[string]int64{}
	for _, candidate := range allCandidates {
		key := sessionDirKey(candidate.RelativePath)
		if candidate.MtimeMs > latestBySession[key] {
			latestBySession[key] = candidate.MtimeMs
		}
	}
	var kept []CleanCandidate
	for _, candidate := range targets {
		latest := latestBySession[sessionDirKey(candidate.RelativePath)]
		if nowMs-latest > subagentActiveWindowMs {
			kept = append(kept, candidate)
		}
	}
	return kept
}

// maxEntriesPerCategoryDefault ports DEFAULT_MAX_ENTRIES_PER_CATEGORY.
const maxEntriesPerCategoryDefault = 100

// usageAccumulator ports createStorageUsageAccumulator: folds scan entries
// into a root-usage snapshot. Bounded: each category keeps only the N largest
// entries; the rest fold into MoreEntriesPath.
type usageAccumulator struct {
	mu         sync.Mutex
	spec       RootSpec
	maxEntries int
	context    CatalogContext
	buckets    map[string]*categoryBucket
	totalBytes int64
	totalFiles int
}

type entryAggregate struct {
	bytes     int64
	fileCount int
}

type categoryBucket struct {
	bytes     int64
	fileCount int
	entries   map[string]*entryAggregate
}

// newUsageAccumulator ports createStorageUsageAccumulator.
func newUsageAccumulator(spec RootSpec, maxEntries int) *usageAccumulator {
	if maxEntries <= 0 {
		maxEntries = maxEntriesPerCategoryDefault
	}
	return &usageAccumulator{
		spec:       spec,
		maxEntries: maxEntries,
		context:    CatalogContext{RootID: spec.ID, HasCustomDataBaseDir: spec.HasCustomDataBaseDir},
		buckets:    map[string]*categoryBucket{},
	}
}

// add ports accumulator.add.
func (a *usageAccumulator) add(entry ScanEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	classification := ClassifyStoragePath(entry.RelativePath, a.context)
	bucket := a.buckets[classification.CategoryID]
	if bucket == nil {
		bucket = &categoryBucket{entries: map[string]*entryAggregate{}}
		a.buckets[classification.CategoryID] = bucket
	}
	bucket.bytes += entry.Bytes
	bucket.fileCount++
	current := bucket.entries[classification.EntryKey]
	if current == nil {
		bucket.entries[classification.EntryKey] = &entryAggregate{bytes: entry.Bytes, fileCount: 1}
	} else {
		current.bytes += entry.Bytes
		current.fileCount++
	}
	a.totalBytes += entry.Bytes
	a.totalFiles++
}

// snapshot ports accumulator.snapshot: every category in CategoryIDs order,
// entries folded to the N largest.
func (a *usageAccumulator) snapshot(volume *Volume) RootUsage {
	a.mu.Lock()
	defer a.mu.Unlock()
	categories := make([]CategoryUsage, 0, len(CategoryIDs))
	for _, id := range CategoryIDs {
		bucket := a.buckets[id]
		category := CategoryUsage{ID: id, Cleanability: CleanabilityOf(id), Entries: []EntryUsage{}}
		if bucket != nil {
			category.Bytes = bucket.bytes
			category.FileCount = bucket.fileCount
			category.Entries = foldEntries(bucket.entries, a.maxEntries)
		}
		categories = append(categories, category)
	}
	return RootUsage{
		ID: a.spec.ID, Path: a.spec.Path, Volume: volume,
		Bytes: a.totalBytes, FileCount: a.totalFiles, Categories: categories,
	}
}

// foldEntries ports foldEntries: sort by bytes desc (path as tiebreaker),
// keep the top maxEntries, fold the rest into MoreEntriesPath.
func foldEntries(entries map[string]*entryAggregate, maxEntries int) []EntryUsage {
	sorted := make([]EntryUsage, 0, len(entries))
	for path, usage := range entries {
		sorted = append(sorted, EntryUsage{RelativePath: path, Bytes: usage.bytes, FileCount: usage.fileCount})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Bytes != sorted[j].Bytes {
			return sorted[i].Bytes > sorted[j].Bytes
		}
		return sorted[i].RelativePath < sorted[j].RelativePath
	})
	if len(sorted) <= maxEntries {
		return sorted
	}
	kept := append([]EntryUsage{}, sorted[:maxEntries]...)
	var restBytes int64
	var restCount int
	for _, item := range sorted[maxEntries:] {
		restBytes += item.Bytes
		restCount += item.FileCount
	}
	return append(kept, EntryUsage{RelativePath: MoreEntriesPath, Bytes: restBytes, FileCount: restCount})
}
