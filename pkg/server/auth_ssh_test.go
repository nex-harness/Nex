package server

import (
	"bytes"
	"encoding/binary"
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

func makeMinimalELF(machine uint16) []byte {
	b := make([]byte, 64)
	copy(b[:4], "\x7fELF")
	b[4] = 2 // ELFCLASS64
	b[5] = 1 // ELFDATA2LSB
	b[6] = 1 // EV_CURRENT
	binary.LittleEndian.PutUint16(b[16:18], 2)       // ET_EXEC
	binary.LittleEndian.PutUint16(b[18:20], machine) // EM_X86_64=62, EM_AARCH64=183
	binary.LittleEndian.PutUint32(b[20:24], 1)       // EV_CURRENT
	return b
}

func TestRemoteOSArchResolutionAndBundledRunners(t *testing.T) {
	// 1. OS & Arch normalization across Linux / macOS / Windows
	cases := [][4]string{
		{"Linux", "x86_64", "linux", "amd64"},
		{"Darwin", "arm64", "darwin", "arm64"},
		{"MINGW64_NT-10.0", "AMD64", "windows", "amd64"},
		{"windows", "aarch64", "windows", "arm64"},
	}
	for _, c := range cases {
		gotOS, gotArch := normalizeTargetOSArch(c[0], c[1])
		if gotOS != c[2] || gotArch != c[3] {
			t.Fatalf("normalizeTargetOSArch(%q, %q) = (%q, %q), want (%q, %q)", c[0], c[1], gotOS, gotArch, c[2], c[3])
		}
	}

	// 2. OS-aware $HOME / default workspace resolution
	ins, ws, bin := resolveRemotePaths(store.SSHTargetRecord{Username: "root"}, remoteProbeInfo{OS: "linux", HomeDir: "/root"})
	if ins != "/root/.nex-runner" || ws != "/root" || bin != "nex-runner" {
		t.Fatalf("linux root paths = (%q, %q, %q)", ins, ws, bin)
	}
	ins, ws, bin = resolveRemotePaths(store.SSHTargetRecord{Username: "tylinux", WorkspacePath: "$HOME/code"}, remoteProbeInfo{OS: "darwin", HomeDir: "/Users/tylinux"})
	if ins != "/Users/tylinux/.nex-runner" || ws != "/Users/tylinux/code" || bin != "nex-runner" {
		t.Fatalf("darwin paths = (%q, %q, %q)", ins, ws, bin)
	}
	ins, ws, bin = resolveRemotePaths(store.SSHTargetRecord{Username: "alice", WorkspacePath: "$HOME"}, remoteProbeInfo{OS: "windows", HomeDir: `C:\Users\alice`})
	if ins != `C:\Users\alice\.nex-runner` || ws != `C:\Users\alice` || bin != "nex-runner.exe" {
		t.Fatalf("windows paths = (%q, %q, %q)", ins, ws, bin)
	}
	// Empty HomeDir falls back cleanly based on OS & username
	_, wsFallbackWin, _ := resolveRemotePaths(store.SSHTargetRecord{Username: "bob"}, remoteProbeInfo{OS: "windows"})
	if wsFallbackWin != `C:\Users\bob` {
		t.Fatalf("expected C:\\Users\\bob fallback, got %q", wsFallbackWin)
	}

	// 3. ELF header auto-detection + Config.RunnersDir bundled runner discovery
	detOS, detArch, err := detectBinaryOSArch(makeMinimalELF(183))
	if err != nil || detOS != "linux" || detArch != "arm64" {
		t.Fatalf("detectBinaryOSArch failed: os=%q arch=%q err=%v", detOS, detArch, err)
	}

	dir := t.TempDir()
	bundledDir := filepath.Join(dir, "runners")
	if err := os.MkdirAll(bundledDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundledDir, "nex-runner-linux-amd64"), makeMinimalELF(62), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundledDir, "nex-runner-windows-amd64.exe"), []byte("MZ-WIN"), 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := New(Config{Store: st, RunnersDir: bundledDir})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ssh/binaries", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/ssh/binaries failed: %d", rec.Code)
	}
	var listed []stagedBinaryInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var seenLinux, seenWin bool
	for _, b := range listed {
		if b.OS == "linux" && b.Arch == "amd64" && b.Source == "bundled" {
			seenLinux = true
		}
		if b.OS == "windows" && b.Arch == "amd64" && b.Source == "bundled" {
			seenWin = true
		}
	}
	if !seenLinux || !seenWin {
		t.Fatalf("expected bundled linux/amd64 and windows/amd64 in %+v", listed)
	}

	// Auto-detect OS/arch on POST without query params
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ssh/binaries", bytes.NewReader(makeMinimalELF(183))))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"arch":"arm64"`) {
		t.Fatalf("auto-detect binary upload failed: %d %s", rec.Code, rec.Body.String())
	}

	// 4. Deterministic runner ID, macOS .app Info.plist, LaunchAgent plist generation & runner delete endpoint
	if got := sshTargetRunnerID("macM4"); got != "run_macm4" {
		t.Fatalf("sshTargetRunnerID(macM4) = %q, want run_macm4", got)
	}
	if got := sshTargetRunnerID("Build Server"); got != "run_build_server" {
		t.Fatalf("sshTargetRunnerID(Build Server) = %q, want run_build_server", got)
	}
	infoPlist := buildDarwinAppInfoPlist()
	for _, want := range []string{
		"com.nex.runner",
		"NexRunner",
		"nex-runner",
		"LSUIElement",
		"NSLocalNetworkUsageDescription",
	} {
		if !strings.Contains(infoPlist, want) {
			t.Fatalf("app Info.plist missing %q:\n%s", want, infoPlist)
		}
	}
	plist := buildDarwinLaunchAgentPlist(
		darwinLaunchAgentLabel("run_macm4"),
		"/Users/tylinux/.nex-runner/NexRunner.app/Contents/MacOS/nex-runner",
		"run_macm4",
		"mac & M4",
		"wss://nex.tylinux.cn/ws/runner",
		"/Users/tylinux",
		"/Users/tylinux/.nex-runner/data",
		"tok123",
		"/Users/tylinux",
		"/Users/tylinux/.nex-runner/runner.log",
	)
	for _, want := range []string{
		"<string>com.nex.runner.run_macm4</string>",
		"<key>AssociatedBundleIdentifiers</key>",
		"<string>com.nex.runner</string>",
		"NexRunner.app/Contents/MacOS/nex-runner",
		"<string>-data-dir</string>",
		"<string>-token</string>",
		"<string>tok123</string>",
		"<string>mac &amp; M4</string>",
		"/opt/homebrew/bin:/usr/local/bin",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("LaunchAgent plist missing %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "UserName") {
		t.Fatalf("LaunchAgent plist should not set UserName:\n%s", plist)
	}

	if err := st.UpsertRunner(t.Context(), store.RunnerRecord{
		ID: "run_stale_tmp", Name: "stale", Status: "offline",
	}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/runners/run_stale_tmp", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/runners/run_stale_tmp expected 200, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunnerReleaseDownloadURLAndLocalDownload(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 1. Versioned server maps 0.2.0 -> v0.2.0 tag
	srv, err := New(Config{Store: st, Version: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.runnerReleaseDownloadURL("linux", "arm64")
	want := "https://github.com/nex-harness/Nex/releases/download/v0.2.0/nex-runner-linux-arm64"
	if got != want {
		t.Fatalf("runnerReleaseDownloadURL(0.2.0) = %q, want %q", got, want)
	}

	// 2. Dev server falls back to releases/latest/download
	srvDev, err := New(Config{Store: st, Version: "0.2.0-dev+abc1234"})
	if err != nil {
		t.Fatal(err)
	}
	gotDev := srvDev.runnerReleaseDownloadURL("windows", "amd64")
	wantDev := "https://github.com/nex-harness/Nex/releases/latest/download/nex-runner-windows-amd64.exe"
	if gotDev != wantDev {
		t.Fatalf("runnerReleaseDownloadURL(dev) = %q, want %q", gotDev, wantDev)
	}

	// 3. Local download via NEX_RUNNER_RELEASE_BASE_URL caches binary for SCP upload
	mockRel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nex-runner-linux-amd64" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("ELF-MOCK-RUNNER-BINARY"))
	}))
	defer mockRel.Close()
	t.Setenv("NEX_RUNNER_RELEASE_BASE_URL", mockRel.URL)

	srvDL, err := New(Config{Store: st, Version: "0.3.0", HTTPClient: mockRel.Client()})
	if err != nil {
		t.Fatal(err)
	}
	path, err := srvDL.downloadRunnerBinaryFromRelease(t.Context(), "linux", "amd64", func(string, ...any) {})
	if err != nil {
		t.Fatalf("downloadRunnerBinaryFromRelease failed: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "ELF-MOCK-RUNNER-BINARY" {
		t.Fatalf("unexpected downloaded binary content: %q err=%v", string(b), err)
	}
}
