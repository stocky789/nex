// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import RFB from "@novnc/novnc";
import "./console.css";

// Console renders one VM's graphical console over a WebSocket bridge. Showing it is the
// request to connect: it attaches as soon as the VM can be reached, reconnects on its own
// if the link drops while the VM is still eligible, and detaches when hidden. `open`
// performs the authenticated request that yields the WebSocket URL for each attempt.
type Props = { name: string; open: () => Promise<string>; disabled?: string };
type Phase = "idle" | "connecting" | "connected" | "closed";
const retryDelay = 5000;

export function Console({ name, open, disabled }: Props) {
  const screen = useRef<HTMLDivElement>(null);
  const rfb = useRef<RFB | null>(null);
  const [phase, setPhase] = useState<Phase>("idle");
  const [detail, setDetail] = useState("");
  const busy = useRef(false);
  const mounted = useRef(true);
  const retry = useRef<number | undefined>(undefined);
  // noVNC reports every server-initiated close as clean; only a disconnect this page asked for is.
  const wanted = useRef(false);
  const stop = () => { wanted.current = true; window.clearTimeout(retry.current); rfb.current?.disconnect(); rfb.current = null; };
  useEffect(() => () => { mounted.current = false; stop(); }, []);
  async function connect() {
    if (busy.current || !screen.current || !mounted.current) return;
    busy.current = true; setDetail(""); setPhase("connecting");
    try {
      const url = await open();
      if (!mounted.current) return;
      stop();
      wanted.current = false;
      const client = new RFB(screen.current, url, { shared: true });
      client.scaleViewport = true;
      client.background = "#000";
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
  const live = phase === "connected";
  return <section className="panel console" aria-label={`Console for ${name}`}>
    <div className="section-heading"><h2>Console</h2><span className={`badge ${live ? "fresh" : phase === "connecting" ? "queued" : "stale"}`}>{live ? "connected" : phase === "connecting" ? "connecting" : phase === "closed" ? "disconnected" : "not connected"}</span></div>
    <div className="console-toolbar">
      <button disabled={!live} onClick={() => rfb.current?.focus({ preventScroll: true })}>Focus console</button>
      <button disabled={!live} onClick={() => rfb.current?.sendCtrlAltDel()}>Send Ctrl+Alt+Del</button>
      <button disabled={!live} onClick={() => screen.current?.requestFullscreen?.()}>Full screen</button>
      <span className="muted">Click the screen or use Focus console to type into the VM; keys then go to the guest, not this page.</span>
    </div>
    {disabled && <p className="warning">{disabled}</p>}
    {detail && <p className="console-status" role="status">{detail}</p>}
    <div ref={screen} className="console-screen" data-phase={phase} tabIndex={-1} />
  </section>;
}
