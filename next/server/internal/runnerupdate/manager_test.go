package runnerupdate

import (
	"testing"

	"github.com/nex-harness/nex/server/internal/runners"
)

func capableInfo(buildID string, capabilities ...string) runners.Info {
	return runners.Info{
		RunnerID:     "r1",
		OS:           "linux",
		Arch:         "x64",
		BuildID:      buildID,
		Capabilities: capabilities,
	}
}

func TestOnRunnerHelloDecisions(t *testing.T) {
	dir := t.TempDir()
	writeBuild(t, dir, "linux-x64", "new")
	manager := NewManager(dir)

	if target := manager.OnRunnerHello(capableInfo("old", CapabilitySelfUpdate)); target != "new" {
		t.Fatalf("outdated capable runner target = %q, want new", target)
	}
	if target := manager.OnRunnerHello(capableInfo("old", "runner-workspaces")); target != "" {
		t.Fatalf("runner without self-update target = %q, want none", target)
	}
	if target := manager.OnRunnerHello(capableInfo("new", CapabilitySelfUpdate)); target != "" {
		t.Fatalf("up-to-date runner target = %q, want none", target)
	}
	if target := manager.OnRunnerHello(capableInfo("", CapabilitySelfUpdate)); target != "" {
		t.Fatalf("legacy runner target = %q, want none", target)
	}
	if target := manager.OnRunnerHello(capableInfo("dev", CapabilitySelfUpdate)); target != "" {
		t.Fatalf("dev runner target = %q, want none", target)
	}
	// A platform with no staged bundle has no target.
	other := runners.Info{RunnerID: "r2", OS: "win32", Arch: "arm64", BuildID: "old", Capabilities: []string{CapabilitySelfUpdate}}
	if target := manager.OnRunnerHello(other); target != "" {
		t.Fatalf("unknown platform target = %q, want none", target)
	}
	// No bundle dir at all disables the feature.
	if target := NewManager("").OnRunnerHello(capableInfo("old", CapabilitySelfUpdate)); target != "" {
		t.Fatalf("disabled manager target = %q, want none", target)
	}
}

// A reconnect with the staged build proves the update landed: a stale
// restarting/failed state must not survive it.
func TestOnRunnerHelloResetsTrackerWhenCurrent(t *testing.T) {
	dir := t.TempDir()
	writeBuild(t, dir, "linux-x64", "new")
	manager := NewManager(dir)

	manager.UpdateStatus("r1", StateFailed, "new", "boom")
	if status := manager.Status("r1"); status.State != StateFailed || status.Error != "boom" {
		t.Fatalf("status = %+v", status)
	}

	manager.OnRunnerHello(capableInfo("new", CapabilitySelfUpdate))
	status := manager.Status("r1")
	if status.State != StateIdle || status.Error != "" {
		t.Fatalf("status after reconnect = %+v, want idle without error", status)
	}
}

func TestTrackerRecordsAndIgnoresUnknownStates(t *testing.T) {
	tracker := NewTracker()
	if status := tracker.Get("missing"); status.State != StateIdle {
		t.Fatalf("unknown runner state = %q, want idle", status.State)
	}

	tracker.Record("r1", StateDownloading, "new", "")
	if status := tracker.Get("r1"); status.State != StateDownloading || status.BuildID != "new" {
		t.Fatalf("status = %+v", status)
	}

	// A failed record keeps the error and the build.
	tracker.Record("r1", StateFailed, "new", "dependencies changed; reinstall required")
	status := tracker.Get("r1")
	if status.State != StateFailed || status.Error != "dependencies changed; reinstall required" {
		t.Fatalf("failed status = %+v", status)
	}

	// A successful record clears the previous error.
	tracker.Record("r1", StateRestarting, "new", "stale")
	status = tracker.Get("r1")
	if status.State != StateRestarting || status.Error != "" {
		t.Fatalf("restarting status = %+v, want no error", status)
	}

	// Unknown states and empty ids are ignored.
	tracker.Record("r1", "exploded", "new", "")
	if status := tracker.Get("r1"); status.State != StateRestarting {
		t.Fatalf("unknown state overwrote the tracker: %+v", status)
	}
	tracker.Record("", StateFailed, "new", "x")
	if status := tracker.Get(""); status.State != StateIdle {
		t.Fatalf("empty runner id recorded: %+v", status)
	}
}
