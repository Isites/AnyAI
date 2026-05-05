package daemon

import (
	"context"
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"strings"
	"time"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/daemon/cron"
	"github.com/Isites/anyai/internal/runtime/daemon/heartbeat"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/tool"
)

// Runtime submits daemon-triggered work back into the runtime kernel instead of
// bypassing it with direct agent execution calls.
type Runtime interface {
	StartTextRun(
		ctx context.Context,
		channelName, agentID, senderID, accountID, sessionID, text string,
		chatType runtimeport.ChatType,
	) (*runtimeport.ManagedRun, error)
}

// Options configure daemon startup for cron jobs and heartbeat checks.
type Options struct {
	Ctx     context.Context
	Config  *config.Config
	Runtime Runtime
}

// Bundle groups all maintenance/daemon processes started for one runtime.
type Bundle struct {
	scheduler  *cron.Scheduler
	jobAdapter *CronSchedulerAdapter
	heartbeats []*heartbeat.Daemon
}

// CronSchedulerAdapter adapts cron.Scheduler to the tools.JobScheduler interface.
type CronSchedulerAdapter struct {
	Scheduler    *cron.Scheduler
	Ctx          context.Context
	AgentFactory func(name string) func(context.Context, string) (string, error)
	OutputFn     cron.OutputFunc
}

func (a *CronSchedulerAdapter) AddJob(name, schedule, prompt string) error {
	var agentFn func(context.Context, string) (string, error)
	if a != nil && a.AgentFactory != nil {
		agentFn = a.AgentFactory(name)
	}
	if agentFn == nil {
		agentFn = func(_ context.Context, _ string) (string, error) {
			runtimelogging.Info("dynamic cron job executed without runtime runner", "name", name)
			return "OK", nil
		}
	}

	err := a.Scheduler.Add(cron.Job{
		Name:     name,
		Schedule: schedule,
		Prompt:   prompt,
		AgentFn:  agentFn,
		OutputFn: a.OutputFn,
	})
	if err != nil {
		return err
	}
	a.Scheduler.Start(a.Ctx)
	return nil
}

func (a *CronSchedulerAdapter) RemoveJob(name string) error {
	return a.Scheduler.Remove(name)
}

func (a *CronSchedulerAdapter) ListJobs() []tools.JobInfo {
	jobs := a.Scheduler.Jobs()
	infos := make([]tools.JobInfo, len(jobs))
	for i, j := range jobs {
		infos[i] = tools.JobInfo{
			Name:     j.Name,
			Schedule: j.Schedule,
			Prompt:   j.Prompt,
			Paused:   j.Paused,
		}
	}
	return infos
}

func (a *CronSchedulerAdapter) PauseJob(name string) error {
	return a.Scheduler.Pause(name)
}

func (a *CronSchedulerAdapter) ResumeJob(name string) error {
	return a.Scheduler.Resume(name)
}

func (a *CronSchedulerAdapter) UpdateJobSchedule(name, schedule string) error {
	return a.Scheduler.UpdateSchedule(name, schedule)
}

// Start launches configured heartbeat daemons and cron jobs.
func Start(opts Options) (*Bundle, error) {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	bundle := &Bundle{
		scheduler: cron.NewScheduler(),
	}
	bundle.jobAdapter = &CronSchedulerAdapter{
		Scheduler: bundle.scheduler,
		Ctx:       ctx,
		AgentFactory: func(jobName string) func(context.Context, string) (string, error) {
			return func(runCtx context.Context, prompt string) (string, error) {
				agentID := defaultAgentID(opts.Config)
				return runWithRuntime(opts.Runtime, runCtx, "cron", agentID, "cron_"+strings.TrimSpace(jobName), prompt)
			}
		},
	}

	cfg := opts.Config
	if cfg == nil {
		return bundle, nil
	}

	if cfg.Heartbeat.Enabled {
		interval, err := time.ParseDuration(strings.TrimSpace(cfg.Heartbeat.Interval))
		if err != nil || interval <= 0 {
			interval = 30 * time.Minute
		}
		for _, agentCfg := range cfg.Agents.List {
			agentID := strings.TrimSpace(agentCfg.ID)
			if agentID == "" {
				continue
			}
			daemon := heartbeat.NewDaemon(agentCfg.Workspace, interval, func(runCtx context.Context, prompt string) (string, error) {
				return runWithRuntime(opts.Runtime, runCtx, "heartbeat", agentID, "heartbeat", prompt)
			})
			daemon.Start(ctx)
			bundle.heartbeats = append(bundle.heartbeats, daemon)
		}
	}

	for _, agentCfg := range cfg.Agents.List {
		agentID := strings.TrimSpace(agentCfg.ID)
		if agentID == "" {
			continue
		}
		for _, cronJob := range agentCfg.Cron {
			jobName := strings.TrimSpace(cronJob.Name)
			jobPrompt := cronJob.Prompt
			if jobName == "" || strings.TrimSpace(cronJob.Schedule) == "" {
				continue
			}
			err := bundle.scheduler.Add(cron.Job{
				Name:     jobName,
				Schedule: cronJob.Schedule,
				Prompt:   jobPrompt,
				AgentFn: func(runCtx context.Context, prompt string) (string, error) {
					return runWithRuntime(opts.Runtime, runCtx, "cron", agentID, "cron_"+jobName, prompt)
				},
			})
			if err != nil {
				return nil, err
			}
		}
	}

	if len(bundle.scheduler.Jobs()) > 0 {
		bundle.scheduler.Start(ctx)
	}
	return bundle, nil
}

func (b *Bundle) JobScheduler() tools.JobScheduler {
	if b == nil {
		return nil
	}
	return b.jobAdapter
}

func (b *Bundle) Stop() {
	if b == nil {
		return
	}
	if b.scheduler != nil {
		b.scheduler.Stop()
	}
	for _, hb := range b.heartbeats {
		hb.Stop()
	}
}

func defaultAgentID(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	for _, agentCfg := range cfg.Agents.List {
		if agentCfg.Entry && strings.TrimSpace(agentCfg.ID) != "" {
			return strings.TrimSpace(agentCfg.ID)
		}
	}
	if len(cfg.Agents.List) == 0 {
		return ""
	}
	return strings.TrimSpace(cfg.Agents.List[0].ID)
}

func runWithRuntime(runtime Runtime, ctx context.Context, channelName, agentID, sessionID, prompt string) (string, error) {
	if runtime == nil {
		runtimelogging.Info("daemon task skipped because runtime runner is unavailable", "agent", agentID, "session_id", sessionID)
		return "OK", nil
	}
	run, err := runtime.StartTextRun(
		ctx,
		strings.TrimSpace(channelName),
		strings.TrimSpace(agentID),
		runtimeport.SystemAgentID,
		"",
		strings.TrimSpace(sessionID),
		prompt,
		runtimeport.ChatTypeDirect,
	)
	if err != nil {
		return "", err
	}

	var output strings.Builder
	var runErr error
	for event := range run.Events {
		if text := runtimeevents.TextDelta(event); text != "" {
			output.WriteString(text)
		}
		if message := runtimeevents.FailureMessage(event); message != "" && runErr == nil {
			runErr = fmt.Errorf("%s", message)
		}
	}
	if runErr != nil {
		return strings.TrimSpace(output.String()), runErr
	}
	return strings.TrimSpace(output.String()), nil
}
