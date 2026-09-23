// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useId, useRef, useState } from "react";
import RFB from "@novnc/novnc";
import { Badge } from "./ui";
import "./console.css";

// Console renders one VM's graphical console over a WebSocket bridge. Showing it is the
// request to connect: it attaches as soon as the VM can be reached, reconnects on its own
// if the link drops while the VM is still eligible, and detaches when hidden. `open`
// performs the authenticated request that yields the WebSocket URL for each attempt.
// `guest` is the host's latest observation of the guest channels; it only decides which
// hints the clipboard and display controls show, never whether input is allowed.
export type Guest = { agent: string; os?: string; hostname?: string; addresses?: { interface: string; address: string }[] | null; clipboard: string; resize: boolean; media?: string };
// A VM can hold more than one CD drive, so a drivers disc sits beside the install disc. Each
// drive is one control group in the toolbar, named by its `label`; a host that reports a single
// drive keeps exactly the controls it had, labelled CD/DVD, with no drive choice to make.
export type ConsoleDrive = { drive: "install" | "drivers"; label: string; currentID?: string; currentName?: string; onInsert: (id: string) => void; onEject: () => void };
export type ConsoleMedia = { drives: ConsoleDrive[]; options: { id: string; name: string }[]; busy?: boolean; note?: string };
// `power` is offered so the operator can be watching before the guest boots: firmware boot
// prompts ("Press any key to boot from CD or DVD") expire in a few seconds, long before a
// console opened after the start has attached.
type Props = { name: string; open: () => Promise<string>; disabled?: string; guest?: Guest | null; popout?: string; media?: ConsoleMedia; power?: { state: string; busy?: boolean; onStart: () => void } };
type Phase = "idle" | "connecting" | "connected" | "closed";
type View = "fit" | "actual" | "remote";
type Prefs = { view: View };
const retryDelay = 5000;
const prefsKey = "nex-console-prefs";
const fromVMLimit = 65536;
const defaultPrefs: Prefs = { view: "fit" };

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
    return { view: ["fit", "actual", "remote"].includes(value.view) ? value.view : "fit" };
  } catch { return defaultPrefs; }
}
// keysym for typing one character: Latin-1 maps directly, everything else uses the Unicode keysym range.
const keysym = (ch: string) => { const cp = ch.codePointAt(0) || 0; return cp === 10 ? 0xff0d : cp === 9 ? 0xff09 : cp < 0x100 ? cp : 0x01000000 | cp; };

// Typing a character is pressing a physical key, with Shift held for half of them. A guest
// reads the scancode noVNC derives from the KeyboardEvent code plus the modifier state it was
// sent, not the keysym: a bare keysym arrives as whatever that key types unshifted, which lost
// every capital and shifted symbol in Windows guests (BUG-0212). Positions below are the US
// layout, the layout Nex gives every VM; characters outside it keep the bare keysym they had.
const shiftKey: Key = [0xffe1, "ShiftLeft"];
// [unshifted character, shifted character, KeyboardEvent code] for each printable US key.
const usKeys: readonly (readonly [string, string, string])[] = [
  ["`", "~", "Backquote"], ["1", "!", "Digit1"], ["2", "@", "Digit2"], ["3", "#", "Digit3"], ["4", "$", "Digit4"],
  ["5", "%", "Digit5"], ["6", "^", "Digit6"], ["7", "&", "Digit7"], ["8", "*", "Digit8"], ["9", "(", "Digit9"],
  ["0", ")", "Digit0"], ["-", "_", "Minus"], ["=", "+", "Equal"], ["[", "{", "BracketLeft"], ["]", "}", "BracketRight"],
  ["\\", "|", "Backslash"], [";", ":", "Semicolon"], ["'", "\"", "Quote"], [",", "<", "Comma"], [".", ">", "Period"],
  ["/", "?", "Slash"], [" ", " ", "Space"], ["\n", "\n", "Enter"], ["\t", "\t", "Tab"],
];
const usLayout = new Map<string, { code: string; shift: boolean }>();
for (const [plain, shifted, code] of usKeys) {
  usLayout.set(plain, { code, shift: false });
  if (shifted !== plain) usLayout.set(shifted, { code, shift: true });
}
for (let i = 0; i < 26; i++) {
  const code = `Key${String.fromCharCode(0x41 + i)}`;
  usLayout.set(String.fromCharCode(0x61 + i), { code, shift: false });
  usLayout.set(String.fromCharCode(0x41 + i), { code, shift: true });
}
// typedKey is the key a US keyboard presses for one character: its X11 keysym, the physical
// KeyboardEvent code noVNC turns into a QEMU scancode, and whether Shift is held for it.
export function typedKey(ch: string): { sym: number; code: string | null; shift: boolean } {
  const at = usLayout.get(ch);
  return { sym: keysym(ch), code: at ? at.code : null, shift: Boolean(at && at.shift) };
}
// typedEvents is the exact sendKey(keysym, code, down) sequence for one character: Shift is
// pressed around the key whenever the character needs it, and released again straight after.
export function typedEvents(ch: string): [number, string | null, boolean][] {
  const { sym, code, shift } = typedKey(ch);
  const press: [number, string | null, boolean][] = [[sym, code, true], [sym, code, false]];
  return shift ? [[shiftKey[0], shiftKey[1], true], ...press, [shiftKey[0], shiftKey[1], false]] : press;
}

export function Console({ name, open, disabled, guest, popout, media, power }: Props) {
  const [toolbarOpen, setToolbarOpen] = useState(true);
  const toolbarID = useId();
  const startHintID = useId();
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
  const [focus, setFocus] = useState(false);
  const busy = useRef(false);
  const mounted = useRef(true);
  const retry = useRef<number | undefined>(undefined);
  // noVNC reports every server-initiated close as clean; only a disconnect this page asked for is.
  const wanted = useRef(false);
  const stop = () => { wanted.current = true; window.clearTimeout(retry.current); rfb.current?.disconnect(); rfb.current = null; };
  useEffect(() => () => { mounted.current = false; stop(); }, []);
  // Focus mode hides the Controller chrome around the console. Escape leaves it, except while the
  // guest display has the keyboard: there Escape belongs to the guest.
  useEffect(() => {
    document.body.classList.toggle("console-focus", focus);
    if (!focus) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape" && !screen.current?.contains(document.activeElement)) setFocus(false); };
    document.addEventListener("keydown", onKey);
    return () => { document.body.classList.remove("console-focus"); document.removeEventListener("keydown", onKey); };
  }, [focus]);
  // Resize is offered only where a guest has reported it can act on the request: a VirtIO
  // display and the running agent that applies the resolution. No report at all — a host
  // console, an older Core, the moment before a VM's first report — is not evidence of the
  // capability, so it is not offered either. A remembered "remote" view falls back to fit.
  const resize = Boolean(guest && guest.resize);
  const effective = (p: Prefs): View => (p.view === "remote" && !resize ? "fit" : p.view);
  function apply(client: RFB, p: Prefs) {
    const view = effective(p);
    client.scaleViewport = view === "fit";
    client.clipViewport = false;
    client.resizeSession = view === "remote";
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
      client.addEventListener("connect", () => {
        setPhase("connected"); setDetail(""); client.focus({ preventScroll: true });
        // noVNC can receive ServerInit while React is still laying out a newly selected tab.
        // Reset its initial 0 × 0 autoscale after paint, then restore the chosen view.
        requestAnimationFrame(() => {
          if (rfb.current !== client) return;
          client.scaleViewport = false;
          requestAnimationFrame(() => { if (rfb.current === client) apply(client, loadPrefs()); });
        });
      });
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
    setClipNote(guest?.clipboard === "ready" ? "Sent to the VM clipboard." : "Sent to the VM clipboard; it only arrives when the guest tools (qemu-guest-agent and spice-vdagent) run in the guest.");
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
      for (const [sym, code, down] of typedEvents(chars[i])) client.sendKey(sym, code, down);
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
  const connectionBadge = <Badge value={live ? "fresh" : phase === "connecting" ? "queued" : "stale"} label={live ? "connected" : phase === "connecting" ? "connecting" : phase === "closed" ? "disconnected" : "not connected"} />;
  const clipboardHint = !guest ? "Clipboard readiness is unknown for this VM; sending works when spice-vdagent runs in the guest."
    : guest.clipboard === "ready" ? "Guest clipboard is ready: text you send appears in the VM and VM copies appear here."
    : guest.clipboard === "waiting" ? "The VM has a clipboard channel but the guest is not running both qemu-guest-agent and spice-vdagent. Install the guest tools, or use Type into VM."
    : "This VM was created before console clipboard support. Recreate it to gain a clipboard channel, or use Type into VM.";
  // One control group per CD drive; with a single drive there is no drive to choose, so the
  // controls are the ones this toolbar has always shown.
  const drives = media ? media.drives : [];
  const mediaOptions = media ? media.options : [];
  const mediaBusy = Boolean(media && media.busy);
  const oneDrive = drives.length === 1;
  return <section ref={section} className={`panel console${toolbarOpen ? "" : " console-collapsed"}`} aria-label={`Console for ${name}`}>
    <div id={toolbarID} className="console-controls" hidden={!toolbarOpen}>
    <div className="section-heading"><h2>Console</h2>{connectionBadge}</div>
    <div className="console-toolbar" role="toolbar" aria-label="Console controls">
      {power && power.state !== "running" && <>
        <button type="button" className="primary" disabled={power.busy} aria-describedby={startHintID} onClick={() => power.onStart()}>{power.busy ? "Starting…" : "Start VM"}</button>
        <span id={startHintID} className="console-hint">Starts the VM with this console open, so it attaches in time for any boot prompt, such as “Press any key to boot from CD or DVD”.</span>
      </>}
      <span className="console-group">
      <button disabled={!live} onClick={() => rfb.current?.focus({ preventScroll: true })}>Focus console</button>
      <button disabled={!live || viewOnly} onClick={() => rfb.current?.sendCtrlAltDel()}>Send Ctrl+Alt+Del</button>
      <select aria-label="Send key combination" disabled={!live || viewOnly} value="" onChange={e => { const combo = keyCombos[Number(e.target.value)]; if (combo) sendCombo(combo.keys); }}>
        <option value="">Send key…</option>
        {keyCombos.map((combo, i) => <option key={combo.label} value={i}>{combo.label}</option>)}
      </select>
      <button disabled={!live} aria-pressed={clipboardOpen} onClick={() => setClipboardOpen(v => !v)}>Clipboard</button>
      </span>
      {drives.length > 0 && <span className="console-group">{drives.map(drive => {
        // With one drive there is nothing to tell apart, so it keeps the name the toolbar has
        // always used; the caller's label names a drive only when the operator has a choice.
        const label = oneDrive ? "CD/DVD" : drive.label || (drive.drive === "drivers" ? "Driver disc" : "Install media");
        return <span key={drive.drive} className="console-drive">
          <label className="console-option">{label}
            <select aria-label={oneDrive ? "Attach ISO" : `Attach ISO to ${label}`} disabled={mediaBusy || !mediaOptions.length} value="" onChange={e => { if (e.target.value) drive.onInsert(e.target.value); }}>
              <option value="">{drive.currentName ? drive.currentName : "Empty"}</option>
              {mediaOptions.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}
            </select>
          </label>
          <button type="button" aria-label={oneDrive ? undefined : `Eject ${label}`} disabled={mediaBusy || !drive.currentID} onClick={() => drive.onEject()}>Eject</button>
        </span>;
      })}</span>}
      <span className="console-group">
      <label className="console-option">View
        <select aria-label="Display scaling" value={view} onChange={e => update({ view: e.target.value as View })}>
          <option value="fit">Fit to window</option>
          <option value="actual">Actual size</option>
          {/* Shown but disabled where no guest reports the capability: the option is a guest
              capability, and silently ignoring the request looked like a fault. */}
          <option value="remote" disabled={!resize}>Resize guest to window{resize ? "" : guest ? " (guest cannot resize)" : " (no guest report)"}</option>
        </select>
      </label>
      <label className="console-option"><input type="checkbox" checked={viewOnly} onChange={e => toggleViewOnly(e.target.checked)} /> View only</label>
      <button disabled={!live} onClick={screenshot}>Screenshot</button>
      <button disabled={Boolean(disabled) || phase === "connecting"} onClick={() => { stop(); void connect(); }}>Reconnect</button>
      <button onClick={() => (document.fullscreenElement ? document.exitFullscreen() : section.current?.requestFullscreen?.())}>Full screen</button>
      <button aria-pressed={focus} onClick={() => setFocus(v => !v)}>{focus ? "Leave focus mode" : "Focus mode"}</button>
      </span>
      {popout && <a className="console-popout" href={popout} target="_blank" rel="noopener">Open in new window</a>}
    </div>
    {/* Two columns where there is room: the clipboard is a toolbar panel above the display,
        and a tall stack of it leaves only a strip of the guest visible (BUG-0207). */}
    {clipboardOpen && <div className="console-clipboard" aria-label="Console clipboard">
      <p className="help console-clip-wide">{clipboardHint}</p>
      <div className="console-clip-column">
        <label>Text to send to the VM<textarea rows={3} value={text} onChange={e => setText(e.target.value)} spellCheck={false} /></label>
        <div className="actions">
          <button type="button" onClick={() => void readClipboard()}>Read my clipboard</button>
          <button type="button" disabled={!live || !text || viewOnly || guest?.clipboard === "no-channel"} onClick={pasteToVM}>Paste into VM</button>
          <button type="button" disabled={!live || !text || viewOnly || typing > 0} onClick={() => void typeToVM()}>{typing ? `Typing… ${typing} left` : "Type into VM"}</button>
        </div>
      </div>
      <div className="console-clip-column">
        <label>Latest text copied inside the VM<textarea rows={3} value={fromVM} readOnly spellCheck={false} /></label>
        <div className="actions"><button type="button" disabled={!fromVM} onClick={copyFromVM}>Copy</button></div>
      </div>
      {clipNote && <p className="help console-clip-wide" role="status">{clipNote}</p>}
    </div>}
    </div>
    {(media?.note || disabled || detail) && <p className="console-status" role="status">{[media?.note, disabled, detail].filter(Boolean).join(" ")}</p>}
    <div className="console-toolbar-edge">
      <button type="button" className="console-toolbar-toggle" aria-expanded={toolbarOpen} aria-controls={toolbarID} aria-label={toolbarOpen ? "Collapse console toolbar" : "Expand console toolbar"} title={toolbarOpen ? "Collapse console toolbar" : "Expand console toolbar"} onClick={() => setToolbarOpen(v => !v)}>
        <svg aria-hidden="true" width="14" height="14" viewBox="0 0 16 16"><path d={toolbarOpen ? "M3 10l5-5 5 5" : "M3 6l5 5 5-5"} fill="none" stroke="currentColor" strokeWidth="2" /></svg>
      </button>
      {!toolbarOpen && connectionBadge}
    </div>
    <div ref={screen} className="console-screen" data-phase={phase} data-view={view} tabIndex={-1} />
    <p hidden={!toolbarOpen} className="panel-note">Click the screen or use Focus console to type into the VM; keys then go to the guest, not this page. {view === "remote" ? "The guest is asked to match this window's size." : view === "actual" ? "Scroll to reach the rest of a large display." : "The display is scaled to fit this panel."}{resize ? "" : guest ? " Resize guest to window is unavailable: this guest has not reported that it can change its resolution. Install the guest tools, which include the display driver and the agent that applies it." : " Resize guest to window is unavailable: no guest report says this display can be resized."}</p>
  </section>;
}
