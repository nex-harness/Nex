package v4pub

import (
	"encoding/json"
	"strings"
	"testing"
)

// 本文件是 AGENTS.md「Desktop 的 desktop-continuous 实时链路与手机的
// web-remote-replayable 恢复链路必须明确区分」的落点：订阅裁决、回放、缓冲溢出的
// 每一条分支都在两个 clientMode 上各跑一遍，模式不同的地方两边都断言。
//
// 两条链路的唯一权威映射在 helloMessageSchema.superRefine
// （packages/shared/src/nex-protocol-v4/transport.ts:55）：
//
//	desktop-continuous  <-> continuous
//	web-remote-replayable <-> replayable
//
// 两者落到的可观测差异只有一处——row.delta 的 streaming path 开关：
// continuous 收 text/inputText/output.text/summaryText，replayable 只收 text。
// 结构性整行（row.appended/row.upserted）在两个 profile 都是同一产品事实，一律放行。
// 因此本文件里 replayable 分支的断言重点是"被滤掉的 row.delta 不出现在帧里，但
// 帧区间与水位不受影响"。

// modeCase 是一个 clientMode 及其权威 delivery profile。
type modeCase struct {
	clientMode string
	profile    DeliveryProfileName
	// streamsInputText 等标记该 profile 是否流式下发对应 path。
	streamsInputText   bool
	streamsOutputText  bool
	streamsSummaryText bool
}

// bothModes 是被测试的且仅有的两条链路。
var bothModes = []modeCase{
	{clientMode: "desktop-continuous", profile: ProfileContinuous, streamsInputText: true, streamsOutputText: true, streamsSummaryText: true},
	{clientMode: "web-remote-replayable", profile: ProfileReplayable, streamsInputText: false, streamsOutputText: false, streamsSummaryText: false},
}

// --- 小工具 -----------------------------------------------------------------

type modeHarness struct {
	pub *Publisher
	src *scriptedSource
}

func newModeHarness(t testing.TB, sessionID string, opts Options) *modeHarness {
	t.Helper()
	if opts.Now == nil {
		opts.Now = func() int64 { return 1_700_000_000_000 }
	}
	src := newScriptedSource(sessionID, "epoch-modes")
	opts.Source = src
	pub, err := NewPublisher(sessionID, "epoch-modes", opts)
	if err != nil {
		t.Fatal(err)
	}
	return &modeHarness{pub: pub, src: src}
}

// ingest 让投影前进到 seq，并产出 deltas（权威日志序不变）。
func (h *modeHarness) ingest(t testing.TB, seq int64, deltas []Delta) {
	t.Helper()
	h.src.seq = seq
	h.src.revision = seq
	h.src.snapshot = withSeqRevision(h.src.snapshot, seq, seq)
	h.src.deltas = deltas
	if err := h.pub.Ingest(nil); err != nil {
		t.Fatalf("ingest seq=%d: %v", seq, err)
	}
}

// subscribe 用该模式的权威 profile 建一条订阅并提交初始帧。
func (h *modeHarness) subscribe(t testing.TB, m modeCase, conn string, base *Base) *SubscribeResult {
	t.Helper()
	if got := ProfileForClientMode(m.clientMode); got != m.profile {
		t.Fatalf("ProfileForClientMode(%q)=%q, want %q", m.clientMode, got, m.profile)
	}
	result, err := h.pub.SubscribeReserved(SubscribeParams{
		ConnectionID:    conn,
		DeliveryProfile: m.profile,
		Base:            base,
	})
	if err != nil {
		t.Fatalf("subscribe %s: %v", m.clientMode, err)
	}
	if result.Reservation != nil {
		result.Reservation.Commit()
	}
	return result
}

// flush 排空一条订阅并返回已提交的帧；无新内容返回 nil。
func (h *modeHarness) flush(t testing.TB, subID string) *Frame {
	t.Helper()
	reservation := h.pub.ReserveFlush(subID)
	if reservation == nil {
		return nil
	}
	if !reservation.Commit() {
		t.Fatalf("flush reservation for %s did not commit", subID)
	}
	frame := reservation.Frame
	return &frame
}

type snapshotProbe struct {
	Seq  int64 `json:"seq"`
	Rows struct {
		Window []struct {
			RowID int64 `json:"rowId"`
		} `json:"window"`
		TotalCount int   `json:"totalCount"`
		FirstRowID int64 `json:"firstRowId"`
	} `json:"rows"`
}

func decodeSnapshot(t testing.TB, raw json.RawMessage) snapshotProbe {
	t.Helper()
	var p snapshotProbe
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode snapshot: %v\n%s", err, raw)
	}
	return p
}

// rowDeltaPaths 列出帧内 row.delta 的 path，按帧内顺序。
func rowDeltaPaths(frame *Frame) []string {
	var paths []string
	for _, d := range frame.Payload.Deltas {
		if d.Op == OpRowDelta {
			paths = append(paths, d.Path)
		}
	}
	return paths
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func ops(t testing.TB, frame *Frame) int {
	t.Helper()
	return len(frame.Payload.Deltas)
}

func textRow(rowID int64, text string) string {
	return `{"op":"row.appended","row":{"rowId":` + itoa(rowID) + `,"kind":"assistantText","text":"` + text + `"}}`
}

func textDelta(rowID int64, path, append string) string {
	return `{"op":"row.delta","rowId":` + itoa(rowID) + `,"path":"` + path + `","append":"` + append + `"}`
}

// deltaList joins per-op JSON fragments into one array. Variadic so a batch
// reads as one op per line (a trailing string literal would otherwise trigger
// Go's automatic semicolon insertion inside the call).
func deltaList(t testing.TB, ops ...string) []Delta {
	t.Helper()
	return mustParse(t, "["+strings.Join(ops, ",")+"]")
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// --- (1) clientMode -> profile 映射 ------------------------------------------

// TestBothModesClientModeProfileMapping pins the two trusted pairings from
// helloMessageSchema.superRefine and asserts the pairing is a bijection: no
// profile may be reachable from the other client mode (that mismatch is a
// handshake error, not a silent downgrade).
func TestBothModesClientModeProfileMapping(t *testing.T) {
	inverse := map[DeliveryProfileName]string{
		ProfileContinuous: "desktop-continuous",
		ProfileReplayable: "web-remote-replayable",
	}
	for _, m := range bothModes {
		if got := ProfileForClientMode(m.clientMode); got != m.profile {
			t.Errorf("ProfileForClientMode(%q)=%q, want %q", m.clientMode, got, m.profile)
		}
		// The inverse must round-trip: profile -> its only legal clientMode.
		if got := ProfileForClientMode(inverse[m.profile]); got != m.profile {
			t.Errorf("profile %q is not reachable from its own client mode", m.profile)
		}
	}

	// The on-wire consequence of the mapping is the streaming-path switch set.
	cont := DeliveryProfiles[ProfileContinuous]
	replay := DeliveryProfiles[ProfileReplayable]
	for _, path := range []string{"text", "inputText", "output.text", "summaryText"} {
		if !cont.streamPath(path) {
			t.Errorf("continuous must stream %q", path)
		}
		if replay.streamPath(path) && path != "text" {
			t.Errorf("replayable must not stream %q", path)
		}
	}
	if !replay.streamPath("text") {
		t.Error("replayable must still stream \"text\"")
	}
	if cont.ToolProgress || !replay.ToolProgress {
		t.Errorf("toolProgress: continuous=%v replayable=%v", cont.ToolProgress, replay.ToolProgress)
	}
}

// --- (a) 首次订阅 -> snapshot ------------------------------------------------

// TestBothModesSubscribeDeliversSnapshotFrame pins that a first subscribe (no
// base) gets an ACK-only verdict `snapshot` plus one initial snapshot frame in
// both modes, with the frame interval (0, currentSeq].
//
// The two profiles currently differ on nothing in the snapshot row visibility
// (filterConversationRowsForProfile is the identity for every row kind), so the
// snapshot bytes must be byte-identical across modes. That equality is the pin:
// when a desktopOnlyRows row finally exists, this test fails instead of one mode
// silently gaining or losing a row.
func TestBothModesSubscribeDeliversSnapshotFrame(t *testing.T) {
	results := map[string][]byte{}
	for _, m := range bothModes {
		t.Run(m.clientMode, func(t *testing.T) {
			// Same session id on both branches: only the client mode may differ,
			// so any snapshot difference below is a real product difference and
			// not an artefact of two unrelated fixtures.
			h := newModeHarness(t, "sess-shared", Options{})
			h.ingest(t, 3, mustParse(t, "["+textRow(1, "a")+","+textRow(2, "b")+"]"))

			r := h.subscribe(t, m, "conn", nil)
			if r.Ack.Mode != ModeSnapshot {
				t.Fatalf("mode=%q, want snapshot", r.Ack.Mode)
			}
			if r.Ack.LogEpoch != "epoch-modes" {
				t.Errorf("logEpoch=%q, want epoch-modes", r.Ack.LogEpoch)
			}
			if r.Reservation == nil || r.InitialFrame == nil {
				t.Fatal("initial snapshot frame must be carried outside the ACK")
			}
			if r.Reservation.DeliveryKind != DeliveryInitial {
				t.Errorf("deliveryKind=%q, want initial", r.Reservation.DeliveryKind)
			}
			f := r.InitialFrame
			if f.Payload.Kind != "snapshot" {
				t.Fatalf("payload kind=%q, want snapshot", f.Payload.Kind)
			}
			if f.FromSeq != 0 || f.ToSeq != 3 {
				t.Errorf("frame interval=(%d,%d], want (0,3]", f.FromSeq, f.ToSeq)
			}
			snap := decodeSnapshot(t, f.Payload.Snapshot)
			if len(snap.Rows.Window) != 2 || snap.Rows.TotalCount != 2 || snap.Rows.FirstRowID != 1 {
				t.Errorf("snapshot rows drifted: %+v", snap.Rows)
			}
			results[string(m.profile)] = f.FrameJSON()
		})
	}
	if string(results[string(ProfileContinuous)]) != string(results[string(ProfileReplayable)]) {
		t.Error("snapshot frame bytes differ between the two modes; row visibility is mode-independent today")
	}
}

// --- (b) 重连：base 有效 -> resume -------------------------------------------

// TestBothModesResumeReplaysFromBaseSeq pins the reconnect contract in both
// modes: a base with the matching epoch and seq inside (floorSeq, currentSeq]
// resumes, the ACK says `resume`, and the initial frame is the (base.seq,
// currentSeq] slice of the retained log through the *same* filter -> coalesce
// pipeline the live link uses.
//
// The mode branches are asserted separately: desktop-continuous replays the
// streaming deltas on inputText / output.text / summaryText, the
// web-remote-replayable recovery link drops them and replays only `text`.
// Structural whole rows are the same product fact in both.
func TestBothModesResumeReplaysFromBaseSeq(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.clientMode, func(t *testing.T) {
			h := newModeHarness(t, "sess-"+string(m.profile), Options{})
			h.ingest(t, 1, mustParse(t, "["+textRow(1, "")+"]"))
			// seq 2 里混着四条 streaming path 的 row.delta。
			h.ingest(t, 2, deltaList(t,
				textDelta(1, "text", "hel"),
				textDelta(1, "inputText", "cmd"),
				textDelta(1, "output.text", "out"),
				textDelta(1, "summaryText", "sum"),
			))
			h.ingest(t, 3, mustParse(t, "["+textRow(2, "next")+"]"))

			r := h.subscribe(t, m, "conn", &Base{LogEpoch: "epoch-modes", Seq: 1})
			if r.Ack.Mode != ModeResume {
				t.Fatalf("mode=%q, want resume", r.Ack.Mode)
			}
			if r.Reservation == nil || r.InitialFrame == nil {
				t.Fatal("resume past the base must carry an initial deltas frame")
			}
			if r.Reservation.DeliveryKind != DeliveryInitial {
				t.Errorf("deliveryKind=%q, want initial", r.Reservation.DeliveryKind)
			}
			f := r.InitialFrame
			if f.Payload.Kind != "deltas" {
				t.Fatalf("payload kind=%q, want deltas", f.Payload.Kind)
			}
			if f.FromSeq != 1 || f.ToSeq != 3 {
				t.Errorf("resume interval=(%d,%d], want (1,3]", f.FromSeq, f.ToSeq)
			}
			paths := rowDeltaPaths(f)
			if !containsPath(paths, "text") {
				t.Errorf("both modes replay text deltas; got %v", paths)
			}
			for _, gated := range []struct {
				path string
				on   bool
			}{
				{"inputText", m.streamsInputText},
				{"output.text", m.streamsOutputText},
				{"summaryText", m.streamsSummaryText},
			} {
				if got := containsPath(paths, gated.path); got != gated.on {
					t.Errorf("%s streaming delta present=%v, want %v (paths=%v)", gated.path, got, gated.on, paths)
				}
			}
			// row.appended is a structural whole row: same fact in both modes, so
			// it must be in both replays.
			var appended int
			for _, d := range f.Payload.Deltas {
				if d.Op == OpRowAppended {
					appended++
				}
			}
			if appended != 1 {
				t.Errorf("structural row.appended count=%d, want 1 in both modes", appended)
			}
		})
	}
}

// TestBothModesResumeAtCurrentSeqSendsNoInitialFrame pins the empty-replay edge
// in both modes: a base that already equals currentSeq resumes with no frame at
// all, and the watermark is adopted so the next flush starts from there.
func TestBothModesResumeAtCurrentSeqSendsNoInitialFrame(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.clientMode, func(t *testing.T) {
			h := newModeHarness(t, "sess-"+string(m.profile), Options{})
			h.ingest(t, 1, mustParse(t, "["+textRow(1, "a")+"]"))
			h.ingest(t, 2, mustParse(t, "["+textRow(2, "b")+"]"))

			r := h.subscribe(t, m, "conn", &Base{LogEpoch: "epoch-modes", Seq: 2})
			if r.Ack.Mode != ModeResume {
				t.Fatalf("mode=%q, want resume", r.Ack.Mode)
			}
			if r.Reservation != nil || r.InitialFrame != nil {
				t.Fatal("base == currentSeq must not emit an initial frame")
			}
			if f := h.flush(t, r.Ack.SubscriptionID); f != nil {
				t.Errorf("flush with nothing new = %+v, want nil", f)
			}
			h.ingest(t, 3, mustParse(t, "["+textRow(3, "c")+"]"))
			f := h.flush(t, r.Ack.SubscriptionID)
			if f == nil {
				t.Fatal("expected a deltas frame after the next ingest")
			}
			if f.FromSeq != 2 || f.ToSeq != 3 {
				t.Errorf("continuity broken: frame interval=(%d,%d], want (2,3]", f.FromSeq, f.ToSeq)
			}
		})
	}
}

// TestBothModesResumeIntervalCoversFilteredSeqs pins the client-side contiguity
// invariant (`frame.fromSeq === store.seq`) in both modes: the frame interval is
// the authoritative (sentSeq, currentSeq] even when the subscriber's profile
// filtered out every delta in a seq, so a replayable link that drops all
// inputText/output.text deltas still gets a well-formed interval rather than a
// gap it cannot explain.
func TestBothModesResumeIntervalCoversFilteredSeqs(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.clientMode, func(t *testing.T) {
			h := newModeHarness(t, "sess-"+string(m.profile), Options{})
			h.ingest(t, 1, mustParse(t, "["+textRow(1, "")+"]"))
			// seq 2 produces only deltas this mode is allowed to drop.
			h.ingest(t, 2, deltaList(t,
				textDelta(1, "inputText", "cmd"),
				textDelta(1, "output.text", "out"),
				textDelta(1, "summaryText", "sum"),
			))

			r := h.subscribe(t, m, "conn", &Base{LogEpoch: "epoch-modes", Seq: 1})
			if r.Ack.Mode != ModeResume {
				t.Fatalf("mode=%q, want resume", r.Ack.Mode)
			}
			f := r.InitialFrame
			if f == nil {
				t.Fatal("expected an initial frame")
			}
			if f.FromSeq != 1 || f.ToSeq != 2 {
				t.Errorf("interval=(%d,%d], want (1,2] regardless of filtering", f.FromSeq, f.ToSeq)
			}
			if m.profile == ProfileReplayable && ops(t, f) != 0 {
				t.Errorf("replayable must replay nothing from a fully filtered seq; got %d ops", ops(t, f))
			}
			if m.profile == ProfileContinuous && ops(t, f) == 0 {
				t.Error("continuous replays the streaming deltas")
			}
		})
	}
}

// --- (c) base 失效 -> snapshot -----------------------------------------------

// TestBothModesInvalidBaseFallsBackToSnapshot pins the negative half of the
// subscribe arbitration in both modes. Every rejection reason (base evicted by
// the retention window, wrong epoch after an agent restart, a seq ahead of the
// publisher, and an absent base) degrades to a full snapshot, never to a resume
// that would hand the client a log it cannot reconcile.
func TestBothModesInvalidBaseFallsBackToSnapshot(t *testing.T) {
	cases := []struct {
		name      string
		retention *int
		ingests   []int64
		base      *Base
	}{
		{name: "absent-base", retention: nil, ingests: []int64{1, 2}, base: nil},
		{name: "base-too-old", retention: ptr(2), ingests: []int64{1, 2, 3}, base: &Base{LogEpoch: "epoch-modes", Seq: 0}},
		{name: "wrong-epoch", retention: nil, ingests: []int64{1, 2}, base: &Base{LogEpoch: "epoch-other", Seq: 1}},
		{name: "future-seq", retention: nil, ingests: []int64{1, 2}, base: &Base{LogEpoch: "epoch-modes", Seq: 3}},
	}
	for _, c := range cases {
		for _, m := range bothModes {
			t.Run(c.name+"/"+m.clientMode, func(t *testing.T) {
				h := newModeHarness(t, "sess-"+string(m.profile), Options{Retention: c.retention})
				for _, seq := range c.ingests {
					h.ingest(t, seq, mustParse(t, "["+textRow(seq, "x")+"]"))
				}

				r := h.subscribe(t, m, "conn", c.base)
				if r.Ack.Mode != ModeSnapshot {
					t.Fatalf("base %+v: mode=%q, want snapshot", c.base, r.Ack.Mode)
				}
				if r.Reservation == nil || r.InitialFrame == nil {
					t.Fatal("snapshot verdict must carry an initial snapshot frame")
				}
				f := r.InitialFrame
				if f.Payload.Kind != "snapshot" {
					t.Errorf("payload kind=%q, want snapshot", f.Payload.Kind)
				}
				current := c.ingests[len(c.ingests)-1]
				if f.FromSeq != 0 || f.ToSeq != current {
					t.Errorf("snapshot interval=(%d,%d], want (0,%d]", f.FromSeq, f.ToSeq, current)
				}
				// The snapshot is the authority the client restarts from.
				snap := decodeSnapshot(t, f.Payload.Snapshot)
				if len(snap.Rows.Window) == 0 {
					t.Error("snapshot must carry the current rows")
				}
			})
		}
	}
}

func ptr(v int) *int { return &v }

// --- (d) 慢订阅者缓冲溢出 -> resync-required --------------------------------

// TestBothModesBackpressureOverflowFaultIsolation pins the per-subscriber
// backpressure contract in both modes: a slow subscriber that exceeds
// subscriberBufferMaxOps degrades to a resync-required fault (the next flush
// hands it a snapshot, no silent delta drop), while a healthy subscriber on the
// same topic and the same mode keeps receiving ordered deltas.
//
// The budget is publisher-wide but the buffer is per subscription, so "healthy"
// here means "flushes inside every window" — exactly the desktop host driving
// flushWindowMs, versus a phone link that stops draining.
func TestBothModesBackpressureOverflowFaultIsolation(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.clientMode, func(t *testing.T) {
			maxOps := 1
			h := newModeHarness(t, "sess-"+string(m.profile), Options{SubscriberBufferMaxOps: &maxOps})
			slow := h.subscribe(t, m, "slow", nil)
			healthy := h.subscribe(t, m, "healthy", nil)

			// Drain the healthy subscriber every step so it never accumulates.
			drain := func() {
				t.Helper()
				if f := h.flush(t, healthy.Ack.SubscriptionID); f != nil && f.Payload.Kind != "deltas" {
					t.Fatalf("healthy subscriber got %q, want deltas", f.Payload.Kind)
				}
			}

			h.ingest(t, 1, mustParse(t, "["+textRow(1, "a")+"]"))
			drain()
			// The slow subscriber now holds one op; each further ingest puts it
			// one over the limit of one.
			h.ingest(t, 2, mustParse(t, "["+textRow(2, "b")+"]"))
			drain()
			h.ingest(t, 3, mustParse(t, "["+textRow(3, "c")+"]"))
			drain()
			h.ingest(t, 4, mustParse(t, "["+textRow(4, "d")+"]"))
			drain()

			// Fault: a snapshot, not a truncated delta batch.
			fault := h.flush(t, slow.Ack.SubscriptionID)
			if fault == nil {
				t.Fatal("the overflowing subscriber must receive a resync-required frame")
			}
			if fault.Payload.Kind != "snapshot" {
				t.Fatalf("overflow frame kind=%q, want snapshot (resync-required)", fault.Payload.Kind)
			}
			if fault.FromSeq != 0 || fault.ToSeq != 4 {
				t.Errorf("recovery frame interval=(%d,%d], want (0,4]", fault.FromSeq, fault.ToSeq)
			}
			snap := decodeSnapshot(t, fault.Payload.Snapshot)
			if len(snap.Rows.Window) != 4 {
				t.Errorf("recovery snapshot has %d rows, want the full 4", len(snap.Rows.Window))
			}

			// The other subscriber on the same topic is untouched by the fault:
			// it never saw resyncRequired, so the next event still reaches it as
			// a plain delta batch with a contiguous interval.
			h.ingest(t, 5, mustParse(t, "["+textRow(5, "e")+"]"))
			healthyFrame := h.flush(t, healthy.Ack.SubscriptionID)
			if healthyFrame == nil {
				t.Fatal("the healthy subscriber must still be draining deltas")
			}
			if healthyFrame.Payload.Kind != "deltas" {
				t.Fatalf("healthy subscriber got %q, want deltas", healthyFrame.Payload.Kind)
			}
			if healthyFrame.FromSeq != 4 || healthyFrame.ToSeq != 5 {
				t.Errorf("healthy frame interval=(%d,%d], want (4,5]", healthyFrame.FromSeq, healthyFrame.ToSeq)
			}

			// Once the subscriber drains the recovery frame, the watermark is realigned:
			// the next event reaches it as a plain delta batch, not another fault.
			// (A subscriber that stalls again simply re-arms resyncRequired — the
			// snapshot-in-flight case re-checks the watermark on commit, as
			// reserveFrame's snapshotRecovery branch does in the TS.)
			recovered := h.flush(t, slow.Ack.SubscriptionID)
			if recovered == nil {
				t.Fatal("slow subscriber must be in step after the recovery frame")
			}
			if recovered.Payload.Kind != "deltas" || recovered.FromSeq != 4 || recovered.ToSeq != 5 {
				t.Fatalf("post-recovery frame = %s (%d,%d], want deltas (4,5]",
					recovered.Payload.Kind, recovered.FromSeq, recovered.ToSeq)
			}
		})
	}
}

// TestBothModesBackpressureFaultIsNotSharedAcrossLinks pins that the overflow is
// per subscription and not per mode: one overflowing desktop-continuous
// subscriber does not degrade a web-remote-replayable subscriber on the same
// publisher, and the reverse. The two links stay observably independent.
func TestBothModesBackpressureFaultIsNotSharedAcrossLinks(t *testing.T) {
	maxOps := 1
	h := newModeHarness(t, "sess-mixed", Options{SubscriberBufferMaxOps: &maxOps})
	desktop := h.subscribe(t, bothModes[0], "desktop", nil)
	phone := h.subscribe(t, bothModes[1], "phone", nil)

	// Only the desktop subscriber stalls; the phone link keeps draining.
	h.ingest(t, 1, mustParse(t, "["+textRow(1, "a")+"]"))
	if f := h.flush(t, phone.Ack.SubscriptionID); f == nil || f.Payload.Kind != "deltas" {
		t.Fatalf("phone link got %v, want deltas", f)
	}
	h.ingest(t, 2, mustParse(t, "["+textRow(2, "b")+"]"))
	if f := h.flush(t, phone.Ack.SubscriptionID); f == nil || f.Payload.Kind != "deltas" {
		t.Fatalf("phone link got %v, want deltas", f)
	}
	h.ingest(t, 3, mustParse(t, "["+textRow(3, "c")+"]"))
	if f := h.flush(t, phone.Ack.SubscriptionID); f == nil || f.Payload.Kind != "deltas" {
		t.Fatalf("phone link got %v, want deltas", f)
	}

	fault := h.flush(t, desktop.Ack.SubscriptionID)
	if fault == nil || fault.Payload.Kind != "snapshot" {
		t.Fatalf("stalled desktop link got %v, want a resync-required snapshot", fault)
	}
}

// TestBothModesProfileFilterShiftsBackpressureBudget pins the one place where
// the two links genuinely diverge under backpressure: the profile filter runs
// *before* the op budget, so an event batch that overflows the continuous link
// fits comfortably in the replayable one because its streaming deltas are
// dropped first.
//
// Both verdicts are asserted: the same event sequence must not fault the
// web-remote-replayable recovery link while it faults desktop-continuous.
func TestBothModesProfileFilterShiftsBackpressureBudget(t *testing.T) {
	verdicts := map[DeliveryProfileName]bool{}
	for _, m := range bothModes {
		t.Run(m.clientMode, func(t *testing.T) {
			// 4 ops in the log, budget 3: only a link that keeps the streaming
			// deltas can exceed it.
			maxOps := 3
			h := newModeHarness(t, "sess-"+string(m.profile), Options{SubscriberBufferMaxOps: &maxOps})
			sub := h.subscribe(t, m, "conn", nil)
			h.ingest(t, 1, deltaList(t,
				textRow(1, ""),
				textDelta(1, "inputText", "cmd"),
				textDelta(1, "output.text", "out"),
				textDelta(1, "summaryText", "sum"),
			))

			f := h.flush(t, sub.Ack.SubscriptionID)
			if f == nil {
				t.Fatal("expected a frame")
			}
			faulted := f.Payload.Kind == "snapshot"
			verdicts[m.profile] = faulted
			if m.profile == ProfileContinuous && !faulted {
				t.Errorf("continuous keeps all 4 ops and must overflow; got %s with %d ops", f.Payload.Kind, ops(t, f))
			}
			if m.profile == ProfileReplayable && faulted {
				t.Errorf("replayable keeps only the structural row.appended and must not overflow; got %s", f.Payload.Kind)
			}
			if !faulted && f.FromSeq != 0 || f.ToSeq != 1 {
				t.Errorf("non-fault frame interval=(%d,%d], want (0,1]", f.FromSeq, f.ToSeq)
			}
		})
	}
	if !verdicts[ProfileContinuous] {
		t.Error("desktop-continuous must fault on this batch")
	}
	if verdicts[ProfileReplayable] {
		t.Error("web-remote-replayable must not fault on the same batch")
	}
}
