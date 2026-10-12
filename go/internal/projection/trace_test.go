package projection

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestS02CoalescedMatchesRecordedTrace compares the Go projection's coalesced
// delta stream for S02 to the deltas the REAL Node agent published for the same
// scenario (go/testdata/traces/agent/S02/out.normalized.json).
//
// The recorded trace is normalized: timestamps become "<time>", ids become
// "<idN>", the workspace becomes "<WS>". This test canonicalizes both sides by
// replacing id-like strings and large numbers with "<P>", so it checks structure,
// ordering and every non-identifier value. It is the end-to-end check that the
// Go projection reproduces the 14 wire deltas of S02.
func TestS02CoalescedMatchesRecordedTrace(t *testing.T) {
	vf := loadVectors(t)
	var s02 *struct {
		Prelude []eventCase
		Events  []eventCase
	}
	for i := range vf.Scenarios {
		if vf.Scenarios[i].ID == "S02" {
			s02 = &struct {
				Prelude []eventCase
				Events  []eventCase
			}{Prelude: vf.Scenarios[i].Prelude, Events: vf.Scenarios[i].Events}
			break
		}
	}
	if s02 == nil {
		t.Fatal("S02 scenario missing from vectors")
	}

	traceData, err := os.ReadFile(filepath.Join("..", "..", "testdata", "traces", "agent", "S02", "out.normalized.json"))
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	frames, err := decodeValue(json.NewDecoder(bytesReader(traceData)))
	if err != nil {
		t.Fatalf("parse trace: %v", err)
	}
	frameList, _ := frames.([]any)
	var wantDeltas []any
	for _, f := range frameList {
		frame, ok := f.(*obj)
		if !ok || strOf(frame.get("method")) != "v4/conversation/deltas(flattened)" {
			continue
		}
		deltas, _ := frame.get("deltas").([]any)
		for _, d := range deltas {
			wantDeltas = append(wantDeltas, canonicalizeValue(d))
		}
	}
	if len(wantDeltas) == 0 {
		t.Fatal("no deltas(flattened) entry in S02 trace")
	}

	p := New(vf.Session, vf.LogEpoch)
	for _, ec := range s02.Prelude {
		event, err := ParseEvent(ec.Input)
		if err != nil {
			t.Fatalf("parse prelude: %v", err)
		}
		if _, err := p.Apply(event); err != nil {
			t.Fatalf("apply prelude: %v", err)
		}
	}
	var all []Delta
	for _, ec := range s02.Events {
		event, err := ParseEvent(ec.Input)
		if err != nil {
			t.Fatalf("parse event: %v", err)
		}
		deltas, err := p.Apply(event)
		if err != nil {
			t.Fatalf("apply event: %v", err)
		}
		all = append(all, deltas...)
	}
	coalesced := coalesceConversationDeltas(all)

	if len(coalesced) != len(wantDeltas) {
		t.Fatalf("S02 coalesced delta count %d != trace %d", len(coalesced), len(wantDeltas))
	}
	for i, d := range coalesced {
		got := canonicalizeValue(d)
		if !jsonDeepEqual(got, wantDeltas[i]) {
			gotJSON, _ := marshalValue(got)
			wantJSON, _ := marshalValue(wantDeltas[i])
			a, b := string(gotJSON), string(wantJSON)
			pos := 0
			for pos < len(a) && pos < len(b) && a[pos] == b[pos] {
				pos++
			}
			lo := pos - 40
			if lo < 0 {
				lo = 0
			}
			t.Fatalf("S02 delta %d differs at %d\n got[...]=%q\nwant[...]=%q\n got=%s\nwant=%s",
				i, pos, safeSlice(a, lo), safeSlice(b, lo), a, b)
		}
	}
}

func safeSlice(s string, from int) string {
	if from >= len(s) {
		return ""
	}
	end := from + 80
	if end > len(s) {
		end = len(s)
	}
	return s[from:end]
}

var idLike = regexp.MustCompile(`^(msg_|call_|call-|ev_|tr_|req_|sess_|epoch_|perm-|runtime_command|cmd-0001)`)

// canonicalizeValue replaces identifier-like strings and large numbers with
// "<P>", mirroring the trace normalizer's placeholders.
func canonicalizeValue(value any) any {
	switch v := value.(type) {
	case *obj:
		for i := range v.vals {
			v.vals[i] = canonicalizeValue(v.vals[i])
		}
		return v
	case []any:
		for i := range v {
			v[i] = canonicalizeValue(v[i])
		}
		return v
	case string:
		if strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">") {
			return "<P>"
		}
		if idLike.MatchString(v) {
			return "<P>"
		}
		return strings.ReplaceAll(v, "/work/proj/", "<WS>/")
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 1_000_000_000_000 {
			return "<P>"
		}
		return v
	case float64:
		if v > 1_000_000_000_000 {
			return "<P>"
		}
		return v
	case int64:
		if v > 1_000_000_000_000 {
			return "<P>"
		}
		return v
	case int:
		if int64(v) > 1_000_000_000_000 {
			return "<P>"
		}
		return v
	default:
		return value
	}
}

// jsonDeepEqual compares two canonicalized values by their JSON form.
func jsonDeepEqual(a, b any) bool {
	aj, _ := marshalValue(a)
	bj, _ := marshalValue(b)
	return string(aj) == string(bj)
}
