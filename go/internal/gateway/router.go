package gateway

// router.go owns ONE question: given a channel name or an HTTP path, does the Go process answer
// it, or does the Node upstream? The answer is computed once per connection from a table loaded
// from disk, and is immutable for that connection's lifetime — there is no hot reload in Phase 2,
// so a mid-connection flip could only ever produce a half-migrated request.
//
// The table file is `<appConfigDir>/gateway-routes.json` (D7: no new environment variables):
//
//	{"version":1,"default":"node","channels":{"<name>":"go"|"node"}, "http":{"<prefix>":"go"|"node"}}
//
// With no file, every channel goes to Node EXCEPT the routes Go owns structurally (static, auth,
// server-info, capability), which are not in the table at all — they are Go's by construction, not
// by configuration. A name in the file that is not a real service channel is a startup error that
// lists the offenders, because silently ignoring a typo leaves that channel on Node forever while
// the operator believes it moved.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/adapters/serverenv"
	"github.com/tylinux/nex/go/internal/gateway/channels"
)

// appConfigDir reproduces getAppConfigDir() (packages/services/src/paths.ts): the v2 directory
// under the data base, which is NEX_DATA_BASE_DIR or $HOME. It is the same resolution
// webauth.DefaultSessionsFilePath uses, so the two files the Go server owns live side by side.
func appConfigDir() string {
	base := serverenv.Trimmed("NEX_DATA_BASE_DIR")
	if base == "" {
		base = serverenv.HomeDir()
	}
	return filepath.Join(base, ".nex", "v2")
}

// HTTPRoutePrefixes are the routes Go owns structurally and that therefore never reach the file.
// They are the strangler's fixed points: if the proxy ever forwarded one, the client would get two
// competing answers (Go's static index.html and Node's SPA fallback), and a forwarded /api/auth/*
// would mean the session cookie is validated twice by two stores.
//
// /ws and /ws/host are matched EXACTLY, not as prefixes, because /ws/remote/:id is a DIFFERENT
// route that legitimately belongs to Node — matching them as prefixes would swallow it here and
// the gateway could never forward a remote-backend socket at all.
var HTTPRoutePrefixes = []string{
	"/api/auth/", // webAuth.mountRoutes
	"/api/server-info",
	"/api/rpc-host-capability",
}

// HTTPRouteExactPaths are Go's own WebSocket endpoints, for the same reason they are not prefixes.
var HTTPRouteExactPaths = []string{
	"/ws",
	"/ws/host",
}

// Router answers "go or node" for a channel name and for an HTTP path.
type Router struct {
	// fallback is the table's "default", applied to any channel the file does not name.
	fallback string
	// channels holds explicit per-channel overrides. A nil value means "not named".
	channels map[string]string
	// http holds path-prefix overrides, checked longest-prefix-first.
	http []httpRoute
	// source records where the table came from, for the startup log.
	source string
}

// httpRoute is one prefix rule from the file's "http" object.
type httpRoute struct {
	prefix string
	route  string
}

// routesFile is the on-disk shape. Version is int so a JSON string or float is rejected by the
// explicit check rather than silently coercing to 0 or 1.
type routesFile struct {
	Version  int               `json:"version"`
	Default  string            `json:"default"`
	Channels map[string]string `json:"channels"`
	HTTP     map[string]string `json:"http"`
}

// LoadRouter reads the routing table and validates it.
//
// A missing file is NOT an error: it means "default routing", which is the strangler's starting
// state (everything on Node except what Go owns structurally). Any other read or validation failure
// IS an error, because starting with a routing table we did not understand would move channels
// between processes without anyone deciding to.
func LoadRouter(path string) (*Router, error) {
	if path == "" {
		path = DefaultRoutesFilePath()
	}
	router := &Router{
		fallback: RouteNode,
		channels: map[string]string{},
		source:   path,
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return router, nil
		}
		return nil, &ErrBadRoutes{Path: path, Why: err.Error()}
	}
	var file routesFile
	// DisallowUnknownFields is deliberate: a misspelled key ("channel" instead of "channels")
	// is the same class of mistake as a misspelled channel name, and silently ignoring it would
	// leave the operator with a table that does nothing.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, &ErrBadRoutes{Path: path, Why: err.Error()}
	}
	// A second JSON value in the file means something appended to it; ignoring the tail would hide
	// the very edit that caused the problem.
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == nil {
		return nil, &ErrBadRoutes{Path: path, Why: "trailing content after the JSON object"}
	}

	if file.Version != TableVersion {
		return nil, &ErrBadRoutes{Path: path, Why: fmt.Sprintf("version %d is not supported (expected %d)", file.Version, TableVersion)}
	}
	if file.Default != "" {
		if err := checkTarget(path, file.Default, "default"); err != nil {
			return nil, err
		}
		router.fallback = file.Default
	}
	if err := router.addChannelRules(path, file.Channels); err != nil {
		return nil, err
	}
	if err := router.addHTTPRules(path, file.HTTP); err != nil {
		return nil, err
	}
	return router, nil
}

// addChannelRules validates and installs the per-channel entries. Duplicate keys are already
// rejected by encoding/json's own strict decode of a JSON object into a map? No — JSON permits a
// repeated key and the last wins — so duplicates are detected here by scanning the raw object.
//
// Encoding into a map loses that information, so the scan is done on a second decode into
// json.RawMessage pairs. The cost is paid once at startup on a file that is a few hundred bytes.
func (r *Router) addChannelRules(path string, rules map[string]string) error {
	if err := checkDuplicateKeys(path, "channels"); err != nil {
		return err
	}
	names := make([]string, 0, len(rules))
	for name, route := range rules {
		if err := checkTarget(path, route, "channels."+name); err != nil {
			return err
		}
		if err := checkChannelName(path, name); err != nil {
			return err
		}
		r.channels[name] = route
		names = append(names, name)
	}
	if unknown := channels.Unknown(names); len(unknown) > 0 {
		sort.Strings(unknown)
		return &ErrUnknownChannel{Names: unknown}
	}
	return nil
}

// addHTTPRules installs the path rules. Prefixes are sorted longest-first so "/api/bots/" wins over
// "/api/" — otherwise a short rule could shadow a more specific one and the operator's intent
// would depend on map iteration order.
func (r *Router) addHTTPRules(path string, rules map[string]string) error {
	if err := checkDuplicateKeys(path, "http"); err != nil {
		return err
	}
	routes := make([]httpRoute, 0, len(rules))
	for prefix, route := range rules {
		if err := checkTarget(path, route, "http."+prefix); err != nil {
			return err
		}
		if !strings.HasPrefix(prefix, "/") {
			return &ErrBadRoutes{Path: path, Why: fmt.Sprintf("http prefix %q must start with '/'", prefix)}
		}
		routes = append(routes, httpRoute{prefix: prefix, route: route})
	}
	sort.Slice(routes, func(i, j int) bool {
		if len(routes[i].prefix) != len(routes[j].prefix) {
			return len(routes[i].prefix) > len(routes[j].prefix)
		}
		return routes[i].prefix < routes[j].prefix
	})
	r.http = routes
	return nil
}

// Channel returns the owner of a channel name. Unknown names get the table's default, which is
// "node" unless the file says otherwise — the strangler default.
func (r *Router) Channel(name string) string {
	if route, ok := r.channels[name]; ok {
		return route
	}
	return r.fallback
}

// ChannelOwnedByGo reports whether the Go process handles this channel.
func (r *Router) ChannelOwnedByGo(name string) bool { return r.Channel(name) == RouteGo }

// HTTPRoute returns the owner of a request path. Paths under a structurally Go-owned prefix always
// return RouteGo regardless of the file: those routes exist because Go implements them, and
// forwarding one would produce a second, competing answer.
func (r *Router) HTTPRoute(path string) string {
	for _, prefix := range HTTPRoutePrefixes {
		if strings.HasPrefix(path, prefix) {
			return RouteGo
		}
	}
	for _, exact := range HTTPRouteExactPaths {
		if path == exact {
			return RouteGo
		}
	}
	for _, route := range r.http {
		if strings.HasPrefix(path, route.prefix) {
			return route.route
		}
	}
	return r.fallback
}

// HTTPRouteOwnedByGo reports whether the Go process handles this path.
func (r *Router) HTTPRouteOwnedByGo(path string) bool { return r.HTTPRoute(path) == RouteGo }

// Source names the file the table came from, for the startup log.
func (r *Router) Source() string { return r.source }

// ExplicitChannels lists the channels the file named, sorted. It is a diagnostic: without it an
// operator who wrote a table has no way to confirm what was applied.
func (r *Router) ExplicitChannels() []string {
	out := make([]string, 0, len(r.channels))
	for name := range r.channels {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// checkTarget rejects anything that is not exactly "go" or "node".
func checkTarget(path, route, where string) error {
	if route != RouteGo && route != RouteNode {
		return &ErrBadRoutes{Path: path, Why: fmt.Sprintf("%s: %q is not a valid target (use %q or %q)", where, route, RouteGo, RouteNode)}
	}
	return nil
}

// checkChannelName rejects an empty or whitespace-only channel name, which could never match a
// real service channel and is therefore a typo.
func checkChannelName(path, name string) error {
	if strings.TrimSpace(name) == "" {
		return &ErrBadRoutes{Path: path, Why: "channels: a channel name must not be empty"}
	}
	return nil
}

// checkDuplicateKeys rejects a JSON object that names the same key twice. encoding/json keeps the
// last occurrence silently, so a table with a duplicated channel would route it to whichever the
// operator happened to write last and report success for the first entry — a silent config bug the
// only defense against is reading the raw bytes.
func checkDuplicateKeys(path, field string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return &ErrBadRoutes{Path: path, Why: err.Error()}
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return &ErrBadRoutes{Path: path, Why: err.Error()}
	}
	entries, err := rawObjectEntries(doc[field])
	if err != nil {
		return &ErrBadRoutes{Path: path, Why: err.Error()}
	}
	seen := map[string]bool{}
	dupes := []string{}
	for _, entry := range entries {
		if seen[entry] {
			dupes = append(dupes, entry)
		}
		seen[entry] = true
	}
	if len(dupes) > 0 {
		return &ErrBadRoutes{Path: path, Why: fmt.Sprintf("%s: duplicate keys %s", field, strings.Join(dupes, ", "))}
	}
	return nil
}
