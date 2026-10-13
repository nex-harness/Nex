// windows.go ports the Windows-only half of packages/services/src/paths.ts:
// readEnvValue, normalizeWindowsComparablePath, isWindowsPathEqualOrInside,
// collectWindowsForbiddenAppInstallDirs and validateDataBaseDirTarget.
//
// It must run on every OS (the data-dir validation decision is made by the
// *server's* platform, not the test runner's), so the Node win32 path functions
// it leans on — win32.normalize and win32.resolve — are ported as pure string
// functions in winpath.go rather than delegated to path/filepath, whose Windows
// build differs from Node's (notably drive-relative "C:" and device roots).
package paths

import (
	"sort"
	"strings"
)

// NEX_WINDOWS_APP_INSTALL_DIR_ENV is NEX_WINDOWS_APP_INSTALL_DIR_ENV from paths.ts.
// It already exists in the TypeScript desktop app.
const NEX_WINDOWS_APP_INSTALL_DIR_ENV = "NEX_WINDOWS_APP_INSTALL_DIR"

// Platform identifiers, matching the NodeJS.Platform strings the TS compares against.
const (
	PlatformWin32 = "win32"
)

// readEnvValue ports the case-insensitive env lookup: exact key first (trimmed,
// non-empty), then any case variant of the key. Go's os.Environ is not folded,
// so callers that need case-insensitivity pass the pairs they captured; the
// pure function keeps the TS order semantics (first non-empty case variant wins
// — in practice exactly one variant exists).
func readEnvValue(env map[string]string, key string) (string, bool) {
	if direct, ok := env[key]; ok {
		trimmed := strings.TrimSpace(direct)
		if trimmed != "" {
			return trimmed, true
		}
	}
	lowerKey := strings.ToLower(key)
	for candidateKey, value := range env {
		if strings.ToLower(candidateKey) != lowerKey {
			continue
		}
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed, true
		}
	}
	return "", false
}

// NormalizeWindowsComparablePath ports normalizeWindowsComparablePath: trim →
// win32.normalize → strip trailing separators → win32.resolve → strip trailing
// separators → lowercase. Empty in, empty out ("" means "not comparable").
func NormalizeWindowsComparablePath(pathValue string) string {
	trimmed := strings.TrimSpace(pathValue)
	if trimmed == "" {
		return ""
	}
	normalized := stripTrailingSeparators(Win32Normalize(trimmed))
	if normalized == "" {
		return ""
	}
	// The data-dir inputs the TS validates are drive roots ("C:\Users\…") and
	// UNC paths, both absolute, so the resolve cwd fallback never fires; pass an
	// empty cwd so a relative input degrades visibly instead of silently
	// borrowing this process's (POSIX) cwd.
	resolved := stripTrailingSeparators(Win32Resolve(normalized, ""))
	if resolved == "" {
		return ""
	}
	return strings.ToLower(resolved)
}

// stripTrailingSeparators is the `.replace(/[\\/]+$/, "")` step: any run of
// back- or forward slashes at the end is removed.
func stripTrailingSeparators(pathValue string) string {
	return strings.TrimRight(pathValue, `\/`)
}

// isWindowsPathEqualOrInside ports the same-named TS function: equal after
// normalization, or the path is strictly under the root (`root + "\"` prefix).
// Either side failing to normalize means "not comparable", i.e. false.
func isWindowsPathEqualOrInside(pathValue, rootValue string) bool {
	normalizedPath := NormalizeWindowsComparablePath(pathValue)
	normalizedRoot := NormalizeWindowsComparablePath(rootValue)
	if normalizedPath == "" || normalizedRoot == "" {
		return false
	}
	return normalizedPath == normalizedRoot || strings.HasPrefix(normalizedPath, normalizedRoot+`\`)
}

// collectWindowsForbiddenAppInstallDirs ports the same-named TS function. It
// returns the ORIGINAL (non-normalized) candidate strings, deduplicated by their
// normalized form, in the TS candidate order: appInstallDir, the env override,
// ProgramFiles/Nex, ProgramFiles(x86)/Nex, ProgramW6432/Nex, LOCALAPPDATA/Programs/Nex.
func collectWindowsForbiddenAppInstallDirs(env map[string]string, appInstallDir string) []string {
	type candidate struct{ raw, normalized string }
	var candidates []candidate
	add := func(raw string, ok bool) {
		if !ok || raw == "" {
			return
		}
		normalized := NormalizeWindowsComparablePath(raw)
		if normalized == "" {
			return
		}
		candidates = append(candidates, candidate{raw: raw, normalized: normalized})
	}
	programFiles, programFilesOK := readEnvValue(env, "ProgramFiles")
	programFilesX86, programFilesX86OK := readEnvValue(env, "ProgramFiles(x86)")
	programW6432, programW6432OK := readEnvValue(env, "ProgramW6432")
	localAppData, localAppDataOK := readEnvValue(env, "LOCALAPPDATA")
	envInstallDir, envInstallDirOK := readEnvValue(env, NEX_WINDOWS_APP_INSTALL_DIR_ENV)

	add(appInstallDir, appInstallDir != "")
	add(envInstallDir, envInstallDirOK)
	if programFilesOK {
		add(Win32Join(programFiles, "Nex"), true)
	}
	if programFilesX86OK {
		add(Win32Join(programFilesX86, "Nex"), true)
	}
	if programW6432OK {
		add(Win32Join(programW6432, "Nex"), true)
	}
	if localAppDataOK {
		add(Win32Join(Win32Join(localAppData, "Programs"), "Nex"), true)
	}

	seen := make(map[string]bool, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if seen[c.normalized] {
			continue
		}
		seen[c.normalized] = true
		result = append(result, c.raw)
	}
	return result
}

// ValidateDataBaseDirTargetResult ports DataBaseDirTargetValidationResult:
// either OK, or the forbidden code plus the forbidden dir that matched.
type ValidateDataBaseDirTargetResult struct {
	OK           bool
	Code         string
	ForbiddenDir string
}

// ValidateDataBaseDirTarget ports validateDataBaseDirTarget. On any platform
// other than win32 the answer is unconditionally OK. env defaults to an empty
// map (the caller passes what it captured of process.env); appInstallDir is the
// optional explicit install dir.
func ValidateDataBaseDirTarget(platform string, targetBaseDir string, env map[string]string, appInstallDir string) ValidateDataBaseDirTargetResult {
	if platform != PlatformWin32 {
		return ValidateDataBaseDirTargetResult{OK: true}
	}
	if env == nil {
		env = map[string]string{}
	}
	for _, forbiddenDir := range collectWindowsForbiddenAppInstallDirs(env, appInstallDir) {
		if isWindowsPathEqualOrInside(targetBaseDir, forbiddenDir) {
			return ValidateDataBaseDirTargetResult{
				OK:           false,
				Code:         DataBaseDirForbiddenWindowsInstallDirErrorCode,
				ForbiddenDir: forbiddenDir,
			}
		}
	}
	return ValidateDataBaseDirTargetResult{OK: true}
}

// ValidateCurrentProcessDataBaseDirTarget applies ValidateDataBaseDirTarget for
// the running process (runtime.GOOS → Node platform spelling) and the process
// environment, which is what settingService.updateDataBaseDir calls before a move.
// Kept here (not in the settings service) because the forbidden-dir collection is
// paths-owned policy. The env snapshot is taken by the caller so tests stay pure.
func ValidateCurrentProcessDataBaseDirTarget(goos, targetBaseDir string, env map[string]string, appInstallDir string) ValidateDataBaseDirTargetResult {
	platform := ""
	if goos == "windows" {
		platform = PlatformWin32
	}
	return ValidateDataBaseDirTarget(platform, targetBaseDir, env, appInstallDir)
}

// sortedEnvKeys is a test/determinism helper: iteration order of a map is not
// stable, and readEnvValue's case-variant scan must not depend on it.
func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
