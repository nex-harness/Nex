package configstore

// MCP entries of the ConfigDoc (docs/next/config-mcp-skills.md P1).
//
// Shape mirrors pi's `McpServerConfig` (research §B): stdio
// {command,args,env,cwd} or http {url,headers,oauth,auth.provider}, plus the
// per-OS command variants the server keeps as an extension. Any string field of
// the form `sec_…` is a stored secret reference; `${ENV}` passes through to the
// runner. Validation at write time is a port of pi's `validateMcpServerConfig`
// so a doc that reaches a runner is one pi accepts.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MCP transport kinds stored in the doc. `sse` is never stored: it is
// normalized to http with a warning (pi rejects the legacy SSE transport).
const (
	MCPKindStdio = "stdio"
	MCPKindHTTP  = "http"
)

// MCPExposures are pi's exposure values (codemode is the default).
var MCPExposures = []string{"codemode", "deferred", "direct", "hidden"}

// MCPOSVariants are the per-OS stdio overrides the server keeps.
var MCPOSVariants = []string{"darwin", "linux", "win32"}

var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// McpServer is one MCP server entry of a layer.
type McpServer struct {
	Name      string       `json:"name"`
	Enabled   bool         `json:"enabled"`
	Exposure  string       `json:"exposure,omitempty"`
	Timeout   int          `json:"timeout,omitempty"`
	Transport MCPTransport `json:"transport"`
}

// MCPTransport is either stdio or http.
type MCPTransport struct {
	Kind string `json:"kind"`
	// stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	// Variants overrides command/args/env on one runner OS (darwin/linux/win32).
	Variants map[string]*MCPCommand `json:"variants,omitempty"`
	// http
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	OAuth   *MCPOAuth         `json:"oauth,omitempty"`
	// AuthProvider sends a pi provider's token instead of OAuth (pi `auth.provider`).
	AuthProvider string `json:"authProvider,omitempty"`
}

// MCPCommand is one per-OS stdio command override.
type MCPCommand struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPOAuth is pi's `oauth` block. ClientSecret may be a secret reference.
type MCPOAuth struct {
	ClientID              string `json:"clientId,omitempty"`
	ClientSecret          string `json:"clientSecret,omitempty"`
	CallbackPort          int    `json:"callbackPort,omitempty"`
	CallbackURL           string `json:"callbackUrl,omitempty"`
	Scope                 string `json:"scope,omitempty"`
	ClientName            string `json:"clientName,omitempty"`
	ClientRegistration    string `json:"clientRegistration,omitempty"`
	AuthServerMetadataURL string `json:"authServerMetadataUrl,omitempty"`
}

// isToggleOnly reports an entry that only carries `enabled` (and nothing else),
// so a runner scope can disable a global server without restating it.
func (m *McpServer) isToggleOnly() bool {
	return m.Transport.Kind == "" && m.Exposure == "" && m.Timeout == 0
}

// CloneMcp returns a deep copy.
func CloneMcp(server *McpServer) *McpServer {
	if server == nil {
		return nil
	}
	copied := *server
	transport := server.Transport
	if transport.Args != nil {
		transport.Args = append([]string(nil), transport.Args...)
	}
	if transport.Env != nil {
		transport.Env = cloneStringMap(transport.Env)
	}
	if transport.Headers != nil {
		transport.Headers = cloneStringMap(transport.Headers)
	}
	if transport.Variants != nil {
		transport.Variants = make(map[string]*MCPCommand, len(transport.Variants))
		for os, command := range server.Transport.Variants {
			if command == nil {
				continue
			}
			variant := *command
			if command.Args != nil {
				variant.Args = append([]string(nil), command.Args...)
			}
			if command.Env != nil {
				variant.Env = cloneStringMap(command.Env)
			}
			transport.Variants[os] = &variant
		}
	}
	if transport.OAuth != nil {
		oauth := *transport.OAuth
		transport.OAuth = &oauth
	}
	copied.Transport = transport
	return &copied
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// MCPServerList returns the effective servers in name order.
func (l Layer) MCPServerList() []*McpServer {
	out := make([]*McpServer, 0, len(l.MCP))
	for _, name := range SortedKeys(l.MCP) {
		if server := l.MCP[name]; server != nil {
			out = append(out, server)
		}
	}
	return out
}

// normalizeMcpExposure maps aliases to pi's current names.
func normalizeMcpExposure(value string) (string, bool) {
	switch value {
	case "codemode-deferred", "codemode":
		return "codemode", true
	case "deferred", "direct", "hidden":
		return value, true
	}
	return "", false
}

// NormalizeMcp applies the two lossless normalizations every write needs
// (`sse` → http, `streamable-http` → http, exposure alias) and returns the
// warnings the UI should show. The mutation is in place on a copy the caller
// owns.
func NormalizeMcp(server *McpServer) []string {
	var warnings []string
	if server == nil {
		return nil
	}
	if exposure, ok := normalizeMcpExposure(server.Exposure); ok {
		if exposure != server.Exposure {
			warnings = append(warnings, fmt.Sprintf("server %q: exposure %q was normalized to %q", server.Name, server.Exposure, exposure))
		}
		server.Exposure = exposure
	}
	switch server.Transport.Kind {
	case "sse", "streamableHttp", "streamable-http":
		// pi rejects the legacy SSE transport; an SSE URL is the same HTTP
		// endpoint for every modern client, so it is kept as http.
		if server.Transport.Kind == "sse" {
			warnings = append(warnings, fmt.Sprintf("server %q: legacy SSE transport was normalized to http", server.Name))
		}
		server.Transport.Kind = MCPKindHTTP
	}
	return warnings
}

// ValidateMcpServer structurally validates one entry like pi's
// validateMcpServerConfig. It expects NormalizeMcp to have run first.
func ValidateMcpServer(path string, server *McpServer) []Issue {
	if server == nil {
		return []Issue{{Path: path, Code: "mcp-invalid", Message: "server entry must be an object"}}
	}
	var issues []Issue
	add := func(code, message string) {
		issues = append(issues, Issue{Path: path, Code: code, Message: message})
	}
	if !mcpServerName.MatchString(server.Name) {
		add("mcp-name-invalid", `server name must use letters, digits, "_" and "-"`)
	}
	if server.Exposure != "" {
		if _, ok := normalizeMcpExposure(server.Exposure); !ok {
			add("mcp-exposure-unsupported", "exposure must be one of "+strings.Join(MCPExposures, ", "))
		}
	}
	if server.Timeout < 0 {
		add("mcp-timeout-invalid", "timeout must be a positive number of seconds")
	}
	switch server.Transport.Kind {
	case MCPKindStdio:
		if strings.TrimSpace(server.Transport.Command) == "" {
			add("mcp-command-missing", "stdio server needs a command")
		}
		for _, os := range SortedKeys(server.Transport.Variants) {
			variant := server.Transport.Variants[os]
			if !containsString(MCPOSVariants, os) {
				add("mcp-variant-os-unsupported", "variant "+os+" must be one of "+strings.Join(MCPOSVariants, ", "))
				continue
			}
			if variant == nil || strings.TrimSpace(variant.Command) == "" {
				add("mcp-variant-command-missing", "variant "+os+" needs a command")
			}
		}
	case MCPKindHTTP:
		if !IsHTTPURL(server.Transport.URL) {
			add("mcp-url-invalid", "http server url must be an http(s) URL")
		}
		if server.Transport.OAuth != nil {
			issues = append(issues, validateMCPOAuth(path+".transport.oauth", server.Transport.OAuth)...)
		}
		if server.Transport.AuthProvider != "" && strings.HasPrefix(strings.ToLower(server.Transport.URL), "http://") {
			// pi: auth requires https except on loopback hosts.
			if !isLoopbackURL(server.Transport.URL) {
				add("mcp-auth-insecure", "auth requires an https URL, or http on localhost")
			}
		}
	case "":
		add("mcp-transport-missing", `server needs a "command" (stdio) or "url" (http) transport`)
	default:
		add("mcp-transport-unsupported", "transport kind must be stdio or http")
	}
	for _, field := range serverSecretFields(server) {
		if err := validateSecretRef(field.value); err != nil {
			add("mcp-secret-ref-invalid", field.label+": "+err.Error())
		}
	}
	return issues
}

// serverSecretFields lists the string fields that may hold a secret reference.
func serverSecretFields(server *McpServer) []struct{ label, value string } {
	var fields []struct{ label, value string }
	appendMap := func(prefix string, values map[string]string) {
		for _, key := range SortedKeys(values) {
			fields = append(fields, struct{ label, value string }{prefix + "." + key, values[key]})
		}
	}
	appendMap("transport.env", server.Transport.Env)
	appendMap("transport.headers", server.Transport.Headers)
	for _, os := range SortedKeys(server.Transport.Variants) {
		if variant := server.Transport.Variants[os]; variant != nil {
			appendMap("transport.variants."+os+".env", variant.Env)
		}
	}
	if server.Transport.OAuth != nil {
		fields = append(fields, struct{ label, value string }{"transport.oauth.clientSecret", server.Transport.OAuth.ClientSecret})
	}
	return fields
}

func validateSecretRef(value string) error {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "sec") && !strings.HasPrefix(trimmed, SecretRefPrefix) {
		return fmt.Errorf("value looks like a secret reference but is malformed")
	}
	return nil
}

func validateMCPOAuth(path string, oauth *MCPOAuth) []Issue {
	var issues []Issue
	add := func(code, message string) {
		issues = append(issues, Issue{Path: path, Code: code, Message: message})
	}
	if oauth.CallbackPort != 0 && (oauth.CallbackPort < 1 || oauth.CallbackPort > 65535) {
		add("mcp-oauth-port-invalid", "callbackPort must be a port number")
	}
	if oauth.CallbackURL != "" && !isLoopbackRedirect(oauth.CallbackURL) {
		add("mcp-oauth-callback-invalid", "callbackUrl must be an http URI on localhost, 127.0.0.1, or [::1]")
	}
	if oauth.ClientRegistration != "" && oauth.ClientRegistration != "dcr" && oauth.ClientRegistration != "cimd" {
		add("mcp-oauth-registration-invalid", `clientRegistration must be "dcr" or "cimd"`)
	}
	if oauth.AuthServerMetadataURL != "" && !IsHTTPURL(oauth.AuthServerMetadataURL) {
		add("mcp-oauth-metadata-invalid", "authServerMetadataUrl must be an http(s) URL")
	}
	return issues
}

func isLoopbackURL(value string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		if strings.HasPrefix(trimmed, "http://"+host+"/") || strings.HasSuffix(trimmed, "://"+host) || strings.Contains(trimmed, "//"+host+":") {
			return true
		}
	}
	return false
}

func isLoopbackRedirect(value string) bool {
	return strings.HasPrefix(value, "http://") && isLoopbackURL(value)
}

func containsString(values []string, value string) bool {
	for _, entry := range values {
		if entry == value {
			return true
		}
	}
	return false
}

// MCPNamespace is pi's tool-namespace rule: `-` and `_` collide, so two server
// names that differ only by those characters cannot coexist.
func MCPNamespace(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// mcpNamespaceClash reports an existing server name whose namespace equals name.
func mcpNamespaceClash(existing map[string]*McpServer, name string) string {
	namespace := MCPNamespace(name)
	names := make([]string, 0, len(existing))
	for key := range existing {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		if key != name && MCPNamespace(key) == namespace {
			return key
		}
	}
	return ""
}
