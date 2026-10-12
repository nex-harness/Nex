// Package hostcap is the Go port of packages/server/src/hostCapability.ts: the short-lived,
// single-use capability a desktop trusted host presents on GET /ws/host.
//
// The store exists only in the HTTP server process's memory. Nothing is persisted, because a
// capability that outlives the process could not be checked against anything.
//
// The one rule that matters for security is reproduced verbatim from the TypeScript: the ticket
// is deleted on EVERY consume — success, expiry, unknown value or replay alike. Only the first
// consume inside the TTL is trusted. The legacy mode header was a replayable long-lived
// privilege claim, and this store exists to make the escalation to trusted-host relay a one-shot,
// time-boxed event.
package hostcap

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// DefaultTTL is DEFAULT_HOST_CAPABILITY_TTL_MS in packages/server/src/hostCapability.ts.
const DefaultTTL = 30 * time.Second

// CapabilityBytes is `randomBytes(32)` in the TypeScript; the string form is base64url, which is
// what `randomBytes(32).toString("base64url")` produces — 43 unpadded characters.
const CapabilityBytes = 32

// Capability is the body of POST /api/rpc-host-capability. The field names and JSON shape are
// the client's contract (packages/shared ServerRemoteHostCapability), so they are fixed here.
type Capability struct {
	Capability string `json:"capability"`
	ExpiresAt  int64  `json:"expiresAt"`
}

// Options mirrors HostCapabilityStoreOptions: every dependency is injectable so the store's
// clock and randomness can be driven deterministically by tests.
type Options struct {
	// TTL is how long an issued capability stays valid. Zero selects DefaultTTL, matching
	// `options.ttlMs ?? DEFAULT_HOST_CAPABILITY_TTL_MS`.
	TTL time.Duration
	// Now is the clock, in milliseconds since the Unix epoch (Date.now()). Zero selects
	// time.Now.
	Now func() int64
	// Generate returns a fresh capability string. Zero selects 32 random bytes in base64url.
	Generate func() string
}

// Store is the one-shot capability store. It is safe for concurrent use: the HTTP handler
// consumes from one goroutine per connection while the issuing endpoint runs on others, and the
// TypeScript relies on Node's single-threaded map for the same effect.
type Store struct {
	ttl       time.Duration
	now       func() int64
	generate  func() string
	mu        sync.Mutex
	expiresBy map[string]int64
}

// New builds a store. The zero Options is the production configuration.
func New(opts Options) *Store {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	now := opts.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	generate := opts.Generate
	if generate == nil {
		generate = RandomCapability
	}
	return &Store{ttl: ttl, now: now, generate: generate, expiresBy: map[string]int64{}}
}

// RandomCapability is the default generator: `randomBytes(32).toString("base64url")`. crypto/rand
// is the only acceptable source — a predictable capability would be a permanent bypass of the
// trusted-host guard.
//
// A failure of the system CSPRNG produces an empty string, which can never be issued as a
// capability by accident and always fails consume. Returning it (rather than panicking) keeps a
// transient entropy failure a 401 instead of a crash of the whole server.
func RandomCapability() string {
	buf := make([]byte, CapabilityBytes)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// Issue mints a capability and returns it with its absolute expiry, the body of
// POST /api/rpc-host-capability. Expired entries are purged first, so a burst of issues cannot
// accumulate dead tickets.
func (s *Store) Issue() Capability {
	issuedAt := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(issuedAt)

	capability := s.generate()
	expiresAt := issuedAt + s.ttl.Milliseconds()
	s.expiresBy[capability] = expiresAt
	return Capability{Capability: capability, ExpiresAt: expiresAt}
}

// Consume redeems a capability. It reports whether the presented value was a live ticket that
// had not been redeemed before.
//
// The order below is the TypeScript's and is load-bearing: the entry is removed BEFORE the
// expiry test, so an expired ticket is burned exactly like a valid one and a later replay finds
// nothing. `expiresAt > consumedAt` (strict) is what makes expiresAt == now an expiry, matching
// the store's `purgeExpired` boundary (`expiresAt <= at`).
func (s *Store) Consume(capability string) bool {
	if capability == "" {
		return false
	}
	consumedAt := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.expiresBy[capability]
	delete(s.expiresBy, capability)
	s.purgeExpiredLocked(consumedAt)
	return ok && expiresAt > consumedAt
}

// Len reports how many unexpired tickets the store holds. It exists for tests and for the
// purge assertions; no production code path needs it.
func (s *Store) Len() int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(now)
	return len(s.expiresBy)
}

// purgeExpiredLocked drops every ticket whose expiry has been reached. The caller holds s.mu.
func (s *Store) purgeExpiredLocked(at int64) {
	for capability, expiresAt := range s.expiresBy {
		if expiresAt <= at {
			delete(s.expiresBy, capability)
		}
	}
}
