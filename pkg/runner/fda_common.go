package runner

import (
	"path/filepath"
	"strings"
)

// resolveAppBundleFromExecutable extracts `<X>.app` when exe lives inside `<X>.app/Contents/MacOS/...`.
func resolveAppBundleFromExecutable(exePath string) string {
	trimmed := strings.TrimSpace(exePath)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	macOSDir := filepath.Dir(cleaned)
	contentsDir := filepath.Dir(macOSDir)
	appDir := filepath.Dir(contentsDir)
	if filepath.Base(macOSDir) == "MacOS" &&
		filepath.Base(contentsDir) == "Contents" &&
		strings.HasSuffix(strings.ToLower(appDir), ".app") {
		return appDir
	}
	return cleaned
}
