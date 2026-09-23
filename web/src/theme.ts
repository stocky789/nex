// SPDX-License-Identifier: AGPL-3.0-only
import { readPreference } from "./dock";

export const themeKey = "nex.theme";

export function resolvedTheme(pref: string): "light" | "dark" {
  if (pref === "light" || pref === "dark") return pref;
  return typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function applyTheme(pref: string) {
  document.documentElement.dataset.theme = resolvedTheme(pref);
}

export function watchSystemTheme(onChange: () => void) {
  if (typeof matchMedia !== "function") return () => {};
  const mq = matchMedia("(prefers-color-scheme: dark)");
  const handler = () => { if (!readPreference(themeKey)) onChange(); };
  mq.addEventListener("change", handler);
  return () => mq.removeEventListener("change", handler);
}
