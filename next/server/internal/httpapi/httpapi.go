// Package httpapi implements the browser-facing HTTP surface: token login,
// session middleware, /api/server-info and the static SPA.
//
// Behaviour mirrors packages/server/src/http.ts and
// packages/server/src/webAuth.ts so the existing web client works unchanged.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nex-harness/nex/server/internal/auth"
	"github.com/nex-harness/nex/server/internal/installer"
	"github.com/nex-harness/nex/server/internal/runners"
	"github.com/nex-harness/nex/server/internal/runnerupdate"
)

// maxRequestBody bounds login bodies.
const maxRequestBody = 64 << 10

// maxTokenLength mirrors MAX_TOKEN_LENGTH in webAuth.ts.
const maxTokenLength = 1024

// ServerInfo is the /api/server-info payload
// (packages/shared/src/server-remote.ts:11-27).
type ServerInfo struct {
	ServerID        string                  `json:"serverId"`
	Name            string                  `json:"name,omitempty"`
	Version         string                  `json:"version"`
	ProtocolVersion int                     `json:"protocolVersion"`
	AuthRequired    bool                    `json:"authRequired"`
	Workspaces      []runners.WorkspaceInfo `json:"workspaces"`
	Capabilities    Capabilities            `json:"capabilities"`
}

// Capabilities mirrors the Node server's reported capability set.
type Capabilities struct {
	DesktopContinuous        bool `json:"desktopContinuous"`
	WebsocketRPC             bool `json:"websocketRpc"`
	ProcessResourceTelemetry bool `json:"processResourceTelemetry"`
}

// Deps are the HTTP layer collaborators.
type Deps struct {
	Logger     *slog.Logger
	Auth       *auth.Service
	Registry   *runners.Registry
	ServerID   string
	ServerName string
	Version    string
	WebRoot    string
	// BrowserWS serves GET /ws.
	BrowserWS http.Handler
	// RunnerWS serves GET /runner.
	RunnerWS http.Handler
	// Config serves the authenticated config import endpoint. Nil disables it
	// (unit tests that do not assemble the config manager).
	Config ConfigAdmin
	// Skills is the optional skill/prompt upload surface (P2). Nil disables the
	// upload routes.
	Skills SkillUploader
	// Installs is the optional SSH runner-install surface
	// (docs/next/runner-install.md). Nil disables the install routes.
	Installs *installer.Manager
	// RunnerToken authenticates the runner bundle download endpoint
	// (docs/next/runner-update.md decision 5). Empty disables that route.
	RunnerToken string
	// Updates is the optional self-update surface. Nil disables the bundle
	// download and the manual update routes.
	Updates *runnerupdate.Manager
}

// SkillUploader stores skill/prompt bundles from the HTTP upload helpers.
type SkillUploader interface {
	// Upload decodes one body ("content" or "files") for kind "skill"/"prompt"
	// and returns the doc version and digest.
	Upload(ctx context.Context, kind, scope, runnerID, name string, body []byte) (any, error)
	// Delete removes a bundle by name.
	Delete(ctx context.Context, kind, scope, runnerID, name string) error
}

// RunnerModelStatus is one row of GET /api/config/runners
// (docs/next/model-onboarding.md "API"). It is a plain data copy so the HTTP
// layer does not depend on internal/local.
//
// The build/update fields (docs/next/runner-update.md decision 4) are filled in
// by the HTTP layer from the registry hello and the runnerupdate manager, so
// internal/local stays unaware of the update feature.
type RunnerModelStatus struct {
	RunnerID           string `json:"runnerId"`
	Name               string `json:"name"`
	Connected          bool   `json:"connected"`
	Mode               string `json:"mode"`
	ModelCount         int    `json:"modelCount"`
	LastAppliedVersion uint64 `json:"lastAppliedVersion"`
	// BuildID is the build the runner reported in hello ("" for a legacy
	// runner or one that is offline).
	BuildID string `json:"buildId"`
	// LatestBuildID is the build staged for the runner's platform.
	LatestBuildID string `json:"latestBuildId"`
	// Outdated is true when both ids are known and differ.
	Outdated bool `json:"outdated"`
	// Capabilities are the runner's advertised optional features.
	Capabilities []string `json:"capabilities"`
	// UpdateState is idle | downloading | restarting | failed.
	UpdateState string `json:"updateState"`
	// UpdateError carries the runner's failure text for updateState=failed.
	UpdateError string `json:"updateError,omitempty"`
}

// ConfigAdmin is the server-side config surface the HTTP layer exposes.
type ConfigAdmin interface {
	// ImportFromRunner imports one runner's local providers/keys into its scope
	// (scope "" or "runner") or the shared global scope.
	ImportFromRunner(ctx context.Context, runnerID, scope string) (any, error)
	// KnownRunners lists runners with a live link or recorded facts.
	KnownRunners() []string
	// RunnerModelStatuses lists every known runner with its name, mode and the
	// model count model-selection would offer it.
	RunnerModelStatuses() []RunnerModelStatus
}

// Server is the HTTP mux.
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

// New builds the HTTP server. A missing web root is allowed; non-API paths
// then answer 404.
func New(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Version == "" {
		deps.Version = "0.0.0-dev"
	}
	if deps.ServerName == "" {
		deps.ServerName = "nex-next-server"
	}
	server := &Server{deps: deps, mux: http.NewServeMux()}
	server.routes()
	return server
}

// Handler returns the root handler with auth middleware applied.
func (s *Server) Handler() http.Handler {
	return s.authMiddleware(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/auth/session", s.handleAuthSession)
	s.mux.HandleFunc("/api/auth/login", s.handleLogin)
	s.mux.HandleFunc("/api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("/api/server-info", s.handleServerInfo)
	if s.deps.Config != nil {
		s.mux.HandleFunc("/api/config/import-runner", s.handleImportRunner)
		s.mux.HandleFunc("/api/config/runners", s.handleConfigRunners)
		s.mux.HandleFunc("/api/config/status", s.handleConfigStatus)
	}
	// Self-update (docs/next/runner-update.md decision 5): the bundle download
	// authenticates with the runner token, the manual trigger with the browser
	// session. Both need a configured bundle dir — without one there is nothing
	// to serve and nothing to send, so the routes stay hidden like the install
	// routes.
	if s.deps.Updates != nil && s.deps.Updates.Enabled() {
		s.mux.HandleFunc("/api/runner-bundle/", s.handleRunnerBundle)
		s.mux.HandleFunc("/api/runner-update", s.handleRunnerUpdate)
	}
	if s.deps.Skills != nil {
		s.mux.HandleFunc("/api/config/skills", s.handleSkillUpload("skill"))
		s.mux.HandleFunc("/api/config/skills/delete", s.handleSkillDelete("skill"))
		s.mux.HandleFunc("/api/config/prompts", s.handleSkillUpload("prompt"))
		s.mux.HandleFunc("/api/config/prompts/delete", s.handleSkillDelete("prompt"))
		if s.deps.Installs != nil {
			s.mux.HandleFunc("/api/runner-install", s.handleRunnerInstall)
			s.mux.HandleFunc("/api/runner-install/", s.handleRunnerInstallGet)
		}
	}
	if s.deps.BrowserWS != nil {
		s.mux.Handle("/ws", s.deps.BrowserWS)
		s.mux.Handle("/ws/", s.deps.BrowserWS)
	}
	if s.deps.RunnerWS != nil {
		s.mux.Handle("/runner", s.deps.RunnerWS)
	}
	s.mux.HandleFunc("/", s.handleStatic)
}

// isTokenProtectedPath mirrors webAuth.ts: /ws and /api/* are protected,
// except /api/auth/*.
func isTokenProtectedPath(pathname string) bool {
	if isAuthRoute(pathname) {
		return false
	}
	if isRunnerTokenRoute(pathname) {
		// 修复/约束：runner 下载 bundle 只带 runner token，没有浏览器 session。
		// 若仍走浏览器鉴权，runner 会拿到 401；这里改为由 handler 自己做
		// 常量时间的 runner token 比较（docs/next/runner-update.md Security）。
		return false
	}
	return pathname == "/ws" || strings.HasPrefix(pathname, "/ws/") || strings.HasPrefix(pathname, "/api/")
}

// isRunnerTokenRoute reports the paths authenticated by the runner bearer
// token rather than by a browser session.
func isRunnerTokenRoute(pathname string) bool {
	return pathname == "/api/runner-bundle" || strings.HasPrefix(pathname, "/api/runner-bundle/")
}

func isAuthRoute(pathname string) bool {
	return pathname == "/api/auth" || strings.HasPrefix(pathname, "/api/auth/")
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Runner-token routes carry no browser session and must not mint one:
		// the handler compares the runner bearer token in constant time.
		if isRunnerTokenRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.deps.Auth.Enabled() {
			next.ServeHTTP(w, r)
			return
		}

		protected := isTokenProtectedPath(r.URL.Path)
		queryToken, hasQueryToken := r.URL.Query()["token"]
		if hasQueryToken {
			key := clientIP(r)
			limiter := s.deps.Auth.Limiter()
			if limiter.LockedFor(key) > 0 {
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many attempts"})
				return
			}
			provided := ""
			if len(queryToken) > 0 {
				provided = queryToken[0]
			}
			if s.deps.Auth.ValidateToken(provided) {
				limiter.RecordSuccess(key)
				// Only page requests get a session cookie; programmatic clients
				// pass ?token= on every request and must not mint sessions.
				// The cookie is set before the handler runs because Go response
				// headers cannot be added after the body has been written.
				if !protected && !isWebSocketPath(r.URL.Path) {
					sessionID, err := s.deps.Auth.CreateSession()
					if err != nil {
						s.deps.Logger.Warn("failed to create session from token query", "error", err)
					} else {
						w.Header().Add("Set-Cookie", sessionCookie(r, sessionID))
					}
				}
				next.ServeHTTP(w, r)
				return
			}
			limiter.RecordFailure(key)
			if protected {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
				return
			}
			// A wrong token on a page request still shows the login page.
			next.ServeHTTP(w, r)
			return
		}

		if !protected {
			next.ServeHTTP(w, r)
			return
		}
		if s.deps.Auth.ValidateSession(readSessionID(r)) {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
	})
}

func isWebSocketPath(pathname string) bool {
	return pathname == "/ws" || strings.HasPrefix(pathname, "/ws/")
}

func readSessionID(r *http.Request) string {
	cookies := parseCookieHeader(r.Header.Get("Cookie"))
	return cookies[auth.SessionCookieName]
}

func parseCookieHeader(header string) map[string]string {
	out := make(map[string]string)
	if header == "" {
		return out
	}
	for _, part := range strings.Split(header, ";") {
		separator := strings.IndexByte(part, '=')
		if separator <= 0 {
			continue
		}
		name := strings.TrimSpace(part[:separator])
		value := strings.TrimSpace(part[separator+1:])
		if name == "" {
			continue
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			out[name] = value
			continue
		}
		out[name] = decoded
	}
	return out
}

// isSecureRequest reports whether the browser reached us over HTTPS.
//
// X-Forwarded-Proto is only consulted for proxies that overwrite it. When a
// proxy appends instead, every value after the first is written by the proxy
// and the last one is the hop closest to us; using the first would let a client
// send "X-Forwarded-Proto: http" and suppress the Secure cookie attribute on an
// HTTPS deployment.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	values := r.Header.Values("X-Forwarded-Proto")
	if len(values) == 0 {
		return false
	}
	var last string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				last = trimmed
			}
		}
	}
	return strings.EqualFold(last, "https")
}

func sessionCookie(r *http.Request, sessionID string) string {
	attributes := []string{
		auth.SessionCookieName + "=" + url.QueryEscape(sessionID),
		"Path=/",
		"HttpOnly",
		"SameSite=Lax",
		fmt.Sprintf("Max-Age=%d", int(auth.SessionTTL/time.Second)),
	}
	if isSecureRequest(r) {
		attributes = append(attributes, "Secure")
	}
	return strings.Join(attributes, "; ")
}

func clearedSessionCookie(r *http.Request) string {
	attributes := []string{
		auth.SessionCookieName + "=",
		"Path=/",
		"HttpOnly",
		"SameSite=Lax",
		"Max-Age=0",
	}
	if isSecureRequest(r) {
		attributes = append(attributes, "Secure")
	}
	return strings.Join(attributes, "; ")
}

// clientIP keys the login rate limiter.
//
// 修复：反向代理后所有客户端的 TCP 对端都是代理地址，5 次错误登录会把所有人一起锁 60 秒。
// 只有显式配置 NEX_TRUSTED_PROXIES 时才读取 X-Forwarded-For，并且只信任受信代理追加的
// 那一跳（从右往左跳过受信代理），客户端无法伪造自己的限流 key；未配置时仍只用 TCP 对端。
func clientIP(r *http.Request) string {
	return limiterKey(r)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	authenticated := false
	if s.deps.Auth.Enabled() {
		authenticated = s.deps.Auth.ValidateSession(readSessionID(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authRequired":  s.deps.Auth.Enabled(),
		"authenticated": authenticated,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !s.deps.Auth.Enabled() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Authentication is not enabled"})
		return
	}

	key := clientIP(r)
	limiter := s.deps.Auth.Limiter()
	if locked := limiter.LockedFor(key); locked > 0 {
		retryAfter := int((locked + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many attempts"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	var payload struct {
		Token *string `json:"token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Token == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	provided := *payload.Token
	if provided == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	if len(provided) > maxTokenLength || !s.deps.Auth.ValidateToken(strings.TrimSpace(provided)) {
		limiter.RecordFailure(key)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid token"})
		return
	}

	limiter.RecordSuccess(key)
	sessionID, err := s.deps.Auth.CreateSession()
	if err != nil {
		s.deps.Logger.Warn("failed to create session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create session"})
		return
	}
	w.Header().Set("Set-Cookie", sessionCookie(r, sessionID))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.deps.Auth.RevokeSession(readSessionID(r))
	w.Header().Set("Set-Cookie", clearedSessionCookie(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	workspaces := s.deps.Registry.Workspaces()
	if workspaces == nil {
		workspaces = []runners.WorkspaceInfo{}
	}
	writeJSON(w, http.StatusOK, ServerInfo{
		ServerID:        s.deps.ServerID,
		Name:            s.deps.ServerName,
		Version:         s.deps.Version,
		ProtocolVersion: 1,
		AuthRequired:    s.deps.Auth.Enabled(),
		Workspaces:      workspaces,
		Capabilities: Capabilities{
			DesktopContinuous:        true,
			WebsocketRPC:             true,
			ProcessResourceTelemetry: false,
		},
	})
}

// handleImportRunner imports one runner's local ~/.pi/agent providers. The UI has
// no import button; this endpoint is the documented alternative (README).
//
//	POST /api/config/import-runner?runnerId=alpha[&scope=global]
//
// scope defaults to "runner" (that runner's scope, the historical behaviour);
// "global" writes the shared layer so Settings can edit the provider and every
// runner receives it. An unknown scope is 400 and an unknown runner is 404.
func (s *Server) handleImportRunner(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runnerID := strings.TrimSpace(r.URL.Query().Get("runnerId"))
	if runnerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "runnerId is required"})
		return
	}
	scope, err := importScope(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	known := false
	for _, id := range s.deps.Config.KnownRunners() {
		if id == runnerID {
			known = true
			break
		}
	}
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "runner offline"})
		return
	}
	summary, err := s.deps.Config.ImportFromRunner(r.Context(), runnerID, scope)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// importScope normalizes the import scope: empty means "runner".
func importScope(r *http.Request) (string, error) {
	switch scope := strings.TrimSpace(r.URL.Query().Get("scope")); scope {
	case "":
		return "runner", nil
	case "global", "runner":
		return scope, nil
	default:
		return "", fmt.Errorf("scope must be global or runner")
	}
}

// handleConfigRunners reports each known runner's name, mode, model count and
// self-update state (docs/next/model-onboarding.md decision 1: the install
// wizard's Models step polls this; docs/next/runner-update.md decision 4 adds
// the build/update fields).
//
//	GET /api/config/runners → {runners:[{runnerId, name, connected, mode,
//	                                modelCount, lastAppliedVersion,
//	                                buildId, latestBuildId, outdated,
//	                                capabilities, updateState, updateError}]}
func (s *Server) handleConfigRunners(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	statuses := s.deps.Config.RunnerModelStatuses()
	if statuses == nil {
		statuses = []RunnerModelStatus{}
	}
	// Build/update facts come from the registry hello + the staged bundles. The
	// local manager does not know either, so they are composed here rather than
	// threaded through the config package.
	for index := range statuses {
		status := &statuses[index]
		if status.Capabilities == nil {
			status.Capabilities = []string{}
		}
		status.UpdateState = runnerupdate.StateIdle
		info, live := s.deps.Registry.RunnerInfo(status.RunnerID)
		if !live {
			// An offline runner has no hello this process; recorded facts do not
			// carry a build id, so the row stays "unknown build, idle".
			continue
		}
		status.BuildID = info.BuildID
		status.Capabilities = append([]string{}, info.Capabilities...)
		if s.deps.Updates != nil {
			if latest, ok := s.deps.Updates.Latest(info); ok {
				status.LatestBuildID = latest
			}
			status.Outdated = s.deps.Updates.Outdated(info)
			update := s.deps.Updates.Status(status.RunnerID)
			if update.State != "" {
				status.UpdateState = update.State
			}
			status.UpdateError = update.Error
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runners": statuses})
}

// bundleFiles is the fixed allow-list of downloadable bundle members
// (docs/next/runner-update.md decision 5). The request path never reaches the
// filesystem except through this map, so no traversal is expressible.
var bundleFiles = map[string]string{
	"runner.mjs":            "text/javascript; charset=utf-8",
	"pty-host.mjs":          "text/javascript; charset=utf-8",
	"lib/ptyHostAssets.mjs": "text/javascript; charset=utf-8",
	"build.json":            "application/json; charset=utf-8",
	"package.json":          "application/json; charset=utf-8",
}

// handleRunnerBundle serves one allow-listed file from the bundle directory of
// the runner's own platform:
//
//	GET /api/runner-bundle/<platform>/<file>   (Authorization: Bearer <runner token>)
//
// The runner token is the link token (docs/next/runner-update.md Security): no
// browser credential can fetch runner code. Unknown platforms, unknown files and
// a missing bundle dir are 404 so the endpoint reveals nothing about the host.
func (s *Server) handleRunnerBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// 常量时间比较，与 runnerws 的 Bearer 校验一致；空 token 永远拒绝。
	if s.deps.RunnerToken == "" || !auth.TokensEqual(s.deps.RunnerToken, bearerToken(r)) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	if s.deps.Updates == nil || !s.deps.Updates.Enabled() {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/runner-bundle/")
	// PathUnescape: Go's mux already decoded the path, but a client may send an
	// encoded separator; unescaping once more then rejecting ".." keeps the
	// allow-list the only way in.
	rest = strings.Trim(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	platform, file := parts[0], parts[1]
	if !runnerupdate.KnownPlatform(platform) {
		http.NotFound(w, r)
		return
	}
	contentType, allowed := bundleFiles[file]
	if !allowed {
		http.NotFound(w, r)
		return
	}

	// Build the path from fixed parts only: platform and file both come from
	// allow-lists above.
	bundleDir := filepath.Join(s.deps.Updates.Bundles.Dir, platform)
	target := filepath.Join(bundleDir, filepath.FromSlash(file))
	if !isInsideDirectory(bundleDir, target) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	handle, err := os.Open(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = handle.Close() }()

	w.Header().Set("Content-Type", contentType)
	// Bundle content is content-addressed by buildId; a runner always wants the
	// bytes it just asked for, never a stale proxy copy.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(file), info.ModTime(), handle)
}

// bearerToken returns the runner bearer credential.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return header[len(prefix):]
	}
	return ""
}

// handleRunnerUpdate sends the manual self-update frame
// (docs/next/runner-update.md decision 5):
//
//	POST /api/runner-update?runnerId=<id>
//
// 404 unknown/offline runner, 409 when the runner is too old to self-update,
// 200 {sent:false, upToDate:true} when it already runs the staged build, and
// 200 {sent:true, buildId} otherwise. A manual send is allowed even when the
// connection already carried an automatic one.
func (s *Server) handleRunnerUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runnerID := strings.TrimSpace(r.URL.Query().Get("runnerId"))
	if runnerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "runnerId is required"})
		return
	}
	info, live := s.deps.Registry.RunnerInfo(runnerID)
	if !live {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "runner offline"})
		return
	}
	if !runnerupdate.CanSelfUpdate(info) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "runner too old to self-update; reinstall"})
		return
	}
	latest, known := s.deps.Updates.Latest(info)
	if !known {
		// 该平台没有 build.json：server 无法证明存在更新的构建，按"已是最新"
		// 返回，与 /api/config/runners 里 outdated=false 保持一致，不发帧。
		writeJSON(w, http.StatusOK, map[string]any{"sent": false, "upToDate": true})
		return
	}
	// 空 buildId 是"未知旧构建"，仍可手动更新；其余按 Outdated 判定。dev 构建
	// 永远不是比较目标，因此视为已是最新。
	if strings.TrimSpace(info.BuildID) != "" && !s.deps.Updates.Outdated(info) {
		writeJSON(w, http.StatusOK, map[string]any{"sent": false, "upToDate": true})
		return
	}
	link, ok := s.deps.Registry.Runner(runnerID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "runner offline"})
		return
	}
	sender, ok := link.(interface{ SendUpdate(string) error })
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "runner link cannot receive updates"})
		return
	}
	if err := sender.SendUpdate(latest); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	s.deps.Logger.Info("manual runner update requested", "runnerId", runnerID,
		"from", info.BuildID, "to", latest)
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "buildId": latest})
}

// handleConfigStatus reports the doc version and per-runner applied state
// (never provider keys).
func (s *Server) handleConfigStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runners := s.deps.Config.KnownRunners()
	writeJSON(w, http.StatusOK, map[string]any{
		"runners": runners,
	})
}

// maxUploadBytes bounds one skill/prompt upload body.
const maxUploadBytes = 4 << 20

// handleSkillUpload stores one bundle (POST). There is no upload button in the
// unchanged Skills page, so this is the documented import path
// (docs/next/config-mcp-skills.md "Upload"):
//
//	POST /api/config/skills?name=pdf-tools  (body: SKILL.md text or {"files":[…]
//	POST /api/config/prompts?name=review    (body: markdown text)
//
// `scope=runner&runnerId=<id>` writes the runner scope instead of global.
func (s *Server) handleSkillUpload(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		scope, runnerID, err := uploadScope(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxUploadBytes+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(body) > maxUploadBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "upload is too large"})
			return
		}
		result, err := s.deps.Skills.Upload(r.Context(), kind, scope, runnerID, name, body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *Server) handleSkillDelete(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		scope, runnerID, err := uploadScope(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.deps.Skills.Delete(r.Context(), kind, scope, runnerID, name); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
	}
}

func uploadScope(r *http.Request) (string, string, error) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	runnerID := strings.TrimSpace(r.URL.Query().Get("runnerId"))
	if scope == "runner" && runnerID == "" {
		return "", "", fmt.Errorf("runnerId is required with scope=runner")
	}
	if scope != "" && scope != "global" && scope != "runner" {
		return "", "", fmt.Errorf("scope must be global or runner")
	}
	return scope, runnerID, nil
}

var staticMimeTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".gif":   "image/gif",
	".html":  "text/html; charset=utf-8",
	".ico":   "image/x-icon",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
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

func staticContentType(filePath string) string {
	if contentType, ok := staticMimeTypes[strings.ToLower(filepath.Ext(filePath))]; ok {
		return contentType
	}
	if detected := mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath))); detected != "" {
		return detected
	}
	return "application/octet-stream"
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	root := strings.TrimSpace(s.deps.WebRoot)
	if root == "" {
		http.NotFound(w, r)
		return
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	filePath, ok := resolveStaticFile(absRoot, r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	// index.html must never be cached so deploys roll out immediately; hashed
	// assets are immutable for a year, matching the Node server.
	if strings.HasSuffix(filePath, "index.html") {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("Content-Type", staticContentType(filePath))

	file, err := os.Open(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = file.Close() }()
	http.ServeContent(w, r, filepath.Base(filePath), info.ModTime(), file)
}

// resolveStaticFile maps a URL path to a file inside root with SPA fallback
// to index.html. Any ".." segment is rejected outright: path.Clean would
// otherwise fold it into the root and silently serve index.html instead of a
// 404 (the Node server resolves and rejects escaping paths).
func resolveStaticFile(root, urlPath string) (string, bool) {
	for _, segment := range strings.Split(urlPath, "/") {
		if segment == ".." {
			return "", false
		}
	}
	clean := path.Clean("/" + urlPath)
	if clean == "/" {
		clean = "/index.html"
	}
	relative := strings.TrimPrefix(clean, "/")
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	if !isInsideDirectory(root, candidate) {
		return "", false
	}
	if info, err := os.Stat(candidate); err == nil {
		if info.IsDir() {
			index := filepath.Join(candidate, "index.html")
			if isInsideDirectory(root, index) {
				if indexInfo, err := os.Stat(index); err == nil && indexInfo.Mode().IsRegular() {
					return index, true
				}
			}
		} else if info.Mode().IsRegular() {
			return candidate, true
		}
	}

	// API paths never fall back to index.html.
	if strings.HasPrefix(clean, "/api/") {
		return "", false
	}
	index := filepath.Join(root, "index.html")
	if info, err := os.Stat(index); err == nil && info.Mode().IsRegular() {
		return index, true
	}
	return "", false
}

func isInsideDirectory(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (!strings.HasPrefix(relative, "..") && !strings.Contains(relative, ".."+string(filepath.Separator)))
}

// RandomServerID returns a stable-looking random id for a first run. The
// caller persists it in the data directory.
func RandomServerID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generating server id: %w", err)
	}
	return "nex-" + hex.EncodeToString(raw[:]), nil
}

// handleRunnerInstall starts (or joins) one SSH runner install
// (docs/next/runner-install.md decision 8). The password is memory-only: it
// is accepted on this request, used for the single SSH session, and never
// persisted, logged, or echoed back.
func (s *Server) handleRunnerInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	var payload installer.SSHTarget
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	if err := payload.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if payload.Name != "" && strings.ContainsAny(payload.Name, "'\n\"\\") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be a single line without quotes"})
		return
	}
	session, started := s.deps.Installs.Start(payload)
	s.deps.Logger.Info("runner install requested",
		"target", session.Target, "installId", session.ID, "started", started)
	writeJSON(w, http.StatusOK, map[string]string{"installId": session.ID})
}

// handleRunnerInstallGet reports one install session's status + logs.
func (s *Server) handleRunnerInstallGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/runner-install/")
	id = strings.Trim(id, "/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "install id is required"})
		return
	}
	session, ok := s.deps.Installs.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown install id"})
		return
	}
	writeJSON(w, http.StatusOK, session.View())
}
