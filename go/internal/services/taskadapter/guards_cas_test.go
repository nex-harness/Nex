package taskadapter

import (
	"strings"
	"testing"
)

// guards_cas_test.go — rows C1–C8 of phase-3-guards.md: the v4 CAS submission, which
// is where a stale baseRevision is turned into "retry against the server's truth"
// instead of "execute anyway".

func revision(v int64) *int64 { return &v }

// C1: a CAS command without a baseRevision throws at construction. A write with no
// guard must fail loudly rather than go through.
func TestCAS_MissingBaseRevisionRefused(t *testing.T) {
	for _, commandType := range []string{"switchModelConfig", "switchCollaborationMode"} {
		err := ValidateEnvelope(CommandRequest{Type: commandType, SessionID: "s1"})
		if err == nil {
			t.Fatalf("%s without baseRevision must be refused", commandType)
		}
		if !strings.Contains(err.Error(), "baseRevision") {
			t.Fatalf("the refusal must name baseRevision, got %v", err)
		}
		// With a baseRevision it is well-formed.
		if err := ValidateEnvelope(CommandRequest{Type: commandType, SessionID: "s1", BaseRevision: revision(0)}); err != nil {
			t.Fatalf("%s with baseRevision must be accepted: %v", commandType, err)
		}
	}
	// A non-CAS command needs no base.
	if err := ValidateEnvelope(CommandRequest{Type: "stop", SessionID: "s1"}); err != nil {
		t.Fatalf("a non-CAS command must not require a baseRevision: %v", err)
	}
}

// C2: a row-targeting command without a baseLogEpoch throws. The set is empty in this
// port, so the row is proven by the rule itself rather than by a live command type.
func TestCAS_MissingBaseLogEpochRefused(t *testing.T) {
	// The rule is registered as an empty table because this step ports no row-targeting
	// command; prove the CHECK is the same one the TS runs by exercising the guard with a
	// synthetic entry and then removing it, so the table stays faithful to the TS set.
	rowTargetingCommands["__rowTargetProbe__"] = true
	defer delete(rowTargetingCommands, "__rowTargetProbe__")

	err := ValidateEnvelope(CommandRequest{Type: "__rowTargetProbe__", SessionID: "s1", BaseRevision: revision(1)})
	if err == nil {
		t.Fatal("a row-target command without baseLogEpoch must be refused")
	}
	if !strings.Contains(err.Error(), "baseLogEpoch") {
		t.Fatalf("the refusal must name baseLogEpoch, got %v", err)
	}
	if err := ValidateEnvelope(CommandRequest{
		Type: "__rowTargetProbe__", SessionID: "s1", BaseRevision: revision(1), BaseLogEpoch: "epoch-1",
	}); err != nil {
		t.Fatalf("a row-target command with baseLogEpoch must be accepted: %v", err)
	}
}

// C3: accepted / duplicate / noop are idempotent success. `duplicate` matters most: a
// retried commandId replays the first verdict, and calling that a failure would turn
// every mobile retry into a user-visible error even though the write landed.
func TestCAS_DuplicateIsIdempotentSuccess(t *testing.T) {
	for _, status := range []CommandAckStatus{AckAccepted, AckDuplicate, AckNoop} {
		ack, err := AssertAckOK("switchModelConfig", CommandAck{Status: status}, "model switch")
		if err != nil {
			t.Fatalf("%s must be idempotent success, got %v", status, err)
		}
		if ack.Status != status {
			t.Fatalf("the ack must pass through, got %s", ack.Status)
		}
	}
}

// C4: rejected / stale / failed raise the structured refusal, carrying reasonCode.
func TestCAS_RejectedCarriesReasonCode(t *testing.T) {
	for _, status := range []CommandAckStatus{AckRejected, AckStale, AckFailed} {
		_, err := AssertAckOK("switchModelConfig", CommandAck{
			Status: status, ReasonCode: "WORKSPACE_MISMATCH", Message: "nope",
		}, "model switch")
		if err == nil {
			t.Fatalf("%s must raise", status)
		}
		var rejected *CommandRejectedError
		if !asRejected(err, &rejected) {
			t.Fatalf("%s must raise a CommandRejectedError, got %T", status, err)
		}
		if rejected.Ack.ReasonCode != "WORKSPACE_MISMATCH" {
			t.Fatalf("reasonCode must be carried through, got %q", rejected.Ack.ReasonCode)
		}
		if rejected.Code() != "NEX_V4_COMMAND_REJECTED" {
			t.Fatalf("the code must be the stable discriminator, got %q", rejected.Code())
		}
	}
}

// C5: a stale ACK is not a failure — it is information. The host re-submits against the
// server's revisionAtDecision.
func TestCAS_StaleConvergesOnRevisionAtDecision(t *testing.T) {
	var revisions []int64
	attempts := 0
	ack, err := SubmitCAS(func(request CommandRequest) (CommandAck, error) {
		attempts++
		revisions = append(revisions, *request.BaseRevision)
		switch attempts {
		case 1:
			// The host has no projection, so the probe is stale.
			return CommandAck{Status: AckStale, RevisionAtDecision: 7}, nil
		case 2:
			// The stale base is stale too: something else moved the config.
			return CommandAck{Status: AckStale, RevisionAtDecision: 9}, nil
		default:
			return CommandAck{Status: AckAccepted, RevisionAtDecision: 9}, nil
		}
	}, CASOptions{Type: "switchModelConfig", SessionID: "s1", ContextMessage: "model switch"})

	if err != nil {
		t.Fatalf("convergence must succeed, got %v", err)
	}
	if ack.Status != AckAccepted {
		t.Fatalf("the final ack must be accepted, got %s", ack.Status)
	}
	want := []int64{0, 7, 9}
	if len(revisions) != len(want) {
		t.Fatalf("want %d attempts, got %d", len(want), len(revisions))
	}
	for index, expected := range want {
		if revisions[index] != expected {
			t.Fatalf("attempt %d used baseRevision %d, want %d", index, revisions[index], expected)
		}
	}
}

// C6: each attempt uses a NEW commandId. A stale verdict does not enter the idempotency
// table, so reusing the id would replay the stale verdict forever.
func TestCAS_StaleRetryUsesNewCommandID(t *testing.T) {
	var commandIDs []string
	attempts := 0
	_, err := SubmitCAS(func(request CommandRequest) (CommandAck, error) {
		attempts++
		commandIDs = append(commandIDs, request.CommandID)
		if attempts < 3 {
			return CommandAck{Status: AckStale, RevisionAtDecision: int64(attempts)}, nil
		}
		return CommandAck{Status: AckAccepted}, nil
	}, CASOptions{
		Type: "switchModelConfig", SessionID: "s1", ContextMessage: "model switch",
		NewCommandID: func(attempt int) string { return "cmd-" + string(rune('a'+attempt)) },
	})
	if err != nil {
		t.Fatalf("convergence: %v", err)
	}
	if len(commandIDs) != 3 {
		t.Fatalf("want 3 command ids, got %d", len(commandIDs))
	}
	seen := map[string]bool{}
	for _, id := range commandIDs {
		if seen[id] {
			t.Fatalf("C6: commandId %q was reused; a stale retry must be a NEW command", id)
		}
		seen[id] = true
	}
}

// C7: attempts are bounded. Pathological concurrency must fail, not loop.
func TestCAS_StaleExhaustionThrows(t *testing.T) {
	attempts := 0
	_, err := SubmitCAS(func(CommandRequest) (CommandAck, error) {
		attempts++
		return CommandAck{Status: AckStale, RevisionAtDecision: int64(attempts)}, nil
	}, CASOptions{Type: "switchModelConfig", SessionID: "s1", ContextMessage: "model switch"})

	if err == nil {
		t.Fatal("an exhausted convergence must throw")
	}
	if attempts != DefaultCASAttempts {
		t.Fatalf("want %d attempts, got %d", DefaultCASAttempts, attempts)
	}
	var rejected *CommandRejectedError
	if !asRejected(err, &rejected) {
		t.Fatalf("want a CommandRejectedError, got %T", err)
	}
	if rejected.Ack.Status != AckStale {
		t.Fatalf("the refusal must carry the last stale ack, got %s", rejected.Ack.Status)
	}
	if !strings.Contains(rejected.Error(), "连续 stale") {
		t.Fatalf("the refusal must say the convergence gave up, got %v", rejected)
	}
}

// C8: the core stale-run protection. A command carrying a stale baseRevision never
// executes — the agent refuses it, no effect is observable, and only the host's
// convergence (C5) can apply the write, against a fresh revision.
func TestStaleCommand_NeverExecutes(t *testing.T) {
	var model string // the observable effect of a successful write

	// The CLI side: a write only lands when the baseRevision matches the current one.
	cliRevision := int64(5)
	send := func(request CommandRequest) (CommandAck, error) {
		if *request.BaseRevision != cliRevision {
			return CommandAck{
				Status:             AckStale,
				RevisionAtDecision: cliRevision,
				ReasonCode:         "STALE_BASE_REVISION",
			}, nil
		}
		model = "glm-4"
		cliRevision++
		return CommandAck{Status: AckAccepted, RevisionAtDecision: cliRevision}, nil
	}

	// A stale command (base 0 against revision 5) through the RAW path: refused, and
	// the model is untouched.
	ack, err := AssertAckOK("switchModelConfig", mustSend(t, send, int64(0)), "direct submit")
	if err == nil {
		t.Fatal("a stale base must be refused")
	}
	if ack.Status != AckStale {
		t.Fatalf("want a stale ack, got %s", ack.Status)
	}
	if model != "" {
		t.Fatalf("a stale command must not execute, but the model became %q", model)
	}

	// The SAME write through the host's convergence path: it converges and lands, using
	// the server's revision rather than the caller's stale one.
	ack, err = SubmitCAS(func(request CommandRequest) (CommandAck, error) {
		return send(CommandRequest{BaseRevision: request.BaseRevision})
	}, CASOptions{Type: "switchModelConfig", SessionID: "s1", ContextMessage: "model switch"})
	if err != nil {
		t.Fatalf("convergence: %v", err)
	}
	if ack.Status != AckAccepted || model != "glm-4" {
		t.Fatalf("the converged write must land, got ack=%s model=%q", ack.Status, model)
	}
}

// mustSend runs one send against a baseRevision and reports the ack verbatim.
func mustSend(t *testing.T, send func(CommandRequest) (CommandAck, error), base int64) CommandAck {
	t.Helper()
	ack, err := send(CommandRequest{Type: "switchModelConfig", BaseRevision: &base})
	if err != nil {
		t.Fatalf("transport failure: %v", err)
	}
	return ack
}
