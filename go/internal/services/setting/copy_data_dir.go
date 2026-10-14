// copy_data_dir.go ports copyDataDirectory (packages/services/src/paths.ts):
// copy {old}/.nex/v2 → {new}/.nex/v2, force:false (existing files abort with
// an EEXIST-family error), skipping setting.json* (bootstrap intermediates)
// and symlinks (Windows non-elevated cp fails on them with EPERM).

package setting

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// CopyDataDirectory ports copyDataDirectory.
func CopyDataDirectory(ctx context.Context, oldBaseDir, newBaseDir string) error {
	oldDir := filepath.Join(oldBaseDir, ".nex", "v2")
	newDir := filepath.Join(newBaseDir, ".nex", "v2")
	return copyTree(ctx, oldDir, newDir)
}

// CopyExistsError is the errorOnExist: force:false failure the TS surfaces as
// ERR_FS_CP_EEXIST.
type CopyExistsError struct{ Path string }

func (e *CopyExistsError) Error() string { return "Target already exists: " + e.Path }

// copyTree mirrors fs.cp recursive + force:false + the filter.
func copyTree(ctx context.Context, src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	return copyEntry(ctx, src, dst, info)
}

func copyEntry(ctx context.Context, src, dst string, info os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// filter: setting.json and its bootstrap intermediates never migrate.
	base := filepath.Base(src)
	if base == "setting.json" || (len(base) > len("setting.json.") && base[:len("setting.json.")] == "setting.json.") {
		return nil
	}
	// filter: symlinks are skipped (Windows EPERM workaround).
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.IsDir() {
		return copyDir(ctx, src, dst)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	return copyFileEntry(ctx, src, dst, info)
}

func copyDir(ctx context.Context, src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		entryPath := filepath.Join(src, entry.Name())
		info, err := os.Lstat(entryPath)
		if err != nil {
			return err
		}
		if err := copyEntry(ctx, entryPath, filepath.Join(dst, entry.Name()), info); err != nil {
			return err
		}
	}
	return nil
}

func copyFileEntry(ctx context.Context, src, dst string, info os.FileInfo) error {
	// force:false → existing destination is an error.
	if _, err := os.Lstat(dst); err == nil {
		return &CopyExistsError{Path: dst}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := info.Mode().Perm()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// CrossDeviceRenameError marks EXDEV from a rename-based move: the caller
// falls back to copy+delete. The settings copy path never renames across
// devices (it always copies), but the fault suite exercises the seam.
type CrossDeviceRenameError struct{ Err error }

func (e *CrossDeviceRenameError) Error() string { return e.Err.Error() }
func (e *CrossDeviceRenameError) Unwrap() error { return e.Err }

// IsCrossDeviceRename reports whether err is EXDEV.
func IsCrossDeviceRename(err error) bool {
	return err != nil && isErrNoCrossDev(err)
}

func isErrNoCrossDev(err error) bool {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return errors.Is(linkErr.Err, syscall.EXDEV)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return errors.Is(pathErr.Err, syscall.EXDEV)
	}
	return errors.Is(err, syscall.EXDEV)
}

// osName returns the Node process.platform spelling.
func osName() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}
