// SPDX-License-Identifier: AGPL-3.0-only
import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { createRoot } from "react-dom/client";
import "./style.css";
import { HostISOs, ManagedVMs, VMDetails, VMPower, type VM, type Storage, type Network, type Media } from "./managed";
import { Console } from "./console";

type Site = { id: string; name: string };
type Pool = Site & { site_id: string; master_host_id: string };
type Check = {
  name: string;
  status: string;
  freshness: string;
  observed_at: string;
  last_success?: string;
  error?: string;
};
type Inventory = {
  observed_at: string;
  hostname: string;
  cpu_model: string;
  cpu_count: number;
  memory_bytes: number;
  virtualization: string[];
  os_version: string;
  agent_version: string;
  nics: { name: string; mac: string; kind?: string; master?: string; parent?: string; addresses: string[]; state: string }[];
  disks: { name: string; size_bytes: number }[];
  checks: Check[];
};
type Host = {
  id: string;
  pool_id: string;
  hostname: string;
  revoked: boolean;
  capabilities?: string[];
  connection: string;
  last_seen: string | null;
  inventory: Inventory | null;
};
type Operation = {
  id: string;
  host_id: string;
  kind: string;
  request_id: string;
  status: string;
  error?: string;
  created_at: string;
  updated_at: string;
  actor: string;
};
type Audit = {
  id: number;
  actor: string;
  action: string;
  resource_id: string;
  created_at: string;
  detail?: string;
};
type UpdateStatus = {
  package: string;
  installed_version: string;
  available_version?: string;
  update_available: boolean;
  repo_url: string;
  phase?: string;
  progress?: number;
  message?: string;
  error?: string;
};
type Data = {
  sites: Site[];
  pools: Pool[];
  hosts: Host[];
  operations: Operation[];
  audit: Audit[];
  vms: VM[];
  storage: Storage[];
  networks: Network[];
  media: Media[];
};
type Session = { username: string; csrf_token: string };
function checkHealth(check: Check) {
  return check.freshness === "fresh"
    ? check.status
    : check.freshness || "unknown";
}
function observedHealth(host: Host) {
  return !host.inventory?.checks?.length
    ? "unknown"
    : host.inventory.checks.some((c) => checkHealth(c) !== "healthy")
      ? "degraded"
      : "healthy";
}
const empty: Data = {
  sites: [],
  pools: [],
  hosts: [],
  operations: [],
  audit: [],
  vms: [], storage: [], networks: [], media: [],
};
const date = (value?: string | null) =>
  value && !value.startsWith("0001-")
    ? new Date(value).toLocaleString()
    : "Not observed";
const size = (value: number) =>
  `${(value / 1024 ** 3).toLocaleString(undefined, { maximumFractionDigits: 1 })} GiB`;
function Brand() {
  return <span className="brand" role="img" aria-label="Nex Controller" />;
}
function Badge({ value }: { value: string }) {
  return <span className={`badge ${value}`}>{value.replaceAll("-", " ")}</span>;
}
type ResourceKind = "site" | "pool" | "host" | "home" | "refresh" | "tasks" | "vm";
const stroke = { strokeWidth: 1.25, strokeLinecap: "round", strokeLinejoin: "round" } as const;
function Icon({ kind }: { kind: ResourceKind }) {
  const paths: Record<ResourceKind, ReactNode> = {
    vm: <><rect x="2" y="3" width="16" height="11" rx="1.5" fill="#93c5fd" stroke="#1d4ed8" /><path d="M7 17h6M10 14v3" stroke="#1d4ed8" /><path d="m8 6 5 3-5 3z" fill="#fff" /></>,
    home: <><rect x="1.5" y="1.5" width="17" height="17" rx="4.5" fill="#3478ff" /><path d="M6.5 14.5V9.5a3.5 3.5 0 0 1 7 0v5" stroke="#fff" strokeWidth="2.4" /></>,
    site: <><path d="M3 18.5V4.5a1 1 0 0 1 1-1h7a1 1 0 0 1 1 1v14" fill="#4f8dff" stroke="#1d4ed8" /><path d="M12 8h4.5a1 1 0 0 1 1 1v9.5" fill="#a8caff" stroke="#1d4ed8" /><path d="M5 6h2M8 6h2M5 9.5h2M8 9.5h2M5 13h2M8 13h2M14 11h1.5M14 14h1.5" stroke="#fff" strokeWidth="1.5" /><path d="M1.5 18.5h17" stroke="#1d4ed8" /></>,
    pool: <><rect x="2" y="3" width="7" height="10" rx="1.2" fill="#a78bfa" stroke="#6d28d9" /><rect x="11" y="3" width="7" height="10" rx="1.2" fill="#a78bfa" stroke="#6d28d9" /><path d="M4 6h3M4 8.5h3M13 6h3M13 8.5h3" stroke="#fff" strokeWidth="1.3" /><circle cx="7.5" cy="11" r=".9" fill="#4c1d95" /><circle cx="16.5" cy="11" r=".9" fill="#4c1d95" /><path d="M5.5 13v3h9v-3M10 16v2" stroke="#6d28d9" /><circle cx="10" cy="18" r="1.4" fill="#7c3aed" /></>,
    host: <><rect x="4" y="2" width="12" height="16" rx="1.5" fill="#2dd4bf" stroke="#0f766e" /><rect x="6" y="4.5" width="8" height="2.2" rx=".6" fill="#f0fdfa" /><rect x="6" y="8" width="8" height="2.2" rx=".6" fill="#f0fdfa" /><circle cx="7.5" cy="14.5" r="1.1" fill="#134e4a" /><rect x="10" y="13.7" width="4" height="1.6" rx=".6" fill="#134e4a" /></>,
    refresh: <><circle cx="10" cy="10" r="8.5" fill="#dbeafe" /><path d="M16.5 8.5A7 7 0 1 0 17 11.5" stroke="#2563eb" strokeWidth="2" /><path d="M17 3v5.5h-5.5" stroke="#2563eb" strokeWidth="2" /></>,
    tasks: <><rect x="4" y="3.5" width="12" height="14.5" rx="1.5" fill="#fbbf24" stroke="#b45309" /><rect x="7" y="2" width="6" height="3" rx="1" fill="#fff7ed" stroke="#b45309" /><path d="m6.5 10 1.6 1.6L11 8.6" stroke="#fff" strokeWidth="1.7" /><path d="M12.5 10h1.5M6.5 14h7" stroke="#fff" strokeWidth="1.5" /></>,
  };
  return <svg className={`resource-icon icon-${kind}`} viewBox="0 0 20 20" fill="none" {...stroke} aria-hidden="true">{paths[kind]}</svg>;
}
function Menu({ label, children }: { label: string; children: ReactNode }) {
  const ref = useRef<HTMLDetailsElement>(null);
  return <details className="menu" ref={ref}
    onBlur={(e) => { if (!e.currentTarget.contains(e.relatedTarget)) e.currentTarget.open = false; }}
    onKeyDown={(e) => { if (e.key === "Escape") { e.currentTarget.open = false; e.currentTarget.querySelector("summary")?.focus(); } }}>
    <summary>{label}</summary>
    <div className="menu-items" onClick={() => { if (ref.current) ref.current.open = false; }}>{children}</div>
  </details>;
}
function SitePicker({ sites, selected, onSelect }: { sites: Site[]; selected?: Site; onSelect: (id: string) => void }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [position, setPosition] = useState({ top: 0, left: 0, width: 0, maxHeight: 320 });
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const options = sites.filter(s => s.name.toLowerCase().includes(query.trim().toLowerCase()));
  const activeSite = options[Math.min(active, options.length - 1)];
  function close(restoreFocus = false) { setOpen(false); if (restoreFocus) trigger.current?.focus(); }
  function locate() {
    const box = trigger.current!.getBoundingClientRect();
    const top = Math.min(box.bottom + 3, Math.max(8, innerHeight - 180));
    setPosition({ top, left: box.left, width: box.width, maxHeight: Math.min(320, innerHeight - top - 8) });
  }
  function show() {
    locate(); setQuery(""); setActive(Math.max(0, sites.findIndex(s => s.id === selected?.id))); setOpen(true);
  }
  function select(id: string) { close(true); onSelect(id); }
  useEffect(() => {
    if (!open) return;
    search.current?.focus();
    const outside = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) close(); };
    const scroll = (event: Event) => { if (!root.current?.contains(event.target as Node)) close(); };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("scroll", scroll, true);
    window.addEventListener("resize", locate);
    return () => { document.removeEventListener("pointerdown", outside); document.removeEventListener("scroll", scroll, true); window.removeEventListener("resize", locate); };
  }, [open]);
  useEffect(() => {
    if (open && activeSite) document.getElementById(`site-option-${activeSite.id}`)?.scrollIntoView({ block: "nearest" });
  }, [open, activeSite?.id]);
  return <div className="site-picker" ref={root} onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) close(); }} onKeyDown={event => {
    if (event.key === "Escape" && open) { event.preventDefault(); event.stopPropagation(); close(true); }
  }}>
    <button ref={trigger} className="site-trigger" aria-haspopup="listbox" aria-expanded={open} aria-controls={open ? "site-options" : undefined} disabled={!sites.length}
      onClick={() => open ? close() : show()} onKeyDown={event => { if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); show(); } }}>
      <span><span className="sr-only">Site: </span>{selected?.name || "Choose site"}</span><span aria-hidden="true">▾</span>
    </button>
    {open && <div className="site-popup" style={position}>
      <input ref={search} type="search" role="combobox" aria-label="Search sites" aria-autocomplete="list" aria-expanded="true" aria-controls="site-options" aria-activedescendant={activeSite ? `site-option-${activeSite.id}` : undefined}
        placeholder="Search sites…" value={query} onChange={event => { setQuery(event.target.value); setActive(0); }} onKeyDown={event => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); setActive(index => Math.max(0, Math.min(options.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)))); }
          else if (event.key === "Enter") { event.preventDefault(); if (activeSite) select(activeSite.id); }
        }} />
      <div id="site-options" role="listbox" aria-label="Sites" className="site-options" tabIndex={-1}>
        {options.map((item, index) => <button type="button" role="option" id={`site-option-${item.id}`} key={item.id} aria-selected={item.id === selected?.id} tabIndex={-1} className={activeSite?.id === item.id ? "active" : ""}
          onPointerMove={() => setActive(index)} onMouseDown={event => event.preventDefault()} onClick={() => select(item.id)}>{item.name}</button>)}
      </div>
      {!options.length && <p className="help" role="status">No matching sites.</p>}
    </div>}
  </div>;
}
function App() {
  const [session, setSession] = useState<Session | null>();
  const [data, setData] = useState<Data>(empty);
  const [route, setRoute] = useState(location.hash.slice(1) || "/");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [search, setSearch] = useState("");
  const [resourceSearch, setResourceSearch] = useState("");
  const [selectedSiteID, setSelectedSiteID] = useState("");
  const [tab, setTab] = useState("General");
  const [form, setForm] = useState<
    "site" | "enroll" | "revoke" | null
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
  const [update, setUpdate] = useState<UpdateStatus | null>(null);
  const [updateNote, setUpdateNote] = useState("");
  const [updatePhase, setUpdatePhase] = useState<"idle" | "checking" | "applying" | "done" | "error">("idle");
  const requestID = useRef<string | null>(null);
  const generation = useRef(0);
  const reloadOrdinal = useRef(0);
  const [kind, id] = route.split("/").filter(Boolean);
  const vm = data.vms.find(v => (kind === "vms" || kind === "console") && v.id === id);
  const host = data.hosts.find((h) => kind === "hosts" && h.id === id);
  const pool = data.pools.find(
    (p) => p.id === (vm?.pool_id || host?.pool_id || (kind === "pools" ? id : "")),
  );
  const site = data.sites.find(
    (s) => s.id === (pool?.site_id || (kind === "sites" ? id : "")),
  );
  const navigationSite = site || data.sites.find(s => s.id === selectedSiteID) || data.sites[0];
  const matchResource = (name: string) => name.toLowerCase().includes(resourceSearch.trim().toLowerCase());
  const navigationPools = data.pools.filter(p => p.site_id === navigationSite?.id && (
    matchResource(p.name) || data.hosts.some(h => h.pool_id === p.id && matchResource(h.hostname)) || data.vms.some(v => v.pool_id === p.id && matchResource(v.name))
  ));
  useEffect(() => {
    if (!session) return;
    const key = `nex-selected-site/${session.username}`;
    if (site) {
      setSelectedSiteID(site.id);
      try { sessionStorage.setItem(key, site.id); } catch {}
    } else {
      try { setSelectedSiteID(sessionStorage.getItem(key) || ""); } catch {}
    }
  }, [site?.id, session?.username]);
  const scopedHosts = data.hosts.filter((h) =>
    host
      ? h.id === host.id
      : pool
        ? h.pool_id === pool.id
        : site
          ? data.pools.some((p) => p.id === h.pool_id && p.site_id === site.id)
          : true,
  );
  const hosts = scopedHosts.filter((h) =>
    h.hostname.toLowerCase().includes(search.toLowerCase()),
  );
  async function api<T>(
    path: string,
    method = "GET",
    body?: unknown,
    progress?: (percent: number) => void,
  ): Promise<T> {
    const requestGeneration = generation.current;
    const response = body instanceof Blob ? await new Promise<Response>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open(method, `/api/v1/${path}`);
      xhr.setRequestHeader("Content-Type", "application/octet-stream");
      if (session) xhr.setRequestHeader("X-CSRF-Token", session.csrf_token);
      xhr.upload.onprogress = e => { if (e.lengthComputable) progress?.(Math.round(e.loaded / e.total * 100)); };
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
    });
    if (response.status === 401 && requestGeneration === generation.current) {
      generation.current++;
      setSession(null);
      setData(empty);
      setToken(null);
    }
    if (!response.ok) {
      let message = `Request failed (${response.status}).`;
      try {
        message = (await response.json()).error || message;
      } catch {}
      throw Object.assign(new Error(message), { status: response.status });
    }
    const result =
      response.status === 204 ? (undefined as T) : await response.json();
    if (requestGeneration !== generation.current)
      throw new Error("Session changed. Sign in and retry the action.");
    return result;
  }
  async function openConsole(id: string) {
    const ticket = await api<{ websocket: string }>(`vms/${encodeURIComponent(id)}/console`, "POST", {});
    return `wss://${location.host}${ticket.websocket}`;
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
        setData({
          sites: sites || [],
          pools: pools || [],
          hosts: hosts || [],
          operations: operations || [],
          audit: audit || [],
          vms: vms || [], storage: storage || [], networks: networks || [], media: media || [],
        });
        setError("");
      }
    } catch (e) {
      if (isCurrent())
        setError(
          (e as Error).message +
            " Inventory may be out of date. Retry when the controller is reachable.",
        );
    } finally {
      if (isCurrent()) setLoading(false);
    }
  }
  useEffect(() => {
    api<Session>("session")
      .then(setSession)
      .catch((e) => {
        setSession(null);
        if ((e as Error & { status?: number }).status !== 401)
          setError((e as Error).message);
      })
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => {
    if (!session) return;
    setLoading(true);
    void reload();
    const timer = setInterval(() => void reload(), 10000);
    return () => clearInterval(timer);
  }, [session]);
  useEffect(() => {
    if (!session || kind !== "updates") return;
    setUpdateNote("");
    void action(async () => {
      setUpdate(await api<UpdateStatus>("updates"));
    });
  }, [session, kind]);
  useEffect(() => {
    const change = () => {
      setRoute(location.hash.slice(1) || "/");
      setSearch("");
      setResourceSearch("");
      setTab("General");
      setForm(null);
      setToken(null);
      setNotice("");
      resetAddHost();
      requestID.current = null;
    };
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  async function action(run: () => Promise<void>) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await run();
    } catch (e) {
      setError((e as Error).message);
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
  }
  function open(next: typeof form) {
    if (busy) return;
    setForm(next);
    setToken(null);
    setError("");
    resetAddHost();
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
          {error && (
            <p className="alert" role="alert">
              {error}
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
  // Console-only page for a separate browser window; the VM page's Console tab links here.
  if (kind === "console")
    return (
      <div className="console-page">
        <div className="console-page-bar"><a href={`#/vms/${id}`}>Nex Controller</a><strong>{vm ? vm.name : "VM console"}</strong>{vm && <span className="muted">{vm.state || "unknown"}</span>}</div>
        {vm ? <Console key={vm.id} name={vm.name} guest={vm.guest} disabled={consoleBlocker(vm, data.hosts.find(h => h.id === vm.host_id))} open={() => openConsole(vm.id)} />
          : <p className="empty">{loading ? "Loading…" : error || "This VM is not known to Nex Controller. Choose it from the Resources tree."}</p>}
      </div>
    );
  const invalid = Boolean(
    kind &&
    kind !== "tasks" &&
    kind !== "updates" &&
    (!id ||
      !["sites", "pools", "hosts", "vms"].includes(kind) ||
      (kind === "sites" && !site) ||
      (kind === "pools" && !pool) ||
      (kind === "hosts" && !host) ||
      (kind === "vms" && !vm)),
  );
  return (
    <div className="desktop-app">
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
      <header className="topbar">
        <a href="#/" aria-label="Nex Controller home">
          <Icon kind="home" /><span>Nex Controller</span>
        </a>
        <div className="account">
          <span>{session.username}</span>
          <button
            onClick={() =>
              void action(async () => {
                await api("session", "DELETE");
                generation.current++;
                setSession(null);
                setData(empty);
                setToken(null);
              })
            }
            disabled={busy}
          >
            Sign out
          </button>
        </div>
      </header>
      <div className="menubar" aria-label="Application menus">
        <Menu label="File"><button disabled={busy} onClick={() => open("site")}>Create site</button><button disabled={busy || !site || Boolean(host && !host.revoked)} onClick={() => open("enroll")}>{host ? "Re-enroll host" : "Add host"}</button></Menu>
        <Menu label="View"><a href="#/">Overview</a><a href="#/tasks">Task history</a><a href="#/updates">Updates</a><button onClick={() => void reload()} disabled={busy}>Refresh view</button></Menu>
        <Menu label="Help"><a href="/api/docs/" target="_blank" rel="noreferrer">API reference</a><a href="/source">Source code</a></Menu>
      </div>
      <div className="toolbar" aria-label="Controller actions">
            <div className="actions">
              {(
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() => open("site")}
                >
                  <Icon kind="site" /> Create site
                </button>
              )}
              {site && !host && (
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() => open("enroll")}
                >
                  <Icon kind="host" /> Add host
                </button>
              )}
              {host && !host.revoked && (
                <>
                  <button
                    className="primary"
                    disabled={busy}
                    onClick={() =>
                      void action(async () => {
                        requestID.current ??= crypto.randomUUID();
                        const op = await api<Operation>(
                          `hosts/${host.id}/operations`,
                          "POST",
                          {
                            kind: "refresh-inventory",
                            request_id: requestID.current,
                          },
                        );
                        requestID.current = null;
                        setNotice(
                          `Inventory refresh ${op.status}. Follow its progress in Task history.`,
                        );
                        await reload();
                      })
                    }
                  >
                    <Icon kind="refresh" /> Refresh inventory
                  </button>
                  <button
                    className="danger"
                    disabled={busy}
                    onClick={() => open("revoke")}
                  >
                    Revoke access
                  </button>
                </>
              )}
              {host?.revoked && (
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() => open("enroll")}
                >
                  Re-enroll host
                </button>
              )}
            </div>
        <button disabled={busy} onClick={() => void reload()}><Icon kind="refresh" /> Refresh view</button>
      </div>
      <div className="shell">
        <aside>
          <div className="nav-title">Resources</div>
          <div className="site-selector">
            <SitePicker sites={data.sites} selected={navigationSite} onSelect={id => { setSelectedSiteID(id); setResourceSearch(""); location.hash = `/sites/${id}`; }} />
            {navigationSite && <a href={`#/sites/${navigationSite.id}`}>Site overview</a>}
            {!loading && !data.sites.length && <><p className="help">{error ? "Sites could not be loaded. Refresh the view to retry." : "No sites yet. Create a site to get started."}</p><button disabled={busy} onClick={() => open("site")}>Create site</button></>}
          </div>
          <label className="resource-search"><span className="sr-only">Search resources</span><input type="search" placeholder="Search resources…" value={resourceSearch} onChange={(e) => setResourceSearch(e.target.value)} /></label>
          <nav aria-label="Infrastructure">
            {navigationPools.map(p => <details key={`${p.id}/${resourceSearch}`} className="tree-branch" open>
              <summary aria-label={`Expand or collapse ${p.name}`}><a className={`tree-node ${kind === "pools" && id === p.id ? "selected" : ""}`} aria-current={kind === "pools" && id === p.id ? "page" : undefined} href={`#/pools/${p.id}`}><Icon kind="pool" />{p.name}</a></summary>
              <div className="tree">{data.hosts.filter(h => h.pool_id === p.id && (matchResource(p.name) || matchResource(h.hostname) || data.vms.some(v => v.host_id === h.id && v.pool_id === p.id && matchResource(v.name)))).map(h =>
                <div key={h.id}><a className={`host-link ${host?.id === h.id ? "selected" : ""}`} aria-current={host?.id === h.id ? "page" : undefined} href={`#/hosts/${h.id}`}><Icon kind="host" />{h.hostname}{p.master_host_id === h.id && " · master"}<span className={`connection-dot ${h.connection}`} title={h.connection} aria-label={`Connection ${h.connection}`} /></a>
                <div className="tree">{data.vms.filter(v => v.pool_id === p.id && v.host_id === h.id && (matchResource(p.name) || matchResource(h.hostname) || matchResource(v.name))).map(v => <a className={`host-link ${vm?.id === v.id ? "selected" : ""}`} key={v.id} aria-current={vm?.id === v.id ? "page" : undefined} href={`#/vms/${v.id}`}><Icon kind="vm" />{v.name}</a>)}</div></div>
              )}</div>
            </details>)}
            {navigationSite && !navigationPools.length && <p className="empty">{resourceSearch ? "No matching resources in this site." : "No hosts in this site."}</p>}
          </nav>
          <div className="sidebar-foot"><a href="#/"><Icon kind="home" /> Infrastructure</a><a className={kind === "tasks" ? "selected" : ""} href="#/tasks"><Icon kind="tasks" /> Task history</a><a className={kind === "updates" ? "selected" : ""} href="#/updates">Updates</a></div>
        </aside>
        <main id="main" tabIndex={-1}>
          <div className="breadcrumbs">
            <a href="#/">Workspace</a>
            {site && (
              <>
                {" "}
                / <a href={`#/sites/${site.id}`}>{site.name}</a>
              </>
            )}
            {pool && (
              <>
                {" "}
                / <a href={`#/pools/${pool.id}`}>{pool.name}</a>
              </>
            )}
            {host && <> / {host.hostname}</>}{vm && <> / {vm.name}</>}
          </div>
          <div className="page-heading">
            <div>
              <p className="eyebrow">
                {host
                  ? "HOST"
                  : pool
                    ? "POOL"
                    : site
                      ? "SITE"
                      : kind === "tasks"
                        ? "OPERATIONS"
                        : kind === "updates"
                          ? "UPDATES"
                          : "INFRASTRUCTURE"}
              </p>
              <h1><Icon kind={vm ? "vm" : host ? "host" : pool ? "pool" : site ? "site" : kind === "tasks" ? "tasks" : "home"} />
                {vm?.name || host?.hostname ||
                  pool?.name ||
                  site?.name ||
                  (kind === "tasks" ? "Task history" : kind === "updates" ? "Software updates" : "Your workspace")}
              </h1>
              <p>
                {vm
                  ? "Desired configuration and observed state."
                  : host
                  ? "Observed resources and management connectivity."
                  : pool
                    ? "Hosts, virtual machines, storage and guest networks for this pool. The pool master controls the pool."
                    : site
                      ? "Pools and hosts at this site."
                      : kind === "tasks"
                        ? "Durable requests, progress and outcomes."
                        : kind === "updates"
                          ? null
                          : "Sites, pools and connected hosts."}
              </p>
            </div>
          </div>
          {kind !== "tasks" && kind !== "updates" && !invalid && <nav className="detail-tabs" aria-label="Resource views">
            {(vm ? ["General", "Console", "Activity"] : host ? ["General", "Networking", "Storage", "Activity"] : pool ? ["General", "Virtual machines", "Storage", "Networking", "ISO library", "Activity"] : ["General", "Activity"]).map((name) =>
              <button key={name} aria-current={tab === name ? "page" : undefined} onClick={() => setTab(name)}>{name}</button>
            )}
          </nav>}
          <div className="workspace-content">

          {error && (
            <div className="alert" role="alert">
              {error}
              <button disabled={busy} onClick={() => void reload()}>
                Retry
              </button>
            </div>
          )}
          {notice && (
            <div className="notice" role="status">
              {notice}
            </div>
          )}
          {loading && <p role="status">Loading your workspace…</p>}
          {form && (
            <section
              className="panel form-panel"
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
                      ? `Revoke access for ${host?.hostname}?`
                      : host
                        ? `Re-enroll ${host.hostname}`
                        : `Add host to ${site?.name}`}
                </h2>
                <button
                  onClick={() => {
                    setForm(null);
                    setToken(null);
                    resetAddHost();
                  }}
                  disabled={busy}
                >
                  Close
                </button>
              </div>
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
                        await api(`hosts/${host!.id}/revoke`, "POST", {});
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
                            e.currentTarget.value === host!.hostname
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
              {form === "enroll" && (
                <>
                  <p>
                    Enroll only the intended host into{" "}
                    <strong>{site?.name}</strong>. Confirm the certificate
                    fingerprint the host presents before adding it.
                  </p>
                  {!host && (
                    <p className="help">
                      The new host becomes the master of a new pool in this site. Enrollment does not make local storage shared or enable high availability.
                    </p>
                  )}
                  {hostAdded ? (
                    <p role="status">
                      Host {hostAdded.host_id}{" "}
                      {host
                        ? "re-enrolled and keeps its pool"
                        : "enrolled as master of a new pool"}
                      ; it appears in the tree after its first report.
                    </p>
                  ) : hostProbe ? (
                    <>
                      <Details>
                        <dt>Address</dt>
                        <dd className="mono">{hostProbe.address}</dd>
                        <dt>Certificate fingerprint</dt>
                        <dd className="mono">{hostProbe.fingerprint}</dd>
                        <dt>Subject</dt>
                        <dd>{hostProbe.subject}</dd>
                        <dt>Expires</dt>
                        <dd>{date(hostProbe.not_after)}</dd>
                      </Details>
                      <p className="help">
                        Confirm this is the intended host before continuing.
                      </p>
                      <div className="actions">
                        <button
                          className="primary"
                          disabled={busy}
                          onClick={() =>
                            void action(async () => {
                              const added = await api<{
                                host_id: string;
                                pool_id: string;
                              }>(`sites/${site!.id}/hosts`, "POST", {
                                address: hostProbe.address,
                                fingerprint: hostProbe.fingerprint,
                                root_password: hostPassword,
                                controller_url: hostControllerURL,
                                ...(host ? { host_id: host.id } : {}),
                              });
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
                                host
                                  ? { site_id: pool!.site_id, host_id: host.id }
                                  : { site_id: site!.id },
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
                        <EnrollmentInstructions host={host} />
                        <p className="help">
                          The host will appear here after enrollment. Keep
                          this page open to see its first report.
                        </p>
                      </>
                    )}
                  </details>
                </>
              )}
            </section>
          )}
          {loading ? null : invalid ? (
            <section className="panel empty">
              <h2>{error ? "Inventory unavailable" : "Object not found"}</h2>
              <p>
                {error
                  ? "Retry the controller connection to load this object."
                  : "This link does not match a site, pool, host or VM in your workspace."}
              </p>
              <a href="#/">Return to overview</a>
            </section>
          ) : kind === "tasks" ? (
            <>
              <Tasks operations={data.operations} hosts={data.hosts} />
              <Activity entries={data.audit} data={data} />
            </>
          ) : kind === "updates" ? (
            <UpdatesPanel
              update={update}
              note={updateNote}
              phase={updatePhase}
              busy={busy}
              onCheck={() =>
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
                })
              }
              onApply={() =>
                void (async () => {
                  setBusy(true);
                  setError("");
                  setUpdateNote("");
                  setUpdatePhase("applying");
                  try {
                    await api("updates/apply", "POST", {});
                    for (let i = 0; i < 600; i++) {
                      await new Promise(r => setTimeout(r, 1000));
                      const st = await api<UpdateStatus>("updates");
                      setUpdate(st);
                      if (st.phase === "done") {
                        setUpdateNote(st.message || "Update complete");
                        setUpdatePhase("done");
                        return;
                      }
                      if (st.phase === "failed") {
                        setUpdatePhase("error");
                        setError(st.error || st.message || "Update failed");
                        return;
                      }
                    }
                    setUpdateNote("Still applying… refresh Updates if needed");
                    setUpdatePhase("done");
                  } catch (e) {
                    setUpdatePhase("error");
                    setError((e as Error).message);
                  } finally {
                    setBusy(false);
                  }
                })()
              }
            />
          ) : (
            <>
              {pool && !host && !vm && <ManagedVMs key={`${session.username}/${pool.id}`} account={session.username} poolID={pool.id} tab={tab} hosts={data.hosts} storage={data.storage} networks={data.networks} media={data.media} vms={data.vms} api={api} reload={reload} setTab={setTab} unavailable={Boolean(error)} />}
              {vm && tab !== "Activity" && <VMPower key={`power-${vm.id}`} vm={vm} host={data.hosts.find(h => h.id === vm.host_id)} api={api} reload={reload} busy={busy} unavailable={Boolean(error)} onNotice={setNotice} />}
              {vm && tab === "General" && <VMDetails vm={vm} hosts={data.hosts} storage={data.storage} networks={data.networks} media={data.media} />}
              {vm && tab === "Console" && <Console key={vm.id} name={vm.name} guest={vm.guest} popout={`#/console/${vm.id}`} disabled={consoleBlocker(vm, data.hosts.find(h => h.id === vm.host_id))} open={() => openConsole(vm.id)} />}
              <div hidden={Boolean(vm) || tab === "Activity" || Boolean(pool && !host && tab !== "General")}>
              {!host && (
                <div className="metrics">
                  <Metric label="Hosts" value={scopedHosts.length} />
                  <Metric
                    label="Fresh connections"
                    value={
                      scopedHosts.filter((h) => h.connection === "fresh").length
                    }
                  />
                  <Metric
                    label="Need attention"
                    value={
                      scopedHosts.filter(
                        (h) =>
                          h.connection !== "fresh" ||
                          observedHealth(h) !== "healthy",
                      ).length
                    }
                  />
                  {pool && (
                    <div className="metric">
                      <span className="tiny-label">Pool master</span>
                      <strong>{data.hosts.find((h) => h.id === pool.master_host_id && !h.revoked)?.hostname || "Unknown"}</strong>
                    </div>
                  )}
                  <div className="metric context">
                    <span className="tiny-label">POOL MEMBERSHIP</span>
                    <p>
                      Local disks stay local.
                      <br />
                      Host access is always explicit.
                    </p>
                  </div>
                </div>
              )}
              {!host && !pool && (
                <section className="panel">
                  <div className="section-heading">
                    <h2>{site ? "Pools" : "Sites"}</h2>
                    <span className="muted">
                      {site
                        ? data.pools.filter((p) => p.site_id === site.id).length
                        : data.sites.length}{" "}
                      total
                    </span>
                  </div>
                  {(site
                    ? data.pools.filter((p) => p.site_id === site.id)
                    : data.sites
                  ).length ? (
                    <div className="scope-list">
                      {(site
                        ? data.pools.filter((p) => p.site_id === site.id)
                        : data.sites
                      ).map((item) => (
                        <a
                          key={item.id}
                          href={`#/${site ? "pools" : "sites"}/${item.id}`}
                        >
                          <span>{item.name}</span>
                          <span aria-hidden="true">→</span>
                        </a>
                      ))}
                    </div>
                  ) : (
                    <div className="empty">
                      <h3>
                        {site ? "Add your first host" : "Start with a site"}
                      </h3>
                      <p>
                        {site
                          ? "Each Nex Core host you add becomes the master of its own pool. Pools are created automatically; you do not create them by hand."
                          : "Organize your hosts by location. Create a site, then connect your first host."}
                      </p>
                      <button
                        disabled={busy}
                        onClick={() => open(site ? "enroll" : "site")}
                      >
                        {site ? "Add host" : "Create site"}
                      </button>
                    </div>
                  )}
                </section>
              )}
              {host ? (
                <><HostDetails host={host} master={pool?.master_host_id === host.id} tab={tab} />{tab === "Storage" && <HostISOs host={host} media={data.media} storage={data.storage} unavailable={Boolean(error)} />}</>
              ) : (
                <section className="panel">
                  <div className="section-heading">
                    <h2>Hosts</h2>
                    <label className="search">
                      <span className="sr-only">Search hosts</span>
                      <input
                        type="search"
                        placeholder="Search hosts…"
                        value={search}
                        onChange={(e) => setSearch(e.target.value)}
                      />
                    </label>
                  </div>
                  {hosts.length ? (
                    <div className="table-scroll">
                      <table>
                        <thead>
                          <tr>
                            <th>Host</th>
                            <th>Connection</th>
                            <th>Last observed health</th>
                            <th>CPU / memory</th>
                            <th>Last report</th>
                          </tr>
                        </thead>
                        <tbody>
                          {hosts.map((h) => (
                            <tr key={h.id}>
                              <td>
                                <a className="strong" href={`#/hosts/${h.id}`}>
                                  {h.hostname}
                                </a>
                                <small>
                                  {
                                    data.pools.find((p) => p.id === h.pool_id)
                                      ?.name
                                  }
                                </small>
                              </td>
                              <td>
                                <Badge value={h.connection} />
                              </td>
                              <td>
                                <Badge value={observedHealth(h)} />
                              </td>
                              <td>
                                {h.inventory
                                  ? `${h.inventory.cpu_count} CPUs / ${size(h.inventory.memory_bytes)}`
                                  : "Not observed"}
                              </td>
                              <td>{date(h.last_seen)}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  ) : (
                    <div className="empty">
                      <h3>
                        {search ? "No matching hosts" : "No hosts enrolled"}
                      </h3>
                      <p>
                        {search
                          ? "Try another hostname."
                          : "Select a site and use Add host to connect a Nex Core host."}
                      </p>
                    </div>
                  )}
                  <p className="panel-note">
                    A stale connection means reachability is unknown. It does
                    not mean virtual machines have stopped.
                  </p>
                </section>
              )}
              </div>
              {tab === "Activity" && <>
              <Activity
                entries={data.audit
                  .filter(
                    (a) =>
                      !kind ||
                      a.resource_id === site?.id ||
                      a.resource_id === pool?.id ||
                      scopedHosts.some(
                        (h) =>
                          a.resource_id === h.id ||
                          a.resource_id.startsWith(h.id + "/"),
                      ),
                  )
                  }
                data={data}
              />
              </>}
            </>
          )}
          </div>
          {!loading && kind !== "tasks" && kind !== "updates" && <details className="activity-dock" open>
            <summary>Recent tasks <span>{data.operations.filter((o) => scopedHosts.some((h) => h.id === o.host_id)).length}</span></summary>
            <Tasks operations={data.operations.filter((o) => scopedHosts.some((h) => h.id === o.host_id)).slice(0, 5)} hosts={data.hosts} recent />
          </details>}
        </main>
      </div>
      <footer className="statusbar"><span>{loading ? "Loading inventory…" : error ? "Controller request failed · inventory may be out of date" : "Observed state · refreshes every 10 seconds"}</span><span>{data.hosts.length} hosts · {data.hosts.filter((h) => h.connection === "fresh").length} fresh connections</span><a href="/source">Nex source code</a></footer>
    </div>
  );
}

function UpdatesPanel({
  update,
  note,
  phase,
  busy,
  onCheck,
  onApply,
}: {
  update: UpdateStatus | null;
  note: string;
  phase: "idle" | "checking" | "applying" | "done" | "error";
  busy: boolean;
  onCheck: () => void;
  onApply: () => void;
}) {
  const working = phase === "checking" || phase === "applying";
  const pct = typeof update?.progress === "number" ? update.progress : undefined;
  const label =
    phase === "checking" ? "Checking…"
      : update?.phase === "downloading" ? `Downloading… ${pct ?? 0}%`
        : update?.phase === "installing" ? "Installing…"
          : update?.phase === "restarting" ? "Restarting…"
            : phase === "applying" ? "Updating…"
              : note || "Done";
  return (
    <section className="panel">
      <dl className="details">
        <div>
          <dt>Installed</dt>
          <dd>{update?.installed_version || "—"}</dd>
        </div>
        <div>
          <dt>Available</dt>
          <dd>{!update ? "—" : update.update_available ? (update.available_version || "—") : "Up to date"}</dd>
        </div>
      </dl>
      {(working || phase === "done") && <p className="small" role="status">{label}</p>}
      {working && (
        <progress
          aria-label={phase === "checking" ? "Checking for updates" : "Applying update"}
          max={100}
          value={phase === "applying" && pct !== undefined ? pct : undefined}
        />
      )}
      {update?.error && <p className="warning" role="status">{update.error}</p>}
      {note && phase === "done" && <p className="notice" role="status">{note}</p>}
      <div className="actions">
        <button disabled={busy} onClick={onCheck}>Check</button>
        <button className="primary" disabled={busy || !update?.update_available} onClick={onApply}>Update</button>
      </div>
    </section>
  );
}
function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="metric">
      <span className="tiny-label">{label}</span>
      <strong>{value}</strong>
    </div>
  );
}
function Details({ children }: { children: ReactNode }) {
  return <dl className="details">{children}</dl>;
}
// consoleBlocker explains why a console cannot be opened now; an empty result means it can.
function consoleBlocker(vm: VM, host?: Host): string {
  if (!host || host.revoked || host.connection !== "fresh") return "The VM's host is not freshly connected. Its console cannot be opened until the host reports again.";
  if (!host.capabilities?.includes("console-v1")) return "The host agent does not support browser consoles. Upgrade Nex Core on this host.";
  if (vm.freshness !== "fresh") return "Current VM state is unknown; wait for a fresh host report before opening the console.";
  if (vm.state !== "running") return `The VM is observed ${vm.state || "unknown"}. Use Start above, wait for the next host report, then connect.`;
  return "";
}
function HostDetails({ host, master, tab }: { host: Host; master: boolean; tab: string }) {
  const inv = host.inventory;
  return (
    <div className="host-details" data-tab={tab}>
      <section className="panel host-overview">
        <div className="section-heading">
          <h2>Host overview</h2>
          <span><span className="badge">{master ? "Pool master" : "Member"}</span> <Badge value={host.connection} /></span>
        </div>
        {host.connection !== "fresh" && (
          <p className="warning">
            {host.revoked
              ? "Access revoked. Re-enroll this host to resume reporting."
              : "The host is not reporting fresh data. Last known observations are shown below; workload execution is unknown."}
          </p>
        )}
        <div hidden={tab !== "General"}>
        <Details>
          <dt>Host identity</dt>
          <dd className="mono">{host.id}</dd>
          <dt>Last connection</dt>
          <dd>{date(host.last_seen)}</dd>
          <dt>Inventory observed</dt>
          <dd>{date(inv?.observed_at)}</dd>
          {inv && (
            <>
              <dt>Processor</dt>
              <dd>
                {inv.cpu_model} · {inv.cpu_count} CPUs
              </dd>
              <dt>Memory</dt>
              <dd>{size(inv.memory_bytes)}</dd>
              <dt>Virtualization features</dt>
              <dd>{inv.virtualization?.join(", ") || "None reported"}</dd>
              <dt>Version</dt>
              <dd>{inv.os_version}</dd>
            </>
          )}
        </Details>
        {!inv && (
          <p className="empty">Waiting for the first inventory report.</p>
        )}
        </div>
      </section>
      {inv && (
        <>
          <section className="panel host-health" hidden={tab !== "General"}>
            <div className="section-heading">
              <h2>Health checks</h2>
              <span className="muted">Last observed results</span>
            </div>
            {inv.checks?.length ? (
              <div className="check-list">
                {inv.checks.map((c) => (
                  <div className="check" key={c.name}>
                    <div>
                      <strong>{c.name}</strong>
                      <p>{c.error || "No error reported"}</p>
                      <small>
                        Observed {date(c.observed_at)} · Last success{" "}
                        {date(c.last_success)}
                      </small>
                    </div>
                    <Badge value={checkHealth(c)} />
                  </div>
                ))}
              </div>
            ) : (
              <p className="empty">No health checks reported.</p>
            )}
          </section>
          <div className="two-column">
            <section className="panel host-network" hidden={tab !== "Networking"}>
              <div className="section-heading">
                <h2>Network interfaces</h2>
              </div>
              {inv.nics?.length ? (() => {
                const nics = inv.nics;
                const uplinks = nics.filter((n) => !n.kind || n.kind === "physical" || n.kind === "bond");
                const carried = (name: string) => [
                  ...nics.filter((b) => b.kind === "bridge" && nics.some((p) => p.name === name && p.master === b.name)).map((b) => `untagged → ${b.name}${b.addresses?.length ? ` (${b.addresses.join(", ")})` : ""}`),
                  ...nics.filter((v) => v.kind === "vlan" && v.parent === name).map((v) => `VLAN ${v.name.slice(name.length + 1)}${v.master ? ` → ${v.master}` : ""}`),
                ];
                return <>
                  {uplinks.map((n) => (
                    <div className="resource" key={n.name}>
                      <strong>{n.name}</strong>
                      <Badge value={n.state} />
                      <p>{carried(n.name).join(" · ") || (n.addresses?.length ? n.addresses.join(", ") : "Not used by any network")}</p>
                      <small className="mono">{n.mac}</small>
                    </div>
                  ))}
                  <details>
                    <summary>All interfaces ({nics.length}) · bridges, VLAN devices and VM ports</summary>
                    {nics.map((n) => (
                      <div className="resource" key={n.name}>
                        <strong>{n.name}</strong>
                        <Badge value={n.state} />
                        <p>{n.kind ? `${n.kind} · ` : ""}{n.master ? `port of ${n.master} · ` : ""}{n.parent ? `on ${n.parent} · ` : ""}{n.addresses?.join(", ") || "No addresses reported"}</p>
                        <small className="mono">{n.mac}</small>
                      </div>
                    ))}
                  </details>
                </>;
              })() : (
                <p className="empty">No interfaces reported.</p>
              )}
            </section>
            <section className="panel host-disks" hidden={tab !== "Storage"}>
              <div className="section-heading">
                <h2>Disks</h2>
              </div>
              {inv.disks?.length ? (
                inv.disks.map((d) => (
                  <div className="resource" key={d.name}>
                    <strong>{d.name}</strong>
                    <span>{size(d.size_bytes)}</span>
                  </div>
                ))
              ) : (
                <p className="empty">No disks reported.</p>
              )}
              <p className="panel-note">
                Observed device capacity. Pool membership does not make disks
                shared.
              </p>
            </section>
          </div>
        </>
      )}
      {!inv && tab !== "General" && <p className="empty">Waiting for the first inventory report.</p>}
    </div>
  );
}
function Tasks({
  operations,
  hosts,
  recent = false,
}: {
  operations: Operation[];
  hosts: Host[];
  recent?: boolean;
}) {
  return (
    <section className="panel">
      <div className="section-heading">
        <h2>{recent ? "Recent tasks" : "Tasks"}</h2>
        {recent && <a href="#/tasks">View all tasks →</a>}
      </div>
      {operations.length ? (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Action / target</th>
                <th>Status</th>
                <th>Actor</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              {operations.map((o) => (
                <tr key={o.id}>
                  <td>
                    <strong>
                      {o.kind === "refresh-inventory"
                        ? "Refresh inventory"
                        : o.kind}
                    </strong>
                    <small>
                      <a href={`#/hosts/${o.host_id}`}>
                        {hosts.find((h) => h.id === o.host_id)?.hostname ||
                          o.host_id}
                      </a>
                    </small>
                    {o.error && <p className="task-error">{o.error}</p>}
                    <small className="mono">{o.id}</small>
                  </td>
                  <td>
                    <Badge value={o.status} />
                    {o.status === "dispatched" && (
                      <small>Awaiting agent result</small>
                    )}
                  </td>
                  <td>{o.actor}</td>
                  <td>{date(o.updated_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="empty">
          No tasks yet. Host inventory refresh requests will appear here.
        </p>
      )}
      <p className="panel-note">
        Pending results survive reconnects. A delayed response is not proof that
        an operation failed.
      </p>
    </section>
  );
}
function Activity({
  entries,
  data,
  recent = false,
}: {
  entries: Audit[];
  data: Data;
  recent?: boolean;
}) {
  return (
    <section className="panel">
      <div className="section-heading">
        <h2>{recent ? "Recent activity" : "Activity and health history"}</h2>
        {recent && <a href="#/tasks">View all activity →</a>}
      </div>
      {entries.length ? (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Action / target</th>
                <th>Actor</th>
                <th>Recorded</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => {
                const [hostID, check] = entry.resource_id.split("/");
                const target =
                  data.hosts.find((h) => h.id === hostID)?.hostname ||
                  data.pools.find((p) => p.id === entry.resource_id)?.name ||
                  data.sites.find((s) => s.id === entry.resource_id)?.name ||
                  entry.resource_id;
                return (
                  <tr key={entry.id}>
                    <td>
                      <strong>
                        {entry.action
                          .replaceAll(".", " · ")
                          .replaceAll("-", " ")}
                      </strong>
                      <small>
                        {target}
                        {check ? ` / ${check}` : ""}
                      </small>
                      {entry.detail && <p className="help">{entry.detail}</p>}
                    </td>
                    <td>{entry.actor}</td>
                    <td>{date(entry.created_at)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="empty">No activity recorded in this scope.</p>
      )}
      <p className="panel-note">
        Recorded changes and debounced health transitions. Current check results
        are shown on each host.
      </p>
    </section>
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
