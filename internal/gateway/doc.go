// Package gateway is the boundary between external transports and the core
// runtime.
//
// Channels and hosted HTTP surfaces submit user input through gateway, while
// gateway owns routing policy, channel supervision, message dispatch, config
// persistence hooks, and replay-safe projection streams. The core runtime stays
// focused on execution, durable events, projections, sessions, tasks, memory,
// and control operations.
//
// The intended flow is:
//
//	channel/http -> gateway -> runtimeport.GatewayRuntime -> runtime services
//
// Runtime events flow back through the same boundary as replayed snapshots plus
// deduplicated live streams so consumers do not need to understand recorder
// internals.
package gateway
