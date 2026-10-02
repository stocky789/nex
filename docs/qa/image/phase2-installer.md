# Phase I-2 installer: S7 result and image-edition install

Date: 2026-10-01. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), spike S7 and
"Installer" D1. Owner-approved options: UEFI-only image edition, initrd `/etc` overlay unit, Debian stays the
package source, Secure Boot supported not required, reinstall dev hosts.

- API impact: none (installer and media only). Guide impact: updated. State owner: `core`
  (the installed host's disks are the record; the installer keeps nothing).
- **Environment: simulation only** (QEMU/KVM, user-mode networking). No physical hardware.
- Status of this file: written during the run; sections say what was and was not exercised.

## S7: can Debian Installer host the Go slot writer? Decision: **yes, keep d-i**

Evidence is the retained log `.cache/installer-lab/s7/s7.log` (gitignored) from a boot of the real
`debian-13.6.0-amd64-netinst.iso` (d-i kernel 6.12.94+deb13-amd64, 2 GiB RAM, two 16 GiB virtio disks) with a
static Go probe and a script injected as `preseed/early_command`. That run was made by an earlier
(cancelled) agent of this programme; its log and inputs (`s7.sh`, `goprobe/main.go`) are on disk and were
read, not re-run, in this pass.

Observed in that log:

| Question | Observed |
|---|---|
| Does a static `CGO_ENABLED=0` Go binary run in the d-i initrd? | Yes: `goprobe: runs linux amd64 go1.27.1 ...`, `/tmp` had 1855 MiB free |
| Which tools are present only after `anna-install`? | `sfdisk` was absent before; `mkfs.fat`, `mkfs.ext4` and `mdadm` were already present from the default udeb set; the script still requests them, so a trimmed set keeps working |
| Udebs that cover the exec list of `nex-hostimage` | `dosfstools-udeb` (mkfs.fat), `e2fsprogs-udeb` (mkfs.ext4), `mdadm-udeb`, `ext4-modules`, `md-modules`; `mount`, `umount`, `udevadm`, `sync`, `modprobe`, `wget`, `sha256sum`, `dd` came with the base. `vfat` and `raid1` modules loaded (`modprobe` rc 0) |
| Absent in d-i (so `nex-hostimage` must not exec them) | `partx`, `wipefs`, `losetup`, `veritysetup`, `findmnt`, `lsblk`. `internal/hostimage` execs only `mount`, `umount`, `udevadm`, `mkfs.fat`, `mkfs.ext4`, `mdadm`, and best-effort `efibootmgr` (not in the udeb set; failure is a warning, the removable-path loader boots) |
| GPT, ESP FAT32, md RAID1 `--name=nex-data` 1.2, ext4 `nex-data`, mount, 64 MiB write, stop | All completed with rc 0 on two disks; `/dev/md/nex-data` appeared; `by-partlabel` showed the six `nex-*` labels |

That run laid the GPT with `sfdisk`, not with the Go code; `internal/hostimage` writes the GPT natively
(`gpt.go`) and was not exercised inside d-i in this pass. See [Outstanding](#outstanding).

**Menu pruning.** Not proven and not needed for this phase. The image edition leaves d-i's menu items
alone: the GUI/text wizard runs from `preseed/early_command`, `partman/init.d/00nex-recheck` hands the selected
disks to `nex-image-install.sh install` (which verifies, then clears them), which ends by rebooting, so `partman`,
`base-installer`, `apt-setup`, `grub-installer` and `late-install.sh` are never reached. Pruning udebs from
the initrd is a size optimisation left to a later phase.

**d-i versus an mkosi live installer.** d-i is kept: it already provides NIC/firmware/disk detection, the
pinned cdebconf frontends and the rescue entries (`installer/README.md`), and S7 shows the Go tool and its
tools run there. A live mkosi installer would re-implement those for no safety gain.

## What changed

Record `docs/changes/2026-10-01-image-installer.json`; CHANGELOG entry under `[Unreleased]`.

- `installer/nex-image-install.sh`: `preflight`, `install`, `probe`, `repair`. UEFI check first, then settings
  validation; the static `nex-hostimage` is copied from `/cdrom/nex/bin`; a net installer downloads the image
  set to RAM (memory-checked) before any disk is touched and the payloads onto the new `nex-data`.
- `installer/gui/nex-config.c`: image edition (media `edition` file, not `nex.edition=classic` on the command
  line) refuses non-UEFI firmware, enforces the 24 GiB floor, stages the `/etc` seed, writes
  `image.env` (`mode`, `layout=single|raid1`, `disks`) and no partman answers. Repair mode skips host identity checks.
- `installer/gui/recheck.sh`: the image edition `exec`s the installer straight after preflight and clears nothing
  itself (classic media keeps its disk-clearing loop). `install` runs `nex-hostimage verify` on the release and
  checks each media payload's SHA-256 first; only then does it stop the md arrays that hold a selected disk (by
  `/proc/mdstat` membership, never `--scan`) and let `nex-hostimage` wipe and lay out the disks. The data mirror is
  assembled from the install's own member partitions, not with `mdadm --assemble --scan`. Net installs verify the
  downloaded image the same way before the disks change; their payloads can only be checked after download, once
  `nex-data` exists (a failure there leaves an installed host and says so). The Controller-package stamp
  `/etc/nex/core-release` ships in the image (`image/mkosi.skeleton`) because the package's `preinst` needs it at
  build time; the installer no longer seeds a copy. Repair clears nothing.
- `installer/gui/wizard-text.sh` and templates: 24 GiB floor, **Repair image** question only when `probe` finds
  a complete data volume, fixed disk set, own confirmation.
- `installer/netinst-manifest.py`, `netinst-fetch.sh`: `payloads.tsv` (renamed from `netinst.tsv`) with
  `image/<file>`, `payload/<sha256>/<file>` rows; `NEX_NETINST_ROWS` picks the rows per edition; the hosting
  base URL is an argument only (CI configuration), none is tracked.
- `installer/build.sh`: `NEX_IMAGE_DIR` + `NEX_HOSTIMAGE_BIN` stage the image set, binary, installer script,
  `edition` file and `payload/<sha256>/` (appliance bundle from `dist/appliance`, gzip VirtIO ISO); Classic boot
  entries are added (the serial entries on image media are relabelled Classic: they never reach the Nex wizard); the net ISO drops the carried image and payloads.

## Checks actually run (2026-10-01, this pass)

- `python3 -m unittest tests.test_image_installer tests.test_image_composition tests.test_image_layout tests.test_netinst tests.test_netinst_network`: 61 tests OK, 1 skipped (2026-10-02 review-fix pass; `test_image_installer` now also pins
  verify-before-write, scoped array stop and assembly, the media payload check, and the serial menu labels; each was shown to fail with its fix reverted).
- Second review-fix pass (2026-10-02): the earlier fixes (verify before wipe, scoped array stop and assembly, shim chain on every ESP, installation stamp from `image/mkosi.skeleton`, zfs mount ordering, `busy_commands`, serial labels) were re-checked by reverting each one: the matching test failed every time. New: os-release now also carries `NEX_MIN_STATE_FORMAT`, `NEX_MD_GENERATION` and `NEX_MD_FALLBACK_SAFE`.
- `python3 tests/integration/installer-handoff.py`: all PASS, including that the hook clears no disk before the image installer runs.
- `sh installer/check.sh`: all PASS (edition floors asserted).
- `sh -n` / `bash -n` on the changed scripts; `python3 scripts/check_docs.py --worktree`; repository privacy test.

## Levels reached

1. S7 decision recorded (evidence from a retained earlier boot log, not re-run here): **reached**.
2. nex-config layout/floor/erase/UEFI refusal; Repair in the text wizard: **reached at script/unit level**;
   the graphical wizard's **Repair image** choice (`installer/gui/nex-wizard.c`: offered only when `probe` finds a complete
   data volume, fixed disk set, own review page, `mode=repair`): **written, compiled and text-pinned; never drawn or clicked** (see [phase2-packaging.md](phase2-packaging.md)).
3. `payloads.tsv` generalisation, offline/netinst media assembly in `build.sh`: **written, unit-tested for the
   manifest and fetch parts; `build.sh` image staging has only had a syntax check**. Splitting the appliance out of
   the `nex-core-local` deb is outside this write set (packaging) and not done: the image edition stages
   `dist/appliance` directly and the classic path is untouched.
4. `installer/check.sh` and netinst tests pass: **reached**.
5. QEMU image-edition install: **not run**. `image/out/` held no release directory (only a 0-byte
   `nex-hostimage` placeholder and a build log) and no ISO was built.

## Outstanding

- Build an ISO with `NEX_IMAGE_DIR`/`NEX_HOSTIMAGE_BIN` and boot it under OVMF with 1 and 2 disks (install and
  repair); BIOS refusal on SeaBIOS; net installer against a staged `payloads.tsv`.
- Run `nex-hostimage install` itself inside the d-i initrd (S7 proved the binary class and tools, not the Go
  GPT writer there); confirm `efibootmgr` absence only warns.
- The graphical wizard's Repair image choice is done (see level 2) but was only compiled (`debian:trixie-slim` container, no
  warnings) and pinned by text; it still needs a VM click-through. The `tests/integration/installer-disk.py` and
  `installer-handoff.py` pins for the image edition (24 GiB floor, UEFI refusal, Repair skips host identity checks, classic
  media unchanged, installer hand-off and net-installer rows) are also done: `installer-disk.py` passed under Xvfb in that
  container ([phase2-packaging.md](phase2-packaging.md)) and `installer-handoff.py` passed on the workstation on 2026-10-01.
- d-i menu/udeb pruning (size only).
- **Serial console.** The serial boot entries run Debian's own questions and the media preseed, never the Nex wizard, so
  on image media they install the Classic edition (they are labelled that way). There is no serial route to the image
  edition yet; a headless machine needs the text entry on a console it can display (or a BMC virtual console).
- **Secure Boot chain on the ESP.** `image/release.py` now installs shim (`BOOTX64.EFI`), the Debian-signed sd-boot
  (`grubx64.efi`) and MokManager on the removable path, per the phase 0 S5 layout. Only the unit test builds that tree;
  no image was rebuilt or booted in this pass. The firmware boot entry `nex-hostimage` registers still points at
  `\EFI\systemd\systemd-bootx64.efi` (a Secure Boot firmware cannot start it directly and falls back to the removable
  path); retargeting it at shim is a change in `internal/hostimage`, not made here.
- The net installer verifies its payloads (appliance, VirtIO ISO) only after download, once `nex-data` exists, because
  they exceed installer memory; a failure at that point leaves an installed host without payloads.
