package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nex-harness/nex/pkg/store"
)

func TestRunnerInstallScripts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "nex.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	srv, err := New(Config{
		Store:         st,
		PairingSecret: "secret-win-123",
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	// Missing token -> 401
	reqUnauth := httptest.NewRequest(http.MethodGet, "https://nex.example.com/api/v1/runners/install.ps1", nil)
	recUnauth := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", recUnauth.Code)
	}

	// Valid token -> PowerShell script
	reqPS := httptest.NewRequest(http.MethodGet, "https://nex.example.com/api/v1/runners/install.ps1?token=secret-win-123", nil)
	reqPS.Header.Set("X-Forwarded-Proto", "https")
	recPS := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recPS, reqPS)
	if recPS.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recPS.Code, recPS.Body.String())
	}
	bodyPS := recPS.Body.String()
	if !strings.Contains(bodyPS, "wss://nex.example.com/ws/runner") ||
		!strings.Contains(bodyPS, "/api/v1/runners/binary/windows/amd64?token=secret-win-123") ||
		!strings.Contains(bodyPS, "Register-ScheduledTask") {
		t.Fatalf("unexpected ps1 script:\n%s", bodyPS)
	}

	// Valid token -> POSIX shell script
	reqSH := httptest.NewRequest(http.MethodGet, "https://nex.example.com/api/v1/runners/install.sh?token=secret-win-123", nil)
	reqSH.Header.Set("X-Forwarded-Proto", "https")
	recSH := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recSH, reqSH)
	if recSH.Code != http.StatusOK {
		t.Fatalf("expected 200 for install.sh, got %d", recSH.Code)
	}
	if !strings.Contains(recSH.Body.String(), "wss://nex.example.com/ws/runner") {
		t.Fatalf("unexpected sh script:\n%s", recSH.Body.String())
	}
}
