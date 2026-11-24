package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const runnerAuthKVKey = "runner_auth_config"

type runnerAuthConfig struct {
	PairingSecret string `json:"pairing_secret"`
	RequireAuth   bool   `json:"require_auth"`
	RotatedAt     int64  `json:"rotated_at"`
}

func generateRunnerPairingSecret() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Server) loadOrInitRunnerAuthConfig(ctx context.Context) (runnerAuthConfig, bool, error) {
	if envSec := strings.TrimSpace(s.cfg.PairingSecret); envSec != "" {
		return runnerAuthConfig{
			PairingSecret: envSec,
			RequireAuth:   true,
		}, true, nil
	}
	raw, err := s.store.GetKVSetting(ctx, runnerAuthKVKey)
	if err == nil && strings.TrimSpace(raw) != "" {
		var cfg runnerAuthConfig
		if json.Unmarshal([]byte(raw), &cfg) == nil && strings.TrimSpace(cfg.PairingSecret) != "" {
			return cfg, false, nil
		}
	}
	sec, err := generateRunnerPairingSecret()
	if err != nil {
		return runnerAuthConfig{}, false, err
	}
	// Default RequireAuth to true only when web auth is enabled; for fresh unauthenticated
	// local/dev servers, keep require_auth=false until explicitly enabled (or NEX_PAIRING_SECRET is set)
	// so local dev & tests work out-of-the-box, while public servers with web password auth are protected by default.
	webAuthCfg := s.loadWebAuthConfig(ctx)
	defaultRequireAuth := webAuthCfg.PasswordHash != ""
	cfg := runnerAuthConfig{
		PairingSecret: sec,
		RequireAuth:   defaultRequireAuth,
		RotatedAt:     time.Now().UnixMilli(),
	}
	b, _ := json.Marshal(cfg)
	_ = s.store.SetKVSetting(ctx, runnerAuthKVKey, string(b))
	return cfg, false, nil
}

func (s *Server) saveRunnerAuthConfig(ctx context.Context, cfg runnerAuthConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.store.SetKVSetting(ctx, runnerAuthKVKey, string(b))
}

// effectiveRunnerPairingSecret returns the active pairing secret (env or persisted) and whether auth is required.
func (s *Server) effectiveRunnerPairingSecret(ctx context.Context) (secret string, requireAuth bool) {
	cfg, _, err := s.loadOrInitRunnerAuthConfig(ctx)
	if err != nil {
		return strings.TrimSpace(s.cfg.PairingSecret), strings.TrimSpace(s.cfg.PairingSecret) != ""
	}
	return cfg.PairingSecret, cfg.RequireAuth
}

// isDirectLocalHostRunnerRequest returns true ONLY when a runner connects directly over
// loopback (127.0.0.1 / ::1) with no reverse-proxy forwarding headers AND advertises
// the exact same OS hostname as this nex-server process. This lets a co-located local
// runner (e.g. systemd nex-v3-runner alongside nex-v3-server) stay connected even when
// pairing auth is enabled, without weakening auth for remote or proxied connections.
func isDirectLocalHostRunnerRequest(r *http.Request, reportedHostname string) bool {
	if r == nil {
		return false
	}
	if strings.TrimSpace(r.Header.Get("X-Forwarded-For")) != "" ||
		strings.TrimSpace(r.Header.Get("X-Real-IP")) != "" ||
		strings.TrimSpace(r.Header.Get("Forwarded")) != "" ||
		strings.TrimSpace(r.Header.Get("CF-Connecting-IP")) != "" {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	serverHost, err := os.Hostname()
	if err != nil || strings.TrimSpace(serverHost) == "" {
		return false
	}
	rep := strings.TrimSpace(reportedHostname)
	return rep != "" && strings.EqualFold(rep, strings.TrimSpace(serverHost))
}

// verifyRunnerAuthToken validates a runner handshake/download token against:
// 1. effective pairing secret (env or persisted)
// 2. existing registered runner's individual AuthToken
// 3. paired_devices device token (for runners paired via nex://pair + nex-relay)
func (s *Server) verifyRunnerAuthToken(ctx context.Context, runnerID, tok string) bool {
	tok = strings.TrimSpace(tok)
	secret, requireAuth := s.effectiveRunnerPairingSecret(ctx)
	if !requireAuth {
		return true
	}
	if tok == "" {
		return false
	}
	if secret != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) == 1 {
		return true
	}
	if runnerID != "" {
		if existing, err := s.store.GetRunner(ctx, runnerID); err == nil && existing != nil && existing.AuthToken != "" {
			if subtle.ConstantTimeCompare([]byte(tok), []byte(existing.AuthToken)) == 1 {
				return true
			}
		}
	}
	if dev, err := s.store.GetPairedDeviceByTokenHash(ctx, sha256Hex(tok)); err == nil && dev != nil && dev.RevokedAt == 0 {
		return true
	}
	return false
}

func (s *Server) handleRunnersAuth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		cfg, envManaged, err := s.loadOrInitRunnerAuthConfig(ctx)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"pairing_secret": cfg.PairingSecret,
			"require_auth":   cfg.RequireAuth,
			"env_managed":    envManaged,
			"rotated_at":     cfg.RotatedAt,
		})
	case http.MethodPut:
		cfg, envManaged, err := s.loadOrInitRunnerAuthConfig(ctx)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if envManaged {
			writeErr(w, http.StatusBadRequest, errors.New("runner auth is managed by NEX_PAIRING_SECRET environment variable"))
			return
		}
		var body struct {
			RequireAuth *bool `json:"require_auth"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if body.RequireAuth != nil {
			cfg.RequireAuth = *body.RequireAuth
		}
		if err := s.saveRunnerAuthConfig(ctx, cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"pairing_secret": cfg.PairingSecret,
			"require_auth":   cfg.RequireAuth,
			"env_managed":    false,
			"rotated_at":     cfg.RotatedAt,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRunnersAuthRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	cfg, envManaged, err := s.loadOrInitRunnerAuthConfig(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if envManaged {
		writeErr(w, http.StatusBadRequest, errors.New("cannot rotate pairing secret when managed by NEX_PAIRING_SECRET"))
		return
	}
	sec, err := generateRunnerPairingSecret()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	cfg.PairingSecret = sec
	cfg.RotatedAt = time.Now().UnixMilli()
	if err := s.saveRunnerAuthConfig(ctx, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pairing_secret": cfg.PairingSecret,
		"require_auth":   cfg.RequireAuth,
		"env_managed":    false,
		"rotated_at":     cfg.RotatedAt,
	})
}
