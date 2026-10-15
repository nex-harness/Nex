// syncpath.go: safe relative path handling for the sync archives, porting
// skillSyncPath.ts (and its pluginSyncPath.ts twin, byte-identical rules).
// Every archive entry name flows through NormalizeSyncRelativePath before it
// is joined onto an extraction root — this is the zip-slip guard.
package skills

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var windowsDriveRe = regexp.MustCompile(`^[a-zA-Z]:`)

// NormalizeSyncRelativePath validates and normalizes one relative archive
// path. It rejects absolute paths (both separators), Windows drive paths,
// backslashes, empty/dot/dot-dot segments — everything that could escape the
// extraction root. The label prefixes the error, matching the TS.
func NormalizeSyncRelativePath(path string, unsafePathLabel string) (string, error) {
	if unsafePathLabel == "" {
		unsafePathLabel = "unsafe skill sync path"
	}
	normalized := strings.TrimRight(strings.TrimLeft(
		strings.ReplaceAll(path, "\\", "/"), "/"), "/")
	if normalized == "" ||
		filepath.IsAbs(path) ||
		strings.Contains(path, "\\") ||
		windowsDriveRe.MatchString(path) {
		return "", fmt.Errorf("%s: %s", unsafePathLabel, path)
	}
	// Leading slashes are stripped by the trim above (matching the TS
	// replace(/^\/+/, "")); a POSIX-absolute or Windows-absolute INPUT is
	// still rejected. Interior empty segments (a//b) collapse via the loop.
	for _, part := range strings.Split(normalized, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%s: %s", unsafePathLabel, path)
		}
	}
	return normalized, nil
}

// ResolveSyncPathWithin resolves path under targetRoot, refusing anything
// that is (or escapes to) the root itself or outside it (resolveSkillSyncPathWithin).
func ResolveSyncPathWithin(targetRoot, path, unsafePathLabel string) (string, error) {
	if unsafePathLabel == "" {
		unsafePathLabel = "unsafe skill sync path"
	}
	normalizedRoot, err := filepath.Abs(targetRoot)
	if err != nil {
		return "", err
	}
	normalizedPath, err := NormalizeSyncRelativePath(path, unsafePathLabel)
	if err != nil {
		return "", err
	}
	targetPath := filepath.Join(normalizedRoot, filepath.FromSlash(normalizedPath))
	relativePath, err := filepath.Rel(normalizedRoot, targetPath)
	if err != nil {
		return "", err
	}
	if relativePath == "" ||
		relativePath == ".." ||
		strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("%s: %s", unsafePathLabel, path)
	}
	return targetPath, nil
}
