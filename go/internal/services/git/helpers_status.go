// helpers_status.go ports the porcelain=v2 status parser and its entry types
// from gitCliHelpers.ts (step 3.3). Split out of helpers.go to stay under the
// 400-line archcheck limit.
package git

import (
	"strconv"
	"strings"
)

type GitStatusEntry struct {
	Path         string
	OriginalPath *string
	Kind         GitChangeKind
	X            *string
	Y            *string
	IsUntracked  bool
	IsConflicted bool
}

// StatusPorcelain is parseStatusPorcelain's result.
type StatusPorcelain struct {
	BranchName         *string
	TrackingBranchName *string
	HeadRefType        GitHeadRefType
	Ahead              int
	Behind             int
	Entries            []GitStatusEntry
}

// inferKindFromStatusCode ports inferKindFromStatusCode.
func inferKindFromStatusCode(statusCode string) GitChangeKind {
	switch statusCode {
	case "A", "?":
		return ChangeKindAdded
	case "D":
		return ChangeKindDeleted
	case "R", "C":
		return ChangeKindRenamed
	default:
		return ChangeKindModified
	}
}

// parseBranchAheadBehind ports parseBranchAheadBehind (+N / -M in one token).
func parseBranchAheadBehind(value string) (ahead, behind int) {
	for i := 0; i < len(value); i++ {
		switch {
		case value[i] == '+':
			digits := takeDigits(value[i+1:])
			if n, err := strconv.Atoi(digits); err == nil {
				ahead = n
			}
			i += len(digits)
		case value[i] == '-':
			digits := takeDigits(value[i+1:])
			if n, err := strconv.Atoi(digits); err == nil {
				behind = n
			}
			i += len(digits)
		}
	}
	return ahead, behind
}

func takeDigits(s string) string {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return s[:end]
}

// parseStatusPorcelain ports parseStatusPorcelain: `git status
// --porcelain=v2 -z` 的价值在于格式稳定，不受本地语言影响。这里集中做一次
// 解析，把 branch/header/rename/unmerged 等低层细节都挡在 repo 层里。
func parseStatusPorcelain(stdout string) StatusPorcelain {
	records := strings.Split(stdout, "\x00")
	parsed := StatusPorcelain{HeadRefType: HeadRefTypeBranch}
	entries := make([]GitStatusEntry, 0, len(records))
	for index := 0; index < len(records); index++ {
		record := records[index]
		if record == "" {
			continue
		}
		switch {
		case strings.HasPrefix(record, "# "):
			applyStatusHeaderRecord(&parsed, record)
		case strings.HasPrefix(record, "? "):
			entries = append(entries, GitStatusEntry{
				Path:         NormalizeGitPath(record[2:]),
				OriginalPath: nil,
				Kind:         ChangeKindAdded,
				X:            nil,
				Y:            ptr("?"),
				IsUntracked:  true,
			})
		case strings.HasPrefix(record, "1 "):
			if match := statusLine1Re.FindStringSubmatch(record); match != nil {
				xy := match[1]
				entries = append(entries, GitStatusEntry{
					Path: NormalizeGitPath(match[2]),
					Kind: inferKindFromStatusCode(xyKind(xy)),
					X:    ptr(string(xy[0])),
					Y:    ptr(string(xy[1])),
				})
			}
		case strings.HasPrefix(record, "2 "):
			if match := statusLine2Re.FindStringSubmatch(record); match != nil {
				xy := match[1]
				originalPath := ""
				if index+1 < len(records) {
					originalPath = records[index+1]
					index++
				}
				var originalPtr *string
				if originalPath != "" {
					originalPtr = ptr(NormalizeGitPath(originalPath))
				}
				entries = append(entries, GitStatusEntry{
					Path:         NormalizeGitPath(match[2]),
					OriginalPath: originalPtr,
					Kind:         ChangeKindRenamed,
					X:            ptr(string(xy[0])),
					Y:            ptr(string(xy[1])),
				})
			}
		case strings.HasPrefix(record, "u "):
			if match := statusLineURe.FindStringSubmatch(record); match != nil {
				xy := match[1]
				entries = append(entries, GitStatusEntry{
					Path:         NormalizeGitPath(match[2]),
					Kind:         ChangeKindModified,
					X:            ptr(string(xy[0])),
					Y:            ptr(string(xy[1])),
					IsConflicted: true,
				})
			}
		}
	}
	parsed.Entries = entries
	return parsed
}

// xyKind mirrors the TS's `xy[0] !== "." ? xy[0] : xy[1]`.
func xyKind(xy string) string {
	if xy[0] != '.' {
		return string(xy[0])
	}
	return string(xy[1])
}

func applyStatusHeaderRecord(parsed *StatusPorcelain, record string) {
	switch {
	case strings.HasPrefix(record, "# branch.head "):
		head := record[len("# branch.head "):]
		if head == "(detached)" {
			parsed.BranchName = nil
			parsed.HeadRefType = HeadRefTypeDetached
		} else {
			parsed.BranchName = ptr(head)
			parsed.HeadRefType = HeadRefTypeBranch
		}
	case strings.HasPrefix(record, "# branch.upstream "):
		parsed.TrackingBranchName = ptr(record[len("# branch.upstream "):])
	case strings.HasPrefix(record, "# branch.ab "):
		parsed.Ahead, parsed.Behind = parseBranchAheadBehind(record[len("# branch.ab "):])
	}
}

// ---------------------------------------------------------------------------
// numstat parsing
// ---------------------------------------------------------------------------

// GitLineStat ports GitLineStat.
