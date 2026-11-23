package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

const releaseManifestCacheTTL = 5 * time.Minute

// RunnerBinaryEntry describes a staged multi-platform nex-runner binary.
type RunnerBinaryEntry struct {
	Filename   string `json:"filename"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
	ReleaseURL string `json:"release_url,omitempty"`
}

// RunnerManifest describes the target nex-runner artifact set hosted or referenced by nex-server.
type RunnerManifest struct {
	Version              string                       `json:"version"`
	CommitSHA            string                       `json:"commit_sha,omitempty"`
	MinProtocolVersion   int                          `json:"min_protocol_version,omitempty"`
	RequiredCapabilities []string                     `json:"required_capabilities,omitempty"`
	BuiltAt              int64                        `json:"built_at,omitempty"`
	ReleaseBaseURL       string                       `json:"release_base_url,omitempty"`
	Binaries             map[string]RunnerBinaryEntry `json:"binaries,omitempty"`
}

type releaseManifestCache struct {
	mu        sync.Mutex
	url       string
	fetchedAt time.Time
	manifest  *RunnerManifest
}

type runnerRuntimeMeta struct {
	RunnerID        string
	OS              string
	Arch            string
	Version         string
	BuildSHA        string
	BinarySHA256    string
	ProtocolVersion int
	Capabilities    []string
	IsDev           bool
	Elevated        bool
	UpdateStatus    string
	UpdateReason    string
	TargetVersion   string
	TargetBuildSHA  string
	TargetSHA256    string
	DownloadURLs    []string
	Incompatible    bool
	UpdatePhase     string
	UpdateProgress  int
	UpdateError     string
}

func (s *Server) effectiveMinRunnerProtocol() int {
	if s.cfg.MinRunnerProtocolVersion > 0 {
		return s.cfg.MinRunnerProtocolVersion
	}
	return protocol.MinRunnerProtocolVersion
}

func (s *Server) effectiveRequiredCapabilities() []string {
	if len(s.cfg.RequiredRunnerCapabilities) > 0 {
		return s.cfg.RequiredRunnerCapabilities
	}
	return nil
}

func (s *Server) loadLocalRunnerManifest() *RunnerManifest {
	var parsed *RunnerManifest
	for _, cand := range s.candidateRunnersDirs() {
		manifestPath := filepath.Join(cand.dir, "manifest.json")
		raw, err := os.ReadFile(manifestPath)
		if err == nil {
			var m RunnerManifest
			if json.Unmarshal(raw, &m) == nil && len(m.Binaries) > 0 {
				// Attach computed hashes/paths if missing
				for key, entry := range m.Binaries {
					if entry.Filename == "" {
						osPart, archPart, _ := strings.Cut(key, "/")
						entry.Filename = runnerBinaryFileName(osPart, archPart)
					}
					fullPath := filepath.Join(cand.dir, entry.Filename)
					if entry.SHA256 == "" {
						if sum, sz, hErr := fileSHA256AndSize(fullPath); hErr == nil {
							entry.SHA256 = sum
							if entry.SizeBytes == 0 {
								entry.SizeBytes = sz
							}
						}
					}
					if entry.ReleaseURL == "" && m.ReleaseBaseURL != "" && entry.Filename != "" {
						entry.ReleaseURL = strings.TrimRight(m.ReleaseBaseURL, "/") + "/" + entry.Filename
					}
					m.Binaries[key] = entry
				}
				parsed = &m
				break
			}
		}
	}

	if parsed != nil {
		return parsed
	}

	// Fallback: synthesize manifest dynamically from staged runner files on disk
	avail := s.listAvailableRunnerBinaries()
	if len(avail) == 0 && strings.TrimSpace(s.cfg.Version) == "" {
		return nil
	}
	m := &RunnerManifest{
		Version:              strings.TrimSpace(s.cfg.Version),
		MinProtocolVersion:   s.effectiveMinRunnerProtocol(),
		RequiredCapabilities: s.effectiveRequiredCapabilities(),
		Binaries:             make(map[string]RunnerBinaryEntry, len(avail)),
	}
	for _, info := range avail {
		osName, archName := normalizeTargetOSArch(info.OS, info.Arch)
		if path, ok := s.findExistingRunnerBinary(osName, archName); ok {
			if sum, sz, hErr := fileSHA256AndSize(path); hErr == nil {
				key := osName + "/" + archName
				m.Binaries[key] = RunnerBinaryEntry{
					Filename:  runnerBinaryFileName(osName, archName),
					SHA256:    sum,
					SizeBytes: sz,
				}
			}
		}
	}
	return m
}

func (s *Server) findExistingRunnerBinary(targetOS, targetArch string) (string, bool) {
	fileName := runnerBinaryFileName(targetOS, targetArch)
	for _, cand := range s.candidateRunnersDirs() {
		p := filepath.Join(cand.dir, fileName)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p, true
		}
	}
	return "", false
}

func (s *Server) fetchReleaseManifest(ctx context.Context) *RunnerManifest {
	url := strings.TrimSpace(s.cfg.ReleaseManifestURL)
	if url == "" {
		return nil
	}
	s.releaseCache.mu.Lock()
	defer s.releaseCache.mu.Unlock()
	if s.releaseCache.url == url && s.releaseCache.manifest != nil && time.Since(s.releaseCache.fetchedAt) < releaseManifestCacheTTL {
		return s.releaseCache.manifest
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return s.releaseCache.manifest
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return s.releaseCache.manifest
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return s.releaseCache.manifest
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return s.releaseCache.manifest
	}
	var m RunnerManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return s.releaseCache.manifest
	}
	s.releaseCache.url = url
	s.releaseCache.fetchedAt = time.Now()
	s.releaseCache.manifest = &m
	return &m
}

func (s *Server) resolveTargetForPlatform(ctx context.Context, osName, archName string) (version, commitSHA string, entry *RunnerBinaryEntry, downloadURLs []string, minProto int, reqCaps []string) {
	osNorm, archNorm := normalizeTargetOSArch(osName, archName)
	key := osNorm + "/" + archNorm
	local := s.loadLocalRunnerManifest()
	rel := s.fetchReleaseManifest(ctx)

	minProto = s.effectiveMinRunnerProtocol()
	reqCaps = s.effectiveRequiredCapabilities()
	if local != nil {
		version = local.Version
		commitSHA = local.CommitSHA
		if local.MinProtocolVersion > minProto {
			minProto = local.MinProtocolVersion
		}
		if len(local.RequiredCapabilities) > 0 && len(reqCaps) == 0 {
			reqCaps = local.RequiredCapabilities
		}
		if e, ok := local.Binaries[key]; ok {
			copyEntry := e
			entry = &copyEntry
		}
	}

	serverFallbackURL := fmt.Sprintf("/api/v1/runners/binary/%s/%s", osNorm, archNorm)
	var releaseAssetURL string
	if rel != nil {
		if re, ok := rel.Binaries[key]; ok {
			relURL := strings.TrimSpace(re.ReleaseURL)
			if relURL == "" && rel.ReleaseBaseURL != "" && re.Filename != "" {
				relURL = strings.TrimRight(rel.ReleaseBaseURL, "/") + "/" + re.Filename
			}
			// Prefer Release only when it carries the exact target binary (or when no local binary exists)
			if relURL != "" && (entry == nil || (re.SHA256 != "" && re.SHA256 == entry.SHA256)) {
				releaseAssetURL = relURL
				if entry == nil {
					copyEntry := re
					entry = &copyEntry
					if version == "" {
						version = rel.Version
					}
					if commitSHA == "" {
						commitSHA = rel.CommitSHA
					}
				}
			}
		}
	}
	if entry != nil {
		if releaseAssetURL == "" && entry.ReleaseURL != "" {
			releaseAssetURL = entry.ReleaseURL
		}
		if releaseAssetURL != "" {
			downloadURLs = append(downloadURLs, releaseAssetURL)
		}
		downloadURLs = append(downloadURLs, serverFallbackURL)
	}
	return version, commitSHA, entry, downloadURLs, minProto, reqCaps
}

func (s *Server) evaluateRunnerUpdate(ctx context.Context, meta *runnerRuntimeMeta) {
	if meta == nil {
		return
	}
	targetVer, targetSHA, entry, urls, minProto, reqCaps := s.resolveTargetForPlatform(ctx, meta.OS, meta.Arch)
	meta.TargetVersion = targetVer
	meta.TargetBuildSHA = targetSHA
	meta.DownloadURLs = urls
	if entry != nil {
		meta.TargetSHA256 = entry.SHA256
	} else {
		meta.TargetSHA256 = ""
	}

	// 1. Check protocol / capability compatibility (requires mandatory upgrade if failing)
	if meta.ProtocolVersion > 0 && meta.ProtocolVersion < minProto {
		meta.UpdateStatus = "update_required"
		meta.Incompatible = true
		meta.UpdateReason = fmt.Sprintf("Runner protocol v%d is below required minimum v%d", meta.ProtocolVersion, minProto)
		return
	}
	if len(reqCaps) > 0 {
		capSet := make(map[string]bool, len(meta.Capabilities))
		for _, c := range meta.Capabilities {
			capSet[strings.TrimSpace(c)] = true
		}
		var missing []string
		for _, req := range reqCaps {
			if !capSet[strings.TrimSpace(req)] {
				missing = append(missing, req)
			}
		}
		if len(missing) > 0 {
			meta.UpdateStatus = "update_required"
			meta.Incompatible = true
			meta.UpdateReason = fmt.Sprintf("Runner missing required capabilities: %s", strings.Join(missing, ", "))
			return
		}
	}

	meta.Incompatible = false

	// 2. Check binary digest / version delta for recommended updates
	if entry != nil && entry.SHA256 != "" {
		if meta.BinarySHA256 != "" && !strings.EqualFold(meta.BinarySHA256, entry.SHA256) {
			meta.UpdateStatus = "update_available"
			if targetVer != "" {
				meta.UpdateReason = fmt.Sprintf("New runner version %s is available", targetVer)
			} else {
				meta.UpdateReason = "Newer runner binary is available"
			}
			return
		}
		if meta.IsDev && targetVer != "" && !protocol.IsDevVersion(targetVer) {
			meta.UpdateStatus = "update_available"
			meta.UpdateReason = fmt.Sprintf("Release %s is available for dev runner", targetVer)
			return
		}
	} else if meta.IsDev && targetVer != "" && !protocol.IsDevVersion(targetVer) && len(urls) > 0 {
		meta.UpdateStatus = "update_available"
		meta.UpdateReason = fmt.Sprintf("Release %s is available for dev runner", targetVer)
		return
	}

	meta.UpdateStatus = "up_to_date"
	meta.UpdateReason = ""
}

func (s *Server) setRunnerRuntimeMeta(meta *runnerRuntimeMeta) {
	if meta == nil || meta.RunnerID == "" {
		return
	}
	s.mu.Lock()
	if s.runnerMeta == nil {
		s.runnerMeta = make(map[string]*runnerRuntimeMeta)
	}
	s.runnerMeta[meta.RunnerID] = meta
	s.mu.Unlock()
}

func (s *Server) getRunnerRuntimeMetaCopy(runnerID string) *runnerRuntimeMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := s.runnerMeta[runnerID]
	if m == nil {
		return nil
	}
	cp := *m
	cp.Capabilities = append([]string(nil), m.Capabilities...)
	cp.DownloadURLs = append([]string(nil), m.DownloadURLs...)
	return &cp
}

func (s *Server) checkRunnerCompatible(ctx context.Context, runnerID string) error {
	meta := s.getRunnerRuntimeMetaCopy(runnerID)
	if meta == nil || !meta.Incompatible {
		return nil
	}
	target := meta.TargetVersion
	if target == "" {
		target = "latest"
	}
	return fmt.Errorf("runner %s is incompatible (%s); upgrade to %s before running tasks", runnerID, meta.UpdateReason, target)
}

func (s *Server) enrichRunnerRecord(ctx context.Context, rec *store.RunnerRecord) {
	if rec == nil {
		return
	}
	rec.IsDev = protocol.IsDevVersion(rec.Version)
	meta := s.getRunnerRuntimeMetaCopy(rec.ID)
	if meta == nil {
		meta = &runnerRuntimeMeta{
			RunnerID: rec.ID,
			OS:       rec.OS,
			Arch:     rec.Arch,
			Version:  rec.Version,
			IsDev:    rec.IsDev,
		}
	}
	s.evaluateRunnerUpdate(ctx, meta)
	rec.BuildSHA = meta.BuildSHA
	rec.BinarySHA256 = meta.BinarySHA256
	rec.ProtocolVersion = meta.ProtocolVersion
	rec.Capabilities = meta.Capabilities
	rec.IsDev = meta.IsDev
	rec.Elevated = meta.Elevated
	rec.UpdateStatus = meta.UpdateStatus
	rec.UpdateReason = meta.UpdateReason
	rec.TargetVersion = meta.TargetVersion
	rec.UpdatePhase = meta.UpdatePhase
	rec.UpdateProgress = meta.UpdateProgress
	rec.UpdateError = meta.UpdateError
	rec.Incompatible = meta.Incompatible
}

func fileSHA256AndSize(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
