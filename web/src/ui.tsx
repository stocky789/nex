// SPDX-License-Identifier: AGPL-3.0-only
import { Component, createContext, useContext, useEffect, useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import { auditLabel, relativeTime, statusLabel } from "./labels";
import type { Audit, Data } from "./types";
import { Icon, type IconKind } from "./icons";

export type Tone = "ok" | "warn" | "bad" | "info" | "neutral";
const tones: [Tone, string[]][] = [
  ["ok", ["fresh", "healthy", "ok", "succeeded", "up", "verified", "passed", "current", "available", "confirmed", "connected", "ready", "in_sync", "running", "online", "met", "enabled"]],
  ["warn", ["stale", "degraded", "timeout", "dispatched", "warning", "pending", "awaiting_fencing", "needs_attention", "unverified", "uncertain", "behind", "disconnected", "unresponsive", "staged", "applied", "unmet", "arming", "paused"]],
  ["bad", ["failed", "error", "revoked", "down", "blocked", "faulted", "rejected", "unavailable", "offline", "critical"]],
  ["info", ["queued", "uploading", "master", "pool master", "provisioned", "connecting"]],
];
export function badgeTone(value: string): Tone {
  const key = value.trim().toLowerCase();
  return tones.find(([, values]) => values.includes(key))?.[0] ?? "neutral";
}
// The square dot-and-text badge. The dot is drawn by CSS; the text is always present.
export function Badge({ value, label, tone }: { value: string; label?: string; tone?: Tone }) {
  return <span className={`badge ${tone ?? badgeTone(value)}`}>{label ?? statusLabel(value)}</span>;
}
export function Banner({ tone, role, children }: { tone: "info" | "warn" | "bad"; role?: "alert" | "status"; children: ReactNode }) {
  return <p className={`banner ${tone}`} role={role}>{children}</p>;
}

export const date = (value?: string | null) =>
  value && !value.startsWith("0001-")
    ? new Date(value).toLocaleString()
    : "Not observed";
export const size = (value: number) => {
  const [unit, name]: [number, string] = value > 0 && value < 1024 ** 2 ? [1024, "KiB"] : value > 0 && value < 1024 ** 3 ? [1024 ** 2, "MiB"] : [1024 ** 3, "GiB"];
  return `${(value / unit).toLocaleString(undefined, { maximumFractionDigits: 1 })} ${name}`;
};

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

// Slide-over width is a per-browser convenience (localStorage), like Overview tile collapse.
// The rail is 68 px; leave it visible. Defaults stay in CSS when nothing is stored.
const slideoverWidthKey = "nex.slideover.width";
export const slideoverMinWidth = 360;
export function clampSlideoverWidth(px: number, viewport = typeof window !== "undefined" ? window.innerWidth : 1280): number {
  const max = Math.max(slideoverMinWidth, viewport - 68);
  return Math.min(max, Math.max(slideoverMinWidth, Math.round(px)));
}
export function loadSlideoverWidth(store?: Pick<Storage, "getItem">): number | null {
  try {
    const raw = (store ?? localStorage).getItem(slideoverWidthKey);
    if (raw == null || raw === "") return null;
    const n = Number(raw);
    return Number.isFinite(n) ? clampSlideoverWidth(n) : null;
  } catch { return null; }
}
export function saveSlideoverWidth(px: number | null, store?: Pick<Storage, "setItem" | "removeItem">) {
  try {
    const s = store ?? localStorage;
    if (px == null) s.removeItem(slideoverWidthKey);
    else s.setItem(slideoverWidthKey, String(clampSlideoverWidth(px)));
  } catch { /* blocked or full store: keep the in-memory width for this session */ }
}

// Panel is the slide-over that opens beside a list, so the list stays in view and usable.
// Escape closes it unless a menu, a dialog or console focus mode has taken the key, and
// closing returns focus to whatever opened it (normally the row).
// Its tabs are one tab stop: the arrow keys, Home and End move between them and select.
// Drag the left edge (or focus it and use the arrows) to widen or narrow; double-click resets.
export function Panel({ icon, title, badge, sub, actions, tabs, tab, onTab, wide, onClose, children }: {
  icon: IconKind; title: ReactNode; badge?: ReactNode; sub?: ReactNode; actions?: ReactNode;
  tabs?: readonly string[]; tab?: string; onTab?: (tab: string) => void; wide?: boolean; onClose: () => void; children: ReactNode;
}) {
  const panel = useRef<HTMLElement>(null);
  const load = useContext(LoadError);
  const id = useId();
  const close = useRef(onClose);
  close.current = onClose;
  const [width, setWidth] = useState<number | null>(() => loadSlideoverWidth());
  const widthRef = useRef(width);
  widthRef.current = width;
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    panel.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented && !document.querySelector("dialog[open]") && !document.body.classList.contains("console-focus")) close.current();
    };
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("keydown", onKey); opener?.focus(); };
  }, []);
  useEffect(() => {
    const onResize = () => setWidth(w => w == null ? null : clampSlideoverWidth(w));
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  const applyWidth = (next: number | null) => {
    const value = next == null ? null : clampSlideoverWidth(next);
    setWidth(value);
    saveSlideoverWidth(value);
    const el = panel.current;
    if (!el) return;
    if (value == null) el.style.removeProperty("--slideover-width");
    else el.style.setProperty("--slideover-width", `${value}px`);
  };
  // Drag writes the CSS variable on the element (rAF-coalesced) so the edge tracks the pointer
  // without a React render per move; width eases only when not dragging (keyboard, reset, wide).
  const onResizePointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const el = panel.current;
    if (!el) return;
    const target = e.currentTarget;
    target.setPointerCapture(e.pointerId);
    document.body.classList.add("slideover-resizing");
    let latest = widthRef.current ?? el.getBoundingClientRect().width;
    let raf = 0;
    const paint = () => {
      raf = 0;
      el.style.setProperty("--slideover-width", `${latest}px`);
      target.setAttribute("aria-valuenow", String(latest));
    };
    const onMove = (ev: PointerEvent) => {
      latest = clampSlideoverWidth(window.innerWidth - ev.clientX);
      if (!raf) raf = requestAnimationFrame(paint);
    };
    const onUp = (ev: PointerEvent) => {
      if (raf) cancelAnimationFrame(raf);
      paint();
      target.releasePointerCapture(ev.pointerId);
      document.body.classList.remove("slideover-resizing");
      target.removeEventListener("pointermove", onMove);
      target.removeEventListener("pointerup", onUp);
      target.removeEventListener("pointercancel", onUp);
      applyWidth(latest);
    };
    target.addEventListener("pointermove", onMove);
    target.addEventListener("pointerup", onUp);
    target.addEventListener("pointercancel", onUp);
  };
  const onResizeKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? 40 : 16;
    const current = widthRef.current ?? panel.current?.getBoundingClientRect().width ?? 420;
    if (e.key === "ArrowLeft") { e.preventDefault(); applyWidth(current + step); }
    else if (e.key === "ArrowRight") { e.preventDefault(); applyWidth(current - step); }
    else if (e.key === "Home") { e.preventDefault(); applyWidth(null); }
  };
  const style = width != null ? { ["--slideover-width" as string]: `${width}px` } : undefined;
  const max = typeof window !== "undefined" ? Math.max(slideoverMinWidth, window.innerWidth - 68) : 820;
  return <section ref={panel} tabIndex={-1} className={`slideover${wide ? " wide" : ""}`} style={style} aria-label={typeof title === "string" ? title : undefined}>
    <div
      className="slideover-resize"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize panel"
      aria-valuemin={slideoverMinWidth}
      aria-valuemax={max}
      aria-valuenow={width ?? undefined}
      title="Drag to resize. Double-click to reset."
      tabIndex={0}
      onPointerDown={onResizePointerDown}
      onDoubleClick={() => applyWidth(null)}
      onKeyDown={onResizeKeyDown}
    />
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

// A monotone cubic through points (Fritsch–Carlson / d3 curveMonotoneX): no overshoot past
// segment ends, and flats stay flat. Shared by Overview charts and decorative sparklines.
export function monotone(p: [number, number][]): string {
  const d = p.slice(1).map(([x, y], k) => (y - p[k][1]) / (x - p[k][0]));
  const m = p.map((_, k) => {
    if (k === 0 || k === p.length - 1) return d[k === 0 ? 0 : k - 1] ?? 0;
    const h0 = p[k][0] - p[k - 1][0], h1 = p[k + 1][0] - p[k][0];
    const mean = (d[k - 1] * h1 + d[k] * h0) / (h0 + h1);
    return (Math.sign(d[k - 1]) + Math.sign(d[k])) * Math.min(Math.abs(d[k - 1]), Math.abs(d[k]), Math.abs(mean) / 2);
  });
  const f = (v: number) => v.toFixed(2);
  return `M${f(p[0][0])},${f(p[0][1])}` + p.slice(1).map(([x, y], k) => {
    const [x0, y0] = p[k], h = (x - x0) / 3;
    return `C${f(x0 + h)},${f(y0 + m[k] * h)} ${f(x - h)},${f(y - m[k + 1] * h)} ${f(x)},${f(y)}`;
  }).join("");
}

// Sparkline draws values in 0–100 with a soft area fill and an end dot; it is decorative, so the
// caller shows the current value as text next to it. Strokes use non-scaling width so a full-width
// stretch keeps a thin line instead of a fat ribbon.
export function Sparkline({ values, series = 1, width = 84, height = 28, title }: { values: number[]; series?: 1 | 2 | 3 | 4; width?: number; height?: number; title?: string }) {
  if (values.length < 2) return null;
  const colour = `var(--s${series})`;
  const x = (i: number) => (i / (values.length - 1)) * width;
  const y = (v: number) => height - 2 - (Math.min(100, Math.max(0, v)) / 100) * (height - 4);
  const line = monotone(values.map((v, i) => [x(i), y(v)]));
  return <svg className="sparkline" viewBox={`0 0 ${width} ${height}`} width={width} height={height} preserveAspectRatio="none" {...title ? { role: "img", "aria-label": title } : { "aria-hidden": true }}>
    {title && <title>{title}</title>}
    <path d={`${line}L${width},${height}L0,${height}Z`} fill={colour} opacity={0.12} />
    <path d={line} fill="none" stroke={colour} strokeWidth={1} strokeLinecap="round" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
    <circle cx={x(values.length - 1)} cy={y(values[values.length - 1])} r={2} fill={colour} />
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
