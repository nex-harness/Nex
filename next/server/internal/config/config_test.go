package config_test

import (
	"strings"
	"testing"

	"github.com/tylinux/nex-next/server/internal/config"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadRequiresTokens(t *testing.T) {
	_, err := config.Load(nil, env(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "NEX_SERVER_AUTH_TOKEN") {
		t.Fatalf("error = %v, want missing auth token", err)
	}

	_, err = config.Load(nil, env(map[string]string{"NEX_SERVER_AUTH_TOKEN": "a"}))
	if err == nil || !strings.Contains(err.Error(), "NEX_RUNNER_TOKEN") {
		t.Fatalf("error = %v, want missing runner token", err)
	}
}

func TestLoadDefaultsAndFlagPriority(t *testing.T) {
	values := map[string]string{
		"NEX_SERVER_AUTH_TOKEN": "a",
		"NEX_RUNNER_TOKEN":      "r",
		"NEX_DATA_DIR":          "/tmp/env-data",
		"NEX_WEB_STATIC_ROOT":   "/tmp/env-web",
		"NEX_SERVER_NAME":       "env-name",
		"NEX_SERVER_ADDR":       ":4000",
	}
	cfg, err := config.Load(nil, env(values))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":4000" || cfg.DataDir != "/tmp/env-data" || cfg.WebRoot != "/tmp/env-web" || cfg.ServerName != "env-name" {
		t.Fatalf("cfg = %+v", cfg)
	}

	overridden, err := config.Load([]string{
		"-addr", "127.0.0.1:5000",
		"-data-dir", "/tmp/flag-data",
		"-web-root", "/tmp/flag-web",
		"-name", "flag-name",
	}, env(values))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if overridden.Addr != "127.0.0.1:5000" || overridden.DataDir != "/tmp/flag-data" ||
		overridden.WebRoot != "/tmp/flag-web" || overridden.ServerName != "flag-name" {
		t.Fatalf("flags did not win: %+v", overridden)
	}
}

func TestLoadBarePortAndDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := config.Load([]string{"-addr", "3031"}, env(map[string]string{
		"NEX_SERVER_AUTH_TOKEN": "a",
		"NEX_RUNNER_TOKEN":      "r",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":3031" {
		t.Fatalf("Addr = %q, want :3031", cfg.Addr)
	}
	if cfg.WebRoot != config.DefaultWebRoot {
		t.Fatalf("WebRoot = %q", cfg.WebRoot)
	}
	if !strings.HasSuffix(cfg.DataDir, ".nex-next/server") {
		t.Fatalf("DataDir = %q, want the ~/.nex-next/server default", cfg.DataDir)
	}
	if cfg.ServerName == "" {
		t.Fatal("ServerName must default to the hostname")
	}
}

func TestAllowedOriginsParsing(t *testing.T) {
	cfg, err := config.Load(nil, env(map[string]string{
		"NEX_SERVER_AUTH_TOKEN": "a",
		"NEX_RUNNER_TOKEN":      "r",
		"NEX_ALLOWED_ORIGINS":   "nex.example, https://other.example:8443 ,,",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"nex.example", "https://other.example:8443"}
	if len(cfg.AllowedOrigins) != len(want) {
		t.Fatalf("AllowedOrigins = %v", cfg.AllowedOrigins)
	}
	for i := range want {
		if cfg.AllowedOrigins[i] != want[i] {
			t.Fatalf("AllowedOrigins = %v, want %v", cfg.AllowedOrigins, want)
		}
	}

	flagCfg, err := config.Load([]string{"-allowed-origins", "flag.example"}, env(map[string]string{
		"NEX_SERVER_AUTH_TOKEN": "a",
		"NEX_RUNNER_TOKEN":      "r",
		"NEX_ALLOWED_ORIGINS":   "env.example",
	}))
	if err != nil {
		t.Fatalf("Load with flag: %v", err)
	}
	if len(flagCfg.AllowedOrigins) != 1 || flagCfg.AllowedOrigins[0] != "flag.example" {
		t.Fatalf("flag AllowedOrigins = %v", flagCfg.AllowedOrigins)
	}
}
