package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const maxAudioUploadBytes = 25 << 20 // 25 MB (OpenAI Whisper limit)

// handleAudioTranscribe proxies multipart audio uploads to the configured
// OpenAI-compatible /audio/transcriptions endpoint without exposing API keys
// to the browser or persisting audio to disk.
func (s *Server) handleAudioTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	prefs, err := s.store.GetUserPreferences(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("load preferences: %w", err))
		return
	}

	baseURL := strings.TrimRight(strings.TrimSpace(prefs.STTBaseURL), "/")
	apiKey := strings.TrimSpace(prefs.STTAPIKey)
	model := strings.TrimSpace(prefs.STTModel)
	if model == "" {
		model = "gpt-4o-mini-transcribe"
	}

	if baseURL == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("speech-to-text API base URL is not configured"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAudioUploadBytes+1024*1024)
	if err := r.ParseMultipartForm(maxAudioUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("parse audio upload: %w", err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("missing audio file field: %w", err))
		return
	}
	defer file.Close()

	filename := "audio.webm"
	if header != nil && strings.TrimSpace(header.Filename) != "" {
		filename = filepath.Base(header.Filename)
	}

	language := strings.TrimSpace(r.FormValue("language"))
	if language == "" {
		language = strings.TrimSpace(prefs.STTLanguage)
	}
	prompt := strings.TrimSpace(r.FormValue("prompt"))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := io.Copy(fw, file); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := mw.WriteField("model", model); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if language != "" && language != "auto" {
		if err := mw.WriteField("language", language); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if prompt != "" {
		if err := mw.WriteField("prompt", prompt); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := mw.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	endpoint := baseURL + "/audio/transcriptions"
	if strings.HasSuffix(baseURL, "/audio/transcriptions") {
		endpoint = baseURL
	}

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, &body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid STT endpoint: %w", err))
		return
	}
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	if apiKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(upReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("transcription request failed: %w", err))
		return
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("read transcription response: %w", err))
		return
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBytes))
		if len(msg) > 400 {
			msg = msg[:400]
		}
		writeErr(w, http.StatusBadGateway, fmt.Errorf("STT provider error (%d): %s", resp.StatusCode, msg))
		return
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		// Fallback for plain text response formats
		parsed.Text = strings.TrimSpace(string(respBytes))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"text":  strings.TrimSpace(parsed.Text),
		"model": model,
	})
}
