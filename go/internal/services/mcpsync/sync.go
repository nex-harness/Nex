// sync.go: the MCP sync surface — local candidates, remote statuses,
// export, the write-access preflight, and the import with filesystem-path
// rewriting.
package mcpsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ListLocalUserMcpCandidates ports listLocalUserMcpCandidates.
func (s *Service) ListLocalUserMcpCandidates(ctx context.Context) (*CandidateListResult, error) {
	records, err := s.collectEffectiveUserMcpRecords()
	if err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, len(records))
	for _, record := range records {
		candidates = append(candidates, Candidate{
			ID:      candidateID(record),
			Name:    record.Name,
			Config:  record.Config,
			Enabled: record.Enabled,
			Source:  record.Source,
			Path:    record.Path,
		})
	}
	return &CandidateListResult{Candidates: candidates, LocalHomeDir: s.homeDir}, nil
}

// ListRemoteUserMcpStatuses ports listRemoteUserMcpStatuses.
func (s *Service) ListRemoteUserMcpStatuses(ctx context.Context, names []string) (*RemoteStatusResult, error) {
	existingByName, err := s.collectEffectiveUserMcpRecordByName()
	if err != nil {
		return nil, err
	}
	statuses := make([]RemoteStatus, 0, len(names))
	for _, name := range names {
		if existing, ok := existingByName[normalizeMcpNameKey(name)]; ok {
			statuses = append(statuses, RemoteStatus{Name: name, Exists: true, Path: existing.Path})
		} else {
			statuses = append(statuses, RemoteStatus{Name: name, Exists: false})
		}
	}
	return &RemoteStatusResult{Statuses: statuses, RemoteHomeDir: s.homeDir}, nil
}

// ExportMcpServers ports exportMcpServers.
func (s *Service) ExportMcpServers(ctx context.Context, serverIDs []string) (*ExportResult, error) {
	records, err := s.collectEffectiveUserMcpRecords()
	if err != nil {
		return nil, err
	}
	candidateByID := map[string]Candidate{}
	for _, record := range records {
		candidate := Candidate{ID: candidateID(record), Name: record.Name, Config: record.Config, Enabled: record.Enabled, Source: record.Source, Path: record.Path}
		candidateByID[candidate.ID] = candidate
	}
	result := &ExportResult{LocalHomeDir: s.homeDir}
	for _, id := range serverIDs {
		candidate, ok := candidateByID[id]
		if !ok {
			return nil, fmt.Errorf("mcp sync candidate not found: %s", id)
		}
		result.Servers = append(result.Servers, ExportedServer{
			ID: candidate.ID, Name: candidate.Name, Config: candidate.Config,
			Enabled: candidate.Enabled, Source: candidate.Source, Path: candidate.Path,
		})
	}
	return result, nil
}

// CheckRemoteUserMcpWriteAccess ports checkRemoteUserMcpWriteAccess.
func (s *Service) CheckRemoteUserMcpWriteAccess(ctx context.Context) (*WriteAccessResult, error) {
	directory := filepath.Dir(s.userNexMcpConfigPath())
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return &WriteAccessResult{OK: false, Path: directory, Error: err.Error()}, nil
	}
	markerPath := filepath.Join(directory, fmt.Sprintf(".nex-sync-preflight-%d-%s", os.Getpid(), randomMarkerID()))
	if err := os.WriteFile(markerPath, []byte("ok"), 0o644); err != nil {
		os.Remove(markerPath)
		return &WriteAccessResult{OK: false, Path: directory, Error: err.Error()}, nil
	}
	os.Remove(markerPath)
	return &WriteAccessResult{OK: true, Path: directory}, nil
}

// ImportMcpServers ports importMcpServers (overwrite refused): filesystem
// server args are rewritten from the local home/workspace to the remote's.
func (s *Service) ImportMcpServers(ctx context.Context, servers []ExportedServer, localHomeDir, localWorkspacePath, remoteWorkspacePath string, overwrite bool) (*ImportResult, error) {
	if overwrite {
		return nil, fmt.Errorf("mcp sync overwrite is not supported")
	}
	targetPath := s.userNexMcpConfigPath()
	current, err := readJSONObject(targetPath)
	if err != nil {
		return nil, err
	}
	if current == nil {
		current = map[string]any{}
	}
	targetServers := readServerMapFromJson(current, nexMcpDescriptor.configKeyName)
	existingByName, err := s.collectEffectiveUserMcpRecordByName()
	if err != nil {
		return nil, err
	}
	result := &ImportResult{}
	changed := false
	for _, server := range servers {
		nameKey := normalizeMcpNameKey(server.Name)
		if _, exists := targetServers[server.Name]; exists {
			result.Results = append(result.Results, ImportResultItem{Name: server.Name, Status: "skipped", Path: targetPath})
			continue
		}
		if existing, ok := existingByName[nameKey]; ok {
			result.Results = append(result.Results, ImportResultItem{Name: server.Name, Status: "skipped", Path: existing.Path})
			continue
		}
		rewrittenConfig := rewriteFilesystemMcpConfig(server.Name,
			setServerEnabled(cloneMap(server.Config), server.Enabled),
			localHomeDir, localWorkspacePath, s.homeDir, remoteWorkspacePath)
		targetServers[server.Name] = rewrittenConfig
		existingByName[nameKey] = ServerRecord{Name: server.Name, Config: rewrittenConfig, Enabled: server.Enabled, Source: "nex", Path: targetPath}
		result.Results = append(result.Results, ImportResultItem{Name: server.Name, Status: "synced", Path: targetPath})
		changed = true
	}
	if changed {
		if err := s.writeTextAtomic(targetPath, marshalIndentedNL(writeServerMapToJson(current, nexMcpDescriptor.configKeyName, targetServers))); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Service) collectEffectiveUserMcpRecords() ([]ServerRecord, error) {
	nexRecords, err := s.readDirectoryServersFromFile(nexMcpDescriptor, "user", "")
	if err != nil {
		return nil, err
	}
	if len(nexRecords) > 0 {
		return sortMcpRecords(nexRecords), nil
	}
	agentsRecords, err := s.readDirectoryServersFromFile(agentsMcpDescriptor, "user", "")
	if err != nil {
		return nil, err
	}
	return sortMcpRecords(agentsRecords), nil
}

func (s *Service) collectEffectiveUserMcpRecordByName() (map[string]ServerRecord, error) {
	records, err := s.collectEffectiveUserMcpRecords()
	if err != nil {
		return nil, err
	}
	result := map[string]ServerRecord{}
	for _, record := range records {
		key := normalizeMcpNameKey(record.Name)
		if _, ok := result[key]; !ok {
			result[key] = record
		}
	}
	return result, nil
}

func sortMcpRecords(records []ServerRecord) []ServerRecord {
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records
}

func candidateID(record ServerRecord) string {
	sum := sha256.Sum256([]byte(record.Source + ":" + record.Path + ":" + record.Name))
	return hex.EncodeToString(sum[:])
}

func normalizeMcpNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func cloneMap(config map[string]any) map[string]any {
	data, _ := json.Marshal(config)
	var out map[string]any
	json.Unmarshal(data, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func randomMarkerID() string {
	buffer := make([]byte, 8)
	if _, err := readRandom(buffer); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(buffer)
}

func readJSONObject(filePath string) (map[string]any, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("无法读取 MCP 配置文件 %s: %s", filePath, err.Error())
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// A corrupt config.json must NOT be treated as empty: merging into
		// it would silently wipe provider, MCP, and secret config.
		return nil, fmt.Errorf("无法解析 MCP 配置文件 %s: %s", filePath, err.Error())
	}
	return parsed, nil
}

func (s *Service) writeTextAtomic(filePath, content string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	tempPath := filepath.Join(filepath.Dir(filePath),
		fmt.Sprintf("%s.%d.%s.tmp", filepath.Base(filePath), os.Getpid(), randomMarkerID()))
	// Remote MCP configs persist env/header/token secrets; the temp file's
	// mode must not depend on the remote umask.
	if err := os.WriteFile(tempPath, []byte(content), secretFileMode); err != nil {
		return err
	}
	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}

func marshalIndentedNL(value any) string {
	data, _ := json.MarshalIndent(value, "", "  ")
	return string(data) + "\n"
}
