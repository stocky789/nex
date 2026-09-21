# Phase 4 gap closure: guest continuity and single-writer evidence

## Status

**Open campaign, partial results.** This records a disposable virtual-lab campaign run on
2026-09-21 against the current source tree. It closes two gaps the
[Phase 4 native acceptance report](2026-09-19-phase4-native-acceptance.md) left explicitly
open, and it does not close the permanent limits listed there. Nothing here is a release,
and nothing here re-accepts Phase 4.

The gaps this campaign addresses, quoted from the prior report: *"the observed VM inventory
was empty, so it does not prove workload continuity or absence of duplicate live execution"*.

## Environment and build identity

Every result below was produced by binaries built from a clean working tree at source
revision `9088abc` (which contains the Phase 4 closure commit) with `make binaries`:

| Artifact | SHA-256 prefix |
| --- | --- |
| `nex-controller` | `9cddc390fe022de6` |
| `nex-core-agent` | `2796c8650d155152` |
| source archive | `983c53827f1e69da` |
| continuity guest ISO | `b8a910659e422b87` |

`nex-core-local` was built (`de67bd646362978b`) but **not deployed**: the pool operations
exercised here are served by `nex-core-agent`. Consequences are named under *Limits*.

The lab is four disposable QEMU virtual machines on a private lab-local VDE segment using
the reserved `198.18.0.0/15` benchmarking range: three Nex Core hosts and one NFS/SMB
storage node. Each Core host runs Debian 13 with libvirt 11.3.0 and nested KVM. The
Controller ran as a separate process on the workstation, reached by the hosts over the
QEMU user-network gateway alias. Host addresses, ports and local paths are omitted; raw
evidence remains local and untracked.

Guests are a purpose-built 19 MB ISO that boots a busybox initramfs and, every second,
appends `<epoch> <boot_id>` to a fixed 512-byte slot on its own virtio disk. Nex gives
managed domains no serial or console device, so the disk slot is the only guest-side
channel; it is read over NFS by a Core host that is not running the guest.

## What the continuity evidence is

`tests/integration/pool_continuity.py` samples, per guest:

- host side — `host_boot_id`, QEMU `pid`, `/proc/<pid>/stat` field 22 `starttime`, libvirt
  `domid`
- guest side — the guest kernel's `boot_id` and its advancing heartbeat timestamp

A guest counts as continuous only when **all** of these are unchanged. The host-side
signals catch a replaced QEMU process, including the pid-reuse case that `pid` alone
misses; the guest-side `boot_id` catches an in-guest reboot that keeps the same QEMU
process, which no host-side signal can see.

`virsh domstate --reason` is recorded but is **not** a pass criterion, because
`running (booted)` is sticky for a domain's whole life. This was confirmed in the lab, not
merely reasoned about: across a deliberate destroy+start (Arm B below) `domstate` read
`running (booted)` both before and after. Any evidence resting on that field alone would
have passed a guest that had definitely been restarted.

## Results

Three arms, same sampler, same guests, same build, same session. Arm B is the control that
makes Arm A and Arm C mean anything.

| Arm | Operation | `beat-core2` | `beat-core3` |
| --- | --- | --- | --- |
| A | null control, 60 s idle | continuous, heartbeat +60.5 s | continuous, heartbeat +60.5 s |
| B | forced restart of `beat-core3` only, through the Controller power API | continuous (untouched) | **restarted**: `pid`, `starttime`, `domid`, `guest_boot_id` all changed |
| C | planned master change, core1 → core2 | continuous, heartbeat +261.2 s | continuous, heartbeat +261.2 s |

Arm B is the load-bearing result: the sampler detected a restart, detected all four
signals, and detected it **only** on the guest that was restarted, leaving the guest on the
other host reported continuous in the same sample.

**Arm C detail.** The pool master moved from core1 to core2 while both guests ran. The
membership generation advanced 3 → 4 and the authority generation 23 → 25; core1 became a
member, core2 became master, and all three hosts returned `ready` and `fresh`. Neither
guest restarted, including the guest on core2 — the host that became master. The
transfer's own stages were observed progressing through `repoint` to `done` with a
`succeeded` result.

Arm A was additionally re-run through the committed harness at a 30 s settle and reported
both guests continuous with a 31.3 s heartbeat advance, so the tracked code is the code
that produced these results.

### Single-writer observation

Sampled from all three hosts by resolving every `/proc/*/fd/*` that points at each guest
disk and reading its access mode from `fdinfo`:

| Guest disk | core1 | core2 | core3 |
| --- | --- | --- | --- |
| `beat-core3` | none | none | one `O_RDWR` holder |
| `beat-core2` | none | one `O_RDWR` holder | none |

Exactly one writable holder per disk, on the host running that guest, with none on the
third host.

### Cross-host exclusion, with its own negative control

On a scratch qcow2 on the same shared NFS repository: while a QEMU process on one host held
the image, a write from a second host failed with `Failed to get "write" lock — Is another
process using the image`. After the holder was stopped, the identical write from the same
second host succeeded. The check therefore discriminates rather than passing vacuously.

### Other behaviour confirmed natively in the same session

- Enrollment: three hosts each became master of an automatically created pool named after
  their hostname, with Core-generated pool and host IDs adopted unchanged.
- Controller-driven join of two members to one pool; all seven pool health checks healthy
  on all three hosts; both members holding verified signed authority replicas at a matching
  digest.
- Maintenance mode enable and disable, with the drain flag's only durable copy in the pool
  master's own `pool-peer.json` membership record.
- Placement refusal on a drained host (`409`) and placement success on a healthy host, with
  the guest disk verified present on the NFS server itself.
- Two members were each power-cycled to change their memory size; both rebooted and the
  pool reconverged to all-ready with no operator action, and the shared NFS repository
  returned healthy afterwards.

## Limits

These are not claimed, and several can never be claimed by this lab class:

- **Fence-verified promotion with running guests is not covered here.** Arm C is a
  *planned* master change. Promotion additionally fences the old master, which by design
  destroys every guest on the fenced host; continuity evidence for promotion must sample
  only guests on surviving hosts, and that run has not been made.
- No physical or BMC fencing. Only `virsh` fencing exists in this build, and no fence
  operation was performed in this campaign at all.
- Three hosts. Five-host scale is out of reach for this lab class.
- `nex-core-local` was not installed, so the libvirt StorageGuard hook, its AppArmor
  policy and the host-local API were not exercised. The single-writer evidence above is
  therefore about observed QEMU behaviour and NFS locking, **not** about Core's
  admission-time hook refusing a start.
- The single-writer sampling is a set of point observations around the operation, not a
  continuous trace; it does not exclude a transient overlap between samples.
- No partition, no interruption, no ownership-conflict case was exercised.
- Guest workloads were a synthetic heartbeat, not a real application workload.

## Reproducing

```
python3 tests/integration/pool_continuity.py \
    --lab-dir <disposable lab> \
    --guests <vm-uuid>=<host>,<vm-uuid>=<host> \
    --reader <host running neither guest> \
    --label <arm> --evidence <dir> [--settle <seconds>]
```

Exit status is 0 only when every sampled guest is continuous. Unit checks for the pure
helpers, including the cases that must report a restart, are in
`tests/test_pool_continuity.py` (9 checks, passing).
