package runtime

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/emitters"
	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// v4Command is the `v4/command` request shape the runtime reads.
type v4Command struct {
	CommandID string          `json:"commandId"`
	ClientID  string          `json:"clientId"`
	SessionID *string         `json:"sessionId"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// handleV4Command dispatches a v4/command by type.
func (r *Runtime) handleV4Command(ctx context.Context, req protocol.Message) ([]byte, error) {
	var cmd v4Command
	if err := json.Unmarshal(req.Params, &cmd); err != nil {
		return nil, &protocol.RPCError{Code: protocol.CodeInvalidParams, Message: "invalid command: " + err.Error()}
	}
	switch cmd.Type {
	case "createSession":
		return r.createSession(ctx, cmd)
	}
	return nil, &protocol.RPCError{Code: protocol.CodeMethodNotFound, Message: "unsupported command: " + cmd.Type}
}

// createSessionPayload is the createSession command payload.
type createSessionPayload struct {
	WorkspaceID string `json:"workspaceId"`
	FirstInput  *struct {
		Text           string          `json:"text"`
		ModelSelection json.RawMessage `json:"modelSelection"`
	} `json:"firstInput"`
}

// createSession implements the recorded createSession flow (see runtime.md):
// prefs#1, mcpTelemetry, prefs#2, persist session rows, respond, then schedule
// the turn. runMu is held for the whole flow so the response is written before
// any other session handler (in particular the subscribe) observes state.
func (r *Runtime) createSession(ctx context.Context, cmd v4Command) ([]byte, error) {
	var payload createSessionPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return nil, &protocol.RPCError{Code: protocol.CodeInvalidParams, Message: "invalid createSession payload: " + err.Error()}
	}

	sessionID := r.deps.IDs.SessionID()
	ws := Workspace{Path: payload.WorkspaceID, Key: payload.WorkspaceID}
	s, err := r.newSession(sessionID, r.logEpoch(), ws)
	if err != nil {
		return nil, err
	}
	s.cmd = commandRef{ID: cmd.CommandID, ClientID: cmd.ClientID}
	if payload.FirstInput != nil {
		s.modelSelection = payload.FirstInput.ModelSelection
		s.firstText = payload.FirstInput.Text
	}
	s.seedPublisher()

	// 1. runtime-materialization preferences.
	prefs1, err := r.requestPrefs(ctx, sessionID, emitters.PrefsRuntimeMaterialization)
	if err != nil {
		return nil, err
	}
	s.setMemoryEnabled(prefsBool(prefs1, "memoryEnabled"))

	// 2. the MCP pool's session_startup, between the two requests.
	s.write(emitters.MCPStartupFrame(emitters.MCPStartupInput{
		Arch: r.deps.PlatformArch, Platform: r.deps.Platform,
		OccurredAt: r.deps.Clock.NowMs(), SessionID: sessionID,
	}))

	// 3. user-execution preferences.
	if _, err := r.requestPrefs(ctx, sessionID, emitters.PrefsUserExecution); err != nil {
		return nil, err
	}

	// 4. persist the session and its entries, then emit the synchronous title
	//    event (raw 1) from the first input: the subscribe that follows the
	//    response must see snapshot seq 2 with this title.
	if err := persistSession(s); err != nil {
		return nil, err
	}
	if _, err := r.deps.Store.UpdateTitle(s.id, titleFromInput(s.firstText), "first_input", s.nowMs()); err != nil {
		_ = err
	}
	s.appendEvent(sessionEvent{
		Type: evSessionTitleUpdated,
		Payload: mustJSON(map[string]any{
			"previousTitle": "", "source": "first_input", "title": titleFromInput(s.firstText),
		}),
	})

	// 5. respond: accepted.
	inputID := cmd.CommandID
	result := mustJSON(map[string]any{
		"commandId":          cmd.CommandID,
		"status":             "accepted",
		"revisionAtDecision": 0,
		"result": map[string]any{
			"type":      "createSession",
			"sessionId": sessionID,
			"input":     map[string]any{"delivery": "startNow", "inputId": inputID},
		},
		"memoryEnabled": s.memoryEnabled,
	})

	// 6. schedule the turn; it waits for the first subscription (see session.firstSub).
	go r.turnStarter(ctx, s, inputID)
	return result, nil
}

// requestPrefs sends a session/requestRuntimePreferences request and returns the
// host's result.
func (r *Runtime) requestPrefs(ctx context.Context, sessionID string, scope emitters.PrefsScope) (json.RawMessage, error) {
	msg := emitters.RuntimePreferencesRequest(r.nextPrefsID(), sessionID, scope)
	return r.Request(ctx, msg)
}

// nextServerID mints the next agent->host request id (after the prefs ids).
func (r *Runtime) nextServerID() protocol.RequestID { return r.nextPrefsID() }

// nextPrefsID mints the recorded ids: server-1, server-2, ...
func (r *Runtime) nextPrefsID() protocol.RequestID {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prefsSeq++
	return protocol.StringID("server-" + itoa(r.prefsSeq))
}

func prefsBool(raw json.RawMessage, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	var b bool
	_ = json.Unmarshal(m[key], &b)
	return b
}

// handleSubscribe arbitrates a conversation subscription and emits the ACK plus
// the initial frame. The initial frame is queued for the post-response outbox so
// it is written after the ACK, as the recorded trace shows.
func (r *Runtime) handleSubscribe(_ context.Context, req protocol.Message) ([]byte, error) {
	var params struct {
		Topic        string `json:"topic"`
		ConnectionID string `json:"connectionId"`
		ClientMode   string `json:"clientMode"`
		Base         *struct {
			LogEpoch string `json:"logEpoch"`
			Seq      int64  `json:"seq"`
		} `json:"base"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, &protocol.RPCError{Code: protocol.CodeInvalidParams, Message: "invalid subscribe params: " + err.Error()}
	}
	sessionID := topicSession(params.Topic)
	s, err := r.session(sessionID)
	if err != nil {
		return nil, err
	}

	profile := v4pub.ProfileForClientMode(params.ClientMode)
	sub := v4pub.SubscribeParams{ConnectionID: params.ConnectionID, DeliveryProfile: profile}
	if params.Base != nil {
		sub.Base = &v4pub.Base{LogEpoch: params.Base.LogEpoch, Seq: params.Base.Seq}
	}
	result, err := s.pub.SubscribeReserved(sub)
	if err != nil {
		return nil, err
	}
	if !s.subscribed {
		s.subscribed = true
		close(s.firstSub)
	}
	if result.InitialFrame != nil {
		frame := *result.InitialFrame
		reservation := result.Reservation
		r.postResponse(func() {
			if reservation != nil && !reservation.Commit() {
				return
			}
			r.emitWireFrame(frame, reservation)
		})
	}
	return subscribeAckResult(s, result), nil
}

// subscribeAckResult builds the ACK-only JSON-RPC result.
func subscribeAckResult(s *session, result *v4pub.SubscribeResult) json.RawMessage {
	ack := map[string]any{
		"subscriptionId": result.Ack.SubscriptionID,
		"mode":           string(result.Ack.Mode),
		"logEpoch":       result.Ack.LogEpoch,
		"openTiming": map[string]any{
			"version":              1,
			"initialFrameEncodeMs": 0,
			"sessionRuntimeState":  "warm",
			"snapshotRowCount":     s.windowRowCount(),
		},
	}
	return mustJSON(map[string]any{"ack": ack})
}

func (r *Runtime) handleUnsubscribe(_ context.Context, req protocol.Message) ([]byte, error) {
	return mustJSON(map[string]any{}), nil
}

func (r *Runtime) handleResync(_ context.Context, req protocol.Message) ([]byte, error) {
	return nil, &protocol.RPCError{Code: protocol.CodeMethodNotFound, Message: "v4/conversation/resync not implemented"}
}

// emitWireFrame encodes one logical frame and writes it as a
// v4/conversation/frame notification per physical wire frame.
func (r *Runtime) emitWireFrame(frame v4pub.Frame, reservation *v4pub.Reservation) {
	var wires [][]byte
	var err error
	if reservation != nil {
		wires, err = v4pub.EncodeReservation(reservation)
	} else {
		wires, err = v4pub.EncodeFrameJSON(frame, v4pub.DeliveryOnline, "", 0)
	}
	if err != nil {
		return
	}
	for _, w := range wires {
		r.write(protocol.Message{Kind: protocol.KindNotification, Method: "v4/conversation/frame", Params: w, HasParams: true})
	}
}
