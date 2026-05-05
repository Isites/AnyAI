// Package runtimeport defines the stable ports around the AnyAI runtime.
//
// The runtime data flow is intentionally split into small surfaces:
//   - SubmissionSurface accepts new ingress, run, and task work.
//   - ProjectionReader exposes recorder/session/task/memory read models.
//   - ProjectionStreamSource exposes live event streams.
//   - RuntimeController exposes explicit control and maintenance commands.
//
// Transport-facing code should normally depend on gateway.Service instead of
// these raw runtime ports. Gateway adapts raw projections into replay surfaces
// that are safer for channels, HTTP APIs, SSE, and UI consumers.
package runtimeport
