// Package runtime contains the core AnyAI execution runtime.
//
// Runtime is assembled from a small set of internal services with distinct
// ownership:
//   - AgentService accepts ingress and run requests.
//   - ProjectionService reads recorder-backed run/session/task views.
//   - ControlService performs rebuild, cancellation, compaction, and
//     maintenance commands.
//   - MemoryService wraps memory reads and scoped search.
//
// The package implements runtimeport.Runtime, but transport-facing packages
// should usually go through gateway so replay, routing, and channel concerns
// stay outside the execution core.
package runtime
