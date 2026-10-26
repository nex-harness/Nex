package local

// `mcp-sync` from the config doc (docs/next/config-mcp-skills.md P1; research
// §A2/C5).
//
// `loadMcpFromUserDirectory` / `saveMcpToUserDirectory` are server-owned: the
// doc's effective MCP list for the calling runner. `listWorkspaceMcpServerStatuses`
// is forwarded by the router to the owning runner (live status); this channel
// only answers an empty list when the doc has no servers, so an older runner
// that lacks the method cannot leave the callback unanswered.
//
// Secret values never reach the browser: a header/env value that is a stored
// secret is shown with the same mask the provider view uses.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

// McpSync serves the mcp-sync channel.
type McpSync struct {
	manager *Manager
}

func newMcpSync(manager *Manager) *McpSync {
	return &McpSync{manager: manager}
}

// Call implements Channel.
func (m *McpSync) Call(ctx context.Context, _ *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	switch method {
	case "loadMcpFromUserDirectory":
		return m.load(arg)
	case "saveMcpToUserDirectory":
		return m.save(arg)
	case "listWorkspaceMcpServerStatuses", "listMcpServerStatuses":
		// `listMcpServerStatuses` is the protocol alias of the status call
		// (packages/services → `mcp/list`); the unchanged UI reaches it through
		// `mcp-sync` and both names carry the same workspace target.
		return m.statuses(ctx, arg)
	case "listLocalUserMcpCandidates":
		return json.RawMessage(`{"servers":[],"candidates":[]}`), nil
	case "listRemoteUserMcpStatuses":
		return json.RawMessage(`{"statuses":[]}`), nil
	case "exportMcpServers":
		return json.RawMessage(`{"content":""}`), nil
	case "checkRemoteUserMcpWriteAccess":
		return json.RawMessage(`{"allowed":false}`), nil
	}
	return nil, &MethodNotFoundError{Method: method}
}

// Listen implements Channel.
func (m *McpSync) Listen(context.Context, *Env, string, rpcwire.Value, func(rpcwire.Value)) (func(), error) {
	return nil, nil
}

func (m *McpSync) load(arg rpcwire.Value) (rpcwire.Value, error) {
	runnerID := m.manager.runnerFor(readViewTarget(arg))
	doc, err := m.manager.store.Snapshot()
	if err != nil {
		return nil, err
	}
	effective := doc.EffectiveForRunner(runnerID)
	servers := []any{}
	secrets := m.manager.store.Secrets()
	for _, server := range effective.MCPServerList() {
		row := map[string]any{
			"source":  "nexagentmcp",
			"scope":   "user",
			"name":    server.Name,
			"config":  m.mcpConfigView(server, secrets),
			"enabled": server.Enabled,
		}
		// The settings list has no warning slot; entries pi would reject (a
		// legacy SSE kind written directly into the doc) are surfaced through
		// `error` on the row.
		if kind := server.Transport.Kind; kind == "sse" || kind == "streamableHttp" {
			row["error"] = fmt.Sprintf("transport %q is served as http (pi rejects legacy SSE)", kind)
		}
		servers = append(servers, row)
	}
	body, err := json.Marshal(map[string]any{"servers": servers})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// mcpConfigView builds the UI's `McpServerConfig` (packages/shared/src/mcp.ts)
// for one doc entry: stdio fields or url/headers, secrets masked.
func (m *McpSync) mcpConfigView(server *configstore.McpServer, secrets *configstore.SecretBox) map[string]any {
	transport := server.Transport
	config := map[string]any{}
	switch transport.Kind {
	case configstore.MCPKindHTTP, "sse", "streamableHttp":
		config["type"] = "http"
		config["url"] = transport.URL
		if headers := maskMcpValues(transport.Headers, secrets); len(headers) > 0 {
			config["headers"] = headers
		}
		if transport.OAuth != nil {
			oauth := map[string]any{}
			if transport.OAuth.ClientID != "" {
				oauth["clientId"] = transport.OAuth.ClientID
			}
			if transport.OAuth.ClientSecret != "" {
				oauth["clientSecret"] = maskMcpValue(transport.OAuth.ClientSecret, secrets)
			}
			if transport.OAuth.Scope != "" {
				oauth["scope"] = transport.OAuth.Scope
			}
			if transport.OAuth.CallbackPort != 0 {
				oauth["callbackPort"] = transport.OAuth.CallbackPort
			}
			if transport.OAuth.CallbackURL != "" {
				oauth["callbackUrl"] = transport.OAuth.CallbackURL
			}
			if transport.OAuth.AuthServerMetadataURL != "" {
				oauth["authServerMetadataUrl"] = transport.OAuth.AuthServerMetadataURL
			}
			config["oauth"] = oauth
		}
	default:
		config["type"] = "stdio"
		config["command"] = transport.Command
		if len(transport.Args) > 0 {
			config["args"] = transport.Args
		}
		if env := maskMcpValues(transport.Env, secrets); len(env) > 0 {
			config["env"] = env
		}
		if transport.Cwd != "" {
			config["cwd"] = transport.Cwd
		}
		if len(transport.Variants) > 0 {
			config["variants"] = transport.Variants
		}
	}
	if server.Exposure != "" {
		config["exposure"] = server.Exposure
	}
	if server.Timeout > 0 {
		config["timeout"] = server.Timeout
	}
	return config
}

func maskMcpValues(values map[string]string, secrets *configstore.SecretBox) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = maskMcpValue(value, secrets)
	}
	return out
}

// maskMcpValue never reveals a stored secret or a plaintext credential: a
// `sec_…` reference and an inline token both render as the mask.
func maskMcpValue(value string, secrets *configstore.SecretBox) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, configstore.SecretRefPrefix) {
		return mcpSecretMask
	}
	if strings.HasPrefix(trimmed, "${") {
		return trimmed
	}
	if trimmed == "" {
		return ""
	}
	return mcpSecretMask
}

// save handles upsert / delete / set-enabled.
func (m *McpSync) save(arg rpcwire.Value) (rpcwire.Value, error) {
	action := stringField(arg, "action")
	scope := stringField(arg, "scope")
	name := stringField(arg, "name")
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	switch action {
	case "delete":
		_, err := m.manager.store.DeleteMcpServer(scope, "", name)
		return rpcwire.Undefined{}, err
	case "set-enabled":
		fields, err := decodeObject(arg)
		if err != nil {
			return nil, err
		}
		enabled := boolField(fields, "enabled")
		if _, present := fields["enabled"]; !present {
			return nil, fmt.Errorf("enabled is required for set-enabled")
		}
		_, setErr := m.manager.store.SetMcpEnabled(scope, "", name, enabled)
		return rpcwire.Undefined{}, setErr
	case "upsert":
		server, secrets, err := mcpServerFromUI(name, arg)
		if err != nil {
			return nil, err
		}
		_, _, err = m.manager.store.UpsertMcpServer(scope, "", server, secrets)
		return rpcwire.Undefined{}, err
	}
	return nil, fmt.Errorf("unknown mcp-sync action %q", action)
}

// mcpServerFromUI turns the UI's loose `McpServerConfig` into a doc entry and
// the plaintext secrets its fields introduce.
func mcpServerFromUI(name string, arg rpcwire.Value) (configstore.McpServer, map[string]string, error) {
	fields, err := decodeObject(arg)
	if err != nil {
		return configstore.McpServer{}, nil, err
	}
	config, ok := fields["config"]
	if !ok {
		// set-enabled is the only action without a config; upsert needs one.
		return configstore.McpServer{}, nil, fmt.Errorf("config is required for upsert")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(config, &raw); err != nil {
		return configstore.McpServer{}, nil, fmt.Errorf("decoding config: %w", err)
	}
	server := configstore.McpServer{Name: name, Enabled: true}
	secrets := map[string]string{}
	stringAt := func(key string) string {
		var text string
		_ = json.Unmarshal(raw[key], &text)
		return strings.TrimSpace(text)
	}
	if value, ok := raw["enabled"]; ok {
		var enabled bool
		if json.Unmarshal(value, &enabled) == nil {
			server.Enabled = enabled
		}
	}
	server.Exposure = stringAt("exposure")
	var timeout float64
	if json.Unmarshal(raw["timeout"], &timeout) == nil && timeout > 0 {
		server.Timeout = int(timeout)
	}
	kind := strings.ToLower(stringAt("type"))
	url := stringAt("url")
	command := stringAt("command")
	if url != "" || kind == "http" || kind == "sse" || kind == "streamablehttp" || kind == "streamable-http" {
		server.Transport.Kind = configstore.MCPKindHTTP
		server.Transport.URL = url
		headers, err := mcpSecretMap(name, "header", raw["headers"], secrets)
		if err != nil {
			return server, nil, err
		}
		server.Transport.Headers = headers
		if oauth, ok := raw["oauth"]; ok && len(oauth) > 0 && string(oauth) != "null" {
			var oauthRaw map[string]string
			if err := json.Unmarshal(oauth, &oauthRaw); err != nil {
				// The UI's oauth object also carries typed fields; decode loosely.
				var loose map[string]any
				if json.Unmarshal(oauth, &loose) != nil {
					return server, nil, fmt.Errorf("decoding oauth: %w", err)
				}
				oauthRaw = map[string]string{}
				for key, value := range loose {
					if text, isText := value.(string); isText {
						oauthRaw[key] = text
					}
				}
			}
			parsed := &configstore.MCPOAuth{
				ClientID:              oauthRaw["clientId"],
				Scope:                 oauthRaw["scope"],
				ClientName:            oauthRaw["clientName"],
				CallbackURL:           oauthRaw["callbackUrl"],
				AuthServerMetadataURL: oauthRaw["authServerMetadataUrl"],
				ClientRegistration:    oauthRaw["clientRegistration"],
			}
			if secret := strings.TrimSpace(oauthRaw["clientSecret"]); secret != "" {
				if strings.HasPrefix(secret, configstore.SecretRefPrefix) {
					parsed.ClientSecret = secret
				} else {
					id := fmt.Sprintf("sec_mcp_%s_oauth", sanitizeSecretSegment(name))
					secrets[id] = secret
					parsed.ClientSecret = id
				}
			}
			server.Transport.OAuth = parsed
		}
		return server, secrets, nil
	}
	server.Transport.Kind = configstore.MCPKindStdio
	server.Transport.Command = command
	if value, ok := raw["args"]; ok {
		var args []string
		if json.Unmarshal(value, &args) == nil {
			server.Transport.Args = args
		}
	}
	env, err := mcpSecretMap(name, "env", raw["env"], secrets)
	if err != nil {
		return server, nil, err
	}
	server.Transport.Env = env
	server.Transport.Cwd = stringAt("cwd")
	if value, ok := raw["variants"]; ok && len(value) > 0 && string(value) != "null" {
		var variants map[string]configstore.MCPCommand
		if err := json.Unmarshal(value, &variants); err == nil {
			server.Transport.Variants = map[string]*configstore.MCPCommand{}
			for key, variant := range variants {
				copied := variant
				server.Transport.Variants[key] = &copied
			}
		}
	}
	return server, secrets, nil
}

// mcpSecretMap turns plaintext env/header values into stored secrets. A value
// already holding a `sec_…` reference (the masked view round-tripping back) is
// kept as is.
func mcpSecretMap(server, kind string, raw json.RawMessage, secrets map[string]string) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%s must map names to strings: %w", kind+"s", err)
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || strings.HasPrefix(trimmed, configstore.SecretRefPrefix) || strings.HasPrefix(trimmed, "${") {
			out[key] = trimmed
			continue
		}
		id := fmt.Sprintf("sec_mcp_%s_%s_%s", sanitizeSecretSegment(server), kind, sanitizeSecretSegment(key))
		secrets[id] = value
		out[key] = id
	}
	return out, nil
}

func sanitizeSecretSegment(value string) string {
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

// statusResponseTimeout bounds the forwarded status call (the runner samples a
// connecting server for up to ~12 s before answering).
const statusResponseTimeout = 20 * time.Second

// statuses answers `listWorkspaceMcpServerStatuses`: the owning runner's live
// statuses. With no MCP server in the doc there is nothing to connect, so an
// empty status map is the truthful answer (and keeps an older runner that lacks
// the method from leaving the settings callback unanswered).
func (m *McpSync) statuses(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	target := readViewTarget(arg)
	if !m.manager.HasMcpServersFor(m.manager.runnerFor(target)) {
		return json.RawMessage(`{"statuses":{}}`), nil
	}
	// The Settings page targets the workspace it has open; without one the
	// first connected runner answers (the MCP list is global in this release).
	link, _ := m.manager.forwardTarget(target)
	if link == nil {
		return nil, fmt.Errorf("no runner connected")
	}
	callCtx, cancel := context.WithTimeout(ctx, statusResponseTimeout)
	defer cancel()
	encoded, err := json.Marshal(map[string]any{
		"workspacePath":     target.WorkspacePath,
		"workspaceIdentity": target.WorkspaceIdentity,
	})
	if err != nil {
		return nil, err
	}
	value, err := link.Call(callCtx, "mcp-sync", "listWorkspaceMcpServerStatuses", encoded)
	if err != nil {
		return nil, err
	}
	return value, nil
}

// mcpSecretMask is what the browser sees instead of an env/header value: the
// real value only ever travels to the runner link.
const mcpSecretMask = "••••"
