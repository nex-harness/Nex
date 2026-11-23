package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEffectiveModelType(t *testing.T) {
	if got := EffectiveModelType(ModelMeta{ID: "gpt-5"}); got != ModelTypeChat {
		t.Fatalf("expected chat default, got %q", got)
	}
	if got := EffectiveModelType(ModelMeta{ID: "flux", Type: ModelTypeImage}); got != ModelTypeImage {
		t.Fatalf("expected image, got %q", got)
	}
	if got := EffectiveModelType(ModelMeta{ID: "jev-latest", Type: ModelTypeClassifier}); got != ModelTypeClassifier {
		t.Fatalf("expected classifier, got %q", got)
	}
}

func TestOpenAIImagesProviderGenerateAndEdit(t *testing.T) {
	var seenPath string
	var seenContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output_format": "png",
			"data": [{"b64_json": "iVBORw0KGgo=", "revised_prompt": "A red fox"}],
			"usage": {"input_tokens": 12, "output_tokens": 100}
		}`))
	}))
	defer srv.Close()

	p := NewOpenAIImagesProvider(ImageProviderConfig{
		BaseURL: srv.URL,
		APIKey:  "test-key",
	})

	// 1. Text-to-image -> /images/generations
	resp, err := p.GenerateImages(context.Background(), &ImageRequest{
		Model: "gpt-image-1",
		Input: []ContentBlock{TextBlock("A red fox")},
		Size:  "1024x1024",
	})
	if err != nil {
		t.Fatalf("GenerateImages err: %v", err)
	}
	if seenPath != "/images/generations" {
		t.Fatalf("expected /images/generations, got %q", seenPath)
	}
	if resp.StopReason != "stop" || len(resp.Output) != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Output[0].Type != BlockImage || resp.Output[0].MimeType != "image/png" || resp.Output[0].DataB64 != "iVBORw0KGgo=" {
		t.Fatalf("unexpected image block: %+v", resp.Output[0])
	}

	// 2. Image edit with reference -> /images/edits (multipart)
	refB64 := base64.StdEncoding.EncodeToString([]byte("fake-png"))
	resp2, err := p.GenerateImages(context.Background(), &ImageRequest{
		Model: "gpt-image-1",
		Input: []ContentBlock{
			TextBlock("Make it snowy"),
			ImageBase64Block("image/png", refB64),
		},
	})
	if err != nil {
		t.Fatalf("GenerateImages edit err: %v", err)
	}
	if seenPath != "/images/edits" || !strings.HasPrefix(seenContentType, "multipart/form-data") {
		t.Fatalf("expected multipart /images/edits, got path=%q ct=%q", seenPath, seenContentType)
	}
	if resp2.StopReason != "stop" {
		t.Fatalf("expected stop, got %+v", resp2)
	}
}

func TestOpenRouterImagesProvider(t *testing.T) {
	var seenModalities []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Modalities []string `json:"modalities"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenModalities = body.Modalities
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "Here is your fox.",
					"images": [{"image_url": {"url": "data:image/webp;base64,UklGRg=="}}]
				}
			}],
			"usage": {"prompt_tokens": 20, "completion_tokens": 50, "cost": 0.03}
		}`))
	}))
	defer srv.Close()

	p := NewOpenRouterImagesProvider(ImageProviderConfig{
		BaseURL: srv.URL,
		APIKey:  "or-key",
	})
	resp, err := p.GenerateImages(context.Background(), &ImageRequest{
		Model:       "google/gemini-2.5-flash-image",
		Input:       []ContentBlock{TextBlock("A red fox")},
		AspectRatio: "16:9",
	})
	if err != nil {
		t.Fatalf("GenerateImages err: %v", err)
	}
	if len(seenModalities) != 2 || seenModalities[0] != "image" {
		t.Fatalf("expected modalities [image,text], got %v", seenModalities)
	}
	if resp.StopReason != "stop" || len(resp.Output) != 2 {
		t.Fatalf("unexpected resp: %+v", resp)
	}
	if resp.Output[1].Type != BlockImage || resp.Output[1].MimeType != "image/webp" || resp.Output[1].DataB64 != "UklGRg==" {
		t.Fatalf("unexpected image block: %+v", resp.Output[1])
	}
	if resp.Usage.CostUSD != 0.03 {
		t.Fatalf("expected cost 0.03, got %v", resp.Usage.CostUSD)
	}
}

func TestSystemOneClassifierProvider(t *testing.T) {
	var seenWireType string
	var seenDangerHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/systemone" {
			t.Errorf("expected /systemone, got %q", r.URL.Path)
		}
		seenDangerHeader = r.Header.Get("TypeSafe-Danger-Zone-Images")
		var body struct {
			Questions map[string]struct {
				Type string `json:"type"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenWireType = body.Questions["safe"].Type

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"answers": {
				"safe": {"type": "noul", "probability": 0.92},
				"tier": {"type": "choice", "choice": "fast", "probability": 0.88, "probabilities": {"fast": 0.88, "deep": 0.12}},
				"urgency": {"type": "score", "score": 3.5, "probability": 0.75}
			},
			"usage": {"input_tokens": 42, "output_tokens": 3, "cost": 0.0001}
		}`))
	}))
	defer srv.Close()

	p := NewSystemOneClassifierProvider(SystemOneConfig{
		ProviderID: "typesafe",
		BaseURL:    srv.URL,
		APIKey:     "ts-key",
	})
	minS, maxS := 0.0, 5.0
	resp, err := p.Classify(context.Background(), &ClassifyRequest{
		Model:  "jev-latest",
		State:  map[string]any{"command": "git status"},
		Images: []ContentBlock{ImageBase64Block("image/png", "iVBORw0KGgo=")},
		Questions: map[string]ClassifierQuestion{
			"safe": {
				Type:         QuestionTypeBool,
				Instructions: "Is this command safe?",
				Criteria:     map[string]string{"true": "Safe", "false": "Unsafe"},
			},
			"tier": {
				Type:    QuestionTypeChoice,
				Choices: []string{"fast", "deep"},
			},
			"urgency": {
				Type: QuestionTypeScore,
				Min:  &minS,
				Max:  &maxS,
			},
		},
	})
	if err != nil {
		t.Fatalf("Classify err: %v", err)
	}
	if seenWireType != "noul" {
		t.Fatalf("expected bool translated to noul, got %q", seenWireType)
	}
	if seenDangerHeader != "true" {
		t.Fatalf("expected TypeSafe-Danger-Zone-Images header, got %q", seenDangerHeader)
	}
	if resp.StopReason != "stop" {
		t.Fatalf("expected stop, got %+v", resp)
	}
	if resp.Answers["safe"].Bool == nil || !*resp.Answers["safe"].Bool || resp.Answers["safe"].Probability != 0.92 {
		t.Fatalf("unexpected bool answer: %+v", resp.Answers["safe"])
	}
	if resp.Answers["tier"].Choice != "fast" {
		t.Fatalf("unexpected choice answer: %+v", resp.Answers["tier"])
	}
	if resp.Answers["urgency"].Score == nil || *resp.Answers["urgency"].Score != 3.5 {
		t.Fatalf("unexpected score answer: %+v", resp.Answers["urgency"])
	}
	if resp.Usage.InputTokens != 42 {
		t.Fatalf("expected 42 input tokens, got %d", resp.Usage.InputTokens)
	}
}
