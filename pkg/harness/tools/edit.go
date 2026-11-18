package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
)

type editArgs struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// EditTool performs exact or whitespace-tolerant string replacement while preserving BOM and CRLF/LF endings.
type EditTool struct{}

func NewEditTool() *EditTool { return &EditTool{} }

func (t *EditTool) Name() string                      { return "edit" }
func (t *EditTool) Category() string                  { return "system" }
func (t *EditTool) IsReadOnly(_ json.RawMessage) bool { return false }

func (t *EditTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "edit",
		Category:    "system",
		Description: "Replace a specific snippet in a file. Automatically preserves UTF-8 BOM and CRLF/LF line endings, and emits a unified diff.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "Path to the file to modify"},
				"old_string": {"type": "string", "description": "Exact snippet to locate and replace"},
				"new_string": {"type": "string", "description": "Replacement snippet"},
				"replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring uniqueness"}
			},
			"required": ["file_path", "old_string", "new_string"]
		}`),
	}
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func (t *EditTool) Execute(_ context.Context, workDir string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args editArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid edit arguments: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.FilePath) == "" {
		return Result{Output: "file_path is required", IsError: true}, nil
	}
	if args.OldString == "" {
		return Result{Output: "old_string must not be empty", IsError: true}, nil
	}

	absPath := ResolvePath(workDir, args.FilePath)
	info, err := os.Stat(absPath)
	if err != nil {
		return Result{Output: fmt.Sprintf("cannot read %s: %v", args.FilePath, err), IsError: true}, nil
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return Result{Output: fmt.Sprintf("read %s failed: %v", args.FilePath, err), IsError: true}, nil
	}

	hasBOM := bytes.HasPrefix(raw, utf8BOM)
	if hasBOM {
		raw = raw[len(utf8BOM):]
	}

	hasCRLF := bytes.Contains(raw, []byte("\r\n"))
	origLF := strings.ReplaceAll(string(raw), "\r\n", "\n")
	oldLF := strings.ReplaceAll(args.OldString, "\r\n", "\n")
	newLF := strings.ReplaceAll(args.NewString, "\r\n", "\n")

	if oldLF == newLF {
		return Result{Output: "new_string is identical to old_string; no changes made", IsError: true}, nil
	}

	var updatedLF string
	replacedCount := 0

	exactCount := strings.Count(origLF, oldLF)
	if exactCount > 0 {
		if !args.ReplaceAll && exactCount > 1 {
			return Result{
				Output:  fmt.Sprintf("old_string matched %d times in %s; include more surrounding lines or set replace_all=true", exactCount, args.FilePath),
				IsError: true,
			}, nil
		}
		if args.ReplaceAll {
			updatedLF = strings.ReplaceAll(origLF, oldLF, newLF)
			replacedCount = exactCount
		} else {
			updatedLF = strings.Replace(origLF, oldLF, newLF, 1)
			replacedCount = 1
		}
	} else {
		// Fallback: line-by-line match tolerating trailing horizontal whitespace differences
		var matchErr string
		updatedLF, replacedCount, matchErr = replaceTrailingWhitespaceTolerant(origLF, oldLF, newLF, args.ReplaceAll)
		if matchErr != "" {
			return Result{Output: fmt.Sprintf("edit %s failed: %s", args.FilePath, matchErr), IsError: true}, nil
		}
	}

	outStr := updatedLF
	if hasCRLF {
		outStr = strings.ReplaceAll(updatedLF, "\n", "\r\n")
	}

	var finalBytes []byte
	if hasBOM {
		finalBytes = make([]byte, 0, len(utf8BOM)+len(outStr))
		finalBytes = append(finalBytes, utf8BOM...)
		finalBytes = append(finalBytes, outStr...)
	} else {
		finalBytes = []byte(outStr)
	}

	if err := os.WriteFile(absPath, finalBytes, info.Mode().Perm()); err != nil {
		return Result{Output: fmt.Sprintf("write %s failed: %v", args.FilePath, err), IsError: true}, nil
	}

	relName := args.FilePath
	if workDir != "" {
		if rel, err := filepath.Rel(workDir, absPath); err == nil && !strings.HasPrefix(rel, "..") {
			relName = filepath.ToSlash(rel)
		}
	}
	diff, adds, dels := GenerateUnifiedDiff(relName, origLF, updatedLF)

	return Result{
		Output:    fmt.Sprintf("Edited %s (%d replacement(s), +%d -%d lines)", relName, replacedCount, adds, dels),
		Diff:      diff,
		Additions: adds,
		Deletions: dels,
	}, nil
}

func trimRightHoriz(s string) string {
	return strings.TrimRight(s, " \t")
}

func replaceTrailingWhitespaceTolerant(haystack, needle, replacement string, replaceAll bool) (string, int, string) {
	hLines := strings.Split(haystack, "\n")
	nLines := strings.Split(needle, "\n")
	if len(nLines) == 0 || len(nLines) > len(hLines) {
		return "", 0, "old_string not found in file"
	}

	var matchStarts []int
	for i := 0; i <= len(hLines)-len(nLines); i++ {
		ok := true
		for j := 0; j < len(nLines); j++ {
			if trimRightHoriz(hLines[i+j]) != trimRightHoriz(nLines[j]) {
				ok = false
				break
			}
		}
		if ok {
			matchStarts = append(matchStarts, i)
		}
	}

	if len(matchStarts) == 0 {
		return "", 0, "old_string not found in file"
	}
	if !replaceAll && len(matchStarts) > 1 {
		return "", 0, fmt.Sprintf("old_string matched %d locations (ignoring trailing spaces); provide more context", len(matchStarts))
	}

	rLines := strings.Split(replacement, "\n")
	var outLines []string
	cursor := 0
	applied := 0
	for _, start := range matchStarts {
		if start < cursor {
			continue
		}
		outLines = append(outLines, hLines[cursor:start]...)
		outLines = append(outLines, rLines...)
		cursor = start + len(nLines)
		applied++
		if !replaceAll {
			break
		}
	}
	outLines = append(outLines, hLines[cursor:]...)
	return strings.Join(outLines, "\n"), applied, ""
}

// GenerateUnifiedDiff computes a compact unified diff and (+/-) line statistics between two LF strings.
func GenerateUnifiedDiff(relPath, oldStr, newStr string) (string, int, int) {
	if oldStr == newStr {
		return "", 0, 0
	}
	oldLines := splitDiffLines(oldStr)
	newLines := splitDiffLines(newStr)

	ops := diffLinesLCS(oldLines, newLines)
	adds, dels := 0, 0
	for _, op := range ops {
		switch op.kind {
		case '+':
			adds++
		case '-':
			dels++
		}
	}

	const contextRadius = 3
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind != ' ' {
			lo := i - contextRadius
			if lo < 0 {
				lo = 0
			}
			hi := i + contextRadius
			if hi >= len(ops) {
				hi = len(ops) - 1
			}
			for k := lo; k <= hi; k++ {
				keep[k] = true
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n", relPath, relPath)

	i := 0
	for i < len(ops) {
		if !keep[i] {
			i++
			continue
		}
		hunkStart := i
		for i < len(ops) && keep[i] {
			i++
		}
		hunkEnd := i

		oldStart, oldCount := 0, 0
		newStart, newCount := 0, 0
		for k := hunkStart; k < hunkEnd; k++ {
			op := ops[k]
			if op.kind != '+' {
				if oldStart == 0 {
					oldStart = op.oldLine
				}
				oldCount++
			}
			if op.kind != '-' {
				if newStart == 0 {
					newStart = op.newLine
				}
				newCount++
			}
		}
		if oldStart == 0 {
			oldStart = 1
		}
		if newStart == 0 {
			newStart = 1
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for k := hunkStart; k < hunkEnd; k++ {
			fmt.Fprintf(&sb, "%c%s\n", ops[k].kind, ops[k].text)
		}
	}

	return strings.TrimRight(sb.String(), "\n"), adds, dels
}

func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type diffOp struct {
	kind    byte // ' ', '+', '-'
	text    string
	oldLine int
	newLine int
}

func diffLinesLCS(a, b []string) []diffOp {
	// Trim common prefix & suffix first so large files diff instantaneously in O(changed_region^2)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}

	midA := a[prefix : len(a)-suffix]
	midB := b[prefix : len(b)-suffix]

	var ops []diffOp
	for i := 0; i < prefix; i++ {
		ops = append(ops, diffOp{kind: ' ', text: a[i], oldLine: i + 1, newLine: i + 1})
	}

	// Guard against pathological huge middle regions (> 2000 lines changed at once)
	if len(midA)*len(midB) > 4_000_000 {
		for i, line := range midA {
			ops = append(ops, diffOp{kind: '-', text: line, oldLine: prefix + i + 1})
		}
		for j, line := range midB {
			ops = append(ops, diffOp{kind: '+', text: line, newLine: prefix + j + 1})
		}
	} else {
		m, n := len(midA), len(midB)
		dp := make([][]int, m+1)
		for i := range dp {
			dp[i] = make([]int, n+1)
		}
		for i := m - 1; i >= 0; i-- {
			for j := n - 1; j >= 0; j-- {
				if midA[i] == midB[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else if dp[i+1][j] >= dp[i][j+1] {
					dp[i][j] = dp[i+1][j]
				} else {
					dp[i][j] = dp[i][j+1]
				}
			}
		}
		i, j := 0, 0
		for i < m || j < n {
			if i < m && j < n && midA[i] == midB[j] {
				ops = append(ops, diffOp{kind: ' ', text: midA[i], oldLine: prefix + i + 1, newLine: prefix + j + 1})
				i++
				j++
			} else if j == n || (i < m && dp[i+1][j] >= dp[i][j+1]) {
				ops = append(ops, diffOp{kind: '-', text: midA[i], oldLine: prefix + i + 1})
				i++
			} else {
				ops = append(ops, diffOp{kind: '+', text: midB[j], newLine: prefix + j + 1})
				j++
			}
		}
	}

	for k := 0; k < suffix; k++ {
		oldIdx := len(a) - suffix + k
		newIdx := len(b) - suffix + k
		ops = append(ops, diffOp{kind: ' ', text: a[oldIdx], oldLine: oldIdx + 1, newLine: newIdx + 1})
	}
	return ops
}
