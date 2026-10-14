// Package codingplansubscription ports
// packages/services/src/coding-plan-subscription/* (step 3.5):
// ICodingPlanSubscriptionService.getDynamicWorkflowClientConfig — the
// dynamic-workflow rollout snapshot from /api/v1/client/configs with a 1h
// cache and fail-closed semantics.
//
// 三条边界（TS 注释原文语义）：
//  1. 本地覆盖（NEX_DYNAMIC_WORKFLOW_MODE）在任何网络动作之前裁决；
//  2. forceRefresh 清掉快照后重拉；
//  3. 请求失败 fail-closed：返回 default（disabled），不把异常抛给调用方。
package codingplansubscription

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ChannelName is ServiceChannels.CodingPlanSubscription.
const ChannelName = "coding-plan-subscription"

// DynamicWorkflowModeEnv is NEX_DYNAMIC_WORKFLOW_MODE (pre-existing TS var).
const DynamicWorkflowModeEnv = "NEX_DYNAMIC_WORKFLOW_MODE"

const (
	clientConfigsPath  = "/api/v1/client/configs"
	requestTimeoutMS   = 15_000
	snapshotCacheTTLMS = 60 * 60 * 1000
)

// Modes is DYNAMIC_WORKFLOW_MODES.
var Modes = []string{"disabled", "onDemand", "alwaysOn"}

// DefaultMode is DEFAULT_DYNAMIC_WORKFLOW_MODE (fail-closed).
const DefaultMode = "disabled"

// ClientConfig is DynamicWorkflowClientConfig.
type ClientConfig struct {
	Mode    string `json:"mode"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"` // "remote" | "override" | "default"
}

// HTTPClient is the ApiClient seam.
type HTTPClient interface {
	GetJSON(ctx context.Context, target *url.URL) (status int, body []byte, err error)
}

// Options configures NewService.
type Options struct {
	Client HTTPClient
	// Env supplies NEX_DYNAMIC_WORKFLOW_MODE and the endpoint overrides.
	Env func() map[string]string
	// AppVersion / Platform build the query string.
	AppVersion string
	Platform   string
	// RuntimeAPIBaseURL resolves the runtime Nex API origin; nil uses
	// NEX_BASE_URL/NEX_ENDPOINT_ORIGIN from Env or the default.
	RuntimeAPIBaseURL func(env map[string]string) string
	// Now is the cache clock (tests).
	Now func() time.Time
}

// Service is ICodingPlanSubscriptionService.
type Service struct {
	client     HTTPClient
	env        func() map[string]string
	appVersion string
	platform   string
	baseURL    func(env map[string]string) string
	now        func() time.Time

	mu             sync.Mutex
	snapshot       []byte // raw envelope body
	snapshotExpiry time.Time
	pending        *pendingRequest
}

type pendingRequest struct {
	done   chan struct{}
	body   []byte
	failed bool
}

// NewService builds the service.
func NewService(opts Options) *Service {
	env := opts.Env
	if env == nil {
		env = func() map[string]string {
			out := map[string]string{}
			for _, entry := range environ() {
				for i := 0; i < len(entry); i++ {
					if entry[i] == '=' {
						out[entry[:i]] = entry[i+1:]
						break
					}
				}
			}
			return out
		}
	}
	base := opts.RuntimeAPIBaseURL
	if base == nil {
		base = defaultRuntimeAPIBaseURL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		client:     opts.Client,
		env:        env,
		appVersion: opts.AppVersion,
		platform:   opts.Platform,
		baseURL:    base,
		now:        now,
	}
}

// DefaultRuntimeBaseURL mirrors DEFAULT_NEX_ENDPOINT_ORIGIN.
const defaultOrigin = "https://zcode.z.ai"

func defaultRuntimeAPIBaseURL(env map[string]string) string {
	for _, key := range []string{"NEX_BASE_URL", "NEX_ENDPOINT_ORIGIN"} {
		if v := strings.TrimSpace(env[key]); v != "" {
			return v
		}
	}
	return defaultOrigin
}

// NormalizeMode ports normalizeDynamicWorkflowMode.
func NormalizeMode(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	for _, mode := range Modes {
		if mode == trimmed {
			return trimmed, true
		}
	}
	return "", false
}

// CreateClientConfig ports createDynamicWorkflowClientConfig.
func CreateClientConfig(mode, source string) ClientConfig {
	return ClientConfig{Mode: mode, Enabled: mode != "disabled", Source: source}
}

// ResolveClientConfig ports resolveDynamicWorkflowClientConfig: override >
// remote legal value > default. 服务端撤掉 key 等于关闭.
func ResolveClientConfig(remote any, env map[string]string) ClientConfig {
	if override, ok := NormalizeMode(env[DynamicWorkflowModeEnv]); ok {
		return CreateClientConfig(override, "override")
	}
	if remoteObj, ok := remote.(map[string]any); ok {
		if remoteMode, ok := NormalizeMode(stringOrEmpty(remoteObj["mode"])); ok {
			return CreateClientConfig(remoteMode, "remote")
		}
	}
	return CreateClientConfig(DefaultMode, "default")
}

func stringOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}

// GetDynamicWorkflowClientConfig ports getDynamicWorkflowClientConfig.
func (s *Service) GetDynamicWorkflowClientConfig(ctx context.Context, forceRefresh bool) (ClientConfig, error) {
	env := s.env()
	// 1. 本地覆盖在任何网络动作之前裁决.
	if _, ok := NormalizeMode(env[DynamicWorkflowModeEnv]); ok {
		return ResolveClientConfig(nil, env), nil
	}
	if forceRefresh {
		s.mu.Lock()
		s.snapshot = nil
		s.snapshotExpiry = time.Time{}
		s.mu.Unlock()
	}
	body, err := s.getClientConfigs(ctx, env)
	if err != nil {
		// 3. fail-closed：请求失败按关闭处理，不抛给调用方.
		return CreateClientConfig(DefaultMode, "default"), nil
	}
	return ResolveClientConfig(parseDynamicWorkflow(body), env), nil
}

// getClientConfigs ports getClientConfigs: 1h snapshot cache + single-flight.
func (s *Service) getClientConfigs(ctx context.Context, env map[string]string) ([]byte, error) {
	s.mu.Lock()
	if s.snapshot != nil && s.snapshotExpiry.After(s.now()) {
		body := s.snapshot
		s.mu.Unlock()
		return body, nil
	}
	if s.pending != nil {
		pending := s.pending
		s.mu.Unlock()
		select {
		case <-pending.done:
			if pending.failed {
				return nil, errPendingFailed
			}
			return pending.body, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	pending := &pendingRequest{done: make(chan struct{})}
	s.pending = pending
	s.mu.Unlock()

	body, err := s.fetch(ctx, env)
	s.mu.Lock()
	if err == nil {
		s.snapshot = body
		s.snapshotExpiry = s.now().Add(time.Duration(snapshotCacheTTLMS) * time.Millisecond)
	}
	s.pending = nil
	s.mu.Unlock()
	pending.body = body
	pending.failed = err != nil
	close(pending.done)
	return body, err
}

var errPendingFailed = context.DeadlineExceeded

func (s *Service) fetch(ctx context.Context, env map[string]string) ([]byte, error) {
	base, err := url.Parse(s.baseURL(env))
	if err != nil {
		return nil, err
	}
	target := base.JoinPath(clientConfigsPath)
	if !strings.HasPrefix(target.Path, "/") {
		target.Path = "/" + target.Path
	}
	query := target.Query()
	query.Set("app_version", s.appVersion)
	query.Set("platform", s.platform)
	target.RawQuery = query.Encode()

	fetchCtx, cancel := context.WithTimeout(ctx, time.Duration(requestTimeoutMS)*time.Millisecond)
	defer cancel()
	status, body, err := s.client.GetJSON(fetchCtx, target)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{Message: apiErrorMessage(status, body), URL: target.String(), Method: "GET", Status: status}
	}
	return body, nil
}

// parseDynamicWorkflow extracts data.configs.dynamicWorkflow from the
// envelope (the TS types it loosely; a missing key means disabled).
func parseDynamicWorkflow(body []byte) any {
	var envelope struct {
		Data *struct {
			Configs *struct {
				DynamicWorkflow json.RawMessage `json:"dynamicWorkflow"`
			} `json:"configs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	if envelope.Data == nil || envelope.Data.Configs == nil {
		return nil
	}
	var parsed any
	if err := json.Unmarshal(envelope.Data.Configs.DynamicWorkflow, &parsed); err != nil {
		return nil
	}
	return parsed
}

// APIError ports ApiError.
type APIError struct {
	Message string
	URL     string
	Method  string
	Status  int
}

func (e *APIError) Error() string { return e.Message }
func (e *APIError) Name() string  { return "ApiError" }

func apiErrorMessage(status int, body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "HTTP " + itoa(status)
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// environ reads the process environment (kept in a tiny helper so the Env
// seam covers it in tests).
var environ = readEnviron

func readEnviron() []string { return os.Environ() }

// SetNowForTests swaps the cache clock (test seam).
func (s *Service) SetNowForTests(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}
