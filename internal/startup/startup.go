// Package startup provides shared gateway startup logic used by both the
// CLI (cmd/) and any future embedded launchers.
package startup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"

	"github.com/Isites/anyai/internal/channel"
	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	"github.com/Isites/anyai/internal/registry"
	"github.com/Isites/anyai/internal/runtime/daemon"
	"github.com/Isites/anyai/internal/runtime/llm"
	"github.com/Isites/anyai/internal/runtime/task"
	tools "github.com/Isites/anyai/internal/runtime/tool"
	httpchannel "github.com/Isites/anyai/internal/startup/http"
)

// Result holds the running runtime components for either `anyai start` or
// `anyai chat`.
type Result struct {
	httpService    *httpchannel.Service
	gateway        *gateway.Service
	config         *config.Config
	channelManager *gateway.ChannelManager
	cliChannel     *channel.CLIChannel
	taskStore      *task.Store
	cleanup        func()
	cleanupOnce    sync.Once
}

// Config returns the active runtime configuration snapshot.
func (r *Result) Config() *config.Config {
	if r == nil {
		return nil
	}
	return r.config
}

// ChannelManager returns the shared channel manager.
func (r *Result) ChannelManager() *gateway.ChannelManager {
	if r == nil {
		return nil
	}
	return r.channelManager
}

// Gateway returns the isolated gateway layer that exposes runtime abilities to
// channels and hosted transports.
func (r *Result) Gateway() *gateway.Service {
	if r == nil {
		return nil
	}
	return r.gateway
}

func (r *Result) TaskStore() *task.Store {
	if r == nil {
		return nil
	}
	return r.taskStore
}

// ActiveChannels reports the currently registered channel names.
func (r *Result) ActiveChannels() []string {
	if r == nil || r.channelManager == nil {
		return nil
	}
	return r.channelManager.AvailableChannels()
}

// CLIChannel returns the interactive/headless CLI channel when chat launch
// mode is active.
func (r *Result) CLIChannel() *channel.CLIChannel {
	if r == nil {
		return nil
	}
	return r.cliChannel
}

// CanServe reports whether this launch has a hosted HTTP server surface.
func (r *Result) CanServe() bool {
	return r != nil && r.httpService != nil
}

// Serve starts the hosted HTTP server when present. Chat-mode launches simply
// return immediately because they do not host an HTTP listener.
func (r *Result) Serve() error {
	if r == nil || r.httpService == nil {
		return nil
	}
	return r.httpService.Serve()
}

// Wait blocks until the interactive foreground channel exits or the context is
// canceled. Non-interactive launches return immediately.
func (r *Result) Wait(ctx context.Context) error {
	if r == nil || r.cliChannel == nil {
		return nil
	}
	select {
	case <-r.cliChannel.Done():
		return nil
	case <-ctx.Done():
		_ = r.cliChannel.Disconnect()
		<-r.cliChannel.Done()
		return nil
	}
}

// Cleanup gracefully tears down the runtime once. It is safe to call multiple
// times.
func (r *Result) Cleanup() {
	if r == nil {
		return
	}
	r.cleanupOnce.Do(func() {
		if r.cleanup != nil {
			r.cleanup()
		}
	})
}

// LaunchMode controls which channels a given entrypoint exposes at runtime.
type LaunchMode string

const (
	LaunchModeChat  LaunchMode = "chat"
	LaunchModeStart LaunchMode = "start"
)

// Options configures gateway startup behavior.
type Options struct {
	// ConnectTimeout is the per-channel connect timeout. Zero means no
	// timeout (channels block until connected). Set this for headless
	// environments where interactive channel setup is not possible.
	ConnectTimeout time.Duration
	// FallbackAgentID overrides the router fallback agent used when a message
	// does not match any explicit binding. CLI chat uses this to honor the
	// selected entry agent without duplicating startup wiring.
	FallbackAgentID string
	// LaunchMode tells the runtime which channels are actually active for this
	// process so system prompts and startup wiring stay aligned.
	LaunchMode LaunchMode
	// NonInteractive disables interactive surfaces. This is mainly useful for
	// tests that need the unified startup path without launching a real UI.
	NonInteractive bool
	// ProviderOverrides injects or replaces initialized providers so tests and
	// embedded runtimes can exercise the full startup path without external LLMs.
	ProviderOverrides map[string]llm.LLMProvider
}

func applyLaunchModeChannels(cfg *config.Config, mode LaunchMode, dataDir string) {
	if cfg == nil || mode == "" {
		return
	}

	switch mode {
	case LaunchModeChat:
		cfg.ActiveChannels = []string{"cli"}
	case LaunchModeStart:
		channels := []string{}
		if strings.TrimSpace(cfg.Channels.Telegram.Token) != "" {
			channels = append(channels, "telegram")
		}
		if resolveWhatsAppDBPath(cfg, dataDir) != "" {
			channels = append(channels, "whatsapp")
		}
		cfg.ActiveChannels = channels
	default:
		cfg.ActiveChannels = nil
	}
}

func resolveWhatsAppDBPath(cfg *config.Config, dataDir string) string {
	if cfg == nil {
		return ""
	}
	if dbPath := strings.TrimSpace(cfg.Channels.WhatsApp.DBPath); dbPath != "" {
		return dbPath
	}
	defaultDB := filepath.Join(dataDir, "whatsapp.db")
	if _, err := os.Stat(defaultDB); err == nil {
		return defaultDB
	}
	return ""
}

// RegisterCLIChannel attaches the Bubble Tea CLI as the only inbound channel
// for `anyai chat`.
func RegisterCLIChannel(core *CoreRuntime, entryAgentID string, nonInteractive bool) *channel.CLIChannel {
	if core == nil {
		return nil
	}
	projectRoot := ""
	if core.Config != nil {
		projectRoot = core.Config.ProjectRoot
	}
	cliChan := channel.NewCLIChannel(projectRoot, entryAgentID)
	if nonInteractive {
		cliChan = channel.NewHeadlessCLIChannel(projectRoot, entryAgentID)
	}
	if core.Gateway != nil {
		cliChan.SetSessionSurface(core.Gateway)
	}
	core.ChannelManager.Register(cliChan)
	return cliChan
}

func registerLaunchChannels(core *CoreRuntime, opt Options) *channel.CLIChannel {
	if core == nil {
		return nil
	}

	switch opt.LaunchMode {
	case LaunchModeChat:
		entryAgentID := strings.TrimSpace(opt.FallbackAgentID)
		if entryAgentID == "" && core.Config != nil && len(core.Config.Agents.List) > 0 {
			entryAgentID = core.Config.Agents.List[0].ID
		}
		return RegisterCLIChannel(core, entryAgentID, opt.NonInteractive)
	case LaunchModeStart:
		registerConfiguredMessagingChannels(core)
	}

	return nil
}

func registerConfiguredMessagingChannels(core *CoreRuntime) {
	if core == nil || core.ChannelManager == nil || core.Config == nil {
		return
	}

	if strings.TrimSpace(core.Config.Channels.Telegram.Token) != "" {
		tgChan := channel.NewTelegramChannel(
			core.Config.Channels.Telegram.Token,
			core.Config.Security.GroupPolicy.RequireMention,
		)
		core.ChannelManager.Register(tgChan)
		runtimelogging.Info("telegram channel registered")
	}

	if waDBPath := resolveWhatsAppDBPath(core.Config, core.DataDir); waDBPath != "" {
		waChan := channel.NewWhatsAppChannel(waDBPath, core.Config.Channels.WhatsApp.AllowedSenders)
		core.ChannelManager.Register(waChan)
		runtimelogging.Info("whatsapp channel registered")
	}
}

// StartGateway starts the full gateway server and returns the runtime handle.
// The caller is responsible for calling Result.Cleanup() on shutdown and, for
// hosted launches, starting the HTTP server via Result.Serve() in a goroutine.
func StartGateway(configPath, version string, opts ...Options) (*Result, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return StartGatewayWithConfig(cfg, version, opts...)
}

// StartGatewayWithConfig starts the full gateway server from an already-loaded config.
func StartGatewayWithConfig(cfg *config.Config, version string, opts ...Options) (*Result, error) {
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.LaunchMode == "" {
		opt.LaunchMode = LaunchModeStart
	}
	logRuntime, err := configureRuntimeLogging(cfg, opt.LaunchMode)
	if err != nil {
		return nil, err
	}
	cleanupLogging := true
	defer func() {
		if cleanupLogging && logRuntime != nil {
			_ = logRuntime.Close()
		}
	}()

	core, err := BuildCoreRuntimeWithConfig(cfg, opt)
	if err != nil {
		return nil, err
	}

	toolReg := core.ToolRegistry
	runtimeService := core.Runtime
	dependencies := runtimeService.Dependencies()
	gatewayService := core.Gateway
	chanMgr := core.ChannelManager
	dataDir := core.DataDir
	var resultHandle *Result
	updater := newRuntimeUpdater(core, opt, func(nextCfg *config.Config) {
		if resultHandle != nil {
			resultHandle.config = nextCfg
		}
	})

	var httpService *httpchannel.Service
	if gatewayService != nil {
		gatewayService.SetVersion(version)
		gatewayService.SetLogBuffer(logRuntime.Buffer())
		gatewayService.SetConfigSaveHook(func(newCfg *config.Config) {
			_ = updater.ApplyConfig(context.Background(), newCfg, "config updated via gateway control surface")
		})
	}
	httpService = httpchannel.NewService(httpchannel.ServiceOptions{
		Config:  cfg,
		Gateway: gatewayService,
	})

	cliChannel := registerLaunchChannels(core, opt)

	// Config / project hot-reload
	var configWatcher *config.Watcher
	var hotWatcher *projectWatcher
	if !strings.EqualFold(strings.TrimSpace(cfg.Gateway.Reload.Mode), "manual") && strings.TrimSpace(cfg.ProjectRoot) != "" {
		watcher, err := newProjectWatcher(cfg.ProjectRoot, dataDir, func() {
			project, loadErr := registry.LoadProject(cfg.ProjectRoot)
			if loadErr != nil {
				runtimelogging.Error("failed to reload project", "root", cfg.ProjectRoot, "error", loadErr)
				return
			}
			_ = updater.ApplyProject(context.Background(), project, "project hot-reloaded")
		})
		if err == nil {
			watcher.Start()
			hotWatcher = watcher
		} else {
			runtimelogging.Warn("project watcher not started", "error", err)
		}
	} else if cfg.Path() != "" {
		watcher, err := config.NewWatcher(cfg.Path(), func(newCfg *config.Config) {
			_ = updater.ApplyConfig(context.Background(), newCfg, "config hot-reloaded")
		})
		if err == nil {
			watcher.Start()
			configWatcher = watcher
		} else {
			runtimelogging.Warn("config watcher not started", "error", err)
		}
	}

	// Start channel manager
	ctx := context.Background()
	if err := chanMgr.Start(ctx); err != nil {
		return nil, fmt.Errorf("start channel manager: %w", err)
	}

	daemonBundle, err := daemon.Start(daemon.Options{
		Ctx:     ctx,
		Config:  cfg,
		Runtime: core.Runtime,
	})
	if err != nil {
		return nil, fmt.Errorf("start daemon bundle: %w", err)
	}
	if jobScheduler := daemonBundle.JobScheduler(); jobScheduler != nil {
		tools.RegisterCron(toolReg, jobScheduler)
		dependencies.SetJobScheduler(jobScheduler)
	}

	cleanup := func() {
		if daemonBundle != nil {
			daemonBundle.Stop()
		}
		chanMgr.Stop()
		if configWatcher != nil {
			configWatcher.Stop()
		}
		if hotWatcher != nil {
			hotWatcher.Stop()
		}
		if httpService != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			httpService.Shutdown(shutdownCtx)
		}
		if logRuntime != nil {
			_ = logRuntime.Close()
		}
	}
	cleanupLogging = false

	resultHandle = &Result{
		httpService:    httpService,
		gateway:        gatewayService,
		config:         cfg,
		channelManager: chanMgr,
		cliChannel:     cliChannel,
		taskStore:      core.TaskStore,
		cleanup:        cleanup,
	}
	return resultHandle, nil
}
