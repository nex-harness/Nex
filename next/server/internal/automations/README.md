# automations

Server-owned automation definitions, schedules and run history for nex-next
(spec: `docs/next/automations-views-search.md` §A).

The server owns them because runners can be offline and the page lists every
automation globally. State is one file, `<Dir>/automations.json` (schema 1,
atomic tmp+rename), with at most 20 definitions and 200 runs per automation
(oldest pruned). A single timer is armed to the earliest `nextRunAt` and
re-armed after every mutation — there is no polling loop.

## Integration surface

```go
service, err := automations.New(automations.Options{
    Dir:        configDir,        // required; holds automations.json
    Logger:     logger,           // optional (nil = silent)
    Now:        nil,              // optional clock (nil = time.Now)
    Location:   nil,              // create-time zone (nil = time.Local)
    Dispatcher: dispatcher,       // required for dispatch; nil = runs fail
})
if err != nil { /* corrupt or newer-schema file */ }

service.OnChange(func() { /* e.g. broadcast "automations:changed" */ })
service.Start(ctx)                    // arm the timer; idempotent
defer service.Close()                 // stop, release claims, persist
```

`New` reads the store eagerly, so a corrupt document fails construction
instead of being overwritten. Mutations after `Close` return an error.

### Dispatcher

One runner call per run:

```go
type Dispatcher interface {
    Dispatch(ctx context.Context, target Target, command Command) (sessionID string, err error)
}

type Target struct{ WorkspacePath, WorkspaceIdentity string }

type Command struct {
    CommandID      string          // == run id; retries are idempotent on the runner
    CreateSession  bool            // true: createSession{firstInput}; false: sendText
    SessionID      string          // bound session for sendText (automation.targetTaskId)
    Text           string          // the automation prompt
    ModelSelection json.RawMessage // raw ModelSelection JSON, nil = workspace default
    Mode           *string         // permission mode, nil = workspace default
}
```

Reference implementation: `nex-agent.sendConversationCommandV4` with
`envelope.commandId = Command.CommandID`, `sessionId: null`,
`type: "createSession"`, `payload.firstInput{text, modelSelection?, mode?}`;
or `type: "sendText"` with `payload.sessionId = Command.SessionID`. Return the
ack's `sessionId`; it is recorded on the run.

### Channel

The browser methods are served by `Service.Call(ctx, method, arg)` (arg is the
positional `[arg]` array from the RPC client) with `Service.HandlesCall` and
`Methods`:

`listAllAutomations`, `listAutomations`, `createAutomation`, `updateAutomation`,
`deleteAutomation`, `setAutomationEnabled`, `restartAutomation`,
`runAutomationNow`, `listAutomationRuns`, `deleteAutomationRun`.

Results are the camelCase `NexAutomation` / `NexAutomationRun` JSON the UI
renders; `runAutomationNow` answers `{"status":"queued"}` or
`{"status":"duplicate"}` (a manual run is already in flight). JavaScript
`undefined` arrives as `{"$undef":true}` and is treated as "not submitted".

### Outcomes

When a run's task row turns terminal, feed it back:

```go
service.RecordOutcome(automations.Outcome{SessionID: row.SessionID, Status: row.Status, Error: row.Error})
```

Statuses map as `succeeded|success|completed|done → succeeded`,
`failed|error|errored → failed`, `stopped|cancelled|aborted|interrupted → stopped`,
`running|queued|pending|starting → running`. A terminal outcome is never
downgraded back to running.

## Behaviour

| Situation | Result |
| --- | --- |
| Fire time missed by > 5 min (server was down) | run `skipped` (`computer_asleep_or_app_not_running`), schedule advances; a one-shot completes |
| Runner offline at fire time | retry 60 s, 3 attempts, then `failed_to_dispatch`; recurring keeps its schedule, a one-shot becomes `failed` + disabled (Restart revives) |
| Crash between claim and dispatch | the claim is persisted before the runner call, so a restart never re-fires that window |
| Concurrent `runAutomationNow` | `{"status":"duplicate"}` while a run (manual or scheduled) is in flight |
| `maxRuns` / `endAt` | counted from schedule-triggered dispatches only; manual runs never end a plan early |
| Pause / resume | `setAutomationEnabled` sets `paused` / `active`; the timer follows |

Every dispatch uses `commandId = runId`, so a retried call is deduplicated by
the runner.

## Scheduling math

`cronExpr` is validated with `robfig/cron/v3` (parse only) and matched on
calendar fields; `scheduleRule` is a direct port of
`packages/services/src/session/automationCron.ts`. Both resolve local wall
times with V8's `new Date(fields)` rules — gap readings shift **forward** by
the gap, ambiguous readings use the **earlier** offset — and the cron walk
reproduces croner 10.0.1 (a skipped hour fires once at its shifted instant, an
ambiguous minute fires once, and a `from` inside an hour's second pass rewinds
into the first pass).

Parity is pinned by `testdata/nextrun.json` (360 cases generated from the real
TypeScript implementation by `testdata/gen.ts`), plus explicit DST tests in
`parity_test.go`. Regenerate with:

```sh
PATH=/Users/tylinux/.nvm/versions/node/v24.21.0/bin:$PATH \
  node_modules/.bin/tsx next/server/internal/automations/testdata/gen.ts
```

## Files

| File | Contents |
| --- | --- |
| `types.go` | package doc, wire types (`Automation`, `Run`, `ScheduleRule`) |
| `store.go` | JSON document, atomic write, run-history pruning |
| `automations.go` | `Service` lifecycle, timer, change notifications |
| `dispatch.go` | tick, claim, dispatch |
| `settle.go` | dispatch success/failure settlement and retry policy |
| `cron.go` | 5-field cron parsing and croner-compatible matching |
| `schedule.go` | `scheduleRule` calendar math |
| `v8date.go` | V8 `Date` DST emulation (`instantOf`) |
| `validate.go`, `create.go`, `carrier.go` | validation and create resolution |
| `crud.go`, `runs.go`, `lifecycle.go`, `scope.go` | definitions and run history |
| `channel_call.go`, `channel.go` | browser channel |
| `outcome.go` | task-row status → run outcome |
| `doc.go` | integration surface (also in `automations.go`'s API) |

Tests: `go test ./internal/automations/... -race`.
