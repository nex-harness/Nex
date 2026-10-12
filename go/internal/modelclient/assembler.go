package modelclient

// Assembler ports StreamingToolCallAssembler: it deduplicates tool-call
// lifecycle events by id, normalizes the tool name and the final JSON input, and
// suppresses a final tool_call that arrives while a streaming input is still open.
//
// The Node class stores normalized tool calls by id in a Map, preserving
// insertion order. snapshot returns a copy of those values in insertion order.
type Assembler struct {
	completedStreamingInputIDs map[string]bool
	normalizedToolCalls        map[string]ToolCall
	order                      []string
	providerExecutedByID       map[string]bool
	streamingInputIDs          map[string]bool

	// Recoveries records input-normalization recoveries for logging parity.
	recoveries []ToolInputRecovery
}

// ToolInputRecovery is one malformed-input recovery, keyed by tool name/source.
type ToolInputRecovery struct {
	ToolCallID string
	ToolName   string
	Status     ToolInputStatus
	Source     string
}

// NewAssembler returns an empty assembler.
func NewAssembler() *Assembler {
	return &Assembler{
		completedStreamingInputIDs: map[string]bool{},
		normalizedToolCalls:        map[string]ToolCall{},
		providerExecutedByID:       map[string]bool{},
		streamingInputIDs:          map[string]bool{},
	}
}

// SnapshotNormalizedToolCalls returns a copy of the normalized tool calls in
// insertion order.
func (a *Assembler) SnapshotNormalizedToolCalls() []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for _, id := range a.order {
		out = append(out, a.normalizedToolCalls[id])
	}
	return out
}

// Recoveries returns the recorded input recoveries in order.
func (a *Assembler) Recoveries() []ToolInputRecovery { return a.recoveries }

// Handle processes one decoded event and returns the events to emit. The mapping
// is:
//
//   - tool_input_start: drop if id already normalized; else normalize the name and
//     remember providerExecuted/streaming state.
//   - tool_input_delta: pass through only when the id is not yet normalized.
//   - tool_input_end: drop if normalized; else mark the streaming input complete.
//   - tool_call: final call; see handleToolCall.
//   - finish: flush then emit the finish event.
//   - everything else: pass through.
func (a *Assembler) Handle(ev Event) ([]Event, error) {
	switch ev.Kind {
	case EventToolInputStart:
		return a.handleToolInputStart(ev)
	case EventToolInputDelta:
		if _, ok := a.normalizedToolCalls[ev.ID]; ok {
			return nil, nil
		}
		return []Event{ev}, nil
	case EventToolInputEnd:
		if _, ok := a.normalizedToolCalls[ev.ID]; ok {
			return nil, nil
		}
		a.completedStreamingInputIDs[ev.ID] = true
		return []Event{ev}, nil
	case EventToolCall:
		return a.handleToolCall(ev.ToolCall)
	case EventFinish:
		a.Flush()
		return []Event{ev}, nil
	default:
		return []Event{ev}, nil
	}
}

// Flush clears per-stream state and returns nothing (matches Node flush()).
func (a *Assembler) Flush() []Event {
	a.completedStreamingInputIDs = map[string]bool{}
	a.providerExecutedByID = map[string]bool{}
	a.streamingInputIDs = map[string]bool{}
	return nil
}

func (a *Assembler) handleToolInputStart(ev Event) ([]Event, error) {
	if _, ok := a.normalizedToolCalls[ev.ID]; ok {
		return nil, nil
	}
	name, err := NormalizeToolName(ev.ToolName, hasProviderExecuted(ev), ev.ID)
	if err != nil {
		return nil, err
	}
	if ev.HasProviderExecuted {
		a.providerExecutedByID[ev.ID] = ev.ProviderExecuted
	}
	a.streamingInputIDs[ev.ID] = true
	out := ev
	out.ToolName = name
	return []Event{out}, nil
}

func (a *Assembler) handleToolCall(tc ToolCall) ([]Event, error) {
	if _, ok := a.normalizedToolCalls[tc.ID]; ok {
		return nil, nil
	}
	if a.streamingInputIDs[tc.ID] && !a.completedStreamingInputIDs[tc.ID] {
		// A streaming input lifecycle is open; the final call must not stand in for
		// the missing tool-input-end.
		return nil, nil
	}

	providerExecuted, hasProviderExecuted := false, false
	if tc.HasProviderExecuted {
		providerExecuted = tc.ProviderExecuted
		hasProviderExecuted = true
	} else if v, ok := a.providerExecutedByID[tc.ID]; ok {
		providerExecuted = v
		hasProviderExecuted = true
	}

	name, err := NormalizeToolName(tc.Name, providerExecuted, tc.ID)
	if err != nil {
		return nil, err
	}

	raw := any(tc.Arguments)
	if tc.HasInput {
		raw = tc.Input
	}
	input, status := NormalizeToolInput(raw)
	if status.Recovered {
		a.recoveries = append(a.recoveries, ToolInputRecovery{
			ToolCallID: tc.ID,
			ToolName:   name,
			Status:     status,
			Source:     "streamText",
		})
	}

	normalized := ToolCall{
		ID:       tc.ID,
		Name:     name,
		Input:    input,
		HasInput: true,
		// Arguments keeps the raw pre-normalization JSON so callers that need the
		// provider's key order (the v4 projection's inputText) can use it.
		Arguments:        tc.Arguments,
		ProviderExecuted: providerExecuted,
		// HasProviderExecuted mirrors the Node spread: providerExecuted is always
		// set (possibly undefined) on the normalized call.
		HasProviderExecuted: hasProviderExecuted,
	}

	delete(a.completedStreamingInputIDs, tc.ID)
	delete(a.providerExecutedByID, tc.ID)
	delete(a.streamingInputIDs, tc.ID)
	if _, exists := a.normalizedToolCalls[tc.ID]; !exists {
		a.order = append(a.order, tc.ID)
	}
	a.normalizedToolCalls[tc.ID] = normalized
	return []Event{{Kind: EventToolCall, ToolCall: normalized}}, nil
}

func hasProviderExecuted(ev Event) bool { return ev.HasProviderExecuted }

// MarshalCanonicalInput re-serializes a normalized tool input the way
// JSON.stringify would for the common object/array/scalar cases (compact, no
// HTML escaping). Used where the final tool input must round-trip as a string.
func MarshalCanonicalInput(input any) (string, error) {
	return marshalJS(input)
}
