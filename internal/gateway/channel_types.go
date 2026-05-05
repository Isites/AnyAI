package gateway

import (
	"context"
	"time"

	"github.com/Isites/anyai/internal/runtime/input"
)

// ChannelStatus describes the state of a channel.
type ChannelStatus string

const (
	StatusDisconnected ChannelStatus = "disconnected"
	StatusConnecting   ChannelStatus = "connecting"
	StatusConnected    ChannelStatus = "connected"
	StatusError        ChannelStatus = "error"
)

// ChatType indicates whether a message is from a direct or group chat.
type ChatType string

const (
	ChatTypeDirect ChatType = "direct"
	ChatTypeGroup  ChatType = "group"
)

// MediaAttachment represents a media file attached to a message.
type MediaAttachment struct {
	Type     string
	FileID   string
	FileName string
	MimeType string
	Caption  string
	Data     []byte
}

// InboundMessage is a normalised message from any channel.
type InboundMessage struct {
	Channel    string
	AccountID  string
	ChatType   ChatType
	SenderID   string
	SenderName string
	Text       string
	ReplyTo    string
	Media      []MediaAttachment
	Blocks     []input.InputBlock
	Timestamp  time.Time
}

// OutboundMessage is a message to send via a channel.
type OutboundMessage struct {
	ChatID      string
	Text        string
	ParseMode   string
	ReplyMarkup any
}

// MessagePolicyDecision reports whether an inbound channel message should be
// accepted by gateway ingress policy.
type MessagePolicyDecision struct {
	Accepted bool
	Reason   string
}

// RunEvent represents a structured runtime event emitted while handling a
// message. Channels can optionally consume these events to surface runtime
// status information to end-users.
type RunEvent struct {
	RunID         string
	AgentID       string
	SessionID     string
	ParentAgentID string
	Name          string
	Timestamp     time.Time
	Payload       map[string]any
}

// ChannelInfo is the read-only channel inventory view exposed by gateway.
type ChannelInfo interface {
	Name() string
	Status() ChannelStatus
}

// Channel is the interface that channel implementations must satisfy before
// being registered into the gateway runtime.
type Channel interface {
	ChannelInfo
	Connect(ctx context.Context) error
	Disconnect() error
	Send(ctx context.Context, msg OutboundMessage) error
	Receive() <-chan InboundMessage
}

// RunEventAware is an optional interface for channels that want live runtime
// status updates in addition to final outbound text messages.
type RunEventAware interface {
	HandleRunEvent(ctx context.Context, event RunEvent) error
}
