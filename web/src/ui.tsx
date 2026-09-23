// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { statusLabel } from "./labels";

export type Tone = "ok" | "warn" | "bad" | "info" | "neutral";
const tones: [Tone, string[]][] = [
  ["ok", ["fresh", "healthy", "ok", "succeeded", "up", "verified", "passed", "current", "available", "confirmed", "connected", "ready", "in_sync", "running", "online", "met"]],
  ["warn", ["stale", "degraded", "timeout", "dispatched", "warning", "pending", "awaiting_fencing", "needs_attention", "unverified", "uncertain", "behind", "disconnected", "unresponsive", "staged", "applied", "unmet", "arming"]],
  ["bad", ["failed", "error", "revoked", "down", "blocked", "faulted", "rejected", "unavailable", "offline", "critical"]],
  ["info", ["queued", "uploading", "master", "pool master", "provisioned", "connecting"]],
];
export function badgeTone(value: string): Tone {
  const key = value.trim().toLowerCase();
  return tones.find(([, values]) => values.includes(key))?.[0] ?? "neutral";
}
// The square dot-and-text badge. The dot is drawn by CSS; the text is always present.
export function Badge({ value, label }: { value: string; label?: string }) {
  return <span className={`badge ${badgeTone(value)}`}>{label ?? statusLabel(value)}</span>;
}
export function Banner({ tone, role, children }: { tone: "info" | "warn" | "bad"; role?: "alert" | "status"; children: ReactNode }) {
  return <p className={`banner ${tone}`} role={role}>{children}</p>;
}
