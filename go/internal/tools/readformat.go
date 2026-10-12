package tools

import (
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
)

// FormatReadModelContent mirrors formatReadOutput for the shapes Go supports.
func FormatReadModelContent(out *ReadOutput) string {
	switch out.Type {
	case "file_unchanged":
		return fileUnchangedStub
	default:
		return formatReadTextOutput(out)
	}
}

func formatReadTextOutput(out *ReadOutput) string {
	if out.Content == "" {
		if out.TotalLines == 0 {
			return formatReadToolResultWarning("Warning: the file exists but the contents are empty.")
		}
		return formatReadToolResultWarning("Warning: the file exists but is shorter than the provided offset (" + strconv.Itoa(out.StartLine) + "). The file has " + strconv.Itoa(out.TotalLines) + " lines.")
	}
	return AddReadLineNumbers(out.Content, out.StartLine)
}

func formatReadToolResultWarning(body string) string {
	return "<system-reminder>" + body + "</system-reminder>"
}

// AddReadLineNumbers mirrors addReadLineNumbers: split on /\r?\n/ and prefix each
// line with `<index+startLine>\t`.
func AddReadLineNumbers(content string, startLine int) string {
	lines := splitCRLFLines(content)
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strconv.Itoa(i + startLine))
		b.WriteByte('\t')
		b.WriteString(line)
	}
	return b.String()
}

// splitCRLFLines mirrors JS `content.split(/\r?\n/)`. Content reaching here is
// already LF-normalized, but keep the CR-strip for direct calls.
func splitCRLFLines(content string) []string {
	parts := strings.Split(content, "\n")
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

// readStatError mirrors the not_found branch: a missing file yields a message with
// a "did you mean" suggestion; other stat errors are rethrown.
func readStatError(err error, filePath string, c *Context) error {
	code := fsErrorCode(err)
	if code == "not_found" {
		return &Failure{ErrorCode: errorCodeReadFileNotFound, Message: missingReadFileMessage(filePath, c)}
	}
	return err
}

func readRangeError(err error, filePath string, c *Context) error {
	code := fsErrorCode(err)
	switch code {
	case "not_found":
		return &Failure{ErrorCode: errorCodeReadFileNotFound, Message: missingReadFileMessage(filePath, c)}
	case "too_large":
		return &Failure{ErrorCode: errorCodeReadFileTooLarge, Message: fsErrorMessage(err)}
	}
	return err
}

const (
	errorCodeReadFileNotFound = 1
	errorCodeReadFileTooLarge = 2
)

func missingReadFileMessage(filePath string, c *Context) string {
	suggestion := findSimilarFilename(filePath, c)
	msg := "File does not exist. Note: your current working directory is " + c.WorkingDirectory + "."
	if suggestion != "" {
		msg += " Did you mean " + suggestion + "?"
	}
	return msg
}

func findSimilarFilename(filePath string, c *Context) string {
	parent := path.Dir(filePath)
	targetName := path.Base(filePath)
	targetStem := stem(targetName)
	names, err := c.FS.ListDirectory(c.Ctx, parent)
	if err != nil {
		return ""
	}
	filtered := make([]string, 0, len(names))
	for _, n := range names {
		if n != targetName {
			filtered = append(filtered, n)
		}
	}
	sort.Strings(filtered)
	for _, n := range filtered {
		if stem(n) == targetStem {
			return n
		}
	}
	for _, n := range filtered {
		if levenshtein(n, targetName) <= 3 {
			return n
		}
	}
	return ""
}

func stem(name string) string {
	ext := path.Ext(name)
	if ext == "" {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

func levenshtein(left, right string) int {
	l := []rune(left)
	r := []rune(right)
	previous := make([]int, len(r)+1)
	current := make([]int, len(r)+1)
	for i := range previous {
		previous[i] = i
	}
	for i := 1; i <= len(l); i++ {
		current[0] = i
		for j := 1; j <= len(r); j++ {
			cost := 1
			if l[i-1] == r[j-1] {
				cost = 0
			}
			current[j] = min3(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
		}
		copy(previous, current)
	}
	if len(r) == 0 {
		return len(l)
	}
	return previous[len(r)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func fsErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func fsErrorMessage(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}
