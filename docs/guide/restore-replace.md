# Restore a backup over an existing VM

## Status

Development Phase 6 / S7. Restore replace keeps the original VM identity, retains the
original disk set until exclusive cutover, places restored disks beside those originals,
updates the definition, and leaves the guest shut off with the VM's current NICs and MAC
addresses. Core refuses powered or in-place overwrite. A disposable replace walkthrough ran
on a three-host lab (restore, repeat replace, snapshot revert); it found the defects fixed
here, and the fixed build has not been walked again.

## Prerequisites

- Completed recovery point on a reachable repository (with captured VM specification)
- Original VM defined on the target host and **shut off**
- Compatible storage with space for restored disks plus retained originals
- For UEFI or vTPM guests, a recovery point with complete matching NVRAM or
  vTPM state; Core refuses missing, corrupt or mismatched firmware artifacts

## Steps

1. Open **Backups** → **Recovery points**, select the point or its **Details**
   button, inspect it, then select **Restore…** → **Replace original VM**.
2. In **Destination**, type the original VM's current name exactly to confirm.
   Choose its current host and suitable storage. The VM and host must have fresh
   observations, and the VM must be **shut off** before **Review restore** is enabled.
3. In **Review and submit**, check the original identity, destination and retained
   current NICs/MACs, then select **Replace original VM** in the fixed footer.
   Follow the task until Core reports **succeeded** at stage `replaced` after exclusive cutover.

## Expected result

Artifacts are copied locally; originals stay on disk under Core's retain record. Exclusive
cutover writes restored disks into the VM's storage, switches the definition to those
files, and leaves the guest stopped. The VM keeps its current NICs and MAC addresses (the
recovery point's are not used), so DHCP or MAC-bound guests keep their identity. The restore pin clears only
after succeeded cutover. Core records retained original paths for inspection if cutover
fails after effects start (`needs_attention` with the exact stage); there is no automatic
recover-original action.

Every replace keeps the previous disk set (and, for a vTPM guest, the previous vTPM state).
Repeated replaces accumulate one retained set each (about the size of the VM's disks) in the
VM's storage directory until you release them, so plan capacity for it.

## Release retained originals

Once the restored VM is verified and you no longer need the pre-replace state, free its
space. A release deletes the original disks and vTPM state that every earlier replace kept
for the VM, and any disk a VM edit detached; it never touches the VM's current disks.
Deleted files cannot be recovered, so keep a backup of anything you may still need.

1. Shut the VM off and wait for its state to show **Shut off**.
2. Open the VM, then **Definition** and select **Release retained original**.
3. Type the VM name and confirm. Follow the task in **Tasks** until Core reports **succeeded**.

Core on the VM's host runs the release and records it, so it continues if the browser or
Controller disconnects, and sending the same request again returns the same outcome. It fails,
changing nothing, with the reason when the name is wrong, the VM is not shut off, the VM has
snapshots (delete them first), a file is still used by another VM or lies outside the VM's
storage directory, or the VM has no retained originals. While a restore, restore-replace, test restore, edit or snapshot
operation on the VM is queued or unfinished the request is refused, so release only after the
replace has succeeded and try again. Without the Controller, use the host-local route
`POST /api/local/v1/pool/vms/{id}/release-retained` with `operation_id` and `confirmation`
(the VM name) on the host that runs the VM. The button appears for every VM, because Nex does
not list what is retained; a VM with nothing retained fails with "VM has no retained
original disks".

## Troubleshooting

- Storage filling after several replaces: [release the retained originals](#release-retained-originals).
- Running guest: shut the VM off first; Core refuses powered replace.
- Original missing: use [Restore as new](restore-as-new.md) instead.
- Firmware-state refusal: check the recovery point's NVRAM or vTPM artifacts;
  do not discard them to force replacement.
- Catalog or manifest mismatch: Core refuses a recovery point whose payload
  differs from the master's catalog digest before replace effects. Inspect
  the repository and catalog; do not edit the manifest or retry against a
  changed point.
- Pre-cutover failure: originals remain the live definition; inspect the failed operation
  before submitting another request. A repository read failure before Core records
  the retained originals fails the run and releases the recovery point.
- Cutover `needs_attention`: inspect the current VM definition and retained originals;
  do not submit another replace until the cutover outcome is reconciled.
- An uncertain reply freezes the selected point, original VM and destination.
  Inspect **Tasks** before **Retry restore request**; keep the same request rather
  than starting another replacement.
- Controller absent: use host-local routes in [Recover without Controller](recover-without-controller.md).

## Verification

Unit coverage covers cutover success, pre-cutover original retention and running-guest
refusal. Release retained original is covered by engine, agent, Controller route and web unit
checks (deleting only detached files, keeping live disks, confirmation, running-guest
refusal, replay); it has not been walked in a browser or on a real replaced VM. The [Phase 6 close record](../qa/2026-09-27-phase6-gate/CLOSED.md) records
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

The [backup workspace review](../qa/2026-10-01-backup-ui/README.md) checked
wrong-name and stale-VM blockers, unavailable-source refusal, retained-NIC review
and replacement submission with fictional API responses. It did not execute a
native cutover or release retained originals.
