package configstore

// Doc writes for MCP servers and skill/prompt bundles (docs/next/
// config-mcp-skills.md P1/P2). Every write goes through Store.Update, so it is
// validated, versioned and pushed like a provider edit.

import (
	"fmt"
	"strings"
)

// UpsertMcpServer writes (or replaces) one MCP server and its secrets. Secret
// fields already holding `sec_…` refs must exist in the store; plain values
// written to fields listed in secrets are stored as new secrets.
func (s *Store) UpsertMcpServer(scope, runnerID string, server McpServer, secrets map[string]string) (ConfigDoc, []string, error) {
	var warnings []string
	normalized := CloneMcp(&server)
	warnings = append(warnings, NormalizeMcp(normalized)...)
	if strings.TrimSpace(normalized.Name) == "" {
		return ConfigDoc{}, nil, fmt.Errorf("server name is required")
	}
	// Store every plaintext secret first: a failed write must not leave the doc
	// referencing a secret that does not exist (the doc write is validated
	// afterwards and rolled back on failure by Store.Update).
	for id, value := range secrets {
		if !strings.HasPrefix(id, SecretRefPrefix) {
			return ConfigDoc{}, warnings, fmt.Errorf("secret id %q must start with %s", id, SecretRefPrefix)
		}
		if err := s.secrets.Put(id, value); err != nil {
			return ConfigDoc{}, warnings, fmt.Errorf("storing secret %s: %w", id, err)
		}
	}
	doc, err := s.Update("mcp-upsert:"+scope+":"+normalized.Name, func(doc *ConfigDoc) error {
		return withMcpLayer(doc, scope, runnerID, func(layer *Layer) error {
			if layer.MCP == nil {
				layer.MCP = map[string]*McpServer{}
			}
			if clash := mcpNamespaceClash(layer.MCP, normalized.Name); clash != "" {
				return fmt.Errorf("server %q conflicts with %q (pi tools share one namespace per server, '-' and '_' are equivalent)", normalized.Name, clash)
			}
			copied := CloneMcp(normalized)
			layer.MCP[normalized.Name] = copied
			return nil
		})
	})
	return doc, warnings, err
}

// DeleteMcpServer removes one MCP server (a missing name is not an error: the
// UI retries deletes after a reload).
func (s *Store) DeleteMcpServer(scope, runnerID, name string) (ConfigDoc, error) {
	return s.Update("mcp-delete:"+scope+":"+name, func(doc *ConfigDoc) error {
		return withMcpLayer(doc, scope, runnerID, func(layer *Layer) error {
			delete(layer.MCP, name)
			return nil
		})
	})
}

// SetMcpEnabled toggles one MCP server in place.
func (s *Store) SetMcpEnabled(scope, runnerID, name string, enabled bool) (ConfigDoc, error) {
	return s.Update("mcp-toggle:"+scope+":"+name, func(doc *ConfigDoc) error {
		return withMcpLayer(doc, scope, runnerID, func(layer *Layer) error {
			server, ok := layer.MCP[name]
			if !ok || server == nil {
				return fmt.Errorf("mcp server %s: %w", name, ErrNotFound)
			}
			copied := CloneMcp(server)
			copied.Enabled = enabled
			layer.MCP[name] = copied
			return nil
		})
	})
}

// withMcpLayer applies mutate to the global or runner layer. It resolves the
// layer by reference for global and by map assignment for runner (Go maps of
// struct values cannot be addressed through a pointer).
func withMcpLayer(doc *ConfigDoc, scope, runnerID string, mutate func(layer *Layer) error) error {
	if scope == "" || scope == ScopeGlobal {
		return mutate(&doc.Scopes.Global)
	}
	if scope != ScopeRunner {
		return fmt.Errorf("scope %q is not editable in this release (use global or runner)", scope)
	}
	if strings.TrimSpace(runnerID) == "" {
		return fmt.Errorf("runnerId is required for the runner scope")
	}
	if doc.Scopes.Runner == nil {
		doc.Scopes.Runner = map[string]Layer{}
	}
	layer := doc.Scopes.Runner[runnerID]
	if err := mutate(&layer); err != nil {
		return err
	}
	if doc.Scopes.Runner == nil {
		doc.Scopes.Runner = map[string]Layer{}
	}
	doc.Scopes.Runner[runnerID] = layer
	return nil
}

// UpsertBundle stores a bundle (content-addressed) and references it in a layer's
// skills/prompts list, replacing an entry with the same name. The returned
// digest is the reference the doc now carries.
func (s *Store) UpsertBundle(scope, runnerID string, ref BundleRef, files []BundleFile) (ConfigDoc, string, error) {
	if strings.TrimSpace(ref.Name) == "" {
		return ConfigDoc{}, "", fmt.Errorf("bundle name is required")
	}
	if ref.Kind != "skill" && ref.Kind != "prompt" {
		return ConfigDoc{}, "", fmt.Errorf(`bundle kind must be "skill" or "prompt"`)
	}
	normalized := make([]BundleFile, 0, len(files))
	for _, file := range files {
		path, err := NormalizeBundlePath(file.Path)
		if err != nil {
			return ConfigDoc{}, "", err
		}
		normalized = append(normalized, BundleFile{Path: path, Content: file.Content})
	}
	bundle := Bundle{Name: ref.Name, Kind: ref.Kind, Description: ref.Description, Files: normalized}
	digest, err := s.StoreBundle(bundle)
	if err != nil {
		return ConfigDoc{}, "", err
	}
	ref.Digest = digest
	doc, err := s.Update("bundle-upsert:"+ref.Kind+":"+ref.Name, func(doc *ConfigDoc) error {
		return withBundleLayer(doc, scope, runnerID, func(layer *Layer) error {
			list := &layer.Skills
			if ref.Kind == "prompt" {
				list = &layer.Prompts
			}
			replaced := false
			for index, existing := range *list {
				if existing.Name != ref.Name {
					continue
				}
				(*list)[index] = ref
				replaced = true
				break
			}
			if !replaced {
				*list = append(*list, ref)
			}
			return nil
		})
	})
	return doc, digest, err
}

// RemoveBundle deletes one skill/prompt reference by name, searching runner
// scopes after the target layer (so deleting a skill from Settings also removes
// a per-runner one).
func (s *Store) RemoveBundle(scope, runnerID, kind, name string) (ConfigDoc, error) {
	return s.Update("bundle-delete:"+kind+":"+name, func(doc *ConfigDoc) error {
		remove := func(layer *Layer) bool {
			list := &layer.Skills
			if kind == "prompt" {
				list = &layer.Prompts
			}
			for index, existing := range *list {
				if existing.Name == name {
					*list = append((*list)[:index], (*list)[index+1:]...)
					return true
				}
			}
			return false
		}
		err := withBundleLayer(doc, scope, runnerID, func(layer *Layer) error {
			if !remove(layer) {
				return fmt.Errorf("%s %s: %w", kind, name, ErrNotFound)
			}
			return nil
		})
		if err == nil || (scope != "" && scope != ScopeGlobal) {
			return err
		}
		// Global delete also clears a same-named bundle from runner scopes (a
		// runner-scope override would otherwise keep the deleted skill alive).
		for key := range doc.Scopes.Runner {
			layer := doc.Scopes.Runner[key]
			if remove(&layer) {
				doc.Scopes.Runner[key] = layer
			}
		}
		return nil
	})
}

// SetBundleEnabled toggles one skill/prompt by name in the target layer.
func (s *Store) SetBundleEnabled(scope, runnerID, kind, name string, enabled bool) (ConfigDoc, error) {
	return s.Update("bundle-toggle:"+kind+":"+name, func(doc *ConfigDoc) error {
		return withBundleLayer(doc, scope, runnerID, func(layer *Layer) error {
			list := &layer.Skills
			if kind == "prompt" {
				list = &layer.Prompts
			}
			for index := range *list {
				if (*list)[index].Name == name {
					(*list)[index].Enabled = enabled
					return nil
				}
			}
			return fmt.Errorf("%s %s: %w", kind, name, ErrNotFound)
		})
	})
}

func withBundleLayer(doc *ConfigDoc, scope, runnerID string, mutate func(layer *Layer) error) error {
	if scope == "" || scope == ScopeGlobal {
		return mutate(&doc.Scopes.Global)
	}
	if scope != ScopeRunner {
		return fmt.Errorf("scope %q is not editable in this release (use global or runner)", scope)
	}
	if strings.TrimSpace(runnerID) == "" {
		return fmt.Errorf("runnerId is required for the runner scope")
	}
	if doc.Scopes.Runner == nil {
		doc.Scopes.Runner = map[string]Layer{}
	}
	layer := doc.Scopes.Runner[runnerID]
	if err := mutate(&layer); err != nil {
		return err
	}
	doc.Scopes.Runner[runnerID] = layer
	return nil
}
