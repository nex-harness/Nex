// service.go ports storage/app/{storageService,scanJob}.ts: the scan job
// (throttled progress, terminal state always fired) and the service that owns
// the latest snapshot and the clean flow. No IO; roots, walking, deletion and
// volume probing all go through the injected ports.
package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// defaultProgressThrottleMs ports DEFAULT_STORAGE_PROGRESS_THROTTLE_MS.
const defaultProgressThrottleMs = 300

// Service is the IStorageService port: the single owner of the running scan
// job, the latest snapshot and the clean flow.
type Service struct {
	roots      RootsResolverPort
	scanRunner ScanRunnerPort
	cleaner    FsCleanerPort
	now        Clock
	throttleMs int64

	mu             sync.Mutex
	currentJob     *scanJob
	latestSnapshot *Snapshot
	latestJobID    string
	jobCounter     int

	progressMu       sync.Mutex
	progressHandlers map[int]func(Snapshot)
	nextHandlerID    int
	closed           bool
}

// Options ports StorageServiceDependencies.
type Options struct {
	Roots      RootsResolverPort
	ScanRunner ScanRunnerPort
	Cleaner    FsCleanerPort
	Now        Clock // nil = unix millis
	// ProgressThrottleMs: minimum gap between progress events; default 300.
	ProgressThrottleMs int64
}

// New ports createStorageService.
func New(options Options) *Service {
	now := options.Now
	throttle := options.ProgressThrottleMs
	if throttle <= 0 {
		throttle = defaultProgressThrottleMs
	}
	return &Service{
		roots: options.Roots, scanRunner: options.ScanRunner, cleaner: options.Cleaner,
		now: now, throttleMs: throttle,
		progressHandlers: map[int]func(Snapshot){},
	}
}

// CleanRequest ports StorageCleanRequest.
type CleanRequest struct {
	RootID     string `json:"rootId"`
	CategoryID string `json:"categoryId"`
}

// CleanResult ports StorageCleanResult.
type CleanResult struct {
	FreedBytes   int64       `json:"freedBytes"`
	DeletedCount int         `json:"deletedCount"`
	SkippedCount int         `json:"skippedCount"`
	Failures     []PathError `json:"failures"`
}

// StartScan ports startScan: cancels any running job, then starts a new one
// and returns its jobId.
func (s *Service) StartScan(ctx context.Context) (string, error) {
	s.cancelCurrentJob()
	s.jobCounter++
	jobID := fmt.Sprintf("scan-%d", s.jobCounter)
	roots, err := s.roots.ResolveRoots(ctx)
	if err != nil {
		return "", err
	}
	// 用 currentJob 判断会在用户主动 cancelScan 后（currentJob 已清空）丢掉取消态
	// 快照。这里按「最近一次启动的 job」判断：旧 job 被新 job 取代后发出的尾包不能
	// 覆盖新 job 的快照。
	s.mu.Lock()
	s.latestJobID = jobID
	s.mu.Unlock()
	job := newScanJob(jobID, roots, s.scanRunner, s.now, s.throttleMs, func(snapshot Snapshot) {
		s.mu.Lock()
		if jobID == s.latestJobID {
			latest := snapshot
			s.latestSnapshot = &latest
		}
		s.mu.Unlock()
		s.fireProgress(snapshot)
	}, func() {
		s.mu.Lock()
		if s.currentJob != nil && s.currentJob.jobID == jobID {
			s.currentJob = nil
		}
		s.mu.Unlock()
	})
	s.mu.Lock()
	s.currentJob = job
	s.mu.Unlock()
	return jobID, nil
}

// CancelScan ports cancelScan: no-op when jobId is not the current job.
func (s *Service) CancelScan(_ context.Context, jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentJob != nil && s.currentJob.jobID == jobID {
		s.currentJob.cancel()
		s.currentJob = nil
	}
}

// GetSnapshot ports getSnapshot: the most recent snapshot, nil if never
// scanned.
func (s *Service) GetSnapshot(_ context.Context) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latestSnapshot
}

// OnScanProgress ports the onScanProgress event. The returned cancel func
// removes the listener; Dispose drops them all.
func (s *Service) OnScanProgress(handler func(Snapshot)) (cancel func()) {
	s.progressMu.Lock()
	if s.closed {
		s.progressMu.Unlock()
		return func() {}
	}
	s.nextHandlerID++
	id := s.nextHandlerID
	s.progressHandlers[id] = handler
	s.progressMu.Unlock()
	return func() {
		s.progressMu.Lock()
		delete(s.progressHandlers, id)
		s.progressMu.Unlock()
	}
}

func (s *Service) fireProgress(snapshot Snapshot) {
	s.progressMu.Lock()
	handlers := make([]func(Snapshot), 0, len(s.progressHandlers))
	for _, handler := range s.progressHandlers {
		handlers = append(handlers, handler)
	}
	s.progressMu.Unlock()
	for _, handler := range handlers {
		handler(snapshot)
	}
}

// Clean ports clean: refuse non-cleanable categories, cancel the running
// scan (its results would be stale after deletion), then plan and delete.
func (s *Service) Clean(ctx context.Context, request CleanRequest) (CleanResult, error) {
	if CleanabilityOf(request.CategoryID) == "none" {
		return CleanResult{}, fmt.Errorf("storage category is not cleanable: %s", request.CategoryID)
	}
	s.cancelCurrentJob()
	roots, err := s.roots.ResolveRoots(ctx)
	if err != nil {
		return CleanResult{}, err
	}
	var root *RootSpec
	for i := range roots {
		if roots[i].ID == request.RootID {
			root = &roots[i]
			break
		}
	}
	if root == nil {
		return CleanResult{}, fmt.Errorf("storage root not available: %s", request.RootID)
	}
	scopes := CleanScopes(request.CategoryID)
	candidates, err := s.cleaner.ListCandidates(ctx, root.Path, scopes)
	if err != nil {
		return CleanResult{}, err
	}
	plan := PlanStorageClean(request.CategoryID, candidates,
		CatalogContext{RootID: root.ID, HasCustomDataBaseDir: root.HasCustomDataBaseDir}, s.now())
	deleteResult, err := s.cleaner.DeleteFiles(ctx, root.Path, plan.Targets, keepDirectories(scopes))
	if err != nil {
		return CleanResult{}, err
	}
	return CleanResult{
		FreedBytes:   deleteResult.FreedBytes,
		DeletedCount: deleteResult.DeletedCount,
		SkippedCount: plan.SkippedCount,
		Failures:     deleteResult.Failures,
	}, nil
}

func keepDirectories(scopes []CleanScope) []string {
	prefixes := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		prefixes = append(prefixes, scope.Prefix)
	}
	return prefixes
}

// Dispose ports dispose: cancel the running scan and drop the event source.
func (s *Service) Dispose() {
	s.cancelCurrentJob()
	s.progressMu.Lock()
	s.closed = true
	s.progressHandlers = map[int]func(Snapshot){}
	s.progressMu.Unlock()
}

func (s *Service) cancelCurrentJob() {
	s.mu.Lock()
	if s.currentJob != nil {
		s.currentJob.cancel()
		s.currentJob = nil
	}
	s.mu.Unlock()
}

// scanJob ports createScanJob: one scan with its own cancellation. Progress
// is throttled to one event per throttleMs; the terminal state
// (complete/cancelled/failed) always fires. State only moves
// scanning → terminal.
type scanJob struct {
	jobID  string
	cancel func()
	done   chan struct{}
}

func newScanJob(jobID string, roots []RootSpec, runner ScanRunnerPort, now Clock, throttleMs int64,
	emit func(Snapshot), onDone func()) *scanJob {
	cancel := make(chan struct{})
	cancelOnce := sync.Once{}
	job := &scanJob{jobID: jobID, done: make(chan struct{})}
	startedAt := now()
	latest := Snapshot{
		JobID: jobID, Status: "scanning", StartedAt: startedAt,
		Roots: initialRoots(roots), Errors: []PathError{},
	}
	var mu sync.Mutex
	var lastEmitAt int64 = -1 << 62
	settled := false

	flush := func() {
		lastEmitAt = now()
		mu.Lock()
		snapshot := latest
		mu.Unlock()
		emit(snapshot)
	}
	onProgress := func(progress ScanProgress) {
		mu.Lock()
		if settled {
			mu.Unlock()
			return
		}
		latest.Roots = progress.Roots
		latest.Errors = progress.Errors
		mu.Unlock()
		elapsed := now() - lastEmitAt
		if elapsed >= throttleMs {
			flush()
		}
		// The TS additionally arms a trailing timer for the in-flight window;
		// Go has no event loop to yield to, so the next progress callback
		// re-checks the throttle — the terminal snapshot is always emitted by
		// settle() either way.
	}
	settle := func(snapshot Snapshot) Snapshot {
		mu.Lock()
		settled = true
		latest = snapshot
		mu.Unlock()
		emit(snapshot)
		return snapshot
	}

	go func() {
		defer close(job.done)
		defer onDone()
		progress, err := runner.Run(context.Background(), ScanRunRequest{
			Roots: roots, Cancel: cancel, OnProgress: onProgress,
		})
		if err != nil {
			if errors.Is(err, ErrScanAborted) {
				settle(Snapshot{JobID: jobID, Status: "cancelled", StartedAt: startedAt,
					Roots: latest.Roots, Errors: latest.Errors, FinishedAt: now()})
			} else {
				code := "UNKNOWN"
				var coded interface{ Code() string }
				if errors.As(err, &coded) {
					code = coded.Code()
				}
				settle(Snapshot{JobID: jobID, Status: "failed", StartedAt: startedAt,
					Roots: latest.Roots, Errors: append(append([]PathError{}, latest.Errors...), PathError{Path: "", Code: code}),
					FinishedAt: now()})
			}
			return
		}
		status := "complete"
		select {
		case <-cancel:
			status = "cancelled"
		default:
		}
		settle(Snapshot{JobID: jobID, Status: status, StartedAt: startedAt,
			Roots: progress.Roots, Errors: progress.Errors, FinishedAt: now()})
	}()

	job.cancel = func() {
		cancelOnce.Do(func() { close(cancel) })
	}
	return job
}

func initialRoots(roots []RootSpec) []RootUsage {
	usages := make([]RootUsage, 0, len(roots))
	for _, root := range roots {
		usages = append(usages, RootUsage{ID: root.ID, Path: root.Path, Categories: []CategoryUsage{}})
	}
	return usages
}
