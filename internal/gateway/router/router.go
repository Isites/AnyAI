package router

import "github.com/Isites/anyai/internal/config"

// Message is the minimal routing input required to resolve an ingress target.
type Message struct {
	Channel   string
	SenderID  string
	AccountID string
	ChatType  string
}

// Router matches inbound messages to agent IDs using binding rules.
type Router struct {
	bindings []config.Binding
	fallback string
}

// NewRouter creates a new gateway-owned message router.
func NewRouter(bindings []config.Binding, fallbackAgentID string) *Router {
	return &Router{
		bindings: bindings,
		fallback: fallbackAgentID,
	}
}

// Resolve adapts the router to the gateway route resolver contract without
// leaking the concrete Message type outside the router package.
func (r *Router) Resolve(channel, senderID, accountID, chatType string) string {
	return r.Route(Message{
		Channel:   channel,
		SenderID:  senderID,
		AccountID: accountID,
		ChatType:  chatType,
	})
}

// Route returns the agent ID that should handle the given message.
// Matching priority: peer.id > peer.kind > accountId > channel > default.
func (r *Router) Route(msg Message) string {
	var channelMatch string

	for _, b := range r.bindings {
		m := b.Match

		if m.Peer != nil && m.Peer.ID != "" && m.Peer.ID == msg.SenderID {
			if m.Channel == "" || m.Channel == msg.Channel {
				return b.AgentID
			}
		}

		if m.Peer != nil && m.Peer.Kind != "" && m.Peer.Kind == msg.ChatType {
			if m.Channel == "" || m.Channel == msg.Channel {
				return b.AgentID
			}
		}

		if m.AccountID != "" && m.AccountID == msg.AccountID {
			if m.Channel == "" || m.Channel == msg.Channel {
				return b.AgentID
			}
		}

		if m.Channel == msg.Channel && m.Peer == nil && m.AccountID == "" {
			channelMatch = b.AgentID
		}
	}

	if channelMatch != "" {
		return channelMatch
	}

	return r.fallback
}

// IsKnownPeer returns true if the given sender ID appears as a peer.id in any binding.
func (r *Router) IsKnownPeer(senderID string) bool {
	for _, b := range r.bindings {
		if b.Match.Peer != nil && b.Match.Peer.ID == senderID {
			return true
		}
	}
	return false
}
