package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/server/internal/rpcwire"
)

const (
	broadcastClaimReservationTTL = 5 * time.Second
	broadcastClaimRetry          = 250 * time.Millisecond
	maxBroadcastClaims           = 1024
)

// BroadcastMessage is the wire shape of one cross-window broadcast
// (packages/services/src/broadcast/broadcast.ts:9-17).
type BroadcastMessage struct {
	Channel        string          `json:"channel"`
	Payload        json.RawMessage `json:"payload"`
	SourceWindowID *int64          `json:"sourceWindowId,omitempty"`
}

type claimRecord struct {
	token     string
	status    string // "reserved" | "committed"
	expiresAt time.Time
}

// Broadcast implements the `broadcast` channel. It mirrors the TypeScript
// server-mode implementation (createBroadcastService with parentPort=null):
// send fans a message out to every subscribed browser connection, and opaque
// claims are process-local reservations.
type Broadcast struct {
	mu        sync.Mutex
	listeners map[uint64]func(rpcwire.Value)
	nextID    uint64
	claims    map[string]*claimRecord
}

// NewBroadcast returns an empty broadcast channel.
func NewBroadcast() *Broadcast {
	return &Broadcast{
		listeners: make(map[uint64]func(rpcwire.Value)),
		claims:    make(map[string]*claimRecord),
	}
}

func newClaimToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("local-%d", time.Now().UnixNano())
	}
	return "local-" + hex.EncodeToString(raw[:])
}

func (b *Broadcast) pruneLocked(now time.Time) {
	for key, record := range b.claims {
		if record.status == "reserved" && !record.expiresAt.IsZero() && !record.expiresAt.After(now) {
			delete(b.claims, key)
		}
	}
	for len(b.claims) > maxBroadcastClaims {
		for key := range b.claims {
			delete(b.claims, key)
			break
		}
	}
}

func (b *Broadcast) acquire(key string) map[string]any {
	key = strings.TrimSpace(key)
	if key == "" {
		return map[string]any{"status": "unavailable"}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.pruneLocked(now)
	if existing, ok := b.claims[key]; ok {
		if existing.status == "committed" {
			return map[string]any{"status": "committed"}
		}
		retry := existing.expiresAt.Sub(now)
		if retry < 0 {
			retry = 0
		}
		if retry > broadcastClaimRetry {
			retry = broadcastClaimRetry
		}
		return map[string]any{"status": "busy", "retryAfterMs": retry.Milliseconds()}
	}
	token := newClaimToken()
	b.claims[key] = &claimRecord{
		token:     token,
		status:    "reserved",
		expiresAt: now.Add(broadcastClaimReservationTTL),
	}
	return map[string]any{
		"status": "acquired",
		"lease":  map[string]any{"key": key, "token": token},
	}
}

func (b *Broadcast) commit(lease map[string]json.RawMessage) {
	var key, token string
	_ = json.Unmarshal(lease["key"], &key)
	_ = json.Unmarshal(lease["token"], &token)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(time.Now())
	if current, ok := b.claims[key]; ok && current.status == "reserved" && current.token == token {
		current.status = "committed"
	}
}

func (b *Broadcast) release(lease map[string]json.RawMessage) {
	var key, token string
	_ = json.Unmarshal(lease["key"], &key)
	_ = json.Unmarshal(lease["token"], &token)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(time.Now())
	if current, ok := b.claims[key]; ok && current.status == "reserved" && current.token == token {
		delete(b.claims, key)
	}
}

func (b *Broadcast) fanOut(message BroadcastMessage) {
	b.mu.Lock()
	listeners := make([]func(rpcwire.Value), 0, len(b.listeners))
	for _, emit := range b.listeners {
		listeners = append(listeners, emit)
	}
	b.mu.Unlock()

	encoded, err := json.Marshal(message)
	if err != nil {
		return
	}
	for _, emit := range listeners {
		emit(json.RawMessage(encoded))
	}
}

// Call implements Channel.
func (b *Broadcast) Call(_ context.Context, _ *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	switch method {
	case "send":
		object, err := decodeObject(arg)
		if err != nil {
			return nil, err
		}
		raw, ok := object["channel"]
		if !ok {
			return nil, errors.New("channel is required")
		}
		var channel string
		if err := json.Unmarshal(raw, &channel); err != nil || strings.TrimSpace(channel) == "" {
			return nil, errors.New("channel must be a non-empty string")
		}
		message := BroadcastMessage{Channel: channel, Payload: json.RawMessage("null")}
		if payload, ok := object["payload"]; ok {
			message.Payload = payload
		}
		if source, ok := object["sourceWindowId"]; ok {
			var sourceID int64
			if err := json.Unmarshal(source, &sourceID); err == nil {
				message.SourceWindowID = &sourceID
			}
		}
		b.fanOut(message)
		return rpcwire.Undefined{}, nil

	case "acquireClaim":
		return b.acquire(claimKey(arg)), nil

	case "commitClaim":
		lease, err := decodeObject(arg)
		if err != nil {
			return nil, err
		}
		b.commit(lease)
		return rpcwire.Undefined{}, nil

	case "releaseClaim":
		lease, err := decodeObject(arg)
		if err != nil {
			return nil, err
		}
		b.release(lease)
		return rpcwire.Undefined{}, nil

	case "tryClaim":
		key := claimKey(arg)
		if key == "" {
			return false, nil
		}
		result := b.acquire(key)
		if result["status"] != "acquired" {
			return false, nil
		}
		lease := result["lease"].(map[string]any)
		b.commit(map[string]json.RawMessage{
			"key":   mustJSON(lease["key"]),
			"token": mustJSON(lease["token"]),
		})
		return true, nil
	}
	return nil, &MethodNotFoundError{Method: method}
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}

// claimKey accepts both shapes used by the channel: the declarative signature
// is `acquireClaim(key)` (positional string, so the body is `["key"]`), while
// hand-written clients send `{"key": "..."}`.
func claimKey(arg rpcwire.Value) string {
	if key := stringArgument(arg); key != "" {
		return key
	}
	return strings.TrimSpace(stringField(arg, "key"))
}

// Listen implements Channel. The only event is onMessage; unknown events are
// an empty event per the MVP contract.
func (b *Broadcast) Listen(_ context.Context, _ *Env, event string, _ rpcwire.Value, emit func(rpcwire.Value)) (func(), error) {
	if event != "onMessage" {
		return nil, nil
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.listeners[id] = emit
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.listeners, id)
		b.mu.Unlock()
	}, nil
}
