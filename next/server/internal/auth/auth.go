// Package auth implements the browser login used by the web client: token
// login, persistent hashed sessions and per-IP login rate limiting.
//
// Behaviour is a port of packages/server/src/webAuth.ts.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SessionCookieName is the cookie that carries the browser session id.
const SessionCookieName = "nex_session"

// SessionTTL is the lifetime of a login session (30 days).
const SessionTTL = 30 * 24 * time.Hour

// maxStoredSessions bounds the persisted session list.
const maxStoredSessions = 100

const sessionsFileVersion = 1

// TokensEqual compares two secrets without leaking their length or content
// through timing: both are hashed first, then compared in constant time.
func TokensEqual(expected, provided string) bool {
	a := sha256.Sum256([]byte(expected))
	b := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func tokenFingerprint(token string) string {
	return sha256Hex("nex-web-session:" + token)
}

type storedSession struct {
	IDHash    string `json:"idHash"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

type sessionsFile struct {
	Version          int             `json:"version"`
	TokenFingerprint string          `json:"tokenFingerprint"`
	Sessions         []storedSession `json:"sessions"`
}

// Store persists hashed browser sessions in a JSON file.
//
// Sessions are keyed by sha256(session id); the raw id never touches disk. A
// token change (detected through the stored fingerprint) discards every
// session, so logins issued for a rotated token cannot survive a restart.
type Store struct {
	token  string
	path   string
	logger *slog.Logger
	now    func() time.Time

	mu       sync.Mutex
	loaded   bool
	sessions []storedSession
}

// NewStore creates a session store. An empty path keeps sessions in memory
// only (tests). A nil logger falls back to a discard logger.
func NewStore(token, path string, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return &Store{
		token:  token,
		path:   path,
		logger: logger,
		now:    time.Now,
	}
}

// newSessionID returns a 256-bit random, URL-safe session identifier.
func newSessionID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generating session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (s *Store) expiry(now time.Time) int64 {
	return now.Add(SessionTTL).UnixMilli()
}

// loadLocked reads the persisted file once. Corrupt or missing files are
// treated as "no sessions" (fail closed for persistence, never for auth).
func (s *Store) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logger.Warn("web session file unreadable, starting empty", "error", err)
		}
		return
	}
	var file sessionsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		s.logger.Warn("web session file is not valid JSON, starting empty", "error", err)
		return
	}
	if file.Version != sessionsFileVersion || file.TokenFingerprint != tokenFingerprint(s.token) {
		// Rotated token or unknown schema: drop everything.
		return
	}
	s.sessions = append([]storedSession(nil), file.Sessions...)
	s.pruneLocked()
}

func (s *Store) pruneLocked() {
	now := s.now().UnixMilli()
	kept := s.sessions[:0]
	for _, session := range s.sessions {
		if session.ExpiresAt > now {
			kept = append(kept, session)
		}
	}
	s.sessions = kept
	if len(s.sessions) > maxStoredSessions {
		// Newest first, then truncate; sort matches webAuth.ts ordering by
		// createdAt descending.
		sortByCreatedDesc(s.sessions)
		s.sessions = s.sessions[:maxStoredSessions]
	}
}

func sortByCreatedDesc(sessions []storedSession) {
	for i := 1; i < len(sessions); i++ {
		current := sessions[i]
		j := i - 1
		for j >= 0 && sessions[j].CreatedAt < current.CreatedAt {
			sessions[j+1] = sessions[j]
			j--
		}
		sessions[j+1] = current
	}
}

// persistLocked atomically writes the current in-memory state.
func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	payload := sessionsFile{
		Version:          sessionsFileVersion,
		TokenFingerprint: tokenFingerprint(s.token),
		Sessions:         s.sessions,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding sessions: %w", err)
	}
	encoded = append(encoded, '\n')

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating session directory: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return fmt.Errorf("writing session file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing session file: %w", err)
	}
	return nil
}

// Create mints a new session and returns its raw id. When persistence fails
// the session is rolled back so a restart cannot silently log the user out.
func (s *Store) Create() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()

	sessionID, err := newSessionID()
	if err != nil {
		return "", err
	}
	now := s.now()
	entry := storedSession{
		IDHash:    sha256Hex(sessionID),
		CreatedAt: now.UnixMilli(),
		ExpiresAt: s.expiry(now),
	}
	s.sessions = append(s.sessions, entry)
	s.pruneLocked()
	if err := s.persistLocked(); err != nil {
		s.sessions = removeSession(s.sessions, entry.IDHash)
		return "", err
	}
	return sessionID, nil
}

func removeSession(sessions []storedSession, idHash string) []storedSession {
	out := sessions[:0]
	for _, session := range sessions {
		if session.IDHash != idHash {
			out = append(out, session)
		}
	}
	return out
}

// Validate reports whether sessionID maps to a live session.
func (s *Store) Validate(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	hash := sha256Hex(sessionID)
	now := s.now().UnixMilli()
	for _, session := range s.sessions {
		if session.IDHash == hash && session.ExpiresAt > now {
			return true
		}
	}
	return false
}

// Revoke deletes one session. Unknown ids are a no-op.
func (s *Store) Revoke(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	hash := sha256Hex(sessionID)
	before := len(s.sessions)
	s.sessions = removeSession(s.sessions, hash)
	if len(s.sessions) == before {
		return
	}
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("failed to persist logout", "error", err)
	}
}

// LimiterEntry tracks login failures for one client key.
type limiterEntry struct {
	failures      int
	lockedUntil   time.Time
	lastFailureAt time.Time
}

// RateLimiter allows five failures per key, then locks the key for 60 seconds.
type RateLimiter struct {
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]*limiterEntry
}

const (
	maxLoginFailures   = 5
	loginLockDuration  = 60 * time.Second
	limiterEntryTTL    = 10 * time.Minute
	limiterMaxEntrties = 1000
)

// NewRateLimiter returns an empty limiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{now: time.Now, entries: make(map[string]*limiterEntry)}
}

// LockedFor returns how long key remains locked (zero when unlocked).
func (l *RateLimiter) LockedFor(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok {
		return 0
	}
	remaining := entry.lockedUntil.Sub(l.now())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RecordFailure increments the failure count and locks the key at five.
func (l *RateLimiter) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) >= limiterMaxEntrties {
		l.dropStaleLocked()
		// Every entry can still be fresh (an attacker cycling source addresses),
		// so dropping only stale keys is not a bound. Evict an unlocked entry
		// to keep the map bounded.
		for len(l.entries) >= limiterMaxEntrties {
			evicted := false
			now := l.now()
			for candidate, entry := range l.entries {
				if !entry.lockedUntil.After(now) {
					delete(l.entries, candidate)
					evicted = true
					break
				}
			}
			if !evicted {
				// All keys are locked; drop an arbitrary one (the lock is
				// advisory and time-bounded).
				for candidate := range l.entries {
					delete(l.entries, candidate)
					break
				}
			}
		}
	}
	now := l.now()
	entry := l.entries[key]
	if entry == nil {
		entry = &limiterEntry{}
		l.entries[key] = entry
	}
	// After a lock expires the count restarts, so a single new failure does not
	// immediately re-lock.
	if !entry.lockedUntil.IsZero() && !entry.lockedUntil.After(now) {
		entry.failures = 0
		entry.lockedUntil = time.Time{}
	}
	entry.failures++
	entry.lastFailureAt = now
	if entry.failures >= maxLoginFailures {
		entry.lockedUntil = now.Add(loginLockDuration)
	}
}

// RecordSuccess clears any failure state for key.
func (l *RateLimiter) RecordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *RateLimiter) dropStaleLocked() {
	now := l.now()
	for key, entry := range l.entries {
		if !entry.lockedUntil.After(now) && now.Sub(entry.lastFailureAt) > limiterEntryTTL {
			delete(l.entries, key)
		}
	}
}

// Service bundles the token, session store and login limiter.
type Service struct {
	// Token is the configured login token; empty disables authentication.
	Token string
	Store *Store
	limit *RateLimiter
}

// NewService builds the auth service. An empty token disables auth entirely,
// matching webAuth.ts's `enabled = Boolean(token)`.
func NewService(token, sessionsPath string, logger *slog.Logger) *Service {
	token = strings.TrimSpace(token)
	service := &Service{Token: token, limit: NewRateLimiter()}
	if token != "" {
		service.Store = NewStore(token, sessionsPath, logger)
	}
	return service
}

// Enabled reports whether a token was configured.
func (s *Service) Enabled() bool { return s.Token != "" }

// ValidateToken compares a submitted token in constant time.
func (s *Service) ValidateToken(provided string) bool {
	return s.Token != "" && TokensEqual(s.Token, provided)
}

// ValidateSession reports whether the raw session id is live.
func (s *Service) ValidateSession(sessionID string) bool {
	if s.Store == nil {
		return true
	}
	return s.Store.Validate(sessionID)
}

// CreateSession mints a new session id.
func (s *Service) CreateSession() (string, error) {
	if s.Store == nil {
		return "", errors.New("authentication is not enabled")
	}
	return s.Store.Create()
}

// RevokeSession deletes one session id.
func (s *Service) RevokeSession(sessionID string) {
	if s.Store != nil {
		s.Store.Revoke(sessionID)
	}
}

// Limiter exposes the login rate limiter.
func (s *Service) Limiter() *RateLimiter { return s.limit }
