package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/store"
)

type chanCronExec struct {
	fired chan store.CronJobRecord
}

func (c *chanCronExec) ExecuteCronPrompt(_ context.Context, job store.CronJobRecord) error {
	c.fired <- job
	return nil
}

func TestAutonomousCronFireOneShotLimitAndOSService(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Schedule a 1-second one-shot finite cron job (recurring=false, max_runs=1)
	job, err := st.UpsertCronJob(ctx, store.CronJobRecord{
		RunnerID:  "run_local",
		Title:     "每秒测试一次",
		CronExpr:  "@every 1s",
		Prompt:    "check disk health",
		Recurring: false,
		MaxRuns:   1,
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	execCh := &chanCronExec{fired: make(chan store.CronJobRecord, 4)}
	eng := NewEngine(st, execCh)
	defer eng.Stop()

	if err := eng.Reload(ctx, "run_local"); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	select {
	case firedJob := <-execCh.fired:
		if firedJob.ID != job.ID {
			t.Fatalf("unexpected fired job ID: %s", firedJob.ID)
		}
		p := BuildPromptForCron(firedJob)
		if p.PermissionMode != "autonomous" || p.Text != "check disk health" {
			t.Fatalf("unexpected cron prompt payload: %+v", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for autonomous cron job to fire")
	}

	// Verify finite job disabled itself after reaching max_runs=1
	jobs, _ := st.ListCronJobs(ctx, "run_local")
	if len(jobs) != 1 || jobs[0].Enabled || jobs[0].RunCount != 1 {
		t.Fatalf("expected finite cron job to auto-complete after 1 run: %+v", jobs)
	}

	// Verify cross-platform kardianos/service handle initializes cleanly
	svc, err := NewOSService([]string{"-server", "ws://localhost:8080/ws/runner"}, func(_ context.Context) {})
	if err != nil || svc == nil {
		t.Fatalf("NewOSService failed: %v", err)
	}
}
