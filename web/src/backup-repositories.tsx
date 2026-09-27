// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";
import type { API, Host } from "./types";
import { AccessLights, Badge, MiniBar, Section, date, size, type HostAccess } from "./ui";
import { Icon } from "./icons";
import {
  backupOpID, nfsTarget, rid,
  type BackupMemberAccess, type BackupRepository,
} from "./backup-types";
import "./settings.css";

type Props = {
  api: API;
  poolID: string;
  hosts: Host[];
  busy: boolean;
  unavailable: boolean;
  onNotice: (text: string, operationID?: string) => void;
};

function memberState(repo: BackupRepository, access: BackupMemberAccess): string {
  if (access.repository_id !== repo.id || (repo.generation != null && access.generation !== repo.generation) || !access.identity) {
    return "pending";
  }
  if (access.error) return access.error;
  return access.readable && access.writable && access.qemu_writable && access.locking ? "accessible" : "not accessible";
}

function hostAccess(repo: BackupRepository, hosts: Host[]): HostAccess[] {
  const poolHosts = hosts.filter(h => h.pool_id === repo.pool_id && !h.revoked);
  return poolHosts.map(host => {
    if (host.connection !== "fresh") {
      return { host: { id: host.id, hostname: host.hostname }, tone: "unknown" as const, label: "Unknown", detail: "Host is not reporting current access" };
    }
    const member = repo.member_access?.find(a => a.host_id === host.id);
    if (!member) {
      return { host: { id: host.id, hostname: host.hostname }, tone: "unknown" as const, label: "Unknown", detail: "No access report" };
    }
    const state = memberState(repo, member);
    if (state === "accessible") {
      return { host: { id: host.id, hostname: host.hostname }, tone: "ok", label: "Accessible", detail: date(member.observed_at) };
    }
    if (state === "pending") {
      return { host: { id: host.id, hostname: host.hostname }, tone: "warn", label: "Pending", detail: date(member.observed_at) };
    }
    return {
      host: { id: host.id, hostname: host.hostname },
      tone: "bad",
      label: state === "not accessible" ? "No access" : "Error",
      detail: member.error || state,
    };
  });
}

function capacityCell(repo: BackupRepository) {
  const a = repo.access;
  const total = a?.total_bytes;
  const available = a?.available_bytes;
  if (!total || available === undefined) return <span className="muted">Not reported</span>;
  const used = total - available;
  return <>
    <MiniBar percent={(used / total) * 100} />
    <small>{size(used)} used · {size(available)} free</small>
  </>;
}

function accessSummary(repo: BackupRepository) {
  const a = repo.access;
  if (!a) return <span className="muted">Not checked</span>;
  if (a.error) return <Badge value="failed" label={a.error} />;
  if (a.readable && a.writable) return <Badge value="ok" label={a.status || "Accessible"} />;
  if (a.status) return <Badge value={a.status} />;
  return <Badge value="degraded" label="Limited" />;
}

export function BackupRepositories({ api, poolID, hosts, busy, unavailable, onNotice }: Props) {
  const [rows, setRows] = useState<BackupRepository[] | null>(null);
  const [error, setError] = useState("");
  const [form, setForm] = useState(false);
  const [name, setName] = useState("");
  const [server, setServer] = useState("");
  const [exportPath, setExportPath] = useState("");
  const [directory, setDirectory] = useState("");
  const [working, setWorking] = useState("");
  const [createUncertain, setCreateUncertain] = useState(false);
  const requests = useRef<Record<string, string>>({});
  const createID = useRef("");
  const loadGen = useRef(0);
  const loadAbort = useRef<AbortController | null>(null);
  const createKey = "create";

  const load = async () => {
    loadAbort.current?.abort();
    const abort = new AbortController();
    loadAbort.current = abort;
    const gen = ++loadGen.current;
    setError("");
    try {
      const next = await api<BackupRepository[]>(`pools/${encodeURIComponent(poolID)}/backup-repositories`, "GET", undefined, undefined, undefined, abort.signal);
      if (gen !== loadGen.current || abort.signal.aborted) return;
      setRows(next);
    } catch (e) {
      if (gen !== loadGen.current || abort.signal.aborted) return;
      setRows(null);
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  useEffect(() => {
    setRows(null);
    setError("");
    setForm(false);
    setCreateUncertain(false);
    requests.current = {};
    createID.current = "";
    void load();
    const timer = window.setInterval(() => { if (document.visibilityState === "visible") void load(); }, 15000);
    return () => { window.clearInterval(timer); loadAbort.current?.abort(); };
  }, [poolID]);

  const requestID = (key: string, prefix: string) => {
    requests.current[key] ??= rid(prefix);
    return requests.current[key];
  };
  const clearRequest = (key: string) => { delete requests.current[key]; };

  const resetForm = () => {
    clearRequest(createKey);
    createID.current = "";
    setCreateUncertain(false);
    setForm(false);
    setName("");
    setServer("");
    setExportPath("");
    setDirectory("");
  };
  const closeForm = () => {
    if (working || createUncertain) return;
    resetForm();
  };

  const onCreate = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim() || !server.trim() || !exportPath.trim() || unavailable || busy) return;
    setWorking(createKey);
    setError("");
    try {
      createID.current ||= rid("brepo");
      const reply = await api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(poolID)}/backup-repositories`, "POST", {
        request_id: requestID(createKey, "brepo"),
        id: createID.current,
        name: name.trim(),
        kind: "nfs",
        source: {
          server: server.trim(),
          export: exportPath.trim(),
          ...(directory.trim() ? { directory: directory.trim() } : {}),
        },
      });
      clearRequest(createKey);
      onNotice(`Backup repository ${name.trim()}: registration requested`, backupOpID(reply));
      resetForm();
      await load();
    } catch (err) {
      const status = (err as { status?: number }).status;
      if (status && status >= 400 && status < 500 && status !== 409) {
        clearRequest(createKey);
        createID.current = "";
        setCreateUncertain(false);
      } else setCreateUncertain(true);
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setWorking("");
    }
  };

  const onTest = async (repo: BackupRepository) => {
    const key = `test-${repo.id}`;
    setWorking(key);
    setError("");
    try {
      const reply = await api<{ operation?: { id?: string } }>(
        `pools/${encodeURIComponent(poolID)}/backup-repositories/${encodeURIComponent(repo.id)}/test`,
        "POST",
        { request_id: requestID(key, "btest") },
      );
      clearRequest(key);
      onNotice(`${repo.name}: access test requested`, backupOpID(reply));
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setWorking("");
    }
  };

  const onImport = async (repo: BackupRepository) => {
    const key = `import-${repo.id}`;
    setWorking(key);
    setError("");
    try {
      const reply = await api<{ operation?: { id?: string } }>(
        `pools/${encodeURIComponent(poolID)}/backup-catalog/import`,
        "POST",
        { request_id: requestID(key, "bimp"), repository_id: repo.id },
      );
      clearRequest(key);
      onNotice(`${repo.name}: import recovery points requested`, backupOpID(reply));
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setWorking("");
    }
  };

  const blocked = unavailable || busy;
  const createWhy = unavailable ? "Controller inventory could not be loaded." : busy ? "Another task is already in progress." : "";

  return <Section icon="backup" title="Backup repositories" actions={<>
    <button type="button" className="sm primary" disabled={blocked || form || Boolean(working)} title={createWhy || undefined} onClick={() => setForm(true)}>
      <Icon kind="backup" />Create
    </button>
    <button type="button" className="sm" onClick={() => { location.hash = "/backups?flow=create-policy"; }}>Policies</button>
  </>}>
    {error && <p className="why" role="alert">{error}</p>}
    {form && <section className="panel form-panel" aria-label="Create backup repository">
      <div className="section-heading">
        <h2>Create backup repository</h2>
        <button type="button" disabled={Boolean(working)} onClick={closeForm}>Close</button>
      </div>
      <form className="vm-fields" onSubmit={onCreate}>
        {createUncertain && <p className="why" role="status">The previous reply was uncertain. Check <a href="#/tasks">Tasks</a>, or retry this exact registration.</p>}
        <label>Name<input required maxLength={100} value={name} autoFocus disabled={Boolean(working) || createUncertain} onChange={e => { setName(e.target.value); clearRequest(createKey); createID.current = ""; }} /></label>
        <label>NFS server<input required value={server} disabled={Boolean(working) || createUncertain} placeholder="nfs.example.test" onChange={e => { setServer(e.target.value); clearRequest(createKey); createID.current = ""; }} /></label>
        <label>Export path<input required value={exportPath} disabled={Boolean(working) || createUncertain} placeholder="/exports/backups" pattern="/.*" onChange={e => { setExportPath(e.target.value); clearRequest(createKey); createID.current = ""; }} /></label>
        <label>Directory within export<input value={directory} disabled={Boolean(working) || createUncertain} placeholder="Optional" onChange={e => { setDirectory(e.target.value); clearRequest(createKey); createID.current = ""; }} /></label>
        <div className="actions">
          <button type="submit" className="primary" disabled={Boolean(working) || !name.trim() || !server.trim() || !exportPath.trim()}>
            {working === createKey ? "Working…" : "Save"}
          </button>
          <button type="button" disabled={Boolean(working)} onClick={closeForm}>Cancel</button>
        </div>
      </form>
    </section>}
    {rows === null && !error ? <p className="muted">Loading…</p> : null}
    {rows && rows.length === 0 ? <p className="empty">No backup repositories. Create an NFS destination first.</p> : null}
    {rows && rows.length > 0 ? <div className="table-scroll"><table className="records resources">
      <thead><tr><th>Name</th><th>Target</th><th>Capacity</th><th>Access</th><th>Host access</th><th>Last check</th><th><span className="sr-only">Actions</span></th></tr></thead>
      <tbody>
        {rows.map(repo => <tr key={repo.id}>
          <td><strong>{repo.name}</strong><small className="mono">{repo.id}</small></td>
          <td className="mono">{nfsTarget(repo)}</td>
          <td>{capacityCell(repo)}</td>
          <td>{accessSummary(repo)}</td>
          <td><AccessLights items={hostAccess(repo, hosts)} /></td>
          <td><span className="muted">{repo.access?.observed_at ? date(repo.access.observed_at) : "Not checked"}</span></td>
          <td className="row-actions">
            <button type="button" className="sm" disabled={blocked || Boolean(working)} title={createWhy || undefined} onClick={() => void onTest(repo)}>
              {working === `test-${repo.id}` ? "Testing…" : "Test access"}
            </button>
            <button type="button" className="sm" disabled={blocked || Boolean(working)} onClick={() => void onImport(repo)}>
              {working === `import-${repo.id}` ? "Importing…" : "Import points"}
            </button>
          </td>
        </tr>)}
      </tbody>
    </table></div> : null}
  </Section>;
}
