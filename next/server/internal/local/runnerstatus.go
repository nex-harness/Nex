package local

// Per-runner model status for the HTTP API (GET /api/config/runners) and the
// install wizard's Models step (docs/next/model-onboarding.md decisions 1-4).
//
// Ownership: the server answers this from the same doc + facts pair the
// model-selection channel uses, so `modelCount` is exactly the composer's list
// for that runner. Runners own the applied runtime and the facts; the wizard
// only reads this status and triggers the one-shot import.

import (
	"sort"
	"strings"
)

// Runner mode values reported by `configApplied.facts.mode`.
const (
	RunnerModeServer   = "server"
	RunnerModeFallback = "fallback"
	// RunnerModeUnknown is reported until the runner has reported facts.
	RunnerModeUnknown = "unknown"
)

// RunnerModelStatus is one row of GET /api/config/runners.
type RunnerModelStatus struct {
	RunnerID  string `json:"runnerId"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	// Mode is "server", "fallback" or "unknown" (no facts yet).
	Mode string `json:"mode"`
	// ModelCount is the number of models model-selection offers this runner.
	ModelCount         int    `json:"modelCount"`
	LastAppliedVersion uint64 `json:"lastAppliedVersion"`
}

// RunnerModelStatuses lists every known runner (a live link or recorded facts)
// with its name, mode and executable model count, sorted by name then id.
func (m *Manager) RunnerModelStatuses() []RunnerModelStatus {
	ids := m.KnownRunners()
	out := make([]RunnerModelStatus, 0, len(ids))
	for _, runnerID := range ids {
		state, _ := m.Applied(runnerID)
		status := RunnerModelStatus{
			RunnerID:           runnerID,
			Name:               m.runnerName(runnerID),
			Connected:          state.Connected,
			Mode:               runnerMode(state.Facts),
			LastAppliedVersion: state.LastApplied,
		}
		// A runner without facts (or with an unreadable doc) has no model list;
		// the wizard treats 0 as "needs models" and re-polls after the push.
		if models, _, err := executableModelsForRunner(m, runnerID); err == nil {
			status.ModelCount = len(models)
		}
		out = append(out, status)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].RunnerID < out[j].RunnerID
	})
	return out
}

// runnerName is the hello name, falling back to the id. A runner known only
// through recorded facts is offline and has no registry entry.
func (m *Manager) runnerName(runnerID string) string {
	if m.reg != nil {
		if info, ok := m.reg.RunnerInfo(runnerID); ok {
			if name := strings.TrimSpace(info.Name); name != "" {
				return name
			}
		}
	}
	return runnerID
}

// runnerMode reads the mode a runner reported in `configApplied.facts`. A
// missing or unrecognized value (an old peer) stays "unknown" rather than
// guessing the mode from the doc.
func runnerMode(facts map[string]any) string {
	if facts == nil {
		return RunnerModeUnknown
	}
	mode, _ := facts["mode"].(string)
	switch strings.TrimSpace(mode) {
	case RunnerModeServer:
		return RunnerModeServer
	case RunnerModeFallback:
		return RunnerModeFallback
	}
	return RunnerModeUnknown
}
