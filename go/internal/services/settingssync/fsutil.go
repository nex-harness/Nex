// fsutil.go: small filesystem helpers used across the package (kept here so
// the category files stay about their category).
package settingssync

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func readFileOrEmpty(filePath string) ([]byte, error) {
	return os.ReadFile(filePath)
}

func unmarshalJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func parentDir(path string) string { return filepath.Dir(path) }

func baseName(path string) string { return filepath.Base(path) }

func joinName(root, name string) string { return filepath.Join(root, name) }

func relativePath(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return target
	}
	return rel
}

// mkdirAll is os.MkdirAll behind the fsutil name.
func mkdirAll(dir string) error { return os.MkdirAll(dir, 0o777) }
