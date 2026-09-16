# Handover — Windows 11 defect batch and VM usage observation

Date: 2026-09-16
Release: **0.2.9** in the current worktree (`release-version.py --current` resolves
`0.2.9 0.2.9`). The local 0.2.9 artifacts are built and verified but are not tagged or published.

Everything below is uncommitted working-tree state. No commits were made; Git remains the project
owner's.

## What landed

Two independent batches converged on one tree.

**1. The Windows 11 UEFI defect batch.** All eighteen findings in
[the lab report](../qa/2026-09-16-windows-11-uefi-lab.md) fixed, recorded as BUG-0196 through
BUG-0213. Scope and the product decisions taken are in
[the lab-fixes plan](2026-09-16-windows-11-lab-fixes.md). Three further defects surfaced while
fixing and verifying:

| | |
|---|---|
| BUG-0214 | Fixed and verified. UUID-keyed NVRAM, legacy-path retention and journaled UEFI-to-BIOS cleanup prevent an orphan. |
| BUG-0215 | Fixed. A host without enrolled-keys firmware failed with an opaque libvirt error; Core now asks the host and refuses in terms of the setting. Found by the lab. |
| BUG-0216 | Fixed. The Go test suite issued commands to the developer's own hypervisor. Found by disrupting a real workstation. |

**2. VM usage observation (roadmap item 8).** Core samples each active VM with
`virsh domstats --raw --nowait` and reports CPU, memory and disk on the host inventory; Controller
validates and passes them through on `GET /api/v1/vms`. `--nowait` is load-bearing: a probe that
takes the domain's job stalls behind exactly the long shutdown BUG-0213 describes. Core computes
the CPU rate rather than the Controller, because `cpu.time` is cumulative and a Controller
subtracting two samples would be deriving pool state the host already knows. Absent is not zero —
fields libvirt declines are nullable. VM details renders the report, and real-browser acceptance
against deployed current Core/Controller passed for reported and null usage with no page errors.

## Verification, and what it does not cover

A disposable lab on a contributor workstation ran the **real Core engine** against real
libvirt 12.7 / QEMU 11.1 / swtpm 0.10.2 and a real Windows install. Full detail, including limits,
is in [the verification report](../qa/2026-09-16-windows-11-fix-verification.md).

The two results worth carrying forward:

- **Windows 11 Enterprise LTSC — the non-IoT edition the report says was refused — installed and
  booted**, with Windows reporting `IsActivated_InitialValue: TRUE, SpecVersion: 2.0`.
- **BUG-0213 reproduced and fixed under A/B on one guest**: the pre-fix binary returned
  `wall=20.138s`, `needs_attention`, `virsh failed: signal: killed`; the fixed binary returned
  `wall=1m0.073s`, `succeeded`. The guest reached `shut off` 17 s after the pre-fix run had
  declared failure, proving that report was false.

That earlier engine-only run was not Nex Core. Later deployed Controller and installer acceptance
closed the eight Controller/installer items: BUG-0197, BUG-0202, BUG-0203, BUG-0204, BUG-0206,
BUG-0207, BUG-0208 and BUG-0209; BUG-0212 exact typing also passed. See [final ISO and Controller
acceptance](../qa/2026-09-16-final-iso-controller-acceptance.md).

## Decisions taken

All three were settled by project owner on 2026-09-16. None is left open.

1. **TPM removal stays refused**, confirming the implemented behaviour. Removing the device
   destroys whatever the guest sealed to it, and libvirt cleans TPM state by iterating the devices
   the persistent definition still carries, so a redefine without `<tpm>` orphans
   `/var/lib/libvirt/swtpm/<uuid>` with nothing able to remove it. An operator who wants no TPM
   deletes the VM and creates it again. Recorded in
   [the lab-fixes plan](2026-09-16-windows-11-lab-fixes.md#product-decisions-this-change-makes).
2. **BUG-0214 was assigned to the firmware variable-store slice**, which now passed its probe and
   lifecycle checks. It keeps firmware changeable on a stopped VM, journals cleanup before the
   destructive boundary, and removes only the validated recorded NVRAM file. Recorded on BUG-0214
   in the ledger and in the debts table.
3. **Firmware stays changeable on a stopped VM; the guide was wrong, not the code.**
   `NeedsStop` has always treated it as an ordinary stop-required change, while
   `create-virtual-machine.md` claimed it could not be changed after creation. The guide is
   corrected and still warns that a guest installed under one firmware will usually not boot under
   the other. No UI warning was added — documentation-only, by the same decision.

## Open work, in priority order

The [VM foundation debts](2026-09-14-production-readiness-handoff.md#vm-foundation-added-2026-09-16)
table is the single tracker; it was reconciled with the lab outcome. Summarised:

1. **Windows TPM/Secure Boot independence matrix.** Open. Fresh 60 GiB cases created, booted
   and deleted with the expected live XML combinations, but Windows Setup did not reach the
   non-IoT requirements decision. Optical-prompt timing and the manual Setup flow limited the
   harness; do not infer guest acceptance from lifecycle or definition checks.
2. **BUG-0213 graceful-shutdown timing.** The historical ~215 s task gap has no recoverable
   cause, and the current final-package attempt did not submit shutdown because guest-agent
   readiness did not arrive within bounded attempts.

The firmware probe and lifecycle test now pass, including the UEFI rename and UEFI-to-BIOS cleanup
path. The Windows system-disk test also passes: Setup showed no disk until the Red Hat VirtIO SCSI
driver was loaded from virtio-win 0.1.302 on the second CD drive, then exposed the 64 GiB VirtIO
disk and completed to OOBE. The disposable VM and disks were deleted. See [the firmware and
VirtIO verification](../qa/2026-09-16-firmware-variable-store-verification.md).

## Where to look

| | |
|---|---|
| What was found | `docs/qa/2026-09-16-windows-11-uefi-lab.md` |
| What was verified, and its limits | `docs/qa/2026-09-16-windows-11-fix-verification.md` |
| Firmware and VirtIO system-disk verification | `docs/qa/2026-09-16-firmware-variable-store-verification.md` |
| Scope and product decisions | `docs/plans/2026-09-16-windows-11-lab-fixes.md` |
| Open debts, single tracker | `docs/plans/2026-09-14-production-readiness-handoff.md` |
| Per-defect detail and regression evidence | `docs/BUG_LEDGER.md`, BUG-0196 … BUG-0216 |
| Contract changes | `docs/API.md`, `docs/api/openapi.json`, `peer-openapi.json` |
| Release notes | `CHANGELOG.md`, `## [0.2.9]` |

Change records: `docs/changes/2026-09-16-*.json` — one per lane, plus
`power-and-shared-contract.json` covering the shared protocol contract, the power fix, the ledger
and the API reference.

## State of the checks

`go build ./...`, `go vet ./internal/...` and `go test -race ./internal/...` pass. All four web
suites, the TypeScript typecheck and the web build pass. The API contract suite passes 29/29.
`python3 scripts/check_docs.py --worktree` passes. The repository privacy test passes.

The Python suite has one error, `test_website_hosting`, which requires nginx; CI installs it and
this workstation does not have it. It is unrelated to either batch.

API impact: **changed contract** — covered by the individual change records. Guide impact:
**updated** — covered likewise. State owner: **none** for this document; it is a handover.
