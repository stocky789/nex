# Test a backup restore

## Status

Development Phase 6 Controller workflow. Core restores a recovery point as a
separate, stopped test VM with disconnected network interfaces. The operator
inspects it and records passed or failed; creating the VM does not verify its
boot or application data automatically.

## Prerequisites

- A completed recovery point on a reachable repository.
- VM-ready storage with room for the restored disks and a suitable host.
- Permission to use the pool's backup and VM controls.

## Steps

1. Choose the pool, then open **Backups** → **Recovery points**. Select the point
   or its **Details** button, inspect it, then select **Restore…** → **Test restore**.
   In **Destination**, enter a new test VM name, host and storage. Select **Review
   restore**, check the new identity and disconnected-network defaults, then
   **Create test restore** in the **Review and submit** footer.
2. Follow the task. When the test restore succeeds, open the recovery point's
   **Verify test restore** section. Each successful test VM that still exists
   has its own labelled section, newest first, with its name, ID and **Open test VM** link.
3. Open the test VM. Check that it boots and that the expected application
   data is usable. Keep its interfaces disconnected unless an isolated
   inspection network has been arranged.
4. Return to the same test VM section and choose **Record passed** or **Record
   failed** based on what you observed. Check the task result. This records
   your finding for that recovery point; it is not automatic boot verification.
   Core checks that this is still the VM created by the selected test restore.
   **Record passed** also requires its current disk and firmware state to be
   readable. **Record failed** remains available when those artifacts are
   damaged, provided the original VM identity and definition can be proven.
5. Shut down the test VM. Once Core freshly reports the VM **shut off** and
   its host connected, type that test VM's exact ID into the cleanup field and
   choose **Clean up test VM and disks** for that section. Follow the cleanup task
   and confirm the test VM disappears. If more than one test VM was made from the point, clean
   each one separately.
   If the VM's host is unreachable, the cleanup task shows **Waiting: member unreachable** and
   continues by itself when the host answers again.

## Expected result

The recovery point retains the recorded test result. Cleanup removes only the
selected stopped test VM and its copied artifacts; it does not delete the
source VM or recovery point. Core keeps the recovery point protected from
retention while that successful test VM awaits cleanup.

## Troubleshooting

- No test VM section: wait for the restore task to succeed and refresh the pool's
  VM and backup state. A pending or failed restore has no verified test VM.
- **Test restore history unavailable**: the run inventory could not be read;
  wait for a fresh master report and reload before recording or cleaning up.
- Cleanup disabled: check the exact typed VM ID, a fresh **shut off** VM
  observation, fresh host connection and whether another request is running.
- Verification refused: the test VM may have been deleted, recreated under
  the same ID or changed since restore, or its owner host may be unavailable.
  Inspect the retained task and VM. Do not record a result for a replacement
  VM; use a new test restore after resolving the original outcome.
- Test restore refused before placement: check that the repository manifest
  still matches the master's catalog point. Core will not restore changed
  content under an existing recovery-point identity. A member on an older
  Core build cannot accept the catalog-bound request; retain the original
  operation and target for reconciliation after upgrading it.
- Lost reply: inspect the task and exact test VM state before retrying the
  same request. Do not create a second test VM to resolve an uncertain one.
- Interrupted allocation: the executing Core can resume only the exact
  retained test restore on the same target after checking its native journal;
  inspect the original task and VM before submitting another request.
- Controller absent: use the authenticated Core-local routes in
  [Recover backups without Controller](recover-without-controller.md).

## Verification

The [post-close source review](../qa/2026-09-27-phase6-post-close-review.md)
records focused UI tests and a desktop/mobile browser fixture for separate
test VM controls, failed run-inventory alerts, exact cleanup selection and
keyboard order. The fixture did not submit Verify/Cleanup or create multiple
native test VMs. A later disposable Core3 BIOS test restore completed with a
point pin and VM creator record. Its isolated boot screen showed no bootable
device, so the operator recorded **Failed**. The Verify and typed-ID Cleanup
receipts succeeded; the VM, copied disk and firmware were absent afterward,
and Core removed the exact owner row and point pin. This checks the Failed
path for one test VM; Passed verification, multiple native test VMs,
interrupted cleanup and guest application recovery remain untested here.

The [backup workspace review](../qa/2026-10-01-backup-ui/README.md) submitted
test-restore, Record failed and exact-ID cleanup through the redesigned UI to
fictional API responses, and checked unavailable-history refusal. This adds UI
coverage only; it does not extend the native verification above.
