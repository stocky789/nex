<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
  // Dark capture over the light one; a transparent range input on top gives mouse, touch and
  // keyboard dragging. static/theme-slider.js enables the input and moves the split; without
  // it the input stays disabled (and hidden) and the split stays at a static 50%.
  let { stem, alt, eager = false }: { stem: string; alt: string; eager?: boolean } = $props();
</script>

<div class="theme-slider">
  <img src={`/gallery/${stem}-light.png`} {alt} width="1440" height="1000" loading={eager ? "eager" : "lazy"} />
  <img class="dark" src={`/gallery/${stem}-dark.png`} alt="" width="1440" height="1000" loading={eager ? "eager" : "lazy"} />
  <span class="tag light-tag" aria-hidden="true">Light</span>
  <span class="tag dark-tag" aria-hidden="true">Dark</span>
  <input type="range" min="0" max="100" step="1" value="50" autocomplete="off" disabled aria-valuetext="50% light, 50% dark" aria-label="Compare light and dark theme: drag to reveal" />
  <span class="divider" aria-hidden="true"><span class="grip">‹ ›</span></span>
</div>

<style>
  .theme-slider { --pos: 50%; position: relative; overflow: hidden; border: 1px solid var(--line); background: #fff; touch-action: pan-y; }
  img { display: block; width: 100%; height: auto; }
  .dark { position: absolute; inset: 0; clip-path: inset(0 0 0 var(--pos)); }
  .divider { position: absolute; top: 0; bottom: 0; left: var(--pos); width: 2px; margin-left: -1px; background: var(--blue); pointer-events: none; }
  .grip { position: absolute; top: 50%; left: 50%; transform: translate(-50%, -50%); padding: 6px 8px; background: var(--blue); color: #fff; font-size: 13px; font-weight: 700; letter-spacing: 2px; white-space: nowrap; }
  .tag { position: absolute; top: 12px; padding: 3px 8px; font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: .08em; pointer-events: none; }
  .light-tag { left: 12px; background: #fff; color: #17233b; }
  .dark-tag { right: 12px; background: #111820; color: #fff; }
  input { position: absolute; inset: 0; width: 100%; height: 100%; margin: 0; opacity: 0; cursor: ew-resize; }
  input:disabled { display: none; }
  .theme-slider:has(input:focus-visible) { outline: 3px solid var(--blue); outline-offset: 2px; }
</style>
