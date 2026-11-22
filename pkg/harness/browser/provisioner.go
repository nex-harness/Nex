package browser

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	cftVersionsEndpoint = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"
	defaultCfTMirrorBase = "https://cdn.npmmirror.com/binaries/chrome-for-testing"
)

type browserCandidate struct {
	name string
	path string
}

// DiscoverBrowser locates a usable Chromium-based executable on the host or inside <dataDir>/browsers.
func DiscoverBrowser(dataDir, overridePath string) (string, string) {
	if p := strings.TrimSpace(overridePath); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, filepath.Base(p)
		}
	}

	// 1. Prefer previously runner-provisioned headless-shell inside <dataDir>/browsers
	if dataDir != "" {
		if bin, ver := findInstalledHeadlessShell(filepath.Join(dataDir, "browsers")); bin != "" {
			return bin, "Chrome Headless Shell " + ver
		}
	}

	// 2. Scan standard OS browser locations & PATH
	var candidates []browserCandidate
	switch runtime.GOOS {
	case "darwin":
		candidates = []browserCandidate{
			{"Google Chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
			{"Chromium", "/Applications/Chromium.app/Contents/MacOS/Chromium"},
			{"Microsoft Edge", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"},
			{"Brave Browser", "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"},
		}
	case "windows":
		localApp := os.Getenv("LOCALAPPDATA")
		progFiles := os.Getenv("ProgramFiles")
		progFiles86 := os.Getenv("ProgramFiles(x86)")
		for _, base := range []string{localApp, progFiles, progFiles86} {
			if base == "" {
				continue
			}
			candidates = append(candidates,
				browserCandidate{"Google Chrome", filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe")},
				browserCandidate{"Microsoft Edge", filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe")},
				browserCandidate{"Brave Browser", filepath.Join(base, "BraveSoftware", "Brave-Browser", "Application", "brave.exe")},
			)
		}
	default:
		for _, cmdName := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
			if resolved, err := exec.LookPath(cmdName); err == nil {
				candidates = append(candidates, browserCandidate{cmdName, resolved})
			}
		}
	}

	for _, c := range candidates {
		if info, err := os.Stat(c.path); err == nil && !info.IsDir() {
			return c.path, c.name
		}
	}
	return "", ""
}

func findInstalledHeadlessShell(browsersDir string) (string, string) {
	entries, err := os.ReadDir(browsersDir)
	if err != nil {
		return "", ""
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))

	targetName := "chrome-headless-shell"
	if runtime.GOOS == "windows" {
		targetName = "chrome-headless-shell.exe"
	}
	for _, ver := range dirs {
		root := filepath.Join(browsersDir, ver)
		var found string
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() {
				return nil
			}
			if d.Name() == targetName {
				found = p
				return io.EOF
			}
			return nil
		})
		if found != "" {
			return found, ver
		}
	}
	return "", ""
}

// Probe verifies browser availability, runtime dynamic libraries, or attach_cdp reachability.
func Probe(ctx context.Context, dataDir string, cfg Config) ProbeResult {
	res := ProbeResult{
		Mode: cfg.Mode,
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
	if cfg.Mode == ModeAttachCDP {
		if strings.TrimSpace(cfg.AttachURL) == "" {
			res.Status = "error"
			res.Error = "attach_url is required in attach_cdp mode"
			return res
		}
		pCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		if _, err := DiscoverAttachWS(pCtx, cfg.AttachURL); err != nil {
			res.Status = "backend_unavailable"
			res.Error = err.Error()
			return res
		}
		res.Status = "ready"
		res.BrowserName = "Remote CDP"
		res.ExecutablePath = cfg.AttachURL
		return res
	}

	execPath, name := DiscoverBrowser(dataDir, cfg.ExecutablePath)
	if execPath == "" {
		if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
			res.Status = "unsupported_arch"
			res.SuggestedCommand = suggestedLinuxInstallCmd()
			res.Error = "no Chromium binary found; Chrome for Testing does not publish linux/arm64 binaries — install Chromium via system package manager"
			return res
		}
		res.Status = "missing_binary"
		res.SuggestedCommand = suggestedLinuxInstallCmd()
		return res
	}

	res.BrowserName = name
	res.ExecutablePath = execPath

	if ver := readMacAppBundleVersion(execPath); ver != "" {
		res.Status = "ready"
		res.Version = name + " " + ver
		return res
	}

	pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(pCtx, execPath, "--version").CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		lower := strings.ToLower(trimmed + " " + err.Error())
		if strings.Contains(lower, "shared libraries") || strings.Contains(lower, ".so") {
			res.Status = "missing_deps"
			res.MissingDeps = parseMissingSharedLibs(trimmed)
			res.SuggestedCommand = suggestedLinuxDepsCmd()
			res.Error = trimmed
			return res
		}
		res.Status = "error"
		res.Error = fmt.Sprintf("%v: %s", err, trimmed)
		return res
	}
	res.Status = "ready"
	res.Version = trimmed
	return res
}

// readMacAppBundleVersion extracts CFBundleShortVersionString from macOS Info.plist
// without launching the full Chromium binary on every UI probe.
func readMacAppBundleVersion(execPath string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	const marker = ".app/Contents/MacOS/"
	idx := strings.Index(execPath, marker)
	if idx < 0 {
		return ""
	}
	plistPath := filepath.Join(execPath[:idx]+".app", "Contents", "Info.plist")
	b, err := os.ReadFile(plistPath)
	if err != nil {
		return ""
	}
	raw := string(b)
	kIdx := strings.Index(raw, "<key>CFBundleShortVersionString</key>")
	if kIdx < 0 {
		return ""
	}
	rest := raw[kIdx:]
	sStart := strings.Index(rest, "<string>")
	if sStart < 0 {
		return ""
	}
	rest = rest[sStart+len("<string>"):]
	sEnd := strings.Index(rest, "</string>")
	if sEnd < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:sEnd])
}

func parseMissingSharedLibs(output string) []string {
	var libs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		if idx := strings.Index(line, "lib"); idx >= 0 {
			rest := line[idx:]
			if sp := strings.IndexAny(rest, ": \t"); sp > 0 {
				rest = rest[:sp]
			}
			if strings.Contains(rest, ".so") && !seen[rest] {
				seen[rest] = true
				libs = append(libs, rest)
			}
		}
	}
	return libs
}

func suggestedLinuxInstallCmd() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	if _, err := exec.LookPath("apt-get"); err == nil {
		return "sudo apt-get update && sudo apt-get install -y chromium"
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		return "sudo dnf install -y chromium"
	}
	if _, err := exec.LookPath("apk"); err == nil {
		return "sudo apk add --no-cache chromium nss freetype harfbuzz ca-certificates ttf-freefont"
	}
	return "Install Chromium or Google Chrome on this host, or switch to Attach CDP mode."
}

func suggestedLinuxDepsCmd() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	if _, err := exec.LookPath("apt-get"); err == nil {
		return "sudo apt-get update && sudo apt-get install -y libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 libasound2t64 || sudo apt-get install -y chromium"
	}
	return suggestedLinuxInstallCmd()
}

type cftDownloadsResp struct {
	Channels struct {
		Stable struct {
			Version   string `json:"version"`
			Downloads struct {
				ChromeHeadlessShell []struct {
					Platform string `json:"platform"`
					URL      string `json:"url"`
				} `json:"chrome-headless-shell"`
			} `json:"downloads"`
		} `json:"Stable"`
	} `json:"channels"`
}

func cftPlatformName() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "linux64"
	case "darwin/arm64":
		return "mac-arm64"
	case "darwin/amd64":
		return "mac-x64"
	case "windows/amd64":
		return "win64"
	default:
		return ""
	}
}

// InstallHeadlessShell downloads and atomically extracts Chrome for Testing `chrome-headless-shell`
// into <dataDir>/browsers/<version> without requiring root privileges.
func InstallHeadlessShell(
	ctx context.Context,
	dataDir string,
	mirrorURL string,
	hc *http.Client,
	onProgress func(InstallProgress),
) (ProbeResult, error) {
	plat := cftPlatformName()
	if plat == "" {
		return ProbeResult{
			Status:           "unsupported_arch",
			OS:               runtime.GOOS,
			Arch:             runtime.GOARCH,
			SuggestedCommand: suggestedLinuxInstallCmd(),
		}, fmt.Errorf("chrome-headless-shell has no official build for %s/%s; use system package manager", runtime.GOOS, runtime.GOARCH)
	}
	if dataDir == "" {
		return ProbeResult{}, fmt.Errorf("dataDir is required to install chrome-headless-shell")
	}
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	emit := func(phase string, pct float64, msg string) {
		if onProgress != nil {
			onProgress(InstallProgress{Phase: phase, Percent: pct, Message: msg})
		}
	}

	emit("resolving", 5, "Resolving latest stable chrome-headless-shell version")
	versionsURL := cftVersionsEndpoint
	mirrorTrimmed := strings.TrimRight(strings.TrimSpace(mirrorURL), "/")
	if mirrorTrimmed != "" {
		versionsURL = mirrorTrimmed + "/last-known-good-versions-with-downloads.json"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionsURL, nil)
	if err != nil {
		return ProbeResult{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("fetch Chrome for Testing metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return ProbeResult{}, fmt.Errorf("metadata request returned HTTP %d", resp.StatusCode)
	}

	var parsed cftDownloadsResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return ProbeResult{}, fmt.Errorf("decode Chrome for Testing metadata: %w", err)
	}
	ver := parsed.Channels.Stable.Version
	var dlURL string
	for _, entry := range parsed.Channels.Stable.Downloads.ChromeHeadlessShell {
		if entry.Platform == plat {
			dlURL = entry.URL
			break
		}
	}
	if ver == "" || dlURL == "" {
		return ProbeResult{}, fmt.Errorf("no chrome-headless-shell download found for %s", plat)
	}
	if mirrorTrimmed != "" {
		dlURL = rewriteCfTDownloadMirror(dlURL, mirrorTrimmed)
	}

	emit("downloading", 15, fmt.Sprintf("Downloading chrome-headless-shell %s (%s)", ver, plat))
	tmpZip, err := os.CreateTemp("", "nex-cft-*.zip")
	if err != nil {
		return ProbeResult{}, err
	}
	tmpZipPath := tmpZip.Name()
	defer os.Remove(tmpZipPath)

	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		tmpZip.Close()
		return ProbeResult{}, err
	}
	dlResp, err := hc.Do(dlReq)
	if err != nil {
		tmpZip.Close()
		return ProbeResult{}, fmt.Errorf("download archive: %w", err)
	}
	if dlResp.StatusCode >= 300 {
		dlResp.Body.Close()
		tmpZip.Close()
		return ProbeResult{}, fmt.Errorf("download returned HTTP %d", dlResp.StatusCode)
	}
	if _, err := io.Copy(tmpZip, dlResp.Body); err != nil {
		dlResp.Body.Close()
		tmpZip.Close()
		return ProbeResult{}, fmt.Errorf("write archive: %w", err)
	}
	dlResp.Body.Close()
	if err := tmpZip.Close(); err != nil {
		return ProbeResult{}, err
	}

	emit("extracting", 75, "Extracting browser archive")
	browsersDir := filepath.Join(dataDir, "browsers")
	if err := os.MkdirAll(browsersDir, 0o755); err != nil {
		return ProbeResult{}, err
	}
	stageDir, err := os.MkdirTemp(browsersDir, ".stage-*")
	if err != nil {
		return ProbeResult{}, err
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			_ = os.RemoveAll(stageDir)
		}
	}()

	if err := unzipArchive(tmpZipPath, stageDir); err != nil {
		return ProbeResult{}, fmt.Errorf("extract zip: %w", err)
	}

	finalDir := filepath.Join(browsersDir, ver)
	_ = os.RemoveAll(finalDir)
	if err := os.Rename(stageDir, finalDir); err != nil {
		return ProbeResult{}, err
	}
	cleanupStage = false

	emit("verifying", 90, "Verifying installed browser")
	probe := Probe(ctx, dataDir, DefaultConfig())
	emit("done", 100, probe.Status)
	return probe, nil
}

func rewriteCfTDownloadMirror(origURL, mirrorBase string) string {
	const officialPrefix = "https://storage.googleapis.com/chrome-for-testing-public/"
	if strings.HasPrefix(origURL, officialPrefix) {
		return strings.TrimRight(mirrorBase, "/") + "/" + strings.TrimPrefix(origURL, officialPrefix)
	}
	u, err := url.Parse(origURL)
	if err != nil {
		return origURL
	}
	m, err := url.Parse(mirrorBase)
	if err != nil || m.Scheme == "" || m.Host == "" {
		return origURL
	}
	u.Scheme = m.Scheme
	u.Host = m.Host
	return u.String()
}

func unzipArchive(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	cleanDest := filepath.Clean(destDir)
	for _, f := range r.File {
		target := filepath.Join(cleanDest, f.Name)
		if !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) && target != cleanDest {
			return fmt.Errorf("illegal zip path: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if f.Mode()&os.ModeSymlink != 0 {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			linkBytes, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			_ = os.Symlink(string(linkBytes), target)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}
