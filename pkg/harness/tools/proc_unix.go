//go:build !windows

package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// DefaultShellName returns the detected POSIX shell on Unix platforms.
func DefaultShellName() string {
	if p, err := exec.LookPath("bash"); err == nil && p != "" {
		return p
	}
	return "/bin/sh"
}

func runPlatformShellCommand(ctx context.Context, dir string, command string, out io.Writer) (int, error) {
	shell := DefaultShellName()
	cmd := exec.Command(shell, "-c", command)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()
	// Put child in its own process group so we can kill all spawned grandchildren cleanly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	pr, pw, err := os.Pipe()
	if err != nil {
		return -1, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return -1, err
	}
	// Parent closes write end immediately so EOF is delivered once child group exits or is killed.
	_ = pw.Close()

	var copyWG sync.WaitGroup
	copyWG.Add(1)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(out, pr)
	}()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-waitDone:
		// Also clean up any stray background grandchildren still in the process group
		// so every write end of the pipe closes and io.Copy receives EOF after draining all bytes.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		copyWG.Wait()
		_ = pr.Close()
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		// Close pipe read end to unblock io.Copy immediately even if a grandchild somehow lingered briefly
		_ = pr.Close()
		waitErr = <-waitDone
		copyWG.Wait()
	}

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, waitErr
	}
	return 0, nil
}
