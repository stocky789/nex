// SPDX-License-Identifier: AGPL-3.0-only
import {
  Fragment,
  startTransition,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { createRoot } from "react-dom/client";
import "./tokens.css";
import "./style.css";
import { powerActions, powerConfirmText, sentence, type VM, type Storage, type Network, type Media } from "./managed";
import { Icon, type IconKind } from "./icons";
import { followUpdate, type UpdateStatus } from "./updates";
import { LoadError, LoadErrorAlert, Menu, ScreenBoundary, badgeTone, date } from "./ui";
import { operationLabel, statusLabel } from "./labels";
import { taskCounts, newFailures, readPreference, writePreference } from "./dock";
import { attentionItems, sampleCounts } from "./overview-model";
import { consoleBlocker } from "./vm-panel";
import { hostConsoleBlocker } from "./host-model";
import { SettingsHead, SettingsNav, movedSection, sections, type Upload } from "./settings";
import {
  LazyBackups, LazyConsole, LazyContainers, LazyEvents, LazyHostList, LazyManagedVMs,
  LazyOverview, LazyRealtimeStats, LazyScreen, LazySettings, LazyTasks, LazyVMList,
} from "./lazy-screens";
import { rememberSignedIn, signedInBefore } from "./session";
import { applyTheme, themeKey, watchSystemTheme } from "./theme";
import type { API, Audit, Data, Host, Operation, Pool, Session, Site } from "./types";

export type { API, Audit, Check, Data, Host, Operation, Pool, Site } from "./types";

// The scope the top bar filters by: a site, and one of its pools or "" for all of them. It is a
// per-browser convenience; the pools themselves come from the hosts on every load.
type Scope = { site: string; pool: string };
const empty: Data = {
  sites: [],
  pools: [],
  hosts: [],
  operations: [],
  audit: [],
  vms: [], storage: [], networks: [], media: [],
};
// A VM has two CD drives: the install drive and the driver disc drive beside it. Media requests
// name the drive; "install" is also what every request sent before the second drive existed, so
// it is left out of the body and stays byte-identical to those requests.
type Drive = "install" | "drivers";
const driveLabels: Record<Drive, string> = { install: "Install media", drivers: "Driver disc" };
// GuestInfo reports each drive's live tray. Read it through this shape so the file compiles
// against a console Guest type from either build.
type GuestDrives = { media?: string; drivers_media?: string };
const recordedMedia = (vm: VM | undefined, drive: Drive) => (drive === "drivers" ? vm?.spec?.drivers_media_id : vm?.spec?.media_id) || "";
const observedMedia = (vm: VM | undefined, drive: Drive) => (vm?.guest as GuestDrives | null | undefined)?.[drive === "drivers" ? "drivers_media" : "media"];
// The recorded ISO alone does not prove a media change reached the VM: a guest that ejected
// its own tray keeps the recorded media_id, so re-inserting it is applied only once the host
// observes the disc back in the drive.
const vmMediaApplied = (vm: VM | undefined, drive: Drive, mediaID: string) =>
  Boolean(vm?.spec) && recordedMedia(vm, drive) === mediaID && !(mediaID && observedMedia(vm, drive) === "empty");
// A browser that has never signed in to this Controller holds no session cookie, so probing
// GET /api/v1/session on the login page can only be answered 401 — which the browser itself
// logs as a failed request on every load (BUG-0208). This marker records that a session was
// established in this browser, so the probe runs when it can succeed and the login form is
// shown straight away when it cannot. It is a hint about this browser and never an
// authorization: the Controller authenticates every request, and a marker left behind by an
// expired session only costs the one 401 that ends it.
function Brand() {
  return <span className="brand" role="img" aria-label="Nex Controller" />;
}
applyTheme(readPreference(themeKey));
watchSystemTheme(() => applyTheme(""));
function readScope(): Scope {
  try {
    const saved = JSON.parse(readPreference("nex.scope"));
    return { site: String(saved.site || ""), pool: String(saved.pool || "") };
  } catch { return { site: "", pool: "" }; }
}
// GlobalSearch jumps to a VM, a host (by name or IP address) or a settings section. It filters
// the inventory already loaded in the browser; "/" focuses it from anywhere but a field. Under
// 900 px the field folds behind a search button and opens across the top bar.
function GlobalSearch({ data }: { data: Data }) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [open, setOpen] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement;
      if (e.key === "/" && !target.closest("input, textarea, select, [contenteditable]")) { e.preventDefault(); setOpen(true); input.current?.focus(); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
  // The folded field is display:none until open, so it takes focus once it is shown.
  useEffect(() => { if (open) input.current?.focus(); }, [open]);
  const q = query.trim().toLowerCase();
  const results = !q ? [] : [
    ...data.vms.flatMap(v => {
      const ip = v.guest?.addresses?.find(a => a.address.includes(q))?.address;
      return v.name.toLowerCase().includes(q) || ip ? [{ href: `/vms/${encodeURIComponent(v.id)}`, label: v.name, detail: ip ? `VM · ${ip}` : "VM" }] : [];
    }),
    ...data.hosts.flatMap(h => {
      const ip = h.inventory?.nics?.flatMap(n => n.addresses || []).find(a => a.includes(q));
      return h.hostname.toLowerCase().includes(q) || ip ? [{ href: `/hosts/${encodeURIComponent(h.id)}`, label: h.hostname, detail: ip ? `Host · ${ip}` : "Host" }] : [];
    }),
    ...[...rail, ...railEnd].filter(([, label]) => label.toLowerCase().includes(q)).map(([path, label]) => ({ href: `/${path}`, label, detail: "Page" })),
    ...sections.filter(s => s.label.toLowerCase().includes(q)).map(s => ({ href: `/settings/${s.key}`, label: s.label, detail: "Setting" })),
  ].slice(0, 8);
  const current = Math.min(active, results.length - 1);
  function go(href: string) { location.hash = href; setQuery(""); setOpen(false); input.current?.blur(); }
  return <>
    <button type="button" ref={button} className="icon-button search-open" aria-label="Search" aria-expanded={open} onClick={() => setOpen(true)}><Icon kind="search" size={18} /></button>
    <div className={`gsearch${open ? " open" : ""}`} role="search" onBlur={e => { if (!e.currentTarget.contains(e.relatedTarget)) setOpen(false); }}>
      <Icon kind="search" />
      <input ref={input} type="search" role="combobox" aria-label="Search VMs, hosts, IP addresses and settings" aria-autocomplete="list" aria-expanded={Boolean(q)} aria-controls="search-results"
        aria-activedescendant={results.length ? `search-result-${current}` : undefined} placeholder="Jump to a VM, host, IP or setting" value={query}
        onChange={e => { setQuery(e.target.value); setActive(0); }}
        onKeyDown={e => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); setActive(Math.max(0, Math.min(results.length - 1, current + (e.key === "ArrowDown" ? 1 : -1)))); }
          else if (e.key === "Enter" && results.length) { e.preventDefault(); go(results[current].href); }
          else if (e.key === "Escape") { e.preventDefault(); setQuery(""); if (open) { setOpen(false); button.current?.focus(); } }
        }} />
      <kbd>/</kbd>
      {q && <ul id="search-results" role="listbox" aria-label="Search results">
        {results.map((r, i) => <li key={r.href} id={`search-result-${i}`} role="option" aria-selected={i === current} onMouseDown={e => e.preventDefault()} onClick={() => go(r.href)}>{r.label}<small>{r.detail}</small></li>)}
        {!results.length && <li className="muted">No matches</li>}
      </ul>}
    </div>
  </>;
}
// Containers sits under VMs in the rail: container services run in Nex-managed node VMs.
const rail: [string, string, IconKind][] = [["", "Overview", "overview"], ["vms", "VMs", "vm"], ["containers", "Containers", "containers"], ["hosts", "Hosts", "host"], ["storage", "Storage", "storage"], ["networks", "Networks", "network"], ["backups", "Backups", "backup"]];
const railEnd: [string, string, IconKind][] = [["tasks", "Tasks", "tasks"], ["events", "Events", "events"], ["settings", "Settings", "settings"]];
// The pool resource view (ManagedVMs) keeps its forms and the VM wizard mounted across these
// screens; each of its tabs lives at one route, and the ISO library is part of Storage.
const poolTabRoutes: Record<string, string> = { "Virtual machines": "/vms", Storage: "/storage", Networking: "/networks", "ISO library": "/storage" };
function App() {
  const [session, setSession] = useState<Session | null>();
  const [data, setData] = useState<Data>(empty);
  const [route, setRoute] = useState(location.hash.slice(1) || "/");
  const [error, setError] = useState("");
  // actionError holds an operator request's refusal; reload() owns error, so the 10 s poll cannot clear it.
  const [actionError, setActionError] = useState("");
  const [notice, setNoticeText] = useState("");
  // A notice about a queued task keeps that task's id, so it clears once the task leaves queued/dispatched.
  const [noticeOp, setNoticeOp] = useState("");
  const noticeRoute = useRef("");
  const setNotice = (text: string, operationID = "") => { noticeRoute.current = text ? (location.hash.slice(1) || "/") : ""; setNoticeText(text); setNoticeOp(operationID); };
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [scope, setScopeState] = useState(readScope);
  const [theme, setTheme] = useState(() => readPreference(themeKey));
  const [eventsSeen, setEventsSeen] = useState(() => readPreference("nex.events-seen"));
  const [powerConfirm, setPowerConfirm] = useState<{ vm: VM; item: typeof powerActions[number] } | null>(null);
  const [powerConfirmError, setPowerConfirmError] = useState("");
  const [powerConfirmWorking, setPowerConfirmWorking] = useState(false);
  const [form, setForm] = useState<
    "site" | "enroll" | "reenroll" | "revoke" | null
  >(null);
  const [token, setToken] = useState<{
    token: string;
    expires_at: string;
  } | null>(null);
  const [hostAddr, setHostAddr] = useState("");
  const [hostPassword, setHostPassword] = useState("");
  const [hostControllerURL, setHostControllerURL] = useState("");
  const [hostProbe, setHostProbe] = useState<{
    address: string;
    fingerprint: string;
    subject: string;
    not_after: string;
  } | null>(null);
  const [hostAdded, setHostAdded] = useState<{
    host_id: string;
    pool_id: string;
  } | null>(null);
  // A retryable enrollment conflict is a wait, not a failure: the entries stay so a retry is one
  // click, and the host's own words stay available in a fold.
  const [hostRetry, setHostRetry] = useState<{ message: string; hostError: string } | null>(null);
  const [update, setUpdate] = useState<UpdateStatus | null>(null);
  const [updateNote, setUpdateNote] = useState("");
  const [updatePhase, setUpdatePhase] = useState<"idle" | "checking" | "applying" | "done" | "error">("idle");
  const requestID = useRef<string | null>(null);
  const generation = useRef(0);
  const reloadOrdinal = useRef(0);
  // After the first inventory paint, later polls apply through a transition so rail and search
  // stay responsive while lists update.
  const inventoryPainted = useRef(false);
  // The previous poll's operations, so reload() can tell a status that just became failed
  // (announce it) from one that was already failed on the last poll (stay quiet).
  const previousOperations = useRef<Operation[]>([]);
  // The browser-to-Controller upload outlives the form that started it, so the App shell owns
  // the request and reports it in Tasks until the operator navigates or reloads.
  const [upload, setUpload] = useState<Upload | null>(null);
  // The VMs screen shows the create wizard instead of the list. Create VM from another screen sets
  // pendingCreate so the hashchange handler keeps the wizard shown after navigating to #/vms.
  const [creating, setCreating] = useState(false);
  const pendingCreate = useRef(false);
  const [mediaNote, setMediaNote] = useState("");
  const [mediaChange, setMediaChange] = useState<{ vmID: string; action: "insert" | "eject"; drive: Drive; mediaID: string; operation: Operation } | null>(null);
  const [mediaBusy, setMediaBusy] = useState(false);
  const uploadRequest = useRef<XMLHttpRequest | null>(null);
  const uploadCancelled = useRef(false);
  const uploadClear = useRef(0);
  const uploadSample = useRef({ at: 0, loaded: 0, rate: 0 });
  // A query after the path seeds a screen's filter, as Networks' VM counts do for the VM list.
  const [routePath, search = ""] = route.split("?");
  const [kind = "", id, routeOperation] = routePath.split("/").filter(Boolean);
  const vm = data.vms.find(v => (kind === "vms" || kind === "console") && v.id === id);
  const host = data.hosts.find((h) => (kind === "hosts" || kind === "host-console") && h.id === id);
  const site = data.sites.find(s => s.id === scope.site) || data.sites[0];
  const sitePools = data.pools.filter(p => p.site_id === site?.id);
  // A site with one pool is that pool, so its pool screens need no extra choice.
  const pool = sitePools.find(p => p.id === scope.pool) || (sitePools.length === 1 ? sitePools[0] : undefined);
  const inScope = (poolID: string) => pool ? poolID === pool.id : sitePools.some(p => p.id === poolID);
  const scopedHosts = data.hosts.filter(h => inScope(h.pool_id));
  const scopedVMs = data.vms.filter(v => inScope(v.pool_id));
  const masterHost = data.hosts.find(h => h.id === pool?.master_host_id);
  const section = kind === "settings" ? id || "overview" : "";
  // Re-enroll and revoke act on the host whose panel is open; Add host adds to the scoped site.
  const target = form === "reenroll" || form === "revoke" ? host : undefined;
  const enrollSite = target ? data.sites.find(s => s.id === data.pools.find(p => p.id === target.pool_id)?.site_id) : site;
  function setScope(next: Scope) {
    setScopeState(next);
    writePreference("nex.scope", JSON.stringify(next));
  }
  async function api<T>(
    path: string,
    method = "GET",
    body?: unknown,
    progress?: (loaded: number, total: number) => void,
    register?: (xhr: XMLHttpRequest) => void,
    signal?: AbortSignal,
  ): Promise<T> {
    const requestGeneration = generation.current;
    const response = body instanceof Blob ? await new Promise<Response>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open(method, `/api/v1/${path}`);
      xhr.setRequestHeader("Content-Type", "application/octet-stream");
      if (session) xhr.setRequestHeader("X-CSRF-Token", session.csrf_token);
      register?.(xhr);
      xhr.upload.onprogress = e => { if (e.lengthComputable) progress?.(e.loaded, e.total); };
      xhr.onerror = () => reject(new Error("Upload connection lost. Check the ISO library and retry with the same file and destination."));
      xhr.onabort = () => reject(new Error("Upload interrupted. Check the ISO library before retrying."));
      xhr.onload = () => resolve(new Response(xhr.responseText, { status: xhr.status }));
      xhr.send(body);
    }) : await fetch(`/api/v1/${path}`, {
      method,
      credentials: "same-origin",
      headers: {
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
        ...(method !== "GET" && session
          ? { "X-CSRF-Token": session.csrf_token }
          : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    });
    if (response.status === 401 && requestGeneration === generation.current) {
      generation.current++;
      rememberSignedIn(false);
      setSession(null);
      inventoryPainted.current = false;
      setData(empty);
      setToken(null);
      setUpdate(null);
    }
    if (!response.ok) {
      let message = `Request failed (${response.status}).`;
      let hostID: string | undefined;
      // A refusal the caller can present as a wait rather than a failure, with the host's own
      // words kept for diagnosis (currently the add-host "pool identity is held" conflict).
      let retryable = false;
      let hostError: string | undefined;
      try {
        const failure = await response.json();
        message = failure.error || message;
        hostID = failure.host_id || undefined;
        retryable = failure.retryable === true;
        hostError = failure.host_error || undefined;
      } catch {}
      throw Object.assign(new Error(message), { status: response.status, host_id: hostID, retryable, host_error: hostError });
    }
    const result =
      response.status === 204 ? (undefined as T) : await response.json();
    if (requestGeneration !== generation.current)
      throw new Error("Session changed. Sign in and retry the action.");
    return result;
  }
  function cancelUpload() {
    uploadCancelled.current = true;
    uploadRequest.current?.abort();
  }
  async function startIsoUpload<T>(path: string, file: File): Promise<T> {
    if (uploadRequest.current || upload?.state === "uploading") {
      throw new Error("Another ISO upload is already in progress.");
    }
    clearTimeout(uploadClear.current);
    uploadCancelled.current = false;
    uploadSample.current = { at: performance.now(), loaded: 0, rate: 0 };
    setUpload({ name: file.name, loaded: 0, total: file.size, percent: 0, rate: 0, state: "uploading", message: "", actor: session?.username || "", updated_at: new Date().toISOString() });
    const settle = (patch: Partial<Upload>) => {
      uploadRequest.current = null;
      setUpload(current => current && { ...current, rate: 0, updated_at: new Date().toISOString(), ...patch });
      uploadClear.current = window.setTimeout(() => setUpload(null), 8000);
    };
    try {
      const result = await api<T>(path, "POST", file, (loaded, total) => {
        const now = performance.now();
        const previous = uploadSample.current;
        const elapsed = now - previous.at;
        // Average the rate over at least 400 ms of deltas; per-chunk values swing too widely to read.
        if (elapsed >= 400) {
          const instant = (loaded - previous.loaded) / (elapsed / 1000);
          uploadSample.current = { at: now, loaded, rate: previous.rate ? previous.rate * 0.6 + instant * 0.4 : instant };
        }
        setUpload(current => current && { ...current, loaded, total, percent: total ? Math.round((loaded / total) * 100) : 0, rate: uploadSample.current.rate });
      }, xhr => { uploadRequest.current = xhr; });
      settle({ state: "succeeded", percent: 100, loaded: file.size });
      return result;
    } catch (e) {
      if (uploadCancelled.current) {
        settle({ state: "cancelled" });
        throw Object.assign(new Error("Upload cancelled."), { cancelled: true });
      }
      settle({ state: "failed", message: (e as Error).message });
      throw e;
    }
  }
  async function openConsole(id: string) {
    const ticket = await api<{ websocket: string }>(`vms/${encodeURIComponent(id)}/console`, "POST", {});
    return `wss://${location.host}${ticket.websocket}`;
  }
  async function openHostConsole(id: string) {
    const ticket = await api<{ websocket: string }>(`hosts/${encodeURIComponent(id)}/console`, "POST", {});
    return `wss://${location.host}${ticket.websocket}`;
  }
  // One request identity per VM+action until it succeeds, so a retry after an uncertain outcome
  // is the same request rather than a second change.
  const mediaRequest = useRef<Record<string, string>>({});
  useEffect(() => {
    if (!mediaChange) return;
    const task = data.operations.find(o => o.id === mediaChange.operation.id) || mediaChange.operation;
    const target = data.vms.find(v => v.id === mediaChange.vmID);
    if (task.status === "failed" || task.status === "needs_attention") {
      if (task !== mediaChange.operation) setMediaChange({ ...mediaChange, operation: task });
    } else if (task.status === "succeeded" || vmMediaApplied(target, mediaChange.drive, mediaChange.mediaID)) {
      setMediaChange(null);
    }
  }, [mediaChange, data.operations, data.vms]);
  async function changeVMMedia(vmID: string, action: "insert" | "eject", drive: Drive, mediaID?: string) {
    setMediaBusy(true);
    setMediaNote("");
    setMediaChange(null);
    const key = `${vmID}/${drive}/${action}`;
    mediaRequest.current[key] ??= crypto.randomUUID();
    try {
      const { operation } = await api<{ operation: Operation }>(`vms/${encodeURIComponent(vmID)}/media`, "POST", {
        request_id: mediaRequest.current[key],
        action,
        // The install drive is the default and is left out, so its request is exactly the one
        // every earlier build sent; only the second drive has to name itself.
        ...(drive === "drivers" ? { drive } : {}),
        ...(action === "insert" ? { media_id: mediaID } : {}),
      });
      delete mediaRequest.current[key];
      setMediaChange({ vmID, action, drive, mediaID: mediaID || "", operation });
      await reload();
    } catch (e) {
      setMediaNote((e as Error).message);
    } finally {
      setMediaBusy(false);
    }
  }
  // Start from the console issues the same durable power request as the panel. Opening the
  // console first and starting from there is how an operator catches a firmware boot prompt that
  // times out in a few seconds; the request, its task and its outcome are unchanged.
  const powerRequest = useRef<Record<string, string>>({});
  const [starting, setStarting] = useState("");
  const [startError, setStartError] = useState("");
  async function startVM(target: VM) {
    // Start on a protected Controller appliance always goes through the confirmation dialog.
    const start = powerActions.find(item => item.action === "start")!;
    if (powerConfirmText(start, target)) { setPowerConfirm({ vm: target, item: start }); return; }
    setStarting(target.id);
    setStartError("");
    powerRequest.current[target.id] ??= crypto.randomUUID();
    try {
      const res = await api<{ operation?: { id?: string } }>(`vms/${encodeURIComponent(target.id)}/power`, "POST", { request_id: powerRequest.current[target.id], action: "start" });
      delete powerRequest.current[target.id];
      setNotice(`Start queued for ${target.name}. Follow its progress in Tasks.`, res?.operation?.id);
      await reload();
    } catch (e) {
      setStartError((e as Error).message);
    } finally {
      setStarting("");
    }
  }
  const vmPowerControls = (target: VM) => ({ state: target.state, busy: busy || starting === target.id, onStart: () => void startVM(target) });
  // vmMediaNote is what the operator is told about this VM's media right now: a request error,
  // or the outcome of the change they asked for, from the durable task and the host's own
  // observation rather than from the response that queued it.
  function vmMediaNote(target: VM) {
    if (mediaNote) return mediaNote;
    if (mediaChange?.vmID !== target.id) return "";
    const task = data.operations.find(o => o.id === mediaChange.operation.id) || mediaChange.operation;
    const what = driveLabels[mediaChange.drive].toLowerCase();
    if (task.status === "failed" || task.status === "needs_attention") {
      return task.error || `The ${what} change did not complete. Check Tasks.`;
    }
    if (task.status !== "succeeded" && !vmMediaApplied(target, mediaChange.drive, mediaChange.mediaID)) {
      return mediaChange.action === "insert" ? `Inserting the ${what}…` : `Ejecting the ${what}…`;
    }
    return "";
  }
  // vmMediaControls builds the console's CD/DVD controls: one entry per drive, sharing the host's
  // ISO list. The second drive is only offered on a host whose Core defines it.
  function vmMediaControls(target: VM) {
    const host = data.hosts.find(h => h.id === target.host_id);
    const options = data.media.filter(m => m.host_id === target.host_id && m.pool_id === target.pool_id && m.status === "succeeded").map(m => ({ id: m.id, name: m.name }));
    const drives: Drive[] = host?.capabilities?.includes("vm-media-drivers-drive-v1") ? ["install", "drivers"] : ["install"];
    const entries = drives.map(drive => {
      const recorded = recordedMedia(target, drive);
      const recordedName = data.media.find(m => m.id === recorded)?.name;
      // A guest can open its own tray: Debian's installer ejects the disc it booted. The host
      // reports the live drive, Nex keeps the ISO it recorded, and the operator gets it back by
      // inserting it again, so it stays in the list while the tray is empty.
      const ejectedInGuest = Boolean(recorded) && observedMedia(target, drive) === "empty";
      return {
        drive,
        label: driveLabels[drive],
        currentID: recorded || undefined,
        currentName: ejectedInGuest ? `${recordedName || "ISO"} (ejected in the VM)` : recordedName,
        onInsert: (id: string) => void changeVMMedia(target.id, "insert", drive, id),
        onEject: () => void changeVMMedia(target.id, "eject", drive),
      };
    });
    if (!host?.capabilities?.includes("vm-media-v1")) {
      return { drives: entries.map(entry => ({ ...entry, onInsert: () => {}, onEject: () => {} })), options: [] as { id: string; name: string }[], busy: true, note: mediaNote || "This host cannot change media yet. Upgrade Nex Core." };
    }
    const ejectedInGuest = entries.some(entry => entry.currentName?.endsWith("(ejected in the VM)"));
    return {
      drives: entries,
      options,
      busy: mediaBusy,
      note: vmMediaNote(target) || (ejectedInGuest ? "The VM ejected the disc. Insert it again to reconnect it." : ""),
    };
  }
  async function reload() {
    const current = generation.current;
    const ordinal = ++reloadOrdinal.current;
    const isCurrent = () =>
      current === generation.current && ordinal === reloadOrdinal.current;
    try {
      const [sites, pools, hosts, operations, audit, vms, storage, networks, media] = await Promise.all([
        api<Site[]>("sites"),
        api<Pool[]>("pools"),
        api<Host[]>("hosts"),
        api<Operation[]>("operations"),
        api<Audit[]>("audit"),
        api<VM[]>("vms"), api<Storage[]>("storage"), api<Network[]>("networks"), api<Media[]>("media"),
      ]);
      if (isCurrent()) {
        // The notice bar only carries text, not a link (its operation id just tracks a queued
        // task so the notice clears once that task resolves); a failed task is already resolved,
        // so the id goes in the text itself instead of triggering that same clear-on-resolve effect.
        // One call per poll: several failures in the same poll become one notice, not a series
        // that only leaves the last one visible.
        const failures = newFailures(previousOperations.current, operations || []);
        const hostnameOf = (o: Operation) => hosts?.find((h) => h.id === o.host_id)?.hostname || o.host_id;
        if (failures.length === 1) {
          const o = failures[0];
          const verb = o.status === "failed" ? "failed" : "needs attention";
          setNotice(`${operationLabel(o.kind)} on ${hostnameOf(o)} ${verb} (task ${o.id}). ${o.error ?? ""}`.trim());
        } else if (failures.length > 1) {
          const items = failures.map((o) => `${operationLabel(o.kind)} on ${hostnameOf(o)} (${o.id})`).join(", ");
          setNotice(`${failures.length} tasks failed: ${items}.`);
        }
        previousOperations.current = operations || [];
        const next = {
          sites: sites || [],
          pools: pools || [],
          hosts: hosts || [],
          operations: operations || [],
          audit: audit || [],
          vms: vms || [], storage: storage || [], networks: networks || [], media: media || [],
        };
        sampleCounts(next);
        if (!inventoryPainted.current) {
          inventoryPainted.current = true;
          setData(next);
        } else {
          startTransition(() => setData(next));
        }
        setError("");
      }
    } catch (e) {
      if (isCurrent())
        setError(
          sentence((e as Error).message) +
            " Inventory may be out of date. Retry when the controller is reachable.",
        );
    } finally {
      if (isCurrent()) setLoading(false);
    }
  }
  useEffect(() => {
    if (!signedInBefore()) {
      setSession(null);
      setLoading(false);
      return;
    }
    api<Session>("session")
      .then(setSession)
      .catch((e) => {
        setSession(null);
        rememberSignedIn(false);
        if ((e as Error & { status?: number }).status !== 401)
          setError((e as Error).message);
      })
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => {
    if (!session) return;
    setLoading(true);
    void reload();
    const timer = setInterval(() => { if (document.visibilityState === "visible") void reload(); }, 10000);
    return () => clearInterval(timer);
  }, [session]);
  // The update status feeds the Overview, Hosts, Members and Updates screens; it is read once per
  // session and again on Check, not on every route.
  useEffect(() => {
    if (session) api<UpdateStatus>("updates").then(setUpdate, () => {});
  }, [session]);
  // #/sites/{id} and #/pools/{id} set the scope and show its overview, and #/pools/{id}/{section}
  // opens that pool's settings section; #/updates moved to settings, and Storage, Networks and the
  // ISO library moved out of it.
  useEffect(() => {
    if (kind === "updates") location.replace("#/settings/updates");
    const moved = kind === "settings" && movedSection(id);
    if (moved) location.replace(moved);
    const linked = kind === "sites" ? data.sites.find(s => s.id === id) && { site: id, pool: "" }
      : kind === "pools" ? data.pools.filter(p => p.id === id).map(p => ({ site: p.site_id, pool: p.id }))[0] : undefined;
    const settingsSection = kind === "pools" ? route.split("/").filter(Boolean)[2] : undefined;
    if (linked) { setScope(linked); location.replace(settingsSection ? `#/settings/${settingsSection}` : "#/"); }
  }, [kind, id, data.sites, data.pools]);
  // Opening Events marks everything recorded so far as seen, which clears the rail's dot.
  // Only warnings and failures raise the rail's dot; recorded changes do not.
  const newestEvent = data.audit.filter(a => ["warn", "bad"].includes(badgeTone(a.action.split(".").pop() || ""))).reduce((newest, a) => a.created_at > newest ? a.created_at : newest, "");
  useEffect(() => {
    if (kind === "events" && newestEvent > eventsSeen) { setEventsSeen(newestEvent); writePreference("nex.events-seen", newestEvent); }
  }, [kind, newestEvent]);
  // Hash changes clear most transient chrome, but the create wizard stays open across its
  // Storage / Networks / ISO side-trips so returning to #/vms resumes the form.
  const wizardRoutes = new Set(["vms", "storage", "networks"]);
  useEffect(() => {
    const change = () => {
      const nextRoute = location.hash.slice(1) || "/";
      const nextKind = nextRoute.split("/").filter(Boolean)[0] || "";
      setRoute(nextRoute);
      if (pendingCreate.current) {
        setCreating(true);
        pendingCreate.current = false;
      } else if (!wizardRoutes.has(nextKind)) {
        setCreating(false);
      }
      setForm(null);
      setToken(null);
      if (noticeRoute.current !== nextRoute) setNotice("");
      setMediaNote("");
      setMediaChange(null);
      setActionError("");
      resetAddHost();
      requestID.current = null;
    };
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  // Create VM opens the scoped pool's wizard on the VMs screen.
  const newVM = () => showPoolTab("Virtual machines");
  function showPoolTab(tab: string) {
    const path = poolTabRoutes[tab];
    if (tab === "Virtual machines") {
      if ((location.hash.slice(1) || "/") === path) { setCreating(true); return; }
      pendingCreate.current = true;
    }
    location.hash = path;
  }
  useEffect(() => {
    if (noticeOp && data.operations.some(o => o.id === noticeOp && o.status !== "queued" && o.status !== "dispatched")) setNotice("");
  }, [data.operations, noticeOp]);
  async function action(run: () => Promise<void>) {
    setBusy(true);
    setActionError("");
    setNotice("");
    try {
      await run();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  function resetAddHost() {
    setHostAddr("");
    setHostPassword("");
    setHostControllerURL(location.origin);
    setHostProbe(null);
    setHostAdded(null);
    setHostRetry(null);
  }
  // A form dialog returns focus to what opened it; a menu item has closed, so its menu's summary.
  const formOpener = useRef<HTMLElement | null>(null);
  useEffect(() => {
    if (form) return;
    formOpener.current?.focus();
    formOpener.current = null;
  }, [form]);
  function open(next: typeof form) {
    if (busy) return;
    const active = document.activeElement as HTMLElement | null;
    formOpener.current = active?.closest("details")?.querySelector("summary") || active;
    setForm(next);
    setToken(null);
    setActionError("");
    resetAddHost();
  }
  function refreshInventory() {
    if (!host) return;
    void action(async () => {
      requestID.current ??= crypto.randomUUID();
      const op = await api<Operation>(`hosts/${host.id}/operations`, "POST", { kind: "refresh-inventory", request_id: requestID.current });
      requestID.current = null;
      setNotice(`Inventory refresh ${statusLabel(op.status).toLowerCase()}. Follow its progress in Tasks.`);
      await reload();
    });
  }
  const submitName = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const name = String(new FormData(e.currentTarget).get("name")).trim();
    await action(async () => {
      const result = await api<Site>("sites", "POST", { name });
      await reload();
      setForm(null);
      location.hash = `/sites/${result.id}`;
    });
  };
  if (session === undefined)
    return (
      <main className="login">
        <Brand />
        <p role="status">Connecting to Nex Controller…</p>
      </main>
    );
  if (!session)
    return (
      <main className="login">
        <Brand />
        <div className="login-card">
          <p className="eyebrow">NEX CONTROLLER</p>
          <h1>Connect to Nex Controller</h1>
          <p>Sign in to manage your sites, pools and hosts.</p>
          {(error || actionError) && (
            <p className="alert" role="alert">
              {actionError || error}
            </p>
          )}
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const values = new FormData(e.currentTarget);
              void action(async () => {
                const next = await api<Session>("session", "POST", {
                  username: values.get("username"),
                  password: values.get("password"),
                });
                rememberSignedIn(true);
                setSession(next);
              });
            }}
          >
            <label>
              Username
              <input
                name="username"
                autoComplete="username"
                required
                autoFocus
              />
            </label>
            <label>
              Password
              <input
                name="password"
                type="password"
                autoComplete="current-password"
                required
              />
            </label>
            <button className="primary" disabled={busy}>
              {busy ? "Signing in…" : "Sign in"}
            </button>
          </form>
          <p className="help">
            Use the administrator account created on your controller.
          </p>
        </div>
        <footer>
          <a href="/source">Source code</a>
          <span>Nex Controller · AGPL-3.0-only</span>
        </footer>
      </main>
    );
  // The confirmation for console Start on a protected appliance; the console-only page renders it too.
  const closePowerConfirm = () => { setPowerConfirm(null); setPowerConfirmError(""); };
  const powerConfirmDialog = powerConfirm && <dialog className="confirm-dialog" ref={el => { if (el && !el.open) el.showModal(); }} aria-labelledby="power-start-title"
        onClose={closePowerConfirm}
        onCancel={e => { if (powerConfirmWorking) e.preventDefault(); else closePowerConfirm(); }}>
        <form onSubmit={e => { e.preventDefault(); if (powerConfirmWorking) return; void (async () => {
          // One request_id per VM and action until it succeeds, so a retry after a lost reply is reconciled, not repeated.
          const key = `${powerConfirm.vm.id}:${powerConfirm.item.action}`;
          powerRequest.current[key] ??= crypto.randomUUID();
          setPowerConfirmWorking(true);
          setPowerConfirmError("");
          try {
            const res = await api<{ operation?: { id?: string } }>(`vms/${encodeURIComponent(powerConfirm.vm.id)}/power`, "POST", { request_id: powerRequest.current[key], action: powerConfirm.item.action });
            delete powerRequest.current[key];
            setNotice(`${powerConfirm.item.label} queued for ${powerConfirm.vm.name}. Follow its progress in Tasks.`, res?.operation?.id);
            closePowerConfirm();
            await reload();
          } catch (err) { setPowerConfirmError((err as Error).message); }
          finally { setPowerConfirmWorking(false); }
        })(); }}>
          <h2 id="power-start-title">{powerConfirm.item.label} {powerConfirm.vm.name}?</h2>
          {powerConfirmText(powerConfirm.item, powerConfirm.vm) && <p>{powerConfirmText(powerConfirm.item, powerConfirm.vm)}</p>}
          {powerConfirmError && <p className="task-error" role="alert">{powerConfirmError}</p>}
          <div className="actions"><button type="button" disabled={powerConfirmWorking} onClick={closePowerConfirm}>Cancel</button>
            <button disabled={powerConfirmWorking} className={powerConfirm.item.action.startsWith("force-") ? "danger" : "primary"}>Confirm {powerConfirm.item.label.toLowerCase()}</button></div>
        </form>
      </dialog>;
  // Console-only page: the VM panel's Console action opens it, and the host Console tab pops out to it.
  if (kind === "console" || kind === "host-console")
    return (
      <div className="console-page"><ScreenBoundary>
        {kind === "host-console" ? <>
          <div className="console-page-bar"><a href={`#/hosts/${id}`}>Nex Controller</a><strong>{host ? (host.hostname || host.id) : "Host console"}</strong>{host && <span className="muted">{host.connection}</span>}</div>
          {host ? <LazyScreen><LazyConsole key={host.id} name={host.hostname || host.id} disabled={hostConsoleBlocker(host)} open={() => openHostConsole(host.id)} /></LazyScreen>
            : <p className="empty">{loading ? "Loading…" : error || "This host is not known to Nex Controller. Open it from the Hosts list."}</p>}
        </> : <>{powerConfirmDialog}
          <div className="console-page-bar"><a href={`#/vms/${id}`}>Nex Controller</a><strong>{vm ? vm.name : "VM console"}</strong>{vm && <span className="muted">{vm.state || "unknown"}</span>}{startError && <span className="task-error" role="alert">{startError}</span>}{notice && <span role="status">{notice}</span>}</div>
          {vm ? <div className="vm-console-panel" key={vm.id}><LazyScreen><LazyConsole key={vm.id} name={vm.name} guest={vm.guest} disabled={consoleBlocker(vm, data.hosts.find(h => h.id === vm.host_id), Boolean(error))} open={() => openConsole(vm.id)} media={vmMediaControls(vm)} power={vmPowerControls(vm)} /><LazyRealtimeStats kind="vm" id={vm.id} api={api} /></LazyScreen></div>
            : <p className="empty">{loading ? "Loading…" : error || "This VM is not known to Nex Controller. Open it from the VM list."}</p>}
        </>}
      </ScreenBoundary></div>
    );
  const counts = taskCounts(data.operations);
  const running = counts.running + (upload?.state === "uploading" ? 1 : 0);
  const unseenEvents = Boolean(newestEvent) && newestEvent > eventsSeen;
  // Nothing loaded, or a failed reload, is unknown: the dot is neutral rather than healthy.
  const scopeTone = error || !scopedHosts.length ? "" : scopedHosts.every(h => h.connection === "fresh") ? "ok" : "warn";
  const railLink = ([path, label, icon]: [string, string, IconKind]) => {
    const current = path === kind || (path === "" && (kind === "sites" || kind === "pools"));
    return <a key={path} href={`#/${path}`} className={path === "containers" ? "rail-sub" : undefined} aria-current={current ? "page" : undefined}><Icon kind={icon} size={20} />{label}{path === "events" && unseenEvents && <span className="dot warn" role="img" aria-label="New events" />}</a>;
  };
  const closeForm = () => {
    setForm(null);
    setToken(null);
    setActionError("");
    resetAddHost();
  };
  // An open slide-over covers the page, so it carries the load error and Retry instead.
  const panelOpen = (kind === "vms" || kind === "hosts") && Boolean(id);
  const loadError = { error, busy, onRetry: () => void reload() };
  // #/sites/… and #/pools/… redirect into scope; show loading rather than notFound while that runs.
  const scopeRedirect = kind === "sites" || kind === "pools";
  const notFound = <section className="panel empty">
    <h2>{error ? "Inventory unavailable" : "Object not found"}</h2>
    <p>{error ? "Inventory could not be loaded. Use Retry above to load it again." : "This link does not match a site, pool, host or VM known to Nex Controller."}</p>
    <a href="#/">Return to overview</a>
  </section>;
  return (
    <LoadError.Provider value={loadError}>
    <div className="app">
      <a
        className="skip"
        href="#main"
        onClick={(e) => {
          e.preventDefault();
          document.getElementById("main")?.focus();
        }}
      >
        Skip to content
      </a>
      <a className="logo" href="#/" aria-label="Nex Controller overview"><span aria-hidden="true">n</span></a>
      <header className="top">
        <Menu className="scope" summaryLabel={`Scope: ${site?.name || "no site"}, ${pool?.name || "all pools"}. Change scope`} label={<>
          <span className={`dot ${scopeTone}`} aria-hidden="true" /><b>{site?.name || "No site"}</b>
          <span className="sep" aria-hidden="true">/</span><b>{pool?.name || "All pools"}</b><span className="count">{scopedHosts.length} {scopedHosts.length === 1 ? "host" : "hosts"}</span><Icon kind="chevron" size={14} />
        </>}>
          {data.sites.map(s => <Fragment key={s.id}>
            <button type="button" aria-current={s.id === site?.id && !pool ? "true" : undefined} onClick={() => setScope({ site: s.id, pool: "" })}>{s.name} · all pools</button>
            {data.pools.filter(p => p.site_id === s.id).map(p => <button key={p.id} type="button" className="indent" aria-current={p.id === pool?.id ? "true" : undefined} onClick={() => setScope({ site: s.id, pool: p.id })}>{p.name}</button>)}
          </Fragment>)}
          <hr />
          <button type="button" disabled={busy} onClick={() => open("site")}>Create site</button>
          <button type="button" disabled={busy || !site} title={site ? undefined : "Create a site first."} onClick={() => open("enroll")}>Add host</button>
        </Menu>
        <GlobalSearch data={data} />
        <div className="tools">
          {/* Under 900 px the words fold away and the pill keeps the pulse and the count. */}
          <a className="activity" href="#/tasks">{running ? <><span className="pulse" aria-hidden="true" />{running}<span className="activity-words"> {running === 1 ? "task" : "tasks"} running</span></>
            : <><span className="activity-words">No tasks running</span><span className="activity-idle" aria-hidden="true">0</span></>}</a>
          <button type="button" className="icon-button" aria-label={`Theme: ${theme || "system"}. Change theme`} title={`Theme: ${theme || "system"}`} onClick={() => {
            const next = theme === "" ? "light" : theme === "light" ? "dark" : "";
            setTheme(next);
            writePreference(themeKey, next);
            applyTheme(next);
          }}><Icon kind="theme" size={18} /></button>
          <Menu align="end" className="account" summaryLabel="Account" label={<Icon kind="account" size={18} />}>
            <p>Signed in as <strong>{session.username}</strong></p>
            <a href="/api/docs/" target="_blank" rel="noreferrer">API reference</a>
            <a href="/source">Nex source code</a>
            <button
              type="button"
              onClick={() =>
                void action(async () => {
                  await api("session", "DELETE");
                  generation.current++;
                  rememberSignedIn(false);
                  setSession(null);
                  inventoryPainted.current = false;
                  setData(empty);
                  setToken(null);
                  setUpdate(null);
                })
              }
              disabled={busy}
            >
              Sign out
            </button>
          </Menu>
        </div>
      </header>
      <nav className="rail" aria-label="Sections">
        {rail.map(railLink)}
        <span className="rail-gap" />
        {railEnd.map(railLink)}
      </nav>
      <main id="main" tabIndex={-1} className="main">
        {section && <SettingsNav section={section} pool={pool} master={masterHost} />}
        <div className="page">
          {error && !panelOpen && <LoadErrorAlert {...loadError} />}
          {actionError && !form && (
            <div className="alert" role="alert">
              {actionError}
            </div>
          )}
          {notice && (
            <div className="notice" role="status">
              {notice}
            </div>
          )}
          <LazyScreen key={kind}>{kind === "" ? <LazyOverview data={data} site={site} pool={pool} hosts={scopedHosts} vms={scopedVMs} api={api} coreAvailable={update?.core_available_version} loading={loading} busy={busy} onCreateSite={() => open("site")} onAddHost={() => open("enroll")} />
            : kind === "vms" ? !creating && <LazyVMList data={data} vms={scopedVMs} pool={pool} network={new URLSearchParams(search).get("network") || undefined} selectedID={id} operationID={routeOperation} api={api} reload={reload} busy={busy} unavailable={Boolean(error)} loading={loading} onNotice={setNotice}
              onCreateVM={newVM} mediaControls={vmMediaControls}
              installMedia={target => ({ busy: mediaBusy, note: vmMediaNote(target), onEject: () => void changeVMMedia(target.id, "eject", "install") })} />
            : kind === "hosts" ? <LazyHostList data={data} hosts={scopedHosts} selectedID={id} api={api} busy={busy} unavailable={Boolean(error)} loading={loading} coreAvailable={update?.core_available_version}
              openHostConsole={openHostConsole} onRefreshInventory={refreshInventory} onReenroll={() => open("reenroll")} onRevoke={() => open("revoke")} />
            : kind === "storage" || kind === "networks" ? <><h1 className="sr-only">{kind === "storage" ? "Storage" : "Networks"}</h1>{!pool && <p className="empty">Choose a pool in the scope menu to see its {kind}.</p>}</>
            : section ? <SettingsHead section={section} pool={pool} master={masterHost} hostCount={scopedHosts.length} />
            : kind === "backups" ? <LazyBackups />
            : kind === "containers" ? <LazyContainers />
            : kind === "tasks" ? <LazyTasks data={data} upload={upload} onCancelUpload={cancelUpload} />
            : kind === "events" ? <LazyEvents entries={data.audit} data={data} conditions={attentionItems(data, data.hosts, data.vms, data.storage.filter(s => !s.tombstone), update?.core_available_version)} />
            : scopeRedirect || loading ? <p role="status">Loading…</p> : notFound}</LazyScreen>
          {/* ManagedVMs owns the create wizard and the Storage / Networks tables for the scoped
              pool. List screens (VMList, HostList) own #/vms and #/hosts; when creating is true
              VMList is hidden and the wizard is shown here. Storage and Networks routes only
              render a heading above, and this mount supplies the real tables. */}
          {pool && <LazyScreen><LazyManagedVMs key={`${session.username}/${pool.id}`} account={session.username} poolID={pool.id} poolName={pool.name} masterHostID={pool.master_host_id} tab={kind === "vms" ? (creating ? "Virtual machines" : "") : kind === "storage" ? "Storage" : kind === "networks" ? "Networking" : ""} hosts={data.hosts} storage={data.storage} networks={data.networks} media={data.media} vms={data.vms} api={api} reload={reload} setTab={showPoolTab} unavailable={Boolean(error)} uploading={upload?.state === "uploading"} startIsoUpload={startIsoUpload} creating={kind === "vms" && creating} onCloseCreate={() => setCreating(false)} onNotice={setNotice} /></LazyScreen>}
          {/* Settings sections that are not pool resources follow the resource sections, in the nav's order. */}
          {section && <LazyScreen><LazySettings section={section} pool={pool} hosts={scopedHosts} api={api} busy={busy} unavailable={Boolean(error)} onEnrollHost={() => open("enroll")} updates={{
              update,
              note: updateNote,
              phase: updatePhase,
              onCheck: () =>
                void action(async () => {
                  setUpdateNote("");
                  setUpdatePhase("checking");
                  try {
                    setUpdate(await api<UpdateStatus>("updates"));
                    setUpdatePhase("idle");
                  } catch (e) {
                    setUpdatePhase("error");
                    throw e;
                  }
                }),
              onApply: () =>
                void (async () => {
                  setBusy(true);
                  setActionError("");
                  setUpdateNote("");
                  setUpdatePhase("applying");
                  try {
                    await api("updates/apply", "POST", {});
                    const st = await followUpdate(api, setUpdate);
                    if (st?.phase === "done") {
                      setUpdateNote(st.message || "Update complete");
                      setUpdatePhase("done");
                    } else if (st) {
                      setUpdatePhase("error");
                      setActionError(st.error || st.message || "Update failed");
                    } else {
                      setUpdateNote("Still applying after 10 minutes; the result is unknown. Select Check to read the current state.");
                      setUpdatePhase("idle");
                    }
                  } catch (e) {
                    setUpdatePhase("error");
                    setActionError((e as Error).message);
                  } finally {
                    setBusy(false);
                  }
                })(),
            }} /></LazyScreen>}
        </div>
      </main>
      {powerConfirmDialog}
      {form && (
        <dialog
          className="form-dialog"
          ref={el => { if (el && !el.open) el.showModal(); }}
          onCancel={e => { e.preventDefault(); if (!busy) closeForm(); }}
          aria-label={
            form === "site"
              ? "Create site"
              : form === "revoke"
                ? "Confirm revoke"
                : "Add host"
          }
        >
              <div className="section-heading">
                <h2>
                  {form === "site"
                    ? "Create a site"
                    : form === "revoke"
                      ? `Revoke access for ${target?.hostname}?`
                      : target
                        ? `Re-enroll ${target.hostname}`
                        : `Add host to ${enrollSite?.name}`}
                </h2>
                <button
                  onClick={closeForm}
                  disabled={busy}
                >
                  Close
                </button>
              </div>
              {actionError && <p className="alert" role="alert">{actionError}</p>}
              {form === "site" && (
                <form onSubmit={submitName}>
                  <label>
                    Site name
                    <input
                      name="name"
                      required
                      maxLength={100}
                      autoFocus
                      placeholder="e.g. Sydney"
                    />
                  </label>
                  <p className="help">
                    A site groups pools by location or purpose.
                  </p>
                  <button className="primary" disabled={busy}>
                    {busy ? "Creating…" : "Create site"}
                  </button>
                </form>
              )}
              {form === "revoke" && (
                <>
                  <p>
                    This blocks the agent from reporting to this controller.
                    Running VMs and local recovery remain available.
                    Re-enrollment will require a new token for this host.
                  </p>
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      void action(async () => {
                        await api(`hosts/${target!.id}/revoke`, "POST", {});
                        setForm(null);
                        await reload();
                        setNotice(
                          "Host access revoked. Running workloads have not been stopped.",
                        );
                      });
                    }}
                  >
                    <label>
                      Type the hostname to confirm
                      <input
                        name="confirm"
                        required
                        autoFocus
                        onChange={(e) =>
                          e.currentTarget.setCustomValidity(
                            e.currentTarget.value === target!.hostname
                              ? ""
                              : "Enter the exact hostname.",
                          )
                        }
                      />
                    </label>
                    <button className="danger" disabled={busy}>
                      Confirm revoke access
                    </button>
                  </form>
                </>
              )}
              {(form === "enroll" || form === "reenroll") && (
                <>
                  <p>
                    Enroll only the intended host into{" "}
                    <strong>{enrollSite?.name}</strong>. Confirm the certificate
                    fingerprint the host presents before adding it.
                  </p>
                  {!target && (
                    <p className="help">
                      A new host becomes the master of a new pool in this site; a host that already belongs to a pool keeps it. Enrollment does not make local storage shared or enable high availability.
                    </p>
                  )}
                  {hostAdded ? (
                    <p role="status">
                      Host {hostAdded.host_id} enrolled in pool{" "}
                      {data.pools.find((p) => p.id === hostAdded.pool_id)?.name || hostAdded.pool_id}; it appears in the host list after its first report. To make it a member of an existing pool, open Settings → Members, then Add host to pool.
                    </p>
                  ) : hostProbe ? (
                    <>
                      <dl className="details">
                        <dt>Address</dt>
                        <dd className="mono">{hostProbe.address}</dd>
                        <dt>Certificate fingerprint</dt>
                        <dd className="mono">{hostProbe.fingerprint}</dd>
                        <dt>Subject</dt>
                        <dd>{hostProbe.subject}</dd>
                        <dt>Expires</dt>
                        <dd>{date(hostProbe.not_after)}</dd>
                      </dl>
                      <p className="help">
                        Confirm this is the intended host before continuing.
                      </p>
                      {hostRetry && (
                        <div className="warning" role="status">
                          <p>{hostRetry.message}</p>
                          {hostRetry.hostError && (
                            <details>
                              <summary>What the host reported</summary>
                              <p className="mono">{hostRetry.hostError}</p>
                            </details>
                          )}
                        </div>
                      )}
                      <div className="actions">
                        <button
                          className="primary"
                          disabled={busy}
                          onClick={() =>
                            void action(async () => {
                              setHostRetry(null);
                              const added = await api<{
                                host_id: string;
                                pool_id: string;
                              }>(`sites/${enrollSite!.id}/hosts`, "POST", {
                                address: hostProbe.address,
                                fingerprint: hostProbe.fingerprint,
                                root_password: hostPassword,
                                controller_url: hostControllerURL,
                                ...(target ? { host_id: target.id } : {}),
                              }).catch((e: Error & { status?: number; host_id?: string; retryable?: boolean; host_error?: string }) => {
                                // Not a failure: the pool master has not reported this member yet.
                                // Keep the confirmed probe and password so retrying is one click.
                                if (e.status === 409 && e.retryable) {
                                  setHostRetry({ message: sentence(e.message.charAt(0).toUpperCase() + e.message.slice(1)), hostError: e.host_error || "" });
                                  return null;
                                }
                                if (e.status === 409 && e.host_id)
                                  throw new Error(`${sentence(e.message.charAt(0).toUpperCase() + e.message.slice(1))} Host ${e.host_id} still trusts a Controller at this address. Use Re-enroll host on that host, or check the Controller address.`);
                                throw e;
                              });
                              if (!added) return;
                              setHostPassword("");
                              setHostAdded(added);
                              await reload();
                            })
                          }
                        >
                          {busy ? "Adding…" : "Confirm and add host"}
                        </button>
                        <button
                          disabled={busy}
                          onClick={() => {
                            setHostProbe(null);
                            setHostPassword("");
                          }}
                        >
                          Cancel
                        </button>
                      </div>
                    </>
                  ) : (
                    <form
                      onSubmit={(e) => {
                        e.preventDefault();
                        void action(async () => {
                          setHostProbe(
                            await api("hosts/probe", "POST", {
                              address: hostAddr,
                            }),
                          );
                        });
                      }}
                    >
                      <label>
                        Host address
                        <input
                          required
                          autoFocus
                          placeholder="10.0.0.5"
                          value={hostAddr}
                          onChange={(e) => setHostAddr(e.target.value)}
                        />
                      </label>
                      <label>
                        Root password
                        <input
                          type="password"
                          required
                          value={hostPassword}
                          onChange={(e) => setHostPassword(e.target.value)}
                        />
                      </label>
                      <label>
                        Controller address
                        <input
                          required
                          value={hostControllerURL}
                          onChange={(e) =>
                            setHostControllerURL(e.target.value)
                          }
                        />
                      </label>
                      <p className="help">
                        The host must be able to reach this address.
                      </p>
                      <button className="primary" disabled={busy}>
                        {busy ? "Connecting…" : "Connect"}
                      </button>
                    </form>
                  )}
                  <details className="manual-enrollment">
                    <summary>Manual enrollment</summary>
                    {!token ? (
                      <button
                        className="primary"
                        disabled={busy}
                        onClick={() =>
                          void action(async () =>
                            setToken(
                              await api(
                                "enrollment-tokens",
                                "POST",
                                { site_id: enrollSite!.id, ...(target ? { host_id: target.id } : {}) },
                              ),
                            ),
                          )
                        }
                      >
                        Generate enrollment token
                      </button>
                    ) : (
                      <>
                        <label>
                          Single-use enrollment token
                          <textarea
                            readOnly
                            value={token.token}
                            rows={2}
                            onFocus={(e) => e.target.select()}
                          />
                        </label>
                        <p className="help">
                          Expires {date(token.expires_at)}. Store securely;
                          this token is shown only here and is removed when
                          you close this panel.
                        </p>
                        <EnrollmentInstructions host={target} />
                        <p className="help">
                          The host will appear here after enrollment. Keep
                          this page open to see its first report.
                        </p>
                      </>
                    )}
                  </details>
                </>
              )}
        </dialog>
      )}
    </div>
    </LoadError.Provider>
  );
}
function EnrollmentInstructions({ host }: { host?: Host }) {
  return (
    <>
      <h3>On the selected host</h3>
      <p>
        Install the Nex Core agent and securely copy the controller CA
        certificate to <code>/etc/nex/controller-ca.pem</code>. Save the token
        in a root-readable file at <code>/root/enrollment-token</code>, then
        run:
      </p>
      <pre>
        <code>{`nex-core-agent enroll --controller ${location.origin} \\\n  --ca-file /etc/nex/controller-ca.pem \\\n  --token-file /root/enrollment-token \\\n  --state-dir /var/lib/nex-core-agent${host ? ` \\\n  --replace-host ${host.id}` : ""}`}</code>
      </pre>
      <p>
        Start the agent service to report inventory. Remove the token file after
        successful enrollment.
      </p>
      <p className="help">
        Do not paste tokens into shell command arguments or disable TLS
        verification. To rotate this host’s certificate, run{" "}
        <code>nex-core-agent rotate --state-dir /var/lib/nex-core-agent</code>{" "}
        on the host.
      </p>
    </>
  );
}
createRoot(document.getElementById("root")!).render(<App />);
