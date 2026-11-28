package server

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

const (
	releaseManifestCacheTTL   = 5 * time.Minute
	runnerDownloadTokenTTL    = 30 * time.Minute
	runnerSucceededBadgeTTL   = 45 * time.Second
)

// issueRunnerDownloadToken mints a short-lived opaque token so connected runners
// can fetch /api/v1/runners/binary/{os}/{arch} even when web password auth is enabled
// and no static PairingSecret/AuthToken was configured on the runner.
func (s *Server) issueRunnerDownloadToken() string {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	tok := hex.EncodeToString(raw[:])
	now := time.Now()
	s.dlTokensMu.Lock()
	defer s.dlTokensMu.Unlock()
	if s.dlTokens == nil {
		s.dlTokens = make(map[string]time.Time)
	}
	for k, exp := range s.dlTokens {
		if now.After(exp) {
			delete(s.dlTokens, k)
		}
	}
	s.dlTokens[tok] = now.Add(runnerDownloadTokenTTL)
	return tok
}

func (s *Server) isValidRunnerDownloadToken(tok string) bool {
	if tok == "" {
		return false
	}
	now := time.Now()
	s.dlTokensMu.Lock()
	defer s.dlTokensMu.Unlock()
	if s.dlTokens == nil {
		return false
	}
	exp, ok := s.dlTokens[tok]
	if !ok {
		return false
	}
	if now.After(exp) {
		delete(s.dlTokens, tok)
		return false
	}
	return true
}

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
	FullDiskAccess  *bool
	AppPath         string
	UpdateStatus    string
	UpdateReason    string
	TargetVersion   string
	TargetBuildSHA  string
	TargetSHA256    string
	DownloadURLs    []string
	Incompatible    bool
	UpdatePhase          string
	UpdateProgress       int
	UpdateMessage        string
	UpdateError          string
	UpdatePhaseUpdatedAt time.Time
	Online               bool
}

func normalizeRunnerVersionForCompare(v string) string {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "v")
	return strings.ToLower(s)
}

func hasRunnerCapability(caps []string, target string) bool {
	for _, c := range caps {
		if strings.EqualFold(strings.TrimSpace(c), target) {
			return true
		}
	}
	return false
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
	if dlTok := s.issueRunnerDownloadToken(); dlTok != "" {
		serverFallbackURL += "?token=" + dlTok
	}
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
	// Detect legacy pre-self-update runners that connected with no BinarySHA256 and no capabilities
	// when a concrete target version/binary exists on the server.
	if meta.Online && meta.BinarySHA256 == "" && !hasRunnerCapability(meta.Capabilities, "self_update_v1") && entry != nil && entry.SHA256 != "" {
		curVer := normalizeRunnerVersionForCompare(meta.Version)
		tgtVer := normalizeRunnerVersionForCompare(targetVer)
		if curVer != "" && tgtVer != "" && curVer != tgtVer {
			meta.UpdateStatus = "update_required"
			meta.Incompatible = true
			meta.UpdateReason = fmt.Sprintf("Legacy runner (%s) lacks self-update and modern protocol capabilities; reinstall %s", meta.Version, targetVer)
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
		// Fallback version comparison for offline or legacy runners without BinarySHA256
		if meta.BinarySHA256 == "" {
			curVer := normalizeRunnerVersionForCompare(meta.Version)
			tgtVer := normalizeRunnerVersionForCompare(targetVer)
			if curVer != "" && tgtVer != "" && curVer != tgtVer {
				meta.UpdateStatus = "update_available"
				meta.UpdateReason = fmt.Sprintf("New runner version %s is available", targetVer)
				return
			}
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
	// Auto-expire transient "succeeded" badge after runnerSucceededBadgeTTL so it does not stay stuck forever.
	if cp.UpdatePhase == "succeeded" && !cp.UpdatePhaseUpdatedAt.IsZero() && time.Since(cp.UpdatePhaseUpdatedAt) >= runnerSucceededBadgeTTL {
		cp.UpdatePhase = ""
		cp.UpdateProgress = 0
		cp.UpdateMessage = ""
	}
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
			Online:   rec.Status == "online",
		}
	} else {
		meta.Online = rec.Status == "online"
	}
	s.evaluateRunnerUpdate(ctx, meta)
	rec.BuildSHA = meta.BuildSHA
	rec.BinarySHA256 = meta.BinarySHA256
	rec.ProtocolVersion = meta.ProtocolVersion
	rec.Capabilities = meta.Capabilities
	rec.IsDev = meta.IsDev
	rec.Elevated = meta.Elevated
	rec.FullDiskAccess = meta.FullDiskAccess
	rec.AppPath = meta.AppPath
	rec.UpdateStatus = meta.UpdateStatus
	rec.UpdateReason = meta.UpdateReason
	rec.TargetVersion = meta.TargetVersion
	rec.TargetBuildSHA = meta.TargetBuildSHA
	rec.UpdatePhase = meta.UpdatePhase
	rec.UpdateProgress = meta.UpdateProgress
	rec.UpdateMessage = meta.UpdateMessage
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

func extractRunnerDownloadToken(r *http.Request) string {
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		if tok := strings.TrimSpace(authz[7:]); tok != "" {
			return tok
		}
	}
	if qTok := strings.TrimSpace(r.URL.Query().Get("token")); qTok != "" {
		return qTok
	}
	return ""
}

func (s *Server) isRunnerBinaryDownloadAuthorized(ctx context.Context, r *http.Request) bool {
	pairingSecret, requireAuth := s.effectiveRunnerPairingSecret(ctx)
	if tok := extractRunnerDownloadToken(r); tok != "" {
		if s.isValidRunnerDownloadToken(tok) {
			return true
		}
		if pairingSecret != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(pairingSecret)) == 1 {
			return true
		}
		if runners, err := s.store.ListRunners(ctx); err == nil {
			for _, rec := range runners {
				if rec.AuthToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(rec.AuthToken)) == 1 {
					return true
				}
			}
		}
		if dev, err := s.store.GetPairedDeviceByTokenHash(ctx, sha256Hex(tok)); err == nil && dev != nil && dev.RevokedAt == 0 {
			return true
		}
		// Fall through to web auth check in case bearer token is a web auth session token.
	}

	enabled, authed := s.isRequestAuthenticated(ctx, r)
	if enabled {
		return authed
	}
	// If web auth is off but runner auth is required, still require a valid runner token unless
	// an explicit paired/device session was already authenticated above.
	if requireAuth && pairingSecret != "" {
		return false
	}
	return authed
}

func isAllowedBinaryPlatform(osName, archName string) bool {
	switch osName {
	case "linux", "darwin", "windows":
	default:
		return false
	}
	switch archName {
	case "amd64", "arm64":
		return true
	default:
		return false
	}
}

func (s *Server) buildCombinedManifest(ctx context.Context) RunnerManifest {
	local := s.loadLocalRunnerManifest()
	rel := s.fetchReleaseManifest(ctx)

	out := RunnerManifest{
		Version:              strings.TrimSpace(s.cfg.Version),
		MinProtocolVersion:   s.effectiveMinRunnerProtocol(),
		RequiredCapabilities: s.effectiveRequiredCapabilities(),
		Binaries:             make(map[string]RunnerBinaryEntry),
	}
	if local != nil {
		if local.Version != "" {
			out.Version = local.Version
		}
		if local.CommitSHA != "" {
			out.CommitSHA = local.CommitSHA
		}
		if local.MinProtocolVersion > out.MinProtocolVersion {
			out.MinProtocolVersion = local.MinProtocolVersion
		}
		if len(out.RequiredCapabilities) == 0 && len(local.RequiredCapabilities) > 0 {
			out.RequiredCapabilities = append([]string(nil), local.RequiredCapabilities...)
		}
		if local.BuiltAt > out.BuiltAt {
			out.BuiltAt = local.BuiltAt
		}
		if local.ReleaseBaseURL != "" {
			out.ReleaseBaseURL = local.ReleaseBaseURL
		}
		for k, v := range local.Binaries {
			out.Binaries[k] = v
		}
	}
	if rel != nil {
		if out.Version == "" || (protocol.IsDevVersion(out.Version) && rel.Version != "" && !protocol.IsDevVersion(rel.Version)) {
			out.Version = rel.Version
		}
		if out.CommitSHA == "" {
			out.CommitSHA = rel.CommitSHA
		}
		if rel.MinProtocolVersion > out.MinProtocolVersion {
			out.MinProtocolVersion = rel.MinProtocolVersion
		}
		if len(out.RequiredCapabilities) == 0 && len(rel.RequiredCapabilities) > 0 {
			out.RequiredCapabilities = append([]string(nil), rel.RequiredCapabilities...)
		}
		if out.ReleaseBaseURL == "" {
			out.ReleaseBaseURL = rel.ReleaseBaseURL
		}
		for k, v := range rel.Binaries {
			relURL := strings.TrimSpace(v.ReleaseURL)
			if relURL == "" && rel.ReleaseBaseURL != "" && v.Filename != "" {
				relURL = strings.TrimRight(rel.ReleaseBaseURL, "/") + "/" + v.Filename
			}
			if existing, exists := out.Binaries[k]; exists {
				if existing.ReleaseURL == "" && relURL != "" && (v.SHA256 == "" || existing.SHA256 == "" || strings.EqualFold(existing.SHA256, v.SHA256)) {
					existing.ReleaseURL = relURL
					out.Binaries[k] = existing
				}
			} else {
				v.ReleaseURL = relURL
				out.Binaries[k] = v
			}
		}
	}
	return out
}

func (s *Server) handleRunnersManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	manifest := s.buildCombinedManifest(r.Context())
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) handleRunnerBinaryDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.isRunnerBinaryDownloadAuthorized(r.Context(), r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "unauthorized runner binary download",
		})
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/runners/binary/")
	osPart, archPart, ok := strings.Cut(rest, "/")
	if !ok || osPart == "" || archPart == "" || strings.Contains(archPart, "/") {
		writeErr(w, http.StatusBadRequest, errors.New("expected /api/v1/runners/binary/{os}/{arch}"))
		return
	}
	osNorm, archNorm := normalizeTargetOSArch(osPart, archPart)
	if !isAllowedBinaryPlatform(osNorm, archNorm) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unsupported runner platform %s/%s", osPart, archPart))
		return
	}

	binPath, found := s.findExistingRunnerBinary(osNorm, archNorm)
	if !found {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no prebuilt runner binary found for %s/%s", osNorm, archNorm))
		return
	}

	shaHex, sizeBytes, err := fileSHA256AndSize(binPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	f, err := os.Open(binPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", runnerBinaryFileName(osNorm, archNorm)))
	w.Header().Set("X-Binary-SHA256", shaHex)
	w.Header().Set("X-Binary-Size", strconv.FormatInt(sizeBytes, 10))
	if r.Method == http.MethodGet && r.Header.Get("Range") == "" && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.WriteHeader(http.StatusOK)
		gw := gzipWriterPool.Get().(*gzip.Writer)
		gw.Reset(w)
		_, _ = io.Copy(gw, f)
		_ = gw.Close()
		gzipWriterPool.Put(gw)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(sizeBytes, 10))
	http.ServeContent(w, r, filepath.Base(binPath), info.ModTime(), f)
}

var (
	errRunnerNotFound   = errors.New("runner not found")
	errRunnerOffline    = errors.New("runner is offline")
	errRunnerDevSkipped = errors.New("dev runner auto-update skipped without force")
	errNoUpdateBinary   = errors.New("no runner update binary or release URL available")
)

type runnerUpdateTriggerRequest struct {
	Mode  string `json:"mode,omitempty"`
	Force *bool  `json:"force,omitempty"`
}

func (s *Server) dispatchRunnerUpdate(ctx context.Context, runnerID, mode string, force bool) (protocol.RunnerUpdatePayload, error) {
	s.mu.RLock()
	rc := s.runners[runnerID]
	s.mu.RUnlock()
	if rc == nil {
		if _, err := s.store.GetRunner(ctx, runnerID); err != nil {
			return protocol.RunnerUpdatePayload{}, errRunnerNotFound
		}
		return protocol.RunnerUpdatePayload{}, errRunnerOffline
	}

	meta := s.getRunnerRuntimeMetaCopy(runnerID)
	if meta == nil {
		rec, err := s.store.GetRunner(ctx, runnerID)
		if err != nil {
			return protocol.RunnerUpdatePayload{}, errRunnerNotFound
		}
		meta = &runnerRuntimeMeta{
			RunnerID: rec.ID,
			OS:       rec.OS,
			Arch:     rec.Arch,
			Version:  rec.Version,
			IsDev:    protocol.IsDevVersion(rec.Version),
		}
	}
	s.evaluateRunnerUpdate(ctx, meta)

	if meta.IsDev && !force {
		return protocol.RunnerUpdatePayload{}, errRunnerDevSkipped
	}
	if len(meta.DownloadURLs) == 0 {
		return protocol.RunnerUpdatePayload{}, fmt.Errorf("%w for %s/%s", errNoUpdateBinary, meta.OS, meta.Arch)
	}

	resolvedMode := strings.TrimSpace(mode)
	if resolvedMode == "" {
		if meta.Incompatible {
			resolvedMode = "immediate"
		} else {
			resolvedMode = "when_idle"
		}
	}
	if resolvedMode != "when_idle" && resolvedMode != "immediate" {
		return protocol.RunnerUpdatePayload{}, fmt.Errorf("invalid update mode %q (expected when_idle or immediate)", resolvedMode)
	}

	payload := protocol.RunnerUpdatePayload{
		RequestID:      "upd_" + uuid.NewString(),
		TargetVersion:  meta.TargetVersion,
		ExpectedSHA256: meta.TargetSHA256,
		DownloadURLs:   append([]string(nil), meta.DownloadURLs...),
		Mode:           resolvedMode,
		Force:          force,
	}
	env, err := protocol.NewEnvelope(protocol.TypeRunnerUpdate, payload)
	if err != nil {
		return protocol.RunnerUpdatePayload{}, err
	}
	env.RunnerID = runnerID
	if err := rc.sendJSON(ctx, env); err != nil {
		return protocol.RunnerUpdatePayload{}, err
	}

	s.mu.Lock()
	if s.runnerMeta == nil {
		s.runnerMeta = make(map[string]*runnerRuntimeMeta)
	}
	cur := s.runnerMeta[runnerID]
	if cur == nil {
		cur = meta
		s.runnerMeta[runnerID] = cur
	}
	cur.TargetVersion = meta.TargetVersion
	cur.TargetBuildSHA = meta.TargetBuildSHA
	cur.TargetSHA256 = meta.TargetSHA256
	cur.DownloadURLs = append([]string(nil), meta.DownloadURLs...)
	cur.UpdatePhase = "queued"
	cur.UpdateProgress = 0
	cur.UpdateMessage = ""
	cur.UpdateError = ""
	s.mu.Unlock()

	s.broadcastRunnerStatus(ctx, runnerID, "online")
	return payload, nil
}

func (s *Server) handleRunnerUpdateTrigger(w http.ResponseWriter, r *http.Request, runnerID string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req runnerUpdateTriggerRequest
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req)
	}
	// Explicit per-runner click allows updating dev runners unless caller explicitly passes force=false.
	force := true
	if req.Force != nil {
		force = *req.Force
	}
	payload, err := s.dispatchRunnerUpdate(r.Context(), runnerID, req.Mode, force)
	if err != nil {
		switch {
		case errors.Is(err, errRunnerNotFound):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, errRunnerOffline), errors.Is(err, errRunnerDevSkipped), errors.Is(err, errNoUpdateBinary):
			writeErr(w, http.StatusConflict, err)
		default:
			writeErr(w, http.StatusBadRequest, err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"runner_id":      runnerID,
		"request_id":     payload.RequestID,
		"target_version": payload.TargetVersion,
		"mode":           payload.Mode,
		"force":          payload.Force,
		"download_urls":  payload.DownloadURLs,
	})
}

func (s *Server) handleRunnersUpdateAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req runnerUpdateTriggerRequest
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req)
	}
	force := false
	if req.Force != nil {
		force = *req.Force
	}

	runners, err := s.store.ListRunners(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	type skipEntry struct {
		RunnerID string `json:"runner_id"`
		Reason   string `json:"reason"`
	}
	type triggeredEntry struct {
		RunnerID      string `json:"runner_id"`
		RequestID     string `json:"request_id"`
		TargetVersion string `json:"target_version,omitempty"`
		Mode          string `json:"mode"`
	}

	triggered := make([]triggeredEntry, 0)
	skipped := make([]skipEntry, 0)

	for i := range runners {
		rec := &runners[i]
		s.enrichRunnerRecord(r.Context(), rec)

		s.mu.RLock()
		online := s.runners[rec.ID] != nil
		s.mu.RUnlock()
		if !online {
			skipped = append(skipped, skipEntry{RunnerID: rec.ID, Reason: "offline"})
			continue
		}
		if rec.IsDev && !force {
			skipped = append(skipped, skipEntry{RunnerID: rec.ID, Reason: "dev_build"})
			continue
		}
		if rec.UpdateStatus == "up_to_date" && !force {
			skipped = append(skipped, skipEntry{RunnerID: rec.ID, Reason: "up_to_date"})
			continue
		}

		payload, dErr := s.dispatchRunnerUpdate(r.Context(), rec.ID, req.Mode, force)
		if dErr != nil {
			skipped = append(skipped, skipEntry{RunnerID: rec.ID, Reason: dErr.Error()})
			continue
		}
		triggered = append(triggered, triggeredEntry{
			RunnerID:      rec.ID,
			RequestID:     payload.RequestID,
			TargetVersion: payload.TargetVersion,
			Mode:          payload.Mode,
		})
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"triggered": triggered,
		"skipped":   skipped,
	})
}

func (s *Server) handleRunnerPermissions(w http.ResponseWriter, r *http.Request, runnerID string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil && err != io.EOF {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	action := strings.TrimSpace(body.Action)
	if action == "" {
		action = "check"
	}
	switch action {
	case "check", "open_settings", "reveal_app", "open_guide":
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unsupported permission action %q", action))
		return
	}

	reqID := "perm_" + uuid.NewString()
	env, err := protocol.NewEnvelope(protocol.TypeRunnerPermission, protocol.RunnerPermissionPayload{
		RequestID: reqID,
		Action:    action,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	env.RunnerID = runnerID
	resp, err := s.callRunnerRPC(r.Context(), runnerID, env, 10*time.Second)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	var res protocol.RunnerPermissionResultPayload
	if err := json.Unmarshal(resp.Payload, &res); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.mu.Lock()
	if cur := s.runnerMeta[runnerID]; cur != nil {
		if res.FullDiskAccess != nil {
			v := *res.FullDiskAccess
			cur.FullDiskAccess = &v
		}
		if res.AppPath != "" {
			cur.AppPath = res.AppPath
		}
	}
	s.mu.Unlock()
	s.broadcastRunnerStatus(r.Context(), runnerID, "online")

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               res.Error == "",
		"runner_id":        runnerID,
		"action":           action,
		"full_disk_access": res.FullDiskAccess,
		"app_path":         res.AppPath,
		"error":            res.Error,
	})
}
