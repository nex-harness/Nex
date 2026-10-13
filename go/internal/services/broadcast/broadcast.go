// Package broadcast ports packages/services/src/broadcast/* (step 3.1):
// BroadcastMessage/IBroadcastService (broadcast.ts) plus the two runtime halves
// of the TS design:
//
//   - Service (broadcastService.ts): what a host process exposes on the
//     `broadcast` channel. send() fans out to local listeners and the hub.
//   - Hub (packages/desktop/src/main/broadcastHub.ts): the main-process relay
//     that forwards a message to every OTHER host — the loop-prevention owner.
//
// AGENTS.md 的回环规则在这里是硬约束：广播不得回声给发送源。在 TS 中 Main 的
// BroadcastHub.relay 跳过 sourceWindowId，Renderer 端 IntlProvider 再用
// sourceWindowId === undefined 判断"本地回声"。Go 的 Hub 在同一处实现同一规则：
// relay 永不把消息发回 publisher，并且给消息盖上 publisher 的 sourceWindowId，
// 接收端可以再据此去重。
package broadcast

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ChannelName is ServiceChannels.Broadcast ("broadcast").
const ChannelName = "broadcast"

// Claim timing constants, verbatim from the TS (broadcastService.ts /
// broadcastHub.ts).
const (
	claimTimeoutMs      = 2_000
	claimReservationTTL = 5 * time.Second
	claimRetryMs        = 250 * time.Millisecond
	maxClaims           = 1_024
)

// Message ports BroadcastMessage: a channel name (e.g. "state:theme",
// "state:locale"), an opaque payload, and the sender's subscriber id stamped by
// the hub so receivers can skip their own echoes.
type Message struct {
	Channel          string `json:"channel"`
	Payload          any    `json:"payload"`
	SourceSubscriber string `json:"sourceSubscriber,omitempty"`
}

// ClaimLease ports BroadcastClaimLease.
type ClaimLease struct {
	Key   string `json:"key"`
	Token string `json:"token"`
}

// ClaimAcquireStatus is the discriminated union BroadcastClaimAcquireResult.
type ClaimAcquireStatus string

const (
	ClaimAcquired    ClaimAcquireStatus = "acquired"
	ClaimBusy        ClaimAcquireStatus = "busy"
	ClaimCommitted   ClaimAcquireStatus = "committed"
	ClaimUnavailable ClaimAcquireStatus = "unavailable"
)

// ClaimAcquireResult ports BroadcastClaimAcquireResult.
type ClaimAcquireResult struct {
	Status       ClaimAcquireStatus `json:"status"`
	Lease        *ClaimLease        `json:"lease,omitempty"`
	RetryAfterMs int64              `json:"retryAfterMs,omitempty"`
}

// claimRecord is one hub-side claim, ported from BroadcastClaimRecord.
type claimRecord struct {
	token     string
	owner     string // subscriber id (the TS ownerWindowId)
	status    string // "reserved" | "committed"
	expiresAt time.Time
	committed bool
}

// Hub is the cross-subscriber relay (the TS BroadcastHub). It is the ONLY owner
// of the no-echo rule: relay skips the publishing subscriber and stamps the
// message with that subscriber's id.
//
// A nil *Hub is valid and means "no cross-window capability" — the same role a
// null parentPort plays in createBroadcastService (web server mode).
type Hub struct {
	mu       sync.Mutex
	subs     map[string]*subscription
	claims   map[string]*claimRecord
	now      func() time.Time
	newToken func() string
}

// subscription is one registered receiver.
type subscription struct {
	id string
	ch chan Message
}

// NewHub builds the relay. now/newToken are injectable for tests.
func NewHub() *Hub {
	return &Hub{
		subs:     map[string]*subscription{},
		claims:   map[string]*claimRecord{},
		now:      time.Now,
		newToken: randomClaimToken,
	}
}

// randomClaimToken mirrors createLocalClaimToken's shape: `local-<uuid>`.
func randomClaimToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("local-%d", time.Now().UnixNano())
	}
	return "local-" + hex.EncodeToString(b)
}

// Subscribe registers a receiver and returns its channel plus a cancel func.
// The channel is buffered (64) and drops-oldest: a slow receiver must never
// block the publisher — the rpc.Channel contract in internal/rpc requires a
// producer that cannot park forever.
func (h *Hub) Subscribe(ctx context.Context) (<-chan Message, func()) {
	h.mu.Lock()
	id := randomClaimToken()
	sub := &subscription{id: id, ch: make(chan Message, 64)}
	h.subs[id] = sub
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		if s, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(s.ch)
		}
		h.mu.Unlock()
	}
	go func() {
		select {
		case <-ctx.Done():
			unsubscribe()
		}
	}()
	return sub.ch, unsubscribe
}

// Relay fans a message from publisherID out to every OTHER subscriber.
//
// THE NO-ECHO RULE (AGENTS.md: 广播不得回声给发送源): the publisher itself is
// skipped here, unconditionally — a client that publishes a theme change must
// never have its own broadcast delivered back, or the theme/language sync loops.
// The message carries sourceSubscriber so a receiver can tell who sent it.
func (h *Hub) Relay(publisherID string, msg Message) {
	msg.SourceSubscriber = publisherID
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subs {
		if sub.id == publisherID {
			continue
		}
		select {
		case sub.ch <- msg:
		default:
			// Slow receiver: drop the oldest queued message and retry once, so a
			// lagging window eventually catches up rather than silently missing
			// every later broadcast.
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- msg:
			default:
			}
		}
	}
}

// AcquireClaim ports the hub's handleClaim: first-wins reservation with a TTL.
func (h *Hub) AcquireClaim(subscriberID, key string) ClaimAcquireResult {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		return ClaimAcquireResult{Status: ClaimUnavailable}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.pruneExpiredClaimsLocked(now)

	existing, ok := h.claims[normalized]
	if ok && existing.committed {
		return ClaimAcquireResult{Status: ClaimCommitted}
	}
	if ok {
		retry := int64(claimRetryMs / time.Millisecond)
		if remaining := existing.expiresAt.Sub(now).Milliseconds(); remaining < retry {
			retry = max64(0, remaining)
		}
		return ClaimAcquireResult{Status: ClaimBusy, RetryAfterMs: retry}
	}
	token := h.newToken()
	h.claims[normalized] = &claimRecord{
		token: token, owner: subscriberID, status: "reserved",
		expiresAt: now.Add(claimReservationTTL),
	}
	h.pruneExpiredClaimsLocked(now)
	return ClaimAcquireResult{Status: ClaimAcquired, Lease: &ClaimLease{Key: normalized, Token: token}}
}

// CommitClaim ports handleClaimCommit: a reservation becomes permanent for the
// process lifetime, matched on owner AND token.
func (h *Hub) CommitClaim(subscriberID string, lease ClaimLease) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneExpiredClaimsLocked(h.now())
	current, ok := h.claims[lease.Key]
	if ok && !current.committed && current.owner == subscriberID && current.token == lease.Token {
		current.committed = true
		current.status = "committed"
		current.expiresAt = time.Time{}
	}
}

// ReleaseClaim ports handleClaimRelease: a matching reservation is dropped.
func (h *Hub) ReleaseClaim(subscriberID string, lease ClaimLease) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneExpiredClaimsLocked(h.now())
	current, ok := h.claims[lease.Key]
	if ok && !current.committed && current.owner == subscriberID && current.token == lease.Token {
		delete(h.claims, lease.Key)
	}
}

// Unregister drops a subscriber and reclaims its un-committed reservations
// (handleClaimRelease's unregister path: committed claims survive so a reopened
// window does not replay a one-time completion).
func (h *Hub) Unregister(subscriberID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, subscriberID)
	for key, claim := range h.claims {
		if claim.owner == subscriberID && !claim.committed {
			delete(h.claims, key)
		}
	}
}

// pruneExpiredClaimsLocked ports pruneExpiredReservations plus the MAX cap.
func (h *Hub) pruneExpiredClaimsLocked(now time.Time) {
	for key, claim := range h.claims {
		if !claim.committed && !claim.expiresAt.IsZero() && !claim.expiresAt.After(now) {
			delete(h.claims, key)
		}
	}
	for len(h.claims) > maxClaims {
		var oldest string
		for key := range h.claims {
			if oldest == "" || key < oldest {
				oldest = key
			}
		}
		delete(h.claims, oldest)
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Service is the per-host IBroadcastService port. Each subscriber gets its own
// Service view over the shared Hub, exactly like one host process's
// createBroadcastService(parentPort) instance.
type Service struct {
	hub          *Hub
	subscriberID string
	local        chan Message
	closeOnce    sync.Once
}

// NewService subscribes to the hub and returns the service bound to that
// subscription. A nil hub means no cross-window capability: sends reach only
// local listeners (the TS parentPort === null case).
func NewService(hub *Hub) *Service {
	svc := &Service{hub: hub, local: make(chan Message, 64)}
	if hub != nil {
		hub.mu.Lock()
		id := randomClaimToken()
		svc.subscriberID = id
		hub.subs[id] = &subscription{id: id, ch: svc.local}
		hub.mu.Unlock()
	}
	return svc
}

// ID returns the subscriber id ("" when not hub-attached).
func (s *Service) ID() string { return s.subscriberID }

// Send ports send(): 1) local listeners, 2) the hub for cross-window relay.
// The hub leg applies the no-echo rule on OUR behalf (our own subscription is
// skipped), so a local echo here is the ONLY way this sender sees the message —
// matching the TS, where emitter.fire() is the sender's own window notification.
func (s *Service) Send(ctx context.Context, msg Message) error {
	msg.Channel = strings.TrimSpace(msg.Channel)
	if msg.Channel == "" {
		return fmt.Errorf("broadcast: channel must be a non-empty string")
	}
	// 1. Local emitter (this host's own renderer).
	select {
	case s.local <- msg:
	default:
	}
	// 2. Cross-window relay; the hub stamps our id and skips us.
	if s.hub != nil {
		s.hub.Relay(s.subscriberID, msg)
	}
	return nil
}

// Messages exposes the local emitter stream (the TS onMessage event).
func (s *Service) Messages() <-chan Message { return s.local }

// AcquireClaim ports acquireClaim against the hub (or locally when detached).
func (s *Service) AcquireClaim(ctx context.Context, key string) (ClaimAcquireResult, error) {
	if s.hub == nil {
		return ClaimAcquireResult{Status: ClaimUnavailable}, nil
	}
	return s.hub.AcquireClaim(s.subscriberID, key), nil
}

// CommitClaim ports commitClaim.
func (s *Service) CommitClaim(ctx context.Context, lease ClaimLease) error {
	if s.hub == nil {
		return nil
	}
	s.hub.CommitClaim(s.subscriberID, lease)
	return nil
}

// ReleaseClaim ports releaseClaim.
func (s *Service) ReleaseClaim(ctx context.Context, lease ClaimLease) error {
	if s.hub == nil {
		return nil
	}
	s.hub.ReleaseClaim(s.subscriberID, lease)
	return nil
}

// TryClaim ports tryClaim: acquire + commit, true only for the first caller.
func (s *Service) TryClaim(ctx context.Context, key string) (bool, error) {
	result, err := s.AcquireClaim(ctx, key)
	if err != nil {
		return false, err
	}
	if result.Status != ClaimAcquired || result.Lease == nil {
		return false, nil
	}
	if err := s.CommitClaim(ctx, *result.Lease); err != nil {
		return false, err
	}
	return true, nil
}

// Close unsubscribes from the hub (the socket.onClose disposal).
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		if s.hub != nil {
			s.hub.Unregister(s.subscriberID)
		}
		close(s.local)
	})
}
