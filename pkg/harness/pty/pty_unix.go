//go:build !windows

package pty

import (
	"os"
	"os/exec"
	"syscall"

	creackpty "github.com/creack/pty"
)

type unixPTY struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func startPlatformPTY(cwd, shell string, cols, rows uint16) (platformPTY, string, error) {
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		if p, err := exec.LookPath("zsh"); err == nil && p != "" {
			shell = p
		} else if p, err := exec.LookPath("bash"); err == nil && p != "" {
			shell = p
		} else {
			shell = "/bin/sh"
		}
	}

	cmd := exec.Command(shell)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")

	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		return nil, "", err
	}
	return &unixPTY{ptmx: ptmx, cmd: cmd}, shell, nil
}

func (u *unixPTY) Read(p []byte) (int, error)  { return u.ptmx.Read(p) }
func (u *unixPTY) Write(p []byte) (int, error) { return u.ptmx.Write(p) }

func (u *unixPTY) Resize(cols, rows uint16) error {
	return creackpty.Setsize(u.ptmx, &creackpty.Winsize{Cols: cols, Rows: rows})
}

func (u *unixPTY) Close() error {
	if u.cmd.Process != nil {
		_ = syscall.Kill(-u.cmd.Process.Pid, syscall.SIGHUP)
		_ = u.cmd.Process.Kill()
	}
	return u.ptmx.Close()
}

func (u *unixPTY) Wait() error {
	return u.cmd.Wait()
}
