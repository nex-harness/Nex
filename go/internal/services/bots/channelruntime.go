// channelruntime.go ports packages/services/src/bots/channelRuntime.ts (the
// cross-provider runtime helpers) and the Telegram polling loop of
// telegramChannelRuntime.ts.
//
// The Feishu WebSocket client is Phase-4 scope (it needs the Go agent's
// long-connection plumbing); the weixin loop is the same shape as the telegram
// one and shares this file's cursor discipline.
package bots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"
)

const (
	// BotRuntimeLockLeaseMillis is BOT_RUNTIME_LOCK_LEASE_MS.
	BotRuntimeLockLeaseMillis = 30_000
	// BotRuntimeLockRetryMillis is BOT_RUNTIME_LOCK_RETRY_MS.
	BotRuntimeLockRetryMillis = 10_000
)

// CreateBotConnectionFingerprint ports createBotConnectionFingerprint: a sha256
// over the JSON array of parts. Two windows sharing a token get the same
// fingerprint, which is what makes the polling lock meaningful.
func CreateBotConnectionFingerprint(parts []string) string {
	encoded, _ := json.Marshal(parts)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// RuntimeStatusSink is BotRuntimeStatusSink.
type RuntimeStatusSink interface {
	GetRuntimeStatus(botID string) *RuntimeInfo
	SetRuntimeStatus(status RuntimeInfo)
}

// RuntimeStatusStore is the in-memory sink the service owns. It is safe for
// concurrent use: the polling loops and the callback path both write.
type RuntimeStatusStore struct {
	mu      sync.RWMutex
	byBotID map[string]RuntimeInfo
}

// NewRuntimeStatusStore builds an empty store.
func NewRuntimeStatusStore() *RuntimeStatusStore {
	return &RuntimeStatusStore{byBotID: map[string]RuntimeInfo{}}
}

// GetRuntimeStatus ports getRuntimeStatus.
func (s *RuntimeStatusStore) GetRuntimeStatus(botID string) *RuntimeInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, ok := s.byBotID[botID]
	if !ok {
		return nil
	}
	return &status
}

// SetRuntimeStatus ports setRuntimeStatus.
func (s *RuntimeStatusStore) SetRuntimeStatus(status RuntimeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byBotID == nil {
		s.byBotID = map[string]RuntimeInfo{}
	}
	s.byBotID[status.BotID] = status
}

// All returns every known runtime status (the getStatus() payload).
func (s *RuntimeStatusStore) All() []RuntimeInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]RuntimeInfo, 0, len(s.byBotID))
	for _, status := range s.byBotID {
		result = append(result, status)
	}
	return result
}

// ErrCallbackFailed is the error assertBotCallbackSucceeded throws. It is
// exported because the polling loop's contract is "a failed callback throws,
// and the caller must NOT advance the cursor".
//
// Bugfix (TS, kept as the comment that matters): provider callback 以返回值表达
// 可恢复的业务失败，不一定 reject。外部消费游标只能在 ok=true 后提交，否则瞬时
// 失败会被错误确认并永久丢消息。
var ErrCallbackFailed = fmt.Errorf("bots: provider callback failed")

// AssertCallbackSucceeded ports assertBotCallbackSucceeded.
func AssertCallbackSucceeded(provider string, result CallbackResult) error {
	if result.OK {
		return nil
	}
	status := "unknown"
	if result.Status != 0 {
		status = strconv.Itoa(result.Status)
	}
	return fmt.Errorf("%s callback failed: status=%s", provider, status)
}

// LatestRefreshQueue ports createLatestRuntimeRefreshQueue: a SERIAL queue whose
// only cancellation mechanism is a generation counter.
//
// Bugfix (TS): 配置保存会连续触发 fire-and-forget refresh。串行队列既要让后一轮
// 等待前一轮释放连接，又不能因前一轮失败永久阻断后续最新配置。
type LatestRefreshQueue struct {
	mu         sync.Mutex
	generation int
	queue      chan struct{}
}

// NewLatestRefreshQueue builds a queue with one slot of capacity.
func NewLatestRefreshQueue() *LatestRefreshQueue {
	return &LatestRefreshQueue{queue: make(chan struct{}, 1)}
}

// Enqueue runs reconcile after every previously enqueued reconcile has settled.
// isLatest reports whether this run is still the newest generation; a reconcile
// that finds itself stale must return without touching provider state.
func (q *LatestRefreshQueue) Enqueue(ctx context.Context, reconcile func(isLatest func() bool) error) error {
	q.mu.Lock()
	q.generation++
	currentGeneration := q.generation
	q.mu.Unlock()

	// Serialise: take the single slot, waiting for the previous holder. The wait
	// is context-aware so a shutdown does not hang behind a stuck refresh.
	select {
	case q.queue <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-q.queue }()

	return reconcile(func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return currentGeneration == q.generation
	})
}

// Invalidate ports invalidate: every in-flight reconcile becomes stale.
func (q *LatestRefreshQueue) Invalidate() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.generation++
}

// WaitFor ports waitFor: a context-aware sleep that returns immediately when the
// context is already done.
func WaitFor(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
