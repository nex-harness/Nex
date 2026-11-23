package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// ImageRequest describes a one-shot image generation or image editing request.
type ImageRequest struct {
	Model       string         `json:"model"`
	Input       []ContentBlock `json:"input"` // BlockText and optional BlockImage references
	Size        string         `json:"size,omitempty"`
	AspectRatio string         `json:"aspect_ratio,omitempty"`
	Quality     string         `json:"quality,omitempty"`
	Count       int            `json:"count,omitempty"`
}

// ImageResponse is the unified result of an ImageProvider.GenerateImages call.
type ImageResponse struct {
	Provider     string         `json:"provider,omitempty"`
	Model        string         `json:"model"`
	Output       []ContentBlock `json:"output"`
	Usage        Usage          `json:"usage"`
	StopReason   string         `json:"stop_reason"` // "stop" | "error" | "aborted"
	ErrorMessage string         `json:"error_message,omitempty"`
}

// ImageProvider executes one-shot image generation/editing requests.
type ImageProvider interface {
	GenerateImages(ctx context.Context, req *ImageRequest) (*ImageResponse, error)
}

// ImageProviderConfig configures an OpenAI or OpenRouter image provider.
type ImageProviderConfig struct {
	ProviderID string // e.g. "openai", "openrouter"
	BaseURL    string
	APIKey     string
	Headers    map[string]string
	HTTPClient *http.Client
}

// OpenAIImagesProvider calls OpenAI-compatible /images/generations and /images/edits endpoints.
type OpenAIImagesProvider struct {
	cfg ImageProviderConfig
}

// NewOpenAIImagesProvider constructs an OpenAI Images API provider.
func NewOpenAIImagesProvider(cfg ImageProviderConfig) *OpenAIImagesProvider {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.ProviderID == "" {
		cfg.ProviderID = "openai"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 180 * time.Second}
	}
	return &OpenAIImagesProvider{cfg: cfg}
}

func (p *OpenAIImagesProvider) GenerateImages(ctx context.Context, req *ImageRequest) (*ImageResponse, error) {
	if req == nil {
		return nil, errors.New("image request is nil")
	}
	prompt, refImages := splitImageInput(req.Input)
	if strings.TrimSpace(prompt) == "" {
		return &ImageResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: "image prompt is required",
		}, nil
	}

	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	var httpReq *http.Request
	var err error

	if len(refImages) == 0 {
		body := map[string]any{
			"model":  req.Model,
			"prompt": prompt,
		}
		if req.Size != "" {
			body["size"] = req.Size
		}
		if req.Quality != "" {
			body["quality"] = req.Quality
		}
		if req.Count > 0 {
			body["n"] = req.Count
		}
		raw, _ := json.Marshal(body)
		httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/images/generations", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
	} else {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("model", req.Model)
		_ = mw.WriteField("prompt", prompt)
		if req.Size != "" {
			_ = mw.WriteField("size", req.Size)
		}
		if req.Quality != "" {
			_ = mw.WriteField("quality", req.Quality)
		}
		if req.Count > 0 {
			_ = mw.WriteField("n", fmt.Sprintf("%d", req.Count))
		}
		for i, img := range refImages {
			rawImg, decErr := base64.StdEncoding.DecodeString(img.DataB64)
			if decErr != nil {
				return &ImageResponse{
					Provider:     p.cfg.ProviderID,
					Model:        req.Model,
					StopReason:   "error",
					ErrorMessage: fmt.Sprintf("invalid reference image %d base64: %v", i, decErr),
				}, nil
			}
			ext := extFromMime(img.MimeType)
			fieldName := "image"
			if len(refImages) > 1 {
				fieldName = "image[]"
			}
			part, partErr := mw.CreateFormFile(fieldName, fmt.Sprintf("ref-%d.%s", i+1, ext))
			if partErr != nil {
				return nil, partErr
			}
			_, _ = part.Write(rawImg)
		}
		_ = mw.Close()
		httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/images/edits", &buf)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	}

	applyImageHeaders(httpReq, p.cfg)
	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return &ImageResponse{Provider: p.cfg.ProviderID, Model: req.Model, StopReason: "aborted", ErrorMessage: err.Error()}, nil
		}
		return &ImageResponse{Provider: p.cfg.ProviderID, Model: req.Model, StopReason: "error", ErrorMessage: err.Error()}, nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if resp.StatusCode >= 400 {
		return &ImageResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))),
		}, nil
	}

	var parsed struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
		OutputFormat string `json:"output_format"`
		Usage        struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return &ImageResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: fmt.Sprintf("decode image response: %v", err),
		}, nil
	}

	mime := mimeFromFormat(parsed.OutputFormat)
	var out []ContentBlock
	for _, item := range parsed.Data {
		if item.B64JSON != "" {
			out = append(out, ImageBase64Block(mime, item.B64JSON))
		} else if item.URL != "" {
			out = append(out, ContentBlock{Type: BlockImage, ImageURL: item.URL, MimeType: mime})
		}
		if item.RevisedPrompt != "" {
			out = append(out, TextBlock(item.RevisedPrompt))
		}
	}
	if len(out) == 0 {
		return &ImageResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: "provider returned no images",
		}, nil
	}

	return &ImageResponse{
		Provider: p.cfg.ProviderID,
		Model:    req.Model,
		Output:   out,
		Usage: Usage{
			InputTokens:  parsed.Usage.InputTokens,
			OutputTokens: parsed.Usage.OutputTokens,
		},
		StopReason: "stop",
	}, nil
}

// OpenRouterImagesProvider calls OpenRouter's /chat/completions endpoint with modalities: ["image", "text"].
type OpenRouterImagesProvider struct {
	cfg ImageProviderConfig
}

// NewOpenRouterImagesProvider constructs an OpenRouter image generation provider.
func NewOpenRouterImagesProvider(cfg ImageProviderConfig) *OpenRouterImagesProvider {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://openrouter.ai/api/v1"
	}
	if cfg.ProviderID == "" {
		cfg.ProviderID = "openrouter"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 180 * time.Second}
	}
	return &OpenRouterImagesProvider{cfg: cfg}
}

func (p *OpenRouterImagesProvider) GenerateImages(ctx context.Context, req *ImageRequest) (*ImageResponse, error) {
	if req == nil {
		return nil, errors.New("image request is nil")
	}
	parts := make([]map[string]any, 0, len(req.Input))
	for _, blk := range req.Input {
		switch blk.Type {
		case BlockText:
			if strings.TrimSpace(blk.Text) != "" {
				parts = append(parts, map[string]any{"type": "text", "text": blk.Text})
			}
		case BlockImage:
			url := blk.ImageURL
			if url == "" && blk.DataB64 != "" {
				m := blk.MimeType
				if m == "" {
					m = "image/png"
				}
				url = "data:" + m + ";base64," + blk.DataB64
			}
			if url != "" {
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": url},
				})
			}
		}
	}
	if len(parts) == 0 {
		return &ImageResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: "image input is empty",
		}, nil
	}

	payload := map[string]any{
		"model":      req.Model,
		"modalities": []string{"image", "text"},
		"messages": []map[string]any{
			{"role": "user", "content": parts},
		},
	}
	if req.AspectRatio != "" {
		payload["image_config"] = map[string]any{"aspect_ratio": req.AspectRatio}
	}
	raw, _ := json.Marshal(payload)
	baseURL := strings.TrimRight(p.cfg.BaseURL, "/")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	applyImageHeaders(httpReq, p.cfg)

	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return &ImageResponse{Provider: p.cfg.ProviderID, Model: req.Model, StopReason: "aborted", ErrorMessage: err.Error()}, nil
		}
		return &ImageResponse{Provider: p.cfg.ProviderID, Model: req.Model, StopReason: "error", ErrorMessage: err.Error()}, nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if resp.StatusCode >= 400 {
		return &ImageResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))),
		}, nil
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
				Images  []struct {
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
					URL string `json:"url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int     `json:"prompt_tokens"`
			CompletionTokens int     `json:"completion_tokens"`
			Cost             float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return &ImageResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: fmt.Sprintf("decode openrouter image response: %v", err),
		}, nil
	}

	var out []ContentBlock
	if len(parsed.Choices) > 0 {
		msg := parsed.Choices[0].Message
		if s, ok := msg.Content.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, TextBlock(s))
		}
		for _, img := range msg.Images {
			u := img.ImageURL.URL
			if u == "" {
				u = img.URL
			}
			if blk, ok := parseDataURLToImageBlock(u); ok {
				out = append(out, blk)
			} else if u != "" {
				out = append(out, ContentBlock{Type: BlockImage, ImageURL: u, MimeType: "image/png"})
			}
		}
	}
	if len(out) == 0 {
		return &ImageResponse{
			Provider:     p.cfg.ProviderID,
			Model:        req.Model,
			StopReason:   "error",
			ErrorMessage: "openrouter returned no images",
		}, nil
	}

	return &ImageResponse{
		Provider: p.cfg.ProviderID,
		Model:    req.Model,
		Output:   out,
		Usage: Usage{
			InputTokens:  parsed.Usage.PromptTokens,
			OutputTokens: parsed.Usage.CompletionTokens,
			CostUSD:      parsed.Usage.Cost,
		},
		StopReason: "stop",
	}, nil
}

func splitImageInput(blocks []ContentBlock) (string, []ContentBlock) {
	var texts []string
	var imgs []ContentBlock
	for _, b := range blocks {
		switch b.Type {
		case BlockText:
			if strings.TrimSpace(b.Text) != "" {
				texts = append(texts, b.Text)
			}
		case BlockImage:
			if b.DataB64 != "" {
				imgs = append(imgs, b)
			}
		}
	}
	return strings.Join(texts, "\n"), imgs
}

func applyImageHeaders(req *http.Request, cfg ImageProviderConfig) {
	req.Header.Set("User-Agent", DefaultUserAgent)
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
}

func parseDataURLToImageBlock(dataURL string) (ContentBlock, bool) {
	if !strings.HasPrefix(dataURL, "data:") {
		return ContentBlock{}, false
	}
	rest := strings.TrimPrefix(dataURL, "data:")
	semi := strings.Index(rest, ";base64,")
	if semi <= 0 {
		return ContentBlock{}, false
	}
	mime := rest[:semi]
	b64 := rest[semi+len(";base64,"):]
	if b64 == "" {
		return ContentBlock{}, false
	}
	return ImageBase64Block(mime, b64), true
}

func mimeFromFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

func extFromMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return "png"
	}
}
