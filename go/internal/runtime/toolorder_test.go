package runtime

import "testing"

// 事件序号的间隔取自 Node 的录制（heldout/read-two-files、heldout/bad-input）：
// 序号间隔只取决于两个事件之间追加了多少条事件，因此比绝对值更稳定。
func seqOf(t *testing.T, facts []toolFact, phase, callID string) int64 {
	t.Helper()
	for _, f := range facts {
		if f.phase == phase && f.toolCallID == callID {
			return f.seq
		}
	}
	t.Fatalf("no %s(%s) fact in %v", phase, callID, phases(facts))
	return 0
}

// Node 录制：scheduled(c1)=14 started(c1)=19 scheduled(c2)=21 started(c2)=25
// completed(c1)=28 completed(c2)=30。c1 的 scheduled 在 c2 的 tool_input_end 之前运行；
// 每个流式调用各有一个 batch，所以两条 completed 之间夹着 batch_complete。
func TestTwoStreamingCallsKeepNodeEventSpacing(t *testing.T) {
	ws := t.TempDir()
	a := writeFixture(t, ws, "a.txt", "A\n")
	b := writeFixture(t, ws, "b.txt", "B\n")
	run := runOneModelStep(t, sseTwoToolCalls("Read", `{"file_path":"`+a+`"}`, `{"file_path":"`+b+`"}`), ws, nil)

	s1, st1 := seqOf(t, run.facts, "scheduled", "c1"), seqOf(t, run.facts, "started", "c1")
	s2, st2 := seqOf(t, run.facts, "scheduled", "c2"), seqOf(t, run.facts, "started", "c2")
	d1, d2 := seqOf(t, run.facts, "completed", "c1"), seqOf(t, run.facts, "completed", "c2")
	if got := st1 - s1; got != 5 {
		t.Errorf("started(c1)-scheduled(c1) = %d, want 5", got)
	}
	if got := s2 - st1; got != 2 {
		t.Errorf("scheduled(c2)-started(c1) = %d, want 2", got)
	}
	if got := st2 - s2; got != 4 {
		t.Errorf("started(c2)-scheduled(c2) = %d, want 4", got)
	}
	if got := d2 - d1; got != 2 {
		t.Errorf("completed(c2)-completed(c1) = %d, want 2 (one batch_complete between)", got)
	}
}

// Node 录制：scheduled=12 failed=16。流式调用在 handler 之前失败时，
// tool_call_error 与 closed/queued/started 三条 ledger 都在流内产生。
func TestStreamedPreStartFailureKeepsNodeEventSpacing(t *testing.T) {
	run := runOneModelStep(t, sseToolCall("Read", `{"file_path":123}`), t.TempDir(), nil)
	if got := seqOf(t, run.facts, "failed", "c1") - seqOf(t, run.facts, "scheduled", "c1"); got != 4 {
		t.Errorf("failed-scheduled = %d, want 4", got)
	}
}
