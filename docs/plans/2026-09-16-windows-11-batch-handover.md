# Handover — Windows 11 defect batch and VM usage observation

Date: 2026-09-16
Release: **0.2.8** (newest `CHANGELOG.md` heading; `release-version.py --current` resolves
`0.2.8 0.2.8`). Nothing is tagged or published.

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
| BUG-0214 | **Open, assigned.** A UEFI→BIOS edit can orphan a VM's firmware variable file. Closed by the firmware variable-store slice, by decision below — not fixed in this batch. |
| BUG-0215 | Fixed. A host without enrolled-keys firmware failed with an opaque libvirt error; Core now asks the host and refuses in terms of the setting. Found by the lab. |
| BUG-0216 | Fixed. The Go test suite issued commands to the developer's own hypervisor. Found by disrupting a real workstation. |

**2. VM usage observation (roadmap item 8, Core half).** Core samples each active VM with
`virsh domstats --raw --nowait` and reports CPU, memory and disk on the host inventory; Controller
validates and passes them through on `GET /api/v1/vms`. `--nowait` is load-bearing: a probe that
takes the domain's job stalls behind exactly the long shutdown BUG-0213 describes. Core computes
the CPU rate rather than the Controller, because `cpu.time` is cumulative and a Controller
subtracting two samples would be deriving pool state the host already knows. Absent is not zero —
fields libvirt declines are nullable. **No UI**, so roadmap item 8 stays open.

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

**This was not Nex Core.** No ISO was built, no Controller ran, `nex-shell.c` was never compiled,
and the library versions are newer than Core ships. Concretely, these remain verified only by
automated checks and code review: BUG-0202's offered eject, BUG-0203, BUG-0204, BUG-0206,
BUG-0207, BUG-0208, BUG-0209, and BUG-0212's actual console control. The typing fix was proved by
sending the same string into the guest through an equivalent implementation, not through
`web/src/console.tsx`.

## Decisions taken

All three were settled by project owner on 2026-09-16. None is left open.

1. **TPM removal stays refused**, confirming the implemented behaviour. Removing the device
   destroys whatever the guest sealed to it, and libvirt cleans TPM state by iterating the devices
   the persistent definition still carries, so a redefine without `<tpm>` orphans
   `/var/lib/libvirt/swtpm/<uuid>` with nothing able to remove it. An operator who wants no TPM
   deletes the VM and creates it again. Recorded in
   [the lab-fixes plan](2026-09-16-windows-11-lab-fixes.md#product-decisions-this-change-makes).
2. **BUG-0214 is assigned to the firmware variable-store slice**, not fixed separately. That slice
   already owns the NVRAM path and is gated on the firmware probe that decides its implementation,
   so closing the orphan there avoids building a mechanism the slice would immediately replace.
   Explicitly *not* closed by refusing UEFI→BIOS edits, because of decision 3. Recorded on
   BUG-0214 in the ledger and in the debts table.
3. **Firmware stays changeable on a stopped VM; the guide was wrong, not the code.**
   `NeedsStop` has always treated it as an ordinary stop-required change, while
   `create-virtual-machine.md` claimed it could not be changed after creation. The guide is
   corrected and still warns that a guest installed under one firmware will usually not boot under
   the other. No UI warning was added — documentation-only, by the same decision.

## Open work, in priority order

The [VM foundation debts](2026-09-14-production-readiness-handoff.md#vm-foundation-added-2026-09-16)
table is the single tracker; it was reconciled with the lab outcome. Summarised:

1. **Firmware variable store probe.** Gates the firmware slice and must run first, on a host that
   may be rebooted. Untouched by this batch.
2. **VirtIO system-disk install using the driver disc.** The one unmet clause of this plan's own
   acceptance. virtio-win was delivered on the second drive and installed after the fact, but the
   guest's system disk was SATA, so loading a storage driver at Setup's disk-selection step was
   never exercised — which is the half W3 was actually about.
3. **Secure Boot taking effect in a guest**, and the TPM and Secure Boot failing independently.
4. **Enrolled Secure Boot certificates.** The lab's host had no enrolled-keys build, so one was
   generated locally. Those are not Microsoft's certificates and prove nothing about Core's
   shipped template. The 2011 UEFI CA expired in June 2026.
5. **The six Controller/installer defects** above, which need a running Controller and an ISO build.
6. **VM usage UI**, and running the observation against real libvirt.
7. **BUG-0213's unexplained delay.** The original entry's ~4 minute gap between task creation and
   failure is still unaccounted for; the lab reproduced a 20 s kill, not that delay.

## Where to look

| | |
|---|---|
| What was found | `docs/qa/2026-09-16-windows-11-uefi-lab.md` |
| What was verified, and its limits | `docs/qa/2026-09-16-windows-11-fix-verification.md` |
| Scope and product decisions | `docs/plans/2026-09-16-windows-11-lab-fixes.md` |
| Open debts, single tracker | `docs/plans/2026-09-14-production-readiness-handoff.md` |
| Per-defect detail and regression evidence | `docs/BUG_LEDGER.md`, BUG-0196 … BUG-0216 |
| Contract changes | `docs/API.md`, `docs/api/openapi.json`, `peer-openapi.json` |
| Release notes | `CHANGELOG.md`, `## [0.2.8]` |

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
