package broadcast

import (
	"context"
	"testing"
	"time"
)

func recvOne(t *testing.T, ch <-chan Message, what string) Message {
	t.Helper()
	select {
	case msg, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed", what)
		}
		return msg
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: timed out", what)
		return Message{}
	}
}

func recvNone(t *testing.T, ch <-chan Message, what string) {
	t.Helper()
	select {
	case msg, ok := <-ch:
		if ok {
			t.Fatalf("%s: unexpected message %+v (no-echo violated)", what, msg)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

// TestBroadcastNoEcho is THE step-3.1 no-echo test: two subscribers; one
// publishes; the publisher must NOT receive its own message back through the
// hub, the other must. This is the AGENTS.md loop-prevention rule
// (广播不得回声给发送源) that keeps theme/language fan-out from looping.
func TestBroadcastNoEcho(t *testing.T) {
	hub := NewHub()
	a := NewService(hub)
	defer a.Close()
	b := NewService(hub)
	defer b.Close()

	ctx := context.Background()
	if err := a.Send(ctx, Message{Channel: "state:theme", Payload: "dark"}); err != nil {
		t.Fatal(err)
	}

	// A's own local emitter DOES fire (the TS send() notifies its own renderer
	// via emitter.fire — that is the intended local echo, not a loop). The
	// HUB leg must not deliver anything back to A on top of that.
	local := recvOne(t, a.Messages(), "publisher local emitter")
	if local.Channel != "state:theme" || local.Payload != "dark" {
		t.Fatalf("local echo payload mismatch: %+v", local)
	}

	// B receives exactly once, stamped with A's subscriber id.
	got := recvOne(t, b.Messages(), "other subscriber")
	if got.Channel != "state:theme" || got.Payload != "dark" {
		t.Fatalf("delivered payload mismatch: %+v", got)
	}
	if got.SourceSubscriber != a.ID() {
		t.Fatalf("sourceSubscriber = %q, want publisher %q", got.SourceSubscriber, a.ID())
	}
	recvNone(t, b.Messages(), "other subscriber (duplicate)")

	// The hub leg delivered nothing back to A: after the local echo, A's
	// stream must be silent.
	recvNone(t, a.Messages(), "publisher hub echo")
}

// TestBroadcastThreeSubscribers extends the no-echo rule to N subscribers.
func TestBroadcastThreeSubscribers(t *testing.T) {
	hub := NewHub()
	publisher := NewService(hub)
	defer publisher.Close()
	others := make([]*Service, 3)
	for i := range others {
		others[i] = NewService(hub)
		defer others[i].Close()
	}
	if err := publisher.Send(context.Background(), Message{Channel: "state:locale", Payload: "zh-CN"}); err != nil {
		t.Fatal(err)
	}
	// Consume the publisher's own local echo first.
	recvOne(t, publisher.Messages(), "publisher local")
	for i, o := range others {
		got := recvOne(t, o.Messages(), "subscriber")
		if got.SourceSubscriber != publisher.ID() {
			t.Errorf("subscriber %d: sourceSubscriber = %q, want %q", i, got.SourceSubscriber, publisher.ID())
		}
	}
	recvNone(t, publisher.Messages(), "publisher hub echo")
}

// TestBroadcastDetachedService pins the parentPort==null case: a service with
// no hub only reaches its own local listeners.
func TestBroadcastDetachedService(t *testing.T) {
	svc := NewService(nil)
	defer svc.Close()
	if svc.ID() != "" {
		t.Fatalf("detached service must have no subscriber id, got %q", svc.ID())
	}
	if err := svc.Send(context.Background(), Message{Channel: "state:theme", Payload: "light"}); err != nil {
		t.Fatal(err)
	}
	recvOne(t, svc.Messages(), "local listener")
	recvNone(t, svc.Messages(), "no second delivery")
}

// TestBroadcastSendValidation pins the zod broadcastMessageSchema rule: a
// non-empty channel is required.
func TestBroadcastSendValidation(t *testing.T) {
	svc := NewService(nil)
	defer svc.Close()
	if err := svc.Send(context.Background(), Message{Channel: "  ", Payload: nil}); err == nil {
		t.Fatal("empty channel must be rejected")
	}
}

// TestClaimLifecycle pins acquire/commit/release/tryClaim and the TTL.
func TestClaimLifecycle(t *testing.T) {
	hub := NewHub()
	a := NewService(hub)
	defer a.Close()
	b := NewService(hub)
	defer b.Close()
	ctx := context.Background()

	// First acquire wins.
	res, err := a.AcquireClaim(ctx, "onboarding:done")
	if err != nil || res.Status != ClaimAcquired || res.Lease == nil {
		t.Fatalf("acquire: %+v, %v", res, err)
	}
	// Second caller on the same key is busy.
	busy, _ := b.AcquireClaim(ctx, "onboarding:done")
	if busy.Status != ClaimBusy || busy.RetryAfterMs <= 0 || busy.RetryAfterMs > int64(claimRetryMs/time.Millisecond) {
		t.Fatalf("busy: %+v", busy)
	}
	// tryClaim on a busy key is false.
	ok, _ := b.TryClaim(ctx, "onboarding:done")
	if ok {
		t.Fatal("tryClaim must fail while reserved by another subscriber")
	}
	// Commit makes it permanent.
	if err := a.CommitClaim(ctx, *res.Lease); err != nil {
		t.Fatal(err)
	}
	committed, _ := b.AcquireClaim(ctx, "onboarding:done")
	if committed.Status != ClaimCommitted {
		t.Fatalf("after commit: %+v", committed)
	}
	ok, _ = b.TryClaim(ctx, "onboarding:done")
	if ok {
		t.Fatal("tryClaim must fail after commit")
	}

	// A second key: acquire then release, then another can take it.
	res2, _ := b.AcquireClaim(ctx, "tips:shown")
	if res2.Status != ClaimAcquired {
		t.Fatalf("res2: %+v", res2)
	}
	if err := b.ReleaseClaim(ctx, *res2.Lease); err != nil {
		t.Fatal(err)
	}
	after, _ := a.AcquireClaim(ctx, "tips:shown")
	if after.Status != ClaimAcquired {
		t.Fatalf("after release: %+v", after)
	}

	// Empty key is unavailable.
	empty, _ := a.AcquireClaim(ctx, "   ")
	if empty.Status != ClaimUnavailable {
		t.Fatalf("empty key: %+v", empty)
	}
}

// TestClaimExpiry pins the reservation TTL: an un-committed reservation frees
// the key after claimReservationTTL.
func TestClaimExpiry(t *testing.T) {
	hub := NewHub()
	current := time.Now()
	hub.now = func() time.Time { return current }
	a := NewService(hub)
	defer a.Close()
	b := NewService(hub)
	defer b.Close()
	ctx := context.Background()

	res, _ := a.AcquireClaim(ctx, "k")
	if res.Status != ClaimAcquired {
		t.Fatalf("acquire: %+v", res)
	}
	// Advance past the TTL.
	current = current.Add(claimReservationTTL + time.Second)
	next, _ := b.AcquireClaim(ctx, "k")
	if next.Status != ClaimAcquired {
		t.Fatalf("expired reservation must free the key: %+v", next)
	}
}

// TestUnregisterReclaimsReservations ports the hub's unregister rule: reserved
// claims die with the subscriber, committed claims survive.
func TestUnregisterReclaimsReservations(t *testing.T) {
	hub := NewHub()
	a := NewService(hub)
	b := NewService(hub)
	ctx := context.Background()

	reserved, _ := a.AcquireClaim(ctx, "reserved-key")
	if reserved.Status != ClaimAcquired {
		t.Fatalf("reserved: %+v", reserved)
	}
	committed, _ := a.AcquireClaim(ctx, "committed-key")
	if committed.Status != ClaimAcquired {
		t.Fatalf("committed: %+v", committed)
	}
	if err := a.CommitClaim(ctx, *committed.Lease); err != nil {
		t.Fatal(err)
	}
	a.Close()

	afterReserved, _ := b.AcquireClaim(ctx, "reserved-key")
	if afterReserved.Status != ClaimAcquired {
		t.Fatalf("uncommitted reservation must be reclaimed on unregister: %+v", afterReserved)
	}
	afterCommitted, _ := b.AcquireClaim(ctx, "committed-key")
	if afterCommitted.Status != ClaimCommitted {
		t.Fatalf("committed claim must survive unregister: %+v", afterCommitted)
	}
	b.Close()
}

// TestClaimTokenMismatch pins the token guard: a release/commit with the wrong
// token must not touch the record.
func TestClaimTokenMismatch(t *testing.T) {
	hub := NewHub()
	a := NewService(hub)
	defer a.Close()
	ctx := context.Background()
	res, _ := a.AcquireClaim(ctx, "k")
	if res.Status != ClaimAcquired {
		t.Fatalf("acquire: %+v", res)
	}
	forged := ClaimLease{Key: "k", Token: "not-the-token"}
	if err := a.ReleaseClaim(ctx, forged); err != nil {
		t.Fatal(err)
	}
	busy, _ := a.AcquireClaim(ctx, "k")
	if busy.Status != ClaimBusy {
		t.Fatalf("forged release must not free the key: %+v", busy)
	}
}
