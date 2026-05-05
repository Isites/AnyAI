package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	runtimesession "github.com/Isites/anyai/internal/runtime/session"
	runtimesessionevents "github.com/Isites/anyai/internal/runtime/sessionevents"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	cliSurfaceColor       = lipgloss.Color("#101214")
	cliPanelColor         = lipgloss.Color("#14171A")
	cliPanelAltColor      = lipgloss.Color("#171B1F")
	cliBorderColor        = lipgloss.Color("#2A3038")
	cliBorderSoftColor    = lipgloss.Color("#23282F")
	cliTextColor          = lipgloss.Color("#ECE7DA")
	cliMutedColor         = lipgloss.Color("#8D939B")
	cliAccentColor        = lipgloss.Color("#D4B06A")
	cliUserColor          = lipgloss.Color("#9DB3D1")
	cliAssistantColor     = lipgloss.Color("#93C49D")
	cliSystemColor        = lipgloss.Color("#D8BE7D")
	cliDangerColor        = lipgloss.Color("#E07B72")
	cliNeutralBadgeColor  = lipgloss.Color("#1B1F24")
	cliMutedBadgeColor    = lipgloss.Color("#20252B")
	cliInputSurfaceColor  = lipgloss.Color("#111417")
	cliTopBarColor        = lipgloss.Color("#121418")
	cliHighlightSoftColor = lipgloss.Color("#1A1F25")

	cliAppStyle = lipgloss.NewStyle().
			Padding(0, 1).
			Background(cliSurfaceColor).
			Foreground(cliTextColor)
	cliTopBarStyle = lipgloss.NewStyle().
			Background(cliTopBarColor).
			Foreground(cliTextColor).
			Padding(0, 1)
	cliTitleBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cliSurfaceColor).
				Background(cliAccentColor).
				Padding(0, 1)
	cliHeaderTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cliTextColor)
	cliHeaderSubtitleStyle = lipgloss.NewStyle().
				Foreground(cliMutedColor)
	cliMutedStyle = lipgloss.NewStyle().
			Foreground(cliMutedColor)
	cliMetaBadgeStyle = lipgloss.NewStyle().
				Foreground(cliTextColor).
				Background(cliMutedBadgeColor).
				Padding(0, 1)
	cliMetaKeyStyle = lipgloss.NewStyle().
			Foreground(cliMutedColor)
	cliPanelStyle = lipgloss.NewStyle().
			Background(cliPanelColor).
			Border(lipgloss.NormalBorder()).
			BorderForeground(cliBorderColor).
			Padding(0, 1)
	cliPanelHeaderTitleStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(cliTextColor)
	cliPanelHeaderMetaStyle = lipgloss.NewStyle().
				Foreground(cliMutedColor)
	cliUserChipStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cliSurfaceColor).
				Background(cliUserColor).
				Padding(0, 1)
	cliAssistantChipStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cliSurfaceColor).
				Background(cliAssistantColor).
				Padding(0, 1)
	cliSystemChipStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cliSurfaceColor).
				Background(cliSystemColor).
				Padding(0, 1)
	cliRunningStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cliAccentColor)
	cliIdleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cliAssistantColor)
	cliErrorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cliDangerColor)
	cliInputPanelStyle = lipgloss.NewStyle().
				Background(cliInputSurfaceColor).
				Border(lipgloss.NormalBorder()).
				BorderForeground(cliBorderSoftColor).
				Padding(0, 1)
	cliHintStyle = lipgloss.NewStyle().
			Foreground(cliMutedColor)
)

const (
	cliMinWidth        = 84
	cliMinHeight       = 18
	cliMaxMessages     = 80
	cliMaxActivities   = 160
	cliMaxPreviewRune  = 180
	cliHeaderHeight    = 3
	cliPanelHeaderRows = 1
	cliBodyGap         = 1
	cliChatMinHeight   = 8
	cliTraceMinHeight  = 7
	cliTraceMaxHeight  = 12
	cliTraceCollapsed  = 4
	cliComposeMinRows  = 1
	cliComposeMaxRows  = 4
)

// CLIChannel implements the Channel interface for terminal interaction.
type CLIChannel struct {
	inbound      chan gateway.InboundMessage
	status       gateway.ChannelStatus
	cancel       context.CancelFunc
	program      *tea.Program
	projectRoot  string
	entryAgentID string
	headless     bool
	mu           sync.Mutex
	once         sync.Once
	shutdown     chan struct{}
}

// InjectMessage enqueues a synthetic inbound message for the headless CLI
// channel so startup-path tests can exercise the real gateway/chat flow.
func (c *CLIChannel) InjectMessage(msg gateway.InboundMessage) error {
	c.mu.Lock()
	headless := c.headless
	status := c.status
	inbound := c.inbound
	c.mu.Unlock()

	if !headless {
		return fmt.Errorf("cli injection is only available in headless mode")
	}
	if status != gateway.StatusConnected {
		return fmt.Errorf("cli channel is not connected")
	}

	if strings.TrimSpace(msg.Channel) == "" {
		msg.Channel = "cli"
	}
	if strings.TrimSpace(msg.SenderID) == "" {
		msg.SenderID = "local"
	}
	if strings.TrimSpace(msg.SenderName) == "" {
		msg.SenderName = "User"
	}
	if msg.ChatType == "" {
		msg.ChatType = gateway.ChatTypeDirect
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	defer func() {
		if recover() != nil {
			status = gateway.StatusDisconnected
		}
	}()
	if status != gateway.StatusConnected {
		return fmt.Errorf("cli channel is disconnected")
	}
	inbound <- msg
	return nil
}

// InjectText is a convenience wrapper around InjectMessage that targets the
// default local sender.
func (c *CLIChannel) InjectText(text string) error {
	return c.InjectMessage(gateway.InboundMessage{Text: text})
}

type cliAssistantMessageMsg struct {
	Text string
}

type cliRunEventMsg struct {
	Event gateway.RunEvent
}

type cliQuitMsg struct{}

type cliConversationEntry struct {
	kind         string
	role         string
	text         string
	summary      string
	at           time.Time
	pending      bool
	tool         string
	toolCallID   string
	input        string
	output       string
	error        string
	isError      bool
	plan         string
	todoID       string
	todoContent  string
	todoStatus   string
	todoCreated  int64
	todoFinished int64
}

type cliActivityEntry struct {
	title     string
	detail    string
	tone      string
	at        time.Time
	depth     int
	runID     string
	eventName string
}

type cliCallFrame struct {
	id     string
	kind   string
	label  string
	detail string
	status string
	error  string
	at     time.Time
}

type cliTraceNode struct {
	runID            string
	agentID          string
	sessionID        string
	parentRunID      string
	parentAgentID    string
	status           string
	preview          string
	startedAt        time.Time
	completedAt      time.Time
	lastEventName    string
	lastEventSummary string
	lastEventAt      time.Time
	eventCount       int
	textDeltaCount   int
	activeToolCalls  map[string]string
	activeAgentCalls map[string]string
	backgroundTasks  map[string]cliBackgroundTask
	agentCallsDone   int
	agentCallsFailed int
	errorMessage     string
	callFrames       []cliCallFrame
}

type cliBackgroundTask struct {
	agentID string
	status  string
	summary string
	err     string
	at      time.Time
}

type cliRuntimeSummary struct {
	State           string
	RunsLive        int
	RunsDone        int
	RunsFailed      int
	SubagentsActive int
	SubagentsDone   int
	SubagentsFailed int
	EventsVisible   int
}

type cliModel struct {
	projectRoot        string
	entryAgentID       string
	sessionStore       *runtimesession.Store
	input              textarea.Model
	spinner            spinner.Model
	chatView           viewport.Model
	traceView          viewport.Model
	width              int
	height             int
	ready              bool
	panelWidth         int
	chatHeight         int
	traceHeight        int
	statusEntry        cliConversationEntry
	sessionMessages    []cliConversationEntry
	pendingMessages    []cliConversationEntry
	activeSessionAgent string
	activeSessionID    string
	pendingBaseline    int
	messages           []cliConversationEntry
	activities         []cliActivityEntry
	traceNodes         map[string]*cliTraceNode
	sendInput          func(string)
	lastTreeRunID      string
	inspectorCollapsed bool
	selectedTraceRunID string
	callStackExpanded  bool
	// Input history for up/down navigation
	inputHistory []string
	historyIndex int // -1 means at current input, >=0 means navigating history
}

// NewCLIChannel creates a new CLI channel adapter backed by Bubble Tea.
func NewCLIChannel(projectRoot, entryAgentID string) *CLIChannel {
	return newCLIChannel(projectRoot, entryAgentID, false)
}

// NewHeadlessCLIChannel creates a non-interactive CLI channel, primarily for
// startup-path tests that should not launch a real TUI.
func NewHeadlessCLIChannel(projectRoot, entryAgentID string) *CLIChannel {
	return newCLIChannel(projectRoot, entryAgentID, true)
}

func newCLIChannel(projectRoot, entryAgentID string, headless bool) *CLIChannel {
	return &CLIChannel{
		inbound:      make(chan gateway.InboundMessage, 10),
		status:       gateway.StatusDisconnected,
		projectRoot:  projectRoot,
		entryAgentID: entryAgentID,
		headless:     headless,
		shutdown:     make(chan struct{}),
	}
}

func (c *CLIChannel) Name() string { return "cli" }

func (c *CLIChannel) Connect(ctx context.Context) error {
	c.mu.Lock()
	c.status = gateway.StatusConnecting
	ctx, c.cancel = context.WithCancel(ctx)
	if c.headless {
		c.status = gateway.StatusConnected
		c.mu.Unlock()

		go func() {
			<-ctx.Done()
			c.shutdownInternal()
		}()

		return nil
	}
	model := newCLIModel(c.projectRoot, c.entryAgentID, c.enqueueInput)
	program := tea.NewProgram(model, tea.WithAltScreen())
	c.program = program
	c.status = gateway.StatusConnected
	c.mu.Unlock()

	go func() {
		defer c.shutdownInternal()
		if _, err := program.Run(); err != nil {
			fmt.Printf("AnyAI CLI exited with error: %v\n", err)
		}
	}()

	go func() {
		<-ctx.Done()
		c.requestQuit()
	}()

	return nil
}

func (c *CLIChannel) Disconnect() error {
	c.requestQuit()
	return nil
}

func (c *CLIChannel) Send(_ context.Context, msg gateway.OutboundMessage) error {
	c.dispatch(cliAssistantMessageMsg{Text: msg.Text})
	return nil
}

func (c *CLIChannel) HandleRunEvent(_ context.Context, event gateway.RunEvent) error {
	c.dispatch(cliRunEventMsg{Event: event})
	return nil
}

func (c *CLIChannel) Receive() <-chan gateway.InboundMessage {
	return c.inbound
}

func (c *CLIChannel) Status() gateway.ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Done is closed when the CLI channel fully shuts down.
func (c *CLIChannel) Done() <-chan struct{} {
	return c.shutdown
}

func (c *CLIChannel) enqueueInput(text string) {
	c.inbound <- gateway.InboundMessage{
		Channel:    "cli",
		ChatType:   gateway.ChatTypeDirect,
		SenderID:   "local",
		SenderName: "User",
		Text:       text,
		Blocks:     ParseCLIReferences(text),
		Timestamp:  time.Now(),
	}
}

func (c *CLIChannel) dispatch(msg tea.Msg) {
	c.mu.Lock()
	program := c.program
	status := c.status
	c.mu.Unlock()
	if program == nil || status == gateway.StatusDisconnected {
		return
	}
	program.Send(msg)
}

func (c *CLIChannel) requestQuit() {
	if c.headless {
		c.shutdownInternal()
		return
	}
	c.dispatch(cliQuitMsg{})
}

func (c *CLIChannel) shutdownInternal() {
	c.once.Do(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.status = gateway.StatusDisconnected
		if c.cancel != nil {
			c.cancel()
		}
		close(c.inbound)
		close(c.shutdown)
	})
}

func newCLIModel(projectRoot, entryAgentID string, sendInput func(string)) *cliModel {
	in := textarea.New()
	// in.Placeholder = "输入自然语言，支持 @file /path、@dir /path、@url https://..."
	in.Prompt = "› "
	in.ShowLineNumbers = false
	in.SetHeight(cliComposeMinRows)
	in.MaxHeight = cliComposeMaxRows
	in.CharLimit = 0
	in.SetWidth(60)
	in.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j"),
		key.WithHelp("ctrl+j", "insert newline"),
	)
	_ = in.Focus()
	in.FocusedStyle.Base = lipgloss.NewStyle()
	in.FocusedStyle.Prompt = lipgloss.NewStyle().Bold(true).Foreground(cliAccentColor)
	in.FocusedStyle.Text = lipgloss.NewStyle().Foreground(cliTextColor)
	in.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(cliMutedColor)
	in.FocusedStyle.CursorLine = lipgloss.NewStyle()
	in.FocusedStyle.EndOfBuffer = lipgloss.NewStyle().Foreground(cliMutedColor)
	in.BlurredStyle = in.FocusedStyle
	in.Cursor.Style = lipgloss.NewStyle().Foreground(cliAccentColor)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = cliRunningStyle

	model := &cliModel{
		projectRoot:        projectRoot,
		entryAgentID:       entryAgentID,
		sessionStore:       newCLISessionStore(projectRoot),
		input:              in,
		spinner:            sp,
		traceNodes:         make(map[string]*cliTraceNode),
		sendInput:          sendInput,
		inspectorCollapsed: true,
		statusEntry: cliConversationEntry{
			// This is a UI-only status card. The actual LLM system prompt is
			// assembled inside the shared agent runtime.
			kind: "status",
			role: "status",
			text: "AnyAI CLI 已就绪。Enter 发送，Ctrl+J 换行，Alt+↑↓ 历史，/quit 退出。",
			at:   time.Now(),
		},
		historyIndex: -1,
	}
	model.rebuildConversation()
	return model
}

func newCLISessionStore(projectRoot string) *runtimesession.Store {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return nil
	}
	return runtimesession.NewStore(filepath.Join(config.ProjectDataDir(root), "sessions"))
}

func summarizeCLIConversationValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return trimForDisplay(strings.TrimSpace(typed), 120)
	case []string:
		return trimForDisplay(strings.Join(typed, ", "), 120)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			text := summarizeCLIConversationValue(item)
			if text != "" {
				parts = append(parts, text)
			}
			if len(parts) >= 3 {
				break
			}
		}
		return trimForDisplay(strings.Join(parts, "  •  "), 120)
	case map[string]any:
		priority := []string{"task", "query", "path", "command", "url", "prompt", "text", "file", "goal"}
		for _, key := range priority {
			if text := summarizeCLIConversationValue(typed[key]); text != "" {
				return text
			}
		}
		for _, value := range typed {
			if text := summarizeCLIConversationValue(value); text != "" {
				return text
			}
		}
		return ""
	default:
		return trimForDisplay(strings.TrimSpace(fmt.Sprintf("%v", value)), 120)
	}
}

func summarizeCLIToolPayload(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return trimForDisplay(strings.TrimSpace(string(raw)), 120)
	}
	return summarizeCLIConversationValue(value)
}

func normalizeCLIPlanLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	checkboxPrefixes := []string{"- [ ] ", "- [x] ", "- [X] ", "* [ ] ", "* [x] ", "* [X] "}
	for _, prefix := range checkboxPrefixes {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	bulletPrefixes := []string{"- ", "* ", "• "}
	for _, prefix := range bulletPrefixes {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	index := 0
	for index < len(line) {
		r := rune(line[index])
		if !unicode.IsDigit(r) {
			break
		}
		index++
	}
	if index > 0 && index < len(line) {
		switch line[index] {
		case '.', ')', ':':
			return strings.TrimSpace(line[index+1:])
		}
	}
	return line
}

func summarizeCLIPlanText(plan string) string {
	lines := strings.Split(strings.ReplaceAll(plan, "\r\n", "\n"), "\n")
	steps := make([]string, 0, len(lines))
	for _, line := range lines {
		if normalized := normalizeCLIPlanLine(line); normalized != "" {
			steps = append(steps, normalized)
		}
	}
	if len(steps) == 0 {
		return "进入规划阶段。"
	}
	current := trimForDisplay(steps[0], 88)
	if len(steps) == 1 {
		return "当前阶段：" + current
	}
	return fmt.Sprintf("当前阶段：%s；后续 %d 步待推进。", current, len(steps)-1)
}

func summarizeCLITodoUpdate(status, content string) string {
	label := "待处理"
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done":
		label = "已完成"
	case "in_progress", "in-progress", "running", "doing", "active":
		label = "进行中"
	case "blocked", "paused", "waiting":
		label = "暂时阻塞"
	case "cancelled", "canceled":
		label = "已取消"
	}
	task := trimForDisplay(nonEmptyCLI(content, "事项"), 88)
	return label + "：" + task
}

func cliConversationEntriesFromSessionEvents(events []runtimeevents.EventRecord) []cliConversationEntry {
	items := make([]cliConversationEntry, 0, len(events))
	toolSlots := make(map[string]int)

	for _, event := range events {
		at := event.Timestamp
		if at.IsZero() {
			at = time.Now()
		}
		switch strings.TrimSpace(event.Name) {
		case runtimeevents.EventSessionInputStored:
			text := strings.TrimSpace(payloadString(event.Payload, "text"))
			if text == "" {
				continue
			}
			items = append(items, cliConversationEntry{
				kind: "message",
				role: "user",
				text: text,
				at:   at,
			})
		case runtimeevents.EventSessionOutputStored:
			text := strings.TrimSpace(payloadString(event.Payload, "text"))
			if text == "" {
				continue
			}
			items = append(items, cliConversationEntry{
				kind: "message",
				role: "assistant",
				text: cliConversationAgentText(event.AgentID, text),
				at:   at,
			})
		case runtimeevents.EventAgentCallCompleted, runtimeevents.EventAgentCallFailed:
			target := nonEmptyCLI(payloadString(event.Payload, "target_agent"), payloadString(event.Payload, "agent"))
			text := nonEmptyCLI(payloadString(event.Payload, "summary"), payloadString(event.Payload, "error"))
			if strings.TrimSpace(target) == "" || strings.TrimSpace(text) == "" {
				continue
			}
			items = append(items, cliConversationEntry{
				kind: "message",
				role: "assistant",
				text: cliConversationAgentText(target, text),
				at:   at,
			})
		case runtimeevents.EventToolCallStarted, runtimeevents.EventToolCompleted, runtimeevents.EventToolFailed:
			toolName := strings.TrimSpace(runtimeevents.ToolName(event))
			if shouldHideCLIConversationTool(toolName) {
				continue
			}
			callID := strings.TrimSpace(payloadString(event.Payload, "id"))
			key := cliConversationToolKey(event.RunID, event.AgentID, event.SessionID, callID, toolName)
			status := "running"
			if event.Name == runtimeevents.EventToolCompleted {
				status = "success"
			}
			if event.Name == runtimeevents.EventToolFailed || strings.TrimSpace(payloadString(event.Payload, "error")) != "" {
				status = "failed"
			}
			label := cliConversationToolLabel(event.AgentID, toolName, status)
			summary := summarizeCLIConversationValue(event.Payload["input"])
			if idx, ok := toolSlots[key]; ok && idx >= 0 && idx < len(items) {
				items[idx].tool = label
				if items[idx].input == "" {
					items[idx].input = summary
				}
				continue
			}
			items = append(items, cliConversationEntry{
				kind:       "tool_call",
				role:       "assistant",
				tool:       label,
				toolCallID: callID,
				input:      summary,
				at:         at,
			})
			toolSlots[key] = len(items) - 1
		}
	}
	return items
}

func cliConversationAgentText(agentID, text string) string {
	text = strings.TrimSpace(text)
	agentID = strings.TrimSpace(agentID)
	if text == "" || agentID == "" {
		return text
	}
	return "[" + agentID + "] " + text
}

func cliConversationToolKey(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, ":")
}

func cliConversationToolLabel(agentID, toolName, status string) string {
	toolName = nonEmptyCLI(strings.TrimSpace(toolName), "tool")
	label := toolName
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		label = agentID + " -> " + toolName
	}
	if status = strings.TrimSpace(status); status != "" {
		label += " [" + status + "]"
	}
	return label
}

func shouldHideCLIConversationTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "", "goal_complete", "await_user_input":
		return true
	default:
		return false
	}
}

func (m *cliModel) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), textarea.Blink, m.spinner.Tick)
}

func (m *cliModel) rebuildConversation() {
	m.refreshStatusEntry()

	body := make([]cliConversationEntry, 0, len(m.sessionMessages)+len(m.pendingMessages))
	body = append(body, m.sessionMessages...)
	body = append(body, m.pendingMessages...)

	maxBody := cliMaxMessages
	status := []cliConversationEntry(nil)
	if strings.TrimSpace(m.statusEntry.text) != "" {
		status = append(status, m.statusEntry)
		maxBody--
	}
	if maxBody < 0 {
		maxBody = 0
	}
	if len(body) > maxBody {
		body = append([]cliConversationEntry(nil), body[len(body)-maxBody:]...)
	}

	m.messages = append(body, status...)
}

func (m *cliModel) refreshStatusEntry() {
	summary := m.runtimeSummary()
	m.statusEntry = cliConversationEntry{
		kind: "status",
		role: "status",
		text: m.formatRuntimeSummary(summary, "   "),
		at:   time.Now(),
	}
}

func (m *cliModel) appendPendingConversation(entry cliConversationEntry) {
	if strings.TrimSpace(entry.kind) == "" {
		entry.kind = "message"
	}
	if strings.TrimSpace(entry.kind) == "message" && strings.TrimSpace(entry.text) == "" {
		return
	}
	if entry.at.IsZero() {
		entry.at = time.Now()
	}
	entry.pending = true
	if len(m.pendingMessages) == 0 {
		m.pendingBaseline = len(m.sessionMessages)
	}
	m.pendingMessages = append(m.pendingMessages, entry)
	m.rebuildConversation()
}

func (m *cliModel) setActiveSession(agentID, sessionID string) {
	agentID = strings.TrimSpace(agentID)
	sessionID = strings.TrimSpace(sessionID)
	if agentID == "" || sessionID == "" {
		return
	}
	if m.activeSessionAgent == agentID && m.activeSessionID == sessionID {
		return
	}
	resetPending := m.activeSessionID != "" && (m.activeSessionAgent != agentID || m.activeSessionID != sessionID)
	m.activeSessionAgent = agentID
	m.activeSessionID = sessionID
	m.sessionMessages = nil
	if resetPending {
		m.pendingMessages = nil
		m.pendingBaseline = 0
	}
}

func (m *cliModel) reconcilePendingConversation() {
	if len(m.pendingMessages) == 0 || len(m.sessionMessages) == 0 {
		return
	}
	start := m.pendingBaseline
	if start < 0 {
		start = 0
	}
	if start > len(m.sessionMessages) {
		start = len(m.sessionMessages)
	}
	recent := m.sessionMessages[start:]
	if len(recent) == 0 {
		return
	}

	used := make([]bool, len(recent))
	filtered := make([]cliConversationEntry, 0, len(m.pendingMessages))
	for _, pending := range m.pendingMessages {
		kind := strings.TrimSpace(pending.kind)
		role := strings.TrimSpace(pending.role)
		text := normalizeCLIConversationMatchText(role, pending.text)
		matched := false
		for idx, persisted := range recent {
			if used[idx] {
				continue
			}
			if strings.TrimSpace(persisted.kind) != kind {
				continue
			}
			if strings.TrimSpace(persisted.role) != role {
				continue
			}
			if normalizeCLIConversationMatchText(role, persisted.text) != text {
				continue
			}
			used[idx] = true
			matched = true
			break
		}
		if !matched {
			filtered = append(filtered, pending)
		}
	}
	m.pendingMessages = filtered
}

func (m *cliModel) syncActiveSessionConversation() {
	if m == nil || m.sessionStore == nil {
		return
	}
	agentID := strings.TrimSpace(m.activeSessionAgent)
	sessionID := strings.TrimSpace(m.activeSessionID)
	if agentID == "" || sessionID == "" {
		return
	}
	sess, err := m.sessionStore.Load(agentID, sessionID)
	if err != nil {
		return
	}
	events := runtimesessionevents.HistoryEventRecords(agentID, sessionID, sess.History())
	m.sessionMessages = cliConversationEntriesFromSessionEvents(events)
	m.reconcilePendingConversation()
	m.rebuildConversation()
}

func (m *cliModel) hasAssistantSessionMessage(text string) bool {
	text = normalizeCLIConversationMatchText("assistant", text)
	if text == "" {
		return false
	}
	start := m.pendingBaseline
	if start < 0 {
		start = 0
	}
	if start > len(m.sessionMessages) {
		start = len(m.sessionMessages)
	}
	for idx := len(m.sessionMessages) - 1; idx >= 0; idx-- {
		if idx < start {
			return false
		}
		entry := m.sessionMessages[idx]
		if strings.TrimSpace(entry.kind) != "message" {
			continue
		}
		if strings.TrimSpace(entry.role) != "assistant" {
			continue
		}
		if normalizeCLIConversationMatchText("assistant", entry.text) == text {
			return true
		}
	}
	return false
}

func normalizeCLIConversationMatchText(role, text string) string {
	text = strings.TrimSpace(text)
	if strings.TrimSpace(role) != "assistant" {
		return text
	}
	if !strings.HasPrefix(text, "[") {
		return text
	}
	closing := strings.Index(text, "]")
	if closing <= 0 || closing+1 >= len(text) {
		return text
	}
	return strings.TrimSpace(text[closing+1:])
}

func shouldSyncCLIConversation(event gateway.RunEvent) bool {
	switch strings.TrimSpace(event.Name) {
	case "", "run.activity", "text.delta", "task.queued", "task.started", "task.running":
		return false
	default:
		return true
	}
}

func (m *cliModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyUp:
			if msg.Alt {
				if len(m.inputHistory) > 0 && m.historyIndex < len(m.inputHistory)-1 {
					m.historyIndex++
					m.input.SetValue(m.inputHistory[len(m.inputHistory)-1-m.historyIndex])
					m.input.CursorEnd()
				}
				return m, nil
			}
		case tea.KeyDown:
			if msg.Alt {
				if m.historyIndex > 0 {
					m.historyIndex--
					m.input.SetValue(m.inputHistory[len(m.inputHistory)-1-m.historyIndex])
					m.input.CursorEnd()
				} else if m.historyIndex == 0 {
					m.historyIndex = -1
					m.input.SetValue("")
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "enter":
			return m.submitInput(cmds)
		case "pgup":
			if m.ready {
				m.chatView.HalfViewUp()
			}
			return m, nil
		case "pgdown":
			if m.ready {
				m.chatView.HalfViewDown()
			}
			return m, nil
		}
	case cliAssistantMessageMsg:
		text := strings.TrimSpace(msg.Text)
		if text != "" {
			m.syncActiveSessionConversation()
			if !m.hasAssistantSessionMessage(text) {
				m.appendPendingConversation(cliConversationEntry{
					kind: "message",
					role: "assistant",
					text: text,
					at:   time.Now(),
				})
			}
		}
		m.refreshViewports()
		return m, nil
	case cliRunEventMsg:
		m.applyRunEvent(msg.Event)
		if strings.TrimSpace(msg.Event.AgentID) == strings.TrimSpace(m.entryAgentID) || strings.TrimSpace(m.activeSessionAgent) == "" {
			m.setActiveSession(msg.Event.AgentID, msg.Event.SessionID)
		}
		if shouldSyncCLIConversation(msg.Event) {
			m.syncActiveSessionConversation()
		}
		m.refreshViewports()
		return m, nil
	case cliQuitMsg:
		return m, tea.Quit
	}

	var cmd tea.Cmd
	oldRows := m.input.LineCount()
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	if m.input.LineCount() != oldRows || m.input.Height() != desiredCLIComposeRows(m.input.LineCount()) {
		if m.width > 0 && m.height > 0 {
			m.resize()
		} else {
			m.syncInputHeight()
		}
	}

	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)

	if m.ready {
		m.chatView, cmd = m.chatView.Update(msg)
		cmds = append(cmds, cmd)
		m.traceView, cmd = m.traceView.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *cliModel) submitInput(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	if text == "/quit" || text == "/exit" {
		return m, tea.Quit
	}
	// Save non-command input to history
	if !strings.HasPrefix(text, "/") {
		m.inputHistory = append(m.inputHistory, text)
		// Keep only last 100 entries
		if len(m.inputHistory) > 100 {
			m.inputHistory = m.inputHistory[len(m.inputHistory)-100:]
		}
		m.historyIndex = -1
	}
	m.appendPendingConversation(cliConversationEntry{
		kind: "message",
		role: "user",
		text: text,
		at:   time.Now(),
	})
	m.input.Reset()
	if m.width > 0 && m.height > 0 {
		m.resize()
	} else {
		m.syncInputHeight()
		m.refreshViewports()
	}
	cmds = append(cmds, func() tea.Msg {
		m.sendInput(text)
		return nil
	})
	return m, tea.Batch(cmds...)
}

func (m *cliModel) View() string {
	content := "AnyAI CLI 正在启动..."
	if !m.ready {
		return m.renderApp(content)
	}

	header := m.renderHeader(m.panelWidth)
	chatMeta := fmt.Sprintf("%d message(s)", len(m.messages))
	body := m.renderPanel("Session Window", chatMeta, m.chatView.View(), m.panelWidth, m.chatHeight)
	inputPanel := m.renderInputPanel(m.panelWidth)
	content = lipgloss.JoinVertical(lipgloss.Left, header, body, inputPanel)

	return m.renderApp(content)
}

func (m *cliModel) renderApp(content string) string {
	return cliAppStyle.Render(content)
}

func (m *cliModel) resize() {
	totalWidth := m.width - cliAppStyle.GetHorizontalFrameSize()
	totalHeight := m.height - cliAppStyle.GetVerticalFrameSize()
	if totalWidth < cliMinWidth {
		totalWidth = cliMinWidth
	}
	if totalHeight < cliMinHeight {
		totalHeight = cliMinHeight
	}

	m.syncInputHeight()
	availableHeight := totalHeight - cliHeaderHeight - cliInputPanelHeight(m.input.Height()) - cliBodyGap
	if availableHeight < cliChatMinHeight {
		availableHeight = cliChatMinHeight
	}
	traceHeight := 0
	chatHeight := availableHeight

	m.panelWidth = totalWidth
	m.chatHeight = chatHeight
	m.traceHeight = traceHeight

	chatInnerWidth := max(10, totalWidth-cliPanelStyle.GetHorizontalFrameSize())
	traceInnerWidth := max(10, totalWidth-cliPanelStyle.GetHorizontalFrameSize())
	chatInnerHeight := max(3, chatHeight-cliPanelStyle.GetVerticalFrameSize()-cliPanelHeaderRows)
	traceInnerHeight := max(3, traceHeight-cliPanelStyle.GetVerticalFrameSize()-cliPanelHeaderRows)

	if !m.ready {
		m.chatView = viewport.New(chatInnerWidth, chatInnerHeight)
		m.traceView = viewport.New(traceInnerWidth, traceInnerHeight)
		m.ready = true
	} else {
		m.chatView.Width = chatInnerWidth
		m.chatView.Height = chatInnerHeight
		m.traceView.Width = traceInnerWidth
		m.traceView.Height = traceInnerHeight
	}

	m.input.SetWidth(max(20, totalWidth-cliInputPanelStyle.GetHorizontalFrameSize()-4))
	m.syncInputHeight()
	m.refreshViewports()
}

func cliInputPanelHeight(inputRows int) int {
	return inputRows + cliInputPanelStyle.GetVerticalFrameSize() + 1
}

func desiredCLIComposeRows(lineCount int) int {
	if lineCount < cliComposeMinRows {
		return cliComposeMinRows
	}
	if lineCount > cliComposeMaxRows {
		return cliComposeMaxRows
	}
	return lineCount
}

func (m *cliModel) syncInputHeight() {
	desiredHeight := desiredCLIComposeRows(m.input.LineCount())
	if m.input.Height() == desiredHeight {
		return
	}

	currentLine := m.input.Line()
	lineInfo := m.input.LineInfo()
	currentColumn := lineInfo.StartColumn + lineInfo.ColumnOffset
	value := m.input.Value()

	m.input.SetHeight(desiredHeight)
	m.resetInputViewport(value, currentLine, currentColumn)
}

func (m *cliModel) resetInputViewport(value string, line, column int) {
	m.input.SetValue(value)

	maxLine := m.input.LineCount() - 1
	if line < 0 {
		line = 0
	}
	if line > maxLine {
		line = maxLine
	}
	for i := 0; i < line; i++ {
		m.input.CursorDown()
	}
	m.input.SetCursor(column)

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(struct{}{})
	_ = cmd
}

func (m *cliModel) renderHeader(width int) string {
	projectLabel := "当前项目"
	if strings.TrimSpace(m.projectRoot) != "" {
		projectLabel = trimForDisplay(filepath.Base(strings.TrimSpace(m.projectRoot)), 24)
	}

	entryAgent := m.entryAgentID
	if entryAgent == "" {
		entryAgent = "default"
	}
	entryAgent = trimForDisplay(entryAgent, 22)

	status := cliIdleStyle.Render("idle")
	if m.runningRunCount() > 0 {
		status = cliRunningStyle.Render(m.spinner.View() + " " + m.runtimeStatusLabel())
	}
	summary := m.runtimeSummary()

	left := lipgloss.JoinHorizontal(lipgloss.Left,
		cliTitleBadgeStyle.Render("AnyAI"),
		" ",
		cliHeaderTitleStyle.Render("session console"),
		" ",
		cliHeaderSubtitleStyle.Render("single-column agent + tool timeline"),
	)
	right := cliHintStyle.Render("PgUp/PgDn page  •  Alt+Up/Down history  •  Esc quit")

	line1 := cliTopBarStyle.Width(width).Render(alignInline(width, left, right))
	line2 := renderHeaderRow(width, []string{
		renderHeaderBadge("project", projectLabel),
		renderHeaderBadge("agent", entryAgent),
		renderHeaderBadge("state", status),
	})
	line3 := renderHeaderRow(width, []string{
		renderHeaderBadge("runs", formatRuntimeRunCounts(summary)),
		renderHeaderBadge("subagents", formatRuntimeSubagentCounts(summary)),
		renderHeaderBadge("events", formatRuntimeEventCount(summary)),
	})

	return lipgloss.JoinVertical(lipgloss.Left, line1, cliMutedStyle.Width(width).Render(line2), cliMutedStyle.Width(width).Render(line3))
}

func (m *cliModel) renderInspectorMeta() string {
	summary := m.runtimeSummary()
	selected := "waiting"
	if node := m.selectedTraceNode(); node != nil {
		selected = trimForDisplay(nonEmptyCLI(node.agentID, node.runID), 18)
	}
	state := "full"
	if m.inspectorCollapsed {
		state = "split"
	}
	return fmt.Sprintf("%s  state %s  %d live  %d done  %d failed  %d sub active  %d sub done  %d sub failed  selected %s",
		state, summary.State, summary.RunsLive, summary.RunsDone, summary.RunsFailed, summary.SubagentsActive, summary.SubagentsDone, summary.SubagentsFailed, selected)
}

func (m *cliModel) renderPanel(title, meta, content string, width, height int) string {
	innerWidth := max(10, width-cliPanelStyle.GetHorizontalFrameSize())
	header := alignInline(innerWidth,
		cliPanelHeaderTitleStyle.Render(title),
		cliPanelHeaderMetaStyle.Render(meta),
	)
	panelContent := lipgloss.JoinVertical(
		lipgloss.Left,
		lipgloss.NewStyle().Width(innerWidth).Render(header),
		content,
	)
	return cliPanelStyle.Width(width).Height(height).Render(panelContent)
}

func (m *cliModel) renderInputPanel(width int) string {
	innerWidth := max(10, width-cliInputPanelStyle.GetHorizontalFrameSize())
	header := alignInline(
		innerWidth,
		cliPanelHeaderTitleStyle.Render("Compose"),
		cliPanelHeaderMetaStyle.Render("Enter send  •  Ctrl+J newline  •  /quit exit"),
	)
	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Width(innerWidth).Render(header),
		m.input.View(),
	)
	return cliInputPanelStyle.Width(width).Render(content)
}

func (m *cliModel) refreshViewports() {
	if !m.ready {
		return
	}
	stickToBottom := m.chatView.AtBottom()
	m.chatView.SetContent(m.renderConversation())
	if stickToBottom {
		m.chatView.GotoBottom()
	}
}

func renderCLIConversationBadge(entry cliConversationEntry) string {
	kind := strings.TrimSpace(entry.kind)
	switch kind {
	case "status":
		return cliSystemChipStyle.Render("STATUS")
	case "message":
		switch strings.TrimSpace(entry.role) {
		case "user":
			return cliUserChipStyle.Render("USER")
		case "assistant":
			return cliAssistantChipStyle.Render("ASSISTANT")
		default:
			return cliSystemChipStyle.Render("SYSTEM")
		}
	case "meta":
		return cliSystemChipStyle.Render("META")
	case "plan":
		return cliSystemChipStyle.Render("PHASE")
	case "todo":
		return cliUserChipStyle.Render("PROGRESS")
	case "tool_call":
		return cliHeaderTitleStyle.Render("TOOL CALL")
	case "tool_result":
		if entry.isError || strings.TrimSpace(entry.error) != "" {
			return cliErrorStyle.Render("TOOL ERROR")
		}
		return cliIdleStyle.Render("TOOL RESULT")
	default:
		return cliSystemChipStyle.Render(strings.ToUpper(nonEmptyCLI(kind, "ENTRY")))
	}
}

func cliConversationCardStyle(entry cliConversationEntry) lipgloss.Style {
	if strings.TrimSpace(entry.kind) == "message" || strings.TrimSpace(entry.kind) == "status" {
		return conversationCardStyle(entry.role)
	}
	style := lipgloss.NewStyle().
		Padding(0, 1).
		Foreground(cliTextColor).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		Background(cliPanelAltColor)
	switch strings.TrimSpace(entry.kind) {
	case "tool_result":
		if entry.isError || strings.TrimSpace(entry.error) != "" {
			return style.BorderForeground(cliDangerColor)
		}
		return style.BorderForeground(cliAssistantColor)
	case "tool_call":
		return style.BorderForeground(cliAccentColor)
	case "plan":
		return style.BorderForeground(cliSystemColor)
	case "todo":
		if strings.EqualFold(strings.TrimSpace(entry.todoStatus), "completed") {
			return style.BorderForeground(cliAssistantColor)
		}
		return style.BorderForeground(cliUserColor)
	case "meta":
		return style.BorderForeground(cliSystemColor)
	default:
		return style.BorderForeground(cliBorderColor)
	}
}

func renderCLIConversationBlock(label, body string, width int, tone lipgloss.Style) string {
	label = strings.TrimSpace(label)
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	lines := make([]string, 0, 2)
	if label != "" {
		lines = append(lines, tone.Render(label))
	}
	if body != "" {
		lines = append(lines, cliMutedStyle.Width(width).Render(wrapCLIText(body, width)))
	}
	return strings.Join(lines, "\n")
}

func renderCLIConversationEntry(entry cliConversationEntry, width int) string {
	badge := renderCLIConversationBadge(entry)
	cardStyle := cliConversationCardStyle(entry)
	innerWidth := max(8, width-cardStyle.GetHorizontalFrameSize())

	header := lipgloss.JoinHorizontal(
		lipgloss.Left,
		badge,
		" ",
		cliMutedStyle.Render(entry.at.Format("15:04:05")),
	)
	if entry.pending {
		header = lipgloss.JoinHorizontal(
			lipgloss.Left,
			header,
			" ",
			cliMetaBadgeStyle.Render("pending"),
		)
	}

	lines := []string{header}
	kind := strings.TrimSpace(entry.kind)
	switch kind {
	case "", "message", "status", "meta":
		body := lipgloss.NewStyle().
			Foreground(cliTextColor).
			Width(innerWidth).
			Render(wrapCLIText(strings.TrimSpace(entry.text), innerWidth))
		lines = append(lines, body)
	case "plan":
		summary := summarizeCLIPlanText(entry.plan)
		body := lipgloss.NewStyle().
			Foreground(cliTextColor).
			Width(innerWidth).
			Render(wrapCLIText(summary, innerWidth))
		lines = append(lines, body)
	case "todo":
		summary := summarizeCLITodoUpdate(entry.todoStatus, nonEmptyCLI(entry.todoContent, entry.todoID, "事项"))
		body := lipgloss.NewStyle().
			Foreground(cliTextColor).
			Width(innerWidth).
			Render(wrapCLIText(summary, innerWidth))
		lines = append(lines, body)
	case "tool_call":
		title := nonEmptyCLI(entry.tool, "tool")
		lines = append(lines, cliHeaderTitleStyle.Width(innerWidth).Render(title))
		if block := renderCLIConversationBlock("summary", entry.input, innerWidth, cliMutedStyle); block != "" {
			lines = append(lines, block)
		}
	case "tool_result":
		title := nonEmptyCLI(entry.tool, "tool")
		lines = append(lines, cliHeaderTitleStyle.Width(innerWidth).Render(title))
		if block := renderCLIConversationBlock("summary", entry.output, innerWidth, cliMutedStyle); block != "" {
			lines = append(lines, block)
		}
		if block := renderCLIConversationBlock("reason", entry.error, innerWidth, cliErrorStyle); block != "" {
			lines = append(lines, block)
		}
	default:
		body := lipgloss.NewStyle().
			Foreground(cliTextColor).
			Width(innerWidth).
			Render(wrapCLIText(strings.TrimSpace(entry.text), innerWidth))
		lines = append(lines, body)
	}

	return cardStyle.Width(width).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

func (m *cliModel) renderConversation() string {
	width := max(16, m.chatView.Width)
	if len(m.messages) == 0 {
		return cliMutedStyle.Width(width).Render("等待输入...")
	}

	entries := make([]string, 0, len(m.messages))
	for _, item := range m.messages {
		entries = append(entries, renderCLIConversationEntry(item, width))
	}

	return strings.Join(entries, "\n\n")
}

func (m *cliModel) renderTrace() string {
	width := max(16, m.traceView.Width)
	var sections []string

	sections = append(sections, m.renderInspectorHero(width))
	sections = append(sections, "")
	sections = append(sections, cliPanelHeaderTitleStyle.Render("Latest Call Stack"))
	sections = append(sections, m.renderLatestCallStack(width))
	sections = append(sections, "")
	sections = append(sections, cliPanelHeaderTitleStyle.Render("Selected Node"))
	sections = append(sections, m.renderSelectedTracePanel(width))

	sections = append(sections, "")
	sections = append(sections, cliPanelHeaderTitleStyle.Render("RunTree"))
	roots := m.sortedTraceRoots()
	if len(roots) > 0 {
		var rows []string
		for _, root := range roots {
			rows = m.appendTraceNodeRows(rows, root, 0, width)
		}
		sections = append(sections, rows...)
	} else {
		sections = append(sections, cliMutedStyle.Width(width).Render("当前还没有运行树节点。"))
	}

	sections = append(sections, "")
	sections = append(sections, cliPanelHeaderTitleStyle.Render("Selected Timeline"))
	selectedTimeline := m.renderSelectedTraceTimeline(width)
	if strings.TrimSpace(selectedTimeline) != "" {
		sections = append(sections, selectedTimeline)
	} else {
		sections = append(sections, cliMutedStyle.Width(width).Render("当前所选节点还没有可展示的关键事件。"))
	}

	sections = append(sections, "")
	sections = append(sections, cliPanelHeaderTitleStyle.Render("Global Activity"))
	if len(m.activities) == 0 {
		sections = append(sections, cliMutedStyle.Width(width).Render("等待运行事件..."))
		return strings.Join(sections, "\n\n")
	}

	start := 0
	if len(m.activities) > 14 {
		start = len(m.activities) - 14
	}
	for _, item := range m.activities[start:] {
		sections = append(sections, renderTimelineEntry(item, width))
	}

	return strings.Join(sections, "\n\n")
}

func (m *cliModel) renderInspectorHero(width int) string {
	lines := []string{m.renderTraceSummary(width)}

	if focus := m.focusTraceNode(); focus != nil {
		agentLabel := nonEmptyCLI(focus.agentID, "agent")
		summary := trimForDisplay(cliTracePhaseDetail(focus), 120)
		lines = append(lines,
			cliHeaderTitleStyle.Render("Current Focus")+" "+renderCLITraceStatus(focus.status)+" "+renderCLITracePhase(focus),
			cliMutedStyle.Width(width).Render(wrapCLIText(agentLabel+"  •  "+summary, width)),
		)
	}

	if selected := m.selectedTraceNode(); selected != nil {
		summary := trimForDisplay(cliTracePhaseDetail(selected), 120)
		lines = append(lines,
			cliHeaderTitleStyle.Render("Selected RunTree Node")+" "+renderCLITraceStatus(selected.status)+" "+renderCLITracePhase(selected),
			cliMutedStyle.Width(width).Render(wrapCLIText(nonEmptyCLI(selected.agentID, "agent")+"  •  "+summary, width)),
		)
	}

	if failure := m.latestFailureActivity(); failure != nil {
		lines = append(lines,
			cliErrorStyle.Render("Attention"),
			cliMutedStyle.Width(width).Render(wrapCLIText(trimForDisplay(failure.detail, 180), width)),
		)
	}

	return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n"))
}

func (m *cliModel) focusTraceNode() *cliTraceNode {
	var selected *cliTraceNode
	for _, node := range m.traceNodes {
		if node == nil {
			continue
		}
		if selected == nil || m.preferCLITraceNode(node, selected, m.lastTreeRunID) {
			selected = node
		}
	}
	return selected
}

func (m *cliModel) selectedTraceNode() *cliTraceNode {
	if strings.TrimSpace(m.selectedTraceRunID) != "" {
		if node := m.traceNodes[m.selectedTraceRunID]; node != nil {
			return node
		}
	}
	node := m.focusTraceNode()
	if node != nil {
		m.selectedTraceRunID = node.runID
	}
	return node
}

func (m *cliModel) latestFailureActivity() *cliActivityEntry {
	for i := len(m.activities) - 1; i >= 0; i-- {
		item := &m.activities[i]
		if item == nil {
			continue
		}
		if item.tone == "error" {
			return item
		}
		lower := strings.ToLower(item.title + " " + item.detail)
		if strings.Contains(lower, "failed") || strings.Contains(lower, "error") {
			return item
		}
	}
	return nil
}

func (m *cliModel) toggleInspector() {
	m.inspectorCollapsed = !m.inspectorCollapsed
	if m.width > 0 && m.height > 0 {
		m.resize()
	}
}

func (m *cliModel) toggleCallStack() {
	m.callStackExpanded = !m.callStackExpanded
}

func (m *cliModel) moveTraceSelection(delta int) {
	if delta == 0 {
		return
	}
	nodes := m.flattenTraceNodes()
	if len(nodes) == 0 {
		return
	}
	current := m.selectedTraceNode()
	index := 0
	if current != nil {
		for i, node := range nodes {
			if node != nil && node.runID == current.runID {
				index = i
				break
			}
		}
	}
	index += delta
	if index < 0 {
		index = 0
	}
	if index >= len(nodes) {
		index = len(nodes) - 1
	}
	if nodes[index] != nil {
		m.selectedTraceRunID = nodes[index].runID
	}
}

func (m *cliModel) flattenTraceNodes() []*cliTraceNode {
	var flat []*cliTraceNode
	var visit func(nodes []*cliTraceNode)
	visit = func(nodes []*cliTraceNode) {
		for _, node := range nodes {
			if node == nil {
				continue
			}
			flat = append(flat, node)
			visit(m.childTraceNodes(node.runID))
		}
	}
	visit(m.sortedTraceRoots())
	return flat
}

func (m *cliModel) renderSelectedTracePanel(width int) string {
	node := m.selectedTraceNode()
	if node == nil {
		return cliMutedStyle.Width(width).Render("等待可用节点。")
	}

	lines := []string{
		cliHeaderTitleStyle.Render(nonEmptyCLI(node.agentID, "agent")) + " " + renderCLITraceStatus(node.status) + " " + renderCLITracePhase(node),
		cliMutedStyle.Width(width).Render(wrapCLIText(trimForDisplay(cliTracePhaseDetail(node), 180), width)),
	}
	lines = append(lines, cliMutedStyle.Width(width).Render("state "+nonEmptyCLI(node.status, "queued")+"  •  phase "+cliTracePhaseLabel(node)))

	meta := []string{"run " + shortID(node.runID)}
	if strings.TrimSpace(node.sessionID) != "" {
		meta = append(meta, "session "+shortID(node.sessionID))
	}
	if node.eventCount > 0 {
		meta = append(meta, fmt.Sprintf("%d event(s)", node.eventCount))
	}
	lines = append(lines, cliMutedStyle.Width(width).Render(strings.Join(meta, "  •  ")))
	if last := strings.TrimSpace(node.lastEventSummary); last != "" && last != cliTracePhaseDetail(node) {
		lines = append(lines, cliMutedStyle.Width(width).Render(wrapCLIText("last event: "+trimForDisplay(last, 180), width)))
	}

	if strings.TrimSpace(node.parentRunID) == "" {
		lines = append(lines, cliMutedStyle.Width(width).Render("entry node"))
	} else {
		parent := nonEmptyCLI(node.parentAgentID, shortID(node.parentRunID))
		lines = append(lines, cliMutedStyle.Width(width).Render("from "+parent))
	}
	if footprint := cliTraceAgentCallFootprint(node); footprint != "" {
		lines = append(lines, cliMutedStyle.Width(width).Render("subagents: "+footprint))
	}
	if tools := cliTraceToolFootprint(node); tools != "" {
		lines = append(lines, cliMutedStyle.Width(width).Render("tools: "+tools))
	}

	if preview := strings.TrimSpace(node.preview); preview != "" {
		lines = append(lines, cliMutedStyle.Width(width).Render(wrapCLIText("output: "+preview, width)))
	}

	if !node.startedAt.IsZero() {
		lines = append(lines, cliMutedStyle.Width(width).Render("started "+node.startedAt.Format("15:04:05")))
	}
	if !node.completedAt.IsZero() {
		lines = append(lines, cliMutedStyle.Width(width).Render("completed "+node.completedAt.Format("15:04:05")))
	}

	lines = append(lines, cliHintStyle.Width(width).Render("Up/Down 可切换 RunTree 节点焦点。F3 可切换最新调用栈。"))
	return strings.Join(lines, "\n")
}

func (m *cliModel) renderSelectedTraceTimeline(width int) string {
	node := m.selectedTraceNode()
	if node == nil {
		return ""
	}
	var related []cliActivityEntry
	for i := len(m.activities) - 1; i >= 0; i-- {
		item := m.activities[i]
		if item.runID == node.runID {
			related = append([]cliActivityEntry{item}, related...)
		}
	}
	if len(related) == 0 {
		return ""
	}
	if len(related) > 6 {
		related = related[len(related)-6:]
	}
	rows := make([]string, 0, len(related))
	for _, item := range related {
		rows = append(rows, renderTimelineEntry(item, width))
	}
	return strings.Join(rows, "\n\n")
}

type cliCallStackFrameView struct {
	agentID string
	depth   int
	frame   cliCallFrame
}

func (m *cliModel) latestCallStackNode() *cliTraceNode {
	return m.focusTraceNode()
}

func (m *cliModel) tracePath(node *cliTraceNode) []*cliTraceNode {
	if node == nil {
		return nil
	}
	var reversed []*cliTraceNode
	current := node
	for current != nil {
		reversed = append(reversed, current)
		if strings.TrimSpace(current.parentRunID) == "" {
			break
		}
		current = m.traceNodes[current.parentRunID]
	}
	path := make([]*cliTraceNode, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		path = append(path, reversed[i])
	}
	return path
}

func (m *cliModel) collectCallStackFrames(path []*cliTraceNode) []cliCallStackFrameView {
	frames := make([]cliCallStackFrameView, 0, 8)
	for depth, node := range path {
		if node == nil {
			continue
		}
		for _, frame := range node.callFrames {
			if strings.TrimSpace(frame.label) == "" {
				continue
			}
			frames = append(frames, cliCallStackFrameView{
				agentID: nonEmptyCLI(node.agentID, "agent"),
				depth:   depth,
				frame:   frame,
			})
		}
	}
	sort.SliceStable(frames, func(i, j int) bool {
		if frames[i].frame.at.Equal(frames[j].frame.at) {
			if frames[i].depth == frames[j].depth {
				return frames[i].frame.label < frames[j].frame.label
			}
			return frames[i].depth < frames[j].depth
		}
		return frames[i].frame.at.Before(frames[j].frame.at)
	})
	if len(frames) > 8 {
		return append([]cliCallStackFrameView(nil), frames[len(frames)-8:]...)
	}
	return frames
}

func (m *cliModel) renderLatestCallStackSummary() string {
	node := m.latestCallStackNode()
	if node == nil {
		return "等待第一条运行事件后展示最新消息的 agent / tool 调用栈。"
	}
	path := m.tracePath(node)
	agents := make([]string, 0, len(path))
	for _, item := range path {
		if item == nil {
			continue
		}
		agents = append(agents, nonEmptyCLI(item.agentID, shortID(item.runID)))
	}
	summary := strings.Join(agents, " -> ")
	frames := m.collectCallStackFrames(path)
	if len(frames) == 0 {
		return nonEmptyCLI(summary, "当前最新消息尚未触发工具或委派。")
	}
	labels := make([]string, 0, len(frames))
	for _, frame := range frames {
		labels = append(labels, frame.frame.label)
	}
	return nonEmptyCLI(summary, "agent stack") + "  •  " + trimForDisplay(strings.Join(labels, " -> "), 120)
}

func renderCLICallStackAgentTitle(node *cliTraceNode, depth, width int) string {
	prefix := traceItemPrefix(depth)
	title := prefix + nonEmptyCLI(node.agentID, "agent") + " " + renderCLITraceStatus(node.status) + " " + renderCLITracePhase(node)
	return lipgloss.NewStyle().Width(width).Render(title)
}

func renderCLICallStackFrameTitle(frame cliCallFrame, depth, width int) string {
	prefix := traceItemPrefix(depth)
	label := strings.TrimSpace(frame.label)
	if label == "" {
		label = "step"
	}
	rendered := label
	switch {
	case strings.TrimSpace(frame.error) != "" || strings.EqualFold(strings.TrimSpace(frame.status), "failed"):
		rendered = cliErrorStyle.Render(label)
	case strings.TrimSpace(frame.kind) == "agent_call":
		rendered = cliRunningStyle.Render(label)
	case strings.TrimSpace(frame.kind) == "tool":
		rendered = cliHeaderTitleStyle.Render(label)
	default:
		rendered = cliMutedStyle.Render(label)
	}
	return lipgloss.NewStyle().Width(width).Render(prefix + rendered)
}

func renderCLICallStackErrorLine(message string, depth, width int) string {
	prefix := traceDetailPrefix(depth)
	msg := strings.TrimSpace(message)
	if msg == "" {
		return ""
	}
	return cliErrorStyle.Width(width).Render(wrapCLIPrefixedBlock("error: "+trimForDisplay(msg, 180), prefix, max(8, width-lipgloss.Width(prefix))))
}

func (m *cliModel) renderLatestCallStack(width int) string {
	node := m.latestCallStackNode()
	if node == nil {
		return cliMutedStyle.Width(width).Render("等待第一条运行事件后展示最新消息的完整调用栈。")
	}

	path := m.tracePath(node)
	if len(path) == 0 {
		return cliMutedStyle.Width(width).Render("当前还没有可用的调用路径。")
	}

	lines := []string{
		cliMutedStyle.Width(width).Render(wrapCLIText(m.renderLatestCallStackSummary(), width)),
	}
	for depth, item := range path {
		if item == nil {
			continue
		}
		lines = append(lines, renderCLICallStackAgentTitle(item, depth, width))
		if m.callStackExpanded {
			metaPrefix := traceDetailPrefix(depth)
			meta := []string{"run " + shortID(item.runID)}
			if depth == 0 {
				meta = append(meta, "entry")
			} else {
				meta = append(meta, "from "+nonEmptyCLI(path[depth-1].agentID, shortID(path[depth-1].runID)))
			}
			if detail := strings.TrimSpace(cliTracePhaseDetail(item)); detail != "" {
				meta = append(meta, trimForDisplay(detail, 96))
			}
			lines = append(lines, cliMutedStyle.Width(width).Render(wrapCLIPrefixedBlock(strings.Join(meta, "  •  "), metaPrefix, max(8, width-lipgloss.Width(metaPrefix)))))
		}
		if errLine := renderCLICallStackErrorLine(item.errorMessage, depth, width); errLine != "" {
			lines = append(lines, errLine)
		}

		if len(item.callFrames) == 0 {
			continue
		}
		for _, frame := range item.callFrames {
			lines = append(lines, renderCLICallStackFrameTitle(frame, depth+1, width))
			if !m.callStackExpanded {
				if errLine := renderCLICallStackErrorLine(frame.error, depth+1, width); errLine != "" {
					lines = append(lines, errLine)
				}
				continue
			}
			metaPrefix := traceDetailPrefix(depth + 1)
			detailParts := []string{frame.at.Format("15:04:05")}
			if strings.TrimSpace(frame.kind) != "" {
				detailParts = append(detailParts, frame.kind)
			}
			if status := strings.TrimSpace(frame.status); status != "" && status != "running" {
				detailParts = append(detailParts, status)
			}
			if strings.TrimSpace(frame.detail) != "" {
				detailParts = append(detailParts, frame.detail)
			}
			lines = append(lines, cliMutedStyle.Width(width).Render(wrapCLIPrefixedBlock(strings.Join(detailParts, "  •  "), metaPrefix, max(8, width-lipgloss.Width(metaPrefix)))))
			if errLine := renderCLICallStackErrorLine(frame.error, depth+1, width); errLine != "" {
				lines = append(lines, errLine)
			}
		}
	}

	lines = append(lines, "", cliHintStyle.Width(width).Render("F3 或 /stack 可切换调用栈细节。F2 切换 split / full，两种布局展示的是同一份上下文内容。"))
	return strings.Join(lines, "\n")
}

func appendCLICallFrame(frames []cliCallFrame, frame cliCallFrame) []cliCallFrame {
	if strings.TrimSpace(frame.label) == "" {
		return frames
	}
	frames = append(frames, frame)
	if len(frames) > 6 {
		return append([]cliCallFrame(nil), frames[len(frames)-6:]...)
	}
	return frames
}

func upsertCLICallFrameResult(frames []cliCallFrame, kind, id, label, status, detail, errMsg string, at time.Time) []cliCallFrame {
	normalizedID := strings.TrimSpace(id)
	normalizedLabel := strings.TrimSpace(label)
	for i := len(frames) - 1; i >= 0; i-- {
		frame := &frames[i]
		if strings.TrimSpace(frame.kind) != kind {
			continue
		}
		matched := false
		if normalizedID != "" && strings.TrimSpace(frame.id) == normalizedID {
			matched = true
		} else if normalizedLabel != "" && strings.EqualFold(strings.TrimSpace(frame.label), normalizedLabel) {
			matched = true
		}
		if !matched {
			continue
		}
		if strings.TrimSpace(status) != "" {
			frame.status = strings.TrimSpace(status)
		}
		if strings.TrimSpace(detail) != "" {
			frame.detail = trimForDisplay(detail, 120)
		}
		if strings.TrimSpace(errMsg) != "" {
			frame.error = trimForDisplay(errMsg, 180)
		}
		if !at.IsZero() {
			frame.at = at
		}
		return frames
	}
	label = normalizedLabel
	if label == "" {
		label = kind
	}
	return appendCLICallFrame(frames, cliCallFrame{
		id:     normalizedID,
		kind:   kind,
		label:  label,
		detail: trimForDisplay(detail, 120),
		status: strings.TrimSpace(status),
		error:  trimForDisplay(errMsg, 180),
		at:     at,
	})
}

func normalizeCLIRunErrorMessage(event gateway.RunEvent) string {
	if msg := strings.TrimSpace(payloadString(event.Payload, "message")); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(payloadString(event.Payload, "error")); msg != "" {
		return msg
	}
	return ""
}

func normalizeCLICallFrameStatus(errMsg, status string) string {
	if strings.TrimSpace(errMsg) != "" {
		return "failed"
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return "completed"
	}
	return status
}

func (m *cliModel) applyRunEvent(event gateway.RunEvent) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	previousEventName := ""
	if strings.TrimSpace(event.RunID) != "" {
		if existing := m.traceNodes[event.RunID]; existing != nil {
			previousEventName = existing.lastEventName
		}
		node := m.ensureTraceNode(event)
		m.lastTreeRunID = m.treeRunIDForNode(node)
		node.eventCount++
		if !cliSuppressTraceSummaryEvent(event.Name) {
			node.lastEventName = event.Name
			node.lastEventSummary = summarizeCLITraceEvent(event)
		}
		node.lastEventAt = event.Timestamp

		switch event.Name {
		case "run.started":
			if node.startedAt.IsZero() {
				node.startedAt = event.Timestamp
			}
			node.status = "running"
		case "text.delta":
			node.status = coalesceCLIStatus(node.status, "running")
			node.preview = appendPreview(node.preview, payloadString(event.Payload, "text"))
			node.textDeltaCount++
		case "tool.call.started", "tool.called":
			node.status = coalesceCLIStatus(node.status, "running")
			if payloadString(event.Payload, "tool") != "callagent" {
				cliRememberTraceTool(node.activeToolCalls, payloadString(event.Payload, "id"), payloadString(event.Payload, "tool"))
				node.callFrames = appendCLICallFrame(node.callFrames, cliCallFrame{
					id:     payloadString(event.Payload, "id"),
					kind:   "tool",
					label:  payloadString(event.Payload, "tool"),
					detail: trimForDisplay(payloadString(event.Payload, "input"), 120),
					status: "running",
					at:     event.Timestamp,
				})
			}
		case "agent.call.started":
			node.status = coalesceCLIStatus(node.status, "running")
			cliRememberTraceTool(node.activeAgentCalls, payloadString(event.Payload, "id"), nonEmptyCLI(payloadString(event.Payload, "target_agent"), payloadString(event.Payload, "agent"), "agent"))
			target := nonEmptyCLI(payloadString(event.Payload, "target_agent"), payloadString(event.Payload, "agent"), "agent")
			node.callFrames = appendCLICallFrame(node.callFrames, cliCallFrame{
				id:     payloadString(event.Payload, "id"),
				kind:   "agent_call",
				label:  "agent call -> " + target,
				detail: trimForDisplay(payloadString(event.Payload, "task"), 120),
				status: "running",
				at:     event.Timestamp,
			})
		case "tool.completed", "tool.failed", "tool.finished":
			node.status = coalesceCLIStatus(node.status, "running")
			if payloadString(event.Payload, "tool") != "callagent" {
				cliForgetTraceTool(node.activeToolCalls, payloadString(event.Payload, "id"), payloadString(event.Payload, "tool"))
				node.callFrames = upsertCLICallFrameResult(
					node.callFrames,
					"tool",
					payloadString(event.Payload, "id"),
					payloadString(event.Payload, "tool"),
					normalizeCLICallFrameStatus(payloadString(event.Payload, "error"), ""),
					"",
					payloadString(event.Payload, "error"),
					event.Timestamp,
				)
			}
		case "agent.call.completed", "agent.call.failed":
			node.status = coalesceCLIStatus(node.status, "running")
			cliForgetTraceTool(node.activeAgentCalls, payloadString(event.Payload, "id"), nonEmptyCLI(payloadString(event.Payload, "target_agent"), payloadString(event.Payload, "agent"), "agent"))
			status := payloadString(event.Payload, "status")
			if status == "" {
				if event.Name == "agent.call.failed" {
					status = "failed"
				} else {
					status = "completed"
				}
			}
			taskID := payloadString(event.Payload, "task_id")
			target := nonEmptyCLI(payloadString(event.Payload, "target_agent"), payloadString(event.Payload, "agent"), "agent")
			node.callFrames = upsertCLICallFrameResult(
				node.callFrames,
				"agent_call",
				payloadString(event.Payload, "id"),
				"agent call -> "+target,
				normalizeCLICallFrameStatus(payloadString(event.Payload, "error"), status),
				"",
				payloadString(event.Payload, "error"),
				event.Timestamp,
			)
			if (status == "running" || status == "queued") && taskID != "" {
				cliRememberBackgroundTask(node.backgroundTasks, taskID, target, status, payloadString(event.Payload, "summary"), payloadString(event.Payload, "error"), event.Timestamp)
				break
			}
			if status == "failed" || payloadString(event.Payload, "error") != "" {
				node.agentCallsFailed++
			} else {
				node.agentCallsDone++
			}
		case "task.queued", "task.started", "task.running", "task.completed", "task.failed", "task.cancelled":
			node.status = coalesceCLIStatus(node.status, "running")
			cliRememberBackgroundTask(node.backgroundTasks, payloadString(event.Payload, "task_id"), nonEmptyCLI(payloadString(event.Payload, "target_agent"), "agent"), nonEmptyCLI(payloadString(event.Payload, "status"), strings.TrimPrefix(event.Name, "task.")), payloadString(event.Payload, "summary"), payloadString(event.Payload, "error"), event.Timestamp)
		case "run.completed":
			node.status = "completed"
			node.completedAt = event.Timestamp
			clear(node.activeToolCalls)
			clear(node.activeAgentCalls)
		case "run.failed":
			node.status = "failed"
			node.completedAt = event.Timestamp
			node.errorMessage = normalizeCLIRunErrorMessage(event)
			clear(node.activeToolCalls)
			clear(node.activeAgentCalls)
		case "run.aborted":
			node.status = "aborted"
			node.completedAt = event.Timestamp
			node.errorMessage = normalizeCLIRunErrorMessage(event)
			clear(node.activeToolCalls)
			clear(node.activeAgentCalls)
		default:
			node.status = coalesceCLIStatus(node.status, "running")
		}
		if strings.TrimSpace(m.selectedTraceRunID) == "" {
			m.selectedTraceRunID = node.runID
		}
	}

	depth := m.runDepth(event.RunID)
	if !cliDisplayActivityEvent(event.Name) {
		return
	}
	switch event.Name {
	case "text.delta":
		if previousEventName == "text.delta" {
			return
		}
	}
	m.addActivity(formatCLIEventTitle(event), m.formatCLIEventDetail(event), event.Timestamp, toneForEvent(event), depth, event.RunID, event.Name)
}

func (m *cliModel) addActivity(title, detail string, at time.Time, tone string, depth int, runID, eventName string) {
	m.activities = append(m.activities, cliActivityEntry{
		title:     title,
		detail:    detail,
		tone:      tone,
		at:        at,
		depth:     depth,
		runID:     runID,
		eventName: eventName,
	})
	if len(m.activities) > cliMaxActivities {
		m.activities = append([]cliActivityEntry(nil), m.activities[len(m.activities)-cliMaxActivities:]...)
	}
}

func (m *cliModel) ensureTraceNode(event gateway.RunEvent) *cliTraceNode {
	node, ok := m.traceNodes[event.RunID]
	if !ok {
		node = &cliTraceNode{
			runID:            event.RunID,
			agentID:          event.AgentID,
			sessionID:        event.SessionID,
			parentAgentID:    event.ParentAgentID,
			status:           "queued",
			startedAt:        event.Timestamp,
			lastEventName:    event.Name,
			lastEventSummary: summarizeCLITraceEvent(event),
			lastEventAt:      event.Timestamp,
			activeToolCalls:  make(map[string]string),
			activeAgentCalls: make(map[string]string),
			backgroundTasks:  make(map[string]cliBackgroundTask),
			callFrames:       nil,
		}
		m.traceNodes[event.RunID] = node
	}
	if node.agentID == "" {
		node.agentID = event.AgentID
	}
	if node.sessionID == "" {
		node.sessionID = event.SessionID
	}
	if node.parentAgentID == "" {
		node.parentAgentID = event.ParentAgentID
	}
	if node.parentRunID == "" &&
		strings.TrimSpace(node.runID) != "" &&
		strings.TrimSpace(node.runID) != strings.TrimSpace(m.lastTreeRunID) &&
		strings.TrimSpace(node.parentAgentID) != "" &&
		strings.TrimSpace(m.lastTreeRunID) != "" {
		node.parentRunID = m.lastTreeRunID
	}
	if node.startedAt.IsZero() {
		node.startedAt = event.Timestamp
	}
	if node.activeToolCalls == nil {
		node.activeToolCalls = make(map[string]string)
	}
	if node.activeAgentCalls == nil {
		node.activeAgentCalls = make(map[string]string)
	}
	if node.backgroundTasks == nil {
		node.backgroundTasks = make(map[string]cliBackgroundTask)
	}
	return node
}

func (m *cliModel) runningRunCount() int {
	count := 0
	for _, node := range m.traceNodes {
		if node == nil {
			continue
		}
		if node.status == "running" || node.status == "queued" {
			count++
		}
	}
	if count == 0 {
		if active, _, _, _ := m.backgroundTaskCounts(); active > 0 {
			return active
		}
	}
	return count
}

func (m *cliModel) runtimeSummary() cliRuntimeSummary {
	running, completed, failed := m.traceStatusCounts()
	agentCallActive, agentCallDone, agentCallFailed := m.agentCallStatusCounts()
	return cliRuntimeSummary{
		State:           m.runtimeStatusLabel(),
		RunsLive:        running,
		RunsDone:        completed,
		RunsFailed:      failed,
		SubagentsActive: agentCallActive,
		SubagentsDone:   agentCallDone,
		SubagentsFailed: agentCallFailed,
		EventsVisible:   len(m.activities),
	}
}

func (m *cliModel) traceStatusCounts() (running, completed, failed int) {
	for _, node := range m.traceNodes {
		if node == nil {
			continue
		}
		switch node.status {
		case "running", "queued":
			running++
		case "completed":
			completed++
		case "failed", "aborted":
			failed++
		}
	}
	return running, completed, failed
}

func (m *cliModel) agentCallStatusCounts() (active, completed, failed int) {
	taskActive, taskCompleted, taskFailed, taskCancelled := m.backgroundTaskCounts()
	for _, node := range m.traceNodes {
		if node == nil {
			continue
		}
		active += len(node.activeAgentCalls)
		completed += node.agentCallsDone
		failed += node.agentCallsFailed
	}
	active += taskActive
	completed += taskCompleted
	failed += taskFailed + taskCancelled
	return active, completed, failed
}

func (m *cliModel) backgroundTaskCounts() (active, completed, failed, cancelled int) {
	for _, node := range m.traceNodes {
		if node == nil {
			continue
		}
		for _, task := range node.backgroundTasks {
			switch task.status {
			case "queued", "running":
				active++
			case "completed":
				completed++
			case "failed":
				failed++
			case "cancelled":
				cancelled++
			}
		}
	}
	return active, completed, failed, cancelled
}

func (m *cliModel) runtimeStatusLabel() string {
	if m.runningRunCount() == 0 {
		return "idle"
	}
	if active, _, _ := m.agentCallStatusCounts(); active > 0 {
		return "subagent active"
	}
	for _, node := range m.traceNodes {
		if node == nil {
			continue
		}
		if cliTracePhaseKind(node) == "generating" {
			return "generating"
		}
	}
	activeTools := 0
	for _, node := range m.traceNodes {
		if node == nil {
			continue
		}
		activeTools += len(node.activeToolCalls)
	}
	if activeTools > 0 {
		return "tool active"
	}
	return "thinking"
}

func (m *cliModel) sortedTraceRoots() []*cliTraceNode {
	roots := make([]*cliTraceNode, 0, len(m.traceNodes))
	for _, node := range m.traceNodes {
		if node == nil {
			continue
		}
		if strings.TrimSpace(node.parentRunID) == "" || m.traceNodes[node.parentRunID] == nil {
			roots = append(roots, node)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].startedAt.Equal(roots[j].startedAt) {
			return roots[i].runID < roots[j].runID
		}
		return roots[i].startedAt.Before(roots[j].startedAt)
	})
	return roots
}

func (m *cliModel) childTraceNodes(parentRunID string) []*cliTraceNode {
	children := make([]*cliTraceNode, 0, len(m.traceNodes))
	for _, node := range m.traceNodes {
		if node == nil || node.parentRunID != parentRunID {
			continue
		}
		children = append(children, node)
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].startedAt.Equal(children[j].startedAt) {
			return children[i].runID < children[j].runID
		}
		return children[i].startedAt.Before(children[j].startedAt)
	})
	return children
}

func (m *cliModel) appendTraceNodeRows(rows []string, node *cliTraceNode, depth, width int) []string {
	if node == nil {
		return rows
	}
	rows = append(rows, renderTraceNodeRow(node, m.treeRunIDForNode(node), depth, width, node.runID == m.selectedTraceRunID, m.treeRunIDForNode(node) == m.lastTreeRunID && depth == 0))
	for _, child := range m.childTraceNodes(node.runID) {
		rows = m.appendTraceNodeRows(rows, child, depth+1, width)
	}
	return rows
}

func conversationCardStyle(role string) lipgloss.Style {
	style := lipgloss.NewStyle().
		Padding(0, 1).
		Foreground(cliTextColor).
		Border(lipgloss.NormalBorder(), false, false, false, true)
	switch role {
	case "user":
		return style.Background(cliHighlightSoftColor).BorderForeground(cliUserColor)
	case "assistant":
		return style.Background(cliPanelAltColor).BorderForeground(cliAssistantColor)
	default:
		return style.Background(lipgloss.Color("#1C1915")).BorderForeground(cliSystemColor)
	}
}

func renderTimelineEntry(item cliActivityEntry, width int) string {
	prefix := traceItemPrefix(item.depth)
	detailPrefix := traceDetailPrefix(item.depth)
	timeLabel := cliMutedStyle.Render(item.at.Format("15:04:05"))
	tone := renderCLIActivityTone(item.tone)
	header := lipgloss.JoinHorizontal(
		lipgloss.Left,
		timeLabel,
		"  ",
		tone,
		" ",
		prefix+item.title,
	)
	innerWidth := max(8, width-lipgloss.Width(detailPrefix))
	bodyLines := wrapCLIPrefixedBlock(item.detail, detailPrefix, innerWidth)
	lines := []string{lipgloss.NewStyle().Width(width).Render(header)}
	if strings.TrimSpace(bodyLines) != "" {
		lines = append(lines, cliMutedStyle.Width(width).Render(bodyLines))
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n"))
}

func renderTraceNodeRow(node *cliTraceNode, treeRunID string, depth, width int, selected bool, focus bool) string {
	prefix := traceItemPrefix(depth)
	detailPrefix := traceDetailPrefix(depth)

	agentLabel := node.agentID
	if strings.TrimSpace(agentLabel) == "" {
		agentLabel = "agent"
	}

	nodeKind := "ENTRY"
	if depth > 0 {
		nodeKind = "STEP"
	}
	title := prefix + cliMetaBadgeStyle.Render(nodeKind) + " " + cliHeaderTitleStyle.Render(agentLabel) + " " + renderCLITraceStatus(node.status) + " " + renderCLITracePhase(node)
	if focus {
		title += " " + cliMetaBadgeStyle.Render("focus")
	}
	if selected {
		title += " " + cliMetaBadgeStyle.Render("selected")
	}

	meta := []string{"run " + shortID(node.runID)}
	if strings.TrimSpace(node.sessionID) != "" {
		meta = append(meta, "session "+shortID(node.sessionID))
	}
	if node.eventCount > 0 {
		meta = append(meta, fmt.Sprintf("%d event(s)", node.eventCount))
	}

	lines := []string{
		title,
		detailPrefix + cliMutedStyle.Render(strings.Join(meta, "  •  ")),
	}
	if depth > 0 && strings.TrimSpace(node.parentRunID) != "" {
		parentLabel := nonEmptyCLI(node.parentAgentID, shortID(node.parentRunID))
		lines = append(lines, detailPrefix+cliMutedStyle.Render("from "+parentLabel))
	}
	if summary := strings.TrimSpace(node.lastEventSummary); summary != "" {
		lines = append(lines, cliMutedStyle.Render(wrapCLIPrefixedBlock("last: "+summary, detailPrefix, max(8, width-lipgloss.Width(detailPrefix)))))
	}
	if preview := strings.TrimSpace(node.preview); preview != "" {
		lines = append(lines, cliMutedStyle.Render(wrapCLIPrefixedBlock("output: "+preview, detailPrefix, max(8, width-lipgloss.Width(detailPrefix)))))
	}
	if !node.completedAt.IsZero() {
		lines = append(lines, detailPrefix+cliMutedStyle.Render("completed "+node.completedAt.Format("15:04:05")))
	} else if !node.startedAt.IsZero() {
		lines = append(lines, detailPrefix+cliMutedStyle.Render("started "+node.startedAt.Format("15:04:05")))
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n"))
}

func appendTrimmedConversation(entries []cliConversationEntry, entry cliConversationEntry) []cliConversationEntry {
	entries = append(entries, entry)
	if len(entries) > cliMaxMessages {
		return append([]cliConversationEntry(nil), entries[len(entries)-cliMaxMessages:]...)
	}
	return entries
}

func appendPreview(current, delta string) string {
	current = strings.TrimSpace(current)
	delta = strings.TrimSpace(delta)
	if delta == "" {
		return current
	}
	combined := strings.TrimSpace(current + delta)
	runes := []rune(combined)
	if len(runes) <= cliMaxPreviewRune {
		return combined
	}
	return string(runes[len(runes)-cliMaxPreviewRune:])
}

func formatCLIEventTitle(event gateway.RunEvent) string {
	agent := event.AgentID
	if agent == "" {
		agent = "agent"
	}
	prefix := "[" + agent + "] "
	switch event.Name {
	case "run.started":
		return cliRunningStyle.Render(prefix + "thinking")
	case "text.delta":
		return cliRunningStyle.Render(prefix + "streaming")
	case "tool.call.requested":
		return cliRunningStyle.Render(prefix + "plan tool " + payloadString(event.Payload, "tool"))
	case "tool.call.started", "tool.called":
		return cliHeaderTitleStyle.Render(prefix + "tool " + payloadString(event.Payload, "tool"))
	case "tool.completed", "tool.failed", "tool.finished":
		if payloadString(event.Payload, "error") != "" {
			return cliErrorStyle.Render(prefix + "tool failed")
		}
		return cliHeaderTitleStyle.Render(prefix + "tool finished")
	case "tool.fanout.completed":
		return cliIdleStyle.Render(prefix + "tool batch " + nonEmptyCLI(payloadString(event.Payload, "status"), "completed"))
	case "memory.recalled":
		return cliIdleStyle.Render(prefix + "memory recalled")
	case "agent.call.started":
		target := payloadString(event.Payload, "target_agent")
		if target == "" {
			target = payloadString(event.Payload, "agent")
		}
		return cliRunningStyle.Render(prefix + "agent call -> " + target)
	case "agent.call.completed", "agent.call.failed":
		status := payloadString(event.Payload, "status")
		if status == "" {
			if event.Name == "agent.call.failed" {
				status = "failed"
			} else {
				status = "completed"
			}
		}
		if status == "running" || status == "queued" {
			return cliRunningStyle.Render(prefix + "agent task " + status)
		}
		if status == "failed" {
			return cliErrorStyle.Render(prefix + "agent call failed")
		}
		return cliIdleStyle.Render(prefix + "agent call " + status)
	case "task.queued":
		return cliRunningStyle.Render(prefix + "task queued")
	case "task.started":
		return cliRunningStyle.Render(prefix + "task started")
	case "task.running":
		return cliRunningStyle.Render(prefix + "task running")
	case "task.completed":
		return cliIdleStyle.Render(prefix + "task completed")
	case "task.failed", "task.cancelled":
		return cliErrorStyle.Render(prefix + "task " + strings.TrimPrefix(event.Name, "task."))
	case "session.compact.requested":
		return cliRunningStyle.Render(prefix + "session compaction requested")
	case "session.compact.completed":
		return cliIdleStyle.Render(prefix + "session compaction completed")
	case "run.completed":
		return cliIdleStyle.Render(prefix + "run completed")
	case "run.failed":
		return cliErrorStyle.Render(prefix + "run failed")
	case "run.aborted":
		return cliErrorStyle.Render(prefix + "run aborted")
	default:
		return cliHeaderTitleStyle.Render(prefix + event.Name)
	}
}

func (m *cliModel) formatCLIEventDetail(event gateway.RunEvent) string {
	var lines []string
	header := "run " + shortID(event.RunID)
	lines = append(lines, header)
	switch event.Name {
	case "text.delta":
		if text := payloadString(event.Payload, "text"); text != "" {
			lines = append(lines, trimForDisplay(text, 180))
		} else {
			lines = append(lines, "assistant is streaming output")
		}
	case "tool.call.requested", "tool.call.started", "tool.called":
		if input := payloadString(event.Payload, "input"); input != "" {
			lines = append(lines, trimForDisplay(input, 180))
		}
	case "tool.completed", "tool.failed", "tool.finished":
		if errMsg := payloadString(event.Payload, "error"); errMsg != "" {
			lines = append(lines, trimForDisplay(errMsg, 180))
		} else if output := payloadString(event.Payload, "output"); output != "" {
			lines = append(lines, trimForDisplay(output, 180))
		}
	case "tool.fanout.completed":
		lines = append(lines, fmt.Sprintf(
			"status: %s  •  completed %d/%d  •  failed %d",
			nonEmptyCLI(payloadString(event.Payload, "status"), "completed"),
			payloadInt(event.Payload, "completed_count"),
			payloadInt(event.Payload, "total_count"),
			payloadInt(event.Payload, "failed_count"),
		))
	case "memory.recalled":
		if query := payloadString(event.Payload, "query"); query != "" {
			lines = append(lines, "query: "+trimForDisplay(query, 180))
		}
		for _, item := range payloadEntries(event.Payload, "entries") {
			label := trimForDisplay(fmt.Sprintf("%v", item["id"]), 180)
			if terms := payloadString(item, "matched_terms"); terms != "" {
				label += "  •  matched " + trimForDisplay(terms, 80)
			}
			lines = append(lines, label)
		}
	case "agent.call.started":
		if task := payloadString(event.Payload, "task"); task != "" {
			lines = append(lines, trimForDisplay(task, 180))
		}
	case "agent.call.completed", "agent.call.failed":
		if summary := payloadString(event.Payload, "summary"); summary != "" {
			lines = append(lines, trimForDisplay(summary, 180))
		}
		if taskID := payloadString(event.Payload, "task_id"); taskID != "" {
			lines = append(lines, "task "+taskID+"  •  "+nonEmptyCLI(payloadString(event.Payload, "status"), "running"))
		}
	case "task.queued", "task.started", "task.running", "task.completed", "task.failed", "task.cancelled":
		lines = append(lines, "task "+shortID(payloadString(event.Payload, "task_id"))+"  •  "+nonEmptyCLI(payloadString(event.Payload, "target_agent"), "agent"))
		if summary := payloadString(event.Payload, "summary"); summary != "" {
			lines = append(lines, trimForDisplay(summary, 180))
		}
		if errMsg := payloadString(event.Payload, "error"); errMsg != "" {
			lines = append(lines, trimForDisplay(errMsg, 180))
		}
	case "session.compact.requested", "session.compact.completed":
		capability := payloadString(event.Payload, "compact_capability")
		strategy := payloadString(event.Payload, "compact_strategy")
		provider := nonEmptyCLI(payloadString(event.Payload, "provider"), "--")
		model := nonEmptyCLI(payloadString(event.Payload, "model"), "--")
		lines = append(lines, "provider "+provider+"  •  model "+model)
		if capability != "" || strategy != "" {
			lines = append(lines, "capability "+nonEmptyCLI(capability, "--")+"  •  strategy "+nonEmptyCLI(strategy, "--"))
		}
		lines = append(lines, fmt.Sprintf(
			"compact %d -> %d  •  keep %d  •  protected %d",
			payloadInt(event.Payload, "history_len_before"),
			payloadInt(event.Payload, "history_len_after"),
			payloadInt(event.Payload, "keep_entries"),
			payloadInt(event.Payload, "protected_prefix_len"),
		))
		if source := payloadString(event.Payload, "summary_source"); source != "" {
			lines = append(lines, "summary source "+source)
		}
		if summary := payloadString(event.Payload, "summary"); summary != "" {
			lines = append(lines, trimForDisplay(summary, 180))
		}
	case "run.failed":
		if msg := payloadString(event.Payload, "message"); msg != "" {
			lines = append(lines, trimForDisplay(msg, 180))
		}
	}
	return strings.Join(lines, "\n")
}

func renderHeaderBadge(label, value string) string {
	content := lipgloss.JoinHorizontal(
		lipgloss.Left,
		cliMetaKeyStyle.Render(label),
		" ",
		value,
	)
	return cliMetaBadgeStyle.Render(content)
}

func renderHeaderRow(width int, items []string) string {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	row := strings.Join(filtered, " ")
	if lipgloss.Width(row) <= width {
		return row
	}
	return strings.Join(filtered, "\n")
}

func (m *cliModel) formatRuntimeSummary(summary cliRuntimeSummary, sep string) string {
	if sep == "" {
		sep = " "
	}
	parts := []string{
		"state " + summary.State,
		"runs " + formatRuntimeRunCounts(summary),
		"subagents " + formatRuntimeSubagentCounts(summary),
		formatRuntimeEventCount(summary),
	}
	return strings.Join(parts, sep)
}

func formatRuntimeRunCounts(summary cliRuntimeSummary) string {
	return fmt.Sprintf("%d live / %d done / %d failed", summary.RunsLive, summary.RunsDone, summary.RunsFailed)
}

func formatRuntimeSubagentCounts(summary cliRuntimeSummary) string {
	return fmt.Sprintf("%d active / %d done / %d failed", summary.SubagentsActive, summary.SubagentsDone, summary.SubagentsFailed)
}

func formatRuntimeEventCount(summary cliRuntimeSummary) string {
	return fmt.Sprintf("%d events", summary.EventsVisible)
}

func (m *cliModel) renderTraceSummary(width int) string {
	summary := m.runtimeSummary()
	parts := []string{
		cliMetaBadgeStyle.Render("state " + summary.State),
		cliMetaBadgeStyle.Render("runs " + formatRuntimeRunCounts(summary)),
		cliMetaBadgeStyle.Render("subagents " + formatRuntimeSubagentCounts(summary)),
		cliMetaBadgeStyle.Render(formatRuntimeEventCount(summary)),
	}
	line := strings.Join(parts, " ")
	return lipgloss.NewStyle().Width(width).Render(line)
}

func renderCLITraceStatus(status string) string {
	label := strings.TrimSpace(status)
	if label == "" {
		label = "queued"
	}

	style := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	switch label {
	case "completed":
		style = style.Foreground(cliSurfaceColor).Background(cliAssistantColor)
	case "failed", "aborted":
		style = style.Foreground(cliSurfaceColor).Background(cliDangerColor)
	case "running":
		style = style.Foreground(cliSurfaceColor).Background(cliAccentColor)
	default:
		style = style.Foreground(cliSurfaceColor).Background(cliSystemColor)
	}
	return style.Render(label)
}

func renderCLITracePhase(node *cliTraceNode) string {
	label := cliTracePhaseLabel(node)
	style := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	switch cliTracePhaseKind(node) {
	case "completed":
		style = style.Foreground(cliSurfaceColor).Background(cliAssistantColor)
	case "failed":
		style = style.Foreground(cliSurfaceColor).Background(cliDangerColor)
	case "generating":
		style = style.Foreground(cliSurfaceColor).Background(cliAssistantColor)
	case "calling", "integrating":
		style = style.Foreground(cliSurfaceColor).Background(cliAccentColor)
	case "tooling":
		style = style.Foreground(cliSurfaceColor).Background(cliUserColor)
	default:
		style = style.Foreground(cliSurfaceColor).Background(cliSystemColor)
	}
	return style.Render(label)
}

func summarizeCLITraceEvent(event gateway.RunEvent) string {
	switch event.Name {
	case "run.started":
		return "model is thinking"
	case "text.delta":
		text := trimForDisplay(payloadString(event.Payload, "text"), 88)
		if text == "" {
			return "model is streaming output"
		}
		return text
	case "tool.call.requested":
		toolName := payloadString(event.Payload, "tool")
		if toolName == "" {
			toolName = "tool"
		}
		return "planned " + toolName
	case "tool.call.started", "tool.called":
		toolName := payloadString(event.Payload, "tool")
		if toolName == "" {
			toolName = "tool"
		}
		return "running " + toolName
	case "tool.completed", "tool.failed", "tool.finished":
		toolName := payloadString(event.Payload, "tool")
		if toolName == "" {
			toolName = "tool"
		}
		if errMsg := payloadString(event.Payload, "error"); errMsg != "" {
			return toolName + " failed: " + trimForDisplay(errMsg, 84)
		}
		if output := payloadString(event.Payload, "output"); output != "" {
			return toolName + " finished: " + trimForDisplay(output, 84)
		}
		return toolName + " finished"
	case "memory.recalled":
		query := payloadString(event.Payload, "query")
		entries := payloadEntries(event.Payload, "entries")
		if query == "" {
			if len(entries) == 0 {
				return "memory recalled"
			}
			return fmt.Sprintf("memory recalled (%d hit)", len(entries))
		}
		if len(entries) == 0 {
			return "memory recalled: " + trimForDisplay(query, 84)
		}
		return fmt.Sprintf("memory recalled (%d): %s", len(entries), trimForDisplay(query, 72))
	case "agent.call.started":
		target := payloadString(event.Payload, "target_agent")
		if target == "" {
			target = payloadString(event.Payload, "agent")
		}
		if target == "" {
			target = "agent"
		}
		task := payloadString(event.Payload, "task")
		if task == "" {
			return "waiting on subagent " + target
		}
		return "waiting on subagent " + target + ": " + trimForDisplay(task, 84)
	case "agent.call.completed", "agent.call.failed":
		status := payloadString(event.Payload, "status")
		if status == "running" || status == "queued" {
			target := payloadString(event.Payload, "target_agent")
			if target == "" {
				target = "agent"
			}
			return target + " task " + status
		}
		target := payloadString(event.Payload, "target_agent")
		if errMsg := payloadString(event.Payload, "error"); errMsg != "" {
			prefix := "agent call failed"
			if target != "" {
				prefix = target + " failed"
			}
			return prefix + ": " + trimForDisplay(errMsg, 84)
		}
		if summary := payloadString(event.Payload, "summary"); summary != "" {
			prefix := "agent call completed"
			if target != "" {
				prefix = target + " finished"
			}
			return prefix + ": " + trimForDisplay(summary, 84)
		}
		if target != "" {
			return target + " finished"
		}
		return "agent call completed"
	case "task.queued", "task.started", "task.running", "task.completed", "task.failed", "task.cancelled":
		target := payloadString(event.Payload, "target_agent")
		if target == "" {
			target = "agent"
		}
		status := nonEmptyCLI(payloadString(event.Payload, "status"), strings.TrimPrefix(event.Name, "task."))
		if status == "failed" {
			if errMsg := payloadString(event.Payload, "error"); errMsg != "" {
				return target + " task failed: " + trimForDisplay(errMsg, 84)
			}
		}
		if status == "completed" {
			if summary := payloadString(event.Payload, "summary"); summary != "" {
				return target + " task completed: " + trimForDisplay(summary, 84)
			}
		}
		return target + " task " + status
	case "session.compact.requested":
		capability := payloadString(event.Payload, "compact_capability")
		if capability == "" {
			return "session compaction requested"
		}
		return "session compaction requested: " + trimForDisplay(capability, 84)
	case "session.compact.completed":
		strategy := payloadString(event.Payload, "compact_strategy")
		capability := payloadString(event.Payload, "compact_capability")
		switch {
		case strategy != "" && capability != "":
			return "session compacted: " + trimForDisplay(strategy+" • "+capability, 84)
		case strategy != "":
			return "session compacted: " + trimForDisplay(strategy, 84)
		case capability != "":
			return "session compacted: " + trimForDisplay(capability, 84)
		default:
			return "session compacted"
		}
	case "run.completed":
		return "run completed"
	case "run.failed":
		msg := payloadString(event.Payload, "message")
		if msg == "" {
			return "run failed"
		}
		return "run failed: " + trimForDisplay(msg, 84)
	case "run.aborted":
		msg := payloadString(event.Payload, "message")
		if msg == "" {
			return "run aborted"
		}
		return "run aborted: " + trimForDisplay(msg, 84)
	default:
		return trimForDisplay(event.Name, 84)
	}
}

func cliDisplayActivityEvent(name string) bool {
	switch strings.TrimSpace(name) {
	case "run.activity", "run.started", "text.delta", "task.queued", "task.started", "task.running", "tool.call.requested":
		return false
	default:
		return true
	}
}

func cliSuppressTraceSummaryEvent(name string) bool {
	switch strings.TrimSpace(name) {
	case "run.activity", "task.queued", "task.started", "task.running", "tool.call.requested":
		return true
	default:
		return false
	}
}

func coalesceCLIStatus(current, fallback string) string {
	current = strings.TrimSpace(current)
	if current != "" {
		return current
	}
	return fallback
}

func renderCLIActivityTone(tone string) string {
	label := strings.TrimSpace(tone)
	if label == "" {
		label = "info"
	}
	style := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	switch label {
	case "success":
		style = style.Foreground(cliSurfaceColor).Background(cliAssistantColor)
	case "warning":
		style = style.Foreground(cliSurfaceColor).Background(cliSystemColor)
	case "error":
		style = style.Foreground(cliSurfaceColor).Background(cliDangerColor)
	default:
		style = style.Foreground(cliSurfaceColor).Background(cliUserColor)
	}
	return style.Render(label)
}

func traceItemPrefix(depth int) string {
	if depth <= 0 {
		return "> "
	}
	return strings.Repeat("| ", depth-1) + "|- "
}

func traceDetailPrefix(depth int) string {
	if depth <= 0 {
		return "  "
	}
	return strings.Repeat("| ", depth) + "  "
}

func prefixMultiline(text, prefix string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func toneForEvent(event gateway.RunEvent) string {
	switch event.Name {
	case "run.failed", "run.aborted":
		return "error"
	case "run.completed", "agent.call.completed", "agent.call.failed":
		if payloadString(event.Payload, "status") == "running" || payloadString(event.Payload, "status") == "queued" {
			return "warning"
		}
		if payloadString(event.Payload, "status") == "failed" {
			return "error"
		}
		return "success"
	case "task.failed", "task.cancelled":
		return "error"
	case "task.completed":
		return "success"
	case "memory.recalled":
		return "success"
	case "session.compact.requested":
		return "warning"
	case "session.compact.completed":
		return "success"
	case "agent.call.started", "run.started", "text.delta", "task.queued", "task.started", "task.running", "tool.call.requested", "tool.call.started":
		return "warning"
	case "tool.completed":
		return "success"
	case "tool.failed", "tool.finished":
		if payloadString(event.Payload, "error") != "" {
			return "error"
		}
		return "success"
	default:
		return "info"
	}
}

func (m *cliModel) runDepth(runID string) int {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return 0
	}
	depth := 0
	visited := map[string]struct{}{}
	currentID := runID
	for {
		node := m.traceNodes[currentID]
		if node == nil || strings.TrimSpace(node.parentRunID) == "" {
			return depth
		}
		if _, seen := visited[currentID]; seen {
			return depth
		}
		visited[currentID] = struct{}{}
		depth++
		currentID = node.parentRunID
	}
}

func alignInline(width int, left, right string) string {
	if right == "" {
		return left
	}
	spacer := width - lipgloss.Width(left) - lipgloss.Width(right)
	if spacer < 1 {
		spacer = 1
	}
	return left + strings.Repeat(" ", spacer) + right
}

func wrapCLIText(text string, width int) string {
	width = max(1, width)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\t", "  ")
	lines := strings.Split(text, "\n")
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			wrapped = append(wrapped, "")
			continue
		}
		wrapped = append(wrapped, wrapCLILine(line, width)...)
	}
	return strings.Join(wrapped, "\n")
}

func wrapCLILine(line string, width int) []string {
	width = max(1, width)
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	current := ""
	for _, word := range words {
		for lipgloss.Width(word) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			chunk, rest := splitCLIToken(word, width)
			lines = append(lines, chunk)
			word = rest
		}
		if word == "" {
			continue
		}
		if current == "" {
			current = word
			continue
		}
		candidate := current + " " + word
		if lipgloss.Width(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func splitCLIToken(token string, width int) (string, string) {
	width = max(1, width)
	runes := []rune(token)
	if lipgloss.Width(token) <= width {
		return token, ""
	}
	chunkWidth := 0
	index := 0
	for index < len(runes) {
		nextWidth := lipgloss.Width(string(runes[index]))
		if chunkWidth+nextWidth > width {
			break
		}
		chunkWidth += nextWidth
		index++
	}
	if index == 0 {
		index = 1
	}
	return string(runes[:index]), string(runes[index:])
}

func wrapCLIPrefixedBlock(text, prefix string, width int) string {
	wrapped := wrapCLIText(text, width)
	return prefixMultiline(wrapped, prefix)
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []string:
		return strings.Join(v, ", ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, strings.TrimSpace(fmt.Sprintf("%v", item)))
		}
		return strings.Join(parts, ", ")
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func payloadInt(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func payloadEntries(payload map[string]any, key string) []map[string]any {
	if payload == nil {
		return nil
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return nil
	}
	switch entries := value.(type) {
	case []map[string]any:
		return entries
	case []any:
		out := make([]map[string]any, 0, len(entries))
		for _, item := range entries {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
}

func trimForDisplay(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func shortID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func nonEmptyCLI(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (m *cliModel) treeRunID(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ""
	}
	node := m.traceNodes[runID]
	return m.treeRunIDForNode(node)
}

func (m *cliModel) treeRunIDForNode(node *cliTraceNode) string {
	if node == nil {
		return ""
	}
	treeRunID := strings.TrimSpace(node.runID)
	current := node
	visited := map[string]struct{}{}
	for current != nil {
		currentRunID := strings.TrimSpace(current.runID)
		if currentRunID != "" {
			treeRunID = currentRunID
		}
		parentRunID := strings.TrimSpace(current.parentRunID)
		if parentRunID == "" {
			break
		}
		if _, seen := visited[parentRunID]; seen {
			break
		}
		visited[parentRunID] = struct{}{}
		parent := m.traceNodes[parentRunID]
		if parent == nil {
			treeRunID = parentRunID
			break
		}
		current = parent
	}
	return treeRunID
}

func (m *cliModel) preferCLITraceNode(candidate, current *cliTraceNode, lastTreeRunID string) bool {
	if current == nil {
		return true
	}
	candidateSameTree := strings.TrimSpace(lastTreeRunID) != "" && m.treeRunIDForNode(candidate) == lastTreeRunID
	currentSameTree := strings.TrimSpace(lastTreeRunID) != "" && m.treeRunIDForNode(current) == lastTreeRunID
	if candidateSameTree != currentSameTree {
		return candidateSameTree
	}
	candidateRunning := candidate.status == "running" || candidate.status == "queued"
	currentRunning := current.status == "running" || current.status == "queued"
	if candidateRunning != currentRunning {
		return candidateRunning
	}
	if !candidate.lastEventAt.Equal(current.lastEventAt) {
		return candidate.lastEventAt.After(current.lastEventAt)
	}
	if !candidate.startedAt.Equal(current.startedAt) {
		return candidate.startedAt.After(current.startedAt)
	}
	return candidate.runID > current.runID
}

func cliRememberTraceTool(active map[string]string, id, name string) {
	if active == nil {
		return
	}
	name = nonEmptyCLI(name, "step")
	id = strings.TrimSpace(id)
	if id == "" {
		id = name
	}
	active[id] = name
}

func cliForgetTraceTool(active map[string]string, id, fallback string) {
	if active == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id != "" {
		delete(active, id)
		return
	}
	fallback = strings.TrimSpace(fallback)
	for key, value := range active {
		if value == fallback {
			delete(active, key)
			return
		}
	}
}

func cliTracePhaseKind(node *cliTraceNode) string {
	if node == nil {
		return "waiting"
	}
	if node.status == "completed" && cliActiveBackgroundTaskCount(node) > 0 {
		return "calling"
	}
	switch node.status {
	case "completed":
		return "completed"
	case "failed", "aborted":
		return "failed"
	case "queued":
		return "queued"
	}
	if len(node.activeAgentCalls) > 0 {
		return "calling"
	}
	if cliActiveBackgroundTaskCount(node) > 0 {
		return "calling"
	}
	if len(node.activeToolCalls) > 0 {
		return "tooling"
	}
	switch node.lastEventName {
	case "text.delta":
		return "generating"
	case "agent.call.completed", "agent.call.failed":
		return "integrating"
	case "tool.completed", "tool.failed", "tool.finished", "memory.recalled", "run.started":
		return "thinking"
	default:
		return "running"
	}
}

func cliTracePhaseLabel(node *cliTraceNode) string {
	switch cliTracePhaseKind(node) {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "queued":
		return "queued"
	case "calling":
		active := 0
		if node != nil {
			active = len(node.activeAgentCalls) + cliActiveBackgroundTaskCount(node)
		}
		if active > 1 {
			return fmt.Sprintf("subagents x%d", active)
		}
		return "subagent active"
	case "tooling":
		if node != nil && len(node.activeToolCalls) > 1 {
			return fmt.Sprintf("tools x%d", len(node.activeToolCalls))
		}
		return "tool active"
	case "generating":
		return "generating"
	case "integrating":
		return "integrating"
	case "thinking":
		return "thinking"
	default:
		return "running"
	}
}

func cliTracePhaseDetail(node *cliTraceNode) string {
	if node == nil {
		return "waiting for runtime events"
	}
	switch cliTracePhaseKind(node) {
	case "completed":
		if footprint := cliTraceAgentCallFootprint(node); footprint != "" {
			return "run completed; " + footprint
		}
		return "run completed"
	case "failed":
		if summary := strings.TrimSpace(node.lastEventSummary); summary != "" {
			return summary
		}
		return "run failed"
	case "queued":
		return "queued for execution"
	case "calling":
		if footprint := cliTraceBackgroundFootprint(node, 3); footprint != "" {
			return "waiting on subagent " + footprint
		}
		return "waiting on subagent"
	case "tooling":
		return "running tool " + cliTraceListNames(node.activeToolCalls, 3)
	case "generating":
		return "model is streaming a reply"
	case "integrating":
		return "agent call finished, integrating result"
	case "thinking":
		return "model is thinking"
	default:
		if summary := strings.TrimSpace(node.lastEventSummary); summary != "" {
			return summary
		}
		return "waiting for runtime events"
	}
}

func cliTraceAgentCallFootprint(node *cliTraceNode) string {
	if node == nil {
		return ""
	}
	var parts []string
	if active := len(node.activeAgentCalls); active > 0 {
		parts = append(parts, fmt.Sprintf("%d active", active))
	}
	backgroundActive, backgroundCompleted, backgroundFailed, backgroundCancelled := cliBackgroundTaskStatusCounts(node)
	if backgroundActive > 0 {
		parts = append(parts, fmt.Sprintf("%d task active", backgroundActive))
	}
	if node.agentCallsDone > 0 {
		parts = append(parts, fmt.Sprintf("%d done", node.agentCallsDone))
	}
	if backgroundCompleted > 0 {
		parts = append(parts, fmt.Sprintf("%d task done", backgroundCompleted))
	}
	if node.agentCallsFailed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", node.agentCallsFailed))
	}
	if backgroundFailed > 0 {
		parts = append(parts, fmt.Sprintf("%d task failed", backgroundFailed))
	}
	if backgroundCancelled > 0 {
		parts = append(parts, fmt.Sprintf("%d cancelled", backgroundCancelled))
	}
	return strings.Join(parts, "  •  ")
}

func cliTraceToolFootprint(node *cliTraceNode) string {
	if node == nil || len(node.activeToolCalls) == 0 {
		return ""
	}
	return cliTraceListNames(node.activeToolCalls, 3)
}

func cliTraceListNames(items map[string]string, limit int) string {
	if len(items) == 0 {
		return ""
	}
	values := make([]string, 0, len(items))
	for _, value := range items {
		values = append(values, value)
	}
	sort.Strings(values)
	if limit > 0 && len(values) > limit {
		remaining := len(values) - limit
		values = append(values[:limit], fmt.Sprintf("+%d more", remaining))
	}
	return strings.Join(values, ", ")
}

func cliRememberBackgroundTask(tasks map[string]cliBackgroundTask, taskID, agentID, status, summary, err string, at time.Time) {
	if tasks == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	current := tasks[taskID]
	if strings.TrimSpace(agentID) != "" {
		current.agentID = agentID
	}
	if strings.TrimSpace(status) != "" {
		current.status = status
	}
	if strings.TrimSpace(summary) != "" {
		current.summary = summary
	}
	if strings.TrimSpace(err) != "" {
		current.err = err
	}
	if !at.IsZero() {
		current.at = at
	}
	tasks[taskID] = current
}

func cliActiveBackgroundTaskCount(node *cliTraceNode) int {
	active, _, _, _ := cliBackgroundTaskStatusCounts(node)
	return active
}

func cliBackgroundTaskStatusCounts(node *cliTraceNode) (active, completed, failed, cancelled int) {
	if node == nil {
		return 0, 0, 0, 0
	}
	for _, task := range node.backgroundTasks {
		switch task.status {
		case "queued", "running":
			active++
		case "completed":
			completed++
		case "failed":
			failed++
		case "cancelled":
			cancelled++
		}
	}
	return active, completed, failed, cancelled
}

func cliTraceBackgroundFootprint(node *cliTraceNode, limit int) string {
	if node == nil {
		return ""
	}
	names := make(map[string]string, len(node.activeAgentCalls)+len(node.backgroundTasks))
	for key, value := range node.activeAgentCalls {
		names[key] = value
	}
	for taskID, task := range node.backgroundTasks {
		if task.status != "queued" && task.status != "running" {
			continue
		}
		label := nonEmptyCLI(task.agentID, "agent")
		if strings.TrimSpace(task.status) != "" {
			label += " (" + task.status + ")"
		}
		names[taskID] = label
	}
	return cliTraceListNames(names, limit)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ParseCLIReferences scans text for @file:path / @file path style references
// (same for @dir and @url) and returns InputBlocks for each.
func ParseCLIReferences(text string) []input.InputBlock {
	// Fast path: no @ references at all
	if !strings.Contains(text, "@file") && !strings.Contains(text, "@dir") && !strings.Contains(text, "@url") {
		return nil
	}

	var blocks []input.InputBlock
	fields := strings.Fields(text)
	for i := 0; i < len(fields); i++ {
		block, consumed := parseCLIReferenceField(fields, i)
		if consumed == 0 {
			continue
		}
		blocks = append(blocks, block)
		i += consumed - 1
	}

	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

func parseCLIReferenceField(fields []string, index int) (input.InputBlock, int) {
	token := fields[index]
	switch {
	case strings.HasPrefix(token, "@file:"):
		return fileReferenceBlock(strings.TrimPrefix(token, "@file:"))
	case token == "@file" && index+1 < len(fields):
		block, ok := fileReferenceBlock(fields[index+1])
		if ok == 0 {
			return input.InputBlock{}, 0
		}
		return block, 2
	case strings.HasPrefix(token, "@dir:"):
		return dirReferenceBlock(strings.TrimPrefix(token, "@dir:"))
	case token == "@dir" && index+1 < len(fields):
		block, ok := dirReferenceBlock(fields[index+1])
		if ok == 0 {
			return input.InputBlock{}, 0
		}
		return block, 2
	case strings.HasPrefix(token, "@url:"):
		return urlReferenceBlock(strings.TrimPrefix(token, "@url:"))
	case token == "@url" && index+1 < len(fields):
		block, ok := urlReferenceBlock(fields[index+1])
		if ok == 0 {
			return input.InputBlock{}, 0
		}
		return block, 2
	default:
		return input.InputBlock{}, 0
	}
}

func fileReferenceBlock(ref string) (input.InputBlock, int) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return input.InputBlock{}, 0
	}
	absPath, _ := filepath.Abs(ref)
	return input.InputBlock{
		Type: "file",
		Name: filepath.Base(ref),
		Path: absPath,
	}, 1
}

func dirReferenceBlock(ref string) (input.InputBlock, int) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return input.InputBlock{}, 0
	}
	absPath, _ := filepath.Abs(ref)
	return input.InputBlock{
		Type: "dir",
		Name: filepath.Base(ref),
		Path: absPath,
	}, 1
}

func urlReferenceBlock(ref string) (input.InputBlock, int) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return input.InputBlock{}, 0
	}
	return input.InputBlock{
		Type: "url",
		URL:  ref,
	}, 1
}
