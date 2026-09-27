// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import type { API } from "./types";
import { date } from "./ui";
import { lastSuccessfulBackup } from "./backup-types";
import type { BackupRecoveryPoint } from "./backup-types";

type Props = { vmID: string; poolID: string; api: API };

export function VMLastBackup({ vmID, poolID, api }: Props) {
  const [point, setPoint] = useState<BackupRecoveryPoint | null | undefined>(undefined);
  const [tested, setTested] = useState<BackupRecoveryPoint | null>(null);
  const [error, setError] = useState("");
  const gen = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const n = ++gen.current;
    setPoint(undefined);
    setTested(null);
    setError("");
    if (!poolID) {
      setPoint(null);
      return () => { controller.abort(); };
    }
    void (async () => {
      try {
        const catalog = await api<BackupRecoveryPoint[]>(
          `pools/${encodeURIComponent(poolID)}/backup-catalog`,
          "GET",
          undefined,
          undefined,
          undefined,
          controller.signal,
        );
        if (n !== gen.current || controller.signal.aborted) return;
        setPoint(lastSuccessfulBackup(catalog, vmID));
        setTested(catalog.filter(p => p.source_vm_id === vmID && p.last_tested_at).sort((a, b) => (b.last_tested_at || "").localeCompare(a.last_tested_at || ""))[0] || null);
      } catch (e) {
        if (n !== gen.current || controller.signal.aborted) return;
        setPoint(null);
        setError(e instanceof Error ? e.message : String(e));
      }
    })();
    return () => { controller.abort(); };
  }, [vmID, poolID]);

  if (point === undefined) return <><dd className="muted">Loading…</dd><dt>Last tested restore</dt><dd className="muted">Loading…</dd></>;
  if (error) {
    return <><dd>
      <span className="muted">Unavailable</span>
      <small className="muted"> · <a href={`#/backups?vm=${encodeURIComponent(vmID)}`}>View backups</a></small>
    </dd><dt>Last tested restore</dt><dd className="muted">Unavailable</dd></>;
  }
  if (!point) {
    return <><dd>
      Never backed up
      <small> · <a href={`#/backups?vm=${encodeURIComponent(vmID)}&flow=backup-now`}>Back up now</a>
        {" · "}
        <a href={`#/backups?vm=${encodeURIComponent(vmID)}`}>View backups</a></small>
    </dd><dt>Last tested restore</dt><dd>Never tested</dd></>;
  }
  return <><dd>
    {date(point.captured_at)}
    <small> · <a href={`#/backups?vm=${encodeURIComponent(vmID)}&flow=backup-now`}>Back up now</a>
      {" · "}
      <a href={`#/backups?vm=${encodeURIComponent(vmID)}`}>View backups</a></small>
  </dd><dt>Last tested restore</dt><dd>{tested ? `${date(tested.last_tested_at)} · ${tested.last_test_result || "Result not recorded"}` : "Never tested"}</dd></>;
}
