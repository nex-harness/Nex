// storagegate_test.go covers the storage startup gate: the rules that decide when
// the first turn may run, which frames are legal continuations, and which failure
// is terminal.
package proto

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/protocol"
)

func storageFrame(phase StoragePhase, seq int64, attempt, database string) string {
	payload := StorageState{
		SchemaVersion: 1,
		AttemptID:     attempt,
		DatabaseID:    database,
		DatabaseKind:  "session",
		Sequence:      seq,
		Phase:         phase,
	}
	raw, _ := json.Marshal(payload)
	return `{"method":"startup/storageState","params":` + string(raw) + `}`
}

// TestGateBlocksTheFirstTurnUntilReady is the gate's whole purpose: with gating
// required, a call made while the agent is still migrating must not reach the
// agent at all.
func TestGateBlocksTheFirstTurnUntilReady(t *testing.T) {
	transport := newFakeTransport()
	transport.on("work", func(req protocol.Message) []protocol.Message {
		return []protocol.Message{{Kind: protocol.KindResponse, ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}}
	})
	client := New(transport, Options{RequireStorageStartup: true})
	defer func() { client.Close(); client.Wait() }()

	done := make(chan error, 1)
	go func() {
		_, err := client.Call(context.Background(), "work", nil, CallOptions{Timeout: 5 * time.Second})
		done <- err
	}()
	// The request must not be on the wire while the gate holds it.
	time.Sleep(50 * time.Millisecond)
	if got := transport.SentMethods(); len(got) != 0 {
		t.Fatalf("a gated request was written before storage was ready: %v", got)
	}
	select {
	case err := <-done:
		t.Fatalf("the gated call returned early: %v", err)
	default:
	}

	transport.Push(storageFrame(StorageChecking, 1, "attempt-1", "db-1"))
	transport.Push(storageFrame(StorageReady, 2, "attempt-1", "db-1"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the released call failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the call was not released by the ready frame")
	}
}

// TestGateNotRequiredNeverBlocks: an agent that cannot report storage state (an old
// CLI) must still serve requests, so a non-required gate is inert.
func TestGateNotRequiredNeverBlocks(t *testing.T) {
	client, _ := newTestClient(t, Options{}, func(f *fakeTransport) {
		f.reply("work", map[string]any{"ok": true})
	})
	if client.Gate().IsWaiting() {
		t.Error("a non-required gate reports waiting")
	}
	if _, err := client.Call(context.Background(), "work", nil, CallOptions{Timeout: time.Second}); err != nil {
		t.Fatalf("call: %v", err)
	}
}

// TestGateRejectsARestartedAttempt is the monotonicity rule: a new attempt (a
// respawned database) must not overwrite the accepted state of this process.
func TestGateRejectsARestartedAttempt(t *testing.T) {
	gate := NewStorageStartupGate(false, 0)
	if !gate.Accept(rawState(t, StorageMigrating, 1, "attempt-1", "db-1")) {
		t.Fatal("the first frame was rejected")
	}
	if gate.Accept(rawState(t, StorageMigrating, 1, "attempt-2", "db-2")) {
		t.Error("a frame from another attempt was accepted")
	}
	snapshot, _ := gate.Snapshot()
	if snapshot.AttemptID != "attempt-1" || snapshot.DatabaseID != "db-1" {
		t.Errorf("snapshot = %+v, want attempt-1/db-1", snapshot)
	}
}

// TestGateRejectsAReplayedSequence: a frame that does not advance the sequence is a
// replay, not progress.
func TestGateRejectsAReplayedSequence(t *testing.T) {
	gate := NewStorageStartupGate(false, 0)
	gate.Accept(rawState(t, StorageMigrating, 5, "a", "d"))
	if gate.Accept(rawState(t, StorageMigrating, 5, "a", "d")) {
		t.Error("a replayed sequence was accepted")
	}
	if gate.Accept(rawState(t, StorageMigrating, 4, "a", "d")) {
		t.Error("an older sequence was accepted")
	}
	if !gate.Accept(rawState(t, StorageMigrating, 6, "a", "d")) {
		t.Error("a newer sequence was rejected")
	}
}

// TestGateReadyIsTerminal: once ready, nothing moves the state again.
func TestGateReadyIsTerminal(t *testing.T) {
	gate := NewStorageStartupGate(false, 0)
	gate.Accept(rawState(t, StorageReady, 1, "a", "d"))
	if gate.Accept(rawState(t, StorageMigrating, 2, "a", "d")) {
		t.Error("a later frame moved a ready gate")
	}
	snapshot, _ := gate.Snapshot()
	if snapshot.Phase != StorageReady {
		t.Errorf("phase = %s, want ready", snapshot.Phase)
	}
	if err := gate.Wait(context.Background()); err != nil {
		t.Errorf("Wait on a ready gate: %v", err)
	}
}

// TestGateFailureIsTerminalAndFailsWaiters: a failed startup must fail the waiting
// turn and every later one with the classified code.
func TestGateFailureIsTerminalAndFailsWaiters(t *testing.T) {
	gate := NewStorageStartupGate(true, time.Minute)
	gate.Accept(rawState(t, StorageMigrating, 1, "a", "d"))
	if !gate.Accept(rawState(t, StorageFailed, 2, "a", "d", "corrupt")) {
		t.Fatal("the failed frame was rejected")
	}
	err := gate.Wait(context.Background())
	var startupErr *StorageStartupError
	if !errors.As(err, &startupErr) || startupErr.Code != "corrupt" {
		t.Fatalf("Wait = %v, want a corrupt startup error", err)
	}
	if gate.Accept(rawState(t, StorageReady, 3, "a", "d")) {
		t.Error("a ready frame revived a failed gate")
	}
}

// TestGateDisposalFailsWithTransportClosed: a transport that ends while the gate is
// waiting must not leave the caller parked forever.
func TestGateDisposalFailsWithTransportClosed(t *testing.T) {
	gate := NewStorageStartupGate(true, time.Minute)
	gate.Dispose()
	err := gate.Wait(context.Background())
	var startupErr *StorageStartupError
	if !errors.As(err, &startupErr) || startupErr.Code != "transport_closed" {
		t.Fatalf("Wait = %v, want transport_closed", err)
	}
	// The first cause is the root cause: a second dispose must not rewrite it.
	gate.Accept(rawState(t, StorageFailed, 1, "a", "d", "io_error"))
	if got := gate.Terminal(); got != err {
		t.Errorf("terminal = %v after a later failure frame, want %v", got, err)
	}
}

// TestGateSynthesisesASnapshotForAnEarlyFailure: failing before the first frame is
// still a fact, and its database identity must stay unresolved rather than borrow
// some other database's id.
func TestGateSynthesisesASnapshotForAnEarlyFailure(t *testing.T) {
	gate := NewStorageStartupGate(true, time.Minute)
	gate.Dispose()
	snapshot, ok := gate.Snapshot()
	if !ok {
		t.Fatal("no snapshot was synthesised")
	}
	if snapshot.Phase != StorageFailed || snapshot.ErrorCode != "transport_closed" {
		t.Errorf("snapshot = %+v", snapshot)
	}
	if len(snapshot.DatabaseID) < len("unresolved:") || snapshot.DatabaseID[:len("unresolved:")] != "unresolved:" {
		t.Errorf("databaseId = %q, want an unresolved identity", snapshot.DatabaseID)
	}
}

// TestGateRejectsAnIllegalFrame: a frame that is not a legal startup state must not
// move the gate at all.
func TestGateRejectsAnIllegalFrame(t *testing.T) {
	gate := NewStorageStartupGate(false, 0)
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"schemaVersion":2,"attemptId":"a","databaseId":"d","databaseKind":"session","sequence":1,"phase":"ready","elapsedMs":0}`),
		json.RawMessage(`{"schemaVersion":1,"attemptId":"","databaseId":"d","databaseKind":"session","sequence":1,"phase":"ready","elapsedMs":0}`),
		json.RawMessage(`{"schemaVersion":1,"attemptId":"a","databaseId":"d","databaseKind":"other","sequence":1,"phase":"ready","elapsedMs":0}`),
		json.RawMessage(`{"schemaVersion":1,"attemptId":"a","databaseId":"d","databaseKind":"session","sequence":0,"phase":"ready","elapsedMs":0}`),
		json.RawMessage(`{"schemaVersion":1,"attemptId":"a","databaseId":"d","databaseKind":"session","sequence":1,"phase":"unknown","elapsedMs":0}`),
		// A failed phase without a code is illegal: the code is the only thing a
		// reader can act on.
		json.RawMessage(`{"schemaVersion":1,"attemptId":"a","databaseId":"d","databaseKind":"session","sequence":1,"phase":"failed","elapsedMs":0}`),
	} {
		if gate.Accept(raw) {
			t.Errorf("an illegal frame was accepted: %s", raw)
		}
	}
	if _, ok := gate.Snapshot(); ok {
		t.Error("an illegal frame moved the gate state")
	}
}

// TestGateFirstStatusTimeoutFailsTheTurn: an agent that never reports storage must
// not hold the first turn forever.
func TestGateFirstStatusTimeoutFailsTheTurn(t *testing.T) {
	gate := NewStorageStartupGate(true, 30*time.Millisecond)
	err := gate.Wait(context.Background())
	var startupErr *StorageStartupError
	if !errors.As(err, &startupErr) || startupErr.Code != "startup_status_timeout" {
		t.Fatalf("Wait = %v, want startup_status_timeout", err)
	}
}

// TestGateObserverSeesAcceptedStatesOnly: the phase stream a host renders must not
// carry a frame the gate refused.
func TestGateObserverSeesAcceptedStatesOnly(t *testing.T) {
	gate := NewStorageStartupGate(false, 0)
	states, unsubscribe := gate.OnChange()
	defer unsubscribe()
	gate.Accept(rawState(t, StorageChecking, 1, "a", "d"))
	gate.Accept(rawState(t, StorageChecking, 1, "a", "d")) // rejected
	select {
	case got := <-states:
		if got.Phase != StorageChecking || got.Sequence != 1 {
			t.Errorf("state = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no state was published")
	}
	select {
	case extra := <-states:
		t.Errorf("a rejected frame was published: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

func rawState(t *testing.T, phase StoragePhase, seq int64, attempt, database string, code ...string) json.RawMessage {
	t.Helper()
	state := StorageState{
		SchemaVersion: 1, AttemptID: attempt, DatabaseID: database,
		DatabaseKind: "session", Sequence: seq, Phase: phase,
	}
	if len(code) > 0 {
		state.ErrorCode = code[0]
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
