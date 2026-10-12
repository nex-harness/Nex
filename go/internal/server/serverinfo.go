package server

import (
	"encoding/json"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tylinux/nex/go/internal/adapters/serverenv"
)

// ProtocolVersion is SERVER_REMOTE_PROTOCOL_VERSION from packages/shared/src/server-remote.ts.
// It is pinned to 1; the schema is a z.literal so a mismatch would be rejected on the client.
const ProtocolVersion = 1

// ServerRemoteInfo is the wire shape of GET /api/server-info, matching
// serverRemoteInfoSchema in packages/shared/src/server-remote.ts. Field order matters: the
// response is compared byte for byte against the recorded Node responses, and encoding/json
// emits struct fields in declaration order.
//
// `name` is omitted (not null) when empty, which Go's `omitempty` reproduces.
type ServerRemoteInfo struct {
	ServerID        string                   `json:"serverId"`
	Name            string                   `json:"name,omitempty"`
	Version         string                   `json:"version"`
	ProtocolVersion int                      `json:"protocolVersion"`
	AuthRequired    bool                     `json:"authRequired"`
	Workspaces      []ServerRemoteWorkspace  `json:"workspaces"`
	Capabilities    ServerRemoteCapabilities `json:"capabilities"`
}

// ServerRemoteWorkspace is one advertised workspace (serverRemoteWorkspaceInfoSchema).
// Label is omitted when empty, matching the optional label in the schema.
type ServerRemoteWorkspace struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
}

// ServerRemoteCapabilities is the capabilities object. The first two are z.literal(true) in
// the schema, so they are not omitempty; processResourceTelemetry is an optional boolean that
// the Go front always advertises.
type ServerRemoteCapabilities struct {
	DesktopContinuous        bool `json:"desktopContinuous"`
	WebSocketRPC             bool `json:"websocketRpc"`
	ProcessResourceTelemetry bool `json:"processResourceTelemetry"`
}

// errURIMalformed represents JS's URIError thrown by decodeURIComponent. It is defined here
// so static.go and the (future) auth code share one value; the message text matches V8 so a
// log line reads the same in both stacks.
type urIError struct{}

func (e *urIError) Error() string { return "URIError: URI malformed" }

var errURIMalformed = &urIError{}

// NewServerInfo reproduces `createServerInfo` from packages/server/src/http.ts.
//
// KNOWN INCONSISTENCY, reproduced on purpose: createServerInfo falls back to
// NEX_SERVER_TOKEN when options.authRequired is unset, while entry-http.ts sets
// authRequired from NEX_SERVER_AUTH_TOKEN. So a deployment that exports only
// NEX_SERVER_AUTH_TOKEN reports authRequired:false here, and one that exports only
// NEX_SERVER_TOKEN reports authRequired:true while web auth is actually disabled. This is a
// pre-existing TypeScript defect, not a porting choice; fixing it here would change observable
// behavior for existing clients. It is recorded in docs/specs/go-rewrite/decisions.md as an
// open item for a maintainer decision.
func NewServerInfo(cfg *Config) ServerRemoteInfo {
	authRequired := cfg.AuthRequired
	if !authRequired {
		authRequired = trimmedEnv("NEX_SERVER_TOKEN") != ""
	}
	return ServerRemoteInfo{
		ServerID:        cfg.ServerID,
		Name:            cfg.Name,
		Version:         Version,
		ProtocolVersion: ProtocolVersion,
		AuthRequired:    authRequired,
		Workspaces:      resolveServerWorkspaces(cfg),
		Capabilities: ServerRemoteCapabilities{
			DesktopContinuous:        true,
			WebSocketRPC:             true,
			ProcessResourceTelemetry: true,
		},
	}
}

// resolveServerWorkspaces mirrors resolveServerWorkspaces: no implicit default (an empty
// NEX_SERVER_WORKSPACE yields no workspaces), label = basename(path) or the path itself when
// the basename is empty (e.g. a trailing slash).
func resolveServerWorkspaces(cfg *Config) []ServerRemoteWorkspace {
	// readTrimmedEnv trims before the emptiness test, so a whitespace-only
	// NEX_SERVER_WORKSPACE registers nothing rather than a blank workspace.
	workspace := strings.TrimSpace(cfg.Workspace)
	if workspace == "" {
		return []ServerRemoteWorkspace{}
	}
	// label = basename(path), falling back to the path itself when the basename is empty
	// (a bare "/", or a value that normalizes away to ".").
	label := path.Base(workspace)
	if label == "." || label == "/" || label == string(filepath.Separator) || label == "" {
		label = workspace
	}
	return []ServerRemoteWorkspace{{Path: workspace, Label: label}}
}

// writeServerInfo emits the JSON body with the exact headers Hono's c.json produces:
// application/json and no cache directives.
func writeServerInfo(w http.ResponseWriter, info ServerRemoteInfo) {
	body, err := json.Marshal(info)
	if err != nil {
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// trimmedEnv reads an environment variable the way http.ts's readTrimmedEnv does: trim, and
// treat empty/whitespace-only as unset. The access itself lives in the serverenv adapter so
// this package imports no "os".
func trimmedEnv(name string) string {
	return strings.TrimSpace(serverenv.Trimmed(name))
}

// hostname mirrors os.hostname() with the same fallback ordering as resolveServerId:
// hostname, then the literal "nex-server" when the OS reports an empty name.
func hostname() string {
	return serverenv.Hostname()
}
