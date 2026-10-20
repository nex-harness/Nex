package local

// `onboarding-record` stub (docs/next/channels-research.md §9).
//
// OccupationOnboarding reads the latest entry on mount so a manually reopened
// onboarding can prefill the previous answers. Missing channel logs a warning
// (`读取预填作答失败`) but is otherwise tolerated; an empty record removes the
// noise and keeps prefill at defaults. Writes are rejected so a future manual
// save does not pretend to persist on a server with no per-device store.

func newOnboardingRecordStub() *Stub {
	return newStubWithErrors(map[string]string{
		"getLatestEntry": `null`,
		"getRecords":     `null`,
	}, map[string]string{
		"appendRecord":            "onboarding record is not persisted by nex-next-server",
		"dismissOnboarding":       "onboarding record is not persisted by nex-next-server",
		"updateRecordPreferences": "onboarding record is not persisted by nex-next-server",
		"clearRecords":            "onboarding record is not persisted by nex-next-server",
	})
}

// `bots` stub (docs/next/channels-research.md §9).
//
// Root syncs app runtime preferences into the bots service on startup. The
// MVP server has no bot runtime; accepting the sync silently removes the
// "Channel name 'bots' not found" warning without exposing bot UI state.

func newBotsStub() *Stub {
	return newStubWithErrors(map[string]string{
		"syncAppRuntimePreferences": `null`,
	}, nil)
}
