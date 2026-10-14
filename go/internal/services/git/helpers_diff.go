// helpers_diff.go ports the diff-result builders from gitCliHelpers.ts
// (step 3.3): preview text splitting, untracked diffs, truncation and the
// binary/NUL detection. Split out of helpers.go to stay under the 400-line
// archcheck limit.
package git

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func toDiffResult(path string, result GitCommandExecutionResult, opts toDiffResultOptions) GitDiffResult {
	if result.TimedOut {
		return unavailableDiff(path, "Git diff command timed out.")
	}
	if result.OutputTruncated {
		return truncatedDiff(path, "Git diff output exceeded the preview limit.")
	}
	allowed := opts.AllowedExitCodes
	if allowed == nil {
		allowed = []int{0}
	}
	allowedHit := false
	for _, code := range allowed {
		if result.ExitCode != nil && *result.ExitCode == code {
			allowedHit = true
			break
		}
	}
	if !allowedHit {
		return unavailableDiff(path, toResultMessage(result))
	}
	if strings.TrimSpace(result.Stdout) == "" {
		summary := opts.EmptySummary
		if summary == "" {
			summary = "No diff output available."
		}
		return unavailableDiff(path, summary)
	}
	if isBinaryDiff(result.Stdout) {
		summary := opts.BinarySummary
		if summary == "" {
			summary = "Binary diff is not previewable."
		}
		return GitDiffResult{
			Path:          path,
			Availability:  DiffAvailabilityBinary,
			Patch:         nil,
			BeforeContent: nil,
			AfterContent:  nil,
			Summary:       ptr(summary),
		}
	}
	return GitDiffResult{
		Path:          path,
		Availability:  DiffAvailabilityPatch,
		Patch:         ptr(result.Stdout),
		BeforeContent: nil,
		AfterContent:  nil,
	}
}

func unavailableDiff(path, summary string) GitDiffResult {
	return GitDiffResult{
		Path:          path,
		Availability:  DiffAvailabilityUnavailable,
		Patch:         nil,
		BeforeContent: nil,
		AfterContent:  nil,
		Summary:       ptr(summary),
	}
}

func truncatedDiff(path, summary string) GitDiffResult {
	return GitDiffResult{
		Path:          path,
		Availability:  DiffAvailabilityTruncated,
		Patch:         nil,
		BeforeContent: nil,
		AfterContent:  nil,
		Summary:       ptr(summary),
	}
}

// splitUntrackedText ports splitUntrackedText (CRLF fold + trailing-newline rule).
func splitUntrackedText(content string) (lines []string, hasTrailingNewline bool) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if normalized == "" {
		return nil, false
	}
	hasTrailingNewline = strings.HasSuffix(normalized, "\n")
	lines = strings.Split(normalized, "\n")
	if hasTrailingNewline {
		lines = lines[:len(lines)-1]
	}
	return lines, hasTrailingNewline
}

// buildUntrackedTextDiffResult ports buildUntrackedTextDiffResult: 未跟踪文件
// 的 patch 不依赖 `git diff --no-index`（Windows 头部问题），统一退回最稳定的
// unified diff 形态。ok=false 表示读取失败，调用方降级。
func buildUntrackedTextDiffResult(absolutePath, repoRelativePath string, maxPreviewBytes int64) (GitDiffResult, bool) {
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return GitDiffResult{}, false
	}
	if bytesContainsNul(content) {
		return unavailableKindDiff(absolutePath, DiffAvailabilityBinary, "Binary diff is not previewable."), true
	}
	if int64(len(content)) > maxPreviewBytes {
		return unavailableKindDiff(absolutePath, DiffAvailabilityTruncated, "Git diff output exceeded the preview limit."), true
	}
	normalizedPath := NormalizeGitPath(repoRelativePath)
	lines, hasTrailingNewline := splitUntrackedText(string(content))
	patchLines := []string{"--- /dev/null", "+++ b/" + normalizedPath}
	if len(lines) > 0 {
		patchLines = append(patchLines, fmt.Sprintf("@@ -0,0 +1,%d @@", len(lines)))
		for _, line := range lines {
			patchLines = append(patchLines, "+"+line)
		}
		if !hasTrailingNewline {
			patchLines = append(patchLines, "\\ No newline at end of file")
		}
	}
	return GitDiffResult{
		Path:          absolutePath,
		Availability:  DiffAvailabilityPatch,
		Patch:         ptr(strings.Join(patchLines, "\n") + "\n"),
		BeforeContent: ptr(""),
		AfterContent:  ptr(string(content)),
	}, true
}

func unavailableKindDiff(path string, availability GitDiffAvailability, summary string) GitDiffResult {
	return GitDiffResult{
		Path:          path,
		Availability:  availability,
		Patch:         nil,
		BeforeContent: nil,
		AfterContent:  nil,
		Summary:       ptr(summary),
	}
}

func bytesContainsNul(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// isPreviewableText ports isPreviewableText (no NUL byte).
func isPreviewableText(content string) bool {
	return !strings.ContainsRune(content, 0)
}

// ---------------------------------------------------------------------------
// config value / input path helpers
// ---------------------------------------------------------------------------

// gitConfigValue ports parseGitConfigValue's result.
type gitConfigValue struct {
	Scope  *string
	Source *string
	Value  *string
}

// parseGitConfigValue ports parseGitConfigValue (`--show-scope --show-origin`
// tab-separated output).
func parseGitConfigValue(result GitCommandExecutionResult) (gitConfigValue, error) {
	if result.ExitCode != nil && *result.ExitCode == 1 {
		return gitConfigValue{}, nil
	}
	if err := ensureGitCommandSucceeded("git config", result); err != nil {
		return gitConfigValue{}, err
	}
	line := strings.TrimRight(result.Stdout, "\r\n")
	// TS: replace(/\r?\n$/, "")
	if strings.HasSuffix(line, "\n") {
		line = line[:len(line)-1]
		if strings.HasSuffix(line, "\r") {
			line = line[:len(line)-1]
		}
	}
	parts := strings.Split(line, "\t")
	if len(parts) < 3 {
		value := (*string)(nil)
		if line != "" {
			value = ptr(line)
		}
		return gitConfigValue{Value: value}, nil
	}
	value := strings.Join(parts[2:], "\t")
	var valuePtr *string
	if value != "" {
		valuePtr = ptr(value)
	}
	scope := parts[0]
	source := parts[1]
	return gitConfigValue{Scope: &scope, Source: &source, Value: valuePtr}, nil
}

// normalizeInputPath ports normalizeInputPath: resolve against the workspace,
// realpath-or-fallback, then repo-relative — rejecting anything that escapes
// the repository.
func normalizeInputPath(resolution GitResolvedRepository, path string) (string, error) {
	rawAbsolutePath := path
	if !filepath.IsAbs(path) {
		rawAbsolutePath = filepath.Join(resolution.WorkspacePath, strings.ReplaceAll(path, "/", string(filepath.Separator)))
	}
	absolutePath := rawAbsolutePath
	if resolved, err := filepath.EvalSymlinks(rawAbsolutePath); err == nil {
		absolutePath = resolved
	}
	repoRelativePath := NormalizeGitPath(relPath(resolution.RepoRoot, absolutePath))
	if repoRelativePath == "" || repoRelativePath == "." || repoRelativePath == ".." || strings.HasPrefix(repoRelativePath, "../") {
		return "", fmt.Errorf("Path is outside repository scope: %s", path)
	}
	return repoRelativePath, nil
}

// relPath is filepath.Rel with the TS `relative()` semantics (already-clean inputs).
func relPath(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		// Fall back to the raw target; the caller's scope check still rejects it.
		return target
	}
	return rel
}

// sortStrings is a tiny helper used by deterministic outputs.
func sortStrings(values []string) {
	sort.Strings(values)
}
