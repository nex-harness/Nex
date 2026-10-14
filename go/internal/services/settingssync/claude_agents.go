// claude_agents.go: the Claude CLAUDE.md → Nex AGENTS.md migration pair.
package settingssync

import (
	"context"
	"os"
	"path/filepath"
)

// ClaudeAgentsFileMigrationStatus is
// SettingsSyncClaudeAgentsFileMigrationStatus.
type ClaudeAgentsFileMigrationStatus struct {
	SourcePath        string `json:"sourcePath"`
	TargetPath        string `json:"targetPath"`
	SourceExists      bool   `json:"sourceExists"`
	TargetExists      bool   `json:"targetExists"`
	Supported         bool   `json:"supported"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

// ClaudeAgentsFileCopyResult is SettingsSyncClaudeAgentsFileCopyResult.
type ClaudeAgentsFileCopyResult struct {
	SourcePath    string `json:"sourcePath"`
	TargetPath    string `json:"targetPath"`
	Status        string `json:"status"` // copied | skipped
	Overwritten   bool   `json:"overwritten"`
	SkippedReason string `json:"skippedReason,omitempty"` // missingSource | targetExists
}

// GetClaudeAgentsFileMigrationStatus ports the same-named method.
func (s *Service) GetClaudeAgentsFileMigrationStatus(ctx context.Context, _ WorkspaceRef) (*ClaudeAgentsFileMigrationStatus, error) {
	l := s.layoutFor("")
	sourcePath := l.claudeUserAgentsFileSourcePath()
	targetPath := l.userNexAgentsFilePath()
	sourceExists := pathExists(sourcePath)
	status := &ClaudeAgentsFileMigrationStatus{
		SourcePath:   sourcePath,
		TargetPath:   targetPath,
		SourceExists: sourceExists,
		TargetExists: pathExists(targetPath),
		Supported:    sourceExists,
	}
	if !sourceExists {
		status.UnavailableReason = "missingSource"
	}
	return status, nil
}

// CopyClaudeAgentsFileToNexAgentsFile ports the same-named method.
func (s *Service) CopyClaudeAgentsFileToNexAgentsFile(ctx context.Context, request CopyClaudeAgentsRequest) (*ClaudeAgentsFileCopyResult, error) {
	l := s.layoutFor("")
	sourcePath := l.claudeUserAgentsFileSourcePath()
	targetPath := l.userNexAgentsFilePath()
	sourceExists := pathExists(sourcePath)
	targetExists := pathExists(targetPath)

	if !sourceExists {
		return &ClaudeAgentsFileCopyResult{
			SourcePath: sourcePath, TargetPath: targetPath,
			Status: "skipped", Overwritten: false, SkippedReason: "missingSource",
		}, nil
	}
	if targetExists && !request.Overwrite {
		return &ClaudeAgentsFileCopyResult{
			SourcePath: sourcePath, TargetPath: targetPath,
			Status: "skipped", Overwritten: false, SkippedReason: "targetExists",
		}, nil
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o777); err != nil {
		return nil, err
	}
	if err := copyFileBytes(sourcePath, targetPath); err != nil {
		return nil, err
	}
	return &ClaudeAgentsFileCopyResult{
		SourcePath: sourcePath, TargetPath: targetPath,
		Status: "copied", Overwritten: targetExists,
	}, nil
}

// WorkspaceRef carries the workspace fields every request accepts.
type WorkspaceRef struct {
	WorkspacePath     string `json:"workspacePath,omitempty"`
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
}

// CopyClaudeAgentsRequest is the copy request.
type CopyClaudeAgentsRequest struct {
	WorkspaceRef
	Overwrite bool `json:"overwrite,omitempty"`
}

func copyFileBytes(source, target string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}
