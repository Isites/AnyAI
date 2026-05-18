// Package gateway is the boundary between external transports and the core
// runtime.
//
// Channels and hosted HTTP surfaces submit user input through gateway, while
// gateway owns routing policy, channel supervision, message dispatch, config
// persistence hooks, and DTO adaptation. The core runtime stays focused on
// execution, durable events, replay-safe projections, sessions, tasks, memory,
// and control operations.
//
// The intended flow is:
//
//	channel/http -> gateway -> runtimeport.GatewayRuntime -> runtime services
//
// Runtime events flow back through the same boundary after runtime has prepared
// consumer replay snapshots plus deduplicated live streams.
package gateway
