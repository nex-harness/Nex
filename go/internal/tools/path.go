package tools

import (
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// normalizeForComparison mirrors normalizeToolPathForComparison(filePath, platform)
// for the local platform: posix clean + NFC on Unix, Windows drive/UNC handling on
// Windows. Tools only compare paths on the platform they run on, so this matches
// Node's default currentPlatform.
func normalizeForComparison(filePath string) string {
	var normalized string
	if runtime.GOOS == "windows" {
		normalized = normalizeWindowsToolPath(filePath)
	} else {
		normalized = path.Clean(filePath)
	}
	return norm.NFC.String(normalized)
}

func normalizeWindowsToolPath(filePath string) string {
	driveAlias := normalizeWindowsDriveAlias(filePath)
	cleaned := filepath.Clean(driveAlias)
	cleaned = filepath.ToSlash(cleaned)
	stripped := stripWindowsExtendedPrefix(cleaned)
	return canonicalizeWindowsDriveLetter(stripped)
}

func normalizeWindowsDriveAlias(filePath string) string {
	if len(filePath) >= 3 && filePath[0] == '/' && isASCIILetter(filePath[1]) && filePath[2] == '/' {
		drive := strings.ToUpper(string(filePath[1]))
		rest := filePath[2:]
		return drive + ":" + strings.ReplaceAll(rest, "/", "\\")
	}
	return filePath
}

func canonicalizeWindowsDriveLetter(filePath string) string {
	if len(filePath) >= 2 && isASCIILetter(filePath[0]) && filePath[1] == ':' {
		return strings.ToUpper(string(filePath[0])) + filePath[1:]
	}
	return filePath
}

func stripWindowsExtendedPrefix(filePath string) string {
	const unc = "//?/UNC/"
	if strings.HasPrefix(filePath, unc) {
		return "//" + filePath[len(unc):]
	}
	const prefix = "//?/"
	if strings.HasPrefix(filePath, prefix) && len(filePath) >= 7 && filePath[5] == ':' {
		return filePath[len(prefix):]
	}
	return filePath
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
