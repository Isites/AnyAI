package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Isites/anyai/internal/gateway"
	"github.com/Isites/anyai/internal/runtime/input"
	runtimesession "github.com/Isites/anyai/internal/runtime/session"
	runtimesessionevents "github.com/Isites/anyai/internal/runtime/sessionevents"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliTestSessionSurface struct {
	store *runtimesession.Store
}

func newCLITestSessionSurface(projectRoot string) *cliTestSessionSurface {
	return &cliTestSessionSurface{
		store: runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions")),
	}
}

func (s *cliTestSessionSurface) ListSessions(agentID string) ([]gateway.SessionInfo, error) {
	infos, err := s.store.List(agentID)
	if err != nil {
		return nil, err
	}
	out := make([]gateway.SessionInfo, len(infos))
	for i, info := range infos {
		out[i] = gateway.SessionInfo{
			ID:           info.ID,
			CreatedAt:    info.CreatedAt,
			LastActivity: info.LastActivity,
			EntryCount:   info.EntryCount,
		}
	}
	return out, nil
}

func (s *cliTestSessionSurface) LoadSession(agentID, sessionID string) (gateway.SessionView, error) {
	if !s.store.Exists(agentID, sessionID) {
		return gateway.SessionView{}, fmt.Errorf("session %q not found", sessionID)
	}
	sess, err := s.store.Load(agentID, sessionID)
	if err != nil {
		return gateway.SessionView{}, err
	}
	records := runtimesessionevents.HistoryEventRecords(agentID, sessionID, sess.History())
	events := make([]gateway.Event, len(records))
	for i, record := range records {
		events[i] = gateway.Event{
			SchemaVersion:   record.SchemaVersion,
			Sequence:        record.Sequence,
			RunID:           record.RunID,
			RunNodeID:       record.RunNodeID,
			ParentRunNodeID: record.ParentRunNodeID,
			AgentID:         record.AgentID,
			SessionID:       record.SessionID,
			Name:            record.Name,
			Timestamp:       record.Timestamp,
			Payload:         record.Payload,
		}
	}
	return gateway.SessionView{
		AgentID: agentID,
		ID:      sessionID,
		History: runtimesession.SerializeHistory(sess),
		Events:  events,
	}, nil
}

func (s *cliTestSessionSurface) CreateSession(agentID, requestedKey, prefix string) (string, error) {
	sessionID := sanitizeCLISessionID(requestedKey)
	if sessionID == "" {
		sessionID = input.DefaultSessionID(prefix, time.Now().UTC())
	}
	if s.store.Exists(agentID, sessionID) {
		return sessionID, nil
	}
	return sessionID, s.store.Create(agentID, sessionID)
}

func executeCLICommand(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch typed := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var msgs []tea.Msg
		for _, child := range typed {
			msgs = append(msgs, executeCLICommand(child)...)
		}
		return msgs
	default:
		return []tea.Msg{typed}
	}
}

func TestParseCLIReferencesNoRefs(t *testing.T) {
	blocks := ParseCLIReferences("hello world")
	assert.Nil(t, blocks)
}

func TestParseCLIReferencesFileRef(t *testing.T) {
	blocks := ParseCLIReferences("check @file:/tmp/test.txt please")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "file", blocks[0].Type)
	assert.Equal(t, "test.txt", blocks[0].Name)
	assert.Contains(t, blocks[0].Path, "test.txt")
}

func TestParseCLIReferencesFileRefNaturalSyntax(t *testing.T) {
	blocks := ParseCLIReferences("check @file /tmp/test.txt please")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "file", blocks[0].Type)
	assert.Equal(t, "test.txt", blocks[0].Name)
	assert.Contains(t, blocks[0].Path, "test.txt")
}

func TestParseCLIReferencesDirRef(t *testing.T) {
	blocks := ParseCLIReferences("scan @dir:/tmp/mydir")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "dir", blocks[0].Type)
	assert.Equal(t, "mydir", blocks[0].Name)
}

func TestParseCLIReferencesDirRefNaturalSyntax(t *testing.T) {
	blocks := ParseCLIReferences("scan @dir /tmp/mydir")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "dir", blocks[0].Type)
	assert.Equal(t, "mydir", blocks[0].Name)
}

func TestParseCLIReferencesURLRef(t *testing.T) {
	blocks := ParseCLIReferences("fetch @url:https://example.com/page")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "url", blocks[0].Type)
	assert.Equal(t, "https://example.com/page", blocks[0].URL)
}

func TestParseCLIReferencesURLRefNaturalSyntax(t *testing.T) {
	blocks := ParseCLIReferences("fetch @url https://example.com/page")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "url", blocks[0].Type)
	assert.Equal(t, "https://example.com/page", blocks[0].URL)
}

func TestParseCLIReferencesImageRef(t *testing.T) {
	blocks := ParseCLIReferences("inspect @image:/tmp/diagram.png")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "image", blocks[0].Type)
	assert.Equal(t, "diagram.png", blocks[0].Name)
	assert.Contains(t, blocks[0].Path, "diagram.png")
}

func TestParseCLIReferencesAutoImagePathRef(t *testing.T) {
	blocks := ParseCLIReferences("inspect @/tmp/diagram.png")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "image", blocks[0].Type)
	assert.Equal(t, "diagram.png", blocks[0].Name)
	assert.Contains(t, blocks[0].Path, "diagram.png")
}

func TestParseCLIReferencesEmbeddedAutoImagePathRef(t *testing.T) {
	blocks := ParseCLIReferences("游戏无法玩，对应最新截图1@~/Desktop/8f34523d-b295-417d-b229-6fad16aea560.png")
	require.Len(t, blocks, 1)
	assert.Equal(t, "image", blocks[0].Type)
	assert.Equal(t, "8f34523d-b295-417d-b229-6fad16aea560.png", blocks[0].Name)
	assert.Contains(t, blocks[0].Path, filepath.Join("Desktop", "8f34523d-b295-417d-b229-6fad16aea560.png"))
}

func TestParseCLIInputStripsEmbeddedImageReferenceFromPrompt(t *testing.T) {
	cleanText, blocks := ParseCLIInput("游戏无法玩，对应最新截图1@~/Desktop/8f34523d-b295-417d-b229-6fad16aea560.png")
	require.Len(t, blocks, 1)
	assert.Equal(t, "游戏无法玩，对应最新截图1", cleanText)
	assert.NotContains(t, cleanText, "@~")
	assert.NotContains(t, cleanText, "Desktop")
	assert.Equal(t, "image", blocks[0].Type)
}

func TestParseCLIInputStripsExplicitReferences(t *testing.T) {
	cleanText, blocks := ParseCLIInput("请看 @image:/tmp/diagram.png 和 @file /tmp/brief.txt")
	require.Len(t, blocks, 2)
	assert.Equal(t, "请看 和", cleanText)
	assert.Equal(t, "image", blocks[0].Type)
	assert.Equal(t, "file", blocks[1].Type)
}

func TestParseCLIReferencesPlainImagePathNeedsAtPrefix(t *testing.T) {
	blocks := ParseCLIReferences("inspect /tmp/diagram.png")
	assert.Nil(t, blocks)
}

func TestParseCLIReferencesPDFRefNaturalSyntax(t *testing.T) {
	blocks := ParseCLIReferences("read @pdf /tmp/spec.pdf")
	assert.Len(t, blocks, 1)
	assert.Equal(t, "pdf", blocks[0].Type)
	assert.Equal(t, "spec.pdf", blocks[0].Name)
	assert.Contains(t, blocks[0].Path, "spec.pdf")
}

func TestParseCLIReferencesMultipleRefs(t *testing.T) {
	blocks := ParseCLIReferences("read @file:/tmp/a.txt and @dir:/tmp/subdir")
	assert.Len(t, blocks, 2)
	assert.Equal(t, "file", blocks[0].Type)
	assert.Equal(t, "dir", blocks[1].Type)
}

func TestParseCLIReferencesEmptyPath(t *testing.T) {
	// @file: with no path after it — should not produce a block
	blocks := ParseCLIReferences("check @file:")
	assert.Nil(t, blocks)
}

func TestSummarizeCLIInputBlocks(t *testing.T) {
	blocks := []gateway.InputBlock{
		{Type: "image", Name: "diagram.png", Path: "/tmp/diagram.png"},
		{Type: "pdf", Name: "spec.pdf", Path: "/tmp/spec.pdf"},
	}
	assert.Equal(t, "已附加 image:diagram.png  •  pdf:spec.pdf", summarizeCLIInputBlocks(blocks))
}

func TestHeadlessCLIInjectTextParsesAttachmentReferences(t *testing.T) {
	ch := NewHeadlessCLIChannel("/tmp/demo-project", "lead")
	require.NoError(t, ch.Connect(context.Background()))
	defer func() { _ = ch.Disconnect() }()

	require.NoError(t, ch.InjectText("游戏无法玩，对应最新截图1@/tmp/diagram.png"))

	select {
	case msg := <-ch.Receive():
		assert.Equal(t, "游戏无法玩，对应最新截图1", msg.Text)
		require.Len(t, msg.Blocks, 1)
		assert.Equal(t, "image", msg.Blocks[0].Type)
		assert.Equal(t, "diagram.png", msg.Blocks[0].Name)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for parsed CLI message")
	}
}

func TestCLIModelRendersPersistentRunTreeAndConversation(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_parent_123456",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.started",
		Timestamp: now,
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_parent_123456",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "agent.call.started",
		Timestamp: now.Add(500 * time.Millisecond),
		Payload: map[string]any{
			"target_agent": "researcher",
			"task":         "请调研并返回三条重点",
		},
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child_123456",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_123456",
		ParentAgentID: "lead",
		Name:          "run.started",
		Timestamp:     now.Add(time.Second),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child_123456",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_123456",
		ParentAgentID: "lead",
		Name:          "tool.called",
		Timestamp:     now.Add(1500 * time.Millisecond),
		Payload: map[string]any{
			"tool":  "web_search",
			"input": "最新行业动态",
		},
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child_123456",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_123456",
		ParentAgentID: "lead",
		Name:          "memory.recalled",
		Timestamp:     now.Add(1600 * time.Millisecond),
		Payload: map[string]any{
			"query": "最新行业动态",
			"entries": []map[string]any{
				{"id": "episodic/current-focus", "matched_terms": []string{"行业", "动态"}},
			},
		},
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child_123456",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_123456",
		ParentAgentID: "lead",
		Name:          "run.completed",
		Timestamp:     now.Add(2 * time.Second),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_parent_123456",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.completed",
		Timestamp: now.Add(3 * time.Second),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliAssistantMessageMsg{Text: "已经完成汇总。"})
	model = updated.(*cliModel)

	view := model.View()
	assert.Contains(t, view, "AnyAI")
	assert.Contains(t, view, "Session Window")
	assert.Contains(t, view, "Esc")
	assert.Contains(t, view, "2 done")
	assert.Contains(t, view, "state idle")
	assert.Contains(t, model.renderConversation(), "已经完成汇总")
	assert.NotContains(t, view, "Runtime Inspector")
	assert.NotContains(t, view, "RunTree")
	assert.NotContains(t, view, "Latest Call Stack")
}

func TestCLIModelShowsCompactionCapabilityAndStrategy(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_compact_1",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "session.compact.completed",
		Timestamp: now,
		Payload: map[string]any{
			"provider":           "anthropic",
			"model":              "claude-sonnet-4-6",
			"compact_capability": "native_compact_available",
			"compact_strategy":   "native_compact",
			"keep_entries":       6,
			"history_len_before": 48,
			"history_len_after":  7,
			"summary_source":     "provider_model",
			"summary":            "checkpoint summary",
		},
	}})
	model = updated.(*cliModel)

	traceView := model.renderTrace()
	assert.Contains(t, traceView, "session compaction completed")
	assert.Contains(t, traceView, "native_compact_available")
	assert.Contains(t, traceView, "native_compact")
	assert.Contains(t, traceView, "provider anthropic")
	assert.Contains(t, traceView, "checkpoint summary")
}

func TestCLIModelStartsWithCollapsedInspector(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	assert.True(t, model.inspectorCollapsed)
	trace := model.renderTrace()
	assert.Contains(t, trace, "Latest Call Stack")
	assert.Contains(t, trace, "RunTree")
}

func TestCLIModelStartupCardUsesStatusLabel(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	conversation := model.renderConversation()
	assert.Contains(t, conversation, "STATUS")
	assert.NotContains(t, conversation, "SYSTEM")
}

func TestCLIModelSyncsRootSessionHistoryIntoConversation(t *testing.T) {
	projectRoot := t.TempDir()
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))

	rootSession, err := store.Load("lead", "cli_local")
	require.NoError(t, err)
	rootSession.Append(runtimesession.UserMessageEntry("拆一下任务"))

	childSession, err := store.Load("researcher", "agentcall_researcher_trace_1")
	require.NoError(t, err)
	childSession.Append(runtimesession.AssistantMessageEntry("子代理内部消息"))

	model := newCLIModelWithSession(projectRoot, "lead", newCLITestSessionSurface(projectRoot), func(string, string, ...string) {})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.started",
		Timestamp: time.Now(),
	}})
	model = updated.(*cliModel)
	assert.Contains(t, model.renderConversation(), "拆一下任务")

	rootSession.Append(runtimesession.AssistantMessageEntry("阶段一：先分析测试现状。"))

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child",
		AgentID:       "researcher",
		SessionID:     "agentcall_researcher_trace_1",
		ParentAgentID: "lead",
		Name:          "run.started",
		Timestamp:     time.Now().Add(time.Second),
	}})
	model = updated.(*cliModel)

	conversation := model.renderConversation()
	assert.Contains(t, conversation, "阶段一：先分析测试现状。")
	assert.NotContains(t, conversation, "子代理内部消息")
}

func TestCLIModelDoesNotDuplicateOutboundAssistantTextWhenSessionAlreadySynced(t *testing.T) {
	projectRoot := t.TempDir()
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))

	sess, err := store.Load("lead", "cli_local")
	require.NoError(t, err)
	sess.Append(runtimesession.UserMessageEntry("继续"))
	sess.Append(runtimesession.AssistantMessageEntry("最终答复"))

	model := newCLIModelWithSession(projectRoot, "lead", newCLITestSessionSurface(projectRoot), func(string, string, ...string) {})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.completed",
		Timestamp: time.Now(),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliAssistantMessageMsg{Text: "最终答复"})
	model = updated.(*cliModel)

	assert.Equal(t, 1, strings.Count(model.renderConversation(), "最终答复"))
}

func TestCLIModelDoesNotDuplicatePendingUserMessageWhenPersistedWithAttachment(t *testing.T) {
	projectRoot := t.TempDir()
	surface := newCLITestSessionSurface(projectRoot)
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))

	var sentText []string
	var sentMessageID string
	model := newCLIModelWithSession(projectRoot, "lead", surface, func(text, sessionID string, messageID ...string) {
		sentText = append(sentText, text)
		if len(messageID) > 0 {
			sentMessageID = messageID[0]
		}
		sess, err := store.Load("lead", sessionID)
		require.NoError(t, err)
		sess.Append(runtimesession.UserMessageWithImagesEntryWithID(sentMessageID, text, []runtimesession.ImageData{
			{ID: "img_1", Name: "shot.png", MimeType: "image/png"},
		}))
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("/session image-session")
	model.input.CursorEnd()
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	model.input.SetValue("检查这个项目 @~/Desktop/shot.png")
	model.input.CursorEnd()

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)
	require.Equal(t, []string{"检查这个项目 @~/Desktop/shot.png"}, sentText)
	require.NotEmpty(t, sentMessageID)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: model.cliSessionID,
		Name:      "session.input.stored",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"entry_id": sentMessageID,
			"text":     "检查这个项目 @~/Desktop/shot.png",
			"images": []runtimesession.ImageData{
				{ID: "img_1", Name: "shot.png", MimeType: "image/png"},
			},
		},
	}})
	model = updated.(*cliModel)

	conversation := model.renderConversation()
	assert.Equal(t, 1, strings.Count(conversation, "检查这个项目 @~/Desktop/shot.png"))
	assert.Contains(t, conversation, "image:shot.png")
	assert.Empty(t, model.pendingMessages)
}

func TestCLIModelPreloadsLatestEntryAgentSession(t *testing.T) {
	projectRoot := t.TempDir()
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))

	oldSession, err := store.Load("lead", "cli_old")
	require.NoError(t, err)
	oldSession.Append(runtimesession.UserMessageEntry("旧会话"))
	time.Sleep(time.Millisecond)

	latestSession, err := store.Load("lead", "cli_latest")
	require.NoError(t, err)
	latestSession.Append(runtimesession.UserMessageEntry("最近会话"))

	model := newCLIModelWithSession(projectRoot, "lead", newCLITestSessionSurface(projectRoot), func(string, string, ...string) {})

	assert.Equal(t, "cli_latest", model.activeSessionID)
	assert.Equal(t, "cli_latest", model.cliSessionID)
	assert.Contains(t, model.renderConversation(), "最近会话")
	assert.NotContains(t, model.renderConversation(), "旧会话")
}

func TestCLIModelSessionCommandCreatesAndUsesSession(t *testing.T) {
	projectRoot := t.TempDir()
	var sentText []string
	var sentSession []string
	model := newCLIModelWithSession(projectRoot, "lead", newCLITestSessionSurface(projectRoot), func(text, sessionID string, _ ...string) {
		sentText = append(sentText, text)
		sentSession = append(sentSession, sessionID)
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("/session fix-session")
	model.input.CursorEnd()
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	assert.Equal(t, "fix-session", model.cliSessionID)
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))
	assert.True(t, store.Exists("lead", "fix-session"))
	assert.Empty(t, model.pendingMessages)
	assert.NotContains(t, model.renderConversation(), "META")
	assert.NotContains(t, model.renderConversation(), "已创建并切换到 session")
	assert.Contains(t, model.renderInputPanel(model.panelWidth), "已创建并切换到 session fix-session")

	model.input.SetValue("继续处理")
	model.input.CursorEnd()
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	assert.Equal(t, []string{"继续处理"}, sentText)
	assert.Equal(t, []string{"fix-session"}, sentSession)
	assert.NotContains(t, model.renderInputPanel(model.panelWidth), "已创建并切换到 session")
}

func TestCLIModelIgnoresStaleAssistantMessageFromPreviousSession(t *testing.T) {
	projectRoot := t.TempDir()
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))
	oldSession, err := store.Load("lead", "old-session")
	require.NoError(t, err)
	oldSession.Append(runtimesession.UserMessageEntry("旧问题"))

	model := newCLIModelWithSession(projectRoot, "lead", newCLITestSessionSurface(projectRoot), func(string, string, ...string) {})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	require.Equal(t, "old-session", model.cliSessionID)

	model.input.SetValue("/session new-session")
	model.input.CursorEnd()
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)
	require.Equal(t, "new-session", model.cliSessionID)

	updated, _ = model.Update(cliAssistantMessageMsg{
		Text:      "旧 session 迟到回复",
		AgentID:   "lead",
		SessionID: "old-session",
		RunID:     "run_old",
	})
	model = updated.(*cliModel)

	assert.NotContains(t, model.renderConversation(), "旧 session 迟到回复")
	assert.Empty(t, model.pendingMessages)
}

func TestCLIModelInputPanelShowsSingleLineCommandHint(t *testing.T) {
	model := newCLIModel("", "", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	panel := model.renderInputPanel(model.panelWidth)
	assert.Contains(t, panel, "/session [id]")
	assert.Contains(t, panel, "/quit")
	assert.Contains(t, panel, "Enter send")
	assert.Contains(t, panel, "Ctrl+J newline")
	lines := strings.Split(panel, "\n")
	hintLines := 0
	for _, line := range lines {
		if strings.Contains(line, "Enter send") || strings.Contains(line, "/session [id]") || strings.Contains(line, "/quit") {
			hintLines++
		}
	}
	assert.Equal(t, 1, hintLines)
	assert.NotContains(t, panel, "/new")
	assert.NotContains(t, panel, "/session-new")
	assert.NotContains(t, panel, "/session new")
	assert.NotContains(t, panel, "/stack")
	assert.NotContains(t, panel, "/exit")
}

func TestCLIModelAllSlashCommandsAreEffective(t *testing.T) {
	t.Run("session without id creates generated session", func(t *testing.T) {
		projectRoot := t.TempDir()
		model := newCLIModelWithSession(projectRoot, "lead", newCLITestSessionSurface(projectRoot), func(string, string, ...string) {})
		updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		model = updated.(*cliModel)

		model.input.SetValue("/session")
		model.input.CursorEnd()
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(*cliModel)
		executeCLICommand(cmd)

		assert.True(t, strings.HasPrefix(model.cliSessionID, "cli_"))
		store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))
		assert.True(t, store.Exists("lead", model.cliSessionID))
	})

	t.Run("session with missing id creates that session", func(t *testing.T) {
		projectRoot := t.TempDir()
		model := newCLIModelWithSession(projectRoot, "lead", newCLITestSessionSurface(projectRoot), func(string, string, ...string) {})
		updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		model = updated.(*cliModel)

		model.input.SetValue("/session beta")
		model.input.CursorEnd()
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(*cliModel)
		executeCLICommand(cmd)

		assert.Equal(t, "beta", model.cliSessionID)
		store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))
		assert.True(t, store.Exists("lead", "beta"))
	})

	t.Run("session with existing id reuses that session", func(t *testing.T) {
		projectRoot := t.TempDir()
		store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))
		existing, err := store.Load("lead", "alpha")
		require.NoError(t, err)
		existing.Append(runtimesession.UserMessageEntry("已有会话内容"))

		model := newCLIModelWithSession(projectRoot, "lead", newCLITestSessionSurface(projectRoot), func(string, string, ...string) {})
		updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		model = updated.(*cliModel)

		model.input.SetValue("/session beta")
		model.input.CursorEnd()
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(*cliModel)
		executeCLICommand(cmd)
		require.Equal(t, "beta", model.cliSessionID)

		model.input.SetValue("/session alpha")
		model.input.CursorEnd()
		updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(*cliModel)
		executeCLICommand(cmd)

		assert.Equal(t, "alpha", model.cliSessionID)
		assert.Contains(t, model.renderConversation(), "已有会话内容")
	})

	t.Run("session rejects multiple args", func(t *testing.T) {
		model := newCLIModel("", "", func(string) {})

		model.input.SetValue("/session one two")
		model.input.CursorEnd()
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(*cliModel)
		executeCLICommand(cmd)

		assert.Empty(t, model.pendingMessages)
		assert.Equal(t, "用法: /session [session-id]", model.sessionNotice)
		assert.Contains(t, model.renderInputPanel(120), "用法: /session [session-id]")
		assert.NotContains(t, model.renderConversation(), "META")
	})

	t.Run("quit", func(t *testing.T) {
		model := newCLIModel("", "", func(string) {})
		model.input.SetValue("/quit")
		model.input.CursorEnd()

		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(*cliModel)

		assert.NotNil(t, model)
		if assert.NotNil(t, cmd) {
			assert.IsType(t, tea.QuitMsg{}, cmd())
		}
	})
}

func TestCLIModelRendersSessionTranscriptCardsForStructuredEntries(t *testing.T) {
	projectRoot := t.TempDir()
	store := runtimesession.NewStore(filepath.Join(projectRoot, "anyai", "sessions"))

	sess, err := store.Load("lead", "cli_local")
	require.NoError(t, err)
	sess.Append(runtimesession.UserMessageEntry("开始执行"))
	sess.Append(runtimesession.ToolCallEntry("tool_1", "readfile", json.RawMessage(`{"path":"src/app.js"}`)))
	sess.Append(runtimesession.ToolResultEntry("tool_1", "file content", "", nil))
	sess.Append(runtimesession.AssistantMessageEntry("已完成阶段播报"))
	sess.Append(runtimesession.MetaEntry("Run failed: browser click timed out"))

	model := newCLIModelWithSession(projectRoot, "lead", newCLITestSessionSurface(projectRoot), func(string, string, ...string) {})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.completed",
		Timestamp: time.Now(),
	}})
	model = updated.(*cliModel)

	conversation := model.renderConversation()
	assert.Contains(t, conversation, "USER")
	assert.Contains(t, conversation, "开始执行")
	assert.Contains(t, conversation, "TOOL CALL")
	assert.Contains(t, conversation, "lead -> readfile [success]")
	assert.Contains(t, conversation, "src/app.js")
	assert.NotContains(t, conversation, "TOOL RESULT")
	assert.NotContains(t, conversation, "file content")
	assert.NotContains(t, conversation, "PHASE")
	assert.NotContains(t, conversation, "PROGRESS")
	assert.Contains(t, conversation, "已完成阶段播报")
	assert.Contains(t, conversation, "META")
	assert.Contains(t, conversation, "Run failed: browser click timed out")
}

func TestCLIModelSurfacesRuntimePhasesAndSubagentLifecycle(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "run.started",
		Timestamp: now,
	}})
	model = updated.(*cliModel)
	assert.Contains(t, model.renderTrace(), "thinking")

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "text.delta",
		Timestamp: now.Add(500 * time.Millisecond),
		Payload: map[string]any{
			"text": "正在生成答复",
		},
	}})
	model = updated.(*cliModel)
	trace := model.renderTrace()
	assert.Contains(t, trace, "generating")
	assert.Contains(t, trace, "streaming")

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "agent.call.started",
		Timestamp: now.Add(time.Second),
		Payload: map[string]any{
			"id":           "deleg_call_1",
			"target_agent": "coder",
			"task":         "实现修复",
		},
	}})
	model = updated.(*cliModel)
	trace = model.renderTrace()
	assert.Contains(t, trace, "subagent active")
	assert.Contains(t, trace, "subagents 1 active / 0 done / 0 failed")
	assert.Contains(t, trace, "waiting on subagent coder")

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child",
		AgentID:       "coder",
		SessionID:     "agentcall_coder_trace_status",
		ParentAgentID: "lead",
		Name:          "run.started",
		Timestamp:     now.Add(1500 * time.Millisecond),
	}})
	model = updated.(*cliModel)
	assert.Contains(t, model.renderTrace(), "coder")

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:         "run_child",
		AgentID:       "coder",
		SessionID:     "agentcall_coder_trace_status",
		ParentAgentID: "lead",
		Name:          "run.completed",
		Timestamp:     now.Add(2 * time.Second),
	}})
	model = updated.(*cliModel)

	updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
		RunID:     "run_root",
		AgentID:   "lead",
		SessionID: "cli_local",
		Name:      "agent.call.completed",
		Timestamp: now.Add(2500 * time.Millisecond),
		Payload: map[string]any{
			"id":           "deleg_call_1",
			"target_agent": "coder",
			"status":       "completed",
			"summary":      "修复已完成",
		},
	}})
	model = updated.(*cliModel)
	trace = model.renderTrace()
	assert.Contains(t, trace, "integrating")
	assert.Contains(t, trace, "subagents 0 active / 1 done / 0 failed")
	assert.Contains(t, trace, "coder finished")
}

func TestCLIModelHeaderAndStatusUseSameRuntimeSummary(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "tech-lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"id":           "call_1",
				"target_agent": "plan-reviewer",
				"task":         "review plan",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(2 * time.Second),
			Payload: map[string]any{
				"id":           "call_2",
				"target_agent": "test-engineer",
				"task":         "verify tests",
			},
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	summary := model.runtimeSummary()
	assert.Equal(t, "subagent active", summary.State)
	assert.Equal(t, 1, summary.RunsLive)
	assert.Equal(t, 2, summary.SubagentsActive)

	status := model.statusEntry.text
	assert.Contains(t, status, "state subagent active")
	assert.Contains(t, status, "runs 1 live / 0 done / 0 failed")
	assert.Contains(t, status, "subagents 2 active / 0 done / 0 failed")
	assert.Contains(t, status, "events")

	header := model.renderHeader(140)
	assert.Contains(t, header, "state")
	assert.Contains(t, header, "subagent active")
	assert.Contains(t, header, "runs")
	assert.Contains(t, header, "1 live / 0 done / 0 failed")
	assert.Contains(t, header, "subagents")
	assert.Contains(t, header, "2 active / 0 done / 0 failed")
}

func TestCLIModelSeparatesTraceNodesByRunAndAgent(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "tech-lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_shared",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:         "run_shared",
			AgentID:       "coder",
			SessionID:     "agentcall_coder_shared",
			ParentAgentID: "tech-lead",
			Name:          "run.started",
			Timestamp:     now.Add(time.Second),
		},
		{
			RunID:         "run_shared",
			AgentID:       "coder",
			SessionID:     "agentcall_coder_shared",
			ParentAgentID: "tech-lead",
			Name:          "run.completed",
			Timestamp:     now.Add(2 * time.Second),
		},
		{
			RunID:     "run_shared",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "tool.called",
			Timestamp: now.Add(3 * time.Second),
			Payload: map[string]any{
				"id":    "tool_after_child",
				"tool":  "bash",
				"input": "go test ./...",
			},
		},
	}
	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	parent := model.traceNodes[cliTraceKey("run_shared", "tech-lead")]
	child := model.traceNodes[cliTraceKey("run_shared", "coder")]
	require.NotNil(t, parent)
	require.NotNil(t, child)
	assert.Equal(t, "running", parent.status)
	assert.Equal(t, "completed", child.status)
	assert.Equal(t, 1, model.runningRunCount())
	running, completed, failed := model.traceStatusCounts()
	assert.Equal(t, 1, running)
	assert.Equal(t, 1, completed)
	assert.Equal(t, 0, failed)
	assert.Equal(t, "tool active", model.runtimeStatusLabel())
}

func TestCLIModelSuppressesTransientRunningActivities(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.activity",
			Timestamp: now.Add(200 * time.Millisecond),
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "text.delta",
			Timestamp: now.Add(400 * time.Millisecond),
			Payload: map[string]any{
				"text": "正在生成答复",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "task.running",
			Timestamp: now.Add(600 * time.Millisecond),
			Payload: map[string]any{
				"task_id":      "task_1",
				"target_agent": "coder",
				"status":       "running",
			},
		},
	}

	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	require.Empty(t, model.activities)
	node := model.traceNodes[cliTraceKey("run_root", "lead")]
	require.NotNil(t, node)
	assert.Equal(t, "text.delta", node.lastEventName)
	assert.Equal(t, "正在生成答复", node.lastEventSummary)
}

func TestWrapCLITextBreaksLongTokens(t *testing.T) {
	wrapped := wrapCLIText(strings.Repeat("a", 35), 10)
	lines := strings.Split(wrapped, "\n")
	assert.Greater(t, len(lines), 1)
	for _, line := range lines {
		assert.LessOrEqual(t, len([]rune(line)), 10)
	}
}

func TestCLIModelCanCollapseInspector(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	initialTraceHeight := model.traceHeight
	assert.True(t, model.inspectorCollapsed)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	assert.True(t, model.inspectorCollapsed)
	assert.Equal(t, initialTraceHeight, model.traceHeight)
	assert.Contains(t, model.View(), "Session Window")
	assert.NotContains(t, model.View(), "Runtime Inspector")
}

func TestCLIModelCanExpandLatestCallStack(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"target_agent": "context-analyst",
				"task":         "读取需求文档并产出分析",
			},
		},
		{
			RunID:         "run_child",
			AgentID:       "context-analyst",
			SessionID:     "agentcall_context",
			ParentAgentID: "tech-lead",
			Name:          "run.started",
			Timestamp:     now.Add(2 * time.Second),
		},
		{
			RunID:         "run_child",
			AgentID:       "context-analyst",
			SessionID:     "agentcall_context",
			ParentAgentID: "tech-lead",
			Name:          "tool.called",
			Timestamp:     now.Add(3 * time.Second),
			Payload: map[string]any{
				"tool":  "read_file",
				"input": "{\"path\":\"workflow-artifacts/user-request.md\"}",
			},
		},
		{
			RunID:         "run_child",
			AgentID:       "context-analyst",
			SessionID:     "agentcall_context",
			ParentAgentID: "tech-lead",
			Name:          "tool.called",
			Timestamp:     now.Add(4 * time.Second),
			Payload: map[string]any{
				"tool":  "bash",
				"input": "{\"command\":\"ls\"}",
			},
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	collapsed := model.renderTrace()
	assert.Contains(t, collapsed, "Latest Call Stack")
	assert.Contains(t, collapsed, "tech-lead -> context-analyst")
	assert.Contains(t, collapsed, "agent call -> context-analyst")
	assert.Contains(t, collapsed, "read_file")
	assert.Contains(t, collapsed, "bash")

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF3})
	model = updated.(*cliModel)

	expanded := model.renderTrace()
	assert.Contains(t, expanded, "tech-lead")
	assert.Contains(t, expanded, "context-analyst")
	assert.Contains(t, expanded, "agent call -> context-analyst")
	assert.Contains(t, expanded, "read_file")
	assert.Contains(t, expanded, "bash")
	assert.Contains(t, expanded, "F2 切换 split / full")
}

func TestCLIModelLatestCallStackShowsNodeErrors(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "tool.called",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"id":    "tool_read",
				"tool":  "read_file",
				"input": "{\"path\":\"missing.md\"}",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "tool.finished",
			Timestamp: now.Add(2 * time.Second),
			Payload: map[string]any{
				"id":    "tool_read",
				"tool":  "read_file",
				"error": "open missing.md: no such file or directory",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.failed",
			Timestamp: now.Add(3 * time.Second),
			Payload: map[string]any{
				"message": "provider stream returned truncated JSON before a complete response: unexpected end of JSON input",
			},
		},
	}

	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	stack := model.renderLatestCallStack(120)
	assert.Contains(t, stack, "read_file")
	assert.Contains(t, stack, "error: open missing.md: no such file or directory")
	assert.Contains(t, stack, "error: provider stream returned truncated JSON before a complete response")
}

func TestCLIModelTreatsRunAbortedAsTerminal(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.aborted",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"message": "run aborted",
				"reason":  "aborted",
			},
		},
	}

	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	node := model.traceNodes[cliTraceKey("run_root", "tech-lead")]
	require.NotNil(t, node)
	assert.Equal(t, "aborted", node.status)
	assert.Equal(t, 0, model.runningRunCount())
	running, completed, failed := model.traceStatusCounts()
	assert.Equal(t, 0, running)
	assert.Equal(t, 0, completed)
	assert.Equal(t, 1, failed)
	assert.Equal(t, "idle", model.runtimeStatusLabel())
	assert.Contains(t, model.renderLatestCallStack(120), "error: run aborted")
}

func TestCLIModelToggleInspectorPreservesTraceContent(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "tech-lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"target_agent": "architect",
				"task":         "输出方案",
			},
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	before := model.renderTrace()

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)
	after := model.renderTrace()

	assert.Equal(t, before, after)
}

func TestCLIModelExpandedInspectorKeepsRunTreeVisible(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 18})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(time.Second),
			Payload: map[string]any{
				"target_agent": "coder",
				"task":         "实现修复",
			},
		},
		{
			RunID:         "run_child",
			AgentID:       "coder",
			SessionID:     "agentcall_child",
			ParentAgentID: "lead",
			Name:          "run.started",
			Timestamp:     now.Add(2 * time.Second),
		},
	}
	for i, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
		for j := 0; j < 8; j++ {
			updated, _ = model.Update(cliRunEventMsg{Event: gateway.RunEvent{
				RunID:     "run_root",
				AgentID:   "lead",
				SessionID: "cli_local",
				Name:      "tool.finished",
				Timestamp: now.Add(time.Duration(i*10+j+3) * time.Second),
				Payload: map[string]any{
					"tool":   "bash",
					"output": "ok",
				},
			}})
			model = updated.(*cliModel)
		}
	}

	view := model.View()
	assert.Contains(t, view, "Session Window")
	assert.NotContains(t, view, "Current Focus")
	assert.NotContains(t, view, "Selected RunTree Node")
	assert.NotContains(t, view, "Global Activity")
}

func TestCLIModelArrowKeysMoveSelectedTraceNode(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:         "run_child",
			AgentID:       "researcher",
			SessionID:     "agentcall_child",
			ParentAgentID: "lead",
			Name:          "run.started",
			Timestamp:     now.Add(time.Second),
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	assert.Equal(t, cliTraceKey("run_root", "lead"), model.selectedTraceKey)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(*cliModel)
	assert.Equal(t, cliTraceKey("run_root", "lead"), model.selectedTraceKey)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(*cliModel)
	assert.Equal(t, cliTraceKey("run_root", "lead"), model.selectedTraceKey)
}

func TestCLIModelPreservesManualChatScrollOnRefresh(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	model = updated.(*cliModel)

	for i := 0; i < 24; i++ {
		updated, _ = model.Update(cliAssistantMessageMsg{Text: fmt.Sprintf("message %02d", i)})
		model = updated.(*cliModel)
	}

	model.chatView.LineUp(6)
	offsetBefore := model.chatView.YOffset
	assert.Greater(t, offsetBefore, 0)
	assert.False(t, model.chatView.AtBottom())

	updated, _ = model.Update(cliAssistantMessageMsg{Text: "new message while reviewing history"})
	model = updated.(*cliModel)

	assert.Equal(t, offsetBefore, model.chatView.YOffset)
	assert.False(t, model.chatView.AtBottom())
}

func TestCLIModelSurfacesBackgroundTaskLifecycle(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyF2})
	model = updated.(*cliModel)

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(200 * time.Millisecond),
			Payload: map[string]any{
				"id":           "deleg_call_1",
				"target_agent": "coder",
				"task":         "实现修复",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "agent.call.completed",
			Timestamp: now.Add(400 * time.Millisecond),
			Payload: map[string]any{
				"id":           "deleg_call_1",
				"target_agent": "coder",
				"status":       "running",
				"task_id":      "task_1",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "task.started",
			Timestamp: now.Add(600 * time.Millisecond),
			Payload: map[string]any{
				"task_id":      "task_1",
				"target_agent": "coder",
				"status":       "running",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "task.completed",
			Timestamp: now.Add(800 * time.Millisecond),
			Payload: map[string]any{
				"task_id":      "task_1",
				"target_agent": "coder",
				"status":       "completed",
				"summary":      "修复已完成",
			},
		},
	}
	for _, event := range events {
		updated, _ = model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	trace := model.renderTrace()
	assert.Contains(t, trace, "state ")
	assert.Contains(t, trace, "task completed")
	assert.Contains(t, trace, "subagents:")
	assert.Contains(t, trace, "task done")
	assert.Contains(t, trace, "coder task completed")
}

func TestCLIModelDoesNotCountToolTasksAsSubagents(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.completed",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "task.started",
			Timestamp: now.Add(100 * time.Millisecond),
			Payload: map[string]any{
				"task_id":   "task_tool_1",
				"task_kind": "tool",
				"tool":      "write_file",
				"status":    "running",
			},
		},
		{
			RunID:     "run_root",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "task.completed",
			Timestamp: now.Add(200 * time.Millisecond),
			Payload: map[string]any{
				"task_id":   "task_tool_1",
				"task_kind": "tool",
				"tool":      "write_file",
				"status":    "completed",
				"summary":   "wrote file",
			},
		},
	}
	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	summary := model.runtimeSummary()
	assert.Equal(t, 0, summary.RunsLive)
	assert.Equal(t, 1, summary.RunsDone)
	assert.Equal(t, 0, summary.SubagentsActive)
	assert.Equal(t, 0, summary.SubagentsDone)
	assert.Equal(t, 0, summary.SubagentsFailed)

	trace := model.renderTrace()
	assert.NotContains(t, trace, "subagents:")
	assert.Contains(t, trace, "write_file")
}

func TestCLIModelDoesNotDoubleCountAgentTaskAndAgentCallCompletion(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "review-lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "review-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "report-generator",
			SessionID: "cli_local",
			Name:      "task.started",
			Timestamp: now.Add(100 * time.Millisecond),
			Payload: map[string]any{
				"task_id":      "task_report",
				"task_kind":    "agent",
				"target_agent": "report-generator",
				"status":       "running",
			},
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "review-lead",
			SessionID: "cli_local",
			Name:      "agent.call.started",
			Timestamp: now.Add(200 * time.Millisecond),
			Payload: map[string]any{
				"id":           "call_report",
				"target_agent": "report-generator",
				"task":         "generate final report",
			},
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "report-generator",
			SessionID: "agentcall_report-generator_run_root",
			Name:      "run.started",
			Timestamp: now.Add(300 * time.Millisecond),
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::report-generator",
			AgentID:   "report-generator",
			SessionID: "agentcall_report-generator_run_root",
			Name:      "run.completed",
			Timestamp: now.Add(400 * time.Millisecond),
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "report-generator",
			SessionID: "cli_local",
			Name:      "task.completed",
			Timestamp: now.Add(500 * time.Millisecond),
			Payload: map[string]any{
				"task_id":      "task_report",
				"task_kind":    "agent",
				"target_agent": "report-generator",
				"status":       "completed",
				"summary":      "report generated",
			},
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "review-lead",
			SessionID: "cli_local",
			Name:      "agent.call.completed",
			Timestamp: now.Add(600 * time.Millisecond),
			Payload: map[string]any{
				"id":           "call_report",
				"task_id":      "task_report",
				"target_agent": "report-generator",
				"status":       "completed",
				"summary":      "report generated",
			},
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "review-lead",
			SessionID: "cli_local",
			Name:      "run.completed",
			Timestamp: now.Add(700 * time.Millisecond),
		},
	}
	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	summary := model.runtimeSummary()
	assert.Equal(t, 0, summary.RunsLive)
	assert.Equal(t, 2, summary.RunsDone)
	assert.Equal(t, 0, summary.SubagentsActive)
	assert.Equal(t, 1, summary.SubagentsDone)
	assert.Equal(t, 0, summary.SubagentsFailed)
}

func TestCLIModelIgnoresLegacyAgentBaseOutputNodeWhenTaskNodeCompleted(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "review-lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "review-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:           "run_root",
			RunNodeID:       "run_root::report-generator",
			ParentRunNodeID: "run_root::review-lead",
			AgentID:         "report-generator",
			SessionID:       "agentcall_report-generator_run_root",
			Name:            "session.output.stored",
			Timestamp:       now.Add(100 * time.Millisecond),
			Payload: map[string]any{
				"text": "final report ready",
			},
		},
		{
			RunID:           "run_root",
			RunNodeID:       "run_root::report-generator::task_report",
			ParentRunNodeID: "run_root::review-lead",
			AgentID:         "report-generator",
			SessionID:       "agentcall_report-generator_run_root",
			Name:            "run.completed",
			Timestamp:       now.Add(200 * time.Millisecond),
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "review-lead",
			SessionID: "cli_local",
			Name:      "run.completed",
			Timestamp: now.Add(300 * time.Millisecond),
		},
	}
	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	summary := model.runtimeSummary()
	assert.Equal(t, "idle", summary.State)
	assert.Equal(t, 0, summary.RunsLive)
	assert.Equal(t, 2, summary.RunsDone)
	assert.Equal(t, "queued", model.traceNodes["run_root::report-generator"].status)
	assert.Equal(t, 0, model.runningRunCount())
	assert.Equal(t, "idle", model.runtimeStatusLabel())
	assert.Contains(t, model.renderHeader(140), "idle")
}

func TestCLIModelTopStateIgnoresTerminalNodeWithStaleBackgroundTask(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "review-lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "review-lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "review-lead",
			SessionID: "cli_local",
			Name:      "task.running",
			Timestamp: now.Add(100 * time.Millisecond),
			Payload: map[string]any{
				"task_id":   "task_tool",
				"task_kind": "tool",
				"tool":      "write_file",
				"status":    "running",
			},
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::review-lead",
			AgentID:   "review-lead",
			SessionID: "cli_local",
			Name:      "run.completed",
			Timestamp: now.Add(200 * time.Millisecond),
		},
	}
	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	summary := model.runtimeSummary()
	assert.Equal(t, 0, summary.RunsLive)
	assert.Equal(t, 1, summary.RunsDone)
	assert.Equal(t, "idle", summary.State)
	assert.Equal(t, 0, model.runningRunCount())
	assert.Equal(t, "idle", model.runtimeStatusLabel())
}

func TestCLIModelRunLifecycleUsesAgentTraceKeyWhenRunNodeIDConflicts(t *testing.T) {
	model := newCLIModel("/tmp/demo-project", "lead", func(string) {})
	now := time.Now()

	events := []gateway.RunEvent{
		{
			RunID:     "run_root",
			RunNodeID: "run_root::lead",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.started",
			Timestamp: now,
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::lead",
			AgentID:   "lead",
			SessionID: "cli_local",
			Name:      "run.completed",
			Timestamp: now.Add(100 * time.Millisecond),
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::lead",
			AgentID:   "worker",
			SessionID: "agentcall_worker_run_root",
			Name:      "run.started",
			Timestamp: now.Add(200 * time.Millisecond),
		},
		{
			RunID:     "run_root",
			RunNodeID: "run_root::worker",
			AgentID:   "worker",
			SessionID: "agentcall_worker_run_root",
			Name:      "run.completed",
			Timestamp: now.Add(300 * time.Millisecond),
		},
	}
	for _, event := range events {
		updated, _ := model.Update(cliRunEventMsg{Event: event})
		model = updated.(*cliModel)
	}

	summary := model.runtimeSummary()
	assert.Equal(t, 0, summary.RunsLive)
	assert.Equal(t, 2, summary.RunsDone)
	assert.Equal(t, "completed", model.traceNodes["run_root::lead"].status)
	assert.Equal(t, "completed", model.traceNodes["run_root::worker"].status)
}

func TestCLIModelSupportsShortcutQuit(t *testing.T) {
	model := newCLIModel("", "", func(string) {})

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(*cliModel)
	assert.NotNil(t, model)
	if assert.NotNil(t, cmd) {
		assert.IsType(t, tea.QuitMsg{}, cmd())
	}
}

func TestCLIModelEnterSendsInput(t *testing.T) {
	var sent []string
	model := newCLIModel("", "", func(text string) {
		sent = append(sent, text)
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	assert.Equal(t, 1, model.input.Height())
	model.input.SetValue("第一行")
	model.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	assert.Equal(t, []string{"第一行"}, sent)
	assert.Equal(t, "", model.input.Value())
	assert.Equal(t, 1, model.input.Height())
	assert.Equal(t, "status", model.messages[len(model.messages)-1].kind)
	assert.Equal(t, "第一行", model.messages[len(model.messages)-2].text)
	assert.NotContains(t, model.renderConversation(), "pending")
}

func TestCLIModelCtrlVAttachesClipboardImageAndSendsIt(t *testing.T) {
	oldReader := cliClipboardImageReader
	cliClipboardImageReader = func() (gateway.InputBlock, error) {
		return gateway.InputBlock{
			Type:     "image",
			Name:     "clipboard.png",
			MimeType: "image/png",
			Data:     []byte{0x89, 'P', 'N', 'G'},
			Meta:     map[string]any{"source": "clipboard"},
		}, nil
	}
	defer func() { cliClipboardImageReader = oldReader }()

	var sentText string
	var sentBlocks []gateway.InputBlock
	model := newCLIModelWithSession(t.TempDir(), "lead", nil, func(text, _ string, _ ...string) {
		sentText = text
	})
	model.sendInputWithBlocks = func(text, _ string, blocks []gateway.InputBlock, _ ...string) {
		sentText = text
		sentBlocks = append([]gateway.InputBlock(nil), blocks...)
	}

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	model = updated.(*cliModel)
	for _, msg := range executeCLICommand(cmd) {
		updated, _ = model.Update(msg)
		model = updated.(*cliModel)
	}

	assert.Contains(t, model.renderInputPanel(model.panelWidth), "image:clipboard.png")
	model.input.SetValue("看这张图")
	model.input.CursorEnd()
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	assert.Equal(t, "看这张图", sentText)
	require.Len(t, sentBlocks, 1)
	assert.Equal(t, "image", sentBlocks[0].Type)
	assert.Equal(t, "clipboard.png", sentBlocks[0].Name)
	assert.Equal(t, []byte{0x89, 'P', 'N', 'G'}, sentBlocks[0].Data)
	assert.Empty(t, model.composerAttachments)
	assert.Contains(t, model.renderConversation(), "image:clipboard.png")
}

func TestCLIModelPasteImageCommandReportsClipboardError(t *testing.T) {
	oldReader := cliClipboardImageReader
	cliClipboardImageReader = func() (gateway.InputBlock, error) {
		return gateway.InputBlock{}, fmt.Errorf("empty clipboard")
	}
	defer func() { cliClipboardImageReader = oldReader }()

	model := newCLIModel("", "", func(string) {})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("/paste-image")
	model.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	for _, msg := range executeCLICommand(cmd) {
		updated, _ = model.Update(msg)
		model = updated.(*cliModel)
	}

	assert.Contains(t, model.renderInputPanel(model.panelWidth), "剪贴板没有可用图片")
}

func TestCLIModelCtrlJAddsNewline(t *testing.T) {
	var sent []string
	model := newCLIModel("", "", func(text string) {
		sent = append(sent, text)
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("第一行")
	model.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	assert.Equal(t, "第一行\n", model.input.Value())
	assert.Equal(t, 2, model.input.Height())
	assert.Empty(t, sent)
}

func TestCLIModelViewKeepsInputPanelVisibleAfterInputGrows(t *testing.T) {
	model := newCLIModel("", "", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("你")
	model.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	view := model.View()
	assert.Contains(t, view, "Compose")
	assert.Contains(t, view, "Session Window")
}

func TestCLIModelFirstEnterKeepsPreviousLineVisible(t *testing.T) {
	model := newCLIModel("", "", func(string) {})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("你")
	model.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	panel := model.renderInputPanel(model.panelWidth)
	assert.Contains(t, panel, "你")
}

func TestCLIModelEnterSendsMultilineInput(t *testing.T) {
	var sent []string
	model := newCLIModel("", "", func(text string) {
		sent = append(sent, text)
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*cliModel)
	model.input.SetValue("第一行\n第二行")
	model.syncInputHeight()
	assert.Equal(t, 2, model.input.Height())

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*cliModel)
	executeCLICommand(cmd)

	assert.Equal(t, []string{"第一行\n第二行"}, sent)
	assert.Equal(t, "", model.input.Value())
	assert.Equal(t, 1, model.input.Height())
	assert.Equal(t, "status", model.messages[len(model.messages)-1].kind)
	assert.Equal(t, "第一行\n第二行", model.messages[len(model.messages)-2].text)
}
