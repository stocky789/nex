# CPU recommendation label

Date: 2026-09-19. Development UI; no deployment or hardware compatibility claim.

The Create VM and Edit VM CPU selectors no longer render raw feature flags.
The available pool baseline is labeled `(Recommended)`. A saved selection receives
that label only when model, machine and feature policies match the current available
baseline. Manual models and unavailable or different saved baselines remain unlabeled.
CPU payloads and Core persistence are unchanged.

## Verification

- `npm --prefix web run typecheck` and `npm --prefix web run build` passed.
- `node web/src/vm-create.test.mjs` and `node web/src/vm-edit.test.mjs` passed.
  Coverage includes feature order, duplicates, model/machine mismatch, unavailable
  baseline and absent feature text while preserving the saved CPU definition.
- `.cache/phase1-qa-venv/bin/python tests/integration/managed_vm_browser.py --simulated --evidence .cache/cpu-recommended-create`
  passed the full walkthrough using the real Controller and simulated Core.
  The CPU check asserts no feature dump, the recommended default, removal of the
  recommendation when discovery is unavailable, and explicit/manual selection.
- `.cache/phase1-qa-venv/bin/python tests/integration/vm_edit_browser.py --evidence .cache/cpu-recommended-edit`
  passed with the real editor and a simulated Core, including stopped/running guards,
  successful edits and narrow layout. CPU-specific assertions check the recommendation
  and absent feature dump.

Limits recorded by the edit run's own `.cache/cpu-recommended-edit/results.json`: it ran against
a simulated host only, with no libvirt redefine, hot-plug, media change or disk allocation
executed; and the pending-edit refusal (409) was exercised in Go tests, not in the browser.

The create baseline and edit CPU captures were visually inspected. In creation, the selected CPU name ends in
`(Recommended)` and the old feature paragraph is absent. Captures remain local under
the evidence directories above. Existing browser scenarios run in both providers'
foundation workflows. No hosted CI result or live host operation is claimed.

API impact: none; presentation only. State owner: none; no new durable record.
Guide impact: updated create/edit CPU instructions.
