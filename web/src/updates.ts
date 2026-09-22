// SPDX-License-Identifier: AGPL-3.0-only
export type UpdateStatus = { package: string; core_available_version?: string; installed_version: string; available_version?: string; update_available: boolean; repo_url: string; phase?: string; progress?: number; message?: string; error?: string };

// Polls the update status once a second until the helper reports done or failed, for up to
// ten minutes. The service restarts mid-apply, so a poll that cannot reach it is part of a
// normal update, not a failure; an HTTP error (one with a status) still is.
export async function followUpdate(
  api: (path: string) => Promise<UpdateStatus>,
  onStatus: (st: UpdateStatus) => void,
  sleep = (ms: number) => new Promise<void>(r => setTimeout(r, ms)),
): Promise<UpdateStatus | undefined> {
  for (let i = 0; i < 600; i++) {
    await sleep(1000);
    let st: UpdateStatus;
    try {
      st = await api("updates");
    } catch (e) {
      if ((e as { status?: number }).status) throw e;
      continue;
    }
    onStatus(st);
    if (st.phase === "done" || st.phase === "failed") return st;
  }
  return undefined;
}

// Strip Debian epoch and package revision so apt candidates like "1:0.4.3-1" compare as 0.4.3.
// Keep a "~" pre-release marker separately; digit-scraping would otherwise treat ~rc1 as newer.
function nexVersion(raw: string): { parts: number[]; prerelease: boolean } | undefined {
  const upstream = raw.replace(/^\d+:/, "").replace(/-\d[0-9A-Za-z.+~]*$/, "");
  const prerelease = upstream.includes("~");
  const parts = (prerelease ? upstream.slice(0, upstream.indexOf("~")) : upstream).match(/\d+/g)?.map(Number);
  return parts?.length ? { parts, prerelease } : undefined;
}

// The host page's version light: green when the host runs the newest Nex Core, orange when a
// newer one exists, red when that is a new major version. Undefined when nothing is known.
// ponytail: numeric-segment compare, not dpkg ordering; leftover base digits after a match read behind.
export function versionLight(installed: string, available?: string): { level: "current" | "behind" | "major"; label: string; title: string } | undefined {
  const token = installed.match(/Nex Core\s+(\S+)/i)?.[1];
  if (!token || !available) return undefined;
  const have = nexVersion(token), want = nexVersion(available);
  if (!have || !want) return undefined;
  let i = 0;
  while (i < want.parts.length && i < have.parts.length && have.parts[i] === want.parts[i]) i++;
  const current = { level: "current" as const, label: "up to date", title: `Up to date: Nex Core ${available} is the newest release.` };
  const behind = { level: "behind" as const, label: "update available", title: `Update available: Nex Core ${available}; this host runs ${installed}.` };
  const major = { level: "major" as const, label: "major release behind", title: `Major release behind: Nex Core ${available} is available and this host runs ${installed}. Update it soon; newer Controllers may not support it.` };
  if (i < want.parts.length && i < have.parts.length) {
    if (have.parts[i]! > want.parts[i]!) return current;
    return i === 0 ? major : behind;
  }
  // Leftover base segments on the host after matching the archive mean it is not the final release.
  if (i < have.parts.length) return behind;
  if (i < want.parts.length) return i === 0 ? major : behind;
  if (have.prerelease && !want.prerelease) return behind;
  // Do not advertise a pre-release archive candidate as an upgrade over a matching final.
  return current;
}
