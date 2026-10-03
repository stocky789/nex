# Phase I-2 image build and host-image tool: results

Date: 2026-10-01. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), I-2 (image, `internal/hostimage`).
**Environment: simulation only** (QEMU/KVM + OVMF, loop devices in a privileged container on one workstation). No physical
firmware, Secure Boot, TPM or multi-disk hardware was used. Two time-boxed sessions (40 min, then the BootProof run below); items marked **not run** were not run.

- API impact: none (no HTTP/RPC change; `nex-hostimage` is a local CLI). Guide impact: none yet (installer wiring is the
  installer lane). State owner: `nex-data` (`/var`) holds all mutable state; `/var/lib/nex/etc/{upper,work}` is the /etc overlay;
  `internal/hostimage` owns GPT layout, slot GUIDs and the ESPs.

## Reached levels

| Level | Result |
|---|---|
| 1. `go test ./internal/hostimage/...` | **Pass** (first session ~13 s; re-run in the second session returned the cached pass: the package was unchanged) |
| 2. Single-disk image builds and boots under QEMU/OVMF, with a reboot | **Pass** (second session, rebuilt from scratch): read-only erofs root, dm-verity status `V`, `/etc` overlay, `/var` on nex-data, `/etc` edit and `/var` data persisted across power-off and cold boot, SSH host key unchanged. |
| 3. Two-disk (md RAID1 nex-data), boot each disk alone | **Pass** (second session): both disks, disk 0 alone and disk 1 alone each reached the probe with the same checks, `/var` on `/dev/md127`, degraded flag 0/1/1, data written with both disks present read back with either alone. Needed defects 4-5 below (one lab-only, one product fix). |

## What exists

- `internal/hostimage`: `layout.go` (per-disk GPT plan: ESP 1 GiB, root-a/b 4 GiB, verity-a/b 64 MiB, nex-data rest), `gpt.go`
  (own GPT reader/writer, both copies, CRCs; verified readable by `sfdisk`), `verity.go` (hash-tree verification, cross-checked
  against `veritysetup` output in a test), `release.go` (manifest + SHA-256 + roothash verification; `SlotIDs` = roothash bits
  0-127 / 128-255), `slot.go` (S2 writer: random GUIDs on all disks -> write -> read back from the medium and verify the tree on
  every disk -> flip GUIDs to the roothash; refuses the running slot), `esp.go` (ESP converger: temp-then-rename, UKI last,
  prune, SHA-256 verify; ESP sync after bless), `etc.go` (seed into the overlay upper, shadow audit), `install.go`
  (`Install`, `Repair`: Repair rewrites partitions 1-5 and keeps the nex-data entry and bytes).
- `cmd/nex-hostimage`: `install`, `repair`, `esp-sync`, `etc-audit --lower DIR`, `verify`. Static binary, built by `image/build.sh`.
  `etc-audit` needs the image's `/etc` named by `--lower` (the overlay hides it; `mount --bind / /mnt` shows it at `/mnt/etc`).
- `image/`: `build.sh`, `release.py` (erofs lz4hc + verity with fixed UUID/salt/timestamps, UKI via ukify, ESP tree, manifest),
  `mkosi.conf`, `mkosi.postinst.chroot` (static os-release with BUILD_ID/SYSEXT_LEVEL, empty machine-id, no host keys, preset
  list of package-enabled units), `mkosi.finalize` (dpkg db tar and package list for layer builds), `mkosi.extra` (fstab,
  mdadm.conf, nex-firstboot / nex-boot-check / nex-esp-sync units, masks, presets), `initrd/` (overlay unit, emergency/rescue
  drop-ins that wait for `nex-initrd-reboot.service` (a boot with a boot counter) or `nex-initrd-reboot-paced.service` (one without), generic mdadm.conf, md re-probe udev rule and unit), `tools/Dockerfile` (pinned container), `base.packages`, `denylist.txt`, `snapshot`,
  `packages.lock` (317 packages), `scripts/image-lock.py`.
- Tests: `internal/hostimage/*_test.go`, `tests/test_image_layout.py`, `tests/integration/image_boot.py`.

## Measured (this run)

| Object | Size |
|---|---|
| Base tree | 708 MiB on disk (545 MiB `/usr`) |
| `root.erofs` (lz4hc,12; `/var` contents, `/boot`, dpkg db excluded) | 314,671,104 B; `root.verity` 2,490,368 B |
| UKI | 142,802,944 B in the first session (initrd 130 MB); 51,221,504 B after the initrd trim below |
| Kernel | 6.12.107+deb13-amd64 from the pinned snapshot only (an apt pin in the mkosi sandbox refuses `deb.debian.org`/`security.debian.org`; the first attempt had pulled 6.12.111 from the live security archive) |

## Defects found and fixed during the run

1. Live archives leaked into the base build (mkosi 25.3 hard-codes them): pinned off in `build.sh`.
2. `systemd-networkd` is enabled by Debian presets and stalled boot on `networkd-wait-online` (networking is ifupdown's): disabled in
   `10-nex.preset` and masked.
3. `nex-etc-overlay.service` ran before the generated `sysroot-var.mount` existed and failed once before succeeding on retry: now
   `Requires=`/`After=sysroot-var.mount initrd-parse-etc.service`. Observed passing in the second session (started once, no retry, in every boot).

## BootProof run (2026-10-01, second session): rebuild from scratch and boot levels

Resource caps used (owner's workstation is shared): every heavy command under `systemd-run --user --scope
-p CPUQuota=400% -p MemoryMax=8G -p MemorySwapMax=0 nice -n 19 ionice -c3`; containers `--cpus 4 --memory 8g`
(now defaults of `image/build.sh` and `tests/integration/image_boot.py`); QEMU `-smp 2 -m 2048`, one VM at a time; no
parallel builds. Peak resource use was **not measured** (no sampler was run); the caps held. About 2 GB of disk images in
`.cache/` were deleted after the runs.

- **Rebuild from scratch:** `image/out` base tree, package cache, release and sandbox removed, then `image/build.sh all`:
  about 2.5 min for the base tree with an empty package cache, 6 s for the release step. The package lock check
  (`scripts/image-lock.py check`) matched all 317 packages. The networkd masks/presets and the `nex-etc-overlay`
  `Requires=`/`After=sysroot-var.mount` ordering live in the tracked sources (`image/mkosi.extra`, `image/initrd`), so no
  hand patching of the built tree is left; the overlay unit started once, with no retry, in every boot below.
- **Install and boot are separate steps with their own budgets** (`image_boot.py --step install|boot`, `--install-timeout`,
  `--boot-timeout`; the install step kills its container when the budget is exceeded). Install took 2 s (single) and 13 s
  (two disks); each boot reached the probe in 16-17 s (46-47 s with one mirror disk missing: the 30 s md "last resort" wait).

### Observed results (final image, roothash `ba517ff7...`, label `dev`, sequence 0)

| Check | Single disk | RAID1, both disks | RAID1, disk 0 alone | RAID1, disk 1 alone |
|---|---|---|---|---|
| `/` is `erofs` with `ro` | pass | pass | pass | pass |
| root is dm-verity, status `V` | pass | pass | pass | pass |
| `/etc` is the overlay; `/var` ext4 on nex-data | pass | pass (on `/dev/md127`) | pass | pass |
| md degraded flag | n/a | 0 | 1 | 1 |
| `touch /usr/x` refused (read-only) | pass | pass | pass | pass |
| seeded hostname, first-boot SSH key, sshd active | pass | pass | pass | pass |
| `/etc` edit and `/var` file written in boot 1 survive the power-off and cold boot | pass (reboot) | n/a (written here) | pass | pass |
| SSH host key unchanged | pass | n/a | pass | pass |

"Reboot" here is a guest `poweroff` followed by a new QEMU process on the same disks and the same OVMF variable store (a
cold boot through firmware, shim-less sd-boot from the removable path and the UKI; this evidence predates the shim chain
in `image/release.py` and so did not exercise it), not an in-guest `reboot`. Failed
units seen: only `ipmievd.service` (no BMC in QEMU). `nex-boot-check` stays `inactive`: nothing pulls `boot-complete.target`
yet (I-3).

### Defects found and fixed in this session

4. **md RAID1 not usable in the initrd when the array was created by a newer kernel.** The install runs on the workstation's
   kernel (7.x). It stamped a field into the v1.2 superblock's reserved area (offset 224, `logical_block_size`), and the
   image's kernel (6.12.107) refused the member: `md: vda6 does not have a valid v1.2 superblock, not importing!`.
   Confirmed by clearing those bytes and the checksum on a copy (the array then assembled in the image's initrd). The
   lab step `clear_newer_md_fields` in `image_boot.py` removes the stamp after the install, with a note in its docstring.
   **Product risk, not fixed here:** after an image update to a kernel that stamps the field, a rollback to a 6.12 slot would
   fail to assemble `nex-data`. I-3 must either pin the md superblock-affecting kernel generation per `state_format` or
   test rollback across it. The real installer runs the image's kernel generation, so a fresh install is not affected.
5. **`/dev/disk/by-label/nex-data` never appeared for an md array assembled in the initrd** (boot hung 90 s, then the
   initrd emergency drop-in rebooted). `blkid` on the node worked and a later `udevadm trigger --action=change` created the
   link. Mechanism not established (observed with 6.12.107 and mdadm 4.4). Fix: `image/initrd` udev rule
   `65-nex-md-reprobe.rules` plus `nex-md-reprobe@.service` (one oneshot re-probe per md disk device without a recognised
   file system). After the change the array mounts as `/var` on every boot above.
6. **`mdmonitor.service` failed** (no recipient configured): `MAILADDR root` added to both `mdadm.conf` files.
7. **The boot probe printed nothing.** A serial getty on `ttyS0` hangs up other openers of the tty, so the probe now writes
   `NEXTEST k=v` lines to `/dev/kmsg` (`printk_devkmsg=on`), and its unit is ordered `After=ssh.service`, not after
   `multi-user.target`: in the two-disk runs `chrony-wait.service` (no clock source in QEMU) held `multi-user.target` for
   minutes. **Open:** for I-3, `nex-boot-check` and `boot-complete.target` must not sit behind `chrony-wait`; decide whether
   to disable it in the preset.
8. `image_boot.py` counted `raid1` lines in `/proc/mdstat` (2: personalities and the array); it now counts array lines.

### Initrd size

`KernelModulesInitrdInclude=` alone did nothing: mkosi 25.3 skips module filtering unless an exclude pattern exists
(`kmod.py: process_kernel_modules` returns when `exclude` is empty). `KernelModulesInitrdExclude=.*` makes the include list
effective (its patterns are matched against `kernel/<path>.ko.xz`, so `^fs/nls/` was also wrong and is now `/fs/nls/`;
`sha256_ssse3` and `crc32c-intel` were added so dm-verity and ext4 keep their accelerated implementations). Result:

| Object | Before | After |
|---|---|---|
| `base.initrd` (microcode excluded; userland archive 35.7 MB compressed + modules archive) | 130,562,796 B | 38,979,632 B |
| UKI `nex-core_dev_0.efi` | 142,802,944 B | 51,221,504 B |

The remaining initrd is the mkosi initrd userland (about 116 MB unpacked: `hwdb.bin` 13.5 MB, gconv 8 MB, perl 7 MB, lvm 6 MB,
networkd, btrfs tools). Trimming those is not cheap (mkosi initrd profile) and was not attempted. The module list is the
earlier agent's storage set (virtio, ahci, nvme, USB storage, mpt3sas, megaraid_sas, hpsa, smartpqi); other HBAs are not
covered and need to be added after hardware inventory. All boots above passed with the trimmed list.

## Open items / not run

- **Not run:** power-cut during a slot write on a real device,
  Secure Boot / signed UKI (the build supports `NEX_UKI_KEY`/`NEX_UKI_CERT`, untested), layer double build (the base image double
  build is recorded in `phase2-repro.md`),
  `nex-boot-check` pass on boot (I-3), firmware boot entries (`efibootmgr` is not in the tools container; the removable-path
  loader booted each disk alone under OVMF).
- Disk replacement, a second slot, and an in-guest `reboot` (as opposed to power-off and cold boot) were not exercised.
- The "smb" package group (`cifs-utils`, `smbclient`) is outside the base per the assignment and no layer ships it yet.
- The unit-test `TestWriteSlot...` oversize-release case was dropped (it needs a 4 GiB fixture).

## Review follow-up (2026-10-02): what changed after the 16-lane review

Changes to the sources below were checked with unit tests and a stubbed script run only; **none was booted in a VM**, so
the initrd and boot-check behaviour is unverified on a real boot.

- **Initrd emergency/rescue no longer reboots forever, and never ends at a locked shell.** The drop-ins used to replace
  `ExecStart` with `systemctl --force reboot`; on a blessed entry (no `+tries` counter) a failed verity check or a missing
  `nex-data` then looped without limit. Leaving the shipped shell instead is no fix: the initrd's `/etc/shadow` has
  `root:*` and `SYSTEMD_SULOGIN_FORCE` is not set, so `sulogin` refuses the login and the host would sit at a console
  message. Forcing it would hand a passwordless root shell to anyone at the console of a machine that holds the data
  volume, so that option was rejected. Now emergency/rescue want two mutually exclusive units, ordered before them:
  `nex-initrd-reboot.service` runs when the EFI variable `LoaderBootCountPath` exists (sd-boot sets it only while the
  booted entry still carries a counter): one try is spent and the previous image is tried after the last. Otherwise
  `nex-initrd-reboot-paced.service` waits 60 s and reboots. Nothing counts those reboots (a tmpfs counter dies with the
  reboot, and writing an EFI variable from the initrd was judged not worth the risk), so the loop is unbounded but slow:
  a transient fault (late disk or array) recovers by itself, the console message stays readable, and an operator can pick
  another entry or the installer's Repair in the firmware boot menu. The Controller guest's initrd overlay carries both units
  and the drop-ins: its UKI has no counter, so it always takes the paced path, and the host sees a guest that never becomes
  ready. Whether `efivarfs` and that variable are visible in this initrd is **not observed**; if not, every failure takes
  the paced path, which is still a reboot. Not booted in a VM.
- **`nex-boot-check` is bounded by time.** `NEX_BOOT_CHECK_BUDGET` (default 600 s) ends the retry loop, no layer check
  starts after it, the writability probes and each `boot-check.d` script run under `timeout`, and `nex-bless-boot bad`
  is limited to 60 s, so the script reaches its bad mark and reboot well inside `TimeoutStartSec=900`. Smoke run with
  stubbed `systemctl`/`findmnt` and a 4 s budget: exit 1 after 4.1 s with the problem list (tries set to 100).
- **`/root` is a link to `/var/roothome`** (`mkosi.postinst.chroot`; tmpfiles creates the 0700 directory), so root's
  `authorized_keys`, history and `known_hosts` persist on `nex-data`. Not observed on a booted host.
- **UKI version sort.** sd-boot orders entries by `IMAGE_VERSION` (VERSION when absent) as a version string. The UKI's
  os-release now carries the zero-padded sequence in both and nothing else (the label stays in `PRETTY_NAME`,
  `VERSION_ID` and `BUILD_ID`), so the order is the sequence order `BootOrder` models whatever the label looks like.
  `release.py` rejects a `NEX_LABEL` outside `[A-Za-z0-9.~_-]+` and a negative `NEX_SEQUENCE`, the set the release reader accepts.
  The sd-boot comparison itself is from the systemd source, not run here.
  One-time caveat: a UKI built before this change carried `IMAGE_VERSION=<label>.<sequence>`, which sorts above the new
  bare zero-padded sequence. No released host has that format (the feature is unreleased); a development ESP built earlier must be
  reinstalled, not updated.
- **`etc-audit`** without `--lower` is an error (the old default path existed nowhere and the audit silently printed nothing),
  and a `--lower` that is missing or not a directory fails.
- **`WriteSlot`'s final GUID check** finds its partitions by entry number (it used the compacted list position, which is wrong
  or panics when an earlier entry is empty); `zeroHead` (unused) was deleted; the two copies of the verity fixture writer are
  one; `TestRepairKeepsData` asserts the `nex-data` entry and disk GUID on every disk.
- `tests/test_image_layout.py` now also pins the data partition number and label, and the partition names used by the installer
  script and the image labs, against `internal/hostimage/layout.go`. The sizes in `image/spike/mkosi/mkosi.repart` (512 MiB ESP, 2 GiB
  roots, 1 GiB data) are **not** the product layout (1 GiB / 4 GiB / rest): that directory is the Phase 0 spike record referenced by
  `phase0.md` and is not used by the product, so it is left as it is and not tested.
