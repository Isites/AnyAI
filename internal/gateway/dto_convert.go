package gateway

import (
	"strings"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/logging"
	"github.com/Isites/anyai/internal/runtime/memory"
	runtimeresources "github.com/Isites/anyai/internal/runtime/resources"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
	"github.com/Isites/anyai/internal/runtime/session"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

func runtimeInputBlocks(blocks []InputBlock) []input.InputBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]input.InputBlock, len(blocks))
	for i, block := range blocks {
		out[i] = input.InputBlock{
			ID:           block.ID,
			Type:         block.Type,
			Name:         block.Name,
			Text:         block.Text,
			Path:         block.Path,
			AttachmentID: block.AttachmentID,
			URL:          block.URL,
			MimeType:     block.MimeType,
			Data:         append([]byte(nil), block.Data...),
			Meta:         cloneMap(block.Meta),
		}
	}
	return out
}

func gatewayInputBlocks(blocks []input.InputBlock) []InputBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]InputBlock, len(blocks))
	for i, block := range blocks {
		out[i] = InputBlock{
			ID:           block.ID,
			Type:         block.Type,
			Name:         block.Name,
			Text:         block.Text,
			Path:         block.Path,
			AttachmentID: block.AttachmentID,
			URL:          block.URL,
			MimeType:     block.MimeType,
			Data:         append([]byte(nil), block.Data...),
			Meta:         cloneMap(block.Meta),
		}
	}
	return out
}

func runtimeIngressRequest(req IngressRequest) runtimeport.IngressRequest {
	sessionID := strings.TrimSpace(req.SessionID)
	return runtimeport.IngressRequest{
		Channel:     strings.TrimSpace(req.Channel),
		RequestedID: strings.TrimSpace(req.RequestedID),
		SenderID:    strings.TrimSpace(req.SenderID),
		AccountID:   strings.TrimSpace(req.AccountID),
		ChatType:    runtimeChatType(req.ChatType),
		Text:        req.Text,
		MessageID:   strings.TrimSpace(req.MessageID),
		Envelope: input.InputEnvelope{
			SessionID: sessionID,
			Blocks:    runtimeInputBlocks(req.Inputs),
		},
		SessionID:     sessionID,
		SessionPrefix: strings.TrimSpace(req.SessionPrefix),
	}
}

func gatewayIngressRequest(req runtimeport.IngressRequest) IngressRequest {
	return IngressRequest{
		Channel:       req.Channel,
		RequestedID:   req.RequestedID,
		SenderID:      req.SenderID,
		AccountID:     req.AccountID,
		ChatType:      gatewayChatType(req.ChatType),
		Text:          req.Text,
		MessageID:     strings.TrimSpace(req.MessageID),
		Inputs:        gatewayInputBlocks(req.Envelope.Blocks),
		SessionID:     firstNonEmpty(req.SessionID, req.Envelope.SessionID),
		SessionPrefix: req.SessionPrefix,
	}
}

func gatewayManagedRun(run *runtimeport.ManagedRun) *ManagedRun {
	if run == nil {
		return nil
	}
	return &ManagedRun{
		RunID:         run.RunID,
		AgentID:       run.AgentID,
		SessionID:     run.SessionID,
		Model:         run.Model,
		Events:        gatewayEventChannel(run.Events),
		Cancel:        run.Cancel,
		OwnsLifecycle: run.OwnsLifecycle,
	}
}

func gatewayEventChannel(events <-chan runtimeevents.EventRecord) <-chan Event {
	if events == nil {
		return nil
	}
	out := make(chan Event, 128)
	go func() {
		defer close(out)
		for event := range events {
			out <- gatewayEvent(event)
		}
	}()
	return out
}

func gatewayRun(run runtimeevents.RunRecord) Run {
	return Run{
		ID:              run.ID,
		RunNodeID:       run.RunNodeID,
		ParentRunNodeID: run.ParentRunNodeID,
		ParentAgentID:   run.ParentAgentID,
		AgentID:         run.AgentID,
		SessionID:       run.SessionID,
		TaskID:          run.TaskID,
		ParentTaskID:    run.ParentTaskID,
		Model:           run.Model,
		Channel:         run.Channel,
		Input:           run.Input,
		Output:          run.Output,
		Error:           run.Error,
		Status:          RunStatus(run.Status),
		CreatedAt:       run.CreatedAt,
		StartedAt:       run.StartedAt,
		CompletedAt:     run.CompletedAt,
	}
}

func gatewayRuns(runs []runtimeevents.RunRecord) []Run {
	if len(runs) == 0 {
		return nil
	}
	out := make([]Run, len(runs))
	for i, run := range runs {
		out[i] = gatewayRun(run)
	}
	return out
}

func gatewayEvent(event runtimeevents.EventRecord) Event {
	return Event{
		SchemaVersion:   event.SchemaVersion,
		Sequence:        event.Sequence,
		RunID:           event.RunID,
		RunNodeID:       event.RunNodeID,
		ParentRunNodeID: event.ParentRunNodeID,
		AgentID:         event.AgentID,
		SessionID:       event.SessionID,
		Name:            event.Name,
		Timestamp:       event.Timestamp,
		Payload:         cloneMap(event.Payload),
	}
}

func runtimeEvent(event Event) runtimeevents.EventRecord {
	return runtimeevents.EventRecord{
		SchemaVersion:   event.SchemaVersion,
		Sequence:        event.Sequence,
		RunID:           event.RunID,
		RunNodeID:       event.RunNodeID,
		ParentRunNodeID: event.ParentRunNodeID,
		AgentID:         event.AgentID,
		SessionID:       event.SessionID,
		Name:            event.Name,
		Timestamp:       event.Timestamp,
		Payload:         cloneMap(event.Payload),
	}
}

func gatewayEvents(events []runtimeevents.EventRecord) []Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]Event, len(events))
	for i, event := range events {
		out[i] = gatewayEvent(event)
	}
	return out
}

func gatewaySessionSnapshot(snapshot runtimeport.SessionSnapshot) SessionView {
	return SessionView{
		AgentID: snapshot.AgentID,
		ID:      snapshot.ID,
		History: cloneHistory(snapshot.History),
		Events:  gatewayEvents(snapshot.Events),
	}
}

func gatewayRunTree(tree runtimeevents.RunTreeRecord) RunTree {
	return RunTree{
		Runs:   gatewayRuns(tree.Runs),
		Events: gatewayEvents(tree.Events),
	}
}

func gatewayRunNode(node runtimeevents.RunNode) RunNode {
	children := make([]RunNode, 0, len(node.Children))
	for _, child := range node.Children {
		children = append(children, gatewayRunNode(child))
	}
	return RunNode{
		Run:      gatewayRun(node.Run),
		Events:   gatewayEvents(node.Events),
		Children: children,
	}
}

func gatewayRunNodes(nodes []runtimeevents.RunNode) []RunNode {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]RunNode, len(nodes))
	for i, node := range nodes {
		out[i] = gatewayRunNode(node)
	}
	return out
}

func gatewaySessionInfo(info session.SessionInfo) SessionInfo {
	return SessionInfo{
		ID:           info.ID,
		CreatedAt:    info.CreatedAt,
		LastActivity: info.LastActivity,
		EntryCount:   info.EntryCount,
	}
}

func gatewaySessionInfos(infos []session.SessionInfo) []SessionInfo {
	if len(infos) == 0 {
		return nil
	}
	out := make([]SessionInfo, len(infos))
	for i, info := range infos {
		out[i] = gatewaySessionInfo(info)
	}
	return out
}

func gatewayMemoryStats(stats memory.Stats) MemoryStats {
	return MemoryStats{
		Total:              stats.Total,
		Active:             stats.Active,
		Episodic:           stats.Episodic,
		Candidates:         stats.Candidates,
		LongTerm:           stats.LongTerm,
		Expired:            stats.Expired,
		LastReindexAt:      stats.LastReindexAt,
		LastPromotionAt:    stats.LastPromotionAt,
		LastPromotionCount: stats.LastPromotionCount,
		LastCleanupAt:      stats.LastCleanupAt,
		LastCleanupRemoved: stats.LastCleanupRemoved,
	}
}

func runtimeMemoryScope(scope MemoryScope) memory.SearchScope {
	return memory.SearchScope{AgentID: scope.AgentID, SessionID: scope.SessionID}
}

func runtimeMemoryLayers(layers []MemoryLayer) []memory.Layer {
	if len(layers) == 0 {
		return nil
	}
	out := make([]memory.Layer, len(layers))
	for i, layer := range layers {
		out[i] = memory.Layer(layer)
	}
	return out
}

func gatewayMemoryLayer(layer memory.Layer) MemoryLayer {
	return MemoryLayer(layer)
}

func gatewayMemoryEntry(entry memory.Entry) MemoryEntry {
	return MemoryEntry{
		ID:       entry.ID,
		Title:    entry.Title,
		Content:  entry.Content,
		FilePath: entry.FilePath,
		ModTime:  entry.ModTime,
		Layer:    gatewayMemoryLayer(entry.Layer),
		Metadata: cloneStringMap(entry.Metadata),
	}
}

func gatewayMemoryMatch(match memory.SearchMatch) MemorySearchMatch {
	return MemorySearchMatch{
		Entry:        gatewayMemoryEntry(match.Entry),
		Score:        match.Score,
		MatchedTerms: append([]string(nil), match.MatchedTerms...),
	}
}

func gatewayMemoryMatches(matches []memory.SearchMatch) []MemorySearchMatch {
	if len(matches) == 0 {
		return nil
	}
	out := make([]MemorySearchMatch, len(matches))
	for i, match := range matches {
		out[i] = gatewayMemoryMatch(match)
	}
	return out
}

func gatewayTask(tk task.Info) Task {
	return Task{
		ID:             tk.ID,
		Kind:           string(tk.Kind),
		Status:         string(tk.Status),
		AgentID:        tk.AgentID,
		RunID:          tk.RunID,
		SessionID:      tk.SessionID,
		ParentTaskID:   tk.ParentTaskID,
		IdleTimeoutMS:  tk.IdleTimeoutMS,
		Input:          tk.Input,
		TargetAgent:    tk.TargetAgent,
		ToolName:       tk.ToolName,
		ProcessName:    tk.ProcessName,
		Summary:        tk.Summary,
		Error:          tk.Error,
		Contract:       tk.Contract,
		Metadata:       cloneMap(tk.Metadata),
		CreatedAt:      tk.CreatedAt,
		StartedAt:      tk.StartedAt,
		LastActivityAt: tk.LastActivityAt,
		UpdatedAt:      tk.UpdatedAt,
		CompletedAt:    tk.CompletedAt,
	}
}

func gatewayTasks(tasks []task.Info) []Task {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]Task, len(tasks))
	for i, tk := range tasks {
		out[i] = gatewayTask(tk)
	}
	return out
}

func gatewayJob(job tools.JobInfo) Job {
	return Job{
		Name:     job.Name,
		Schedule: job.Schedule,
		Prompt:   job.Prompt,
		Paused:   job.Paused,
	}
}

func gatewayResourceCatalog(catalog *runtimeresources.Catalog) ResourceCatalog {
	if catalog == nil {
		return ResourceCatalog{}
	}
	return ResourceCatalog{
		SharedSkills: gatewaySkillDescriptors(catalog.SharedSkills()),
		Agents:       gatewayAgentResourcesList(catalog.Agents()),
	}
}

func gatewayAgentResourcesList(items []runtimeresources.AgentResources) []AgentResources {
	if len(items) == 0 {
		return nil
	}
	out := make([]AgentResources, len(items))
	for i, item := range items {
		out[i] = gatewayAgentResources(item)
	}
	return out
}

func gatewayAgentResources(item runtimeresources.AgentResources) AgentResources {
	return AgentResources{
		Agent:           item.Agent,
		SharedSkills:    gatewaySkillDescriptors(item.SharedSkills),
		PrivateSkills:   gatewaySkillDescriptors(item.PrivateSkills),
		EffectiveSkills: gatewaySkillDescriptors(item.EffectiveSkills),
		Tools:           gatewayToolDescriptors(item.Tools),
	}
}

func gatewaySkillDescriptors(items []runtimeresources.SkillDescriptor) []SkillDescriptor {
	if len(items) == 0 {
		return nil
	}
	out := make([]SkillDescriptor, len(items))
	for i, item := range items {
		out[i] = SkillDescriptor{
			Name:        item.Name,
			Description: item.Description,
			Tags:        append([]string(nil), item.Tags...),
			Scope:       item.Scope,
			Source:      item.Source,
		}
	}
	return out
}

func gatewayToolDescriptors(items []runtimeresources.ToolDescriptor) []ToolDescriptor {
	if len(items) == 0 {
		return nil
	}
	out := make([]ToolDescriptor, len(items))
	for i, item := range items {
		out[i] = ToolDescriptor{
			Name:        item.Name,
			Description: item.Description,
			Metadata:    gatewayToolMetadata(item.Metadata),
		}
	}
	return out
}

func gatewayToolMetadata(meta tools.ToolMetadata) ToolMetadata {
	return ToolMetadata{
		Name:             meta.Name,
		TimeoutHintMS:    meta.TimeoutHintMS,
		Effect:           string(meta.Effect),
		Tags:             append([]string(nil), meta.Tags...),
		AllowParallel:    meta.AllowParallel,
		RequiresApproval: meta.RequiresApproval,
	}
}

func gatewayJobs(jobs []tools.JobInfo) []Job {
	if len(jobs) == 0 {
		return nil
	}
	out := make([]Job, len(jobs))
	for i, job := range jobs {
		out[i] = gatewayJob(job)
	}
	return out
}

func gatewayLogEntry(entry logging.LogEntry) LogEntry {
	return LogEntry{
		Time:    entry.Time,
		Level:   entry.Level.String(),
		Message: entry.Message,
		Attrs:   entry.Attrs,
	}
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneHistory(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make([]map[string]any, len(src))
	for i, item := range src {
		out[i] = cloneMap(item)
	}
	return out
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
