package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

func (s *Server) handleRunnerCronManage(ctx context.Context, rc *runnerConn, env protocol.Envelope) {
	var req protocol.CronManagePayload
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		reply, _ := protocol.NewEnvelope(protocol.TypeError, map[string]any{"error": "invalid cron.manage payload"})
		reply.ReplyTo = env.ID
		_ = rc.sendJSON(ctx, reply)
		return
	}

	replyErr := func(msg string) {
		reply, _ := protocol.NewEnvelope(protocol.TypeError, map[string]any{"error": msg})
		reply.ReplyTo = env.ID
		_ = rc.sendJSON(ctx, reply)
	}
	replyOK := func(payload any) {
		reply, _ := protocol.NewEnvelope(protocol.TypeCronManage, payload)
		reply.ReplyTo = env.ID
		_ = rc.sendJSON(ctx, reply)
	}

	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "list":
		jobs, err := s.store.ListCronJobs(ctx, "")
		if err != nil {
			replyErr(err.Error())
			return
		}
		replyOK(map[string]any{"jobs": jobs})

	case "create", "update":
		j := req.Job
		if strings.TrimSpace(j.ID) == "" {
			if strings.TrimSpace(req.JobID) != "" {
				j.ID = strings.TrimSpace(req.JobID)
			} else {
				j.ID = "cron_" + uuid.NewString()[:8]
			}
		}
		if strings.TrimSpace(j.RunnerID) == "" {
			j.RunnerID = rc.id
		}
		if strings.TrimSpace(j.CronExpr) == "" || strings.TrimSpace(j.Prompt) == "" {
			replyErr("cron_expr and prompt are required")
			return
		}
		if strings.TrimSpace(j.Title) == "" {
			j.Title = j.Prompt
			if len(j.Title) > 48 {
				j.Title = j.Title[:48]
			}
		}
		if strings.TrimSpace(j.PermissionMode) == "" {
			j.PermissionMode = "autonomous"
		}
		if req.Action == "create" {
			j.Enabled = true
			j.Recurring = true
		}
		saved, err := s.store.UpsertCronJob(ctx, j)
		if err != nil {
			replyErr(err.Error())
			return
		}
		s.BroadcastConfigSync(ctx)
		replyOK(map[string]any{"ok": true, "job": saved})

	case "delete":
		id := strings.TrimSpace(req.JobID)
		if id == "" {
			id = strings.TrimSpace(req.Job.ID)
		}
		if id == "" {
			replyErr("job_id is required")
			return
		}
		if err := s.store.DeleteCronJob(ctx, id); err != nil {
			replyErr(err.Error())
			return
		}
		s.BroadcastConfigSync(ctx)
		replyOK(map[string]any{"ok": true, "deleted": id})

	case "run_now":
		id := strings.TrimSpace(req.JobID)
		if id == "" {
			id = strings.TrimSpace(req.Job.ID)
		}
		jobs, err := s.store.ListCronJobs(ctx, "")
		if err != nil {
			replyErr(err.Error())
			return
		}
		var found *store.CronJobRecord
		for i := range jobs {
			if jobs[i].ID == id {
				found = &jobs[i]
				break
			}
		}
		if found == nil {
			replyErr(fmt.Sprintf("cron job %q not found", id))
			return
		}
		targetRunner := strings.TrimSpace(found.RunnerID)
		if targetRunner == "" {
			targetRunner = rc.id
		}
		if !s.isRunnerOnline(targetRunner) {
			replyErr(fmt.Sprintf("runner %q is offline", targetRunner))
			return
		}
		runEnv, _ := protocol.NewEnvelope(protocol.TypeCronRunNow, protocol.CronRunNowPayload{Job: *found})
		runEnv.RunnerID = targetRunner
		if _, err := s.callRunnerRPC(ctx, targetRunner, runEnv, 15*time.Second); err != nil {
			replyErr(err.Error())
			return
		}
		replyOK(map[string]any{"ok": true, "triggered": id, "runner_id": targetRunner})

	default:
		replyErr("unsupported action: " + req.Action)
	}
}
