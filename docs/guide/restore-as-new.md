# Restore a backup as a new VM

## Status

Development Phase 6 / S4 Controller UI for restore-as-new from a completed recovery
point. Core copies artifacts to local storage, reconstructs the VM from the captured
specification in the recovery-point manifest, places disks on the selected storage, and
registers a new stopped VM with a new identity and disconnected NICs. Points published
before captured-spec storage must be re-backed up before restore-as-new. Replace-existing
exclusive cutover is covered separately in [Restore a backup over an existing VM](restore-replace.md).

## Prerequisites

- At least one completed recovery point on a reachable backup repository
  ([Back up a VM now](backup-now.md)).
- Compatible VM-ready storage with enough space for every restored disk.
- Matching Controller and Core builds that expose backup restore routes.
- For UEFI or vTPM guests, a recovery point with complete matching NVRAM or
  vTPM state. Missing, corrupt or mismatched state is refused.

## Steps

1. Open **Backups** → **Recovery points**. Use Search, VM or Repository in the
   sidebar if needed; on a smaller screen, open **Filters** first.
2. Select a recovery point or its **Details** button. Inspect its source, capture
   time, repository, consistency, deletion protection and last test. Selection
   only opens details; it does not submit a restore.
3. Select **Restore…** and keep the default **Restore as new** choice.
   In **Destination**, enter a **New VM name** and choose **Host** and **Storage**,
   then select **Review restore**.
4. In **Review and submit**, check name, new VM ID, host, storage and the stopped,
   disconnected-network defaults. Select **Restore as new** in the fixed footer.
   Follow the task; when it succeeds, open the new VM from **VMs** before connecting networks.

## Expected result

A new stopped VM is registered with the chosen name and storage. Source guest identity
and MACs are not reused. Connecting NICs or starting the guest is a separate operator
choice after inspection.

## Troubleshooting

- Source VM missing from inventory: restore still works from the catalog entry; the
  list marks the source as unavailable.
- Storage list empty or refused: pick storage Core reports usable for the target host.
- Recovery point not found: the task fails before a restore run, pin or VM is
  created. Recheck the selected repository and point in the catalog; if the
  point is imported later, start a new restore request.
- Firmware-state refusal: check that the recovery point contains complete,
  intact NVRAM or vTPM state; do not discard an artifact to bypass the refusal.
- Catalog or manifest mismatch: Core refuses a point whose repository manifest
  no longer matches the master's catalog digest. Check the repository and
  catalog for an intact point before starting another restore; do not edit a
  manifest to make it match.
- A restore that stops before placement, for example on a repository read
  failure, fails and releases its recovery point; start a new request. Core
  removes the restore's local artifact copies once it succeeds or fails.
- Restore remains `needs_attention` during allocation: keep the same VM and
  operation ID, and inspect the host task. The original Core can resume the
  exact retained allocation after restart and verify the result. Do not submit
  a changed target or assume a promoted master can repeat native work.
- An uncertain reply freezes the destination and restore choice. Inspect **Tasks**
  before **Retry restore request**, which resends the same target and request
  identity. Back and Close remain disabled; do not create another VM to resolve
  the original outcome.
- Static guest IPs or hostnames inside the image are unchanged; review before attaching
  production networks.
- Replace existing VM: [Restore a backup over an existing VM](restore-replace.md).
- Controller absent: [Recover backups without Controller](recover-without-controller.md).

## Verification

The [product NFS lab](../qa/2026-09-27-phase6-product-backup-lab.md) exercised
restore-as-new with original disks unavailable. [Campaign A](../qa/2026-09-27-phase6-campaign-a.md)
exercised Core-local restore during a Controller outage. The original S4 guide change
did not include a browser walkthrough; see the later [browser report](../qa/2026-09-27-phase6-backup-browser.md)
for its exact scope.
The later firmware restore implementation has unit coverage but has not yet
been walked through with a guest OS and sealed application data. A later
[bounded native review](../qa/2026-09-27-phase6-post-close-review.md) restored
the original UEFI/vTPM operation on a disposable member and reached UEFI
firmware with matching pre-start disk/NVRAM/TPM artifacts. The blank disk did
not prove OS boot or application recovery.

The [backup workspace review](../qa/2026-10-01-backup-ui/README.md) exercised
inspection without a write, the two-step restore flow, retained drafts, missing
source handling and exact retry with fictional API responses. Queued replies in
that review are not proof of native restore, guest boot or application recovery.
