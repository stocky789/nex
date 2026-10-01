// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import type { API, Host } from "./types";
import { Badge, Banner, Section, date } from "./ui";
import { heldRequestID } from "./managed";
import { relativeTime } from "./labels";

// Image-edition hosts update themselves: the host's own engine fetches and verifies the release,
// writes the unused slot, arms the next boot only on Activate and falls back by itself when a boot
// fails. This panel shows exactly what the host reports in inventory.image and relays one action at a
// time. It stores nothing; a replaced Controller shows the same state from the next host report.

export const IMAGE_UPDATE_CAPABILITY = "image-update-v1";
export type ImageRelease = { label: string; sequence: number; root_hash?: string; slot?: string };
export type ImageError = { code: string; message: string };
export type ImageOperation = { id: string; kind: string; phase: string; sequence?: number; error?: ImageError; started_at: string; finished_at?: string };
export type ImageStatus = {
  booted: ImageRelease; staged?: ImageRelease; previous?: ImageRelease; available?: ImageRelease;
  channel?: string; phase: string; rollback_floor: number; state_format: number; secure_boot?: boolean;
  operation?: ImageOperation; failure?: ImageError;
};
export type ImageView = { host_id: string; connection: string; supported: boolean; image?: ImageStatus };
export type ImageAction = "check" | "apply" | "activate" | "rollback" | "cancel";
type Task = { id: string; status: string; error?: string };

const phases: Record<string, string> = {
  idle: "Up to date", checking: "Checking", downloading: "Downloading", writing: "Writing the unused slot",
  staged: "Staged; the next boot is unchanged", armed: "Armed; reboot pending", booted: "Booted; waiting for the health check",
  committed: "Committed", rolled_back: "Rolled back",
};
export const imagePhaseLabel = (phase: string) => phases[phase] ?? phase;
const running = (image: ImageStatus) => Boolean(image.operation && !image.operation.finished_at);

// Hosts the panel lists: those whose Core offered image-update-v1 or already reports an image.
export const imageHosts = (hosts: Host[]) =>
  hosts.filter(h => !h.revoked && (h.capabilities?.includes(IMAGE_UPDATE_CAPABILITY) || (h.inventory as { image?: unknown } | null)?.image));
export const hostImage = (host: Host) => (host.inventory as { image?: ImageStatus } | null)?.image;

// Which actions make sense now. These are hints so a button is not offered that cannot work; the host's
// engine refuses again on its own record with a stable code, and that refusal is shown as the task error.
export function imageActions(image: ImageStatus | undefined, connection: string): Record<ImageAction, { enabled: boolean; why: string }> {
  const off = (why: string) => ({ enabled: false, why });
  const on = { enabled: true, why: "" };
  if (!image) return { check: off("This host has not reported its image yet."), apply: off("This host has not reported its image yet."), activate: off("This host has not reported its image yet."), rollback: off("This host has not reported its image yet."), cancel: off("This host has not reported its image yet.") };
  if (connection !== "fresh") { const why = "The host must be freshly connected."; return { check: off(why), apply: off(why), activate: off(why), rollback: off(why), cancel: off(why) }; }
  const busy = running(image);
  const armed = image.phase === "armed";
  return {
    check: busy ? off("An update operation is running.") : on,
    apply: busy ? off("An update operation is running.")
      : armed ? off("The update is armed; reboot or roll back first.")
        : !image.available || image.available.sequence <= image.booted.sequence ? off("No newer release has been found. Check first.") : on,
    activate: busy ? off("An update operation is running.") : image.phase === "staged" && image.staged ? on : off("Nothing is staged."),
    rollback: busy ? off("An update operation is running.")
      : image.previous && (image.phase === "committed" || image.phase === "booted") ? on : off("There is no previous release to return to."),
    cancel: busy && !armed ? on : off(armed ? "Cancel is refused once the update is armed." : "Nothing is running."),
  };
}

// One request identity per unchanged request: a retry after a lost reply replays the same operation.
export async function submitImageAction(
  api: API, host: string, action: ImageAction, held: { id: string; signature: string },
  options: { sequence?: number; activate?: "stage_only" | "reboot"; shutdownVMs?: boolean } = {},
): Promise<Task> {
  const body: Record<string, unknown> = {};
  if (action === "apply") {
    if (options.sequence) body.sequence = options.sequence;
    body.activate = options.activate ?? "stage_only";
  }
  if (action === "apply" || action === "activate" || action === "rollback") body.running_vms = options.shutdownVMs ? "shutdown" : "refuse";
  body.request_id = heldRequestID(held, `${host}:${action}:${JSON.stringify(body)}`);
  const accepted = await api<{ operation: Task }>(`hosts/${encodeURIComponent(host)}/updates/${action}`, "POST", body);
  return accepted.operation;
}

const verb: Record<ImageAction, string> = { check: "Check", apply: "Download and stage", activate: "Reboot into update", rollback: "Roll back", cancel: "Cancel" };
const release = (r?: ImageRelease) => r ? `${r.label} (sequence ${r.sequence}${r.slot ? `, slot ${r.slot}` : ""})` : "—";
const fault = (e?: ImageError) => e ? `${e.code}: ${e.message}` : "";

export function ImageUpdates({ hosts, api, busy }: { hosts: Host[]; api: API; busy: boolean }) {
  const list = imageHosts(hosts);
  if (!list.length) return null;
  return <Section icon="refresh" title="Host images" count={list.length}>
    {list.map(host => <ImageHost key={host.id} host={host} api={api} busy={busy} />)}
    <p className="note">Each image-edition host checks, downloads and verifies releases itself and keeps the update record on its own disks. Staging never changes the next boot; only rebooting into the update does, and a failed boot returns to the previous release by itself.</p>
  </Section>;
}

function ImageHost({ host, api, busy }: { host: Host; api: API; busy: boolean }) {
  const [view, setView] = useState<ImageView | undefined>();
  const [task, setTask] = useState<Task | undefined>();
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState<ImageAction | "">("");
  const [shutdown, setShutdown] = useState(false);
  const held = useRef({ id: "", signature: "" });
  const image = view?.image ?? hostImage(host);
  const connection = view?.connection ?? host.connection;
  const actions = imageActions(image, connection);
  const active = image ? running(image) : false;

  async function refresh() {
    try { setView(await api<ImageView>(`hosts/${encodeURIComponent(host.id)}/updates`)); } catch { /* the next tick or action tries again; the host report is the record */ }
  }
  // Follow a running operation from the host's own report while the tab is visible.
  useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => { if (document.visibilityState === "visible") void refresh(); }, 3000);
    return () => clearInterval(timer);
  }, [active, host.id]);

  async function run(action: ImageAction) {
    setConfirming(""); setError("");
    try {
      const accepted = await submitImageAction(api, host.id, action, held.current, { shutdownVMs: shutdown, activate: "stage_only", sequence: action === "apply" ? image?.available?.sequence : undefined });
      setTask(accepted);
      held.current = { id: "", signature: "" };
      // The host reports the result with its next heartbeat; read it again shortly.
      setTimeout(() => void refresh(), 2000);
    } catch (e) { setError((e as Error).message || "The request failed."); }
  }
  const ask = (action: ImageAction) => (action === "activate" || action === "rollback") ? setConfirming(action) : void run(action);
  const button = (action: ImageAction, primary = false) =>
    <button key={action} type="button" className={primary ? "sm primary" : "sm"} disabled={busy || !actions[action].enabled} title={actions[action].why || undefined} onClick={() => ask(action)}>{verb[action]}</button>;

  return <div className="image-host" role="group" aria-label={`Image updates for ${host.hostname}`}>
    <h3>{host.hostname}{" "}{image && <Badge value={image.phase} label={imagePhaseLabel(image.phase)} />}</h3>
    {!image ? <p className="note">This host has not reported its image update state yet.</p> : <dl className="details">
      <dt>Running</dt><dd>{release(image.booted)}</dd>
      <dt>Staged</dt><dd>{release(image.staged)}</dd>
      <dt>Previous</dt><dd>{release(image.previous)}</dd>
      <dt>Available</dt><dd>{image.available ? release(image.available) : "Nothing newer is known"}</dd>
      <dt>Channel</dt><dd>{image.channel || "—"}</dd>
      <dt>Rollback floor</dt><dd>Sequence {image.rollback_floor}</dd>
      <dt>State format</dt><dd>{image.state_format}</dd>
      {image.secure_boot !== undefined && <><dt>Secure Boot</dt><dd>{image.secure_boot ? "Enforcing" : "Off"}</dd></>}
      {image.operation && <><dt>Last operation</dt><dd>{image.operation.kind} · {image.operation.phase}{" "}<time dateTime={image.operation.started_at} title={date(image.operation.started_at)}>{relativeTime(image.operation.started_at)}</time></dd></>}
    </dl>}
    {image?.failure && <Banner tone="bad" role="status">{image.failure.code === "boot_failed" ? "The new release failed its health check and the host returned to the previous release. " : ""}{fault(image.failure)}</Banner>}
    {image?.operation?.error && <Banner tone="warn" role="status">{fault(image.operation.error)}</Banner>}
    {error && <Banner tone="bad" role="alert">{error}</Banner>}
    {task && <p className="small" role="status">Task <a className="task-link" href="#/tasks" title={`Task ${task.id}`}>{task.id}</a> {task.status}. The host reports the result below.</p>}
    {connection !== "fresh" && <p className="note">This host is {connection}; actions are unavailable until it reconnects.</p>}
    <div className="actions">
      {button("check")}{button("apply", true)}{button("activate", true)}{button("rollback")}{button("cancel")}
    </div>
    {confirming && <div className="confirm" role="alertdialog" aria-label={`Confirm ${verb[confirming]}`}>
      <p>{confirming === "activate"
        ? `Reboot ${host.hostname} into the staged release? The host restarts now. It gets three tries; if the new release does not pass its health check it returns to the previous release by itself.`
        : `Reboot ${host.hostname} into the previous release? The host restarts now.`}</p>
      <label><input type="checkbox" checked={shutdown} onChange={e => setShutdown(e.target.checked)} /> Shut down running guests gracefully first (otherwise the host refuses while any guest runs)</label>
      <div className="actions"><button type="button" className="sm primary" onClick={() => void run(confirming)}>{verb[confirming]}</button><button type="button" className="sm" onClick={() => setConfirming("")}>Keep as is</button></div>
    </div>}
  </div>;
}
