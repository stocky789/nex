# Controller desktop layout verification

Date: 2026-09-09. Result: **passed** local verification and independent review. Scope: web shell per
the [layout plan](../../plans/2026-09-09-controller-desktop-layout.md) (menu, icon toolbar, resource
tree, header and views, property rows, tasks/status). Core local UI, installer and backend unchanged.
API impact: existing contract. Guide: [Controller guide](../../guide/manage-controller.md) updated.

## Method

Disposable HTTPS Controller and agent, temporary state, Playwright Chromium, built UI. Real
enrollment/reconciliation; **virsh results and failures simulated**. Browser certificate bypass is lab
only; API client and agent verify the temporary CA.

```sh
npm --prefix web run typecheck
npm --prefix web run build
GO="$PWD/.cache/toolchains/go/bin/go" \
NEX_QA_CAPTURES="$PWD/docs/qa/controller-desktop-layout/captures" \
.cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser \
  --evidence docs/qa/controller-desktop-layout/results.json
```

## Results

All passed; **16 result groups**. [Results](results.json) (binaries, checks), [build.json](build.json)
(UI hashes, environment).

- Guide steps: keyboard login and invalid credentials; site/pool via menu and toolbar; token
  generate/close; tree search/collapse; General/Networking/Storage/Activity; refresh and tasks;
  wrong-hostname revoke rejection; revoke/re-enroll; sign-out and expired-session recovery.
- Process: enrollment, rotation, revoke/re-enroll identity, reconnect. No page errors.
- Layout: chrome geometry and overflow at 1440×1000; all views and menu bounds at 360px; keyboard
  menu Enter/Escape, disclosure arrows, navigation, view buttons, task drawer, skip link.
- States: empty pool, invalid resource, loading, failed/stalled probes, interrupted request/Retry,
  stopped agent. VM effects not covered.
- Regression: a later menu-bounds check failed (Help clipped at 360px) before removing the mobile
  right-alignment override; final run passed ([BUG-0026](../../BUG_LEDGER.md#bug-0026--desktop-help-menu-clipped-on-narrow-screens-during-development)).
- Owner inspected captures; independent reviewer checked implementation, guide/API applicability,
  both CI invocations, results and captures: no blockers, menu fix verified.
- Docs structure, scoped assessment and whitespace checks passed locally.

| View/state | Capture |
|---|---|
| Desktop shell, selected host | `captures/desktop-shell.png` (not committed) |
| Host networking | `captures/networking-desktop.png` (not committed) |
| Disks | `captures/storage-desktop.png` (not committed) |
| Scoped history | `captures/activity-desktop.png` (not committed) |
| Empty pool | `captures/empty-pool-desktop.png` (not committed) |
| Named confirmation, focus | `captures/revoke-confirmation.png` (not committed) |
| Narrow | `captures/host-360.png` (not committed) |
| Corrected dropdown | `captures/help-menu-360.png` (not committed) |
| Interrupted read | `captures/network-error-360.png` (not committed) |
| Stopped agent | `captures/stale-host-360.png` (not committed) |

## Limits

No real guest, storage, hardware, appliance or ISO acceptance; hosted CI not run (both workflows
invoke the harness). Delivery: [matching-source rebuild](../controller-desktop-rebuild/checks.md);
deployed Controller VMs are not upgraded by new media. Captures are pinned to this build; current
look: [controller-ui-colors](../controller-ui-colors/checks.md).
