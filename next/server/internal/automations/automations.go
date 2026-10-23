package automations

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Timing constants. The spec fixes the policy values
// (docs/next/automations-views-search.md §A).
const (
	// misfireGrace: a scheduled fire time the server missed by more than this
	// (server was down) is recorded as `skipped` and the schedule advances
	// instead of firing late.
	misfireGrace = 5 * time.Minute
	// dispatchRetryDelay: a dispatch failure (runner offline) is retried every
	// 60 s.
	dispatchRetryDelay = 60 * time.Second
	// dispatchMaxAttempts: three attempts, then failed_to_dispatch.
	dispatchMaxAttempts = 3
	// dispatchTimeout bounds one runner call so a hung runner cannot wedge the
	// timer loop.
	dispatchTimeout = 60 * time.Second
	// claimStaleMS: a claim held longer than this is considered abandoned
	// (crashed server) and may be recovered.
	claimStaleMS = 10 * 60 * 1000
	// minTimerDelay avoids a busy loop when nextRunAt is already in the past.
	minTimerDelay = 50 * time.Millisecond
)

// Target identifies the runner-side workspace an automation belongs to.
type Target struct {
	WorkspacePath     string
	WorkspaceIdentity string
}

// Command is the single runner command a dispatch performs.
//
// Exactly one of the two forms is used:
//   - CreateSession: a new session whose first input is the prompt (the old
//     host's createTask + sendPrompt collapsed into one atomic call);
//   - SendText: the prompt is sent to an existing session (TargetTaskID).
//
// CommandID equals the run id, so a retried dispatch is idempotent on the
// runner side.
type Command struct {
	CommandID string
	// CreateSession is true for a new session; false sends text to SessionID.
	CreateSession bool
	// SessionID is the bound session for SendText.
	SessionID string
	// Text is the automation prompt.
	Text string
	// ModelSelection is the frozen per-run selection (raw JSON, may be nil).
	ModelSelection json.RawMessage
	// Mode is the permission mode (nil = workspace default).
	Mode *string
}

// Dispatcher delivers one Command to the runner that owns the target
// workspace and returns the runner session id.
//
// The wiring agent implements it with registry.CallWorkspace +
// nex-agent.sendConversationCommandV4 (createSession{firstInput} or sendText)
// using Command.CommandID as the envelope commandId.
type Dispatcher interface {
	Dispatch(ctx context.Context, target Target, command Command) (sessionID string, err error)
}

// DispatcherFunc adapts a function to Dispatcher.
type DispatcherFunc func(ctx context.Context, target Target, command Command) (string, error)

// Dispatch implements Dispatcher.
func (f DispatcherFunc) Dispatch(ctx context.Context, target Target, command Command) (string, error) {
	return f(ctx, target, command)
}

// Options configures a Service.
type Options struct {
	// Dir holds automations.json. It is usually the server config dir.
	Dir string
	// Logger may be nil.
	Logger *slog.Logger
	// Now is the injected clock (nil = time.Now). Tests advance it.
	Now func() time.Time
	// Location is the server's local zone; new automations capture its name
	// as their timezone (nil = time.Local).
	Location *time.Location
	// Dispatcher delivers runs. When nil, dispatches fail (runs stay claimed
	// until the stale reclaim) and runAutomationNow reports the configuration
	// error.
	Dispatcher Dispatcher
}

// Service owns the automations document, the single schedule timer and the
// dispatch state machine.
type Service struct {
	mu         sync.Mutex
	store      *store
	logger     *slog.Logger
	nowFn      func() time.Time
	location   *time.Location
	dispatch   Dispatcher
	changed    []func()
	timer      *time.Timer
	timerArmed bool
	// pendingNotify is set by the store after a write and consumed by
	// flushNotify once the lock is released.
	pendingNotify bool
	closed        bool
	ctx           context.Context
}

// New opens the store immediately (a corrupt or unknown-schema file fails
// construction instead of being overwritten later).
func New(options Options) (*Service, error) {
	if strings.TrimSpace(options.Dir) == "" {
		return nil, errors.New("automations: Dir is required")
	}
	logger := options.Logger
	nowFn := options.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	location := options.Location
	if location == nil {
		location = time.Local
	}
	service := &Service{
		store:    newStore(options.Dir, logger),
		logger:   logger,
		nowFn:    nowFn,
		location: location,
		dispatch: options.Dispatcher,
		ctx:      context.Background(),
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.store.load(); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) now() time.Time { return s.nowFn() }

// OnChange registers a callback invoked after every persisted mutation, for
// the browser notification. It is called without the service lock, after the
// write is durable and after the timer has been re-armed.
func (s *Service) OnChange(fn func()) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changed = append(s.changed, fn)
}

// Start arms the schedule timer and runs an initial tick (missed windows are
// resolved at startup). The timer re-arms itself after each fire or mutation;
// there is no polling loop. Start is idempotent.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.closed || s.timerArmed {
		s.mu.Unlock()
		return
	}
	if ctx != nil {
		s.ctx = ctx
	}
	s.timerArmed = true
	s.store.mutation = s.onMutationLocked
	s.mu.Unlock()
	go s.run(ctx)
}

// Close stops the timer, releases in-flight claims and waits for work
// started by Close to settle. After Close every mutation returns an error.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	// Release claims so the next process does not wait for the stale timeout.
	for index := range s.store.doc.Automations {
		automation := s.store.doc.Automations[index]
		if automation.Running {
			automation.Running = false
			automation.ClaimedAt = nil
			if automation.DispatchStatus == DispatchClaimed {
				automation.DispatchStatus = DispatchIdle
			}
			s.store.doc.Automations[index] = automation
		}
	}
	_ = s.store.persist()
	s.mu.Unlock()
}

// onMutationLocked re-arms the timer and marks a pending change; called by the
// store after a successful write, with the lock held. The callback itself runs
// outside the lock (flushNotify) so it can call back into the service.
func (s *Service) onMutationLocked() {
	if s.closed {
		return
	}
	if s.timerArmed {
		s.armLocked()
	}
	s.pendingNotify = true
}

// notifyInvoke runs the pending change hooks with the lock released, then
// takes it again. It is called from mutating methods while their deferred
// unlock is still pending: at that point the document is durable and the timer
// has been re-armed, so a hook reading through the service sees the new state,
// and re-acquiring the lock before returning keeps the deferred Unlock valid.
// A hook must not call back into a mutating method (it would block on the
// re-acquired lock); hooks are for out-of-band notifications.
func (s *Service) notifyInvoke() {
	if !s.pendingNotify {
		return
	}
	s.pendingNotify = false
	callbacks := append([]func(){}, s.changed...)
	if len(callbacks) == 0 {
		return
	}
	s.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
	s.mu.Lock()
}

// notify fans out the change hook without holding the lock.
func (s *Service) notify() {
	s.mu.Lock()
	callbacks := append([]func(){}, s.changed...)
	s.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

// flushPendingNotify runs the change hooks iff a write marked a pending
// change; used by the tick and the manual dispatch after they release the
// lock (ordinary mutations use notifyInvoke instead).
func (s *Service) flushPendingNotify() {
	s.mu.Lock()
	pending := s.pendingNotify
	if pending {
		s.pendingNotify = false
	}
	s.mu.Unlock()
	if pending {
		s.notify()
	}
}

// nextDueLocked returns the earliest instant at which the timer must fire:
// the earliest nextRunAt of an enabled automation or a pending retry.
func (s *Service) nextDueLocked() (time.Time, bool) {
	var earliest time.Time
	found := false
	for index := range s.store.doc.Automations {
		automation := &s.store.doc.Automations[index]
		if !automation.Enabled || automation.LifecycleStatus != LifecycleActive || automation.Running {
			continue
		}
		var candidate *int64
		if automation.RetryAt != nil {
			candidate = automation.RetryAt
		} else if automation.NextRunAt != nil {
			candidate = automation.NextRunAt
		}
		if candidate == nil {
			continue
		}
		at := timeOfMillis(*candidate)
		if !found || isAfter(earliest, at) {
			earliest = at
			found = true
		}
	}
	return earliest, found
}

// armLocked (re)sets the timer to the earliest due instant. Called after
// every persisted mutation and after each tick.
func (s *Service) armLocked() {
	if s.closed {
		return
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	due, ok := s.nextDueLocked()
	if !ok {
		return
	}
	delay := time.Duration(since(due, s.now()))
	if delay < minTimerDelay {
		delay = minTimerDelay
	}
	s.timer = time.AfterFunc(delay, s.tick)
}

// tick is the timer callback: it claims and dispatches every due automation,
// then re-arms for the next instant.
func (s *Service) tick() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.timer = nil
	s.mu.Unlock()
	// processDue claims, dispatches and settles; every commit re-arms the
	// timer, so a single extra arm here covers the "nothing was due" case.
	s.processDue()
	s.mu.Lock()
	if !s.closed {
		s.armLocked()
	}
	s.mu.Unlock()
}

// run drives the timer in a goroutine and exits on context cancellation.
func (s *Service) run(ctx context.Context) {
	if ctx == nil {
		s.tick()
		return
	}
	s.tick()
	<-ctx.Done()
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.timerArmed = false
	s.mu.Unlock()
}

// pendingDispatch is one claimed run waiting for its runner call.
func (s *Service) logError(message string, err error) {
	if s.logger == nil || err == nil {
		return
	}
	s.logger.Error("automations: "+message, "error", err)
}

func (s *Service) logInfo(message string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Info("automations: "+message, args...)
}

func (s *Service) logWarn(message string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Warn("automations: "+message, args...)
}
