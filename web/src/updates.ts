// SPDX-License-Identifier: AGPL-3.0-only
export type UpdateStatus = { package: string; installed_version: string; available_version?: string; update_available: boolean; repo_url: string; phase?: string; progress?: number; message?: string; error?: string };

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
