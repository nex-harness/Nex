package server

import (
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tylinux/nex/go/internal/adapters/webroot"
)

// staticMimeTypes is the verbatim table from `staticMimeTypes` in packages/server/src/http.ts.
// Anything not listed falls back to application/octet-stream; note that this is *not* Go's
// mime.TypeByExtension, which differs on .js (.js -> text/javascript; charset=utf-8 only in
// newer tables), .map (.map -> application/json is missing from Go's table) and .wasm, so the
// table is spelled out rather than delegated.
var staticMimeTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".gif":   "image/gif",
	".html":  "text/html; charset=utf-8",
	".ico":   "image/x-icon",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".map":   "application/json; charset=utf-8",
	".png":   "image/png",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
	".wasm":  "application/wasm",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

const (
	// cacheNoCache and cacheImmutable are the two values `resolveStaticFile`'s caller sets.
	cacheNoCache    = "no-cache"
	cacheImmutable  = "public, max-age=31536000, immutable"
	defaultMimeType = "application/octet-stream"
)

// StaticHandler serves files under root, reproducing the `app.get("*")` block of
// packages/server/src/http.ts. Filesystem access goes through internal/adapters/webroot.
type StaticHandler struct {
	root        webroot.Root
	spaFallback bool
}

// NewStaticHandler validates root and returns the handler. Node resolves the root per request
// (`resolve(staticRoot)`), so a relative root tracks the process cwd; the Go equivalent is to
// resolve once here, which is equivalent for a server that does not chdir.
func NewStaticHandler(root string, spaFallback bool) (*StaticHandler, error) {
	resolved, ok := webroot.New(strings.TrimSpace(root))
	if !ok {
		return nil, ErrNoStaticRoot
	}
	return &StaticHandler{root: resolved, spaFallback: spaFallback}, nil
}

func (s *StaticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pathname, ok := urlPathname(r)
	if !ok {
		// Go's http.Server rejects a malformed request target before a handler runs; the
		// recorded Node responses contain none, so this only guards direct handler use.
		writeBadRequest(w)
		return
	}
	filePath, err := resolveStaticFile(s.root, pathname, s.spaFallback)
	if err != nil {
		// Node lets decodeURIComponent's URIError escape the handler; Hono converts it to a
		// 500 with the plain-text body "Internal Server Error". Reproduced, not fixed.
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if filePath == "" {
		writeNotFound(w)
		return
	}
	content, readErr := webroot.Read(filePath)
	if readErr != nil {
		// resolveStaticFile stat()ed the file first, so this is a genuine read error; Node's
		// readFile rejection would surface as a 500 rather than a 404.
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	cacheControl := cacheImmutable
	if strings.HasSuffix(filePath, "index.html") {
		cacheControl = cacheNoCache
	}
	h := w.Header()
	h.Set("Cache-Control", cacheControl)
	h.Set("Content-Type", staticContentType(filePath))
	h.Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(content)
}

// resolveStaticFile ports `resolveStaticFile` from packages/server/src/http.ts. It returns an
// empty path (and nil error) when nothing should be served — 404 or SPA fallback. A non-nil
// error means the request was malformed (bad percent-encoding), which Node answers with 500.
//
// `pathname` must already be the normalized pathname, i.e. what `new URL(c.req.url).pathname`
// yields in Node (see urlPathname).
func resolveStaticFile(root webroot.Root, pathname string, spaFallback bool) (string, error) {
	normalized := pathname
	if normalized == "/" {
		normalized = "/index.html"
	}
	decoded, err := decodeURIComponentJS(normalized)
	if err != nil {
		return "", err
	}
	relativePath := strings.TrimLeft(decoded, "/")
	rootPath := root.Path()
	candidate := root.Join(relativePath)
	if !isInsideDirectory(rootPath, candidate) {
		return "", nil
	}

	switch webroot.Stat(candidate) {
	case webroot.KindDirectory:
		// A directory is served through its own index.html. The second inside-root check
		// mirrors the TypeScript, which re-resolves and re-checks after the join.
		candidate = root.Join(relativePath + "/index.html")
		if !isInsideDirectory(rootPath, candidate) {
			return "", nil
		}
		if webroot.Stat(candidate) != webroot.KindFile {
			// No index.html in this directory: fall through to the SPA rules below.
			break
		}
		return candidate, nil
	case webroot.KindFile:
		return candidate, nil
	}

	// Static miss: fall back to index.html only for non-protected paths.
	if !spaFallback || !isStaticFallbackAllowed(pathname) {
		return "", nil
	}
	return rootIndexFile(root), nil
}

// rootIndexFile returns the SPA entry point, or "" when it does not exist.
func rootIndexFile(root webroot.Root) string {
	candidate := root.Join("index.html")
	if webroot.Stat(candidate) != webroot.KindFile {
		return ""
	}
	return candidate
}

// isInsideDirectory ports `isInsideDirectory`: relative(root, candidate) must not start with
// ".." and must not contain "../" (on POSIX) or "..\" (on Windows) as a path segment.
// filepath.Rel reproduces node:path's relative for both platforms.
func isInsideDirectory(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if strings.HasPrefix(rel, "..") {
		return false
	}
	separator := string(filepath.Separator)
	return !strings.Contains(rel, ".."+separator)
}

// isStaticFallbackAllowed ports `isStaticFallbackAllowed`: /api/* and /ws/* are token
// protected and must never receive the SPA shell (webAuth.ts isTokenProtectedPath).
func isStaticFallbackAllowed(pathname string) bool {
	return !isTokenProtectedPath(pathname)
}

// isTokenProtectedPath mirrors webAuth.ts: /api/auth/* is exempt, everything else under
// /api/ or /ws is protected.
func isTokenProtectedPath(pathname string) bool {
	if isAuthRoute(pathname) {
		return false
	}
	return isWebSocketPath(pathname) || strings.HasPrefix(pathname, "/api/")
}

func isWebSocketPath(pathname string) bool {
	return pathname == "/ws" || strings.HasPrefix(pathname, "/ws/")
}

func isAuthRoute(pathname string) bool {
	return pathname == "/api/auth" || strings.HasPrefix(pathname, "/api/auth/")
}

// staticContentType ports `staticContentType`: lookup by lowercased extension.
func staticContentType(filePath string) string {
	ext := strings.ToLower(path.Ext(filePath))
	if mime, ok := staticMimeTypes[ext]; ok {
		return mime
	}
	return defaultMimeType
}

// writeNotFound reproduces Hono's `c.notFound()`: status 404, text/plain; charset=UTF-8,
// body "404 Not Found". Go's default is "404 page not found\n" with a lowercase charset, so
// the response is written explicitly rather than via http.NotFound.
func writeNotFound(w http.ResponseWriter) {
	writePlain(w, http.StatusNotFound, "404 Not Found")
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// decodeURIComponentJS decodes percent-encoding the way JS's decodeURIComponent does: it
// operates on the byte string, then decodes UTF-8. Invalid escapes and invalid UTF-8 are
// errors (URIError), mirroring the throw.
func decodeURIComponentJS(input string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(input); {
		if input[i] != '%' {
			out.WriteByte(input[i])
			i++
			continue
		}
		decoded, size, err := decodePercentAt(input, i)
		if err != nil {
			return "", err
		}
		out.Write(decoded)
		i += size
	}
	return out.String(), nil
}

// decodePercentAt decodes one %XX escape (the byte before it is already consumed) plus any
// immediately following escapes, so the whole run of encoded bytes is decoded together and
// validated as UTF-8 — which is what decodeURIComponent does for multi-byte characters.
func decodePercentAt(input string, start int) ([]byte, int, error) {
	var raw []byte
	i := start
	for i < len(input) && input[i] == '%' {
		if i+2 >= len(input) {
			return nil, 0, errURIMalformed
		}
		high, err := hexVal(input[i+1])
		if err != nil {
			return nil, 0, err
		}
		low, err := hexVal(input[i+2])
		if err != nil {
			return nil, 0, err
		}
		raw = append(raw, high<<4|low)
		i += 3
	}
	decoded, err := decodeUTF8Strict(raw)
	if err != nil {
		return nil, 0, err
	}
	return decoded, i - start, nil
}

// decodeUTF8Strict validates that raw holds well-formed UTF-8. decodeURIComponent throws on
// malformed UTF-8 just as it does on malformed escapes, and Go's utf8.Valid matches that.
// Lone surrogates encoded in UTF-8 (CESU-8 style, e.g. %ED%A0%80) are rejected by the WHATWG
// UTF-8 decoder used by decodeURIComponent, and utf8.Valid rejects them too.
func decodeUTF8Strict(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, errURIMalformed
	}
	return raw, nil
}

func hexVal(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, errURIMalformed
}
