// Package clientconfig ports packages/services/src/client-config/* (step
// 3.5): IClientConfigService — the window-level public-config read with a
// 1h TTL cache keyed by the full request URL. 首期仅公开配置；账户灰度不得
// 通过此实例或缓存复用。
package clientconfig

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ChannelName is ServiceChannels.ClientConfig.
const ChannelName = "client-config"

const (
	cacheTTLMS        = 60 * 60 * 1000
	requestTimeoutMS  = 15_000
	clientConfigsPath = "/api/v1/client/configs"
)

// Snapshot is ClientConfigSnapshot: only explicitly-approved public fields.
type Snapshot struct {
	PluginStoreOrder any `json:"pluginStoreOrder"`
}

// HTTPClient performs one GET; the adapter layer owns net/http.
type HTTPClient interface {
	// GetJSON fetches url with the request timeout applied by the caller;
	// responseURL is the final URL after redirects ("" when unknown).
	GetJSON(ctx context.Context, target *url.URL) (status int, responseURL string, body []byte, err error)
}

// RequestContext is the per-request context from the host.
type RequestContext struct {
	EndpointOrigin string
	AppVersion     string
	Platform       string
}

// Options configures NewService.
type Options struct {
	Client HTTPClient
	// ResolveRequestContext supplies endpoint/version/platform.
	ResolveRequestContext func() (RequestContext, error)
	// Now is the cache clock; nil uses time.Now.
	Now func() time.Time
}

// Service is IClientConfigService.
type Service struct {
	client  HTTPClient
	resolve func() (RequestContext, error)
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

// cacheEntry is one URL's cache slot. pending broadcasts the in-flight
// request's result to EVERY waiter: a buffered channel would deliver to only
// one receiver, so the result is memoized in the entry and waiters poll the
// closed done channel (a close broadcasts).
type cacheEntry struct {
	snapshot  *Snapshot
	expiresAt time.Time
	pending   *pendingRequest
}

type pendingRequest struct {
	done   chan struct{}
	result pendingResult
}

type pendingResult struct {
	snapshot *Snapshot
	err      error
}

// NewService builds the service.
func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		client:  opts.Client,
		resolve: opts.ResolveRequestContext,
		now:     now,
		entries: map[string]*cacheEntry{},
	}
}

// GetSnapshot ports getSnapshot(options): cache per URL, forceRefresh
// bypasses the TTL, concurrent callers share one in-flight request.
func (s *Service) GetSnapshot(ctx context.Context, forceRefresh bool) (*Snapshot, error) {
	requestContext, err := s.resolve()
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(requestContext.EndpointOrigin)
	if err != nil {
		return nil, err
	}
	target = target.JoinPath(clientConfigsPath)
	// JoinPath keeps the path relative when the base has no path component;
	// force the canonical absolute form (matches new URL(path, origin)).
	if !strings.HasPrefix(target.Path, "/") {
		target.Path = "/" + target.Path
	}
	query := target.Query()
	query.Set("app_version", requestContext.AppVersion)
	query.Set("platform", requestContext.Platform)
	target.RawQuery = query.Encode()
	key := target.String()

	s.mu.Lock()
	entry, ok := s.entries[key]
	if !ok {
		entry = &cacheEntry{}
		s.entries[key] = entry
	}
	if forceRefresh {
		entry.snapshot = nil
		entry.expiresAt = time.Time{}
	}
	if entry.snapshot != nil && entry.expiresAt.After(s.now()) {
		snapshot := *entry.snapshot
		s.mu.Unlock()
		return &snapshot, nil
	}
	if entry.pending == nil {
		pending := &pendingRequest{done: make(chan struct{})}
		entry.pending = pending
		// The fetch runs detached from the triggering caller's context: a
		// cancelled caller must not abort the shared in-flight request for
		// the others (the TS ties it only to its own 15s AbortController).
		fetchCtx, cancel := context.WithTimeout(context.Background(), time.Duration(requestTimeoutMS)*time.Millisecond)
		go func() {
			defer cancel()
			snapshot, err := s.fetch(fetchCtx, target)
			pending.result = pendingResult{snapshot: snapshot, err: err}
			s.mu.Lock()
			if current, ok := s.entries[key]; ok && current.pending == pending {
				current.snapshot = snapshot
				if err == nil {
					current.expiresAt = s.now().Add(time.Duration(cacheTTLMS) * time.Millisecond)
				}
				current.pending = nil
			}
			s.mu.Unlock()
			// close(done) broadcasts to every waiter.
			close(pending.done)
		}()
	}
	pending := entry.pending
	s.mu.Unlock()

	// The fetch goroutine applies its own 15s timeout; the CALLER keeps its
	// own context deadline (e.g. the RPC request's) so a cancelled caller
	// does not wait on the shared in-flight request longer than it wants.
	select {
	case <-pending.done:
		if pending.result.err != nil {
			return nil, pending.result.err
		}
		snapshot := *pending.result.snapshot
		return &snapshot, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fetch ports fetchSnapshot: redirect refusal (the endpoint may change
// between awaits — a response from a different URL is a context change, not a
// snapshot), parseClientConfigSnapshot validation. The 15s timeout is applied
// by the caller (the shared in-flight request owns its own clock).
func (s *Service) fetch(ctx context.Context, target *url.URL) (*Snapshot, error) {
	status, responseURL, body, err := s.client.GetJSON(ctx, target)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Public client config HTTP %d", status)
	}
	// 设置中的 endpoint 可能在两次 await 之间变化；不能把新地址的响应缓存到旧地址。
	if responseURL != "" && responseURL != target.String() {
		return nil, fmt.Errorf("Public client config request context changed")
	}
	return ParseClientConfigSnapshot(body)
}
