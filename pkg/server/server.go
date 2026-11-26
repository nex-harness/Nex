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
	"github.com/nex-harness/nex/pkg/relay"
	"github.com/nex-harness/nex/pkg/remote"
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
	PublicBaseURL              string          // Advertised base URL for /llm-proxy/*
	ListenPort                 int             // Local TCP listen port for LAN URL advertisement
	RunnersDir                 string          // Optional directory containing prebuilt multi-platform nex-runner-<os>-<arch> binaries
	Version                    string          // Server build version string
	BuildSHA                   string          // Server short commit hash (exposed for debug/about diagnostics)
	ReleaseManifestURL         string          // Optional URL pointing to GitHub/Gitea Release manifest.json / version.json
	AppReleaseAPIURL           string          // Optional URL overriding GitHub Releases latest API for server/desktop update checks
	MinRunnerProtocolVersion   int             // Minimum runner protocol_version required (defaults to protocol.MinRunnerProtocolVersion)
	RequiredRunnerCapabilities []string        // Optional capabilities required for compatibility gating
	StaticFS                   fs.FS           // Optional embedded clients/web/dist filesystem
	HTTPClient    *http.Client    // Optional custom HTTP client (for tests / models.dev / proxy)
	RemoteMCP     RemoteMCPExecutor
	EnableRelay   bool   // Mount stateless zero-trust /relay/v1/* endpoints onto this server
	RelayToken    string // Optional shared token protecting /relay/v1/*
}

type runnerConn struct {
	id      string
	ws      *websocket.Conn
	sendMu  sync.Mutex
	evicted bool
}

func (rc *runnerConn) sendJSON(ctx context.Context, env protocol.Envelope) error {
	if rc == nil || rc.ws == nil {
		return nil
	}
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
	id              string
	ws              *websocket.Conn
	sendMu          sync.Mutex
	subMu           sync.RWMutex
	subscribed      map[string]bool // session_id -> true; "*" means all root updates
	ptyAttached     map[string]bool // pty_id -> true
	browserAttached map[string]bool // browser tab_id -> true
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
	cfg       Config
	store     *store.Store
	mux          *http.ServeMux
	client       *http.Client
	streamClient *http.Client
	relay        *relay.Server
	remoteMgr *remote.HostManager

	mu               sync.RWMutex
	runners          map[string]*runnerConn
	runnerMeta       map[string]*runnerRuntimeMeta
	clients          map[string]*clientConn
	ptyToRunner      map[string]string
	browserToRunner  map[string]string
	planNotifiedSess map[string]bool
	pendingRPC       map[string]chan protocol.Envelope
	modelsCache      modelsDevCache
	releaseCache     releaseManifestCache
	appUpdateCache   appUpdateCache
	dlTokensMu       sync.Mutex
	dlTokens         map[string]time.Time

	reconcileDelay       time.Duration
	abortFallbackTimeout time.Duration
}

func (s *Server) effectiveReconcileDelay() time.Duration {
	if s.reconcileDelay > 0 {
		return s.reconcileDelay
	}
	return 1500 * time.Millisecond
}

func (s *Server) effectiveAbortFallbackTimeout() time.Duration {
	if s.abortFallbackTimeout > 0 {
		return s.abortFallbackTimeout
	}
	return 3 * time.Second
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
	streamHC := cfg.HTTPClient
	if streamHC == nil {
		var tr *http.Transport
		if defTr, ok := http.DefaultTransport.(*http.Transport); ok && defTr != nil {
			tr = defTr.Clone()
			tr.ResponseHeaderTimeout = 60 * time.Second
		}
		streamHC = &http.Client{
			Transport: tr,
			Timeout:   0,
		}
	}

	s := &Server{
		cfg:              cfg,
		store:            cfg.Store,
		mux:              http.NewServeMux(),
		client:           hc,
		streamClient:     streamHC,
		runners:          make(map[string]*runnerConn),
		runnerMeta:       make(map[string]*runnerRuntimeMeta),
		clients:          make(map[string]*clientConn),
		ptyToRunner:      make(map[string]string),
		browserToRunner:  make(map[string]string),
		planNotifiedSess: make(map[string]bool),
		pendingRPC:       make(map[string]chan protocol.Envelope),
	}
	if cfg.EnableRelay {
		s.relay = relay.New(relay.Config{AuthToken: cfg.RelayToken})
	}

	ctx := context.Background()
	// Reset any stale 'online' runners persisted before a previous server restart/crash.
	_ = s.store.MarkAllRunnersOffline(ctx)

	s.registerRoutes()

	rm, err := remote.NewHostManager(remote.HostManagerOptions{
		DataDir:    cfg.Store.DataDir(),
		ListenPort: cfg.ListenPort,
		Handler:    s.Handler(),
		DeviceAuthorizer: func(pubkeyFP string) (string, bool) {
			dev, dErr := cfg.Store.GetPairedDeviceByFP(context.Background(), pubkeyFP)
			if dErr != nil || dev == nil {
				return "", false
			}
			_ = cfg.Store.TouchPairedDevice(context.Background(), dev.ID, "relay")
			return dev.ID, true
		},
	})
	if err != nil {
		return nil, err
	}
	rcfg := remote.DefaultHostConfig()
	if rawCfg, gErr := cfg.Store.GetRemoteHostConfigJSON(ctx); gErr == nil && strings.TrimSpace(rawCfg) != "" {
		_ = json.Unmarshal([]byte(rawCfg), &rcfg)
	}
	rm.ApplyConfig(rcfg)
	s.remoteMgr = rm

	return s, nil
}

// RemoteManager returns the server's zero-trust Remote Access coordinator.
func (s *Server) RemoteManager() *remote.HostManager {
	return s.remoteMgr
}

// Close releases background remote-access tunnels and relay links cleanly.
func (s *Server) Close() error {
	if s.remoteMgr != nil {
		return s.remoteMgr.Close()
	}
	return nil
}

// Handler returns the root http.Handler for the server.
func (s *Server) Handler() http.Handler {
	return corsMiddleware(gzipMiddleware(s.authMiddleware(s.mux)))
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Nex-Device-Token")
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
		if (m.Enabled || len(m.WorkspaceOverrides) > 0) && matchesRunner(m.Scope, m.TargetRunners) {
			cp := m
			if cp.HostMode == "server_hosted" {
				// Never expose SaaS MCP credentials/env to remote runners
				cp.Command = ""
				cp.Args = nil
				cp.Env = nil
				cp.Headers = nil
				cp.URL = ""
			}
			mcps = append(mcps, cp)
		}
	}

	allCreds, _ := s.store.ListCredentials(ctx)
	var creds []store.CredentialRecord
	for _, c := range allCreds {
		if !c.Enabled {
			continue
		}
		if len(c.TargetRunners) > 0 {
			if !matchesRunner("runner", c.TargetRunners) {
				continue
			}
		} else if c.Scope == "runner" {
			continue
		}
		creds = append(creds, s.store.DecryptCredentialForRunner(c, runnerID))
	}

	skillVisRules, _ := s.store.GetSkillVisibilityRules(ctx)
	memories, _ := s.store.ListMemories(ctx, "")
	prefs, _ := s.store.GetUserPreferences(ctx)
	var globalInstructions *string
	if prefs.GlobalInstructions != "" {
		gi := prefs.GlobalInstructions
		globalInstructions = &gi
	}
	return &protocol.ConfigSnapshot{
		ConfigRev:            rev,
		ServerBaseURL:        s.cfg.PublicBaseURL,
		Providers:            sanitizedProviders,
		Models:               models,
		AgentProfiles:        profiles,
		Skills:               skills,
		SkillVisibilityRules: skillVisRules,
		Plugins:              plugins,
		MCPServers:           mcps,
		CronJobs:             crons,
		Credentials:          creds,
		CredentialsSynced:    true,
		Memories:             memories,
		MemoriesSynced:       true,
		GlobalInstructions:   globalInstructions,
		MemorySettings: &protocol.MemoryConfigSync{
			AutoExtract: prefs.MemoryAutoExtract,
			ProviderID:  prefs.MemoryProviderID,
			ModelID:     prefs.MemoryModelID,
		},
		BrowserSettings: &protocol.BrowserConfigSync{
			Enabled:        prefs.BrowserEnabled,
			Mode:           prefs.BrowserMode,
			Headless:       prefs.BrowserHeadless,
			ProfileMode:    prefs.BrowserProfileMode,
			AttachURL:      prefs.BrowserAttachURL,
			ExecutablePath: prefs.BrowserExecutablePath,
			DownloadMirror: prefs.BrowserDownloadMirror,
		},
		MultimodalSettings: &protocol.MultimodalConfigSync{
			ImageMode:                  prefs.ImageMode,
			ImageDriver:                prefs.ImageDriver,
			ImageBaseURL:               prefs.ImageBaseURL,
			ImageAPIKey:                prefs.ImageAPIKey,
			ImageProviderID:            prefs.ImageProviderID,
			ImageModelID:               prefs.ImageModelID,
			ImageProviders:             prefs.ImageProviders,
			ActiveImageProviderID:      prefs.ActiveImageProviderID,
			ActiveImageModelID:         prefs.ActiveImageModelID,
			ClassifierMode:             prefs.ClassifierMode,
			ClassifierDriver:           prefs.ClassifierDriver,
			ClassifierProviderID:       prefs.ClassifierProviderID,
			ClassifierModelID:          prefs.ClassifierModelID,
			ClassifierBaseURL:          prefs.ClassifierBaseURL,
			ClassifierAPIKey:           prefs.ClassifierAPIKey,
			ClassifierProviders:        prefs.ClassifierProviders,
			ActiveClassifierProviderID: prefs.ActiveClassifierProviderID,
			ActiveClassifierModelID:    prefs.ActiveClassifierModelID,
			JevAutoApproveEnabled:      prefs.JevAutoApproveEnabled,
			JevAutoRouterEnabled:       prefs.JevAutoRouterEnabled,
		},
		GoalSettings: &protocol.GoalConfigSync{
			MaxRounds:                prefs.GoalMaxRounds,
			MaxTurnsPerRound:         prefs.GoalMaxTurnsPerRound,
			MaxConsecutiveNoProgress: prefs.GoalMaxConsecutiveNoProgress,
			VerifierProviderID:       prefs.GoalVerifierProviderID,
			VerifierModelID:          prefs.GoalVerifierModelID,
		},
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
	if err := s.checkRunnerCompatible(ctx, runnerID); err != nil {
		return protocol.Envelope{}, err
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

	hc := s.streamClient
	if hc == nil {
		hc = s.client
	}
	resp, err := hc.Do(upReq)
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
