package execution

import (
	"testing"

	"github.com/Isites/anyai/internal/config"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveIngressAgentUsesRequestedThenDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "entry", Name: "Entry", Model: "test/model"},
	}
	deps := runtimeport.ExecutionDeps{Config: cfg}

	assert.Equal(t, "explicit", ResolveAgent(deps, runtimeport.IngressRequest{
		Channel:     "http",
		RequestedID: "explicit",
		SenderID:    "http",
		AccountID:   "http",
		ChatType:    runtimeport.ChatTypeDirect,
	}).AgentID)

	assert.Equal(t, "entry", ResolveAgent(deps, runtimeport.IngressRequest{
		Channel:   "http",
		SenderID:  "http",
		AccountID: "http",
		ChatType:  runtimeport.ChatTypeDirect,
	}).AgentID)

	deps.IngressResolver = func(req runtimeport.IngressRequest) string {
		if req.Channel == "http" {
			return "bound"
		}
		return ""
	}
	decision := ResolveAgent(deps, runtimeport.IngressRequest{
		Channel:   "http",
		SenderID:  "http",
		AccountID: "http",
		ChatType:  runtimeport.ChatTypeDirect,
	})
	assert.Equal(t, "bound", decision.AgentID)
	assert.Equal(t, "resolver", decision.Source)
}

func TestRecordInputLifecycleEmitsInputAndAttachmentEvents(t *testing.T) {
	recorder := runtimeevents.NewRecorder()
	req := runtimeport.IngressRequest{
		RunID:       "run_1",
		SessionID:   "sess_1",
		Channel:     "http",
		SenderID:    "user_1",
		AccountID:   "acct_1",
		ChatType:    runtimeport.ChatTypeDirect,
		RequestedID: "entry",
		Envelope: input.InputEnvelope{
			Blocks: []input.InputBlock{
				{Type: "text", Text: "hello"},
				{ID: "att_file", Type: "file", Name: "brief.txt", Path: "/tmp/brief.txt", MimeType: "text/plain"},
				{Type: "image", Name: "diagram.png", Path: "/tmp/diagram.png", MimeType: "image/png"},
			},
		},
	}

	recordInputLifecycle(recorder, req, "entry")

	events := recorder.ListRunEvents("run_1")
	require.Len(t, events, 4)
	assert.Equal(t, "input.received", events[0].Name)
	assert.Equal(t, "attachment.stored", events[1].Name)
	assert.Equal(t, "attachment.stored", events[2].Name)
	assert.Equal(t, "input.normalized", events[3].Name)
	assert.Equal(t, 3, events[0].Payload["block_count"])
	assert.Equal(t, 2, events[0].Payload["attachment_count"])
	assert.Equal(t, "att_file", events[1].Payload["attachment_id"])
	assert.Equal(t, "input_3", events[2].Payload["attachment_id"])
}

func TestRecordRouteDecisionEmitsAcceptedThenRouted(t *testing.T) {
	recorder := runtimeevents.NewRecorder()
	req := runtimeport.IngressRequest{
		RunID:       "run_route",
		SessionID:   "sess_route",
		Channel:     "http",
		SenderID:    "user_1",
		AccountID:   "acct_1",
		ChatType:    runtimeport.ChatTypeDirect,
		RequestedID: "entry",
		Envelope: input.InputEnvelope{
			Blocks: []input.InputBlock{{Type: "text", Text: "hello"}},
		},
	}

	recordRouteDecision(recorder, req, Decision{AgentID: "entry", Source: "requested"})

	events := recorder.ListRunEvents("run_route")
	require.Len(t, events, 2)
	assert.Equal(t, runtimeevents.EventRunAccepted, events[0].Name)
	assert.Equal(t, runtimeevents.EventRunRouted, events[1].Name)
	run, ok := recorder.GetRun("run_route")
	require.True(t, ok)
	assert.Equal(t, runtimeevents.RunStatusQueued, run.Status)
}
