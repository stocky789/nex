// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type ReactNode } from "react";

// Renders only the visible slice of a long list inside a scroll parent, with spacer rows for the
// rest. Used by the VM table when it is not grouped by host.
export function useVirtualWindow(count: number, rowHeight: number, overscan = 8) {
  const scroller = useRef<HTMLElement | null>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewHeight, setViewHeight] = useState(600);

  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const onScroll = () => setScrollTop(el.scrollTop);
    const measure = () => setViewHeight(el.clientHeight || 600);
    measure();
    el.addEventListener("scroll", onScroll, { passive: true });
    const ro = typeof ResizeObserver === "function" ? new ResizeObserver(measure) : null;
    ro?.observe(el);
    return () => { el.removeEventListener("scroll", onScroll); ro?.disconnect(); };
  }, [count]);

  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const visible = Math.ceil(viewHeight / rowHeight) + overscan * 2;
  const end = Math.min(count, start + visible);
  return {
    scroller,
    start,
    end,
    topPad: start * rowHeight,
    bottomPad: Math.max(0, (count - end) * rowHeight),
    totalHeight: count * rowHeight,
  };
}

export function VirtualPad({ height, colSpan }: { height: number; colSpan: number }) {
  if (height <= 0) return null;
  return <tr aria-hidden="true" className="virtual-pad"><td colSpan={colSpan} style={{ height, padding: 0, border: 0 }} /></tr>;
}

export function bindScroller(ref: { current: HTMLElement | null }, node: HTMLElement | null) {
  ref.current = node;
}
