package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ClassifierQuestionType identifies the typed judgment requested from a classifier model.
type ClassifierQuestionType string

const (
	QuestionTypeChoice ClassifierQuestionType = "choice"
	QuestionTypeBool   ClassifierQuestionType = "bool"
	QuestionTypeScore  ClassifierQuestionType = "score"
)

// ClassifierQuestion defines one typed question in a ClassifyRequest.
type ClassifierQuestion struct {
	Type         ClassifierQuestionType `json:"type"`
	Instructions string                 `json:"instructions,omitempty"`
	Choices      []string               `json:"choices,omitempty"`  // for "choice"
	Criteria     map[string]string      `json:"criteria,omitempty"` // for "bool" ("true"/"false") or "choice"
	Min          *float64               `json:"min,omitempty"`      // for "score"
	Max          *float64               `json:"max,omitempty"`      // for "score"
}

// ClassifierAnswer is the structured answer returned by a classifier model.
type ClassifierAnswer struct {
	Type          ClassifierQuestionType `json:"type"`
	Choice        string                 `json:"choice,omitempty"`
	Bool          *bool                  `json:"bool,omitempty"`
	Score         *float64               `json:"score,omitempty"`
	Probability   float64                `json:"probability,omitempty"`
	Probabilities map[string]float64     `json:"probabilities,omitempty"`
}

// ClassifyRequest is the provider-neutral input to a ClassifierProvider.
type ClassifyRequest struct {
	Model     string                        `json:"model"`
	State     any                           `json:"state"`
	Images    []ContentBlock                `json:"images,omitempty"`
	Questions map[string]ClassifierQuestion `json:"questions"`
}

// ClassifyResponse is the unified result of a ClassifierProvider.Classify call.
type ClassifyResponse struct {
	Provider     string                      `json:"provider,omitempty"`
	Model        string                      `json:"model"`
	Answers      map[string]ClassifierAnswer `json:"answers"`
	Usage        Usage                       `json:"usage"`
	StopReason   string                      `json:"stop_reason"` // "stop" | "error" | "aborted"
	ErrorMessage string                      `json:"error_message,omitempty"`
}

// ClassifierProvider executes structured classification/decision requests (e.g., TypeSafe Jev / System One).
type ClassifierProvider interface {
	Classify(ctx context.Context, req *ClassifyRequest) (*ClassifyResponse, error)
}

// ClassifierImageMode controls how images are encoded on the wire for System One services.
type ClassifierImageMode string

const (
	ClassifierImageAssets        ClassifierImageMode = "assets"           // TypeSafe / Workers AI Jev
	ClassifierImageStateImageURL ClassifierImageMode = "state-image-urls" // OpenRouter
)

// SystemOneConfig configures a TypeSafe System One (/v1/systemone) classifier provider.
type SystemOneConfig struct {
	ProviderID string              // e.g. "typesafe", "openrouter", "llama-cpp"
	BaseURL    string              // e.g. "https://api.typesafe.ai/v1", "https://openrouter.ai/api/v1", "http://127.0.0.1:9931/v1"
	APIKey     string
	ImageMode  ClassifierImageMode // defaults to ClassifierImageAssets
	Headers    map[string]string
	HTTPClient *http.Client
}

// SystemOneClassifierProvider implements ClassifierProvider for TypeSafe System One endpoints.
type SystemOneClassifierProvider struct {
	cfg SystemOneConfig
}

// NewSystemOneClassifierProvider constructs a System One classifier provider.
func NewSystemOneClassifierProvider(cfg SystemOneConfig) *SystemOneClassifierProvider {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://api.typesafe.ai/v1"
	}
	if cfg.ProviderID == "" {
		cfg.ProviderID = "typesafe"
	}
	if cfg.ImageMode == "" {
		if strings.Contains(strings.ToLower(cfg.BaseURL), "openrouter.ai") {
			cfg.ImageMode = ClassifierImageStateImageURL
		} else {
			cfg.ImageMode = ClassifierImageAssets
		}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &SystemOneClassifierProvider{cfg: cfg}
}

func (p *SystemOneClassifierProvider) Classify(ctx context.Context, req *ClassifyRequest) (*ClassifyResponse, error) {
	if req == nil {
		return nil, errors.New("classify request is nil")
	}
	if len(req.Questions) == 0 {
		return &ClassifyResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: "classify request requires at least one question",
		}, nil
	}

	wireQuestions := make(map[string]any, len(req.Questions))
	for name, q := range req.Questions {
		switch q.Type {
		case QuestionTypeBool:
			// TypeSafe System One wire format uses "noul" for boolean questions.
			wq := map[string]any{"type": "noul"}
			if q.Instructions != "" {
				wq["instructions"] = q.Instructions
			}
			if len(q.Criteria) > 0 {
				wq["criteria"] = q.Criteria
			}
			wireQuestions[name] = wq
		case QuestionTypeChoice:
			wq := map[string]any{"type": "choice"}
			if q.Instructions != "" {
				wq["instructions"] = q.Instructions
			}
			if len(q.Choices) > 0 {
				wq["choices"] = q.Choices
			}
			if len(q.Criteria) > 0 {
				wq["criteria"] = q.Criteria
			}
			wireQuestions[name] = wq
		case QuestionTypeScore:
			wq := map[string]any{"type": "score"}
			if q.Instructions != "" {
				wq["instructions"] = q.Instructions
			}
			if q.Min != nil {
				wq["min"] = *q.Min
			}
			if q.Max != nil {
				wq["max"] = *q.Max
			}
			wireQuestions[name] = wq
		default:
			return &ClassifyResponse{
				Provider:     p.cfg.ProviderID,
				Model:        req.Model,
				StopReason:   "error",
				ErrorMessage: fmt.Sprintf("unsupported question type %q for %q", q.Type, name),
			}, nil
		}
	}

	statePayload := req.State
	var assets map[string]any
	hasImages := false
	if len(req.Images) > 0 {
		switch p.cfg.ImageMode {
		case ClassifierImageStateImageURL:
			parts := make([]any, 0, len(req.Images)+1)
			for _, img := range req.Images {
				u := img.ImageURL
				if u == "" && img.DataB64 != "" {
					m := img.MimeType
					if m == "" {
						m = "image/png"
					}
					u = "data:" + m + ";base64," + img.DataB64
				}
				if u != "" {
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
					hasImages = true
				}
			}
			parts = append(parts, req.State)
			statePayload = parts
		default: // ClassifierImageAssets
			assets = make(map[string]any, len(req.Images))
			refs := make([]any, 0, len(req.Images)+1)
			for i, img := range req.Images {
				if img.DataB64 == "" {
					continue
				}
				key := fmt.Sprintf("<image-%d>", i+1)
				format := extFromMime(img.MimeType)
				if format == "jpg" {
					format = "jpeg"
				}
				assets[key] = map[string]any{
					"type":        "image",
					"format":      format,
					"data_base64": img.DataB64,
				}
				refs = append(refs, key)
				hasImages = true
			}
			if hasImages {
				refs = append(refs, req.State)
				statePayload = refs
			}
		}
	}

	body := map[string]any{
		"model":     req.Model,
		"state":     statePayload,
		"questions": wireQuestions,
	}
	if len(assets) > 0 {
		body["assets"] = assets
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	baseURL := normalizeV1BaseURL(p.cfg.BaseURL)
	endpoint := baseURL + "/systemone"
	if strings.HasSuffix(baseURL, "/systemone") {
		endpoint = baseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", DefaultUserAgent)
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	if hasImages && p.cfg.ImageMode == ClassifierImageAssets {
		httpReq.Header.Set("TypeSafe-Danger-Zone-Images", "true")
	}
	for k, v := range p.cfg.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return &ClassifyResponse{Provider: p.cfg.ProviderID, Model: req.Model, StopReason: "aborted", ErrorMessage: err.Error()}, nil
		}
		return &ClassifyResponse{Provider: p.cfg.ProviderID, Model: req.Model, StopReason: "error", ErrorMessage: err.Error()}, nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode >= 400 {
		return &ClassifyResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))),
		}, nil
	}

	var parsed struct {
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   struct {
			InputTokens  int     `json:"input_tokens"`
			PromptTokens int     `json:"prompt_tokens"`
			OutputTokens int     `json:"output_tokens"`
			Cost         float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return &ClassifyResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: fmt.Sprintf("decode systemone response: %v", err),
		}, nil
	}

	inTok := parsed.Usage.InputTokens
	if inTok == 0 {
		inTok = parsed.Usage.PromptTokens
	}
	usage := Usage{
		InputTokens:  inTok,
		OutputTokens: parsed.Usage.OutputTokens,
		CostUSD:      parsed.Usage.Cost,
	}

	answers := make(map[string]ClassifierAnswer, len(req.Questions))
	for name, q := range req.Questions {
		rawAns, ok := parsed.Answers[name]
		if !ok {
			return &ClassifyResponse{
				Provider:     p.cfg.ProviderID,
				Model:        req.Model,
				Usage:        usage,
				StopReason:   "error",
				ErrorMessage: fmt.Sprintf("missing answer for question %q", name),
			}, nil
		}
		ans, parseErr := parseSystemOneAnswer(q.Type, rawAns)
		if parseErr != nil {
			return &ClassifyResponse{
				Provider:     p.cfg.ProviderID,
				Model:        req.Model,
				Usage:        usage,
				StopReason:   "error",
				ErrorMessage: fmt.Sprintf("invalid answer for %q: %v", name, parseErr),
			}, nil
		}
		answers[name] = ans
	}

	return &ClassifyResponse{
		Provider:   p.cfg.ProviderID,
		Model:      req.Model,
		Answers:    answers,
		Usage:      usage,
		StopReason: "stop",
	}, nil
}

func parseSystemOneAnswer(qType ClassifierQuestionType, raw json.RawMessage) (ClassifierAnswer, error) {
	var obj struct {
		Type          string             `json:"type"`
		Choice        string             `json:"choice"`
		Value         any                `json:"value"`
		Bool          *bool              `json:"bool"`
		Score         *float64           `json:"score"`
		Probability   float64            `json:"probability"`
		Probabilities map[string]float64 `json:"probabilities"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ClassifierAnswer{}, err
	}

	switch qType {
	case QuestionTypeBool:
		var b bool
		prob := obj.Probability
		if obj.Bool != nil {
			b = *obj.Bool
		} else if vb, ok := obj.Value.(bool); ok {
			b = vb
		} else {
			// TypeSafe noul returns probability of true (0..1)
			b = prob >= 0.5
		}
		return ClassifierAnswer{
			Type:          QuestionTypeBool,
			Bool:          &b,
			Probability:   prob,
			Probabilities: obj.Probabilities,
		}, nil
	case QuestionTypeChoice:
		choice := obj.Choice
		if choice == "" {
			if vs, ok := obj.Value.(string); ok {
				choice = vs
			}
		}
		if choice == "" && len(obj.Probabilities) > 0 {
			bestP := -1.0
			for k, v := range obj.Probabilities {
				if v > bestP {
					bestP = v
					choice = k
				}
			}
			if obj.Probability == 0 && bestP >= 0 {
				obj.Probability = bestP
			}
		}
		return ClassifierAnswer{
			Type:          QuestionTypeChoice,
			Choice:        choice,
			Probability:   obj.Probability,
			Probabilities: obj.Probabilities,
		}, nil
	case QuestionTypeScore:
		var sc float64
		if obj.Score != nil {
			sc = *obj.Score
		} else if vf, ok := obj.Value.(float64); ok {
			sc = vf
		}
		return ClassifierAnswer{
			Type:          QuestionTypeScore,
			Score:         &sc,
			Probability:   obj.Probability,
			Probabilities: obj.Probabilities,
		}, nil
	default:
		return ClassifierAnswer{}, fmt.Errorf("unknown question type %q", qType)
	}
}
