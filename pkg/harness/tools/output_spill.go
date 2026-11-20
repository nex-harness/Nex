package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultSpillMaxChars = 24000

// SpillLargeOutput writes oversized tool output to a temporary file under os.TempDir()/nex-tool-outputs
// and returns a bounded inline view (head + truncation notice with exact readable file path + tail).
// If fullOutput fits within maxChars, it is returned unchanged with an empty spillPath.
func SpillLargeOutput(toolName, fullOutput string, maxChars int) (inline string, spillPath string) {
	if maxChars <= 0 {
		maxChars = defaultSpillMaxChars
	}
	if len(fullOutput) <= maxChars {
		return fullOutput, ""
	}

	dir := filepath.Join(os.TempDir(), "nex-tool-outputs")
	_ = os.MkdirAll(dir, 0o755)
	safeName := strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(toolName)
	if safeName == "" {
		safeName = "tool"
	}
	if f, err := os.CreateTemp(dir, fmt.Sprintf("nex-%s-*.log", safeName)); err == nil {
		spillPath = f.Name()
		_, _ = f.WriteString(fullOutput)
		_ = f.Close()
	}

	headLen := maxChars / 3
	tailLen := maxChars - headLen
	if headLen < 128 {
		headLen = maxChars / 2
		tailLen = maxChars - headLen
	}
	totalLines := strings.Count(fullOutput, "\n") + 1
	omitted := len(fullOutput) - headLen - tailLen

	var notice string
	if spillPath != "" {
		notice = fmt.Sprintf(
			"\n\n... [output truncated: %d bytes / %d lines total, %d bytes omitted; full output saved to %s — use read(file_path=%q, offset=..., limit=...) to inspect] ...\n\n",
			len(fullOutput), totalLines, omitted, spillPath, spillPath,
		)
	} else {
		notice = fmt.Sprintf(
			"\n\n... [output truncated: %d bytes / %d lines total, %d bytes omitted] ...\n\n",
			len(fullOutput), totalLines, omitted,
		)
	}
	return fullOutput[:headLen] + notice + fullOutput[len(fullOutput)-tailLen:], spillPath
}
