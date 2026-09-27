# Recovery QA: Core crash after native snapshot effects

Date: 2026-09-26. Status: **passed for the stopped, two-disk post-native-effect
crash boundary on the r8 disposable three-Core lab**. This is sanitized
historical evidence; host, pool, VM, operation, snapshot, and filesystem
identifiers are omitted.

## Candidate and fixture

The package-based Debian 13 Core ran the `0.3.6~recovery.snapshot7`
`nex-core-local` candidate (binary SHA-256
`f23e66df94ebec6e6bd35393dfab1053458a9bb863a231673209a66acd69aedd`). The
matching Controller candidate used the same version label (binary SHA-256
`b8821e8e8df5d0746755fd20b9624d4b0b8beb189f8f34dc15ed5821e72fcd9e`). The
installed Debian package remained `0.3.6~recovery.s0`; these were lab-only
runtime replacements. The matched source archive SHA-256 was
`b5f47101418aa0f9be931eb98e4d4c0fea6ebaf7a6c184947bf0d6997cb4a4fe`, with
159 source entries and source-map SHA-256
`7fe88b5f4eface2a19c3cb3ab095d45309566b4279fab1ecdcece061e8ac52ab`.

The fixture was one stopped BIOS VM with two synthetic disks and marker data.
The owner submitted one snapshot request through the authenticated local Core
API; the initial response was HTTP 202 with status `pending`. Controller B
remained available during this run; no Controller outage was tested. The test
wrapper matched the native snapshot-create command by
the operation-derived native name, allowed that command to finish once, and
held Core settlement at the boundary. Before interruption, the native
snapshot listing matched the requested point, both disk markers (`0x11` and
`0x22`) read completely, and both current disk chains were present.

At that same boundary, the request had not settled: the native command had
returned success, the journal was still in `snapshot-native`, and the master
catalog still contained the prior three points and no new point. The master's
exact reservation was pending. Both member replicas were signed and verified at generation 27,
had identical digests, and each acknowledged that exact pending reservation.
This established that the real native effect had succeeded while durable Core
settlement and master publication were still pending.

## Interruption and exact retry

The held native client exceeded Core's approximately 20-second command timeout.
Before the service interruption, Core had already recorded `snapshot-native`,
`needs_attention` and `virsh failed: signal: killed`; the reservation remained
unsettled. The service kill did not interrupt an active native client.

The selected Core service identity and running candidate hash were then checked.
The service was runtime-masked to suppress automatic restart and its verified
process was sent `SIGKILL`. The receipt confirms the mask, `control-group`
kill mode, signal, candidate hash, zero service MainPID and empty service cgroup.
The post-kill journal retained the unresolved native stage. The gate files still
recorded one successful invocation and no release; these files do not establish
that the wrapper process was still alive. No second native create occurred.

After restoring the service, the owner retried the exact original API request
with the same operation identity and body. The authenticated operation
readback returned HTTP 200 and `succeeded`. Core's journal advanced to
`snapshot-completed`, recovery stage `completed`, result `succeeded`, and
`settled=true`. The master inventory then contained four catalog entries:
the prior three and exactly one new snapshot descriptor. Both member replicas
were verified at generation 28 with matching digests and four snapshots; the
pending reservation was cleared. Native snapshot metadata matched the
descriptor, the VM remained shut off, both disk chains contained five layers,
and both marker reads again completed with their expected values.

## Evidence and limits

The ignored local evidence was inspected in this order: pre-interruption
native/journal state, master and signed-member pending summary, service-kill
receipt, post-kill journal, authenticated owner API/catalog readback,
post-replay native/disk readback, and the two post-replay member replicas.
Their SHA-256 digests are:

- `d6517d5aabe04a2885b894225720dcdf8b0eb9f3485f9107a4245e9e10b40913`
- `69b5b84a29debed509d4f6bdb25dbee03a58e5913ea5a488b783d32dea901100`
- `e99f24c5013aaf62f051d3a072e9b494889c2fa22d5a04896e1a8cea4bd35408`
- `0a08575ebacbb8b37bd2f9e55cff95eae4436460a5ec6144dd8c1043b33148e8`
- `a561413e3c5fc2e6ac942a5b664c7f0fd4a940c43e7458f19318133a11c8e2d9`
- `7b6b5c9865322384e51f5c19612fa85ee0b2a86a6e7e4962acfc37d71db4188a`
- `bb82707836186cbb22b8bcd7c97f4b4bd9a2c8603138218f586efdc671a18aee`
- `04c40775421cba7037f63b616e4cc09e2dac9ad0d566e68ce1e59d90fe8eddaf`

An earlier v2 fault-hook attempt missed the native command and did not
interrupt Core; it is not counted as crash evidence. A wrong-master request
returned HTTP 503 before effects, and a rate-limited HTTP 429 occurred during
polling; neither is part of the successful retry proof. The accepted result
covers a native-client timeout followed by Core service interruption before
settlement for one stopped, two-disk VM on this
package-based disposable lab. It does not establish running-guest behavior,
other crash points, hardware/ISO installation, or broader storage/runtime
coverage. This record establishes only the crash-boundary item. The separate
[promotion/rejoin proof](2026-09-26-recovery-snapshot-promotion-r8.md) and
reviewed Core safety checks complete the bounded S1 gate; neither recovery
phase is complete.
