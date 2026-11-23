//go:build windows

package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func (n *Node) platformSwapAndRestart(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
	_ = os.Remove(backupPath)
	if err := os.Rename(currentExe, backupPath); err != nil {
		return fmt.Errorf("rename running executable to backup: %w", err)
	}

	tmpSameDir := fmt.Sprintf("%s.tmp.%d.exe", strings.TrimSuffix(currentExe, filepath.Ext(currentExe)), os.Getpid())
	if err := copyFile(stagedPath, tmpSameDir, 0o755); err != nil {
		_ = os.Rename(backupPath, currentExe)
		return fmt.Errorf("stage binary in target directory: %w", err)
	}
	if err := os.Rename(tmpSameDir, currentExe); err != nil {
		_ = os.Remove(tmpSameDir)
		_ = os.Rename(backupPath, currentExe)
		return fmt.Errorf("install new executable: %w", err)
	}
	_ = os.Remove(stagedPath)

	if err := n.updater.saveState(*state); err != nil {
		_ = os.Rename(backupPath, currentExe)
		return fmt.Errorf("save update state: %w", err)
	}

	n.invokeRestartHook()
	_ = n.Close()

	cmd := exec.Command(currentExe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func (n *Node) platformRollbackAndRestart(ctx context.Context, currentExe, backupPath string, state *UpdateState) error {
	failedOld := currentExe + ".failed"
	_ = os.Remove(failedOld)
	_ = os.Rename(currentExe, failedOld)
	if err := os.Rename(backupPath, currentExe); err != nil {
		_ = os.Rename(failedOld, currentExe)
		return err
	}
	_ = os.Remove(failedOld)

	n.invokeRestartHook()
	_ = n.Close()

	cmd := exec.Command(currentExe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
