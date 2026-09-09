// SPDX-License-Identifier: AGPL-3.0-only
// Minimal typing for the bundled noVNC client (MPL-2.0); only the members Nex uses.
declare module "@novnc/novnc" {
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, url: string | WebSocket, options?: { shared?: boolean });
    scaleViewport: boolean;
    clipViewport: boolean;
    resizeSession: boolean;
    viewOnly: boolean;
    qualityLevel: number;
    compressionLevel: number;
    background: string;
    disconnect(): void;
    focus(options?: FocusOptions): void;
    sendCtrlAltDel(): void;
    sendKey(keysym: number, code: string | null, down?: boolean): void;
    clipboardPasteFrom(text: string): void;
    toBlob(callback: (blob: Blob | null) => void, type?: string, quality?: number): void;
  }
}
