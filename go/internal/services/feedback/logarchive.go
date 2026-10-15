// logarchive.go holds the diagnostic log archive: the tar.gz writer, the
// UTF-16/printable validation and the cleanup. Split out of feedback.go so
// the service stays short; symbols are unchanged.
package feedback

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

func newLogArchive(sources []ArchiveSource, outputRoot, appVersion, commit, runtimeVersion string) *logArchive {
	return &logArchive{sources: sources, outputRoot: outputRoot, appVersion: appVersion, commit: commit, runtimeVersion: runtimeVersion}
}

type preparedArchive struct {
	Path string
	Size int64
}

// Prepare ports prepareCompactLogArchive + createFeedbackDiagnosticArchive:
// whitelist sources, bounded reads, no raw fallback when the bytes are not
// safely decodable text, redaction before packaging.
func (a *logArchive) Prepare(ctx context.Context, full bool, onProgress func(processed, total int64)) (preparedArchive, error) {
	budget := int64(archiveMaxTotalBytes)
	if !full {
		budget = compactMaxTotalBytes
	}
	now := time.Now()
	// 按本地自然日筛选文件；次日零点由日历计算，兼容夏令时的 23/25 小时日。
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	dayEnd := dayStart.Add(24 * time.Hour)
	isToday := func(mtime time.Time) bool {
		return !mtime.Before(dayStart) && mtime.Before(dayEnd)
	}
	if onProgress != nil {
		onProgress(0, 0)
	}
	if err := os.MkdirAll(a.outputRoot, 0o755); err != nil {
		return preparedArchive{}, err
	}
	outputDir, err := os.MkdirTemp(a.outputRoot, "archive-")
	if err != nil {
		return preparedArchive{}, err
	}
	archivePath := filepath.Join(outputDir, "nex-diagnostic-logs.zip")
	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	skipped := map[string]int{}
	skip := func(reason string) { skipped[reason]++ }
	var totalBytes int64
	visited := 0

	for _, source := range a.sources {
		root, err := filepath.EvalSymlinks(source.Directory)
		if err != nil {
			continue
		}
		info, err := os.Lstat(source.Directory)
		if err != nil || !info.IsDir() {
			continue
		}
		// 不跟随诊断根目录本身的链接；规范化系统目录别名（如 macOS /var）。
		var walk func(directory, prefix string, depth int) error
		walk = func(directory, prefix string, depth int) error {
			if depth > archiveMaxDepth || visited >= archiveMaxVisited {
				return nil
			}
			names, err := os.ReadDir(directory)
			if err != nil {
				return nil
			}
			sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
			for _, name := range names {
				visited++
				if visited > archiveMaxVisited {
					return nil
				}
				if name.Type() == os.ModeSymlink {
					continue
				}
				absolutePath := filepath.Join(directory, name.Name())
				archiveName := strings.TrimPrefix(prefix+"/"+name.Name(), "/")
				if name.IsDir() {
					if err := walk(absolutePath, archiveName, depth+1); err != nil {
						return err
					}
					continue
				}
				if !name.Type().IsRegular() || !diagnosticFileRe.MatchString(name.Name()) {
					continue
				}
				resolved, err := filepath.EvalSymlinks(absolutePath)
				if err != nil || resolved != absolutePath {
					skip("unsafe-path")
					continue
				}
				info, err := os.Lstat(absolutePath)
				if err != nil || !info.Mode().IsRegular() ||
					info.Size() > archiveMaxFileBytes || totalBytes+info.Size() > budget ||
					!isToday(info.ModTime()) {
					skip("metadata-policy")
					continue
				}
				// O_NOFOLLOW equivalent: open, then verify the opened file's
				// identity (dev/inode/size/mtime) still matches the stat.
				file, err := os.Open(absolutePath)
				if err != nil {
					skip("open-failed")
					continue
				}
				opened, err := file.Stat()
				if err != nil || !opened.Mode().IsRegular() ||
					sameFile(info, opened) != true ||
					!isToday(opened.ModTime()) || opened.Size() > archiveMaxFileBytes ||
					totalBytes+opened.Size() > budget {
					file.Close()
					skip("opened-file-policy")
					continue
				}
				bytesRead, readErr := io.ReadAll(io.LimitReader(file, opened.Size()+1))
				file.Close()
				if readErr != nil || int64(len(bytesRead)) != opened.Size() {
					skip("short-read")
					continue
				}
				text, ok := decodeDiagnosticLog(bytesRead)
				if !ok {
					skip("unsupported-text")
					continue
				}
				data := []byte(redactDiagnostic(text))
				if int64(len(data)) > archiveMaxFileBytes || totalBytes+int64(len(data)) > budget {
					skip("redacted-size-limit")
					continue
				}
				totalBytes += int64(len(data))
				entries = append(entries, entry{name: archiveName, data: data})
			}
			return nil
		}
		if err := walk(root, source.ArchivePrefix, 0); err != nil {
			return preparedArchive{}, err
		}
	}

	zipBuffer := &bytes.Buffer{}
	zipWriter := zip.NewWriter(zipBuffer)
	for _, e := range entries {
		writer, err := zipWriter.Create(e.name)
		if err != nil {
			os.RemoveAll(outputDir)
			return preparedArchive{}, err
		}
		if _, err := writer.Write(e.data); err != nil {
			os.RemoveAll(outputDir)
			return preparedArchive{}, err
		}
	}
	about := fmt.Sprintf(
		"Nex diagnostic logs\ntimestamp: %s\nappVersion: %s\ncommit: %s\nnode: %s\nincludedLogFiles: %d\nskippedLogFiles: %d\nskippedLogFilesByReason: %s\nScope: diagnostic text log files modified today (local time); credentials and structured payloads redacted.\n",
		now.UTC().Format(time.RFC3339), a.appVersion, a.commit, a.runtimeVersion,
		len(entries), sumValues(skipped), marshalSkipped(skipped))
	writer, err := zipWriter.Create("about.txt")
	if err == nil {
		_, err = writer.Write([]byte(about))
	}
	if err != nil {
		os.RemoveAll(outputDir)
		return preparedArchive{}, err
	}
	if err := zipWriter.Close(); err != nil {
		os.RemoveAll(outputDir)
		return preparedArchive{}, err
	}
	if err := os.WriteFile(archivePath, zipBuffer.Bytes(), 0o644); err != nil {
		os.RemoveAll(outputDir)
		return preparedArchive{}, err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		os.RemoveAll(outputDir)
		return preparedArchive{}, err
	}
	if onProgress != nil {
		onProgress(info.Size(), info.Size())
	}
	return preparedArchive{Path: archivePath, Size: info.Size()}, nil
}

func sameFile(a, b os.FileInfo) bool {
	type sys interface{ Sys() any }
	_ = sys(nil)
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && os.SameFile(a, b)
}

func sumValues(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func marshalSkipped(counts map[string]int) string {
	if len(counts) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%q:%d", key, counts[key]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Cleanup ports cleanupLogArchive: removes the archive file's parent dir
// (the mkdtemp layout) and nothing wider.
func (a *logArchive) Cleanup(path string) {
	if path == "" {
		return
	}
	os.RemoveAll(filepath.Dir(path))
}

// decodeDiagnosticLog ports decodeDiagnosticLog: UTF-16LE/BE BOM detection
// plus printable-text validation; undecodable bytes never fall through to a
// raw include.
func decodeDiagnosticLog(data []byte) (string, bool) {
	if len(data) >= 2 {
		if data[0] == 0xff && data[1] == 0xfe {
			return decodeUTF16(data[2:], true)
		}
		if data[0] == 0xfe && data[1] == 0xff {
			return decodeUTF16(data[2:], false)
		}
	}
	return validatePrintable(string(data))
}

func decodeUTF16(data []byte, little bool) (string, bool) {
	if len(data)%2 != 0 {
		return "", false
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		if little {
			units[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
		} else {
			units[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
		}
	}
	return validatePrintable(string(utf16.Decode(units)))
}

func validatePrintable(text string) (string, bool) {
	if !utf8Valid(text) {
		return "", false
	}
	for _, r := range text {
		if r < 32 && r != 9 && r != 10 && r != 13 {
			return "", false
		}
	}
	return text, true
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == unicode.ReplacementChar && !strings.ContainsRune(s, 0xfffd) {
			return false
		}
	}
	return true
}
