package runner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
)

type bgLifecycleMockProvider struct {
	mu       sync.Mutex
	requests []*ai.StreamRequest
	handler  func(callIdx int, ctx context.Context, req *ai.StreamRequest, ch chan<- ai.StreamEvent)
}

func (p *bgLifecycleMockProvider) Driver() ai.Driver { return ai.DriverAnthropic }

func (p *bgLifecycleMockProvider) Stream(ctx context.Context, req *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
	p.mu.Lock()
	idx := len(p.requests)
	reqCopy := *req
	reqCopy.Messages = append([]ai.Message(nil), req.Messages...)
	p.requests = append(p.requests, &reqCopy)
	fn := p.handler
	p.mu.Unlock()

	ch := make(chan ai.StreamEvent, 8)
	go func() {
		defer close(ch)
		fn(idx, ctx, &reqCopy, ch)
	}()
	return ch, nil
}

func extractMsgText(msg ai.Message) string {
	var sb strings.Builder
	for _, b := range msg.Content {
		if b.Text != "" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func (p *bgLifecycleMockProvider) capturedRequests() []*ai.StreamRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*ai.StreamRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

func TestBackgroundBashSurvivesTurnEndAndWakesParentWithContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workDir := t.TempDir()
	wakeDone := make(chan struct{}, 1)

	prov := &bgLifecycleMockProvider{}
	prov.handler = func(callIdx int, _ context.Context, _ *ai.StreamRequest, ch chan<- ai.StreamEvent) {
		switch callIdx {
		case 0:
			msg := &ai.Message{
				Role: ai.RoleAssistant,
				Content: []ai.ContentBlock{
					ai.ToolCallBlock("call_bg_1", "bash", json.RawMessage(`{"command":"sleep 0.25 && printf 'gh-run-ok\\n'","description":"Watch GitHub Actions","run_in_background":true}`)),
				},
			}
			ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonToolUse}
		case 1:
			msg := &ai.Message{
				Role:    ai.RoleAssistant,
				Content: []ai.ContentBlock{ai.TextBlock("Started background watcher.")},
			}
			ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: "Started background watcher."}
			ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn}
		default:
			msg := &ai.Message{
				Role:    ai.RoleAssistant,
				Content: []ai.ContentBlock{ai.TextBlock("Notified parent of background completion.")},
			}
			ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: "Notified parent of background completion."}
			ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonEndTurn}
			select {
			case wakeDone <- struct{}{}:
			default:
			}
		}
	}

	node, err := NewNode(Config{
		RunnerID:         "run_bg_wake",
		Name:             "BG Wake Runner",
		DataDir:          t.TempDir(),
		WorkspaceDefault: workDir,
		ProviderOverride: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	wiring := AttachDefaultSubsystems(node)
	defer wiring.Close()

	sessID := "sess_bg_wake_1"
	_, err = node.ExecutePrompt(ctx, protocol.PromptPayload{
		SessionID:      sessID,
		Text:           "Watch CI run",
		WorkspacePath:  workDir,
		ProviderID:     "prov_custom",
		ModelID:        "model_custom",
		ThinkingEffort: ai.ThinkingHigh,
		PermissionMode: string(agent.PermissionFullAccess),
	}, "chat", false)
	if err != nil {
		t.Fatalf("ExecutePrompt turn 1 failed: %v", err)
	}

	// Foreground turn has ended; the detached background bash should STILL be running!
	running := node.listSessionBackgroundTasks(sessID)
	if len(running) != 1 || running[0].Kind != "local_bash" {
		t.Fatalf("expected 1 running local_bash after turn end, got %+v", running)
	}

	// Wait for the background command to finish and automatically wake the parent session.
	select {
	case <-wakeDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for parent wake-up turn after background bash completion")
	}

	// Wait for the wake-up turn to finish recording events/blobs and settle to idle.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(node.listSessionBackgroundTasks(sessID)) == 0 {
			rec, _ := node.Store().GetSession(ctx, sessID)
			evsCheck, _ := node.Store().ListSessionEventsAfter(ctx, sessID, 0)
			agentEnds := 0
			for _, ev := range evsCheck {
				if ev.EventType == string(agent.EventAgentEnd) {
					agentEnds++
				}
			}
			if rec != nil && rec.Status == "idle" && agentEnds >= 2 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := node.listSessionBackgroundTasks(sessID); len(got) != 0 {
		t.Fatalf("expected 0 running background tasks after completion, got %+v", got)
	}

	reqs := prov.capturedRequests()
	if len(reqs) < 3 {
		t.Fatalf("expected >= 3 model requests (start tool, turn end, background wake-up), got %d", len(reqs))
	}
	wakeReq := reqs[2]
	if wakeReq.Model != "model_custom" {
		t.Fatalf("wake-up turn lost parent model context: got %q, want %q", wakeReq.Model, "model_custom")
	}
	if wakeReq.ThinkingEffort != ai.ThinkingHigh {
		t.Fatalf("wake-up turn lost parent ThinkingEffort: got %q, want %q", wakeReq.ThinkingEffort, ai.ThinkingHigh)
	}
	if !strings.Contains(wakeReq.FullSystemPrompt(), workDir) {
		t.Fatalf("wake-up turn system prompt missing parent workspace %q", workDir)
	}

	lastMsgText := ""
	if len(wakeReq.Messages) > 0 {
		lastMsgText = extractMsgText(wakeReq.Messages[len(wakeReq.Messages)-1])
	}
	if !strings.Contains(lastMsgText, "gh-run-ok") || !strings.Contains(lastMsgText, "[Background command") {
		t.Fatalf("expected wake-up message to contain background command notification and output, got %q", lastMsgText)
	}

	evs, err := node.Store().ListSessionEventsAfter(ctx, sessID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawRunningUpdate, sawClearedUpdate bool
	for _, ev := range evs {
		if ev.EventType != string(agent.EventBackgroundTasksUpdated) {
			continue
		}
		var parsed struct {
			Tasks []protocol.BackgroundTaskInfo `json:"tasks"`
		}
		if json.Unmarshal(ev.Payload, &parsed) == nil {
			if len(parsed.Tasks) == 1 && parsed.Tasks[0].Kind == "local_bash" {
				sawRunningUpdate = true
			}
			if len(parsed.Tasks) == 0 {
				sawClearedUpdate = true
			}
		}
	}
	if !sawRunningUpdate || !sawClearedUpdate {
		t.Fatalf("expected both running and cleared background_tasks_updated events (sawRunning=%v, sawCleared=%v)", sawRunningUpdate, sawClearedUpdate)
	}
}

func TestForegroundAbortDoesNotKillDetachedBackgroundTasksAndIndividualStopWorks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workDir := t.TempDir()
	foregroundBusy := make(chan struct{}, 1)

	prov := &bgLifecycleMockProvider{}
	prov.handler = func(callIdx int, callCtx context.Context, req *ai.StreamRequest, ch chan<- ai.StreamEvent) {
		userText := ""
		if len(req.Messages) > 0 {
			userText = extractMsgText(req.Messages[0])
		}
		if strings.Contains(userText, "watch-gh-action-subagent") {
			<-callCtx.Done()
			ch <- ai.StreamEvent{Type: ai.StreamEventError, Err: callCtx.Err()}
			return
		}

		switch callIdx {
		case 0:
			msg := &ai.Message{
				Role: ai.RoleAssistant,
				Content: []ai.ContentBlock{
					ai.ToolCallBlock("call_bash_long", "bash", json.RawMessage(`{"command":"sleep 30","description":"Long watcher","run_in_background":true}`)),
					ai.ToolCallBlock("call_sub_long", "subagent", json.RawMessage(`{"prompt":"watch-gh-action-subagent","agent_type":"explore","description":"Watch GH Action","run_in_background":true}`)),
				},
			}
			ch <- ai.StreamEvent{Type: ai.StreamEventDone, Message: msg, StopReason: ai.StopReasonToolUse}
		default:
			select {
			case foregroundBusy <- struct{}{}:
			default:
			}
			<-callCtx.Done()
			ch <- ai.StreamEvent{Type: ai.StreamEventError, Err: callCtx.Err()}
		}
	}

	node, err := NewNode(Config{
		RunnerID:         "run_bg_abort",
		Name:             "BG Abort Runner",
		DataDir:          t.TempDir(),
		WorkspaceDefault: workDir,
		ProviderOverride: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	wiring := AttachDefaultSubsystems(node)
	defer wiring.Close()

	sessID := "sess_bg_abort_1"
	turnDone := make(chan error, 1)
	go func() {
		_, err := node.ExecutePrompt(ctx, protocol.PromptPayload{
			SessionID:      sessID,
			Text:           "Launch background watchers and keep working",
			WorkspacePath:  workDir,
			PermissionMode: string(agent.PermissionFullAccess),
		}, "chat", false)
		turnDone <- err
	}()

	// Wait until foreground turn has spawned both background tasks and is blocked in step 1.
	select {
	case <-foregroundBusy:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for foreground turn to become busy")
	}

	deadline := time.Now().Add(2 * time.Second)
	var running []protocol.BackgroundTaskInfo
	for time.Now().Before(deadline) {
		running = node.listSessionBackgroundTasks(sessID)
		if len(running) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(running) != 2 {
		t.Fatalf("expected 2 running background tasks (1 bash + 1 subagent), got %+v", running)
	}

	// Abort the foreground turn: foreground should exit, detached tasks MUST survive!
	node.AbortSession(ctx, sessID)
	select {
	case <-turnDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("foreground turn did not exit after AbortSession")
	}

	time.Sleep(100 * time.Millisecond)
	surviving := node.listSessionBackgroundTasks(sessID)
	if len(surviving) != 2 {
		t.Fatalf("expected both detached background tasks to survive AbortSession, got %+v", surviving)
	}

	var bashTaskID, agentTaskID string
	for _, tk := range surviving {
		switch tk.Kind {
		case "local_bash":
			bashTaskID = tk.TaskID
		case "local_agent":
			agentTaskID = tk.TaskID
		}
	}
	if bashTaskID == "" || agentTaskID == "" {
		t.Fatalf("expected one local_bash and one local_agent, got %+v", surviving)
	}

	// Stop each detached task individually via StopBackgroundTask.
	stopped, err := node.StopBackgroundTask(ctx, sessID, bashTaskID, "user")
	if err != nil || !stopped {
		t.Fatalf("StopBackgroundTask(bash) failed: stopped=%v err=%v", stopped, err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(node.listSessionBackgroundTasks(sessID)) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	afterBashStop := node.listSessionBackgroundTasks(sessID)
	if len(afterBashStop) != 1 || afterBashStop[0].TaskID != agentTaskID {
		t.Fatalf("expected only background subagent remaining after stopping bash, got %+v", afterBashStop)
	}

	stopped, err = node.StopBackgroundTask(ctx, sessID, agentTaskID, "user")
	if err != nil || !stopped {
		t.Fatalf("StopBackgroundTask(subagent) failed: stopped=%v err=%v", stopped, err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(node.listSessionBackgroundTasks(sessID)) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final := node.listSessionBackgroundTasks(sessID); len(final) != 0 {
		t.Fatalf("expected 0 remaining background tasks after individual stops, got %+v", final)
	}
}
