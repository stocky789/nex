# Windows 11 UEFI lab fixes

Date: 2026-09-16
Status: implemented and accepted for this defect batch. All eighteen findings are fixed and
recorded as BUG-0196 through BUG-0213. Three further defects surfaced while fixing and verifying
them: BUG-0214, BUG-0215 and BUG-0216 are also fixed. Disposable labs verified the Core-side fixes
against real libvirt, QEMU and Windows, and deployed Controller, installer and browser acceptance
closed their applicable checks. See the [verification report](../qa/2026-09-16-windows-11-fix-verification.md),
[final ISO and Controller acceptance](../qa/2026-09-16-final-iso-controller-acceptance.md) and the
[handover](2026-09-16-windows-11-batch-handover.md).

## Authorized scope

[The lab report](../qa/2026-09-16-windows-11-uefi-lab.md) recorded eighteen issues found
installing Windows 11 IoT Enterprise LTSC under UEFI on a fresh Nex Core 0.2.7 host. Its
"Fix directions" section was explicitly written for triage, not as decisions.

Project owner, 2026-09-16: fix all eighteen. The missing virtual TPM (W1) is called out
separately as the blocker to record and fix first, because it is the reason the standard
Windows 11 editions cannot install at all.

This is defect work against the existing VM foundation. It does not open Phase 4, migration,
master transfer or HA, and it does not expand the current milestone beyond the reported issues.

## Product decisions this change makes

The report offered alternatives. These are the choices taken, so later work does not re-litigate
them:

- **Emulated TPM 2.0 is a per-VM option, not an unconditional device.** Nex ships `swtpm` on
  Core and emits a `tpm-crb` device only when the VM's definition asks for it. The Create VM
  wizard defaults it on for a Windows guest, because Windows 11 refuses to install without it,
  and off otherwise. A TPM changes the guest's measured boot and can bind BitLocker keys, so
  adding one to every VM silently is not acceptable.
- **Secure Boot becomes an explicit, stated property of the definition** rather than something
  libvirt infers from `firmware='efi'`. A definition that predates the field keeps Secure Boot
  on with enrolled keys, which is exactly what libvirt already chose for it; nothing that has
  Windows installed has its firmware changed underneath it.
- **Every VM gets a second CD drive** instead of Windows-specific device defaults. The report's
  alternative was defaulting Windows guests to SATA and E1000E so inbox drivers cover them. That
  trades away VirtIO performance permanently to work around a missing drive. A second drive lets
  the virtio-win disc sit beside the install disc, which is what the operator actually needed,
  and it removes the mid-Setup disc-swap workaround rather than documenting it.
- **The post-install install-media eject is an offered action, not an automatic one.** Nex does
  not guess that an install finished and quietly rewrite a VM's definition.
- **`guest.resize` is reported from the guest, not from the device model.** A capability Nex
  advertises must be one the guest can actually perform.
- **A TPM cannot be removed from a VM once it has one.** Decision: project owner, 2026-09-16,
  confirming the implemented behaviour. libvirt cleans TPM state by iterating the devices the
  persistent definition still carries, so a redefine without `<tpm>` orphans
  `/var/lib/libvirt/swtpm/<uuid>` — the endorsement key and anything the guest sealed to it,
  such as a BitLocker key — with nothing able to remove it afterwards. Refusing the transition
  keeps the definition and the host state in step, so deletion always removes what the VM
  created. An operator who wants no TPM deletes the VM and creates it again. Migration cost is
  zero: the field is new in this change, so no VM has ever had one removed.
- **Firmware can be changed on a stopped VM; the guide was wrong, not the code.** Decision:
  project owner, 2026-09-16. `NeedsStop` has always treated firmware as an ordinary
  stop-required change, while `create-virtual-machine.md` claimed it could not be changed after
  creation. The guide is corrected to match, and still warns that a guest installed under one
  firmware will usually not boot under the other. No UI warning was added; the guidance is
  documentation-only by the same decision.

## Durable state

State owner: Core, for everything that is part of a VM. The Secure Boot choice, the TPM choice
and the second drive's media ID are fields of the VM definition, which lives in the pool master's
managed journal and returns to a fresh Controller through the existing inventory and adoption
path. No new Controller-owned record is added; the emulated TPM's own state lives on the host
under libvirt's `swtpm` directory alongside the NVRAM it already keeps per VM, and is removed
with the VM.

The auto-registered local repository's name (W10) is already a pool-master record. Only the
default applied at first registration changes; no existing registration is renamed or migrated.

## API impact

Changed contract. `VMBoot` gains `secure_boot` and `tpm`; `VMCreateSpec` gains
`drivers_media_id`; the VM media request gains `drive`; `GuestInfo` gains `drivers_media` and
redefines `resize`. Media gains a delete operation. All new fields are omitted-by-default so an
older Controller and an older Core keep their current behaviour, and hosts advertise the second
drive through a capability rather than being assumed to have it.

## Guide impact

Updated. Creating and editing a VM (firmware, Secure Boot, TPM, driver disc), the VM console
(typing into a Windows guest, starting from the console, choosing a drive, when resize is
unavailable), VM power (ejecting install media after an install), storage repositories
(removing a failed ISO), guest tools and the installer screens.

## Work ownership

| Issues | Lane |
|---|---|
| W1, W3 (Core), W4, W15 | Core VM definition: domain XML, firmware features, TPM, second CD drive, Windows enlightenments, `swtpm` packaging |
| W18 | Core power: virsh command timeouts and reconciling an interrupted shutdown from observed state |
| W5, W8, W9, W10 | Media library: download error reporting, media deletion, upload banner state, default repository name |
| W6, W14, W16 | Guest observation and installer: resize truth, corrected Windows product name, installer step list |
| W2, W12, W17, W3 (console) | VM console: shifted-character typing, layout, start from console, drive selection |
| W1/W3/W4 (UI), W7, W9, W11, W13 | VM wizard, edit form, details page, login session probe |

Work ownership follows [AGENTS.md](../../AGENTS.md#agent-workflow). Each lane records its own
`docs/changes/*.json` assessment; the bug ledger and API reference are appended centrally to
keep IDs unique.

## Acceptance

Every fix ships a runnable regression check that fails without it, recorded in
[the bug ledger](../BUG_LEDGER.md) as BUG-0196 through BUG-0213 with its actual command and
result.

**Acceptance status, 2026-09-16.** All three walkthrough clauses passed across the recorded
disposable-lab runs: non-IoT Windows 11 Enterprise LTSC installed with a TPM present; the fixed
shutdown reported success after the pre-fix binary reproduced the false failure on the same guest;
and a fresh Windows install loaded the Red Hat VirtIO SCSI driver from the second CD drive, exposed
the 64 GiB VirtIO system disk and reached OOBE. See the
[firmware and VirtIO verification](../qa/2026-09-16-firmware-variable-store-verification.md).

The original engine-only run used libvirt 12.7 / QEMU 11.1 rather than an installed Nex Core.
Later final-package and deployed-Controller checks are recorded separately. The independent
Windows TPM/Secure Boot requirements matrix and current BUG-0213 graceful-shutdown timing remain
open in the [production-readiness tracker](2026-09-14-production-readiness-handoff.md#vm-foundation-added-2026-09-16).

## Follow-up after acceptance — bundled virtio-win (2026-09-16; reopened 2026-09-17)

The second CD drive fixed the install path; the recorded lab used manually imported virtio-win
media. The 2026-09-16 staging, host-seeding and wizard-preselection rollout was implemented and
unit-tested, but has been reopened: the public 0.2.9 ISO excludes the bundled ISO because complete
corresponding source and a tested delivery path for its GPL/LGPL contents are not retained.

Automatic installer staging, host seeding and wizard pre-selection remain blocked until that
source/legal closure is complete. Until then, operators import matching media as a normal
`iso-import` and select it as **Driver disc**. The second-drive contract and
`drivers_media_id` validation are unchanged (API impact: existing contract; no schema change).
The historical manual-import lab evidence remains valid; it does not verify the withdrawn
bundled-media rollout.

Driver disc from the console, 2026-09-17: console insert and eject of the driver disc failed with
400 (BUG-0235, fixed for 0.3.0; not in the published 0.2.9 release). The same-ISO refusal was
also included in 0.3.0: Controller and Core validate that the driver disc differs from install
media. Its focused regression tests establish the refusal; this historical lab did not rerun a
native driver-disc insert after that follow-up.
