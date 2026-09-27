# Restore a backup over an existing VM

## Status

Development Phase 6 / S7. Restore replace keeps the original VM identity, retains the
original disk set until exclusive cutover, places restored disks beside those originals,
updates the definition, and leaves the guest shut off with disconnected NICs. Core refuses
powered or in-place overwrite. A disposable three-host replace walkthrough has not run.

## Prerequisites

- Completed recovery point on a reachable repository (with captured VM specification)
- Original VM defined on the target host and **shut off**
- Compatible storage with space for restored disks plus retained originals
- For UEFI or vTPM guests, a recovery point with complete matching NVRAM or
  vTPM state; Core refuses missing, corrupt or mismatched firmware artifacts

## Steps

1. Open **Backups**, select the recovery point, then **Replace**.
2. Choose host and storage. Confirm the original VM identity.
3. Review and submit. Follow the task until Core reports **succeeded** at stage
   `replaced` after exclusive cutover.

## Expected result

Artifacts are copied locally; originals stay on disk under Core's retain record. Exclusive
cutover writes restored disks into the VM's storage, switches the definition to those
files, and leaves the guest stopped with NICs disconnected. The restore pin clears only
after succeeded cutover. Core records retained original paths for inspection if cutover
fails after effects start (`needs_attention` with the exact stage); there is no automatic
recover-original action.

## Troubleshooting

- Running guest: shut the VM off first; Core refuses powered replace.
- Original missing: use [Restore as new](restore-as-new.md) instead.
- Firmware-state refusal: check the recovery point's NVRAM or vTPM artifacts;
  do not discard them to force replacement.
- Catalog or manifest mismatch: Core refuses a recovery point whose payload
  differs from the master's catalog digest before replace effects. Inspect
  the repository and catalog; do not edit the manifest or retry against a
  changed point. An older member Core cannot accept the catalog-bound restore
  request; retain the same operation for reconciliation after upgrading it.
- Pre-cutover failure: originals remain the live definition; inspect the failed operation
  before submitting another request.
- Cutover `needs_attention`: inspect the current VM definition and retained originals;
  do not submit another replace until the cutover outcome is reconciled.
- Controller absent: use host-local routes in [Recover without Controller](recover-without-controller.md).

## Verification

Unit coverage covers cutover success, pre-cutover original retention and running-guest
refusal. The [Phase 6 close record](../qa/2026-09-27-phase6-gate/CLOSED.md) records
bounded Campaign A/B evidence, but no disposable three-host replace walkthrough.
The later [post-close native review](../qa/2026-09-27-phase6-post-close-review.md)
restored a UEFI/vTPM recovery point **as a new VM** on a disposable member:
pre-start disk, NVRAM and TPM state matched, and isolated startup reached
UEFI firmware with swtpm. The blank disk did not prove guest OS boot or
sealed application-data recovery. That run did not exercise replace cutover;
native replace, rollback after an interrupted cutover and three-host
replacement remain open.
The [current candidate browser rewalk](../qa/2026-09-27-phase6-post-close-review.md#current-candidate-browser-rewalk)
checked Replace availability and typed confirmation for present source VMs,
and absence of the action for unavailable sources. It did not submit Replace.
