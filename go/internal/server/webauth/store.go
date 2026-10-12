package webauth

import (
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/serverenv"
	"github.com/tylinux/nex/go/internal/adapters/webauthfs"
)

// storedSession is StoredSession in webAuth.ts: the session id itself never reaches the disk,
// only its SHA-256, so a leaked file cannot be replayed as a cookie.
type storedSession struct {
	IDHash    string `json:"idHash"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// sessionsFile is the on-disk shape `{version:1, tokenFingerprint, sessions:[...]}`.
//
// Field order matches the TypeScript object literal (version, tokenFingerprint, sessions) so the
// JSON a Node process reads back is structurally identical, and the `indent = 2` plus trailing
// newline of `JSON.stringify(payload, null, 2)` is reproduced byte for byte.
type sessionsFile struct {
	Version          int             `json:"version"`
	TokenFingerprint string          `json:"tokenFingerprint"`
	Sessions         []storedSession `json:"sessions"`
}

// SessionStore ports `createWebSessionStore`: the single owner of web-sessions.json.
//
// Concurrency story (the plan asks for a decision here). The TypeScript serializes its writes
// with a promise chain and treats its in-memory slice as the authority. That does not port,
// because during the strangler transition the Node upstream writes the same file: a Go-only
// in-memory authority would silently drop whatever Node logged in. So the file is the authority
// here — every operation re-reads it under the store lock, applies its change on top and writes
// back atomically. That costs one file read per request that carries a session cookie, and it
// keeps the two implementations correct against each other. The lock is what makes concurrent Go
// requests correct; the atomic rename is what makes a concurrent Node write safe (it wins or
// loses wholesale, never interleaves). A session minted by Node is therefore visible to Go on
// the very next request.
type SessionStore struct {
	mu          sync.Mutex
	filePath    string
	fingerprint string
	now         func() time.Time
	// memory holds the sessions for a memory-only store, and the last good read for a
	// file-backed one (so a transient read error does not require re-authentication).
	memory []storedSession
	// onWarn receives the non-fatal diagnostics the TypeScript logs: an unreadable file and a
	// failed logout persist. Login persist failures are returned instead, because a session that
	// was not written must not be handed out.
	onWarn func(message string, err error)
}

// NewSessionStore returns the store for token. An empty filePath keeps it memory-only, which is
// what the TypeScript does when sessionsFilePath is unset. A nil clock means wall time; a nil
// onWarn drops diagnostics.
func NewSessionStore(token, filePath string, now func() time.Time, onWarn func(string, error)) *SessionStore {
	if now == nil {
		now = time.Now
	}
	return &SessionStore{
		filePath:    filePath,
		fingerprint: tokenFingerprint(token),
		now:         now,
		onWarn:      onWarn,
	}
}

// SessionsFileName is the fixed file name under the app config dir, as documented in
// docs/specs/web-token-login.md. It is not configurable on the Go side: Node derives the same
// path from getAppConfigDir(), and a second name would split the two implementations' state.
const SessionsFileName = "web-sessions.json"

// DefaultSessionsFilePath reproduces `join(getAppConfigDir(), "web-sessions.json")` from
// packages/services/src/paths.ts: <data base>/.nex/v2/web-sessions.json, where the data base is
// NEX_DATA_BASE_DIR or $HOME. The legacy .zcode copy the TypeScript performs on first resolution
// is a migration nicety for a directory Go never writes to, and is not reproduced here.
func DefaultSessionsFilePath() string {
	base := serverenv.Trimmed("NEX_DATA_BASE_DIR")
	if base == "" {
		base = serverenv.HomeDir()
	}
	return filepath.Join(base, ".nex", "v2", SessionsFileName)
}

// Create ports `create()`: mint a session id, append its hash and persist. The id is returned
// only after the write succeeded, so a login never hands out a session that dies on restart.
// A write failure removes the entry again and surfaces the error (the route answers 500).
func (s *SessionStore) Create() (string, error) {
	id, err := newSessionID()
	if err != nil {
		return "", err
	}
	createdAt := s.now().UnixMilli()
	entry := storedSession{
		IDHash:    sha256Hex(id),
		CreatedAt: createdAt,
		ExpiresAt: createdAt + SessionTTL,
	}
	if err := s.mutate(func(sessions *[]storedSession) error {
		*sessions = append(*sessions, entry)
		return nil
	}); err != nil {
		return "", err
	}
	return id, nil
}

// Validate ports `validate()`: a live session with this exact id hash, or false. An empty id is
// false without touching the file.
func (s *SessionStore) Validate(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	return s.has(sessionID)
}

// Revoke ports `revoke()`: drop the session and persist. A failed write is logged and swallowed
// (docs/specs/web-token-login.md: logout is not blocked by a persist failure), so the session
// only survives in the file until it expires or the file is rewritten.
func (s *SessionStore) Revoke(sessionID string) {
	if sessionID == "" {
		return
	}
	idHash := sha256Hex(sessionID)
	if err := s.mutate(func(sessions *[]storedSession) error {
		kept := make([]storedSession, 0, len(*sessions))
		for _, session := range *sessions {
			if session.IDHash != idHash {
				kept = append(kept, session)
			}
		}
		*sessions = kept
		return nil
	}); err != nil {
		s.warn("failed to persist logout", err)
	}
}

// mutate applies change to the session list and writes the result back when the store is
// file-backed, re-reading the file first so a Node-side write is never clobbered. It returns the
// first error; callers decide whether it is fatal. A memory-only store (no file path, as in the
// TypeScript when sessionsFilePath is unset) just applies the change in memory.
func (s *SessionStore) mutate(change func(*[]storedSession) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.currentLocked()
	if err != nil {
		return err
	}
	if err := change(&sessions); err != nil {
		return err
	}
	sessions = s.prune(sessions)
	s.memory = sessions
	if s.filePath == "" {
		return nil
	}
	return s.persist(sessions)
}

// currentLocked returns the sessions the store works from: the file's contents when it is
// file-backed (re-read on every call, so a write by the Node server is picked up rather than
// clobbered), or the in-memory list for a memory-only store. Caller holds the lock.
func (s *SessionStore) currentLocked() ([]storedSession, error) {
	if s.filePath == "" {
		return s.memory, nil
	}
	sessions, err := s.load()
	if err != nil {
		// An unreadable *file* (permissions, a directory in its place) is not the same as an
		// empty one: load() already fails closed to "no sessions" for a missing or corrupt
		// file, so this is a genuine I/O failure and is surfaced rather than silently treated as
		// "log everyone out".
		s.warn("sessions file unreadable", err)
		return nil, err
	}
	s.memory = sessions
	return sessions, nil
}

// has reports whether a live session with this id hash exists.
func (s *SessionStore) has(sessionID string) bool {
	idHash := sha256Hex(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.now().UnixMilli()
	sessions, err := s.currentLocked()
	if err != nil {
		return false
	}
	for _, session := range sessions {
		if session.IDHash == idHash && session.ExpiresAt > current {
			return true
		}
	}
	return false
}

// load reads and validates the file, then prunes. A missing or corrupt file yields no sessions —
// fail closed, never "auth off" (docs/specs/web-token-login.md). Only a genuine I/O error is
// returned, and currentLocked turns that into a rejected mutation / an unauthenticated request.
//
// The version and the token fingerprint must both match: a rotated token invalidates every
// stored session, which is exactly the "change NEX_SERVER_AUTH_TOKEN and restart" rule in the
// spec's acceptance list.
func (s *SessionStore) load() ([]storedSession, error) {
	data, ok, err := webauthfs.ReadFile(s.filePath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	var parsed sessionsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, nil
	}
	if parsed.Version != sessionsFileVersion || parsed.TokenFingerprint != s.fingerprint {
		return nil, nil
	}
	sessions := make([]storedSession, 0, len(parsed.Sessions))
	for _, session := range parsed.Sessions {
		if session.IDHash == "" {
			continue
		}
		sessions = append(sessions, session)
	}
	return s.prune(sessions), nil
}

// prune ports `prune`: drop expired sessions, then keep only the newest maxStoredSessions by
// creation time. Both run on load and before every write, matching the TypeScript.
//
// The sort must be stable and descending on createdAt, exactly like
// `sessions.sort((l, r) => r.createdAt - l.createdAt).slice(0, 100)`, because two sessions can
// share a millisecond (two logins in the same tick) and the tie order decides which survives.
func (s *SessionStore) prune(sessions []storedSession) []storedSession {
	current := s.now().UnixMilli()
	kept := make([]storedSession, 0, len(sessions))
	for _, session := range sessions {
		if session.ExpiresAt > current {
			kept = append(kept, session)
		}
	}
	if len(kept) <= maxStoredSessions {
		return kept
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return kept[i].CreatedAt > kept[j].CreatedAt
	})
	return kept[:maxStoredSessions]
}

// persist writes the session list atomically with mode 0600.
func (s *SessionStore) persist(sessions []storedSession) error {
	if sessions == nil {
		sessions = []storedSession{}
	}
	encoded, err := json.MarshalIndent(sessionsFile{
		Version:          sessionsFileVersion,
		TokenFingerprint: s.fingerprint,
		Sessions:         sessions,
	}, "", "  ")
	if err != nil {
		return err
	}
	// JSON.stringify(payload, null, 2) + "\n".
	return webauthfs.WriteAtomic(s.filePath, append(encoded, '\n'))
}

func (s *SessionStore) warn(message string, err error) {
	if s.onWarn != nil {
		s.onWarn(message, err)
	}
}

// sha256Hex ports `sha256Hex`. The hex encoding is lowercase, like Node's digest("hex").
func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hexLower(sum[:])
}
