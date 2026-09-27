# Recovery S0 native snapshot probe

Date: 2026-09-25. This is partial native evidence for the recovery S0 plan. It
does not close S0 or establish product snapshot, backup, or restore behavior.

## Environment and method

The probe used one newly created, package-based Debian 13 Core candidate from the
disposable three-host fixture described in the [Core lab report](2026-09-25-recovery-s0-core-lab.md).
The candidate began with no domains or workload disks. Each case defines an
independently named stopped BIOS/QEMU domain with one or two 64 MiB qcow2 disks,
no network devices, and no guest OS. Known 4 KiB markers are read or written only
after `virsh domstate` reports `shut off`. Checks inspect inactive XML, exact
qemu-img backing chains, markers, snapshot metadata, domain state, and available
libvirt/AppArmor evidence. The report records the installed command help and
runtime versions. This is package-based Core evidence, not ISO installation
evidence.

The final canonical patched packages were built from libvirt
`11.3.0-3+deb13u3+nex1`; the installed QEMU packages were
`1:10.0.13+ds-0+deb13u1`. The canonical package artifacts are documented in the
[libvirt backport report](2026-09-25-recovery-libvirt-backport.md). The native
reports remain in ignored `.cache/recovery-s0-native/`; this file summarizes and
sanitizes them.

The final local rerun used a throttled disposable environment: one vCPU and
3 GiB for Core, plus a separate one-vCPU, 1 GiB storage guest, with both outer
QEMU guests under a one-CPU aggregate quota and low CPU/I/O weights. This setup
verifies correctness only; its runtimes are not performance measurements.

## Observed results

- With the distro libvirt helper, a stopped-created external snapshot deletion
  failed during block commit because its generated AppArmor policy included a
  write deny for a backing image. `snapshotDeleteInProgress` remained in snapshot
  metadata. No metadata or permissions were changed. The exact affected saved
  domains and chains were reconciled before retrying; the missing post-point head
  for a noncurrent reverted branch was absent from current and saved-point chains
  and was not treated as data loss. Libvirt 11.3 source review confirms deletion
  selects the chain through current/first-child snapshot definitions and checks
  the selected target's in-progress flag. A native retry of the exact selected
  deletion after the canonical package install succeeded for all three preserved
  baseline cases (one disk, two disks, and matched-start/stop one disk), clearing
  the parent flag while retaining the expected point markers and chains. No flag
  was manually edited. The failed-delete retry evidence does not simulate a
  process crash during deletion.
- The canonical package's `virt-aa-helper` dry run passed the fixed-policy check.
  The matched source, build, test and helper-policy evidence is summarized in the
  backport report. The native baseline failure is distinct from the patched
  native reruns below.
- The verified four-patch candidate was installed on Core A using exactly its
  selected 15-package closure. All selected packages report
  `11.3.0-3+deb13u3+nex1`; libvirtd restarted, and the candidate helper
  regression passed for readonly-backing policy and partial-chain suffix /
  explicit-empty handling. The package manifest and source/build verification
  are recorded in the backport report.
- On the patched canonical package, four local-filesystem cases passed: one- and
  two-disk rapid reverts, and one- and two-disk reverts spaced by more than one
  second. Each created points A and B, restored their expected markers, created a
  new branch point after reverting, performed immediate repeated reverts with
  unique UUID-suffixed overlay paths, deleted C/B/A, and successfully reopened
  the consolidated chain with libvirt/QEMU. This verifies disk-chain reopening,
  not an operating-system boot.
- The same four cases passed on the dedicated NFSv4.2 VM-storage mount after the
  probe validated the exact mountpoint, filesystem type and source. Before
  lifecycle operations, the probe verified every disk was readable and writable
  as `libvirt-qemu`; no permission broadening was used. All snapshot, revert,
  deletion and reopen steps passed for both disk counts and timings.
- All four lifecycle cases also passed on the dedicated authenticated SMB 3.1.1
  VM-storage mount with sealing enabled, and all four passed on the dedicated
  exclusive 2 GiB iSCSI LUN formatted ext4. Each case verified its selected mount
  identity and `libvirt-qemu` access before creating disks, then checked markers,
  chains, snapshot deletion, and QEMU reopen. These are synthetic marker-only
  disks; the tests do not claim workload or production storage acceptance.
- In the NFS cases, a per-disk chain measured two images and 536,576 total
  `actual-size` bytes at point A, then three images and 868,352 bytes at point B.
  These are observations for marker-writing 64 MiB qcow2 fixtures, not a capacity
  guarantee or a product quota. The probe records chain image counts and summed
  image `actual-size` after each point.
- A separate two-disk `snapshot-create-as --atomic` injected failure used a valid
  first-disk destination and a second destination beneath a nonexistent directory.
  Libvirt/QEMU returned an error; afterward both disk heads, exact one-image
  chains, markers, shutoff state, and empty snapshot list were unchanged. This
  result was reproduced on the canonical patched package as well as on the
  distro baseline. It establishes clean behavior for that injected failure only,
  not universal all-or-none behavior.
- On the earlier two-patch candidate, a separate three-point oldest-parent
  deletion control passed with one disk; the two-disk control failed with
  `Unable to read from monitor: Connection reset by peer`. Read-only
  reconciliation found all three snapshot labels and no
  `snapshotDeleteInProgress` flags; the current marker `0x13` and newest saved
  marker `0x12` remained on both disks. The target's saved content advanced from
  `0x10` to `0x11` on both disks. The middle point remained readable on vdb but
  its vda backing chain referenced a missing target overlay. The transient
  AppArmor `.files` samples show `stress-01-1` with `rwk`, then later `rk` shortly
  before QEMU logged `Permission denied` opening that same image and shut down.
  This is consistent with a domain-wide profile reload racing parallel per-disk
  commit completion, but the samples show generated rule-file revisions rather
  than direct kernel profile-transition telemetry. The failed domain is
  preserved. Native evidence: ignored
  `.cache/recovery-s0-native/parent-delete-controls/` and its two read-only
  reconciliation directories.
- With the four-patch candidate, corrected one- and two-disk oldest-parent
  controls passed the complete three-point lifecycle. Each oldest-parent merge
  recorded a QEMU PID under its exact UUID-bound enforcing AppArmor profile.
  The remaining points were deleted one at a time, with retained markers,
  current marker `0x13`, and clear `snapshotDeleteInProgress` flags verified
  after each deletion. Both synthetic domains reopened under QEMU after all
  points were removed. The optional profile assertion is required for the
  parent merge that exercises serialized per-disk commit; later cleanup deletes
  record process observations without requiring one. Evidence: ignored
  `.cache/recovery-s0-native/four-patch-parent-delete-complete/`.
- On the same candidate, the regular one-/two-disk rapid and paced lifecycle
  suite passed all four cases. It restored the expected markers, checked unique
  UUID-suffixed revert overlays, deleted points C/B/A, found no lingering delete
  flags, and reopened every consolidated synthetic chain. The watcher records
  whether a QEMU process was observed but does not require one during each
  deletion. An earlier overbroad AppArmor-gated run was incomplete when point B
  deletion succeeded but no QEMU PID was observable; read-only reconciliation
  confirmed retained point A and current markers and no in-progress flags. Those
  two generated domains remain preserved and were not counted as passing
  lifecycle cases. Passing evidence: ignored
  `.cache/recovery-s0-native/four-patch-local-lifecycle-complete/`.
- The two-disk 32-point chain stress failed when deleting an older parent and a
  leaf with `failed to parse JSON: nesting too deep`. The points,
  current markers, and snapshot labels reconciled after each error; the 32-point
  run's parent marker remained `0x18` and current marker `0x30`. The 16-point
  older-parent delete instead returned a monitor reset and left a partially
  advanced target; later exhaustive reconciliation confirmed the unrelated
  stress-03 point remained at `0x13` and the current point at `0x20`. These are
  distinct observed failure modes. No snapshot-count ceiling is inferred from
  these tests.
- Source tracing places the JSON error on the temporary QEMU start for inactive
  non-leaf deletion: libvirt refreshes disk information through QMP `query-block`,
  whose nested backing-image response is parsed by json-c with its default depth
  of 32. QEMU `10.0.13` does not support the later `query-block flat` option, so
  that proposed mitigation is not compatible with this tested Core runtime. A
  scoped QMP JSON parser depth adjustment bounded to libvirt's existing backing-
  chain traversal limit plus the protocol envelope is under source review; no
  parser patch or deep-chain green run is included here. This is separate from the
  profile race and does not establish a product snapshot-count limit.
- The initial Docker experiment failed during deletion because the container
  could not satisfy libvirt's xattr/permission requirements. It is retained as a
  lab-setup limitation and is not used as evidence about native backend support.

## Reproduction

The local canonical lifecycle and NFS lifecycle use the same bounded runner. A
fresh ignored evidence directory is required for each run. The NFS invocation
requires the authority-provided disposable lab and exact mount source supplied
at execution time:

```sh
python3 tests/integration/recovery_s0.py \
  --lab-dir .cache/recovery-s0-core.<selected-lab> \
  --node core1 \
  --evidence .cache/recovery-s0-native/<new-evidence-directory> \
  --storage-root /mnt/recovery-s0-vm-nfs \
  --expected-fstype nfs4 \
  --expected-source '<selected NFS export>' \
  --expect-uuid-revert-overlays
```

The four-patch local lifecycle uses `--expect-uuid-revert-overlays` without
`--require-apparmor`. The serialized oldest-parent merge control uses
`--parent-delete-control --require-apparmor`; that flag is restricted to this
mode and requires an exact enforcing QEMU process observation during the merge.

The runner rejects an existing evidence directory, validates the candidate as a
disposable fixture, validates exact `findmnt` fields for selected storage, creates
a UUID-scoped private child and sentinel, and removes only its own successful
scratch. Incomplete or uncertain operations retain the domain and artifacts for
review. The exclusive-iSCSI selector additionally checks that the selected source
is a block disk of the expected size and is attached to the named active iSCSI
target. The selector checks are unit-tested. The four-case SMB and exclusive
iSCSI lifecycle runs are recorded in ignored `.cache/recovery-s0-native/` as
`smb-lifecycle-canonical/` and `iscsi-lifecycle-canonical/`.

The bounded atomic-create-failure probe can be reproduced with
`--atomic-create-failure` against a fresh disposable candidate. The retained
failed-delete recovery check accepts only an explicitly named preserved disposable
domain and point C target; it reconciles state before issuing the native delete.
The bounded oldest-parent control uses `--parent-delete-control` and runs fresh
one-disk then two-disk three-point chains. The read-only exhaustive reconciliation
mode accepts only a generated three-point S0 domain name using
`--reconcile-chain-domain nex-recovery-s0-chain3x2-<suffix>`. The 16/32-point
stress is available with `--stress-point-chain 16|32` on fresh local-storage
domains. Failed or partially mutated domains remain preserved for review.
The `--require-apparmor` check validates the domain's startup process and samples
that domain's QEMU PID and `/proc/<pid>/attr/current` during the oldest-parent
merge. It requires the exact UUID profile in enforce mode for that operation.
The watcher records process observations for all deletions; cleanup deletions
that do not merge need not have a QEMU process. Retained-point reconciliation
fails if any `snapshotDeleteInProgress` flag remains and reports the flag map.
The four-patch native controls exercised these checks.

## Limits and next evidence

The observed results do not cover process interruption during deletion, job
cancellation/stall/disappearance, pool shutdown, capacity exhaustion, or an
operating-system boot after consolidation. Firmware profile results are in the
[firmware report](2026-09-25-recovery-s0-firmware.md); backup/restore results are
in the [backup report](2026-09-25-recovery-s0-backup.md). The earlier two-patch
parent-delete failure has a four-patch native green control, but
daemon-interruption recovery and broader failure injection remain open. The JSON
nesting failure needs a targeted source regression
and deep-chain native verification. These failures do not support an arbitrary
snapshot count ceiling. The separate historical source-chain reopen error after
backup had a permission-denied message; available evidence did not establish its
cause, so it remains unresolved. A separate bounded probe now records a public
off-branch blockcommit/rebase and metadata-only oldest-point removal sequence;
see the [off-branch snapshot report](2026-09-25-recovery-s0-branch.md). That
manual native sequence is evidence only and does not establish an implemented
Core recovery operation.

API impact: none; the probe calls native libvirt/QEMU commands and adds no Nex API
contract. Guide impact: none; no operator behavior changed. State owner: none;
probe resources are ephemeral. Recovery S0 remains open pending the delete-race
and JSON-depth fixes, interruption/recovery gates, and remaining API-backed Core/
Controller and user-guide acceptance work.
