// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import type { API, Data, Pool } from "./types";
import type { Storage } from "./managed";
import { Badge, Panel, Section, date } from "./ui";
import { Icon } from "./icons";
import {
  appendBackupPage, backupOpID, collectBackupPageItems, pageURL, refreshLoadedPages, refreshRunHistory,
  type BackupCatalogPage, type BackupRecoveryPoint, type BackupRepository, type BackupRun, type BackupRunPage,
} from "./backup-types";
import { heldFor, sendHeld, type RestoreDraft } from "./backup-held";
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

type BackupNowStep = 1 | 2;
type RestoreStep = 1 | 2;
type RestoreMode = "new" | "replace" | "test";
const restoreKey = (mode: RestoreMode, pointID: string) => `${mode}-${pointID}`;
// One back-up-now request is held per VM; the key carries the VM because the request body does not.
const backupNowPrefix = "backup-now-";
const backupNowKey = (vmID: string) => `${backupNowPrefix}${vmID}`;
type BackupNowBody = { request_id: string; repository_id: string; consistency: "stopped" | "crash" };
const message = (e: unknown) => e instanceof Error ? e.message : String(e);

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

// useLatestLoad lets only the newest load of a kind report: begin() aborts the previous one.
function useLatestLoad() {
  const [load] = useState(() => {
    let current = 0, controller: AbortController | undefined;
    return {
      begin() {
        controller?.abort();
        const own = controller = new AbortController(), id = ++current;
        return { signal: own.signal, stale: () => id !== current || own.signal.aborted };
      },
      cancel() { controller?.abort(); },
    };
  });
  return load;
}

export function Backups({ api, data, pool, busy, unavailable, reload, onNotice, vmFilter = "", flow = "" }: Props) {
  // Requests whose outcome is unknown outlive this page, so a retry after visiting Tasks is still exact.
  const held = heldFor(pool?.id || "");
  const heldBackup = Object.keys(held.bodies).find(key => key.startsWith(backupNowPrefix));
  const heldBackupBody = heldBackup ? held.bodies[heldBackup] as BackupNowBody : undefined;
  const heldCreate = Boolean(held.bodies["policy-create"]);
  const heldJob = heldCreate || Object.keys(held.bodies).some(key => key.startsWith("repository-"));
  const [catalogPage, setCatalogPage] = useState<BackupCatalogPage | null>(null);
  const [repos, setRepos] = useState<BackupRepository[] | null>(null);
  const [policies, setPolicies] = useState<BackupPolicy[] | null>(null);
  const [jobRuns, setJobRuns] = useState<BackupRun[] | null>(null);
  const [pointRuns, setPointRuns] = useState<BackupRun[] | null>(null);
  const [error, setError] = useState("");
  const [catalogError, setCatalogError] = useState("");
  const [repositoryError, setRepositoryError] = useState("");
  const [policyError, setPolicyError] = useState("");
  const [runError, setRunError] = useState("");
  const [jobRunError, setJobRunError] = useState("");
  const [pageNotice, setPageNotice] = useState("");
  const [loadingMore, setLoadingMore] = useState("");
  const [query, setQuery] = useState("");
  const [filterVM, setFilterVM] = useState(vmFilter);
  const [filterRepo, setFilterRepo] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [selectedPoint, setSelectedPoint] = useState<BackupRecoveryPoint | null>(null);
  const [view, setView] = useState<"jobs" | "points">(heldJob ? "jobs" : heldBackup || Object.keys(held.drafts).length ? "points" : flow === "create-policy" ? "jobs" : vmFilter || flow ? "points" : "jobs");
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [backupOpen, setBackupOpen] = useState(flow === "backup-now" || Boolean(heldBackup));
  const [policyOpen, setPolicyOpen] = useState(flow === "create-policy" || heldCreate);
  const [repositoryPanelOpen, setRepositoryPanelOpen] = useState(false);
  const [backupStep, setBackupStep] = useState<BackupNowStep>(heldBackup ? 2 : 1);
  const [backupVM, setBackupVM] = useState(heldBackup ? heldBackup.slice(backupNowPrefix.length) : vmFilter);
  const [backupRepo, setBackupRepo] = useState(heldBackupBody?.repository_id ?? "");
  const [backupUncertain, setBackupUncertain] = useState(Boolean(heldBackup));
  const [restoreMode, setRestoreMode] = useState<RestoreMode>("new");
  const [restoreStep, setRestoreStep] = useState<RestoreStep>(1);
  const [restoreName, setRestoreName] = useState("");
  const [restoreStorage, setRestoreStorage] = useState("");
  const [restoreHost, setRestoreHost] = useState("");
  const [restoreVMID, setRestoreVMID] = useState<string>(() => crypto.randomUUID());
  const [replaceConfirm, setReplaceConfirm] = useState("");
  const [cleanupConfirm, setCleanupConfirm] = useState<Record<string, string>>({});
  const [working, setWorking] = useState("");
  const jobRunsRef = useRef<BackupRun[] | null>(null);
  jobRunsRef.current = jobRuns;
  const catalogLoaded = useRef(0);
  catalogLoaded.current = catalogPage?.items.length ?? 0;
  const presentationDrafts = useRef<Record<string, RestoreDraft>>({});
  const inventoryLoad = useLatestLoad();
  const jobRunLoad = useLatestLoad();
  const jobRunLoading = useRef(false);
  const catalogLoad = useLatestLoad();
  const runLoad = useLatestLoad();
  const catalogScope = useRef("");
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
  const reportedPoint = points?.find(p => p.id === selectedID);
  const selected = reportedPoint || (selectedPoint?.id === selectedID ? selectedPoint : null);
  const restoreOriginal = replacementTarget(selected, poolVMs);
  const uncertainRestore = Boolean(selected && held.drafts[restoreKey(restoreMode, selected.id)]);
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
    const { signal, stale } = catalogLoad.begin();
    const scope = currentFilterKey();
    const base = catalogPath();
    try {
      const first = await api<BackupCatalogPage>(base, "GET", undefined, undefined, undefined, signal);
      if (stale()) return;
      // Re-read every page the operator has loaded: Core's page generation does not change when a point's
      // protection or test result does, so keeping loaded rows would show stale retention state.
      const loaded = catalogScope.current === scope ? catalogLoaded.current : 0;
      const page = await refreshLoadedPages<BackupRecoveryPoint, BackupCatalogPage>(first, loaded, (cursor, generation) =>
        api<BackupCatalogPage>(pageURL(base, cursor, generation), "GET", undefined, undefined, undefined, signal));
      if (stale()) return;
      catalogScope.current = scope;
      setCatalogPage(page);
      setCatalogError("");
    } catch (err) {
      if (stale()) return;
      setCatalogError(`Recovery points unavailable: ${String(err)}`);
    }
  };

  // loadRuns collects the selected point's complete run history. A poll keeps the
  // previous history on screen and never restarts a collection still in progress.
  const loadRuns = async (retry = 0) => {
    const pointID = selectedIDRef.current;
    if (!poolID || !pointID || (retry === 0 && runLoading.current === pointID)) return;
    const { signal, stale } = runLoad.begin();
    runLoading.current = pointID;
    const base = `pools/${encodeURIComponent(poolID)}/backup-runs?recovery_point_id=${encodeURIComponent(pointID)}`;
    try {
      const response = await api<BackupRunPage>(base, "GET", undefined, undefined, undefined, signal);
      if (stale()) return;
      const all = await collectBackupPageItems(response, (cursor, generation) => api<BackupRunPage>(pageURL(base, cursor, generation), "GET", undefined, undefined, undefined, signal));
      if (stale()) return;
      runLoading.current = "";
      setPointRuns(all);
      setRunError("");
    } catch (err) {
      if (stale()) return;
      runLoading.current = "";
      if ((err as { status?: number }).status === 409 && retry < 2) { await loadRuns(retry + 1); return; }
      setRunError(`Test restore history unavailable: ${String(err)}`);
    }
  };

  // Complete job history has its own lifetime: polling must not restart a slow scan.
  const loadJobRuns = async () => {
    if (!poolID || jobRunLoading.current) return;
    const { signal, stale } = jobRunLoad.begin();
    jobRunLoading.current = true;
    const base = `pools/${encodeURIComponent(poolID)}/backup-runs`;
    try {
      for (let retry = 0; ; retry++) {
        try {
          const first = await api<BackupRunPage>(base, "GET", undefined, undefined, undefined, signal);
          if (stale()) return;
          const all = await refreshRunHistory(jobRunsRef.current, first, (cursor, generation) => api<BackupRunPage>(pageURL(base, cursor, generation), "GET", undefined, undefined, undefined, signal));
          if (stale()) return;
          setJobRuns(all);
          setJobRunError("");
          return;
        } catch (err) {
          if (stale()) return;
          if (err && typeof err === "object" && "status" in err && err.status === 409 && retry < 2) continue;
          throw err;
        }
      }
    } catch (err) {
      if (stale()) return;
      setJobRuns(null);
      setJobRunError(`Backup run history unavailable: ${String(err)}`);
    } finally {
      if (!stale()) jobRunLoading.current = false;
    }
  };

  const load = async () => {
    if (!poolID) {
      setRepos([]);
      setPolicies([]);
      return;
    }
    const { signal, stale } = inventoryLoad.begin();
    await Promise.all([
      api<BackupRepository[]>(`pools/${encodeURIComponent(poolID)}/backup-repositories`, "GET", undefined, undefined, undefined, signal).then(value => {
        if (stale()) return;
        setRepos(value);
        setRepositoryError("");
      }).catch(err => {
        if (stale()) return;
        setRepos(null);
        setRepositoryError(`Repository inventory unavailable: ${String(err)}`);
      }),
      api<BackupPolicy[]>(`pools/${encodeURIComponent(poolID)}/backup-policies`, "GET", undefined, undefined, undefined, signal).then(value => {
        if (stale()) return;
        setPolicies(value);
        setPolicyError("");
      }).catch(err => {
        if (stale()) return;
        setPolicies(null);
        setPolicyError(`Backup jobs unavailable: ${String(err)}`);
      }),
    ]);
  };
  const refresh = async () => { await Promise.all([load(), loadJobRuns(), loadCatalog(), loadRuns()]); };

  const loadMoreCatalog = async () => {
    if (!catalogPage?.next_cursor || loadingMore) return;
    setLoadingMore("catalog");
    const base = catalogPath();
    const cursor = catalogPage.next_cursor;
    const scope = filterKey;
    const path = pageURL(base, cursor, catalogPage.generation);
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
    if (reportedPoint) setSelectedPoint(reportedPoint);
  }, [reportedPoint]);

  useEffect(() => {
    if (Object.keys(held.bodies).some(key => key.startsWith(backupNowPrefix)) || Object.keys(held.drafts).length || working || repositoryPanelOpen || policyOpen) return;
    saveRestoreDraft();
    setFilterVM(vmFilter);
    setBackupVM(vmFilter);
    setSelectedID("");
    setRestoreOpen(false);
    setView(flow === "create-policy" ? "jobs" : vmFilter || flow ? "points" : "jobs");
    setBackupOpen(flow === "backup-now");
    setPolicyOpen(flow === "create-policy");
  }, [vmFilter, flow]);

  // The page is keyed by pool, so every ref and state starts fresh; this only starts the loads.
  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => { if (document.visibilityState === "visible") void refresh(); }, 15000);
    return () => { window.clearInterval(timer); inventoryLoad.cancel(); jobRunLoad.cancel(); catalogLoad.cancel(); runLoad.cancel(); };
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
    runLoad.cancel();
    runLoading.current = "";
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


  const filtered = useMemo(() => [...(points || [])]
    .sort((a, b) => (b.captured_at || "").localeCompare(a.captured_at || "")), [points]);
  const pointTotal = catalogPage?.total ?? filtered.length;

  const panelLocked = Boolean(working) || backupUncertain || uncertainRestore || repositoryPanelOpen;
  const saveRestoreDraft = () => {
    if (selected && restoreOpen && !uncertainRestore) presentationDrafts.current[restoreKey(restoreMode, selected.id)] = {
      name: restoreName, host: restoreHost, storage: restoreStorage, vmID: restoreVMID, confirmation: replaceConfirm,
    };
  };
  const inspectPoint = (point: BackupRecoveryPoint) => {
    if (panelLocked) return;
    saveRestoreDraft();
    setBackupOpen(false);
    setPolicyOpen(false);
    setSelectedPoint(point);
    setSelectedID(point.id);
    setRestoreOpen(false);
    setCleanupConfirm({});
  };
  const switchView = (next: "jobs" | "points") => {
    if (panelLocked || policyOpen) return;
    saveRestoreDraft();
    setRestoreOpen(false);
    setBackupOpen(false);
    setView(next);
  };
  const viewPoints = (vmID: string) => {
    if (panelLocked || policyOpen) return;
    switchView("points");
    setFilterVM(vmID);
  };

  const openRestore = (point: BackupRecoveryPoint, mode: RestoreMode = "new") => {
    if (panelLocked) return;
    saveRestoreDraft();
    const key = restoreKey(mode, point.id);
    const pending = held.drafts[key];
    const draft = pending || presentationDrafts.current[key];
    const source = replacementTarget(point, poolVMs);
    if (mode === "replace" && !source) return;
    setBackupOpen(false);
    setPolicyOpen(false);
    setSelectedPoint(point);
    setSelectedID(point.id);
    setRestoreOpen(true);
    setRestoreMode(mode);
    setRestoreStep(pending ? 2 : 1);
    setRestoreName(draft?.name ?? (mode === "replace" ? source!.name : defaultRestoreName(point)));
    setRestoreStorage(draft?.storage ?? "");
    setRestoreHost(draft?.host ?? source?.host_id ?? pool?.master_host_id ?? "");
    setRestoreVMID(draft?.vmID ?? crypto.randomUUID());
    setReplaceConfirm(draft?.confirmation ?? "");
    setCleanupConfirm({});
  };

  // Reopen a restore whose outcome is unknown after the page was left, so its exact request can be retried.
  useEffect(() => {
    const key = Object.keys(held.drafts)[0];
    const point = key && held.points[key];
    if (point) openRestore(point, key.split("-")[0] as RestoreMode);
  }, []);

  const closeBackup = () => {
    if (working || backupUncertain) return;
    setBackupOpen(false);
    setBackupStep(1);
  };

  const backupTarget = poolVMs.find(v => v.id === backupVM);
  const backupRunning = backupTarget?.state === "running";
  const backupConsistency = backupRunning ? "crash" : "stopped";
  const backupBlocker = (() => {
    if (unavailable) return "Controller inventory could not be loaded.";
    if (busy) return "Another task is already in progress.";
    if (!backupVM) return "Select a VM.";
    if (!backupRepo) return "Select a repository.";
    if (!backupTarget) return "Selected VM is not in this pool.";
    if (backupTarget.protected) return "Controller appliance: use its own panel.";
    if (backupTarget.state !== "shut off" && backupTarget.state !== "running") return `Guest must be shut off or running (observed ${backupTarget.state}).`;
    if (backupRunning && backupTarget.spec?.boot?.firmware === "uefi") return "Online backup is not supported for UEFI guests yet.";
    if (backupTarget.freshness !== "fresh") return "Current VM state is unknown.";
    const host = data.hosts.find(h => h.id === backupTarget.host_id);
    if (!host || host.revoked || host.connection !== "fresh") return "Needs a freshly connected host.";
    return "";
  })();

  const submitBackup = async () => {
    if (backupBlocker || !backupTarget) return;
    const key = backupNowKey(backupTarget.id);
    setWorking(key);
    setError("");
    try {
      const outcome = await sendHeld(held, key, "bnow", (request_id): BackupNowBody => ({ request_id, repository_id: backupRepo, consistency: backupConsistency }),
        body => api<{ operation?: { id?: string } }>(`vms/${encodeURIComponent(backupTarget.id)}/backup`, "POST", body));
      if ("error" in outcome) {
        setBackupUncertain(!outcome.definitive);
        setError(message(outcome.error));
        return;
      }
      setBackupUncertain(false);
      onNotice(`${backupTarget.name}: backup requested`, backupOpID(outcome.reply));
      setBackupOpen(false);
      setBackupStep(1);
      await reload().catch(e => setError(message(e)));
      await refresh();
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
      held.drafts[key] = { name: restoreName, host: restoreHost, storage: restoreStorage, vmID: restoreVMID, confirmation: replaceConfirm };
      held.points[key] = selected;
      const path = `pools/${encodeURIComponent(pool.id)}/${restoreMode === "replace" ? "backup-restore-replace" : restoreMode === "test" ? "backup-test-restore" : "backup-restore"}`;
      const outcome = await sendHeld(held, key, restoreMode === "replace" ? "brep" : restoreMode === "test" ? "btes" : "bres", request_id => ({
        request_id,
        host_id: restoreHost,
        repository_id: selected.repository_id,
        recovery_point_id: selected.id,
        storage_id: restoreStorage,
        ...(restoreMode === "replace" ? { original_vm_id: selected.source_vm_id, confirm_replace: true } : { new_vm_id: restoreVMID, new_vm_name: restoreName.trim() }),
      }), body => api<{ operation?: { id?: string } }>(path, "POST", body));
      if ("error" in outcome) {
        if (outcome.definitive) { delete held.drafts[key]; delete held.points[key]; }
        setError(message(outcome.error));
        return;
      }
      delete held.drafts[key];
      delete held.points[key];
      delete presentationDrafts.current[key];
      setRestoreOpen(false);
      const label = restoreMode === "replace" ? "restore replace" : restoreMode === "test" ? "test restore" : "restore as new";
      onNotice(`${restoreName.trim() || selected.source_vm_name || selected.id}: ${label} requested`, backupOpID(outcome.reply));
      setSelectedID("");
      await reload().catch(e => setError(message(e)));
      await refresh();
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
      const outcome = await sendHeld(held, key, "bpro", request_id => ({
        request_id, repository_id: point.repository_id, recovery_point_id: point.id, protected: !point.protected,
      }), body => api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(pool.id)}/backup-protect`, "POST", body));
      if ("error" in outcome) { setError(message(outcome.error)); return; }
      onNotice(`${point.source_vm_name || point.id}: ${outcome.body.protected ? "protect" : "unprotect"} requested`, backupOpID(outcome.reply));
      await refresh();
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
      const outcome = await sendHeld(held, key, "btest", request_id => ({
        request_id, repository_id: point.repository_id, recovery_point_id: point.id, test_vm_id: vmID, ...(result ? { result } : {}),
      }), body => api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(pool.id)}/backup-test-${action}`, "POST", body));
      if ("error" in outcome) { setError(message(outcome.error)); return; }
      onNotice(`${point.source_vm_name || point.id}: ${action === "verify" ? `${result} test result` : "test VM cleanup"} requested`, backupOpID(outcome.reply));
      setCleanupConfirm({});
      await reload().catch(e => setError(message(e)));
      await refresh();
    } finally {
      setWorking("");
    }
  };

  const pointPanel = Boolean(selected && view === "points" && !backupOpen && !policyOpen);


  if (!pool) {
    return <><h1 className="sr-only">Backups</h1><p className="empty">Choose a pool in the scope menu to see its backups.</p></>;
  }

  return <div className="backup-workspace">
    <h1 className="sr-only">Backups</h1>
    <div className="backup-tabs" role="tablist" aria-label="Backup views" onKeyDown={e => {
      if (panelLocked || policyOpen || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
      e.preventDefault();
      const next = e.key === "Home" ? "jobs" : e.key === "End" ? "points" : view === "jobs" ? "points" : "jobs";
      switchView(next);
      document.getElementById(`backup-${next}-tab`)?.focus();
    }}>
      <button type="button" role="tab" id="backup-jobs-tab" tabIndex={view === "jobs" ? 0 : -1} aria-controls="backup-jobs-view" aria-selected={view === "jobs"} disabled={panelLocked || policyOpen} onClick={() => switchView("jobs")}>Backup jobs</button>
      <button type="button" role="tab" id="backup-points-tab" tabIndex={view === "points" ? 0 : -1} aria-controls="recovery-points" aria-selected={view === "points"} disabled={panelLocked || policyOpen} onClick={() => switchView("points")}>Recovery points</button>
    </div>
    {/* Each message renders once, on the surface the operator is looking at: a panel shows its own. */}
    {!(backupOpen || policyOpen || pointPanel || repositoryPanelOpen) && (view === "jobs" ? [jobRunError] : [error, catalogError, repositoryError, runError]).filter(Boolean).map(text => <p key={text} className="why" role="alert">{text}</p>)}
    {backupOpen && <Panel wide icon="backup" title="Back up now" onClose={closeBackup} closeDisabled={Boolean(working) || backupUncertain}
      footer={<>
        {backupBlocker && <p className="why" role="status">{backupBlocker}</p>}
        <div className="actions">
          {backupStep === 1 ? <button type="submit" form="backup-now-destination" className="primary" disabled={!backupVM || !backupRepo || Boolean(working)}>Review backup</button> :
            <><button type="button" disabled={Boolean(working) || backupUncertain} onClick={() => setBackupStep(1)}>Back</button><button type="button" className="primary" disabled={Boolean(backupBlocker) || Boolean(working)} onClick={() => void submitBackup()}>{working.startsWith(backupNowPrefix) ? "Working…" : backupUncertain ? "Retry backup request" : "Back up now"}</button></>}
          <button type="button" disabled={Boolean(working) || backupUncertain} onClick={closeBackup}>Cancel</button>
        </div>
      </>}>
      {error && <p className="why" role="alert">{error}</p>}
      {repositoryError && <p className="why" role="alert">{repositoryError}</p>}
      {repos === null && !repositoryError && <p className="muted" role="status">Loading backup repositories…</p>}
      <ol className="backup-steps"><li aria-current={backupStep === 1 ? "step" : undefined}>Destination</li><li aria-current={backupStep === 2 ? "step" : undefined}>Review and submit</li></ol>
      {backupUncertain && <p className="why" role="status">The previous reply was uncertain. Retry the same backup request, or check <a href="#/tasks">Tasks</a>; the request is kept if you leave this page.</p>}
      {backupStep === 1 && <form id="backup-now-destination" className="stack" onSubmit={e => { e.preventDefault(); if (backupVM && backupRepo) setBackupStep(2); }}>
        <label>Virtual machine<select required value={backupVM} disabled={Boolean(working) || backupUncertain} onChange={e => setBackupVM(e.target.value)}>
          <option value="">Select VM</option>
          {poolVMs.filter(v => !v.protected).map(v => <option key={v.id} value={v.id}>{v.name}{v.state !== "shut off" ? ` · ${v.state}` : ""}</option>)}
        </select></label>
        <label>Repository<select required value={backupRepo} disabled={Boolean(working) || backupUncertain} onChange={e => setBackupRepo(e.target.value)}>
          <option value="">Select repository</option>
          {(repos || []).map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select></label>
        {repos !== null && repos.length === 0 && <p className="why">Create a backup repository in <a href="#/settings/backups">Settings → Backups</a> before backing up a VM.</p>}
        {backupRunning && <p className="note">This VM is running: the backup is crash-consistent, as if power were cut.</p>}
      </form>}
      {backupStep === 2 && <div className="stack">
        <dl className="details backup-summary">
          <dt>VM</dt><dd>{backupTarget?.name || backupVM}</dd>
          <dt>Repository</dt><dd>{repoByID.get(backupRepo)?.name || backupRepo}</dd>
          <dt>Mode</dt><dd>{backupConsistency === "stopped" ? "Stopped full backup" : "Online crash-consistent"}</dd>
          <dt>Disks</dt><dd>{backupTarget?.spec?.disks?.length ? backupTarget.spec.disks.map(d => d.name).join(", ") : "From Core definition"}</dd>
        </dl>
      </div>}
    </Panel>}

    <div id="backup-jobs-view" role="tabpanel" aria-labelledby="backup-jobs-tab" hidden={view !== "jobs"}>
    <BackupPolicies key={pool.id} onViewPoints={viewPoints}
      api={api} data={data} pool={pool} repos={repos} policies={policies} runs={jobRuns} error={error || policyError || repositoryError}
      busy={busy} unavailable={unavailable} working={working} setWorking={setWorking}
      onNotice={onNotice} onError={setError} reload={async () => { await reload(); await refresh(); }}
      held={held}
      onRepositoryPanelChange={setRepositoryPanelOpen}
      openCreate={policyOpen} onOpenCreate={() => { if (!panelLocked) { saveRestoreDraft(); setSelectedID(""); setRestoreOpen(false); setBackupOpen(false); setPolicyOpen(true); } }} onCloseCreate={() => setPolicyOpen(false)}
    />
    </div>
    <div id="recovery-points" role="tabpanel" aria-labelledby="backup-points-tab" hidden={view !== "points"}><Section icon="backup" title="Recovery points" count={catalogPage?.total} actions={<>
      <button type="button" className="sm primary" disabled={unavailable || busy || panelLocked || policyOpen} onClick={() => { saveRestoreDraft(); setSelectedID(""); setRestoreOpen(false); setBackupOpen(true); setBackupStep(1); }}>
        <Icon kind="backup" />Back up now
      </button>
      <button type="button" className="sm" onClick={() => { location.hash = "/settings/backups"; }}>Repositories</button>
    </>}>
      {pageNotice && !pointPanel && <p className="why" role="status">{pageNotice}</p>}
      <button type="button" className="backup-filter-toggle" aria-expanded={filtersOpen} aria-controls="backup-point-filters" onClick={() => setFiltersOpen(open => !open)}>Filters</button>
      <div className="backup-layout">
      <aside id="backup-point-filters" className={`backup-filters${filtersOpen ? " open" : ""}`} aria-label="Recovery point filters">
        <label>Search<input type="search" value={query} disabled={panelLocked} onChange={e => setQuery(e.target.value)} placeholder="VM, repository or ID" /></label>
        <label>VM<select value={filterVM} disabled={panelLocked} onChange={e => setFilterVM(e.target.value)}>
          <option value="">All VMs</option>
          {poolVMs.map(v => <option key={v.id} value={v.id}>{v.name}</option>)}
          {(points || []).filter((p, i, all) => !poolVMs.some(v => v.id === p.source_vm_id) && all.findIndex(x => x.source_vm_id === p.source_vm_id) === i).map(p =>
            <option key={p.source_vm_id} value={p.source_vm_id}>{p.source_vm_name || p.source_vm_id} (unavailable)</option>)}
        </select></label>
        <label>Repository<select value={filterRepo} disabled={panelLocked} onChange={e => setFilterRepo(e.target.value)}>
          <option value="">All repositories</option>
          {(repos || []).map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select></label>
        <button type="button" disabled={panelLocked || !(filterVM || filterRepo || query)} onClick={() => { setFilterVM(""); setFilterRepo(""); setQuery(""); }}>Clear filters</button>
      </aside>
      <div className="backup-list">
      {points === null && !catalogError ? <p className="backup-loading muted" role="status">Loading recovery points…</p> : null}
      {points && filtered.length === 0 ? <p className="empty">{filterVM || filterRepo || query.trim() ? "No recovery points match." : repos === null ? repositoryError || "Loading backup repositories…" : repos.length ? "No recovery points yet. Back up a VM to create one." : "No backup repositories. Create a destination in Settings first."}</p> : null}
      {filtered.length > 0 ? <div className="table-scroll"><table className="records backup-points">
        <thead><tr><th>VM / recovery point</th><th className="backup-secondary">Captured</th><th className="backup-secondary">Repository</th><th className="backup-secondary">Consistency</th><th className="backup-secondary">Protected</th><th>Last test</th><th><span className="sr-only">Details</span></th></tr></thead>
        <tbody>
          {filtered.map(p => {
            const repo = repoByID.get(p.repository_id);
            const live = replacementTarget(p, poolVMs);
            return <tr key={p.id} className={p.id === selectedID ? "sel" : undefined} onClick={() => inspectPoint(p)}>
              <td>
                <strong className="backup-primary">{live ? <a href={`#/vms/${encodeURIComponent(live.id)}`} onClick={e => e.stopPropagation()}>{live.name}</a> : (p.source_vm_name || p.source_vm_id)}</strong>
                {!live ? <small className="muted">Source VM unavailable</small> : null}
                <span className="backup-row-detail">{date(p.captured_at)}</span>
              </td>
              <td className="backup-secondary"><span className="backup-meta">{date(p.captured_at)}</span></td>
              <td className="backup-secondary">{repo?.name || <span className="mono">{p.repository_id}</span>}</td>
              <td className="backup-secondary"><Badge value={p.consistency} tone={p.consistency === "crash" ? "warn" : "ok"} /></td>
              <td className="backup-secondary">{p.protected ? <Badge value="protected" tone="info" /> : <span className="muted">—</span>}</td>
              <td>{p.last_test_result ? <Badge value={p.last_test_result} /> : <span className="backup-meta">Not tested</span>}</td>
              <td><button type="button" className="sm backup-row-open" disabled={panelLocked} aria-label={`Inspect ${live?.name || p.source_vm_name || p.source_vm_id} (${date(p.captured_at)})`} onClick={e => { e.stopPropagation(); inspectPoint(p); }}>Details</button></td>
            </tr>;
          })}
        </tbody>
      </table></div> : null}
      {catalogPage && <p className="muted" role="status">Showing {filtered.length} of {pointTotal} recovery points.</p>}
      {catalogPage?.next_cursor && <button type="button" className="sm" disabled={Boolean(loadingMore)} onClick={() => void loadMoreCatalog()}>{loadingMore === "catalog" ? "Loading…" : "Load more recovery points"}</button>}
      </div></div>
    </Section></div>

    {selected && view === "points" && !backupOpen && !policyOpen && <Panel wide icon="backup" title={restoreOpen ? "Restore recovery point" : selected.source_vm_name || selected.source_vm_id} badge={<Badge value={selected.consistency} tone={selected.consistency === "crash" ? "warn" : "ok"} />}
      closeDisabled={panelLocked}
      onClose={() => { if (!panelLocked) { saveRestoreDraft(); setSelectedID(""); setRestoreOpen(false); } }}
      sub={<>Captured {date(selected.captured_at)} · {repoByID.get(selected.repository_id)?.name || selected.repository_id}</>}
      footer={restoreOpen ? <>
        {restoreBlocker && <p className="why" role="status">{restoreBlocker}</p>}
        <div className="actions">
          {restoreStep === 1 ? <button type="submit" form="backup-restore-destination" className="primary" disabled={Boolean(restoreBlocker) || Boolean(working)}>Review restore</button> :
            <><button type="button" disabled={panelLocked} onClick={() => setRestoreStep(1)}>Back</button><button type="button" className={`primary${restoreMode === "replace" ? " danger" : ""}`} disabled={Boolean(restoreBlocker) || Boolean(working)} onClick={() => void submitRestore()}>{working ? "Working…" : uncertainRestore ? "Retry restore request" : restoreMode === "replace" ? "Replace original VM" : restoreMode === "test" ? "Create test restore" : "Restore as new"}</button></>}
          <button type="button" disabled={panelLocked} onClick={() => { saveRestoreDraft(); setRestoreOpen(false); }}>Back to recovery point</button>
        </div>
      </> : undefined}
      actions={!restoreOpen ? <>
        <button type="button" className="sm primary" disabled={panelLocked || busy || unavailable} onClick={() => openRestore(selected)}>Restore…</button>
        <button type="button" className="sm" disabled={panelLocked || busy || unavailable} onClick={() => void toggleProtect(selected)}>{held.bodies[`protect-${selected.id}`] ? "Retry protection request" : selected.protected ? "Unprotect from deletion" : "Protect from deletion"}</button>
      </> : undefined}>
      {error && <p className="why" role="alert">{error}</p>}
      {runError && <p className="why" role="alert">{runError}</p>}
      {!reportedPoint && <p className="why" role="status">This recovery point is outside the currently loaded results. Showing the last reported details; this does not mean it was deleted.</p>}
      {pageNotice && <p className="why" role="status">{pageNotice}</p>}
      {retentionWarnings(runs).map(run => <p key={run.operation_id} className="why" role="status">Backup succeeded, but retention could not remove some older recovery points ({date(run.updated_at)}): {run.error}</p>)}
      {!restoreOriginal && <p className="why" role="status">Replace unavailable: the original VM is not in this pool. Use Restore as new.</p>}
      {!restoreOpen && pointRuns === null && !runError && <p className="backup-loading muted" role="status">Loading complete test restore history…</p>}
      {!restoreOpen && (busy || unavailable) && <p className="why" role="status">{unavailable ? "Controller inventory could not be loaded." : "Another task is already in progress."}</p>}
      <section className="tile" aria-label="Recovery point">
        <dl className="details backup-summary">
          <dt>Recovery point</dt><dd className="mono">{selected.id}</dd>
          <dt>Source VM</dt><dd>{selected.source_vm_name || selected.source_vm_id}<br /><span className="mono">{selected.source_vm_id}</span></dd>
          <dt>Repository</dt><dd>{repoByID.get(selected.repository_id)?.name || selected.repository_id}</dd>
          <dt>Captured</dt><dd>{date(selected.captured_at)}</dd>
          <dt>Consistency</dt><dd><Badge value={selected.consistency} tone={selected.consistency === "crash" ? "warn" : "ok"} /></dd>
          <dt>Protected from deletion</dt><dd>{selected.protected ? "Yes" : "No"}</dd>
          <dt>Last test</dt><dd>{selected.last_test_result || "Not tested"}</dd>
        </dl>
      </section>
      {restoreOpen && <section className="backup-panel" aria-label="Restore">
        <ol className="backup-steps"><li aria-current={restoreStep === 1 ? "step" : undefined}>Destination</li><li aria-current={restoreStep === 2 ? "step" : undefined}>Review and submit</li></ol>
        {uncertainRestore && <p className="why" role="status">The previous reply was uncertain. Retry the same request with these selections, or check <a href="#/tasks">Tasks</a>; the request is kept if you leave this page. The destination and restore choice are frozen.</p>}
        <fieldset disabled={panelLocked || restoreStep === 2}>
          <legend>Restore choice</legend>
          <label className="backup-choice"><input type="radio" name="backup-restore-mode" checked={restoreMode === "new"} onChange={() => openRestore(selected, "new")} /><span><strong>Restore as new</strong><small className="backup-meta">Create a separate VM with new identity. The original VM is unchanged.</small></span></label>
          <label className="backup-choice"><input type="radio" name="backup-restore-mode" checked={restoreMode === "replace"} disabled={!restoreOriginal} onChange={() => openRestore(selected, "replace")} /><span><strong>Replace original VM</strong><small className="backup-meta">Replace the original VM's disks after exact-name confirmation. Requires a freshly observed shut-off VM.</small></span></label>
          <label className="backup-choice"><input type="radio" name="backup-restore-mode" checked={restoreMode === "test"} onChange={() => openRestore(selected, "test")} /><span><strong>Test restore</strong><small className="backup-meta">Create an isolated VM, inspect its data, then record the result and clean it up.</small></span></label>
        </fieldset>
        {restoreStep === 1 && <form id="backup-restore-destination" className="stack" onSubmit={(e: FormEvent) => { e.preventDefault(); if (!restoreBlocker) setRestoreStep(2); }}>
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
        </form>}
        {restoreStep === 2 && <div className="stack">
          <dl className="details backup-summary">
            {restoreMode === "replace" ? <><dt>Original VM</dt><dd>{restoreOriginal?.name || selected.source_vm_id} · {selected.source_vm_id}</dd></> : <><dt>New name</dt><dd>{restoreName}</dd><dt>New VM ID</dt><dd className="mono">{restoreVMID}</dd></>}
            <dt>Host</dt><dd>{data.hosts.find(h => h.id === restoreHost)?.hostname || restoreHost}</dd>
            <dt>Storage</dt><dd>{poolStorage.find(s => s.id === restoreStorage)?.name || restoreStorage}</dd>
            <dt>Power / network</dt><dd>{restoreMode === "replace" ? "Stopped · current NICs and MACs retained" : "Stopped · disconnected"}</dd>
          </dl>
        </div>}
      </section>}
      {!restoreOpen && activeTests.length > 0 && <section className="tile" aria-label="Test restore verification">
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
  </div>;
}
