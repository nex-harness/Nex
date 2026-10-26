package configstore

import (
	"encoding/json"
	"strings"
)

// PushDoc is the merged snapshot the server side of the link sends
// (`config {version, doc, secrets, bundles}`): the effective layer for one
// runner, not the layered document.
type PushDoc struct {
	Schema  int    `json:"schema"`
	Version uint64 `json:"version"`
	// Owned is true when the server config defines at least one provider and an
	// entitled secret exists for every such provider. Runners in fallback mode
	// (Owned false) keep reading their own ~/.pi/agent.
	Owned     bool                       `json:"owned"`
	Providers []Provider                 `json:"providers"`
	Settings  map[string]json.RawMessage `json:"settings,omitempty"`
	// MCP is the effective MCP server list (only when the doc defines any
	// entry); an empty list keeps the runner on pi's own mcp.json discovery.
	MCP []McpServer `json:"mcp,omitempty"`
	// MCPOwned is true when the doc has at least one MCP server: the runner then
	// uses only these servers, independently of provider ownership.
	MCPOwned bool `json:"mcpOwned,omitempty"`
	// Skills and Prompts are enabled bundle references plus their digests.
	Skills  []BundleRef `json:"skills,omitempty"`
	Prompts []BundleRef `json:"prompts,omitempty"`
}

// PushPayload is one S→R `config` frame body.
type PushPayload struct {
	Version uint64            `json:"version"`
	Doc     PushDoc           `json:"doc"`
	Secrets map[string]string `json:"secrets"`
	Bundles []json.RawMessage `json:"bundles"`
	// Warnings are non-fatal notes about this snapshot (e.g. a normalized SSE
	// transport). Informational: they do not affect the applied config.
	Warnings []string `json:"warnings,omitempty"`
}

// ProviderSecretIDs returns the secret ids a provider's fields reference.
func (p *Provider) ProviderSecretIDs() []string {
	var ids []string
	if strings.HasPrefix(strings.TrimSpace(p.APIKey), SecretRefPrefix) {
		ids = append(ids, strings.TrimSpace(p.APIKey))
	}
	return ids
}

// ProjectPush builds the per-runner snapshot: effective providers (with API keys
// resolved to the runner's entitled secrets), owned-mode decision and the secret
// map a runner may receive.
//
// Entitlement (spec decision 3): a provider's secrets go to every runner where
// the provider is enabled (global = all), minus runner-scope disable toggles. A
// provider whose key cannot be resolved (missing secret, or not an inline
// `${ENV}`) is not "owned" — the runner must not silently send unauthenticated
// requests; it stays visible in the view with `authConfigured:false`.
func (s *Store) ProjectPush(runnerID string) (PushPayload, []Issue, error) {
	doc, err := s.Snapshot()
	if err != nil {
		return PushPayload{}, nil, err
	}
	return s.projectPush(doc, runnerID)
}

func (s *Store) projectPush(doc ConfigDoc, runnerID string) (PushPayload, []Issue, error) {
	effective := doc.EffectiveForRunner(runnerID)
	secrets := map[string]string{}
	payload := PushPayload{
		Version: doc.Version,
		Doc: PushDoc{
			Schema:    SchemaVersion,
			Version:   doc.Version,
			Settings:  effective.Settings,
			Providers: []Provider{},
		},
		Secrets: secrets,
		Bundles: []json.RawMessage{},
	}
	var issues []Issue
	payload.Doc.MCP, payload.Doc.MCPOwned = projectMCP(effective, secrets, s.secrets, &issues)
	payload.Doc.Skills = enabledBundles(effective.Skills, "skill")
	payload.Doc.Prompts = enabledBundles(effective.Prompts, "prompt")
	payload.Bundles = s.projectBundles(append(append([]BundleRef{}, payload.Doc.Skills...), payload.Doc.Prompts...), &issues)
	for _, provider := range effective.ProviderList() {
		keyRef := provider.APIKey
		copied := cloneProvider(*provider)
		copied.APIKey = "" // the value travels in `secrets`, never inline
		payload.Doc.Providers = append(payload.Doc.Providers, copied)
		if !provider.Enabled {
			continue
		}
		refs := []string{}
		if strings.HasPrefix(strings.TrimSpace(keyRef), SecretRefPrefix) {
			refs = append(refs, strings.TrimSpace(keyRef))
		}
		for _, id := range refs {
			value, ok, err := s.secrets.Get(id)
			if err != nil {
				// A corrupt/unreadable secret makes the provider unentitled rather
				// than failing the whole push.
				issues = append(issues, Issue{Path: "secrets." + id, Code: "secret-unreadable", Message: "secret could not be decrypted"})
				continue
			}
			if ok {
				secrets[id] = value
			}
		}
	}
	payload.Doc.Owned = ownedMode(payload.Doc.Providers)
	for index := range payload.Doc.Providers {
		provider := &payload.Doc.Providers[index]
		if _, ok := secrets[SecretID(provider.ID)]; ok {
			provider.AuthConfigured = true
		}
	}
	return payload, issues, nil
}

// projectMCP resolves the effective MCP servers for one runner: per-OS
// variants are kept (the runner picks its own OS), secret references are
// replaced by resolved values and dropped from the pushed strings. Disabled
// servers are still pushed (the UI shows them off) but their secrets are not.
func projectMCP(effective Layer, secrets map[string]string, box *SecretBox, issues *[]Issue) ([]McpServer, bool) {
	servers := effective.MCPServerList()
	if len(servers) == 0 {
		return nil, false
	}
	out := make([]McpServer, 0, len(servers))
	for _, server := range servers {
		copied := CloneMcp(server)
		if copied.Name == "" {
			continue
		}
		if copied.Enabled {
			copied.Transport.Env = resolveMCPSecrets(secrets, box, copied.Transport.Env, issues)
			copied.Transport.Headers = resolveMCPSecrets(secrets, box, copied.Transport.Headers, issues)
			for _, os := range SortedKeys(copied.Transport.Variants) {
				if variant := copied.Transport.Variants[os]; variant != nil {
					variant.Env = resolveMCPSecrets(secrets, box, variant.Env, issues)
				}
			}
			if copied.Transport.OAuth != nil && strings.HasPrefix(strings.TrimSpace(copied.Transport.OAuth.ClientSecret), SecretRefPrefix) {
				copied.Transport.OAuth.ClientSecret = resolveSecret(secrets, box, copied.Transport.OAuth.ClientSecret, issues)
			}
		} else {
			// A disabled server is still listed (the UI shows it off), but none
			// of its values travel: they may belong to another runner.
			copied.Transport.Env = nil
			copied.Transport.Headers = nil
			if copied.Transport.OAuth != nil {
				copied.Transport.OAuth.ClientSecret = ""
			}
			for _, variant := range copied.Transport.Variants {
				if variant != nil {
					variant.Env = nil
				}
			}
		}
		out = append(out, *copied)
	}
	return out, true
}

// resolveMCPSecrets replaces `sec_…` values by their plaintext and removes the
// ones the runner may not receive (a missing field is absent for pi).
func resolveMCPSecrets(secrets map[string]string, box *SecretBox, values map[string]string, issues *[]Issue) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for _, key := range SortedKeys(values) {
		value := values[key]
		if strings.HasPrefix(strings.TrimSpace(value), SecretRefPrefix) {
			resolved, ok := resolvedSecret(secrets, box, value, issues)
			if !ok {
				continue
			}
			out[key] = resolved
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func resolveSecret(secrets map[string]string, box *SecretBox, ref string, issues *[]Issue) string {
	value, ok := resolvedSecret(secrets, box, ref, issues)
	if !ok {
		return ""
	}
	return value
}

func resolvedSecret(secrets map[string]string, box *SecretBox, ref string, issues *[]Issue) (string, bool) {
	id := strings.TrimSpace(ref)
	if value, ok := secrets[id]; ok {
		return value, true
	}
	if box == nil {
		return "", false
	}
	value, ok, err := box.Get(id)
	if err != nil {
		*issues = append(*issues, Issue{Path: "secrets." + id, Code: "secret-unreadable", Message: "secret could not be decrypted"})
		return "", false
	}
	if !ok {
		*issues = append(*issues, Issue{Path: "secrets." + id, Code: "secret-missing", Message: "referenced secret is not stored"})
		return "", false
	}
	secrets[id] = value
	return value, true
}

// enabledBundles keeps the enabled references of one kind.
func enabledBundles(refs []BundleRef, kind string) []BundleRef {
	var out []BundleRef
	for _, ref := range refs {
		if ref.Enabled && ref.Kind == kind {
			out = append(out, ref)
		}
	}
	return out
}

// projectBundles loads the referenced bundles for the push. A missing or
// corrupt bundle is an issue (the runner never receives it).
func (s *Store) projectBundles(refs []BundleRef, issues *[]Issue) []json.RawMessage {
	out := []json.RawMessage{}
	seen := map[string]struct{}{}
	for _, ref := range refs {
		if _, done := seen[ref.Digest]; done {
			continue
		}
		seen[ref.Digest] = struct{}{}
		bundle, err := s.LoadBundle(ref.Digest)
		if err != nil {
			*issues = append(*issues, Issue{Path: "bundles." + ref.Name, Code: "bundle-unreadable", Message: err.Error()})
			continue
		}
		encoded, err := json.Marshal(bundle)
		if err != nil {
			continue
		}
		out = append(out, encoded)
	}
	return out
}

// ownedMode implements "local fallback until configured, then server wins": the
// server owns a runner's config as soon as the doc defines at least one enabled
// provider. An enabled provider without an entitled secret is reported (its
// models do not become executable); the runner is still server-owned so an edit
// in Settings is never silently overridden by local ~/.pi/agent files.
func ownedMode(providers []Provider) bool {
	for index := range providers {
		if providers[index].Enabled {
			return true
		}
	}
	return false
}

// ResolveAPIKeys records, for each enabled provider, whether the runner will have
// a usable credential. It mutates the push snapshot's provider list (the field is
// not part of the stored doc; `authConfigured` is runtime-only).
func (p *PushPayload) ResolveAPIKeys() {
	for index := range p.Doc.Providers {
		provider := &p.Doc.Providers[index]
		id := SecretID(provider.ID)
		if _, ok := p.Secrets[id]; ok {
			provider.AuthConfigured = true
		}
	}
}
