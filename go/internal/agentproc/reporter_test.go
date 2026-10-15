// reporter_test.go is the in-memory ProcessLifecycleReporter the lifecycle tests
// assert against. It records everything so a test can ask "was the crash
// classified as unexpected?", "did the diagnostic come through redacted?" and
// "was exactly one ready record produced?" without scraping logs.
package agentproc

import "sync"

type recordingReporter struct {
	mu         sync.Mutex
	spawns     []SpawnEvent
	readies    []ReadyEvent
	exceptions []ExceptionEvent
	errors     []ErrorEvent
	exits      []ExitEvent
}

func (r *recordingReporter) OnSpawn(ev SpawnEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spawns = append(r.spawns, ev)
}

func (r *recordingReporter) OnReady(ev ReadyEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readies = append(r.readies, ev)
}

func (r *recordingReporter) OnException(ev ExceptionEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exceptions = append(r.exceptions, ev)
}

func (r *recordingReporter) OnError(ev ErrorEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, ev)
}

func (r *recordingReporter) OnExit(ev ExitEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exits = append(r.exits, ev)
}

func (r *recordingReporter) exitCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.exits)
}

func (r *recordingReporter) exceptionCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.exceptions)
}

func (r *recordingReporter) lastExit() ExitEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.exits) == 0 {
		return ExitEvent{}
	}
	return r.exits[len(r.exits)-1]
}

func (r *recordingReporter) lastException() ExceptionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.exceptions) == 0 {
		return ExceptionEvent{}
	}
	return r.exceptions[len(r.exceptions)-1]
}

func (r *recordingReporter) spawnCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.spawns)
}

func (r *recordingReporter) readyCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.readies)
}

func (r *recordingReporter) errorCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errors)
}

func (r *recordingReporter) allExits() []ExitEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ExitEvent(nil), r.exits...)
}
