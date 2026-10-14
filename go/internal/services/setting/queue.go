// queue.go ports settingsWriteQueue.ts + the promise chaining in
// createSettingServiceWithMigrations.
//
// 语义（TS 实测）：
//   - updateQueue 串行所有 update；每个 update 拿到一个递增 generation，
//     generation 落后（有新 update 入队）的写不再提交 rename —— 后入队的
//     patch 最终落盘，旧写不覆盖新写（这是 TS 队列的"coalescing"：不是合并
//     patch，而是允许新写取代未提交的旧写）。
//   - 30s 超时只作用于提交前阶段；进入 rename 临界区后必须等收口。
//   - get() 先等 updateQueue 排空（读取不越过已入队写入）。
//   - commitQueue 只串行化 rename 临界区本身（runExclusiveCommit），保持
//     磁盘顺序与队列顺序一致。

package setting

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// QueueOptions configures NewQueue.
type QueueOptions struct {
	// TimeoutMs is SETTINGS_WRITE_QUEUE_TIMEOUT_MS (default 30_000).
	TimeoutMs int64
	// Now/Sleep are test seams for the timeout clock.
	Now   func() time.Time
	Sleep func(time.Duration)
}

// Queue is the settings write queue.
type Queue struct {
	mu sync.Mutex
	// updateChain serializes updates (the TS updateQueue promise chain).
	updateChain chan struct{}
	// commitChain serializes the rename critical sections.
	commitChain chan struct{}
	// generation is the write-queue generation counter.
	generation int64
	timeoutMs  int64
	now        func() time.Time
	sleep      func(time.Duration)
}

// NewQueue builds a queue with both chains already closed (idle).
func NewQueue(opts QueueOptions) *Queue {
	timeout := opts.TimeoutMs
	if timeout <= 0 {
		timeout = SettingsWriteQueueTimeoutMS
	}
	idle := make(chan struct{})
	close(idle)
	q := &Queue{
		updateChain: idle,
		commitChain: idle,
		timeoutMs:   timeout,
		now:         opts.Now,
		sleep:       opts.Sleep,
	}
	if q.now == nil {
		q.now = time.Now
	}
	if q.sleep == nil {
		q.sleep = time.Sleep
	}
	return q
}

// WaitUpdateQueue drains the update chain (the `await updateQueue` in get()).
func (q *Queue) WaitUpdateQueue(ctx context.Context) error {
	q.mu.Lock()
	chain := q.updateChain
	q.mu.Unlock()
	select {
	case <-chain:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunExclusiveCommit runs commit under the commit chain (runSettingsCommit):
// commits execute strictly one after another, in queue order — the NEXT
// queued rename waits until this one has finished.
func (q *Queue) RunExclusiveCommit(commit func() error) error {
	q.mu.Lock()
	prev := q.commitChain
	done := make(chan struct{})
	q.commitChain = done
	q.mu.Unlock()
	<-prev
	err := commit()
	close(done)
	return err
}

// EnqueueSettingsWrite ports enqueueSettingsWrite: serialize runUpdate behind
// the chain, then run it with a fresh generation and the queue timeout.
func (q *Queue) EnqueueSettingsWrite(
	ctx context.Context,
	runUpdate func(shouldCommit func() bool, enterCommitPhase func()) error,
) error {
	q.mu.Lock()
	prev := q.updateChain
	prevDone := make(chan struct{})
	q.updateChain = prevDone
	q.mu.Unlock()

	// Wait for the previous update, then run ours. The chain slot closes
	// after OUR update settles (success or failure), matching the TS
	// `updateQueue = queued.catch(() => {})`.
	runErr := func() error {
		<-prev
		return q.runCurrentUpdate(ctx, runUpdate)
	}()
	close(prevDone)
	return runErr
}

// runCurrentUpdate ports runCurrentUpdate: generation bump, timeout race.
//
// 与 TS 语义对齐的关键点：generation 在本 update 真正开始执行时分配（TS 的
// `const currentGeneration = ++writeQueueGeneration` 位于 runCurrentUpdate 内部、
// 即 promise 链轮到它时才运行）。因为队列本身串行执行，正在跑的 update 拿到的
// 永远是最新 generation，只有"超时后才走完提交"或"队列重入"的写会 stale。
func (q *Queue) runCurrentUpdate(
	ctx context.Context,
	runUpdate func(shouldCommit func() bool, enterCommitPhase func()) error,
) error {
	q.mu.Lock()
	q.generation++
	currentGeneration := q.generation
	q.mu.Unlock()

	shouldCommit := func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return currentGeneration == q.generation
	}
	// expireCurrentWrite: bump generation so the stale write cannot commit.
	expireCurrentWrite := func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.generation == currentGeneration {
			q.generation++
		}
	}

	return q.withSettingsWriteQueueTimeout(ctx, runUpdate, shouldCommit, expireCurrentWrite)
}

// withSettingsWriteQueueTimeout ports withSettingsWriteQueueTimeout.
func (q *Queue) withSettingsWriteQueueTimeout(
	ctx context.Context,
	runUpdate func(shouldCommit func() bool, enterCommitPhase func()) error,
	shouldCommit func() bool,
	expireCurrentWrite func(),
) error {
	type result struct {
		err error
	}
	runDone := make(chan result, 1)
	commitPhaseEntered := false
	enterCommitPhase := func() {
		commitPhaseEntered = true
	}

	go func() {
		runDone <- result{err: runUpdate(shouldCommit, enterCommitPhase)}
	}()

	// 设置写入队列持有真实持久化顺序，Provider 层不 await 也无法释放这里
	// 的 pending。超时只允许发生在提交前阶段；进入 rename 提交后必须等临
	// 界区收口，避免旧写晚到覆盖新设置。Promise.race 语义：超时让调用方
	// 拿到错误，但 body 继续在后台跑完（runDone 有缓冲，不会泄漏）。
	timeout := time.Duration(q.timeoutMs) * time.Millisecond
	timer := time.NewTimer(timeout)
	for {
		select {
		case res := <-runDone:
			return res.err
		case <-timer.C:
			if commitPhaseEntered {
				// 提交阶段不受超时约束：等待临界区收口。
				res := <-runDone
				return res.err
			}
			expireCurrentWrite()
			return fmt.Errorf("settingService update timed out after %dms", q.timeoutMs)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
