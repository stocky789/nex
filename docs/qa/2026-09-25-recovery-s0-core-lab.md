# Recovery S0 Core lab preparation

Date: 2026-09-25. Candidate: `nex-core-local 0.3.6~recovery.s0`. This records
disposable package-based Core candidates and scratch storage prepared for S0
experiments. S0 implementation and phase acceptance remain open.

## Reproduction and provenance

The lab reused [`cluster_ha_lab.py`](../../tests/integration/cluster_ha_lab.py) with a new private prefix and
the verified Debian 13 genericcloud image below. It created three fresh Core
overlays and one storage guest, each on a private, uplink-free VDE network. Guest
SSH used QEMU user networking with host forwards bound to loopback. No host bridge,
host libvirt domain, existing virtual disk, or external target was used. All four
guests share one workstation and therefore do not establish independent hardware
or production failure domains.

The Debian image came from
`https://cloud.debian.org/images/cloud/trixie/20260831-2587/debian-13-genericcloud-amd64-20260831-2587.qcow2`.
Its SHA-512 matched Debian's published `SHA512SUMS`:
`8ea9faae810043a0b35b0149f05014f26705c2339ffb11ead308f33e844a87cc3ef46ec81d5262b38817b6a88af404874d48a5857ebe072ef6a31dfb6e371f50`.
Its SHA-256 was `85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3`.

To prepare another isolated fixture after placing that image at the relative cache
path, the only fixture-specific overrides are `BASE` and `PREFIX`:

```sh
python3 - <<'PY'
import runpy
from pathlib import Path

fixture = runpy.run_path("tests/integration/cluster_ha_lab.py")
scope = fixture["prepare"].__globals__
scope["BASE"] = Path(
    ".cache/recovery-s0-core/artifacts/debian-13-genericcloud-amd64-20260831-2587.qcow2"
).resolve()
scope["PREFIX"] = "recovery-s0-core."
fixture["prepare"]()
PY
```

The fixture starts Core guests at 3 GiB; this run orderly shut them down and
restarted the same fresh overlays at 8 GiB for Core A and 6 GiB each for Core B and
Core C. The separate storage guest used 2 GiB. Each guest had 2 vCPUs and a 32 GiB
virtual system disk. The Core package was built from the working tree source archive
and signed Controller appliance using the [`appliance/README.md`](../../appliance/README.md)
instructions and [`build.py`](../../packaging/core-local/build.py); the ephemeral
signing key stayed outside the repository. The source archive SHA-256 was
`4ab9b264c4d3649e4dc5b1770dfd72cb90ef1f3657f867e3209cb582325a4caa`. The matching
package SHA-256 was
`3c302fc06d96d7972b3b5da0eb1ca5255c16b8492d5969c585a2b9f2ed3d7eb4` (631,321,440
bytes). Its signed Controller qcow SHA-256 was
`e7bb7427c9d634b24402fb6de44f274cd17148dd0f6ad60feaa3b66eb983b920`. Package
identity, version and architecture, source/package digest, manifest signature and
signed image digest were independently verified.

This was a package-based candidate, not a Core ISO installation. The package
pre-install check rejected a fresh generic Debian image without
`/etc/nex/core-release`. In each explicitly disposable guest, setup then wrote the
same stamp the ISO installer writes (`NEX_CORE=1` and `INSTALLED_BY=nex-core-iso`)
before installing the package. The stamp does not turn the cloud image into an ISO
installation and is not presented as installer acceptance. Core and storage package
dependencies otherwise came from the guest's Debian 13 package sources. OVMF and
software TPM packages on Core B were checked against the ISO recipe's immutable
2026-09-20 Debian and security snapshots before use.

## Installed package and service state

All three Core guests reported `nex-core-local 0.3.6~recovery.s0`,
`libvirt-daemon-system 11.3.0-3+deb13u3`,
`qemu-system-x86 1:10.0.13+ds-0+deb13u1`, `nfs-common 1:2.8.3-1`,
`cifs-utils 2:7.4-1`, and `open-iscsi 2.1.11-1+deb13u2`. On Core B, the firmware
packages matched the installer snapshot: `ovmf 2025.02-8+deb13u1`,
`swtpm 0.7.1-1.5`, and `swtpm-tools 0.7.1-1.5`. The snapshot check used a temporary,
isolated APT sources/list/cache configuration, not the guest's system APT settings.
The pinned sources are recorded in [`installer/build.sh`](../../installer/build.sh).

`nex-core-local.service` and `libvirtd.socket` were enabled and active on all three
Core guests. On the storage guest, `nfs-server.service`, `smbd.service`, and
`rtslib-fb-targetctl.service` were enabled and active. Its installed server packages
were `nfs-kernel-server 1:2.8.3-1`, `samba 2:4.22.11+dfsg-0+deb13u1`, and
`targetcli-fb 1:2.1.53-1.3`. Core system storage and the storage guest's scratch
exports were on ext4.

## Scratch backend setup and observed checks

The storage guest provides the original repository export/share/target plus
separate VM-disk eligibility fixtures. Repository and VM-disk access use separate
exports and shares so the repository experiment does not imply QEMU disk
eligibility.

| Backend | Disposable setup | Observed check |
|---|---|---|
| NFS repository scratch | NFSv4.2 export with synchronous writes and `root_squash`, limited to the private lab network. | Core A and Core C mounted the export twice at `/mnt/recovery-s0-nfs` and `/mnt/recovery-s0-nfs-alias`. They exchanged root-squashed markers. This export remains dedicated to the repository/authority probe; its root-squash identity is not treated as proof of VM-disk eligibility. The separate [NFS primitive report](2026-09-25-recovery-s0-repository.md) records concurrent ownership, locking, and publication results. |
| NFS VM-disk scratch | A separate NFSv4.2 export uses `root_squash` and maps anonymous requests to Core A's observed `libvirt-qemu` uid/gid. Only Core A is allowed to mount it. | Core A mounted it at `/mnt/recovery-s0-vm-nfs`. A root-created file accepted `fchown` to the QEMU uid, `chmod 0600`, and `fsync`; its observed owner/mode matched. `libvirt-qemu` could read and write the owner-only file. This directly checks the access model used by `qemuStorageAccess`; it is an eligibility probe, not a VM workload or shared-storage proof. |
| Bounded-capacity NFS scratch | A separate 128 MiB file-backed ext4 filesystem is exported only to Core C using NFSv4.2, `root_squash`, and anonymous uid/gid mapping to Core C's observed `libvirt-qemu` identity. | Core C mounted it at `/mnt/recovery-s0-capacity-nfs`. The mount root and reserved `backup-enospc` child are `64055:992`, mode `0700`; a root-created file accepted `fchown` to that identity, and `libvirt-qemu` created, read, wrote, and removed a probe file. After setup the filesystem reported 117,440,512 bytes available. This is a bounded capacity fixture for a separate ENOSPC backup experiment; it is not evidence of backup failure handling or restore correctness. |
| SMB repository scratch | Guest-writable share `recovery-s0` used for the earlier marker check. | Core B enumerated and mounted it using SMB 3.1.1, then synced and read back a marker. That guest-writable mount does not establish the authenticated, encrypted Core VM-storage contract. |
| SMB VM-disk scratch | A separate authenticated share requires SMB encryption and uses a disposable Samba account. Core B's private credential file was mode `0600`; the credentials and account secret are kept only in the disposable lab. | Core B mounted it at `/mnt/recovery-s0-vm-smb` with the options returned by `smbMountOptions`: SMB 3.1.1, encryption, strict cache, the observed `libvirt-qemu` uid/gid, and file/directory modes `0600`/`0700`. A root-created file accepted `fchown` to the QEMU uid, `chmod 0600`, and `fsync`; `libvirt-qemu` could read and write it. This is a mount/access eligibility probe, not a VM workload or production server compatibility claim. |
| iSCSI | One new 2 GiB file-backed LUN. Its portal was bound to the private lab interface, and its ACL mapped only Core B's disposable initiator identity. | Core B discovered the target, confirmed the only new 2 GiB disk had no partition, mount, filesystem signature, or `blkid` result, then created ext4, mounted it read-write, and synced/read back a marker. No other host logged in or mounted that filesystem. |

The SMB VM-disk mount used the existing Core option builder and `qemuStorageAccess`
file-ownership model in `internal/vmhost/storage_mounts.go` and
`internal/vmhost/storage_native.go`; no product storage code changed. These checks
prove only the local lab's client and filesystem behavior. They do not
prove independent storage failure domains, production server compatibility, actual
backup creation, crash durability, partition fencing, takeover safety, or restore.
The disposable iSCSI filesystem and NFS/SMB markers are setup probes, not product
repositories. The NFS authority experiment is limited as described in its report.

## Cleanup and recovery

Wait until each experiment owner has removed only its uniquely named nested guest,
then unmount the lab's repository and VM-storage NFS mounts, the capacity NFS mount,
SMB mounts, and iSCSI mount. To stop the fixture processes,
select the exact generated lab directory and run:

```sh
python3 - <<'PY'
import runpy
from pathlib import Path

fixture = runpy.run_path("tests/integration/cluster_ha_lab.py")
scope = fixture["cleanup"].__globals__
scope["PREFIX"] = "recovery-s0-core."
fixture["cleanup"](Path(".cache/recovery-s0-core.<run>"))
PY
```

The fixture validates its disposable marker and recorded process command before
stopping only its four QEMU processes and private VDE switch; it does not remove the
lab directory or artifacts. Private run metadata contains the checked restart steps
and exact resource ownership. Do not clean any storage export or LUN until all tests
are complete and Core B has logged out; the capacity export is backed by its own
file and loop-mounted ext4 filesystem and must be removed separately from the
existing exports.

## API, guide, state, and phase

API impact: none. This is lab preparation and filesystem evidence; it adds no Nex
operation, contract, or durable product state. Guide impact: none because no operator
workflow changed. State owner: none. Roadmap impact: none; S0 and the
[recovery plan](../plans/2026-09-25-recovery-snapshots-backups-restores.md) remain
open. This evidence does not implement recovery snapshots, backup/restore, Controller
adoption, or same-host retirement and redeployment.
