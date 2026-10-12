//go:build windows

// Windows half of the process-tree hooks declared in killtree.go.
//
// Windows has no signals and no process groups, so the same "kill the whole
// tree" contract that kill(-pgid) expresses on Unix is expressed by a Job
// Object (the boundary the spec names for step 1.8). Assignment can fail — most
// commonly because the agent already runs inside a job that forbids nesting —
// so the failure is recorded rather than returned as fatal and
// `taskkill /T /F` takes over, exactly like the win32 branch of
// terminateProcessTree in
// apps/nex-cli/packages/adapters/src/exec/node-execution-adapter-process.ts.
package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// execCmd is the name killtree.go uses for the command it prepares.
type execCmd = osexec.Cmd

const (
	// jobKillExitCode is the status Task Manager and WaitForSingleObject report
	// for everything the job terminated. Unix keeps SIGTERM/SIGKILL; Windows has
	// one final act, so a single code has to stand for both phases.
	jobKillExitCode = 1
	// processGoneMaxPoll bounds the watcher so a handle that never signals (a
	// process wedged in an uninterruptible syscall) cannot keep a goroutine
	// polling forever.
	processGoneMaxPoll = 30 * time.Second
)

// jobs maps a child pid to its Job Object. Start and releaseProcessTree are the
// only writers, so a plain map under a mutex is enough; no third-party library
// is needed for a per-process resource like this one.
var jobs = struct {
	mu    sync.Mutex
	byPID map[int]windows.Handle
}{byPID: make(map[int]windows.Handle)}

// jobHandle returns the job this pid was assigned to. The second result is
// false when the tree is not tracked: assignment failed, or the child already
// exited and the handle was released.
func jobHandle(pid int) (windows.Handle, bool) {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	h, ok := jobs.byPID[pid]
	return h, ok
}

// prepareSpawnPlatform asks for a new process group so a console Ctrl+C does
// not race the job, and for a hidden window so a bash tool never flashes a
// console. It mirrors `windowsHide: true` and the missing `detached` of the TS
// spawn options; CREATE_NEW_PROCESS_GROUP is what replaces setpgid.
func prepareSpawnPlatform(cmd *execCmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

// attachProcessTreePlatform creates a Job Object for the child and assigns it.
// The job carries JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, which is what makes a
// cancelled tree die even when this process crashes.
//
// A non-nil error is kept on the Process (p.treeErr) instead of failing the
// spawn: a bash tool that cannot be tracked is still better than a bash tool
// that cannot be started, and the kill path falls back to taskkill.
func attachProcessTreePlatform(p *Process) error {
	job, err := windows.CreateJobObject(nil, jobNameFor(p.pid))
	if err != nil {
		return fmt.Errorf("exec: create job object for pid %d: %w", p.pid, err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("exec: set job limits for pid %d: %w", p.pid, err)
	}
	// A handle is needed for the process; os.Process only exposes its pid, so
	// reopen it with the two rights the job API takes.
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("exec: open pid %d for job assignment: %w", p.pid, err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("exec: assign pid %d to job: %w", p.pid, err)
	}
	jobs.mu.Lock()
	jobs.byPID[p.pid] = job
	jobs.mu.Unlock()
	return nil
}

// jobNameFor is the (optional) job name; it only shows up in debuggers.
func jobNameFor(pid int) *uint16 {
	return windows.StringToUTF16Ptr("nex-exec-tree-" + strconv.Itoa(pid))
}

// releaseProcessTreePlatform closes the job handle once the child exited and
// its pipes are closed.
//
// killed decides whether the job still carries JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
// at close time: closing a flagged job terminates everything still in it, which
// is what a cancelled tree wants and what a normal exit must not trigger (the
// flag is dropped first so grandchildren that outlive the command, e.g. a
// daemon the user started on purpose, are not taken down with it).
func releaseProcessTreePlatform(pid int, killed bool) {
	jobs.mu.Lock()
	job, ok := jobs.byPID[pid]
	delete(jobs.byPID, pid)
	jobs.mu.Unlock()
	if !ok {
		return
	}
	if !killed {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		// Clearing the flags is best effort: on a vanished job the handle close
		// below is still the right cleanup.
		_, _ = windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		)
	}
	_ = windows.CloseHandle(job)
}

// signalTree terminates the tree this Process owns.
func (p *Process) signalTree(ctx context.Context, sig Signal) error {
	return signalProcessTree(ctx, p.pid, sig)
}

// signalProcessTree terminates the job led by pid.
//
// Windows has no SIGTERM: TerminateJobObject is final, so terminateSignal and
// killSignal do the same work and the second call only has to catch a job that
// was assigned late (after Start returned) and therefore missed the first
// TerminateJobObject.
func signalProcessTree(ctx context.Context, pid int, _ Signal) error {
	if pid <= 1 {
		return fmt.Errorf("exec: refusing to signal process tree of pid %d", pid)
	}
	job, tracked := jobHandle(pid)
	if tracked {
		if err := windows.TerminateJobObject(job, jobKillExitCode); err != nil {
			return fmt.Errorf("exec: terminate job of pid %d: %w", pid, err)
		}
		return nil
	}
	// No job (assignment failed, or the standalone entry point only had a pid):
	// taskkill /T /F is the documented Windows tree kill.
	return taskkillTree(ctx, pid)
}

// taskkillTree is the fallback kill, the same command the TypeScript adapter
// spawns on win32. Its output is discarded: it is a cleanup step, and a
// diagnostic that surfaces through the tool result would leak Windows console
// text into a Unix-parsed transcript.
func taskkillTree(_ context.Context, pid int) error {
	cmd := osexec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	if err := cmd.Run(); err != nil {
		// taskkill exits non-zero when the tree is already gone, which is the
		// benign case here; anything still alive at that point is a real failure.
		if pidExists(pid) {
			return fmt.Errorf("exec: taskkill /T /F /PID %d: %w", pid, err)
		}
	}
	return nil
}

// pidExists mirrors the Unix signal-0 probe.
func pidExists(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return !errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	_ = windows.CloseHandle(handle)
	return true
}

// processTreeGone reports when pid has left the process table. It polls the
// process handle instead of the job, because Wait only has to know that the
// tree is collectable before it escalates.
func processTreeGone(pid int) <-chan struct{} {
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			// The process is already gone (or unreachable): nothing to wait for.
			return
		}
		defer windows.CloseHandle(handle)
		// WaitForSingleObject only takes a millisecond budget, so the deadline is
		// re-armed per slice instead of using INFINITE.
		const slice = uint32(100)
		deadline := time.Now().Add(processGoneMaxPoll)
		for {
			if time.Now().After(deadline) {
				return
			}
			switch event, err := windows.WaitForSingleObject(handle, slice); {
			case err != nil:
				return
			case event == uint32(windows.WAIT_OBJECT_0):
				return
			}
		}
	}()
	return gone
}

// graceTimer is the deadline killProcessTree waits on between the two signal
// phases; on Windows both phases are the same call, so the timer exists mainly
// to keep the shared body unchanged.
type graceTimer struct {
	timer *time.Timer
	once  sync.Once
}

func newGraceTimer(grace time.Duration) *graceTimer {
	return &graceTimer{timer: time.NewTimer(grace)}
}

func (g *graceTimer) done() <-chan time.Time { return g.timer.C }

// stop releases the timer. Calling it twice, or after the timer fired, is safe.
func (g *graceTimer) stop() {
	g.once.Do(func() {
		if !g.timer.Stop() {
			select {
			case <-g.timer.C:
			default:
			}
		}
	})
}

// exitedSignal is always "": Windows has no signals, so a terminated process
// reports Killed() == false and the bash tool maps the exit code instead. That
// is why node-execution-adapter-process.ts treats win32 stop state separately
// from `stop.killed || state.signal`.
func exitedSignal(*os.ProcessState) string { return "" }
