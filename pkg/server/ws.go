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
	if !s.verifyRunnerAuthToken(ctx, hello.RunnerID, hello.AuthToken) {
		if !(strings.TrimSpace(hello.AuthToken) == "" && isDirectLocalHostRunnerRequest(r, hello.Hostname)) {
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

	isDev := hello.IsDev || protocol.IsDevVersion(hello.Version)
	protoVer := hello.ProtocolVersion
	if protoVer <= 0 {
		protoVer = 1
	}
	meta := &runnerRuntimeMeta{
		RunnerID:        hello.RunnerID,
		OS:              hello.OS,
		Arch:            hello.Arch,
		Version:         hello.Version,
		BuildSHA:        hello.BuildSHA,
		BinarySHA256:    hello.BinarySHA256,
		ProtocolVersion: protoVer,
		Capabilities:    append([]string(nil), hello.Capabilities...),
		IsDev:           isDev,
		Elevated:        hello.Elevated,
		FullDiskAccess:  hello.FullDiskAccess,
		AppPath:         hello.AppPath,
		UpdatePhase:     hello.UpdateState,
		UpdateError:     hello.UpdateError,
		Online:          true,
	}
	s.evaluateRunnerUpdate(ctx, meta)
	s.setRunnerRuntimeMeta(meta)

	rc := &runnerConn{id: hello.RunnerID, ws: ws}
	s.mu.Lock()
	s.runners[hello.RunnerID] = rc
	s.mu.Unlock()

	s.broadcastRunnerStatus(ctx, hello.RunnerID, "online")

	defer func() {
		s.mu.Lock()
		evicted := rc.evicted
		if s.runners[hello.RunnerID] == rc {
			delete(s.runners, hello.RunnerID)
		}
		s.mu.Unlock()
		if evicted {
			return
		}
		if rec, err := s.store.GetRunner(context.Background(), hello.RunnerID); err == nil {
			rec.Status = "offline"
			rec.LastSeenAt = time.Now().UnixMilli()
			_ = s.store.UpsertRunner(context.Background(), *rec)
		}
		s.broadcastRunnerStatus(context.Background(), hello.RunnerID, "offline")
	}()

	// Send runner.welcome with latest config snapshot and update compatibility verdict
	snap, _ := s.buildConfigSnapshot(ctx, hello.RunnerID)
	autoUpdatePref, updateModePref := s.effectiveRunnerUpdatePolicy(ctx, hello.RunnerID)
	hasMatchedSSH := false
	if updateModePref == "scp" {
		hasMatchedSSH = s.hasMatchingSSHTarget(ctx, hello.RunnerID, hello.Name, hello.Hostname)
	}
	autoUpdateAllowed := autoUpdatePref &&
		!(updateModePref == "scp" && hasMatchedSSH) &&
		!meta.IsDev &&
		(meta.UpdateStatus == "update_available" || meta.UpdateStatus == "update_required") &&
		len(meta.DownloadURLs) > 0
	serverVer := strings.TrimSpace(s.cfg.Version)
	if serverVer == "" {
		serverVer = "dev"
	}
	welcomeEnv, _ := protocol.NewEnvelope(protocol.TypeRunnerWelcome, protocol.RunnerWelcomePayload{
		RunnerID:           hello.RunnerID,
		Snapshot:           snap,
		ServerVersion:      serverVer,
		MinProtocolVersion: s.effectiveMinRunnerProtocol(),
		UpdateStatus:       meta.UpdateStatus,
		UpdateReason:       meta.UpdateReason,
		TargetVersion:      meta.TargetVersion,
		TargetBuildSHA:     meta.TargetBuildSHA,
		TargetSHA256:       meta.TargetSHA256,
		DownloadURLs:       meta.DownloadURLs,
		AutoUpdateAllowed:  autoUpdateAllowed,
		Incompatible:       meta.Incompatible,
	})
	_ = rc.sendJSON(ctx, welcomeEnv)

	if hello.ActiveSessionsSynced {
		helloTime := now
		activeSet := make(map[string]bool, len(hello.ActiveSessionIDs))
		for _, id := range hello.ActiveSessionIDs {
			if id != "" {
				activeSet[id] = true
			}
		}
		go s.reconcileRunnerSessionsOnConnect(hello.RunnerID, rc, helloTime, activeSet)
	}

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

		case protocol.TypeCronManage:
			go s.handleRunnerCronManage(ctx, rc, env)

		case protocol.TypeRunnerPermissionResult:
			var res protocol.RunnerPermissionResultPayload
			if json.Unmarshal(env.Payload, &res) == nil {
				s.mu.Lock()
				if cur := s.runnerMeta[hello.RunnerID]; cur != nil {
					if res.FullDiskAccess != nil {
						v := *res.FullDiskAccess
						cur.FullDiskAccess = &v
					}
					if res.AppPath != "" {
						cur.AppPath = res.AppPath
					}
				}
				s.mu.Unlock()
				s.broadcastRunnerStatus(ctx, hello.RunnerID, "online")
			}

		case protocol.TypeRunnerUpdateStatus:
			var st protocol.RunnerUpdateStatusPayload
			if json.Unmarshal(env.Payload, &st) == nil {
				s.mu.Lock()
				if cur := s.runnerMeta[hello.RunnerID]; cur != nil {
					cur.UpdatePhase = st.Phase
					cur.UpdateProgress = st.ProgressPct
					cur.UpdateMessage = st.Message
					cur.UpdateError = st.Error
					cur.UpdatePhaseUpdatedAt = time.Now()
					if st.Phase == "succeeded" {
						cur.UpdateMessage = ""
						cur.UpdateError = ""
					}
				}
				s.mu.Unlock()
				s.broadcastRunnerStatus(ctx, hello.RunnerID, "online")
				if st.Phase == "succeeded" {
					rid := hello.RunnerID
					go func() {
						timer := time.NewTimer(runnerSucceededBadgeTTL)
						defer timer.Stop()
						<-timer.C
						s.mu.Lock()
						cur := s.runnerMeta[rid]
						changed := false
						if cur != nil && cur.UpdatePhase == "succeeded" && time.Since(cur.UpdatePhaseUpdatedAt) >= runnerSucceededBadgeTTL {
							cur.UpdatePhase = ""
							cur.UpdateProgress = 0
							cur.UpdateMessage = ""
							changed = true
						}
						online := s.runners[rid] != nil
						s.mu.Unlock()
						if changed && online {
							s.broadcastRunnerStatus(context.Background(), rid, "online")
						}
					}()
				}
			}

		case protocol.TypePTYClosed:
			s.broadcastToAllClients(ctx, env)

		case protocol.TypeBrowserState:
			var st struct {
				Tabs []struct {
					TabID string `json:"tab_id"`
				} `json:"tabs"`
			}
			if json.Unmarshal(env.Payload, &st) == nil {
				s.mu.Lock()
				for _, tab := range st.Tabs {
					if tab.TabID != "" {
						s.browserToRunner[tab.TabID] = hello.RunnerID
					}
				}
				s.mu.Unlock()
			}
			s.broadcastToAllClients(ctx, env)

		case protocol.TypeBrowserHandoff:
			s.broadcastToAllClients(ctx, env)
			var hst struct {
				SessionID string `json:"session_id"`
				TabID     string `json:"tab_id"`
				State     string `json:"state"`
				Reason    string `json:"reason,omitempty"`
				Message   string `json:"message,omitempty"`
			}
			if json.Unmarshal(env.Payload, &hst) == nil && hst.State == "awaiting_human" && hst.SessionID != "" {
				sessionTitle := ""
				isChild := false
				if sess, err := s.store.GetSession(ctx, hst.SessionID); err == nil && sess != nil {
					if sess.ParentSessionID != "" {
						isChild = true
					}
					sessionTitle = strings.TrimSpace(sess.Title)
				}
				if !isChild {
					s.emitClientNotification(ctx, protocol.ClientNotificationPayload{
						SessionID:    hst.SessionID,
						RunnerID:     hello.RunnerID,
						Kind:         "browser_handoff",
						SessionTitle: sessionTitle,
						Title:        strings.TrimSpace(hst.Reason),
						Body:         strings.TrimSpace(hst.Message),
						RequestID:    hst.TabID,
					})
				}
			}
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

	// Persistence rule: transient streaming deltas (message_update / tool_execution_update)
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
		meta := *evPayload.SessionMeta
		if existing, err := s.store.GetSession(ctx, ev.SessionID); err == nil && existing != nil {
			// Preserve Server's authoritative title (e.g. user-renamed via PATCH /api/v1/sessions/{id})
			// so Runner's stale local SessionMeta on agent_start/outbox replay never overwrites it.
			existingTitle := strings.TrimSpace(existing.Title)
			if existingTitle != "" && existingTitle != "Runner Session" && existingTitle != "New Conversation" {
				meta.Title = existing.Title
			}
		}
		_, _ = s.store.CreateOrUpdateSession(ctx, meta)
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
	s.maybeEmitSessionNotification(ctx, rc.id, savedEv)
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
		id:              "cli_" + uuid.NewString(),
		ws:              ws,
		subscribed:      map[string]bool{"*": true},
		ptyAttached:     make(map[string]bool),
		browserAttached: make(map[string]bool),
	}

	s.mu.Lock()
	s.clients[cc.id] = cc
	s.mu.Unlock()
	defer func() {
		cc.subMu.RLock()
		watchedTabs := make([]string, 0, len(cc.browserAttached))
		for tid := range cc.browserAttached {
			watchedTabs = append(watchedTabs, tid)
		}
		cc.subMu.RUnlock()

		s.mu.Lock()
		delete(s.clients, cc.id)
		var remaining []*clientConn
		for _, c := range s.clients {
			remaining = append(remaining, c)
		}
		s.mu.Unlock()

		// Stop runner screencasts when no remaining viewer watches those tabs.
		for _, tid := range watchedTabs {
			stillWatched := false
			for _, other := range remaining {
				other.subMu.RLock()
				if other.browserAttached[tid] {
					stillWatched = true
				}
				other.subMu.RUnlock()
				if stillWatched {
					break
				}
			}
			if !stillWatched {
				s.mu.RLock()
				rid := s.browserToRunner[tid]
				rc := s.runners[rid]
				s.mu.RUnlock()
				if rc != nil {
					stopEnv, _ := protocol.NewEnvelope(protocol.TypeBrowserControl, map[string]any{
						"action": "stream_stop",
						"tab_id": tid,
					})
					_ = rc.sendJSON(context.Background(), stopEnv)
				}
			}
		}
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

		case protocol.TypeSessionAbort:
			s.handleClientAbort(ctx, cc, env)

		case protocol.TypeSessionSteer, protocol.TypeSessionFollowUp, protocol.TypeSessionBgTaskStop,
			protocol.TypeApprovalResolve, protocol.TypeUIResolve, protocol.TypeBrowserInput, protocol.TypeBrowserHandoff:
			s.forwardControlToRunner(ctx, cc, env)

		case protocol.TypeWorkspaceReq, protocol.TypePTYOpen, protocol.TypePTYResize, protocol.TypePTYClose, protocol.TypePTYList,
			protocol.TypeBrowserControl, protocol.TypeBrowserProbe, protocol.TypeBrowserClearData:
			go s.proxyClientRPC(ctx, cc, env)

		case protocol.TypeBrowserInstall:
			go s.proxyClientRPCWithTimeout(ctx, cc, env, 5*time.Minute)
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

	effortToSave := strings.TrimSpace(string(p.ThinkingEffort))
	if effortToSave == "" {
		effortToSave = "off"
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
			ThinkingEffort:   effortToSave,
			PermissionMode:   p.PermissionMode,
			Status:           "running",
		})
	} else {
		p.RunnerID = existing.RunnerID
		if p.WorkspacePath == "" {
			p.WorkspacePath = existing.WorkspacePath
		}
		if p.ProviderID != "" {
			existing.ProviderID = p.ProviderID
		}
		if p.ModelID != "" {
			existing.ModelID = p.ModelID
		}
		if p.PermissionMode != "" {
			existing.PermissionMode = p.PermissionMode
		}
		existing.ThinkingEffort = effortToSave
		existing.Status = "running"
		existing.UpdatedAt = time.Now().UnixMilli()
		existing.LastActiveAt = existing.UpdatedAt
		_, _ = s.store.CreateOrUpdateSession(ctx, *existing)
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
	if err := s.checkRunnerCompatible(ctx, p.RunnerID); err != nil {
		errEnv, _ := protocol.NewEnvelope(protocol.TypeError, map[string]string{
			"error": err.Error(),
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

func (s *Server) finalizeSessionInterrupted(ctx context.Context, sessionID, runnerID, reason string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return false
	}
	sess, err := s.store.GetSession(ctx, sessionID)
	if err != nil || sess == nil || sess.Status != "running" {
		return false
	}
	if runnerID == "" {
		runnerID = sess.RunnerID
	}
	sess.Status = "idle"
	if _, err := s.store.CreateOrUpdateSession(ctx, *sess); err != nil {
		return false
	}

	// Finalize running child sessions before broadcasting parent's agent_end so
	// clients reacting to parent completion observe consistent idle state across the tree.
	if children, cErr := s.store.ListSessions(ctx, runnerID, true); cErr == nil {
		for _, child := range children {
			if child.ParentSessionID == sessionID && child.Status == "running" {
				s.finalizeSessionInterrupted(ctx, child.ID, runnerID, reason)
			}
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"type":   "agent_end",
		"reason": reason,
	})
	savedEv, err := s.store.AppendSessionEvent(ctx, store.SessionEvent{
		SessionID: sessionID,
		TurnIdx:   0,
		EventType: "agent_end",
		Payload:   payload,
	}, false)
	if err == nil {
		outEnv, _ := protocol.NewEnvelope(protocol.TypeSessionEvent, savedEv)
		outEnv.SessionID = savedEv.SessionID
		outEnv.RunnerID = runnerID
		outEnv.Seq = savedEv.Seq
		s.broadcastSessionEvent(ctx, savedEv.SessionID, outEnv)
	}
	return true
}

func (s *Server) reconcileRunnerSessionsOnConnect(runnerID string, rc *runnerConn, helloTime int64, activeSet map[string]bool) {
	delay := s.effectiveReconcileDelay()
	if delay > 0 {
		time.Sleep(delay)
	}
	s.mu.RLock()
	cur := s.runners[runnerID]
	s.mu.RUnlock()
	if cur != rc {
		return
	}
	bgCtx := context.Background()
	sessions, err := s.store.ListSessions(bgCtx, runnerID, true)
	if err != nil {
		return
	}
	for _, sess := range sessions {
		if sess.Status != "running" {
			continue
		}
		if activeSet[sess.ID] {
			continue
		}
		if sess.LastActiveAt > helloTime {
			continue
		}
		s.finalizeSessionInterrupted(bgCtx, sess.ID, runnerID, "runner_restarted")
	}
}

func (s *Server) abortSession(ctx context.Context, sessionID, hintRunnerID string, rawEnv *protocol.Envelope) bool {
	runnerID := hintRunnerID
	if sess, err := s.store.GetSession(ctx, sessionID); err == nil && sess != nil && sess.RunnerID != "" {
		runnerID = sess.RunnerID
	}

	s.mu.RLock()
	rc := s.runners[runnerID]
	s.mu.RUnlock()
	if rc == nil {
		return s.finalizeSessionInterrupted(ctx, sessionID, runnerID, "aborted_offline")
	}

	var lastSeqBefore uint64
	if evs, err := s.store.ListSessionEventsAfter(ctx, sessionID, 0); err == nil && len(evs) > 0 {
		lastSeqBefore = evs[len(evs)-1].Seq
	}

	var fwd protocol.Envelope
	if rawEnv != nil {
		fwd = *rawEnv
	} else {
		fwd, _ = protocol.NewEnvelope(protocol.TypeSessionAbort, map[string]any{})
	}
	fwd.SessionID = sessionID
	fwd.RunnerID = runnerID
	_ = rc.sendJSON(ctx, fwd)

	timeout := s.effectiveAbortFallbackTimeout()
	go func() {
		time.Sleep(timeout)
		bgCtx := context.Background()
		sessAfter, err := s.store.GetSession(bgCtx, sessionID)
		if err != nil || sessAfter == nil || sessAfter.Status != "running" {
			return
		}
		evsAfter, _ := s.store.ListSessionEventsAfter(bgCtx, sessionID, lastSeqBefore)
		if len(evsAfter) == 0 {
			s.finalizeSessionInterrupted(bgCtx, sessionID, runnerID, "aborted_timeout")
		}
	}()
	return false
}

func (s *Server) handleClientAbort(ctx context.Context, cc *clientConn, env protocol.Envelope) {
	if strings.TrimSpace(env.SessionID) == "" {
		return
	}
	_ = s.abortSession(ctx, env.SessionID, env.RunnerID, &env)
}

func (s *Server) forwardControlToRunner(ctx context.Context, cc *clientConn, env protocol.Envelope) {
	runnerID := env.RunnerID
	if env.SessionID != "" {
		if sess, err := s.store.GetSession(ctx, env.SessionID); err == nil && sess != nil && sess.RunnerID != "" {
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
	s.proxyClientRPCWithTimeout(ctx, cc, env, 15*time.Second)
}

func (s *Server) proxyClientRPCWithTimeout(ctx context.Context, cc *clientConn, env protocol.Envelope, timeout time.Duration) {
	runnerID := env.RunnerID
	if runnerID == "" && env.SessionID != "" {
		if sess, err := s.store.GetSession(ctx, env.SessionID); err == nil {
			runnerID = sess.RunnerID
		}
	}
	if env.Type == protocol.TypeBrowserControl {
		var bc struct {
			Action string `json:"action"`
			TabID  string `json:"tab_id"`
		}
		if json.Unmarshal(env.Payload, &bc) == nil && bc.TabID != "" {
			if bc.Action == "stream_start" {
				cc.subMu.Lock()
				cc.browserAttached[bc.TabID] = true
				cc.subMu.Unlock()
				if runnerID != "" {
					s.mu.Lock()
					s.browserToRunner[bc.TabID] = runnerID
					s.mu.Unlock()
				}
			} else if bc.Action == "stream_stop" || bc.Action == "close_tab" {
				cc.subMu.Lock()
				delete(cc.browserAttached, bc.TabID)
				cc.subMu.Unlock()
			}
		}
	}
	resp, err := s.callRunnerRPC(ctx, runnerID, env, timeout)
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
	if resp.Type == protocol.TypeBrowserState {
		var st struct {
			ActiveTabID string `json:"active_tab_id"`
			Tabs        []struct {
				TabID string `json:"tab_id"`
			} `json:"tabs"`
		}
		if json.Unmarshal(resp.Payload, &st) == nil {
			if runnerID != "" {
				s.mu.Lock()
				for _, tab := range st.Tabs {
					if tab.TabID != "" {
						s.browserToRunner[tab.TabID] = runnerID
					}
				}
				s.mu.Unlock()
			}
			var bcReq struct {
				Action string `json:"action"`
			}
			if env.Type == protocol.TypeBrowserControl && json.Unmarshal(env.Payload, &bcReq) == nil && bcReq.Action == "stream_start" && st.ActiveTabID != "" {
				cc.subMu.Lock()
				cc.browserAttached[st.ActiveTabID] = true
				cc.subMu.Unlock()
			}
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
	op, streamID, _, err := protocol.DecodePTYBinaryFrame(frame)
	if err != nil {
		return
	}
	switch op {
	case protocol.PTYBinaryStdout:
		s.mu.Lock()
		s.ptyToRunner[streamID] = runnerID
		clients := make([]*clientConn, 0, len(s.clients))
		for _, c := range s.clients {
			clients = append(clients, c)
		}
		s.mu.Unlock()

		for _, c := range clients {
			c.subMu.RLock()
			attached := c.ptyAttached[streamID]
			c.subMu.RUnlock()
			if attached {
				_ = c.sendBinary(ctx, frame)
			}
		}

	case protocol.BrowserBinaryScreencast:
		s.mu.Lock()
		s.browserToRunner[streamID] = runnerID
		clients := make([]*clientConn, 0, len(s.clients))
		for _, c := range s.clients {
			clients = append(clients, c)
		}
		s.mu.Unlock()

		for _, c := range clients {
			c.subMu.RLock()
			attached := c.browserAttached[streamID]
			c.subMu.RUnlock()
			if attached {
				_ = c.sendBinary(ctx, frame)
			}
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

func (s *Server) emitClientNotification(ctx context.Context, payload protocol.ClientNotificationPayload) {
	if strings.TrimSpace(payload.SessionID) == "" || strings.TrimSpace(payload.Kind) == "" {
		return
	}
	env, err := protocol.NewEnvelope(protocol.TypeClientNotify, payload)
	if err != nil {
		return
	}
	env.SessionID = payload.SessionID
	env.RunnerID = payload.RunnerID
	s.broadcastToAllClients(ctx, env)
}

func (s *Server) maybeEmitSessionNotification(ctx context.Context, runnerID string, ev store.SessionEvent) {
	switch ev.EventType {
	case "agent_start":
		s.mu.Lock()
		delete(s.planNotifiedSess, ev.SessionID)
		s.mu.Unlock()
		return
	case "agent_end", "ui_prompt_requested", "approval_requested", "tool_execution_end":
	default:
		return
	}

	sessionTitle := ""
	if sess, err := s.store.GetSession(ctx, ev.SessionID); err == nil && sess != nil {
		if sess.ParentSessionID != "" {
			return
		}
		sessionTitle = strings.TrimSpace(sess.Title)
	}

	var inner struct {
		Error      string `json:"error,omitempty"`
		ToolCallID string `json:"tool_call_id,omitempty"`
		ToolName   string `json:"tool_name,omitempty"`
		ToolResult *struct {
			IsError bool `json:"is_error,omitempty"`
		} `json:"tool_result,omitempty"`
		ApprovalID string                           `json:"approval_id,omitempty"`
		UIPrompt   *protocol.UIPromptRequestPayload `json:"ui_prompt,omitempty"`
	}
	_ = json.Unmarshal(ev.Payload, &inner)

	switch ev.EventType {
	case "tool_execution_end":
		if inner.ToolName == "exit_plan_mode" && (inner.ToolResult == nil || !inner.ToolResult.IsError) {
			s.mu.Lock()
			s.planNotifiedSess[ev.SessionID] = true
			s.mu.Unlock()
			s.emitClientNotification(ctx, protocol.ClientNotificationPayload{
				SessionID:    ev.SessionID,
				RunnerID:     runnerID,
				Kind:         "plan_approval",
				SessionTitle: sessionTitle,
				ToolName:     "exit_plan_mode",
				RequestID:    inner.ToolCallID,
			})
		}
	case "agent_end":
		s.mu.Lock()
		planNotified := s.planNotifiedSess[ev.SessionID]
		delete(s.planNotifiedSess, ev.SessionID)
		s.mu.Unlock()
		if planNotified {
			return
		}
		if strings.Contains(strings.ToLower(inner.Error), "cancel") {
			return
		}
		s.emitClientNotification(ctx, protocol.ClientNotificationPayload{
			SessionID:    ev.SessionID,
			RunnerID:     runnerID,
			Kind:         "task_completed",
			SessionTitle: sessionTitle,
			Body:         strings.TrimSpace(inner.Error),
		})
	case "ui_prompt_requested":
		reqID := ""
		title := ""
		body := ""
		if inner.UIPrompt != nil {
			reqID = strings.TrimSpace(inner.UIPrompt.RequestID)
			title = strings.TrimSpace(inner.UIPrompt.Title)
			body = strings.TrimSpace(inner.UIPrompt.Message)
			if body == "" && len(inner.UIPrompt.Options) > 0 {
				limit := len(inner.UIPrompt.Options)
				if limit > 3 {
					limit = 3
				}
				body = strings.Join(inner.UIPrompt.Options[:limit], " / ")
			}
		}
		s.emitClientNotification(ctx, protocol.ClientNotificationPayload{
			SessionID:    ev.SessionID,
			RunnerID:     runnerID,
			Kind:         "question",
			SessionTitle: sessionTitle,
			Title:        title,
			Body:         body,
			RequestID:    reqID,
		})
	case "approval_requested":
		s.emitClientNotification(ctx, protocol.ClientNotificationPayload{
			SessionID:    ev.SessionID,
			RunnerID:     runnerID,
			Kind:         "approval",
			SessionTitle: sessionTitle,
			ToolName:     inner.ToolName,
			RequestID:    inner.ApprovalID,
		})
	}
}
