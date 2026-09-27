# Recovery S0 multi-backend native DR matrix

Date: 2026-09-26. Candidate: libvirt `11.3.0-3+deb13u3+nex4` on disposable
Debian 13 Core guests. This records a fresh multi-backend native snapshot and
consolidation campaign. It does **not** close Phase 6 product backup/restore,
Controller replacement, or independent storage failure-domain certification.

## Environment

- Disposable three-Core + storage fixture from
  [`cluster_ha_lab.py`](../../tests/integration/cluster_ha_lab.py) with prefix
  `recovery-s0-core.` (lab dir retained under ignored `.cache/`).
- Verified Debian 13 genericcloud image
  `debian-13-genericcloud-amd64-20260831-2587.qcow2`.
- Core memory after bump: Core A 8 GiB, Core B/C 6 GiB, storage 2 GiB.
- Storage guest exports: NFSv4.2 VM-disk (Core A only, anonuid→`libvirt-qemu`),
  authenticated SMB 3.1.1 sealed share (Core B), exclusive 2 GiB iSCSI LUN
  (Core B initiator ACL), plus local ext4 image directory.
- Patched libvirt binary packages built from
  [`packaging/libvirt/build-matched-libvirt.sh`](../../packaging/libvirt/build-matched-libvirt.sh)
  (`+nex4`); helper readonly/partial-chain policy check passed. Source-package
  rebuild after binary packaging hit a quilt refresh need on
  `preserve-readonly-revert-media.patch` and was skipped for this lab; binaries
  and staged handoff hashes were verified on the guests.

## Lifecycle matrix (`recovery_s0.py`)

Each backend ran the four-case rapid/paced one- and two-disk stopped external
snapshot lifecycle with `--expect-uuid-revert-overlays` (create A/B, revert,
branch, delete C/B/A, reopen).

| Backend | Node | Mount | Result |
|---|---|---|---|
| Local ext4 | core1 | `/var/lib/libvirt/images/nex-recovery-s0` | 4/4 passed |
| NFSv4.2 | core1 | `/mnt/recovery-s0-vm-nfs` | 4/4 passed |
| SMB 3.1.1 + seal | core2 | `/mnt/recovery-s0-vm-smb` | 4/4 passed |
| Exclusive iSCSI ext4 | core2 | `/mnt/recovery-s0-iscsi-exclusive` (`/dev/sda`, 2G) | 4/4 passed |

Raw evidence: ignored `.cache/recovery-s0-dr-20260926/{local,nfs,smb,iscsi}-lifecycle/`.

## Consolidation and midcommit interruption (`recovery_s0_consolidate.py`)

Public two-disk paused consolidation (`normal`) and libvirtd kill during an
active throttled `blockcommit` (`midcommit`), with enforcing AppArmor required.

| Backend | normal | midcommit |
|---|---|---|
| Local | passed | passed |
| NFS | passed | first attempt failed-preserved (`QMP commit job is not an active nonterminal target commit` — race before boundary latch); **retry passed** |
| SMB | passed | passed |
| iSCSI | passed | passed |

Raw evidence: ignored `.cache/recovery-s0-dr-20260926/consolidate-*/`.

## Limits

- Synthetic marker-only disks; no guest OS boot, no product snapshot/backup API.
- Single workstation / nested KVM; not independent hardware or storage domains.
- Phase 6 Core-owned backup repositories, schedules, restore verification, and
  Controller-replacement adoption are unimplemented and were not exercised.
- GFS2 / shared-block multi-writer paths were not in this campaign.
- NFS midcommit needed one retry after a missed active-job boundary.
