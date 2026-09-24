# Controller action refresh audit

Date: 2026-09-24. Development working tree; local Chromium with simulated API replies.
No live hosts, storage changes, fencing, installation or upgrades are exercised.

## Scope and findings

| Action or view | Existing path and outcome |
|---|---|
| Site creation, host enrollment/re-enrollment/revocation, VM console power and media | Shell action handlers reload inventory after acceptance; the ten-second inventory poll follows reported outcomes. Inventory refresh now clears its queued notice when its tracked task settles. |
| VM creation, editing, tags, single/bulk power and deletion | Existing callbacks reload inventory; list/panel props come from subsequent host reports. |
| Network and media registration, rename/removal, ISO upload/download | Existing callbacks reload inventory; queued transfers remain tasks until Core reports their outcome. |
| Storage registration/edit/recheck/detach, failed-request discard, ZFS provision/replacement | Existing callbacks reload inventory; this audit does not execute destructive host operations. |
| Pool membership, maintenance, authority, fencing profiles and BMC configuration | Successful actions already reread membership, but idle reports did not keep polling. Now follow every visible ten-second tick; uncertain submissions also reread without replaying the write. |
| Management networking | Successful stages already reread; polling previously required a known staged/applied record. Now follow late/external changes while idle and reconcile uncertain submission responses. |
| Clustered storage | Separate status report previously loaded once after submission. Now follows later Core stages without manually refreshing. |
| Host hardware and membership | Hardware previously loaded on opening/retry; completing Refresh inventory did not update it. Hardware now rereads after that task succeeds and every minute; both dedicated reports catch up on visibility/reconnection. |
| Controller updates | Check reads update status; Apply follows the helper's status through restart. If the submit response is lost, status reads continue through temporary server errors without resending the update. No activity, or an old terminal report without observed activity, leaves the outcome unknown. |
| Consoles, discovery and historical charts | Console ticket creation and discovery return their requested result directly. Existing chart cadences and intentional long-range manual refresh are unchanged. |

The shell catches up immediately on browser visibility and online events. Pool/network
report sequence guards prevent older requests overwriting a newer report. Polls only read
existing APIs; Core retains ownership of operations and durable pool state. Acceptance
does not mean completion. Automatic polls preserve the uncertain-fence guard; the existing
explicit Refresh Core status acknowledgement behavior is unchanged.

## Verification

The focused browser regression is `tests/integration/controller_refresh_browser.py`.
Its pre-fix assets reproduced the open hardware panel remaining unchanged after Core reported
the inventory-refresh task complete. The built UI passed 12 Chromium scenarios with mocked API
responses, including an update POST accepted with a lost response and a transient status 503;
the browser never repeated that POST. The update scenario failed against the build before the
fix. Captures of refreshed hardware, staged networking, BMC setup, clustered-storage progress,
read-error states and the console confirmation were inspected. There were no page errors.

Executed locally:

- `npm --prefix web test`, `npm --prefix web run typecheck`, and
  `npm --prefix web run build`: passed.
- `.cache/phase1-qa-venv/bin/python tests/integration/controller_refresh_browser.py
  --evidence .cache/controller-refresh-browser`: passed all 12 scenarios in local Chromium.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`: passed
  before unrelated guest OS assets appeared; the final shared-tree run fails on personal
  path strings inside those assets. The same privacy patterns pass for this assessment's
  24 paths.
- Extra smoke, `managed_vm_browser.py --simulated`: stopped before VM creation at its obsolete
  `link` named `1 task running` selector. The current activity control is a menu, so this
  walkthrough does not establish VM creation acceptance. No production change to that already
  repaired flow was made by this audit.
- `python3 scripts/check_docs.py --worktree`: passed before unrelated guest OS assets
  appeared; the final shared-tree run reports those assets missing from their own
  documentation assessment. The scoped check passes for this assessment's 24 paths.

Unrelated work arrived in the shared checkout during verification; it is excluded from this
change's assessment. Forge execution requires the project owner's commit/push; no remote run
or live-host, destructive storage, network or fencing correctness is claimed.

API impact: **none** — browser scheduling/presentation only; no API behavior or contract changes. Guide impact: **updated**. State owner: **none**.
