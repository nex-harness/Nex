//go:build !windows

package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func (n *Node) platformSwapAndRestart(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
	if err := copyFile(currentExe, backupPath, 0o755); err != nil {
		// Fall back to DataDir backup if binary directory rejects secondary writes.
		fallbackBackup := filepath.Join(n.cfg.DataDir, filepath.Base(currentExe)+".bak")
		if fErr := copyFile(currentExe, fallbackBackup, 0o755); fErr != nil {
			return fmt.Errorf("backup current binary: %w", err)
		}
		backupPath = fallbackBackup
		state.BackupPath = fallbackBackup
	}

	tmpSameDir := fmt.Sprintf("%s.tmp.%d", currentExe, os.Getpid())
	if err := copyFile(stagedPath, tmpSameDir, 0o755); err != nil {
		return fmt.Errorf("stage binary in target directory: %w", err)
	}
	if err := os.Rename(tmpSameDir, currentExe); err != nil {
		_ = os.Remove(tmpSameDir)
		return fmt.Errorf("atomic rename into %s: %w", currentExe, err)
	}
	_ = os.Remove(stagedPath)

	if err := n.updater.saveState(*state); err != nil {
		_ = copyFile(backupPath, currentExe, 0o755)
		return fmt.Errorf("save update state: %w", err)
	}

	n.invokeRestartHook()
	_ = n.Close()
	return syscall.Exec(currentExe, os.Args, os.Environ())
}

func (n *Node) platformRollbackAndRestart(ctx context.Context, currentExe, backupPath string, state *UpdateState) error {
	tmpSameDir := fmt.Sprintf("%s.rollback.%d", currentExe, os.Getpid())
	if err := copyFile(backupPath, tmpSameDir, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpSameDir, currentExe); err != nil {
		_ = os.Remove(tmpSameDir)
		return err
	}
	_ = os.Remove(backupPath)

	n.invokeRestartHook()
	_ = n.Close()
	return syscall.Exec(currentExe, os.Args, os.Environ())
}
