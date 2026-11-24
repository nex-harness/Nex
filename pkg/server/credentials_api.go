package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func (s *Server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListCredentials(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		sanitized := make([]store.CredentialRecord, 0, len(list))
		for _, c := range list {
			sanitized = append(sanitized, c.Sanitized())
		}
		writeJSON(w, http.StatusOK, sanitized)
	case http.MethodPost, http.MethodPut:
		var rec store.CredentialRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		saved, err := s.store.UpsertCredential(r.Context(), rec)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		s.BroadcastConfigSync(r.Context())
		writeJSON(w, http.StatusOK, saved.Sanitized())
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCredentialsImportEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID            string   `json:"id,omitempty"`
		Name          string   `json:"name"`
		Description   string   `json:"description,omitempty"`
		StorageMode   string   `json:"storage_mode,omitempty"`
		UsagePolicy   string   `json:"usage_policy,omitempty"`
		Scope         string   `json:"scope,omitempty"`
		WorkspacePath string   `json:"workspace_path,omitempty"`
		TargetRunners []string `json:"target_runners,omitempty"`
		RawEnv        string   `json:"raw_env"`
		Merge         bool     `json:"merge"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	parsed := store.ParseDotEnv(body.RawEnv)
	if len(parsed) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("no valid KEY=VALUE entries found in pasted .env content"))
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Imported .env"
	}

	rec := store.CredentialRecord{
		ID:            strings.TrimSpace(body.ID),
		Name:          name,
		Description:   strings.TrimSpace(body.Description),
		StorageMode:   body.StorageMode,
		UsagePolicy:   body.UsagePolicy,
		Scope:         body.Scope,
		WorkspacePath: body.WorkspacePath,
		TargetRunners: body.TargetRunners,
		Entries:       parsed,
		Enabled:       true,
	}

	if body.Merge {
		var prev *store.CredentialRecord
		if rec.ID != "" {
			prev, _ = s.store.GetCredential(r.Context(), rec.ID)
		} else {
			prev, _ = s.store.GetCredentialByName(r.Context(), rec.Name)
		}
		if prev != nil {
			rec.ID = prev.ID
			byKey := make(map[string]store.CredentialEntry, len(prev.Entries)+len(parsed))
			order := make([]string, 0, len(prev.Entries)+len(parsed))
			for _, e := range prev.Entries {
				byKey[e.Key] = e
				order = append(order, e.Key)
			}
			for _, e := range parsed {
				if _, exists := byKey[e.Key]; !exists {
					order = append(order, e.Key)
				}
				byKey[e.Key] = e
			}
			merged := make([]store.CredentialEntry, 0, len(order))
			for _, k := range order {
				merged = append(merged, byKey[k])
			}
			rec.Entries = merged
		}
	}

	saved, err := s.store.UpsertCredential(r.Context(), rec)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.BroadcastConfigSync(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"credential":     saved.Sanitized(),
		"imported_count": len(parsed),
	})
}

func (s *Server) handleCredentialSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/credentials/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	id := parts[0]

	if len(parts) == 1 {
		if r.Method == http.MethodDelete {
			if err := s.store.DeleteCredential(r.Context(), id); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			s.BroadcastConfigSync(r.Context())
			writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	action := parts[1]
	switch action {
	case "local-values":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			RunnerID string            `json:"runner_id"`
			Values   map[string]string `json:"values"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		runnerID := strings.TrimSpace(body.RunnerID)
		if runnerID == "" {
			writeErr(w, http.StatusBadRequest, errors.New("runner_id is required"))
			return
		}
		env, err := protocol.NewEnvelope(protocol.TypeCredentialLocalSet, protocol.CredentialLocalSetPayload{
			CredentialID: id,
			Values:       body.Values,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		resp, err := s.callRunnerRPC(r.Context(), runnerID, env, 10*time.Second)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		var res protocol.CredentialLocalSetResult
		_ = json.Unmarshal(resp.Payload, &res)
		writeJSON(w, http.StatusOK, res)

	case "probe":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			RunnerID      string `json:"runner_id"`
			WorkspacePath string `json:"workspace_path,omitempty"`
			Command       string `json:"command,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		runnerID := strings.TrimSpace(body.RunnerID)
		if runnerID == "" {
			writeErr(w, http.StatusBadRequest, errors.New("runner_id is required"))
			return
		}
		cmd := strings.TrimSpace(body.Command)
		if cmd == "" {
			if rec, err := s.store.GetCredential(r.Context(), id); err == nil && rec != nil {
				cmd = rec.ProbeCommand
			}
		}
		if cmd == "" {
			writeErr(w, http.StatusBadRequest, errors.New("probe command is not configured for this credential group"))
			return
		}
		env, err := protocol.NewEnvelope(protocol.TypeCredentialProbe, protocol.CredentialProbePayload{
			CredentialID:  id,
			WorkspacePath: body.WorkspacePath,
			Command:       cmd,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		resp, err := s.callRunnerRPC(r.Context(), runnerID, env, 15*time.Second)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		var res protocol.CredentialProbeResult
		_ = json.Unmarshal(resp.Payload, &res)
		writeJSON(w, http.StatusOK, res)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
