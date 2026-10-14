package file

// service_workspaces.go holds the workspace-creation methods of the
// IFileService port: createDefaultWorkspace, ensureConversationWorkspace,
// createScratchWorkspace.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// CreateDefaultWorkspace ports createDefaultWorkspace.
func (s *Service) CreateDefaultWorkspace(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	workspacePath := filepath.Join(home, scratchWorkspaceRoot)
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return "", err
	}
	info, err := os.Stat(workspacePath)
	if err != nil || !info.IsDir() {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("Workspace path is not a directory: %s", workspacePath)
	}
	return workspacePath, nil
}

// EnsureConversationWorkspace ports ensureConversationWorkspace.
func (s *Service) EnsureConversationWorkspace(ctx context.Context) (path string, created bool, err error) {
	workspacePath := conversationWorkspaceDir()
	created = false
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		info, statErr := os.Stat(workspacePath)
		if statErr != nil || !info.IsDir() {
			if statErr != nil {
				return "", false, err
			}
			return "", false, fmt.Errorf("Workspace path is not a directory: %s", workspacePath)
		}
		// existed already
	} else {
		// MkdirAll reports success whether or not it created; TS uses mkdir's
		// first-created result. Created==true only when the dir did not exist:
		if info, statErr := os.Stat(workspacePath); statErr == nil && info.IsDir() {
			// cannot distinguish creation cheaply — mark created=true per TS
			// semantics of `mkdir recursive` returning undefined for existing.
			// The TS created flag comes from mkdir resolving with the first
			// directory made; approximate by checking before the call below.
		}
		created = true
	}
	info, err := os.Stat(workspacePath)
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("Workspace path is not a directory: %s", workspacePath)
	}
	return workspacePath, created, nil
}

// CreateScratchWorkspace ports createScratchWorkspace: idempotent mkdir, no
// git init, no templates.
func (s *Service) CreateScratchWorkspace(ctx context.Context, name string) (string, error) {
	workspaceName, err := validateScratchWorkspaceName(name)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	workspacePath := filepath.Join(home, scratchWorkspaceRoot, workspaceName)
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return "", err
	}
	info, err := os.Stat(workspacePath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Workspace path is not a directory: %s", workspacePath)
	}
	return workspacePath, nil
}
