// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";
import type { API, Host } from "./types";
import { AccessLights, Badge, MiniBar, Section, checkedAt, date, size, type HostAccess } from "./ui";
import { Icon } from "./icons";
import { confirmMatches } from "./managed";
import { OrphanedBackups } from "./backup-orphans";
import {
  backupKinds, backupOpID, loadRecoveryPoints, repositoryTarget, repositoryHealth, rid,
  type BackupKind, type BackupMemberAccess, type BackupRecoveryPoint, type BackupRepository,
} from "./backup-types";
import "./settings.css";

type Props = {
  api: API;
  poolID: string;
  masterHostID: string;
  hosts: Host[];
  busy: boolean;
  unavailable: boolean;
  onNotice: (text: string, operationID?: string) => void;
};

const blankRemote = { kind: "nfs" as BackupKind, server: "", exportPath: "", directory: "", share: "", endpoint: "", bucket: "", prefix: "", region: "", root: "", port: "", hostKey: "", username: "", secret: "", sshKey: "", domain: "" };

// validRepositoryID mirrors Core's identifier rule for a repository ID the operator chooses.
export const validRepositoryID = (id: string) => /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(id);
const validHost = (value: string) => value !== "" && !/[\s/\\@]/.test(value);
const validComponent = (value: string) => value !== "" && value !== "." && value !== ".." && !/[/\\]/.test(value);
const validRelative = (path: string) => path.split("/").every(validComponent);

// validSMBDomain mirrors Core: an optional plain domain or workgroup name.
export const validSMBDomain = (value: string) => value.length <= 253 && !/[\s\p{Cc}/\\=,;'"`$&|<>()]/u.test(value);

// backupSource builds the registration source, or null when Core's validator would refuse it,
// so the credential is never delivered for a destination that cannot be registered.
export function backupSource(f: typeof blankRemote) {
  const server = f.server.trim(), directory = f.directory.trim();
  if (f.kind === "nfs") {
    const exportPath = f.exportPath.trim();
    return server && exportPath && !/[/\\]/.test(server) && !directory.startsWith("/") && !directory.includes("..")
      ? { source: { server, export: exportPath, ...(directory ? { directory } : {}) } } : null;
  }
  if (f.kind === "smb3") {
    const share = f.share.trim();
    return validHost(server) && validComponent(share) && (!directory || validRelative(directory))
      ? { smb_source: { server, share, ...(directory ? { directory } : {}) } } : null;
  }
  if (f.kind === "s3") {
    const endpoint = f.endpoint.trim(), bucket = f.bucket.trim(), prefix = f.prefix.trim(), region = f.region.trim();
    return /^https:\/\/[^\s/?#@]+$/.test(endpoint) && validComponent(bucket) && (!prefix || validRelative(prefix)) && /^[a-z0-9-]{0,32}$/.test(region)
      ? { s3_source: { endpoint, bucket, ...(prefix ? { prefix } : {}), ...(region ? { region } : {}) } } : null;
  }
  const root = f.root.trim(), hostKey = f.hostKey.trim(), port = Number(f.port);
  return validHost(server) && (root === "/" || (root.startsWith("/") && validRelative(root.slice(1)))) &&
    /^SHA256:[A-Za-z0-9+/]{43}$/.test(hostKey) && (!f.port || (Number.isInteger(port) && port >= 1 && port <= 65535))
    ? { sftp_source: { host: server, root, host_key_sha256: hostKey, ...(f.port ? { port } : {}) } } : null;
}

function memberState(repo: BackupRepository, access: BackupMemberAccess): string {
  if (access.repository_id !== repo.id) {
    return "pending";
  }
  if (access.error) return access.error;
  return access.readable && access.writable ? "accessible" : "not accessible";
}

export function hostAccess(repo: BackupRepository, hosts: Host[], masterHostID: string): HostAccess[] {
  const poolHosts = hosts.filter(h => h.pool_id === repo.pool_id && !h.revoked);
  return poolHosts.map(host => {
    if (host.connection !== "fresh") {
      return { host: { id: host.id, hostname: host.hostname }, tone: "unknown" as const, label: "Unknown", detail: "Host is not reporting current access" };
    }
    if (host.id === masterHostID) {
      const access = repo.access;
      if (repositoryHealth(repo) === "unknown") {
        return { host: { id: host.id, hostname: host.hostname }, tone: "unknown" as const, label: "Unknown", detail: `Last report: ${date(access?.observed_at)}` };
      }
      if (access?.status === "failed" || (access?.status === "ok" && (!access.readable || !access.writable))) {
        return { host: { id: host.id, hostname: host.hostname }, tone: "bad" as const, label: "No access", detail: access.error || date(access.observed_at) };
      }
      if (access?.status === "ok") {
        return { host: { id: host.id, hostname: host.hostname }, tone: "ok", label: "Accessible", detail: date(access.observed_at) };
      }
      return { host: { id: host.id, hostname: host.hostname }, tone: "unknown" as const, label: "Unknown", detail: date(access?.observed_at) };
    }
    const member = repo.member_access?.find(a => a.host_id === host.id);
    if (!member) {
      return { host: { id: host.id, hostname: host.hostname }, tone: "unknown" as const, label: "Unknown", detail: "No access report" };
    }
    if (repositoryHealth(repo) === "unknown") {
      return { host: { id: host.id, hostname: host.hostname }, tone: "unknown" as const, label: "Unknown", detail: `Last report: ${date(member.observed_at)}` };
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

function capacityCell(repo: BackupRepository, current: boolean) {
  const a = repo.access;
  const total = a?.total_bytes;
  const available = a?.available_bytes;
  if (!total || available === undefined) return <span className="muted">Not reported</span>;
  const used = total - available;
  return <>
    <MiniBar percent={(used / total) * 100} />
    <small>{current ? "" : "Last reported "}{size(used)} used · {size(available)} free</small>
  </>;
}

function healthSummary(repo: BackupRepository) {
  const status = repositoryHealth(repo);
  const transition = repo.last_health_transition;
  return <>
    <Badge value={status} label={status === "unknown" ? "Unknown" : undefined} />
    {status === "offline" && <small role="alert">{repo.health?.error || "Repository is offline"}</small>}
    {status === "unknown" && <small>Current access is unknown; the last check is not current.</small>}
    {transition && <small>Last {transition.status === "offline" ? "offline" : "recovered"}: {date(transition.at)}{transition.error ? ` · ${transition.error}` : ""}</small>}
  </>;
}

// forgetBody carries the counts the operator was shown; Core refuses if the catalog changed since.
export const forgetBody = (requestID: string, typed: string, points: BackupRecoveryPoint[]) => ({
  request_id: requestID, confirmation: typed, recovery_points: points.length, protected_points: points.filter(p => p.protected).length,
});

function ForgetRepository({ api, poolID, repo, onClose, onDone }: {
  api: API; poolID: string; repo: BackupRepository; onClose: () => void; onDone: (operationID?: string) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const requestID = useRef("");
  const [points, setPoints] = useState<BackupRecoveryPoint[] | null>(null);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // A changed confirmation or catalog is a different request; an unchanged retry replays.
  useEffect(() => { requestID.current = rid("bforget"); }, [typed, points]);
  useEffect(() => {
    // Unmounting removes the dialog without closing it, so return focus to the opener ourselves.
    const opener = document.activeElement as HTMLElement | null;
    dialog.current?.showModal();
    loadRecoveryPoints(api, poolID, { repository_id: repo.id }).then(setPoints, e => setError(e instanceof Error ? e.message : String(e)));
    return () => opener?.focus();
  }, []);
  const protectedCount = points?.filter(p => p.protected).length ?? 0;
  const submit = async () => {
    if (!points) return;
    setBusy(true); setError("");
    try {
      const reply = await api<{ operation?: { id?: string } }>(
        `pools/${encodeURIComponent(poolID)}/backup-repositories/${encodeURIComponent(repo.id)}/forget`, "POST", forgetBody(requestID.current, typed, points));
      onDone(backupOpID(reply));
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  };
  return <dialog ref={dialog} className="confirm-dialog" aria-labelledby="forget-repository-title" onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}>
    <div className="section-heading"><h2 id="forget-repository-title">Forget {repo.name}</h2><button type="button" disabled={busy} onClick={onClose}>Close</button></div>
    <p className="warning">Nex forgets this repository, its recovery points and its run history, and removes its mounts and saved credentials from every host. Nothing is deleted from the destination itself. Register the same destination again and import its recovery points to bring them back.</p>
    {error && <p className="alert" role="alert">{error}</p>}
    {!points && !error && <p role="status">Counting recovery points…</p>}
    {points && <p role="status">{points.length} recovery point{points.length === 1 ? "" : "s"} will be forgotten{protectedCount > 0 ? `, ${protectedCount} of them protected` : ""}.</p>}
    <label>Type the repository name to confirm<input required autoComplete="off" value={typed} disabled={busy} onChange={e => setTyped(e.target.value)} /><span className="mono">{repo.name}</span></label>
    <div className="actions">
      <button type="button" disabled={busy} onClick={onClose}>Cancel</button>
      <button type="button" className="danger" disabled={busy || !points || !confirmMatches(typed, repo.name)} onClick={() => void submit()}>{busy ? "Forgetting…" : "Forget repository"}</button>
    </div>
  </dialog>;
}

export function BackupRepositories({ api, poolID, masterHostID, hosts, busy, unavailable, onNotice }: Props) {
  const [rows, setRows] = useState<BackupRepository[] | null>(null);
  const [error, setError] = useState("");
  const [formError, setFormError] = useState("");
  const [form, setForm] = useState(false);
  const [name, setName] = useState("");
  const [repoID, setRepoID] = useState("");
  const [replaceConfirm, setReplaceConfirm] = useState("");
  const [remote, setRemote] = useState(blankRemote);
  const [working, setWorking] = useState("");
  const [createUncertain, setCreateUncertain] = useState(false);
  const [forgetting, setForgetting] = useState<BackupRepository | null>(null);
  const [orphans, setOrphans] = useState<BackupRepository | null>(null);
  const requests = useRef<Record<string, string>>({});
  const createBody = useRef<({ request_id: string; id: string; name: string; kind: BackupKind; credential_request_id?: string; credential_ref?: string; replace_confirmation?: string } & Partial<Pick<BackupRepository, "source" | "smb_source" | "s3_source" | "sftp_source">>) | null>(null);
  const credentialReceipt = useRef("");
  const formScope = useRef(0);
  const loadGen = useRef(0);
  const loadAbort = useRef<AbortController | null>(null);
  const createKey = "create";
  const credentialKey = "credential";
  const source = backupSource(remote);
  const credentialReady = remote.kind === "nfs" || Boolean(remote.username.trim() && !(remote.kind === "smb3" && (remote.username.includes("=") || !validSMBDomain(remote.domain.trim()))) &&
    (credentialReceipt.current || (remote.kind === "sftp" ? remote.sshKey.trim() : remote.secret)));
  // Entering an existing repository's ID replaces its credential or SFTP host key; any other ID re-registers a forgotten destination.
  const existing = rows?.find(r => r.id === repoID.trim());
  const idReady = !repoID.trim() || (validRepositoryID(repoID.trim()) && (!existing || replaceConfirm === existing.id));
  const canCreate = Boolean(name.trim() && source && credentialReady && idReady && !unavailable && !busy);

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
    formScope.current++;
    resetForm();
    requests.current = {};
    setWorking("");
    setRows(null);
    setError("");
    setForgetting(null);
    setOrphans(null);
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
    clearRequest(credentialKey);
    createBody.current = null;
    credentialReceipt.current = "";
    setCreateUncertain(false);
    setForm(false);
    setFormError("");
    setName("");
    setRepoID("");
    setReplaceConfirm("");
    setRemote(blankRemote);
  };
  const change = (update: () => void) => {
    clearRequest(createKey);
    clearRequest(credentialKey);
    createBody.current = null;
    credentialReceipt.current = "";
    update();
  };
  const closeForm = () => {
    if (working || createUncertain) return;
    resetForm();
  };

  const onCreate = async (e: FormEvent) => {
    e.preventDefault();
    if (unavailable || busy || (!createBody.current && (!canCreate || !source))) return;
    const scope = formScope.current;
    setWorking(createKey);
    setFormError("");
    try {
      if (!createBody.current) {
        const registrationID = requestID(createKey, "brepo");
        const credentialID = remote.kind === "nfs" ? "" : requestID(credentialKey, "bcred");
        createBody.current = {
          request_id: registrationID, id: repoID.trim() || rid("brepo"), name: name.trim(), kind: remote.kind,
          ...source!,
          ...(credentialID ? { credential_request_id: credentialID } : {}),
          ...(existing ? { replace_confirmation: existing.id } : {}),
        };
      }
      const body = createBody.current;
      if (body.credential_request_id && !credentialReceipt.current) {
        const credentials = remote.kind === "sftp" ? { username: remote.username.trim(), ssh_private_key: remote.sshKey } : { username: remote.username.trim(), secret: remote.secret, ...(remote.kind === "smb3" && remote.domain.trim() ? { domain: remote.domain.trim() } : {}) };
        const reply = await api<{ receipt: string }>("storage/credentials", "POST", {
          request_id: body.credential_request_id, registration_request_id: `backup-credential:${body.kind}:${body.request_id}`,
          pool_id: poolID, host_id: masterHostID, repository_id: body.id, credentials, repository: { name: body.name, ...source },
        });
        if (scope !== formScope.current) return;
        credentialReceipt.current = reply.receipt;
        body.credential_ref = reply.receipt;
        setRemote(current => ({ ...current, secret: "", sshKey: "" }));
      }
      const reply = await api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(poolID)}/backup-repositories`, "POST", body);
      if (scope !== formScope.current) return;
      clearRequest(createKey);
      onNotice(`Backup repository ${body.name}: registration requested`, backupOpID(reply));
      resetForm();
      await load();
    } catch (err) {
      if (scope !== formScope.current) return;
      const status = (err as { status?: number }).status;
      if (status && status >= 400 && status < 500 && status !== 409) {
        clearRequest(createKey);
        clearRequest(credentialKey);
        createBody.current = null;
        credentialReceipt.current = "";
        setCreateUncertain(false);
      } else setCreateUncertain(true);
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      if (scope === formScope.current) setWorking("");
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
  const fieldsDisabled = Boolean(working) || createUncertain;
  const patchRemote = (values: Partial<typeof blankRemote>) => change(() => setRemote(current => ({ ...current, ...values })));

  return <Section icon="backup" title="Backup repositories" actions={<>
    <button type="button" className="sm primary" disabled={blocked || form || Boolean(working)} title={createWhy || undefined} onClick={() => setForm(true)}>
      <Icon kind="backup" />Create
    </button>
    <button type="button" className="sm" onClick={() => { location.hash = "/backups"; }}>Backup jobs</button>
  </>}>
    {error && <p className="why" role="alert">{error}</p>}
    {form && <section className="panel form-panel" aria-label="Create backup repository">
      <div className="section-heading">
        <h2>Create backup repository</h2>
        <button type="button" disabled={Boolean(working)} onClick={closeForm}>Close</button>
      </div>
      <form className="vm-fields" onSubmit={onCreate}>
        {formError && <p className="why" role="alert">{formError}</p>}
        {createUncertain && <p className="why" role="status">The previous reply was uncertain. Retry this exact registration, or check <a href="#/tasks" target="_blank" rel="noreferrer">Tasks (opens in a new tab)</a>. Leaving this form discards the held request, so keep it open.</p>}
        <label>Name<input required maxLength={100} value={name} autoFocus disabled={fieldsDisabled} onChange={e => change(() => setName(e.target.value))} /></label>
        <label>Repository ID<input value={repoID} maxLength={64} pattern="[A-Za-z0-9][A-Za-z0-9_\-]*" placeholder="Optional; generated when empty" autoComplete="off" disabled={fieldsDisabled} onChange={e => change(() => setRepoID(e.target.value))} /></label>
        <p className="note">Leave empty for a new repository. To bring back a forgotten destination, or to replace a lost credential or an SFTP host key, enter the repository's original ID (shown under its name in the list); its recovery points are kept.</p>
        {existing && <>
          <p className="warning">{existing.name} already exists. Saving replaces its credential and, for SFTP, its pinned host key. The destination must be the same one; Core refuses a different destination.</p>
          <label>Type the repository ID to confirm<input required autoComplete="off" value={replaceConfirm} disabled={fieldsDisabled} onChange={e => setReplaceConfirm(e.target.value)} /><span className="mono">{existing.id}</span></label>
        </>}
        <label>Destination type<select value={remote.kind} disabled={fieldsDisabled} onChange={e => patchRemote({ ...blankRemote, kind: e.target.value as BackupKind, server: remote.server, exportPath: remote.exportPath, directory: remote.directory })}>
          {backupKinds.map(kind => <option key={kind} value={kind}>{kind === "smb3" ? "SMB3" : kind === "s3" ? "S3 compatible" : kind === "sftp" ? "SFTP" : "NFS"}</option>)}
        </select></label>
        {remote.kind === "s3" ? <>
          <label>HTTPS endpoint<input required type="url" value={remote.endpoint} disabled={fieldsDisabled} placeholder="https://objects.example.test" onChange={e => patchRemote({ endpoint: e.target.value })} /></label>
          <label>Bucket<input required value={remote.bucket} disabled={fieldsDisabled} onChange={e => patchRemote({ bucket: e.target.value })} /></label>
          <label>Prefix<input value={remote.prefix} disabled={fieldsDisabled} placeholder="Optional" onChange={e => patchRemote({ prefix: e.target.value })} /></label>
          <label>Region<input value={remote.region} disabled={fieldsDisabled} placeholder="us-east-1" pattern="[a-z0-9\-]{0,32}" onChange={e => patchRemote({ region: e.target.value })} /></label>
        </> : <>
          <label>{remote.kind === "nfs" ? "NFS server" : remote.kind === "smb3" ? "SMB server" : "Server"}<input required value={remote.server} disabled={fieldsDisabled} placeholder="backup.example.test" onChange={e => patchRemote({ server: e.target.value })} /></label>
          {remote.kind === "nfs" && <label>Export path<input required value={remote.exportPath} disabled={fieldsDisabled} placeholder="/exports/backups" pattern="/.*" onChange={e => patchRemote({ exportPath: e.target.value })} /></label>}
          {remote.kind === "smb3" && <label>Share<input required value={remote.share} disabled={fieldsDisabled} onChange={e => patchRemote({ share: e.target.value })} /></label>}
          {(remote.kind === "nfs" || remote.kind === "smb3") && <label>Directory within {remote.kind === "nfs" ? "export" : "share"}<input value={remote.directory} disabled={fieldsDisabled} placeholder="Optional" onChange={e => patchRemote({ directory: e.target.value })} /></label>}
          {remote.kind === "sftp" && <>
            <label>Port<input type="number" min="1" max="65535" value={remote.port} disabled={fieldsDisabled} placeholder="22" onChange={e => patchRemote({ port: e.target.value })} /></label>
            <label>Repository root<input required value={remote.root} disabled={fieldsDisabled} placeholder="/backups" pattern="/.*" onChange={e => patchRemote({ root: e.target.value })} /></label>
          </>}
          {remote.kind === "sftp" && <label>Pinned SSH host key fingerprint<input required value={remote.hostKey} disabled={fieldsDisabled} placeholder="SHA256:…" onChange={e => patchRemote({ hostKey: e.target.value })} /></label>}
        </>}
        {remote.kind !== "nfs" && <>
          <label>{remote.kind === "s3" ? "Access key ID" : "Username"}<input required value={remote.username} autoComplete="username" disabled={fieldsDisabled} onChange={e => patchRemote({ username: e.target.value })} /></label>
          {remote.kind === "smb3" && <label>Domain<input value={remote.domain} autoComplete="off" disabled={fieldsDisabled} placeholder="Optional, for domain accounts" onChange={e => patchRemote({ domain: e.target.value })} /></label>}
          {remote.kind === "sftp" ? <label>SSH private key<textarea required={!credentialReceipt.current} value={remote.sshKey} autoComplete="off" spellCheck={false} disabled={fieldsDisabled} onChange={e => patchRemote({ sshKey: e.target.value })} /></label>
            : <label>{remote.kind === "s3" ? "Secret access key" : "Password"}<input required={!credentialReceipt.current} type="password" value={remote.secret} autoComplete="new-password" disabled={fieldsDisabled} onChange={e => patchRemote({ secret: e.target.value })} /></label>}
        </>}
        <div className="actions">
          <button type="submit" className="primary" disabled={Boolean(working) || (!createBody.current && !canCreate) || blocked}>
            {working === createKey ? "Working…" : "Save"}
          </button>
          <button type="button" disabled={Boolean(working)} onClick={closeForm}>Cancel</button>
        </div>
      </form>
    </section>}
    {rows === null && !error ? <p className="muted">Loading…</p> : null}
    {rows && rows.length === 0 ? <p className="empty">No backup repositories. Create a destination first.</p> : null}
    {rows && rows.length > 0 ? <div className="table-scroll"><table className="records resources backup-repos">
      <thead><tr><th>Name</th><th>Target</th><th>Capacity</th><th>Health</th><th>Host access</th><th>Last check</th><th><span className="sr-only">Actions</span></th></tr></thead>
      <tbody>
        {rows.map(repo => <tr key={repo.id}>
          <td><strong>{repo.name}</strong><small className="mono">{repo.id}</small></td>
          <td><small>{repo.kind.toUpperCase()}</small><span className="mono">{repositoryTarget(repo)}</span></td>
          <td>{capacityCell(repo, repositoryHealth(repo) === "online")}</td>
          <td>{healthSummary(repo)}</td>
          <td><AccessLights items={hostAccess(repo, hosts, masterHostID)} /></td>
          <td><span className="muted">{checkedAt(repo.health?.observed_at)}</span></td>
          <td className="row-actions">
            <button type="button" className="sm" disabled={blocked || Boolean(working)} title={createWhy || undefined} aria-label={`Test access to ${repo.name}`} onClick={() => void onTest(repo)}>
              {working === `test-${repo.id}` ? "Testing…" : "Test access"}
            </button>
            <button type="button" className="sm" disabled={blocked || Boolean(working)} aria-label={`Import points into ${repo.name}`} onClick={() => void onImport(repo)}>
              {working === `import-${repo.id}` ? "Importing…" : "Import points"}
            </button>
            <button type="button" className="sm" disabled={blocked || Boolean(working)} aria-label={`Orphaned backups in ${repo.name}`} onClick={() => setOrphans(repo)}>Orphaned backups</button>
            <button type="button" className="sm danger" disabled={blocked || Boolean(working)} aria-label={`Forget ${repo.name}`} onClick={() => setForgetting(repo)}>Forget</button>
          </td>
        </tr>)}
      </tbody>
    </table></div> : null}
    {/* Forget is queued (202), so the first refetch usually still lists the repository; refetch again once Core has had time to drop it. */}
    {orphans && <OrphanedBackups api={api} poolID={poolID} repo={orphans} onClose={() => setOrphans(null)} />}
    {forgetting && <ForgetRepository api={api} poolID={poolID} repo={forgetting} onClose={() => setForgetting(null)}
      onDone={operationID => { onNotice(`${forgetting.name}: forget requested`, operationID); setForgetting(null); void load(); window.setTimeout(() => void load(), 3000); }} />}
  </Section>;
}
