package fsx

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
)

// atomicWrite mirrors atomicWrite() in adapters/src/fs/index.ts: write to a temp
// file in the same directory, copy the existing mode, fsync, then rename over the
// target. A symlink target is refused (never written through).
func atomicWrite(path string, content []byte, existingMode *os.FileMode) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return &Error{
			Code:    "io_error",
			Path:    path,
			Message: "Refusing to write through symlink: " + path + ". Resolve the symlink and pass the real target path explicitly.",
		}
	}

	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	dir := dirOf(path)
	tempPath := path + ".tmp." + strconv.Itoa(os.Getpid()) + "." + suffix

	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	writeErr := func() error {
		if _, err := f.Write(content); err != nil {
			return err
		}
		if existingMode != nil {
			if err := f.Chmod(*existingMode); err != nil {
				return err
			}
		}
		return f.Sync()
	}()
	closeErr := f.Close()
	if writeErr != nil {
		os.Remove(tempPath)
		return writeErr
	}
	if closeErr != nil {
		os.Remove(tempPath)
		return closeErr
	}
	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return err
	}
	_ = dir
	return nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == os.PathSeparator {
			if i == 0 {
				return string(os.PathSeparator)
			}
			return path[:i]
		}
	}
	return "."
}

func randomSuffix() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
