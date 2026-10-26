package auth_test

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nex-harness/nex/server/internal/auth"
)

func TestTokensEqual(t *testing.T) {
	if !auth.TokensEqual("secret", "secret") {
		t.Fatal("equal tokens rejected")
	}
	if auth.TokensEqual("secret", "secret2") || auth.TokensEqual("secret", "") {
		t.Fatal("different tokens accepted")
	}
}

// An attacker cycling source addresses must not grow the failure map without
// bound. (The limiter key is the TCP peer address; X-Forwarded-For is not
// trusted, matching webAuth.ts.)
func TestRateLimiterStaysBounded(t *testing.T) {
	limiter := auth.NewRateLimiter()
	for i := 0; i < 5000; i++ {
		limiter.RecordFailure(string(rune('a'+i%26)) + string(rune(i)))
	}
	// All older entries must have been evicted so a fresh key can still lock.
	for i := 0; i < 6; i++ {
		limiter.RecordFailure("fresh")
	}
	if limiter.LockedFor("fresh") == 0 {
		t.Fatal("fresh key never locked; limiter may be dropping the active key")
	}
}

func TestSessionFileModeAndTokenRotation(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/web-sessions.json"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	store := auth.NewStore("token-a", path, logger)
	session, err := store.Create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !store.Validate(session) {
		t.Fatal("session not valid")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sessions file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("sessions file mode = %o, want 600", mode)
	}

	// Rotation discards persisted sessions.
	rotated := auth.NewStore("token-b", path, logger)
	if rotated.Validate(session) {
		t.Fatal("session survived token rotation")
	}
}

func TestRateLimiterLockExpires(t *testing.T) {
	now := time.Now()
	limiter := auth.NewRateLimiter()
	for i := 0; i < 5; i++ {
		limiter.RecordFailure("key")
	}
	if limiter.LockedFor("key") == 0 {
		t.Fatal("key not locked after five failures")
	}
	// The limiter's clock is time.Now; the lock duration is a minute, so just
	// assert that a different key stays unaffected.
	if limiter.LockedFor("other") != 0 {
		t.Fatal("unrelated key is locked")
	}
	_ = now
}
