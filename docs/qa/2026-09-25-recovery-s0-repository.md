# Recovery S0: NFS repository primitives

Date: 2026-09-25. Status: **native filesystem experiment passed; repository feature and
S0 acceptance remain open**. The project owner authorized a new disposable local lab.

## Environment and reproduction

Two fresh Debian 13 Core guests used a third, dedicated Debian storage guest's NFSv4.2
export. Each client mounted that export twice, at `/mnt/recovery-s0-nfs` and
`/mnt/recovery-s0-nfs-alias`. The export used synchronous writes and root squashing;
the scratch area allowed the mapped anonymous account to write. Storage was the storage
guest's ext4 filesystem. Guests share one physical workstation: this demonstrates separate
virtual guests, not physical failure-domain independence or production NFS compatibility.

The package candidate was `nex-core-local 0.3.6~recovery.s0`; its SHA256 was
`3c302fc06d96d7972b3b5da0eb1ca5255c16b8492d5969c585a2b9f2ed3d7eb4`.
This was a package-based Debian cloud lab, not a Core ISO installation test.

After the recovery lab has prepared those mounts on Core A (`core1`) and Core C (`core3`):

```sh
python3 tests/integration/recovery_s0_repository.py --lab .cache/recovery-s0-core.<run>
```

Replace `<run>` with the explicitly selected disposable fixture directory. The script
validates its location and fixture marker, uses its private SSH configuration, checks
the exact mountpoints use the same NFSv4.2 export before writing, and allocates a unique scratch child.
It leaves that child's small files and a private JSON result naming both scratch paths
and the observed mount options in the lab for inspection.
Destroy these with the selected disposable lab after related experiments finish; this
script neither formats disks nor removes existing files or mounts.

The primary agent executed the script three times; all exited zero. The final tested script
SHA256 was `99e4295bc2bf0f193e0f39e85af4177321508a194bdbeb1b96f4a841ee32181f`.
The first two runs preceded the explicit NFS version/source capture requested in independent
review; the final run includes those assertions and records the retained scratch paths. Generated lab identities
and local paths are omitted from this written account; it is a sanitized report, not raw output.

## Observed results

| Check | Observed result |
|---|---|
| Competing ownership claims | Concurrent `O_CREAT | O_EXCL | O_NOFOLLOW` creates from two clients produced exactly one creator and one existing-file result. The winner synced the marker and parent directory. |
| Alias identity | Both paths on both clients read the same complete repository/pool marker and the winning pool value. |
| Shared coordination | Core A held an exclusive `flock`; Core C's nonblocking request through its alias was refused. After explicit release, Core C acquired the lock. |
| Persistent ownership | Releasing the transient lock left the same owner marker readable on Core C. Lock release did not create another owner. |
| Publication visibility | Core A synced a manifest and staging directory, renamed it, and synced its parent. Core C read the complete published manifest and observed the staging directory absent. |

## Limits and implementation consequence

These results support further design around a shared durable repository owner marker,
with existing Core authority controlling writes. They do not implement that design or prove
that arbitrary backup/prune callers enforce it. A filesystem lock is not a fencing mechanism.
Crash durability, server restart, partitions, writer exclusion after authority loss, durable
restore pins, active prune conflicts and takeover remain untested. The publication observation
was after completion; it does not prove every concurrent reader or power-loss boundary.

No actual backup payload, imported manifest validator, recovery API, scheduler, restoration,
Controller replacement or browser workflow was exercised. Do not use this experiment to close
the mandatory three-host campaigns or claim repository support on other NFS services.
