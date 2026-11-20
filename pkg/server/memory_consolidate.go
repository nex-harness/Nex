package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

type memoryConsolidateRequest struct {
	ProviderID string `json:"provider_id,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
}

type memoryConsolidateResponse struct {
	BeforeCount int                  `json:"before_count"`
	AfterCount  int                  `json:"after_count"`
	Summary     string               `json:"summary"`
	Memories    []store.MemoryRecord `json:"memories"`
}

type llmConsolidatePayload struct {
	Summary  string               `json:"summary"`
	Memories []store.MemoryRecord `json:"memories"`
}

func (s *Server) handleMemoryConsolidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req memoryConsolidateRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	existing, err := s.store.ListMemories(ctx, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(existing) < 2 {
		writeJSON(w, http.StatusOK, memoryConsolidateResponse{
			BeforeCount: len(existing),
			AfterCount:  len(existing),
			Summary:     "At least 2 memories are needed before consolidation.",
			Memories:    existing,
		})
		return
	}

	prov, modelID, err := s.resolveMemoryModel(ctx, req.ProviderID, req.ModelID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	res, err := s.runMemoryConsolidation(ctx, prov, modelID, existing)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) resolveMemoryModel(ctx context.Context, reqProvID, reqModelID string) (ai.Provider, string, error) {
	prefs, _ := s.store.GetUserPreferences(ctx)
	provID := strings.TrimSpace(reqProvID)
	modelID := strings.TrimSpace(reqModelID)
	if provID == "" {
		provID = strings.TrimSpace(prefs.MemoryProviderID)
	}
	if modelID == "" {
		modelID = strings.TrimSpace(prefs.MemoryModelID)
	}

	provs, err := s.store.ListProviders(ctx)
	if err != nil {
		return nil, "", err
	}
	var chosen *store.ProviderRecord
	for i := range provs {
		if provID != "" && provs[i].ID == provID {
			chosen = &provs[i]
			break
		}
		if provID == "" && provs[i].Enabled {
			if chosen == nil || (chosen.APIKey == "" && provs[i].APIKey != "") {
				chosen = &provs[i]
			}
		}
	}
	if chosen == nil {
		return nil, "", fmt.Errorf("no enabled LLM provider configured for memory consolidation")
	}

	apiKey := chosen.APIKey
	if apiKey == "" {
		if chosen.Driver == ai.DriverAnthropic {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		} else {
			apiKey = os.Getenv("OPENAI_API_KEY")
		}
	}

	if modelID == "" {
		mods, _ := s.store.ListModels(ctx, chosen.ID)
		if len(mods) > 0 {
			modelID = mods[0].ID
		} else if chosen.Driver == ai.DriverAnthropic {
			modelID = "claude-sonnet-4-5"
		} else {
			modelID = "gpt-5-codex"
		}
	}

	prov, err := ai.NewProvider(chosen.Driver, chosen.BaseURL, apiKey, s.client)
	if err != nil {
		return nil, "", err
	}
	return prov, modelID, nil
}

func (s *Server) runMemoryConsolidation(ctx context.Context, prov ai.Provider, modelID string, existing []store.MemoryRecord) (memoryConsolidateResponse, error) {
	inputJSON, _ := json.MarshalIndent(existing, "", "  ")
	sysPrompt := "You are the Memory Consolidation (Dream) engine for a coding agent. " +
		"Given the JSON array of saved long-term memories:\n" +
		"1. Merge duplicate or closely overlapping memories into a single crisp, self-contained entry.\n" +
		"2. Resolve contradictions by favoring the entry with the newer `updated_at` timestamp.\n" +
		"3. Remove trivial, obsolete, or secret/credential-containing entries.\n" +
		"4. Preserve accurate `scope` ('global' or 'project'), `workspace_path`, and `category` ('preference', 'architecture', 'convention', or 'fact'). Reuse the surviving `id` when possible.\n" +
		"5. Keep the language consistent with the original memories.\n" +
		"Return ONLY valid JSON matching: {\"summary\":\"one-sentence summary of changes\",\"memories\":[{\"id\":\"...\",\"scope\":\"global|project\",\"workspace_path\":\"...\",\"category\":\"...\",\"title\":\"...\",\"content\":\"...\"}]}"

	stream, err := prov.Stream(ctx, &ai.StreamRequest{
		Model:           modelID,
		SystemPrompt:    sysPrompt,
		Messages:        []ai.Message{ai.NewUserTextMessage("Consolidate these memories:\n" + string(inputJSON))},
		MaxOutputTokens: 2048,
		ThinkingEffort:  "",
	})
	if err != nil {
		return memoryConsolidateResponse{}, err
	}

	var raw strings.Builder
	for ev := range stream {
		if ev.Type == ai.StreamEventTextDelta {
			raw.WriteString(ev.TextDelta)
		} else if ev.Type == ai.StreamEventDone && ev.Message != nil && raw.Len() == 0 {
			raw.WriteString(ev.Message.PlainText())
		}
	}

	parsed, err := parseConsolidateJSON(raw.String())
	if err != nil {
		return memoryConsolidateResponse{}, fmt.Errorf("failed to parse consolidation output: %w", err)
	}

	// Sanitize candidates & preserve existing created_at timestamps when IDs match
	createdByID := make(map[string]int64, len(existing))
	for _, m := range existing {
		createdByID[m.ID] = m.CreatedAt
	}
	now := time.Now().UnixMilli()
	cleaned := make([]store.MemoryRecord, 0, len(parsed.Memories))
	for _, m := range parsed.Memories {
		m.Title = strings.TrimSpace(m.Title)
		m.Content = strings.TrimSpace(m.Content)
		if m.Title == "" || m.Content == "" {
			continue
		}
		if tools.ContainsSecretLikeMaterial(m.Title) || tools.ContainsSecretLikeMaterial(m.Content) {
			continue
		}
		if ts, ok := createdByID[m.ID]; ok && ts > 0 {
			m.CreatedAt = ts
		}
		m.UpdatedAt = now
		cleaned = append(cleaned, m)
	}
	// Safety guard: never wipe every non-secret memory silently if model returned empty array
	if len(cleaned) == 0 && len(existing) > 0 {
		return memoryConsolidateResponse{}, fmt.Errorf("consolidation produced 0 memories; aborted to protect existing records")
	}

	saved, err := s.store.ReplaceAllMemories(ctx, cleaned)
	if err != nil {
		return memoryConsolidateResponse{}, err
	}
	s.BroadcastConfigSync(ctx)
	notifyEnv, _ := protocol.NewEnvelope(protocol.TypeMemorySync, protocol.MemorySyncPayload{Action: "refresh"})
	s.broadcastToAllClients(ctx, notifyEnv)

	summary := strings.TrimSpace(parsed.Summary)
	if summary == "" {
		summary = fmt.Sprintf("Consolidated %d -> %d memories.", len(existing), len(saved))
	}
	return memoryConsolidateResponse{
		BeforeCount: len(existing),
		AfterCount:  len(saved),
		Summary:     summary,
		Memories:    saved,
	}, nil
}

func parseConsolidateJSON(raw string) (*llmConsolidatePayload, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found in response")
	}
	var out llmConsolidatePayload
	if err := json.Unmarshal([]byte(s[start:end+1]), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
