package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nex-harness/nex/server/internal/app"
	"github.com/nex-harness/nex/server/internal/runners"
)

const (
	testAuthToken   = "browser-secret"
	testRunnerToken = "runner-secret"
)

func newTestServer(t *testing.T) (*httptest.Server, *app.App, string) {
	t.Helper()
	dataDir := t.TempDir()
	application, err := app.New(app.Options{
		DataDir:     dataDir,
		WebRoot:     filepath.Join(dataDir, "missing-web-root"),
		AuthToken:   testAuthToken,
		RunnerToken: testRunnerToken,
		ServerName:  "test-server",
		Version:     "9.9.9",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	server := httptest.NewServer(application.Handler())
	t.Cleanup(server.Close)
	return server, application, dataDir
}

// noopLink satisfies runners.Link for registry-only server-info tests.
type noopLink struct{}

func (noopLink) Call(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func (noopLink) Listen(string, string, string, json.RawMessage, func(json.RawMessage)) error {
	return nil
}

func (noopLink) Unlisten(string) {}

func (noopLink) Close() {}

func (noopLink) Done() <-chan struct{} { return nil }

func noRedirectClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func postJSON(t *testing.T, client *http.Client, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return response
}

func get(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	return response
}

func TestAuthSessionUnauthenticated(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	response := get(t, client, server.URL+"/api/auth/session")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var payload struct {
		AuthRequired  bool `json:"authRequired"`
		Authenticated bool `json:"authenticated"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.AuthRequired || payload.Authenticated {
		t.Fatalf("payload = %+v, want authRequired=true authenticated=false", payload)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestProtectedPathsRejectAnonymous(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	for _, path := range []string{"/api/server-info", "/ws", "/ws/anything", "/runner"} {
		response := get(t, client, server.URL+path)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", path, response.StatusCode)
		}
	}

	// /api/auth/* stays reachable.
	response := get(t, client, server.URL+"/api/auth/session")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d, want 200", response.StatusCode)
	}
}

func TestLoginSuccessSetsCookieAndAuthenticates(t *testing.T) {
	server, _, dataDir := newTestServer(t)
	client := noRedirectClient(t)

	response := postJSON(t, client, server.URL+"/api/auth/login", `{"token":"browser-secret"}`, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d body=%s, want 200", response.StatusCode, body)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "nex_session" || cookie.Value == "" {
		t.Fatalf("cookie = %+v", cookie)
	}
	if !cookie.HttpOnly {
		t.Fatalf("cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Fatalf("Path = %q, want /", cookie.Path)
	}
	if cookie.MaxAge != 30*24*60*60 {
		t.Fatalf("MaxAge = %d, want 2592000", cookie.MaxAge)
	}
	if cookie.Secure {
		t.Fatalf("Secure must be absent on plain HTTP")
	}

	sessionResponse := get(t, client, server.URL+"/api/auth/session")
	defer sessionResponse.Body.Close()
	var payload struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.NewDecoder(sessionResponse.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.Authenticated {
		t.Fatalf("session cookie did not authenticate")
	}

	info := get(t, client, server.URL+"/api/server-info")
	defer info.Body.Close()
	if info.StatusCode != http.StatusOK {
		t.Fatalf("server-info status = %d, want 200", info.StatusCode)
	}

	// The raw session id must never be persisted.
	raw, err := os.ReadFile(filepath.Join(dataDir, "web-sessions.json"))
	if err != nil {
		t.Fatalf("reading sessions file: %v", err)
	}
	if strings.Contains(string(raw), cookie.Value) {
		t.Fatalf("sessions file contains the raw session id")
	}
	if !strings.Contains(string(raw), "idHash") {
		t.Fatalf("sessions file does not contain hashed ids: %s", raw)
	}
}

func TestLoginSecureCookieBehindProxy(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	response := postJSON(t, client, server.URL+"/api/auth/login", `{"token":"browser-secret"}`,
		map[string]string{"X-Forwarded-Proto": "https"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("expected a Secure cookie behind https proxy, got %+v", cookies)
	}
}

func TestLoginBadTokenAndMalformedBody(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	bad := postJSON(t, client, server.URL+"/api/auth/login", `{"token":"wrong"}`, nil)
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401", bad.StatusCode)
	}

	for _, body := range []string{`not json`, `{}`, `{"token":123}`, `{"token":""}`} {
		response := postJSON(t, client, server.URL+"/api/auth/login", body, nil)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", body, response.StatusCode)
		}
	}
}

func TestLoginLockoutAfterFiveFailures(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	for attempt := 1; attempt <= 5; attempt++ {
		response := postJSON(t, client, server.URL+"/api/auth/login", `{"token":"wrong"}`, nil)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt, response.StatusCode)
		}
	}

	locked := postJSON(t, client, server.URL+"/api/auth/login", `{"token":"browser-secret"}`, nil)
	defer locked.Body.Close()
	if locked.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked status = %d, want 429", locked.StatusCode)
	}
	if locked.Header.Get("Retry-After") == "" {
		t.Fatalf("missing Retry-After header")
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	login := postJSON(t, client, server.URL+"/api/auth/login", `{"token":"browser-secret"}`, nil)
	_ = login.Body.Close()

	if info := get(t, client, server.URL+"/api/server-info"); info.StatusCode != http.StatusOK {
		t.Fatalf("pre-logout server-info = %d, want 200", info.StatusCode)
	} else {
		_ = info.Body.Close()
	}

	logout := postJSON(t, client, server.URL+"/api/auth/logout", `{}`, nil)
	defer logout.Body.Close()
	if logout.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", logout.StatusCode)
	}
	cleared := false
	for _, cookie := range logout.Cookies() {
		// Go normalizes "Max-Age=0" to MaxAge=-1 (delete now).
		if cookie.Name == "nex_session" && cookie.Value == "" && cookie.MaxAge <= 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("logout did not clear the cookie: %+v", logout.Cookies())
	}

	if info := get(t, client, server.URL+"/api/server-info"); info.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout server-info = %d, want 401", info.StatusCode)
	} else {
		_ = info.Body.Close()
	}
}

func TestQueryTokenMintsCookieOnPageRequests(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	// Page request with a valid token: 404 (no web root) but cookie minted.
	page := get(t, client, server.URL+"/?token=browser-secret")
	defer page.Body.Close()
	if page.StatusCode != http.StatusNotFound {
		t.Fatalf("page status = %d, want 404 without web root", page.StatusCode)
	}
	cookies := page.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "nex_session" {
		t.Fatalf("expected a session cookie, got %+v", cookies)
	}

	// A protected request with ?token= gets access but no session cookie.
	fresh := noRedirectClient(t)
	protected := get(t, fresh, server.URL+"/api/server-info?token=browser-secret")
	defer protected.Body.Close()
	if protected.StatusCode != http.StatusOK {
		t.Fatalf("server-info status = %d, want 200", protected.StatusCode)
	}
	if len(protected.Cookies()) != 0 {
		t.Fatalf("protected token request minted a cookie: %+v", protected.Cookies())
	}

	// A wrong token on a page request renders the SPA (404 here) without cookie.
	wrong := noRedirectClient(t)
	badPage := get(t, wrong, server.URL+"/?token=nope")
	defer badPage.Body.Close()
	if len(badPage.Cookies()) != 0 {
		t.Fatalf("wrong token minted a cookie: %+v", badPage.Cookies())
	}
}

func TestServerInfoShape(t *testing.T) {
	server, application, _ := newTestServer(t)
	client := noRedirectClient(t)

	// Two runners advertising the same path must both appear, each with its
	// own identity and runner metadata.
	application.Registry.Register(runners.Info{
		RunnerID: "runner_0123456789abcdef",
		Name:     "mac-mini",
		OS:       "darwin",
		Workspaces: []runners.Workspace{
			{Path: "/home/me/app", Label: "ignored"},
		},
	}, noopLink{})
	application.Registry.Register(runners.Info{
		RunnerID: "alpha",
		Name:     "win-box",
		OS:       "win32",
		Workspaces: []runners.Workspace{
			{Path: "/home/me/app"},
		},
	}, noopLink{})

	response := get(t, client, server.URL+"/api/server-info?token=browser-secret")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["protocolVersion"] != float64(1) {
		t.Fatalf("protocolVersion = %v, want 1", payload["protocolVersion"])
	}
	if payload["authRequired"] != true {
		t.Fatalf("authRequired = %v, want true", payload["authRequired"])
	}
	if payload["serverId"] == "" || payload["serverId"] == nil {
		t.Fatalf("missing serverId")
	}
	capabilities, ok := payload["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("missing capabilities")
	}
	if capabilities["desktopContinuous"] != true || capabilities["websocketRpc"] != true {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	workspaces, ok := payload["workspaces"].([]any)
	if !ok || len(workspaces) != 2 {
		t.Fatalf("workspaces = %+v, want two runner rows", payload["workspaces"])
	}
	first, _ := workspaces[0].(map[string]any)
	second, _ := workspaces[1].(map[string]any)
	want := []map[string]any{
		{
			"path":              "/home/me/app",
			"label":             "mac-mini: app",
			"workspaceIdentity": "runner:runner_0123456789abcdef:/home/me/app",
			"runnerId":          "runner_0123456789abcdef",
			"runnerName":        "mac-mini",
			"runnerOs":          "darwin",
		},
		{
			"path":              "/home/me/app",
			"label":             "win-box: app",
			"workspaceIdentity": "runner:alpha:/home/me/app",
			"runnerId":          "alpha",
			"runnerName":        "win-box",
			"runnerOs":          "win32",
		},
	}
	for key, value := range want[0] {
		if first[key] != value {
			t.Fatalf("workspaces[0].%s = %v, want %v", key, first[key], value)
		}
	}
	for key, value := range want[1] {
		if second[key] != value {
			t.Fatalf("workspaces[1].%s = %v, want %v", key, second[key], value)
		}
	}
}

func TestServerInfoEmptyWorkspacesIsArray(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	response := get(t, client, server.URL+"/api/server-info?token=browser-secret")
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	workspaces, ok := payload["workspaces"].([]any)
	if !ok || len(workspaces) != 0 {
		t.Fatalf("workspaces = %+v, want empty array", payload["workspaces"])
	}
}

func TestStaticServingAndSpaFallback(t *testing.T) {
	dataDir := t.TempDir()
	webRoot := filepath.Join(dataDir, "web")
	if err := os.MkdirAll(filepath.Join(webRoot, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<html>index</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	application, err := app.New(app.Options{
		DataDir:     dataDir,
		WebRoot:     webRoot,
		AuthToken:   testAuthToken,
		RunnerToken: testRunnerToken,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	server := httptest.NewServer(application.Handler())
	t.Cleanup(server.Close)
	client := noRedirectClient(t)

	index := get(t, client, server.URL+"/")
	defer index.Body.Close()
	if index.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d, want 200", index.StatusCode)
	}
	if got := index.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("index content type = %q", got)
	}
	if got := index.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", got)
	}

	asset := get(t, client, server.URL+"/assets/app.js")
	defer asset.Body.Close()
	if asset.StatusCode != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", asset.StatusCode)
	}
	if got := asset.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Fatalf("asset content type = %q", got)
	}
	if got := asset.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("asset Cache-Control = %q, want immutable", got)
	}

	spa := get(t, client, server.URL+"/some/deep/route")
	defer spa.Body.Close()
	if spa.StatusCode != http.StatusOK {
		t.Fatalf("SPA fallback status = %d, want 200", spa.StatusCode)
	}

	api := get(t, client, server.URL+"/api/does-not-exist?token=browser-secret")
	defer api.Body.Close()
	if api.StatusCode == http.StatusOK {
		t.Fatalf("unknown API path must not fall back to index.html")
	}

	traversal := get(t, client, server.URL+"/%2e%2e/%2e%2e/etc/passwd")
	defer traversal.Body.Close()
	if traversal.StatusCode == http.StatusOK {
		t.Fatalf("traversal must not serve content, got status %d", traversal.StatusCode)
	}
}

// A client-supplied X-Forwarded-Proto must not suppress Secure when the trusted
// proxy appends its own value (standard X-Forwarded-* semantics).
func TestSecureCookieUsesTrustedProxyValue(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	response := postJSON(t, client, server.URL+"/api/auth/login", `{"token":"browser-secret"}`,
		map[string]string{"X-Forwarded-Proto": "http, https"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("cookie = %+v, want Secure from the last forwarded value", cookies)
	}
}

// The login limiter must key on the TCP peer, not on a spoofable header.
func TestLoginLockoutIgnoresForwardedFor(t *testing.T) {
	server, _, _ := newTestServer(t)
	client := noRedirectClient(t)

	for attempt := 0; attempt < 5; attempt++ {
		response := postJSON(t, client, server.URL+"/api/auth/login", `{"token":"wrong"}`,
			map[string]string{"X-Forwarded-For": "203.0.113." + string(rune('1'+attempt))})
		_ = response.Body.Close()
	}
	// A different X-Forwarded-For would bypass a header-keyed limiter.
	locked := postJSON(t, client, server.URL+"/api/auth/login", `{"token":"browser-secret"}`,
		map[string]string{"X-Forwarded-For": "198.51.100.9"})
	defer locked.Body.Close()
	if locked.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (header spoofed around the limiter)", locked.StatusCode)
	}
}
