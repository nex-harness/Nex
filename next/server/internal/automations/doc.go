package automations

// Integration surface
//
// The wiring step in internal/local (or wherever the local channel registry is
// assembled) uses exactly this API; everything else in the package is
// unexported:
//
//	service, err := automations.New(automations.Options{
//	    Dir:        configDir,          // holds automations.json
//	    Logger:     slogLogger,         // optional (nil = silent)
//	    Now:        nil,                // optional clock, nil = time.Now
//	    Location:   nil,                // optional create-time zone, nil = time.Local
//	    Dispatcher: runnerDispatcher,   // required for dispatch; nil makes runs fail
//	})
//	if err != nil { ... }               // corrupt or newer-schema file
//	service.OnChange(func() {           // optional: browser notification
//	    broadcast.send(channel "automations:changed")
//	})
//	service.Start(ctx)                  // arms the single schedule timer
//	defer service.Close()               // stops it, releases claims, persists
//
// Dispatcher is implemented by the caller: one runner call per Command.
//
//	type Dispatcher interface {
//	    Dispatch(ctx context.Context, target Target, command Command) (sessionID string, err error)
//	}
//	type Target struct { WorkspacePath, WorkspaceIdentity string }
//	type Command struct {
//	    CommandID     string          // == run id: idempotent retries on the runner
//	    CreateSession bool            // true: createSession{firstInput}; false: sendText
//	    SessionID     string          // bound session for SendText (TargetTaskID)
//	    Text          string          // the automation prompt
//	    ModelSelection json.RawMessage // raw ModelSelection JSON, nil = workspace default
//	    Mode          *string          // permission mode, nil = workspace default
//	}
//
// The reference implementation sends nex-agent.sendConversationCommandV4 with
// envelope.commandId = Command.CommandID, sessionId = null, type createSession
// and payload.firstInput{text, modelSelection?, mode?} — or type sendText with
// payload.sessionId = Command.SessionID. Returning the ack's sessionId is what
// lands in the run history.
//
// Channel handling: HandlesCall(method) bool and Call(ctx, method, arg) — the
// ten nex-agent methods (see Methods) with the browser's positional `[arg]`
// payload. Results are the camelCase NexAutomation/NexAutomationRun JSON.
//
// Outcome feed: the window-controller calls
//
//	service.RecordOutcome(automations.Outcome{SessionID: row.SessionID, Status: row.Status, Error: row.Error})
//
// whenever a task row belonging to a run turns terminal (or changes at all;
// a terminal outcome is never downgraded).
//
// Everything is safe for concurrent use; no method may be called before New
// succeeds, and mutations after Close return an error.
