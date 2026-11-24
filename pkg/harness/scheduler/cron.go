package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kardianos/service"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
	"github.com/robfig/cron/v3"
)

// JobExecutor runs a scheduled prompt autonomously on the local Runner (even when offline from nex-server!).
type JobExecutor interface {
	ExecuteCronPrompt(ctx context.Context, job store.CronJobRecord) error
}

// Engine wraps robfig/cron/v3 to execute locally cached CronJobRecords autonomously.
type Engine struct {
	st   *store.Store
	exec JobExecutor

	mu     sync.Mutex
	cron   *cron.Cron
	closed bool
}

// NewEngine constructs an autonomous Runner Cron scheduler.
func NewEngine(st *store.Store, exec JobExecutor) *Engine {
	return &Engine{
		st:   st,
		exec: exec,
	}
}

// Reload stops the previous cron schedule and rebuilds entries from locally persisted CronJobRecords.
func (e *Engine) Reload(ctx context.Context, runnerID string) error {
	jobs, err := e.st.ListCronJobs(ctx, runnerID)
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	if e.cron != nil {
		e.cron.Stop()
	}

	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	c := cron.New(cron.WithParser(parser))

	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		if !job.Recurring && job.MaxRuns > 0 && job.RunCount >= job.MaxRuns {
			continue
		}
		if !job.Recurring && job.MaxRuns == 0 && job.RunCount >= 1 {
			continue
		}
		jCopy := job
		spec := jCopy.CronExpr
		if jCopy.Timezone != "" && jCopy.Timezone != "Local" {
			spec = fmt.Sprintf("CRON_TZ=%s %s", jCopy.Timezone, jCopy.CronExpr)
		}
		_, err := c.AddFunc(spec, func() {
			e.fireJob(jCopy.ID)
		})
		if err != nil {
			// Skip malformed schedule without crashing daemon
			continue
		}
	}

	c.Start()
	e.cron = c
	return nil
}

func (e *Engine) fireJob(jobID string) {
	ctx := context.Background()
	jobs, err := e.st.ListCronJobs(ctx, "")
	if err != nil {
		return
	}
	var target *store.CronJobRecord
	for i := range jobs {
		if jobs[i].ID == jobID {
			target = &jobs[i]
			break
		}
	}
	if target == nil || !target.Enabled {
		return
	}

	target.LastRunAt = time.Now().UnixMilli()
	target.RunCount++
	if !target.Recurring {
		limit := target.MaxRuns
		if limit <= 0 {
			limit = 1
		}
		if target.RunCount >= limit {
			target.Enabled = false
		}
	}
	_, _ = e.st.UpsertCronJob(ctx, *target)

	if e.exec != nil {
		_ = e.exec.ExecuteCronPrompt(ctx, *target)
	}
}

// Stop halts the Cron scheduler cleanly.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.cron != nil {
		e.cron.Stop()
		e.cron = nil
	}
}

// BuildPromptForCron converts a CronJobRecord into an autonomous PromptPayload.
func BuildPromptForCron(job store.CronJobRecord) protocol.PromptPayload {
	perm := job.PermissionMode
	if perm == "" {
		perm = "autonomous"
	}
	return protocol.PromptPayload{
		SessionID:         fmt.Sprintf("sess_cron_%s_%d", job.ID, time.Now().UnixNano()),
		RunnerID:          job.RunnerID,
		WorkspacePath:     job.WorkspacePath,
		PermissionMode:    perm,
		CronCredentialIDs: job.CredentialIDs,
		Text:              job.Prompt,
	}
}

// --- Cross-Platform OS Service Wrapper (`kardianos/service`: launchd / systemd / Windows Service) ---

type daemonProgram struct {
	runFn  func(ctx context.Context)
	cancel context.CancelFunc
}

func (p *daemonProgram) Start(_ service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.runFn(ctx)
	return nil
}

func (p *daemonProgram) Stop(_ service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

// NewOSService creates a cross-platform launchd / systemd / Windows Service handle for nex-runner.
func NewOSService(args []string, runFn func(ctx context.Context)) (service.Service, error) {
	svcCfg := &service.Config{
		Name:        "nex-runner",
		DisplayName: "Nex-v3 Autonomous Agent Runner",
		Description: "Headless cross-platform autonomous agent runner for Nex-v3",
		Arguments:   args,
	}
	return service.New(&daemonProgram{runFn: runFn}, svcCfg)
}
