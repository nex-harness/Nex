package tools

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// ReadStateEntry mirrors ReadFileStateEntry (types.ts).
type ReadStateEntry struct {
	Path          string
	Content       string
	Offset        *int
	Limit         *int
	IsPartialView bool
	ReadAt        time.Time
	SourceTool    string
	RevisionID    string
	MtimeMs       *float64
	SizeBytes     *int64
}

// ReadState is the per-session map from a range key to the last read entry.
type ReadState struct {
	entries map[string]ReadStateEntry
}

// NewReadState returns an empty read-state map.
func NewReadState() *ReadState { return &ReadState{entries: map[string]ReadStateEntry{}} }

func (s *ReadState) set(key string, entry ReadStateEntry) {
	if s == nil {
		return
	}
	s.entries[key] = entry
}

// NormalizeMtimeMs mirrors normalizeReadFileStateMtimeMs (floor to whole ms).
func NormalizeMtimeMs(v *float64) *float64 {
	if v == nil {
		return nil
	}
	f := math.Floor(*v)
	return &f
}

// PathKey mirrors normalizeToolPathForComparison for the posix case the tests use.
// Windows drive/UNC handling is applied when the platform asks for it; the tools
// run on the same platform in both stacks, so path comparison is by cleaned
// absolute path plus NFC normalization.
func PathKey(filePath string) string {
	return normalizeForComparison(filePath)
}

// CreateReadStateKey mirrors createReadFileStateKey: path \0 offset \0 limit.
func CreateReadStateKey(filePath string, offset, limit *int) string {
	off := 1
	if offset != nil {
		off = *offset
	}
	lim := ""
	if limit != nil {
		lim = strconv.Itoa(*limit)
	}
	return strings.Join([]string{PathKey(filePath), strconv.Itoa(off), lim}, "\x00")
}

// FindLatest mirrors findLatestReadFileState: the entry for the path with the
// greatest ReadAt (ties keep the later-inserted entry, matching the Node loop).
func (s *ReadState) FindLatest(filePath string) (ReadStateEntry, bool) {
	if s == nil {
		return ReadStateEntry{}, false
	}
	key := PathKey(filePath)
	var latest ReadStateEntry
	found := false
	latestReadAt := math.Inf(-1)
	for _, e := range s.entries {
		if PathKey(e.Path) != key {
			continue
		}
		at := float64(e.ReadAt.UnixNano()) / 1e6
		if at < latestReadAt {
			continue
		}
		latest = e
		latestReadAt = at
		found = true
	}
	return latest, found
}

// isStrictFullRead mirrors isStrictFullRead.
func isStrictFullRead(entry ReadStateEntry) bool {
	if entry.IsPartialView {
		return false
	}
	off := 1
	if entry.Offset != nil {
		off = *entry.Offset
	}
	return off <= 1 && entry.Limit == nil
}

// nowUTC is the clock used for ReadAt ordering.
func nowUTC() time.Time { return time.Now().UTC() }
