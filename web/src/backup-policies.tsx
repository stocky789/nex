// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useId, useMemo, useState, type FormEvent } from "react";
import type { API, Data, Operation, Pool } from "./types";
import { Panel, Section, Badge, checkedAt, date } from "./ui";
import { statusLabel } from "./labels";
import { DeleteBackupPolicy } from "./backup-policy-delete";
import { backupOpID, repositoryHealth, type BackupRepository, type BackupRun } from "./backup-types";
import { sendHeld, type Held } from "./backup-held";

export type BackupPolicy = {
  id: string;
  schedule_id: string;
  pool_id: string;
  name: string;
  enabled: boolean;
  paused: boolean;
  repository_id: string;
  capture_mode: string;
  time_zone: string;
  rule: { frequency: string; hour: number; minute: number; weekdays?: number[] };
  vm_ids: string[];
  retention: { keep_latest: number; protect_newer_than?: number };
  limits?: { max_concurrent_per_host?: number; max_concurrent_per_repo?: number };
  overrides?: { vm_id: string; retention?: BackupPolicy["retention"]; capture_mode?: string }[];
  revision: number;
  next_dispatch_at?: string;
  last_dispatch_at?: string;
};

type Props = {
  api: API;
  data: Data;
  pool: Pool;
  repos: BackupRepository[] | null;
  policies: BackupPolicy[] | null;
  runs: BackupRun[] | null;
  busy: boolean;
  unavailable: boolean;
  working: string;
  setWorking: (v: string) => void;
  onNotice: (text: string, operationID?: string) => void;
  onError: (text: string) => void;
  error: string;
  reload: () => Promise<void>;
  held: Held;
  onViewPoints: (vmID: string) => void;
  openCreate: boolean;
  onOpenCreate: () => void;
  onCloseCreate: () => void;
  onRepositoryPanelChange: (open: boolean) => void;
};

type Step = 1 | 2 | 3;
type SwitchDraft = { policy: BackupPolicy; target: string; targetRepo: BackupRepository | null; confirmed: boolean; uncertain: boolean; taskStatus: string };

// The browser's own zone list; UTC is added because some browsers omit it.
const timeZones = (() => { const zones = typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : []; return zones.includes("UTC") ? zones : ["UTC", ...zones]; })();
const stepNames = ["Coverage", "Timing and retention", "Review"];
// nextRun shows the time in the job's own zone, the one its Schedule column names.
const nextRun = (p: BackupPolicy) => {
  // Core omits or zeroes the time for a job that is paused, disabled or not yet scheduled.
  if (p.paused || !p.enabled || !p.next_dispatch_at || p.next_dispatch_at.startsWith("0001-")) return "—";
  try { return new Date(p.next_dispatch_at).toLocaleString(undefined, { timeZone: p.time_zone }); } catch { return date(p.next_dispatch_at); }
};
const message = (e: unknown) => e instanceof Error ? e.message : String(e);
type PolicyCreateBody = {
  request_id: string; name: string; enabled: boolean; paused: boolean; repository_id: string; capture_mode: string;
  time_zone: string; rule: BackupPolicy["rule"]; vm_ids: string[]; retention: { keep_latest: number };
};
const weekdayLabels = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export type RepositoryChangeBody = {
  request_id: string; expected_revision: number; schedule_id: string; name: string; enabled: boolean; paused: boolean;
  repository_id: string; capture_mode: string; time_zone: string; rule: BackupPolicy["rule"]; vm_ids: string[];
  retention: BackupPolicy["retention"]; limits: BackupPolicy["limits"]; overrides: BackupPolicy["overrides"];
};

export function repositoryChangeBody(policy: BackupPolicy, repositoryID: string, requestID: string): RepositoryChangeBody {
  return {
    request_id: requestID,
    expected_revision: policy.revision,
    schedule_id: policy.schedule_id,
    name: policy.name,
    enabled: policy.enabled,
    paused: policy.paused,
    repository_id: repositoryID,
    capture_mode: policy.capture_mode,
    time_zone: policy.time_zone,
    rule: policy.rule,
    vm_ids: policy.vm_ids,
    retention: policy.retention,
    limits: policy.limits,
    overrides: policy.overrides,
  };
}

export function requestRepositoryChange(api: API, poolID: string, policyID: string, body: RepositoryChangeBody) {
  return api<{ operation?: { id?: string; status?: string } }>(`pools/${encodeURIComponent(poolID)}/backup-policies/${encodeURIComponent(policyID)}`, "PUT", body);
}

const failedStatuses = ["failed", "missed", "needs_attention"];
export type VMResult = { last: BackupRun; lastSuccess?: BackupRun };
// jobVMResults keys each VM of one job to its latest run and its latest successful run.
export function jobVMResults(policyID: string, runs: BackupRun[] | null) {
  const results = new Map<string, VMResult>();
  for (const run of runs || []) {
    if (!run.vm_id || run.policy_id !== policyID) continue;
    const cur = results.get(run.vm_id);
    const last = !cur || run.updated_at > cur.last.updated_at ? run : cur.last;
    const lastSuccess = run.status === "succeeded" && (!cur?.lastSuccess || run.updated_at > cur.lastSuccess.updated_at) ? run : cur?.lastSuccess;
    results.set(run.vm_id, { last, lastSuccess });
  }
  return results;
}
// failedVMs lists the VMs whose latest run for this job failed, was missed or needs attention;
// a later successful run clears it.
export function failedVMs(policy: { id: string; vm_ids: string[] }, runs: BackupRun[] | null) {
  const results = jobVMResults(policy.id, runs);
  return (policy.vm_ids || []).filter(vmID => failedStatuses.includes(results.get(vmID)?.last.status ?? ""));
}

// Keep each Core result distinct; skipped or missed runs are not running or failed.
function lastResult(vmIDs: string[], results: Map<string, VMResult>) {
  const counts = new Map<string, number>();
  for (const id of vmIDs) {
    const status = results.get(id)?.last.status || "No runs yet";
    counts.set(status, (counts.get(status) || 0) + 1);
  }
  return [...counts].map(([status, count]) => `${count} ${statusLabel(status).toLowerCase()}`).join(" · ");
}

export function BackupPolicies({
  api, data, pool, repos, policies, runs, onViewPoints, busy, unavailable, working, setWorking, error,
  onNotice, onError, reload, held, openCreate, onOpenCreate, onCloseCreate, onRepositoryPanelChange,
}: Props) {
  const formID = useId();
  const switchFormID = useId();
  const [step, setStep] = useState<Step>(1);
  const [name, setName] = useState("Nightly");
  const [vms, setVMs] = useState<string[]>([]);
  const [repo, setRepo] = useState("");
  const [tz, setTZ] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  const [hour, setHour] = useState(2);
  const [minute, setMinute] = useState(0);
  const [frequency, setFrequency] = useState<"daily" | "weekly">("daily");
  const [weekdays, setWeekdays] = useState<number[]>([1, 2, 3, 4, 5]);
  const [keepLatest, setKeepLatest] = useState(7);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [jobFilter, setJobFilter] = useState<"all" | "failing" | "paused">("all");
  const [uncertain, setUncertain] = useState(false);
  const [switchDraft, setSwitchDraft] = useState<SwitchDraft | null>(null);
  const [deleting, setDeleting] = useState<BackupPolicy | null>(null);
  const patchSwitch = (values: Partial<SwitchDraft>) => setSwitchDraft(cur => cur && { ...cur, ...values });
  const { policy: switching, target: switchTarget = "", targetRepo: switchTargetRepo, confirmed: switchConfirmed = false, uncertain: switchUncertain = false, taskStatus: switchTaskStatus = "" }: Partial<SwitchDraft> = switchDraft ?? {};
  // A request whose outcome is unknown survives leaving this page (see backup-held.ts); rebuild its form so the exact request can be retried.
  useEffect(() => {
    const body = held.bodies["policy-create"] as PolicyCreateBody | undefined;
    if (!body) return;
    setName(body.name); setVMs(body.vm_ids); setRepo(body.repository_id); setTZ(body.time_zone);
    setFrequency(body.rule.frequency as "daily" | "weekly"); setHour(body.rule.hour); setMinute(body.rule.minute);
    setWeekdays(body.rule.weekdays ?? [1, 2, 3, 4, 5]); setKeepLatest(body.retention.keep_latest);
    setStep(3); setUncertain(true);
  }, []);
  useEffect(() => {
    if (switchDraft || !policies || !repos) return;
    const key = Object.keys(held.bodies).find(k => k.startsWith("repository-"));
    const policy = key && policies.find(p => `repository-${p.id}` === key);
    if (!key || !policy) return;
    const target = (held.bodies[key] as RepositoryChangeBody).repository_id;
    setSwitchDraft({ policy, target, targetRepo: repos.find(r => r.id === target) || null, confirmed: true, uncertain: true, taskStatus: "" });
  }, [policies, repos]);
  const poolVMs = useMemo(() => data.vms.filter(v => v.pool_id === pool.id && !v.protected), [data.vms, pool.id]);
  const selectedRepository = repos?.find(r => r.id === repo);
  const selectedHealth = selectedRepository ? repositoryHealth(selectedRepository) : "unknown";
  const oldSwitchRepo = repos?.find(r => r.id === switching?.repository_id);
  const newSwitchRepo = switchUncertain ? switchTargetRepo : repos?.find(r => r.id === switchTarget);
  useEffect(() => { onRepositoryPanelChange(Boolean(switching)); }, [switching, onRepositoryPanelChange]);
  const [columnCount, setColumnCount] = useState(() => typeof matchMedia === "function" && matchMedia("(max-width: 600px)").matches ? 2 : 6);
  useEffect(() => {
    const media = matchMedia("(max-width: 600px)");
    const update = () => setColumnCount(media.matches ? 2 : 6);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  const blocker = (() => {
    if (unavailable) return "Controller inventory could not be loaded.";
    if (busy) return "Another task is already in progress.";
    if (!name.trim()) return "Enter a name.";
    if (!vms.length) return "Select at least one VM.";
    if (!repo) return "Select a repository.";
    if (!tz.trim()) return "Enter a time zone.";
    if (frequency === "weekly" && !weekdays.length) return "Select at least one weekday.";
    if (keepLatest < 1) return "Keep at least one recovery point.";
    return "";
  })();

  const close = () => {
    if (working || uncertain) return;
    delete held.bodies["policy-create"];
    setStep(1);
    onCloseCreate();
  };

  const change = (update: () => void) => {
    if (uncertain) return;
    delete held.bodies["policy-create"];
    update();
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (blocker || step !== 3) return;
    const key = "policy-create";
    setWorking(key);
    onError("");
    try {
      const outcome = await sendHeld(held, key, "bpol", (request_id): PolicyCreateBody => ({
        request_id,
        name: name.trim(),
        enabled: true,
        paused: false,
        repository_id: repo,
        capture_mode: "stopped",
        time_zone: tz.trim(),
        rule: { frequency, hour, minute, ...(frequency === "weekly" ? { weekdays } : {}) },
        vm_ids: vms,
        retention: { keep_latest: keepLatest },
      }), body => api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(pool.id)}/backup-policies`, "POST", body));
      if ("error" in outcome) {
        setUncertain(!outcome.definitive);
        onError(message(outcome.error));
        return;
      }
      setUncertain(false);
      onNotice(`${name.trim()}: backup job save requested`, backupOpID(outcome.reply));
      setStep(1);
      onCloseCreate();
      await reload().catch(err => onError(message(err)));
    } finally {
      setWorking("");
    }
  };

  const pause = async (policy: BackupPolicy, paused: boolean) => {
    const key = `pause-${policy.id}`;
    setWorking(key);
    onError("");
    try {
      const outcome = await sendHeld(held, key, "bpause", request_id => ({ request_id, paused }),
        body => api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(pool.id)}/backup-policies/${encodeURIComponent(policy.id)}/pause`, "POST", body));
      if ("error" in outcome) { onError(message(outcome.error)); return; }
      onNotice(`${policy.name}: ${paused ? "pause" : "resume"} requested`, backupOpID(outcome.reply));
      await reload();
    } catch (err) {
      onError(message(err));
    } finally {
      setWorking("");
    }
  };

  const runNow = async (policy: BackupPolicy, vmID: string) => {
    const key = `run-${policy.id}-${vmID}`;
    setWorking(key);
    onError("");
    try {
      const outcome = await sendHeld(held, key, "brun", request_id => ({ request_id, vm_id: vmID }),
        body => api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(pool.id)}/backup-policies/${encodeURIComponent(policy.id)}/run-now`, "POST", body));
      if ("error" in outcome) { onError(message(outcome.error)); return; }
      onNotice(`${policy.name}: run now`, backupOpID(outcome.reply));
      await reload();
    } catch (err) {
      onError(message(err));
    } finally {
      setWorking("");
    }
  };

  const changeRepository = async (e: FormEvent) => {
    e.preventDefault();
    if (!switching || !newSwitchRepo || newSwitchRepo.id === switching.repository_id || !switchConfirmed || working || unavailable || busy) return;
    const current = policies?.find(p => p.id === switching.id);
    if (!switchUncertain && (!current || current.revision !== switching.revision || current.repository_id !== switching.repository_id)) {
      onError("This backup job changed since you opened it. Close and reopen the change to review its current settings.");
      return;
    }
    const key = `repository-${switching.id}`;
    setWorking(key);
    onError("");
    try {
      // The job's VM list goes to Core exactly as Core reported it: only Core knows which VMs still exist.
      const outcome = await sendHeld(held, key, "bpol", request_id => repositoryChangeBody(switching, newSwitchRepo.id, request_id),
        body => requestRepositoryChange(api, pool.id, switching.id, body));
      if ("error" in outcome) {
        patchSwitch({ uncertain: !outcome.definitive });
        onError(message(outcome.error));
        return;
      }
      setSwitchDraft(null);
      onNotice(`${switching.name}: repository change requested`, backupOpID(outcome.reply));
      await reload().catch(err => onError(message(err)));
    } finally {
      setWorking("");
    }
  };

  const checkSwitchTask = async () => {
    if (!switching || !switchUncertain) return;
    const key = `repository-${switching.id}`;
    const pending = held.bodies[key];
    if (!pending) return;
    setWorking(key);
    onError("");
    try {
      const operations = await api<Operation[]>("operations");
      const task = operations.find(op => op.request_id === pending.request_id && op.kind === "backup-policy-upsert");
      if (!task) patchSwitch({ taskStatus: "No matching recent task is reported. Keep this panel open and retry the exact request." });
      else if (task.status === "succeeded") {
        delete held.bodies[key];
        setSwitchDraft(null);
        onNotice(`${switching.name}: repository change completed`, task.id);
        await reload();
      } else if (task.status === "failed") {
        delete held.bodies[key];
        setSwitchDraft(null);
        onError(task.error || "Backup job change failed. Reload the backup job before trying again.");
        await reload();
      } else patchSwitch({ taskStatus: `Task ${task.status}. Keep this panel open or retry the exact request.` });
    } catch (err) {
      onError(message(err));
    } finally {
      setWorking("");
    }
  };

  const closeSwitch = () => {
    if (working || switchUncertain || !switching) return;
    delete held.bodies[`repository-${switching.id}`];
    setSwitchDraft(null);
  };

  const toggleVM = (id: string) => {
    setVMs(cur => cur.includes(id) ? cur.filter(x => x !== id) : [...cur, id]);
  };

  const toggleDay = (d: number) => {
    setWeekdays(cur => cur.includes(d) ? cur.filter(x => x !== d) : [...cur, d].sort());
  };

  const failingCount = (policies || []).filter(p => failedVMs(p, runs).length > 0).length;
  const pausedCount = (policies || []).filter(p => p.paused).length;
  const shown = (policies || []).filter(p => jobFilter === "all" || (jobFilter === "failing" ? failedVMs(p, runs).length > 0 : p.paused));

  return <>
    <Section icon="backup" title="Backup jobs" count={policies?.length} actions={<>
      <button type="button" className="sm primary" disabled={unavailable || busy || Boolean(working) || Boolean(switching) || openCreate} onClick={onOpenCreate}>Create backup job</button>
    </>}>
      {error && !openCreate && !switching && <p className="why" role="alert">{error}</p>}
      {policies === null && !error ? <p className="muted backup-loading" role="status">Loading backup jobs…</p> : null}
      {policies && policies.length === 0 ? <div className="empty-state">
        <strong>No backup jobs yet</strong>
        <p>A backup job picks VMs, a repository and a schedule, then keeps the newest recovery points for you.</p>
        <button type="button" className="sm primary" disabled={unavailable || busy || Boolean(working) || Boolean(switching) || openCreate} onClick={onOpenCreate}>Create backup job</button>
      </div> : null}
      {policies && policies.length > 0 ? <>
        <div className="job-toolbar">
          <div className="chips" role="group" aria-label="Filter backup jobs">
            {([["all", "All", policies.length], ["failing", "Needs attention", runs === null ? null : failingCount], ["paused", "Paused", pausedCount]] as const).map(([key, label, n]) =>
              <button key={key} type="button" className="chip" aria-pressed={jobFilter === key} disabled={Boolean(switching) || key !== "all" && (n === 0 || n === null)} onClick={() => setJobFilter(key)}>{label}<span className="count">{n ?? "Unknown"}</span></button>)}
          </div>
          <span className="job-toolbar-end">
            <button type="button" className="link" onClick={() => setExpanded(Object.fromEntries(policies.map(p => [p.id, true])))}>Expand all</button>
            <button type="button" className="link" disabled={Boolean(switching)} onClick={() => setExpanded(Object.fromEntries(policies.map(p => [p.id, false])))}>Collapse all</button>
          </span>
        </div>
        {jobFilter === "failing" && runs === null ? <p className="why" role="status">Run history is unavailable. Jobs needing attention are unknown; choose All to inspect jobs.</p> : shown.length === 0 ? <p className="empty">No {jobFilter === "failing" ? "backup jobs need attention" : `${jobFilter} backup jobs`}.</p> : <div className="table-scroll"><table className="records jobs backup-jobs">
          <thead><tr><th>Name</th><th className="backup-secondary">Schedule</th><th className="backup-secondary">Next run</th><th className="backup-secondary">Last result</th><th className="backup-secondary">Status</th><th><span className="sr-only">Details</span></th></tr></thead>
          {shown.map(p => {
            const policyRepository = repos?.find(r => r.id === p.repository_id);
            const repoName = policyRepository?.name || p.repository_id;
            const health = policyRepository ? repositoryHealth(policyRepository) : "unknown";
            const results = jobVMResults(p.id, runs);
            const failed = failedVMs(p, runs);
            const open = expanded[p.id] ?? failed.length > 0;
            const vmIDs = p.vm_ids || [];
            const resultSummary = runs === null ? "Unknown" : results.size ? lastResult(vmIDs, results) : "No runs yet";
            const actionBlocker = working ? "Another action is in progress." : unavailable ? "Controller inventory could not be loaded." : busy ? "Another task is already in progress." : "";
            const repositoryBlocker = actionBlocker || (!p.revision ? "The job revision is not available." : repos === null ? error || "Loading backup repositories…" : !repos.some(r => r.id !== p.repository_id) ? "Register another backup repository in Settings first." : "");
            const toggle = () => { if (switching?.id !== p.id) setExpanded(cur => ({ ...cur, [p.id]: !open })); };
            return <tbody key={p.id} className={`job${open ? " open" : ""}`}>
              <tr className="job-row" onClick={toggle}>
                <td>
                  <div className="job-name">
                    <span><strong className="backup-primary">{p.name}</strong><small className="muted backup-meta">{vmIDs.length} VM{vmIDs.length === 1 ? "" : "s"} · {repoName} · {p.capture_mode}</small>
                      <small className="backup-row-detail">{resultSummary} · Last run: {date(p.last_dispatch_at)} · {p.paused ? "Paused" : p.enabled ? "Enabled" : "Disabled"}</small>
                      {health !== "online" && <small className="backup-meta"><Badge value={health} label={`Repository ${health}`} /> · Last check: {checkedAt(policyRepository?.health?.observed_at)}{health === "offline" && policyRepository?.health?.error ? ` · ${policyRepository.health.error}` : ""}</small>}</span>
                  </div>
                </td>
                <td className="muted backup-secondary">{p.rule?.frequency} {String(p.rule?.hour).padStart(2, "0")}:{String(p.rule?.minute).padStart(2, "0")} {p.time_zone}</td>
                <td className="muted backup-secondary">{nextRun(p)}</td>
                <td className="backup-secondary">{resultSummary}</td>
                <td className="backup-secondary">{p.paused ? <Badge value="paused" /> : p.enabled ? <Badge value="enabled" /> : <Badge value="disabled" />}</td>
                <td><button type="button" className="sm backup-row-open" aria-expanded={open} disabled={switching?.id === p.id} aria-label={`${open ? "Collapse" : "Inspect"} ${p.name}`} onClick={e => { e.stopPropagation(); toggle(); }}>{open ? "Collapse" : "Details"}</button></td>
              </tr>
              {open && <tr className="job-context"><td colSpan={columnCount}>
                <p className="muted backup-meta">{p.rule?.frequency} at {String(p.rule?.hour).padStart(2, "0")}:{String(p.rule?.minute).padStart(2, "0")} ({p.time_zone}) · Next run: {nextRun(p)}</p>
                <div className="actions">
                  <button type="button" className="sm" disabled={Boolean(repositoryBlocker) || openCreate || Boolean(switching)} title={repositoryBlocker || undefined} aria-label={`Change repository of ${p.name}`} onClick={() => { setExpanded(cur => ({ ...cur, [p.id]: true })); setSwitchDraft({ policy: p, target: "", targetRepo: null, confirmed: false, uncertain: false, taskStatus: "" }); onError(""); }}>Change repository</button>
                  <button type="button" className="sm" aria-label={`${p.paused ? "Resume" : "Pause"} ${p.name}`} disabled={Boolean(actionBlocker)} title={actionBlocker || undefined} onClick={() => void pause(p, !p.paused)}>
                    {held.bodies[`pause-${p.id}`] ? "Retry request" : p.paused ? "Resume" : "Pause"}
                  </button>
                  <button type="button" className="sm danger" aria-label={`Delete ${p.name}`} disabled={Boolean(actionBlocker) || openCreate || Boolean(switching)} title={actionBlocker || undefined} onClick={() => setDeleting(p)}>Delete</button>
                </div>
                {(repositoryBlocker || actionBlocker) && <p className="muted backup-meta">{repositoryBlocker || actionBlocker}</p>}
              </td></tr>}
              {open && vmIDs.map(vmID => {
                const result = results.get(vmID);
                const vm = data.vms.find(v => v.id === vmID);
                return <tr key={vmID} className="job-vm">
                  <td><div className="job-vm-name">
                    {vm ? <a className="backup-primary" href={`#/vms/${encodeURIComponent(vmID)}`}>{vm.name}</a> : <span className="muted">VM {vmID.slice(0, 8)}</span>}
                  </div><small className="backup-row-detail">{runs === null ? "Unknown" : result ? statusLabel(result.last.status) : "No runs yet"} · {result ? date(result.last.updated_at) : "Not observed"}</small>
                    {result?.last.error && <small className="muted job-error">{result.last.error}</small>}
                  </td>
                  <td className="muted backup-secondary" colSpan={2}>{runs === null ? "Last successful backup unknown" : result?.lastSuccess ? `Last backup ${date(result.lastSuccess.updated_at)}` : "Never backed up by this job"}</td>
                  <td className="backup-secondary">{runs === null ? <Badge value="unknown" /> : result ? <Badge value={result.last.status} /> : <span className="muted">No runs yet</span>}</td>
                  <td className="backup-secondary" />
                  <td><div className="row-actions inline">
                    <button type="button" className="sm" disabled={Boolean(actionBlocker)} title={actionBlocker || undefined} aria-label={`Run ${p.name} now for ${vm?.name || vmID}`} onClick={() => void runNow(p, vmID)}>{working === `run-${p.id}-${vmID}` ? "Starting…" : "Run now"}</button>
                    <button type="button" className="sm" disabled={openCreate || Boolean(switching)} title={openCreate || switching ? "Finish or cancel the open backup panel before viewing recovery points." : undefined} aria-label={`View points of ${vm?.name || vmID}`} onClick={() => onViewPoints(vmID)}>View points</button>
                  </div></td>
                </tr>;
              })}
            </tbody>;
          })}
        </table></div>}
      </> : null}
    </Section>

    {deleting && <DeleteBackupPolicy api={api} poolID={pool.id} policy={deleting} onClose={() => setDeleting(null)}
      onDone={operationID => { onNotice(`${deleting.name}: delete requested`, operationID); setDeleting(null); const refresh = () => reload().catch(err => onError(err instanceof Error ? err.message : String(err))); void refresh(); window.setTimeout(() => void refresh(), 3000); }} />}

    {switching && <Panel icon="backup" title="Change repository" onClose={closeSwitch} wide closeDisabled={Boolean(working) || switchUncertain} footer={<div className="actions">
      <button type="submit" form={switchFormID} className="sm primary" disabled={!newSwitchRepo || !switchConfirmed || Boolean(working) || unavailable || busy}>{switchUncertain ? "Retry exact request" : "Save repository change"}</button>
      <button type="button" className="sm" disabled={Boolean(working) || switchUncertain} onClick={closeSwitch}>Cancel</button>
    </div>}>
      <form id={switchFormID} className="stack backup-panel" onSubmit={changeRepository}>
        {error && <p className="why" role="alert">{error}</p>}
        {switchUncertain && <><p className="why" role="status">The previous reply was uncertain. Keep this panel open to check its task or retry the exact request.</p><button type="button" className="sm" disabled={Boolean(working)} onClick={() => void checkSwitchTask()}>Check task status</button></>}
        {switchTaskStatus && <p className="why" role="status">{switchTaskStatus}</p>}
        <dl className="kv backup-summary">
          <dt>Backup job</dt><dd>{switching.name}</dd>
          <dt>Current repository</dt><dd>{oldSwitchRepo?.name || switching.repository_id} · {oldSwitchRepo ? repositoryHealth(oldSwitchRepo) : "unknown"} · Last check: {checkedAt(oldSwitchRepo?.health?.observed_at)}</dd>
        </dl>
        <label>New repository<select required value={switchTarget} disabled={switchUncertain || Boolean(working)} onChange={e => patchSwitch({ target: e.target.value, targetRepo: repos?.find(r => r.id === e.target.value) || null, confirmed: false })}>
          <option value="">Select…</option>
          {repos?.filter(r => r.id !== switching.repository_id).map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select></label>
        {newSwitchRepo && repositoryHealth(newSwitchRepo) !== "online" && <p className="why" role="status">{newSwitchRepo.name} is {repositoryHealth(newSwitchRepo) === "offline" ? "offline" : "not confirmed online"}. Core will check access before a backup runs.</p>}
        <label className="check" style={{ justifyContent: "flex-start" }}><input type="checkbox" required checked={switchConfirmed} disabled={!newSwitchRepo || switchUncertain || Boolean(working)} onChange={e => patchSwitch({ confirmed: e.target.checked })} />I confirm this backup job should use {newSwitchRepo?.name || "the selected repository"}</label>
      </form>
    </Panel>}

    {openCreate && !switching ? <Panel icon="backup" title="Create backup job" onClose={close} wide closeDisabled={Boolean(working) || uncertain} footer={<div className="actions">
      {step > 1 ? <button type="button" className="sm" disabled={Boolean(working) || uncertain} onClick={() => setStep(s => (s - 1) as Step)}>Back</button> : null}
      {step < 3 ? <button type="button" className="sm primary" disabled={Boolean(working) || uncertain || unavailable || busy || (step === 1 && (!name.trim() || !vms.length || !repo)) || (step === 2 && Boolean(blocker))} onClick={() => setStep(s => (s + 1) as Step)}>Next: {stepNames[step]}</button> : null}
      {step === 3 ? <button type="submit" form={formID} className="sm primary" disabled={Boolean(blocker) || Boolean(working)}>{working === "policy-create" ? "Submitting…" : uncertain ? "Retry exact request" : "Save backup job"}</button> : null}
      <button type="button" className="sm" disabled={Boolean(working) || uncertain} onClick={close}>Cancel</button>
    </div>}>
      <form id={formID} className="stack backup-panel" onSubmit={submit}>
        {error && <p className="why" role="alert">{error}</p>}
        {uncertain && <p className="why" role="status">The previous reply was uncertain. Retry this exact backup job request, or check its task in <a href="#/tasks">Tasks</a>; the request is kept if you leave this page.</p>}
        <ol className="backup-steps" aria-label="Create backup job steps">
          {stepNames.map((label, i) => <li key={label} aria-current={step === i + 1 ? "step" : undefined}>{label}</li>)}
        </ol>
        <h3>{stepNames[step - 1]}</h3>
        {(unavailable || busy) && <p className="why" role="status">{unavailable ? "Controller inventory could not be loaded." : "Another task is already in progress."}</p>}
        {step === 1 ? <>
          <label>Name<input value={name} disabled={uncertain} onChange={e => change(() => setName(e.target.value))} required /></label>
          {repos === null ? !error && <p className="muted" role="status">Loading backup repositories…</p> : repos.length ? <label>Repository<select value={repo} disabled={uncertain} onChange={e => change(() => setRepo(e.target.value))} required>
            <option value="">Select…</option>
            {repos.map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select></label> : <p className="why" role="status">A backup repository is required. <a href="#/settings/backups">Open Backup repositories in Settings</a> to register a destination, then return to create the job.</p>}
          <fieldset>
            <legend>VMs · {vms.length} selected</legend>
            {poolVMs.map(v => <label key={v.id} className="check" style={{ justifyContent: "flex-start", alignItems: "center" }}>
              <input type="checkbox" checked={vms.includes(v.id)} disabled={uncertain} onChange={() => change(() => toggleVM(v.id))} />{v.name}
            </label>)}
            {!poolVMs.length ? <p className="muted">No VMs in this pool.</p> : null}
          </fieldset>
          <p className="muted backup-meta" role="status">{vms.length} VM{vms.length === 1 ? "" : "s"} selected.</p>
        </> : null}
        {step === 2 ? <>
          <label>Time zone<select value={tz} disabled={uncertain} onChange={e => change(() => setTZ(e.target.value))} required>
            {(timeZones.includes(tz) ? timeZones : [tz, ...timeZones]).map(z => <option key={z} value={z}>{z.replace(/_/g, " ")}</option>)}
          </select></label>
          <label>Frequency<select value={frequency} disabled={uncertain} onChange={e => change(() => setFrequency(e.target.value as "daily" | "weekly"))}>
            <option value="daily">Daily</option>
            <option value="weekly">Weekly</option>
          </select></label>
          {frequency === "weekly" ? <fieldset>
            <legend>Days</legend>
            {weekdayLabels.map((label, i) => <label key={label} className="check">
              <input type="checkbox" checked={weekdays.includes(i)} disabled={uncertain} onChange={() => change(() => toggleDay(i))} />{label}
            </label>)}
          </fieldset> : null}
          <div className="vm-fields">
            <label>Start time<input type="time" required value={`${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`} disabled={uncertain} onChange={e => change(() => {
              const [h, m] = e.target.value.split(":").map(Number);
              if (Number.isInteger(h) && Number.isInteger(m)) { setHour(h); setMinute(m); }
            })} /></label>
            <label>Keep latest<input type="number" min={1} max={365} value={keepLatest} disabled={uncertain} onChange={e => change(() => setKeepLatest(Number(e.target.value)))} /></label>
          </div>
          <p className="muted">Each backup keeps this backup job's newest {keepLatest} recovery points per VM and removes its older ones. Only points this backup job created are ever removed; manual backups and other backup jobs' points are never pruned. Recovery points are also kept if they are protected, restoring, or newer than the age window; those do not count toward the {keepLatest}.</p>
          {frequency === "weekly" && !weekdays.length ? <p className="why" role="alert">Select at least one weekday.</p> : null}
        </> : null}
        {step === 3 ? <>
          <dl className="kv backup-summary">
            <dt>Name</dt><dd>{name.trim()}</dd>
            <dt>VMs</dt><dd>{vms.length} selected<ul>{vms.map(id => <li key={id}>{data.vms.find(v => v.id === id)?.name || id}</li>)}</ul></dd>
            <dt>Repository</dt><dd>{repos?.find(r => r.id === repo)?.name || repo}</dd>
            <dt>Schedule</dt><dd>{frequency === "daily" ? "Every day" : weekdays.map(d => weekdayLabels[d]).join(", ")} at {String(hour).padStart(2, "0")}:{String(minute).padStart(2, "0")} ({tz})</dd>
            <dt>Retention</dt><dd>Keep latest {keepLatest}</dd>
            <dt>Capture</dt><dd>stopped</dd>
          </dl>
          {blocker ? <p className="why" role="status">{blocker}</p> : null}
        </> : null}
        {(step === 1 || step === 3) && selectedRepository && selectedHealth !== "online" ? <p className="why" role="status">{selectedRepository.name} is {selectedHealth === "offline" ? "offline" : "not confirmed online"}. Last check: {checkedAt(selectedRepository.health?.observed_at)}. Core will check it before a backup runs; this backup job will keep the selected repository.</p> : null}
      </form>
    </Panel> : null}
  </>;
}
