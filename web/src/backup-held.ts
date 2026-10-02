// SPDX-License-Identifier: AGPL-3.0-only
// Exact-retry state for backup actions. A reply that never arrived leaves Core's
// outcome unknown, so the same body (and request_id) must be replayed; Core
// dedupes by request_id. The store is module-level so it outlives the Backups
// route: following the "check Tasks" link unmounts the page, and a fresh
// request_id after returning could duplicate an action that did land. It is
// keyed by pool and holds only requests this tab sent; it is never durable state.
import { rid, type BackupRecoveryPoint } from "./backup-types";

export type RestoreDraft = { name: string; host: string; storage: string; vmID: string; confirmation: string };
type HeldBody = { request_id: string };

export type Held = {
  // Frozen request bodies by action key: present only while an action is in flight or its outcome is unknown.
  bodies: Record<string, HeldBody>;
  // The restore form behind an unsettled restore, and the point it targets, so the form can be reopened.
  drafts: Record<string, RestoreDraft>;
  points: Record<string, BackupRecoveryPoint>;
};

const store = new Map<string, Held>();

export function heldFor(poolID: string): Held {
  let held = store.get(poolID);
  if (!held) store.set(poolID, held = { bodies: {}, drafts: {}, points: {} });
  return held;
}

// A definitive refusal (4xx other than 409) means Core did not accept the request.
export const isDefinitive = (error: unknown) => {
  const status = (error as { status?: number } | null)?.status;
  return typeof status === "number" && status >= 400 && status < 500 && status !== 409;
};

export type HeldOutcome<T, B> = { reply: T; body: B } | { error: unknown; definitive: boolean };

// sendHeld sends the frozen body for key, creating it on first use. Success and a
// definitive refusal release it; anything else keeps it so the next call replays it.
export async function sendHeld<B extends HeldBody, T>(held: Held, key: string, prefix: string, make: (requestID: string) => B, send: (body: B) => Promise<T>): Promise<HeldOutcome<T, B>> {
  const body = (held.bodies[key] ??= make(rid(prefix))) as B;
  try {
    const reply = await send(body);
    delete held.bodies[key];
    return { reply, body };
  } catch (error) {
    const definitive = isDefinitive(error);
    if (definitive) delete held.bodies[key];
    return { error, definitive };
  }
}
