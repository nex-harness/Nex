package codemode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nex-harness/nex/pkg/store"
)

// DiscoveredMCPServer represents an MCP server discovered from a local config file
// such as <workspace>/.mcp.json or ~/.nex/mcp.json.
type DiscoveredMCPServer struct {
	store.MCPServerRecord
	Source     string `json:"source"`      // "workspace" | "user"
	SourcePath string `json:"source_path"` // absolute path to the JSON config file
	SourceRoot string `json:"source_root"` // compact display label, e.g. ".mcp.json", "~/.nex/mcp.json"
}

type rawMCPFileEntry struct {
	Description string            `json:"description,omitempty"`
	Transport   string            `json:"transport,omitempty"`
	Type        string            `json:"type,omitempty"` // Claude Code / Cursor alias ("stdio" | "http" | "sse")
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Exposure    string            `json:"exposure,omitempty"`
	Disabled    bool              `json:"disabled,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
}

// DiscoverLocalMCPServers scans workspace and user MCP config files in precedence order:
// 1. <workspace>/.mcp.json (workspace scope)
// 2. ~/.nex/mcp.json (user scope)
// 3. ~/.claude/mcp.json (user scope, Claude Code compatibility)
// Higher-precedence entries win on name collision.
func DiscoverLocalMCPServers(workspacePath, homeDir string) []DiscoveredMCPServer {
	ws := store.NormalizeWorkspaceKey(workspacePath)
	home := strings.TrimSpace(homeDir)
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	type candidate struct {
		path   string
		source string
		root   string
		wsPath string
	}
	var candidates []candidate
	if ws != "" {
		candidates = append(candidates, candidate{
			path:   filepath.Join(ws, ".mcp.json"),
			source: "workspace",
			root:   ".mcp.json",
			wsPath: ws,
		})
	}
	if home != "" {
		candidates = append(candidates,
			candidate{
				path:   filepath.Join(home, ".nex", "mcp.json"),
				source: "user",
				root:   "~/.nex/mcp.json",
			},
			candidate{
				path:   filepath.Join(home, ".claude", "mcp.json"),
				source: "user",
				root:   "~/.claude/mcp.json",
			},
		)
	}

	seen := make(map[string]bool)
	var out []DiscoveredMCPServer

	for _, c := range candidates {
		entries := parseMCPJSONFile(c.path)
		if len(entries) == 0 {
			continue
		}
		names := make([]string, 0, len(entries))
		for k := range entries {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, rawName := range names {
			name := strings.TrimSpace(rawName)
			if name == "" || seen[name] {
				continue
			}
			e := entries[rawName]
			transport := strings.ToLower(strings.TrimSpace(e.Transport))
			if transport == "" {
				transport = strings.ToLower(strings.TrimSpace(e.Type))
			}
			if transport == "" {
				if strings.TrimSpace(e.URL) != "" {
					transport = "http"
				} else if strings.TrimSpace(e.Command) != "" {
					transport = "stdio"
				}
			}
			if transport == "" {
				continue
			}
			enabled := !e.Disabled
			if e.Enabled != nil {
				enabled = *e.Enabled
			}
			exposure := store.NormalizeMCPExposure(e.Exposure)
			if exposure == "" {
				exposure = "codemode"
			}
			scope := "global"
			if c.source == "workspace" {
				scope = "workspace"
			}
			rec := store.MCPServerRecord{
				Name:          name,
				Description:   strings.TrimSpace(e.Description),
				Scope:         scope,
				WorkspacePath: c.wsPath,
				HostMode:      "runner_local",
				Transport:     transport,
				Command:       strings.TrimSpace(e.Command),
				Args:          e.Args,
				Env:           e.Env,
				URL:           strings.TrimSpace(e.URL),
				Headers:       e.Headers,
				Exposure:      exposure,
				IdleTTLSec:    300,
				Enabled:       enabled,
			}
			seen[name] = true
			out = append(out, DiscoveredMCPServer{
				MCPServerRecord: rec,
				Source:          c.source,
				SourcePath:      c.path,
				SourceRoot:      c.root,
			})
		}
	}

	return out
}

func parseMCPJSONFile(path string) map[string]rawMCPFileEntry {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	var wrapped struct {
		MCPServers map[string]rawMCPFileEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.MCPServers) > 0 {
		return wrapped.MCPServers
	}
	// Fallback: top-level map of server entries
	var direct map[string]rawMCPFileEntry
	if err := json.Unmarshal(data, &direct); err == nil && len(direct) > 0 {
		// Guard against non-entry top-level keys like {"mcpServers": {}}
		delete(direct, "mcpServers")
		if len(direct) > 0 {
			return direct
		}
	}
	return nil
}

// MergeDiscoveredServers merges file-discovered local MCP servers into the pool so they
// are immediately reachable via codemode without overwriting control-plane servers unless
// the discovered server is workspace-scoped.
func (p *Pool) MergeDiscoveredServers(discovered []DiscoveredMCPServer) {
	if len(discovered) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range discovered {
		if !d.Enabled {
			continue
		}
		// Workspace-scoped .mcp.json takes precedence; user-scoped only fills gaps.
		if _, exists := p.servers[d.Name]; exists && d.Source != "workspace" {
			continue
		}
		p.servers[d.Name] = d.MCPServerRecord
	}
}
