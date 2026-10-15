// env.go ports the spawn environment construction of the TS manager: the
// sanitize → runtime-env → spawn-env → command-env → workspace-identity merge of
// nexAgentProcessManager.ts's spawn() call, and the proxy/CA/no-proxy patches of
// runtime-tools/agentProxyEnv.ts.
//
// Order matters and is the contract: the settings-page values land *after* the
// inherited environment (so a shell's HTTP_PROXY cannot win) and the
// deployment-specific command env lands *after* those (so a packaged command
// still overrides everything). The workspace identity is applied last because it
// is the isolation fact: no deployment patch may be able to point an agent at a
// different workspace identity.
package agentproc

import (
	"os"
	"regexp"
	"strings"
)

// Environment keys shared with the agent side (packages/shared).
const (
	// EnvRuntimeEnvKey is NEX_RUNTIME_ENV. Only this key selects the runtime
	// mode; NODE_ENV is deliberately not trusted.
	EnvRuntimeEnvKey = "NEX_RUNTIME_ENV"
	// EnvHTTPProxyKey / EnvNoProxyKey are the Nex-specific proxy keys that let the
	// agent's http adapter reuse the settings-page proxy without reading the
	// user's shell environment.
	EnvHTTPProxyKey = "NEX_HTTP_PROXY"
	EnvNoProxyKey   = "NEX_NO_PROXY"
	// EnvCaCertKey is NEX_AGENT_CA_CERT.
	EnvCaCertKey = "NEX_AGENT_CA_CERT"
	// EnvWorkspaceIdentity is NEX_WORKSPACE_IDENTITY.
	EnvWorkspaceIdentity = "NEX_WORKSPACE_IDENTITY"
	// EnvBaseURL is the authoritative Nex API origin the host resolved and
	// hands down so the agent's trust decisions cannot fork from the host's.
	EnvBaseURL = "NEX_BASE_URL"
)

// RuntimeEnv is NexRuntimeEnv.
type RuntimeEnv string

// The three runtime modes.
const (
	RuntimeDevelopment RuntimeEnv = "development"
	RuntimeProduction  RuntimeEnv = "production"
	RuntimeTest        RuntimeEnv = "test"
)

// NormalizeRuntimeEnv ports normalizeNexRuntimeEnv.
func NormalizeRuntimeEnv(value string) (RuntimeEnv, bool) {
	switch RuntimeEnv(strings.ToLower(strings.TrimSpace(value))) {
	case RuntimeDevelopment:
		return RuntimeDevelopment, true
	case RuntimeProduction:
		return RuntimeProduction, true
	case RuntimeTest:
		return RuntimeTest, true
	default:
		return "", false
	}
}

// ResolveRuntimeEnv ports resolveNexRuntimeEnv: the normalized NEX_RUNTIME_ENV,
// or production when it is absent or unrecognized.
func ResolveRuntimeEnv(env []string) RuntimeEnv {
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok && key == EnvRuntimeEnvKey {
			if normalized, ok := NormalizeRuntimeEnv(value); ok {
				return normalized
			}
		}
	}
	return RuntimeProduction
}

// sanitizedRuntimeEnvKeys is SANITIZED_RUNTIME_ENV_KEYS from
// packages/shared/src/runtimeEnv.ts. These keys never reach an agent child
// through the inherited environment: the settings-page proxy/CA values are
// injected explicitly below, and the CUA broker material must not leak into an
// arbitrary MCP server or a Bash tool child (confused-deputy).
var sanitizedRuntimeEnvKeys = map[string]bool{
	"NODE_ENV":                                 true,
	"ELECTRON_RUN_AS_NODE":                     true,
	"NODE_NO_WARNINGS":                         true,
	"HTTP_PROXY":                               true,
	"HTTPS_PROXY":                              true,
	"ALL_PROXY":                                true,
	"NO_PROXY":                                 true,
	"NODE_EXTRA_CA_CERTS":                      true,
	"SSL_CERT_FILE":                            true,
	"SSL_CERT_DIR":                             true,
	"REQUESTS_CA_BUNDLE":                       true,
	"CURL_CA_BUNDLE":                           true,
	"GIT_SSL_CAINFO":                           true,
	"NEX_REMOTE_RUNTIME_NETWORK_AUTHORITY":     true,
	"NEX_REMOTE_HTTP_PROXY":                    true,
	"NEX_REMOTE_NO_PROXY":                      true,
	"NEX_CUA_PERMISSION_BROKER_SOCKET":         true,
	"NEX_CUA_PERMISSION_BROKER_TOKEN":          true,
	"NEX_CUA_PERMISSION_BROKER_REFRESH_MARKER": true,
	"NEX_CUA_PLUGIN_AUTHORITY":                 true,
}

// ShouldSanitizeEnvKey ports shouldSanitizeNexRuntimeEnvKey, including the
// package-manager proxy variables (npm_config_proxy & co): a pnpm/yarn export in
// the developer's shell must not silently become the agent's proxy.
func ShouldSanitizeEnvKey(key string) bool {
	if sanitizedRuntimeEnvKeys[strings.ToUpper(key)] {
		return true
	}
	upper := strings.ToUpper(key)
	for _, prefix := range []string{"NPM_CONFIG_", "YARN_", "PNPM_"} {
		if strings.HasPrefix(upper, prefix) {
			switch strings.TrimPrefix(upper, prefix) {
			case "HTTP_PROXY", "HTTPS_PROXY", "PROXY", "ALL_PROXY", "NO_PROXY", "CAFILE", "CA":
				return true
			}
		}
	}
	return false
}

// SanitizeEnv ports sanitizeNexRuntimeEnv over a `KEY=VALUE` list. Empty
// variables are dropped, exactly as the TS skips `undefined`.
func SanitizeEnv(env []string) []string {
	sanitized := make([]string, 0, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" || ShouldSanitizeEnvKey(key) {
			continue
		}
		sanitized = append(sanitized, key+"="+value)
	}
	return sanitized
}

// EnvPatch is an ordered set of extra variables for one spawn, as a slice of
// KEY=VALUE pairs. Later patches win over earlier ones, so building the child's
// environment is a fold and nothing has to remember the precedence at the call
// site.
type EnvPatch []string

// Set adds or replaces a variable in the patch.
func (p EnvPatch) Set(key, value string) EnvPatch {
	for index, entry := range p {
		if existing, _, _ := strings.Cut(entry, "="); existing == key {
			p[index] = key + "=" + value
			return p
		}
	}
	return append(p, key+"="+value)
}

// proxyEnvKeys is PROXY_ENV_KEYS: the uppercase trio wins over an inherited
// shell value because the model registry matches HTTPS_PROXY before HTTP_PROXY.
var proxyEnvKeys = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", EnvHTTPProxyKey}

// noProxyEnvKeys is NO_PROXY_ENV_KEYS: only the rules the user typed in the
// settings page are honored; the shell's NO_PROXY is not inherited.
var noProxyEnvKeys = []string{"NO_PROXY", "no_proxy", EnvNoProxyKey}

// BuildProxyEnv ports buildAgentRuntimeEnv: the proxy, the no-proxy list and the
// custom CA as one patch. Only explicit settings reach the child.
func BuildProxyEnv(httpProxy, noProxy, caCertPath string) EnvPatch {
	patch := EnvPatch{}
	if normalized := normalizeProxyValue(httpProxy); normalized != "" {
		for _, key := range proxyEnvKeys {
			patch = patch.Set(key, normalized)
		}
	}
	if normalized := normalizeNoProxyValue(noProxy); normalized != "" {
		for _, key := range noProxyEnvKeys {
			patch = patch.Set(key, normalized)
		}
	}
	if trimmed := strings.TrimSpace(caCertPath); trimmed != "" {
		// NODE_EXTRA_CA_CERTS has to exist before the runtime starts; NEX_AGENT_CA_CERT
		// carries the same path to adapters and tool children that do not read the
		// Node variable.
		patch = patch.Set("NODE_EXTRA_CA_CERTS", trimmed).Set(EnvCaCertKey, trimmed)
	}
	return patch
}

// BuildEndpointOriginEnv ports buildAgentEndpointOriginEnv. NEX_BASE_URL is the
// highest-priority origin key, so it overrides an inherited NEX_ENDPOINT_ORIGIN
// and both sides of the official-MCP trust decision then read the same input.
func BuildEndpointOriginEnv(endpointOrigin string) EnvPatch {
	trimmed := strings.TrimSpace(endpointOrigin)
	if trimmed == "" {
		return nil
	}
	return EnvPatch{EnvBaseURL + "=" + trimmed}
}

// normalizeProxyValue ports normalizeProxyValue: a value that already carries a
// scheme is kept verbatim (socks5:// and friends), a bare host:port gets http://.
func normalizeProxyValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if schemeProxyPattern.MatchString(trimmed) {
		return trimmed
	}
	return "http://" + trimmed
}

// normalizeNoProxyValue ports normalizeNoProxyValue: split on commas, trim each
// token, drop empties, rejoin. Only the user's own entries survive.
func normalizeNoProxyValue(value string) string {
	tokens := strings.Split(value, ",")
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if trimmed := strings.TrimSpace(token); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, ",")
}

// schemeProxyPattern matches an explicit scheme prefix ("http://", "socks5://", ...).
var schemeProxyPattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)

// BuildWorkspaceIdentityEnv ports buildAgentWorkspaceIdentityEnv: a local
// workspace keeps the path fallback, so the key is absent rather than empty.
func BuildWorkspaceIdentityEnv(workspaceIdentity string) EnvPatch {
	trimmed := strings.TrimSpace(workspaceIdentity)
	if trimmed == "" {
		return nil
	}
	return EnvPatch{EnvWorkspaceIdentity + "=" + trimmed}
}

// BuildSpawnEnv folds the sources into the child's environment in the TS order:
// sanitized inherited env, the runtime mode, the settings patch, the deployment
// patch, then the workspace identity.
func BuildSpawnEnv(opts SpawnEnvOptions) []string {
	env := SanitizeEnv(opts.HostEnv)
	env = append(env, EnvRuntimeEnvKey+"="+string(ResolveRuntimeEnv(opts.HostEnv)))
	for _, patch := range []EnvPatch{opts.Settings, opts.EndpointOrigin, opts.Command, BuildWorkspaceIdentityEnv(opts.WorkspaceIdentity)} {
		env = mergeEnv(env, patch)
	}
	return env
}

// SpawnEnvOptions is the input of BuildSpawnEnv.
type SpawnEnvOptions struct {
	// HostEnv is the host process environment (`KEY=VALUE` pairs).
	HostEnv []string
	// Settings is the settings-page patch (BuildProxyEnv).
	Settings EnvPatch
	// EndpointOrigin is the host-resolved Nex API origin.
	EndpointOrigin EnvPatch
	// Command is the deployment patch (AgentCommand.Env).
	Command EnvPatch
	// WorkspaceIdentity is the isolation fact, applied last.
	WorkspaceIdentity string
}

// mergeEnv applies a patch over a KEY=VALUE list, replacing in place and
// appending what is new, so the result has no duplicate keys (a duplicate key
// would leave the effective value to the platform's parsing).
func mergeEnv(env []string, patch EnvPatch) []string {
	if len(patch) == 0 {
		return env
	}
	index := make(map[string]int, len(env))
	for position, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok {
			index[key] = position
		}
	}
	for _, entry := range patch {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if position, exists := index[key]; exists {
			env[position] = entry
			continue
		}
		index[key] = len(env)
		env = append(env, entry)
	}
	return env
}

// HostEnv returns the current process environment as a KEY=VALUE slice. It is
// the Go spelling of `process.env` and is a seam: tests pass an explicit list.
func HostEnv() []string { return os.Environ() }
