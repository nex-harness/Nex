// Command nex-next-server runs the nex-next Go server: browser auth, the
// browser channel-RPC WebSocket, the runner link and the web static build.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nex-harness/nex/server/internal/app"
	"github.com/nex-harness/nex/server/internal/config"
	"github.com/nex-harness/nex/server/internal/httpapi"
)

// Version is overridable at build time with -ldflags "-X main.Version=...".
var Version = "0.0.0-dev"

func main() {
	os.Exit(run())
}

func run() int {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return 2
	}

	if invalid := httpapi.SetTrustedProxies(cfg.TrustedProxies); len(invalid) > 0 {
		logger.Error("invalid NEX_TRUSTED_PROXIES entries", "entries", invalid)
		return 2
	}
	if len(cfg.TrustedProxies) > 0 {
		logger.Info("login rate limiter trusts X-Forwarded-For from proxies", "proxies", cfg.TrustedProxies)
	}

	application, err := app.New(app.Options{
		DataDir:          cfg.DataDir,
		ConfigDir:        cfg.ConfigDir,
		WebRoot:          cfg.WebRoot,
		AuthToken:        cfg.AuthToken,
		RunnerToken:      cfg.RunnerToken,
		ServerName:       cfg.ServerName,
		Version:          Version,
		AllowedOrigins:   cfg.AllowedOrigins,
		PublicRunnerURL:  cfg.PublicRunnerURL,
		RunnerBundlesDir: cfg.RunnerBundlesDir,
	}, logger)
	if err != nil {
		logger.Error("cannot start server", "error", err)
		return 1
	}
	// 优雅退出时停掉 automation 定时器并释放 in-flight claim，否则下次启动要等
	// stale 回收（10 分钟）才能重新派发该窗口。
	defer application.Close()
	if cfg.PublicRunnerURL != "" {
		logger.Info("SSH runner install API enabled",
			"runnerUrl", cfg.PublicRunnerURL, "bundles", cfg.RunnerBundlesDir)
	}

	if info, err := os.Stat(cfg.WebRoot); err != nil || !info.IsDir() {
		logger.Warn("web static root is missing; non-API requests will 404", "path", cfg.WebRoot)
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           application.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Info("nex-next-server listening", "addr", cfg.Addr, "dataDir", cfg.DataDir,
		"configDir", cfg.ConfigDir, "webRoot", cfg.WebRoot, "version", Version)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "error", err)
		return 1
	}
	logger.Info("nex-next-server stopped")
	return 0
}
