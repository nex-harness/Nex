// helpers.go ports packages/services/src/git/repo/gitCliHelpers.ts (step 3.3):
// porcelain v2 status parsing, numstat parsing, branch-mutation issue mapping,
// diff result shaping, untracked line counting, and the repository guards.
// Every regexp, string match and message is transcribed from the TS.
package git

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// result guards (ensureGitCommandSucceeded and friends)
// ---------------------------------------------------------------------------

func toResultMessage(result GitCommandExecutionResult) string {
	message := strings.TrimSpace(result.Stderr)
	if message == "" {
		message = strings.TrimSpace(result.Stdout)
	}
	if message == "" {
		return fmt.Sprintf("exitCode=%v", exitCodeString(result))
	}
	return message
}

func exitCodeString(result GitCommandExecutionResult) string {
	if result.ExitCode == nil {
		return "null"
	}
	return strconv.Itoa(*result.ExitCode)
}

// ensureGitCommandSucceeded ports ensureGitCommandSucceeded. allowedExitCodes
// defaults to {0}.
func ensureGitCommandSucceeded(label string, result GitCommandExecutionResult, allowedExitCodes ...int) error {
	if len(allowedExitCodes) == 0 {
		allowedExitCodes = []int{0}
	}
	if result.TimedOut {
		// timeout 阈值和进程清理总耗时不是一回事；保留 elapsed 与超时配置。
		return fmt.Errorf("%s timed out after %dms (elapsed=%dms)", label, result.TimeoutMs, result.DurationMs)
	}
	if result.OutputTruncated {
		return fmt.Errorf("%s output exceeded limit", label)
	}
	for _, code := range allowedExitCodes {
		if result.ExitCode != nil && *result.ExitCode == code {
			return nil
		}
	}
	return fmt.Errorf("%s failed: %s", label, toResultMessage(result))
}

// isNotRepositoryResult ports isNotRepositoryResult.
func isNotRepositoryResult(result GitCommandExecutionResult) bool {
	stderr := strings.ToLower(result.Stderr)
	return strings.Contains(stderr, "not a git repository") || strings.Contains(stderr, "outside repository")
}

// isMissingWorkingDirectoryResult ports isMissingWorkingDirectoryResult.
// The TS checks exitCode === -2 (its spawn-error code) or stderr markers.
func isMissingWorkingDirectoryResult(result GitCommandExecutionResult) bool {
	stderr := strings.ToLower(result.Stderr)
	if result.ExitCode != nil && *result.ExitCode == -2 && strings.Contains(stderr, "enoent") {
		return true
	}
	return strings.Contains(stderr, "unable to read current working directory") ||
		strings.Contains(stderr, "no such file or directory")
}

// ensureRepositoryAvailable ports ensureRepositoryAvailable.
func ensureRepositoryAvailable(resolution GitResolvedRepository, label string) (GitResolvedRepository, error) {
	if !resolution.IsGitAvailable {
		return GitResolvedRepository{}, fmt.Errorf("Cannot %s: Git binary is not available", label)
	}
	if !resolution.IsRepository {
		return GitResolvedRepository{}, fmt.Errorf("Cannot %s: workspace is not inside a Git repository", label)
	}
	return resolution, nil
}

// ---------------------------------------------------------------------------
// branch mutation issues (parseGitBranchMutationIssues)
// ---------------------------------------------------------------------------

var (
	trackedOverwriteHeader   = regexp.MustCompile(`(?i)your local changes to the following files would be overwritten by (checkout|switch)`)
	untrackedOverwriteHeader = regexp.MustCompile(`(?i)the following untracked working tree files would be overwritten by (checkout|switch)`)
	indentedLine             = regexp.MustCompile(`^\s+`)
	operationInProgressRe    = regexp.MustCompile(`cannot switch branch while (merging|rebasing|cherry-picking|reverting|bisecting)`)
)

// toInvalidBranchNameIssue ports toInvalidBranchNameIssue.
func toInvalidBranchNameIssue(detail string) GitBranchMutationIssue {
	trimmed := strings.TrimSpace(detail)
	var detailPtr *string
	if trimmed != "" {
		detailPtr = &trimmed
	}
	return GitBranchMutationIssue{
		Code:    "invalid-branch-name",
		Message: "Branch name is invalid.",
		Detail:  detailPtr,
	}
}

func ptr(s string) *string { return &s }

// toNormalizedLines ports toNormalizedLines: CRLF fold + trailing whitespace strip.
func toNormalizedLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\v\f\r\n")
	}
	return lines
}

// extractIndentedPaths ports extractIndentedPaths.
func extractIndentedPaths(lines []string, header *regexp.Regexp) []string {
	headerIndex := -1
	for i, line := range lines {
		if header.MatchString(strings.ToLower(line)) {
			headerIndex = i
			break
		}
	}
	if headerIndex < 0 {
		return nil
	}
	var paths []string
	for i := headerIndex + 1; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		if !indentedLine.MatchString(line) {
			break
		}
		value := strings.TrimSpace(line)
		if value != "" {
			paths = append(paths, NormalizeGitPath(value))
		}
	}
	return paths
}

// parseGitBranchMutationIssues ports parseGitBranchMutationIssues: 把常见
// stderr 归一成稳定 issue code，避免 UI 直接依赖易变的原始文案。
func parseGitBranchMutationIssues(result GitCommandExecutionResult) []GitBranchMutationIssue {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	lines := toNormalizedLines(detail)
	normalizedDetail := strings.ToLower(detail)
	var detailPtr *string
	if detail != "" {
		detailPtr = &detail
	}

	if paths := extractIndentedPaths(lines, trackedOverwriteHeader); len(paths) > 0 {
		return []GitBranchMutationIssue{{
			Code:    "tracked-changes-would-be-overwritten",
			Message: "Tracked changes would be overwritten by switching branches.",
			Paths:   paths,
			Detail:  detailPtr,
		}}
	}
	if paths := extractIndentedPaths(lines, untrackedOverwriteHeader); len(paths) > 0 {
		return []GitBranchMutationIssue{{
			Code:    "untracked-changes-would-be-overwritten",
			Message: "Untracked files would be overwritten by switching branches.",
			Paths:   paths,
			Detail:  detailPtr,
		}}
	}
	switch {
	case strings.Contains(normalizedDetail, "already exists"):
		return []GitBranchMutationIssue{{Code: "branch-already-exists", Message: "Branch already exists.", Detail: detailPtr}}
	case strings.Contains(normalizedDetail, "invalid reference:"):
		return []GitBranchMutationIssue{{Code: "target-branch-not-found", Message: "Target branch was not found.", Detail: detailPtr}}
	case strings.Contains(normalizedDetail, "is already used by worktree at"):
		return []GitBranchMutationIssue{{Code: "branch-in-other-worktree", Message: "Branch is already checked out in another worktree.", Detail: detailPtr}}
	case strings.Contains(normalizedDetail, "resolve your current index first"):
		return []GitBranchMutationIssue{{Code: "conflicts-present", Message: "Repository still has unresolved conflicts.", Detail: detailPtr}}
	case operationInProgressRe.MatchString(normalizedDetail),
		strings.Contains(normalizedDetail, "you have not concluded your merge"),
		strings.Contains(normalizedDetail, "rebase in progress"):
		return []GitBranchMutationIssue{{Code: "operation-in-progress", Message: "Another Git operation is still in progress.", Detail: detailPtr}}
	}
	return []GitBranchMutationIssue{{Code: "unknown", Message: "Git could not complete the branch operation.", Detail: detailPtr}}
}

// ---------------------------------------------------------------------------
// porcelain v2 status parsing
// ---------------------------------------------------------------------------

var (
	statusLine1Re = regexp.MustCompile(`^1 ([^ ]{2}) [^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+ (.+)$`)
	statusLine2Re = regexp.MustCompile(`^2 ([^ ]{2}) [^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+ (.+)$`)
	statusLineURe = regexp.MustCompile(`^u ([^ ]{2}) [^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+ (.+)$`)
)

// GitStatusEntry ports GitStatusEntry.
func joinRepoPath(repoRoot, repoRelativePath string) string {
	parts := strings.Split(NormalizeGitPath(repoRelativePath), "/")
	return filepath.Join(append([]string{repoRoot}, parts...)...)
}

// countUntrackedFileLines ports countUntrackedFileLines: NUL byte => binary =>
// 0（保留条目、跳过统计）；超过预算返回 0；正常情况返回行数（含"最后一行
// 无换行符"的 +1 规则）。ok=false 表示 stat/read 失败，调用方按 {0,0} 记录。
func countUntrackedFileLines(absolutePath string, buffer []byte) (lines int, ok bool) {
	info, err := os.Stat(absolutePath)
	if err != nil || info.IsDir() || info.Size() > GitUntrackedStatMaxBytes {
		return 0, true
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, len(buffer))
	var totalBytes int64
	newlines := 0
	lastByte := byte(10)
	for totalBytes <= GitUntrackedStatMaxBytes {
		// 文件可能在 stat 后增长；实际读取也必须受预算约束。
		length := int64(len(buffer))
		if remaining := GitUntrackedStatMaxBytes + 1 - totalBytes; remaining < length {
			length = remaining
		}
		n, err := reader.Read(buffer[:length])
		if n > 0 {
			totalBytes += int64(n)
			if totalBytes > GitUntrackedStatMaxBytes {
				return 0, true
			}
			for i := 0; i < n; i++ {
				if buffer[i] == 0 {
					return 0, true
				}
				if buffer[i] == 10 {
					newlines++
				}
			}
			lastByte = buffer[n-1]
		}
		if err != nil {
			if err == io.EOF {
				if lastByte == 10 {
					return newlines, true
				}
				return newlines + 1, true
			}
			return 0, false
		}
	}
	return 0, true
}

// fileExists ports fileExists (access-based).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ---------------------------------------------------------------------------
// diff result shaping
// ---------------------------------------------------------------------------

func isBinaryDiff(stdout string) bool {
	return strings.Contains(stdout, "GIT binary patch") || strings.Contains(stdout, "Binary files ")
}

// toDiffResultOptions ports the toDiffResult options bag.
type toDiffResultOptions struct {
	AllowedExitCodes []int
	EmptySummary     string
	BinarySummary    string
}

// toDiffResult ports toDiffResult.
