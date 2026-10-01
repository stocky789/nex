# Read-only classic root: simulated-host lab (2026-10-01)

Phase I-1 of the [immutable Nex Core plan](../plans/2026-10-01-immutable-nex-core.md): new classic
installs get a separate `/var` (an md pair when mirrored), `/` mounted read-only, `/etc` and `/root`
kept on `/var`, and package installs inside a short writable window.

**This is a simulation of an installed host, not an installer run.** No Debian Installer, GRUB, Nex
package or Nex ISO was used; none was available in this environment. It proves the initramfs script,
the initramfs hook, `nex-root-ro.service` and `internal/rootfs` on a stock Debian 13 userland. It does
not prove the partman recipe, the installer, the package maintainer scripts or real hardware.

## Environment and method

- Host: x86_64 Linux with KVM, QEMU 11.1.1, Docker 29.8.1, Go 1.27.1. Nothing outside the repository's
  `.cache` lab directory was written; QEMU ran with no network and only the lab's raw files.
- Guest userland: `debian:trixie-slim` plus systemd, `linux-image-amd64` (6.12.111+deb13), `initramfs-tools`
  (klibc only, no busybox), `mdadm` and `dbus`. The repository's `packaging/core-local/initramfs-local-bottom`,
  `initramfs-hook` and `nex-root-ro.service` are copied in unchanged before the kernel is installed, so
  its initramfs contains them.
- Disks: raw ext4 files for `/` and `/var` (UUID-addressed fstab), booted directly with QEMU
  `-kernel`/`-initrd`; the mirror scenarios use GPT and two `mdadm` RAID1 arrays (`/` and `/var`) over two
  files, built by a helper boot inside the guest.
- A lab-only `nex-lab` deb has a `postinst` that writes `/boot` and `/etc`. It is installed through the
  repository's `internal/rootfs.OpenWritable` inside `systemd-run` with the same sandbox properties as
  `nex-core-local.service` (PrivateTmp, ProtectKernelTunables, ProtectKernelModules, ProtectControlGroups,
  NoNewPrivileges, RestrictSUIDSGID, LockPersonality, ProtectHome).
- Driver: `python3 tests/integration/readonly_root.py` (builds everything and boots seven times - one boot only builds the mirror disks - failing on any
  `FAIL`). Result lines come from a probe inside the guest over the serial console.

## Results

Final run on 2026-10-01 with the repository files as left in the working tree: 7 boots, each ending in
`DONE`, 80 `PASS` check lines and 0 `FAIL`.

| Scenario | What was observed |
|---|---|
| Single disk, first boot | `/` `ro`; `/etc` and `/root` are binds of `/lib/nex/etc` and `/roothome` on the `/var` device; `touch` under `/usr`, `/boot` and `/` fails; `/etc`, `/root`, `/var` writable; an edit to `/etc` is not on the root file system's own `/etc`; plain `dpkg -i` fails; `mount -o remount,rw /` then `ro` by hand works; `dpkg -i` through `OpenWritable` in the sandboxed unit installs the payload and writes `/boot` and `/etc`; `/` is read-only again afterwards |
| Single disk, second boot | `/` `ro`; `/etc`, `/root`, `/var` markers and the dpkg database and payload survived; the seeded `/etc` was not recreated (the dpkg-written `/etc` file is still there) |
| Classic control (fstab without `/var`) | Script is a no-op: `/` writable, `/etc` not a mount, `nex-root-ro.service` inactive |
| Two-array mirror, first and second boot | `/` is `md0`, `/var` is `md1`, `/etc` is `md1[/lib/nex/etc]`; both arrays `[UU]`; the same checks as the single disk pass, including persistence across reboot |
| Two-array mirror, one disk removed | Both arrays start with one of two members; `/var` mounts, `/etc` is bound, `/` is `ro`, markers present. The initramfs waited about 20 s for the missing member before starting the arrays degraded |

Findings that changed the code during the lab:

- The first initramfs script used `cp`, `rm` and `dirname`, which klibc does not have and busybox only
  provides when the recommended package is installed. The host booted as a writable classic host (the
  designed fallback) with the message `Could not prepare /etc and /root on /var`. A hook now copies
  `cp` and `rm` from coreutils into the initramfs.
- klibc `mount` rejects `--bind`; the script uses `-o bind`.
- Without the hook or the bind, `nex-root-ro.service` stays inactive and the host runs with a writable root
  rather than a read-only root with stale `/etc`.

Lab artefact, not a design property: in this minimal guest, writes that were not `fsync`ed were lost at
`poweroff` on both the read-only layout and a writable classic root (checked with a control file), so the
probe calls `sync` before powering off. Whether stock hosts lose data this way was not investigated.

## Unit tests (this change)

- `go test -race ./internal/rootfs ./internal/nexupdate`: the root's state is read through the runner that
  also writes (a caller whose own view of `/` is read-only never remounts a writable host); the window opens
  only for a read-only `/`, closes in order `rw`, install, `ro` even when the install fails, and refuses to
  install when the remount fails or the state cannot be read; windows are serialized by one lock, so a package
  install waits for a bootloader window instead of being made read-only underneath itself; a cancelled wait
  gives up.
- `go test ./internal/vmhost -run SystemDisk`: both arrays on a disk are repaired in one operation, including
  a sibling whose only working member is on another disk; a sibling with no working member, or a replacement
  already holding a sibling's member, is refused before the wipe; a resume never reports success for a failed
  or vanished replacement member; a healthy array is untouched; BIOS `grub-install` runs between a remount
  `rw` and `ro` even when GRUB fails; a writable root is never remounted.
- `python3 tests/integration/installer-hostname.py` compiles the real handoff renderer and checks the
  `nex-single` and `nex-mirror` recipes for one, two and three disks, including NVMe `p` suffixes.
- `sh installer/check.sh` passes.

## Not verified

- A real Debian Installer run of the new `nex-single` and `nex-mirror` recipes (partman acceptance, minimum
  sizes at the 16 GiB floor, BIOS MBR and UEFI GPT outcomes, `$iflabel` handling on a single disk).
- The installed `nex-core-local` package: `postinst` enabling `nex-root-ro.service`, `dpkg-trigger
  update-initramfs` producing an initramfs with the script at first boot, the `prerm` refusal.
- GRUB boot from either mirror disk, UEFI boot, Secure Boot.
- `nexupdate.ApplyLocal` against the real Nex APT archive, and every other Core service (libvirt, corosync,
  ZFS, DKMS) on a read-only root. The "no runtime writer outside `/etc`, `/var`, `/run`" claim rests on a
  static trace of the Go code and packaging scripts, not a run. The DKMS boot-time autoinstall
  (`dkms.service`) was not examined.
- The real replacement of a failed mirror disk with two arrays (unit tests only).
- Hosts upgraded in place from a classic install: they have no `/var` entry, are not converted, and were
  exercised only as the classic control above.
