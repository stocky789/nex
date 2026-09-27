# Network rate and console chart display check

Date: 2026-09-25. Environment: local development build, Chromium with simulated Controller
API and VM console traffic. No physical Core host was used.

- `node web/src/stats.test.mjs` passed network byte-to-bit conversion at Mbps/Gbps, network
  axis labels, and unchanged disk byte units.
- `node web/src/host-list.test.mjs`, `npm --prefix web run typecheck`, and
  `npm --prefix web run build` passed.
- `.cache/phase1-qa-venv/bin/python tests/integration/console_controls_browser.py` passed.
  It exercised live loading, errors, retry, stacking, console connection, desktop two-column
  and narrow single-column chart geometry, and network tooltip Mbps versus disk KiB/s.
  The generated desktop and mobile captures under ignored `.cache/console-controls-browser/`
  were inspected; the console remained visible and the taller charts were readable.

The browser fixture simulates host samples and RFB. It does not prove network counter accuracy
on hardware or a deployed Core/Controller pair. No forge run is claimed.
