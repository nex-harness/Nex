package automations

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixedZone is the zone every test schedules in: no DST, so wall-clock maths
// in the scheduler tests stays obvious.
func fixedZone(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("loading Asia/Shanghai: %v", err)
	}
	return loc
}

// fakeClock is the injected Service clock. Tests set it directly; Set must be
// safe to call from a dispatcher while the service runs.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	onSet func(time.Time)
}

func newFakeClock(start string) *fakeClock {
	at, err := time.Parse(time.RFC3339, start)
	if err != nil {
		panic(err)
	}
	return &fakeClock{now: at}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(value time.Time) {
	c.mu.Lock()
	c.now = value
	hook := c.onSet
	c.mu.Unlock()
	if hook != nil {
		hook(value)
	}
}

func (c *fakeClock) Advance(delta time.Duration) { c.Set(c.Now().Add(delta)) }

// call records one dispatcher invocation.
type call struct {
	Target  Target
	Command Command
	At      time.Time
}

// fakeDispatcher answers with configurable results and records every call.
type fakeDispatcher struct {
	mu        sync.Mutex
	calls     []call
	respond   func(call call) (string, error)
	onCall    func(call call)
	sessionID atomic.Int64
}

func (d *fakeDispatcher) Dispatch(_ context.Context, target Target, command Command) (string, error) {
	entry := call{Target: target, Command: command}
	d.mu.Lock()
	d.calls = append(d.calls, entry)
	respond := d.respond
	hook := d.onCall
	d.mu.Unlock()
	if hook != nil {
		hook(entry)
	}
	if respond != nil {
		return respond(entry)
	}
	return "session-" + command.CommandID, nil
}

func (d *fakeDispatcher) Calls() []call {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]call(nil), d.calls...)
}

func (d *fakeDispatcher) Len() int { return len(d.Calls()) }

// waitFor polls until condition holds or the deadline passes.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// testService builds a Service over a temp dir with an injected clock.
type testService struct {
	*Service
	clock      *fakeClock
	dispatcher *fakeDispatcher
	dir        string
}

func newTestService(t *testing.T, options Options, start string) *testService {
	t.Helper()
	clock := newFakeClock(start)
	if options.Dir == "" {
		options.Dir = t.TempDir()
	}
	if options.Location == nil {
		options.Location = fixedZone(t)
	}
	options.Now = clock.Now
	dispatcher := &fakeDispatcher{}
	if options.Dispatcher == nil {
		options.Dispatcher = dispatcher
	}
	service, err := New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(service.Close)
	return &testService{Service: service, clock: clock, dispatcher: dispatcher, dir: options.Dir}
}

// create makes a recurring automation through the public create path.
func (s *testService) create(t *testing.T, params CreateParams) Automation {
	t.Helper()
	created, err := s.createAutomation(params, s.clock.Now())
	if err != nil {
		t.Fatalf("createAutomation: %v", err)
	}
	return *created
}

// stored returns the persisted copy of an automation (disk state, not memory).
func (s *testService) stored(t *testing.T, automationID string) Automation {
	t.Helper()
	document := readDocument(t, s.dir)
	for _, automation := range document.Automations {
		if automation.AutomationID == automationID {
			return automation
		}
	}
	t.Fatalf("automation %s not on disk", automationID)
	return Automation{}
}

// runs returns the persisted runs of an automation, newest first.
func (s *testService) runs(t *testing.T, automationID string) []Run {
	t.Helper()
	all := readDocument(t, s.dir).Runs
	var out []Run
	for index := len(all) - 1; index >= 0; index-- {
		if all[index].AutomationID == automationID {
			out = append(out, all[index])
		}
	}
	return out
}

// readDocument decodes automations.json straight from disk, so assertions see
// exactly what survives a crash.
func readDocument(t *testing.T, dir string) document {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, storeFileName))
	if err != nil {
		t.Fatalf("reading store: %v", err)
	}
	var parsed document
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decoding store: %v", err)
	}
	return parsed
}

// everyMinute is a recurring automation with an explicit daily rule that never
// stops; tests usually want a concrete schedule.
func everyMinute(cronExpr string) CreateParams {
	return CreateParams{
		Title:         "Test automation",
		Prompt:        "do the thing",
		CronExpr:      cronExpr,
		WorkspacePath: "/work/app",
	}
}

func ptr[T any](value T) *T { return &value }
