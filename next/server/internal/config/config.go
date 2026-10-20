// Package config loads the nex-next-server process configuration from command
// line flags and environment variables.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Default values for flags and configuration.
const (
	DefaultAddr    = ":3030"
	DefaultWebRoot = "packages/web/dist"
)

// Config is the fully resolved server configuration.
type Config struct {
	// Addr is the TCP listen address, e.g. ":3030" or "127.0.0.1:3030".
	Addr string
	// DataDir holds server-id, web sessions and UI settings.
	DataDir string
	// WebRoot is the directory that contains the built web client. It may not
	// exist; in that case the HTTP layer answers 404 for non-API paths.
	WebRoot string
	// AuthToken is the browser login token. Required.
	AuthToken string
	// RunnerToken is the bearer token accepted on GET /runner. Required.
	RunnerToken string
	// ServerName is the human readable name reported by /api/server-info.
	ServerName string
	// AllowedOrigins lists extra WebSocket Origin host patterns accepted by
	// /ws (in addition to the request Host). Needed only when a reverse proxy
	// rewrites Host, which would otherwise make same-origin browser handshakes
	// fail the Origin check.
	AllowedOrigins []string
}

// Load parses flags from args (without the program name) and applies the
// documented environment variable fallbacks. It returns an error when a
// required secret is missing.
func Load(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("nex-next-server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	addr := fs.String("addr", "", "listen address (default :3030, env NEX_SERVER_ADDR)")
	dataDir := fs.String("data-dir", "", "data directory (default ~/.nex-next/server, env NEX_DATA_DIR)")
	webRoot := fs.String("web-root", "", "web static root (default packages/web/dist, env NEX_WEB_STATIC_ROOT)")
	name := fs.String("name", "", "server display name (default hostname, env NEX_SERVER_NAME)")
	origins := fs.String("allowed-origins", "", "comma-separated WebSocket Origin host patterns (env NEX_ALLOWED_ORIGINS)")

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parsing flags: %w", err)
	}

	cfg := Config{
		Addr:           firstNonEmpty(*addr, getenv("NEX_SERVER_ADDR"), getenv("PORT")),
		DataDir:        firstNonEmpty(*dataDir, getenv("NEX_DATA_DIR")),
		WebRoot:        firstNonEmpty(*webRoot, getenv("NEX_WEB_STATIC_ROOT")),
		AuthToken:      strings.TrimSpace(getenv("NEX_SERVER_AUTH_TOKEN")),
		RunnerToken:    strings.TrimSpace(getenv("NEX_RUNNER_TOKEN")),
		ServerName:     firstNonEmpty(*name, getenv("NEX_SERVER_NAME")),
		AllowedOrigins: splitList(firstNonEmpty(*origins, getenv("NEX_ALLOWED_ORIGINS"))),
	}

	if cfg.AuthToken == "" {
		return Config{}, errors.New("NEX_SERVER_AUTH_TOKEN is required")
	}
	if cfg.RunnerToken == "" {
		return Config{}, errors.New("NEX_RUNNER_TOKEN is required")
	}

	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	} else if isBarePort(cfg.Addr) {
		cfg.Addr = ":" + cfg.Addr
	}

	if cfg.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("resolving home directory: %w", err)
		}
		cfg.DataDir = filepath.Join(home, ".nex-next", "server")
	}
	if cfg.WebRoot == "" {
		cfg.WebRoot = DefaultWebRoot
	}
	if cfg.ServerName == "" {
		if host, err := os.Hostname(); err == nil && host != "" {
			cfg.ServerName = host
		} else {
			cfg.ServerName = "nex-next-server"
		}
	}

	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func isBarePort(addr string) bool {
	if addr == "" {
		return false
	}
	for _, r := range addr {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
