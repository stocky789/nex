# Snapshot deletion and startup safety on the r6 Core candidate

This QA records two bounded checks against the selected disposable Core host running the locally built `0.3.6~recovery.snapshot5` executable. The Debian package version remained `0.3.6~recovery.s0`; only the executable was replaced for this lab run. The Controller was a fresh replacement enrolled to the same disposable pool.

## Controller deletion requests

Using the existing authenticated VM-delete API, the test submitted one request with disk deletion disabled and one with disk deletion enabled for the retained-snapshot VM. Both requests were accepted and reached a terminal failed operation with the retained-snapshot refusal. Before and after each request, Controller inventory and Core observations agreed: the VM and snapshot descriptor remained present, native snapshot metadata and disk chains were unchanged, and the recorded disk markers and file identities still matched. No delete operation reached disk undefine or wipe.

This proves the replacement Controller presents the Core master's existing retained-snapshot refusal for both delete choices. It does not bypass the master authority check or prove the downstream Core VM-delete engine's keep/wipe refusal independently; the authenticated requests were refused at the master admission stage.

## Native startup guard

The installed r6 `nex-core-local storage-guard` command was invoked with the selected VM's inactive domain XML and an isolated state directory. That directory contained a mode-0600 copy of the settled snapshot journal transformed into a valid synthetic unresolved local-intent record. The command refused startup with the local snapshot-intent error before repository verification. The original settled journal hash was identical before and after the check.

This is a compiled-candidate CLI check of the actual hook entry point and recovery gate. It used a derived journal in an isolated directory; it did not edit the Core journal, invoke `virsh start`, or exercise libvirt's hook process end to end. The Go regressions cover unresolved local intent, a valid reservation, settled state, and refusal before repository probes.

## Checks and limits

- Authenticated Controller B delete with `delete_disks=false`: terminal failure for retained snapshots; VM, catalog, and native state unchanged.
- Authenticated Controller B delete with `delete_disks=true`: terminal failure for retained snapshots; VM, catalog, and native state unchanged.
- r6 `storage-guard` isolated local-intent fixture: expected refusal; original journal unchanged.
- Focused and full `internal/vmhost` test results are recorded in [the storage-guard change assessment](../changes/2026-09-26-storage-guard-snapshot-fence.json).

The run used private disposable-lab records for exact identities, commands, and hashes. Those values are intentionally omitted from this public report. No product support claim follows from this candidate-specific evidence; broader S0 acceptance remains open.

## Independent review

Independent read-only review checked the immutable candidate artifacts and install receipt,
both authenticated Controller refusal results and their unchanged Core observations, and the
isolated storage-guard fixture. No evidence blocker was found. The single-host master resolves
its authority locally; this does not establish a cross-host mTLS exchange. The native Engine
retained-snapshot regression is a serial software test using the actual lock and fake libvirt,
not a concurrent native race campaign. The CLI fixture did not start a VM.
