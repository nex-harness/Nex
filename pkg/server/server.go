package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/evanw/esbuild/pkg/api"
	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

// RemoteMCPExecutor executes server-hosted MCP tool calls routed from Runners.
type RemoteMCPExecutor interface {
	CallTool(ctx context.Context, namespace, toolName string, args json.RawMessage) (json.RawMessage, error)
}

// Config configures the central Control Plane Server.
type Config struct {
	Store         *store.Store
	PairingSecret string          // Optional shared pairing token for Runner registration
	PublicBaseURL string          // Advertised base URL for /llm-proxy/*
	StaticFS      fs.FS           // Optional embedded clients/web/dist filesystem
	HTTPClient    *http.Client    // Optional custom HTTP client (for tests / models.dev / proxy)
	RemoteMCP     RemoteMCPExecutor
}

type runnerConn struct {
	id     string
	ws     *websocket.Conn
	sendMu sync.Mutex
}

func (rc *runnerConn) sendJSON(ctx context.Context, env protocol.Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	rc.sendMu.Lock()
	defer rc.sendMu.Unlock()
	wCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return rc.ws.Write(wCtx, websocket.MessageText, b)
}

func (rc *runnerConn) sendBinary(ctx context.Context, frame []byte) error {
	rc.sendMu.Lock()
	defer rc.sendMu.Unlock()
	wCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return rc.ws.Write(wCtx, websocket.MessageBinary, frame)
}

type clientConn struct {
	id          string
	ws          *websocket.Conn
	sendMu      sync.Mutex
	subMu       sync.RWMutex
	subscribed  map[string]bool // session_id -> true; "*" means all root updates
	ptyAttached map[string]bool // pty_id -> true
}

func (cc *clientConn) sendJSON(ctx context.Context, env protocol.Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	cc.sendMu.Lock()
	defer cc.sendMu.Unlock()
	wCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return cc.ws.Write(wCtx, websocket.MessageText, b)
}

func (cc *clientConn) sendBinary(ctx context.Context, frame []byte) error {
	cc.sendMu.Lock()
	defer cc.sendMu.Unlock()
	wCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return cc.ws.Write(wCtx, websocket.MessageBinary, frame)
}

// Server implements the Nex-v3 Central Control Plane (HTTP REST + WS Hub + LLM Proxy).
type Server struct {
	cfg    Config
	store  *store.Store
	mux    *http.ServeMux
	client *http.Client

	mu          sync.RWMutex
	runners     map[string]*runnerConn
	clients     map[string]*clientConn
	ptyToRunner map[string]string
	pendingRPC  map[string]chan protocol.Envelope
}

// New constructs and wires a Control Plane Server.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("server: Store is required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}

	s := &Server{
		cfg:         cfg,
		store:       cfg.Store,
		mux:         http.NewServeMux(),
		client:      hc,
		runners:     make(map[string]*runnerConn),
		clients:     make(map[string]*clientConn),
		ptyToRunner: make(map[string]string),
		pendingRPC:  make(map[string]chan protocol.Envelope),
	}

	// Seed default embedded models if models table is currently empty
	ctx := context.Background()
	if existing, err := s.store.ListModels(ctx, ""); err == nil && len(existing) == 0 {
		_ = s.seedBuiltInProvidersAndModels(ctx)
	}

	s.registerRoutes()
	return s, nil
}

func (s *Server) seedBuiltInProvidersAndModels(ctx context.Context) error {
	_ = s.store.UpsertProvider(ctx, store.ProviderRecord{
		ID: "anthropic", Name: "Anthropic", Driver: ai.DriverAnthropic,
		BaseURL: "https://api.anthropic.com/v1", ProxyMode: "direct", Enabled: true,
	})
	_ = s.store.UpsertProvider(ctx, store.ProviderRecord{
		ID: "openai", Name: "OpenAI Codex", Driver: ai.DriverOpenAIResponses,
		BaseURL: "https://api.openai.com/v1", ProxyMode: "direct", Enabled: true,
	})
	if seed, err := ai.EmbeddedModelsCatalog(); err == nil {
		_ = s.store.UpsertModelsBatch(ctx, seed, false)
	}
	return nil
}

// Handler returns the root http.Handler for the server.
func (s *Server) Handler() http.Handler {
	return corsMiddleware(s.mux)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// TranspilePluginTS compiles TypeScript plugin source into a self-contained JS bundle via pure-Go esbuild.
func TranspilePluginTS(sourceTS string) (compiledJS string, sha256Hex string, err error) {
	res := api.Transform(sourceTS, api.TransformOptions{
		Loader:            api.LoaderTS,
		Format:            api.FormatCommonJS,
		Target:            api.ES2020,
		MinifyWhitespace:  false,
		MinifyIdentifiers: false,
	})
	if len(res.Errors) > 0 {
		return "", "", fmt.Errorf("esbuild transform error: %s", res.Errors[0].Text)
	}
	js := string(res.Code)
	sum := sha256.Sum256(res.Code)
	return js, hex.EncodeToString(sum[:]), nil
}

// buildConfigSnapshot builds the configuration snapshot scoped to runnerID.
func (s *Server) buildConfigSnapshot(ctx context.Context, runnerID string) (*protocol.ConfigSnapshot, error) {
	rev, _ := s.store.GetConfigRev(ctx)
	providers, _ := s.store.ListProviders(ctx)
	// Strip plaintext API keys for providers configured in server_proxy mode!
	sanitizedProviders := make([]store.ProviderRecord, 0, len(providers))
	for _, p := range providers {
		cp := p
		if cp.ProxyMode == "server_proxy" {
			cp.APIKey = "runner-proxy-token"
			if s.cfg.PublicBaseURL != "" {
				cp.BaseURL = strings.TrimRight(s.cfg.PublicBaseURL, "/") + "/llm-proxy/" + cp.ID
			}
		}
		sanitizedProviders = append(sanitizedProviders, cp)
	}

	models, _ := s.store.ListModels(ctx, "")
	profiles, _ := s.store.ListAgentProfiles(ctx)
	allSkills, _ := s.store.ListSkills(ctx)
	allPlugins, _ := s.store.ListPlugins(ctx)
	allMCP, _ := s.store.ListMCPServers(ctx)
	crons, _ := s.store.ListCronJobs(ctx, runnerID)

	matchesRunner := func(scope string, targets []string) bool {
		if scope == "" || scope == "global" || len(targets) == 0 {
			return true
		}
		for _, t := range targets {
			if t == runnerID {
				return true
			}
		}
		return false
	}

	var skills []store.SkillRecord
	for _, sk := range allSkills {
		if sk.Enabled && matchesRunner(sk.Scope, sk.TargetRunners) {
			skills = append(skills, sk)
		}
	}
	var plugins []store.PluginRecord
	for _, pl := range allPlugins {
		if pl.Enabled && matchesRunner(pl.Scope, pl.TargetRunners) {
			plugins = append(plugins, pl)
		}
	}
	var mcps []store.MCPServerRecord
	for _, m := range allMCP {
		if m.Enabled && matchesRunner(m.Scope, m.TargetRunners) {
			cp := m
			if cp.HostMode == "server_hosted" {
				// Never expose SaaS MCP credentials/env to remote runners
				cp.Command = ""
				cp.Args = nil
				cp.Env = nil
				cp.URL = ""
			}
			mcps = append(mcps, cp)
		}
	}

	memories, _ := s.store.ListMemories(ctx, "")
	return &protocol.ConfigSnapshot{
		ConfigRev:     rev,
		ServerBaseURL: s.cfg.PublicBaseURL,
		Providers:     sanitizedProviders,
		Models:        models,
		AgentProfiles: profiles,
		Skills:        skills,
		Plugins:       plugins,
		MCPServers:    mcps,
		CronJobs:      crons,
		Memories:      memories,
	}, nil
}

// BroadcastConfigSync pushes the latest ConfigSnapshot to all online Runners.
func (s *Server) BroadcastConfigSync(ctx context.Context) {
	s.mu.RLock()
	conns := make([]*runnerConn, 0, len(s.runners))
	for _, rc := range s.runners {
		conns = append(conns, rc)
	}
	s.mu.RUnlock()

	for _, rc := range conns {
		snap, err := s.buildConfigSnapshot(ctx, rc.id)
		if err != nil {
			continue
		}
		env, err := protocol.NewEnvelope(protocol.TypeConfigSync, snap)
		if err == nil {
			env.RunnerID = rc.id
			_ = rc.sendJSON(ctx, env)
		}
	}
}

func (s *Server) callRunnerRPC(ctx context.Context, runnerID string, env protocol.Envelope, timeout time.Duration) (protocol.Envelope, error) {
	s.mu.RLock()
	rc := s.runners[runnerID]
	s.mu.RUnlock()
	if rc == nil {
		return protocol.Envelope{}, fmt.Errorf("runner %s is offline", runnerID)
	}
	if env.ID == "" {
		env.ID = "rpc_" + uuid.NewString()
	}
	ch := make(chan protocol.Envelope, 1)
	s.mu.Lock()
	s.pendingRPC[env.ID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingRPC, env.ID)
		s.mu.Unlock()
	}()

	if err := rc.sendJSON(ctx, env); err != nil {
		return protocol.Envelope{}, err
	}

	rCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case resp := <-ch:
		return resp, nil
	case <-rCtx.Done():
		return protocol.Envelope{}, rCtx.Err()
	}
}

// FetchBlobFromAnyRunner attempts to pull a CAS blob from online Runners if Server doesn't have it cached yet.
func (s *Server) FetchBlobFromAnyRunner(ctx context.Context, hash string) ([]byte, string, error) {
	s.mu.RLock()
	runnerIDs := make([]string, 0, len(s.runners))
	for id := range s.runners {
		runnerIDs = append(runnerIDs, id)
	}
	s.mu.RUnlock()

	for _, rid := range runnerIDs {
		reqEnv, _ := protocol.NewEnvelope(protocol.TypeBlobFetch, protocol.BlobFetchPayload{Hash: hash})
		resp, err := s.callRunnerRPC(ctx, rid, reqEnv, 8*time.Second)
		if err != nil {
			continue
		}
		var bd protocol.BlobDataPayload
		if err := json.Unmarshal(resp.Payload, &bd); err == nil && bd.Error == "" && len(bd.Data) > 0 {
			_, _, _ = s.store.PutBlob(ctx, bd.Data, bd.Mime, 1)
			return bd.Data, bd.Mime, nil
		}
	}
	return nil, "", fmt.Errorf("blob %s not found on server or online runners", hash)
}

// handleLLMProxy streams requests to upstream provider while injecting Server-held API keys.
func (s *Server) handleLLMProxy(w http.ResponseWriter, r *http.Request) {
	// Path pattern: /llm-proxy/{provider_id}/{rest...}
	trimmed := strings.TrimPrefix(r.URL.Path, "/llm-proxy/")
	provID, restPath, ok := strings.Cut(trimmed, "/")
	if !ok || provID == "" {
		http.Error(w, "invalid proxy path", http.StatusBadRequest)
		return
	}

	prov, err := s.store.GetProvider(r.Context(), provID)
	if err != nil || !prov.Enabled {
		http.Error(w, "provider not found or disabled", http.StatusNotFound)
		return
	}

	targetURL := strings.TrimRight(prov.BaseURL, "/") + "/" + strings.TrimLeft(restPath, "/")
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	upReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	upReq.Header.Set("Accept", r.Header.Get("Accept"))
	upReq.Header.Set("User-Agent", ai.DefaultUserAgent)

	if prov.Driver == ai.DriverAnthropic {
		upReq.Header.Set("anthropic-version", "2023-06-01")
		if b := r.Header.Get("anthropic-beta"); b != "" {
			upReq.Header.Set("anthropic-beta", b)
		}
		if prov.APIKey != "" {
			upReq.Header.Set("x-api-key", prov.APIKey)
		}
	} else {
		if prov.APIKey != "" {
			upReq.Header.Set("Authorization", "Bearer "+prov.APIKey)
		}
	}

	resp, err := s.client.Do(upReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 16*1024)
	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rErr != nil {
			if !errors.Is(rErr, io.EOF) {
				return
			}
			break
		}
	}
}
