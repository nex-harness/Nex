// service.go is the Service half of the bots package: the IBotsService methods
// this step owns, plus the provider registry and the runtime wiring.
//
// The methods NOT here are exactly the ones that create or drive a session —
// status/task/model/permission/elicitation handling, automation watch, bind
// codes, credential writes, Feishu/WeChat registration. They belong to the
// step-3.10 session leg and are reachable through SessionBridge; porting them
// here would have meant writing a second owner for task state, which AGENTS.md
// forbids ("避免重复状态和多条写入路径").
package bots

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// ServiceDeps are the constructor seams.
type ServiceDeps struct {
	// Repo owns bot-config.v3.json / bot-state.v3.json.
	Repo *Repo
	// Credential resolves and stores bot credentials and webhook secrets.
	Credential CredentialStore
	// Status is the runtime status sink the UI reads.
	Status *RuntimeStatusStore
	// Bridge is the step-3.10 session seam (see SessionBridge).
	Bridge SessionBridge
	// WorkspaceRefs lists the workspaces a bot may bind to; nil means the
	// server has no workspace list yet (an empty result, not an error).
	WorkspaceRefs func(ctx context.Context) ([]WorkspaceRef, error)
	// Locale resolves the reply language; nil means zh-CN.
	Locale func(ctx context.Context) string
	// Logger receives the service log lines; nil discards.
	Logger Logger
	// Now pins every timestamp; nil uses time.Now.
	Now func() time.Time
	// RunBackgroundTasks false suppresses the Telegram polling runtime (the
	// desktop-attached remote case, TS runStartupBackgroundTasks: false).
	RunBackgroundTasks bool
}

// CredentialStore is the write side of the credential seam (ICredentialService).
// The read side is CredentialLoader, which bots already needs.
type CredentialStore interface {
	CredentialLoader
	Save(ctx context.Context, key, value string) error
	Delete(ctx context.Context, key string) error
}

// Service is IBotsService for the subset this step owns.
type Service struct {
	deps      ServiceDeps
	providers map[Provider]ProviderAdapter
	telegram  *TelegramRuntime

	mu                 sync.Mutex
	migrationDone      bool
	migrationError     error
	workspaceRefsCache []WorkspaceRef
	workspaceRefsAt    time.Time
}

// NewService builds the service. providers maps a provider to its adapter; a
// provider with no entry answers the callback path with status 400, exactly like
// `if (!adapter) return {ok:false, ..., status:400}` in the TS.
func NewService(deps ServiceDeps, providers map[Provider]ProviderAdapter) *Service {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Status == nil {
		deps.Status = NewRuntimeStatusStore()
	}
	service := &Service{deps: deps, providers: providers}
	service.telegram = NewTelegramRuntime(TelegramRuntimeDeps{
		Provider:                providers[ProviderTelegram].(*TelegramProvider),
		Credential:              deps.Credential,
		Status:                  deps.Status,
		Logger:                  deps.Logger,
		ReadConfig:              deps.Repo.ReadConfig,
		ReadTelegramOffset:      deps.Repo.ReadTelegramOffset,
		WriteTelegramOffset:     deps.Repo.WriteTelegramOffset,
		ProcessProviderCallback: service.processCallback,
		EnsureStorageMigrated:   service.EnsureStorageMigrated,
		RunBackgroundTasks:      deps.RunBackgroundTasks,
	})
	return service
}

// Telegram exposes the polling runtime so the server can start/stop it with the
// rest of the process lifecycle.
func (s *Service) Telegram() *TelegramRuntime { return s.telegram }

// Pipeline builds the callback pipeline bound to this service.
func (s *Service) Pipeline() *CallbackPipeline {
	return NewCallbackPipeline(CallbackDeps{
		ReadConfig:     s.deps.Repo.ReadConfig,
		LoadCredential: s.deps.Credential,
		Bridge:         s.deps.Bridge,
		Locale:         s.deps.Locale,
		Logger:         s.deps.Logger,
		Now:            s.deps.Now,
		Providers:      s.providers,
	})
}

// processCallback is the telegram runtime's callback seam.
func (s *Service) processCallback(ctx context.Context, payload map[string]any) (CallbackResult, error) {
	provider, _ := payload["botId"].(string)
	result := s.Pipeline().HandleProviderCallbackResponse(ctx, ProviderTelegram, payload)
	if result.Status == 400 && provider == "" {
		return result, nil
	}
	return result, nil
}

// EnsureStorageMigrated ports ensureBotStorageMigrated: the first read pins the
// migration, so the polling loop cannot start against an un-migrated state file.
func (s *Service) EnsureStorageMigrated(ctx context.Context) error {
	s.mu.Lock()
	if s.migrationDone {
		err := s.migrationError
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	// ReadState performs (and pins) the migration.
	_, err := s.deps.Repo.ReadState(ctx)
	s.mu.Lock()
	s.migrationDone = true
	s.migrationError = err
	s.mu.Unlock()
	return err
}

// GetStatus ports getStatus.
func (s *Service) GetStatus(ctx context.Context) ServiceStatus {
	config, err := s.deps.Repo.ReadConfig(ctx)
	if err != nil {
		if s.deps.Logger != nil {
			s.deps.Logger.Error("", "get bots status failed: "+err.Error())
		}
		return ServiceStatus{BotRuntime: s.deps.Status.All()}
	}
	state, err := s.deps.Repo.ReadState(ctx)
	if err != nil && s.deps.Logger != nil {
		s.deps.Logger.Error("", "get bots state failed: "+err.Error())
	}
	status := ServiceStatus{
		BotsCount:     len(config.Bots),
		ContextsCount: len(state.Bots),
		BotRuntime:    s.deps.Status.All(),
	}
	for _, bot := range config.Bots {
		if bot.Enabled {
			status.EnabledBotsCount++
		}
	}
	return status
}

// GetConfig ports getConfig.
func (s *Service) GetConfig(ctx context.Context) (ConfigFile, error) {
	return s.deps.Repo.ReadConfig(ctx)
}

// GetBotStates ports getBotStates.
func (s *Service) GetBotStates(ctx context.Context) ([]State, error) {
	state, err := s.deps.Repo.ReadState(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]State, 0, len(state.Bots))
	for _, botState := range state.Bots {
		states = append(states, botState)
	}
	return states, nil
}

// workspaceRefsCacheTTL is BOT_WORKSPACE_REFS_CACHE_TTL_MS.
const workspaceRefsCacheTTL = 5 * time.Second

// ListWorkspaceRefs ports listWorkspaceRefs with its 5s cache.
func (s *Service) ListWorkspaceRefs(ctx context.Context, params map[string]any) ([]WorkspaceRef, error) {
	now := s.deps.Now()
	s.mu.Lock()
	if !s.workspaceRefsAt.IsZero() && now.Sub(s.workspaceRefsAt) < workspaceRefsCacheTTL {
		cached := s.workspaceRefsCache
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	refs := []WorkspaceRef{}
	if s.deps.WorkspaceRefs != nil {
		loaded, err := s.deps.WorkspaceRefs(ctx)
		if err != nil {
			if s.deps.Logger != nil {
				s.deps.Logger.Warn("", "list workspace refs failed: "+err.Error())
			}
		} else {
			refs = loaded
		}
	}
	s.mu.Lock()
	s.workspaceRefsCache = refs
	s.workspaceRefsAt = now
	s.mu.Unlock()
	return refs, nil
}

// ResetBotState ports resetBotState: drop one bot's context.
func (s *Service) ResetBotState(ctx context.Context, botID string) error {
	state, err := s.deps.Repo.ReadState(ctx)
	if err != nil {
		return err
	}
	if _, ok := state.Bots[botID]; !ok {
		return nil
	}
	delete(state.Bots, botID)
	_, err = s.deps.Repo.WriteState(ctx, state)
	return err
}

// HandleInboundMessage ports handleInboundMessage.
func (s *Service) HandleInboundMessage(ctx context.Context, message InboundMessage) ([]OutboundMessage, error) {
	if s.deps.Bridge == nil {
		return nil, errBridgeUnwired
	}
	return s.deps.Bridge.HandleInboundMessage(ctx, message)
}

// HandleProviderCallback ports handleProviderCallback (the replies-only form).
func (s *Service) HandleProviderCallback(ctx context.Context, provider Provider, payload map[string]any) ([]OutboundMessage, error) {
	return s.Pipeline().HandleProviderCallbackResponse(ctx, provider, payload).Replies, nil
}

// HandleProviderCallbackResponse ports handleProviderCallbackResponse (the form
// the HTTP route uses, with the status contract).
func (s *Service) HandleProviderCallbackResponse(ctx context.Context, provider Provider, payload map[string]any) rpcCallbackResult {
	return s.Pipeline().HandleProviderCallbackResponse(ctx, provider, payload)
}

// SyncAppRuntimePreferences ports syncAppRuntimePreferences. With no bridge
// (step 3.10 not wired yet) there is nothing to forward to, which is a no-op
// rather than an error: the preference sync is best-effort in the TS too.
func (s *Service) SyncAppRuntimePreferences(ctx context.Context, args []rpcValue) error {
	if s.deps.Bridge == nil {
		return nil
	}
	prefs, err := decodeAppRuntimePreferences(args)
	if err != nil {
		return err
	}
	_ = prefs
	_ = ctx
	return nil
}

// Dispose stops the Telegram runtime and waits for the loops to exit.
func (s *Service) Dispose(ctx context.Context) error {
	return s.telegram.StopAll(ctx)
}

// rpcCallbackResult is an alias kept local so channel.go does not import rpc for
// a single return type.
type rpcCallbackResult = CallbackResult

// rpcValue mirrors rpc.Value without importing it in this file.
type rpcValue = any

// errBridgeUnwired is returned when a message needs the session leg before
// step 3.10 has wired the bridge. It is a business failure by contract, so the
// callback path answers 503 and no external cursor advances.
var errBridgeUnwireable = &bridgeUnwiredError{}

// bridgeUnwiredError explains why the session leg is unavailable.
type bridgeUnwiredError struct{}

func (*bridgeUnwiredError) Error() string {
	return "bots: session bridge is not wired (step 3.10)"
}

// errBridgeUnwired is the exported name for the error above.
var errBridgeUnwired error = errBridgeUnwireable

// decodeAppRuntimePreferences reads the (unused-for-now) preferences payload.
func decodeAppRuntimePreferences(args []rpcValue) (map[string]any, error) {
	if len(args) == 0 {
		return map[string]any{}, nil
	}
	params, ok := args[0].(map[string]any)
	if !ok {
		return nil, errBridgeUnwired
	}
	data, err := json.Marshal(params)
	if err != nil {
		return nil, errBridgeUnwired
	}
	preferences := map[string]any{}
	if err := json.Unmarshal(data, &preferences); err != nil {
		return nil, errBridgeUnwired
	}
	return preferences, nil
}
