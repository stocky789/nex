# Host ISO browser acceptance

Runner: `tests/integration/host_iso_browser.py`. Served app.js SHA-256 `fd82675f…887288` (full hashes in
[checks.md](checks.md)). Captures are local only (`docs/qa/**/*.png` is gitignored).

## Disposable simulated run

```sh
GO="$PWD/.cache/toolchains/go/bin/go" .cache/phase1-qa-venv/bin/python tests/integration/host_iso_browser.py --evidence docs/qa/host-isos
```

Passed; see [results](simulated-results.json). A real HTTPS Controller and simulated mTLS agent registered synthetic
storage and uploaded a synthetic ISO, so transfer completion came from the report flow. Other statuses, foreign host/pool rows,
stale/revoked/old-agent/no-inventory and refresh failure are injected browser responses, which show presentation only.

Covered: empty → completed; queued/dispatched/failed/attention labels; filtering by host and pool; missing or
foreign-ID storage; records kept on refresh failure; Retry; keyboard Storage and task history; 360 px without
overflow (table scrolls internally; focusing the task link reveals it).

Inspected: `simulated-statuses-desktop.png`, `simulated-statuses-360.png`,
`simulated-task-keyboard-360.png`, `simulated-controller-unavailable.png` (latest include the site picker).
Not checked: the held initial loading response. Transfer success is historical, not a continuous existence check.

## Retained native lab

```sh
.cache/phase1-qa-venv/bin/python tests/integration/host_iso_browser.py --config .cache/nex-fresh-install.<redacted>/config.json --evidence docs/qa/host-isos
```

Read-only: no upload, registration or guest change. Passed against the same hash; see [results](native-results.json). The real host
showed its Debian and Nex ISO records with exact bytes, storage name/path, task links and historical completed labels. The
site picker was opened, searched and selected with Enter. Inspected: `native-desktop.png`, `native-360.png`,
`native-site-picker.png`. The lab was left running.
