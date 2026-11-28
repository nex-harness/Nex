package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/remote"
)

const (
	webAuthKVKey     = "web_auth_config"
	webAuthCookie    = "nex_web_auth"
	pbkdf2Iterations = 100_000
	tokenTTL         = 30 * 24 * time.Hour
	maxAuthTokens    = 20
)

type webAuthToken struct {
	Token     string `json:"token"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

type webAuthConfig struct {
	PasswordHash string         `json:"password_hash"`
	Tokens       []webAuthToken `json:"tokens,omitempty"`
}

// pbkdf2SHA256 implements RFC 2898 PBKDF2-HMAC-SHA256 using only Go stdlib (zero external deps).
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha256.Size
	numBlocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, numBlocks*hashLen)
	var blockBuf [4]byte
	for block := 1; block <= numBlocks; block++ {
		blockBuf[0] = byte(block >> 24)
		blockBuf[1] = byte(block >> 16)
		blockBuf[2] = byte(block >> 8)
		blockBuf[3] = byte(block)

		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write(blockBuf[:])
		u := mac.Sum(nil)
		out := make([]byte, len(u))
		copy(out, u)

		for i := 1; i < iter; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(u[:0])
			for j := range out {
				out[j] ^= u[j]
			}
		}
		dk = append(dk, out...)
	}
	return dk[:keyLen]
}

func hashWebPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, 32)
	return fmt.Sprintf("pbkdf2$sha256$%d$%s$%s", pbkdf2Iterations, hex.EncodeToString(salt), hex.EncodeToString(dk)), nil
}

func verifyWebPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha256" {
		return false
	}
	salt, err := hex.DecodeString(parts[3])
	if err != nil || len(salt) == 0 {
		return false
	}
	expected, err := hex.DecodeString(parts[4])
	if err != nil || len(expected) == 0 {
		return false
	}
	var iter int
	if _, err := fmt.Sscanf(parts[2], "%d", &iter); err != nil || iter < 1000 || iter > 1_000_000 {
		return false
	}
	actual := pbkdf2SHA256([]byte(password), salt, iter, len(expected))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func generateAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) loadWebAuthConfig(ctx context.Context) webAuthConfig {
	raw, err := s.store.GetKVSetting(ctx, webAuthKVKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return webAuthConfig{}
	}
	var cfg webAuthConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return webAuthConfig{}
	}
	return cfg
}

func (s *Server) saveWebAuthConfig(ctx context.Context, cfg webAuthConfig) error {
	now := time.Now().UnixMilli()
	valid := make([]webAuthToken, 0, len(cfg.Tokens))
	for _, t := range cfg.Tokens {
		if t.Token != "" && t.ExpiresAt > now {
			valid = append(valid, t)
		}
	}
	if len(valid) > maxAuthTokens {
		valid = valid[len(valid)-maxAuthTokens:]
	}
	cfg.Tokens = valid
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.store.SetKVSetting(ctx, webAuthKVKey, string(b))
}

func extractRequestToken(r *http.Request) string {
	if c, err := r.Cookie(webAuthCookie); err == nil && strings.TrimSpace(c.Value) != "" {
		return strings.TrimSpace(c.Value)
	}
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	if qTok := strings.TrimSpace(r.URL.Query().Get("token")); qTok != "" {
		return qTok
	}
	return ""
}

func extractDeviceToken(r *http.Request) string {
	if tok := strings.TrimSpace(r.Header.Get("X-Nex-Device-Token")); tok != "" {
		return tok
	}
	return strings.TrimSpace(r.URL.Query().Get("device_token"))
}

func sha256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Server) isRequestAuthenticated(ctx context.Context, r *http.Request) (enabled bool, ok bool) {
	if peer, hasPeer := remote.PeerFromContext(ctx); hasPeer {
		if !peer.PairingOnly && peer.DeviceID != "" {
			if dev, err := s.store.GetPairedDeviceByFP(ctx, peer.PubKeyFP); err == nil && dev != nil {
				cfg := s.loadWebAuthConfig(ctx)
				return strings.TrimSpace(cfg.PasswordHash) != "", true
			}
		}
		return true, false
	}

	if devTok := extractDeviceToken(r); devTok != "" {
		if dev, err := s.store.GetPairedDeviceByTokenHash(ctx, sha256Hex(devTok)); err == nil && dev != nil {
			_ = s.store.TouchPairedDevice(ctx, dev.ID, "")
			cfg := s.loadWebAuthConfig(ctx)
			return strings.TrimSpace(cfg.PasswordHash) != "", true
		}
		// Explicitly reject unknown or revoked paired-device credentials even if Web Password is off
		return true, false
	}

	cfg := s.loadWebAuthConfig(ctx)
	if strings.TrimSpace(cfg.PasswordHash) == "" {
		return false, true
	}
	tok := extractRequestToken(r)
	if tok == "" {
		return true, false
	}
	now := time.Now().UnixMilli()
	for _, entry := range cfg.Tokens {
		if entry.ExpiresAt > now && subtle.ConstantTimeCompare([]byte(entry.Token), []byte(tok)) == 1 {
			return true, true
		}
	}
	return true, false
}

func setAuthCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	isHTTPS := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     webAuthCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// Static SPA assets, health check, runner reverse WS, pairing exchange, and auth bootstrap endpoints remain accessible
		if p == "/api/v1/health" ||
			p == "/api/v1/auth/status" ||
			p == "/api/v1/auth/login" ||
			p == "/api/v1/remote/pair/complete" ||
			p == "/api/v1/ssh/binary" ||
			p == "/api/v1/runners/install.ps1" ||
			p == "/api/v1/runners/install.sh" ||
			strings.HasPrefix(p, "/api/v1/runners/binary/") ||
			p == "/ws/runner" ||
			strings.HasPrefix(p, "/llm-proxy/") ||
			(!strings.HasPrefix(p, "/api/") && p != "/ws/client") {
			next.ServeHTTP(w, r)
			return
		}

		enabled, ok := s.isRequestAuthenticated(r.Context(), r)
		if enabled && !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":         "authentication required",
				"auth_required": true,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	enabled, ok := s.isRequestAuthenticated(r.Context(), r)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       enabled,
		"authenticated": ok,
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.loadWebAuthConfig(r.Context())
	if strings.TrimSpace(cfg.PasswordHash) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": false})
		return
	}
	if !verifyWebPassword(body.Password, cfg.PasswordHash) {
		writeErr(w, http.StatusUnauthorized, errors.New("invalid password"))
		return
	}

	tok, err := generateAuthToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now()
	cfg.Tokens = append(cfg.Tokens, webAuthToken{
		Token:     tok,
		CreatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(tokenTTL).UnixMilli(),
	})
	if err := s.saveWebAuthConfig(r.Context(), cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	setAuthCookie(w, r, tok, int(tokenTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"enabled":       true,
		"authenticated": true,
		"token":         tok,
	})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	tok := extractRequestToken(r)
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.loadWebAuthConfig(r.Context())
	if tok != "" && len(cfg.Tokens) > 0 {
		remaining := make([]webAuthToken, 0, len(cfg.Tokens))
		for _, t := range cfg.Tokens {
			if subtle.ConstantTimeCompare([]byte(t.Token), []byte(tok)) != 1 {
				remaining = append(remaining, t)
			}
		}
		cfg.Tokens = remaining
		_ = s.saveWebAuthConfig(r.Context(), cfg)
	}
	setAuthCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.loadWebAuthConfig(r.Context())
	if strings.TrimSpace(cfg.PasswordHash) != "" {
		if !verifyWebPassword(body.CurrentPassword, cfg.PasswordHash) {
			writeErr(w, http.StatusUnauthorized, errors.New("current password is incorrect"))
			return
		}
	}

	newPwd := body.NewPassword
	if newPwd == "" {
		// Disable password protection
		cfg.PasswordHash = ""
		cfg.Tokens = nil
		if err := s.saveWebAuthConfig(r.Context(), cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		setAuthCookie(w, r, "", -1)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"enabled":       false,
			"authenticated": true,
		})
		return
	}

	if len(newPwd) < 4 {
		writeErr(w, http.StatusBadRequest, errors.New("password must be at least 4 characters"))
		return
	}

	hash, err := hashWebPassword(newPwd)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	tok, err := generateAuthToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now()
	cfg.PasswordHash = hash
	cfg.Tokens = []webAuthToken{{
		Token:     tok,
		CreatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(tokenTTL).UnixMilli(),
	}}
	if err := s.saveWebAuthConfig(r.Context(), cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	setAuthCookie(w, r, tok, int(tokenTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"enabled":       true,
		"authenticated": true,
		"token":         tok,
	})
}
