package runtimeport

import "github.com/Isites/anyai/internal/runtime/input"

// ChatType is the runtime-owned conversation classification used by ingress,
// routing, and policy services after gateway/channel inputs are normalized.
type ChatType string

const (
	ChatTypeDirect ChatType = "direct"
	ChatTypeGroup  ChatType = "group"
)

// IngressRequest normalizes runtime-facing inputs from all transports before
// they are executed by the runtime. Gateway is responsible for building and
// routing these requests before handing them to runtime.
type IngressRequest struct {
	RunID         string
	Channel       string
	RequestedID   string
	SenderID      string
	AccountID     string
	ChatType      ChatType
	Text          string
	MessageID     string
	Envelope      input.InputEnvelope
	SessionID     string
	SessionPrefix string
	ParentAgentID string
}
