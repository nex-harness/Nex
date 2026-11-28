//go:build darwin

package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// detectFullDiskAccess checks whether the process holds macOS Full Disk Access without triggering any prompt.
func detectFullDiskAccess() *bool {
	home, _ := os.UserHomeDir()
	var probes []string
	if strings.TrimSpace(home) != "" {
		probes = append(probes,
			filepath.Join(home, "Library", "Application Support", "com.apple.TCC", "TCC.db"),
			filepath.Join(home, "Library", "Safari", "Bookmarks.plist"),
		)
	}
	probes = append(probes, "/Library/Application Support/com.apple.TCC/TCC.db")

	sawDenied := false
	for _, p := range probes {
		f, err := os.Open(p)
		if err == nil {
			_ = f.Close()
			v := true
			return &v
		}
		if os.IsPermission(err) {
			sawDenied = true
		}
	}
	if sawDenied {
		v := false
		return &v
	}
	v := false
	return &v
}

func resolveRunnerAppPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if realPath, rErr := filepath.EvalSymlinks(exe); rErr == nil && realPath != "" {
		exe = realPath
	}
	return resolveAppBundleFromExecutable(exe)
}

func handleRunnerPermissionAction(action string) (*bool, string, error) {
	appPath := resolveRunnerAppPath()
	act := strings.TrimSpace(action)
	if act == "" {
		act = "check"
	}

	switch act {
	case "check":
		return detectFullDiskAccess(), appPath, nil
	case "open_settings":
		if out, err := exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles").CombinedOutput(); err != nil {
			return detectFullDiskAccess(), appPath, fmt.Errorf("open System Settings failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return detectFullDiskAccess(), appPath, nil
	case "reveal_app":
		if appPath == "" {
			return detectFullDiskAccess(), "", fmt.Errorf("unable to resolve runner executable path")
		}
		if out, err := exec.Command("open", "-R", appPath).CombinedOutput(); err != nil {
			return detectFullDiskAccess(), appPath, fmt.Errorf("reveal in Finder failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return detectFullDiskAccess(), appPath, nil
	case "open_guide":
		var errs []string
		if out, err := exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles").CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("open System Settings failed: %v (%s)", err, strings.TrimSpace(string(out))))
		}
		if appPath != "" {
			if out, err := exec.Command("open", "-R", appPath).CombinedOutput(); err != nil {
				errs = append(errs, fmt.Sprintf("reveal in Finder failed: %v (%s)", err, strings.TrimSpace(string(out))))
			}
		}
		if len(errs) > 0 {
			return detectFullDiskAccess(), appPath, fmt.Errorf("%s", strings.Join(errs, "; "))
		}
		return detectFullDiskAccess(), appPath, nil
	default:
		return detectFullDiskAccess(), appPath, fmt.Errorf("unsupported permission action %q", act)
	}
}
