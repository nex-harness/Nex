// Package webauthfs is the filesystem adapter for the web session store.
//
// go/internal/server/webauth owns the security-relevant logic (what counts as a valid
// session, the fingerprint, pruning) but must not import "os" (enforced by
// internal/archcheck), so the file operations live here: a read that distinguishes "not there
// yet" from "unreadable", and the atomic, mode-0600 write.
package webauthfs

import (
	"os"
	"path/filepath"
	"strconv"
)

// ReadFile returns the whole file. A missing file reports ok=false rather than an error,
// because "no sessions file yet" is the normal first-run state.
func ReadFile(path string) (data []byte, ok bool, err error) {
	content, err := os.ReadFile(path) //nolint:gosec // path comes from the server config dir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return content, true, nil
}

// WriteAtomic writes data to path via a temporary file in the same directory and renames it
// into place, so a reader never observes a partial file. The file (and the temp file) are
// created with mode 0600 because the stored hashes are credential material.
//
// The temp name embeds the pid, mirroring `${filePath}.${process.pid}.tmp` in the TypeScript.
// It is not random: two Go processes never share a pid concurrently, and the rename is atomic,
// so a leftover temp from a crash is overwritten rather than raced on.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
