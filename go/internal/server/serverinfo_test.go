package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// clearServerInfoEnv blanks every env var createServerInfo reads, so a value left in the test
// process environment (or exported on a developer machine) cannot change a result.
func clearServerInfoEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NEX_SERVER_ID", "")
	t.Setenv("NEX_SERVER_NAME", "")
	t.Setenv("NEX_SERVER_WORKSPACE", "")
	t.Setenv("NEX_SERVER_TOKEN", "")
	t.Setenv("NEX_SERVER_AUTH_TOKEN", "")
}

// TestServerInfoShape asserts the field set and order of GET /api/server-info against
// serverRemoteInfoSchema (packages/shared/src/server-remote.ts). The comparison is textual on
// purpose: the recorded Node body is compared byte for byte elsewhere, and a reordered or
// renamed field is a wire change a decoded-and-re-encoded assertion would hide.
func TestServerInfoShape(t *testing.T) {
	clearServerInfoEnv(t)
	t.Setenv("NEX_SERVER_ID", "shape-server")

	info := NewServerInfo(&Config{ServerID: "shape-server"})
	body, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"serverId":"shape-server","version":"` + Version +
		`","protocolVersion":1,"authRequired":false,"workspaces":[],` +
		`"capabilities":{"desktopContinuous":true,"websocketRpc":true,"processResourceTelemetry":true}}`
	if string(body) != want {
		t.Errorf("server-info body:\n got %s\nwant %s", body, want)
	}
}

// TestServerInfoNameOmittedWhenEmpty pins the optionality: `name` is absent, not null, when
// neither Options.Name nor NEX_SERVER_NAME yields a non-blank value. Hono/JSON.stringify drops
// the key entirely, and a client reading `info.name === undefined` distinguishes the two.
func TestServerInfoNameOmittedWhenEmpty(t *testing.T) {
	cases := []struct {
		name     string
		envValue string
		option   string
		wantName string
		wantKey  bool
	}{
		{name: "unset env and option", envValue: "", option: "", wantName: "", wantKey: false},
		{name: "whitespace env", envValue: "   ", option: "", wantName: "", wantKey: false},
		{name: "env set", envValue: "From Env", option: "", wantName: "From Env", wantKey: true},
		{name: "whitespace option", envValue: "", option: "  ", wantName: "", wantKey: false},
		// readTrimmedEnv trims, so a padded value is reported without the padding.
		{name: "env padded", envValue: "  Padded  ", option: "", wantName: "Padded", wantKey: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			clearServerInfoEnv(t)
			t.Setenv("NEX_SERVER_NAME", testCase.envValue)

			// NewHandler is where the env read happens (options -> NEX_SERVER_NAME), so the
			// env-driven cases can only be observed through it.
			_, cfg, err := NewHandler(Options{Name: testCase.option})
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}
			info := NewServerInfo(cfg)
			if info.Name != testCase.wantName {
				t.Errorf("Name = %q, want %q", info.Name, testCase.wantName)
			}
			body, err := json.Marshal(info)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			_, present := decoded["name"]
			if present != testCase.wantKey {
				t.Errorf("name key present = %v, want %v (body %s)", present, testCase.wantKey, body)
			}
		})
	}
}

// TestServerInfoCapabilities pins the capability triple. desktopContinuous and websocketRpc
// are z.literal(true) in the schema — a false would make a client reject the response — and
// processResourceTelemetry is the optional flag older servers omit, so it must be present here
// and must not be dropped by omitempty.
func TestServerInfoCapabilities(t *testing.T) {
	clearServerInfoEnv(t)
	info := NewServerInfo(&Config{ServerID: "s"})

	if !info.Capabilities.DesktopContinuous {
		t.Error("capabilities.desktopContinuous = false, want true")
	}
	if !info.Capabilities.WebSocketRPC {
		t.Error("capabilities.websocketRpc = false, want true")
	}
	if !info.Capabilities.ProcessResourceTelemetry {
		t.Error("capabilities.processResourceTelemetry = false, want true")
	}
	if info.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion = %d, want %d", info.ProtocolVersion, ProtocolVersion)
	}
	if info.Version != Version {
		t.Errorf("version = %q, want %q", info.Version, Version)
	}
}

// TestServerInfoServerIDResolution pins the fallback chain of resolveServerId:
// options -> NEX_SERVER_ID -> hostname -> "nex-server". Only the first two are settable in a
// test; the hostname fallback is covered by TestHttpHandlerFallsBackToHostname.
func TestServerInfoServerIDResolution(t *testing.T) {
	clearServerInfoEnv(t)
	t.Setenv("NEX_SERVER_ID", "  env-id  ")

	if got := NewServerInfo(&Config{ServerID: "opt-id"}).ServerID; got != "opt-id" {
		t.Errorf("option wins: ServerID = %q, want %q", got, "opt-id")
	}
	// A blank option falls through to the env, and readTrimmedEnv trims.
	handler, cfg, err := NewHandler(Options{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if cfg.ServerID != "env-id" {
		t.Errorf("env wins over hostname: ServerID = %q, want %q", cfg.ServerID, "env-id")
	}
	if got := NewServerInfo(cfg).ServerID; got != "env-id" {
		t.Errorf("NewServerInfo ServerID = %q, want %q", got, "env-id")
	}
	_ = handler
}

// TestServerInfoWorkspaces pins resolveServerWorkspaces: no implicit default (an unset
// NEX_SERVER_WORKSPACE yields an empty array, not "/"), and label = basename with the path
// itself as the fallback when the basename is empty.
func TestServerInfoWorkspaces(t *testing.T) {
	cases := []struct {
		name      string
		workspace string
		wantLen   int
		wantPath  string
		wantLabel string
	}{
		{name: "unset yields empty list", workspace: "", wantLen: 0},
		{name: "whitespace yields empty list", workspace: "   ", wantLen: 0},
		{name: "plain path", workspace: "/srv/demo", wantLen: 1, wantPath: "/srv/demo", wantLabel: "demo"},
		{name: "trailing slash keeps path as label", workspace: "/srv/demo/", wantLen: 1, wantPath: "/srv/demo/", wantLabel: "demo"},
		{name: "root path is its own label", workspace: "/", wantLen: 1, wantPath: "/", wantLabel: "/"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			clearServerInfoEnv(t)
			info := NewServerInfo(&Config{ServerID: "s", Workspace: testCase.workspace})
			if len(info.Workspaces) != testCase.wantLen {
				t.Fatalf("workspaces = %v, want %d entries", info.Workspaces, testCase.wantLen)
			}
			if testCase.wantLen == 0 {
				return
			}
			got := info.Workspaces[0]
			if got.Path != testCase.wantPath {
				t.Errorf("path = %q, want %q", got.Path, testCase.wantPath)
			}
			if got.Label != testCase.wantLabel {
				t.Errorf("label = %q, want %q", got.Label, testCase.wantLabel)
			}
		})
	}
}

// TestServerInfoAuthRequired covers the authRequired derivation and, in particular, the
// NEX_SERVER_TOKEN / NEX_SERVER_AUTH_TOKEN inconsistency that this port reproduces on purpose.
//
// In TypeScript:
//   - entry-http.ts reads NEX_SERVER_AUTH_TOKEN and passes authRequired: true when it is set;
//   - createServerInfo reads NEX_SERVER_TOKEN as the fallback when options.authRequired is unset.
//
// The two variables are different names, so each one alone flips exactly one of the two
// behaviors and the response can disagree with the server's real auth state. Fixing that here
// would change observable behavior for deployed servers, so it is recorded rather than fixed;
// see docs/specs/go-rewrite/decisions.md.
func TestServerInfoAuthRequired(t *testing.T) {
	cases := []struct {
		name           string
		optionAuth     bool
		authTokenEnv   string // NEX_SERVER_AUTH_TOKEN: entry-http.ts's variable
		serverTokenEnv string // NEX_SERVER_TOKEN: createServerInfo's fallback
		want           bool
	}{
		{name: "no token at all", want: false},
		{
			name:           "option wins over env",
			optionAuth:     true,
			serverTokenEnv: "",
			want:           true,
		},
		{
			name: "AUTH_TOKEN alone reports false",
			// entry-http.ts would set authRequired: true here, so the reported value
			// disagrees with the deployed auth state. Reproduced, not corrected.
			authTokenEnv: "secret",
			want:         false,
		},
		{
			name:           "TOKEN alone reports true",
			serverTokenEnv: "secret",
			want:           true,
		},
		{
			name:           "whitespace TOKEN counts as unset",
			serverTokenEnv: "   ",
			want:           false,
		},
		{
			name:           "option false with TOKEN set still reports true",
			optionAuth:     false,
			serverTokenEnv: "secret",
			want:           true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			clearServerInfoEnv(t)
			t.Setenv("NEX_SERVER_AUTH_TOKEN", testCase.authTokenEnv)
			t.Setenv("NEX_SERVER_TOKEN", testCase.serverTokenEnv)

			info := NewServerInfo(&Config{ServerID: "s", AuthRequired: testCase.optionAuth})
			if info.AuthRequired != testCase.want {
				t.Errorf("authRequired = %v, want %v", info.AuthRequired, testCase.want)
			}
		})
	}
}

// TestServerInfoAuthTokenEnvDoesNotAffectResponse states the inconsistency from the other
// side: NEX_SERVER_AUTH_TOKEN — the variable entry-http.ts actually reads — never reaches
// server-info. It is the reason Options carries AuthRequired separately from AuthToken.
func TestServerInfoAuthTokenEnvDoesNotAffectResponse(t *testing.T) {
	clearServerInfoEnv(t)
	t.Setenv("NEX_SERVER_AUTH_TOKEN", "deployed-token")

	if got := NewServerInfo(&Config{ServerID: "s"}).AuthRequired; got {
		t.Errorf("authRequired = true with only NEX_SERVER_AUTH_TOKEN set; the port must " +
			"reproduce the TypeScript fallback (NEX_SERVER_TOKEN) and report false")
	}
}

// TestServerInfoServedOverHTTP checks the handler-level contract for the endpoint: status 200,
// application/json, and no cache headers (Hono's c.json sets none, and the UI refetches on
// reconnect, so an accidental long-lived cache would be a real defect).
func TestServerInfoServedOverHTTP(t *testing.T) {
	clearServerInfoEnv(t)
	t.Setenv("NEX_SERVER_ID", "http-check")

	handler, _, err := NewHandler(Options{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, "/api/server-info", nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", method, recorder.Code)
		}
		if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
			t.Errorf("%s: Content-Type = %q, want %q", method, got, want)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "" {
			t.Errorf("%s: Cache-Control = %q, want none", method, got)
		}
	}

	// A trailing slash is a different path in Hono and 404s; it must not be normalized into
	// the endpoint.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/server-info/", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("/api/server-info/ status = %d, want 404", recorder.Code)
	}

	// POST is not registered either.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/server-info", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("POST /api/server-info status = %d, want 404", recorder.Code)
	}
}

// TestHttpHandlerFallsBackToHostname covers the last two steps of resolveServerId's chain: with no
// option and no env, the id is the hostname, or "nex-server" when the OS reports none.
func TestHttpHandlerFallsBackToHostname(t *testing.T) {
	clearServerInfoEnv(t)

	_, cfg, err := NewHandler(Options{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	want := hostname()
	if want == "" {
		want = "nex-server"
	}
	if cfg.ServerID != want {
		t.Errorf("ServerID = %q, want %q", cfg.ServerID, want)
	}
}

// TestHttpPortFromEnv pins entry-http.ts's `Number(process.env["PORT"]) || 3030`: a missing,
// non-numeric or zero value falls back to 3030.
func TestHttpPortFromEnv(t *testing.T) {
	cases := map[string]int{
		"":       DefaultPort,
		"   ":    DefaultPort,
		"0":      DefaultPort,
		"abc":    DefaultPort,
		"8080":   8080,
		" 3031 ": 3031,
	}
	for value, want := range cases {
		t.Setenv("PORT", value)
		if got := PortFromEnv(); got != want {
			t.Errorf("PORT=%q: got %d, want %d", value, got, want)
		}
	}
	if DefaultPort != 3030 {
		t.Errorf("DefaultPort = %d, want 3030", DefaultPort)
	}
}
