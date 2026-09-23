// SPDX-License-Identifier: AGPL-3.0-only
import { Component, createContext, useContext, useEffect, useId, useRef, type ReactNode } from "react";
import { auditLabel, relativeTime, statusLabel } from "./labels";
import type { Audit, Data } from "./types";
import { Icon, type IconKind } from "./icons";

export type Tone = "ok" | "warn" | "bad" | "info" | "neutral";
const tones: [Tone, string[]][] = [
  ["ok", ["fresh", "healthy", "ok", "succeeded", "up", "verified", "passed", "current", "available", "confirmed", "connected", "ready", "in_sync", "running", "online", "met"]],
  ["warn", ["stale", "degraded", "timeout", "dispatched", "warning", "pending", "awaiting_fencing", "needs_attention", "unverified", "uncertain", "behind", "disconnected", "unresponsive", "staged", "applied", "unmet", "arming"]],
  ["bad", ["failed", "error", "revoked", "down", "blocked", "faulted", "rejected", "unavailable", "offline", "critical"]],
  ["info", ["queued", "uploading", "master", "pool master", "provisioned", "connecting"]],
];
export function badgeTone(value: string): Tone {
  const key = value.trim().toLowerCase();
  return tones.find(([, values]) => values.includes(key))?.[0] ?? "neutral";
}
// The square dot-and-text badge. The dot is drawn by CSS; the text is always present.
export function Badge({ value, label }: { value: string; label?: string }) {
  return <span className={`badge ${badgeTone(value)}`}>{label ?? statusLabel(value)}</span>;
}
export function Banner({ tone, role, children }: { tone: "info" | "warn" | "bad"; role?: "alert" | "status"; children: ReactNode }) {
  return <p className={`banner ${tone}`} role={role}>{children}</p>;
}

export const date = (value?: string | null) =>
  value && !value.startsWith("0001-")
    ? new Date(value).toLocaleString()
    : "Not observed";
export const size = (value: number) =>
  `${(value / 1024 ** 3).toLocaleString(undefined, { maximumFractionDigits: 1 })} GiB`;

// The inventory load error and its Retry. The page shows it above the screen, and an open panel
// shows it in its body instead, since the panel covers the page's alert.
type LoadErrorState = { error: string; busy: boolean; onRetry: () => void };
export const LoadError = createContext<LoadErrorState>({ error: "", busy: false, onRetry() {} });
export function LoadErrorAlert({ error, busy, onRetry }: LoadErrorState) {
  return <div className="alert" role="alert">{error} <button type="button" className="sm" disabled={busy} onClick={onRetry}>Retry</button></div>;
}

// One screen that cannot render a report it was given says so, instead of blanking the whole
// console; the shell keys it by route, so moving to another screen tries again.
export class ScreenBoundary extends Component<{ children: ReactNode }, { error: string }> {
  state = { error: "" };
  static getDerivedStateFromError(e: unknown) { return { error: e instanceof Error ? e.message : String(e) }; }
  render() {
    return this.state.error ? <section className="panel empty" role="alert"><h2>This screen could not be shown</h2>
      <p>Nex Controller received a report it could not display: {this.state.error}. Other screens still work; reload the page to try again.</p></section> : this.props.children;
  }
}

// A current condition that needs an operator, naming what it is about and linking to it.
export type Attention = { tone: Tone; title: string; detail: string; href: string };
export function AttentionList({ items }: { items: Attention[] }) {
  return <ul className="ov-attention">{items.map((a, i) => <li key={i}><a href={a.href}>
    <span className={`ov-icon ${a.tone}`}><Icon kind={a.tone === "bad" ? "failed" : a.tone === "info" ? "info" : "warning"} /></span>
    <span>{a.title}<small>{a.detail}</small></span>
    <span className="ov-chev"><Icon kind="chevron" size={14} /></span>
  </a></li>)}</ul>;
}

// Menu is a disclosure that closes on Escape (focus back on its summary), on a choice and when
// focus leaves it.
export function Menu({ label, summaryLabel, className = "", align, children }: { label: ReactNode; summaryLabel?: string; className?: string; align?: "start" | "end"; children: ReactNode }) {
  const ref = useRef<HTMLDetailsElement>(null);
  return <details className={`menu ${className}${align === "end" ? " menu-end" : ""}`} ref={ref}
    onBlur={(e) => { if (!e.currentTarget.contains(e.relatedTarget)) e.currentTarget.open = false; }}
    onKeyDown={(e) => { if (e.key === "Escape" && e.currentTarget.open) { e.preventDefault(); e.currentTarget.open = false; e.currentTarget.querySelector("summary")?.focus(); } }}>
    <summary aria-label={summaryLabel}>{label}</summary>
    <div className="menu-items" onClick={() => { if (ref.current) ref.current.open = false; }}>{children}</div>
  </details>;
}

// Panel is the slide-over that opens beside a list, so the list stays in view and usable.
// Escape closes it unless a menu, a dialog or console focus mode has taken the key, and
// closing returns focus to whatever opened it (normally the row).
// Its tabs are one tab stop: the arrow keys, Home and End move between them and select.
export function Panel({ icon, title, badge, sub, actions, tabs, tab, onTab, wide, onClose, children }: {
  icon: IconKind; title: ReactNode; badge?: ReactNode; sub?: ReactNode; actions?: ReactNode;
  tabs?: readonly string[]; tab?: string; onTab?: (tab: string) => void; wide?: boolean; onClose: () => void; children: ReactNode;
}) {
  const panel = useRef<HTMLElement>(null);
  const load = useContext(LoadError);
  const id = useId();
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    panel.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented && !document.querySelector("dialog[open]") && !document.body.classList.contains("console-focus")) close.current();
    };
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("keydown", onKey); opener?.focus(); };
  }, []);
  return <section ref={panel} tabIndex={-1} className={`slideover${wide ? " wide" : ""}`} aria-label={typeof title === "string" ? title : undefined}>
    <header>
      <h2><Icon kind={icon} size={18} />{title}{badge}</h2>
      <button type="button" className="icon-button" aria-label="Close" onClick={onClose}><Icon kind="close" size={18} /></button>
    </header>
    {sub && <div className="sub">{sub}</div>}
    {actions && <div className="quick">{actions}</div>}
    {tabs && <div className="tabs" role="tablist" onKeyDown={e => {
      const at = tabs.indexOf(tab || ""), last = tabs.length - 1;
      const next = { ArrowRight: at === last ? 0 : at + 1, ArrowLeft: at <= 0 ? last : at - 1, Home: 0, End: last }[e.key];
      if (next === undefined) return;
      e.preventDefault();
      onTab?.(tabs[next]);
      (e.currentTarget.children[next] as HTMLElement).focus();
    }}>
      {tabs.map((name, i) => <button key={name} id={`${id}-tab-${i}`} type="button" role="tab" aria-selected={name === tab} aria-controls={`${id}-body`} tabIndex={name === tab ? 0 : -1} onClick={() => onTab?.(name)}>{name}</button>)}
    </div>}
    <div className="slideover-body" id={`${id}-body`} role={tabs ? "tabpanel" : undefined} aria-labelledby={tabs ? `${id}-tab-${tabs.indexOf(tab || "")}` : undefined}>{load.error && <LoadErrorAlert {...load} />}{children}</div>
  </section>;
}

// The panel for a VM or host link that the loaded inventory does not hold. When the inventory
// failed to load, the panel's own load error above this offers Retry.
export function MissingPanel({ icon, noun, unavailable, loading, onClose }: {
  icon: IconKind; noun: "VM" | "Host"; unavailable: boolean; loading: boolean; onClose: () => void;
}) {
  const name = noun === "VM" ? noun : noun.toLowerCase();
  return <Panel icon={icon} title={`${noun} ${unavailable ? "unavailable" : "not found"}`} onClose={onClose}>
    <p className="empty">{unavailable ? `This ${name} cannot be shown until the inventory loads.` : loading ? "Loading…" : `This link does not match a ${name} known to Nex Controller.`}</p>
  </Panel>;
}

// Section is a settings-style block: icon, title, optional count and header actions, then content.
export function Section({ icon, title, count, actions, children }: { icon: IconKind; title: string; count?: number; actions?: ReactNode; children: ReactNode }) {
  const id = useId();
  return <section className="section" aria-labelledby={id}>
    <h2><span className="section-title"><Icon kind={icon} size={18} /><span id={id}>{title}</span>{count !== undefined && <span className="count">{count}</span>}</span>{actions && <span className="section-actions">{actions}</span>}</h2>
    {children}
  </section>;
}

// About keeps a section's longer explanation one click away instead of under every table.
export function About({ title, children }: { title: string; children: ReactNode }) {
  return <details className="about"><summary><Icon kind="info" />{title}</summary><div className="note">{children}</div></details>;
}

// HostAccess is one pool host's access to a repository or network, as its host reports it.
// "unknown" is drawn hollow: no current report is never shown as access.
export type HostAccess = { host: { id: string; hostname: string }; tone: Tone | "unknown"; label: string; detail: string };
// AccessLights draws one light per pool host, named by hostname, with the state in its tooltip
// and accessible name, and the states as words underneath so colour never stands alone.
export function AccessLights({ items }: { items: HostAccess[] }) {
  if (!items.length) return <span className="muted">No hosts</span>;
  const counts = new Map<string, number>();
  for (const a of items) counts.set(a.label, (counts.get(a.label) || 0) + 1);
  return <div className="access-lights">
    {items.map(a => { const text = `${a.host.hostname}: ${a.label}${a.detail ? `. ${a.detail}` : ""}`;
      return <a key={a.host.id} href={`#/hosts/${a.host.id}`} title={text} aria-label={text}><span className={`dot ${a.tone}`} aria-hidden="true" />{a.host.hostname}</a>; })}
    <small>{[...counts].map(([label, n]) => `${n} ${label.toLowerCase()}`).join(" · ")}</small>
  </div>;
}

// MiniBar is a table cell's utilisation: a 60 px bar plus the percentage; unknown shows a dash.
export function MiniBar({ percent }: { percent?: number | null }) {
  if (percent == null) return <span className="muted">—</span>;
  // VM CPU can pass 100 % on several vCPUs; only the bar is capped.
  const value = Math.round(percent);
  return <span className="minibar"><span aria-hidden="true"><i className={value > 80 ? "hot" : ""} style={{ width: `${Math.min(100, Math.max(0, value))}%` }} /></span>{value}%</span>;
}

// Meter is a full-width capacity bar for cards and tiles.
export function Meter({ percent, label, series = 1 }: { percent: number; label: string; series?: 1 | 2 | 3 | 4 }) {
  const value = Math.min(100, Math.max(0, percent));
  return <div className="meter" role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(value)}><i style={{ width: `${value}%`, background: `var(--s${series})` }} /></div>;
}

// Sparkline draws values in 0–100 with a soft area fill and an end dot; it is decorative, so the
// caller shows the current value as text next to it.
export function Sparkline({ values, series = 1, width = 84, height = 28, title }: { values: number[]; series?: 1 | 2 | 3 | 4; width?: number; height?: number; title?: string }) {
  if (values.length < 2) return null;
  const colour = `var(--s${series})`;
  const x = (i: number) => (i / (values.length - 1)) * width;
  const y = (v: number) => height - 2 - (Math.min(100, Math.max(0, v)) / 100) * (height - 4);
  const line = values.map((v, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join("");
  return <svg className="sparkline" viewBox={`0 0 ${width} ${height}`} width={width} height={height} preserveAspectRatio="none" {...title ? { role: "img", "aria-label": title } : { "aria-hidden": true }}>
    {title && <title>{title}</title>}
    <path d={`${line}L${width},${height}L0,${height}Z`} fill={colour} opacity={0.15} />
    <path d={line} fill="none" stroke={colour} strokeWidth={1.5} />
    <circle cx={x(values.length - 1)} cy={y(values[values.length - 1])} r={2.5} fill={colour} />
  </svg>;
}

// Activity is the audit history: recorded changes and debounced health transitions, for the
// Events screen and each resource's Activity tab.
export function auditTarget(entry: Audit, data: Data): { target: string; check?: string } {
  const [hostID, check] = entry.resource_id.split("/");
  const target =
    data.hosts.find((h) => h.id === hostID)?.hostname ||
    data.vms.find((v) => v.id === entry.resource_id)?.name ||
    data.pools.find((p) => p.id === entry.resource_id)?.name ||
    data.sites.find((s) => s.id === entry.resource_id)?.name ||
    entry.resource_id;
  return { target, check };
}
export function Activity({ entries, data }: { entries: Audit[]; data: Data }) {
  return (
    <section className="panel">
      <div className="section-heading">
        <h2>Activity and health history</h2>
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
                const { target, check } = auditTarget(entry, data);
                return (
                  <tr key={entry.id}>
                    <td>
                      <strong>{auditLabel(entry.action)}</strong>
                      <small>
                        {target}
                        {check ? ` / ${check}` : ""}
                      </small>
                      {entry.detail && <p className="help">{entry.detail}</p>}
                    </td>
                    <td>{entry.actor}</td>
                    <td className="muted"><time dateTime={entry.created_at} title={date(entry.created_at)}>{relativeTime(entry.created_at)}</time></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="empty">No activity recorded in this scope.</p>
      )}
    </section>
  );
}
