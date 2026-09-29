// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import type { API, Data, Pool } from "./types";
import type { Storage } from "./managed";
import { Badge, Panel, Section, date } from "./ui";
import { Icon } from "./icons";
import {
  appendBackupPage, backupOpID, collectBackupPageItems, refreshBackupPage, rid, stableBody,
  type BackupCatalogPage, type BackupRecoveryPoint, type BackupRepository, type BackupRun, type BackupRunPage,
} from "./backup-types";
import { BackupPolicies, type BackupPolicy } from "./backup-policies";
import "./settings.css";

type Props = {
  api: API;
  data: Data;
  pool?: Pool;
  busy: boolean;
  unavailable: boolean;
  reload: () => Promise<void>;
  onNotice: (text: string, operationID?: string) => void;
  vmFilter?: string;
  flow?: string;
};

type BackupNowStep = 1 | 2 | 3;
type RestoreStep = 1 | 2 | 3;
type RestoreMode = "new" | "replace" | "test";
type RestoreDraft = { name: string; host: string; storage: string; vmID: string; confirmation: string };
const restoreKey = (mode: RestoreMode, pointID: string) => `${mode}-${pointID}`;

function storageUsable(s: Storage, hostID?: string) {
  if (s.tombstone || !s.capabilities?.vm_disk) return false;
  if (!hostID) return true;
  if (s.kind === "nfs" || s.kind === "smb" || (s.kind === "iscsi" && s.source?.iscsi?.mode === "clustered_gfs2")) {
    return s.member_access?.some(a => a.host_id === hostID && a.readable && a.writable && a.qemu_writable && a.locking) === true;
  }
  return s.host_id === hostID;
}

function defaultRestoreName(point: BackupRecoveryPoint) {
  const base = (point.source_vm_name || "restored").replace(/\s+/g, "-").slice(0, 40);
  return `${base}-restore`;
}

export function replacementTarget(point: BackupRecoveryPoint | null, vms: Data["vms"]) {
  return point ? vms.find(v => v.id === point.source_vm_id) : undefined;
}

// retentionWarnings lists succeeded backups whose retention could not remove some
// expired points; Core records that on the run's error.
export function retentionWarnings(runs: BackupRun[] | null) {
  return (runs || []).filter(run => run.status === "succeeded" && run.error && run.kind !== "backup-test-restore");
}

export function activeTestRestores(runs: BackupRun[] | null, point: BackupRecoveryPoint | null, vms: Data["vms"], poolID: string) {
  if (!point) return [];
  return (runs || [])
    .filter(run => run.kind === "backup-test-restore" && run.status === "succeeded" && run.recovery_point_id === point.id && run.repository_id === point.repository_id)
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at))
    .flatMap(run => {
      const vm = run.host_id && vms.find(candidate => candidate.id === run.new_vm_id && candidate.pool_id === poolID && candidate.host_id === run.host_id);
      return vm ? [{ run, vm }] : [];
    });
}

export function Backups({ api, data, pool, busy, unavailable, reload, onNotice, vmFilter = "", flow = "" }: Props) {
  const [catalogPage, setCatalogPage] = useState<BackupCatalogPage | null>(null);
  const [repos, setRepos] = useState<BackupRepository[] | null>(null);
  const [policies, setPolicies] = useState<BackupPolicy[] | null>(null);
  const [pointRuns, setPointRuns] = useState<BackupRun[] | null>(null);
  const [error, setError] = useState("");
  const [catalogError, setCatalogError] = useState("");
  const [repositoryError, setRepositoryError] = useState("");
  const [policyError, setPolicyError] = useState("");
  const [runError, setRunError] = useState("");
  const [pageNotice, setPageNotice] = useState("");
  const [loadingMore, setLoadingMore] = useState("");
  const [query, setQuery] = useState("");
  const [filterVM, setFilterVM] = useState(vmFilter);
  const [filterRepo, setFilterRepo] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [backupOpen, setBackupOpen] = useState(flow === "backup-now");
  const [policyOpen, setPolicyOpen] = useState(flow === "create-policy");
  const [tab, setTab] = useState<"points" | "policies">(flow === "create-policy" ? "policies" : "points");
  const [backupStep, setBackupStep] = useState<BackupNowStep>(1);
  const [backupVM, setBackupVM] = useState(vmFilter);
  const [backupRepo, setBackupRepo] = useState("");
  const [backupMode, setBackupMode] = useState<"stopped" | "crash" | "quiesced">("stopped");
  const [backupUncertain, setBackupUncertain] = useState(false);
  const [restoreMode, setRestoreMode] = useState<RestoreMode>("new");
  const [restoreStep, setRestoreStep] = useState<RestoreStep>(1);
  const [restoreName, setRestoreName] = useState("");
  const [restoreStorage, setRestoreStorage] = useState("");
  const [restoreHost, setRestoreHost] = useState("");
  const [restoreVMID, setRestoreVMID] = useState<string>(() => crypto.randomUUID());
  const [replaceConfirm, setReplaceConfirm] = useState("");
  const [cleanupConfirm, setCleanupConfirm] = useState<Record<string, string>>({});
  const [working, setWorking] = useState("");
  const requests = useRef<Record<string, string>>({});
  const pendingBackup = useRef<Record<string, { vm_id: string; repository_id: string; consistency: string }>>({});
  const pendingProtect = useRef<Record<string, { repository_id: string; recovery_point_id: string; protected: boolean }>>({});
  const restoreDrafts = useRef<Record<string, RestoreDraft>>({});
  const loadGen = useRef(0);
  const loadAbort = useRef<AbortController | null>(null);
  const catalogGen = useRef(0);
  const catalogAbort = useRef<AbortController | null>(null);
  const catalogScope = useRef("");
  const runGen = useRef(0);
  const runAbort = useRef<AbortController | null>(null);
  const runLoading = useRef("");
  const filters = useRef({ filterVM, filterRepo, query });
  filters.current = { filterVM, filterRepo, query };
  const selectedIDRef = useRef(selectedID);
  selectedIDRef.current = selectedID;
  const filterKey = `${filterVM}\x00${filterRepo}\x00${query}`;
  const currentFilterKey = () => `${filters.current.filterVM}\x00${filters.current.filterRepo}\x00${filters.current.query}`;
  const previousFilterKey = useRef(filterKey);

  const poolID = pool?.id || "";
  const points = catalogPage?.items ?? null;
  const runs = pointRuns;
  const poolVMs = useMemo(() => data.vms.filter(v => !poolID || v.pool_id === poolID), [data.vms, poolID]);
  const poolStorage = useMemo(() => data.storage.filter(s => !poolID || s.pool_id === poolID), [data.storage, poolID]);
  const repoByID = useMemo(() => new Map((repos || []).map(r => [r.id, r])), [repos]);
  const selected = points?.find(p => p.id === selectedID) || null;
  const restoreOriginal = replacementTarget(selected, poolVMs);
  const uncertainRestore = Boolean(selected && restoreDrafts.current[restoreKey(restoreMode, selected.id)]);
  const activeTests = activeTestRestores(runs, selected, data.vms, poolID);

  const catalogPath = () => {
    const { filterVM, filterRepo, query } = filters.current;
    const params = new URLSearchParams();
    if (filterVM) params.set("vm_id", filterVM);
    if (filterRepo) params.set("repository_id", filterRepo);
    if (query.trim()) params.set("search", query.trim());
    return `pools/${encodeURIComponent(poolID)}/backup-catalog${params.size ? `?${params}` : ""}`;
  };

  const loadCatalog = async () => {
    if (!poolID) { setCatalogPage({ items: [], generation: "", total: 0, untested_count: 0, backup_days_14: [] }); return; }
    catalogAbort.current?.abort();
    const abort = new AbortController();
    catalogAbort.current = abort;
    const gen = ++catalogGen.current;
    const scope = currentFilterKey();
    try {
      const page = await api<BackupCatalogPage>(catalogPath(), "GET", undefined, undefined, undefined, abort.signal);
      if (gen !== catalogGen.current || abort.signal.aborted) return;
      const sameScope = catalogScope.current === scope;
      catalogScope.current = scope;
      setCatalogPage(current => refreshBackupPage(current, page, sameScope));
      setCatalogError("");
    } catch (err) {
      if (gen !== catalogGen.current || abort.signal.aborted) return;
      setCatalogPage(null);
      setCatalogError(`Recovery points unavailable: ${String(err)}`);
    }
  };

  // loadRuns collects the selected point's complete run history. A poll keeps the
  // previous history on screen and never restarts a collection still in progress.
  const loadRuns = async (retry = 0) => {
    const pointID = selectedIDRef.current;
    if (!poolID || !pointID || (retry === 0 && runLoading.current === pointID)) return;
    runAbort.current?.abort();
    const abort = new AbortController();
    runAbort.current = abort;
    const gen = ++runGen.current;
    runLoading.current = pointID;
    const base = `pools/${encodeURIComponent(poolID)}/backup-runs?recovery_point_id=${encodeURIComponent(pointID)}`;
    try {
      const response = await api<BackupRunPage>(base, "GET", undefined, undefined, undefined, abort.signal);
      if (gen !== runGen.current || abort.signal.aborted) return;
      const all = await collectBackupPageItems(response, (cursor, generation) => api<BackupRunPage>(`${base}&cursor=${encodeURIComponent(cursor)}&generation=${encodeURIComponent(generation)}`, "GET", undefined, undefined, undefined, abort.signal));
      if (gen !== runGen.current || abort.signal.aborted) return;
      runLoading.current = "";
      setPointRuns(all);
      setRunError("");
    } catch (err) {
      if (gen !== runGen.current || abort.signal.aborted) return;
      runLoading.current = "";
      if ((err as { status?: number }).status === 409 && retry < 2) { await loadRuns(retry + 1); return; }
      setRunError(`Test restore history unavailable: ${String(err)}`);
    }
  };

  const load = async () => {
    if (!poolID) {
      setRepos([]);
      return;
    }
    loadAbort.current?.abort();
    const abort = new AbortController();
    loadAbort.current = abort;
    const gen = ++loadGen.current;
    const [repositories, policyList] = await Promise.allSettled([
        api<BackupRepository[]>(`pools/${encodeURIComponent(poolID)}/backup-repositories`, "GET", undefined, undefined, undefined, abort.signal),
        api<BackupPolicy[]>(`pools/${encodeURIComponent(poolID)}/backup-policies`, "GET", undefined, undefined, undefined, abort.signal),
      ]);
    if (gen !== loadGen.current || abort.signal.aborted) return;
    setRepos(repositories.status === "fulfilled" ? repositories.value : null);
    setPolicies(policyList.status === "fulfilled" ? policyList.value : null);
    setRepositoryError(repositories.status === "rejected" ? `Repository inventory unavailable: ${String(repositories.reason)}` : "");
    setPolicyError(policyList.status === "rejected" ? `Policies unavailable: ${String(policyList.reason)}` : "");
  };
  const refresh = async () => { await Promise.all([load(), loadCatalog(), loadRuns()]); };

  const loadMoreCatalog = async () => {
    if (!catalogPage?.next_cursor || loadingMore) return;
    setLoadingMore("catalog");
    const base = catalogPath();
    const cursor = catalogPage.next_cursor;
    const scope = filterKey;
    const path = `${base}${base.includes("?") ? "&" : "?"}cursor=${encodeURIComponent(cursor)}&generation=${encodeURIComponent(catalogPage.generation)}`;
    try {
      const page = await api<BackupCatalogPage>(path);
      if (scope !== currentFilterKey()) return;
      setCatalogPage(current => current?.generation === page.generation && current.next_cursor === cursor ? appendBackupPage(current, page, point => point.id) as BackupCatalogPage : current);
      setCatalogError("");
      setPageNotice("");
    } catch (err) {
      if (scope !== currentFilterKey()) return;
      if ((err as { status?: number }).status === 409) {
        await loadCatalog();
        setPageNotice("Recovery point inventory changed. Showing the newest page; load more again to continue.");
      } else setCatalogError(`Could not load more recovery points: ${String(err)}`);
    } finally {
      setLoadingMore("");
    }
  };

  useEffect(() => {
    if (pendingBackup.current["backup-now"]) return;
    setFilterVM(vmFilter);
    setBackupVM(vmFilter);
    if (flow === "backup-now") setBackupOpen(true);
    if (flow === "create-policy") { setTab("policies"); setPolicyOpen(true); }
  }, [vmFilter, flow]);

  useEffect(() => {
    setCatalogPage(null);
    setRepos(null);
    setPolicies(null);
    setPointRuns(null);
    setError("");
    setCatalogError("");
    setRepositoryError("");
    setPolicyError("");
    setRunError("");
    setPageNotice("");
    setSelectedID("");
    selectedIDRef.current = "";
    runLoading.current = "";
    setBackupUncertain(false);
    setBackupOpen(flow === "backup-now");
    setPolicyOpen(flow === "create-policy");
    setTab(flow === "create-policy" ? "policies" : "points");
    setBackupStep(1);
    requests.current = {};
    pendingBackup.current = {};
    pendingProtect.current = {};
    restoreDrafts.current = {};
    void refresh();
    const timer = window.setInterval(() => { if (document.visibilityState === "visible") void refresh(); }, 15000);
    return () => { window.clearInterval(timer); loadAbort.current?.abort(); catalogAbort.current?.abort(); runAbort.current?.abort(); };
  }, [poolID]);

  useEffect(() => {
    if (previousFilterKey.current === filterKey) return;
    previousFilterKey.current = filterKey;
    setCatalogPage(null);
    setPageNotice("");
    // Each change is a relayed Core read, so typing settles before one is sent.
    const timer = window.setTimeout(() => void loadCatalog(), 300);
    return () => window.clearTimeout(timer);
  }, [poolID, filterKey]);

  useEffect(() => {
    if (!selectedID) return;
    setPointRuns(null);
    setRunError("");
    void loadRuns();
  }, [poolID, selectedID]);

  useEffect(() => {
    if (backupUncertain) return;
    if (!repos?.length) return;
    if (!backupRepo || !repos.some(r => r.id === backupRepo)) {
      setBackupRepo(repos.length === 1 ? repos[0].id : "");
    }
  }, [repos, backupUncertain]);

  const requestID = (key: string, prefix: string) => {
    requests.current[key] ??= rid(prefix);
    return requests.current[key];
  };
  const clearRequest = (key: string) => { delete requests.current[key]; };

  const filtered = useMemo(() => [...(points || [])]
    .sort((a, b) => (b.captured_at || "").localeCompare(a.captured_at || "")), [points]);
  const pointTotal = catalogPage?.total ?? filtered.length;

  const openRestore = (point: BackupRecoveryPoint, mode: RestoreMode = "new") => {
    if (working) return;
    const draft = restoreDrafts.current[restoreKey(mode, point.id)];
    const source = replacementTarget(point, poolVMs);
    if (mode === "replace" && !source) return;
    setSelectedID(point.id);
    setRestoreMode(mode);
    setRestoreStep(draft ? 3 : 1);
    setRestoreName(draft?.name || (mode === "replace" ? source!.name : defaultRestoreName(point)));
    setRestoreStorage(draft?.storage || "");
    setRestoreHost(draft?.host || source?.host_id || pool?.master_host_id || "");
    setRestoreVMID(draft?.vmID || crypto.randomUUID());
    setReplaceConfirm(draft?.confirmation || "");
    setCleanupConfirm({});
  };

  const closeBackup = () => {
    if (working || backupUncertain) return;
    clearRequest("backup-now");
    delete pendingBackup.current["backup-now"];
    setBackupOpen(false);
    setBackupStep(1);
  };

  const backupTarget = poolVMs.find(v => v.id === backupVM);
  const backupRunning = backupTarget?.state === "running";
  const backupConsistency = backupRunning ? (backupMode === "stopped" ? "crash" : backupMode) : "stopped";
  const backupBlocker = (() => {
    if (unavailable) return "Controller inventory could not be loaded.";
    if (busy) return "Another task is already in progress.";
    if (!backupVM) return "Select a VM.";
    if (!backupRepo) return "Select a repository.";
    if (!backupTarget) return "Selected VM is not in this pool.";
    if (backupTarget.protected) return "Controller appliance: use its own panel.";
    if (backupTarget.state !== "shut off" && backupTarget.state !== "running") return `Guest must be shut off or running (observed ${backupTarget.state}).`;
    if (backupRunning && backupConsistency === "quiesced") return "Quiesced online backup is not supported yet.";
    if (backupRunning && backupTarget.spec?.boot?.firmware === "uefi") return "Online backup is not supported for UEFI guests yet.";
    if (backupTarget.freshness !== "fresh") return "Current VM state is unknown.";
    const host = data.hosts.find(h => h.id === backupTarget.host_id);
    if (!host || host.revoked || host.connection !== "fresh") return "Needs a freshly connected host.";
    return "";
  })();

  const submitBackup = async () => {
    if (backupBlocker || !backupTarget) return;
    const key = "backup-now";
    setWorking(key);
    setError("");
    try {
      const body = stableBody(pendingBackup.current, key, { vm_id: backupTarget.id, repository_id: backupRepo, consistency: backupConsistency });
      const reply = await api<{ operation?: { id?: string } }>(`vms/${encodeURIComponent(body.vm_id)}/backup`, "POST", {
        request_id: requestID(key, "bnow"),
        repository_id: body.repository_id,
        consistency: body.consistency,
      });
      clearRequest(key);
      delete pendingBackup.current[key];
      setBackupUncertain(false);
      onNotice(`${backupTarget.name}: backup requested`, backupOpID(reply));
      setBackupOpen(false);
      setBackupStep(1);
      await reload().catch(e => setError(e instanceof Error ? e.message : String(e)));
      await refresh();
    } catch (e) {
      const status = (e as { status?: number }).status;
      if (status && status >= 400 && status < 500 && status !== 409) {
        clearRequest(key);
        delete pendingBackup.current[key];
        setBackupUncertain(false);
      } else setBackupUncertain(true);
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setWorking("");
    }
  };

  const restoreStorageOptions = (() => {
    if (!selected) return [] as Storage[];
    const hostID = restoreHost || pool?.master_host_id;
    return poolStorage.filter(s => storageUsable(s, hostID) || s.id === restoreStorage);
  })();

  const restoreBlocker = (() => {
    if (!selected) return "Select a recovery point.";
    if (unavailable) return "Controller inventory could not be loaded.";
    if (busy) return "Another task is already in progress.";
    if (restoreMode === "test" && pointRuns === null) return runError || "Loading complete test restore history.";
    if (restoreMode === "replace") {
      const original = restoreOriginal;
      if (!original) return "Original VM unavailable; use Restore as new.";
      if (original.freshness !== "fresh" || data.hosts.find(h => h.id === original.host_id)?.connection !== "fresh") return "Original VM state is unknown; wait for a fresh host report.";
      if (original.state !== "shut off") return "Original VM must be shut off.";
      if (restoreHost !== original.host_id) return "Restore replace must target the original VM's host.";
      if (replaceConfirm !== original.name) return "Enter the original VM name to confirm replacement.";
    } else if (!restoreName.trim()) {
      return "Enter a VM name.";
    }
    if (!restoreHost) return "Select a host.";
    if (!restoreStorage) return "Select storage.";
    return "";
  })();

  const submitRestore = async () => {
    if (restoreBlocker || !selected || !pool) return;
    const key = restoreKey(restoreMode, selected.id);
    setWorking(key);
    setError("");
    try {
      restoreDrafts.current[key] = { name: restoreName, host: restoreHost, storage: restoreStorage, vmID: restoreVMID, confirmation: replaceConfirm };
      let path = `pools/${encodeURIComponent(pool.id)}/backup-restore`;
      let body: Record<string, unknown> = {
        request_id: requestID(key, restoreMode === "replace" ? "brep" : restoreMode === "test" ? "btes" : "bres"),
        host_id: restoreHost,
        repository_id: selected.repository_id,
        recovery_point_id: selected.id,
        storage_id: restoreStorage,
      };
      if (restoreMode === "replace") {
        path = `pools/${encodeURIComponent(pool.id)}/backup-restore-replace`;
        body = { ...body, original_vm_id: selected.source_vm_id, confirm_replace: true };
      } else if (restoreMode === "test") {
        path = `pools/${encodeURIComponent(pool.id)}/backup-test-restore`;
        body = { ...body, new_vm_id: restoreVMID, new_vm_name: restoreName.trim() };
      } else {
        body = { ...body, new_vm_id: restoreVMID, new_vm_name: restoreName.trim() };
      }
      const reply = await api<{ operation?: { id?: string } }>(path, "POST", body);
      clearRequest(key);
      delete restoreDrafts.current[key];
      const label = restoreMode === "replace" ? "restore replace" : restoreMode === "test" ? "test restore" : "restore as new";
      onNotice(`${restoreName.trim() || selected.source_vm_name || selected.id}: ${label} requested`, backupOpID(reply));
      setSelectedID("");
      await reload().catch(e => setError(e instanceof Error ? e.message : String(e)));
      await refresh();
    } catch (e) {
      const status = (e as { status?: number }).status;
      if (status && status >= 400 && status < 500 && status !== 409) {
        clearRequest(key);
        delete restoreDrafts.current[key];
      }
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setWorking("");
    }
  };

  const toggleProtect = async (point: BackupRecoveryPoint) => {
    if (!pool || busy || unavailable || working) return;
    const key = `protect-${point.id}`;
    setWorking(key);
    setError("");
    try {
      const body = stableBody(pendingProtect.current, key, {
        repository_id: point.repository_id,
        recovery_point_id: point.id,
        protected: !point.protected,
      });
      const reply = await api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(pool.id)}/backup-protect`, "POST", {
        request_id: requestID(key, "bpro"),
        ...body,
      });
      clearRequest(key);
      delete pendingProtect.current[key];
      onNotice(`${point.source_vm_name || point.id}: ${body.protected ? "protect" : "unprotect"} requested`, backupOpID(reply));
      await refresh();
    } catch (e) {
      const status = (e as { status?: number }).status;
      if (status && status >= 400 && status < 500 && status !== 409) {
        clearRequest(key);
        delete pendingProtect.current[key];
      }
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setWorking("");
    }
  };

  const testAction = async (point: BackupRecoveryPoint, vmID: string, action: "verify" | "cleanup", result?: "passed" | "failed") => {
    const test = activeTests.find(({ vm }) => vm.id === vmID);
    const cleanupReady = test?.vm.freshness === "fresh" && test.vm.state === "shut off" && data.hosts.find(h => h.id === test.vm.host_id)?.connection === "fresh";
    if (!pool || pointRuns === null || !test || busy || unavailable || working || (action === "cleanup" && (cleanupConfirm[vmID] !== vmID || !cleanupReady))) return;
    const key = `test-${action}-${point.id}-${vmID}-${result || ""}`;
    setWorking(key);
    setError("");
    try {
      const reply = await api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(pool.id)}/backup-test-${action}`, "POST", {
        request_id: requestID(key, "btest"),
        repository_id: point.repository_id,
        recovery_point_id: point.id,
        test_vm_id: vmID,
        ...(result ? { result } : {}),
      });
      clearRequest(key);
      onNotice(`${point.source_vm_name || point.id}: ${action === "verify" ? `${result} test result` : "test VM cleanup"} requested`, backupOpID(reply));
      setCleanupConfirm({});
      await reload().catch(e => setError(e instanceof Error ? e.message : String(e)));
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setWorking("");
    }
  };

  if (!pool) {
    return <><h1 className="sr-only">Backups</h1><p className="empty">Choose a pool in the scope menu to see its backups.</p></>;
  }

  return <>
    <h1 className="sr-only">Backups</h1>
    <div className="vm-fields" style={{ marginBottom: 8 }}>
      <button type="button" className={tab === "points" ? "sm primary" : "sm"} onClick={() => setTab("points")}>Recovery points</button>
      <button type="button" className={tab === "policies" ? "sm primary" : "sm"} onClick={() => setTab("policies")}>Policies</button>
    </div>
    <BackupPolicies key={pool.id} active={tab === "policies"}
      api={api} data={data} pool={pool} repos={repos || []} policies={policies} error={error || policyError}
      busy={busy} unavailable={unavailable} working={working} setWorking={setWorking}
      onNotice={onNotice} onError={setError} reload={async () => { await reload(); await refresh(); }}
      requestID={requestID} clearRequest={clearRequest}
      openCreate={policyOpen} onOpenCreate={() => setPolicyOpen(true)} onCloseCreate={() => setPolicyOpen(false)}
    />
    {tab === "points" ? <Section icon="backup" title="Recovery points" count={pointTotal} actions={<>
      <button type="button" className="sm primary" disabled={unavailable || busy || Boolean(working)} onClick={() => { setBackupOpen(true); setBackupStep(1); }}>
        <Icon kind="backup" />Back up now
      </button>
      <button type="button" className="sm" disabled={unavailable || busy || Boolean(working)} onClick={() => { setTab("policies"); setPolicyOpen(true); }}>Create policy</button>
      <button type="button" className="sm" disabled={unavailable || busy || Boolean(working) || !selected || pointRuns === null} onClick={() => selected && openRestore(selected, "test")}>Test restore</button>
      <button type="button" className="sm" onClick={() => { location.hash = "/settings/backups"; }}>Repositories</button>
    </>}>
      {(error || catalogError || repositoryError) && <p className="why" role="alert">{error || catalogError || repositoryError}</p>}
      {runError && <p className="why" role="alert">{runError}</p>}
      {selected && pointRuns === null && !runError && <p className="muted" role="status">Loading complete test restore history…</p>}
      {pageNotice && <p className="why" role="status">{pageNotice}</p>}
      <div className="vm-fields" style={{ marginBottom: 8 }}>
        <label>Search<input type="search" value={query} onChange={e => setQuery(e.target.value)} placeholder="VM, repository or ID" /></label>
        <label>VM<select value={filterVM} onChange={e => setFilterVM(e.target.value)}>
          <option value="">All VMs</option>
          {poolVMs.map(v => <option key={v.id} value={v.id}>{v.name}</option>)}
          {(points || []).filter((p, i, all) => !poolVMs.some(v => v.id === p.source_vm_id) && all.findIndex(x => x.source_vm_id === p.source_vm_id) === i).map(p =>
            <option key={p.source_vm_id} value={p.source_vm_id}>{p.source_vm_name || p.source_vm_id} (unavailable)</option>)}
        </select></label>
        <label>Repository<select value={filterRepo} onChange={e => setFilterRepo(e.target.value)}>
          <option value="">All repositories</option>
          {(repos || []).map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select></label>
      </div>
      {points === null && !catalogError ? <p className="muted">Loading…</p> : null}
      {points && filtered.length === 0 ? <p className="empty">{filterVM || filterRepo || query.trim() ? "No recovery points match." : repos?.length ? "No recovery points yet. Back up a VM to create one." : "No backup repositories. Create a destination in Settings first."}</p> : null}
      {filtered.length > 0 ? <div className="table-scroll"><table className="records">
        <thead><tr><th>VM</th><th>Captured</th><th>Repository</th><th>Consistency</th><th>Protect</th><th>Test</th><th><span className="sr-only">Actions</span></th></tr></thead>
        <tbody>
          {filtered.map(p => {
            const repo = repoByID.get(p.repository_id);
            const live = replacementTarget(p, poolVMs);
            return <tr key={p.id} className={p.id === selectedID ? "sel" : undefined} onClick={() => openRestore(p, "new")}>
              <td>
                <strong>{live ? <a href={`#/vms/${encodeURIComponent(live.id)}`} onClick={e => e.stopPropagation()}>{live.name}</a> : (p.source_vm_name || p.source_vm_id)}</strong>
                {!live ? <small className="muted">Source VM unavailable</small> : null}
              </td>
              <td><span className="muted">{date(p.captured_at)}</span></td>
              <td>{repo?.name || <span className="mono">{p.repository_id}</span>}</td>
              <td><Badge value={p.consistency} /></td>
              <td>{p.protected ? <Badge value="protected" /> : <span className="muted">—</span>}</td>
              <td>{p.last_test_result ? <Badge value={p.last_test_result} /> : <span className="muted">—</span>}</td>
              <td className="row-actions">
                <button type="button" className="sm" disabled={Boolean(working)} onClick={e => { e.stopPropagation(); openRestore(p, "new"); }}>Restore as new</button>
                {live && <button type="button" className="sm" disabled={Boolean(working)} onClick={e => { e.stopPropagation(); openRestore(p, "replace"); }}>Replace</button>}
                <button type="button" className="sm" disabled={Boolean(working) || busy || unavailable} onClick={e => { e.stopPropagation(); void toggleProtect(p); }}>
                  {working === `protect-${p.id}` ? "…" : pendingProtect.current[`protect-${p.id}`] ? "Retry request" : p.protected ? "Unprotect" : "Protect"}
                </button>
              </td>
            </tr>;
          })}
        </tbody>
      </table></div> : null}
      {catalogPage && <p className="muted" role="status">Showing {filtered.length} of {pointTotal} recovery points.</p>}
      {catalogPage?.next_cursor && <button type="button" className="sm" disabled={Boolean(loadingMore)} onClick={() => void loadMoreCatalog()}>{loadingMore === "catalog" ? "Loading…" : "Load more recovery points"}</button>}
    </Section> : null}

    {backupOpen && <section className="panel form-panel" aria-label="Back up now">
      <div className="section-heading">
        <h2>Back up now · step {backupStep} of 3</h2>
        <button type="button" disabled={Boolean(working)} onClick={closeBackup}>Close</button>
      </div>
      {backupUncertain && <p className="why" role="status">The previous reply was uncertain. Check <a href="#/tasks">Tasks</a>, or retry the same backup request.</p>}
      {backupStep === 1 && <form className="vm-fields" onSubmit={e => { e.preventDefault(); if (backupVM && backupRepo) setBackupStep(2); }}>
        <label>Virtual machine<select required value={backupVM} disabled={Boolean(working) || backupUncertain} onChange={e => { setBackupVM(e.target.value); setBackupMode("stopped"); clearRequest("backup-now"); }}>
          <option value="">Select VM</option>
          {poolVMs.filter(v => !v.protected).map(v => <option key={v.id} value={v.id}>{v.name}{v.state !== "shut off" ? ` · ${v.state}` : ""}</option>)}
        </select></label>
        <label>Repository<select required value={backupRepo} disabled={Boolean(working) || backupUncertain} onChange={e => { setBackupRepo(e.target.value); clearRequest("backup-now"); }}>
          <option value="">Select repository</option>
          {(repos || []).map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select></label>
        {backupRunning && <label>Mode<select value={backupMode === "stopped" ? "crash" : backupMode} onChange={e => { setBackupMode(e.target.value as "crash" | "quiesced"); clearRequest("backup-now"); }} disabled={Boolean(working) || backupUncertain}>
          <option value="crash">Crash-consistent</option>
          <option value="quiesced">Quiesced</option>
        </select></label>}
        <div className="actions">
          <button type="submit" className="primary" disabled={!backupVM || !backupRepo || Boolean(working)}>Next</button>
          <button type="button" disabled={Boolean(working)} onClick={closeBackup}>Cancel</button>
        </div>
      </form>}
      {backupStep === 2 && <div className="vm-fields">
        <dl className="details">
          <dt>VM</dt><dd>{backupTarget?.name || backupVM}</dd>
          <dt>Repository</dt><dd>{repoByID.get(backupRepo)?.name || backupRepo}</dd>
          <dt>Mode</dt><dd>{backupConsistency === "stopped" ? "Stopped full backup" : backupConsistency === "crash" ? "Online crash-consistent" : "Online quiesced"}</dd>
          <dt>Disks</dt><dd>{backupTarget?.spec?.disks?.length ? backupTarget.spec.disks.map(d => d.name).join(", ") : "From Core definition"}</dd>
        </dl>
        {backupBlocker && <p className="why" role="status">{backupBlocker}</p>}
        <div className="actions">
          <button type="button" className="primary" disabled={Boolean(backupBlocker) || Boolean(working)} onClick={() => setBackupStep(3)}>Next</button>
          <button type="button" disabled={Boolean(working)} onClick={() => setBackupStep(1)}>Back</button>
        </div>
      </div>}
      {backupStep === 3 && <div className="vm-fields">
        <p>Submit a {backupConsistency === "stopped" ? "stopped" : "online"} full backup of <strong>{backupTarget?.name}</strong> to <strong>{repoByID.get(backupRepo)?.name || backupRepo}</strong>.</p>
        {backupBlocker && <p className="why" role="status">{backupBlocker}</p>}
        <div className="actions">
          <button type="button" className="primary" disabled={Boolean(backupBlocker) || Boolean(working)} onClick={() => void submitBackup()}>
            {working === "backup-now" ? "Working…" : "Back up now"}
          </button>
          <button type="button" disabled={Boolean(working) || backupUncertain} onClick={() => setBackupStep(2)}>Back</button>
        </div>
      </div>}
    </section>}

    {selected && <Panel icon="backup" title={selected.source_vm_name || selected.source_vm_id} badge={<Badge value={selected.consistency} />}
      onClose={() => { if (!working) setSelectedID(""); }}
      sub={<>Captured {date(selected.captured_at)} · {repoByID.get(selected.repository_id)?.name || selected.repository_id}</>}
      actions={<>
        <button type="button" className="sm" disabled={Boolean(working)} onClick={() => openRestore(selected, "new")}>As new</button>
        {restoreOriginal && <button type="button" className="sm" disabled={Boolean(working)} onClick={() => openRestore(selected, "replace")}>Replace</button>}
        <button type="button" className="sm" disabled={Boolean(working)} onClick={() => openRestore(selected, "test")}>Test</button>
        <button type="button" className="sm" disabled={Boolean(working) || busy || unavailable} onClick={() => void toggleProtect(selected)}>{pendingProtect.current[`protect-${selected.id}`] ? "Retry request" : selected.protected ? "Unprotect" : "Protect"}</button>
      </>}>
      {error && <p className="why" role="alert">{error}</p>}
      {runError && <p className="why" role="alert">{runError}</p>}
      {pageNotice && <p className="why" role="status">{pageNotice}</p>}
      {retentionWarnings(runs).map(run => <p key={run.operation_id} className="why" role="status">Backup succeeded, but retention could not remove some older recovery points ({date(run.updated_at)}): {run.error}</p>)}
      {!restoreOriginal && <p className="why" role="status">Replace unavailable: the original VM is not in this pool. Use Restore as new.</p>}
      <section className="tile" aria-label="Recovery point">
        <dl className="details">
          <dt>Recovery point</dt><dd className="mono">{selected.id}</dd>
          <dt>Source VM</dt><dd className="mono">{selected.source_vm_id}</dd>
          <dt>Repository</dt><dd>{repoByID.get(selected.repository_id)?.name || selected.repository_id}</dd>
          <dt>Captured</dt><dd>{date(selected.captured_at)}</dd>
          <dt>Consistency</dt><dd><Badge value={selected.consistency} /></dd>
          <dt>Protected</dt><dd>{selected.protected ? "Yes" : "No"}</dd>
          <dt>Last test</dt><dd>{selected.last_test_result || "—"}</dd>
        </dl>
      </section>
      <section className="tile" aria-label="Restore">
        <h2>{restoreMode === "replace" ? "Restore replace" : restoreMode === "test" ? "Test restore" : "Restore as new"} · step {restoreStep} of 3</h2>
        {restoreStep === 1 && <form className="vm-fields" onSubmit={(e: FormEvent) => { e.preventDefault(); if (!restoreBlocker) setRestoreStep(2); }}>
          {restoreMode !== "replace" && <label>New VM name<input required maxLength={100} value={restoreName} disabled={Boolean(working) || uncertainRestore} onChange={e => setRestoreName(e.target.value)} /></label>}
          {restoreMode === "replace" && restoreOriginal && <p className="note">Replaces <strong>{restoreOriginal.name}</strong> ({selected.source_vm_id}). Original disks retained until exclusive cutover. Guest must be shut off.</p>}
          {restoreMode === "replace" && restoreOriginal && <label>Type original VM name to confirm<input required value={replaceConfirm} disabled={Boolean(working) || uncertainRestore} onChange={e => setReplaceConfirm(e.target.value)} /></label>}
          <label>Host<select required value={restoreHost} disabled={Boolean(working) || uncertainRestore} onChange={e => { setRestoreHost(e.target.value); setRestoreStorage(""); }}>
            <option value="">Select host</option>
            {data.hosts.filter(h => h.pool_id === pool.id && !h.revoked && (restoreMode !== "replace" || h.id === poolVMs.find(v => v.id === selected.source_vm_id)?.host_id)).map(h => <option key={h.id} value={h.id}>{h.hostname}{h.connection !== "fresh" ? " · stale" : ""}</option>)}
          </select></label>
          <label>Storage<select required value={restoreStorage} disabled={Boolean(working) || uncertainRestore} onChange={e => setRestoreStorage(e.target.value)}>
            <option value="">Select storage</option>
            {restoreStorageOptions.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}
          </select></label>
          {restoreMode !== "replace" && <p className="note">New UUID and MACs. Guest starts stopped with NICs disconnected.</p>}
          {restoreMode === "replace" && restoreBlocker && <p className="why" role="status">{restoreBlocker}</p>}
          <div className="actions">
            <button type="submit" className="primary" disabled={Boolean(restoreBlocker) || Boolean(working)}>Next</button>
          </div>
        </form>}
        {restoreStep === 2 && <div className="vm-fields">
          <dl className="details">
            {restoreMode === "replace" ? <><dt>Original VM</dt><dd>{restoreOriginal?.name || selected.source_vm_id} · {selected.source_vm_id}</dd></> : <><dt>New name</dt><dd>{restoreName}</dd><dt>New VM ID</dt><dd className="mono">{restoreVMID}</dd></>}
            <dt>Host</dt><dd>{data.hosts.find(h => h.id === restoreHost)?.hostname || restoreHost}</dd>
            <dt>Storage</dt><dd>{poolStorage.find(s => s.id === restoreStorage)?.name || restoreStorage}</dd>
            <dt>Power / network</dt><dd>Stopped · disconnected</dd>
          </dl>
          <div className="actions">
            <button type="button" className="primary" disabled={Boolean(working)} onClick={() => setRestoreStep(3)}>Next</button>
            <button type="button" disabled={Boolean(working)} onClick={() => setRestoreStep(1)}>Back</button>
          </div>
        </div>}
        {restoreStep === 3 && <div className="vm-fields">
          {uncertainRestore && <p className="why" role="status">The previous reply was uncertain. Check <a href="#/tasks">Tasks</a>, or retry the same request with these selections.</p>}
          {restoreBlocker && <p className="why" role="status">{restoreBlocker}</p>}
          <div className="actions">
            <button type="button" className="primary" disabled={Boolean(restoreBlocker) || Boolean(working)} onClick={() => void submitRestore()}>
              {working.includes(selected.id) ? "Working…" : restoreMode === "replace" ? "Replace" : restoreMode === "test" ? "Test restore" : "Restore as new"}
            </button>
            <button type="button" disabled={Boolean(working)} onClick={() => setRestoreStep(2)}>Back</button>
          </div>
        </div>}
      </section>
      {activeTests.length > 0 && <section className="tile" aria-label="Test restore verification">
        <h2>Verify test restore</h2>
        <p className="note">Inspect each isolated test VM, confirm boot and application data, then record its result for this recovery point.</p>
        {activeTests.map(({ run, vm }) => {
          const cleanupReady = vm.freshness === "fresh" && vm.state === "shut off" && data.hosts.find(h => h.id === vm.host_id)?.connection === "fresh";
          return <section key={run.operation_id} className="substrip" aria-label={`Test VM ${vm.name} (${vm.id})`}>
            <h3>{vm.name}</h3>
            <p><a href={`#/vms/${encodeURIComponent(vm.id)}`} aria-label={`Open test VM ${vm.name} (${vm.id})`}>Open test VM</a> · <span className="mono">{vm.id}</span></p>
            <div className="actions">
              <button type="button" className="sm" aria-label={`Record passed for ${vm.name} (${vm.id})`} disabled={Boolean(working) || busy || unavailable} onClick={() => void testAction(selected, vm.id, "verify", "passed")}>Record passed</button>
              <button type="button" className="sm" aria-label={`Record failed for ${vm.name} (${vm.id})`} disabled={Boolean(working) || busy || unavailable} onClick={() => void testAction(selected, vm.id, "verify", "failed")}>Record failed</button>
            </div>
            <label>Type test VM ID to confirm cleanup<input value={cleanupConfirm[vm.id] || ""} disabled={Boolean(working)} onChange={e => setCleanupConfirm(cur => ({ ...cur, [vm.id]: e.target.value }))} /></label>
            {!cleanupReady && <p className="why" role="status">The test VM must be freshly observed shut off before cleanup.</p>}
            <button type="button" className="sm danger" aria-label={`Clean up ${vm.name} (${vm.id}) and disks`} disabled={Boolean(working) || busy || unavailable || cleanupConfirm[vm.id] !== vm.id || !cleanupReady} onClick={() => void testAction(selected, vm.id, "cleanup")}>Clean up test VM and disks</button>
          </section>;
        })}
      </section>}
    </Panel>}
  </>;
}
