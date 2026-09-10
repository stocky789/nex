# Controller desktop layout verification

Date: 2026-09-09
Status: Frontend slice passed local verification and independent review.

## Scope and reference

Rebuild the Controller web shell around a Windows desktop application layout:
compact menu and icon toolbar, left resource tree, selected resource header and
horizontal views, dense property rows, bottom tasks and status. Retain Nex identity.
The [layout plan](../../plans/2026-09-09-controller-desktop-layout.md) describes the
layout and records authorization. No third-party artwork or code
is shipped. The Core local UI, installer, VM capabilities and backend are unchanged.

API impact: **existing contract** — session, site/pool creation, host inventory,
enrollment/revocation, refresh-inventory operations and audit records retain their
existing requests, authentication, agent command/result and retry semantics.
API reference changes are unnecessary because the contracts are unchanged.
Guide impact: **updated** — the [Controller guide](../../guide/manage-controller.md)
describes the resource pane, menus, detail views and task drawer.

## Environment and method

Disposable local HTTPS Controller and agent processes, temporary SQLite/identity
state, Playwright Chromium, and the built React UI. The Phase 1 harness exercises
actual API and agent enrollment/reconciliation. Its virsh results and failure modes
are explicitly simulated. Temporary browser certificate bypass is confined to the
lab; the API client and agent verify the temporary CA. No live host is changed.

## Results

The following commands passed on the final UI:

```sh
npm --prefix web run typecheck
npm --prefix web run build
GO="$PWD/.cache/toolchains/go/bin/go" \
NEX_QA_CAPTURES="$PWD/docs/qa/controller-desktop-layout/captures" \
.cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser \
  --evidence docs/qa/controller-desktop-layout/results.json
```

The browser/process command exited zero with all **16 result groups completed**.
[Results](results.json) identify the binaries and executed checks;
[UI hashes and environment](build.json) identify the actual source/assets tested.
The guide's changed browser steps were exercised: keyboard login and invalid
credentials; create site/pool through the menu and toolbar; generate/close a token;
search and collapse the tree; switch General/Networking/Storage/Activity views;
refresh inventory and inspect tasks; invalid hostname confirmation rejection;
revocation/re-enrollment entry; sign-out and expired-session recovery. Real process
checks cover enrollment, credential rotation, revoke/re-enroll identity and reconnect.
No browser page errors occurred. Deliberately rejected authentication/CSRF and
injected failed requests are expected failures, not healthy HTTP responses.

Desktop chrome geometry and page overflow passed at 1440×1000. All resource views
and menu bounds passed at 360px. Keyboard checks cover menu Enter/Escape, disclosure
arrows, selected navigation, view buttons, the task drawer and skip-to-content.
Empty pool, invalid resource, session loading, failed/stalled probes, interrupted
requests/Retry and an actually stopped agent were exercised. VM effects remain
outside these checks.

The first complete redesigned browser run passed. A subsequent, additional menu
bounds check caught Help extending beyond the left edge at 360px; it failed before
removing the mobile right-alignment override. The final complete run passed that
regression and all prior checks. See [BUG-0026](../../BUG_LEDGER.md#bug-0026--desktop-help-menu-clipped-on-narrow-screens-during-development).

The implementation owner inspected actual desktop and narrow captures, including
the final corrected menu, dense property content, empty and error states, stale
warnings and named revocation confirmation. An independent reviewer checked the
implementation, guide/API applicability, both CI invocations, recorded results and
captures; it found no blocking regressions and verified the menu correction.

| View/state | Inspected capture |
|---|---|
| Desktop shell and selected host | [General](captures/desktop-shell.png) |
| Observed host networking | [Networking](captures/networking-desktop.png) |
| Observed disks | [Storage](captures/storage-desktop.png) |
| Scoped history | [Activity](captures/activity-desktop.png) |
| Empty pool | [Empty](captures/empty-pool-desktop.png) |
| Named confirmation and keyboard focus | [Revoke](captures/revoke-confirmation.png) |
| Narrow layout | [Host at 360px](captures/host-360.png) |
| Corrected mobile dropdown | [Help at 360px](captures/help-menu-360.png) |
| Interrupted inventory read | [Error and Retry](captures/network-error-360.png) |
| Stopped agent | [Stale host](captures/stale-host-360.png) |

Documentation structure, scoped working-tree assessment coverage and whitespace
checks passed. These are local checks of an uncommitted change; committed-range
and hosted-CI checks are not claimed.

## Limits

This frontend slice does not establish real guest, storage, hardware, appliance or
ISO acceptance. The later [matching-source artifact rebuild](../controller-desktop-rebuild/checks.md)
records delivery of this UI; already deployed Controller VMs are not upgraded by
rebuilding installation media. Hosted GitHub/Gitea Actions have not
been run by this change. Both workflows already invoke the extended Phase 1 browser
harness on pushes and pull requests.

The captures above are pinned to this slice's build. The current icon and color
refresh is captured under [controller-ui-colors](../controller-ui-colors/checks.md).
