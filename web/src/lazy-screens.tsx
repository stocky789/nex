// SPDX-License-Identifier: AGPL-3.0-only
import { lazy, Suspense, type ComponentType, type ReactNode } from "react";
import { ScreenBoundary } from "./ui";

function screen<T extends ComponentType<any>>(load: () => Promise<{ default: T }>) {
  return lazy(load);
}

export const LazyOverview = screen(() => import("./overview").then(m => ({ default: m.Overview })));
export const LazyVMList = screen(() => import("./vm-list").then(m => ({ default: m.VMList })));
export const LazyHostList = screen(() => import("./host-list").then(m => ({ default: m.HostList })));
export const LazySettings = screen(() => import("./settings").then(m => ({ default: m.Settings })));
export const LazyBackups = screen(() => import("./backups").then(m => ({ default: m.Backups })));
export const LazyContainers = screen(() => import("./rail-screens").then(m => ({ default: m.Containers })));
export const LazyTasks = screen(() => import("./rail-screens").then(m => ({ default: m.Tasks })));
export const LazyEvents = screen(() => import("./rail-screens").then(m => ({ default: m.Events })));
export const LazyConsole = screen(() => import("./console").then(m => ({ default: m.Console })));
export const LazyRealtimeStats = screen(() => import("./stats").then(m => ({ default: m.RealtimeStats })));
export const LazyManagedVMs = screen(() => import("./managed").then(m => ({ default: m.ManagedVMs })));

export function LazyScreen({ children }: { children: ReactNode }) {
  return <ScreenBoundary><Suspense fallback={<p role="status">Loading…</p>}>{children}</Suspense></ScreenBoundary>;
}
