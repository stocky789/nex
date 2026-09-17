// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";

export type IconKind = "site" | "pool" | "host" | "home" | "refresh" | "tasks" | "vm" | "new-vm" | "back" | "forward" | "start" | "shutdown" | "reboot" | "stop" | "console" | "delete";
const stroke = { strokeWidth: 1.25, strokeLinecap: "round", strokeLinejoin: "round" } as const;
const vm = <><rect x="2" y="3" width="16" height="11" rx="1.5" fill="#93c5fd" stroke="#1d4ed8" /><path d="M7 17h6M10 14v3" stroke="#1d4ed8" /><path d="m8 6 5 3-5 3z" fill="#fff" /></>;
export function Icon({ kind }: { kind: IconKind }) {
  const paths: Record<IconKind, ReactNode> = {
    vm,
    "new-vm": <>{vm}<circle cx="16" cy="4.5" r="3.4" fill="#facc15" stroke="#a16207" /><path d="M16 2.8v3.4M14.3 4.5h3.4" stroke="#713f12" strokeWidth="1.5" /></>,
    home: <><rect x="1.5" y="1.5" width="17" height="17" rx="4.5" fill="#3478ff" /><path d="M6.5 14.5V9.5a3.5 3.5 0 0 1 7 0v5" stroke="#fff" strokeWidth="2.4" /></>,
    site: <><path d="M3 18.5V4.5a1 1 0 0 1 1-1h7a1 1 0 0 1 1 1v14" fill="#4f8dff" stroke="#1d4ed8" /><path d="M12 8h4.5a1 1 0 0 1 1 1v9.5" fill="#a8caff" stroke="#1d4ed8" /><path d="M5 6h2M8 6h2M5 9.5h2M8 9.5h2M5 13h2M8 13h2M14 11h1.5M14 14h1.5" stroke="#fff" strokeWidth="1.5" /><path d="M1.5 18.5h17" stroke="#1d4ed8" /></>,
    pool: <><rect x="2" y="3" width="7" height="10" rx="1.2" fill="#a78bfa" stroke="#6d28d9" /><rect x="11" y="3" width="7" height="10" rx="1.2" fill="#a78bfa" stroke="#6d28d9" /><path d="M4 6h3M4 8.5h3M13 6h3M13 8.5h3" stroke="#fff" strokeWidth="1.3" /><circle cx="7.5" cy="11" r=".9" fill="#4c1d95" /><circle cx="16.5" cy="11" r=".9" fill="#4c1d95" /><path d="M5.5 13v3h9v-3M10 16v2" stroke="#6d28d9" /><circle cx="10" cy="18" r="1.4" fill="#7c3aed" /></>,
    host: <><rect x="4" y="2" width="12" height="16" rx="1.5" fill="#2dd4bf" stroke="#0f766e" /><rect x="6" y="4.5" width="8" height="2.2" rx=".6" fill="#f0fdfa" /><rect x="6" y="8" width="8" height="2.2" rx=".6" fill="#f0fdfa" /><circle cx="7.5" cy="14.5" r="1.1" fill="#134e4a" /><rect x="10" y="13.7" width="4" height="1.6" rx=".6" fill="#134e4a" /></>,
    refresh: <><circle cx="10" cy="10" r="8.5" fill="#dbeafe" /><path d="M16.5 8.5A7 7 0 1 0 17 11.5" stroke="#2563eb" strokeWidth="2" /><path d="M17 3v5.5h-5.5" stroke="#2563eb" strokeWidth="2" /></>,
    tasks: <><rect x="4" y="3.5" width="12" height="14.5" rx="1.5" fill="#fbbf24" stroke="#b45309" /><rect x="7" y="2" width="6" height="3" rx="1" fill="#fff7ed" stroke="#b45309" /><path d="m6.5 10 1.6 1.6L11 8.6" stroke="#fff" strokeWidth="1.7" /><path d="M12.5 10h1.5M6.5 14h7" stroke="#fff" strokeWidth="1.5" /></>,
    back: <><circle cx="10" cy="10" r="8.5" fill="#4ade80" stroke="#15803d" /><path d="M13.5 10h-7M9.5 7l-3 3 3 3" stroke="#fff" strokeWidth="2" /></>,
    forward: <><circle cx="10" cy="10" r="8.5" fill="#4ade80" stroke="#15803d" /><path d="M6.5 10h7M10.5 7l3 3-3 3" stroke="#fff" strokeWidth="2" /></>,
    start: <><circle cx="10" cy="10" r="8.5" fill="#22c55e" stroke="#15803d" /><path d="m8 6.5 5.5 3.5L8 13.5z" fill="#fff" /></>,
    shutdown: <><circle cx="10" cy="10" r="8.5" fill="#ef4444" stroke="#991b1b" /><path d="M10 5v5" stroke="#fff" strokeWidth="2" /><path d="M7.2 7.3a4 4 0 1 0 5.6 0" stroke="#fff" strokeWidth="2" /></>,
    reboot: <><circle cx="10" cy="10" r="8.5" fill="#22c55e" stroke="#15803d" /><path d="M13.6 8.5A4 4 0 1 0 14 11.3" stroke="#fff" strokeWidth="2" /><path d="M14.2 5.5v3.2H11" stroke="#fff" strokeWidth="2" /></>,
    stop: <><circle cx="10" cy="10" r="8.5" fill="#b91c1c" stroke="#7f1d1d" /><rect x="6.8" y="6.8" width="6.4" height="6.4" rx="1" fill="#fff" /></>,
    console: <><rect x="2" y="3" width="16" height="12" rx="1.5" fill="#64748b" stroke="#334155" /><rect x="3.5" y="4.5" width="13" height="8" rx=".6" fill="#0f172a" /><path d="M6 8.5h2.5M6 11h5" stroke="#94a3b8" strokeWidth="1.4" /><path d="M7 17h6M10 15v2" stroke="#334155" /></>,
    delete: <><circle cx="10" cy="10" r="8.5" fill="#b91c1c" stroke="#7f1d1d" /><path d="M7.2 7.2l5.6 5.6M12.8 7.2l-5.6 5.6" stroke="#fff" strokeWidth="2" /></>,
  };
  return <svg className={`resource-icon icon-${kind}`} viewBox="0 0 20 20" fill="none" {...stroke} aria-hidden="true">{paths[kind]}</svg>;
}
