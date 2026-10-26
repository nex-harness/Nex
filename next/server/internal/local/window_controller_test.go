package local_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nex-harness/nex/server/internal/local"
	"github.com/nex-harness/nex/server/internal/rpcwire"
	"github.com/nex-harness/nex/server/internal/runners"
)

// fakeRunner serves nex-task list/mutation calls from an in-memory task table
// and lets tests fire dynamic workspace events.
type fakeRunner struct {
	mu       sync.Mutex
	tasks    map[string][]fakeTask
	calls    []string
	listens  map[string]func(json.RawMessage)
	callHook func(channel, method string, arg json.RawMessage)
	done     chan struct{}
}

type fakeTask struct {
	taskID    string
	title     string
	pinned    bool
	archived  bool
	createdAt int64
	updatedAt int64
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{tasks: map[string][]fakeTask{}, listens: map[string]func(json.RawMessage){}, done: make(chan struct{})}
}

func (f *fakeRunner) seed(path string, tasks ...fakeTask) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[path] = append(f.tasks[path], tasks...)
}

func (f *fakeRunner) setTasks(path string, tasks ...fakeTask) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[path] = tasks
}

func (f *fakeRunner) Call(_ context.Context, channel, method string, arg json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, channel+"."+method)
	hook := f.callHook
	f.mu.Unlock()
	if hook != nil {
		hook(channel, method, arg)
	}
	var params struct {
		WorkspacePath string   `json:"workspacePath"`
		TaskID        string   `json:"taskId"`
		TaskIDs       []string `json:"taskIds"`
	}
	_ = json.Unmarshal(arg, &params)

	switch method {
	case "listTasks", "listPinnedTasks", "listArchivedTasks":
		return f.list(params.WorkspacePath, method)
	case "deleteArchivedTask":
		return json.RawMessage(`true`), nil
	case "deleteArchivedTasks":
		payload := map[string]any{"deletedTaskIds": params.TaskIDs, "skippedTaskIds": []string{}, "failedTaskIds": []string{}}
		encoded, _ := json.Marshal(payload)
		return encoded, nil
	case "archiveTask", "unarchiveTask", "setTaskPinned", "setTaskUnread", "renameTask":
		return json.RawMessage(`{"taskId":"` + params.TaskID + `","traceId":"` + params.TaskID + `","title":"x","workspacePath":"` + params.WorkspacePath + `","createdAt":1,"updatedAt":2,"mode":"build"}`), nil
	}
	return json.RawMessage("null"), nil
}

func (f *fakeRunner) list(path, method string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := make([]map[string]any, 0)
	for _, task := range f.tasks[path] {
		switch method {
		case "listPinnedTasks":
			if !task.pinned || task.archived {
				continue
			}
		case "listArchivedTasks":
			if !task.archived {
				continue
			}
		default:
			if task.pinned || task.archived {
				continue
			}
		}
		items = append(items, map[string]any{
			"taskId":        task.taskID,
			"traceId":       task.taskID,
			"title":         task.title,
			"workspacePath": path,
			"createdAt":     task.createdAt,
			"updatedAt":     task.updatedAt,
			"mode":          "build",
			"status":        "completed",
		})
	}
	encoded, err := json.Marshal(items)
	return encoded, err
}

func (f *fakeRunner) Listen(id, _channel, _event string, _ json.RawMessage, emit func(json.RawMessage)) error {
	f.mu.Lock()
	f.listens[id] = emit
	f.mu.Unlock()
	return nil
}

func (f *fakeRunner) Unlisten(id string) {
	f.mu.Lock()
	delete(f.listens, id)
	f.mu.Unlock()
}

func (f *fakeRunner) Close() {}

func (f *fakeRunner) Done() <-chan struct{} { return f.done }

// fire emits one nex-task workspace event to every internal listen.
func (f *fakeRunner) fire(workspacePath string) {
	f.mu.Lock()
	listeners := make([]func(json.RawMessage), 0, len(f.listens))
	for _, emit := range f.listens {
		listeners = append(listeners, emit)
	}
	f.mu.Unlock()
	event, _ := json.Marshal(map[string]any{
		"type":          "workspace_task_list_changed",
		"workspacePath": workspacePath,
		"taskId":        "t1",
		"reason":        "task_archived",
	})
	for _, emit := range listeners {
		emit(event)
	}
}

func (f *fakeRunner) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			count++
		}
	}
	return count
}

func decodeListResult(t *testing.T, value rpcwire.Value) ([]map[string]any, int, bool) {
	t.Helper()
	raw, ok := value.(json.RawMessage)
	if !ok {
		t.Fatalf("listTaskList = %T, want json.RawMessage", value)
	}
	var result struct {
		Items   []map[string]any `json:"items"`
		Total   int              `json:"total"`
		HasMore bool             `json:"hasMore"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decoding listTaskList: %v (%s)", err, raw)
	}
	return result.Items, result.Total, result.HasMore
}

func listTaskList(t *testing.T, controller *local.WindowController, query map[string]any) ([]map[string]any, int, bool) {
	t.Helper()
	encoded, err := json.Marshal(query)
	if err != nil {
		t.Fatalf("encoding query: %v", err)
	}
	value, err := controller.Call(context.Background(), &local.Env{ConnectionID: "c"}, "listTaskList", json.RawMessage(encoded))
	if err != nil {
		t.Fatalf("listTaskList: %v", err)
	}
	return decodeListResult(t, value)
}

func newRegistryWithRunners(t *testing.T) (*runners.Registry, *fakeRunner, *fakeRunner) {
	t.Helper()
	registry := runners.NewRegistry(nil)
	alpha := newFakeRunner()
	beta := newFakeRunner()
	registry.Register(runners.Info{
		RunnerID:   "alpha",
		Workspaces: []runners.Workspace{{Path: "/w/one"}},
	}, alpha)
	registry.Register(runners.Info{
		RunnerID:   "beta",
		Workspaces: []runners.Workspace{{Path: "/w/two"}},
	}, beta)
	return registry, alpha, beta
}

func TestListTaskListFansOutMergesSortsSearchesAndLimits(t *testing.T) {
	registry, alpha, beta := newRegistryWithRunners(t)
	alpha.seed("/w/one",
		fakeTask{taskID: "a1", title: "Alpha notes", pinned: true, createdAt: 100, updatedAt: 900},
		fakeTask{taskID: "a2", title: "Alpha other", createdAt: 300, updatedAt: 500},
		fakeTask{taskID: "a3", title: "Alpha archived", archived: true, createdAt: 400, updatedAt: 800},
	)
	beta.seed("/w/two",
		fakeTask{taskID: "b1", title: "Beta pinned notes", pinned: true, createdAt: 200, updatedAt: 700},
		fakeTask{taskID: "b2", title: "Beta plain", createdAt: 500, updatedAt: 600},
	)
	controller := local.NewWindowController(registry, nil)
	scopes := []map[string]any{
		{"workspacePath": "/w/one", "workspaceIdentity": "runner:alpha:/w/one"},
		{"workspacePath": "/w/two", "workspaceIdentity": "runner:beta:/w/two"},
	}

	pinned, total, hasMore := listTaskList(t, controller, map[string]any{
		"kind": "pinned", "workspaceScopes": scopes, "sortBy": "updated",
	})
	if total != 2 || len(pinned) != 2 || hasMore {
		t.Fatalf("pinned = %d rows (total %d, hasMore %v), want 2", len(pinned), total, hasMore)
	}
	if ids := taskIDs(pinned); !equalStrings(ids, []string{"a1", "b1"}) {
		t.Fatalf("pinned order = %v, want updated desc [a1 b1]", ids)
	}
	for _, item := range pinned {
		if item["sourceAvailability"] != "online" || item["liveStatus"] != "completed" {
			t.Fatalf("controller fields missing: %v", item)
		}
		if item["workspaceIdentity"] == "" || item["workspaceIdentity"] == nil {
			t.Fatalf("row lost workspaceIdentity: %v", item)
		}
	}

	archived, total, _ := listTaskList(t, controller, map[string]any{
		"kind": "archived", "workspaceScopes": scopes, "sortBy": "created",
	})
	if total != 1 || !equalStrings(taskIDs(archived), []string{"a3"}) {
		t.Fatalf("archived = %v (total %d), want [a3]", taskIDs(archived), total)
	}

	timeline, total, _ := listTaskList(t, controller, map[string]any{
		"kind": "timeline", "workspaceScopes": scopes, "sortBy": "created",
	})
	if total != 2 || !equalStrings(taskIDs(timeline), []string{"b2", "a2"}) {
		t.Fatalf("timeline = %v (total %d), want created desc [b2 a2]", taskIDs(timeline), total)
	}

	// Search is a case-insensitive title substring across runners.
	searched, total, _ := listTaskList(t, controller, map[string]any{
		"kind": "pinned", "workspaceScopes": scopes, "sortBy": "updated", "search": "PINNED notes",
	})
	if total != 1 || !equalStrings(taskIDs(searched), []string{"b1"}) {
		t.Fatalf("search = %v (total %d), want [b1]", taskIDs(searched), total)
	}

	// Limit truncates but total/hasMore describe the full result.
	limited, total, hasMore := listTaskList(t, controller, map[string]any{
		"kind": "timeline", "workspaceScopes": scopes, "sortBy": "created", "limit": 1,
	})
	if len(limited) != 1 || total != 2 || !hasMore {
		t.Fatalf("limit = %d rows (total %d, hasMore %v), want 1/2/true", len(limited), total, hasMore)
	}
}

func TestListTaskListKeepsRowWhenOneRunnerFails(t *testing.T) {
	registry, alpha, beta := newRegistryWithRunners(t)
	alpha.seed("/w/one", fakeTask{taskID: "a1", title: "Alpha", createdAt: 1, updatedAt: 1})
	beta.seed("/w/two", fakeTask{taskID: "b1", title: "Beta", createdAt: 2, updatedAt: 2})
	beta.callHook = func(_channel, _method string, _arg json.RawMessage) {
		// The beta runner is "offline" for reads.
		registry.Unregister("beta", beta)
	}
	controller := local.NewWindowController(registry, nil)
	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind": "timeline",
		"workspaceScopes": []map[string]any{
			{"workspacePath": "/w/one", "workspaceIdentity": "runner:alpha:/w/one"},
			{"workspacePath": "/w/two", "workspaceIdentity": "runner:beta:/w/two"},
		},
		"sortBy": "updated",
	})
	if total != 1 || !equalStrings(taskIDs(items), []string{"a1"}) {
		t.Fatalf("timeline = %v (total %d), want [a1] with beta offline", taskIDs(items), total)
	}
}

func TestControllerRefreshesAndPushesSnapshotAfterRunnerEvent(t *testing.T) {
	registry, alpha, _ := newRegistryWithRunners(t)
	alpha.seed("/w/one", fakeTask{taskID: "a1", title: "Alpha", createdAt: 1, updatedAt: 1})
	controller := local.NewWindowController(registry, nil)
	env := &local.Env{ConnectionID: "conn-events"}
	frames := make(chan json.RawMessage, 16)
	if _, err := controller.Listen(context.Background(), env, "onDynamicControllerFrame", rpcwire.Undefined{}, func(value rpcwire.Value) {
		if raw, ok := value.(json.RawMessage); ok {
			frames <- raw
		}
	}); err != nil {
		t.Fatalf("listen: %v", err)
	}

	scope := map[string]any{"workspacePath": "/w/one", "workspaceIdentity": "runner:alpha:/w/one"}
	// First query subscribes the server to the runner's events and caches rows.
	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind": "timeline", "workspaceScopes": []map[string]any{scope}, "sortBy": "updated",
	})
	if total != 1 || !equalStrings(taskIDs(items), []string{"a1"}) {
		t.Fatalf("initial timeline = %v", taskIDs(items))
	}
	if _, err := controller.Call(context.Background(), env, "subscribeControllerV4",
		json.RawMessage(`[{"topic":"controller/tasks-index"}]`)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	<-frames // initial snapshot

	// A runner-side mutation arrives as a workspace event; the server must re-read
	// the workspace and push a snapshot with the new row.
	alpha.setTasks("/w/one",
		fakeTask{taskID: "a1", title: "Alpha", archived: true, createdAt: 1, updatedAt: 1},
		fakeTask{taskID: "a2", title: "New", createdAt: 3, updatedAt: 3},
	)
	alpha.fire("/w/one")

	frame := waitForTaskFrame(t, frames, func(row map[string]any) bool {
		return fmt.Sprint(row["taskId"]) == "a2"
	})
	if frame == "" {
		t.Fatal("controller did not push a snapshot containing the new row")
	}
	items, total, _ = listTaskList(t, controller, map[string]any{
		"kind": "timeline", "workspaceScopes": []map[string]any{scope}, "sortBy": "updated",
	})
	if total != 1 || !equalStrings(taskIDs(items), []string{"a2"}) {
		t.Fatalf("timeline after event = %v (total %d), want [a2]", taskIDs(items), total)
	}
}

func TestDeleteArchivedTaskForwardsToOwningRunner(t *testing.T) {
	registry, alpha, beta := newRegistryWithRunners(t)
	controller := local.NewWindowController(registry, nil)
	address := map[string]any{
		"workspacePath":     "/w/two",
		"workspaceIdentity": "runner:beta:/w/two",
		"taskId":            "b9",
	}
	encoded, _ := json.Marshal(map[string]any{"address": address})
	value, err := controller.Call(context.Background(), &local.Env{ConnectionID: "c"}, "deleteArchivedTask", json.RawMessage(encoded))
	if err != nil {
		t.Fatalf("deleteArchivedTask: %v", err)
	}
	if raw, ok := value.(json.RawMessage); !ok || string(raw) != "true" {
		t.Fatalf("deleteArchivedTask = %v, want true", value)
	}
	if beta.callCount("nex-task.deleteArchivedTask") != 1 {
		t.Fatal("delete was not forwarded to the owning runner")
	}
	if alpha.callCount("nex-task.deleteArchivedTask") != 0 {
		t.Fatal("delete leaked to a runner that does not own the scope")
	}

	batch, _ := json.Marshal(map[string]any{"address": address, "taskIds": []string{"b9"}})
	result, err := controller.Call(context.Background(), &local.Env{ConnectionID: "c"}, "deleteArchivedTasks", json.RawMessage(batch))
	if err != nil {
		t.Fatalf("deleteArchivedTasks: %v", err)
	}
	var decoded struct {
		Deleted []string `json:"deletedTaskIds"`
	}
	if raw, ok := result.(json.RawMessage); ok {
		_ = json.Unmarshal(raw, &decoded)
	}
	if !equalStrings(decoded.Deleted, []string{"b9"}) {
		t.Fatalf("deleteArchivedTasks = %v, want [b9]", result)
	}
}

// waitForTaskFrame drains frames until one is a tasks-index snapshot containing a row
// that satisfies predicate, returning the matching row's taskId.
func waitForTaskFrame(t *testing.T, frames chan json.RawMessage, predicate func(map[string]any) bool) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case raw := <-frames:
			var frame struct {
				Topic   string `json:"topic"`
				Payload struct {
					Kind     string `json:"kind"`
					Snapshot struct {
						Tasks []map[string]any `json:"tasks"`
					} `json:"snapshot"`
				} `json:"payload"`
			}
			if json.Unmarshal(raw, &frame) != nil {
				continue
			}
			if frame.Topic != "controller/tasks-index" || frame.Payload.Kind != "snapshot" {
				continue
			}
			for _, task := range frame.Payload.Snapshot.Tasks {
				// Controller rows nest the row identity under `meta`.
				meta, _ := task["meta"].(map[string]any)
				if predicate(meta) {
					return fmt.Sprint(meta["taskId"])
				}
			}
		case <-deadline:
			return ""
		}
	}
}

func taskIDs(items []map[string]any) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, fmt.Sprint(item["taskId"]))
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}
