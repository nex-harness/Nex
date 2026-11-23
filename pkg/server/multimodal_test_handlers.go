package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

func (s *Server) handleImageTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Driver  string `json:"driver"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		ModelID string `json:"model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "model_id is required"})
		return
	}

	driver := ai.ImageDriver(strings.TrimSpace(req.Driver))
	if driver == "" {
		driver = ai.ImageDriverOpenAI
	}
	provider := ai.NewImageProvider(driver, ai.ImageProviderConfig{
		ProviderID: string(driver),
		BaseURL:    strings.TrimSpace(req.BaseURL),
		APIKey:     strings.TrimSpace(req.APIKey),
	})

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := provider.GenerateImages(ctx, &ai.ImageRequest{
		Model: modelID,
		Input: []ai.ContentBlock{ai.TextBlock("A minimal 1-color blue square icon")},
		Count: 1,
	})
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"latency_ms": elapsed,
			"error":      err.Error(),
		})
		return
	}
	if resp == nil || resp.StopReason != "stop" {
		msg := "unknown error"
		if resp != nil && resp.ErrorMessage != "" {
			msg = resp.ErrorMessage
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"latency_ms": elapsed,
			"error":      msg,
		})
		return
	}
	imgCount := 0
	for _, blk := range resp.Output {
		if blk.Type == ai.BlockImage {
			imgCount++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"latency_ms": elapsed,
		"images":     imgCount,
		"model":      modelID,
	})
}

func (s *Server) handleClassifierTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Driver  string `json:"driver"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		ModelID string `json:"model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "model_id is required"})
		return
	}

	baseURL := strings.TrimSpace(req.BaseURL)
	imgMode := ai.ClassifierImageAssets
	if strings.TrimSpace(req.Driver) == "openrouter_system_one" || strings.Contains(strings.ToLower(baseURL), "openrouter.ai") {
		imgMode = ai.ClassifierImageStateImageURL
		if baseURL == "" {
			baseURL = "https://openrouter.ai/api/v1"
		}
	} else if baseURL == "" {
		baseURL = "https://api.typesafe.ai/v1"
	}

	provider := ai.NewSystemOneClassifierProvider(ai.SystemOneConfig{
		ProviderID: "test-classifier",
		BaseURL:    baseURL,
		APIKey:     strings.TrimSpace(req.APIKey),
		ImageMode:  imgMode,
	})

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := provider.Classify(ctx, &ai.ClassifyRequest{
		Model: modelID,
		State: map[string]any{"message": "ping ok"},
		Questions: map[string]ai.ClassifierQuestion{
			"ok": {
				Type:         ai.QuestionTypeBool,
				Instructions: "Does the message say ok?",
				Criteria:     map[string]string{"true": "Says ok", "false": "Does not say ok"},
			},
		},
	})
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"latency_ms": elapsed,
			"error":      err.Error(),
		})
		return
	}
	if resp == nil || resp.StopReason != "stop" {
		msg := "unknown error"
		if resp != nil && resp.ErrorMessage != "" {
			msg = resp.ErrorMessage
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"latency_ms": elapsed,
			"error":      msg,
		})
		return
	}
	ans := resp.Answers["ok"]
	summary := fmt.Sprintf("ok=%v (p=%.2f)", ans.Bool != nil && *ans.Bool, ans.Probability)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"latency_ms": elapsed,
		"summary":    summary,
		"model":      modelID,
	})
}
