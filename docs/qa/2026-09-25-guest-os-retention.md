# Retained guest OS verification

Date: 2026-09-25. Environment: development working tree, temporary Go test stores and
HTTP test servers; built Controller UI in headless Chromium with simulated API responses.
No live host, guest, disk or network was changed. No deployment or forge run is claimed.

## Cause and scope

Core asks QEMU Guest Agent for `guest-get-osinfo`. Its live cache drops facts on a channel
disconnect or failed refresh, and only attaches guest observations to running VMs.
Controller previously substituted the configured Linux/Windows family without explaining
the change. These code paths confirm that the reported symptom is possible; they do not
establish whether the operator's particular VM slept or why its agent stopped answering.

Core now records OS name, canonical ID and detection time for managed pool VMs. Live
agent readiness, addresses and clipboard state remain separate. The master's VM record
owns history; an assigned host's durable observation survives an unavailable master for
later publication. UI reads the host record and marks historical facts **(last detected)**.

## Browser walkthrough

`npm --prefix web run build` and the following local harnesses passed:

```sh
.cache/phase1-qa-venv/bin/python .cache/guest-os-retention/browser.py
.cache/phase1-qa-venv/bin/python .cache/guest-os-retention/extra.py
```

The browser reviewer inspected the console design reference and all twelve captures on
2026-09-25; the primary also inspected `pmsuspended-none.png`. Results were saved to
`.cache/guest-os-retention/results.json` and `extra-results.json` with no browser JavaScript
errors. These local harnesses and captures are untracked, not portable regression evidence.

- VM list and Overview show live OS facts normally. Tab/Enter opens the VM Overview.
- Running guests with disconnected or unresponsive agents, sleeping (`pmsuspended`) and
  stopped guests retain the Debian name and icon with **(last detected)** after page reload.
- Unknown/stale-host state keeps history visibly historical; guest readiness is not restored.
- A new live OS replaces the old name and icon. No saved history shows **Linux (configured)**.
- Delayed initial inventory displays loading, then the retained OS after the response arrives.
  Empty inventory and API failure keep their existing empty/error presentations.
- Keyboard focus reaches the historical OS link with the correct accessible label; its
  enclosing tooltip has the recorded timestamp. The 800px Overview wraps the label without
  clipping. Captures include `initial-loading.png`, `retained-list.png`, `narrow-overview.png`,
  `running-disconnected.png`, `running-unresponsive.png`, `shut off-none.png`, `unknown-none.png`,
  `no-history.png`, `api-error.png`, `empty.png`, `reference.png` and `pmsuspended-none.png`.

## Runnable checks

The new assertion in `node web/src/guest-os.test.mjs` failed before the UI change: it expected
the saved Debian name but received **Linux (configured)**. It passed after the change.
`npm --prefix web test` and `npm --prefix web run typecheck` passed. Existing forge Phase 1
UI jobs execute the regression through the same suite on pushes and pull requests.

The following backend checks passed:

```sh
go test ./internal/agent ./internal/controller -run 'TestDetectedOS|TestGuestProbe' -count=1
go test -race ./internal/agent -run 'TestDetectedOS|TestGuestProbeCache|TestISODownloadProgress|TestEnroll|TestPoolVMInventory' -count=1
go test ./internal/controller ./internal/protocol -count=1
python3 -m unittest discover -s tests -p 'test_api_contract.py'
python3 -m unittest discover -s tests -p 'test_repository_privacy.py'
python3 scripts/generate_api.py --check
```

All 74 API contract tests passed. The Controller regression initially failed because the
existing stored-report whitelist omitted the new field; adding it made re-adoption pass.
Tests exercise actual authenticated Controller reports/reads and mTLS Core peer inventory
publication with a 503 master outage, store reopen and retry, plus forged-host rejection.
Direct store tests cover invalid/absent facts, newer OS replacement, old retries, deletion
and authority export/adoption. Probe fixtures prove failed or cached guest responses do
not create a new detection timestamp. Two empty Controller databases recover the same
Core-owned record without live guest data; stale hosts keep history separate from health.
Compatibility checks use a strict old decoder and three actual HTTP reports to verify
first-report suppression, positive acknowledgement and subsequent negative acknowledgement.

`go test ./internal/agent -count=1` did **not** pass: the existing
`TestAutostartPassAttemptsLocalStorageGuests` fixture fails because its hard-coded
`/var/lib/nex-storage/local-1` storage path does not exist in this environment. Isolated
execution reproduced it. Temporary diagnostic logging identified the underlying `lstat`
failure and was removed. This local-autostart branch does not call the guest probe or peer
inventory retention path. No live storage directory was created to make the test pass.

Independent review found the initial missing legacy-Controller negotiation; it was corrected
and rechecked with the nonempty-history HTTP tests. The reviewer independently passed the
focused agent and Controller tests and found no remaining blocking retention-code issue.
Existing forge Go jobs include the regression tests; this records local execution only.

`python3 scripts/check_docs.py --worktree` and `git diff --check` passed. API impact:
**changed contract**; guide impact: **updated**; durable state owner: **core**.
Feature-only code/test files add 645 and remove 17 lines (**net +628**), excluding documentation
and shared files with pre-existing changes. Shared-file hunks preserve those concurrent edits;
no whole-worktree line count is attributed to this fix. Changes remain unstaged.

## Limits

Browser observations use simulated API responses. Go checks use fake libvirt/agent responses
and temporary stores, not actual sleep/hibernate or a physically restarted Core host. Live
guest power transitions and the operator's original failure remain unverified; this report
does not close that live verification gate or certify a release.

Retention covers VMs with managed pool definitions. Unmanaged domains and separately adopted
Controller appliances have no such OS record. All pool hosts must support retention before
relying on history through master changes: an older Core master cannot retain the added field.
Ordering uses detection timestamps, so a host clock moved backwards can delay a historical
update until it passes the previous timestamp; available live OS facts still display normally.
