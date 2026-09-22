# Mirrored system disk in the installer — partial verification

2026-09-22, development ISOs built from the working tree. Slice 3 (installer part) of the
[software RAID plan](../../plans/2026-09-22-software-raid.md).

**Result: source-level checks and the installer's own screens passed; installation was proved as
far as partitioning and base-system installation. No installed host was booted, so every boot,
detach and leftover-array check below is OUTSTANDING.** The lab run was stopped by the project
owner while installs were still running, because the concurrent lab VMs made the workstation
unresponsive. Nothing here is a claim that a mirrored host booted.

## Builds

| Item | Value |
|---|---|
| ISO A (before the swap fix) | `nex-core-0.4.3~raid1-amd64.iso`, SHA256 `fa0b5c3e57b64632be957f6cb0edb3baa46eb50f123183314f2da4a0dc586025` |
| ISO B (with `partman-basicfilesystems/no_swap`) | `nex-core-0.4.3~raid2-amd64.iso`, SHA256 `3300495069c9eb3756dd2317e85ad8993fdf00cccb95a63cf3ff9a97a2ccf46c` |
| Command | `NEX_VERSION=0.4.3~raid1 ./installer/build.sh`, then `NEX_VERSION=0.4.3~raid2 ./installer/build.sh` (a deliberate local version so the release lane's `0.4.3` artifacts were not overwritten) |
| Lab | Disposable QEMU/KVM guests, 4 vCPU, 6 GiB, new sparse 128 GiB raw files under `.cache/`, QEMU user networking with loopback forwards. No real disk, host or network was touched. |

`mdadm 4.4-11` was downloaded into the ISO's offline repository by the package closure
(`installer/packages.txt`), so the installed host does not need a mirror or network for it.

## Source-level checks (all run, all passed)

| Check | Result |
|---|---|
| `bash installer/check.sh` | Passed, including the new assertions: the RAID recipe (`partman-auto/method raid`, the `nex-mirror` expert recipe, the ESP entry, the `partman-auto-raid` recipe), `grub-installer/bootdev` for legacy BIOS, `force-efi-extra-removable`, `no_swap`, per-disk validation, the 10% rule, the multiselect Disk step in both frontends, `mdadm`, the mdadm.conf/initramfs regeneration, the ESP copy, and BUG-0413's existing `partman-md/device_remove_md` guard |
| `python3 tests/integration/installer-disk.py` | Passed. The real validator, compiled against a simulated device boundary, accepts a two-disk mirror and rejects a capacity mismatch, the same disk twice, an empty selection, an unsupported second disk and a changed serial on the second member |
| `python3 tests/integration/installer-text-wizard.py` | Passed, 17 scenarios including two new ones: a two-disk selection writes both disks to the handoff (`disk=/dev/sda;/dev/sdg;` and the matching size/serial/major:minor lists), and a mismatched pair is refused with the 10% reason and asked again |
| `python3 -m unittest tests.test_fresh_install_lab` | Passed (disposable-target guards still reject a foreign or symlinked target) |
| GTK wizard compile | `cc -O2 -Wall -Wextra` inside the pinned ISO builder image: clean |
| `nex-config` compile | `cc -O2 -Wall -Wextra` with glib and libcrypt: clean |

## Lab observations (what actually ran)

| Check | Result |
|---|---|
| Graphical Disk step, UEFI and BIOS, two disks | Passed at the time against the pre-rework list UI (Ctrl-click). After the tick-list rework, selection lives in the **Use** column only; `fresh_install_gui.py` now clicks ~x=180 without Ctrl (SR-065 / BUG-0511). That harness path has not been re-run in the lab. |
| Graphical Review, two disks | Passed. `ERASE AND INSTALL /dev/vdb + /dev/vda • Whole disks • RAID1 mirror, ext4`, and the confirmation reads `Erase all data on /dev/vdb + /dev/vda`. Unchecked confirmation still blocks the install and both targets remained all-zero |
| Text frontend, two disks | Passed. The disk question is a checkbox multiselect; the review names both disks, both capacities, both serials and `Layout: md RAID1 mirror of 2 disks, one ext4 root`, and defaults to **No** |
| Partitioning, UEFI two disks (ISO A) | Passed. `recheck.sh` cleared both disks, partman-auto-raid created the array and installation proceeded to the base system. Both lab disk files carry a GPT protective MBR and both were written (7.4 GiB on the second member while the mirror synchronised) |
| Partitioning, BIOS two disks, text frontend (ISO B) | Passed. Partitioning completed and the base system installed |
| **partman-md marker finding** | Established. BUG-0413's `: > /var/lib/partman/md` marker does **not** stop the new array. In `partman-md` 114 the marker only gates `init.d/md-devices` (module load and `mdadm --assemble --scan`), `lib/md-remove.sh` `device_remove_md` and the interactive RAID menu. `partman-auto-raid`'s `auto-raidcfg` loads `md-mod` itself and runs `mdadm --create` regardless, which is what the two-disk installs above observed. No change to the marker was needed, so leftover arrays on unselected disks stay out of partman |
| **Swap question finding** | Found and fixed. The mirrored layout has no swap partition, so partman asked `You have not selected any partitions for use as swap space … return to the partitioning menu?` mid-install on ISO A. ISO B seeds `partman-basicfilesystems/no_swap boolean false` for the mirror, and the ISO B text install partitioned and installed with no such question |
| **Multi-ESP finding** | Established from source. Debian 13's `grub-efi-amd64` has no multi-ESP support to reuse: `grub-efi/install_devices` and `grub-multi-install` do not exist in `grub2` 2.12-9+deb13u2 (they are Ubuntu's). Its postinst runs one `grub-install` against the single ESP mounted at `/boot/efi`. The installer therefore writes the other members' ESPs itself, by copying the ESP GRUB maintains, repeated by an APT `DPkg::Post-Invoke` hook |
| Unselected disk untouched (partial) | In one BIOS lab a single disk was selected while a second blank disk was attached: after partitioning and the base-system install, the second lab disk file still had zero allocated blocks and an all-zero first sector |

| Disk step reworked after the lab run (not lab-verified) | Both frontends now present a tick list of the same sorted disks with device, capacity, SSD/HDD/NVMe, model, serial and a blank/has-partitions flag; the graphical step uses a checkbox column with Space/Enter, a live summary of the resulting layout and inline red reasons for the 16 GiB and 10 % rules, preselects a lone eligible disk and offers a mirror tip. Compiled clean and asserted by installer/check.sh, installer-disk.py and installer-text-wizard.py. A headless capture was attempted and abandoned: no Xvfb in the builder image, no GTK 2 runtime on the workstation, and the wizard reads absolute /sys paths that cannot take a fixture bind mount in an unprivileged container. **The new screens have not been seen running**; the captures in this record show the previous single-column list |

| Mirror layout changed after the lab run (not lab-verified) | Review found that deriving the member partition index from the running firmware breaks when partman chooses GPT on a legacy BIOS machine (disks over 2 TiB), and that `grub-install --target=i386-pc` cannot embed on GPT without a BIOS boot partition. The mirror recipe now seeds `partman-partitioning/default_label gpt` and lays down a 1 MiB BIOS boot partition and the ESP on every disk, so the member is always partition 3 on both firmwares. The installs recorded above used the earlier two-partition layout, so the current recipe has not been run |

| ESP mount and copy fixes (not lab-verified) | After lane D's booted-host evidence and SR-058: fstab gains `nofail,x-systemd.device-timeout=5s`; `nex-sync-esp` remounts a surviving member ESP when `/boot/efi` is empty, rewrites that fstab UUID, creates `EFI/` on targets, and reports failure when it copies nothing. check.sh applies the install sed to a fixture fstab and `sh -n`s the ESP/TARGET heredocs. None of this has been seen on a booted host; outstanding item 6 still includes detach of the ESP-owning disk. |

Captures (not committed, under `.cache/software-raid/C/`): `uefi-03-two-disks.png`,
`uefi-04-review.png`, `uefi-05-progress.png` (the swap question), `uefi-06-after-swap.png`,
`m-uefi2-review.png`, `m-bios2-review.png`, `m-single-review.png`, `m-text-02-disks.png`,
`m-text-03-selected.png`, `m-text-04-erase.png`, `m-text-05-installing.png`.

## OUTSTANDING — not run, not claimed

None of these were executed; the stop order ended the lab run while four installs were still in
the base-system stage.

1. A completed two-disk UEFI install, its first boot, and booting with disk 1 detached, then
   reattached, then disk 2 detached.
2. The same for a legacy BIOS install (`grub-installer/bootdev` writing GRUB to every member).
3. A completed two-disk text-frontend install and its boot.
4. A completed single-disk install, confirming the unchanged layout end to end.
5. A leftover md array on an unselected third disk left untouched by an install onto another disk.
6. Everything that depends on a booted host: `mdadm` present in the installed system, the
   generated `/etc/mdadm/mdadm.conf`, the array in the initramfs, `mdadm --detail` showing a
   clean RAID1 with metadata 1.2, GRUB present on every ESP, the `nex-sync-esp` copy and its APT
   hook, the `nofail` ESP mount surviving a missing or replaced disk, and the absence of any swap
   partition. The current three-partition GPT layout in particular has never been installed.
7. Physical hardware, Secure Boot, NVMe/mixed-model disks and mirrors of more than two disks.

Until 1–6 are run on a disposable lab, slice 3's installer acceptance is open.
