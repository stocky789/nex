# Hosts management IP regression

Development source, 2026-09-25. A local Chromium browser loaded the built Controller UI from a temporary HTTP server with simulated Core inventory. The fixture put `lo` first (`127.0.0.1/8`), then an IPv6 link-local and `192.0.2.10/24` on `br0`.

`node web/src/host-list.test.mjs` failed before the fix: the Hosts table displayed `127.0.0.1`. After the fix, it passed and checked the table, panel, and absent bridge address. `npm --prefix web run typecheck` passed. `npm --prefix web run build` passed. `.cache/phase1-qa-venv/bin/python tests/integration/site_selector_browser.py --evidence .cache/iso-url-progress-browser` passed nine reported groups after the fix. The browser asserted that the table and host panel displayed `192.0.2.10` and not the loopback address. The `host-management-ip.png` capture was inspected locally; it showed the management address in the row, panel subtitle, and properties. Captures under `.cache/` are untracked.

This is simulated inventory. No live host, stale-address recovery, or actual network configuration was exercised.
