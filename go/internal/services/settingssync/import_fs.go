// import_fs.go: the copy/symlink import primitives (fs.cp errorOnExist
// semantics, Windows hard-link fallback for command files).
package settingssync

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// ---------------------------------------------------------------------------
// import primitives (copy/symlink)
// ---------------------------------------------------------------------------

// importSkillDirectory ports importSkillDirectory: symlink (dir; junction on
// Windows) or recursive copy with EEXIST-on-existing.
func importSkillDirectory(sourcePath, targetPath, importMode string) error {
	if importMode == "symlink" {
		return createDirectorySymlink(sourcePath, targetPath)
	}
	return copyTreeNonDestructive(sourcePath, targetPath)
}

// importPluginDirectory ports importPluginDirectory (same shapes).
func importPluginDirectory(sourcePath, targetPath, importMode string) error {
	if importMode == "symlink" {
		return createDirectorySymlink(sourcePath, targetPath)
	}
	return copyTreeNonDestructive(sourcePath, targetPath)
}

// importCommandFile ports importCommandFile: symlink on Unix, HARD link on
// Windows (EXDEV → copy fallback), or copy.
func importCommandFile(sourcePath, targetPath, importMode string) error {
	if importMode == "symlink" {
		if runtime.GOOS == "windows" {
			// Windows 创建文件级 symlink 需要管理员权限，改用硬链接；跨分区
			// (EXDEV) 等硬链接失败时回退到复制。
			if err := os.Link(absPath(sourcePath), targetPath); err == nil {
				return nil
			}
			// fall through to copy
		} else {
			return os.Symlink(absPath(sourcePath), targetPath)
		}
	}
	return copyFileNonDestructive(sourcePath, targetPath)
}

// createDirectorySymlink ports createDirectorySymlink (absolute source).
func createDirectorySymlink(sourcePath, targetPath string) error {
	return os.Symlink(absPath(sourcePath), targetPath)
}

// copyTreeNonDestructive ports fs.cp recursive + errorOnExist + force:false.
func copyTreeNonDestructive(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	return copyTreeEntry(source, target, info)
}

func copyTreeEntry(source, target string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return &existsError{target}
	}
	if info.IsDir() {
		return copyTreeDir(source, target)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	return copyFileNonDestructive(source, target)
}

func copyTreeDir(source, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return &existsError{target}
	}
	if err := os.MkdirAll(target, infoModeOr(infoOf(source), 0o755)); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath := filepath.Join(source, entry.Name())
		info, err := os.Lstat(entryPath)
		if err != nil {
			return err
		}
		if err := copyTreeEntry(entryPath, filepath.Join(target, entry.Name()), info); err != nil {
			return err
		}
	}
	return nil
}

func copyFileNonDestructive(source, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return &existsError{target}
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := infoModeOr(infoOf(source), 0o644)
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// existsError models the ERR_FS_CP_EEXIST family.
type existsError struct{ path string }

func (e *existsError) Error() string { return "Target already exists: " + e.path }

func infoOf(path string) os.FileInfo {
	info, err := os.Stat(path)
	return infoOrNil(err, info)
}

func infoOrNil(err error, info os.FileInfo) os.FileInfo {
	if err != nil {
		return nil
	}
	return info
}

func infoModeOr(info os.FileInfo, fallback os.FileMode) os.FileMode {
	if info == nil {
		return fallback
	}
	return info.Mode().Perm()
}

func toSet(items []string) map[string]bool {
	if items == nil {
		return nil
	}
	out := map[string]bool{}
	for _, item := range items {
		out[item] = true
	}
	return out
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// targetScopeOf maps a skill candidate's internal scope to a source scope.
func targetScopeOf(c skillCandidate) string { return c.sourceRootScope }

func targetScopeOfCommand(c commandCandidate) string { return c.sourceRootScope }
