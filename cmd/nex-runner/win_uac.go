//go:build windows

package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func isProcessElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func maybeElevateWindows(rawArgs []string, noUAC, uacAttempted, interactive bool) (bool, error) {
	if isProcessElevated() || !interactive || noUAC || uacAttempted {
		return false, nil
	}
	if err := relaunchAsAdministrator(rawArgs); err != nil {
		return false, err
	}
	return true, nil
}

func relaunchAsAdministrator(rawArgs []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	cwd, _ := os.Getwd()
	params := buildWindowsCommandLine(buildElevatedArgs(rawArgs))

	verbPtr, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	var paramsPtr *uint16
	if params != "" {
		paramsPtr, err = windows.UTF16PtrFromString(params)
		if err != nil {
			return err
		}
	}
	var dirPtr *uint16
	if cwd != "" {
		dirPtr, _ = windows.UTF16PtrFromString(cwd)
	}

	return windows.ShellExecute(0, verbPtr, exePtr, paramsPtr, dirPtr, windows.SW_SHOWNORMAL)
}
