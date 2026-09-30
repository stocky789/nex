// SPDX-License-Identifier: AGPL-3.0-only
import { useMemo, useRef, useState, type FormEvent } from "react";
import type { API, Data, Operation, Pool } from "./types";
import { Panel, Section, Badge, date } from "./ui";
import { backupOpID, repositoryHealth, stableBody, type BackupRepository } from "./backup-types";

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
  active: boolean;
  api: API;
  data: Data;
  pool: Pool;
  repos: BackupRepository[];
  policies: BackupPolicy[] | null;
  busy: boolean;
  unavailable: boolean;
  working: string;
  setWorking: (v: string) => void;
  onNotice: (text: string, operationID?: string) => void;
  onError: (text: string) => void;
  error: string;
  reload: () => Promise<void>;
  requestID: (key: string, prefix: string) => string;
  clearRequest: (key: string) => void;
  openCreate: boolean;
  onOpenCreate: () => void;
  onCloseCreate: () => void;
};

type Step = 1 | 2 | 3;
type SwitchDraft = { policy: BackupPolicy; target: string; targetRepo: BackupRepository | null; confirmed: boolean; uncertain: boolean; taskStatus: string };

const weekdayLabels = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export function repositoryChangeBody(policy: BackupPolicy, repositoryID: string, requestID: string) {
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

export function requestRepositoryChange(api: API, poolID: string, policyID: string, body: ReturnType<typeof repositoryChangeBody>) {
  return api<{ operation?: { id?: string; status?: string } }>(`pools/${encodeURIComponent(poolID)}/backup-policies/${encodeURIComponent(policyID)}`, "PUT", body);
}

export function BackupPolicies({
  active, api, data, pool, repos, policies, busy, unavailable, working, setWorking, error,
  onNotice, onError, reload, requestID, clearRequest, openCreate, onOpenCreate, onCloseCreate,
}: Props) {
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
  const [runVM, setRunVM] = useState<Record<string, string>>({});
  const [uncertain, setUncertain] = useState(false);
  const [switchDraft, setSwitchDraft] = useState<SwitchDraft | null>(null);
  const patchSwitch = (values: Partial<SwitchDraft>) => setSwitchDraft(cur => cur && { ...cur, ...values });
  const { policy: switching, target: switchTarget = "", targetRepo: switchTargetRepo, confirmed: switchConfirmed = false, uncertain: switchUncertain = false, taskStatus: switchTaskStatus = "" }: Partial<SwitchDraft> = switchDraft ?? {};
  const pendingPause = useRef<Record<string, { paused: boolean }>>({});
  const pendingSwitch = useRef<Record<string, ReturnType<typeof repositoryChangeBody>>>({});
  const poolVMs = useMemo(() => data.vms.filter(v => v.pool_id === pool.id && !v.protected), [data.vms, pool.id]);
  const selectedRepository = repos.find(r => r.id === repo);
  const selectedHealth = selectedRepository ? repositoryHealth(selectedRepository) : "unknown";
  const oldSwitchRepo = repos.find(r => r.id === switching?.repository_id);
  const newSwitchRepo = switchUncertain ? switchTargetRepo : repos.find(r => r.id === switchTarget);

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
    clearRequest("policy-create");
    setStep(1);
    onCloseCreate();
  };

  const change = (update: () => void) => {
    if (uncertain) return;
    clearRequest("policy-create");
    update();
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (blocker || step !== 3) return;
    const key = "policy-create";
    setWorking(key);
    onError("");
    try {
      const reply = await api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(pool.id)}/backup-policies`, "POST", {
        request_id: requestID(key, "bpol"),
        name: name.trim(),
        enabled: true,
        paused: false,
        repository_id: repo,
        capture_mode: "stopped",
        time_zone: tz.trim(),
        rule: {
          frequency,
          hour,
          minute,
          ...(frequency === "weekly" ? { weekdays } : {}),
        },
        vm_ids: vms,
        retention: { keep_latest: keepLatest },
      });
      clearRequest(key);
      setUncertain(false);
      onNotice(`${name.trim()}: policy save requested`, backupOpID(reply));
      setStep(1);
      onCloseCreate();
      await reload().catch(err => onError(err instanceof Error ? err.message : String(err)));
    } catch (err) {
      const status = (err as { status?: number }).status;
      if (status && status >= 400 && status < 500 && status !== 409) {
        clearRequest(key);
        setUncertain(false);
      } else setUncertain(true);
      onError(err instanceof Error ? err.message : String(err));
    } finally {
      setWorking("");
    }
  };

  const pause = async (policy: BackupPolicy, paused: boolean) => {
    const key = `pause-${policy.id}`;
    setWorking(key);
    onError("");
    try {
      const body = stableBody(pendingPause.current, key, { paused });
      const reply = await api<{ operation?: { id?: string } }>(
        `pools/${encodeURIComponent(pool.id)}/backup-policies/${encodeURIComponent(policy.id)}/pause`,
        "POST",
        { request_id: requestID(key, "bpause"), ...body },
      );
      clearRequest(key);
      delete pendingPause.current[key];
      onNotice(`${policy.name}: ${body.paused ? "pause" : "resume"} requested`, backupOpID(reply));
      await reload();
    } catch (err) {
      const status = (err as { status?: number }).status;
      if (status && status >= 400 && status < 500 && status !== 409) {
        clearRequest(key);
        delete pendingPause.current[key];
      }
      onError(err instanceof Error ? err.message : String(err));
    } finally {
      setWorking("");
    }
  };

  const runNow = async (policy: BackupPolicy, vmID: string) => {
    const key = `run-${policy.id}-${vmID}`;
    setWorking(key);
    onError("");
    try {
      const reply = await api<{ operation?: { id?: string } }>(
        `pools/${encodeURIComponent(pool.id)}/backup-policies/${encodeURIComponent(policy.id)}/run-now`,
        "POST",
        { request_id: requestID(key, "brun"), vm_id: vmID },
      );
      clearRequest(key);
      onNotice(`${policy.name}: run now`, backupOpID(reply));
      await reload();
    } catch (err) {
      onError(err instanceof Error ? err.message : String(err));
    } finally {
      setWorking("");
    }
  };

  const changeRepository = async (e: FormEvent) => {
    e.preventDefault();
    if (!switching || !newSwitchRepo || newSwitchRepo.id === switching.repository_id || !switchConfirmed || working || unavailable || busy) return;
    const current = policies?.find(p => p.id === switching.id);
    if (!switchUncertain && (!current || current.revision !== switching.revision || current.repository_id !== switching.repository_id)) {
      onError("This policy changed since you opened it. Close and reopen the change to review its current settings.");
      return;
    }
    const key = `repository-${switching.id}`;
    setWorking(key);
    onError("");
    try {
      const body = stableBody(pendingSwitch.current, key, repositoryChangeBody(switching, newSwitchRepo.id, requestID(key, "bpol")));
      const reply = await requestRepositoryChange(api, pool.id, switching.id, body);
      clearRequest(key);
      delete pendingSwitch.current[key];
      setSwitchDraft(null);
      onNotice(`${switching.name}: repository change requested`, backupOpID(reply));
      await reload().catch(err => onError(err instanceof Error ? err.message : String(err)));
    } catch (err) {
      const status = (err as { status?: number }).status;
      if (status && status >= 400 && status < 500 && status !== 409) {
        clearRequest(key);
        delete pendingSwitch.current[key];
        patchSwitch({ uncertain: false });
      } else patchSwitch({ uncertain: true });
      onError(err instanceof Error ? err.message : String(err));
    } finally {
      setWorking("");
    }
  };

  const checkSwitchTask = async () => {
    if (!switching || !switchUncertain) return;
    const key = `repository-${switching.id}`;
    const held = pendingSwitch.current[key];
    if (!held) return;
    setWorking(key);
    onError("");
    try {
      const operations = await api<Operation[]>("operations");
      const task = operations.find(op => op.request_id === held.request_id && op.kind === "backup-policy-upsert");
      if (!task) patchSwitch({ taskStatus: "No matching recent task is reported. Keep this panel open and retry the exact request." });
      else if (task.status === "succeeded") {
        clearRequest(key);
        delete pendingSwitch.current[key];
        setSwitchDraft(null);
        onNotice(`${switching.name}: repository change completed`, task.id);
        await reload();
      } else if (task.status === "failed") {
        clearRequest(key);
        delete pendingSwitch.current[key];
        setSwitchDraft(null);
        onError(task.error || "Policy change failed. Reload the policy before trying again.");
        await reload();
      } else patchSwitch({ taskStatus: `Task ${task.status}. Keep this panel open or retry the exact request.` });
    } catch (err) {
      onError(err instanceof Error ? err.message : String(err));
    } finally {
      setWorking("");
    }
  };

  const closeSwitch = () => {
    if (working || switchUncertain || !switching) return;
    const key = `repository-${switching.id}`;
    clearRequest(key);
    delete pendingSwitch.current[key];
    setSwitchDraft(null);
  };

  const toggleVM = (id: string) => {
    setVMs(cur => cur.includes(id) ? cur.filter(x => x !== id) : [...cur, id]);
  };

  const toggleDay = (d: number) => {
    setWeekdays(cur => cur.includes(d) ? cur.filter(x => x !== d) : [...cur, d].sort());
  };

  if (!active) return null;

  return <>
    <Section icon="backup" title="Policies" count={policies?.length ?? 0} actions={<>
      <button type="button" className="sm primary" disabled={unavailable || busy || Boolean(working)} onClick={onOpenCreate}>Create policy</button>
    </>}>
      {error && <p className="why" role="alert">{error}</p>}
      {policies === null && !error ? <p className="muted">Loading…</p> : null}
      {policies && policies.length === 0 ? <p className="empty">No backup policies.</p> : null}
      {policies && policies.length > 0 ? <div className="table-scroll"><table className="records">
        <thead><tr><th>Name</th><th>VMs</th><th>Schedule</th><th>Next</th><th>Status</th><th><span className="sr-only">Actions</span></th></tr></thead>
        <tbody>
          {policies.map(p => {
            const policyRepository = repos.find(r => r.id === p.repository_id);
            const repoName = policyRepository?.name || p.repository_id;
            const health = policyRepository ? repositoryHealth(policyRepository) : "unknown";
            return <tr key={p.id}>
              <td><strong>{p.name}</strong><small className="muted">{repoName} · {p.capture_mode}</small>{health !== "online" && <small><Badge value={health} label={`Repository ${health}`} /> · Last check: {date(policyRepository?.health?.observed_at)}{health === "offline" && policyRepository?.health?.error ? ` · ${policyRepository.health.error}` : ""}</small>}</td>
              <td>{p.vm_ids?.length || 0}</td>
              <td className="muted">{p.rule?.frequency} {String(p.rule?.hour).padStart(2, "0")}:{String(p.rule?.minute).padStart(2, "0")} {p.time_zone}</td>
              <td className="muted">{p.next_dispatch_at ? date(p.next_dispatch_at) : "—"}</td>
              <td>{p.paused ? <Badge value="paused" /> : p.enabled ? <Badge value="enabled" /> : <Badge value="disabled" />}</td>
              <td className="row-actions">
                <button type="button" className="sm" disabled={Boolean(working) || unavailable || busy || !p.revision || !repos.some(r => r.id !== p.repository_id)} onClick={() => { setSwitchDraft({ policy: p, target: "", targetRepo: null, confirmed: false, uncertain: false, taskStatus: "" }); onError(""); }}>Change repository</button>
                <button type="button" className="sm" disabled={Boolean(working) || unavailable || busy} onClick={() => pause(p, !p.paused)}>
                  {pendingPause.current[`pause-${p.id}`] ? "Retry request" : p.paused ? "Resume" : "Pause"}
                </button>
                {(p.vm_ids || []).length > 0 && <>
                  <select aria-label={`VM for ${p.name} Run now`} value={p.vm_ids.includes(runVM[p.id]) ? runVM[p.id] : p.vm_ids[0]} onChange={e => setRunVM(cur => ({ ...cur, [p.id]: e.target.value }))} disabled={Boolean(working) || unavailable || busy}>
                    {p.vm_ids.map(vmID => <option key={vmID} value={vmID}>{data.vms.find(v => v.id === vmID)?.name || vmID}</option>)}
                  </select>
                  <button type="button" className="sm" disabled={Boolean(working) || unavailable || busy} onClick={() => runNow(p, p.vm_ids.includes(runVM[p.id]) ? runVM[p.id] : p.vm_ids[0])}>Run now</button>
                </>}
              </td>
            </tr>;
          })}
        </tbody>
      </table></div> : null}
    </Section>

    {switching && <Panel icon="backup" title="Change repository" onClose={closeSwitch}>
      <form className="stack" onSubmit={changeRepository}>
        {error && <p className="why" role="alert">{error}</p>}
        {switchUncertain && <><p className="why" role="status">The previous reply was uncertain. Keep this panel open to check its task or retry the exact request.</p><button type="button" className="sm" disabled={Boolean(working)} onClick={() => void checkSwitchTask()}>Check task status</button></>}
        {switchTaskStatus && <p className="why" role="status">{switchTaskStatus}</p>}
        <dl className="kv">
          <dt>Policy</dt><dd>{switching.name}</dd>
          <dt>Current repository</dt><dd>{oldSwitchRepo?.name || switching.repository_id} · {oldSwitchRepo ? repositoryHealth(oldSwitchRepo) : "unknown"} · Last check: {date(oldSwitchRepo?.health?.observed_at)}</dd>
          <dt>New repository</dt><dd>{newSwitchRepo ? `${newSwitchRepo.name} · ${repositoryHealth(newSwitchRepo)} · Last check: ${date(newSwitchRepo.health?.observed_at)}` : "Select a repository"}</dd>
        </dl>
        <label>New repository<select required value={switchTarget} disabled={switchUncertain} onChange={e => patchSwitch({ target: e.target.value, targetRepo: repos.find(r => r.id === e.target.value) || null, confirmed: false })}>
          <option value="">Select…</option>
          {repos.filter(r => r.id !== switching.repository_id).map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select></label>
        {newSwitchRepo && repositoryHealth(newSwitchRepo) !== "online" && <p className="why" role="status">{newSwitchRepo.name} is {repositoryHealth(newSwitchRepo) === "offline" ? "offline" : "not confirmed online"}. Core will check access before a backup runs.</p>}
        <label className="check" style={{ justifyContent: "flex-start" }}><input type="checkbox" required checked={switchConfirmed} disabled={!newSwitchRepo || switchUncertain} onChange={e => patchSwitch({ confirmed: e.target.checked })} />I confirm this policy should use {newSwitchRepo?.name || "the selected repository"}</label>
        <div className="actions">
          <button type="submit" className="sm primary" disabled={!newSwitchRepo || !switchConfirmed || Boolean(working) || unavailable || busy}>Save repository change</button>
          <button type="button" className="sm" disabled={Boolean(working)} onClick={closeSwitch}>Cancel</button>
        </div>
      </form>
    </Panel>}

    {openCreate ? <Panel icon="backup" title="Create policy" onClose={close} wide>
      <form className="stack" onSubmit={submit}>
        {error && <p className="why" role="alert">{error}</p>}
        {uncertain && <p className="why" role="status">The previous reply was uncertain. Check <a href="#/tasks">Tasks</a>, or retry this exact policy request.</p>}
        <p className="muted">Step {step} of 3</p>
        {step === 1 ? <>
          <label>Name<input value={name} disabled={uncertain} onChange={e => change(() => setName(e.target.value))} required /></label>
          <label>Repository<select value={repo} disabled={uncertain} onChange={e => change(() => setRepo(e.target.value))} required>
            <option value="">Select…</option>
            {repos.map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select></label>
          <fieldset>
            <legend>VMs</legend>
            {poolVMs.map(v => <label key={v.id} className="check">
              <input type="checkbox" checked={vms.includes(v.id)} disabled={uncertain} onChange={() => change(() => toggleVM(v.id))} />{v.name}
            </label>)}
            {!poolVMs.length ? <p className="muted">No VMs in this pool.</p> : null}
          </fieldset>
        </> : null}
        {step === 2 ? <>
          <label>Time zone<input value={tz} disabled={uncertain} onChange={e => change(() => setTZ(e.target.value))} required /></label>
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
            <label>Hour<input type="number" min={0} max={23} value={hour} disabled={uncertain} onChange={e => change(() => setHour(Number(e.target.value)))} /></label>
            <label>Minute<input type="number" min={0} max={59} value={minute} disabled={uncertain} onChange={e => change(() => setMinute(Number(e.target.value)))} /></label>
            <label>Keep latest<input type="number" min={1} max={365} value={keepLatest} disabled={uncertain} onChange={e => change(() => setKeepLatest(Number(e.target.value)))} /></label>
          </div>
          <p className="muted">Each backup keeps this policy's newest {keepLatest} recovery points per VM and removes its older ones. Only points this policy created are ever removed; manual backups and other policies' points are never pruned. Recovery points are also kept if they are protected, restoring, or newer than the age window; those do not count toward the {keepLatest}.</p>
          {frequency === "weekly" && !weekdays.length ? <p className="why" role="alert">Select at least one weekday.</p> : null}
        </> : null}
        {step === 3 ? <>
          <dl className="kv">
            <dt>Name</dt><dd>{name.trim()}</dd>
            <dt>VMs</dt><dd>{vms.length}</dd>
            <dt>Repository</dt><dd>{repos.find(r => r.id === repo)?.name || repo}</dd>
            <dt>Schedule</dt><dd>{frequency} {String(hour).padStart(2, "0")}:{String(minute).padStart(2, "0")} {tz}</dd>
            <dt>Retention</dt><dd>Keep latest {keepLatest}</dd>
            <dt>Capture</dt><dd>stopped</dd>
          </dl>
          {blocker ? <p className="why" role="alert">{blocker}</p> : null}
        </> : null}
        {(step === 1 || step === 3) && selectedRepository && selectedHealth !== "online" ? <p className="why" role="status">{selectedRepository.name} is {selectedHealth === "offline" ? "offline" : "not confirmed online"}. Last check: {date(selectedRepository.health?.observed_at)}. Core will check it before a backup runs; this policy will keep the selected repository.</p> : null}
        <div className="row-actions">
          {step > 1 ? <button type="button" className="sm" disabled={Boolean(working)} onClick={() => setStep(s => (s - 1) as Step)}>Back</button> : null}
          {step < 3 ? <button type="button" className="sm primary" disabled={(step === 1 && (!name.trim() || !vms.length || !repo)) || (step === 2 && Boolean(blocker))} onClick={() => setStep(s => (s + 1) as Step)}>Next</button> : null}
          {step === 3 ? <button type="submit" className="sm primary" disabled={Boolean(blocker) || Boolean(working)}>Save policy</button> : null}
          <button type="button" className="sm" disabled={Boolean(working)} onClick={close}>Cancel</button>
        </div>
      </form>
    </Panel> : null}
  </>;
}
