//go:build !windows

package main

import (
	"context"
	"fmt"

	"github.com/nex-harness/nex/pkg/runner"
)

func isProcessElevated() bool {
	return false
}

func maybeElevateWindows(rawArgs []string, noUAC, uacAttempted, interactive bool) (bool, error) {
	return false, nil
}

func relaunchAsAdministrator(rawArgs []string) error {
	return nil
}

func handleAutostartSubcommand(action string, rawArgs []string) error {
	return fmt.Errorf("autostart subcommand (%s) is only supported on Windows (use 'service' on Linux/macOS)", action)
}

func startWindowsTray(ctx context.Context, cancel context.CancelFunc, node *runner.Node, rawArgs []string, trayEnabled bool) func() {
	return func() {}
}
