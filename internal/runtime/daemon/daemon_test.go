package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type daemonCall struct {
	channel   string
	agentID   string
	sessionID string
	prompt    string
}

func TestStartLaunchesHeartbeatAndCron(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "HEARTBEAT.md"), []byte("- Check queue"), 0o644))

	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "assistant",
			Entry:     true,
			Workspace: workspace,
			Cron: []config.CronConfig{
				{Name: "digest", Schedule: "20ms", Prompt: "build digest"},
			},
		},
	}
	cfg.Heartbeat.Enabled = true
	cfg.Heartbeat.Interval = "20ms"

	calls := make(chan daemonCall, 8)
	rt := stubRuntime{
		calls: calls,
	}

	bundle, err := Start(Options{
		Ctx:     context.Background(),
		Config:  cfg,
		Runtime: rt,
	})
	require.NoError(t, err)
	defer bundle.Stop()

	deadline := time.After(2 * time.Second)
	seenHeartbeat := false
	seenCron := false
	for !(seenHeartbeat && seenCron) {
		select {
		case item := <-calls:
			assert.Contains(t, []string{"heartbeat", "cron"}, item.channel)
			assert.Equal(t, "assistant", item.agentID)
			if item.sessionID == "heartbeat" {
				seenHeartbeat = true
			}
			if item.sessionID == "cron_digest" && item.prompt == "build digest" {
				seenCron = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for heartbeat and cron executions (heartbeat=%v cron=%v)", seenHeartbeat, seenCron)
		}
	}
}

func TestJobSchedulerAddJobUsesDefaultAgent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "entry", Entry: true},
	}

	calls := make(chan struct {
		channel   string
		agentID   string
		sessionID string
		prompt    string
	}, 4)
	rt := stubRuntime{calls: calls}

	bundle, err := Start(Options{
		Ctx:     context.Background(),
		Config:  cfg,
		Runtime: rt,
	})
	require.NoError(t, err)
	defer bundle.Stop()

	require.NotNil(t, bundle.JobScheduler())
	require.NoError(t, bundle.JobScheduler().AddJob("dynamic", "20ms", "hello"))

	select {
	case item := <-calls:
		assert.Equal(t, "cron", item.channel)
		assert.Equal(t, "entry", item.agentID)
		assert.Equal(t, "cron_dynamic", item.sessionID)
		assert.Equal(t, "hello", item.prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dynamic cron job execution")
	}
}

type stubRuntime struct {
	calls any
}

func (s stubRuntime) StartTextRun(
	_ context.Context,
	channelName, agentID, _, _, sessionID, text string,
	_ runtimeport.ChatType,
) (*runtimeport.ManagedRun, error) {
	switch calls := s.calls.(type) {
	case chan struct {
		channel   string
		agentID   string
		sessionID string
		prompt    string
	}:
		calls <- struct {
			channel   string
			agentID   string
			sessionID string
			prompt    string
		}{channel: channelName, agentID: agentID, sessionID: sessionID, prompt: text}
	case chan daemonCall:
		calls <- daemonCall{channel: channelName, agentID: agentID, sessionID: sessionID, prompt: text}
	}

	events := make(chan runtimeevents.EventRecord, 2)
	go func() {
		defer close(events)
		events <- runtimeevents.EventRecord{Name: "text.delta", Payload: map[string]any{"text": "OK"}}
		events <- runtimeevents.EventRecord{Name: "run.completed"}
	}()

	return &runtimeport.ManagedRun{
		RunID:     "run_stub",
		AgentID:   agentID,
		SessionID: sessionID,
		Events:    events,
		Cancel:    func() {},
	}, nil
}
