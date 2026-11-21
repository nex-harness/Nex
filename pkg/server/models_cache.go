package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

const (
	modelsDevCacheTTL      = 12 * time.Hour
	modelsDevFetchTimeout  = 20 * time.Second
	modelsDevCacheFileName = "models_dev_cache.json"
)

type modelsDevCache struct {
	mu          sync.RWMutex
	catalog     []ai.ModelMeta
	fetchedAt   time.Time
	diskLoaded  bool
	refreshMu   sync.Mutex
	refreshWait chan struct{}
}

func (s *Server) modelsDevCachePath() string {
	if s.store == nil || s.store.DataDir() == "" {
		return ""
	}
	return filepath.Join(s.store.DataDir(), modelsDevCacheFileName)
}

func (s *Server) ensureDiskModelsCatalogLoaded() {
	s.modelsCache.mu.RLock()
	loaded := s.modelsCache.diskLoaded
	s.modelsCache.mu.RUnlock()
	if loaded {
		return
	}

	s.modelsCache.mu.Lock()
	defer s.modelsCache.mu.Unlock()
	if s.modelsCache.diskLoaded {
		return
	}
	s.modelsCache.diskLoaded = true

	p := s.modelsDevCachePath()
	if p == "" {
		return
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) == 0 {
		return
	}
	parsed, err := ai.ParseModelsDevJSON(raw)
	if err != nil || len(parsed) == 0 {
		return
	}
	s.modelsCache.catalog = parsed
	s.modelsCache.fetchedAt = info.ModTime()
}

func (s *Server) saveModelsCatalogToDisk(raw []byte) {
	p := s.modelsDevCachePath()
	if p == "" || len(raw) == 0 {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, p)
	}
}

func (s *Server) refreshModelsCatalog(ctx context.Context, endpoint string, persist bool) ([]ai.ModelMeta, error) {
	// Coalesce concurrent default-endpoint fetches so parallel lookups only hit models.dev once.
	if endpoint == "" {
		s.modelsCache.refreshMu.Lock()
		if ch := s.modelsCache.refreshWait; ch != nil {
			s.modelsCache.refreshMu.Unlock()
			select {
			case <-ch:
				s.modelsCache.mu.RLock()
				cat := s.modelsCache.catalog
				s.modelsCache.mu.RUnlock()
				if len(cat) > 0 {
					return cat, nil
				}
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			s.modelsCache.refreshMu.Lock()
		}
		waitCh := make(chan struct{})
		s.modelsCache.refreshWait = waitCh
		s.modelsCache.refreshMu.Unlock()
		defer func() {
			s.modelsCache.refreshMu.Lock()
			s.modelsCache.refreshWait = nil
			close(waitCh)
			s.modelsCache.refreshMu.Unlock()
		}()
	}

	fetchCtx, cancel := context.WithTimeout(ctx, modelsDevFetchTimeout)
	defer cancel()

	raw, err := ai.FetchModelsDevRaw(fetchCtx, s.client, endpoint)
	if err != nil {
		return nil, err
	}
	parsed, err := ai.ParseModelsDevJSON(raw)
	if err != nil {
		return nil, err
	}

	if len(parsed) > 0 {
		s.modelsCache.mu.Lock()
		s.modelsCache.catalog = parsed
		s.modelsCache.fetchedAt = time.Now()
		s.modelsCache.diskLoaded = true
		s.modelsCache.mu.Unlock()
		if persist {
			s.saveModelsCatalogToDisk(raw)
		}
	}
	return parsed, nil
}

// getModelsDevCatalog returns the cached models.dev catalog (memory -> disk -> live fetch -> embedded seed).
// When the cached catalog is older than modelsDevCacheTTL, it returns the cached snapshot immediately
// and triggers a non-blocking background refresh.
func (s *Server) getModelsDevCatalog(ctx context.Context) []ai.ModelMeta {
	s.ensureDiskModelsCatalogLoaded()

	s.modelsCache.mu.RLock()
	cat := s.modelsCache.catalog
	fetchedAt := s.modelsCache.fetchedAt
	s.modelsCache.mu.RUnlock()

	if len(cat) > 0 {
		if time.Since(fetchedAt) > modelsDevCacheTTL {
			go func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), modelsDevFetchTimeout)
				defer cancel()
				_, _ = s.refreshModelsCatalog(bgCtx, "", true)
			}()
		}
		return cat
	}

	if live, err := s.refreshModelsCatalog(ctx, "", true); err == nil && len(live) > 0 {
		return live
	}

	seed, _ := ai.EmbeddedModelsCatalog()
	return seed
}
