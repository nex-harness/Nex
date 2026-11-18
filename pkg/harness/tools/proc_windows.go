//go:build windows

package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DefaultShellName probes Git Bash, PowerShell 7 (pwsh), or Windows PowerShell.
func DefaultShellName() string {
	if p, err := exec.LookPath("bash.exe"); err == nil && p != "" {
		return p
	}
	gitBash := filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
	if _, err := os.Stat(gitBash); err == nil {
		return gitBash
	}
	if p, err := exec.LookPath("pwsh.exe"); err == nil && p != "" {
		return p
	}
	if p, err := exec.LookPath("powershell.exe"); err == nil && p != "" {
		return p
	}
	return "cmd.exe"
}

func buildWindowsCommand(command string) *exec.Cmd {
	shell := DefaultShellName()
	base := filepath.Base(shell)
	switch base {
	case "bash.exe", "bash":
		return exec.Command(shell, "-c", command)
	case "pwsh.exe", "powershell.exe":
		return exec.Command(shell, "-NoProfile", "-NonInteractive", "-Command", command)
	default:
		return exec.Command(shell, "/C", command)
	}
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func assignPIDToJob(job windows.Handle, pid int) {
	if job == 0 || pid <= 0 {
		return
	}
	procHandle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(procHandle)
	_ = windows.AssignProcessToJobObject(job, procHandle)
}

func runPlatformShellCommand(ctx context.Context, dir string, command string, out io.Writer) (int, error) {
	cmd := buildWindowsCommand(command)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()

	job, _ := createKillOnCloseJob()
	defer func() {
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
	}()

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
	_ = pw.Close()

	if cmd.Process != nil && job != 0 {
		assignPIDToJob(job, cmd.Process.Pid)
	}

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
	case <-ctx.Done():
		// Closing the Job Object handle kills every process in the job instantaneously.
		if job != 0 {
			_ = windows.CloseHandle(job)
			job = 0
		} else if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = pr.Close()
		waitErr = <-waitDone
	}

	_ = pr.Close()
	copyWG.Wait()

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, waitErr
	}
	return 0, nil
}
