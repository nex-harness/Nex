// Package app assembles the nex-next server: local channels, runner registry,
// browser channel dispatcher, runner link and HTTP API.
package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/tylinux/nex-next/server/internal/auth"
	"github.com/tylinux/nex-next/server/internal/browserws"
	"github.com/tylinux/nex-next/server/internal/httpapi"
	"github.com/tylinux/nex-next/server/internal/local"
	"github.com/tylinux/nex-next/server/internal/runners"
	"github.com/tylinux/nex-next/server/internal/runnerws"
)

// Options are the resolved runtime options (see internal/config).
type Options struct {
	DataDir     string
	WebRoot     string
	AuthToken   string
	RunnerToken string
	ServerName  string
	Version     string
	// AllowedOrigins adds accepted /ws Origin host patterns for reverse
	// proxies that rewrite Host.
	AllowedOrigins []string
}

// App is one fully assembled server.
type App struct {
	Options  Options
	Logger   *slog.Logger
	Registry *runners.Registry
	Auth     *auth.Service
	Local    *local.Registry

	serverID string
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
	localRegistry.Register("window-controller", local.NewWindowController())
	for name, channel := range local.StubChannels() {
		localRegistry.Register(name, channel)
	}

	return &App{
		Options:  options,
		Logger:   logger,
		Registry: registry,
		Auth:     authService,
		Local:    localRegistry,
		serverID: serverID,
	}, nil
}

// Handler builds the HTTP handler (auth middleware included).
func (a *App) Handler() http.Handler {
	browser := browserws.NewHandler(browserws.Deps{
		Logger:         a.Logger,
		Registry:       a.Registry,
		Local:          a.Local,
		NewNexAgent:    local.NewNexAgentLocal,
		AllowedOrigins: a.Options.AllowedOrigins,
	})

	runnerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runnerws.Serve(a.Logger, a.Registry, a.Options.RunnerToken, a.Options.Version, w, r)
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
	}).Handler()
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
