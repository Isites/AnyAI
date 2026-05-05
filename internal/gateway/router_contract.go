package gateway

// RouteResolver is the gateway-owned routing contract used to map inbound
// channel context onto a target agent. Concrete routing strategies stay inside
// gateway packages and are injected during startup wiring.
type RouteResolver interface {
	Resolve(channel, senderID, accountID, chatType string) string
	IsKnownPeer(senderID string) bool
}
