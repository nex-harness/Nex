package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadUpdateAndInstallValidation(t *testing.T) {
	payload := bytes.Repeat([]byte("NEX-UPDATE-PKG-"), 512)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	h := &HostService{}
	ver := "v0.4.99-test"
	defer os.RemoveAll(filepath.Join(updateDownloadsRoot(), ver))

	res, err := h.DownloadUpdate(srv.URL+"/Nex-v0.4.99-test.dmg", "../Nex-v0.4.99-test.dmg", ver)
	if err != nil {
		t.Fatalf("DownloadUpdate failed: %v", err)
	}
	if res.Status != "ready" || res.Percent != 100 || res.DownloadedBytes != int64(len(payload)) {
		t.Fatalf("unexpected download result: %+v", res)
	}
	if filepath.Dir(res.LocalPath) != filepath.Join(updateDownloadsRoot(), ver) {
		t.Fatalf("unexpected sanitized download location: %s", res.LocalPath)
	}

	// Second call re-uses cached file without error even if URL changes path
	cached, err := h.DownloadUpdate(srv.URL+"/Nex-v0.4.99-test.dmg", "Nex-v0.4.99-test.dmg", ver)
	if err != nil || cached.Status != "ready" || cached.LocalPath != res.LocalPath {
		t.Fatalf("expected cached update reuse, got %+v err=%v", cached, err)
	}

	// Path traversal / external path must be rejected by InstallUpdate
	if err := h.InstallUpdate("/etc/passwd"); err == nil {
		t.Fatalf("expected InstallUpdate to reject path outside managed directory")
	}
}
