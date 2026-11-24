package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func (s *Server) registerRoutes() {
	// WebSocket endpoints
	s.mux.HandleFunc("/ws/runner", s.handleRunnerWS)
	s.mux.HandleFunc("/ws/client", s.handleClientWS)

	// Optional LLM Gateway Proxy for zero-key managed runners
	s.mux.HandleFunc("/llm-proxy/", s.handleLLMProxy)

	// REST API v1
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("/api/v1/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("/api/v1/auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("/api/v1/auth/password", s.handleAuthPassword)
	s.mux.HandleFunc("/api/v1/preferences", s.handlePreferences)
	s.mux.HandleFunc("/api/v1/instructions/global", s.handleGlobalInstructions)
	s.mux.HandleFunc("/api/v1/providers", s.handleProviders)
	s.mux.HandleFunc("/api/v1/providers/", s.handleProviderDelete)
	s.mux.HandleFunc("/api/v1/models", s.handleModels)
	s.mux.HandleFunc("/api/v1/models/", s.handleModelDelete)
	s.mux.HandleFunc("/api/v1/models/sync", s.handleModelsSync)
	s.mux.HandleFunc("/api/v1/models/fetch-remote", s.handleModelsFetchRemote)
	s.mux.HandleFunc("/api/v1/models/lookup", s.handleModelLookup)
	s.mux.HandleFunc("/api/v1/models/test", s.handleModelTest)
	s.mux.HandleFunc("/api/v1/image/test", s.handleImageTest)
	s.mux.HandleFunc("/api/v1/classifier/test", s.handleClassifierTest)
	s.mux.HandleFunc("/api/v1/runners", s.handleRunners)
	s.mux.HandleFunc("/api/v1/runners/auth", s.handleRunnersAuth)
	s.mux.HandleFunc("/api/v1/runners/auth/rotate", s.handleRunnersAuthRotate)
	s.mux.HandleFunc("/api/v1/runners/manifest", s.handleRunnersManifest)
	s.mux.HandleFunc("/api/v1/runners/binary/", s.handleRunnerBinaryDownload)
	s.mux.HandleFunc("/api/v1/runners/update-all", s.handleRunnersUpdateAll)
	s.mux.HandleFunc("/api/v1/runners/", s.handleRunnerSubroutes)
	s.mux.HandleFunc("/api/v1/sessions", s.handleSessions)
	s.mux.HandleFunc("/api/v1/sessions/search", s.handleSessionSearch)
	s.mux.HandleFunc("/api/v1/sessions/", s.handleSessionSubroutes)
	s.mux.HandleFunc("/api/v1/archives", s.handleArchives)
	s.mux.HandleFunc("/api/v1/archives/", s.handleArchiveSubroutes)
	s.mux.HandleFunc("/api/v1/blobs/", s.handleBlobs)
	s.mux.HandleFunc("/api/v1/stats", s.handleStats)
	s.mux.HandleFunc("/api/v1/agents", s.handleAgentProfiles)
	s.mux.HandleFunc("/api/v1/agents/", s.handleAgentDelete)
	s.mux.HandleFunc("/api/v1/skills", s.handleSkills)
	s.mux.HandleFunc("/api/v1/skills/discover", s.handleSkillsDiscover)
	s.mux.HandleFunc("/api/v1/skills/visibility", s.handleSkillVisibility)
	s.mux.HandleFunc("/api/v1/skills/", s.handleSkillDelete)
	s.mux.HandleFunc("/api/v1/plugins", s.handlePlugins)
	s.mux.HandleFunc("/api/v1/plugins/", s.handlePluginDelete)
	s.mux.HandleFunc("/api/v1/mcp", s.handleMCPServers)
	s.mux.HandleFunc("/api/v1/mcp/import", s.handleMCPImport)
	s.mux.HandleFunc("/api/v1/mcp/", s.handleMCPSubroutes)
	s.mux.HandleFunc("/api/v1/cron", s.handleCronJobs)
	s.mux.HandleFunc("/api/v1/cron/", s.handleCronDelete)
	s.mux.HandleFunc("/api/v1/credentials", s.handleCredentials)
	s.mux.HandleFunc("/api/v1/credentials/import-env", s.handleCredentialsImportEnv)
	s.mux.HandleFunc("/api/v1/credentials/", s.handleCredentialSubroutes)
	s.mux.HandleFunc("/api/v1/memories", s.handleMemories)
	s.mux.HandleFunc("/api/v1/memories/consolidate", s.handleMemoryConsolidate)
	s.mux.HandleFunc("/api/v1/memories/", s.handleMemoryDelete)
	s.mux.HandleFunc("/api/v1/ssh/config-hosts", s.handleSSHConfigHosts)
	s.mux.HandleFunc("/api/v1/ssh/targets", s.handleSSHTargets)
	s.mux.HandleFunc("/api/v1/ssh/targets/", s.handleSSHTargetSubroutes)
	s.mux.HandleFunc("/api/v1/ssh/binaries", s.handleSSHBinaries)
	s.mux.HandleFunc("/api/v1/ssh/binary", s.handleSSHBinaryDownload)
	s.mux.HandleFunc("/api/v1/remote/status", s.handleRemoteStatus)
	s.mux.HandleFunc("/api/v1/remote/config", s.handleRemoteConfig)
	s.mux.HandleFunc("/api/v1/remote/pair/start", s.handleRemotePairStart)
	s.mux.HandleFunc("/api/v1/remote/pair/complete", s.handleRemotePairComplete)
	s.mux.HandleFunc("/api/v1/remote/devices", s.handleRemoteDevices)
	s.mux.HandleFunc("/api/v1/remote/devices/", s.handleRemoteDeviceSubroutes)
	s.mux.HandleFunc("/api/v1/remote/tsnet/probe", s.handleRemoteTSNetProbe)
	s.mux.HandleFunc("/api/v1/remote/tsnet/install", s.handleRemoteTSNetInstall)
	s.mux.HandleFunc("/api/v1/remote/tsnet", s.handleRemoteTSNetUninstall)

	if s.relay != nil {
		s.relay.Mount(s.mux)
	}

	// Embedded Web UI SPA fallback
	if s.cfg.StaticFS != nil {
		fileServer := http.FileServer(http.FS(s.cfg.StaticFS))
		s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
			if clean == "" {
				clean = "index.html"
			}
			if f, err := s.cfg.StaticFS.Open(clean); err == nil {
				_ = f.Close()
				if strings.HasPrefix(clean, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else if clean == "index.html" {
					w.Header().Set("Cache-Control", "no-cache")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			// SPA fallback to index.html
			w.Header().Set("Cache-Control", "no-cache")
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	onlineRunners := len(s.runners)
	onlineClients := len(s.clients)
	s.mu.RUnlock()
	rev, _ := s.store.GetConfigRev(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"online_runners": onlineRunners,
		"online_clients": onlineClients,
		"config_rev":     rev,
	})
}

func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		prefs, err := s.store.GetUserPreferences(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, prefs)
	case http.MethodPut, http.MethodPost:
		prefs, err := s.store.GetUserPreferences(r.Context())
		if err != nil {
			prefs = store.DefaultUserPreferences()
		}
		if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := s.store.SaveUserPreferences(r.Context(), prefs); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if prefs.AutoArchiveAfterDays > 0 {
			_, _ = s.store.ArchiveIdleSessions(r.Context(), time.Duration(prefs.AutoArchiveAfterDays)*24*time.Hour, time.Now())
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, prefs)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGlobalInstructions serves the user-global AGENTS.md (~/.nex/AGENTS.md).
// The content is persisted in SQLite and mirrored to disk so that local sessions
// and Runners pick it up through prompts.BuildSystemSections.
func (s *Server) handleGlobalInstructions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		prefs, err := s.store.GetUserPreferences(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		content := prefs.GlobalInstructions
		if content == "" {
			if b, readErr := os.ReadFile(globalInstructionsPath()); readErr == nil {
				content = string(b)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"content": content,
			"path":    globalInstructionsDisplayPath,
		})
	case http.MethodPut, http.MethodPost:
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		prefs, err := s.store.GetUserPreferences(r.Context())
		if err != nil {
			prefs = store.DefaultUserPreferences()
		}
		prefs.GlobalInstructions = body.Content
		if err := s.store.SaveUserPreferences(r.Context(), prefs); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// Best-effort disk mirror: ~/.nex/AGENTS.md keeps local sessions and
		// any non-synced runner reading the same content.
		path := globalInstructionsPath()
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr == nil {
			_ = os.WriteFile(path, []byte(body.Content), 0o600)
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{
			"content": body.Content,
			"path":    globalInstructionsDisplayPath,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// globalInstructionsDisplayPath is the user-facing location of the global AGENTS.md.
const globalInstructionsDisplayPath = "~/.nex/AGENTS.md"

// globalInstructionsPath resolves the on-disk global AGENTS.md path, falling
// back to a relative path when the home directory cannot be determined.
func globalInstructionsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".nex", "AGENTS.md")
	}
	return filepath.Join(home, ".nex", "AGENTS.md")
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListProviders(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost, http.MethodPut:
		var p store.ProviderRecord
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := s.store.UpsertProvider(r.Context(), p); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, p)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		provID := r.URL.Query().Get("provider_id")
		list, err := s.store.ListModels(r.Context(), provID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost, http.MethodPut:
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		trimmed := strings.TrimSpace(string(body))
		var batch []ai.ModelMeta
		if strings.HasPrefix(trimmed, "[") {
			if err := json.Unmarshal(body, &batch); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		} else {
			var m ai.ModelMeta
			if err := json.Unmarshal(body, &m); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			batch = []ai.ModelMeta{m}
		}
		for i := range batch {
			if batch[i].Source == "" {
				batch[i].Source = "custom"
			}
		}
		if err := s.store.UpsertModelsBatch(r.Context(), batch, true); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		if len(batch) == 1 && !strings.HasPrefix(trimmed, "[") {
			writeJSON(w, http.StatusOK, batch[0])
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"saved": len(batch)})
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleModelsSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	catalog, err := s.refreshModelsCatalog(r.Context(), strings.TrimSpace(body.URL), strings.TrimSpace(body.URL) == "")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	// Filter to registered providers in store so foreign key constraints are satisfied
	provs, _ := s.store.ListProviders(r.Context())
	knownProv := make(map[string]bool, len(provs))
	for _, p := range provs {
		knownProv[p.ID] = true
	}
	var filtered []ai.ModelMeta
	for _, m := range catalog {
		if knownProv[m.ProviderID] {
			filtered = append(filtered, m)
		}
	}
	if err := s.store.UpsertModelsBatch(r.Context(), filtered, false); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.BroadcastConfigSync(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"synced":        len(filtered),
		"synced_count":  len(filtered),
		"total_catalog": len(catalog),
	})
}

func (s *Server) handleModelsFetchRemote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		BaseURL    string `json:"base_url"`
		APIKey     string `json:"api_key"`
		ProviderID string `json:"provider_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	base := strings.TrimRight(strings.TrimSpace(body.BaseURL), "/")
	if base == "" {
		writeErr(w, http.StatusBadRequest, errors.New("base_url is required"))
		return
	}
	endpoint := base + "/models"
	if !strings.HasSuffix(base, "/v1") {
		endpoint = base + "/v1/models"
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Header.Set("User-Agent", ai.DefaultUserAgent)
	if strings.TrimSpace(body.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(body.APIKey))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "upstream /v1/models returned HTTP " + strconv.Itoa(resp.StatusCode)})
		return
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	ids := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if strings.TrimSpace(item.ID) != "" {
			ids = append(ids, item.ID)
		}
	}
	// Resolve metadata from the cached models.dev catalog in one pass so batch-add needs zero extra lookups.
	catalog := s.getModelsDevCatalog(r.Context())
	matchedMeta := make(map[string]ai.ModelMeta, len(ids))
	for _, id := range ids {
		if meta, ok := ai.LookupModelMetaForProvider(catalog, id, body.ProviderID); ok {
			matchedMeta[id] = meta
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":       ids,
		"matched_meta": matchedMeta,
	})
}

func (s *Server) handleModelLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
	providerID := strings.TrimSpace(r.URL.Query().Get("provider_id"))
	var modelIDs []string
	if r.Method == http.MethodPost {
		var body struct {
			ModelID    string   `json:"model_id"`
			ModelIDs   []string `json:"model_ids"`
			ProviderID string   `json:"provider_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if modelID == "" {
			modelID = strings.TrimSpace(body.ModelID)
		}
		if providerID == "" {
			providerID = strings.TrimSpace(body.ProviderID)
		}
		modelIDs = body.ModelIDs
	}

	catalog := s.getModelsDevCatalog(r.Context())

	if len(modelIDs) > 0 {
		results := make(map[string]ai.ModelMeta, len(modelIDs))
		for _, rawID := range modelIDs {
			clean := strings.TrimSpace(rawID)
			if clean == "" {
				continue
			}
			if meta, ok := ai.LookupModelMetaForProvider(catalog, clean, providerID); ok {
				results[clean] = meta
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
		return
	}

	if modelID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model_id is required"})
		return
	}
	if meta, ok := ai.LookupModelMetaForProvider(catalog, modelID, providerID); ok {
		writeJSON(w, http.StatusOK, map[string]any{"found": true, "meta": meta})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": false})
}

func (s *Server) handleModelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Driver  string `json:"driver"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		ModelID string `json:"model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	prov, err := ai.NewProvider(ai.Driver(body.Driver), body.BaseURL, body.APIKey, s.client)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	start := time.Now()
	ch, err := prov.Stream(r.Context(), &ai.StreamRequest{
		Model:        body.ModelID,
		SystemPrompt: "Reply with OK.",
		Messages: []ai.Message{
			{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.BlockText, Text: "ping"}}},
		},
		MaxOutputTokens: 16,
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "latency_ms": time.Since(start).Milliseconds()})
		return
	}
	var out strings.Builder
	var streamErr string
	for ev := range ch {
		if ev.Type == ai.StreamEventTextDelta {
			out.WriteString(ev.TextDelta)
		} else if ev.Type == ai.StreamEventError && ev.Err != nil {
			streamErr = ev.Err.Error()
		}
	}
	if streamErr != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": streamErr, "latency_ms": time.Since(start).Milliseconds()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reply": strings.TrimSpace(out.String()), "latency_ms": time.Since(start).Milliseconds()})
}

func (s *Server) handleRunners(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRunners(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Mask auth tokens and enrich live runner update/compatibility metadata
	for i := range list {
		list[i].AuthToken = ""
		s.enrichRunnerRecord(r.Context(), &list[i])
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRunnerSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/runners/")
	runnerID, action, _ := strings.Cut(rest, "/")
	if runnerID == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if action == "" && r.Method == http.MethodDelete {
		s.mu.Lock()
		rc := s.runners[runnerID]
		if rc != nil {
			rc.evicted = true
			delete(s.runners, runnerID)
		}
		s.mu.Unlock()
		if rc != nil {
			_ = rc.ws.Close(1000, "runner removed")
		}
		if err := s.store.DeleteResourceByName(r.Context(), "runners", "id", runnerID); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.broadcastRunnerStatus(r.Context(), runnerID, "deleted")
		writeJSON(w, http.StatusOK, map[string]any{"deleted": runnerID})
		return
	}
	if action == "update" {
		s.handleRunnerUpdateTrigger(w, r, runnerID)
		return
	}
	if action != "dirs" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	reqPath := strings.TrimSpace(r.URL.Query().Get("path"))
	includeHidden := r.URL.Query().Get("include_hidden") == "1" || r.URL.Query().Get("include_hidden") == "true"

	env, err := protocol.NewEnvelope(protocol.TypeWorkspaceReq, map[string]any{
		"action":         "fs_browse_dirs",
		"workspace_path": reqPath,
		"include_hidden": includeHidden,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	env.RunnerID = runnerID
	resp, err := s.callRunnerRPC(r.Context(), runnerID, env, 15*time.Second)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if resp.Type == protocol.TypeError {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(resp.Payload)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp.Payload)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		runnerID := r.URL.Query().Get("runner_id")
		incChildren := r.URL.Query().Get("include_children") == "1"
		list, err := s.store.ListSessions(r.Context(), runnerID, incChildren)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		var sess store.SessionRecord
		if err := json.NewDecoder(r.Body).Decode(&sess); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		created, err := s.store.CreateOrUpdateSession(r.Context(), sess)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSessionSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	sessID, action, _ := strings.Cut(rest, "/")
	if sessID == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			sess, err := s.store.GetSession(r.Context(), sessID)
			if err != nil {
				writeErr(w, http.StatusNotFound, err)
				return
			}
			writeJSON(w, http.StatusOK, sess)
		case http.MethodPatch, http.MethodPut:
			sess, err := s.store.GetSession(r.Context(), sessID)
			if err != nil {
				writeErr(w, http.StatusNotFound, err)
				return
			}
			var patch struct {
				Title          *string `json:"title"`
				PermissionMode *string `json:"permission_mode"`
				ProviderID     *string `json:"provider_id"`
				ModelID        *string `json:"model_id"`
				ThinkingEffort *string `json:"thinking_effort"`
			}
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			if patch.Title != nil && strings.TrimSpace(*patch.Title) != "" {
				sess.Title = strings.TrimSpace(*patch.Title)
			}
			if patch.PermissionMode != nil && *patch.PermissionMode != "" {
				sess.PermissionMode = *patch.PermissionMode
			}
			if patch.ProviderID != nil && *patch.ProviderID != "" {
				sess.ProviderID = *patch.ProviderID
			}
			if patch.ModelID != nil && *patch.ModelID != "" {
				sess.ModelID = *patch.ModelID
			}
			if patch.ThinkingEffort != nil && strings.TrimSpace(*patch.ThinkingEffort) != "" {
				sess.ThinkingEffort = strings.TrimSpace(*patch.ThinkingEffort)
			}
			updated, err := s.store.CreateOrUpdateSession(r.Context(), *sess)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, updated)
		case http.MethodDelete:
			if err := s.store.DeleteActiveSession(r.Context(), sessID); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "session_id": sessID})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}

	case "events":
		after, _ := strconv.ParseUint(r.URL.Query().Get("after_seq"), 10, 64)
		evs, err := s.store.ListSessionEventsAfter(r.Context(), sessID, after)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, evs)

	case "telemetry":
		tels, err := s.store.ListSessionTelemetry(r.Context(), sessID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, tels)

	case "archive":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := s.store.ArchiveSession(r.Context(), sessID); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"archived": true, "session_id": sessID})

	case "fork":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			UpToSeq    uint64 `json:"up_to_seq"`
			BeforeSeq  uint64 `json:"before_seq"`
			Title      string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		upTo := body.UpToSeq
		excludeAll := false
		if body.BeforeSeq > 0 {
			if body.BeforeSeq <= 1 {
				excludeAll = true
			} else {
				upTo = body.BeforeSeq - 1
			}
		}
		forked, copied, err := s.store.ForkSession(r.Context(), sessID, upTo, excludeAll, strings.TrimSpace(body.Title))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		// Mirror forked history to the target Runner if currently online so next LLM turn has full history!
		if forked.RunnerID != "" {
			if env, err := protocol.NewEnvelope(protocol.TypeSessionForkSync, protocol.SessionForkSyncPayload{
				Session: forked,
				Events:  copied,
			}); err == nil {
				env.RunnerID = forked.RunnerID
				env.SessionID = forked.ID
				_, _ = s.callRunnerRPC(r.Context(), forked.RunnerID, env, 8*time.Second)
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"session":       forked,
			"copied_events": len(copied),
		})

	case "export":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		md, filename, err := s.store.ExportSessionMarkdown(r.Context(), sessID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		if r.URL.Query().Get("format") == "json" {
			writeJSON(w, http.StatusOK, map[string]string{
				"filename": filename,
				"markdown": md,
			})
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(md))

	case "checkpoints":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		sess, err := s.store.GetSession(r.Context(), sessID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		if sess.RunnerID != "" {
			if env, eErr := protocol.NewEnvelope(protocol.TypeSessionRewind, protocol.SessionRewindPayload{
				Action:    "list",
				SessionID: sessID,
			}); eErr == nil {
				env.RunnerID = sess.RunnerID
				env.SessionID = sessID
				if reply, rErr := s.callRunnerRPC(r.Context(), sess.RunnerID, env, 8*time.Second); rErr == nil && reply.Type != protocol.TypeError {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(reply.Payload)
					return
				}
			}
		}
		cps, err := s.store.ListFileCheckpoints(r.Context(), sessID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if cps == nil {
			cps = []store.FileCheckpoint{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"checkpoints": cps})

	case "truncate":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		sess, err := s.store.GetSession(r.Context(), sessID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		var body struct {
			FromSeq uint64 `json:"from_seq"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.FromSeq == 0 {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("from_seq > 0 is required"))
			return
		}
		removed, err := s.store.TruncateSessionEventsFrom(r.Context(), sessID, body.FromSeq)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// Mirror truncated history to the target Runner if currently online so next LLM turn uses truncated history
		if sess.RunnerID != "" {
			remaining, _ := s.store.ListSessionEventsAfter(r.Context(), sessID, 0)
			if env, eErr := protocol.NewEnvelope(protocol.TypeSessionForkSync, protocol.SessionForkSyncPayload{
				Session: *sess,
				Events:  remaining,
			}); eErr == nil {
				env.RunnerID = sess.RunnerID
				env.SessionID = sessID
				_, _ = s.callRunnerRPC(r.Context(), sess.RunnerID, env, 8*time.Second)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"removed":  removed,
			"from_seq": body.FromSeq,
		})

	case "rewind":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		sess, err := s.store.GetSession(r.Context(), sessID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		var body struct {
			BeforeSeq uint64 `json:"before_seq"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if sess.RunnerID != "" {
			if env, eErr := protocol.NewEnvelope(protocol.TypeSessionRewind, protocol.SessionRewindPayload{
				Action:    "rewind",
				SessionID: sessID,
				BeforeSeq: body.BeforeSeq,
			}); eErr == nil {
				env.RunnerID = sess.RunnerID
				env.SessionID = sessID
				if reply, rErr := s.callRunnerRPC(r.Context(), sess.RunnerID, env, 15*time.Second); rErr == nil {
					if reply.Type == protocol.TypeError {
						w.Header().Set("Content-Type", "application/json; charset=utf-8")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write(reply.Payload)
						return
					}
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(reply.Payload)
					return
				}
			}
		}
		res, err := s.store.RewindSessionFiles(r.Context(), sessID, body.BeforeSeq)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, res)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) handleSessionSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := s.store.SearchSessions(r.Context(), q, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if hits == nil {
		hits = []store.SessionSearchHit{}
	}
	writeJSON(w, http.StatusOK, hits)
}

func (s *Server) handleArchives(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	list, err := s.store.ListArchivedSessions(r.Context(), limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleArchiveSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/archives/")
	sessID, action, _ := strings.Cut(rest, "/")
	if sessID == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch {
	case action == "events" && r.Method == http.MethodGet:
		evs, err := s.store.GetArchivedSessionEvents(r.Context(), sessID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, evs)

	case action == "unarchive" && r.Method == http.MethodPost:
		if err := s.store.UnarchiveSession(r.Context(), sessID); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"unarchived": true, "session_id": sessID})

	case action == "" && r.Method == http.MethodDelete:
		if err := s.store.DeleteArchivedSessionPermanently(r.Context(), sessID); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "session_id": sessID})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) handleBlobs(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/api/v1/blobs/")
	hash = strings.TrimPrefix(hash, "sha256:")
	if hash == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	data, mime, err := s.store.GetBlob(r.Context(), hash)
	if err != nil {
		// On-demand pull from online Runner if blob originated on Runner!
		data, mime, err = s.FetchBlobFromAnyRunner(r.Context(), hash)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
	}
	if mime == "" {
		mime = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", mime)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	runnerID := r.URL.Query().Get("runner_id")
	winStr := r.URL.Query().Get("window")
	if winStr == "" {
		winStr = string(store.WindowToday)
	}
	tzName := r.URL.Query().Get("tz")
	loc := time.Local
	if tzName != "" {
		if parsedLoc, err := time.LoadLocation(tzName); err == nil {
			loc = parsedLoc
		}
	}
	sum, err := s.store.QueryUsageSummary(r.Context(), runnerID, store.UsageWindow(winStr), time.Now(), loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleAgentProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListAgentProfiles(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost, http.MethodPut:
		var p store.AgentProfile
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := s.store.UpsertAgentProfile(r.Context(), p); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, p)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListSkills(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost, http.MethodPut:
		var sk store.SkillRecord
		if err := json.NewDecoder(r.Body).Decode(&sk); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := s.store.UpsertSkill(r.Context(), sk); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, sk)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListPlugins(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost, http.MethodPut:
		var pl store.PluginRecord
		if err := json.NewDecoder(r.Body).Decode(&pl); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if pl.SourceTS != "" {
			js, hash, err := TranspilePluginTS(pl.SourceTS)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			pl.CompiledJS = js
			pl.CompiledJSHash = hash
		}
		if err := s.store.UpsertPlugin(r.Context(), pl); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, pl)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMCPServers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListMCPServers(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost, http.MethodPut:
		var m store.MCPServerRecord
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := s.store.UpsertMCPServer(r.Context(), m); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, m)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCronJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		runnerID := r.URL.Query().Get("runner_id")
		list, err := s.store.ListCronJobs(r.Context(), runnerID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost, http.MethodPut:
		var j store.CronJobRecord
		if err := json.NewDecoder(r.Body).Decode(&j); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		saved, err := s.store.UpsertCronJob(r.Context(), j)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, saved)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCronDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/cron/")
	if err := s.store.DeleteCronJob(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.BroadcastConfigSync(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) deleteRegistryHandler(w http.ResponseWriter, r *http.Request, prefix, table, col string) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, prefix)
	if err := s.store.DeleteResourceByName(r.Context(), table, col, key); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.BroadcastConfigSync(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	s.deleteRegistryHandler(w, r, "/api/v1/providers/", "providers", "id")
}
func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/models/")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	provID := r.URL.Query().Get("provider_id")
	if err := s.store.DeleteModel(r.Context(), provID, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.BroadcastConfigSync(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}
func (s *Server) handleAgentDelete(w http.ResponseWriter, r *http.Request) {
	s.deleteRegistryHandler(w, r, "/api/v1/agents/", "agent_profiles", "slug")
}
func (s *Server) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	s.deleteRegistryHandler(w, r, "/api/v1/skills/", "skills_registry", "name")
}
func (s *Server) handlePluginDelete(w http.ResponseWriter, r *http.Request) {
	s.deleteRegistryHandler(w, r, "/api/v1/plugins/", "plugins_registry", "name")
}
func (s *Server) handleMCPDelete(w http.ResponseWriter, r *http.Request) {
	s.deleteRegistryHandler(w, r, "/api/v1/mcp/", "mcp_servers", "name")
}

func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ws := r.URL.Query().Get("workspace_path")
		list, err := s.store.ListMemories(r.Context(), ws)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost, http.MethodPut:
		var rec store.MemoryRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(rec.Title) == "" || strings.TrimSpace(rec.Content) == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("title and content are required"))
			return
		}
		saved, err := s.store.UpsertMemory(r.Context(), rec)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, saved)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/memories/")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteMemory(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.BroadcastConfigSync(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}
