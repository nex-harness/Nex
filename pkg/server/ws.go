package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func (s *Server) handleRunnerWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(16 * 1024 * 1024)
	defer ws.Close(websocket.StatusNormalClosure, "bye")

	ctx := r.Context()

	// Expect runner.hello as first frame
	_, rawHello, err := ws.Read(ctx)
	if err != nil {
		return
	}
	var helloEnv protocol.Envelope
	if err := json.Unmarshal(rawHello, &helloEnv); err != nil || helloEnv.Type != protocol.TypeRunnerHello {
		return
	}
	var hello protocol.RunnerHelloPayload
	if err := json.Unmarshal(helloEnv.Payload, &hello); err != nil || hello.RunnerID == "" {
		return
	}
	if s.cfg.PairingSecret != "" && hello.AuthToken != s.cfg.PairingSecret {
		existing, rErr := s.store.GetRunner(ctx, hello.RunnerID)
		if rErr != nil || existing.AuthToken != hello.AuthToken {
			_ = ws.Close(websocket.StatusPolicyViolation, "unauthorized runner token")
			return
		}
	}

	now := time.Now().UnixMilli()
	_ = s.store.UpsertRunner(ctx, store.RunnerRecord{
		ID:               hello.RunnerID,
		Name:             hello.Name,
		Hostname:         hello.Hostname,
		OS:               hello.OS,
		Arch:             hello.Arch,
		Shell:            hello.Shell,
		Version:          hello.Version,
		Tags:             hello.Tags,
		Status:           "online",
		LastSeenAt:       now,
		SyncedConfigRev:  hello.SyncedConfigRev,
		AuthToken:        hello.AuthToken,
		WorkspaceDefault: hello.WorkspaceDefault,
	})

	rc := &runnerConn{id: hello.RunnerID, ws: ws}
	s.mu.Lock()
	s.runners[hello.RunnerID] = rc
	s.mu.Unlock()

	s.broadcastRunnerStatus(ctx, hello.RunnerID, "online")

	defer func() {
		s.mu.Lock()
		if s.runners[hello.RunnerID] == rc {
			delete(s.runners, hello.RunnerID)
		}
		s.mu.Unlock()
		if rec, err := s.store.GetRunner(context.Background(), hello.RunnerID); err == nil {
			rec.Status = "offline"
			rec.LastSeenAt = time.Now().UnixMilli()
			_ = s.store.UpsertRunner(context.Background(), *rec)
		}
		s.broadcastRunnerStatus(context.Background(), hello.RunnerID, "offline")
	}()

	// Send runner.welcome with latest config snapshot
	snap, _ := s.buildConfigSnapshot(ctx, hello.RunnerID)
	welcomeEnv, _ := protocol.NewEnvelope(protocol.TypeRunnerWelcome, protocol.RunnerWelcomePayload{
		RunnerID: hello.RunnerID,
		Snapshot: snap,
	})
	_ = rc.sendJSON(ctx, welcomeEnv)

	for {
		msgType, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if msgType == websocket.MessageBinary {
			s.forwardBinaryFromRunner(ctx, hello.RunnerID, data)
			continue
		}

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		env.RunnerID = hello.RunnerID

		// Resolve pending Server -> Runner RPCs (e.g. blob.fetch, workspace.req, pty.open)
		if env.ReplyTo != "" {
			s.mu.RLock()
			waitCh := s.pendingRPC[env.ReplyTo]
			s.mu.RUnlock()
			if waitCh != nil {
				select {
				case waitCh <- env:
				default:
				}
			}
			// If it was a pty.opened reply, record pty_id -> runner_id mapping
			if env.Type == protocol.TypePTYOpened {
				var p struct {
					PTYID string `json:"pty_id"`
				}
				if json.Unmarshal(env.Payload, &p) == nil && p.PTYID != "" {
					s.mu.Lock()
					s.ptyToRunner[p.PTYID] = hello.RunnerID
					s.mu.Unlock()
				}
			}
		}

		switch env.Type {
		case protocol.TypeSessionEvent:
			s.ingestRunnerSessionEvent(ctx, rc, env)

		case protocol.TypeMCPRemoteCall:
			go s.handleRunnerRemoteMCPCall(ctx, rc, env)

		case protocol.TypeMemorySync:
			go s.handleRunnerMemorySync(ctx, env)

		case protocol.TypePTYClosed:
			s.broadcastToAllClients(ctx, env)
		}
	}
}

func (s *Server) ingestRunnerSessionEvent(ctx context.Context, rc *runnerConn, env protocol.Envelope) {
	var ev store.SessionEvent
	if err := json.Unmarshal(env.Payload, &ev); err != nil {
		return
	}
	if ev.SessionID == "" {
		ev.SessionID = env.SessionID
	}
	if ev.Seq == 0 {
		ev.Seq = env.Seq
	}
	if ev.SessionID == "" {
		return
	}

	// ZCode Persistence Rule: transient streaming deltas (message_update / tool_execution_update)
	// are broadcast live in memory ONLY and never persisted as 1-char rows in SQLite!
	if ev.EventType == "message_update" || ev.EventType == "tool_execution_update" {
		if ev.CreatedAt == 0 {
			ev.CreatedAt = time.Now().UnixMilli()
		}
		outEnv, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, ev)
		outEnv.SessionID = ev.SessionID
		outEnv.RunnerID = rc.id
		outEnv.Seq = ev.Seq
		s.broadcastSessionEvent(ctx, ev.SessionID, outEnv)
		return
	}

	if ev.EventType == "session_title_updated" {
		var titlePayload struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(ev.Payload, &titlePayload) == nil && strings.TrimSpace(titlePayload.Title) != "" {
			if sess, gErr := s.store.GetSession(ctx, ev.SessionID); gErr == nil && sess != nil {
				sess.Title = strings.TrimSpace(titlePayload.Title)
				_, _ = s.store.CreateOrUpdateSession(ctx, *sess)
			}
			outEnv, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, ev)
			outEnv.SessionID = ev.SessionID
			outEnv.RunnerID = rc.id
			s.broadcastToAllClients(ctx, outEnv)
		}
		return
	}

	// Ensure session row exists on Server (including offline Cron or Subagent sessions created on Runner!)
	var evPayload struct {
		SessionMeta *store.SessionRecord `json:"session_meta,omitempty"`
		Telemetry   *store.TurnTelemetry `json:"telemetry,omitempty"`
	}
	_ = json.Unmarshal(ev.Payload, &evPayload)

	if evPayload.SessionMeta != nil {
		_, _ = s.store.CreateOrUpdateSession(ctx, *evPayload.SessionMeta)
	} else if _, err := s.store.GetSession(ctx, ev.SessionID); err != nil {
		_, _ = s.store.CreateOrUpdateSession(ctx, store.SessionRecord{
			ID:       ev.SessionID,
			RunnerID: rc.id,
			Title:    "Runner Session",
		})
	}

	savedEv, err := s.store.AppendSessionEvent(ctx, ev, false)
	if err != nil {
		return
	}

	if savedEv.EventType == "agent_end" {
		if sess, gErr := s.store.GetSession(ctx, savedEv.SessionID); gErr == nil && sess != nil {
			sess.Status = "idle"
			_, _ = s.store.CreateOrUpdateSession(ctx, *sess)
		}
	}

	if evPayload.Telemetry != nil {
		tel := *evPayload.Telemetry
		tel.SessionID = ev.SessionID
		tel.RunnerID = rc.id
		_ = s.store.RecordTurnTelemetry(ctx, tel)
	}

	// Acknowledge back to Runner so it marks Outbox entry as synced
	ackEnv, _ := protocol.NewEnvelope(protocol.TypeOutboxAck, protocol.OutboxAckPayload{
		SessionID: savedEv.SessionID,
		AckSeq:    savedEv.Seq,
	})
	_ = rc.sendJSON(ctx, ackEnv)

	// Broadcast to subscribed Web/Desktop/Mobile clients
	outEnv, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, savedEv)
	outEnv.SessionID = savedEv.SessionID
	outEnv.RunnerID = rc.id
	outEnv.Seq = savedEv.Seq
	s.broadcastSessionEvent(ctx, savedEv.SessionID, outEnv)
}

func (s *Server) handleRunnerRemoteMCPCall(ctx context.Context, rc *runnerConn, env protocol.Envelope) {
	var req protocol.MCPRemoteCallPayload
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		return
	}
	var respPayload protocol.MCPRemoteResultPayload
	if s.cfg.RemoteMCP == nil {
		respPayload.Error = "no server-hosted MCP executor configured"
	} else {
		res, err := s.cfg.RemoteMCP.CallTool(ctx, req.Namespace, req.ToolName, req.Arguments)
		if err != nil {
			respPayload.Error = err.Error()
		} else {
			respPayload.Result = res
		}
	}
	replyEnv, _ := protocol.NewEnvelope(protocol.TypeMCPRemoteResult, respPayload)
	replyEnv.ReplyTo = env.ID
	_ = rc.sendJSON(ctx, replyEnv)
}

func (s *Server) handleRunnerMemorySync(ctx context.Context, env protocol.Envelope) {
	var p protocol.MemorySyncPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	switch p.Action {
	case "upsert":
		if p.Record == nil || strings.TrimSpace(p.Record.Title) == "" || strings.TrimSpace(p.Record.Content) == "" {
			return
		}
		if _, err := s.store.UpsertMemory(ctx, *p.Record); err != nil {
			return
		}
	case "delete":
		if strings.TrimSpace(p.MemoryID) == "" {
			return
		}
		if err := s.store.DeleteMemory(ctx, p.MemoryID); err != nil {
			return
		}
	default:
		return
	}
	s.BroadcastConfigSync(ctx)
	notifyEnv, _ := protocol.NewEnvelope(protocol.TypeMemorySync, protocol.MemorySyncPayload{Action: "refresh"})
	s.broadcastToAllClients(ctx, notifyEnv)
}

func (s *Server) handleClientWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(8 * 1024 * 1024)
	defer ws.Close(websocket.StatusNormalClosure, "bye")

	ctx := r.Context()
	cc := &clientConn{
		id:          "cli_" + uuid.NewString(),
		ws:          ws,
		subscribed:  map[string]bool{"*": true},
		ptyAttached: make(map[string]bool),
	}

	s.mu.Lock()
	s.clients[cc.id] = cc
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, cc.id)
		s.mu.Unlock()
	}()

	for {
		msgType, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if msgType == websocket.MessageBinary {
			s.forwardBinaryFromClient(ctx, cc, data)
			continue
		}

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}

		switch env.Type {
		case protocol.TypeClientSubscribe:
			var sub protocol.ClientSubscribePayload
			if json.Unmarshal(env.Payload, &sub) == nil && sub.SessionID != "" {
				cc.subMu.Lock()
				cc.subscribed[sub.SessionID] = true
				cc.subMu.Unlock()

				// Replay missed events > last_seen_seq immediately!
				if missed, err := s.store.ListSessionEventsAfter(ctx, sub.SessionID, sub.LastSeenSeq); err == nil {
					for _, mev := range missed {
						replayEnv, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, mev)
						replayEnv.SessionID = mev.SessionID
						replayEnv.Seq = mev.Seq
						_ = cc.sendJSON(ctx, replayEnv)
					}
				}
			}

		case protocol.TypeSessionPrompt:
			s.routeClientPrompt(ctx, cc, env)

		case protocol.TypeSessionSteer, protocol.TypeSessionFollowUp, protocol.TypeSessionAbort,
			protocol.TypeApprovalResolve, protocol.TypeUIResolve:
			s.forwardControlToRunner(ctx, cc, env)

		case protocol.TypeWorkspaceReq, protocol.TypePTYOpen, protocol.TypePTYResize, protocol.TypePTYClose, protocol.TypePTYList:
			go s.proxyClientRPC(ctx, cc, env)
		}
	}
}

func (s *Server) routeClientPrompt(ctx context.Context, cc *clientConn, env protocol.Envelope) {
	var p protocol.PromptPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	if p.RunnerID == "" {
		p.RunnerID = env.RunnerID
	}
	if p.SessionID == "" {
		p.SessionID = "sess_" + uuid.NewString()
	}
	title := p.Text
	if runes := []rune(title); len(runes) > 48 {
		title = string(runes[:48]) + "..."
	}
	if title == "" {
		title = "New Conversation"
	}

	if existing, err := s.store.GetSession(ctx, p.SessionID); err != nil {
		_, _ = s.store.CreateOrUpdateSession(ctx, store.SessionRecord{
			ID:               p.SessionID,
			RunnerID:         p.RunnerID,
			Title:            title,
			WorkspacePath:    p.WorkspacePath,
			AgentProfileSlug: p.AgentProfileSlug,
			ProviderID:       p.ProviderID,
			ModelID:          p.ModelID,
			PermissionMode:   p.PermissionMode,
			Status:           "running",
		})
	} else {
		p.RunnerID = existing.RunnerID
		if p.WorkspacePath == "" {
			p.WorkspacePath = existing.WorkspacePath
		}
	}

	cc.subMu.Lock()
	cc.subscribed[p.SessionID] = true
	cc.subMu.Unlock()

	s.mu.RLock()
	rc := s.runners[p.RunnerID]
	s.mu.RUnlock()
	if rc == nil {
		errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{
			"error": "target runner is offline: " + p.RunnerID,
		})
		errEnv.ReplyTo = env.ID
		errEnv.SessionID = p.SessionID
		_ = cc.sendJSON(ctx, errEnv)
		return
	}

	fwdEnv, _ := protocol.NewEnvelope(protocol.TypeSessionPrompt, p)
	fwdEnv.ID = env.ID
	fwdEnv.SessionID = p.SessionID
	fwdEnv.RunnerID = p.RunnerID
	_ = rc.sendJSON(ctx, fwdEnv)
}

func (s *Server) forwardControlToRunner(ctx context.Context, cc *clientConn, env protocol.Envelope) {
	runnerID := env.RunnerID
	if runnerID == "" && env.SessionID != "" {
		if sess, err := s.store.GetSession(ctx, env.SessionID); err == nil {
			runnerID = sess.RunnerID
		}
	}
	s.mu.RLock()
	rc := s.runners[runnerID]
	s.mu.RUnlock()
	if rc == nil {
		errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": "runner offline"})
		errEnv.ReplyTo = env.ID
		_ = cc.sendJSON(ctx, errEnv)
		return
	}
	_ = rc.sendJSON(ctx, env)
}

func (s *Server) proxyClientRPC(ctx context.Context, cc *clientConn, env protocol.Envelope) {
	runnerID := env.RunnerID
	if runnerID == "" && env.SessionID != "" {
		if sess, err := s.store.GetSession(ctx, env.SessionID); err == nil {
			runnerID = sess.RunnerID
		}
	}
	resp, err := s.callRunnerRPC(ctx, runnerID, env, 15*time.Second)
	if err != nil {
		errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{"error": err.Error()})
		errEnv.ReplyTo = env.ID
		_ = cc.sendJSON(ctx, errEnv)
		return
	}
	if resp.Type == protocol.TypePTYOpened {
		var p struct {
			PTYID   string `json:"pty_id"`
			History []byte `json:"history,omitempty"`
		}
		if json.Unmarshal(resp.Payload, &p) == nil && p.PTYID != "" {
			cc.subMu.Lock()
			cc.ptyAttached[p.PTYID] = true
			cc.subMu.Unlock()
		}
	}
	resp.ReplyTo = env.ID
	_ = cc.sendJSON(ctx, resp)
}

func (s *Server) forwardBinaryFromClient(ctx context.Context, cc *clientConn, frame []byte) {
	op, ptyID, _, err := protocol.DecodePTYBinaryFrame(frame)
	if err != nil || op != protocol.PTYBinaryStdin {
		return
	}
	cc.subMu.Lock()
	cc.ptyAttached[ptyID] = true
	cc.subMu.Unlock()

	s.mu.RLock()
	runnerID := s.ptyToRunner[ptyID]
	rc := s.runners[runnerID]
	s.mu.RUnlock()
	if rc != nil {
		_ = rc.sendBinary(ctx, frame)
	}
}

func (s *Server) forwardBinaryFromRunner(ctx context.Context, runnerID string, frame []byte) {
	op, ptyID, _, err := protocol.DecodePTYBinaryFrame(frame)
	if err != nil || op != protocol.PTYBinaryStdout {
		return
	}
	s.mu.Lock()
	s.ptyToRunner[ptyID] = runnerID
	clients := make([]*clientConn, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	for _, c := range clients {
		c.subMu.RLock()
		attached := c.ptyAttached[ptyID]
		c.subMu.RUnlock()
		if attached {
			_ = c.sendBinary(ctx, frame)
		}
	}
}

func (s *Server) broadcastSessionEvent(ctx context.Context, sessionID string, env protocol.Envelope) {
	// Check if this is a subagent child session; only push full deltas to clients explicitly subscribed to sessionID!
	isChild := false
	if sess, err := s.store.GetSession(ctx, sessionID); err == nil && sess.ParentSessionID != "" {
		isChild = true
	}

	s.mu.RLock()
	clients := make([]*clientConn, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	for _, c := range clients {
		c.subMu.RLock()
		directSub := c.subscribed[sessionID]
		wildcard := c.subscribed["*"]
		c.subMu.RUnlock()

		if directSub || (!isChild && wildcard) {
			_ = c.sendJSON(ctx, env)
		}
	}
}

func (s *Server) broadcastRunnerStatus(ctx context.Context, runnerID, status string) {
	env, _ := protocol.NewEnvelope(protocol.TypeRunnerStatus, map[string]string{
		"runner_id": runnerID,
		"status":    status,
	})
	env.RunnerID = runnerID
	s.broadcastToAllClients(ctx, env)
}

func (s *Server) broadcastToAllClients(ctx context.Context, env protocol.Envelope) {
	s.mu.RLock()
	clients := make([]*clientConn, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	for _, c := range clients {
		_ = c.sendJSON(ctx, env)
	}
}
