// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import RFB from "@novnc/novnc";
import "./console.css";

// Console renders one VM's graphical console over a WebSocket bridge. Showing it is the
// request to connect: it attaches as soon as the VM can be reached, reconnects on its own
// if the link drops while the VM is still eligible, and detaches when hidden. `open`
// performs the authenticated request that yields the WebSocket URL for each attempt.
// `guest` is the host's latest observation of the guest channels; it only decides which
// hints the clipboard and display controls show, never whether input is allowed.
export type Guest = { agent: string; os?: string; hostname?: string; addresses?: { interface: string; address: string }[] | null; clipboard: string; resize: boolean };
export type ConsoleMedia = { currentID?: string; currentName?: string; options: { id: string; name: string }[]; busy?: boolean; note?: string; onInsert: (id: string) => void; onEject: () => void };
type Props = { name: string; open: () => Promise<string>; disabled?: string; guest?: Guest | null; popout?: string; media?: ConsoleMedia };
type Phase = "idle" | "connecting" | "connected" | "closed";
type View = "fit" | "actual" | "remote";
type Quality = "auto" | "low" | "high";
type Prefs = { view: View; quality: Quality };
const retryDelay = 5000;
const prefsKey = "nex-console-prefs";
const fromVMLimit = 65536;
const defaultPrefs: Prefs = { view: "fit", quality: "auto" };
// Tight encoding levels noVNC negotiates with QEMU: JPEG quality 0-9 and zlib compression 0-9.
const qualityLevels: Record<Quality, [number, number]> = { auto: [6, 2], low: [2, 9], high: [9, 1] };

// X11 keysyms (noVNC sends the matching QEMU scancode when a KeyboardEvent code is given).
const K = { ctrl: [0xffe3, "ControlLeft"], alt: [0xffe9, "AltLeft"], super: [0xffeb, "MetaLeft"], tab: [0xff09, "Tab"], esc: [0xff1b, "Escape"], del: [0xffff, "Delete"], f: (n: number): Key => [0xffbd + n, `F${n}`] } as const;
type Key = readonly [number, string];
const keyCombos: { label: string; keys: Key[] }[] = [
  ...[1, 2, 3, 4, 5, 6].map(n => ({ label: `Ctrl+Alt+F${n}`, keys: [K.ctrl, K.alt, K.f(n)] })),
  { label: "Ctrl+Alt+F7", keys: [K.ctrl, K.alt, K.f(7)] },
  { label: "Ctrl+C", keys: [K.ctrl, [0x63, "KeyC"] as Key] }, // interrupt, most useful on the host shell console
  { label: "Alt+Tab", keys: [K.alt, K.tab] },
  { label: "Alt+F4", keys: [K.alt, K.f(4)] },
  { label: "Ctrl+Esc", keys: [K.ctrl, K.esc] },
  { label: "Windows / Super key", keys: [K.super] },
  { label: "Escape", keys: [K.esc] },
];

function loadPrefs(): Prefs {
  try {
    const value = JSON.parse(localStorage.getItem(prefsKey) || "{}");
    return { view: ["fit", "actual", "remote"].includes(value.view) ? value.view : "fit", quality: ["auto", "low", "high"].includes(value.quality) ? value.quality : "auto" };
  } catch { return defaultPrefs; }
}
// keysym for typing one character: Latin-1 maps directly, everything else uses the Unicode keysym range.
const keysym = (ch: string) => { const cp = ch.codePointAt(0) || 0; return cp === 10 ? 0xff0d : cp === 9 ? 0xff09 : cp < 0x100 ? cp : 0x01000000 | cp; };

export function Console({ name, open, disabled, guest, popout, media }: Props) {
  const section = useRef<HTMLElement>(null);
  const screen = useRef<HTMLDivElement>(null);
  const rfb = useRef<RFB | null>(null);
  const [phase, setPhase] = useState<Phase>("idle");
  const [detail, setDetail] = useState("");
  const [prefs, setPrefs] = useState<Prefs>(loadPrefs);
  const [viewOnly, setViewOnly] = useState(false);
  const readOnly = useRef(false); // mirrors viewOnly for reconnects, which must not restore input silently
  const [clipboardOpen, setClipboardOpen] = useState(false);
  const [text, setText] = useState("");
  const [fromVM, setFromVM] = useState("");
  const [clipNote, setClipNote] = useState("");
  const [typing, setTyping] = useState(0); // characters left to type; 0 when idle
  const busy = useRef(false);
  const mounted = useRef(true);
  const retry = useRef<number | undefined>(undefined);
  // noVNC reports every server-initiated close as clean; only a disconnect this page asked for is.
  const wanted = useRef(false);
  const stop = () => { wanted.current = true; window.clearTimeout(retry.current); rfb.current?.disconnect(); rfb.current = null; };
  useEffect(() => () => { mounted.current = false; stop(); }, []);
  // A remembered "remote" view falls back to fit for a VM whose display cannot be resized.
  const resize = guest ? guest.resize : true;
  const effective = (p: Prefs): View => (p.view === "remote" && !resize ? "fit" : p.view);
  function apply(client: RFB, p: Prefs) {
    const view = effective(p);
    client.scaleViewport = view === "fit";
    client.clipViewport = false;
    client.resizeSession = view === "remote";
    [client.qualityLevel, client.compressionLevel] = qualityLevels[p.quality];
    client.viewOnly = readOnly.current;
  }
  async function connect() {
    if (busy.current || !screen.current || !mounted.current) return;
    busy.current = true; setDetail(""); setPhase("connecting");
    try {
      const url = await open();
      if (!mounted.current) return;
      stop();
      wanted.current = false;
      const client = new RFB(screen.current, url, { shared: true });
      client.background = "#000";
      apply(client, loadPrefs());
      client.addEventListener("connect", () => { setPhase("connected"); setDetail(""); client.focus({ preventScroll: true }); });
      client.addEventListener("disconnect", () => {
        if (rfb.current === client) rfb.current = null;
        setPhase("closed");
        if (wanted.current || !mounted.current) return;
        setDetail("Console connection ended unexpectedly. Reconnecting…");
        retry.current = window.setTimeout(() => void connect(), retryDelay);
      });
      client.addEventListener("securityfailure", e => setDetail(`Console refused: ${(e as CustomEvent<{ reason?: string }>).detail?.reason || "security failure"}`));
      client.addEventListener("credentialsrequired", () => { setDetail("The VM display asks for credentials, which Nex consoles do not use."); stop(); });
      client.addEventListener("clipboard", e => {
        // Guest-controlled text is only shown; it reaches the operator's clipboard through Copy.
        setFromVM(((e as CustomEvent<{ text: string }>).detail?.text ?? "").slice(0, fromVMLimit));
        setClipNote("Text copied inside the VM is shown below; select Copy to place it on your clipboard.");
      });
      rfb.current = client;
    } catch (e) {
      setPhase("closed");
      setDetail(`${(e as Error).message || "Console could not be opened."} Retrying…`);
      if (mounted.current) retry.current = window.setTimeout(() => void connect(), retryDelay);
    } finally { busy.current = false; }
  }
  // Connect when shown and whenever the VM becomes eligible again; detach while it is not.
  useEffect(() => {
    if (disabled) { stop(); setPhase("idle"); setDetail(""); return; }
    void connect();
  }, [disabled]);
  function update(next: Partial<Prefs>) {
    const p = { ...prefs, ...next };
    setPrefs(p);
    try { localStorage.setItem(prefsKey, JSON.stringify(p)); } catch {}
    if (rfb.current) apply(rfb.current, p);
  }
  function toggleViewOnly(value: boolean) {
    readOnly.current = value;
    setViewOnly(value);
    if (rfb.current) rfb.current.viewOnly = value;
  }
  function sendCombo(keys: Key[]) {
    const client = rfb.current;
    if (!client) return;
    keys.forEach(([sym, code]) => client.sendKey(sym, code, true));
    [...keys].reverse().forEach(([sym, code]) => client.sendKey(sym, code, false));
    client.focus({ preventScroll: true });
  }
  async function readClipboard() {
    try {
      const value = await navigator.clipboard.readText();
      setText(value);
      setClipNote(value ? "Your clipboard text is ready to send." : "Your clipboard has no text.");
    } catch { setClipNote("The browser did not allow reading your clipboard; paste into the text box instead."); }
  }
  function pasteToVM() {
    if (!rfb.current || !text) return;
    rfb.current.clipboardPasteFrom(text);
    setClipNote(guest?.clipboard === "ready" ? "Sent to the VM clipboard." : "Sent to the VM clipboard; it only arrives when spice-vdagent runs in the guest.");
    rfb.current.focus({ preventScroll: true });
  }
  // typeToVM sends text as keystrokes, which works in any guest, including installers and
  // text consoles. QEMU queues key events with a small delay, so pacing keeps order intact.
  async function typeToVM() {
    const client = rfb.current;
    if (!client || !text || typing) return;
    const chars = Array.from(text.replace(/\r\n?/g, "\n"));
    setClipNote("");
    for (let i = 0; i < chars.length; i++) {
      if (rfb.current !== client) break;
      setTyping(chars.length - i);
      const sym = keysym(chars[i]);
      client.sendKey(sym, null, true);
      client.sendKey(sym, null, false);
      await new Promise(r => setTimeout(r, 25));
    }
    setTyping(0);
    client.focus({ preventScroll: true });
  }
  function copyFromVM() {
    navigator.clipboard?.writeText(fromVM).then(() => setClipNote("Copied to your clipboard."), () => setClipNote("The browser did not allow writing your clipboard; select the text and copy it."));
  }
  function screenshot() {
    rfb.current?.toBlob(blob => {
      if (!blob) return;
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `${name}-${new Date().toISOString().replace(/[:.]/g, "-")}.png`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    }, "image/png");
  }
  const live = phase === "connected";
  const view = effective(prefs);
  const clipboardHint = !guest ? "Clipboard readiness is unknown for this VM; sending works when spice-vdagent runs in the guest."
    : guest.clipboard === "ready" ? "Guest clipboard is ready: text you send appears in the VM and VM copies appear here."
    : guest.clipboard === "waiting" ? "The VM has a clipboard channel but no spice-vdagent is running in the guest. Install the guest tools, or use Type into VM."
    : "This VM was created before console clipboard support. Recreate it to gain a clipboard channel, or use Type into VM.";
  return <section ref={section} className="panel console" aria-label={`Console for ${name}`}>
    <div className="section-heading"><h2>Console</h2><span className={`badge ${live ? "fresh" : phase === "connecting" ? "queued" : "stale"}`}>{live ? "connected" : phase === "connecting" ? "connecting" : phase === "closed" ? "disconnected" : "not connected"}</span></div>
    <div className="console-toolbar" role="toolbar" aria-label="Console controls">
      <button disabled={!live} onClick={() => rfb.current?.focus({ preventScroll: true })}>Focus console</button>
      <button disabled={!live || viewOnly} onClick={() => rfb.current?.sendCtrlAltDel()}>Send Ctrl+Alt+Del</button>
      <select aria-label="Send key combination" disabled={!live || viewOnly} value="" onChange={e => { const combo = keyCombos[Number(e.target.value)]; if (combo) sendCombo(combo.keys); }}>
        <option value="">Send key…</option>
        {keyCombos.map((combo, i) => <option key={combo.label} value={i}>{combo.label}</option>)}
      </select>
      <button disabled={!live} aria-pressed={clipboardOpen} onClick={() => setClipboardOpen(v => !v)}>Clipboard</button>
      {media && <>
        <label className="console-option">CD/DVD
          <select aria-label="Attach ISO" disabled={media.busy || !media.options.length} value="" onChange={e => { if (e.target.value) media.onInsert(e.target.value); }}>
            <option value="">{media.currentName ? media.currentName : "Empty"}</option>
            {media.options.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}
          </select>
        </label>
        <button type="button" disabled={media.busy || !media.currentID} onClick={() => media.onEject()}>Eject</button>
      </>}
      <label className="console-option">View
        <select aria-label="Display scaling" value={view} onChange={e => update({ view: e.target.value as View })}>
          <option value="fit">Fit to window</option>
          <option value="actual">Actual size</option>
          {resize && <option value="remote">Resize guest to window</option>}
        </select>
      </label>
      <label className="console-option">Quality
        <select aria-label="Image quality" value={prefs.quality} onChange={e => update({ quality: e.target.value as Quality })}>
          <option value="auto">Balanced</option>
          <option value="low">Low bandwidth</option>
          <option value="high">Best image</option>
        </select>
      </label>
      <label className="console-option"><input type="checkbox" checked={viewOnly} onChange={e => toggleViewOnly(e.target.checked)} /> View only</label>
      <button disabled={!live} onClick={screenshot}>Screenshot</button>
      <button disabled={Boolean(disabled) || phase === "connecting"} onClick={() => { stop(); void connect(); }}>Reconnect</button>
      <button onClick={() => (document.fullscreenElement ? document.exitFullscreen() : section.current?.requestFullscreen?.())}>Full screen</button>
      {popout && <a className="console-popout" href={popout} target="_blank" rel="noopener">Open in new window</a>}
    </div>
    {clipboardOpen && <div className="console-clipboard" aria-label="Console clipboard">
      <p className="help">{clipboardHint}</p>
      <label>Text to send to the VM<textarea rows={3} value={text} onChange={e => setText(e.target.value)} spellCheck={false} /></label>
      <div className="actions">
        <button type="button" onClick={() => void readClipboard()}>Read my clipboard</button>
        <button type="button" disabled={!live || !text || viewOnly || guest?.clipboard === "no-channel"} onClick={pasteToVM}>Paste into VM</button>
        <button type="button" disabled={!live || !text || viewOnly || typing > 0} onClick={() => void typeToVM()}>{typing ? `Typing… ${typing} left` : "Type into VM"}</button>
      </div>
      <label>Latest text copied inside the VM<textarea rows={3} value={fromVM} readOnly spellCheck={false} /></label>
      <div className="actions"><button type="button" disabled={!fromVM} onClick={copyFromVM}>Copy</button></div>
      {clipNote && <p className="help" role="status">{clipNote}</p>}
    </div>}
    {media?.note && <p className="console-status" role="status">{media.note}</p>}
    {disabled && <p className="warning">{disabled}</p>}
    {detail && <p className="console-status" role="status">{detail}</p>}
    <div ref={screen} className="console-screen" data-phase={phase} data-view={view} tabIndex={-1} />
    <p className="panel-note">Click the screen or use Focus console to type into the VM; keys then go to the guest, not this page. {view === "remote" ? "The guest is asked to match this window's size; guests without a VirtIO display driver keep their own resolution." : view === "actual" ? "Scroll to reach the rest of a large display." : "The display is scaled to fit this panel."}</p>
  </section>;
}
