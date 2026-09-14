# Fixed command toolbar — verification

Date: 2026-09-10. Scope: the Controller shell's menu row and fixed toolbar from the
[desktop layout plan](../../plans/2026-09-09-controller-desktop-layout.md#fixed-command-toolbar--2026-09-10):
Back/Forward, Create site/Add host/New VM, the VM power group with its confirmation
dialog, Refresh view, and the new **Host** menu.

## Environment and method

Disposable HTTPS Controller and real agent from `tests/integration/phase1.py` (CachyOS, headless
Chromium, 1440×1000 and 360×800). The lab has no guests, so the power group uses a **simulated VM
record** from labeled browser route stubs (groups named "simulated"): UI behavior only; host power
execution is in the [console QA report](../console/checks.md).

## Commands and results

```sh
npm --prefix web run typecheck
npm --prefix web run build
GO="$PWD/.cache/toolchains/go/bin/go" NEX_QA_CAPTURES=<scratch dir> \
.cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser \
  --evidence docs/qa/controller-toolbar/results.json
```

Typecheck and build passed. The browser/process command exited zero with all **19 result
groups completed**, including the two new groups:

- *fixed toolbar*: on a host page the four power buttons are disabled with the tooltip
  `Select a virtual machine in Resources.`, **Add host** is disabled, **New VM** is enabled;
  on Task history **New VM** is disabled; **Back**/**Forward** move between the host page and
  Task history; **New VM** from the host page opens the pool's **Virtual machines** view with
  **Create VM · 1 of 6** shown.
- *simulated VM power*: with a fresh running VM the status line reads
  `Observed running · fresh`, **Start** is disabled and Shut down/Reboot/Force stop are
  enabled; **Force stop** opens the dialog `Force stop Toolbar fixture?` with the data-loss
  warning; **Cancel** and Escape close it without a request; a `409` rejection keeps the dialog
  open with the error; the retry submits the same `request_id` and, once accepted, closes the
  dialog and shows `Force stop requested for Toolbar fixture`; with the VM observed
  `shut off`, a rejected **Start** opens the dialog with the error, **Confirm start** retries
  with the same `request_id`, and focus returns to the toolbar's **Start** button.

The pre-existing groups now drive **Refresh inventory**, **Revoke access** and
**Re-enroll host** through the **Host** menu (including at 360 px width, where every menu
stays inside the viewport) and passed unchanged otherwise.

## Inspected captures

`desktop-shell`, `toolbar-new-vm`, `power-confirm-dialog`, `power-confirm-rejected`,
`host-360` and `revoked-host-360` were inspected: toolbar groups separated by thin rules,
greyed buttons with grey icons, the wizard opened by New VM, the modal dialog with backdrop
and its in-dialog error, and the toolbar wrapping to several rows at phone width. Captures
are not committed.

## Limits

- Power execution, Start of a stopped guest and the Controller-appliance warning text were
  not exercised against a real guest here; see the console QA report for those.
- At 360 px the fixed toolbar wraps to five rows rather than collapsing to icons.
- Independent review (read-only subagent) found: Escape during an in-flight request
  dropped its failure, a rejected Start greyed the whole group through the shell's outage
  flag, and focus was lost after a confirmed submit. All three were fixed (Escape is held
  while working, the dialog is the error surface for every action, the dialog closes after
  the button can take focus) and the last two are now asserted by the acceptance script.
  Remaining reviewer note: the greyed reasons for **Add host** and **New VM** live only in
  their tooltips, which keyboard users cannot reach.
