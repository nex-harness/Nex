// atomicdir.go: the atomic directory activation used by plugin install /
// uninstall — a Go port of the *sequence* semantics of
// adapters/src/plugins/atomic-directory.ts (stage → flip → finalize /
// rollback with a `.transaction.json` marker and a `.<name>.backup`), kept
// to the standalone mode: the coordinated mode's authority file is the CLI
// process's installed_plugins.json, which Phase 4 owns. The on-disk marker
// names and record shape stay byte-compatible with the TS so a crash between
// the two engines recovers identically.
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AtomicTransactionRecordV2 is the `.transaction.json` payload
// (AtomicTransactionRecordV2; mode is always "standalone" here).
type AtomicTransactionRecordV2 struct {
	AuthorityPath string `json:"authorityPath,omitempty"`
	HadTarget     bool   `json:"hadTarget"`
	Mode          string `json:"mode"` // "coordinated" | "standalone"
	OwnerID       string `json:"ownerId"`
	OwnerPID      int    `json:"ownerPid"`
	StageName     string `json:"stageName"`
	TransactionID string `json:"transactionId"`
	Version       int    `json:"version"`
}

// AtomicActivation is the staged directory handle (AtomicDirectoryActivation).
type AtomicActivation struct {
	TargetPath    string
	TransactionID string
	StagePath     string

	backupPath      string
	transactionPath string
	hadTarget       bool
	finalized       bool
	rolledBack      bool
}

// StageDirectoryAtomic prepares a staging directory next to targetPath and
// records the transaction marker. The caller copies plugin content into
// StagePath, then Finalize flips it into place atomically (rename), or
// Rollback restores the previous state.
func StageDirectoryAtomic(targetPath string) (*AtomicActivation, error) {
	parent := filepath.Dir(targetPath)
	name := filepath.Base(targetPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	transactionID := randomHexID()
	stagePath := filepath.Join(parent, fmt.Sprintf(".%s.stage-%s", name, transactionID))
	if err := os.MkdirAll(stagePath, 0o755); err != nil {
		return nil, err
	}
	hadTarget := pathExists(targetPath)
	activation := &AtomicActivation{
		TargetPath:      targetPath,
		TransactionID:   transactionID,
		StagePath:       stagePath,
		backupPath:      filepath.Join(parent, "."+name+".backup"),
		transactionPath: filepath.Join(parent, "."+name+".transaction.json"),
		hadTarget:       hadTarget,
	}
	if err := activation.writeTransactionMarker(); err != nil {
		os.RemoveAll(stagePath)
		return nil, err
	}
	return activation, nil
}

func (a *AtomicActivation) writeTransactionMarker() error {
	record := AtomicTransactionRecordV2{
		HadTarget:     a.hadTarget,
		Mode:          "standalone",
		OwnerID:       processOwnerID,
		OwnerPID:      os.Getpid(),
		StageName:     filepath.Base(a.StagePath),
		TransactionID: a.TransactionID,
		Version:       2,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(a.transactionPath, append(data, '\n'), 0o644)
}

// Finalize flips the staged directory into place: the old target (if any)
// becomes the backup, the stage renames onto the target, the marker is
// removed, and the backup is dropped (the commit point).
func (a *AtomicActivation) Finalize() error {
	if a.rolledBack {
		return fmt.Errorf("atomic directory activation already rolled back: %s", a.TargetPath)
	}
	if a.finalized {
		return nil
	}
	if a.hadTarget {
		if err := os.Rename(a.TargetPath, a.backupPath); err != nil {
			return err
		}
	}
	if err := os.Rename(a.StagePath, a.TargetPath); err != nil {
		// Restore the backup so the directory is never left empty.
		if a.hadTarget {
			os.Rename(a.backupPath, a.TargetPath)
		}
		return err
	}
	os.RemoveAll(a.backupPath)
	os.Remove(a.transactionPath)
	a.finalized = true
	return nil
}

// Rollback removes the staged directory and restores the backup.
func (a *AtomicActivation) Rollback() error {
	if a.finalized {
		return fmt.Errorf("atomic directory activation already finalized: %s", a.TargetPath)
	}
	if a.rolledBack {
		return nil
	}
	os.RemoveAll(a.StagePath)
	if pathExists(a.backupPath) {
		os.RemoveAll(a.TargetPath)
		if err := os.Rename(a.backupPath, a.TargetPath); err != nil {
			return err
		}
	}
	os.Remove(a.transactionPath)
	a.rolledBack = true
	return nil
}

// RecoverAtomicTarget ports recoverAtomicTargetSync for standalone
// transactions: it resolves a `.transaction.json` left by a crashed writer
// and returns the path readers should use. Since a Go process cannot probe
// the TS's owner-pid start time cross-platform without shelling out, a stale
// marker (owner process dead) is recovered immediately; a live owner is
// detected only when it is this process.
func RecoverAtomicTarget(targetPath string) string {
	parent := filepath.Dir(targetPath)
	name := filepath.Base(targetPath)
	backupPath := filepath.Join(parent, "."+name+".backup")
	transactionPath := filepath.Join(parent, "."+name+".transaction.json")

	raw, err := os.ReadFile(transactionPath)
	if err != nil {
		return resolveCrashedAtomicRead(targetPath, backupPath)
	}
	var record AtomicTransactionRecordV2
	if err := json.Unmarshal(raw, &record); err != nil || record.Version != 2 {
		return resolveCrashedAtomicRead(targetPath, backupPath)
	}
	if record.OwnerPID == os.Getpid() && record.OwnerID == processOwnerID {
		// Our own in-flight transaction: read the committed generation.
		if record.HadTarget && pathExists(backupPath) {
			return backupPath
		}
		return targetPath
	}
	// Standalone transaction from a dead (or unknown) writer: recover.
	return resolveCrashedAtomicRead(targetPath, backupPath)
}

func resolveCrashedAtomicRead(targetPath, backupPath string) string {
	haveTarget := pathExists(targetPath)
	haveBackup := pathExists(backupPath)
	switch {
	case haveTarget && haveBackup:
		os.RemoveAll(backupPath)
	case !haveTarget && haveBackup:
		os.Rename(backupPath, targetPath)
	case haveTarget && !haveBackup:
		// committed or untouched
	}
	return targetPath
}

// InstallDirectoryAtomic copies sourceDir into targetPath atomically: stage,
// copy, finalize. The stage lives in the target's parent so the final rename
// stays on one filesystem.
func InstallDirectoryAtomic(sourceDir, targetPath string) error {
	activation, err := StageDirectoryAtomic(targetPath)
	if err != nil {
		return err
	}
	if err := copyTree(sourceDir, activation.StagePath); err != nil {
		activation.Rollback()
		return err
	}
	if err := activation.Finalize(); err != nil {
		activation.Rollback()
		return err
	}
	return nil
}

func copyTree(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		// Symlinks and specials are skipped: plugin installs only carry
		// regular files (the archive extractor refuses link entries).
		return nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, info.Mode().Perm())
}
