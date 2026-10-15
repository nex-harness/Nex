// modelsdev.go ports modelsDevCatalog.ts: the models.dev public catalog
// client — fetch with timeout, memory + disk cache with a 24h refresh
// window, namespace-aware lookup and cross-provider majority aggregation
// into a sparse ModelConfig.
package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ModelsDevAPIURL is MODELS_DEV_API_URL.
const ModelsDevAPIURL = "https://models.dev/api.json"

const modelsDevRefreshInterval = 24 * time.Hour
const modelsDevFetchTimeout = 10 * time.Second

// ModelsDevOptions keeps the test seams: URLs, HTTP client, clock, cache path.
type ModelsDevOptions struct {
	Client    *http.Client // nil = a default client with the fetch timeout
	FetchURL  string       // nil/"" = ModelsDevAPIURL
	CachePath string       // nil/"" = <appConfig>/runtime/models-dev/api.json
	Now       func() time.Time
}

// modelsDevModel is one catalog entry (subset the TS reads).
type modelsDevModel struct {
	Modalities *struct {
		Input []string `json:"input,omitempty"`
	} `json:"modalities,omitempty"`
	Limit *struct {
		Context *int64 `json:"context,omitempty"`
		Output  *int64 `json:"output,omitempty"`
	} `json:"limit,omitempty"`
	ToolCall         *bool `json:"tool_call,omitempty"`
	StructuredOutput *bool `json:"structured_output,omitempty"`
	ReasoningOptions []struct {
		Type   string   `json:"type,omitempty"`
		Values []string `json:"values,omitempty"`
	} `json:"reasoning_options,omitempty"`
}

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models,omitempty"`
}

// ModelsDevCatalog is the parsed catalog (provider → models).
type ModelsDevCatalog map[string]modelsDevProvider

// ModelsDevMatch is one hit.
type ModelsDevMatch struct {
	ProviderID string
	Model      modelsDevModel
}

// ModelInfoLookupResult ports the lookup result.
type ModelInfoLookupResult struct {
	Found      bool           `json:"found"`
	ProviderID string         `json:"providerId,omitempty"`
	Config     map[string]any `json:"config"`
}

type modelsDevCache struct {
	Data      ModelsDevCatalog
	FetchedAt int64 // unix ms
}

// ModelsDev is the catalog service (memory + disk + network, in-flight share).
type ModelsDev struct {
	opts     ModelsDevOptions
	mu       sync.Mutex
	memory   *modelsDevCache
	inflight *modelsDevInflight
}

type modelsDevInflight struct {
	done    chan struct{}
	catalog ModelsDevCatalog
	err     error
}

// NewModelsDev builds the catalog service.
func NewModelsDev(opts ModelsDevOptions) *ModelsDev {
	if opts.FetchURL == "" {
		opts.FetchURL = ModelsDevAPIURL
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: modelsDevFetchTimeout}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &ModelsDev{opts: opts}
}

// LookupModelInfo ports lookupModelsDevModelInfo: 获取模型信息 entry. The
// catalog being unavailable (offline, no cache) is an ERROR so callers can
// distinguish 未收录 from 查询失败.
func (m *ModelsDev) LookupModelInfo(ctx context.Context, modelID string) (*ModelInfoLookupResult, error) {
	catalog, err := m.loadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errors.New("models.dev catalog is unavailable")
	}
	matches := FindModelsDevModels(catalog, modelID)
	if len(matches) == 0 {
		return &ModelInfoLookupResult{Found: false, Config: map[string]any{}}, nil
	}
	return &ModelInfoLookupResult{
		Found:      true,
		ProviderID: matches[0].ProviderID,
		Config:     ModelConfigFromMatches(matches),
	}, nil
}

func (m *ModelsDev) loadCatalog(ctx context.Context) (ModelsDevCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.opts.Now()
	if m.memory != nil && now.UnixMilli()-m.memory.FetchedAt < int64(modelsDevRefreshInterval/time.Millisecond) {
		return m.memory.Data, nil
	}
	if m.inflight != nil {
		inflight := m.inflight
		m.mu.Unlock()
		select {
		case <-inflight.done:
			m.mu.Lock()
			return inflight.catalog, inflight.err
		case <-ctx.Done():
			m.mu.Lock()
			return nil, ctx.Err()
		}
	}
	inflight := &modelsDevInflight{done: make(chan struct{})}
	m.inflight = inflight
	m.mu.Unlock()
	catalog, err := m.loadCatalogUncached(ctx)
	m.mu.Lock()
	m.inflight = nil
	inflight.catalog, inflight.err = catalog, err
	close(inflight.done)
	return catalog, err
}

func (m *ModelsDev) loadCatalogUncached(ctx context.Context) (ModelsDevCatalog, error) {
	disk := m.readDiskCache()
	now := m.opts.Now().UnixMilli()
	if disk != nil && now-disk.FetchedAt < int64(modelsDevRefreshInterval/time.Millisecond) {
		m.memory = disk
		return disk.Data, nil
	}
	data, err := m.fetchCatalog(ctx)
	if err != nil {
		// 离线时退回过期的磁盘数据；不写入内存缓存，下次调用仍会重试网络.
		if disk != nil {
			return disk.Data, nil
		}
		return nil, err
	}
	cache := &modelsDevCache{Data: data, FetchedAt: now}
	m.memory = cache
	m.writeDiskCache(cache)
	return data, nil
}

func (m *ModelsDev) fetchCatalog(ctx context.Context) (ModelsDevCatalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.opts.FetchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	resp, err := m.opts.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev API responded with HTTP %d", resp.StatusCode)
	}
	var catalog ModelsDevCatalog
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil, err
	}
	if !isCatalog(catalog) {
		return nil, errors.New("models.dev API returned an empty catalog")
	}
	return catalog, nil
}

func isCatalog(catalog ModelsDevCatalog) bool {
	return len(catalog) > 0
}

func (m *ModelsDev) cacheFilePath() string {
	if m.opts.CachePath != "" {
		return m.opts.CachePath
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nex", "v2", "runtime", "models-dev", "api.json")
}

type modelsDevCacheFile struct {
	Data      ModelsDevCatalog `json:"data"`
	FetchedAt int64            `json:"fetchedAt"`
}

func (m *ModelsDev) readDiskCache() *modelsDevCache {
	data, err := os.ReadFile(m.cacheFilePath())
	if err != nil {
		// 文件不存在或内容损坏都等价于"无缓存".
		return nil
	}
	var cache modelsDevCacheFile
	if err := json.Unmarshal(data, &cache); err != nil || !isCatalog(cache.Data) {
		return nil
	}
	return &modelsDevCache{Data: cache.Data, FetchedAt: cache.FetchedAt}
}

func (m *ModelsDev) writeDiskCache(cache *modelsDevCache) {
	path := m.cacheFilePath()
	payload, err := json.Marshal(modelsDevCacheFile{Data: cache.Data, FetchedAt: cache.FetchedAt})
	if err != nil {
		return
	}
	// 先写临时文件再 rename，避免多进程并发读到半截 JSON；失败只影响下次拉取速度.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	temp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(temp, payload, 0o644); err != nil {
		return
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
	}
}
