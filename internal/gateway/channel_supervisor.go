package gateway

import (
	"context"
	"strings"
)

// processChannel keeps the adapter thin: it forwards inbound messages to the
// runtime dispatcher and relies on runtime/sessionlane for per-session
// ordering, queueing, and interrupt/collect policies.
func (cm *ChannelManager) processChannel(ctx context.Context, ch Channel) {
	defer cm.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch.Receive():
			if !ok {
				return
			}

			go cm.dispatch.handle(ctx, ch, msg)
		}
	}
}

func responseChatID(msg InboundMessage) string {
	chatID := strings.TrimSpace(msg.AccountID)
	if chatID != "" {
		return chatID
	}
	return strings.TrimSpace(msg.SenderID)
}
