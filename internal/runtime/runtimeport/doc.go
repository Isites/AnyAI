// Package runtimeport defines the stable ports around the AnyAI runtime.
//
// The runtime data flow is intentionally split into small surfaces:
//   - SubmissionSurface accepts new ingress, run, and task work.
//   - ProjectionReader exposes runtime-owned visibility read models.
//   - ProjectionStreamSource exposes runtime live event streams.
//   - RuntimeController exposes explicit control and maintenance commands.
//
// Transport-facing code should normally depend on gateway.Service instead of
// these broad runtime ports. Runtime prepares replay-safe visibility surfaces;
// gateway performs DTO and transport adaptation.
package runtimeport
