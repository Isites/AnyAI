package gateway

import (
	"context"
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"sort"
	"sync"
	"time"
)

// ChannelManager bridges channel adapters to the gateway ingress surface.
// It owns channel lifecycle and leaves routing, session derivation, and run
// orchestration to gateway/runtime services.
type ChannelManager struct {
	channels map[string]Channel
	dispatch *dispatcher

	connectTimeout time.Duration // 0 means no timeout (blocks until connected)
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	mu             sync.RWMutex
}

// NewChannelManager creates a new ChannelManager.
func NewChannelManager(runtime ChannelPort, dmPolicy string) *ChannelManager {
	if dmPolicy == "" {
		dmPolicy = "ignore"
	}
	return &ChannelManager{
		channels: make(map[string]Channel),
		dispatch: newDispatcher(runtime, dmPolicy),
	}
}

// SetConnectTimeout sets a per-channel connect timeout. When non-zero,
// channels that don't connect within this duration are skipped.
func (cm *ChannelManager) SetConnectTimeout(d time.Duration) {
	cm.connectTimeout = d
}

// Register adds a channel adapter.
func (cm *ChannelManager) Register(ch Channel) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.channels[ch.Name()] = ch
}

// Start connects all channels and launches message processing goroutines.
func (cm *ChannelManager) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	cm.cancel = cancel

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for name, ch := range cm.channels {
		var err error
		if cm.connectTimeout > 0 {
			done := make(chan error, 1)
			go func(c Channel) {
				done <- c.Connect(ctx)
			}(ch)
			select {
			case err = <-done:
			case <-time.After(cm.connectTimeout):
				runtimelogging.Warn("channel connect timed out, skipping", "channel", name)
				continue
			}
		} else {
			err = ch.Connect(ctx)
		}
		if err != nil {
			runtimelogging.Warn("failed to connect channel, skipping", "channel", name, "error", err)
			continue
		}

		cm.wg.Add(1)
		go cm.processChannel(ctx, ch)
	}

	return nil
}

// Stop disconnects all channels and waits for goroutines to finish.
func (cm *ChannelManager) Stop() {
	if cm.cancel != nil {
		cm.cancel()
	}

	cm.mu.RLock()
	for _, ch := range cm.channels {
		if err := ch.Disconnect(); err != nil {
			runtimelogging.Error("disconnect channel", "channel", ch.Name(), "error", err)
		}
	}
	cm.mu.RUnlock()

	cm.wg.Wait()
}

// SendToChannel sends a message to a specific channel and chat ID.
func (cm *ChannelManager) SendToChannel(ctx context.Context, channelName, chatID, text string) error {
	cm.mu.RLock()
	ch, ok := cm.channels[channelName]
	cm.mu.RUnlock()

	if !ok {
		return fmt.Errorf("channel %q not connected", channelName)
	}

	return ch.Send(ctx, OutboundMessage{
		ChatID: chatID,
		Text:   text,
	})
}

// AvailableChannels returns the names of all connected channels.
func (cm *ChannelManager) AvailableChannels() []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	names := make([]string, 0, len(cm.channels))
	for name := range cm.channels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Channels returns the current channel inventory as a read-only view.
func (cm *ChannelManager) Channels() []ChannelInfo {
	if cm == nil {
		return nil
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	names := make([]string, 0, len(cm.channels))
	for name := range cm.channels {
		names = append(names, name)
	}
	sort.Strings(names)

	views := make([]ChannelInfo, 0, len(names))
	for _, name := range names {
		if ch := cm.channels[name]; ch != nil {
			views = append(views, ch)
		}
	}
	return views
}

type channelInventoryEntry struct {
	name   string
	status ChannelStatus
}

func (e channelInventoryEntry) Name() string {
	return e.name
}

func (e channelInventoryEntry) Status() ChannelStatus {
	return e.status
}

func defaultChannelInventory(names []string) []ChannelInfo {
	out := make([]ChannelInfo, 0, len(names))
	for _, name := range names {
		out = append(out, channelInventoryEntry{
			name:   name,
			status: StatusDisconnected,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})
	return out
}
