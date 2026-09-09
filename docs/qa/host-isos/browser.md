# Host ISO browser acceptance

The focused browser runner is `tests/integration/host_iso_browser.py`. API impact: existing contract; it reads the existing host, pool, storage, media and task records. It adds no Controller/Core endpoint or command. Guide changes and aggregate checks are owned by the parent change.

## Disposable simulated run

Run from the repository root with the installed Go toolchain and Playwright environment:

```sh
GO="$PWD/.cache/toolchains/go/bin/go" .cache/phase1-qa-venv/bin/python tests/integration/host_iso_browser.py --evidence docs/qa/host-isos
```

The final run passed against served JavaScript SHA-256 `fd82675f5553f6ccfbe3f07a2fa8cb7e46769975622b137cacec3baf19887288`; see [machine results](simulated-results.json). The runner launches a real disposable HTTPS Controller and simulated mTLS agent, registers synthetic storage and uploads a synthetic ISO. Completed transfer state comes from the Controller/agent report flow. Additional statuses, foreign host/pool rows, stale/revoked/old-agent/no-inventory states, and failed refresh are explicitly injected browser responses. They establish presentation behavior, not real storage correctness.

Checks cover empty-to-completed records; queued, dispatched, failed and attention labels; media filtering by both host and pool; missing storage and matching storage IDs from the wrong host or pool; retained records after refresh failure; Retry recovery; keyboard Storage navigation and task history; and a 360px document without horizontal overflow. The ISO table scrolls internally, and focusing the task link reveals its column before keyboard activation.

Inspected captures include [desktop](simulated-statuses-desktop.png), [360px](simulated-statuses-360.png), [keyboard task column](simulated-task-keyboard-360.png), and [Controller error](simulated-controller-unavailable.png). The latest captures include the site picker. The test does not independently hold the initial workspace loading response. Transfer success is historical verification, not a continuous check that the file still exists; VM creation checks its selected media again.

## Retained native lab

Read-only verification uses `--config` with the private disposable lab configuration. It reads the existing actual host and ISO records, opens Storage through keyboard navigation, and captures desktop and 360px views. It does not upload media, register storage, or change guest state. The native run passed against the same final JavaScript hash; see [native results](native-results.json), [desktop](native-desktop.png) and [360px](native-360.png), visually inspected alongside the [open searchable site picker](native-site-picker.png). The native run also opened the picker, searched for the current site, and selected it with Enter without mutating records. The actual host showed Debian and Nex ISO records, exact byte sizes, the selected storage name/path and task links, with historical completed-transfer labels.

```sh
.cache/phase1-qa-venv/bin/python tests/integration/host_iso_browser.py --config .cache/nex-fresh-install.s_h0lmf6/config.json --evidence docs/qa/host-isos
```

The private configuration supplies existing credentials without printing them. No native lab resources were changed by this runner, and the retained lab remains running.
