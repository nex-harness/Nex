package tools

import (
	"errors"
	"path"
)

// ErrEmptyPath is the CoreError message resolveWorkspacePath throws for "".
var ErrEmptyPath = errors.New("Tool path must not be empty")

// ResolveWorkspacePath mirrors resolveWorkspacePath(): an absolute path is
// cleaned, a relative one is joined to the working directory. As in the Node
// release it does not hard-block paths outside workspaceRoot.
func ResolveWorkspacePath(inputPath, workingDirectory, workspaceRoot, operation string) (string, error) {
	normalizedWorkingDirectory, err := normalizeAbsoluteDirectory(workingDirectory, "workingDirectory")
	if err != nil {
		return "", err
	}
	if _, err := normalizeAbsoluteDirectory(workspaceRoot, "workspaceRoot"); err != nil {
		return "", err
	}
	if len(inputPath) == 0 {
		return "", ErrEmptyPath
	}
	if path.IsAbs(inputPath) {
		return path.Clean(inputPath), nil
	}
	return path.Join(normalizedWorkingDirectory, inputPath), nil
}

func normalizeAbsoluteDirectory(value, label string) (string, error) {
	normalized := path.Clean(value)
	if !path.IsAbs(normalized) {
		return "", errors.New(label + " must be an absolute path for tool execution")
	}
	return normalized, nil
}
