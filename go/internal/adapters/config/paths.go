// Package config resolves where the agent keeps its files. Environment and
// filesystem access is confined here (and in other adapters) by the archcheck test.
package config

import (
	"os"
	"path/filepath"
)

// SessionDBPath is the default session database: ~/.nex/cli/db/db.sqlite, where
// "~" follows os.UserHomeDir (HOME on Unix, USERPROFILE on Windows), the same
// source Node's os.homedir() uses. The Node agent can relocate it through its
// config file (storage.sessionDbPath); reading that file is not ported yet, so a
// custom path is currently unsupported rather than silently ignored elsewhere.
func SessionDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".nex", "cli", "db", "db.sqlite"), nil
}

// EnsureParent creates the directory holding path.
func EnsureParent(path string) error { return os.MkdirAll(filepath.Dir(path), 0o755) }
