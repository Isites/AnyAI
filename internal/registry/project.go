package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Isites/anyai/internal/config"
	agentdoc "github.com/Isites/anyai/internal/runtime/agent"
)

type LoadMode string

const (
	LoadModeDirectory LoadMode = "directory"
	LoadModeFile      LoadMode = "file"
)

// Project is a resolved AnyAI project ready for runtime use.
type Project struct {
	InputPath       string
	RootDir         string
	ConfigPath      string
	Mode            LoadMode
	FileModeSingle  bool
	ExplicitEntryID string
	SharedSkillsDir string
	SharedMCPsDir   string
	MemoryDir       string
	Agents          []ProjectAgent
	Config          *config.Config
	ProjectConfig   *config.ProjectConfig
}

// ProjectAgent is a scanned agent.md plus resolved runtime config.
type ProjectAgent struct {
	ID       string
	FilePath string
	Dir      string
	Config   config.AgentConfig
}

// LoadProject resolves a path into an AnyAI project and produces a runtime config.
func LoadProject(path string) (*Project, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat path: %w", err)
	}

	mode := LoadModeDirectory
	if !info.IsDir() {
		if filepath.Base(absPath) != "agent.md" {
			return nil, fmt.Errorf("file mode requires an agent.md file, got %q", filepath.Base(absPath))
		}
		mode = LoadModeFile
	}

	rootDir, configPath, fileModeSingle, err := resolveProjectRoot(absPath, info.IsDir())
	if err != nil {
		return nil, err
	}

	var projectCfg *config.ProjectConfig
	if configPath != "" {
		projectCfg, err = config.LoadProject(configPath)
		if err != nil {
			return nil, err
		}
	}

	agentPaths, err := scanAgentFiles(rootDir, absPath, mode, fileModeSingle)
	if err != nil {
		return nil, err
	}
	if len(agentPaths) == 0 {
		return nil, fmt.Errorf("no agent.md found under %s", rootDir)
	}

	project := &Project{
		InputPath:      absPath,
		RootDir:        rootDir,
		ConfigPath:     configPath,
		Mode:           mode,
		FileModeSingle: fileModeSingle,
		ProjectConfig:  projectCfg,
	}
	project.SharedSkillsDir = existingDir(filepath.Join(rootDir, "common", "skills"))
	project.SharedMCPsDir = existingDir(filepath.Join(rootDir, "common", "mcps"))
	project.MemoryDir = existingDir(filepath.Join(rootDir, "anyai", "memory"))

	if err := project.buildAgents(agentPaths); err != nil {
		return nil, err
	}

	project.Config, err = project.buildRuntimeConfig()
	if err != nil {
		return nil, err
	}

	if mode == LoadModeFile {
		for _, a := range project.Agents {
			if a.FilePath == absPath {
				project.ExplicitEntryID = a.ID
				break
			}
		}
		if project.ExplicitEntryID == "" {
			return nil, fmt.Errorf("explicit entry %s was not loaded", absPath)
		}
	}

	return project, nil
}

// SelectEntry returns the entry agent based on the design's precedence rules.
func (p *Project) SelectEntry(overrideAgentID string) (*config.AgentConfig, error) {
	if p == nil || p.Config == nil {
		return nil, fmt.Errorf("project is not loaded")
	}

	if p.Mode == LoadModeFile {
		if overrideAgentID != "" {
			return nil, fmt.Errorf("--agent cannot be used when running a specific agent.md file")
		}
		agentCfg, ok := p.Config.GetAgent(p.ExplicitEntryID)
		if !ok {
			return nil, fmt.Errorf("entry agent %q not found", p.ExplicitEntryID)
		}
		return agentCfg, nil
	}

	if overrideAgentID != "" {
		agentCfg, ok := p.Config.GetAgent(overrideAgentID)
		if !ok {
			return nil, fmt.Errorf("agent %q not found", overrideAgentID)
		}
		return agentCfg, nil
	}

	var entryAgents []config.AgentConfig
	for _, a := range p.Config.Agents.List {
		if a.Entry {
			entryAgents = append(entryAgents, a)
		}
	}
	if len(entryAgents) == 1 {
		agentCfg, _ := p.Config.GetAgent(entryAgents[0].ID)
		return agentCfg, nil
	}

	if rootAgentID := p.rootAgentID(); rootAgentID != "" {
		agentCfg, _ := p.Config.GetAgent(rootAgentID)
		return agentCfg, nil
	}

	if len(p.Config.Agents.List) == 1 {
		return &p.Config.Agents.List[0], nil
	}

	var ids []string
	for _, a := range p.Config.Agents.List {
		ids = append(ids, a.ID)
	}
	return nil, fmt.Errorf("multiple agents found and no unique entry could be determined; choose one of: %s", strings.Join(ids, ", "))
}

func (p *Project) rootAgentID() string {
	for _, a := range p.Agents {
		if filepath.Clean(a.Dir) == filepath.Clean(p.RootDir) {
			return a.ID
		}
	}
	return ""
}

func resolveProjectRoot(absPath string, isDir bool) (rootDir, configPath string, fileModeSingle bool, err error) {
	if isDir {
		rootDir = absPath
		configPath = findAnyAIConfig(rootDir)
		return rootDir, configPath, false, nil
	}

	dir := filepath.Dir(absPath)
	if cfgPath := findAnyAIConfigUpwards(dir); cfgPath != "" {
		return filepath.Dir(cfgPath), cfgPath, false, nil
	}
	return dir, "", true, nil
}

func findAnyAIConfig(dir string) string {
	for _, name := range []string{"anyai.yaml", "anyai.yml"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func findAnyAIConfigUpwards(dir string) string {
	current := dir
	for {
		if path := findAnyAIConfig(current); path != "" {
			return path
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func scanAgentFiles(rootDir, inputPath string, mode LoadMode, fileModeSingle bool) ([]string, error) {
	if mode == LoadModeFile && fileModeSingle {
		return []string{inputPath}, nil
	}

	var paths []string
	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "agent.md" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan agents: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

func (p *Project) buildAgents(agentPaths []string) error {
	agents := make([]ProjectAgent, 0, len(agentPaths))
	seenIDs := map[string]string{}
	entryCount := 0

	for _, path := range agentPaths {
		doc, err := agentdoc.ParseFile(path)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}

		dir := filepath.Dir(path)
		isRootAgent := filepath.Clean(dir) == filepath.Clean(p.RootDir)
		id := strings.TrimSpace(doc.ID)
		if id == "" {
			if isRootAgent {
				id = filepath.Base(p.RootDir)
			} else {
				id = filepath.Base(dir)
			}
		}
		if prev, ok := seenIDs[id]; ok {
			return fmt.Errorf("duplicate agent id %q in %s and %s", id, prev, path)
		}
		seenIDs[id] = path

		workspace := strings.TrimSpace(doc.Workspace)
		if workspace == "" {
			workspace = dir
		} else if !filepath.IsAbs(workspace) {
			workspace = filepath.Join(dir, workspace)
		}
		workspace, err = filepath.Abs(workspace)
		if err != nil {
			return fmt.Errorf("resolve workspace for %s: %w", path, err)
		}
		if !isWithin(workspace, p.RootDir) {
			return fmt.Errorf("workspace %q for agent %q is outside project root %q", workspace, id, p.RootDir)
		}

		inheritShared := true
		if doc.Skills.InheritShared != nil {
			inheritShared = *doc.Skills.InheritShared
		}
		inheritSharedMCPs := true
		if doc.MCPs.InheritShared != nil {
			inheritSharedMCPs = *doc.MCPs.InheritShared
		}

		model := doc.Model
		if p.ProjectConfig != nil {
			model = p.ProjectConfig.ResolveModel(model)
		}
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("agent %q has no model and project has no models.default", id)
		}

		fallbacks := append([]string(nil), doc.Fallbacks...)
		if p.ProjectConfig != nil {
			for i, fallback := range fallbacks {
				fallbacks[i] = p.ProjectConfig.ResolveModel(fallback)
			}
		}

		agentCfg := config.AgentConfig{
			ID:                  id,
			Name:                firstNonEmpty(doc.Name, id),
			Description:         doc.Description,
			Workspace:           workspace,
			Model:               model,
			Fallbacks:           fallbacks,
			MaxTurns:            doc.MaxTurns,
			SystemPrompt:        doc.Body,
			Tools:               config.ToolPolicy{Allow: doc.Tools.Allow, Deny: doc.Tools.Deny},
			Entry:               doc.Entry,
			Tags:                doc.Tags,
			PrivateSkillsDir:    existingDir(filepath.Join(dir, "skills")),
			InheritSharedSkills: inheritShared,
			PrivateMCPsDir:      existingDir(filepath.Join(dir, "mcps")),
			InheritSharedMCPs:   inheritSharedMCPs,
		}
		if doc.Entry {
			entryCount++
		}

		agents = append(agents, ProjectAgent{
			ID:       id,
			FilePath: path,
			Dir:      dir,
			Config:   agentCfg,
		})
	}

	if entryCount > 1 {
		return fmt.Errorf("multiple agents declare entry: true")
	}

	p.Agents = agents
	return nil
}

func (p *Project) buildRuntimeConfig() (*config.Config, error) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = nil
	cfg.Bindings = nil
	cfg.ProjectName = filepath.Base(p.RootDir)
	cfg.ProjectRoot = p.RootDir
	cfg.ProjectConfigDir = p.RootDir
	cfg.SharedSkillsDir = p.SharedSkillsDir
	cfg.SharedMCPsDir = p.SharedMCPsDir
	cfg.Memory.Dir = filepath.Join(cfg.RuntimeDataDir(), "memory")

	if p.ProjectConfig != nil {
		cfg.ProjectName = firstNonEmpty(p.ProjectConfig.Name, filepath.Base(p.RootDir))
		cfg.ProjectConfigDir = p.ProjectConfig.Dir()
		cfg.Runtime.IdleTimeoutMS = p.ProjectConfig.Runtime.IdleTimeoutMS
		cfg.Runtime.AgentCall.DepthLimit = p.ProjectConfig.Runtime.AgentCall.DepthLimit
		cfg.Runtime.AgentCall.MaxParallel = p.ProjectConfig.Runtime.AgentCall.MaxParallel
		cfg.Runtime.Tools.MaxAttempts = p.ProjectConfig.Runtime.Tools.MaxAttempts
		cfg.Runtime.Tools.RetryBackoffMS = p.ProjectConfig.Runtime.Tools.RetryBackoffMS
		cfg.Runtime.Tools.LoopDetection = config.ToolLoopDetectionConfig{
			Enabled:          p.ProjectConfig.Runtime.Tools.LoopDetection.Enabled,
			HistorySize:      p.ProjectConfig.Runtime.Tools.LoopDetection.HistorySize,
			WarningThreshold: p.ProjectConfig.Runtime.Tools.LoopDetection.WarningThreshold,
			BlockThreshold:   p.ProjectConfig.Runtime.Tools.LoopDetection.BlockThreshold,
		}
		cfg.Runtime.Sessions.Compaction = config.SessionCompactionConfig{
			Enabled:              config.CloneBoolPtr(p.ProjectConfig.Runtime.Sessions.Compaction.Enabled),
			TriggerMode:          p.ProjectConfig.Runtime.Sessions.Compaction.TriggerMode,
			EntryThreshold:       p.ProjectConfig.Runtime.Sessions.Compaction.EntryThreshold,
			TokenThreshold:       p.ProjectConfig.Runtime.Sessions.Compaction.TokenThreshold,
			KeepRecentUserTurns:  p.ProjectConfig.Runtime.Sessions.Compaction.KeepRecentUserTurns,
			KeepRecentUserTokens: p.ProjectConfig.Runtime.Sessions.Compaction.KeepRecentUserTokens,
			SummaryMaxTokens:     p.ProjectConfig.Runtime.Sessions.Compaction.SummaryMaxTokens,
		}
		cfg.Logging = config.LoggingConfig{
			FileLevel:      p.ProjectConfig.Logging.FileLevel,
			StderrLevel:    p.ProjectConfig.Logging.StderrLevel,
			WhatsMeowLevel: p.ProjectConfig.Logging.WhatsMeowLevel,
			MirrorStderr:   config.CloneBoolPtr(p.ProjectConfig.Logging.MirrorStderr),
			Rotation: config.LogRotationConfig{
				Filename:   p.ProjectConfig.Logging.Rotation.Filename,
				MaxBytes:   p.ProjectConfig.Logging.Rotation.MaxBytes,
				MaxBackups: p.ProjectConfig.Logging.Rotation.MaxBackups,
			},
		}
		cfg.Memory.Enabled = p.ProjectConfig.Memory.EnabledValue()
		cfg.Memory.AutoCapture = config.MemoryAutoCaptureConfig{
			Enabled:              p.ProjectConfig.Memory.AutoCapture.Enabled,
			OnSessionEnd:         p.ProjectConfig.Memory.AutoCapture.OnSessionEnd,
			OnAgentCallComplete:  p.ProjectConfig.Memory.AutoCapture.OnAgentCallComplete,
			OnUserConfirmed:      p.ProjectConfig.Memory.AutoCapture.OnUserConfirmed,
			OnHighValueDecision:  p.ProjectConfig.Memory.AutoCapture.OnHighValueDecision,
			MinImportance:        p.ProjectConfig.Memory.AutoCapture.MinImportance,
			CandidateTTL:         p.ProjectConfig.Memory.AutoCapture.CandidateTTL,
			EpisodicTTL:          p.ProjectConfig.Memory.AutoCapture.EpisodicTTL,
			CleanupInterval:      p.ProjectConfig.Memory.AutoCapture.CleanupInterval,
			PromoteMinImportance: p.ProjectConfig.Memory.AutoCapture.PromoteMinImportance,
		}
		cfg.Memory.Inject = config.MemoryInjectConfig{
			MaxItems: p.ProjectConfig.Memory.Inject.MaxItems,
		}
		if cfg.Memory.AutoCapture.Enabled {
			cfg.Memory.Enabled = true
		}
		providers, err := buildProviderConfigs(p.ProjectConfig)
		if err != nil {
			return nil, err
		}
		cfg.Providers = providers
		applyProjectChannelConfig(cfg, p.ProjectConfig)
		applyProjectSecurityConfig(cfg, p.ProjectConfig)
	}

	entryID := p.rootAgentID()
	if len(p.Agents) == 1 {
		entryID = p.Agents[0].ID
	}
	if uniqueEntry := uniqueEntryID(p.Agents); uniqueEntry != "" {
		entryID = uniqueEntry
	}

	agents := append([]ProjectAgent(nil), p.Agents...)
	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].ID == entryID {
			return true
		}
		if agents[j].ID == entryID {
			return false
		}
		return agents[i].ID < agents[j].ID
	})
	for _, a := range agents {
		cfg.Agents.List = append(cfg.Agents.List, a.Config)
	}

	if entryID == "" && len(cfg.Agents.List) > 0 {
		entryID = cfg.Agents.List[0].ID
	}

	if entryID != "" {
		cfg.Bindings = []config.Binding{
			{
				AgentID: entryID,
				Match: config.BindingMatch{
					Channel: "cli",
					Peer:    &config.PeerMatch{ID: "local"},
				},
			},
			{
				AgentID: entryID,
				Match:   config.BindingMatch{Channel: "cli"},
			},
			{
				AgentID: entryID,
				Match:   config.BindingMatch{Channel: "http"},
			},
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func buildProviderConfigs(projectCfg *config.ProjectConfig) (map[string]config.ProviderConfig, error) {
	providers := map[string]config.ProviderConfig{}
	if projectCfg == nil {
		return providers, nil
	}
	for name, provider := range projectCfg.Providers {
		apiKey := provider.APIKey
		if provider.APIKeyEnv != "" {
			apiKey = os.Getenv(provider.APIKeyEnv)
		}
		baseURL := provider.BaseURL
		if provider.BaseURLEnv != "" {
			if v := os.Getenv(provider.BaseURLEnv); v != "" {
				baseURL = v
			}
		}
		kind := provider.Kind
		if kind == "" {
			kind = name
		}

		headers := config.CloneHeaders(provider.Headers)
		if provider.HeadersEnv != "" {
			envHeaders, err := config.ParseHeadersJSON(os.Getenv(provider.HeadersEnv))
			if err != nil {
				return nil, fmt.Errorf("provider %q headers_env %q: %w", name, provider.HeadersEnv, err)
			}
			headers = config.MergeHeaders(headers, envHeaders)
		}

		providers[name] = config.ProviderConfig{
			Kind:    kind,
			BaseURL: baseURL,
			APIKey:  apiKey,
			Headers: headers,
		}
	}
	return providers, nil
}

func applyProjectChannelConfig(cfg *config.Config, projectCfg *config.ProjectConfig) {
	if cfg == nil || projectCfg == nil {
		return
	}
	enabled := map[string]bool{}
	for _, name := range projectCfg.Channels.Gateway.Enabled {
		enabled[strings.ToLower(strings.TrimSpace(name))] = true
	}
	if len(enabled) > 0 {
		cfg.Channels.CLI.Enabled = enabled["cli"]
	}
	if listen := strings.TrimSpace(projectCfg.Channels.HTTP.Listen); listen != "" {
		if host, port, err := splitListenAddr(listen); err == nil {
			cfg.Gateway.Host = host
			cfg.Gateway.Port = port
		}
	}
	if token := strings.TrimSpace(projectCfg.Channels.Telegram.Token); token != "" {
		cfg.Channels.Telegram.Token = token
	}
	if env := strings.TrimSpace(projectCfg.Channels.Telegram.TokenEnv); env != "" {
		if token := os.Getenv(env); token != "" {
			cfg.Channels.Telegram.Token = token
		}
	}
	if dbPath := strings.TrimSpace(projectCfg.Channels.WhatsApp.DBPath); dbPath != "" {
		cfg.Channels.WhatsApp.DBPath = dbPath
	}
}

func applyProjectSecurityConfig(cfg *config.Config, projectCfg *config.ProjectConfig) {
	if cfg == nil || projectCfg == nil {
		return
	}
	if level := strings.TrimSpace(projectCfg.Security.ExecApprovals.Level); level != "" {
		cfg.Security.ExecApprovals.Level = level
	}
	if len(projectCfg.Security.ExecApprovals.Allowlist) > 0 {
		cfg.Security.ExecApprovals.Allowlist = append([]string(nil), projectCfg.Security.ExecApprovals.Allowlist...)
	}
	if policy := strings.TrimSpace(projectCfg.Security.DMPolicy.UnknownSenders); policy != "" {
		cfg.Security.DMPolicy.UnknownSenders = policy
	}
	cfg.Security.GroupPolicy.RequireMention = projectCfg.Security.GroupPolicy.RequireMention
}

func uniqueEntryID(agents []ProjectAgent) string {
	var entryID string
	for _, a := range agents {
		if !a.Config.Entry {
			continue
		}
		if entryID != "" {
			return ""
		}
		entryID = a.ID
	}
	return entryID
}

func splitListenAddr(listen string) (string, int, error) {
	host, portStr, ok := strings.Cut(listen, ":")
	if !ok || portStr == "" {
		return "", 0, fmt.Errorf("invalid listen address %q", listen)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return "", 0, fmt.Errorf("invalid listen port %q", listen)
	}
	return host, port, nil
}

func existingDir(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ""
	}
	return path
}

func samePath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftClean := filepath.Clean(left)
	rightClean := filepath.Clean(right)
	if leftClean == rightClean {
		return true
	}
	leftAbs, leftErr := filepath.Abs(leftClean)
	rightAbs, rightErr := filepath.Abs(rightClean)
	if leftErr == nil && rightErr == nil {
		return leftAbs == rightAbs
	}
	return false
}

func isWithin(path, root string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	return absPath == absRoot || strings.HasPrefix(absPath, absRoot+string(filepath.Separator))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
