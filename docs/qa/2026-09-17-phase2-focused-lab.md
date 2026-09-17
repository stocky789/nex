# Phase 2 focused lab re-checks on published 0.3.1

Date: 2026-09-17. Disposable nested-KVM fresh install from
`dist/nex-core-0.3.1-amd64.iso` (SHA-256
`2cf73f837c3a4f6be0fa56443179acdd01897fdd91832540cc445a6bc45b0f75`).

## Result

All four focused probes passed:

| Probe | Result |
| --- | --- |
| Oversized create (2000 GiB) | Failed with `insufficient free storage including reserve` |
| Dedicated-memory admission (14 GiB on 16 GiB host with appliance) | Failed with dedicated-memory headroom refusal |
| Running remove+add NIC | Succeeded |
| BUG-0256 add-only after remove (c2) | Succeeded without duplicate MAC on `br0`; live MACs `:00`–`:03` distinct (`:00` still live as orphan, not reused) |

Evidence (lab cache, not published images): 
`.cache/phase2-gate6-evidence-20260917T105522Z-retry1/phase2-focused-probes.json`,
`mac-live-check.json`, `oversized-create.json`, `memory-admission.json`.

Harness: `tests/integration/phase2_focused_probes.py` against the deployed Controller.

## Limits

- Guest OS install/reboot/data persistence was not re-run; it already passed on gate5
  (`0.2.10~gate5`) and is unchanged by the MAC fix.
- Empty-pool table screenshots and queued/failed create page residuals were accepted as
  Phase 2 polish residuals at close ([close record](2026-09-17-phase2-gate/CLOSED.md)).
- **Phase 2 closed 2026-09-17** ([close record](2026-09-17-phase2-gate/CLOSED.md)).
- VirtIO bundling shipped on `dist/nex-core-0.3.2-amd64.iso` (SHA-256
  `bf9900d2970de4cb4601a57b6de6b412a4c42bd1746ad25bb0b23e90d27bc10e`): ISO carries
  `/nex/media/virtio-win.iso` (877373440 bytes, SHA-256
  `303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d`) plus
  `/nex/third-party/virtio-win/` NOTICE and matching Fedora source RPM tree; the
  release source bundle retains the same under `provided-sources/third-party/virtio-win/`.
