package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nex-harness/nex/pkg/store"
)

func TestWebAuthLifecycleAndMiddleware(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	// 1. Initially disabled -> protected endpoints accessible
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("expected auth disabled initially, got %d %s", rec.Code, rec.Body.String())
	}

	// 2. Enable password
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/auth/password", strings.NewReader(`{"new_password":"secret123"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set password failed: %d %s", rec.Code, rec.Body.String())
	}
	var setResp struct {
		Enabled bool   `json:"enabled"`
		Token   string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &setResp)
	if !setResp.Enabled || setResp.Token == "" {
		t.Fatalf("expected enabled + token, got %+v", setResp)
	}

	// 3. Unauthenticated call to /api/v1/sessions -> 401
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/v1/sessions, got %d", rec.Code)
	}

	// 4. Health & auth/status remain public
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected /api/v1/health public, got %d", rec.Code)
	}

	// 5. Wrong password -> 401; right password -> 200 + cookie + token
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"password":"wrong"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on wrong password, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"password":"secret123"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid login, got %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != webAuthCookie {
		t.Fatalf("expected %s cookie on login", webAuthCookie)
	}

	// 6. Authenticated with cookie -> 200
	rec = httptest.NewRecorder()
	authedReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	authedReq.AddCookie(cookies[0])
	h.ServeHTTP(rec, authedReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with auth cookie, got %d", rec.Code)
	}

	// 7. Disable password (requires current_password)
	rec = httptest.NewRecorder()
	disableReq := httptest.NewRequest(http.MethodPut, "/api/v1/auth/password", strings.NewReader(`{"current_password":"secret123","new_password":""}`))
	disableReq.AddCookie(cookies[0])
	h.ServeHTTP(rec, disableReq)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("expected password disable success, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestSSHConfigParserTargetsAndBinaryStaging(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	cfgContent := `
# Example SSH config
Host *
    ServerAliveInterval 60

Host remotebuilder
    HostName 192.168.123.42
    User tylinux
    Port 2222
    IdentityFile ~/.ssh/id_ed25519

Host gpu-box
    HostName 10.0.0.99
    User root
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts := parseSSHConfigFile(cfgPath, 0)
	if len(hosts) != 2 {
		t.Fatalf("expected 2 concrete SSH hosts (wildcard skipped), got %+v", hosts)
	}
	if hosts[0].Alias != "remotebuilder" || hosts[0].HostName != "192.168.123.42" || hosts[0].User != "tylinux" || hosts[0].Port != 2222 {
		t.Fatalf("unexpected first host: %+v", hosts[0])
	}
	if hosts[1].Alias != "gpu-box" || hosts[1].Port != 22 {
		t.Fatalf("unexpected second host: %+v", hosts[1])
	}

	// Test SSH targets CRUD + password redaction + binary staging
	st, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ssh/targets", strings.NewReader(`{
		"name":"Remote Builder",
		"ssh_alias":"remotebuilder",
		"host":"192.168.123.42",
		"port":22,
		"username":"tylinux",
		"auth_method":"password",
		"password":"top-secret-password",
		"install_mode":"scp"
	}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create ssh target failed: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "top-secret-password") || !strings.Contains(rec.Body.String(), `"has_password":true`) {
		t.Fatalf("expected redacted password with has_password=true, got %s", rec.Body.String())
	}

	// Upload staged binary and verify one-time download token
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ssh/binaries?os=linux&arch=amd64", bytes.NewReader([]byte("ELF-MOCK-BINARY"))))
	if rec.Code != http.StatusOK {
		t.Fatalf("binary upload failed: %d %s", rec.Code, rec.Body.String())
	}

	tok := issueBinaryDownloadToken("linux", "amd64")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ssh/binary?os=linux&arch=amd64&dl_token="+tok, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ELF-MOCK-BINARY" {
		t.Fatalf("binary download failed: %d %q", rec.Code, rec.Body.String())
	}
}
