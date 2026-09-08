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

type Site = { id: string; name: string };
type Pool = Site & { site_id: string };
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
  nics: { name: string; mac: string; addresses: string[]; state: string }[];
  disks: { name: string; size_bytes: number }[];
  checks: Check[];
};
type Host = {
  id: string;
  pool_id: string;
  hostname: string;
  revoked: boolean;
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
type Data = {
  sites: Site[];
  pools: Pool[];
  hosts: Host[];
  operations: Operation[];
  audit: Audit[];
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
function App() {
  const [session, setSession] = useState<Session | null>();
  const [data, setData] = useState<Data>(empty);
  const [route, setRoute] = useState(location.hash.slice(1) || "/");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [search, setSearch] = useState("");
  const [form, setForm] = useState<
    "site" | "pool" | "enroll" | "revoke" | null
  >(null);
  const [token, setToken] = useState<{
    token: string;
    expires_at: string;
  } | null>(null);
  const requestID = useRef<string | null>(null);
  const generation = useRef(0);
  const reloadOrdinal = useRef(0);
  const [kind, id] = route.split("/").filter(Boolean);
  const host = data.hosts.find((h) => kind === "hosts" && h.id === id);
  const pool = data.pools.find(
    (p) => p.id === (host?.pool_id || (kind === "pools" ? id : "")),
  );
  const site = data.sites.find(
    (s) => s.id === (pool?.site_id || (kind === "sites" ? id : "")),
  );
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
  ): Promise<T> {
    const requestGeneration = generation.current;
    const response = await fetch(`/api/v1/${path}`, {
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
  async function reload() {
    const current = generation.current;
    const ordinal = ++reloadOrdinal.current;
    const isCurrent = () =>
      current === generation.current && ordinal === reloadOrdinal.current;
    try {
      const [sites, pools, hosts, operations, audit] = await Promise.all([
        api<Site[]>("sites"),
        api<Pool[]>("pools"),
        api<Host[]>("hosts"),
        api<Operation[]>("operations"),
        api<Audit[]>("audit"),
      ]);
      if (isCurrent()) {
        setData({
          sites: sites || [],
          pools: pools || [],
          hosts: hosts || [],
          operations: operations || [],
          audit: audit || [],
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
    const change = () => {
      setRoute(location.hash.slice(1) || "/");
      setSearch("");
      setForm(null);
      setToken(null);
      setNotice("");
      requestID.current = null;
    };
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  useEffect(() => {
    if (!session) return;
    setLoading(true);
    void reload();
    const timer = setInterval(() => void reload(), 10000);
    return () => clearInterval(timer);
  }, [session]);
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
  function open(next: typeof form) {
    if (busy) return;
    setForm(next);
    setToken(null);
    setError("");
  }
  const submitName = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const name = String(new FormData(e.currentTarget).get("name")).trim();
    await action(async () => {
      const result = await api<Site>(
        form === "site" ? "sites" : "pools",
        "POST",
        form === "site" ? { name } : { name, site_id: site?.id },
      );
      await reload();
      setForm(null);
      location.hash = `/${form === "site" ? "sites" : "pools"}/${result.id}`;
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
          <p className="eyebrow">YOUR INFRASTRUCTURE, CONNECTED</p>
          <h1>Welcome back.</h1>
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
  const invalid = Boolean(
    kind &&
    kind !== "tasks" &&
    (!id ||
      !["sites", "pools", "hosts"].includes(kind) ||
      (kind === "sites" && !site) ||
      (kind === "pools" && !pool) ||
      (kind === "hosts" && !host)),
  );
  return (
    <>
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
          <Brand />
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
      <div className="shell">
        <aside>
          <div className="nav-title">WORKSPACE</div>
          <nav aria-label="Infrastructure">
            <a className={!kind ? "selected" : ""} href="#/">
              Overview <span>{data.hosts.length}</span>
            </a>
            {data.sites.map((s) => (
              <div key={s.id}>
                <a
                  className={kind === "sites" && id === s.id ? "selected" : ""}
                  href={`#/sites/${s.id}`}
                >
                  <span aria-hidden="true">◈</span> {s.name}
                </a>
                {data.pools
                  .filter((p) => p.site_id === s.id)
                  .map((p) => (
                    <div key={p.id} className="tree">
                      <a
                        className={
                          kind === "pools" && id === p.id ? "selected" : ""
                        }
                        href={`#/pools/${p.id}`}
                      >
                        {p.name}
                      </a>
                      {data.hosts
                        .filter((h) => h.pool_id === p.id)
                        .map((h) => (
                          <a
                            className={`host-link ${host?.id === h.id ? "selected" : ""}`}
                            key={h.id}
                            href={`#/hosts/${h.id}`}
                          >
                            <span aria-hidden="true">▤</span> {h.hostname}
                          </a>
                        ))}
                    </div>
                  ))}
              </div>
            ))}
            <a className={kind === "tasks" ? "selected" : ""} href="#/tasks">
              Task history
            </a>
          </nav>
          <div className="sidebar-foot">
            <span className="tiny-label">NEX CONTROLLER</span>
            <p>
              One workspace.
              <br />
              Every host in view.
            </p>
            <a href="/api/docs/">API reference ↗</a>
            <a href="/source">Source code ↗</a>
          </div>
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
            {host && <> / {host.hostname}</>}
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
                        : "INFRASTRUCTURE"}
              </p>
              <h1>
                {host?.hostname ||
                  pool?.name ||
                  site?.name ||
                  (kind === "tasks" ? "Task history" : "Your workspace")}
              </h1>
              <p>
                {host
                  ? "Observed resources and management connectivity."
                  : pool
                    ? "Host health and enrollment for this pool."
                    : site
                      ? "Pools and hosts at this site."
                      : kind === "tasks"
                        ? "Durable requests, progress and outcomes."
                        : "Bring your infrastructure together, one pool at a time."}
              </p>
            </div>
            <div className="actions">
              {!kind && (
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() => open("site")}
                >
                  Create site
                </button>
              )}
              {site && !pool && (
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() => open("pool")}
                >
                  Create pool
                </button>
              )}
              {pool && !host && (
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() => open("enroll")}
                >
                  Add host
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
                    Refresh inventory
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
          </div>
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
                  : form === "pool"
                    ? "Create pool"
                    : form === "revoke"
                      ? "Confirm revoke"
                      : "Add host"
              }
            >
              <div className="section-heading">
                <h2>
                  {form === "site"
                    ? "Create a site"
                    : form === "pool"
                      ? `Create a pool in ${site?.name}`
                      : form === "revoke"
                        ? `Revoke access for ${host?.hostname}?`
                        : host
                          ? `Re-enroll ${host.hostname}`
                          : `Add host to ${pool?.name}`}
                </h2>
                <button
                  onClick={() => {
                    setForm(null);
                    setToken(null);
                  }}
                  disabled={busy}
                >
                  Close
                </button>
              </div>
              {(form === "site" || form === "pool") && (
                <form onSubmit={submitName}>
                  <label>
                    {form === "site" ? "Site name" : "Pool name"}
                    <input
                      name="name"
                      required
                      maxLength={100}
                      autoFocus
                      placeholder={
                        form === "site" ? "e.g. Sydney" : "e.g. Production"
                      }
                    />
                  </label>
                  <p className="help">
                    {form === "site"
                      ? "A site groups pools by location or purpose."
                      : "A pool groups hosts. Enrollment does not make local storage shared or enable high availability."}
                  </p>
                  <button className="primary" disabled={busy}>
                    {busy
                      ? "Creating…"
                      : form === "site"
                        ? "Create site"
                        : "Create pool"}
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
                    <strong>{pool?.name}</strong>. Verify the controller address
                    and trusted CA on the host before continuing.
                  </p>
                  {!token ? (
                    <button
                      className="primary"
                      disabled={busy}
                      onClick={() =>
                        void action(async () =>
                          setToken(
                            await api("enrollment-tokens", "POST", {
                              pool_id: pool!.id,
                              ...(host ? { host_id: host.id } : {}),
                            }),
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
                        Expires {date(token.expires_at)}. Store securely; this
                        token is shown only here and is removed when you close
                        this panel.
                      </p>
                      <EnrollmentInstructions host={host} />
                      <p className="help">
                        The host will appear here after enrollment. Keep this
                        page open to see its first report.
                      </p>
                    </>
                  )}
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
                  : "This link does not match a site, pool or host in your workspace."}
              </p>
              <a href="#/">Return to overview</a>
            </section>
          ) : kind === "tasks" ? (
            <>
              <Tasks operations={data.operations} hosts={data.hosts} />
              <Activity entries={data.audit} data={data} />
            </>
          ) : (
            <>
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
                        {site ? "Create your first pool" : "Start with a site"}
                      </h3>
                      <p>
                        {site
                          ? "Group your hosts in a pool before enrolling them."
                          : "Organize your hosts by location. Create a site, then a pool, then connect your first host."}
                      </p>
                      <button
                        disabled={busy}
                        onClick={() => open(site ? "pool" : "site")}
                      >
                        {site ? "Create pool" : "Create site"}
                      </button>
                    </div>
                  )}
                </section>
              )}
              {host ? (
                <HostDetails host={host} />
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
                        {search
                          ? "No matching hosts"
                          : pool
                            ? "Ready for your first host"
                            : "No hosts enrolled"}
                      </h3>
                      <p>
                        {search
                          ? "Try another hostname."
                          : pool
                            ? "Create a short-lived enrollment token and connect a Nex Core agent."
                            : "Create a pool and use Add host to connect your infrastructure."}
                      </p>
                      {pool && !search && (
                        <button disabled={busy} onClick={() => open("enroll")}>
                          Add host
                        </button>
                      )}
                    </div>
                  )}
                  <p className="panel-note">
                    A stale connection means reachability is unknown. It does
                    not mean virtual machines have stopped.
                  </p>
                </section>
              )}
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
                  .slice(0, 5)}
                data={data}
                recent
              />
              <Tasks
                operations={data.operations
                  .filter((o) => scopedHosts.some((h) => h.id === o.host_id))
                  .slice(0, 5)}
                hosts={data.hosts}
                recent
              />
            </>
          )}
          <footer>
            <span>Observed state · refreshes every 10 seconds</span>
            <a href="/source">Nex source code</a>
          </footer>
        </main>
      </div>
    </>
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
function HostDetails({ host }: { host: Host }) {
  const inv = host.inventory;
  return (
    <>
      <section className="panel">
        <div className="section-heading">
          <h2>Host overview</h2>
          <Badge value={host.connection} />
        </div>
        {host.connection !== "fresh" && (
          <p className="warning">
            {host.revoked
              ? "Access revoked. Re-enroll this host to resume reporting."
              : "The host is not reporting fresh data. Last known observations are shown below; workload execution is unknown."}
          </p>
        )}
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
              <dt>Operating system</dt>
              <dd>{inv.os_version}</dd>
              <dt>Agent version</dt>
              <dd>{inv.agent_version}</dd>
            </>
          )}
        </Details>
        {!inv && (
          <p className="empty">Waiting for the first inventory report.</p>
        )}
      </section>
      {inv && (
        <>
          <section className="panel">
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
            <section className="panel">
              <div className="section-heading">
                <h2>Network interfaces</h2>
              </div>
              {inv.nics?.length ? (
                inv.nics.map((n) => (
                  <div className="resource" key={n.name}>
                    <strong>{n.name}</strong>
                    <Badge value={n.state} />
                    <p>{n.addresses?.join(", ") || "No addresses reported"}</p>
                    <small className="mono">{n.mac}</small>
                  </div>
                ))
              ) : (
                <p className="empty">No interfaces reported.</p>
              )}
            </section>
            <section className="panel">
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
    </>
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
