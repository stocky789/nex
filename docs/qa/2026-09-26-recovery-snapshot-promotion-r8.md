# Recovery QA: snapshot promotion, former-master return, and replica rejoin

Date: 2026-09-26. Status: **passed for the stopped, two-disk owner snapshot and
the configured virtual-fence promotion/rejoin path on the r8 disposable
three-Core lab**. This is sanitized historical evidence; host, pool, VM,
operation, snapshot, and filesystem identifiers are omitted.

## Candidate and fixture

The three package-based Debian 13 Core guests ran matched
`0.3.6~recovery.snapshot7` binaries; the disposable Controller ran the matching
candidate. The installed Core Debian package remained
`0.3.6~recovery.s0`. The source map SHA-256 was
`7fe88b5f4eface2a19c3cb3ab095d45309566b4279fab1ecdcece061e8ac52ab`, the source
archive SHA-256 was
`b5f47101418aa0f9be931eb98e4d4c0fea6ebaf7a6c184947bf0d6997cb4a4fe`, and the
Core and Controller binary SHA-256 values were respectively
`f23e66df94ebec6e6bd35393dfab1053458a9bb863a231673209a66acd69aedd` and
`b8821e8e8df5d0746755fd20b9624d4b0b8beb189f8f34dc15ed5821e72fcd9e`. These
were lab-only runtime replacements; this was not an ISO installation test.

All three Core guests shared one outer virtualization host and one isolated
lab network. The owner had one stopped BIOS VM with two synthetic disks. Before
the promotion run, its four retained native snapshot points and the completed
operation record were present on the current master. Both disk marker reads
completed with the expected `0x11` and `0x22` patterns.

## Fence and promotion

Core3 was configured through the authenticated Controller fence-profile API
with a virtual libvirt target. Core3's private peer link was then isolated
while its management path remained available. A fresh Core1 promotion
preflight reported `ready_to_fence=true` and named exactly Core3 as the fence
target. The confirmed promotion used that exact target set. The Controller
operation succeeded, and outer libvirt reported the exact Core3 guest shut
off. Core1's persisted transfer record reports a passed fence with
`exclusion_verified=true`; its authority membership records Core3 as excluded
from replica acknowledgements. Core1 became the authoritative master at
membership generation 5.

While Core3 remained powered off, Core2 submitted a new snapshot through its
authenticated local Core API. The API accepted one operation with HTTP 202.
Core1's durable VM record gained exactly one descriptor and matching settled
recovery receipt, with no pending recovery reservation. The native snapshot
list gained the operation-derived point, the VM remained shut off, and both
disk marker reads passed. Core2 acknowledged Core1's verified authority
replica at generation 34. Core3 remained off and explicitly excluded from
replica acknowledgements during this operation.

## Former-master boot restriction and rejoin

The fenced Core3 guest was started paused. Before resuming its CPUs, the QEMU
monitor disabled only its private peer NIC; its management NIC remained
available. On boot it retained its old membership generation and recorded a
durable `unconfirmed` restriction stating that it could not start shared
storage guests or write pool authority until the other members confirmed
authority. The service was active, and the private interface had no carrier.
This state was observed in Core's on-disk `pool-peer.json`; no journal or
authority record was edited by the test.

The private NIC was restored on the same guest. Core3 then adopted Core1 as
master at membership generation 5, cleared the restriction, and received a
verified signed authority replica at generation 35. That replica contains the
same five snapshot descriptors and settled receipt as Core1. Core1 accepted
Core3's inventory at generation 5 and its persisted membership no longer marks
Core3 excluded from replica acknowledgements. The Controller's authoritative
member view reports Core2 and Core3 fresh, ready, and acknowledged at generation
35 with the same digest; Core3's outer guest is running again.

The authoritative master catalog contains the four prior points and exactly
one new post-promotion point. The owner journal, native point metadata, disk heads and marker data were
verified after the new capture, before former-master rejoin. The final rejoin
receipts establish catalog and settlement continuity; they do not repeat the
owner disk-marker reads.

## Evidence and limits

The following SHA-256 values bind the ignored private receipts inspected for
this record. The receipts are not published because they contain private lab
identifiers and paths.

An independent read-only review checked the preflight, exact fence record,
power-off observation, post-promotion snapshot and settled catalog, isolated
boot restriction, direct master exclusion-clear/inventory record, and member
replica acknowledgements. It found no evidence mismatch. The receipts show the final current acknowledgement
and cleared exclusion, not a separate capture between inventory acceptance and
clearing. The ordering is established by the independently reviewed
`completePoolVMReplicaRejoin` implementation and
`TestReturningFencedMemberRejoinsBeforeReplicaAckExclusionClears` regression.

- Isolated-candidate preflight: `441d6367232286f2e02ba2487f03c0a060c127ee2c5beafc49a991bc7564920c`
- Exact fence request: `aaab47635a42cf9b9fc52f2df6c368dcd39fe259f99e19db414206833bae8325`
- Promotion acceptance: `a40fce02a6baead0cb3a099456d6ed382ed38045458bcac15af33e4f493a33d7`
- Promotion operation readback: `b1d5dfaa0feaf95e76c9040ec964179d0ec15c5bed1a51ce2d3de3cce6c4243d`
- New-master projection after fence: `975d122bdeb51ae022c1a87848fddffc881abce8a25ae0a8a364ec2ab36c0509`
- Direct pre-snapshot fence, exclusion, catalog, and marker proof: `5e001faf7d9ee4bb495bc6de601d5befb1604915fa069d4251b7fe413795b880`
- New owner snapshot and master-settlement proof: `af27351a00042c3bb6a3de67e34afbc52d716b6428db25c5f3d4fb653b1309d4`
- Post-snapshot member-replica readback: `2d34e5429bed6160f54d91e946c268407a96b9cb2244a16362b09046b785a268`
- Isolated former-master boot restriction: `bd0c1eac6eb1ab1fdabdb4fd0f9a835c9d933350c05b216d6e393efdaa14541c`
- Former-master rejoin and restriction clear: `40edad3e2b814cdfc08a0f82b7d78ee47f0e255e2c0e908f53ecba7741d00f69`
- Direct master exclusion-clear, accepted inventory, and replica ACK: `bf0fcddb377056d237a2d78c2d2ae02ce2a6e0d66245f87b26f087f796aaca7b`

This proves the configured virtual fence and Core authority-replica flow in a
single-host disposable lab. It does not prove independent hardware fencing,
hardware watchdog behavior, automatic HA, a Corosync quorum, running-guest
snapshot behavior, ISO installation, or a broader disaster-recovery campaign.
The separate [R8 native-effect crash/replay QA](2026-09-26-recovery-snapshot-core-crash-r8.md)
documents its bounded combined timeout-and-service-crash boundary; this
promotion run does not broaden that crash claim. Together with the other reviewed
Core safety checks and crash evidence, this completes the bounded S1 gate in the
[recovery plan](../plans/2026-09-25-recovery-snapshots-backups-restores.md).
S2–S8 and both recovery phases remain open.
