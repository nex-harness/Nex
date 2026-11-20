package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
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
	s.mux.HandleFunc("/api/v1/preferences", s.handlePreferences)
	s.mux.HandleFunc("/api/v1/providers", s.handleProviders)
	s.mux.HandleFunc("/api/v1/providers/", s.handleProviderDelete)
	s.mux.HandleFunc("/api/v1/models", s.handleModels)
	s.mux.HandleFunc("/api/v1/models/", s.handleModelDelete)
	s.mux.HandleFunc("/api/v1/models/sync", s.handleModelsSync)
	s.mux.HandleFunc("/api/v1/models/fetch-remote", s.handleModelsFetchRemote)
	s.mux.HandleFunc("/api/v1/models/lookup", s.handleModelLookup)
	s.mux.HandleFunc("/api/v1/models/test", s.handleModelTest)
	s.mux.HandleFunc("/api/v1/runners", s.handleRunners)
	s.mux.HandleFunc("/api/v1/runners/", s.handleRunnerSubroutes)
	s.mux.HandleFunc("/api/v1/sessions", s.handleSessions)
	s.mux.HandleFunc("/api/v1/sessions/", s.handleSessionSubroutes)
	s.mux.HandleFunc("/api/v1/archives", s.handleArchives)
	s.mux.HandleFunc("/api/v1/archives/", s.handleArchiveSubroutes)
	s.mux.HandleFunc("/api/v1/blobs/", s.handleBlobs)
	s.mux.HandleFunc("/api/v1/stats", s.handleStats)
	s.mux.HandleFunc("/api/v1/agents", s.handleAgentProfiles)
	s.mux.HandleFunc("/api/v1/agents/", s.handleAgentDelete)
	s.mux.HandleFunc("/api/v1/skills", s.handleSkills)
	s.mux.HandleFunc("/api/v1/skills/", s.handleSkillDelete)
	s.mux.HandleFunc("/api/v1/plugins", s.handlePlugins)
	s.mux.HandleFunc("/api/v1/plugins/", s.handlePluginDelete)
	s.mux.HandleFunc("/api/v1/mcp", s.handleMCPServers)
	s.mux.HandleFunc("/api/v1/mcp/", s.handleMCPDelete)
	s.mux.HandleFunc("/api/v1/cron", s.handleCronJobs)
	s.mux.HandleFunc("/api/v1/cron/", s.handleCronDelete)
	s.mux.HandleFunc("/api/v1/memories", s.handleMemories)
	s.mux.HandleFunc("/api/v1/memories/", s.handleMemoryDelete)

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
				fileServer.ServeHTTP(w, r)
				return
			}
			// SPA fallback to index.html
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
		var prefs store.UserPreferences
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
		writeJSON(w, http.StatusOK, prefs)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
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
		var m ai.ModelMeta
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if m.Source == "" {
			m.Source = "custom"
		}
		if err := s.store.UpsertModelsBatch(r.Context(), []ai.ModelMeta{m}, true); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, m)
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
	catalog, err := ai.FetchModelsDevCatalog(r.Context(), s.client, body.URL)
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
	writeJSON(w, http.StatusOK, map[string]any{"synced": len(filtered), "total_catalog": len(catalog)})
}

func (s *Server) handleModelsFetchRemote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
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
	writeJSON(w, http.StatusOK, map[string]any{"models": ids})
}

func (s *Server) handleModelLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("model_id"))
	if modelID == "" && r.Method == http.MethodPost {
		var body struct {
			ModelID string `json:"model_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		modelID = strings.TrimSpace(body.ModelID)
	}
	if modelID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model_id is required"})
		return
	}
	// Try live models.dev first with short timeout, fallback to embedded seed.
	catalog, err := ai.FetchModelsDevCatalog(r.Context(), s.client, "")
	if err != nil || len(catalog) == 0 {
		catalog, _ = ai.EmbeddedModelsCatalog()
	}
	if meta, ok := ai.LookupModelMeta(catalog, modelID); ok {
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
	// Mask auth tokens in UI responses
	for i := range list {
		list[i].AuthToken = ""
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRunnerSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/runners/")
	runnerID, action, _ := strings.Cut(rest, "/")
	if runnerID == "" || action != "dirs" {
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

	default:
		w.WriteHeader(http.StatusNotFound)
	}
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
