// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import type { API, Host } from "./types";
import { Badge, Banner, Section, date } from "./ui";
import { heldRequestID } from "./managed";
import { relativeTime } from "./labels";
import { unfinished } from "./dock";
import { ChannelPicker, channelLabel } from "./channel";

// Hosts update themselves: the host's own engine fetches and verifies the release,
// writes the unused slot, arms the next boot only on Activate and falls back by itself when a boot
// fails. This panel shows exactly what the host reports in inventory.image and relays one action at a
// time. It stores nothing; a replaced Controller shows the same state from the next host report.

export const IMAGE_UPDATE_CAPABILITY = "image-update-v1";
export type ImageRelease = { label: string; sequence: number; root_hash?: string; slot?: string };
export type ImageError = { code: string; message: string };
export type ImageOperation = { id: string; kind: string; phase: string; sequence?: number; error?: ImageError; started_at: string; finished_at?: string };
export type ImageStatus = {
  booted: ImageRelease; staged?: ImageRelease; previous?: ImageRelease; available?: ImageRelease;
  channel?: string; base_url?: string; phase: string; rollback_floor: number; state_format: number; secure_boot?: boolean;
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

// What a host's own report says about an update, for the lists that only need a hint. Derived from the
// reported inventory.image alone: the Controller does not look for releases. A host that reports nothing
// gets no notice; a host that is not freshly connected keeps its last report but says so.
export type ImageUpdateNotice = { kind: "available" | "staged" | "armed"; label: string; detail: string; current: boolean };
export function imageUpdateNotice(host: Host): ImageUpdateNotice | null {
  const image = hostImage(host);
  if (host.revoked || !image?.booted) return null;
  const current = host.connection === "fresh";
  const make = (kind: ImageUpdateNotice["kind"], label: string, release: ImageRelease | undefined): ImageUpdateNotice => ({
    kind, current, label: current ? label : `${label} (last report)`,
    detail: `${release ? `${release.label} (sequence ${release.sequence}). ` : ""}${current ? "" : "The host is not connected; this is its last report. "}Reported by the host.`,
  });
  if (image.phase === "armed") return make("armed", "Update pending reboot", image.staged);
  if (image.phase === "staged" && image.staged) return make("staged", "Update staged", image.staged);
  if (image.available && image.available.sequence > image.booted.sequence && image.available.sequence !== image.staged?.sequence) return make("available", "Update available", image.available);
  return null;
}
export function ImageUpdateBadge({ host }: { host: Host }) {
  const notice = imageUpdateNotice(host);
  return notice ? <span title={notice.detail}><Badge value={`update-${notice.kind}`} label={notice.label} tone={notice.current ? "info" : "neutral"} /></span> : null;
}

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

// The settings action changes the host's update source. Core writes channel and base URL together, so the
// host's current base URL travels with the new channel; leaving it out would clear the source.
export async function submitImageSettings(
  api: API, host: string, channel: string, baseURL: string | undefined, held: { id: string; signature: string },
): Promise<Task> {
  const body: Record<string, unknown> = { channel };
  if (baseURL) body.base_url = baseURL;
  body.request_id = heldRequestID(held, `${host}:settings:${JSON.stringify(body)}`);
  const accepted = await api<{ operation: Task }>(`hosts/${encodeURIComponent(host)}/updates/settings`, "POST", body);
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
    <p className="note">Each host checks, downloads and verifies releases itself and keeps the update record on its own disks. Staging never changes the next boot; only rebooting into the update does, and a failed boot returns to the previous release by itself.</p>
  </Section>;
}

// A host's report is identified by when it was last seen and how it was connected. A view fetched
// from the Controller is only newer than the report the panel was given while that identity is
// unchanged; a new report (or a lost connection) replaces it, so the panel never shows older data
// than the page's own host list.
export const reportStamp = (host: Host) => `${host.last_seen ?? ""}|${host.connection}`;
export type FetchedView = { view: ImageView; stamp: string };
export function imageSnapshot(host: Host, fetched?: FetchedView): { image: ImageStatus | undefined; connection: string } {
  const current = fetched && fetched.stamp === reportStamp(host) ? fetched.view : undefined;
  return { image: current?.image ?? hostImage(host), connection: current?.connection ?? host.connection };
}
// The panel keeps reading while the host reports a running operation or a task it submitted has
// not finished: the task's end is the signal that the host's report is due.
export const stillFollowing = (active: boolean, task?: Task) => active || Boolean(task && unfinished.has(task.status));

function ImageHost({ host, api, busy }: { host: Host; api: API; busy: boolean }) {
  const [fetched, setFetched] = useState<FetchedView | undefined>();
  const [task, setTask] = useState<Task | undefined>();
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState<ImageAction | "">("");
  const [shutdown, setShutdown] = useState(false);
  const held = useRef({ id: "", signature: "" });
  const stamp = useRef("");
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (confirming && !element.open) element.showModal();
    else if (!confirming && element.open) element.close();
  }, [confirming]);
  stamp.current = reportStamp(host);
  const { image, connection } = imageSnapshot(host, fetched);
  const actions = imageActions(image, connection);
  const active = image ? running(image) : false;
  const following = stillFollowing(active, task);

  async function refresh() {
    const requested = stamp.current;
    try { setFetched({ view: await api<ImageView>(`hosts/${encodeURIComponent(host.id)}/updates`), stamp: requested }); } catch { /* the next tick or action tries again; the host report is the record */ }
  }
  // Follow a running operation, and a submitted task until it ends, from the host's own report
  // while the tab is visible. A task's end is followed by one more read: the host reports its result.
  useEffect(() => {
    if (!following) return;
    const timer = setInterval(() => {
      if (document.visibilityState !== "visible") return;
      void refresh();
      if (task && unfinished.has(task.status)) {
        void api<Task[]>("operations").then(list => { const current = list.find(op => op.id === task.id); if (current) setTask(current); }).catch(() => { /* retried on the next tick */ });
      }
    }, 3000);
    return () => clearInterval(timer);
  }, [following, host.id, task?.id, task?.status]);
  useEffect(() => { if (task && !unfinished.has(task.status)) void refresh(); }, [task?.id, task?.status]);

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
  async function changeChannel(channel: string) {
    setError("");
    setTask(await submitImageSettings(api, host.id, channel, image?.base_url, held.current));
    held.current = { id: "", signature: "" };
    setTimeout(() => void refresh(), 2000);
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
      <dt>Channel</dt><dd>{channelLabel(image.channel)}</dd>
      <dt>Rollback floor</dt><dd>Sequence {image.rollback_floor}</dd>
      <dt>State format</dt><dd>{image.state_format}</dd>
      {image.secure_boot !== undefined && <><dt>Secure Boot</dt><dd>{image.secure_boot ? "Enforcing" : "Off"}</dd></>}
      {image.operation && <><dt>Last operation</dt><dd>{image.operation.kind} · {image.operation.phase}{" "}<time dateTime={image.operation.started_at} title={date(image.operation.started_at)}>{relativeTime(image.operation.started_at)}</time></dd></>}
    </dl>}
    {image && <ChannelPicker current={image.channel} disabled={busy || connection !== "fresh" || active || following} apply={changeChannel} />}
    {image?.failure && <Banner tone="bad" role="status">{image.failure.code === "boot_failed" ? "The new release failed its health check and the host returned to the previous release. " : ""}{fault(image.failure)}</Banner>}
    {image?.operation?.error && <Banner tone="warn" role="status">{fault(image.operation.error)}</Banner>}
    {error && <Banner tone="bad" role="alert">{error}</Banner>}
    {task?.error && (task.status === "failed" || task.status === "needs_attention") && <Banner tone={task.status === "failed" ? "bad" : "warn"} role="alert">{task.error}</Banner>}
    {task && <p className="small" role="status">Task <a className="task-link" href="#/tasks" title={`Task ${task.id}`}>{task.id}</a> {task.status}. Host image state is shown above.</p>}
    {connection !== "fresh" && <p className="note">This host is {connection}; actions are unavailable until it reconnects.</p>}
    <div className="actions">
      {button("check")}{button("apply", true)}{button("activate", true)}{button("rollback")}{button("cancel")}
    </div>
    <dialog ref={dialog} className="confirm-dialog" aria-labelledby={`image-confirm-${host.id}`} onClose={() => setConfirming("")}>
      {confirming && <form aria-label={`Confirm ${verb[confirming]}`} onSubmit={e => { e.preventDefault(); void run(confirming); }}>
        <h2 id={`image-confirm-${host.id}`}>{verb[confirming]} {host.hostname}?</h2>
        <p>{confirming === "activate"
          ? `Reboot ${host.hostname} into the staged release? The host restarts now. It gets three tries; if the new release does not pass its health check it returns to the previous release by itself.`
          : `Reboot ${host.hostname} into the previous release? The host restarts now.`}</p>
        <label><input type="checkbox" checked={shutdown} onChange={e => setShutdown(e.target.checked)} /> Shut down running guests gracefully first (otherwise the host refuses while any guest runs)</label>
        {!actions[confirming].enabled && <p className="task-error" role="alert">{actions[confirming].why || "This action is no longer available."}</p>}
        <div className="actions"><button type="button" onClick={() => setConfirming("")}>Keep as is</button><button className="primary" disabled={busy || !actions[confirming].enabled}>{verb[confirming]}</button></div>
      </form>}
    </dialog>
  </div>;
}
