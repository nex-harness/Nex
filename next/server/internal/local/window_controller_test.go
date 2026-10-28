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
	// searchTasks 的桩：命中按 workspacePath 隔离，方便断言跨 scope 不串。
	searchMatches map[string][]fakeSearchMatch
	searchDelay   time.Duration
	searchErr     error
	searchCalls   []string
}

// fakeSearchMatch is one `searchTasks` hit; snippets are optional so a
// title-only hit carries no snippet fields.
type fakeSearchMatch struct {
	taskID   string
	snippet  string
	snippets []string
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
	return &fakeRunner{
		tasks:         map[string][]fakeTask{},
		listens:       map[string]func(json.RawMessage){},
		searchMatches: map[string][]fakeSearchMatch{},
		done:          make(chan struct{}),
	}
}

// setSearch installs the runner's searchTasks hits for one workspace.
func (f *fakeRunner) setSearch(path string, matches ...fakeSearchMatch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchMatches[path] = matches
}

// searchTaskCalls lists the `searchTasks` query strings this runner saw.
func (f *fakeRunner) searchTaskCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.searchCalls...)
}

// search serves `nex-task.searchTasks`. The delay respects ctx so a test can
// exercise the controller's per-runner timeout (the real link also abandons the
// wait when its context ends).
func (f *fakeRunner) search(ctx context.Context, path, query string) (json.RawMessage, error) {
	f.mu.Lock()
	delay := f.searchDelay
	err := f.searchErr
	matches := append([]fakeSearchMatch(nil), f.searchMatches[path]...)
	f.mu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		item := map[string]any{"taskId": match.taskID}
		if match.snippet != "" {
			item["searchSnippet"] = match.snippet
		}
		if len(match.snippets) > 0 {
			item["searchSnippets"] = match.snippets
		}
		items = append(items, item)
	}
	payload := map[string]any{"matches": items, "total": len(items), "hasMore": false}
	return json.Marshal(payload)
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

func (f *fakeRunner) Call(ctx context.Context, channel, method string, arg json.RawMessage) (json.RawMessage, error) {
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
		Query         string   `json:"query"`
	}
	_ = json.Unmarshal(arg, &params)

	switch method {
	case "listTasks", "listPinnedTasks", "listArchivedTasks":
		return f.list(params.WorkspacePath, method)
	case "searchTasks":
		f.mu.Lock()
		f.searchCalls = append(f.searchCalls, params.Query)
		f.mu.Unlock()
		return f.search(ctx, params.WorkspacePath, params.Query)
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

// ── full-text search fan-out (docs/next/automations-views-search.md §C) ──

func searchScopes(scopePairs ...[2]string) []map[string]any {
	scopes := make([]map[string]any, 0, len(scopePairs))
	for _, pair := range scopePairs {
		scopes = append(scopes, map[string]any{"workspacePath": pair[0], "workspaceIdentity": pair[1]})
	}
	return scopes
}

// TestListTaskListSearchIncludesTranscriptOnlyMatchWithSnippets 覆盖：正文命中
// 但标题不含关键词的行仍要出现，并带上 runner 的摘要字段。
func TestListTaskListSearchIncludesTranscriptOnlyMatchWithSnippets(t *testing.T) {
	registry, alpha, _ := newRegistryWithRunners(t)
	alpha.seed("/w/one",
		fakeTask{taskID: "a1", title: "Alpha notes", createdAt: 1, updatedAt: 10},
		fakeTask{taskID: "a2", title: "Unrelated", createdAt: 2, updatedAt: 20},
	)
	alpha.setSearch("/w/one", fakeSearchMatch{
		taskID:   "a2",
		snippet:  "...the needle is here...",
		snippets: []string{"...the needle is here...", "needle again"},
	})
	controller := local.NewWindowController(registry, nil)

	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind":            "timeline",
		"workspaceScopes": searchScopes([2]string{"/w/one", "runner:alpha:/w/one"}),
		"sortBy":          "updated",
		"search":          "needle",
	})
	if total != 1 || !equalStrings(taskIDs(items), []string{"a2"}) {
		t.Fatalf("search = %v (total %d), want transcript-only [a2]", taskIDs(items), total)
	}
	if items[0]["searchSnippet"] != "...the needle is here..." {
		t.Fatalf("searchSnippet = %v, want the runner snippet", items[0]["searchSnippet"])
	}
	snippets, _ := items[0]["searchSnippets"].([]any)
	if len(snippets) != 2 || snippets[0] != "...the needle is here..." || snippets[1] != "needle again" {
		t.Fatalf("searchSnippets = %v, want both runner snippets", items[0]["searchSnippets"])
	}
	if calls := alpha.searchTaskCalls(); len(calls) != 1 || calls[0] != "needle" {
		t.Fatalf("runner searchTasks calls = %v, want one for %q", calls, "needle")
	}
}

// TestListTaskListSearchTitleOnlyMatchHasNoSnippets 覆盖：标题命中但没有正文
// 命中时，行不带任何摘要字段。
func TestListTaskListSearchTitleOnlyMatchHasNoSnippets(t *testing.T) {
	registry, alpha, _ := newRegistryWithRunners(t)
	alpha.seed("/w/one", fakeTask{taskID: "a1", title: "Needle in title", createdAt: 1, updatedAt: 10})
	// runner 也返回了同名任务，但没给摘要（runner 侧标题命中不带摘要）。
	alpha.setSearch("/w/one", fakeSearchMatch{taskID: "a1"})
	controller := local.NewWindowController(registry, nil)

	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind":            "timeline",
		"workspaceScopes": searchScopes([2]string{"/w/one", "runner:alpha:/w/one"}),
		"sortBy":          "updated",
		"search":          "needle",
	})
	if total != 1 || !equalStrings(taskIDs(items), []string{"a1"}) {
		t.Fatalf("search = %v (total %d), want [a1]", taskIDs(items), total)
	}
	if _, present := items[0]["searchSnippet"]; present {
		t.Fatalf("title-only match carried searchSnippet: %v", items[0])
	}
	if _, present := items[0]["searchSnippets"]; present {
		t.Fatalf("title-only match carried searchSnippets: %v", items[0])
	}
}

// TestListTaskListSearchDropsUnknownTaskIds 覆盖：runner 命中的 taskId 在行缓存
// 里没有对应行时被丢弃（行只来自 rowsForScope）。
func TestListTaskListSearchDropsUnknownTaskIds(t *testing.T) {
	registry, alpha, _ := newRegistryWithRunners(t)
	alpha.seed("/w/one", fakeTask{taskID: "a1", title: "Alpha", createdAt: 1, updatedAt: 10})
	alpha.setSearch("/w/one",
		fakeSearchMatch{taskID: "ghost", snippet: "not a row"},
		fakeSearchMatch{taskID: "a1", snippet: "real"},
	)
	controller := local.NewWindowController(registry, nil)

	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind":            "timeline",
		"workspaceScopes": searchScopes([2]string{"/w/one", "runner:alpha:/w/one"}),
		"sortBy":          "updated",
		"search":          "alpha",
	})
	if total != 1 || !equalStrings(taskIDs(items), []string{"a1"}) {
		t.Fatalf("search = %v (total %d), want [a1] with ghost dropped", taskIDs(items), total)
	}
}

// TestListTaskListSearchMatchesAreScopedPerIdentity 覆盖：同一个 taskId 在两个
// scope 下是两个任务，命中不能跨 scope 串。
func TestListTaskListSearchMatchesAreScopedPerIdentity(t *testing.T) {
	registry, alpha, beta := newRegistryWithRunners(t)
	alpha.seed("/w/one", fakeTask{taskID: "shared", title: "One", createdAt: 1, updatedAt: 10})
	beta.seed("/w/two", fakeTask{taskID: "shared", title: "Two", createdAt: 2, updatedAt: 20})
	// 只有 alpha 的 runner 在正文里命中了 shared。
	alpha.setSearch("/w/one", fakeSearchMatch{taskID: "shared", snippet: "alpha only"})
	controller := local.NewWindowController(registry, nil)

	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind": "timeline",
		"workspaceScopes": searchScopes(
			[2]string{"/w/one", "runner:alpha:/w/one"},
			[2]string{"/w/two", "runner:beta:/w/two"},
		),
		"sortBy": "updated",
		"search": "snippet-text",
	})
	if total != 1 {
		t.Fatalf("search matched %d rows, want only the alpha scope's task", total)
	}
	if items[0]["workspaceIdentity"] != "runner:alpha:/w/one" {
		t.Fatalf("matched row = %v, want the alpha scope's row", items[0])
	}
}

// TestListTaskListSearchMergesScopesSortsAndLimits 覆盖：两个 scope 的正文命中
// 合并后按 sortBy 排序、应用 limit，total/hasMore 描述合并后的全量。
func TestListTaskListSearchMergesScopesSortsAndLimits(t *testing.T) {
	registry, alpha, beta := newRegistryWithRunners(t)
	alpha.seed("/w/one",
		fakeTask{taskID: "a1", title: "A1", createdAt: 1, updatedAt: 100},
		fakeTask{taskID: "a2", title: "A2", createdAt: 2, updatedAt: 300},
	)
	beta.seed("/w/two",
		fakeTask{taskID: "b1", title: "B1", createdAt: 3, updatedAt: 200},
		fakeTask{taskID: "b2", title: "B2", createdAt: 4, updatedAt: 400},
	)
	alpha.setSearch("/w/one",
		fakeSearchMatch{taskID: "a1", snippet: "one"},
		fakeSearchMatch{taskID: "a2", snippet: "two"},
	)
	beta.setSearch("/w/two",
		fakeSearchMatch{taskID: "b1", snippet: "three"},
		fakeSearchMatch{taskID: "b2", snippet: "four"},
	)
	controller := local.NewWindowController(registry, nil)
	scopes := searchScopes(
		[2]string{"/w/one", "runner:alpha:/w/one"},
		[2]string{"/w/two", "runner:beta:/w/two"},
	)

	items, total, hasMore := listTaskList(t, controller, map[string]any{
		"kind": "timeline", "workspaceScopes": scopes, "sortBy": "updated", "search": "hit",
	})
	if total != 4 || hasMore || !equalStrings(taskIDs(items), []string{"b2", "a2", "b1", "a1"}) {
		t.Fatalf("search = %v (total %d, hasMore %v), want updated desc [b2 a2 b1 a1]", taskIDs(items), total, hasMore)
	}
	if items[0]["searchSnippet"] != "four" {
		t.Fatalf("first row snippet = %v, want four", items[0]["searchSnippet"])
	}

	limited, total, hasMore := listTaskList(t, controller, map[string]any{
		"kind": "timeline", "workspaceScopes": scopes, "sortBy": "updated", "search": "hit", "limit": 2,
	})
	if len(limited) != 2 || total != 4 || !hasMore {
		t.Fatalf("limited search = %d rows (total %d, hasMore %v), want 2/4/true", len(limited), total, hasMore)
	}
	if !equalStrings(taskIDs(limited), []string{"b2", "a2"}) {
		t.Fatalf("limited search = %v, want the two newest", taskIDs(limited))
	}
}

// TestListTaskListSearchStillAppliesKindFilter 覆盖：合并后仍按 kind 过滤，正文
// 命中但属于别的分区（如 archived）的行不会出现在 timeline。
func TestListTaskListSearchStillAppliesKindFilter(t *testing.T) {
	registry, alpha, _ := newRegistryWithRunners(t)
	alpha.seed("/w/one",
		fakeTask{taskID: "a1", title: "Active", createdAt: 1, updatedAt: 10},
		fakeTask{taskID: "a2", title: "Archived", archived: true, createdAt: 2, updatedAt: 20},
	)
	alpha.setSearch("/w/one",
		fakeSearchMatch{taskID: "a1", snippet: "active hit"},
		fakeSearchMatch{taskID: "a2", snippet: "archived hit"},
	)
	controller := local.NewWindowController(registry, nil)

	timeline, total, _ := listTaskList(t, controller, map[string]any{
		"kind":            "timeline",
		"workspaceScopes": searchScopes([2]string{"/w/one", "runner:alpha:/w/one"}),
		"sortBy":          "updated",
		"search":          "hit",
	})
	if total != 1 || !equalStrings(taskIDs(timeline), []string{"a1"}) {
		t.Fatalf("timeline search = %v (total %d), want [a1]", taskIDs(timeline), total)
	}

	archived, total, _ := listTaskList(t, controller, map[string]any{
		"kind":            "archived",
		"workspaceScopes": searchScopes([2]string{"/w/one", "runner:alpha:/w/one"}),
		"sortBy":          "updated",
		"search":          "hit",
	})
	if total != 1 || !equalStrings(taskIDs(archived), []string{"a2"}) {
		t.Fatalf("archived search = %v (total %d), want [a2]", taskIDs(archived), total)
	}
}

// TestListTaskListShortSearchSkipsRunner 覆盖：短于 2 个 rune 的查询（含单个
// CJK 字符）不调用 runner，只做标题匹配。
func TestListTaskListShortSearchSkipsRunner(t *testing.T) {
	registry, alpha, _ := newRegistryWithRunners(t)
	alpha.seed("/w/one",
		fakeTask{taskID: "a1", title: "笔记 alpha", createdAt: 1, updatedAt: 10},
		fakeTask{taskID: "a2", title: "笔记 beta", createdAt: 2, updatedAt: 20},
	)
	alpha.setSearch("/w/one", fakeSearchMatch{taskID: "a2", snippet: "runner hit"})
	controller := local.NewWindowController(registry, nil)
	scopes := searchScopes([2]string{"/w/one", "runner:alpha:/w/one"})

	// 单个 CJK 字符：按 rune 计数是 1，必须走标题匹配。
	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind": "timeline", "workspaceScopes": scopes, "sortBy": "updated", "search": "笔",
	})
	if total != 2 || len(items) != 2 {
		t.Fatalf("single-CJK search = %v (total %d), want both rows", taskIDs(items), total)
	}
	for _, item := range items {
		if _, present := item["searchSnippet"]; present {
			t.Fatalf("short query attached a snippet: %v", item)
		}
	}

	// 一个 ASCII 字符同理。
	if _, total, _ = listTaskList(t, controller, map[string]any{
		"kind": "timeline", "workspaceScopes": scopes, "sortBy": "updated", "search": "a",
	}); total != 2 {
		t.Fatalf("single-ASCII search total = %d, want 2", total)
	}

	if calls := alpha.searchTaskCalls(); len(calls) != 0 {
		t.Fatalf("runner searchTasks called %v for short queries, want none", calls)
	}
}

// TestListTaskListSearchRunnerErrorFallsBackToTitle 覆盖：runner 报错时该 scope
// 退回标题匹配，列表不失败。
func TestListTaskListSearchRunnerErrorFallsBackToTitle(t *testing.T) {
	registry, alpha, _ := newRegistryWithRunners(t)
	alpha.seed("/w/one",
		fakeTask{taskID: "a1", title: "Needle title", createdAt: 1, updatedAt: 10},
		fakeTask{taskID: "a2", title: "Other", createdAt: 2, updatedAt: 20},
	)
	alpha.searchErr = fmt.Errorf("search exploded")
	controller := local.NewWindowController(registry, nil)

	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind":            "timeline",
		"workspaceScopes": searchScopes([2]string{"/w/one", "runner:alpha:/w/one"}),
		"sortBy":          "updated",
		"search":          "needle",
	})
	if total != 1 || !equalStrings(taskIDs(items), []string{"a1"}) {
		t.Fatalf("search after runner error = %v (total %d), want title-only [a1]", taskIDs(items), total)
	}
	if _, present := items[0]["searchSnippet"]; present {
		t.Fatalf("fallback row carried a snippet: %v", items[0])
	}
}

// TestListTaskListSearchTimeoutFallsBackToTitle 覆盖：runner 慢于单 scope 预算时
// 只等约一个 timeout，然后退回标题匹配。
func TestListTaskListSearchTimeoutFallsBackToTitle(t *testing.T) {
	registry, alpha, _ := newRegistryWithRunners(t)
	alpha.seed("/w/one", fakeTask{taskID: "a1", title: "Needle title", createdAt: 1, updatedAt: 10})
	alpha.searchDelay = 2 * time.Second
	controller := local.NewWindowController(registry, nil).WithSearchTimeout(80 * time.Millisecond)

	started := time.Now()
	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind":            "timeline",
		"workspaceScopes": searchScopes([2]string{"/w/one", "runner:alpha:/w/one"}),
		"sortBy":          "updated",
		"search":          "needle",
	})
	elapsed := time.Since(started)
	if total != 1 || !equalStrings(taskIDs(items), []string{"a1"}) {
		t.Fatalf("search after timeout = %v (total %d), want title-only [a1]", taskIDs(items), total)
	}
	if elapsed > time.Second {
		t.Fatalf("listTaskList took %v, want it to give up near the 80ms budget", elapsed)
	}
}

// TestListTaskListSearchQueriesScopesInParallel 覆盖：多个 runner 的搜索并发执行，
// 总耗时接近单个 delay 而不是相加。
func TestListTaskListSearchQueriesScopesInParallel(t *testing.T) {
	registry, alpha, beta := newRegistryWithRunners(t)
	alpha.seed("/w/one", fakeTask{taskID: "a1", title: "One", createdAt: 1, updatedAt: 10})
	beta.seed("/w/two", fakeTask{taskID: "b1", title: "Two", createdAt: 2, updatedAt: 20})
	alpha.setSearch("/w/one", fakeSearchMatch{taskID: "a1", snippet: "one"})
	beta.setSearch("/w/two", fakeSearchMatch{taskID: "b1", snippet: "two"})
	const delay = 300 * time.Millisecond
	alpha.searchDelay = delay
	beta.searchDelay = delay
	controller := local.NewWindowController(registry, nil)

	started := time.Now()
	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind": "timeline",
		"workspaceScopes": searchScopes(
			[2]string{"/w/one", "runner:alpha:/w/one"},
			[2]string{"/w/two", "runner:beta:/w/two"},
		),
		"sortBy": "updated",
		"search": "hit",
	})
	elapsed := time.Since(started)
	if total != 2 || !equalStrings(taskIDs(items), []string{"a1", "b1"}) {
		t.Fatalf("search = %v (total %d), want both scopes merged", taskIDs(items), total)
	}
	// 串行会 ≥ 2×delay；留出调度余量仍必须明显小于 2×。
	if elapsed >= 2*delay {
		t.Fatalf("search fan-out took %v for two %v runners; want parallel (~1x)", elapsed, delay)
	}
}

// TestListTaskListSearchWithoutRegistryStaysTitleOnly 覆盖：没有 runner registry
// （单测里构造的 controller）时搜索不 panic、也不尝试调用 runner；没有行缓存
// 所以结果是空列表，等价于只做标题匹配。
func TestListTaskListSearchWithoutRegistryStaysTitleOnly(t *testing.T) {
	controller := local.NewWindowController(nil, nil)
	items, total, _ := listTaskList(t, controller, map[string]any{
		"kind":            "timeline",
		"workspaceScopes": searchScopes([2]string{"/w/one", "runner:alpha:/w/one"}),
		"sortBy":          "updated",
		"search":          "needle",
	})
	if total != 0 || len(items) != 0 {
		t.Fatalf("nil-registry search = %v (total %d), want an empty list", taskIDs(items), total)
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
