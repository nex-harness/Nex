// Package skillsync is the ISkillSyncService port (step 3.7): local user
// skill candidates for SSH sync, remote existence statuses, the tar.gz
// export/import through the shared archive codec, and the write-access
// preflight.
//
// Source: packages/services/src/skill-sync/skillSyncService.ts. Remote
// statuses must re-validate directoryName server-side (the client's
// candidates are not trusted); imports extract to a temp dir, validate
// SKILL.md presence and dedupe by frontmatter name before copying.
package skillsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tylinux/nex/go/internal/skills"
)

// ChannelName is the rpc channel name (ServiceChannels.SkillSync).
const ChannelName = "skill-sync"

// DefaultMaxArchiveBytes ports DEFAULT_MAX_ARCHIVE_BYTES (20 MiB).
const DefaultMaxArchiveBytes = 20 * 1024 * 1024

// Candidate ports SkillSyncCandidate.
type Candidate struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DirectoryName string `json:"directoryName"`
	Description   string `json:"description,omitempty"`
	Path          string `json:"path"`
	SizeBytes     int64  `json:"sizeBytes"`
}

// CandidateListResult ports SkillSyncCandidateListResult.
type CandidateListResult struct {
	Candidates      []Candidate `json:"candidates"`
	MaxArchiveBytes int64       `json:"maxArchiveBytes"`
}

// RemoteStatus ports one SkillSyncRemoteStatus.
type RemoteStatus struct {
	DirectoryName string `json:"directoryName"`
	Exists        bool   `json:"exists"`
	Path          string `json:"path,omitempty"`
}

// RemoteStatusResult ports SkillSyncRemoteStatusResult.
type RemoteStatusResult struct {
	Statuses []RemoteStatus `json:"statuses"`
}

// ArchiveExportResult ports SkillSyncArchiveExportResult.
type ArchiveExportResult struct {
	Archive      []byte `json:"archive"`
	ArchiveBytes int    `json:"archiveBytes"`
	Skills       []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		DirectoryName string `json:"directoryName"`
	} `json:"skills"`
}

// ImportResultItem ports one SkillSyncImportResult item.
type ImportResultItem struct {
	Name          string `json:"name"`
	DirectoryName string `json:"directoryName"`
	Status        string `json:"status"` // synced | skipped | failed
	Path          string `json:"path,omitempty"`
	Error         string `json:"error,omitempty"`
}

// ImportResult ports SkillSyncImportResult.
type ImportResult struct {
	Results []ImportResultItem `json:"results"`
}

// WriteAccessResult ports RemoteSyncWriteAccessResult.
type WriteAccessResult struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`
}

// Options carry the seams.
type Options struct {
	HomeDir         string
	MaxArchiveBytes int64
}

// Service is the ISkillSyncService implementation.
type Service struct {
	options         Options
	homeDir         string
	maxArchiveBytes int64
}

// New builds the service.
func New(options Options) *Service {
	if options.MaxArchiveBytes <= 0 {
		options.MaxArchiveBytes = DefaultMaxArchiveBytes
	}
	return &Service{options: options, homeDir: options.HomeDir, maxArchiveBytes: options.MaxArchiveBytes}
}

func (s *Service) userNexSkillRoot() string    { return skills.UserNexSkillRoot(s.homeDir) }
func (s *Service) userAgentsSkillRoot() string { return skills.UserAgentsSkillRoot(s.homeDir) }

// ListLocalUserSkillCandidates ports listLocalUserSkillCandidates: .nex
// candidates first, then uncovered .agents candidates, deduped by canonical
// SKILL.md path.
func (s *Service) ListLocalUserSkillCandidates(ctx context.Context) (*CandidateListResult, error) {
	candidates, err := s.collectUserSkillCandidates()
	if err != nil {
		return nil, err
	}
	return &CandidateListResult{Candidates: candidates, MaxArchiveBytes: s.maxArchiveBytes}, nil
}

// ListRemoteUserSkillStatuses ports listRemoteUserSkillStatuses. The
// directoryNames are re-validated as safe relative paths server-side; an
// existing directory by folder OR by frontmatter name counts as present.
func (s *Service) ListRemoteUserSkillStatuses(ctx context.Context, directoryNames []string, requestedSkills []struct {
	DirectoryName string
	Name          string
}) (*RemoteStatusResult, error) {
	root := s.userNexSkillRoot()
	existingSkillPathByName, err := s.collectUserSkillDirectoryPathByName()
	if err != nil {
		return nil, err
	}
	requestedNameByDirectory := map[string]string{}
	for _, skill := range requestedSkills {
		normalized, err := skills.NormalizeSyncRelativePath(skill.DirectoryName, "")
		if err != nil {
			return nil, err
		}
		requestedNameByDirectory[normalized] = skill.Name
	}
	statuses := make([]RemoteStatus, 0, len(directoryNames))
	for _, rawName := range directoryNames {
		directoryName, err := skills.NormalizeSyncRelativePath(rawName, "")
		if err != nil {
			return nil, err
		}
		path, err := skills.ResolveSyncPathWithin(root, directoryName, "")
		if err != nil {
			return nil, err
		}
		if _, statErr := os.Stat(path); statErr == nil {
			statuses = append(statuses, RemoteStatus{DirectoryName: directoryName, Exists: true, Path: path})
			continue
		}
		var existingPath string
		if requestedName, ok := requestedNameByDirectory[directoryName]; ok {
			existingPath = existingSkillPathByName[skills.NormalizeSkillNameKey(requestedName)]
		}
		if existingPath != "" {
			statuses = append(statuses, RemoteStatus{DirectoryName: directoryName, Exists: true, Path: existingPath})
		} else {
			statuses = append(statuses, RemoteStatus{DirectoryName: directoryName, Exists: false})
		}
	}
	return &RemoteStatusResult{Statuses: statuses}, nil
}

// ExportSkillsArchive ports exportSkillsArchive: size-check the selection,
// build the archive, size-check the archive.
func (s *Service) ExportSkillsArchive(ctx context.Context, skillIDs []string) (*ArchiveExportResult, error) {
	candidates, err := s.collectUserSkillCandidates()
	if err != nil {
		return nil, err
	}
	candidateByID := map[string]Candidate{}
	for _, candidate := range candidates {
		candidateByID[candidate.ID] = candidate
	}
	var selected []Candidate
	var selectedBytes int64
	for _, id := range skillIDs {
		candidate, ok := candidateByID[id]
		if !ok {
			return nil, fmt.Errorf("skill sync candidate not found: %s", id)
		}
		selected = append(selected, candidate)
		selectedBytes += candidate.SizeBytes
	}
	if selectedBytes > s.maxArchiveBytes {
		return nil, &skills.SizeLimitError{ActualBytes: selectedBytes, MaxBytes: s.maxArchiveBytes, Phase: "selected-content"}
	}
	entries := make([]skills.ArchiveEntry, 0, len(selected))
	for _, candidate := range selected {
		sourcePath, err := filepath.EvalSymlinks(filepath.Dir(candidate.Path))
		if err != nil {
			sourcePath = filepath.Dir(candidate.Path)
		}
		entries = append(entries, skills.ArchiveEntry{SourcePath: sourcePath, ArchivePath: candidate.DirectoryName})
	}
	archive, err := skills.CreateSyncArchive(entries)
	if err != nil {
		return nil, err
	}
	if int64(len(archive)) > s.maxArchiveBytes {
		return nil, &skills.SizeLimitError{ActualBytes: int64(len(archive)), MaxBytes: s.maxArchiveBytes, Phase: "archive"}
	}
	result := &ArchiveExportResult{Archive: archive, ArchiveBytes: len(archive)}
	for _, candidate := range selected {
		result.Skills = append(result.Skills, struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			DirectoryName string `json:"directoryName"`
		}{ID: candidate.ID, Name: candidate.Name, DirectoryName: candidate.DirectoryName})
	}
	return result, nil
}

// CheckRemoteUserSkillWriteAccess ports checkRemoteUserSkillWriteAccess.
func (s *Service) CheckRemoteUserSkillWriteAccess(ctx context.Context) (*WriteAccessResult, error) {
	return checkDirectoryWriteAccess(s.userNexSkillRoot()), nil
}

// ImportSkillsArchive ports importSkillsArchive (overwrite is refused).
func (s *Service) ImportSkillsArchive(ctx context.Context, archive []byte, overwrite bool) (*ImportResult, error) {
	if overwrite {
		return nil, fmt.Errorf("skill sync overwrite is not supported")
	}
	if int64(len(archive)) > s.maxArchiveBytes {
		return nil, &skills.SizeLimitError{ActualBytes: int64(len(archive)), MaxBytes: s.maxArchiveBytes, Phase: "archive"}
	}
	return s.importArchive(archive)
}

// checkDirectoryWriteAccess ports checkRemoteSyncDirectoryWriteAccess: write
// and remove a unique marker file.
func checkDirectoryWriteAccess(directoryPath string) *WriteAccessResult {
	if err := os.MkdirAll(directoryPath, 0o755); err != nil {
		return &WriteAccessResult{OK: false, Path: directoryPath, Error: err.Error()}
	}
	markerPath := filepath.Join(directoryPath,
		fmt.Sprintf(".nex-sync-preflight-%d-%s", os.Getpid(), randomHexID()))
	if err := os.WriteFile(markerPath, []byte("ok"), 0o644); err != nil {
		os.Remove(markerPath)
		return &WriteAccessResult{OK: false, Path: directoryPath, Error: err.Error()}
	}
	os.Remove(markerPath)
	return &WriteAccessResult{OK: true, Path: directoryPath}
}

func randomHexID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(buffer)
}
