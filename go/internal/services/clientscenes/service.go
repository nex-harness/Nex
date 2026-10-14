// Package clientscenes ports packages/services/src/client-scenes/* (step
// 3.5): IClientScenesService.list — a GET on /api/v1/client/scenes through
// the shared ApiClient (readApiJson: non-2xx → ApiError with the response
// body's error/message/msg/detail field as the message).
package clientscenes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ChannelName is ServiceChannels.ClientScenes.
const ChannelName = "client-scenes"

// ScenesPath is the API prefix.
const ScenesPath = "/api/v1/client/scenes"

// Response is ClientScenesResponse: {code, msg, data: ClientSceneConfig[]}.
// The config/option/item shapes are open maps — the service passes them
// through; consumers validate their own fields.
type Response struct {
	Code int64           `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// HTTPClient is the ApiClient seam (same shape as clientconfig).
type HTTPClient interface {
	// GetJSON fetches; status is the HTTP status code and body the raw
	// response body (already read).
	GetJSON(ctx context.Context, target *url.URL) (status int, body []byte, err error)
}

// Options configures NewService.
type Options struct {
	Client HTTPClient
	// RuntimeAPIBaseURL resolves the runtime Nex API origin (the TS's
	// buildRuntimeNexApiUrl(process.env, path)); nil uses the default
	// https://zcode.z.ai.
	RuntimeAPIBaseURL func() string
}

// Service is IClientScenesService.
type Service struct {
	client  HTTPClient
	baseURL func() string
}

// NewService builds the service.
func NewService(opts Options) *Service {
	base := opts.RuntimeAPIBaseURL
	if base == nil {
		base = func() string { return "https://zcode.z.ai" }
	}
	return &Service{client: opts.Client, baseURL: base}
}

// List ports list(): GET /api/v1/client/scenes via readApiJson semantics.
func (s *Service) List(ctx context.Context) (*Response, error) {
	base, err := url.Parse(s.baseURL())
	if err != nil {
		return nil, err
	}
	target := base.JoinPath(ScenesPath)
	if !strings.HasPrefix(target.Path, "/") {
		target.Path = "/" + target.Path
	}
	status, body, err := s.client.GetJSON(ctx, target)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		// readApiJson: prefer the JSON body's error/message/msg/detail, else
		// the trimmed body, else HTTP <status>.
		return nil, &APIError{
			Message: readResponseMessage(status, body),
			URL:     target.String(),
			Method:  "GET",
			Status:  status,
		}
	}
	var response Response
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, &APIError{
			Message: "Invalid JSON response",
			URL:     target.String(),
			Method:  "GET",
			Status:  status,
		}
	}
	return &response, nil
}

// APIError ports ApiError (name + passthrough fields).
type APIError struct {
	Message string
	URL     string
	Method  string
	Status  int
}

func (e *APIError) Error() string { return e.Message }

// Name mirrors error.name for RPC serialization.
func (e *APIError) Name() string { return "ApiError" }

func readResponseMessage(status int, body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	var parsed struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		for _, candidate := range []string{parsed.Error, parsed.Message, parsed.Msg, parsed.Detail} {
			if strings.TrimSpace(candidate) != "" {
				return strings.TrimSpace(candidate)
			}
		}
	}
	return trimmed
}
