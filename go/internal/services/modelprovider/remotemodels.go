// remotemodels.go ports remoteModelList.ts: "从 /v1/models 获取" — request
// {baseUrl}/models with the provider's key/headers, parse the common
// response shapes (OpenAI/Anthropic {data:[{id}]}, Gemini {models:[{name}]},
// bare arrays), dedupe, strip Gemini's models/ prefix. One retry on
// transient network errors.
package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

const remoteModelListTimeout = 15 * time.Second
const anthropicAPIVersion = "2023-06-01"

// RemoteModelList is {ids}.
type RemoteModelList struct {
	IDs []string `json:"ids"`
}

// BuildRemoteModelListRequest ports buildRemoteModelListRequest. Headers are
// case-insensitive: user-provided Authorization / X-Api-Key are not
// duplicated by defaults.
func BuildRemoteModelListRequest(config *providerconfig.ProviderConfig) (string, http.Header, error) {
	if config == nil || config.API == nil || config.API.BaseURL == nil {
		return "", nil, errors.New("Provider base URL is not configured")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(*config.API.BaseURL), "/")
	if baseURL == "" {
		return "", nil, errors.New("Provider base URL is not configured")
	}
	headers := http.Header{}
	for key, value := range config.API.Headers {
		headers.Set(key, value)
	}
	if headers.Get("accept") == "" {
		headers.Set("accept", "application/json")
	}
	apiKey := ""
	if config.Access != nil && providerconfig.IsAPIKeyAccess(config.Access.AccessType()) &&
		config.Access.APIKey != nil && config.Access.APIKey.APIKey != nil {
		apiKey = strings.TrimSpace(*config.Access.APIKey.APIKey)
	}
	apiType := ""
	if config.API.Type != nil {
		apiType = *config.API.Type
	}
	if apiKey != "" {
		if apiType == providerconfig.APIAnthropicMessages {
			if headers.Get("x-api-key") == "" {
				headers.Set("x-api-key", apiKey)
			}
			if headers.Get("anthropic-version") == "" {
				headers.Set("anthropic-version", anthropicAPIVersion)
			}
		} else if headers.Get("authorization") == "" {
			headers.Set("authorization", "Bearer "+apiKey)
		}
	}
	return baseURL + "/models", headers, nil
}

// ParseRemoteModelIDs ports parseRemoteModelIds.
func ParseRemoteModelIDs(payload []byte) []string {
	var parsed any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil
	}
	entries := modelEntries(parsed)
	ids := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		id := modelEntryID(entry)
		if id == "" {
			continue
		}
		id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func modelEntries(payload any) []any {
	switch typed := payload.(type) {
	case []any:
		return typed
	case map[string]any:
		if data, ok := typed["data"].([]any); ok {
			return data
		}
		if models, ok := typed["models"].([]any); ok {
			return models
		}
	}
	return nil
}

func modelEntryID(entry any) string {
	switch typed := entry.(type) {
	case string:
		return typed
	case map[string]any:
		if id, ok := typed["id"].(string); ok {
			return id
		}
		if name, ok := typed["name"].(string); ok {
			return name
		}
	}
	return ""
}

// FetchRemoteModelList ports fetchRemoteModelList: 2 attempts, retry only on
// transient network errors (HTTP status errors carry no retry value).
func FetchRemoteModelList(ctx context.Context, client *http.Client, config *providerconfig.ProviderConfig) (*RemoteModelList, error) {
	if client == nil {
		client = &http.Client{Timeout: remoteModelListTimeout}
	}
	url, headers, err := BuildRemoteModelListRequest(config)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		for key, values := range headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt == 0 && isTransientNetworkError(err) {
				continue
			}
			return nil, fmt.Errorf("fetch failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			statusErr := fmt.Errorf("Model list request failed: HTTP %d", resp.StatusCode)
			resp.Body.Close()
			return nil, statusErr
		}
		var payload []byte
		payload, err = readAll(resp.Body, remoteModelListTimeout)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			if attempt == 0 && isTransientNetworkError(err) {
				continue
			}
			return nil, fmt.Errorf("fetch failed: %v", err)
		}
		return &RemoteModelList{IDs: ParseRemoteModelIDs(payload)}, nil
	}
	return nil, fmt.Errorf("fetch failed: %v", lastErr)
}

func isTransientNetworkError(err error) bool {
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var dnsErr *netDNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"connection refused", "connection reset", "no such host", "connection timed out", "network is unreachable"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
