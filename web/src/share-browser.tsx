// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type ReactNode } from "react";

// The NFS export / SMB share folder picker used by VM storage and backup repository forms.
// Both ask the same transient Core discovery (POST /api/v1/storage/discover).
export type BrowseKind = "nfs" | "smb";
export type BrowseCandidate = { path?: string; browsable: boolean; existing?: boolean; source?: { nfs?: { export: string }; smb?: { share: string } } };
export type BrowseResult = { status: string; error?: string; candidates?: BrowseCandidate[] };
type Browser = { kind: BrowseKind; path: string; base: string; version: number; candidates: BrowseCandidate[]; selected?: BrowseCandidate; loading: boolean; error: string };

type Options = {
  server: string;
  // Changes whenever anything the listing depends on changes (host, server, account); an open browser then closes.
  // It may contain a credential, so it is only compared, never kept in React state.
  context: string;
  discover: (kind: BrowseKind, path: string, signal: AbortSignal) => Promise<BrowseResult>;
  // container is the NFS export or SMB share; directory is relative to it.
  onSelect: (kind: BrowseKind, container: string, directory: string) => void;
};

// The folder a Select click maps to: an SMB path is /share/dir, an NFS path is inside its export.
export function browseSelection(kind: BrowseKind, path: string, base: string, exported?: string): { container: string; directory: string } {
  if (kind === "smb") {
    const [share, ...directory] = path.replace(/^\//, "").split("/");
    return { container: share, directory: directory.join("/") };
  }
  const root = base || exported || path;
  return { container: root, directory: path === root ? "" : path.startsWith(`${root}/`) ? path.slice(root.length + 1) : "" };
}

export function useShareBrowser({ server, context, discover, onSelect }: Options): { browse: (kind: BrowseKind) => void; close: () => void; dialog: ReactNode } {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const abortRef = useRef<AbortController | null>(null);
  const [browser, setBrowser] = useState<Browser | null>(null);
  const scope = useRef({ context, version: 0 });
  if (scope.current.context !== context) scope.current = { context, version: scope.current.version + 1 };
  useEffect(() => {
    if (browser && !dialogRef.current?.open) dialogRef.current?.showModal();
  }, [browser]);
  useEffect(() => {
    if (browser && browser.version !== scope.current.version) close();
  }, [browser, context]);
  useEffect(() => () => abortRef.current?.abort(), []);
  // A reload replaces the focused control; keep keyboard and screen reader users inside the dialog.
  useEffect(() => {
    if (!browser || browser.loading) return;
    const active = document.activeElement;
    if (!active || !dialogRef.current?.contains(active) || active === dialogRef.current || (active as HTMLButtonElement).disabled) (listRef.current ?? dialogRef.current)?.focus();
  }, [browser?.loading, browser?.path]);
  function cancel() { const abort = abortRef.current; abort?.abort(); if (abortRef.current === abort) abortRef.current = null; setBrowser(null); }
  function close() { cancel(); dialogRef.current?.close(); }
  async function load(kind: BrowseKind, path = "", base = "") {
    abortRef.current?.abort();
    const abort = new AbortController(); abortRef.current = abort;
    const started = scope.current.version;
    const current = () => !abort.signal.aborted && abortRef.current === abort && started === scope.current.version;
    setBrowser({ kind, path, base, version: started, candidates: [], loading: true, error: "" });
    try {
      const result = await discover(kind, path, abort.signal);
      if (current()) setBrowser({ kind, path, base, version: started, candidates: result.status === "succeeded" ? result.candidates?.filter(candidate => candidate.browsable) || [] : [], loading: false, error: result.status === "succeeded" ? result.error || "" : result.error || "Discovery did not complete." });
    } catch (e) {
      if (current()) setBrowser({ kind, path, base, version: started, candidates: [], loading: false, error: (e as Error).message });
    } finally { if (abortRef.current === abort) abortRef.current = null; }
  }
  function openFolder(candidate: BrowseCandidate) {
    if (!browser || !candidate.path) return;
    const base = browser.base || (browser.kind === "nfs" ? candidate.source?.nfs?.export || candidate.path : candidate.source?.smb?.share || "");
    void load(browser.kind, candidate.path, base);
  }
  function up() {
    if (!browser?.path) return;
    if (browser.kind === "nfs" && browser.path === browser.base) { void load(browser.kind); return; }
    const parent = browser.path.slice(0, browser.path.lastIndexOf("/")) || "";
    void load(browser.kind, parent, parent ? browser.base : "");
  }
  function select() {
    if (!browser?.selected?.path) return;
    const { container, directory } = browseSelection(browser.kind, browser.selected.path, browser.base, browser.selected.source?.nfs?.export);
    onSelect(browser.kind, container, directory);
    close();
  }
  const flagged = Boolean(browser?.candidates.some(candidate => candidate.existing));
  const dialog = <dialog ref={dialogRef} tabIndex={-1} className="confirm-dialog storage-browser" aria-labelledby="storage-browser-title" onCancel={event => { event.preventDefault(); close(); }} onClose={() => { if (!dialogRef.current?.open) cancel(); }}>{browser && <>
    <h2 id="storage-browser-title">{browser.kind === "smb" ? "Browse shared folders" : "Browse exports"}</h2>
    <p className="mono storage-browser-path">{browser.kind === "smb" ? `//${server}${browser.path}` : `${server}:${browser.path || "/"}`}</p>
    <div className="actions"><button type="button" title="Back / Up" disabled={browser.loading || !browser.path} onClick={up}>Up</button><button type="button" disabled={browser.loading} onClick={() => void load(browser.kind, browser.path, browser.base)}>Refresh</button></div>
    <div role="status">{browser.loading ? "Loading folders…" : browser.error ? "" : `${browser.candidates.length} ${browser.candidates.length === 1 ? "folder" : "folders"} listed.`}</div>
    {browser.loading ? null : browser.error ? <><p className="alert" role="alert">{browser.error}</p><button type="button" onClick={() => void load(browser.kind, browser.path, browser.base)}>Retry</button></> : browser.candidates.length ? <><ul ref={listRef} tabIndex={-1} className="storage-browser-list">{browser.candidates.map((candidate, index) => <li key={`${candidate.path}-${index}`}><button type="button" className="storage-browser-folder" aria-label={candidate.existing ? `Select ${candidate.path}, a backup repository was found here` : `Select ${candidate.path}`} aria-pressed={browser.selected?.path === candidate.path} onClick={() => setBrowser({ ...browser, selected: candidate })} onDoubleClick={() => openFolder(candidate)}><span className="storage-browser-name">{candidate.path}</span>{candidate.existing && <span className="storage-existing-badge" title="A backup repository was found here" aria-hidden="true">E</span>}</button></li>)}</ul>{flagged && <p className="help">E = a backup repository was found here; registration still verifies it.</p>}{browser.candidates.length >= 64 && <p className="help">Showing up to 64 results; use a known path manually if your folder is missing.</p>}</> : <p className="empty">No folders were found. Enter the source manually or choose another location.</p>}
    <div className="actions"><button type="button" disabled={browser.loading || !browser.selected} onClick={() => browser.selected && openFolder(browser.selected)}>Open folder</button><button type="button" onClick={close}>Cancel</button><button type="button" className="primary" disabled={browser.loading || !browser.selected} onClick={select}>Select folder</button></div>
  </>}</dialog>;
  return { browse: kind => void load(kind), close, dialog };
}
