package runnerupdate

import (
	"strings"

	"github.com/nex-harness/nex/server/internal/runners"
)

// CapabilitySelfUpdate is the hello capability that permits the pull-based
// update (docs/next/runner-update.md decision 2).
const CapabilitySelfUpdate = "self-update"

// Manager is the server's self-update decision surface: it knows the staged
// build per platform and the per-runner update state. It implements
// runnerws.UpdateHandler and backs GET /api/config/runners plus
// POST /api/runner-update.
type Manager struct {
	Bundles *Bundles
	Tracker *Tracker
}

// NewManager returns a manager reading bundles from dir. An empty dir disables
// everything: no platform has a latest build, so no update is ever sent.
func NewManager(dir string) *Manager {
	return &Manager{Bundles: NewBundles(dir), Tracker: NewTracker()}
}

// Enabled reports whether a bundle directory was configured.
func (m *Manager) Enabled() bool {
	return m != nil && m.Bundles != nil && m.Bundles.Dir != ""
}

// OnRunnerHello implements runnerws.UpdateHandler: it decides the automatic
// update for a just-connected runner.
//
// A reconnect whose buildId already equals the staged build proves the update
// landed (or was never needed), so the tracker is reset to idle. Only a runner
// that advertised `self-update` is ever sent the frame.
func (m *Manager) OnRunnerHello(info runners.Info) string {
	if m == nil || m.Bundles == nil || m.Tracker == nil {
		return ""
	}
	latest, known := m.Bundles.LatestFor(info)
	if !known {
		return ""
	}
	if strings.TrimSpace(info.BuildID) == latest {
		m.Tracker.Reset(info.RunnerID)
		return ""
	}
	if !HasCapability(info.Capabilities, CapabilitySelfUpdate) {
		return ""
	}
	if !m.Bundles.Outdated(info) {
		return ""
	}
	return latest
}

// UpdateStatus implements runnerws.UpdateHandler.
func (m *Manager) UpdateStatus(runnerID, state, buildID, errorMessage string) {
	if m == nil || m.Tracker == nil {
		return
	}
	m.Tracker.Record(runnerID, state, buildID, errorMessage)
}

// Status returns the recorded update state for one runner.
func (m *Manager) Status(runnerID string) Status {
	if m == nil || m.Tracker == nil {
		return Status{State: StateIdle}
	}
	return m.Tracker.Get(runnerID)
}

// Latest returns the staged build for a runner's platform.
func (m *Manager) Latest(info runners.Info) (string, bool) {
	if m == nil || m.Bundles == nil {
		return "", false
	}
	return m.Bundles.LatestFor(info)
}

// Outdated reports whether the runner runs a different build than the staged
// one for its platform.
func (m *Manager) Outdated(info runners.Info) bool {
	if m == nil || m.Bundles == nil {
		return false
	}
	return m.Bundles.Outdated(info)
}

// CanSelfUpdate reports whether the runner advertised the capability.
func CanSelfUpdate(info runners.Info) bool {
	return HasCapability(info.Capabilities, CapabilitySelfUpdate)
}
