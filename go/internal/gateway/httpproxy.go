package gateway

// httpproxy.go forwards the HTTP routes Go does not own to the Node upstream.
//
// The set is small and fixed by the strangler: /api/pets/* (spritesheet), /api/bots/* (provider
// callbacks), /api/connect-remote and /ws/remote/:id. Everything else Go answers itself, and
// which side owns a path is the Router's decision — the proxy itself carries no route table, it
// asks.
//
// Four properties are load-bearing:
//
//   - bodies stream. httputil.ReverseProxy copies with io.Copy and hands the response body
//     straight through, so a webhook payload or a spritesheet is never held in memory. The
//     request body is not read either: the Director returns the client's own *http.Request.
//   - hop-by-hop headers are stripped in BOTH directions by the library itself
//     (removeHopByHopHeaders + removeConnectionHeaders, RFC 7230 §6.1). Forwarding a Connection
//     header or a Content-Length belonging to the leg in between produces a response the client
//     cannot parse.
//   - no X-Forwarded-* is added. That is what Rewrite gives us: the SetXForwarded path is only
//     taken on the Director path, and X-Forwarded-For would be false anyway — the upstream's only
//     consumer of a client address is the login rate limiter, which reads the socket's
//     remoteAddressKey. Every proxied login must keep looking like it came from 127.0.0.1,
//     exactly as it would when Node was the only server.
//   - the client's credentials are replaced, never forwarded. Go terminates the client's own
//     authentication; the upstream sees the Go-generated shared secret instead. Forwarding a
//     browser's session cookie would work today only because both processes share
//     web-sessions.json, and that coincidence is exactly what must not become the design.

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// HTTPProxy forwards non-Go HTTP routes to the Node upstream.
type HTTPProxy struct {
	proxy  *httputil.ReverseProxy
	target *url.URL
	router *Router
	// secret is the Go-generated shared secret the upstream is authenticated with.
	secret string
	// log receives upstream failures. Nil discards them.
	log Logger
}

// NewHTTPProxy builds the proxy. router may be nil, in which case nothing is forwarded (the Go
// server owns everything) — that is the deployment with no Node upstream configured.
//
// authToken is the Go-generated shared secret. It replaces whatever credential the client
// presented, because the upstream is reached by a machine, not by the browser.
func NewHTTPProxy(target string, authToken string, router *Router) (*HTTPProxy, error) {
	if strings.TrimSpace(target) == "" {
		return nil, ErrNoUpstream
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, &ErrBadRoutes{Why: "upstream base URL must be http or https"}
	}
	instance := &HTTPProxy{target: parsed, router: router, secret: authToken}
	instance.proxy = &httputil.ReverseProxy{
		// Rewrite, not Director: only Director auto-adds X-Forwarded-*, and those headers would
		// be a lie (see the file header). Rewrite also gets Out.Host from the target for free.
		Rewrite: func(pr *httputil.ProxyRequest) {
			// Unlike Director, Rewrite does NOT prefill the outbound URL from the target: the
			// scheme and host must be set here or the transport sees an empty scheme and fails
			// every request with "unsupported protocol scheme". Setting Out.URL.Host alone is not
			// enough — the transport uses Out.Host for the Host header but Out.URL.Host for the
			// address it dials.
			pr.Out.URL.Scheme = parsed.Scheme
			pr.Out.URL.Host = parsed.Host
			pr.Out.URL.Path = singleJoiningSlash(parsed.Path, pr.In.URL.Path)
			pr.Out.URL.RawPath = singleJoiningSlash(parsed.EscapedPath(), pr.In.URL.EscapedPath())
			// The client's query travels with it: the forwarded routes parse it, and dropping it
			// would silently change the meaning of the request.
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			instance.rewriteCredentials(pr.Out.URL)
			// The upstream sees the Go front as its peer, on its Host. A Host the upstream does
			// not recognize is the one header the remoteAddressKey logic could be confused by.
			pr.Out.Host = parsed.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			instance.note("gateway: upstream request failed", err)
			// 502 says "the route exists, the server that owns it is down" — a different answer
			// from the 404 the Go shell gives for a path nobody owns. It is the only status this
			// file produces, and it is never a fabricated RPC response.
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}
	return instance, nil
}

// rewriteCredentials replaces the client's `token` with the shared secret.
//
// The client's own credential is removed first so a browser's ?token= (or a stale one) cannot
// reach the upstream at all; the Go front is the authenticated peer, and it authenticates as
// itself. When Go generated no secret — Node running without auth on loopback — the query is left
// alone, because there is nothing to replace it with and removing it would only lose information.
func (p *HTTPProxy) rewriteCredentials(target *url.URL) {
	if p.secret == "" {
		return
	}
	query := target.Query()
	query.Del("token")
	query.Set("token", p.secret)
	target.RawQuery = query.Encode()
}

// Handles reports whether this path belongs to the upstream.
func (p *HTTPProxy) Handles(path string) bool {
	if p == nil || p.router == nil {
		return false
	}
	return !p.router.HTTPRouteOwnedByGo(path)
}

// ServeHTTP forwards one request, unless the router says this path belongs to Go.
//
// In the real chain the HTTP shell only hands the proxy what Go does not own, so this check never
// fires. It is here because the proxy is also mountable on its own, and a route that reached it
// anyway — a future shell that mounts the proxy too early, or a /ws/remote/:id path that matched
// too broadly — would then produce a SECOND answer for a path Go already answers. Forwarding
// /api/auth/login would validate one session against two stores and mint a second cookie; the
// symptom would be a client whose logout does not log it out.
func (p *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.Handles(r.URL.Path) {
		// 404, not 403: from the caller's side a path Go owns is a path nobody serves here, which
		// is what the shell's own notFound says.
		http.NotFound(w, r)
		return
	}
	p.proxy.ServeHTTP(w, r)
}

// note emits an upstream failure through the proxy's logger when one is configured.
func (p *HTTPProxy) note(msg string, err error) {
	if p.log != nil {
		p.log.Warn(msg, "target", p.target.String(), "error", err)
	}
}

// singleJoiningSlash joins a base path and a request path with exactly one slash, so a base with a
// trailing slash and a path with a leading one do not produce "//" — which Node would answer with a
// 404 the client cannot tell apart from a missing route.
func singleJoiningSlash(base, requestPath string) string {
	switch {
	case base == "" || base == "/":
		return requestPath
	case strings.HasSuffix(base, "/") && strings.HasPrefix(requestPath, "/"):
		return base + requestPath[1:]
	case !strings.HasSuffix(base, "/") && !strings.HasPrefix(requestPath, "/"):
		return base + "/" + requestPath
	default:
		return base + requestPath
	}
}
