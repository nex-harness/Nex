package local

// `window-controller` (docs/next/channels-research.md §3, docs/next/task-state.md).
//
// The controller is an aggregation layer only: task state lives on the runners'
// metadata stores. `listTaskList` fans out to the runner that owns each
// `workspaceScopes[]` entry, merges/sorts/searches the rows, and controller
// frames are snapshots of the rows the server has read back. The server keeps
// itself subscribed to each runner's `nex-task.onDynamicWorkspaceEvent` and
// refreshes a workspace (then pushes a snapshot) after any mutation, so the
// pinned section and archived view update without a page reload.
//
// Snapshots (not deltas) are required because `NexTaskMeta` does not carry
// membership: an event alone cannot be turned into a complete delta row.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/server/internal/automations"
	"github.com/nex-harness/nex/server/internal/rpcwire"
	"github.com/nex-harness/nex/server/internal/runners"
)

const (
	controllerLogEpoch = "nex-next-controller-1"
	// controllerRefreshDelay coalesces a burst of mutations (e.g. batch delete)
	// into one refresh per workspace.
	controllerRefreshDelay = 120 * time.Millisecond
)

type controllerListener struct {
	id   uint64
	emit func(rpcwire.Value)
}

// taskRow is one cached row: the runner's meta plus the membership the server
// derived from which partition listed it.
type taskRow struct {
	workspacePath     string
	workspaceIdentity string
	taskID            string
	createdAt         int64
	updatedAt         int64
	title             string
	status            string
	meta              map[string]any
	pinned            bool
	archived          bool
}

func (r taskRow) key() string {
	return workspaceKey(r.workspacePath, r.workspaceIdentity) + "\x00" + r.taskID
}

func workspaceKey(path, identity string) string {
	if trimmed := strings.TrimSpace(identity); trimmed != "" {
		return trimmed
	}
	return path
}

// WindowController is the sidebar controller.
type WindowController struct {
	mu      sync.Mutex
	nextID  uint64
	seq     uint64
	listens map[string][]controllerListener
	// topics remembers the topic each subscription id was created for, so
	// resyncControllerV4 emits a frame for the right topic.
	topics map[string]string
	// subscriptionOwner maps a subscription id to its browser connection, so a
	// background refresh can push a frame without walking the listener table.
	subscriptionOwner map[string]string
	// rowsByWorkspace caches the last successful read per workspace key.
	rowsByWorkspace map[string]map[string]taskRow
	// refreshEpochs debounces one in-flight refresh per workspace key.
	refreshPending map[string]time.Time
	// internalSubscribed tracks workspaces whose runner events the server listens to.
	internalSubscribed map[string]struct{}
	internalListenSeq  uint32
	// activeScopes holds the read scopes the controller still serves (a
	// browser listed or subscribed to them). The automation outcome feed only
	// trusts terminal rows read from an active scope: a background read can
	// fail without clearing the cache, so cached rows may be stale by the
	// time a later workspace read runs.
	activeScopes map[string]listScope
	// automationOutcomes receives the terminal runs of automation sessions.
	automationOutcomes AutomationOutcomeSink

	registry *runners.Registry
	logger   *slog.Logger
}

// AutomationOutcomeSink records a task row's status on the automation run that
// created its session. *automations.Service satisfies it via RecordOutcome.
type AutomationOutcomeSink interface {
	RecordOutcome(outcome automations.Outcome) bool
}

// NewWindowController returns a controller bound to the runner registry.
func NewWindowController(registry *runners.Registry, logger *slog.Logger) *WindowController {
	if logger == nil {
		logger = slog.Default()
	}
	return &WindowController{
		listens:            make(map[string][]controllerListener),
		topics:             make(map[string]string),
		subscriptionOwner:  make(map[string]string),
		rowsByWorkspace:    make(map[string]map[string]taskRow),
		refreshPending:     make(map[string]time.Time),
		internalSubscribed: make(map[string]struct{}),
		activeScopes:       make(map[string]listScope),
		registry:           registry,
		logger:             logger,
	}
}

// WithAutomationOutcomes makes the controller feed task-row outcomes to the
// automations service. Nil (the default) keeps the feed off.
//
// 限制（v1）：只有当前有浏览器列出过/订阅过的 workspace 会回流终态。自动化
// 的 workspace 若从未被浏览（例如只在别的 workspace 里开着 Automations 页），
// 其 run 的终态不会写回 run history；调度、runNow 和 run 记录本身不受影响。
// 解决需要 app 启动时按 automations.json 里每个 automation 的 workspaceIdentity
// 主动 RegisterListen，这里先不做。
func (c *WindowController) WithAutomationOutcomes(sink AutomationOutcomeSink) *WindowController {
	c.mu.Lock()
	c.automationOutcomes = sink
	c.mu.Unlock()
	return c
}

// Call implements Channel.
func (c *WindowController) Call(ctx context.Context, env *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	switch method {
	case "listTaskList":
		return c.listTaskList(ctx, arg)
	case "deleteArchivedTask":
		return c.forwardMutation(ctx, arg, "deleteArchivedTask")
	case "deleteArchivedTasks":
		return c.forwardMutation(ctx, arg, "deleteArchivedTasks")
	case "mutateTask":
		return c.mutateTask(ctx, arg)
	case "subscribeControllerV4":
		topic := normalizeControllerTopic(stringField(firstArgument(arg), "topic"))
		c.mu.Lock()
		c.nextID++
		c.seq++
		seq := c.seq
		subscriptionID := fmt.Sprintf("ctl-%d", c.nextID)
		c.topics[subscriptionID] = topic
		c.subscriptionOwner[subscriptionID] = env.ConnectionID
		// 订阅方要拿到全部 workspace 的快照；这些 scope 的缓存行也因此可被
		// automation outcome 回流信任。
		c.markScopesActiveLocked(c.scopesLocked())
		rows := c.rowsLocked()
		c.mu.Unlock()
		c.emitSnapshot(env.ConnectionID, subscriptionID, topic, seq, rows)
		return ackJSON(subscriptionID), nil
	case "resyncControllerV4":
		params := firstArgument(arg)
		subscriptionID := stringField(params, "subscriptionId")
		if subscriptionID == "" {
			subscriptionID = "ctl"
		}
		c.mu.Lock()
		topic := c.topics[subscriptionID]
		c.seq++
		seq := c.seq
		rows := c.rowsLocked()
		c.mu.Unlock()
		c.emitSnapshot(env.ConnectionID, subscriptionID, topic, seq, rows)
		return ackJSON(subscriptionID), nil
	case "unsubscribeControllerV4":
		subscriptionID := stringField(firstArgument(arg), "subscriptionId")
		if subscriptionID != "" {
			c.mu.Lock()
			delete(c.topics, subscriptionID)
			delete(c.subscriptionOwner, subscriptionID)
			c.mu.Unlock()
		}
		return rpcwire.Undefined{}, nil
	}
	return nil, &MethodNotFoundError{Method: method}
}

// ── listTaskList ──

type listQuery struct {
	Kind     string
	SortBy   string
	Search   string
	Limit    *int
	Scopes   []listScope
	HasLimit bool
}

type listScope struct {
	WorkspacePath     string
	WorkspaceIdentity string
}

func parseListQuery(arg rpcwire.Value) (listQuery, error) {
	object, err := decodeObject(arg)
	if err != nil {
		return listQuery{}, err
	}
	var query listQuery
	query.Kind = decodeString(object["kind"])
	query.SortBy = decodeString(object["sortBy"])
	query.Search = strings.TrimSpace(decodeString(object["search"]))
	if query.Kind == "" {
		query.Kind = "timeline"
	}
	if query.SortBy != "created" {
		query.SortBy = "updated"
	}
	if raw, ok := object["limit"]; ok {
		var limit float64
		if json.Unmarshal(raw, &limit) == nil && limit > 0 {
			value := int(limit)
			query.Limit = &value
			query.HasLimit = true
		}
	}
	if raw, ok := object["workspaceScopes"]; ok {
		var scopes []map[string]json.RawMessage
		if json.Unmarshal(raw, &scopes) == nil {
			for _, scope := range scopes {
				entry := listScope{
					WorkspacePath:     decodeString(scope["workspacePath"]),
					WorkspaceIdentity: strings.TrimSpace(decodeString(scope["workspaceIdentity"])),
				}
				if strings.TrimSpace(entry.WorkspacePath) == "" {
					continue
				}
				query.Scopes = append(query.Scopes, entry)
			}
		}
	}
	return query, nil
}

func (c *WindowController) listTaskList(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	query, err := parseListQuery(arg)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0)
	seen := make(map[string]struct{})
	scopes := dedupeScopes(query.Scopes)
	// 只有本次列表确实读取的 scope 才参与 automation outcome 回流：缓存里其它
	// workspace 的行可能是旧的，用它们写终态会把已被后续运行覆盖的状态写回去。
	c.markScopesActive(scopes)
	for _, scope := range scopes {
		rows := c.rowsForScope(ctx, scope)
		for _, row := range rows {
			if !matchesKind(row, query.Kind) {
				continue
			}
			if query.Search != "" && !strings.Contains(strings.ToLower(row.title), strings.ToLower(query.Search)) {
				continue
			}
			// Same workspace path on two runners yields distinct rows; the address
			// (identity) keeps them apart, so dedupe on the full task address.
			key := row.key()
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			items = append(items, listItem(row))
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		leftAt, rightAt := itemSortValue(items[i], query.SortBy), itemSortValue(items[j], query.SortBy)
		if leftAt != rightAt {
			return leftAt > rightAt
		}
		return itemString(items[i], "taskId") < itemString(items[j], "taskId")
	})
	total := len(items)
	if query.HasLimit && query.Limit != nil && *query.Limit < total {
		items = items[:*query.Limit]
	}
	body, err := json.Marshal(map[string]any{
		"items":   items,
		"total":   total,
		"hasMore": total > len(items),
	})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func dedupeScopes(scopes []listScope) []listScope {
	seen := make(map[string]struct{}, len(scopes))
	out := make([]listScope, 0, len(scopes))
	for _, scope := range scopes {
		key := workspaceKey(scope.WorkspacePath, scope.WorkspaceIdentity)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func matchesKind(row taskRow, kind string) bool {
	switch kind {
	case "pinned":
		return row.pinned && !row.archived
	case "archived":
		return row.archived
	case "active":
		return !row.archived
	default: // timeline / project list
		return !row.pinned && !row.archived
	}
}

// listItem is `NexTaskMeta & {sourceAvailability, liveStatus}`.
func listItem(row taskRow) map[string]any {
	meta := copyMeta(row.meta)
	meta["sourceAvailability"] = "online"
	meta["liveStatus"] = liveStatus(row.status)
	return meta
}

func copyMeta(meta map[string]any) map[string]any {
	out := make(map[string]any, len(meta)+2)
	for key, value := range meta {
		out[key] = value
	}
	return out
}

// liveStatus mirrors the reference service's liveStatusFromMeta.
func liveStatus(status string) string {
	switch status {
	case "running":
		return "running"
	case "completed":
		return "completed"
	case "error":
		return "error"
	default:
		return "idle"
	}
}

func itemSortValue(item map[string]any, sortBy string) int64 {
	field := "updatedAt"
	if sortBy == "created" {
		field = "createdAt"
	}
	switch value := item[field].(type) {
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return parsed
		}
	case float64:
		return int64(value)
	}
	return 0
}

func itemString(item map[string]any, field string) string {
	if value, ok := item[field].(string); ok {
		return value
	}
	return ""
}

// ── runner reads ──

// rowsForScope reads the three partitions for one scope from its runner. A
// failed read keeps the previous cache for that workspace (the UI must not see
// a transient runner error as an empty list).
func (c *WindowController) rowsForScope(ctx context.Context, scope listScope) map[string]taskRow {
	key := workspaceKey(scope.WorkspacePath, scope.WorkspaceIdentity)
	c.subscribeInternal(key, scope)

	rows, err := c.readScope(ctx, scope)
	if err != nil {
		c.logger.Warn("window-controller listTaskList refresh failed",
			"workspacePath", scope.WorkspacePath, "workspaceIdentity", scope.WorkspaceIdentity, "error", err)
		c.mu.Lock()
		defer c.mu.Unlock()
		if cached, ok := c.rowsByWorkspace[key]; ok {
			return cached
		}
		return map[string]taskRow{}
	}
	c.mu.Lock()
	c.rowsByWorkspace[key] = rows
	c.mu.Unlock()
	c.recordAutomationOutcomes(scope, rows)
	return rows
}

// markScopesActive remembers the scopes a browser read; refresh and subscribe
// call this before trusting cached rows for the outcome feed.
func (c *WindowController) markScopesActive(scopes []listScope) {
	c.mu.Lock()
	c.markScopesActiveLocked(scopes)
	c.mu.Unlock()
}

func (c *WindowController) markScopesActiveLocked(scopes []listScope) {
	for _, scope := range scopes {
		c.activeScopes[workspaceKey(scope.WorkspacePath, scope.WorkspaceIdentity)] = scope
	}
}

// scopesLocked lists the workspaces the controller currently caches rows for.
func (c *WindowController) scopesLocked() []listScope {
	scopes := make([]listScope, 0, len(c.rowsByWorkspace))
	for _, rows := range c.rowsByWorkspace {
		for _, row := range rows {
			scopes = append(scopes, listScope{WorkspacePath: row.workspacePath, WorkspaceIdentity: row.workspaceIdentity})
			break
		}
	}
	return scopes
}

// isScopeActive reports whether the browser still serves scope.
func (c *WindowController) isScopeActive(scope listScope) bool {
	key := workspaceKey(scope.WorkspacePath, scope.WorkspaceIdentity)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.activeScopes[key]
	return ok
}

// recordAutomationOutcomes feeds every row of one freshly read scope to the
// automations service. RecordOutcome itself ignores non-automation sessions and
// non-terminal statuses, so the whole partition is offered; Error is the row's
// error text when the runner exposes one.
func (c *WindowController) recordAutomationOutcomes(scope listScope, rows map[string]taskRow) {
	c.mu.Lock()
	sink := c.automationOutcomes
	c.mu.Unlock()
	if sink == nil {
		return
	}
	for _, row := range rows {
		if row.taskID == "" || row.status == "" {
			continue
		}
		sink.RecordOutcome(automations.Outcome{
			SessionID: row.taskID,
			Status:    row.status,
			Error:     automationRowError(row),
		})
	}
}

// automationRowError returns the row's error text as recorded by the runner.
func automationRowError(row taskRow) string {
	for _, field := range []string{"errorMessage", "lastError", "error"} {
		if text := stringValue(row.meta[field]); text != "" {
			return text
		}
	}
	return ""
}

func (c *WindowController) readScope(ctx context.Context, scope listScope) (map[string]taskRow, error) {
	if c.registry == nil {
		// Unit tests build a controller without a runner registry: no rows, not a panic.
		return map[string]taskRow{}, nil
	}
	target := runners.Target{
		WorkspacePath:     scope.WorkspacePath,
		WorkspaceIdentity: scope.WorkspaceIdentity,
	}
	arg, err := json.Marshal(map[string]any{
		"workspacePath": scope.WorkspacePath,
		// Rows must echo the identity the UI opened the scope with.
		"workspaceIdentity": scope.WorkspaceIdentity,
	})
	if err != nil {
		return nil, err
	}
	rows := make(map[string]taskRow)
	for _, partition := range []struct {
		method  string
		pinned  bool
		archive bool
	}{
		{method: "listPinnedTasks", pinned: true},
		{method: "listArchivedTasks", archive: true},
		{method: "listTasks"},
	} {
		value, err := c.registry.CallWorkspace(ctx, target, "nex-task", partition.method, arg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", partition.method, err)
		}
		metas, err := decodeTaskMetas(value, scope)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", partition.method, err)
		}
		for _, row := range metas {
			// A row may appear in several partitions; membership accumulates so a later
			// partition cannot clear a flag an earlier one proved.
			if existing, ok := rows[row.key()]; ok {
				row.pinned = row.pinned || existing.pinned
				row.archived = row.archived || existing.archived
			}
			row.pinned = row.pinned || partition.pinned
			row.archived = row.archived || partition.archive
			rows[row.key()] = row
		}
	}
	return rows, nil
}

/** decodeTaskMetas parses a runner `NexTaskMeta[]`, preserving numeric values. */
func decodeTaskMetas(value json.RawMessage, scope listScope) ([]taskRow, error) {
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	var items []map[string]any
	if err := decoder.Decode(&items); err != nil {
		return nil, err
	}
	rows := make([]taskRow, 0, len(items))
	for _, item := range items {
		taskID, _ := item["taskId"].(string)
		if taskID == "" {
			continue
		}
		workspacePath, _ := item["workspacePath"].(string)
		if workspacePath == "" {
			workspacePath = scope.WorkspacePath
		}
		identity, _ := item["workspaceIdentity"].(string)
		// Rows must carry the identity the scope was opened with; a runner that
		// echoed nothing gets the routed identity back.
		if strings.TrimSpace(identity) == "" {
			identity = scope.WorkspaceIdentity
			if identity != "" {
				item["workspaceIdentity"] = identity
			}
		}
		rows = append(rows, taskRow{
			workspacePath:     workspacePath,
			workspaceIdentity: strings.TrimSpace(identity),
			taskID:            taskID,
			createdAt:         numberField(item, "createdAt"),
			updatedAt:         numberField(item, "updatedAt"),
			title:             stringValue(item["title"]),
			status:            stringValue(item["status"]),
			meta:              item,
		})
	}
	return rows, nil
}

func numberField(item map[string]any, field string) int64 {
	if value, ok := item[field].(json.Number); ok {
		if parsed, err := value.Int64(); err == nil {
			return parsed
		}
	}
	return 0
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func (c *WindowController) rowsLocked() []taskRow {
	rows := make([]taskRow, 0)
	for _, workspace := range c.rowsByWorkspace {
		for _, row := range workspace {
			rows = append(rows, row)
		}
	}
	return rows
}

// ── mutations (forwarded to the owning runner) ──

func (c *WindowController) forwardMutation(ctx context.Context, arg rpcwire.Value, method string) (rpcwire.Value, error) {
	object, err := decodeObject(arg)
	if err != nil {
		return nil, err
	}
	address, err := decodeAddress(object["address"])
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"workspacePath": address.WorkspacePath,
		"taskId":        address.TaskID,
	}
	if address.WorkspaceIdentity != "" {
		payload["workspaceIdentity"] = address.WorkspaceIdentity
	}
	if method == "deleteArchivedTasks" {
		var taskIDs []string
		if raw, ok := object["taskIds"]; ok {
			_ = json.Unmarshal(raw, &taskIDs)
		}
		payload["taskIds"] = taskIDs
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.callRunner(ctx, address, "nex-task", method, encoded)
}

// mutateTask is the desktop-host entry point; the web UI never calls it
// (docs/next/channels-research.md §3). Supported mutations forward to nex-task
// so both surfaces share the single writer.
func (c *WindowController) mutateTask(ctx context.Context, arg rpcwire.Value) (rpcwire.Value, error) {
	object, err := decodeObject(arg)
	if err != nil {
		return nil, err
	}
	address, err := decodeAddress(object["address"])
	if err != nil {
		return nil, err
	}
	mutation, err := decodeObject(object["mutation"])
	if err != nil {
		return nil, err
	}
	kind := decodeString(mutation["kind"])
	payload := map[string]any{
		"workspacePath": address.WorkspacePath,
		"taskId":        address.TaskID,
	}
	if address.WorkspaceIdentity != "" {
		payload["workspaceIdentity"] = address.WorkspaceIdentity
	}
	method := ""
	switch kind {
	case "pin":
		method = "setTaskPinned"
		payload["pinned"] = decodeBool(mutation["pinned"])
	case "archive":
		if archived := decodeBool(mutation["archived"]); archived {
			method = "archiveTask"
		} else {
			method = "unarchiveTask"
		}
	case "delete-archived":
		method = "deleteArchivedTask"
	case "mark-read":
		method = "setTaskUnread"
		payload["unread"] = false
		if raw, ok := mutation["expectedUnreadAt"]; ok {
			var expected json.Number
			if json.Unmarshal(raw, &expected) == nil {
				payload["expectedUnreadAt"] = expected
			}
		}
	case "mark-unread":
		method = "setTaskUnread"
		payload["unread"] = true
	default:
		// open/resume/delete are window-shell actions, not task-state mutations.
		return rpcwire.Undefined{}, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.callRunner(ctx, address, "nex-task", method, encoded)
}

func (c *WindowController) callRunner(ctx context.Context, address taskAddress, channel, method string, arg json.RawMessage) (rpcwire.Value, error) {
	if c.registry == nil {
		return nil, runners.ErrNoRunnerConnected
	}
	value, err := c.registry.CallWorkspace(ctx, runners.Target{
		WorkspacePath:     address.WorkspacePath,
		WorkspaceIdentity: address.WorkspaceIdentity,
	}, channel, method, arg)
	if err != nil {
		return nil, err
	}
	c.scheduleRefresh(address.WorkspacePath, address.WorkspaceIdentity)
	return json.RawMessage(value), nil
}

type taskAddress struct {
	WorkspacePath     string
	WorkspaceIdentity string
	TaskID            string
}

func decodeAddress(value json.RawMessage) (taskAddress, error) {
	if len(value) == 0 {
		return taskAddress{}, fmt.Errorf("address is required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return taskAddress{}, fmt.Errorf("decoding address: %w", err)
	}
	address := taskAddress{
		WorkspacePath:     decodeString(object["workspacePath"]),
		WorkspaceIdentity: strings.TrimSpace(decodeString(object["workspaceIdentity"])),
		TaskID:            decodeString(object["taskId"]),
	}
	if address.WorkspacePath == "" || address.TaskID == "" {
		return taskAddress{}, fmt.Errorf("address requires workspacePath and taskId")
	}
	return address, nil
}

func decodeString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return text
}

func decodeBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return value
}

// ── internal runner subscriptions + controller frames ──

// subscribeInternal makes the server listen to one workspace's nex-task events,
// so a mutation made from any browser refreshes this controller. The registry
// binding survives runner reconnects (RegisterListen re-issues it), and a
// repeated call for the same workspace key is a no-op.
func (c *WindowController) subscribeInternal(workspaceKeyValue string, scope listScope) {
	c.mu.Lock()
	if _, exists := c.internalSubscribed[workspaceKeyValue]; exists {
		c.mu.Unlock()
		return
	}
	c.internalSubscribed[workspaceKeyValue] = struct{}{}
	c.internalListenSeq++
	listenID := c.internalListenSeq
	c.mu.Unlock()

	if c.registry == nil {
		return
	}
	arg, err := json.Marshal(map[string]any{
		"workspacePath":     scope.WorkspacePath,
		"workspaceIdentity": scope.WorkspaceIdentity,
	})
	if err != nil {
		return
	}
	err = c.registry.RegisterListen(
		"server-controller",
		listenID,
		runners.Target{
			WorkspacePath:     scope.WorkspacePath,
			WorkspaceIdentity: scope.WorkspaceIdentity,
		},
		"nex-task",
		"onDynamicWorkspaceEvent",
		arg,
		func(event json.RawMessage) {
			var envelope struct {
				WorkspacePath     string `json:"workspacePath"`
				WorkspaceIdentity string `json:"workspaceIdentity"`
			}
			if json.Unmarshal(event, &envelope) != nil {
				return
			}
			c.scheduleRefresh(envelope.WorkspacePath, envelope.WorkspaceIdentity)
		},
	)
	if err != nil {
		c.logger.Warn("window-controller internal listen failed",
			"workspacePath", scope.WorkspacePath, "error", err)
		c.mu.Lock()
		delete(c.internalSubscribed, workspaceKeyValue)
		c.mu.Unlock()
	}
}

// scheduleRefresh coalesces mutations into one refresh per workspace and then
// pushes a snapshot to every controller subscriber.
func (c *WindowController) scheduleRefresh(workspacePath, workspaceIdentity string) {
	if workspacePath == "" {
		return
	}
	key := workspaceKey(workspacePath, workspaceIdentity)
	c.mu.Lock()
	if _, pending := c.refreshPending[key]; pending {
		c.mu.Unlock()
		return
	}
	c.refreshPending[key] = time.Now()
	c.mu.Unlock()

	time.AfterFunc(controllerRefreshDelay, func() {
		c.mu.Lock()
		delete(c.refreshPending, key)
		c.mu.Unlock()
		scope := listScope{
			WorkspacePath:     workspacePath,
			WorkspaceIdentity: workspaceIdentity,
		}
		// Background work must not inherit a browser's cancellation.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := c.readScope(ctx, scope)
		if err != nil {
			c.logger.Warn("window-controller refresh failed",
				"workspacePath", workspacePath, "error", err)
			// 这次读取失败，缓存行可能已过期。只有浏览器仍在服务的 scope
			// （列表或订阅过）才用缓存行回流 automation 终态。
			if !c.isScopeActive(scope) {
				return
			}
			c.mu.Lock()
			cached, ok := c.rowsByWorkspace[key]
			c.mu.Unlock()
			if !ok {
				return
			}
			c.recordAutomationOutcomes(scope, cached)
			return
		}
		c.mu.Lock()
		c.rowsByWorkspace[key] = rows
		c.mu.Unlock()
		c.recordAutomationOutcomes(scope, rows)
		c.publishSnapshot()
	})
}

// publishSnapshot pushes the current rows to every controller subscriber.
func (c *WindowController) publishSnapshot() {
	c.mu.Lock()
	c.seq++
	seq := c.seq
	rows := c.rowsLocked()
	type target struct {
		connectionID   string
		subscriptionID string
		topic          string
	}
	subscriptions := make([]target, 0, len(c.topics))
	for subscriptionID, topic := range c.topics {
		connectionID := c.subscriptionOwner[subscriptionID]
		if connectionID == "" {
			continue
		}
		subscriptions = append(subscriptions, target{
			connectionID:   connectionID,
			subscriptionID: subscriptionID,
			topic:          topic,
		})
	}
	c.mu.Unlock()

	for _, subscription := range subscriptions {
		c.emitSnapshot(subscription.connectionID, subscription.subscriptionID, subscription.topic, seq, rows)
	}
}

func ackJSON(subscriptionID string) json.RawMessage {
	ack, _ := json.Marshal(map[string]any{
		"ack": map[string]any{
			"subscriptionId": subscriptionID,
			"mode":           "snapshot",
			"logEpoch":       controllerLogEpoch,
		},
	})
	return ack
}

func normalizeControllerTopic(topic string) string {
	if topic == "controller/workspaces" {
		return topic
	}
	return "controller/tasks-index"
}

// emitSnapshot delivers one controller frame to the connection's
// onDynamicControllerFrame listeners.
func (c *WindowController) emitSnapshot(connectionID, subscriptionID, topic string, seq uint64, rows []taskRow) {
	topic = normalizeControllerTopic(topic)
	snapshot := map[string]any{
		"protocolVersion": 1,
		"logEpoch":        controllerLogEpoch,
	}
	if topic == "controller/workspaces" {
		snapshot["workspaces"] = []any{}
	} else {
		tasks := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			tasks = append(tasks, controllerRow(row))
		}
		snapshot["tasks"] = tasks
	}
	frame := map[string]any{
		"topic":          topic,
		"subscriptionId": subscriptionID,
		"logEpoch":       controllerLogEpoch,
		"fromSeq":        0,
		"toSeq":          seq,
		"sentAt":         time.Now().UnixMilli(),
		"payload": map[string]any{
			"kind":     "snapshot",
			"snapshot": snapshot,
		},
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return
	}
	c.mu.Lock()
	listeners := append([]controllerListener(nil), c.listens[connectionID]...)
	c.mu.Unlock()
	for _, listener := range listeners {
		listener.emit(json.RawMessage(encoded))
	}
}

// controllerRow is `WindowHostControllerTaskRow`.
func controllerRow(row taskRow) map[string]any {
	address := map[string]any{
		"workspacePath": row.workspacePath,
		"taskId":        row.taskID,
	}
	if row.workspaceIdentity != "" {
		address["workspaceIdentity"] = row.workspaceIdentity
	}
	return map[string]any{
		"address":            address,
		"meta":               row.meta,
		"membership":         map[string]any{"pinned": row.pinned, "archived": row.archived, "active": !row.archived},
		"sourceAvailability": "online",
		"liveStatus":         liveStatus(row.status),
	}
}

// Listen implements Channel. onDynamicControllerFrame is the only event; the
// payload is pushed when subscribe/resync is called, matching the reference
// service's notification ordering.
func (c *WindowController) Listen(_ context.Context, env *Env, event string, _ rpcwire.Value, emit func(rpcwire.Value)) (func(), error) {
	if event != "onDynamicControllerFrame" {
		return nil, nil
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.listens[env.ConnectionID] = append(c.listens[env.ConnectionID], controllerListener{id: id, emit: emit})
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		remaining := c.listens[env.ConnectionID][:0]
		for _, listener := range c.listens[env.ConnectionID] {
			if listener.id != id {
				remaining = append(remaining, listener)
			}
		}
		if len(remaining) == 0 {
			delete(c.listens, env.ConnectionID)
			// A dropped browser never calls unsubscribeControllerV4; forget its
			// subscriptions so background refreshes stop addressing it.
			for subscriptionID, owner := range c.subscriptionOwner {
				if owner != env.ConnectionID {
					continue
				}
				delete(c.subscriptionOwner, subscriptionID)
				delete(c.topics, subscriptionID)
			}
		} else {
			c.listens[env.ConnectionID] = remaining
		}
		c.mu.Unlock()
	}, nil
}
