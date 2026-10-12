// Package server implements the Go nex-server HTTP shell: the static file handler and
// GET /api/server-info. It is a byte-compatible port of packages/server/src/http.ts.
//
// Scope is step 2.5 of docs/specs/go-rewrite/phase-2-go-server.md. The routes owned by later
// steps (web auth 2.6, host capability and WebSocket upgrade 2.7, gateway 2.8) are not
// registered yet; while the strangler runs, the Node upstream owns them. Because this
// handler answers 404 for anything it does not own, and because /api/* never falls back to
// index.html, an unowned API path is a 404 here exactly as it is in Node.
package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Version is the NEX_VERSION reported by GET /api/server-info. packages/shared/src/version.ts
// resolves __NEX_VERSION__ from a bundler define and falls back to "0.0.0-dev" when the
// define is absent, which is the case for every non-bundled run (tests, `node --import tsx`).
// The Go value is injected the same way: -ldflags "-X ...server.Version=1.2.3".
var Version = "0.0.0-dev"

// DefaultPort mirrors `Number(process.env["PORT"]) || 3030` in entry-http.ts.
const DefaultPort = 3030

// serverInfoPath is the only /api/* route this step owns.
const serverInfoPath = "/api/server-info"

// Options are the Go equivalents of HttpServerOptions in packages/server/src/http.ts.
// Every field is optional; the zero value reproduces the Node server's defaults.
//
// The environment variables are read here, not in a separate config loader, so no new env
// var is introduced (D7) and the precedence stays visible next to the field it feeds.
//
//	StaticRoot    <- NEX_WEB_STATIC_ROOT
//	ServerID      <- NEX_SERVER_ID
//	Name          <- NEX_SERVER_NAME
//	WorkspacePath <- NEX_SERVER_WORKSPACE
//	AuthToken     <- NEX_SERVER_AUTH_TOKEN (entry-http.ts)
//	Host          <- NEX_SERVER_HOST, then HOST
//	Port          <- PORT, then 3030
type Options struct {
	// ServerID overrides the derived server id. Empty means: this field, then NEX_SERVER_ID,
	// then the hostname, then "nex-server".
	ServerID string
	// Name is the display name; omitted from the response when empty.
	Name string
	// Host and Port are recorded for step 2.7 (the listener); nothing binds yet.
	Host string
	Port int
	// StaticRoot enables static serving when non-blank. Node gates the whole static block on
	// `options.staticRoot?.trim()`, so whitespace-only means "disabled".
	StaticRoot string
	// SPAFallback defaults to true when StaticRoot is set (entry-http.ts passes true).
	SPAFallback bool
	// AuthRequired is the value entry-http.ts passes when NEX_SERVER_AUTH_TOKEN is set.
	AuthRequired bool
	// AuthToken is the web-login token (NEX_SERVER_AUTH_TOKEN). Step 2.6 owns it; the field
	// exists so the ownership is fixed before the auth middleware lands.
	AuthToken string
	// WorkspacePath is NEX_SERVER_WORKSPACE; empty means no workspaces are advertised.
	WorkspacePath string
}

// Config is the frozen configuration the handler uses. Node resolves the same values once at
// construction in entry-http.ts, except that createServerInfo re-reads NEX_SERVER_TOKEN per
// request — which NewServerInfo preserves.
type Config struct {
	StaticRoot   string
	SPAFallback  bool
	ServerID     string
	Name         string
	Workspace    string
	AuthRequired bool
}

// NewHandler builds the HTTP handler and its resolved config. It does not listen: step 2.7
// owns the socket, so callers drive it with httptest.NewServer or their own net/http server.
//
// Route registration order does not matter here (Go's ServeMux picks the most specific
// pattern), but the observable behavior is fixed: GET/HEAD /api/server-info is JSON, and
// everything else falls through to static serving when a root is configured. Methods other
// than GET and HEAD are not registered on /api/server-info and therefore reach the static
// handler, which in turn only answers GET/HEAD — matching Hono, where app.get registers GET
// (and Node's fetch maps a HEAD request onto the GET handler).
func NewHandler(opts Options) (http.Handler, *Config, error) {
	cfg := &Config{
		StaticRoot:   strings.TrimSpace(opts.StaticRoot),
		SPAFallback:  opts.SPAFallback,
		ServerID:     firstNonEmpty(strings.TrimSpace(opts.ServerID), trimmedEnv("NEX_SERVER_ID"), hostname(), "nex-server"),
		Name:         firstNonEmpty(strings.TrimSpace(opts.Name), trimmedEnv("NEX_SERVER_NAME")),
		Workspace:    firstNonEmpty(strings.TrimSpace(opts.WorkspacePath), trimmedEnv("NEX_SERVER_WORKSPACE")),
		AuthRequired: opts.AuthRequired,
	}

	// The /api/* namespace is dispatched directly rather than through http.ServeMux for two
	// reasons, both observable:
	//   - a method-scoped ServeMux pattern answers a mismatched method with 405 and an Allow
	//     header, where Hono answers 404;
	//   - ServeMux rewrites the request path (cleaning "..", collapsing "//") and replies 301
	//     when it changes one, where Hono hands the target to the handler untouched — so
	//     /../etc/passwd and //assets/app.js must reach the static handler verbatim.
	// Only GET /api/server-info exists in this step; every other /api/* route belongs to a
	// later step and, while the strangler runs, to the Node upstream. Steps 2.6 and 2.7 add
	// their routes to this same dispatcher.
	handleAPI := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeNotFound(w)
			return
		}
		if r.URL.EscapedPath() != serverInfoPath {
			writeNotFound(w)
			return
		}
		writeServerInfo(w, NewServerInfo(cfg))
	}

	if cfg.StaticRoot == "" {
		// Without a static root Node registers no catch-all, so every other path is Hono's
		// notFound().
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isAPIPath(r.URL.EscapedPath()) {
				handleAPI(w, r)
				return
			}
			writeNotFound(w)
		}), cfg, nil
	}

	static, err := NewStaticHandler(cfg.StaticRoot, cfg.SPAFallback)
	if err != nil {
		return nil, nil, err
	}
	// app.get("*") only ever matches GET and HEAD; anything else is a 404.
	gated := methodGate(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped := r.URL.EscapedPath()
		if isAPIPath(escaped) {
			handleAPI(w, r)
			return
		}
		gated.ServeHTTP(w, r)
	}), cfg, nil
}

// pathWouldBeRewritten reports whether ServeMux would clean the target. Only the cases the
// recorded responses exercise are flagged: a bare or percent-encoded dot segment, and an empty
// segment produced by a leading "//".
func pathWouldBeRewritten(escapedPath string) bool {
	if strings.Contains(escapedPath, "//") {
		return true
	}
	lower := strings.ToLower(escapedPath)
	return strings.Contains(lower, "/%2e") || strings.Contains(lower, "%2e/") ||
		strings.Contains(lower, "/./") || strings.Contains(lower, "/../")
}

// isAPIPath reports whether the target belongs to the /api/* namespace.
func isAPIPath(escapedPath string) bool {
	return escapedPath == "/api" || strings.HasPrefix(escapedPath, "/api/")
}

// methodGate rejects everything but GET and HEAD with Hono's 404. Go's ServeMux does not
// dispatch on method for the catch-all pattern, so a POST / would otherwise reach the static
// handler; Hono's app.get("*") never matches a non-GET request.
func methodGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeNotFound(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// PortFromEnv reads PORT with entry-http.ts's semantics: a non-numeric or zero value falls
// back to DefaultPort. Host resolution (NEX_SERVER_HOST, then HOST) lives with the listener
// in step 2.7, but the same rule is kept here so the precedence is written down once.
func PortFromEnv() int {
	if raw := trimmedEnv("PORT"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value != 0 {
			return value
		}
	}
	return DefaultPort
}

// ErrNoStaticRoot is returned by NewStaticHandler for a blank root. It exists so callers can
// distinguish "static serving not configured" from a real filesystem error.
var ErrNoStaticRoot = errors.New("server: static root is empty")
