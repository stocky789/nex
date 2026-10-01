# Host image footprint baseline (plan spike S8, work items 0–1)

Date: 2026-10-01. Plan: `docs/plans/2026-10-01-immutable-nex-core.md` (S8, composition items 0–1).

## Method (real run, container — not a booted host)

- Target: disposable `debian:13-slim` Docker container, Debian 13 (trixie), `main contrib`, no
  recommends, `policy-rc.d` denying service starts. Packages installed exactly as
  `installer/packages.txt` (the ISO builder's `--no-install-recommends` model).
- Measured with `scripts/measure-footprint.sh` (dpkg `Installed-Size` per group, plus `du`).
- The workstation was not measured. Raw output was kept under `.cache/footprint/` (gitignored).
- **Not measured** (container limits): initramfs, UKI, compiled `zfs.ko` (`/var/lib/dkms` is 2 MiB, so the
  build output was not confirmed present), `/usr/share/nex` (Nex packages not installed), EROFS compression,
  verity hash tree, the 1.6 GB of payloads, a full-update download. Slot sizing below is therefore a
  derivation from uncompressed package sizes, not a measurement of a built image.

## Results (current trimmed package list)

| Group | Packages | Installed-Size |
|---|---:|---:|
| base (kernel, qemu, libvirt, OVMF, libs, userland) | 395 | 706.6 MiB |
| dev-tools (linux-headers, gcc/cpp, perl, make, dkms) | 25 | 269.5 MiB |
| smb / storage clients | 15 | 32.7 MiB |
| zfs (dkms source + utils) | 6 | 25.5 MiB |
| cluster (corosync, pacemaker, sanlock, fencing) | 40 | 25.3 MiB |
| **Total** | 481 | **1059.6 MiB** |

`du`: `/usr/lib/modules` 106 MiB, `/usr/src` 106 MiB, `/var/lib/dkms` 2 MiB, whole root 1115 MiB.
Largest: kernel image 106 MiB, `gcc-14` 67 MiB, `qemu-system-x86` 63 MiB, `linux-headers-common` 60 MiB,
`libicu76` 37 MiB, `cpp-14` 34 MiB, `libruby3.3` 29 MiB, `libperl5.40` 29 MiB, `samba-libs` 25 MiB.

The `dev-tools` group (headers, compiler, DKMS toolchain) is 25% of the root and exists only to
build ZFS on the host; a prebuilt module in the image (later wave) removes it. `libruby3.3` and Perl
come in through Pacemaker/`pcs` and base tooling.

## Phase 1 trim delta

Dropping `virtinst`, `sudo`, `corosync-qnetd` (same container, adding the three back):
Installed-Size 1105.9 → 1059.6 MiB (**−46.4 MiB**, 50 packages), `du` root 1149 → 1115 MiB (**−34 MiB**).
`nftables` is kept.

Caller trace (no host caller found): `virt-install`/`virt-clone` appear only in the comment at
`internal/vmhost/firmware.go:81`, `installer/README.md` (now rewritten to use the Core API) and the old
`iso-smoke.py` package pin; Core drives libvirt directly. Host `sudo` is used only by website publishing
scripts (`packaging/website/*`, `scripts/website-ci-deploy.sh`), which run on the publishing server, not a
Core host. `corosync-qnetd*` binaries are called only from `internal/controller/witness.go`
(Controller-hosted); `internal/vmhost/pool_witness.go` uses only qdevice tooling.

## Derived sizing recommendation (derivation, not measurement)

- Root content today ≈ 1.06–1.12 GiB uncompressed; allow ≈ 0.2 GiB for built `zfs.ko`, initramfs
  and UKI ⇒ ≈ 1.3 GiB per release. At the plan's 1.5× headroom the minimum slot is ≈ 2 GiB. Slots cannot
  grow without moving `nex-data`, so recommend **4 GiB slots** (≈ 3× headroom; EROFS compression and
  dropping dev-tools later only add margin). Re-measure on a built image before fixing the layout.
- Minimum disk: 2 × 4 GiB slots + ~1 GiB ESP/boot state ≈ 9 GiB, leaving `nex-data` to hold a staged
  update (≈ 1.3 GiB) + payloads (1.6 GB) + working space + the default VM store. Recommend raising
  the floor from 16 GiB to **24 GiB** (≈ 15 GiB `nex-data`), with 32 GiB advised for real use.

## Host qnetd handling (resolved in this change set)

With `corosync-qnetd` dropped from the host package list, the host install no longer calls
`package-service-policy.sh runtime-qnetd` and the host's disable step no longer names the qnetd unit;
the Controller appliance path keeps both. Checked by static inspection of `installer/late-install.sh`
and `appliance/package-service-policy.sh` only: no ISO build or installation was run.
