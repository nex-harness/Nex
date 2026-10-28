// Package app assembles the nex-next server: local channels, runner registry,
// browser channel dispatcher, runner link and HTTP API.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nex-harness/nex/server/internal/auth"
	"github.com/nex-harness/nex/server/internal/automations"
	"github.com/nex-harness/nex/server/internal/browserws"
	"github.com/nex-harness/nex/server/internal/configstore"
	"github.com/nex-harness/nex/server/internal/httpapi"
	"github.com/nex-harness/nex/server/internal/installer"
	"github.com/nex-harness/nex/server/internal/local"
	"github.com/nex-harness/nex/server/internal/runners"
	"github.com/nex-harness/nex/server/internal/runnerws"
)

// Options are the resolved runtime options (see internal/config).
type Options struct {
	DataDir string
	// ConfigDir is the server-owned config store root; empty means
	// <DataDir>/config.
	ConfigDir   string
	WebRoot     string
	AuthToken   string
	RunnerToken string
	ServerName  string
	Version     string
	// AllowedOrigins adds accepted /ws Origin host patterns for reverse
	// proxies that rewrite Host.
	AllowedOrigins []string
	// PublicRunnerURL is the wss:// URL installed runners dial back; empty
	// disables the SSH runner-install API (docs/next/runner-install.md).
	PublicRunnerURL string
	// RunnerBundlesDir overrides the per-platform bundle directory.
	RunnerBundlesDir string
}

// App is one fully assembled server.
type App struct {
	Options  Options
	Logger   *slog.Logger
	Registry *runners.Registry
	Auth     *auth.Service
	Local    *local.Registry
	// Config owns the server-side desired config and the runner push.
	Config *local.Manager
	// Installs is the SSH runner-install manager (nil when
	// NEX_PUBLIC_RUNNER_URL is unset — the API surface stays hidden).
	Installs *installer.Manager
	// Automations owns the server-side automation definitions, schedule and run
	// history (docs/next/automations-views-search.md §A).
	Automations *automations.Service

	// automationsCancel stops the schedule timer's context on Close.
	automationsCancel context.CancelFunc
	serverID          string
}

// New assembles the server. It creates the data directory and persists a
// stable server id on first run.
func New(options Options, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if options.Version == "" {
		options.Version = "0.0.0-dev"
	}
	if options.ServerName == "" {
		options.ServerName = "nex-next-server"
	}
	if err := os.MkdirAll(options.DataDir, 0o700); err != nil {
		return nil, err
	}
	// 修复：ConfigDir 为空时 configstore 会落到相对路径（进程 cwd），测试里直接
	// 构造 App 时把 config.json、secrets.enc 和密钥写进了源码树 next/server/test/。
	// 按字段注释在这里统一兜成 <DataDir>/config，不依赖调用方先走 internal/config。
	if options.ConfigDir == "" {
		options.ConfigDir = filepath.Join(options.DataDir, "config")
	}
	serverID, err := loadOrCreateServerID(options.DataDir)
	if err != nil {
		return nil, err
	}

	registry := runners.NewRegistry(logger)
	authService := auth.NewService(options.AuthToken,
		filepath.Join(options.DataDir, "web-sessions.json"), logger)

	settings := local.NewSettings(options.DataDir, registry.Paths)
	localRegistry := local.NewRegistry()
	localRegistry.Register("setting", settings)
	localRegistry.Register("broadcast", local.NewBroadcast())
	// Automations are server-owned: definitions, the schedule timer and run
	// history live in <ConfigDir>/automations.json, and a due run is dispatched
	// to the runner that owns its workspace. The store is read at construction,
	// so a corrupt or newer-schema file fails startup instead of being
	// overwritten. The timer starts below, after every dependency exists.
	automationService, err := automations.New(automations.Options{
		Dir:        options.ConfigDir,
		Logger:     logger,
		Dispatcher: local.NewRunnerAutomationsDispatcher(registry),
	})
	if err != nil {
		return nil, err
	}
	automationService.OnChange(func() {
		// v1: no browser push; the Automations page reloads on its own actions.
		logger.Debug("automations changed")
	})
	localRegistry.Register("window-controller",
		local.NewWindowController(registry, logger).WithAutomationOutcomes(automationService))
	// Server-owned config (docs/next/config-mcp-skills.md P0): provider-settings,
	// model-selection and the config channel read the doc + runner facts in both
	// modes (fallback compiles the runner's own catalog), and a doc change or a
	// runner's fresh facts re-broadcast both views.
	store := configstore.NewStore(options.ConfigDir, logger)
	configManager := local.NewManager(store, registry, logger, local.PublishConfigViews)
	localRegistry.Register("provider-settings", local.NewProviderSettingsChannel(configManager))
	localRegistry.Register("model-selection", local.NewModelSelectionChannel(configManager))
	localRegistry.Register("config", local.NewConfigChannel(configManager))
	// P1/P2 of the same doc: MCP servers and skill bundles are server-owned; live
	// MCP statuses are forwarded to the owning runner by the router.
	localRegistry.Register("mcp-sync", local.NewMcpSyncChannel(configManager))
	localRegistry.Register("skills", local.NewSkillsChannel(configManager))
	for name, channel := range local.StubChannels() {
		localRegistry.Register(name, channel)
	}

	// SSH runner-install channel (docs/next/runner-install.md): enabled only
	// when the operator set a public runner URL (the installed runner dials
	// it). The bundles dir defaults next to the server binary.
	var installs *installer.Manager
	if options.PublicRunnerURL != "" {
		bundlesDir := options.RunnerBundlesDir
		if bundlesDir == "" {
			bundlesDir = "bin/runner-bundles"
		}
		registryLookup := func(runnerID string) bool {
			_, ok := registry.Runner(runnerID)
			return ok
		}
		installs = installer.NewManager(logger)
		installs.LookupRunner = registryLookup
		installs.ServerURL = options.PublicRunnerURL
		installs.RunnerToken = options.RunnerToken
		installs.Bundles = installer.Bundles{Dir: bundlesDir}
	}

	application := &App{
		Options:     options,
		Logger:      logger,
		Registry:    registry,
		Auth:        authService,
		Local:       localRegistry,
		Config:      configManager,
		Installs:    installs,
		Automations: automationService,
		serverID:    serverID,
	}
	// Start arms the single schedule timer; missed windows are resolved on this
	// first tick, so it must run before the HTTP server accepts traffic.
	automationsCtx, automationsCancel := context.WithCancel(context.Background())
	application.automationsCancel = automationsCancel
	automationService.Start(automationsCtx)
	return application, nil
}

// Close stops the automation timer and releases its in-flight claims (the next
// process does not wait for the stale timeout). It is safe to call once.
func (a *App) Close() {
	if a == nil {
		return
	}
	if a.automationsCancel != nil {
		a.automationsCancel()
		a.automationsCancel = nil
	}
	if a.Automations != nil {
		a.Automations.Close()
	}
}

// Handler builds the HTTP handler (auth middleware included).
func (a *App) Handler() http.Handler {
	// The automation methods have no workspace in their argument, so the router
	// marks them TargetLocalUnsupported and the browser dispatcher reaches them
	// through this per-connection table. Without the delegate they fall to the
	// empty-list placeholder and the page stays empty.
	var newNexAgent func() *local.NexAgentLocal = func() *local.NexAgentLocal {
		return local.NewNexAgentLocal()
	}
	if automationService := a.Automations; automationService != nil {
		newNexAgent = func() *local.NexAgentLocal {
			return local.NewNexAgentLocal(local.WithAutomations(automationService))
		}
	}
	browser := browserws.NewHandler(browserws.Deps{
		Logger:         a.Logger,
		Registry:       a.Registry,
		Local:          a.Local,
		NewNexAgent:    newNexAgent,
		AllowedOrigins: a.Options.AllowedOrigins,
	})

	runnerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runnerws.ServeWithConfig(a.Logger, a.Registry, a.Options.RunnerToken, a.Options.Version, a.Config, w, r)
	})

	return httpapi.New(httpapi.Deps{
		Logger:     a.Logger,
		Auth:       a.Auth,
		Registry:   a.Registry,
		ServerID:   a.serverID,
		ServerName: a.Options.ServerName,
		Version:    a.Options.Version,
		WebRoot:    a.Options.WebRoot,
		BrowserWS:  browser,
		RunnerWS:   runnerHandler,
		Config:     configAdmin{a.Config},
		Skills:     skillUploader{a.Config},
		Installs:   a.Installs,
	}).Handler()
}

// configAdmin adapts *local.Manager to httpapi.ConfigAdmin.
type configAdmin struct{ manager *local.Manager }

func (c configAdmin) ImportFromRunner(ctx context.Context, runnerID, scope string) (any, error) {
	return c.manager.AdminImportFromRunner(ctx, runnerID, scope)
}

func (c configAdmin) KnownRunners() []string { return c.manager.KnownRunners() }

func (c configAdmin) RunnerModelStatuses() []httpapi.RunnerModelStatus {
	statuses := c.manager.RunnerModelStatuses()
	out := make([]httpapi.RunnerModelStatus, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, httpapi.RunnerModelStatus{
			RunnerID:           status.RunnerID,
			Name:               status.Name,
			Connected:          status.Connected,
			Mode:               status.Mode,
			ModelCount:         status.ModelCount,
			LastAppliedVersion: status.LastAppliedVersion,
		})
	}
	return out
}

// skillUploader adapts *local.Manager to httpapi.SkillUploader.
type skillUploader struct{ manager *local.Manager }

func (u skillUploader) Upload(ctx context.Context, kind, scope, runnerID, name string, body []byte) (any, error) {
	return u.manager.UploadSkillOrPrompt(kind, scope, runnerID, name, body)
}

func (u skillUploader) Delete(ctx context.Context, kind, scope, runnerID, name string) error {
	_, err := u.manager.DeleteSkillOrPrompt(kind, scope, runnerID, name)
	return err
}

// loadOrCreateServerID persists a random id so /api/server-info is stable
// across restarts.
func loadOrCreateServerID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "server-id.json")
	raw, err := os.ReadFile(path)
	if err == nil {
		var stored struct {
			ServerID string `json:"serverId"`
		}
		if json.Unmarshal(raw, &stored) == nil && stored.ServerID != "" {
			return stored.ServerID, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	id, err := httpapi.RandomServerID()
	if err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(map[string]string{"serverId": id}, "", "  ")
	if err != nil {
		return "", err
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return "", err
	}
	return id, nil
}
