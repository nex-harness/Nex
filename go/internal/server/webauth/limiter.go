package webauth

import (
	"sync"
	"time"
)

const (
	// maxLoginFailures is MAX_LOGIN_FAILURES: the n-th failure locks the address.
	maxLoginFailures = 5
	// loginLockDuration is LOGIN_LOCK_MS.
	loginLockDuration = 60_000 * time.Millisecond
	// limiterEntryTTL is LIMITER_ENTRY_TTL_MS: an unlocked entry idle for longer than this is
	// dropped, so the table cannot grow without bound.
	limiterEntryTTL = 10 * 60_000 * time.Millisecond
	// limiterMaxEntries is LIMITER_MAX_ENTRIES, the size at which stale entries are swept
	// before a new key is admitted.
	limiterMaxEntries = 1000
)

// loginRateLimiter ports `createLoginRateLimiter`: a per-key failure counter that locks the key
// for a minute once it reaches maxLoginFailures, with the counter cleared on success.
//
// State is in memory only (docs/specs/web-token-login.md) and is not shared with the Node
// server, so a restarted server re-opens its lockouts. The clock is injected so the table can be
// tested without sleeping.
type loginRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*limiterEntry
	now     func() time.Time
}

type limiterEntry struct {
	failures      int
	lockedUntil   time.Time
	lastFailureAt time.Time
}

// newLoginRateLimiter returns a limiter driven by now. A nil clock means wall time.
func newLoginRateLimiter(now func() time.Time) *loginRateLimiter {
	if now == nil {
		now = time.Now
	}
	return &loginRateLimiter{entries: map[string]*limiterEntry{}, now: now}
}

// lockedFor ports `lockedForMs`: the remaining lock time in milliseconds, 0 when not locked.
func (l *loginRateLimiter) lockedFor(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry == nil {
		return 0
	}
	remaining := entry.lockedUntil.Sub(l.now())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// recordFailure ports `recordFailure`. An entry whose lock has expired restarts from zero
// failures, so a client that waits out the lock is not immediately locked again by its next
// mistake — and one failure never re-locks on its own.
func (l *loginRateLimiter) recordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) >= limiterMaxEntries {
		l.dropStaleLocked()
	}
	current := l.now()
	entry, ok := l.entries[key]
	if !ok {
		entry = &limiterEntry{lastFailureAt: current}
		l.entries[key] = entry
	}
	if !entry.lockedUntil.IsZero() && !entry.lockedUntil.After(current) {
		entry.failures = 0
		entry.lockedUntil = time.Time{}
	}
	entry.failures++
	entry.lastFailureAt = current
	if entry.failures >= maxLoginFailures {
		entry.lockedUntil = current.Add(loginLockDuration)
	}
}

// recordSuccess ports `recordSuccess`: a correct token clears the key's counter entirely.
func (l *loginRateLimiter) recordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// dropStaleLocked ports `dropStale`: unlocked entries idle beyond limiterEntryTTL are dropped.
// Caller holds the lock.
func (l *loginRateLimiter) dropStaleLocked() {
	current := l.now()
	for key, entry := range l.entries {
		if !entry.lockedUntil.After(current) && current.Sub(entry.lastFailureAt) > limiterEntryTTL {
			delete(l.entries, key)
		}
	}
}
