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
}

// PushPayload is one S→R `config` frame body.
type PushPayload struct {
	Version uint64            `json:"version"`
	Doc     PushDoc           `json:"doc"`
	Secrets map[string]string `json:"secrets"`
	Bundles []json.RawMessage `json:"bundles"`
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
