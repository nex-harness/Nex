package local_test

// Wiring for the server-owned automations (docs/next/automations-views-search.md
// §A): the nex-agent table must answer the ten automation methods through the
// automations service instead of the empty-list placeholder, the dispatcher must
// build the runner command envelope, and the window-controller must feed task
// rows back as run outcomes.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nex-harness/nex/server/internal/automations"
	"github.com/nex-harness/nex/server/internal/local"
	"github.com/nex-harness/nex/server/internal/rpcwire"
	"github.com/nex-harness/nex/server/internal/runners"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// recordingDispatcher captures every dispatch and answers a fixed session id.
type recordingDispatcher struct {
	mu       sync.Mutex
	targets  []automations.Target
	commands []automations.Command
	session  string
	err      error
	done     chan struct{}
}

func (d *recordingDispatcher) Dispatch(_ context.Context, target automations.Target, command automations.Command) (string, error) {
	d.mu.Lock()
	d.targets = append(d.targets, target)
	d.commands = append(d.commands, command)
	err := d.err
	d.mu.Unlock()
	select {
	case d.done <- struct{}{}:
	default:
	}
	if err != nil {
		return "", err
	}
	return d.session, nil
}

func (d *recordingDispatcher) snapshot() ([]automations.Command, []automations.Target) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]automations.Command(nil), d.commands...), append([]automations.Target(nil), d.targets...)
}

// callNexAgent invokes one browser-shaped call: the argument is the positional
// `[arg]` array the ProxyChannel sends.
func callNexAgent(t *testing.T, table *local.NexAgentLocal, method string, args ...any) rpcwire.Value {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encoding args: %v", err)
	}
	value, err := table.Call(context.Background(), &local.Env{ConnectionID: "test"}, method, json.RawMessage(encoded))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return value
}

func decodeList(t *testing.T, value rpcwire.Value) []map[string]any {
	t.Helper()
	raw, ok := value.(json.RawMessage)
	if !ok {
		t.Fatalf("value = %T, want json.RawMessage", value)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("decoding list: %v (raw %s)", err, raw)
	}
	return items
}

func newWiringService(t *testing.T, dispatcher automations.Dispatcher) *automations.Service {
	t.Helper()
	service, err := automations.New(automations.Options{
		Dir:        t.TempDir(),
		Logger:     testLogger(),
		Dispatcher: dispatcher,
	})
	if err != nil {
		t.Fatalf("automations.New: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func TestNexAgentDelegatesAutomationMethods(t *testing.T) {
	dispatcher := &recordingDispatcher{session: "session-1", done: make(chan struct{}, 1)}
	service := newWiringService(t, dispatcher)
	table := local.NewNexAgentLocal(local.WithAutomations(service))

	for _, method := range automations.Methods {
		if !table.HandlesCall(method) {
			t.Fatalf("HandlesCall(%q) = false, want true", method)
		}
	}

	// 空列表而不是 "Method not found"：这是 UI 打开页面时的第一个调用。
	if items := decodeList(t, callNexAgent(t, table, "listAllAutomations")); len(items) != 0 {
		t.Fatalf("listAllAutomations = %v, want empty", items)
	}

	created := callNexAgent(t, table, "createAutomation", map[string]any{
		"title":                "nightly report",
		"cronExpr":             "0 3 * * *",
		"prompt":               "summarize today",
		"workspacePath":        "/work/app",
		"workspaceIdentity":    "runner:alpha:/work/app",
		"recurring":            true,
		"relativeDelayMinutes": json.RawMessage(`{"$undef":true}`),
	})
	raw, ok := created.(json.RawMessage)
	if !ok {
		t.Fatalf("createAutomation = %T", created)
	}
	var automation map[string]any
	if err := json.Unmarshal(raw, &automation); err != nil {
		t.Fatalf("decoding created automation: %v", err)
	}
	automationID, _ := automation["automationId"].(string)
	if automationID == "" {
		t.Fatalf("created automation has no id: %s", raw)
	}

	items := decodeList(t, callNexAgent(t, table, "listAllAutomations"))
	if len(items) != 1 || items[0]["automationId"] != automationID {
		t.Fatalf("listAllAutomations = %v, want the created automation", items)
	}
	if scoped := decodeList(t, callNexAgent(t, table, "listAutomations", map[string]any{
		"workspacePath":     "/work/app",
		"workspaceIdentity": "runner:alpha:/work/app",
	})); len(scoped) != 1 {
		t.Fatalf("listAutomations = %v, want the created automation", scoped)
	}

	// runAutomationNow 先认领，异步派发。
	result := callNexAgent(t, table, "runAutomationNow", map[string]any{
		"automationId":      automationID,
		"workspacePath":     "/work/app",
		"workspaceIdentity": "runner:alpha:/work/app",
	})
	if string(result.(json.RawMessage)) != `{"status":"queued"}` {
		t.Fatalf("runAutomationNow = %s, want queued", result)
	}
	select {
	case <-dispatcher.done:
	case <-time.After(5 * time.Second):
		t.Fatal("manual run was never dispatched")
	}
	commands, targets := dispatcher.snapshot()
	command := commands[len(commands)-1]
	if !command.CreateSession || command.Text != "summarize today" {
		t.Fatalf("dispatch command = %+v, want createSession with the prompt", command)
	}
	if target := targets[len(targets)-1]; target.WorkspaceIdentity != "runner:alpha:/work/app" || target.WorkspacePath != "/work/app" {
		t.Fatalf("dispatch target = %+v", target)
	}

	runs := decodeList(t, callNexAgent(t, table, "listAutomationRuns", map[string]any{
		"automationId":      automationID,
		"workspacePath":     "/work/app",
		"workspaceIdentity": "runner:alpha:/work/app",
	}))
	if len(runs) != 1 || runs[0]["dispatchStatus"] != "dispatched" || runs[0]["sessionId"] != "session-1" {
		t.Fatalf("listAutomationRuns = %v, want one dispatched run on session-1", runs)
	}

	// Single flight: a second immediate run while the first is in flight reports
	// duplicate only while claimed; here the claim was released, so it queues.
	if string(callNexAgent(t, table, "deleteAutomation", map[string]any{
		"automationId":      automationID,
		"workspacePath":     "/work/app",
		"workspaceIdentity": "runner:alpha:/work/app",
	}).(json.RawMessage)) != "null" {
		t.Fatal("deleteAutomation did not answer null")
	}
	if items := decodeList(t, callNexAgent(t, table, "listAllAutomations")); len(items) != 0 {
		t.Fatalf("listAllAutomations after delete = %v, want empty", items)
	}
}

func TestNexAgentWithoutAutomationsKeepsPlaceholder(t *testing.T) {
	table := local.NewNexAgentLocal()
	if items := decodeList(t, callNexAgent(t, table, "listAllAutomations")); len(items) != 0 {
		t.Fatalf("placeholder listAllAutomations = %v, want empty", items)
	}
	if table.HandlesCall("createAutomation") {
		t.Fatal("without a delegate the table must not claim createAutomation")
	}
}

func TestRunnerAutomationsDispatcherEnvelope(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	link := &captureLink{reply: json.RawMessage(`{"commandId":"run-1","status":"accepted","revisionAtDecision":1,"result":{"type":"createSession","sessionId":"session-9"}}`)}
	registry.Register(runners.Info{
		RunnerID:   "alpha",
		Name:       "alpha",
		OS:         "linux",
		Workspaces: []runners.Workspace{{Path: "/work/app"}},
	}, link)
	dispatcher := local.NewRunnerAutomationsDispatcher(registry)
	mode := "acceptEdits"
	modelSelection := json.RawMessage(`{"providerId":"anthropic","modelId":"claude","options":{"thinking":true}}`)

	sessionID, err := dispatcher.Dispatch(context.Background(), automations.Target{
		WorkspacePath:     "/work/app",
		WorkspaceIdentity: "runner:alpha:/work/app",
	}, automations.Command{
		CommandID:      "auto-1:manual:run-1",
		CreateSession:  true,
		Text:           "summarize today",
		ModelSelection: modelSelection,
		Mode:           &mode,
		AutomationID:   "auto-1",
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sessionID != "session-9" {
		t.Fatalf("sessionId = %q, want session-9", sessionID)
	}

	channel, method, arg := link.lastCall()
	if channel != "nex-agent" || method != "sendConversationCommandV4" {
		t.Fatalf("call = %s.%s", channel, method)
	}
	// The runner's call table reads the ProxyChannel positional envelope.
	var positional []map[string]json.RawMessage
	if err := json.Unmarshal(arg, &positional); err != nil || len(positional) != 1 {
		t.Fatalf("argument = %s, want one positional element", arg)
	}
	if got := string(positional[0]["workspacePath"]); got != `"/work/app"` {
		t.Fatalf("workspacePath = %s", got)
	}
	var envelope struct {
		CommandID string `json:"commandId"`
		ClientID  string `json:"clientId"`
		SessionID *string
		Type      string `json:"type"`
		IssuedAt  int64  `json:"issuedAt"`
		Payload   struct {
			FirstInput struct {
				Text           string          `json:"text"`
				ModelSelection json.RawMessage `json:"modelSelection"`
				Mode           string          `json:"mode"`
				AutomationID   string          `json:"automationId"`
			} `json:"firstInput"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(positional[0]["envelope"], &envelope); err != nil {
		t.Fatalf("decoding envelope: %v", err)
	}
	if envelope.CommandID != "auto-1:manual:run-1" || envelope.Type != "createSession" || envelope.SessionID != nil {
		t.Fatalf("envelope identity = %+v", envelope)
	}
	if envelope.ClientID == "" || envelope.IssuedAt == 0 {
		t.Fatalf("envelope missing clientId/issuedAt: %+v", envelope)
	}
	if envelope.Payload.FirstInput.Text != "summarize today" ||
		envelope.Payload.FirstInput.Mode != "acceptEdits" ||
		envelope.Payload.FirstInput.AutomationID != "auto-1" {
		t.Fatalf("firstInput = %+v", envelope.Payload.FirstInput)
	}
	// The selection round-trips opaquely; key order follows encoding/json.
	var selection map[string]any
	if err := json.Unmarshal(envelope.Payload.FirstInput.ModelSelection, &selection); err != nil {
		t.Fatalf("modelSelection: %v", err)
	}
	if selection["providerId"] != "anthropic" || selection["modelId"] != "claude" {
		t.Fatalf("modelSelection = %v", selection)
	}
}

func TestRunnerAutomationsDispatcherRejectedAck(t *testing.T) {
	registry := runners.NewRegistry(testLogger())
	link := &captureLink{reply: json.RawMessage(`{"commandId":"r","status":"rejected","reasonCode":"session_required","message":"needs sessionId","revisionAtDecision":0}`)}
	registry.Register(runners.Info{RunnerID: "alpha", Name: "alpha", OS: "linux",
		Workspaces: []runners.Workspace{{Path: "/work/app"}}}, link)
	dispatcher := local.NewRunnerAutomationsDispatcher(registry)

	if _, err := dispatcher.Dispatch(context.Background(),
		automations.Target{WorkspacePath: "/work/app", WorkspaceIdentity: "runner:alpha:/work/app"},
		automations.Command{CommandID: "r", CreateSession: true, Text: "x"}); err == nil {
		t.Fatal("a rejected ack must be a dispatch failure")
	}

	// sendText travels with envelope.sessionId and a top-level text payload.
	link.reply = json.RawMessage(`{"commandId":"s","status":"accepted","revisionAtDecision":1,"result":{"type":"inputAccepted","sessionId":"session-2"}}`)
	sessionID, err := dispatcher.Dispatch(context.Background(),
		automations.Target{WorkspacePath: "/work/app", WorkspaceIdentity: "runner:alpha:/work/app"},
		automations.Command{CommandID: "s", CreateSession: false, SessionID: "session-2", Text: "again"})
	if err != nil {
		t.Fatalf("sendText dispatch: %v", err)
	}
	if sessionID != "session-2" {
		t.Fatalf("sessionId = %q", sessionID)
	}
	_, _, arg := link.lastCall()
	var positional []map[string]json.RawMessage
	if err := json.Unmarshal(arg, &positional); err != nil {
		t.Fatalf("argument: %v", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(positional[0]["envelope"], &envelope); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if string(envelope["type"]) != `"sendText"` || string(envelope["sessionId"]) != `"session-2"` {
		t.Fatalf("sendText envelope = %s", positional[0]["envelope"])
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(envelope["payload"], &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if string(payload["text"]) != `"again"` {
		t.Fatalf("sendText payload = %s", envelope["payload"])
	}
}

func TestWindowControllerFeedsAutomationOutcomes(t *testing.T) {
	dispatcher := &recordingDispatcher{session: "session-row", done: make(chan struct{}, 1)}
	service := newWiringService(t, dispatcher)
	table := local.NewNexAgentLocal(local.WithAutomations(service))

	created := callNexAgent(t, table, "createAutomation", map[string]any{
		"title":             "nightly report",
		"cronExpr":          "0 3 * * *",
		"prompt":            "summarize today",
		"workspacePath":     "/work/app",
		"workspaceIdentity": "runner:alpha:/work/app",
		"recurring":         true,
	})
	var automation map[string]any
	if err := json.Unmarshal(created.(json.RawMessage), &automation); err != nil {
		t.Fatalf("decoding automation: %v", err)
	}
	automationID, _ := automation["automationId"].(string)

	callNexAgent(t, table, "runAutomationNow", map[string]any{
		"automationId":      automationID,
		"workspacePath":     "/work/app",
		"workspaceIdentity": "runner:alpha:/work/app",
	})
	select {
	case <-dispatcher.done:
	case <-time.After(5 * time.Second):
		t.Fatal("manual run was never dispatched")
	}

	registry := runners.NewRegistry(testLogger())
	link := &captureLink{}
	link.listTasks = json.RawMessage(`[{"taskId":"session-row","workspacePath":"/work/app","workspaceIdentity":"runner:alpha:/work/app","status":"completed","title":"nightly report","createdAt":1,"updatedAt":2}]`)
	registry.Register(runners.Info{RunnerID: "alpha", Name: "alpha", OS: "linux",
		Workspaces: []runners.Workspace{{Path: "/work/app"}}}, link)
	controller := local.NewWindowController(registry, testLogger()).WithAutomationOutcomes(service)

	if _, err := controller.Call(context.Background(), &local.Env{ConnectionID: "ui"}, "listTaskList", json.RawMessage(`[{"workspaceScopes":[{"workspacePath":"/work/app","workspaceIdentity":"runner:alpha:/work/app"}]}]`)); err != nil {
		t.Fatalf("listTaskList: %v", err)
	}

	runs := decodeList(t, callNexAgent(t, table, "listAutomationRuns", map[string]any{
		"automationId":      automationID,
		"workspacePath":     "/work/app",
		"workspaceIdentity": "runner:alpha:/work/app",
	}))
	if len(runs) != 1 || runs[0]["outcome"] != "succeeded" {
		t.Fatalf("runs after the terminal row = %v, want outcome succeeded", runs)
	}
}

// captureLink is a fake runner link that records calls and answers with reply.
type captureLink struct {
	mu        sync.Mutex
	reply     json.RawMessage
	listTasks json.RawMessage
	calls     []capturedCall
}

type capturedCall struct {
	channel string
	method  string
	arg     json.RawMessage
}

func (l *captureLink) Call(_ context.Context, channel, method string, arg json.RawMessage) (json.RawMessage, error) {
	l.mu.Lock()
	l.calls = append(l.calls, capturedCall{channel: channel, method: method, arg: arg})
	reply, listTasks := l.reply, l.listTasks
	l.mu.Unlock()
	if method == "listTasks" {
		if listTasks == nil {
			return json.RawMessage("[]"), nil
		}
		return listTasks, nil
	}
	if method == "listPinnedTasks" || method == "listArchivedTasks" {
		return json.RawMessage("[]"), nil
	}
	if reply == nil {
		return json.RawMessage("null"), nil
	}
	return reply, nil
}

func (l *captureLink) Listen(string, string, string, json.RawMessage, func(json.RawMessage)) error {
	return nil
}
func (l *captureLink) Unlisten(string) {}
func (l *captureLink) Close()          {}
func (l *captureLink) Done() <-chan struct{} {
	closed := make(chan struct{})
	close(closed)
	return closed
}

func (l *captureLink) lastCall() (string, string, json.RawMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.calls) == 0 {
		return "", "", nil
	}
	last := l.calls[len(l.calls)-1]
	return last.channel, last.method, last.arg
}
