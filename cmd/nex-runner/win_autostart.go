//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const autostartTaskName = "NexRunner"

func autostartStatus() (installed bool, details string, err error) {
	out, qErr := exec.Command("schtasks.exe", "/Query", "/TN", autostartTaskName).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if qErr != nil {
		return false, text, nil
	}
	return true, text, nil
}

func installAutostart(rawArgs []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	cmdLine := buildAutostartCommandLine(exe, rawArgs)
	out, err := exec.Command(
		"schtasks.exe",
		"/Create",
		"/TN", autostartTaskName,
		"/TR", cmdLine,
		"/SC", "ONLOGON",
		"/RL", "HIGHEST",
		"/F",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /Create failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallAutostart() error {
	out, err := exec.Command(
		"schtasks.exe",
		"/Delete",
		"/TN", autostartTaskName,
		"/F",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /Delete failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func handleAutostartSubcommand(action string, rawArgs []string) error {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "install":
		if err := installAutostart(rawArgs); err != nil {
			return err
		}
		fmt.Printf("Windows elevated autostart task %q installed\n", autostartTaskName)
		return nil
	case "uninstall":
		if err := uninstallAutostart(); err != nil {
			return err
		}
		fmt.Printf("Windows elevated autostart task %q removed\n", autostartTaskName)
		return nil
	case "status":
		installed, details, err := autostartStatus()
		if err != nil {
			return err
		}
		if installed {
			fmt.Printf("Autostart %q: installed\n%s\n", autostartTaskName, details)
		} else {
			fmt.Printf("Autostart %q: not installed\n", autostartTaskName)
		}
		return nil
	default:
		return fmt.Errorf("unknown autostart action %q (expected install|uninstall|status)", action)
	}
}
