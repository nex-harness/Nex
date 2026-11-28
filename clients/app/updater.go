package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

// UpdateDownloadProgress reports download & ready status for desktop/mobile app updates.
type UpdateDownloadProgress struct {
	Version         string  `json:"version"`
	AssetName       string  `json:"asset_name"`
	DownloadedBytes int64   `json:"downloaded_bytes"`
	TotalBytes      int64   `json:"total_bytes"`
	Percent         float64 `json:"percent"`
	Status          string  `json:"status"` // "downloading" | "ready" | "error"
	LocalPath       string  `json:"local_path,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// EventUpdateProgress broadcasts live update download progress to the webview UI.
var EventUpdateProgress = mygo.NewEvent[UpdateDownloadProgress]("nex:update-progress")

func updateDownloadsRoot() string {
	return filepath.Join(os.TempDir(), "nex-updates")
}

func sanitizePathSegment(raw, fallback string) string {
	clean := strings.TrimSpace(raw)
	clean = filepath.Base(clean)
	if clean == "" || clean == "." || clean == ".." {
		return fallback
	}
	clean = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, clean)
	if clean == "" || clean == "." || clean == ".." {
		return fallback
	}
	return clean
}

// DownloadUpdate downloads a release installer/asset into os.TempDir()/nex-updates/<version>/<assetName>,
// emitting EventUpdateProgress updates and returning immediately if already cached on disk.
func (h *HostService) DownloadUpdate(downloadURL, assetName, version string) (UpdateDownloadProgress, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(downloadURL))
	if err != nil || (parsedURL.Scheme != "https" && parsedURL.Scheme != "http") || parsedURL.Host == "" {
		return UpdateDownloadProgress{Status: "error", Error: "invalid download URL"}, fmt.Errorf("invalid download URL")
	}

	safeVer := sanitizePathSegment(version, "latest")
	if strings.TrimSpace(assetName) == "" {
		assetName = filepath.Base(parsedURL.Path)
	}
	safeName := sanitizePathSegment(assetName, "nex-update")

	targetDir := filepath.Join(updateDownloadsRoot(), safeVer)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return UpdateDownloadProgress{Status: "error", Error: err.Error()}, err
	}
	targetPath := filepath.Join(targetDir, safeName)

	// Re-use already downloaded non-empty artifact cleanly.
	if info, statErr := os.Stat(targetPath); statErr == nil && !info.IsDir() && info.Size() > 0 {
		ready := UpdateDownloadProgress{
			Version:         safeVer,
			AssetName:       safeName,
			DownloadedBytes: info.Size(),
			TotalBytes:      info.Size(),
			Percent:         100,
			Status:          "ready",
			LocalPath:       targetPath,
		}
		_ = EventUpdateProgress.Broadcast(ready)
		return ready, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return UpdateDownloadProgress{Status: "error", Error: err.Error()}, err
	}
	verStr := strings.TrimSpace(mygo.App.Version())
	if verStr == "" {
		verStr = "0.4.0"
	}
	req.Header.Set("User-Agent", "Nex-App/"+verStr)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return UpdateDownloadProgress{Status: "error", Error: err.Error()}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		dlErr := fmt.Errorf("download returned HTTP %d", resp.StatusCode)
		return UpdateDownloadProgress{Status: "error", Error: dlErr.Error()}, dlErr
	}

	tmpPath := targetPath + fmt.Sprintf(".part-%d", time.Now().UnixNano())
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return UpdateDownloadProgress{Status: "error", Error: err.Error()}, err
	}

	totalBytes := resp.ContentLength
	var downloaded int64
	lastEmit := time.Time{}
	buf := make([]byte, 32*1024)

	emit := func(status string) UpdateDownloadProgress {
		pct := 0.0
		if totalBytes > 0 {
			pct = float64(downloaded) * 100 / float64(totalBytes)
			if pct > 100 {
				pct = 100
			}
		}
		p := UpdateDownloadProgress{
			Version:         safeVer,
			AssetName:       safeName,
			DownloadedBytes: downloaded,
			TotalBytes:      totalBytes,
			Percent:         pct,
			Status:          status,
		}
		_ = EventUpdateProgress.Broadcast(p)
		return p
	}

	emit("downloading")
	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := f.Write(buf[:n]); wErr != nil {
				_ = f.Close()
				_ = os.Remove(tmpPath)
				return UpdateDownloadProgress{Status: "error", Error: wErr.Error()}, wErr
			}
			downloaded += int64(n)
			if time.Since(lastEmit) >= 180*time.Millisecond {
				lastEmit = time.Now()
				emit("downloading")
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return UpdateDownloadProgress{Status: "error", Error: rErr.Error()}, rErr
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return UpdateDownloadProgress{Status: "error", Error: err.Error()}, err
	}
	if downloaded <= 0 {
		_ = os.Remove(tmpPath)
		emptyErr := fmt.Errorf("downloaded update file is empty")
		return UpdateDownloadProgress{Status: "error", Error: emptyErr.Error()}, emptyErr
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return UpdateDownloadProgress{Status: "error", Error: err.Error()}, err
	}

	ready := UpdateDownloadProgress{
		Version:         safeVer,
		AssetName:       safeName,
		DownloadedBytes: downloaded,
		TotalBytes:      downloaded,
		Percent:         100,
		Status:          "ready",
		LocalPath:       targetPath,
	}
	_ = EventUpdateProgress.Broadcast(ready)
	return ready, nil
}

// InstallUpdate launches/opens a downloaded update installer located inside the managed nex-updates folder.
func (h *HostService) InstallUpdate(localPath string) error {
	cleanPath := filepath.Clean(strings.TrimSpace(localPath))
	root := filepath.Clean(updateDownloadsRoot())
	rel, err := filepath.Rel(root, cleanPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("update path is outside managed directory")
	}
	info, err := os.Stat(cleanPath)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return fmt.Errorf("update installer file is missing or invalid")
	}

	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", cleanPath).Start()
	case "windows":
		return exec.Command("cmd", "/C", "start", "", cleanPath).Start()
	case "linux":
		return exec.Command("xdg-open", cleanPath).Start()
	default:
		return fmt.Errorf("automatic installer launch is not supported on %s", runtime.GOOS)
	}
}
