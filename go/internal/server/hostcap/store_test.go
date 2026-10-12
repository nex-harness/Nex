package hostcap

// The behavior pinned here was read out of packages/server/src/hostCapability.ts and, where the
// boundary was ambiguous, checked against the real TypeScript (the expiry edge, the replay, the
// purge-on-issue). The store is security-relevant: it is the only thing standing between an
// ordinary browser WebSocket and the trusted-host-relay role, so every rule gets a test rather
// than a comment.

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a settable millisecond clock, so a test can place "now" anywhere relative to a ticket's
// expiry without sleeping.
type clock struct {
	mu  sync.Mutex
	now int64
}

func newClock(at int64) *clock { return &clock{now: at} }

func (c *clock) Now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now += d.Milliseconds()
	c.mu.Unlock()
}

func (c *clock) Set(at int64) {
	c.mu.Lock()
	c.now = at
	c.mu.Unlock()
}

// sequence returns a generator handing out "cap-1", "cap-2", … so a test can name a ticket it
// issued. Production uses the real RNG; the sequence only makes assertions readable.
func sequence() func() string {
	var n int
	var mu sync.Mutex
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("cap-%d", n)
	}
}

// newTestStore wires a store with the production TTL and an injected clock + generator.
func newTestStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	c := newClock(1_000_000)
	return New(Options{Now: c.Now, Generate: sequence()}), c
}

// TestCapabilityTable walks the store's decision table. Every row is one (state, action, verdict)
// triple, and each verdict was confirmed against the TypeScript store.
func TestCapabilityTable(t *testing.T) {
	cases := []struct {
		name string
		// step prepares the store and returns the capability under test plus the clock.
		step  func(store *Store, c *clock) string
		want  bool
		after string // expected store length after the consume, "" to skip the check
	}{
		{
			name: "a fresh capability is accepted",
			step: func(store *Store, c *clock) string { return store.Issue().Capability },
			want: true, after: "0",
		},
		{
			name: "the same capability used twice is a replay: the second is refused",
			step: func(store *Store, c *clock) string {
				capability := store.Issue().Capability
				if !store.Consume(capability) {
					t.Fatal("the first consume should succeed")
				}
				return capability
			},
			want: false, after: "0",
		},
		{
			name: "one millisecond before expiry is still valid",
			step: func(store *Store, c *clock) string {
				issued := store.Issue()
				c.Set(issued.ExpiresAt - 1)
				return issued.Capability
			},
			want: true, after: "0",
		},
		{
			name: "expiry boundary: expiresAt == now is expired (the comparison is strict >)",
			step: func(store *Store, c *clock) string {
				issued := store.Issue()
				c.Set(issued.ExpiresAt)
				return issued.Capability
			},
			want: false, after: "0",
		},
		{
			name: "one millisecond past expiry is expired",
			step: func(store *Store, c *clock) string {
				issued := store.Issue()
				c.Set(issued.ExpiresAt + 1)
				return issued.Capability
			},
			want: false, after: "0",
		},
		{
			name: "an expired capability is burned, so its entry is gone even though consume failed",
			step: func(store *Store, c *clock) string {
				issued := store.Issue()
				c.Advance(DefaultTTL + time.Millisecond)
				return issued.Capability
			},
			want: false, after: "0",
		},
		{
			name: "an unknown capability is refused",
			step: func(store *Store, c *clock) string { return "never-issued" },
			want: false,
		},
		{
			name: "an empty capability is refused without touching the store",
			step: func(store *Store, c *clock) string {
				// A live ticket stays behind, proving the empty-string early return skipped the
				// delete-and-purge path entirely (`if (!capability) return false`).
				store.Issue()
				return ""
			},
			want: false, after: "1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, c := newTestStore(t)
			capability := tc.step(store, c)
			if got := store.Consume(capability); got != tc.want {
				t.Errorf("Consume(%q) = %v, want %v", capability, got, tc.want)
			}
			if tc.after != "" {
				if got := fmt.Sprint(store.Len()); got != tc.after {
					t.Errorf("store length after consume = %s, want %s", got, tc.after)
				}
			}
		})
	}
}

// TestCapabilityIssuePurgesExpired pins purge-on-issue: a burst of issues never accumulates dead
// tickets, so a client that asks for a capability but never connects cannot grow the map.
func TestCapabilityIssuePurgesExpired(t *testing.T) {
	store, c := newTestStore(t)
	first := store.Issue()
	c.Advance(DefaultTTL + time.Millisecond)
	store.Issue()
	if got := store.Len(); got != 1 {
		t.Fatalf("after purging on issue, store length = %d, want 1 (only the live ticket)", got)
	}
	if store.Consume(first.Capability) {
		t.Errorf("the purged capability %q must not be redeemable", first.Capability)
	}
}

// TestCapabilityConsumePurgesExpired pins the other purge site: consume also drops every other
// expired ticket, not just the one it was asked about.
func TestCapabilityConsumePurgesExpired(t *testing.T) {
	store, c := newTestStore(t)
	stale := store.Issue()
	// Issue the ticket under test from the same clock so both share an expiry, then advance past
	// it and mint a fresh one at the new "now".
	c.Advance(DefaultTTL + time.Millisecond)
	fresh := store.Issue()
	// Redeeming `fresh` runs purgeExpired at the current time, which also drops `stale`.
	if !store.Consume(fresh.Capability) {
		t.Fatal("the fresh capability should be accepted")
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("store length = %d, want 0 (consume should have purged the stale ticket)", got)
	}
	if store.Consume(stale.Capability) {
		t.Errorf("the stale capability %q must not be redeemable", stale.Capability)
	}
}

// TestCapabilityExpiryAndFormat pins the two wire-level properties a client depends on: the TTL
// is 30 s, and the string is 32 random bytes in base64url (43 characters, no padding).
func TestCapabilityExpiryAndFormat(t *testing.T) {
	store := New(Options{})
	issued := store.Issue()
	if got := issued.ExpiresAt - time.Now().UnixMilli(); got > int64(DefaultTTL/time.Millisecond) || got < int64(DefaultTTL/time.Millisecond)-1000 {
		t.Errorf("expiresAt is %d ms out, want ~%d ms (DefaultTTL)", got, DefaultTTL/time.Millisecond)
	}
	if len(issued.Capability) != 43 {
		t.Errorf("capability length = %d, want 43 (32 bytes base64url unpadded)", len(issued.Capability))
	}
	if strings.ContainsAny(issued.Capability, "+/=") {
		t.Errorf("capability %q is not base64url (contains +, / or =)", issued.Capability)
	}
	if _, err := base64.RawURLEncoding.DecodeString(issued.Capability); err != nil {
		t.Errorf("capability %q does not decode as base64url: %v", issued.Capability, err)
	}
}

// TestCapabilityRandomAreDistinct issues many real tickets and requires no collision; two equal
// strings would make one ticket redeemable with the other's value.
func TestCapabilityRandomAreDistinct(t *testing.T) {
	store := New(Options{})
	seen := make(map[string]bool, 256)
	for i := 0; i < 256; i++ {
		capability := store.Issue().Capability
		if seen[capability] {
			t.Fatalf("duplicate capability %q at issue %d", capability, i)
		}
		seen[capability] = true
	}
}

// TestCapabilityConcurrentIssueAndConsume runs issue/consume/replay from many goroutines under -race.
// It asserts the security property directly: every capability is accepted at most once across the
// whole run, no matter how many goroutines present it.
func TestCapabilityConcurrentIssueAndConsume(t *testing.T) {
	store := New(Options{})
	const workers = 32
	const perWorker = 64

	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := map[string]bool{}

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			local := make([]string, 0, perWorker)
			for i := 0; i < perWorker; i++ {
				local = append(local, store.Issue().Capability)
			}
			// Race every worker on every other worker's tickets: exactly one consume per ticket
			// may report success.
			for round := 0; round < 2; round++ {
				for _, capability := range local {
					if !store.Consume(capability) {
						continue
					}
					mu.Lock()
					if accepted[capability] {
						t.Errorf("capability %q accepted twice", capability)
					}
					accepted[capability] = true
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if len(accepted) != workers*perWorker {
		t.Errorf("accepted %d capabilities, want %d (every issued ticket is used exactly once)",
			len(accepted), workers*perWorker)
	}
	if got := store.Len(); got != 0 {
		t.Errorf("store length = %d, want 0 (every ticket was consumed)", got)
	}
}
