package gateway

// Router tests: the routing decision itself, and the validation of the file that configures it.
//
// The decision table is small on purpose — everything is Node except what the file or the Go
// channel set says — so these cases are about the EDGES: the default, an explicit entry, an
// inherited default, and a structurally Go-owned HTTP route that no file can move.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRoutes writes a routing table into a temp dir and returns its path.
func writeRoutes(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), RoutesFileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	return path
}

// TestRouterDefaultIsEverythingToNode is the strangler's starting state, and the state that must
// survive a deleted gateway-routes.json: with no file, every channel goes upstream.
func TestRouterDefaultIsEverythingToNode(t *testing.T) {
	router, err := LoadRouter(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing table must not be an error: %v", err)
	}
	for _, name := range []string{"file", "git", "nex-agent", "window-controller", "something-new"} {
		if got := router.Channel(name); got != RouteNode {
			t.Errorf("Channel(%q) = %q, want %q with no table", name, got, RouteNode)
		}
	}
}

// TestRouterChannelDecisions is the per-channel table: explicit entries win, unlisted channels
// take the table's default, and a "go" entry is the only thing that moves a channel off Node.
func TestRouterChannelDecisions(t *testing.T) {
	path := writeRoutes(t, `{
	  "version": 1,
	  "default": "node",
	  "channels": {"file": "go", "git": "go", "terminal": "node"}
	}`)
	router, err := LoadRouter(path)
	if err != nil {
		t.Fatalf("LoadRouter: %v", err)
	}
	cases := []struct {
		channel string
		want    string
	}{
		{"file", RouteGo},
		{"git", RouteGo},
		{"terminal", RouteNode},
		{"nex-session", RouteNode},
		{"", RouteNode},
	}
	for _, tc := range cases {
		if got := router.Channel(tc.channel); got != tc.want {
			t.Errorf("Channel(%q) = %q, want %q", tc.channel, got, tc.want)
		}
	}
	if got := router.ExplicitChannels(); len(got) != 3 {
		t.Errorf("ExplicitChannels() = %v, want the three named in the file", got)
	}
}

// TestRouterDefaultGoInvertsUnlistedChannels pins the one setting that is easy to misread: a
// `"default":"go"` table sends everything the file does not name to Go, which is what a
// late-phase migration uses to shrink the upstream's surface.
func TestRouterDefaultGoInvertsUnlistedChannels(t *testing.T) {
	path := writeRoutes(t, `{"version":1,"default":"go","channels":{"file":"node"}}`)
	router, err := LoadRouter(path)
	if err != nil {
		t.Fatalf("LoadRouter: %v", err)
	}
	if got := router.Channel("git"); got != RouteGo {
		t.Errorf("Channel(git) = %q, want %q under a go default", got, RouteGo)
	}
	if got := router.Channel("file"); got != RouteNode {
		t.Errorf("Channel(file) = %q, want %q (the file overrides the default)", got, RouteNode)
	}
}

// TestRouterHTTPTable covers prefix routing, including that the LONGEST prefix wins so a more
// specific rule is not shadowed by a shorter one regardless of map order.
func TestRouterHTTPTable(t *testing.T) {
	path := writeRoutes(t, `{
	  "version": 1,
	  "default": "node",
	  "http": {"/api/bots/": "go", "/api/": "node"}
	}`)
	router, err := LoadRouter(path)
	if err != nil {
		t.Fatalf("LoadRouter: %v", err)
	}
	cases := []struct {
		path string
		want string
	}{
		{"/api/bots/webhook", RouteGo},
		{"/api/connect-remote", RouteNode},
		{"/ws/remote/abc", RouteNode},
	}
	for _, tc := range cases {
		if got := router.HTTPRoute(tc.path); got != tc.want {
			t.Errorf("HTTPRoute(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestRouterStructuralHTTPRoutesAreNotConfigurable: the routes Go implements exist because Go
// implements them. A table that forwarded /api/server-info would produce two competing answers for
// one path (Go's JSON and Node's), and a forwarded /api/auth/* would validate a session twice
// against two stores. So the table cannot move them.
func TestRouterStructuralHTTPRoutesAreNotConfigurable(t *testing.T) {
	path := writeRoutes(t, `{
	  "version": 1,
	  "default": "node",
	  "http": {"/api/server-info": "node", "/api/auth/login": "node", "/ws": "node"}
	}`)
	router, err := LoadRouter(path)
	if err != nil {
		t.Fatalf("LoadRouter: %v", err)
	}
	for _, p := range []string{"/api/server-info", "/api/auth/login", "/api/auth/session", "/ws", "/ws/host"} {
		if got := router.HTTPRoute(p); got != RouteGo {
			t.Errorf("HTTPRoute(%q) = %q, want %q (structurally Go-owned)", p, got, RouteGo)
		}
	}
}

// TestRouterTableValidation is the typo-protection matrix. Each row is a file an operator could
// plausibly write and a mistake that must not be silently accepted.
func TestRouterTableValidation(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantErr  bool
		contains string
	}{
		{
			name:    "unknown channel name is refused and named",
			body:    `{"version":1,"channels":{"fil":"go","terminal2":"node"}}`,
			wantErr: true,
			// The message must LIST them: an operator who typo'd three names needs to know which.
			contains: "fil, terminal2",
		},
		{
			name:     "duplicate channel key is refused",
			body:     `{"version":1,"channels":{"file":"go","file":"node"}}`,
			wantErr:  true,
			contains: "duplicate",
		},
		{
			name:     "duplicate http prefix is refused",
			body:     `{"version":1,"http":{"/api/":"go","/api/":"node"}}`,
			wantErr:  true,
			contains: "duplicate",
		},
		{
			name:     "wrong version is refused",
			body:     `{"version":2,"channels":{"file":"go"}}`,
			wantErr:  true,
			contains: "version",
		},
		{
			name:     "missing version is refused",
			body:     `{"channels":{"file":"go"}}`,
			wantErr:  true,
			contains: "version",
		},
		{
			name:     "unknown target value is refused",
			body:     `{"version":1,"channels":{"file":"rust"}}`,
			wantErr:  true,
			contains: "not a valid target",
		},
		{
			name:     "unknown top-level key is refused",
			body:     `{"version":1,"channel":{"file":"go"}}`,
			wantErr:  true,
			contains: "unknown field",
		},
		{
			name:     "trailing content is refused",
			body:     `{"version":1}{"version":1}`,
			wantErr:  true,
			contains: "trailing content",
		},
		{
			name:     "http prefix without a leading slash is refused",
			body:     `{"version":1,"http":{"api/bots":"go"}}`,
			wantErr:  true,
			contains: "must start with",
		},
		{
			name:     "empty channel name is refused",
			body:     `{"version":1,"channels":{"":"go"}}`,
			wantErr:  true,
			contains: "empty",
		},
		{
			name: "a well-formed table is accepted",
			body: `{"version":1,"default":"node","channels":{"file":"go"},"http":{"/api/bots/":"node"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadRouter(writeRoutes(t, tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LoadRouter accepted an invalid table:\n%s", tc.body)
				}
				if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
					t.Errorf("error = %q, want it to mention %q", err, tc.contains)
				}
				return
			}
			if err != nil {
				t.Errorf("LoadRouter rejected a valid table: %v", err)
			}
		})
	}
}

// TestRouterUnknownChannelIsTypedError: callers may want to distinguish "your table names a
// channel that does not exist" from "the file is not JSON" without parsing the message.
func TestRouterUnknownChannelIsTypedError(t *testing.T) {
	_, err := LoadRouter(writeRoutes(t, `{"version":1,"channels":{"nope":"go"}}`))
	var unknown *ErrUnknownChannel
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v (%T), want *ErrUnknownChannel", err, err)
	}
	if len(unknown.Names) != 1 || unknown.Names[0] != "nope" {
		t.Errorf("Names = %v, want [nope]", unknown.Names)
	}
	var bad *ErrBadRoutes
	if errors.As(err, &bad) {
		t.Error("an unknown channel must not also report as a structurally bad table")
	}
}

// TestDefaultRoutesFilePath pins the path D7 forces: no environment variable, so the file lives
// beside web-sessions.json in the app config dir.
func TestDefaultRoutesFilePath(t *testing.T) {
	base := t.TempDir()
	t.Setenv("NEX_DATA_BASE_DIR", base)
	want := filepath.Join(base, ".nex", "v2", RoutesFileName)
	got := DefaultRoutesFilePath()
	if got != want {
		t.Errorf("DefaultRoutesFilePath() = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, filepath.Join(".nex", "v2", RoutesFileName)) {
		t.Errorf("DefaultRoutesFilePath() = %q, want it under <data base>/.nex/v2", got)
	}
	if !strings.HasPrefix(got, os.Getenv("NEX_DATA_BASE_DIR")) {
		t.Errorf("DefaultRoutesFilePath() = %q, want it under NEX_DATA_BASE_DIR", got)
	}
}
