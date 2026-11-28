package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/pkg/protocol"
)

const (
	defaultGitHubReleaseAPIURL = "https://api.github.com/repos/nex-harness/Nex/releases/latest"
	defaultGitHubReleasesPage  = "https://github.com/nex-harness/Nex/releases"
	appUpdateCacheTTL          = 10 * time.Minute
)

// ReleaseAssetInfo describes a downloadable release asset.
type ReleaseAssetInfo struct {
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
	Size        int64  `json:"size,omitempty"`
}

// AppUpdateCheckResult is returned by GET /api/v1/updates/check.
type AppUpdateCheckResult struct {
	CheckedAt          int64              `json:"checked_at"`
	ReleaseURL         string             `json:"release_url"`
	LatestVersion      string             `json:"latest_version"`
	PublishedAt        string             `json:"published_at,omitempty"`
	ReleaseNotes       string             `json:"release_notes,omitempty"`
	ServerVersion      string             `json:"server_version"`
	ServerIsDev        bool               `json:"server_is_dev"`
	ServerHasUpdate    bool               `json:"server_has_update"`
	ClientVersion      string             `json:"client_version,omitempty"`
	ClientPlatform     string             `json:"client_platform,omitempty"`
	ClientIsDev        bool               `json:"client_is_dev,omitempty"`
	ClientHasUpdate    bool               `json:"client_has_update,omitempty"`
	ClientDownloadURL  string             `json:"client_download_url,omitempty"`
	ClientAssetName    string             `json:"client_asset_name,omitempty"`
	Assets             []ReleaseAssetInfo `json:"assets,omitempty"`
	Cached             bool               `json:"cached"`
	Error              string             `json:"error,omitempty"`
}

type githubLatestRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

type appUpdateCache struct {
	mu        sync.Mutex
	fetchedAt time.Time
	release   *githubLatestRelease
}

func (s *Server) effectiveAppReleaseAPIURL() string {
	if u := strings.TrimSpace(s.cfg.AppReleaseAPIURL); u != "" {
		return u
	}
	if u := strings.TrimSpace(os.Getenv("NEX_UPDATE_CHECK_URL")); u != "" {
		return u
	}
	return defaultGitHubReleaseAPIURL
}

// compareSemver compares two semver strings (e.g. "v0.3.0" vs "0.2.1").
// Returns -1 if a < b, 0 if a == b, 1 if a > b, and ok=false if either cannot be parsed.
func compareSemver(a, b string) (cmp int, ok bool) {
	pa, okA := parseSemverTuple(a)
	pb, okB := parseSemverTuple(b)
	if !okA || !okB {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1, true
		}
		if pa[i] > pb[i] {
			return 1, true
		}
	}
	return 0, true
}

func parseSemverTuple(raw string) ([3]int, bool) {
	var out [3]int
	v := strings.TrimSpace(raw)
	v = strings.TrimPrefix(v, "v")
	if v == "" || protocol.IsDevVersion(v) {
		return out, false
	}
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i := 0; i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func versionHasUpdate(current, latest string) bool {
	cur := strings.TrimSpace(current)
	lat := strings.TrimSpace(latest)
	if lat == "" || protocol.IsDevVersion(lat) {
		return false
	}
	if cur == "" || protocol.IsDevVersion(cur) {
		return false
	}
	if cmp, ok := compareSemver(cur, lat); ok {
		return cmp < 0
	}
	return strings.TrimPrefix(cur, "v") != strings.TrimPrefix(lat, "v")
}

func pickDesktopAsset(assets []ReleaseAssetInfo, platform string) *ReleaseAssetInfo {
	p := strings.ToLower(strings.TrimSpace(platform))
	if p == "" || len(assets) == 0 {
		return nil
	}
	matchExts := func(exts ...string) *ReleaseAssetInfo {
		for i := range assets {
			lower := strings.ToLower(assets[i].Name)
			if strings.HasPrefix(lower, "nex-runner-") || strings.HasPrefix(lower, "nex-relay-") || strings.HasPrefix(lower, "nex-server-") {
				continue
			}
			for _, ext := range exts {
				if strings.HasSuffix(lower, ext) {
					return &assets[i]
				}
			}
		}
		return nil
	}
	switch {
	case p == "darwin" || p == "macos" || p == "mac":
		if a := matchExts(".dmg"); a != nil {
			return a
		}
	case p == "win32" || p == "windows" || p == "win":
		if a := matchExts("-setup.exe", ".msi", ".exe"); a != nil {
			return a
		}
	case p == "linux":
		if a := matchExts(".appimage", ".deb", ".rpm"); a != nil {
			return a
		}
	case p == "android":
		if a := matchExts(".apk"); a != nil {
			return a
		}
	}
	return nil
}

func (s *Server) fetchLatestAppRelease(ctx context.Context, force bool) (*githubLatestRelease, bool, error) {
	s.appUpdateCache.mu.Lock()
	if !force && s.appUpdateCache.release != nil && time.Since(s.appUpdateCache.fetchedAt) < appUpdateCacheTTL {
		rel := s.appUpdateCache.release
		s.appUpdateCache.mu.Unlock()
		return rel, true, nil
	}
	s.appUpdateCache.mu.Unlock()

	apiURL := s.effectiveAppReleaseAPIURL()
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Nex-Server/"+strings.TrimSpace(s.cfg.Version))

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, false, fmt.Errorf("release check returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rel githubLatestRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, false, fmt.Errorf("decode release metadata: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, false, fmt.Errorf("release metadata missing tag_name")
	}

	s.appUpdateCache.mu.Lock()
	s.appUpdateCache.release = &rel
	s.appUpdateCache.fetchedAt = time.Now()
	s.appUpdateCache.mu.Unlock()

	return &rel, false, nil
}

// handleUpdatesCheck handles GET /api/v1/updates/check?force=1&client_version=...&client_platform=...
func (s *Server) handleUpdatesCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	force := q.Get("force") == "1" || strings.EqualFold(q.Get("force"), "true")
	clientVer := strings.TrimSpace(q.Get("client_version"))
	clientPlatform := strings.TrimSpace(q.Get("client_platform"))

	serverVer := strings.TrimSpace(s.cfg.Version)
	if serverVer == "" {
		serverVer = "dev"
	}

	res := AppUpdateCheckResult{
		CheckedAt:      time.Now().UnixMilli(),
		ReleaseURL:     defaultGitHubReleasesPage,
		ServerVersion:  serverVer,
		ServerIsDev:    protocol.IsDevVersion(serverVer),
		ClientVersion:  clientVer,
		ClientPlatform: clientPlatform,
		ClientIsDev:    clientVer != "" && protocol.IsDevVersion(clientVer),
	}

	rel, cached, err := s.fetchLatestAppRelease(r.Context(), force)
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}

	latest := strings.TrimSpace(rel.TagName)
	res.LatestVersion = latest
	res.PublishedAt = strings.TrimSpace(rel.PublishedAt)
	res.ReleaseNotes = strings.TrimSpace(rel.Body)
	if strings.TrimSpace(rel.HTMLURL) != "" {
		res.ReleaseURL = strings.TrimSpace(rel.HTMLURL)
	}
	res.Cached = cached
	res.ServerHasUpdate = versionHasUpdate(serverVer, latest)

	assets := make([]ReleaseAssetInfo, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.BrowserDownloadURL) == "" {
			continue
		}
		assets = append(assets, ReleaseAssetInfo{
			Name:        a.Name,
			DownloadURL: a.BrowserDownloadURL,
			Size:        a.Size,
		})
	}
	res.Assets = assets

	if clientVer != "" {
		res.ClientHasUpdate = versionHasUpdate(clientVer, latest)
	}
	if picked := pickDesktopAsset(assets, clientPlatform); picked != nil {
		res.ClientDownloadURL = picked.DownloadURL
		res.ClientAssetName = picked.Name
	}

	writeJSON(w, http.StatusOK, res)
}
