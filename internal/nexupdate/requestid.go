// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

// ValidRequestID is the engine's own rule for an idempotency key (8 to 64 letters, digits, - or _,
// starting with a letter or digit). Callers that mint request IDs (the pool rollout coordinator)
// and the fakes that stand in for the engine in their tests use this one check.
func ValidRequestID(id string) bool { return requestIDRE.MatchString(id) }
