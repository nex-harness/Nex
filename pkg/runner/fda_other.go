//go:build !darwin

package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func detectFullDiskAccess() *bool {
	return nil
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
	if act == "" || act == "check" {
		return nil, appPath, nil
	}
	return nil, appPath, fmt.Errorf("macOS Full Disk Access guide is only supported on darwin runners")
}
