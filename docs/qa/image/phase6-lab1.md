# Phase I-6 local lab 1: image-edition installer and boot security (QEMU/KVM)

Date: 2026-10-02. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md). Owner decisions: approved plan options, no remote
publishing, private hosts never in tracked files.

**Everything in this file is SIMULATION.** One workstation, QEMU/KVM, virtio disks, user-mode networking. Virtual Secure Boot (OVMF with
variables built by `virt-fw-vars`) and the virtual TPM (`swtpm`) never establish physical-hardware behaviour. Real = nothing here.

- API impact: none. Guide impact: verification status only ([install](../../guide/install-nex-core.md), [reinstall](../../guide/reinstall-keeping-state.md)).
  State owner: `core` (the installed host's disks are the record; the installer keeps nothing).
- Resource rules kept: one VM at a time (`-smp 2 -m 2048`), heavy commands under `systemd-run --user --scope` (`CPUQuota=200%`/`300%`,
  `MemoryMax=4G`/`3G`, no swap) with `nice -n 19 ionice -c3`, containers `--cpus 1..2 --memory 1..4g`. Peak use was not sampled. The 75-minute box
  was used up; scenarios not reached are listed under Not run.

## What was built and how the lab differs from a release

The full `installer/build.sh` needs the classic package closure, the matched libvirt and dlm builds and the Controller appliance, none of
which is under test, so `tests/integration/image_iso_lab.py` assembles a **lab ISO** (22 s, 1.3 GB) from the pinned Debian netinst ISO: the
image set (`.cache/core-image/out/release`, core1 sequence 1), the static `nex-hostimage`, the real `installer/nex-image-install.sh`,
`nex-config` compiled from `installer/gui`, the real `00nex-recheck`, `prepare` and `S66nex-wizard`, and a payload directory
`payload/<sha256>/` holding a **small lab fixture** (not the Controller appliance or VirtIO ISO). Differences from the real media, all lab-only:

- **The interactive wizard is replaced by a stub** that writes the same `config.ini` handoff from a record on a 1 MiB virtio disk (mode, disk
  serials, host name). Everything after the handoff is product code. The real text wizard is covered by `installer-text-wizard.py` against a
  fixture; the graphical one was only drawn (below).
- A lab-only partman hook adds a control shell on the second serial port to the staged `/etc` seed, and the installer log is mirrored to
  `ttyS0`. The stock Debian GTK initrd is used (no rebuilt cdebconf frontend); the boot entry is text mode.
- The root password and the host name are lab values. **No full ISO and no net-installer ISO were built.**

## Results (all virtual)

| # | Check | Result |
|---|---|---|
| 2 | Install on **1 disk** under virtual UEFI (OVMF), then boot: read-only erofs root, dm-verity status `V`, `/etc` overlay, `/var` ext4 on `nex-data`, `/usr` not writable, installer-seeded hostname and `br0` network config (DHCP address `10.0.2.15/24`), SSH host key generated at first boot and sshd active, payload under `/var/lib/nex/payload/<sha256>/` with the matching hash, `nex-core-local` active | **Pass** (install 100 s; the host reached a shell 12 s after the kernel started) |
| 2 | `nex-core-local` API answers on the host's address | **Not shown.** The run's probe asked `127.0.0.1:9443` and got connection refused (the service listens on the host address, `https://10.0.2.15:9443` is what the console banner prints); the probe now asks the host address and **was not re-run** |
| 2 | Install and boot on **2 disks** (md RAID1 `nex-data`) | **Not run** (time) |
| 2 | Root password from the wizard works | **Not established.** In an earlier boot of the same kind of host an SSH password login with the lab password was rejected and the serial `login` ended before the password prompt. The shadow entry was not inspected before the box ended; treat "root can log in with the password given to the installer" as **unverified and possibly broken** |
| 3 | Legacy BIOS (SeaBIOS) boot of the installer refused before any disk write | **Pass.** The installer printed "The image edition installs on UEFI firmware only ... No disk has been changed."; SHA-256 of the first and last MiB, and of the whole 25 GiB disk (pre-filled with random data), identical before and after |
| 4 | Repair on an installed 2-disk host | **Not run** |
| 5 | Wrong-disk safety (unselected disk untouched) | **Not run** (script written: part of `image_install_lab.py raid1`) |
| 5 | Interrupted install is re-runnable | **Not run** (script written: `interrupted`) |
| 6 | Virtual Secure Boot (OVMF `secboot` code, Microsoft keys in db, Nex development certificate in MokList, per phase 0 S5): signed UKI boots, `bootctl` "Secure Boot: enabled (user)", kernel "locked down from EFI Secure Boot" | **Pass** (virtual) |
| 6 | Unsigned UKI under Secure Boot | **Refused**: "Security Violation -- rejected probably by Secure Boot", no kernel started (virtual) |
| 6 | Signed UKI with one byte flipped (tampered) | **Refused**, same message (virtual) |
| 6 | Unsigned UKI with Secure Boot off | **Boots** ("Secure boot disabled") (virtual) |
| 6 | `nex-update check` and `apply -activate reboot` with Secure Boot enforcing | **Pass**: release 2 (signed UKI) booted under Secure Boot, still enforcing, `/etc` and `/var` data survived (virtual) |
| 6 | Rollback under Secure Boot | see "Rollback" below |
| 7 | vTPM (swtpm, TPM2): PCR 4, 7, 11 of the booted UKI | below |
| 8 | GUI wizard under Xvfb, Repair choice screenshot | **Done** (virtual display, synthetic `/sys`): see Defects 4 and 5 |

### Virtual TPM observations (observation only, nothing sealed)

PCR values are from `/sys/class/tpm/tpm0/pcr-sha256` (sha256 bank), under Secure Boot with the Nex certificate in MokList:

| Boot | PCR 4 | PCR 7 | PCR 11 |
|---|---|---|---|
| release 1, boot 1 | `0B4DEE1C...` | `EF271D8A...` | `ECE7213A...` |
| release 1, boot 2 (same UKI) | `0B4DEE1C...` | `EF271D8A...` | `ECE7213A...` |
| release 2 after update | `2C95B79F...` | `EF271D8A...` | `5BAD5DDE...` |

PCR 11 is identical on two boots of the same UKI and differs for release 2; PCR 7 is the same for both releases (same Secure Boot policy and
certificates); PCR 4 differs with the loaded UKI. Whether PCR 11 returns to the release 1 value after a rollback is **not shown**: the rollback step did not complete (below).

## Defects found and fixed (with regression tests)

1. **The image edition's `answers.cfg` kept only the locale.** Debian Installer runs its keyboard, root account, clock and network-name steps
   before the partman hook where the image installer starts, so after the wizard it stopped at "Configure the keyboard". Fix: `installer/gui/nex-config.c`
   keeps those answers (not the partman/GRUB ones) for Install; Repair gets neutral answers with a locked placeholder account (**the Repair answers
   are not exercised in a VM**). Regression test: `tests/integration/installer-hostname.py` (`check_image`; shown failing on the old file, passing now).
2. **`mkfifo` is absent in the installer's BusyBox**, so the progress reader failed (the install itself still ran, but without progress and with a
   misleading status path). Fix: `installer/nex-image-install.sh` uses a plain pipe and a status file. Tests: `tests/test_image_installer.py`
   (`test_install_needs_no_mkfifo_and_reports_the_tool_output_and_status`, failing-tool status preserved).
3. **The new data volume node can be absent for a moment** after `nex-hostimage` closes the disk (the kernel re-reads the table), which stopped the
   payload copy with "data volume ... is missing". Fix: wait for the node (30 s, `NEX_NODE_WAIT`). Test: `test_the_data_volume_node_may_appear_a_moment_after_the_image_is_written`.
4. **The graphical wizard never showed Repair image**: `gtk_widget_show_all()` returns immediately for a widget marked no-show-all, even when called on it.
   Fix in `installer/gui/nex-wizard.c` (`offer_repair`). Drawn and inspected under Xvfb (`tests/integration/installer-gui-repair.py`, screenshots
   in `.cache/phase6-logs/gui/`, local and gitignored): welcome page with the choice, "Repair image" selected, and the review page "Repair image and keep data" with its
   own unticked confirmation that blocks the button until ticked.
5. The Repair note ran off the screen and its field label was clipped; the note now wraps (`wrap_label_allocate`) and the label reads "Action". Still
   clipped, not changed here: the step list's first entry "Welcome" at 1024x768.

Not changed, observed: the md array is created without `--assume-clean`, so a fresh mirror resynchronises its whole data partition (about 16 GiB for
25 GiB disks); the lab used `discard=unmap,detect-zeroes=unmap` on the images to keep that off the host disk.

## Rollback

**Not shown.** After the update, `nex-update rollback` was issued while the host was still in its counted trial boot of release 2 (`phase=booted`,
ESP `nex-core_2026.10.1_1.efi nex-core_2026.10.2_2+2-1.efi`, not yet blessed). The host did not reboot within 420 s and the script stopped; the
command's own output (`/tmp/lastcmd.out` in the guest) was not read before the VM was killed, so the reason is **unknown** (a refusal during the
trial boot is the likely cause, not established). The earlier update-lab run (`image_update.py`, Secure Boot off) is the record for rollback itself.
The result of PCR 11 across update and rollback is therefore only half observed: release 1 -> release 2 differ.

## Not run

Two-disk install and boot, Repair on a mirror (with the nex-data byte comparison), wrong-disk safety, interrupted install, the net-installer variant,
a full `installer/build.sh` ISO, the root-password login (above), the `nex-core-local` API probe on the host address, and every physical-hardware
behaviour. Scripts for the first four exist in `tests/integration/image_install_lab.py` and are untested beyond a Python syntax check except
`single` and `bios`, which ran.

## Reproduce

```
python3 tests/integration/image_iso_lab.py --release .cache/core-image/out/release --hostimage .cache/core-image/out/nex-hostimage --out .cache/phase6/iso/nex-image-lab.iso
python3 tests/integration/image_install_lab.py --iso .cache/phase6/iso/nex-image-lab.iso --work .cache/phase6/lab --payload-sha <printed fixture sha256> SCENARIO   # one scenario per run: single, bios, raid1, repair (after raid1), interrupted
python3 tests/integration/image_sign_release.py --release REL --out SIGNED --key .cache/image-keys/nex-module.key --cert .cache/image-keys/nex-module.crt [--tamper]   # --out must be a new directory (a sibling SIGNED.image_sign_release marker lets a rerun replace it)
python3 tests/integration/image_sign_release.py --release REL1 --out $(dirname SIGNED1)/tampered --tamper --key ... --cert ...   # the tampered-UKI fixture image_sb_lab.py reads (unless --skip-negative)
python3 tests/integration/image_sb_lab.py --rel1 ... (see its docstring)
python3 tests/integration/installer-gui-repair.py
```
Throwaway container image `nex-lab-gtk:phase6` (gcc, GTK 2 dev, xvfb, swtpm) was committed from `debian:trixie-slim` for this lab and has been removed
(rebuild it with `apt-get install gcc libc6-dev pkg-config libglib2.0-dev libgtk2.0-dev libcrypt-dev xvfb xdotool xauth swtpm swtpm-tools python3 imagemagick x11-apps fonts-dejavu-core`).
Disk images, ISOs and signed release copies were deleted; logs and screenshots are kept in `.cache/phase6-logs/` (gitignored).
At the end no QEMU process, swtpm or lab container was left running.
