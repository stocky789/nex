// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import type { API, Host, Pool } from "./types";
import { Badge, Banner, Section, date } from "./ui";
import { heldRequestID } from "./managed";
import { relativeTime } from "./labels";
import { unfinished } from "./dock";
import { hostImage } from "./image-updates";

// A pool rollout is run by the pool master's Core: it stages every host, swaps the Controller appliance
// first, then updates one host at a time and the master last. This view shows only what the master and the
// hosts report (the rollout record comes back with the master's inventory) and relays one request at a
// time. It stores nothing; a replaced Controller shows the same rollout from the next report.

export const POOL_ROLLOUT_CAPABILITY = "pool-update-rollout-v1";
export type RolloutHost = { host_id: string; stage: string; stopped_by_update?: string[]; error?: string; attempt?: number };
export type RolloutController = { stage: string; host_id?: string; error?: string; attempt?: number };
export type Rollout = {
  id: string; pool_id: string; sequence: number; running_vms: string; state: string; phase: string;
  pause?: { code: string; message: string }; pause_requested?: boolean; hosts: RolloutHost[]; controller?: RolloutController;
  finalized_sequence?: number; created_at: string; updated_at: string;
};
export type RolloutView = { pool_id: string; connection: string; supported: boolean; rollout?: Rollout };
export type RolloutControl = "pause" | "resume" | "abort" | "rollback_host";
type Task = { id: string; status: string; error?: string };

const stages: Record<string, string> = {
  pending: "Waiting to stage", staged: "Staged", maintenance: "Entering maintenance", teardown: "Waiting for the pool's watchdogs",
  drain: "Stopping virtual machines", activate: "Activating", boot: "Rebooting", confirm: "Confirming the release", release: "Leaving maintenance",
  restart: "Restarting virtual machines", soak: "Soaking", done: "Updated", rolled_back: "Rolled back",
};
export const hostStageLabel = (stage: string) => stages[stage] ?? stage;
const controllerStages: Record<string, string> = { scan: "Looking for the Controller", swap: "Swapping the Controller image", done: "Done" };
export const controllerStageLabel = (c?: RolloutController) => c ? (controllerStages[c.stage] ?? c.stage) : "Waiting for staging";
const phases: Record<string, string> = { staging: "Staging on every host", controller: "Controller first", hosts: "Updating hosts", finalize: "Finalized" };
export const rolloutPhaseLabel = (phase: string) => phases[phase] ?? phase;

// Stages in which a host has activated the release: only then is a host rollback meaningful.
const activated: Record<string, true> = { boot: true, confirm: true, release: true, restart: true, soak: true, done: true };

// Refusals and failed operations carry "code: message"; the code is stable, the message literal.
export function refusal(error?: string): { code: string; message: string } | undefined {
  if (!error) return undefined;
  const m = /^([a-z][a-z0-9_]*): (.*)$/s.exec(error);
  return m ? { code: m[1], message: m[2] } : { code: "", message: error };
}

const pauseHints: Record<string, string> = {
  operator_paused: "Paused by an operator. Resume to continue.",
  master_changed: "The pool master changed. Review the pool, then resume or abort.",
  needs_attention: "A step needs an operator. Read the message, fix it, then resume or abort.",
  running_vms: "Running virtual machines block the update. Stop them, or resume with running virtual machines shut down.",
  step_timeout: "A step did not finish in time. Check the host, then resume.",
  rolled_back: "A host or the Controller fell back to its previous release. Resume stages it again; abort keeps committed hosts.",
  unconfirmed: "A host has not confirmed the release.",
  quorum_margin: "Updating the next host would cost the pool its majority.",
  host_busy: "The next host is running another update operation.",
  ha_enabled: "High availability is enabled. Turn it off to continue.",
  membership_changed: "The pool membership changed. Review the pool and resume.",
};
export const pauseHint = (code: string) => pauseHints[code] ?? "";

// Which controls make sense now. Hints only: the master's Core refuses again on its own record.
export function rolloutControls(view: RolloutView | undefined): Record<"pause" | "resume" | "abort", { enabled: boolean; why: string }> {
  const off = (why: string) => ({ enabled: false, why });
  const on = { enabled: true, why: "" };
  const r = view?.rollout;
  if (!r) return { pause: off("There is no update in progress."), resume: off("There is no update in progress."), abort: off("There is no update in progress.") };
  if (view.connection !== "fresh") { const why = "The pool master must be freshly connected."; return { pause: off(why), resume: off(why), abort: off(why) }; }
  const active = r.state === "running" || r.state === "paused";
  return {
    pause: r.state === "running" && !r.pause_requested ? on : off(r.pause_requested ? "A pause is already requested." : "The update is not running."),
    resume: r.state === "paused" ? on : off("The update is not paused."),
    abort: active ? on : off("The update has ended."),
  };
}
export const canRollBackHost = (view: RolloutView | undefined, host: RolloutHost) =>
  view?.connection === "fresh" && view.rollout?.state === "paused" && activated[host.stage] === true;

// The sequence the pool's hosts agree is available, to prefill the request: the smallest one every
// image-edition host reports, so no host is asked for a release it has not found.
export function offeredSequence(hosts: Host[]): number | undefined {
  const seen = hosts.filter(h => !h.revoked).map(h => hostImage(h)?.available?.sequence);
  return seen.length && seen.every((s): s is number => typeof s === "number") ? Math.min(...seen) : undefined;
}

type Mode = "check" | "start";
export async function submitRollout(
  api: API, pool: string, mode: Mode, held: { id: string; signature: string }, sequence: number, shutdownVMs: boolean,
): Promise<Task> {
  const body = { sequence, running_vms: shutdownVMs ? "shutdown" : "refuse", request_id: "" };
  body.request_id = heldRequestID(held, `${pool}:${mode}:${JSON.stringify({ sequence, running_vms: body.running_vms })}`);
  const path = mode === "check" ? "updates/check" : "updates/rollouts";
  return (await api<{ operation: Task }>(`pools/${encodeURIComponent(pool)}/${path}`, "POST", body)).operation;
}
export async function submitControl(api: API, pool: string, rollout: string, action: RolloutControl, held: { id: string; signature: string }, hostID?: string): Promise<Task> {
  const body: Record<string, unknown> = { action };
  if (hostID) body.host_id = hostID;
  body.request_id = heldRequestID(held, `${pool}:${rollout}:${JSON.stringify(body)}`);
  return (await api<{ operation: Task }>(`pools/${encodeURIComponent(pool)}/updates/rollouts/${encodeURIComponent(rollout)}/control`, "POST", body)).operation;
}

// One request at a time: a second click while one is on the wire does nothing, so a double submit can
// never reach the master twice (the held request ID still makes a retry after a failure the same request).
export async function exclusive<T>(gate: { current: boolean }, send: () => Promise<T>): Promise<T | undefined> {
  if (gate.current) return undefined;
  gate.current = true;
  try { return await send(); } finally { gate.current = false; }
}

// A read that comes back late must not replace a newer report of the same rollout.
export function newerView(current: RolloutView | undefined, incoming: RolloutView): RolloutView {
  const a = current?.rollout, b = incoming.rollout;
  return current && a && b && a.id === b.id && Date.parse(b.updated_at) < Date.parse(a.updated_at) ? current : incoming;
}

// initial is the record the panel starts from (the page's last read, or a test); it is replaced by the first read.
export function PoolRollout({ pool, hosts, api, busy, initial }: { pool: Pool; hosts: Host[]; api: API; busy: boolean; initial?: RolloutView }) {
  const [view, setView] = useState<RolloutView | undefined>(initial);
  const [unread, setUnread] = useState(false);
  const [sending, setSending] = useState(false);
  const gate = useRef(false);
  const [task, setTask] = useState<Task | undefined>();
  const [taskKind, setTaskKind] = useState<Mode | "control" | "">("");
  const [error, setError] = useState("");
  const [sequence, setSequence] = useState("");
  const [shutdown, setShutdown] = useState(false);
  const [confirming, setConfirming] = useState<{ action: RolloutControl | "start"; host?: RolloutHost } | undefined>();
  const held = useRef({ id: "", signature: "" });
  const nameOf = (id?: string) => hosts.find(h => h.id === id)?.hostname ?? id ?? "—";
  const offered = offeredSequence(hosts);
  const wanted = Number(sequence || offered || 0);
  const rollout = view?.rollout;
  const following = rollout?.state === "running" || Boolean(task && unfinished.has(task.status));

  async function refresh() {
    try { const next = await api<RolloutView>(`pools/${encodeURIComponent(pool.id)}/updates`); setView(current => newerView(current, next)); setUnread(false); }
    catch { setUnread(true); /* the record on screen is the master's last report; the next tick tries again */ }
  }
  useEffect(() => { if (initial) setView(current => newerView(current, initial)); }, [initial]);
  useEffect(() => { void refresh(); }, [pool.id]);
  useEffect(() => {
    const timer = setInterval(() => {
      if (document.visibilityState !== "visible") return;
      void refresh();
      if (following && task && unfinished.has(task.status)) {
        void api<Task[]>("operations").then(list => { const current = list.find(op => op.id === task.id); if (current) setTask(current); }).catch(() => { /* retried on the next tick */ });
      }
    }, following ? 3000 : 15000);
    return () => clearInterval(timer);
  }, [following, pool.id, task?.id, task?.status]);
  useEffect(() => { if (task && !unfinished.has(task.status)) void refresh(); }, [task?.id, task?.status]);

  async function submit(kind: Mode | "control", send: () => Promise<Task>) {
    setError(""); setConfirming(undefined);
    await exclusive(gate, async () => {
      setSending(true);
      try { setTask(await send()); setTaskKind(kind); held.current = { id: "", signature: "" }; setTimeout(() => void refresh(), 2000); }
      catch (e) { setError((e as Error).message || "The request failed."); }
      finally { setSending(false); }
    });
  }
  const control = (action: RolloutControl, host?: RolloutHost) =>
    submit("control", () => submitControl(api, pool.id, rollout!.id, action, held.current, host?.host_id));
  const ask = (action: RolloutControl, host?: RolloutHost) => (action === "abort" || action === "rollback_host") ? setConfirming({ action, host }) : void control(action, host);
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (confirming && !element.open) element.showModal();
    else if (!confirming && element.open) element.close();
  }, [confirming]);

  const live = view && unread ? { ...view, connection: "unreachable" } : view;
  const controls = rolloutControls(live);
  const result = task && !unfinished.has(task.status) ? refusal(task.error) : undefined;
  const fresh = live?.connection === "fresh";
  const active = rollout?.state === "running" || rollout?.state === "paused";
  const unsupported = view && !view.supported;
  const startable = Boolean(fresh && !unsupported && wanted && !active);
  const confirmable = !confirming ? false : confirming.action === "start" ? startable
    : confirming.action === "abort" ? controls.abort.enabled
    : Boolean(confirming.host && canRollBackHost(live, confirming.host));
  const poolHosts = hosts.filter(h => !h.revoked).length;

  return <Section icon="refresh" title="Pool update" count={rollout ? rollout.hosts.length : undefined}>
    {!view ? <p className="note">Reading the pool master's update record…</p> : <>
      <p className="note">Updates every host in this pool to a new Nex Core release, one host at a time, so the pool stays up. Members are updated first and the master last.</p>
      {unsupported && <Banner tone="warn" role="status">Pool update is not available yet: the master and every member must run a Nex Core version that supports it ({POOL_ROLLOUT_CAPABILITY}). Update the hosts that lack it one by one under Host images first.</Banner>}
      {live!.connection !== "fresh" && <p className="note">The pool master is {live!.connection}; the record below is its last report and actions are unavailable until it reconnects.</p>}
      {rollout ? <>
        <dl className="details">
          <dt>Release number</dt><dd>{rollout.sequence}</dd>
          <dt>State</dt><dd><Badge value={rollout.state} label={rollout.state === "paused" ? "Paused" : rollout.state === "running" ? "Running" : rollout.state === "finalized" ? "Finalized" : "Aborted"} />{" "}{rolloutPhaseLabel(rollout.phase)}</dd>
          <dt>Running virtual machines</dt><dd>{rollout.running_vms === "shutdown" ? "Shut down gracefully, then restarted on the same host" : "Refused while any virtual machine runs"}</dd>
          <dt>Started</dt><dd><time dateTime={rollout.created_at} title={date(rollout.created_at)}>{relativeTime(rollout.created_at)}</time></dd>
          {rollout.finalized_sequence ? <><dt>Finalized</dt><dd>Release number {rollout.finalized_sequence}; irreversible migrations may now run</dd></> : null}
        </dl>
        {rollout.pause && <Banner tone={rollout.pause.code === "operator_paused" ? "info" : "bad"} role="status">
          <b>{rollout.pause.code}</b>: {rollout.pause.message}{pauseHint(rollout.pause.code) ? ` ${pauseHint(rollout.pause.code)}` : ""}
        </Banner>}
        <table className="rollout-steps">
          <caption className="sr-only">Pool update steps in order</caption>
          <thead><tr><th scope="col">Step</th><th scope="col">Where</th><th scope="col">Status</th><th scope="col"><span className="sr-only">Actions</span></th></tr></thead>
          <tbody>
            <tr><th scope="row">Controller first</th><td>{rollout.controller?.host_id ? nameOf(rollout.controller.host_id) : "The host running the Controller"}</td>
              <td>{controllerStageLabel(rollout.controller)}{rollout.controller?.error ? <><br /><span className="small">{rollout.controller.error}</span></> : null}</td><td /></tr>
            {rollout.hosts.map(host => <tr key={host.host_id}>
              <th scope="row">Update host</th><td>{nameOf(host.host_id)}</td>
              <td><Badge value={host.stage === "done" ? "ready" : host.stage === "rolled_back" ? "failed" : "pending"} label={hostStageLabel(host.stage)} />
                {host.stopped_by_update?.length ? <span className="small"> · {host.stopped_by_update.length} virtual machine{host.stopped_by_update.length === 1 ? "" : "s"} stopped by the update</span> : null}
                {host.error ? <><br /><span className="small">{host.error}</span></> : null}</td>
              <td>{canRollBackHost(live, host) && <button type="button" className="sm" disabled={busy || sending} onClick={() => ask("rollback_host", host)}>Roll back host</button>}</td>
            </tr>)}
          </tbody>
        </table>
      </> : <p className="note">No pool update has been started.</p>}
      {error && <Banner tone="bad" role="alert">{error}</Banner>}
      {task && <p className="small" role="status">Task <a className="task-link" href="#/tasks" title={`Task ${task.id}`}>{task.id}</a> {task.status}.
        {result ? taskKind === "check" ? <> Readiness check refused: <b>{result.code || "refused"}</b> {result.message}</> : <> Refused: <b>{result.code || "refused"}</b> {result.message}</> : task.status === "succeeded" && taskKind === "check" ? " Readiness check passed: the pool is ready for this release." : null}</p>}
      <div className="actions">
        {(["pause", "resume", "abort"] as const).map(action =>
          <button key={action} type="button" className="sm" disabled={busy || sending || !controls[action].enabled} title={controls[action].why || undefined} onClick={() => ask(action)}>{action[0].toUpperCase() + action.slice(1)}</button>)}
      </div>
      {!active && <form className="rollout-start" onSubmit={e => { e.preventDefault(); }}>
        <label>Release number <input type="number" min={1} value={sequence} placeholder={offered ? String(offered) : ""} onChange={e => setSequence(e.target.value)} /></label>
        <label><input type="checkbox" checked={shutdown} onChange={e => setShutdown(e.target.checked)} /> Shut down running virtual machines gracefully (otherwise the update waits for you to stop each host's virtual machines)</label>
        <div className="actions">
          <button type="button" className="sm" disabled={busy || sending || !fresh || unsupported || !wanted} title={!wanted ? "No release number is known. Check each host first." : undefined}
            onClick={() => void submit("check", () => submitRollout(api, pool.id, "check", held.current, wanted, shutdown))}>Check readiness</button>
          <button type="button" className="sm primary" disabled={busy || sending || !fresh || unsupported || !wanted}
            onClick={() => setConfirming({ action: "start" })}>Start update</button>
        </div>
      </form>}
      <dialog ref={dialog} className="confirm-dialog" aria-labelledby="rollout-confirm-title" onCancel={e => { if (sending) e.preventDefault(); }} onClose={() => setConfirming(undefined)}>
        {confirming && <form aria-label={`Confirm ${confirming.action === "start" ? "start update" : confirming.action === "abort" ? "abort" : "host rollback"}`} onSubmit={e => { e.preventDefault(); if (!confirmable) return; if (confirming.action === "start") void submit("start", () => submitRollout(api, pool.id, "start", held.current, wanted, shutdown)); else void control(confirming.action, confirming.host); }}>
          <h2 id="rollout-confirm-title">{confirming.action === "start" ? "Start pool update?" : confirming.action === "abort" ? "Abort pool update?" : `Roll back ${nameOf(confirming.host?.host_id)}?`}</h2>
          <p>{confirming.action === "start"
            ? `Update ${poolHosts} host${poolHosts === 1 ? "" : "s"} to release ${wanted}? Every host reboots, one at a time, the master last. Running virtual machines: ${shutdown ? "shut down gracefully, then restarted on the same host" : "the update waits for each host's virtual machines to stop"}.`
            : confirming.action === "abort"
              ? "Abort the pool update? Hosts that already finished stay on the new release. The host in progress is taken out of maintenance and its stopped virtual machines are restarted."
              : `Reboot ${nameOf(confirming.host?.host_id)} into its previous release? The host restarts now.`}</p>
          {!confirmable && <p className="task-error" role="alert">This is no longer possible with the pool's current state.</p>}
          <div className="actions"><button type="button" onClick={() => setConfirming(undefined)}>Keep as is</button><button className="primary" disabled={sending || !confirmable}>{confirming.action === "start" ? "Start update" : confirming.action === "abort" ? "Abort update" : "Roll back host"}</button></div>
        </form>}
      </dialog>
      <p className="note">The pool master runs the update and keeps its record, so it carries on if this Controller is replaced. Nothing is ever forced.</p>
    </>}
  </Section>;
}
