package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/nex-harness/nex/pkg/store"
)

func TestHandleAudioTranscribe(t *testing.T) {
	// Mock OpenAI-compatible transcription server
	var gotAuth, gotModel, gotLang, gotFilename string
	var gotAudio []byte
	mockSTT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotModel = r.FormValue("model")
		gotLang = r.FormValue("language")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer f.Close()
		gotFilename = hdr.Filename
		gotAudio, _ = io.ReadAll(f)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello nex voice"}`))
	}))
	defer mockSTT.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "nex.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	prefs := store.DefaultUserPreferences()
	prefs.STTEnabled = true
	prefs.STTEngine = "api"
	prefs.STTBaseURL = mockSTT.URL + "/v1"
	prefs.STTAPIKey = "sk-test-stt"
	prefs.STTModel = "whisper-1"
	prefs.STTLanguage = "zh"
	if err := st.SaveUserPreferences(context.Background(), prefs); err != nil {
		t.Fatalf("save prefs: %v", err)
	}

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	var reqBuf bytes.Buffer
	mw := multipart.NewWriter(&reqBuf)
	fw, err := mw.CreateFormFile("file", "recording.webm")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = fw.Write([]byte("fake-opus-bytes"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audio/transcribe", &reqBuf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Text != "hello nex voice" {
		t.Fatalf("unexpected text: %q", resp.Text)
	}
	if gotAuth != "Bearer sk-test-stt" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
	if gotModel != "whisper-1" || gotLang != "zh" || gotFilename != "recording.webm" {
		t.Fatalf("unexpected forwarded fields: model=%q lang=%q file=%q", gotModel, gotLang, gotFilename)
	}
	if string(gotAudio) != "fake-opus-bytes" {
		t.Fatalf("unexpected audio payload: %q", string(gotAudio))
	}
}
