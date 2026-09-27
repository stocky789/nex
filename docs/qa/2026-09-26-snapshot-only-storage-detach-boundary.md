# Snapshot-only storage-detach evidence boundary

Date: 2026-09-26. Status: **isolated master-guard regression passed; a native
snapshot-only detach fixture is not constructible through the current supported
VM lifecycle.** No CoreB or Controller operation was run for this check.

## Source trace

The existing API permits a stopped VM edit to remove an A-backed disk and add a
new disk on repository B, but it does not move an existing disk between storage
registrations. `protocol.NeedsStop` rejects changes to an existing disk's
storage, size or provisioning. The vmhost update layout matches disks by name,
allocates newly named disks on their requested repository, and records every
removed disk's complete backing chain in the VM journal's retained-file list.
That retained file remains a separate storage-detach dependency. The VM delete
path cannot wipe it while a snapshot catalog is retained; its delete guard is
covered separately by BUG-0612.

At the pool master, `BeginPoolStorageMutation` checks a pending snapshot recovery
and then checks the retained snapshot's captured storage IDs before recording a
storage mutation. That history guard returns the exact refusal
`pool repository is required by a retained VM snapshot`. The later host-local
guard also counts current and retained disk paths, so a native A-to-B edit cannot
make storage A exclusively snapshot-referenced with today's supported APIs.

## Verification

Added `TestPoolStorageDetachSnapshotHistoryBlocksOldRepositoryAfterCurrentDefinitionMoves`
to `internal/agent/pool_vm_recovery_test.go`. It constructs the authority record
with the current VM disk on B and an older snapshot descriptor naming A. The
test checks that A is refused with the exact retained-snapshot reason and that B
passes this snapshot-history guard. Passing B here is scoped to the master guard;
the test does not claim a complete detach because the host-local guard still
protects the current B disk.

Command run: `go test ./internal/agent -run
'TestPoolStorageDetach(SnapshotHistoryBlocksOldRepositoryAfterCurrentDefinitionMoves|IsRefusedWhileVMRecoveryUsesRepository)'
-count=1` — passed.

The prior r7 Core API walkthrough is documented in
[retained snapshot resource dependencies](2026-09-26-recovery-snapshot-resource-dependencies.md).
It captured a snapshot on A and moved the current definition to B, then got a
storage-detach failure whose error also named the imported ISO and the old
retained disk. That evidence remains partial and is not upgraded to a
snapshot-only native result here. It also does not test the cross-Core shared
repository lock: its repositories were local to one Core. No new VM, repository,
native snapshot, resource detach or cleanup was attempted.

## Remaining acceptance

The isolated test establishes the master's historical dependency refusal when
the current pool definition uses another repository. It does not establish a
snapshot-only native detach where the host has no retained A disk, because the
public edit/delete lifecycle currently offers no way to reach that state while
preserving the snapshot. A future supported lifecycle that releases a removed
disk while retaining its snapshot would need a disposable Core API walkthrough.
The separate positive shared-storage lock/lease behavior also remains open; a
single-Core local repository does not exercise it.
