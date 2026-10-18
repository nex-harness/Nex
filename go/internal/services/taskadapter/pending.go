package taskadapter

import "sync"

// pending.go — the pending optimistic state boundaries (guards E1–E8).
//
// The renderer keeps a pending optimistic list; the host keeps the queue. The rule is
// one-directional: the renderer's list is a PROJECTION of the host queue (E2), and the
// live-only buffers below are per-RUN state that must not survive into the next one.

// liveToolProjection is LiveToolProjection: one tool call observed live in the current
// run. `order` is the arrival order used for eviction.
type liveToolProjection struct {
	Order           int64
	ParentToolUseID string
	ToolID          string
	ToolCall        map[string]any
}

// Bounds for the live projection tables. These are the TS constants; they bound MEMORY,
// not correctness, so they are preserved verbatim rather than re-derived.
const (
	MaxLiveToolProjectionTasks        = 128
	MaxLiveToolProjectionToolsPerTask = 2000
)

// pendingState owns the live tool projections and the per-run streaming-input cache.
type pendingState struct {
	mu sync.Mutex
	// projections is taskKey → (toolId → liveToolProjection). Insertion order is kept
	// implicitly by the order field, because a plain map has no order.
	projections map[string]map[string]*liveToolProjection
	// projectionOrder is the eviction cursor: the task whose oldest order is smallest.
	projectionOrder int64
	// streamingInputs is taskKey → toolId → the streaming input built so far. It is
	// cleared whenever a new run starts, so a new turn never continues an old tool's
	// partial input.
	streamingInputs map[string]map[string]string
}

func newPendingState() *pendingState {
	return &pendingState{
		projections:     map[string]map[string]*liveToolProjection{},
		streamingInputs: map[string]map[string]string{},
	}
}

// ClearRun ports the two clears at the top of sendPromptToAgent (E1).
//
// Both are per-RUN state and both must die before the new run claims ownership:
//
//   - the live tool projection holds tools seen only in the PREVIOUS run's stream. Left
//     in place, the next run's snapshot would append them to the new reply's tail.
//   - the streaming-input cache holds a partial tool input. Left in place, a tool with
//     the same id in the new run would resume an input built from the old turn's bytes.
func (p *pendingState) ClearRun(target Target) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.projections, target.TaskKey())
	delete(p.streamingInputs, target.TaskKey())
}

// Remember ports rememberLiveToolProjection: it records one live tool and enforces the
// two bounds.
func (p *pendingState) Remember(target Target, toolID string, parentToolUseID string, toolCall map[string]any) {
	key := target.TaskKey()
	p.mu.Lock()
	defer p.mu.Unlock()
	projection, ok := p.projections[key]
	if !ok {
		projection = map[string]*liveToolProjection{}
		p.projections[key] = projection
		p.evictOldestTaskLocked()
	}
	if _, exists := projection[toolID]; !exists {
		p.projectionOrder++
	}
	projection[toolID] = &liveToolProjection{
		Order:           p.projectionOrder,
		ParentToolUseID: parentToolUseID,
		ToolID:          toolID,
		ToolCall:        toolCall,
	}
	p.trimTaskLocked(projection)
}

// evictOldestTaskLocked drops the oldest task's projection once the task count exceeds
// the budget (E5). Evicting by ARRIVAL order matters: the most recent run is the one
// a snapshot may need.
func (p *pendingState) evictOldestTaskLocked() {
	for len(p.projections) > MaxLiveToolProjectionTasks {
		var oldestKey string
		var oldestOrder int64
		first := true
		for key, projection := range p.projections {
			if first {
				oldestKey, oldestOrder, first = key, oldestOrderOf(projection), false
				continue
			}
			if order := oldestOrderOf(projection); order < oldestOrder {
				oldestKey, oldestOrder = key, order
			}
		}
		if oldestKey == "" {
			return
		}
		delete(p.projections, oldestKey)
	}
}

func oldestOrderOf(projection map[string]*liveToolProjection) int64 {
	oldest := int64(0)
	first := true
	for _, item := range projection {
		if first || item.Order < oldest {
			oldest = item.Order
			first = false
		}
	}
	return oldest
}

// trimTaskLocked drops the LOWEST-order tools once the per-task budget is exceeded
// (E6). The newest tool is kept: a live projection must never drop the tool currently
// streaming.
func (p *pendingState) trimTaskLocked(projection map[string]*liveToolProjection) {
	for len(projection) > MaxLiveToolProjectionToolsPerTask {
		var oldestID string
		var oldestOrder int64
		first := true
		for toolID, item := range projection {
			if first || item.Order < oldestOrder {
				oldestID, oldestOrder, first = toolID, item.Order, false
			}
		}
		if oldestID == "" {
			return
		}
		delete(projection, oldestID)
	}
}

// Snapshot copies one task's live projection in arrival order.
func (p *pendingState) Snapshot(target Target) []liveToolProjection {
	p.mu.Lock()
	defer p.mu.Unlock()
	projection := p.projections[target.TaskKey()]
	out := make([]liveToolProjection, 0, len(projection))
	for _, item := range projection {
		out = append(out, *item)
	}
	// Insertion sort by order: the slice is tiny and this keeps the file dependency-free.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Order > out[j].Order; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// AppendStreamingInput ports appendNexStreamingToolInputDelta.
func (p *pendingState) AppendStreamingInput(target Target, toolID, delta string) string {
	key := target.TaskKey()
	p.mu.Lock()
	defer p.mu.Unlock()
	inputs, ok := p.streamingInputs[key]
	if !ok {
		inputs = map[string]string{}
		p.streamingInputs[key] = inputs
	}
	inputs[toolID] += delta
	return inputs[toolID]
}

// ForgetTask drops every per-task buffer. Called when a task is deleted or closed: the
// buffers would otherwise outlive the task and be counted against the next one's
// budget.
func (p *pendingState) ForgetTask(target Target) { p.ClearRun(target) }

// Diagnostics ports the memoryDiagnostics counters: read-only sizes.
func (p *pendingState) Diagnostics() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]int{
		"liveProjectionTasks": len(p.projections),
		"streamingInputTasks": len(p.streamingInputs),
	}
}

// PendingCommands builds the snapshot's `runtime.pendingCommands` member (E2). It is
// always the HOST queue: a phone refresh reads the host, and a renderer-only pending
// list would be lost or contradicted on the next snapshot.
func PendingCommands(q *queue, target Target) []Command {
	commands := q.list(target.TaskKey())
	if commands == nil {
		// 手机 replayable 快照需要用空数组表达「用户输入队列已清空」。
		return []Command{}
	}
	return commands
}

// IncludeEmptyPendingElicitations ports the `includeEmptyPendingElicitations` rule
// (E3): the replayable phone path needs an EXPLICIT empty set to clear a stale prompt
// on restore, while desktop-continuous must not receive one — there an empty set would
// clear prompts that are legitimately in flight.
func IncludeEmptyPendingElicitations(clientMode string, pendingCount int) bool {
	return pendingCount > 0 || clientMode == "web-remote-replayable"
}
