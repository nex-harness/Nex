package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/harness/codemode"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/skills"
	"github.com/nex-harness/nex/pkg/store"
)

var validMCPNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func (s *Server) firstOnlineRunnerID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id := range s.runners {
		return id
	}
	return ""
}

func (s *Server) isRunnerOnline(runnerID string) bool {
	if runnerID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.runners[runnerID]
	return ok
}

// handleSkillsDiscover scans the selected Runner (or local host fallback) for Agent Skills
// across workspace, user, and registry roots, and overlays Server visibility rules.
func (s *Server) handleSkillsDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runnerID := strings.TrimSpace(r.URL.Query().Get("runner_id"))
	workspacePath := strings.TrimSpace(r.URL.Query().Get("workspace_path"))

	rules, err := s.store.GetSkillVisibilityRules(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	targetRunner := runnerID
	if targetRunner == "" {
		targetRunner = s.firstOnlineRunnerID()
	}

	var discovered []skills.DiscoveredSkill
	usedRunner := ""
	if targetRunner != "" && s.isRunnerOnline(targetRunner) {
		if env, eErr := protocol.NewEnvelope(protocol.TypeSkillsDiscover, protocol.SkillsDiscoverPayload{
			WorkspacePath: workspacePath,
		}); eErr == nil {
			env.RunnerID = targetRunner
			if reply, rErr := s.callRunnerRPC(r.Context(), targetRunner, env, 8*time.Second); rErr == nil && reply.Type != protocol.TypeError {
				if json.Unmarshal(reply.Payload, &discovered) == nil {
					usedRunner = targetRunner
				}
			}
		}
	}

	if usedRunner == "" {
		regSkills, _ := s.store.ListSkills(r.Context())
		discovered = skills.Discover(skills.DiscoverOptions{
			WorkspacePath:  workspacePath,
			DataDir:        s.store.DataDir(),
			RegistrySkills: regSkills,
			Rules:          rules,
			IncludeContent: true,
		})
	} else {
		// Always overlay Server's authoritative visibility rules
		for i := range discovered {
			skills.ApplyVisibility(&discovered[i], rules, workspacePath)
		}
	}
	if discovered == nil {
		discovered = []skills.DiscoveredSkill{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"skills":         discovered,
		"runner_id":      usedRunner,
		"workspace_path": store.NormalizeWorkspaceKey(workspacePath),
		"rules":          rules,
	})
}

// handleSkillVisibility updates global or per-workspace visibility for a discovered skill.
func (s *Server) handleSkillVisibility(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name          string `json:"name"`
		ScopeTarget   string `json:"scope_target"` // "global" | "workspace"
		WorkspacePath string `json:"workspace_path"`
		Enabled       *bool  `json:"enabled"` // nil resets workspace override to inherit global
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.ScopeTarget) == "" {
		body.ScopeTarget = "global"
	}
	rules, err := s.store.SetSkillVisibility(r.Context(), body.Name, body.ScopeTarget, body.WorkspacePath, body.Enabled)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.BroadcastConfigSync(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"rules": rules,
	})
}

// handleMCPImport parses Claude Desktop / Cursor / Pi / VS Code and generic MCP JSON definitions
// and installs/updates them in bulk.
func (s *Server) handleMCPImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RawJSON       string          `json:"raw_json"`
		Config        json.RawMessage `json:"config"`
		Scope         string          `json:"scope"`
		WorkspacePath string          `json:"workspace_path"`
		HostMode      string          `json:"host_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	raw := body.Config
	if len(raw) == 0 && strings.TrimSpace(body.RawJSON) != "" {
		raw = json.RawMessage(strings.TrimSpace(body.RawJSON))
	}
	if len(raw) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("raw_json or config is required"))
		return
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}

	serverMap := top
	if nested, ok := top["mcpServers"]; ok {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(nested, &m); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid mcpServers object: %w", err))
			return
		}
		serverMap = m
	} else if nested, ok := top["servers"]; ok {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(nested, &m); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid servers object: %w", err))
			return
		}
		serverMap = m
	}

	if len(serverMap) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("no MCP server entries found in JSON"))
		return
	}

	defaultScope := strings.TrimSpace(body.Scope)
	if defaultScope == "" {
		defaultScope = "global"
	}
	defaultHostMode := strings.TrimSpace(body.HostMode)
	if defaultHostMode == "" {
		defaultHostMode = "runner_local"
	}

	var imported []store.MCPServerRecord
	for name, entryRaw := range serverMap {
		cleanName := strings.TrimSpace(name)
		if !validMCPNameRE.MatchString(cleanName) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid MCP server name %q (allowed: letters, digits, _, -)", name))
			return
		}
		var spec struct {
			Type        string            `json:"type"`
			Transport   string            `json:"transport"`
			HostMode    string            `json:"host_mode"`
			Command     string            `json:"command"`
			Args        []string          `json:"args"`
			Env         map[string]string `json:"env"`
			URL         string            `json:"url"`
			Headers     map[string]string `json:"headers"`
			Description string            `json:"description"`
			Exposure    string            `json:"exposure"`
			Enabled     *bool             `json:"enabled"`
			IdleTTLSec  int               `json:"idle_ttl_sec"`
			Timeout     int               `json:"timeout"`
		}
		if err := json.Unmarshal(entryRaw, &spec); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid entry %q: %w", cleanName, err))
			return
		}
		tKind := strings.ToLower(strings.TrimSpace(spec.Type))
		if tKind == "" {
			tKind = strings.ToLower(strings.TrimSpace(spec.Transport))
		}
		if tKind == "sse" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("server %q uses legacy sse transport; use streamable HTTP (type: http) instead", cleanName))
			return
		}
		if tKind == "" {
			if strings.TrimSpace(spec.URL) != "" {
				tKind = "http"
			} else {
				tKind = "stdio"
			}
		} else if tKind == "streamable-http" || tKind == "streamable_http" {
			tKind = "http"
		}
		if tKind == "stdio" && strings.TrimSpace(spec.Command) == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("stdio MCP server %q requires command", cleanName))
			return
		}
		if tKind == "http" && strings.TrimSpace(spec.URL) == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("HTTP MCP server %q requires url", cleanName))
			return
		}

		hostMode := strings.TrimSpace(spec.HostMode)
		if hostMode == "" {
			hostMode = defaultHostMode
		}
		enabled := true
		if spec.Enabled != nil {
			enabled = *spec.Enabled
		}
		rec := store.MCPServerRecord{
			Name:          cleanName,
			Description:   strings.TrimSpace(spec.Description),
			HostMode:      hostMode,
			Transport:     tKind,
			Command:       strings.TrimSpace(spec.Command),
			Args:          spec.Args,
			Env:           spec.Env,
			URL:           strings.TrimSpace(spec.URL),
			Headers:       spec.Headers,
			Exposure:      store.NormalizeMCPExposure(spec.Exposure),
			Scope:         defaultScope,
			WorkspacePath: store.NormalizeWorkspaceKey(body.WorkspacePath),
			IdleTTLSec:    spec.IdleTTLSec,
			Enabled:       enabled,
		}
		if err := s.store.UpsertMCPServer(r.Context(), rec); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if saved, err := s.store.GetMCPServer(r.Context(), cleanName); err == nil && saved != nil {
			imported = append(imported, *saved)
		} else {
			imported = append(imported, rec)
		}
	}

	s.BroadcastConfigSync(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"count":    len(imported),
		"imported": imported,
	})
}

// handleMCPSubroutes handles:
// - DELETE /api/v1/mcp/{name}
// - POST   /api/v1/mcp/{name}/test  (or /probe)
// - PUT    /api/v1/mcp/{name}/visibility
func (s *Server) handleMCPSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/mcp/")
	parts := strings.SplitN(rest, "/", 2)
	rawName := parts[0]
	name, err := url.PathUnescape(rawName)
	if err != nil {
		name = rawName
	}
	name = strings.TrimSpace(name)
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	action := ""
	if len(parts) == 2 {
		action = strings.Trim(parts[1], "/")
	}

	switch {
	case action == "" && r.Method == http.MethodDelete:
		if err := s.store.DeleteResourceByName(r.Context(), "mcp_servers", "name", name); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})

	case (action == "test" || action == "probe") && r.Method == http.MethodPost:
		rec, err := s.store.GetMCPServer(r.Context(), name)
		if err != nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("MCP server %q not found", name))
			return
		}
		runnerID := strings.TrimSpace(r.URL.Query().Get("runner_id"))
		var body struct {
			RunnerID string `json:"runner_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if runnerID == "" {
			runnerID = strings.TrimSpace(body.RunnerID)
		}

		var probeRes codemode.MCPProbeResult
		probedRemotely := false
		if rec.HostMode == "runner_local" {
			targetRunner := runnerID
			if targetRunner == "" {
				targetRunner = s.firstOnlineRunnerID()
			}
			if targetRunner != "" && s.isRunnerOnline(targetRunner) {
				if env, eErr := protocol.NewEnvelope(protocol.TypeMCPProbe, protocol.MCPProbePayload{
					Server: *rec,
				}); eErr == nil {
					env.RunnerID = targetRunner
					if reply, rErr := s.callRunnerRPC(r.Context(), targetRunner, env, 20*time.Second); rErr == nil && reply.Type != protocol.TypeError {
						if json.Unmarshal(reply.Payload, &probeRes) == nil && probeRes.Status != "" {
							probedRemotely = true
						}
					}
				}
			}
		}
		if !probedRemotely {
			probeRes = codemode.ProbeServer(r.Context(), *rec)
		}

		catBytes, _ := json.Marshal(probeRes.Tools)
		if len(catBytes) == 0 || string(catBytes) == "null" {
			catBytes = []byte("[]")
		}
		_ = s.store.UpdateMCPServerProbeResult(r.Context(), name, probeRes.Status, probeRes.Error, catBytes)
		s.BroadcastConfigSync(r.Context())
		updated, _ := s.store.GetMCPServer(r.Context(), name)
		writeJSON(w, http.StatusOK, map[string]any{
			"probe":  probeRes,
			"server": updated,
		})

	case action == "visibility" && (r.Method == http.MethodPut || r.Method == http.MethodPost):
		var body struct {
			ScopeTarget   string `json:"scope_target"` // "global" | "workspace" | ""
			WorkspacePath string `json:"workspace_path"`
			Enabled       *bool  `json:"enabled"`
			Exposure      string `json:"exposure"` // "codemode" | "deferred" | "direct" | "hidden"
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		updated, err := s.store.SetMCPServerVisibility(r.Context(), name, body.ScopeTarget, body.WorkspacePath, body.Enabled, body.Exposure)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, updated)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
