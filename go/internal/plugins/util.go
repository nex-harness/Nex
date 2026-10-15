// util.go: small shared helpers for the plugins package.
package plugins

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"runtime"
	"time"
)

// processOwnerID mirrors atomicProcessOwnerId: one random id per process,
// distinguishing two writers that share a pid space (tests) and marking
// records as ours.
var processOwnerID = randomHexID()

func randomHexID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand failure is fatal for a security boundary; fall back to
		// a time-derived id so the process still starts.
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))[:32]
	}
	return hex.EncodeToString(buffer)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func jsonMarshalIndent(value any) ([]byte, error) {
	return json.MarshalIndent(value, "", "  ")
}

func timeNowUnix() int64 { return time.Now().Unix() }

func runtimeOS() string { return runtime.GOOS }

// ContentFingerprint ports computeRecursiveContentFingerprint: a stable
// content hash of a directory tree (sorted children, name + child hash).
func ContentFingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	if info.Mode().IsRegular() {
		hash.Write([]byte("file\x00"))
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		hash.Write(data)
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	if !info.IsDir() {
		return "", &UnsupportedArchiveSourceError{Path: path}
	}
	hash.Write([]byte("dir\x00"))
	children, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	// localeCompare order (byte order for the plugin charset).
	names := make([]string, 0, len(children))
	for _, child := range children {
		names = append(names, child.Name())
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	for _, name := range names {
		hash.Write([]byte(name))
		hash.Write([]byte("\x00"))
		childHash, err := ContentFingerprint(joinPath(path, name))
		if err != nil {
			return "", err
		}
		hash.Write([]byte(childHash))
		hash.Write([]byte("\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// UnsupportedArchiveSourceError marks non-regular, non-dir sources.
type UnsupportedArchiveSourceError struct{ Path string }

func (e *UnsupportedArchiveSourceError) Error() string {
	return "unsupported plugin archive source: " + e.Path
}

// RecursiveSize ports computeRecursiveSize (plugin-sync variant): regular
// files contribute their size, directories recurse, anything else 0.
func RecursiveSize(path string) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if info.Mode().IsRegular() {
		return info.Size(), nil
	}
	if !info.IsDir() {
		return 0, nil
	}
	var total int64
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		size, err := RecursiveSize(joinPath(path, entry.Name()))
		if err != nil {
			return 0, err
		}
		total += size
	}
	return total, nil
}

func joinPath(base, name string) string {
	return base + string(os.PathSeparator) + name
}
