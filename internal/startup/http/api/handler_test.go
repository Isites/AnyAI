package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRunPlane struct {
	runEventsCalled       int
	subscribeReplayCalled int
	subscribeLiveCalled   int
	runTreeCalled         int
	runTreeSummaryCalled  int
}

type fakeRuntimePlane struct {
	rebuildCalled           int
	rebuildFromEventsCalled int
}

func (p *fakeRuntimePlane) RebuildProjections() error {
	p.rebuildCalled++
	return nil
}

func (p *fakeRuntimePlane) RebuildProjectionsFromEvents() error {
	p.rebuildFromEventsCalled++
	return nil
}

func (p *fakeRuntimePlane) EventStorageDir() string { return "" }

func (p *fakeRuntimePlane) AttachmentBaseDir() string { return "" }

func (p *fakeRunPlane) RunList() []gateway.Run { return nil }

func (p *fakeRunPlane) StartAcceptedRun(
	context.Context,
	string,
	string,
	[]gateway.InputBlock,
	string,
	string,
	string,
	string,
	string,
	gateway.ChatType,
	string,
) (*gateway.ManagedRun, gateway.Run, error) {
	return nil, gateway.Run{}, nil
}

func (p *fakeRunPlane) RunRecord(runID string) (gateway.Run, bool) {
	return gateway.Run{
		ID:        runID,
		AgentID:   "assistant",
		SessionID: "session",
		Status:    gateway.RunStatusRunning,
		StartedAt: time.Now().UTC(),
	}, true
}

func (p *fakeRunPlane) RunEvents(string) []gateway.Event {
	p.runEventsCalled++
	return []gateway.Event{{Name: gateway.EventRunStarted}}
}

func (p *fakeRunPlane) SubscribeRunLive(string) (<-chan gateway.Event, func(), error) {
	p.subscribeLiveCalled++
	ch := make(chan gateway.Event)
	close(ch)
	return ch, func() {}, nil
}

func (p *fakeRunPlane) SubscribeRunReplay(string) ([]gateway.Event, <-chan gateway.Event, func(), error) {
	p.subscribeReplayCalled++
	ch := make(chan gateway.Event)
	close(ch)
	return []gateway.Event{{Name: gateway.EventRunStarted}}, ch, func() {}, nil
}

func (p *fakeRunPlane) CancelRun(string) error { return nil }

func (p *fakeRunPlane) RunTreeRecord(string) (gateway.RunTree, bool) {
	return gateway.RunTree{Events: []gateway.Event{{Name: gateway.EventRunStarted}}}, true
}

func (p *fakeRunPlane) RunTree(string) ([]gateway.RunNode, bool) {
	p.runTreeCalled++
	return []gateway.RunNode{{
		Run:    gateway.Run{ID: "run_1", AgentID: "assistant"},
		Events: []gateway.Event{{Name: gateway.EventRunStarted}},
	}}, true
}

func (p *fakeRunPlane) RunTreeSummary(string) ([]gateway.RunNode, bool) {
	p.runTreeSummaryCalled++
	return []gateway.RunNode{{Run: gateway.Run{ID: "run_1", AgentID: "assistant"}}}, true
}

func (p *fakeRunPlane) SubscribeRunTreeLive(string) (<-chan gateway.Event, func(), error) {
	ch := make(chan gateway.Event)
	close(ch)
	return ch, func() {}, nil
}

func (p *fakeRunPlane) SubscribeRunTreeReplay(string) ([]gateway.Event, <-chan gateway.Event, func(), error) {
	ch := make(chan gateway.Event)
	close(ch)
	return []gateway.Event{{Name: gateway.EventRunStarted}}, ch, func() {}, nil
}

func TestRunEventsLiveStreamDoesNotPreloadReplay(t *testing.T) {
	runPlane := &fakeRunPlane{}
	handler := NewHandlerWithPlanes(HandlerPlanes{Run: runPlane}, nil)

	req := httptest.NewRequest(http.MethodGet, "/runs/run_1/events?stream=1&replay=0", nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, runPlane.runEventsCalled)
	assert.Equal(t, 0, runPlane.subscribeReplayCalled)
	assert.Equal(t, 1, runPlane.subscribeLiveCalled)
}

func TestRunTreeEventsZeroUsesSummaryPath(t *testing.T) {
	runPlane := &fakeRunPlane{}
	handler := NewHandlerWithPlanes(HandlerPlanes{Run: runPlane}, nil)

	req := httptest.NewRequest(http.MethodGet, "/runs/run_1/tree", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, runPlane.runTreeCalled)
	assert.Equal(t, 1, runPlane.runTreeSummaryCalled)
}

func TestRunTreeEventsOneUsesFullPath(t *testing.T) {
	runPlane := &fakeRunPlane{}
	handler := NewHandlerWithPlanes(HandlerPlanes{Run: runPlane}, nil)

	req := httptest.NewRequest(http.MethodGet, "/runs/run_1/tree?events=1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, runPlane.runTreeCalled)
	assert.Equal(t, 0, runPlane.runTreeSummaryCalled)
}

func TestRuntimeRebuildProjectionsDefaultsToIndexMode(t *testing.T) {
	runtimePlane := &fakeRuntimePlane{}
	handler := NewHandlerWithPlanes(HandlerPlanes{Runtime: runtimePlane, Run: &fakeRunPlane{}}, nil)

	req := httptest.NewRequest(http.MethodPost, "/runtime/rebuild-projections", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, runtimePlane.rebuildCalled)
	assert.Equal(t, 0, runtimePlane.rebuildFromEventsCalled)
	assert.Contains(t, rec.Body.String(), `"mode":"index"`)
}

func TestRuntimeRebuildProjectionsSupportsExplicitFullReplay(t *testing.T) {
	runtimePlane := &fakeRuntimePlane{}
	handler := NewHandlerWithPlanes(HandlerPlanes{Runtime: runtimePlane, Run: &fakeRunPlane{}}, nil)

	req := httptest.NewRequest(http.MethodPost, "/runtime/rebuild-projections?mode=from_events", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, runtimePlane.rebuildCalled)
	assert.Equal(t, 1, runtimePlane.rebuildFromEventsCalled)
	assert.Contains(t, rec.Body.String(), `"mode":"from_events"`)
}
