# Manage VM snapshots

## Status

This recovery development workflow covers stopped capture, corrected stopped revert for one
disposable two-disk UEFI/Secure Boot/vTPM guest, leaf delete, catalog rename, and Controller
VM Snapshots tab create/list/rename/revert/delete for the recorded lab scope. Exact same-operation
retry after a corrected stopped revert settled without a second native revert, and guest disk
markers matched the retained point. Parent points with native children are refused before effects.

Running create admits crash-consistent capture on supported BIOS guests and refuses UEFI
running capture in this development slice. Quiesced mode requires a responding guest agent and
fails honestly when the agent is absent; it does not silently fall back to crash. A snapshot
depends on its original storage and is not an independent backup.

## Prerequisites

- A selected disposable development host with matching Core components, a reachable current
  pool master, and a managed VM assigned to that host.
- Use matching development Controller and Core builds for inventory and adoption checks.
  Update Controller first: an older development build can reject the new snapshot report
  fields and show stale inventory. This does not release a Core recovery reservation.
- Root access to the host-local HTTPS service, with its certificate verified. Use its normal
  authenticated session, same-origin and CSRF controls.
- For stopped create, revert or delete: a shut-off VM, available storage for every disk and
  required firmware state, and no unresolved operation for that VM. Core validates the complete
  writable disk set before capture.
- For running create: a running BIOS guest with available storage and no unresolved operation.
  Choose crash-consistent or quiesced. Quiesced needs a working guest agent channel.

## Steps

### From Nex Controller

1. Open the VM and select the Snapshots tab.
2. For a shut-off VM, enter a name (and optional description) and take a snapshot. For a
   running BIOS guest, choose crash-consistent or quiesced, then take the snapshot. Follow
   the resulting task until it succeeds or fails with a recorded cause.
3. Rename, revert or delete from the same list. Revert and delete need a stopped guest; delete
   is leaf-only.
4. To snapshot several shut-off VMs at once, select their checkboxes on the **VMs** list and
   choose **Snapshot**. Enter one shared name; the confirmation lists each selected VM as
   *Will be requested* or skipped (running guests, unknown/stale VM state, hosts that are not
   freshly connected, or the Controller appliance). Each VM gets its own create task; retry
   after a failure resubmits only the failed ones with the same name. Running capture still
   uses each VM's Snapshots tab.

### From the host-local API

1. Confirm the VM state matches the intended mode (stopped for default create; running for
   `crash` or `quiesced`). Record the VM's stable ID.
2. Create a unique operation ID using 1–64 letters, digits, underscores or hyphens, starting
   with a letter or digit. Retain it for status checks and retries; it is also the snapshot ID.
3. Send `POST /api/local/v1/pool/vms/{id}/snapshots` to the assigned host with
   `operation_id`, a descriptive `name`, optional `description`, and optional `mode`
   (`stopped`, `crash`, or `quiesced`). Do not send a host ID, VM definition or disk paths;
   Core resolves these from its own records.
4. A `202` response means Core saved the request. Follow
   `GET /api/local/v1/pool/vms/{id}/snapshot-operations/{operation_id}` until its result settles.
   Closing the client or disconnecting Controller does not cancel accepted work.
5. Read `GET /api/local/v1/pool/vms/{id}/snapshots`. A completed capture is listed only after
   the pool master accepts the matching host evidence. Check the name, creation time and
   captured specification revision.

For the stopped-revert development procedure:

1. Select the retained snapshot from the authoritative catalog and shut down the VM.
   Reverting discards the current disk and firmware state from the active VM; confirm the
   selected VM and recovery point before submitting the request.
2. Choose a fresh operation ID, different from the selected snapshot ID. Send
   `POST /api/local/v1/pool/vms/{id}/snapshots/{snapshot_id}/revert` to the assigned host
   with only `operation_id`. Core resolves the retained configuration and disk paths.
3. Follow the same operation-status endpoint until it reports `succeeded`. A `202` response
   records acceptance, not completion. Keep the exact request for retries.
4. Inspect the restored configuration before starting the VM. Core restores the captured
   execution configuration and paired firmware state while preserving the current VM identity,
   name, description, tags and autostart setting. The VM remains stopped after revert.
5. Start the VM through its normal power control and verify the required guest data and
   application state. A successful request alone does not prove guest recovery.

For the stopped leaf-delete development procedure:

1. Select a retained leaf point from the authoritative catalog (no native children) and
   shut down the VM. Deleting the current leaf consolidates its overlays into their parents
   with public blockcommit without `--delete`, then removes metadata and unlinks only
   overlays proven unreferenced.
2. Choose a fresh operation ID, different from the selected snapshot ID. Send
   `POST /api/local/v1/pool/vms/{id}/snapshots/{snapshot_id}/delete` to the assigned host
   with only `operation_id`.
3. Follow the operation-status endpoint until it reports `succeeded`. Keep the exact request
   for retries; an exact retry past reservation must not repeat native effects.
4. Confirm the catalog no longer lists the deleted point. Parent points with children remain
   unsupported in this slice.

To rename or redescribe a retained point without changing its disks:

1. Read the catalog entry and choose a unique name (and optional description).
2. Send `PATCH /api/local/v1/pool/vms/{id}/snapshots/{snapshot_id}` to the assigned host with
   `name` and optional `description`. Capture digests and creation time stay fixed.
3. Confirm the catalog lists the new name. Unresolved recovery on that VM or a duplicate name
   is refused.

## Expected result

The operation reports `succeeded` after native disk and required firmware checks and master
confirmation. Stopped create leave the VM stopped; running create leave it running.
The master retains the snapshot identity, name, description and capture descriptor; the
executing host retains detailed capture and disk-chain records. A replacement Controller
obtains pool inventory from the master.

Core permits one background snapshot operation per host in this slice. Unrelated VM controls
remain available. Conflicting changes to the protected VM or its storage are held; emergency
Shut down and Force stop remain available. Managed pool VMs keep native libvirt autostart
disabled so a restart cannot bypass Core's ownership admission. VM deletion is refused while
retained snapshots exist, even when keeping disk files. Pool-member ejection with
retained snapshots remains blocked in this development slice until the catalog can move
with its VM; keeping a native snapshot file alone would not preserve its catalog.

Retained snapshots also keep references to the storage, ISO images and networks used at
capture time. Editing the current VM or ejecting an ISO does not release those references.
Core refuses destructive removal or storage source changes that would break a recovery
point. Older development snapshots without recorded dependency details block such changes
conservatively until their dependencies can be established or the snapshot is removed
through a supported lifecycle. Ordinary starts, storage leases, renames and rechecks remain
available subject to their existing safety checks. Removing a pool ISO or network also
requires every ready member to acknowledge the removal intent. If that check cannot
complete, restore member connectivity and inspect the existing task before submitting
more work; an uncertain removal continues to protect the resource. Core reconciles the
saved removal in the background when its safety checks permit, even with Controller offline.
Snapshot reservation and completion also wait for acknowledgement from every ready pool
member so a replacement master retains their records. If a member is unavailable, the
snapshot may remain unresolved even when local capture finished; restore connectivity and
retry the original operation ID rather than requesting another capture.

The development promotion correction lets surviving hosts continue after Core has verified
and recorded the fencing of an unavailable member. That host remains a pool member; it must
adopt current authority and report its VMs before Core makes its acknowledgement mandatory
again. A connection failure alone never qualifies. Pools promoted by an older candidate
retain the conservative wait. A [disposable three-host walkthrough](../qa/2026-09-26-recovery-snapshot-promotion-r8.md)
verified a new snapshot while the fenced former master stayed off, then its restricted
boot, safe rejoin and return to mandatory acknowledgement. This does not establish the
separate backup/disaster-recovery workflow or physical fencing hardware.

## Troubleshooting

- **400:** correct the request shape, ID, mode or metadata. Unknown fields are rejected.
- **404 on revert or delete:** the selected point is absent from the authoritative catalog;
  refresh the catalog and verify the VM and snapshot IDs.
- **409:** enroll the Core if the response requests enrollment. Otherwise, an operation ID was
  reused with different input, or prior work for the VM is unresolved.
  Inspect the original operation; do not choose a new ID to bypass its protection.
- **503 or a lost response:** availability or acceptance may be uncertain. Query the same operation
  ID and retry the exact original request. Never infer that no effect occurred from a timeout.
- **`needs_attention`:** retain the original ID. An exact retry requests reconciliation of the
  saved intent and observed native state; it does not authorize blind repetition of an uncertain
  disk operation. Conflicting work remains held until Core proves the outcome.
- **`failed`:** read the recorded cause. A name conflict requires a different name; the catalog
  permits up to 32 retained points. Running UEFI capture and missing guest-agent quiesce each
  fail with an explicit cause and do not invent a quieter mode. A corrected request uses a new
  operation ID after Core confirms the failure. Reusing the old ID returns its original result.
- **Catalog unavailable:** restore access to the current master. An unavailable catalog is not an
  empty catalog. A copied disk or a successful request is not proof of a usable recovery point.

## Verification

The [first native stopped-revert walkthrough](../qa/2026-09-26-stopped-revert-native-validation.md)
records BUG-0647 and the preserved operation awaiting recovery.

`go test ./internal/localcore` passed, including HTTP authentication, same-origin/CSRF,
input refusal, enrollment errors, request/status routing, empty catalogs and disconnected
callers. These tests use a simulated agent channel; they do not establish native snapshot
correctness. A [native host-API attempt](../qa/2026-09-26-recovery-core-api-preparation.md)
verified acceptance, exact retry and changed-input refusal with Controller offline, but capture
failed during local-storage identity validation. Subsequent candidates exposed memory-unit
validation errors while preserving the original disks and reservation. The corrected candidate
completed that same reserved operation with Controller offline: the master catalog and native
snapshot agree, both disk markers verify, and the VM remains stopped. Exact replay after a
Core service restart passed with Controller offline; the same Controller then reconnected
and adopted the matching catalog entry. Failed harness attempts remain recorded separately.
A separate fresh Controller adopted the original host, pool, snapshot descriptor and
succeeded operation; native marker checks passed in a read-only continuation after a
runner error. The linked QA records that error and its old-Controller fingerprint limit.
A later [stopped two-disk crash check](../qa/2026-09-26-recovery-snapshot-core-crash-r8.md)
timed out after native capture, then killed Core before master settlement. Restart and
exact-request retry settled that point without another native capture; both signed member replicas,
disk chains and markers verified. Other interruption points remain unverified.

Controller-relayed running create on a disposable BIOS guest settled a crash-consistent
point into the master catalog while the guest stayed running. The same lab refused UEFI
running crash create before effects and failed quiesced create honestly when the guest
agent was not connected. A later managed BIOS guest with a responding agent settled
`mode=quiesced` into the catalog while remaining running. See
[running-mode evidence](../qa/2026-09-26-recovery-snapshot-running-mode.md).
A Controller kill while the guest filesystem was frozen was followed by an agent thaw and a
successful post-restart quiesced create. A Playwright Snapshots-tab walkthrough signed in,
showed running Mode choices, listed retained points and submitted a crash create that the
master catalog accepted ([browser evidence](../qa/2026-09-26-recovery-snapshot-browser.md)).
Core killed during an in-flight quiesced capture (guest frozen, `virsh` present) still
settled that operation after restart with the guest thawed. Browser rename on a stopped
guest settled in the catalog. On a matching-disk never-started guest, browser revert and
leaf delete both settled successfully
([browser evidence](../qa/2026-09-26-recovery-snapshot-browser.md)). VMs-list bulk stopped
Snapshot settled a shared name on two matching-disk shut-off guests in the same lab.
Independent review of that bulk batch returned PASS WITH GAPS and the sticky-name retry
gap was fixed. Phase 5 closed for disposable virtual-lab scope (2026-09-27);
[close record](../qa/2026-09-26-phase5-gate/CLOSED.md). Multi-host
replacement for running capture remains unverified beyond the earlier stopped campaigns.
BUG-0647 (exact retry after libvirt rewrote snapshot heads) is fixed with native
evidence; that failure mode is no longer an open revert blocker.
The separate [native feasibility evidence](../qa/2026-09-25-recovery-s0-native.md) establishes
only the specific installed-version experiments it records.
