package server

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylinux/nex/go/internal/adapters/webroot"
)

// notFoundType is Hono's c.notFound() content type. Go's http.NotFound uses a lowercase
// charset and different text, so every 404 in this package is written explicitly.
const notFoundType = "text/plain; charset=UTF-8"

// writeFixture materializes a static root for the table tests. Contents are ASCII so the
// assertions can name the expected body directly.
func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"index.html":            "<html>root</html>",
		"app.js":                "console.log(1)",
		"style.css":             "body{}",
		"data.json":             `{"k":1}`,
		"logo.png":              "PNG",
		"mod.wasm":              "ASM",
		"photo.jpeg":            "JPEG",
		"anim.gif":              "GIF",
		"photo.webp":            "WEBP",
		"font.woff":             "WOFF",
		"font.woff2":            "WOFF2",
		"README.md":             "# doc",
		"weird.bin":             "BINARY",
		"UPPER.PNG":             "UPPER",
		"file with space.txt":   "SPACED",
		"nested/index.html":     "<html>nested</html>",
		"nested/only.txt":       "ONLY",
		"secret/passwd":         "root:x:0:0::/root:/bin/sh",
		"empty-dir/.keep-empty": "",
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// staticFixtureRoot builds a handler over writeFixture with SPA fallback on or off.
func staticFixtureRoot(t *testing.T, spaFallback bool) http.Handler {
	t.Helper()
	handler, err := NewStaticHandler(writeFixture(t), spaFallback)
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}
	return handler
}

// TestStaticTable pins the observable behavior of every rule in the static section of
// packages/server/src/http.ts. The expectations are the recorded TypeScript responses
// (go/testdata/http/requests.json), restated here so a regression names the rule it broke.
func TestStaticTable(t *testing.T) {
	// SPA fallback is ON for every row unless a row opts out, because that is how the server
	// is deployed (entry-http.ts always passes spaFallback: true when a static root is set).
	cases := []struct {
		name          string
		target        string
		noSPAFallback bool
		wantStatus    int
		wantType      string
		wantCache     string
		wantBody      string // "" means the body is not asserted
	}{
		// Root and index.html share a cache policy and a body.
		{name: "root serves index", target: "/", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
		{name: "explicit index", target: "/index.html", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},

		// MIME table, including the two binary types.
		{name: "js", target: "/app.js", wantStatus: 200, wantType: "text/javascript; charset=utf-8", wantCache: cacheImmutable, wantBody: "console.log(1)"},
		{name: "css", target: "/style.css", wantStatus: 200, wantType: "text/css; charset=utf-8", wantCache: cacheImmutable},
		{name: "json", target: "/data.json", wantStatus: 200, wantType: "application/json; charset=utf-8", wantCache: cacheImmutable},
		{name: "png binary", target: "/logo.png", wantStatus: 200, wantType: "image/png", wantCache: cacheImmutable, wantBody: "PNG"},
		{name: "wasm binary", target: "/mod.wasm", wantStatus: 200, wantType: "application/wasm", wantCache: cacheImmutable, wantBody: "ASM"},
		{name: "jpeg", target: "/photo.jpeg", wantStatus: 200, wantType: "image/jpeg", wantCache: cacheImmutable},
		{name: "gif", target: "/anim.gif", wantStatus: 200, wantType: "image/gif", wantCache: cacheImmutable},
		{name: "webp", target: "/photo.webp", wantStatus: 200, wantType: "image/webp", wantCache: cacheImmutable},
		{name: "woff", target: "/font.woff", wantStatus: 200, wantType: "font/woff", wantCache: cacheImmutable},
		{name: "woff2", target: "/font.woff2", wantStatus: 200, wantType: "font/woff2", wantCache: cacheImmutable},
		{name: "uppercase extension", target: "/UPPER.PNG", wantStatus: 200, wantType: "image/png", wantCache: cacheImmutable, wantBody: "UPPER"},
		{name: "unknown extension", target: "/README.md", wantStatus: 200, wantType: defaultMimeType, wantCache: cacheImmutable},
		{name: "no extension", target: "/weird.bin", wantStatus: 200, wantType: defaultMimeType, wantCache: cacheImmutable},

		// Query strings are not part of the filesystem lookup.
		{name: "missing map falls back", target: "/missing.js.map", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
		{name: "query string ignored", target: "/app.js?v=abc123", wantStatus: 200, wantType: "text/javascript; charset=utf-8", wantCache: cacheImmutable, wantBody: "console.log(1)"},
		{name: "percent-encoded space", target: "/file%20with%20space.txt", wantStatus: 200, wantType: "text/plain; charset=utf-8", wantCache: cacheImmutable, wantBody: "SPACED"},
		{name: "percent-encoded separator", target: "/app%2Ejs", wantStatus: 200, wantType: "text/javascript; charset=utf-8", wantCache: cacheImmutable, wantBody: "console.log(1)"},

		// Directories fall back to their own index.html, not the root one.
		{name: "directory serves its index", target: "/nested", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>nested</html>"},
		{name: "directory with trailing slash", target: "/nested/", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>nested</html>"},
		{name: "file inside directory", target: "/nested/only.txt", wantStatus: 200, wantType: "text/plain; charset=utf-8", wantCache: cacheImmutable, wantBody: "ONLY"},

		// Path traversal. None of these may read a file outside the root: /secret/passwd is
		// inside the root, ../ is not.
		{name: "traversal to sibling", target: "/../etc/passwd", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
		{name: "encoded traversal", target: "/%2e%2e/etc/passwd", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
		{name: "encoded traversal uppercase", target: "/%2E%2E/%2E%2E/etc/passwd", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
		{name: "encoded slash traversal", target: "/..%2fetc%2fpasswd", wantStatus: 404, wantType: notFoundType},
		// Dot segments spelled with a half-encoded dot are still dot segments, so these
		// collapse to a path inside the root rather than escaping it.
		{name: "mixed encoded traversal collapses", target: "/.%2e/%2e./empty-dir/", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
		{name: "interior dot segments collapse", target: "/nested/../app.js", wantStatus: 200, wantType: "text/javascript; charset=utf-8", wantCache: cacheImmutable, wantBody: "console.log(1)"},
		{name: "in-root secret is reachable", target: "/secret/passwd", wantStatus: 200, wantType: defaultMimeType, wantCache: cacheImmutable, wantBody: "root:x:0:0::/root:/bin/sh"},
		{name: "traversal to root index", target: "/nested/..", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},

		// SPA fallback: on for page paths, never for protected namespaces.
		{name: "missing page falls back", target: "/login", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
		{name: "deep page falls back", target: "/settings/general", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
		{name: "missing page without fallback", target: "/login", noSPAFallback: true, wantStatus: 404, wantType: notFoundType},
		{name: "api never falls back", target: "/api/does-not-exist", wantStatus: 404, wantType: notFoundType},
		{name: "api auth is not protected", target: "/api/auth/login", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
		{name: "ws never falls back", target: "/ws", wantStatus: 404, wantType: notFoundType},
		{name: "ws subpath never falls back", target: "/ws/remote/none", wantStatus: 404, wantType: notFoundType},
		{name: "empty dir falls back to root index", target: "/empty-dir/", wantStatus: 200, wantType: "text/html; charset=utf-8", wantCache: cacheNoCache, wantBody: "<html>root</html>"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handler := staticFixtureRoot(t, !testCase.noSPAFallback)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, escapeForGoRequest(testCase.target), nil))

			if recorder.Code != testCase.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}
			if got := recorder.Header().Get("Content-Type"); got != testCase.wantType {
				t.Errorf("Content-Type = %q, want %q", got, testCase.wantType)
			}
			if got := recorder.Header().Get("Cache-Control"); got != testCase.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, testCase.wantCache)
			}
			if testCase.wantBody != "" && recorder.Body.String() != testCase.wantBody {
				t.Errorf("body = %q, want %q", recorder.Body.String(), testCase.wantBody)
			}
		})
	}
}

// TestStaticSPAFallbackToggle covers the spaFallback=false branch directly: the same missing
// page 404s, and the same real file still serves.
func TestStaticSPAFallbackToggle(t *testing.T) {
	handler := staticFixtureRoot(t, false)

	for _, target := range []string{"/login", "/settings/general", "/nested/missing"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, recorder.Code)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "console.log(1)" {
		t.Errorf("real file with fallback off: status = %d body = %q", recorder.Code, recorder.Body.String())
	}
}

// TestStaticNotFoundShape pins Hono's notFound() body and headers, which differ from Go's
// http.NotFound in both the charset case and the text.
func TestStaticNotFoundShape(t *testing.T) {
	handler := staticFixtureRoot(t, true)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/nope", nil))

	if got, want := recorder.Body.String(), "404 Not Found"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got, want := recorder.Header().Get("Content-Type"), "text/plain; charset=UTF-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "" {
		t.Errorf("404 carries Cache-Control %q, want none", got)
	}
}

// TestStaticMalformedPercentEncoding covers the URIError path: decodeURIComponent throws and
// Hono turns it into a 500. Reproduced rather than turned into a 404, because a client seeing
// a different status than the Node server sends is a visible regression.
func TestStaticMalformedPercentEncoding(t *testing.T) {
	// Only the cases a real net/http server forwards to a handler are listed. A malformed
	// escape such as "/%ZZ" never arrives: Go's request parser rejects the target with 400
	// before routing, where Node passes it on and answers 500. That difference is a property of
	// the two HTTP stacks, not of this handler, and is recorded in the phase-2 caveats.
	handler := staticFixtureRoot(t, true)
	for _, target := range []string{"/%FF", "/%C3%28", "/%ED%A0%80", "/%C0%AE%C0%AE/secret"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, rawRequest(target))
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", target, recorder.Code)
		}
		if got, want := recorder.Body.String(), "Internal Server Error"; got != want {
			t.Errorf("%s: body = %q, want %q", target, got, want)
		}
	}
}

// rawRequest builds a request from a raw origin-form target. httptest.NewRequest goes through
// url.Parse, which rejects a malformed escape and a literal ".." path segment — exactly the
// inputs this package has to survive.
//
// Path is the decoded form and RawPath the verbatim target, which is what a real net/http
// server produces: RawPath is populated only when it is a valid encoding of Path, and
// EscapedPath() then returns it unchanged. Decoding here mirrors net/url so the handler sees
// the same escaped value it would see in production.
func rawRequest(target string) *http.Request {
	decoded, err := url.PathUnescape(target)
	if err != nil {
		// net/http never gets here — it answers 400 before routing — but keeping the raw
		// bytes lets the handler's own guard be exercised directly.
		decoded = target
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.URL = &url.URL{Path: decoded, RawPath: target}
	request.RequestURI = target
	return request
}

// TestStaticHeadMatchesGetWithoutBody checks HEAD is routed like GET but writes no body. Hono
// registers app.get, which Node answers with the same headers and no content.
func TestStaticHeadMatchesGetWithoutBody(t *testing.T) {
	handler := staticFixtureRoot(t, true)
	for _, target := range []string{"/", "/app.js", "/logo.png", "/login"} {
		get := httptest.NewRecorder()
		handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, target, nil))
		head := httptest.NewRecorder()
		handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, target, nil))

		if head.Code != get.Code {
			t.Errorf("%s: HEAD status = %d, GET status = %d", target, head.Code, get.Code)
		}
		if head.Body.Len() != 0 {
			t.Errorf("%s: HEAD wrote %d body bytes, want 0", target, head.Body.Len())
		}
		if got, want := head.Header().Get("Content-Type"), get.Header().Get("Content-Type"); got != want {
			t.Errorf("%s: HEAD Content-Type = %q, GET = %q", target, got, want)
		}
	}
}

// TestStaticTraversalProperty generates random percent-encoded paths and asserts that whatever
// the handler serves is inside the static root — either a real fixture file or the root
// index.html after a legitimate fallback, and never a traversal target.
//
// The corpus mixes every encoding trick the handler has to survive: dot segments, encoded
// separators, encoded dots, overlong UTF-8 and stray escapes.
func TestStaticTraversalProperty(t *testing.T) {
	root := writeFixture(t)
	handler, err := NewStaticHandler(root, true)
	if err != nil {
		t.Fatalf("NewStaticHandler: %v", err)
	}
	resolved, ok := webroot.New(root)
	if !ok {
		t.Fatalf("webroot.New(%q) rejected the fixture root", root)
	}

	pieces := []string{
		"%2e", "%2E", "..", ".", "%2f", "%5c", "%252f", "%00", "%ff", "%c0%ae",
		"/", "//", "\\", "a", "b.txt", "index.html", "passwd", "secret", "..%2f", "....",
	}
	random := rand.New(rand.NewSource(20251012)) //nolint:gosec // deterministic corpus, not security
	allowed := map[string]bool{
		filepath.Join(root, "index.html"): true,
	}

	for i := 0; i < 4000; i++ {
		var target strings.Builder
		target.WriteByte('/')
		for n := random.Intn(6) + 1; n > 0; n-- {
			target.WriteString(pieces[random.Intn(len(pieces))])
		}
		path := target.String()

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, escapeForGoRequest(path), nil))
		if recorder.Code == http.StatusOK {
			// Re-resolve the way the handler did and assert the served file is in the root.
			served := servedFileFor(t, resolved, path)
			if served == "" {
				t.Fatalf("path %q returned 200 but resolved to no file", path)
			}
			if !allowed[served] && !isInsideDirectory(root, served) {
				t.Fatalf("path %q escaped the static root: served %q (root %q)", path, served, root)
			}
		}
	}
}

// servedFileFor re-runs the resolution for a path and returns what the handler served.
func servedFileFor(t *testing.T, root webroot.Root, path string) string {
	t.Helper()
	pathname, ok := urlPathname(httptest.NewRequest(http.MethodGet, escapeForGoRequest(path), nil))
	if !ok {
		return ""
	}
	resolved, err := resolveStaticFile(root, pathname, true)
	if err != nil {
		return ""
	}
	return resolved
}

// TestStaticIsInsideDirectory pins the traversal guard itself, including the Windows separator, so
// the property test above cannot regress into a symlink-free "it happens to work" state.
func TestStaticIsInsideDirectory(t *testing.T) {
	root := filepath.FromSlash("/srv/web")
	sep := string(filepath.Separator)
	cases := []struct {
		candidate string
		want      bool
	}{
		{candidate: filepath.Join(root, "app.js"), want: true},
		{candidate: root, want: true},
		{candidate: filepath.Join(root, "assets", "app.js"), want: true},
		{candidate: filepath.Join(root, "..", "etc", "passwd"), want: false},
		{candidate: filepath.FromSlash("/srv/web/../etc/passwd"), want: false},
		{candidate: filepath.FromSlash("/srv/other/app.js"), want: false},
		{candidate: filepath.Join(root, "a..b", "app.js"), want: true},
		{candidate: filepath.FromSlash("/srv/web/.."), want: false},
	}
	for _, testCase := range cases {
		if got := isInsideDirectory(root, testCase.candidate); got != testCase.want {
			t.Errorf("isInsideDirectory(%q, %q) = %v, want %v (sep %q)", root, testCase.candidate, got, testCase.want, sep)
		}
	}
}

// TestStaticContentTypeTable pins every MIME entry, including the fallback, as a unit so a
// future edit to the table has to be deliberate.
func TestStaticContentTypeTable(t *testing.T) {
	cases := map[string]string{
		"a.css":    "text/css; charset=utf-8",
		"a.gif":    "image/gif",
		"a.html":   "text/html; charset=utf-8",
		"a.ico":    "image/x-icon",
		"a.jpg":    "image/jpeg",
		"a.jpeg":   "image/jpeg",
		"a.js":     "text/javascript; charset=utf-8",
		"a.json":   "application/json; charset=utf-8",
		"a.map":    "application/json; charset=utf-8",
		"a.png":    "image/png",
		"a.svg":    "image/svg+xml",
		"a.txt":    "text/plain; charset=utf-8",
		"a.wasm":   "application/wasm",
		"a.webp":   "image/webp",
		"a.woff":   "font/woff",
		"a.woff2":  "font/woff2",
		"a.JS":     "text/javascript; charset=utf-8",
		"a.bin":    defaultMimeType,
		"a":        defaultMimeType,
		"a.tar.gz": defaultMimeType,
	}
	for name, want := range cases {
		if got := staticContentType(name); got != want {
			t.Errorf("staticContentType(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestStaticRootEmptyRejected documents that static serving stays off when no root is set,
// matching `if (options.staticRoot?.trim())` in the TypeScript.
func TestStaticRootEmptyRejected(t *testing.T) {
	for _, root := range []string{"", "   "} {
		if _, err := NewStaticHandler(root, true); err != ErrNoStaticRoot {
			t.Errorf("NewStaticHandler(%q) error = %v, want ErrNoStaticRoot", root, err)
		}
	}
}

// TestHandlerWithoutStaticRootStillServesServerInfo proves the API route does not depend on a
// static root, and that everything else is a Hono-shaped 404.
func TestHandlerWithoutStaticRootStillServesServerInfo(t *testing.T) {
	t.Setenv("NEX_SERVER_ID", "no-static-root")
	t.Setenv("NEX_SERVER_NAME", "")
	t.Setenv("NEX_SERVER_WORKSPACE", "")
	t.Setenv("NEX_SERVER_TOKEN", "")

	handler, _, err := NewHandler(Options{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	info := httptest.NewRecorder()
	handler.ServeHTTP(info, httptest.NewRequest(http.MethodGet, "/api/server-info", nil))
	if info.Code != http.StatusOK {
		t.Fatalf("server-info status = %d, want 200", info.Code)
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusNotFound {
		t.Errorf("page status = %d, want 404 without a static root", page.Code)
	}
	if got, want := page.Body.String(), "404 Not Found"; got != want {
		t.Errorf("page body = %q, want %q", got, want)
	}
}

// TestHttpHandlerRejectsNonGETMethods covers the method gate: Hono's app.get("*") never matches a
// non-GET request, so POST/PUT/DELETE are a plain 404 rather than the static file.
func TestHttpHandlerRejectsNonGETMethods(t *testing.T) {
	handler, _, err := NewHandler(Options{StaticRoot: writeFixture(t), SPAFallback: true})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, "/app.js", nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s /app.js: status = %d, want 404", method, recorder.Code)
		}
	}
}

// TestStaticRemoveDotSegments pins the WHATWG dot-segment rules the handler depends on, including
// the case-insensitive percent-encoded spellings that decide whether "%2e%2e" is a traversal
// attempt or an ordinary (already collapsed) path.
func TestStaticRemoveDotSegments(t *testing.T) {
	cases := map[string]string{
		"/":                         "/",
		"/a/b":                      "/a/b",
		"/a/./b":                    "/a/b",
		"/a/../b":                   "/b",
		"/../a":                     "/a",
		"/a/b/../../c":              "/c",
		"/%2e%2e/etc/passwd":        "/etc/passwd",
		"/%2E%2E/%2E%2E/etc/passwd": "/etc/passwd",
		"/.%2e/%2e./etc":            "/etc",
		"/..%2fetc%2fpasswd":        "/..%2fetc%2fpasswd",
		"/a/%2f/b":                  "/a/%2f/b",
		"/a/b/":                     "/a/b/",
		"/a/.":                      "/a/",
		"/a/..":                     "/",
		"/./":                       "/",
		"/a//b":                     "/a//b",
		"/...":                      "/...",
		"/a/%2e%2e/b":               "/b",
	}
	for input, want := range cases {
		if got := removeDotSegments(input); got != want {
			t.Errorf("removeDotSegments(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestDecodeURIComponentJSMatchesNode pins the decoder's edges: valid escapes, multi-byte
// UTF-8 decoded as a unit, and the malformed cases that must raise URIError.
func TestDecodeURIComponentJSMatchesNode(t *testing.T) {
	valid := map[string]string{
		"/a%20b":              "/a b",
		"/a%2Fb":              "/a/b",
		"/%E4%B8%AD%E6%96%87": "/中文",
		"/caf%C3%A9":          "/café",
		"/%252e":              "/%2e",
		"/plain":              "/plain",
		"/%41%42":             "/AB",
	}
	for input, want := range valid {
		got, err := decodeURIComponentJS(input)
		if err != nil {
			t.Errorf("decodeURIComponentJS(%q) error = %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("decodeURIComponentJS(%q) = %q, want %q", input, got, want)
		}
	}

	malformed := []string{"/%", "/%Z", "/%ZZ", "/%FF", "/%C3%28", "/%ED%A0%80", "/a%"}
	for _, input := range malformed {
		if got, err := decodeURIComponentJS(input); err == nil {
			t.Errorf("decodeURIComponentJS(%q) = %q, want URIError", input, got)
		}
	}
}

// TestServerInfoShapeAndHeaders pins the JSON contract of GET /api/server-info against the
// recorded Node responses: content type, and the fact that the body has no trailing newline.
func TestServerInfoShapeAndHeaders(t *testing.T) {
	t.Setenv("NEX_SERVER_ID", "shape-check")
	t.Setenv("NEX_SERVER_NAME", "")
	t.Setenv("NEX_SERVER_WORKSPACE", "")
	t.Setenv("NEX_SERVER_TOKEN", "")

	handler, _, err := NewHandler(Options{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/server-info", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "" {
		t.Errorf("server-info carries Cache-Control %q, want none", got)
	}
	body := recorder.Body.String()
	want := `{"serverId":"shape-check","version":"` + Version +
		`","protocolVersion":1,"authRequired":false,"workspaces":[],"capabilities":` +
		`{"desktopContinuous":true,"websocketRpc":true,"processResourceTelemetry":true}}`
	if body != want {
		t.Errorf("body = %s\nwant   %s", body, want)
	}
	if got := fmt.Sprintf("%d", len(body)); recorder.Header().Get("Content-Length") != got {
		t.Errorf("Content-Length = %q, want %q", recorder.Header().Get("Content-Length"), got)
	}
}
