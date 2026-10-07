// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import { ChannelPicker, channelLabel } from "./channel";
import type { API } from "./types";

// The host-local Updates screen: this host's own update engine, read and driven through
// /api/local/v1/updates. The engine keeps the record; this page only shows it and asks for one action at
// a time. Staging never changes the next boot; rebooting into the update does, and a release that fails
// its health check is replaced by the previous one by the host itself.

type Release = { label: string; sequence: number };
type Slot = { slot?: string; role: string; label: string; sequence: number; root_hash: string };
type Fault = { code: string; message: string };
export type LocalUpdateStatus = {
  running: boolean; slots: Slot[]; channel: string; available?: Release; phase: string; rollback_floor: number; checked_at?: string;
  operation?: { id: string; kind: string; phase: string; error?: Fault; started_at: string; finished_at?: string };
  failure?: Fault; base_url?: string;
};
export type LocalUpdateAction = "check" | "apply" | "activate" | "rollback" | "cancel";

const phases: Record<string, string> = {
  idle: "Up to date", checking: "Checking", downloading: "Downloading", writing: "Writing the unused slot",
  staged: "Staged; the next boot is unchanged", armed: "Armed; reboot pending", booted: "Booted; waiting for the health check",
  committed: "Committed", rolled_back: "Rolled back",
};
export const localPhaseLabel = (phase: string) => phases[phase] ?? phase;
const slotOf = (st: LocalUpdateStatus | null, role: string) => st?.slots?.find(s => s.role === role);

// Which actions make sense now. These are hints so a button is not offered that cannot work; the engine
// refuses again on its own record with a stable code, and that refusal is shown.
export function localUpdateActions(st: LocalUpdateStatus | null): Record<LocalUpdateAction, { enabled: boolean; why: string }> {
  const off = (why: string) => ({ enabled: false, why });
  const on = { enabled: true, why: "" };
  if (!st) { const why = "This host has not reported its update state yet."; return { check: off(why), apply: off(why), activate: off(why), rollback: off(why), cancel: off(why) }; }
  const busy = st.running;
  const armed = st.phase === "armed";
  const booted = slotOf(st, "booted");
  return {
    check: busy ? off("An update operation is running.") : on,
    apply: busy ? off("An update operation is running.")
      : armed ? off("The update is armed; reboot or roll back first.")
        : !st.available || (booted && st.available.sequence <= booted.sequence) ? off("No newer release has been found. Check first.") : on,
    activate: busy ? off("An update operation is running.") : st.phase === "staged" && slotOf(st, "staged") ? on : off("Nothing is staged."),
    rollback: busy ? off("An update operation is running.")
      : slotOf(st, "previous") && (st.phase === "committed" || st.phase === "booted") ? on : off("There is no previous release to return to."),
    cancel: busy && !armed ? on : off(armed ? "Cancel is refused once the update is armed." : "Nothing is running."),
  };
}

const verb: Record<LocalUpdateAction, string> = { check: "Check", apply: "Download and stage", activate: "Reboot into update", rollback: "Roll back", cancel: "Cancel" };
const release = (r?: { label: string; sequence: number; slot?: string }) => r ? `${r.label} (sequence ${r.sequence}${r.slot ? `, slot ${r.slot}` : ""})` : "—";
const fault = (f?: Fault) => f ? `${f.code}: ${f.message}` : "";

export function LocalUpdates({ api, busy }: { api: API; busy: boolean }) {
  const [status, setStatus] = useState<LocalUpdateStatus | null>(null);
  const [error, setError] = useState("");
  const [working, setWorking] = useState(false);
  const [confirming, setConfirming] = useState<"" | "activate" | "rollback">("");
  const [shutdown, setShutdown] = useState(false);
  const held = useRef({ id: "", signature: "" });
  async function read() {
    try { setStatus(await api<LocalUpdateStatus>("updates")); setError(""); }
    catch (e) { setError((e as Error).message || "The request failed."); }
  }
  useEffect(() => { void read(); }, []);
  // Follow a running operation from the engine's own record. A host that reboots into the update stops
  // answering for a while; the next successful read shows where it came back.
  useEffect(() => {
    if (!status?.running) return;
    const timer = window.setInterval(() => void read(), 3000);
    return () => window.clearInterval(timer);
  }, [status?.running]);
  const actions = localUpdateActions(status);

  async function run(action: LocalUpdateAction) {
    setConfirming(""); setError(""); setWorking(true);
    try {
      if (action === "check") {
        setStatus(await api<LocalUpdateStatus>("updates/check", "POST", {}));
      } else {
        const body: Record<string, unknown> = {};
        if (action === "apply") { body.activate = "stage_only"; if (status?.available) body.sequence = status.available.sequence; }
        if (action === "apply" || action === "activate" || action === "rollback") body.running_vms = shutdown ? "shutdown" : "refuse";
        const signature = `${action}:${JSON.stringify(body)}`;
        if (held.current.signature !== signature || !held.current.id) held.current = { id: crypto.randomUUID(), signature };
        body.request_id = held.current.id;
        await api(`updates/${action}`, "POST", body);
        held.current = { id: "", signature: "" };
        await read();
      }
    } catch (e) { setError((e as Error).message || "The request failed."); }
    finally { setWorking(false); }
  }
  async function changeChannel(channel: string) {
    await api("updates/settings", "PUT", { channel, base_url: status?.base_url ?? "" });
    await read();
  }
  const button = (action: LocalUpdateAction, primary = false) =>
    <button key={action} type="button" className={primary ? "primary" : undefined} disabled={busy || working || !actions[action].enabled} title={actions[action].why || undefined} onClick={() => (action === "activate" || action === "rollback") ? setConfirming(action) : void run(action)}>{verb[action]}</button>;
  const booted = slotOf(status, "booted"), staged = slotOf(status, "staged"), previous = slotOf(status, "previous");

  return <section>
    <dl className="defaults">
      <div><dt>Running</dt><dd>{release(booted)}</dd></div>
      <div><dt>Available</dt><dd>{!status ? "—" : status.available ? release(status.available) : "No newer release"}</dd></div>
      <div><dt>State</dt><dd>{status ? localPhaseLabel(status.phase) : "—"}</dd></div>
    </dl>
    {status && <dl className="defaults">
      <div><dt>Staged</dt><dd>{release(staged)}</dd></div>
      <div><dt>Previous</dt><dd>{release(previous)}</dd></div>
      <div><dt>Channel</dt><dd>{channelLabel(status.channel)}</dd></div>
    </dl>}
    <ChannelPicker current={status?.channel} disabled={busy || working || Boolean(status?.running)} apply={changeChannel} />
    <p className="note">After you reboot into an update this host restarts. Running virtual machines are shut down cleanly first if you ask for that; otherwise the host refuses while any guest runs. A release that fails its health check is replaced by the previous one automatically.</p>
    {status?.running && <progress aria-label="Update operation running" />}
    {status?.failure && <p className="warning" role="status">{status.failure.code === "boot_failed" ? "The new release failed its health check and the host returned to the previous release. " : ""}{fault(status.failure)}</p>}
    {status?.operation?.error && <p className="warning" role="status">{fault(status.operation.error)}</p>}
    {error && <p className="warning" role="alert">{error}</p>}
    <div className="actions">{button("check")}{button("apply", true)}{button("activate", true)}{button("rollback")}{button("cancel")}</div>
    {confirming && <form aria-label={`Confirm ${verb[confirming]}`} onSubmit={e => { e.preventDefault(); void run(confirming); }}>
      <p>{confirming === "activate" ? "Reboot this host into the staged release? The host restarts now. It gets three tries; if the new release does not pass its health check it returns to the previous release by itself." : "Reboot this host into the previous release? The host restarts now."}</p>
      <label><input type="checkbox" checked={shutdown} onChange={e => setShutdown(e.target.checked)} /> Shut down running virtual machines gracefully first</label>
      <div className="actions"><button type="button" onClick={() => setConfirming("")}>Keep as is</button><button className="primary" disabled={busy || working || !actions[confirming].enabled}>{verb[confirming]}</button></div>
    </form>}
  </section>;
}
