# R7 snapshot replication and pool-promotion walkthrough

Date: 2026-09-26. Status: **partial native acceptance**. Snapshot creation, replicated reservation/settlement, and promotion with catalog adoption passed in a disposable three-Core lab. A second post-promotion snapshot request was held before native effects because the fenced former master remained marked ready for replica acknowledgements. The requested Core-crash-after-native-effects boundary was not reached.

## Candidate and fixture

The package-based Debian 13 Core guests ran the r7 `nex-core-local` candidate, version `0.3.6~recovery.snapshot6`, binary SHA-256 `1435dc55ed49950f5ed9e3987f453752f9fb27b97e8b12159eab316a802d7ee7`. The installed Debian package remained `0.3.6~recovery.s0`; the lab used a candidate executable replacement. The matching Controller B candidate binary SHA-256 was `69bd3ac73371b8f26b63c4f904411c4b0f6cb5aae19644fef48851596d221af0`. The candidate used matched libvirt 11.3.0-3+deb13u3+nex3. This was not an ISO-installed Core test.

Controller B was replaced with the r7 executable while preserving its existing database and TLS identity. The checks below show it consuming Core-reported operation state; they do not establish adoption by a newly initialized Controller database.

The isolated lab had three Core guests (Core A, Core B, and Core C) on a private virtual network. Core A began as master; Core B owned a stopped managed VM with two writable disks and seeded marker data; Core C was the promotion candidate. The test used the existing local Core snapshot API, authenticated Controller APIs, pool mTLS replication, and a lab-only libvirt power-off fence. It did not boot the VM.

## Snapshot while Controller was unavailable

With Controller B unavailable, Core B accepted a stopped-snapshot request through its local authenticated API, completed the native operation, and returned the Core-owned catalog entry. The exact request retry was accepted idempotently and a changed-name retry was refused. The VM remained shut off and both disk markers remained readable. After Controller B returned, it adopted the successful Core operation from Core inventory.

## Reservation and settlement replication before acknowledgement

A disposable wrapper on Core B paused the exact `virsh snapshot-create-as` call before invoking the real executable. While the wrapper was held, the master's durable authority record contained the exact pending reservation and descriptor. Both other hosts held verified signed member replicas at generation 20 with the same digest and exact pending fields. Neither member catalog contained the new descriptor, and native snapshot metadata did not yet contain the requested point. The coordinator captured these records before releasing the wrapper, which establishes their ordering before native execution; the raw receipt has one observation time for the combined capture, not a separate timestamp for each host.

The r7 settlement path synchronously pushes the changed authority and checks an exact acknowledgement from every ready member before it returns success. After the operation reported `succeeded`, the master's durable authority record showed the completed descriptor and settlement receipt, and both member records showed verified signed replicas at generation 21 with the same digest and descriptor. Those post-success observations do not carry per-replica timestamps; the acknowledgement ordering follows from the exercised success path, while the observations confirm the resulting state. The pending reservation was cleared, both seeded disk markers still read correctly, and the VM remained shut off. One generated summary boolean was false because its predicate expected the wrong field shape and is not used as success evidence.

## Fenced promotion and adoption

Promotion preflight refused while Core A's service still held authority. After that service was stopped, the candidate's preflight required fencing exactly Core A. The accepted promotion used the lab virsh fence; the outer disposable domain for Core A was observed shut off. Controller B then reported Core C as master, all three pool members, and the exact promotion operation as succeeded.

After promotion, Core B read the catalog through its normal local Core API. Both retained snapshot descriptors were returned from the new master, the managed VM still appeared shut off on Core B, and Controller B's operations API showed the earlier snapshot operation adopted as `snapshot-create/succeeded`. This confirms the completed replicated catalog survived old-master exclusion and was visible through the upgraded Controller process; it is not a fresh-database replacement test.

## Post-promotion admission blocker

A separate snapshot request after promotion was deliberately not treated as a native test. Core C durably saved the exact pending reservation and Core B received the matching verified replica, but the request remained `needs_attention` at the local-intent stage. The prior master Core A was powered off and stale, while its membership entry still said `ready`. The snapshot acknowledgement path requires every ready member to acknowledge, so the Core B request received a refusal before any native snapshot command ran. The catalog still contained only the two completed points, the VM and markers were unchanged, and the exact pending reservation is retained. No retry, member removal, or authority weakening was used to make the test pass. This is an unresolved post-promotion readiness issue tracked separately.

## Checks and limits

- Controller outage: authenticated local snapshot request, same-request retry, and changed-intent refusal — passed.
- Pre-native ordering: exact pending reservation observed in master and both ready-member replicas before the real native command — passed.
- Completion: settlement ACK path returned success; subsequent readback showed the exact descriptor and settlement receipt in the master authority record and both verified signed member replicas; both markers remained readable — passed.
- Old-master exclusion: supported virtual fence powered off Core A; promotion reached terminal success — passed.
- Controller operation adoption and new-master readback: snapshot operation appeared as succeeded and both catalog points were returned by the new master through Core B — passed.
- Post-promotion second reservation: safely refused before native effects because the fenced old master remained membership-ready; exact pending state preserved — blocked, not passed.
- Core process crash after native snapshot success but before Core journal settlement — not reached. The attempted operation stopped at reservation acknowledgement, before `virsh` invocation.

The exact private raw receipts are ignored local captures. Their SHA-256 hashes are recorded here so the evidence reviewed for this report can be identified without publishing its private contents:

| Receipt | SHA-256 |
| --- | --- |
| Controller-outage snapshot result | `243f1d170a0059c52af8fa26ef553625866e0153efde756652184b1de344753a` |
| Pending pre-native reservation capture | `29911fd2b613a5ec74135e8fed83538a450b656fa22138f3086030e92668e7bf` |
| Post-success master and member observations | `a25f29ddaf9fa86515ac9fa28c8a17c9d3d35d5b313cc13ef4419548d0041cd1` |
| Core snapshot operation result | `74c72e83396640bb78cf5abf7dd9808f81c37a8756ff6708fe435cfbc4cf881e` |
| Promotion result and adopted master view | `73c42e50b8e8311d1a281a4aa649deb7675ef711e3373190bb8d5a7d4ee196bc` |
| Post-promotion catalog and VM readback | `02410155a69a10780629351f5929d3bc80593dc45a03bcdc9c85f7dffc80cd01` |
| Controller snapshot-operation adoption | `a829ba56774a9437069e89558086731d44ef8aa6795c1d3e709479e71f2d7ddf` |

All VM names, operation IDs, host identities, addresses, credentials, and command captures remain in ignored local evidence and are omitted here. The lab used nested virtual machines and a virtual libvirt fence, not independent hardware or a physical BMC. The result does not establish ISO installation, production-network behavior, hardware fencing, adoption into a fresh Controller database, or the unreached interruption boundary.
