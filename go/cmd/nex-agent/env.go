package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// nodePlatform maps Go's GOOS to the Node process.platform value.
func nodePlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	default:
		return runtime.GOOS
	}
}

// nodeArch maps Go's GOARCH to the Node process.arch value.
func nodeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	default:
		return runtime.GOARCH
	}
}

// osRelease is os.release(): the kernel release. Linux exposes it in
// /proc/sys/kernel/osrelease; other platforms fall back to `uname -r`.
func osRelease() string {
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	if out, err := exec.Command("uname", "-r").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// shellName mirrors the effective bash shell selection: it prefers the candidate
// order the adapter uses (SHELL=...bash -> bash first, else zsh then bash) and
// returns the first shell that exists.
func shellName() string {
	candidates := []string{"zsh", "bash"}
	if b := filepath.Base(os.Getenv("SHELL")); b != "" {
		if strings.Contains(b, "bash") {
			candidates = []string{"bash", "zsh"}
		} else if kind := posixShellKind(b); kind != "" {
			candidates = []string{kind, "zsh", "bash"}
		}
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	if b := filepath.Base(os.Getenv("SHELL")); b != "" {
		return b
	}
	return "bash"
}

func posixShellKind(name string) string {
	if strings.Contains(name, "bash") {
		return "bash"
	}
	if strings.Contains(name, "zsh") {
		return "zsh"
	}
	return ""
}
