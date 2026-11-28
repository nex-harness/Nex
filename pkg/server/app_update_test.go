package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nex-harness/nex/pkg/store"
)

func TestAppUpdateCheckEndpoint(t *testing.T) {
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     "v0.4.0",
			"html_url":     "https://github.com/nex-harness/Nex/releases/tag/v0.4.0",
			"published_at": "2026-11-25T10:00:00Z",
			"body":         "## Highlights\n- Faster updates",
			"assets": []map[string]any{
				{"name": "nex-runner-v0.4.0-windows-amd64.exe", "browser_download_url": "https://example.com/nex-runner.exe", "size": 99999},
				{"name": "Nex-v0.4.0-darwin-arm64.dmg", "browser_download_url": "https://example.com/Nex-v0.4.0.dmg", "size": 12345},
				{"name": "Nex-v0.4.0-windows-amd64-setup.exe", "browser_download_url": "https://example.com/Nex-v0.4.0-setup.exe", "size": 23456},
				{"name": "Nex-v0.4.0-linux-amd64.AppImage", "browser_download_url": "https://example.com/Nex-v0.4.0.AppImage", "size": 34567},
			},
		})
	}))
	defer gh.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{
		Store:            st,
		Version:          "v0.3.0",
		AppReleaseAPIURL: gh.URL,
		HTTPClient:       gh.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. First request fetches upstream and matches darwin .dmg asset
	resp, err := http.Get(ts.URL + "/api/v1/updates/check?client_version=v0.3.0&client_platform=darwin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out AppUpdateCheckResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.LatestVersion != "v0.4.0" {
		t.Fatalf("expected latest v0.4.0, got %q", out.LatestVersion)
	}
	if !out.ServerHasUpdate || !out.ClientHasUpdate {
		t.Fatalf("expected both server and client to have updates: %+v", out)
	}
	if out.ClientAssetName != "Nex-v0.4.0-darwin-arm64.dmg" || out.ClientDownloadURL != "https://example.com/Nex-v0.4.0.dmg" {
		t.Fatalf("unexpected darwin asset match: %+v", out)
	}
	if out.Cached {
		t.Fatalf("expected first request not cached")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", hits.Load())
	}

	// 2. Second request without force uses cache; up-to-date client_version reports ClientHasUpdate=false
	resp2, err := http.Get(ts.URL + "/api/v1/updates/check?client_version=v0.4.0&client_platform=win32")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out2 AppUpdateCheckResult
	if err := json.NewDecoder(resp2.Body).Decode(&out2); err != nil {
		t.Fatal(err)
	}
	if !out2.Cached || hits.Load() != 1 {
		t.Fatalf("expected cached response with 1 hit, got cached=%v hits=%d", out2.Cached, hits.Load())
	}
	if out2.ClientHasUpdate {
		t.Fatalf("expected client v0.4.0 to be up to date")
	}
	if out2.ClientAssetName != "Nex-v0.4.0-windows-amd64-setup.exe" {
		t.Fatalf("unexpected win32 asset match: %q", out2.ClientAssetName)
	}

	// 3. Force refresh bypasses cache
	resp3, err := http.Get(ts.URL + "/api/v1/updates/check?force=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	var out3 AppUpdateCheckResult
	if err := json.NewDecoder(resp3.Body).Decode(&out3); err != nil {
		t.Fatal(err)
	}
	if out3.Cached || hits.Load() != 2 {
		t.Fatalf("expected force refresh to hit upstream (hits=%d, cached=%v)", hits.Load(), out3.Cached)
	}
}
