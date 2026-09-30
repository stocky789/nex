// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type FormEvent } from "react";
import type { API, Host } from "./types";
import { AccessLights, Badge, MiniBar, Section, date, size, type HostAccess } from "./ui";
import { Icon } from "./icons";
import {
  backupKinds, backupOpID, repositoryTarget, repositoryHealth, rid,
  type BackupKind, type BackupMemberAccess, type BackupRepository,
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

export function BackupRepositories({ api, poolID, masterHostID, hosts, busy, unavailable, onNotice }: Props) {
  const [rows, setRows] = useState<BackupRepository[] | null>(null);
  const [error, setError] = useState("");
  const [form, setForm] = useState(false);
  const [name, setName] = useState("");
  const [remote, setRemote] = useState(blankRemote);
  const [working, setWorking] = useState("");
  const [createUncertain, setCreateUncertain] = useState(false);
  const requests = useRef<Record<string, string>>({});
  const createID = useRef("");
  const credentialReceipt = useRef("");
  const loadGen = useRef(0);
  const loadAbort = useRef<AbortController | null>(null);
  const createKey = "create";
  const credentialKey = "credential";
  const source = backupSource(remote);
  const credentialReady = remote.kind === "nfs" || Boolean(remote.username.trim() && !(remote.kind === "smb3" && (remote.username.includes("=") || !validSMBDomain(remote.domain.trim()))) &&
    (credentialReceipt.current || (remote.kind === "sftp" ? remote.sshKey.trim() : remote.secret)));
  const canCreate = Boolean(name.trim() && source && credentialReady && !unavailable && !busy);

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
    clearRequest(credentialKey);
    createID.current = "";
    credentialReceipt.current = "";
    setCreateUncertain(false);
    setForm(false);
    setName("");
    setRemote(blankRemote);
  };
  const change = (update: () => void) => {
    clearRequest(createKey);
    clearRequest(credentialKey);
    createID.current = "";
    credentialReceipt.current = "";
    update();
  };
  const closeForm = () => {
    if (working || createUncertain) return;
    resetForm();
  };

  const onCreate = async (e: FormEvent) => {
    e.preventDefault();
    if (!canCreate || !source) return;
    setWorking(createKey);
    setError("");
    try {
      createID.current ||= rid("brepo");
      const registrationID = requestID(createKey, "brepo");
      const credentialID = remote.kind === "nfs" ? "" : requestID(credentialKey, "bcred");
      if (credentialID && !credentialReceipt.current) {
        const credentials = remote.kind === "sftp" ? { username: remote.username.trim(), ssh_private_key: remote.sshKey } : { username: remote.username.trim(), secret: remote.secret, ...(remote.kind === "smb3" && remote.domain.trim() ? { domain: remote.domain.trim() } : {}) };
        credentialReceipt.current = (await api<{ receipt: string }>("storage/credentials", "POST", {
          request_id: credentialID, registration_request_id: `backup-credential:${remote.kind}:${registrationID}`,
          pool_id: poolID, host_id: masterHostID, repository_id: createID.current, credentials, repository: { name: name.trim(), ...source },
        })).receipt;
        setRemote(current => ({ ...current, secret: "", sshKey: "" }));
      }
      const reply = await api<{ operation?: { id?: string } }>(`pools/${encodeURIComponent(poolID)}/backup-repositories`, "POST", {
        request_id: registrationID,
        id: createID.current,
        name: name.trim(),
        kind: remote.kind,
        ...source,
        ...(credentialID ? { credential_ref: credentialReceipt.current, credential_request_id: credentialID } : {}),
      });
      clearRequest(createKey);
      onNotice(`Backup repository ${name.trim()}: registration requested`, backupOpID(reply));
      resetForm();
      await load();
    } catch (err) {
      const status = (err as { status?: number }).status;
      if (status && status >= 400 && status < 500 && status !== 409) {
        clearRequest(createKey);
        clearRequest(credentialKey);
        createID.current = "";
        credentialReceipt.current = "";
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
  const fieldsDisabled = Boolean(working) || createUncertain;
  const patchRemote = (values: Partial<typeof blankRemote>) => change(() => setRemote(current => ({ ...current, ...values })));

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
        <label>Name<input required maxLength={100} value={name} autoFocus disabled={fieldsDisabled} onChange={e => change(() => setName(e.target.value))} /></label>
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
          <button type="submit" className="primary" disabled={Boolean(working) || !canCreate}>
            {working === createKey ? "Working…" : "Save"}
          </button>
          <button type="button" disabled={Boolean(working)} onClick={closeForm}>Cancel</button>
        </div>
      </form>
    </section>}
    {rows === null && !error ? <p className="muted">Loading…</p> : null}
    {rows && rows.length === 0 ? <p className="empty">No backup repositories. Create a destination first.</p> : null}
    {rows && rows.length > 0 ? <div className="table-scroll"><table className="records resources">
      <thead><tr><th>Name</th><th>Target</th><th>Capacity</th><th>Health</th><th>Host access</th><th>Last check</th><th><span className="sr-only">Actions</span></th></tr></thead>
      <tbody>
        {rows.map(repo => <tr key={repo.id}>
          <td><strong>{repo.name}</strong><small className="mono">{repo.id}</small></td>
          <td><small>{repo.kind.toUpperCase()}</small><span className="mono">{repositoryTarget(repo)}</span></td>
          <td>{capacityCell(repo, repositoryHealth(repo) === "online")}</td>
          <td>{healthSummary(repo)}</td>
          <td><AccessLights items={hostAccess(repo, hosts, masterHostID)} /></td>
          <td><span className="muted">{repo.health?.observed_at ? date(repo.health.observed_at) : "Not checked"}</span></td>
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
