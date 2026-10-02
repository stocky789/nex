// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import type { API } from "./types";
import { Badge, Panel, date, size } from "./ui";
import { rid, type BackupRepository } from "./backup-types";

export type OrphanGroup = {
  vm_id: string; vm_name?: string; recovery_points: number; protected_points: number; size_bytes: number;
  size_unknown_points?: number; oldest: string; newest: string; in_use?: boolean;
};
type OrphanQuery = { repository_id: string; groups: OrphanGroup[]; warnings?: string[] };
type CleanupRecord = { status: "running" | "succeeded" | "failed"; error?: string; points: { recovery_point_id: string; vm_id: string; status: string; reason?: string }[] };
type Operation = { id: string; status: string; error?: string };

// orphanConfirmation is the text Core expects; with protected points included it states how many.
export const orphanConfirmation = (points: number, protectedPoints: number, includeProtected: boolean) =>
  `delete ${points} recovery points${includeProtected ? ` including ${protectedPoints} protected` : ""}`;

// cleanupBody carries the totals the operator was shown; Core refuses if they changed since.
export function cleanupBody(requestID: string, groups: OrphanGroup[], includeProtected: boolean, typed: string) {
  const points = groups.reduce((n, g) => n + g.recovery_points, 0), protectedPoints = groups.reduce((n, g) => n + g.protected_points, 0);
  return { request_id: requestID, vm_ids: groups.map(g => g.vm_id), include_protected: includeProtected, confirmation: typed, recovery_points: points, protected_points: protectedPoints };
}

export type CleanupPoll = { record: CleanupRecord } | { refusal: string } | { stalled: CleanupRecord };

// pollCleanup reads Core's operation, then its cleanup record. null means Core has not planned the
// cleanup yet. A failed operation with no record is a pure refusal (404), so its error is the answer.
// A record still `running` behind a failed or needs-attention operation will never settle: stalled.
export async function pollCleanup(api: API, base: string, id: string): Promise<CleanupPoll | null> {
  const op = (await api<Operation[]>("operations")).find(o => o.id === id);
  if (!op) throw new Error("Core has not reported this cleanup yet.");
  if (op.status === "queued" || op.status === "dispatched") return null;
  const record = await api<CleanupRecord>(`${base}/orphan-cleanups/${id}`).catch(e => { if (op.status === "failed") return null; throw e; });
  if (!record) return { refusal: op.error || "Core refused the cleanup." };
  return record.status === "running" && (op.status === "failed" || op.status === "needs_attention") ? { stalled: record } : { record };
}

const message = (e: unknown) => e instanceof Error ? e.message : String(e);

export function OrphanedBackups({ api, poolID, repo, onClose }: { api: API; poolID: string; repo: BackupRepository; onClose: () => void }) {
  const base = `pools/${encodeURIComponent(poolID)}/backup-repositories/${encodeURIComponent(repo.id)}`;
  const requestID = useRef("");
  const [query, setQuery] = useState<OrphanQuery | null>(null);
  const [picked, setPicked] = useState<string[]>([]);
  const [includeProtected, setIncludeProtected] = useState(false);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [submitted, setSubmitted] = useState("");
  const [record, setRecord] = useState<CleanupRecord | null>(null);
  const [refusal, setRefusal] = useState("");
  const [pollError, setPollError] = useState("");
  const [stalled, setStalled] = useState(false);
  const misses = useRef(0);
  const chosen = query?.groups.filter(g => picked.includes(g.vm_id)) ?? [];
  const totals = cleanupBody("", chosen, includeProtected, typed);
  const want = orphanConfirmation(totals.recovery_points, totals.protected_points, includeProtected);
  // A changed selection or confirmation is a different request; an unchanged retry replays.
  useEffect(() => { requestID.current = rid("borphans"); }, [picked, includeProtected, typed]);
  const loadQuery = () => api<OrphanQuery>(`${base}/orphans`).then(setQuery, e => setError(message(e)));
  useEffect(() => { void loadQuery(); }, []);
  // Core runs the cleanup; follow its record, and the operation for a refusal, which has no record.
  useEffect(() => {
    if (!submitted || record?.status === "succeeded" || record?.status === "failed" || refusal || stalled) return;
    const timer = window.setInterval(async () => {
      try {
        const result = await pollCleanup(api, base, submitted);
        misses.current = 0; setPollError("");
        if (!result) return;
        if ("refusal" in result) setRefusal(result.refusal);
        else if ("stalled" in result) { setRecord(result.stalled); setStalled(true); }
        else setRecord(result.record);
      } catch (e) {
        if (++misses.current >= 15) setPollError(message(e));
      }
    }, 2000);
    return () => window.clearInterval(timer);
  }, [submitted, record?.status, refusal, stalled]);
  // After a refusal (for example changed counts) start over from a fresh look at the repository.
  const reviewAgain = () => {
    setSubmitted(""); setRefusal(""); setRecord(null); setStalled(false); setPollError(""); misses.current = 0;
    setPicked([]); setTyped(""); setIncludeProtected(false); setQuery(null); setError("");
    void loadQuery();
  };
  const toggle = (id: string) => setPicked(current => current.includes(id) ? current.filter(x => x !== id) : [...current, id]);
  const submit = async () => {
    setBusy(true); setError("");
    const body = cleanupBody(requestID.current, chosen, includeProtected, typed);
    try {
      await api(`${base}/orphan-cleanups`, "POST", body);
      setSubmitted(body.request_id);
    } catch (e) { setError(message(e)); }
    finally { setBusy(false); }
  };
  const vmName = (id: string) => query?.groups.find(g => g.vm_id === id)?.vm_name || id;
  const deleting = totals.recovery_points - (includeProtected ? 0 : totals.protected_points);
  const counts = record?.points.reduce<Record<string, number>>((n, p) => ({ ...n, [p.status]: (n[p.status] ?? 0) + 1 }), {}) ?? {};
  return <Panel icon="backup" title={`Orphaned backups: ${repo.name}`} wide onClose={onClose}>
    <p className="muted">Recovery points here whose VM is no longer in the pool. Deleting them removes their data from the destination and cannot be undone.</p>
    {error && <p className="alert" role="alert">{error}</p>}
    {!query && !error && <p role="status">Checking the pool's VM inventory…</p>}
    {query?.warnings?.map(w => <p key={w} className="warning">{w}</p>)}
    {query && query.groups.length === 0 && <p className="empty">No orphaned recovery points in this repository.</p>}
    {query && query.groups.length > 0 && !submitted && <>
      <div className="table-scroll"><table className="records">
        <thead><tr><th><span className="sr-only">Select</span></th><th>VM</th><th>Points</th><th>Protected</th><th>Size</th><th>Newest</th><th>Oldest</th></tr></thead>
        <tbody>{query.groups.map(g => <tr key={g.vm_id}>
          <td><input type="checkbox" aria-label={`Select ${g.vm_name || g.vm_id}`} checked={picked.includes(g.vm_id)} disabled={busy} onChange={() => toggle(g.vm_id)} /></td>
          <td><strong>{g.vm_name || "Unnamed VM"}</strong><small className="mono">{g.vm_id}</small>{g.in_use && <small>Some points are in use and will be skipped.</small>}</td>
          <td>{g.recovery_points}</td><td>{g.protected_points}</td>
          <td>{size(g.size_bytes)}{g.size_unknown_points ? <small>{g.size_unknown_points} not sized</small> : null}</td>
          <td>{date(g.newest)}</td><td>{date(g.oldest)}</td>
        </tr>)}</tbody>
      </table></div>
      <label className="check" style={{ justifyContent: "flex-start", alignItems: "center" }}><input type="checkbox" checked={includeProtected} disabled={busy} onChange={e => setIncludeProtected(e.target.checked)} />Include protected recovery points</label>
      <p role="status">{chosen.length === 0 ? "Select the VMs to clean up." : `${chosen.length} VM${chosen.length === 1 ? "" : "s"} selected with ${totals.recovery_points} recovery point${totals.recovery_points === 1 ? "" : "s"}, ${totals.protected_points} protected. ${deleting} will be deleted${includeProtected ? "" : `; ${totals.protected_points} protected will be kept`}. The confirmation text counts every selected point.`}</p>
      {chosen.length > 0 && <label>Type the text below to confirm<input autoComplete="off" value={typed} disabled={busy} onChange={e => setTyped(e.target.value)} /><span className="mono">{want}</span></label>}
      <div className="actions">
        <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
        <button type="button" className="danger" disabled={busy || chosen.length === 0 || typed !== want} onClick={() => void submit()}>{busy ? "Requesting…" : "Delete recovery points"}</button>
      </div>
    </>}
    {submitted && <div>
      {refusal && record?.error !== refusal && <p className="alert" role="alert">{refusal}</p>}
      {pollError && <p className="alert" role="alert">{pollError} The cleanup may still be running; check <a href="#/tasks">Tasks</a>.</p>}
      {stalled && <p className="alert" role="alert">Core reports this cleanup's task as failed or needing attention while its record still says running, so it may not finish. Review the repository again to see what remains, and check <a href="#/tasks">Tasks</a> for the cause.</p>}
      {!record && !refusal && !pollError && <p role="status">Waiting for Core to start the cleanup…</p>}
      {record && <>
        <p role="status"><Badge value={record.status} /> {counts.deleted ?? 0} deleted, {counts.skipped ?? 0} skipped, {counts.failed ?? 0} failed, {counts.pending ?? 0} waiting.</p>
        {record.error && <p className="alert" role="alert">{record.error}</p>}
        <div className="table-scroll"><table className="records">
          <thead><tr><th>Recovery point</th><th>VM</th><th>Result</th></tr></thead>
          <tbody>{record.points.map(p => <tr key={p.recovery_point_id}>
            <td className="mono">{p.recovery_point_id}</td><td>{vmName(p.vm_id)}</td><td>{p.status}{p.reason ? <small>{p.reason}</small> : null}</td>
          </tr>)}</tbody>
        </table></div>
      </>}
      <div className="actions">
        {(refusal || stalled) && <button type="button" onClick={reviewAgain}>Review again</button>}
        <button type="button" onClick={onClose}>Close</button>
      </div>
    </div>}
  </Panel>;
}
