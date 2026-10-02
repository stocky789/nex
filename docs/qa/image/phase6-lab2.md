# Phase I-6 local lab 2: installer and boot scenarios lab 1 did not run (QEMU/KVM)

Date: 2026-10-02. Follows [lab 1](phase6-lab1.md). Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md).

**Everything in this file is SIMULATION.** One workstation, QEMU/KVM, virtio disks, user-mode networking, OVMF (virtual UEFI), virtual
Secure Boot and a virtual TPM (`swtpm`). Nothing here establishes physical-hardware behaviour. Real = nothing here.

- API impact: none (the rollback fix makes the engine accept a documented request in a phase where the Updates panel already offers it; the
  endpoint, its body and its error codes are unchanged; `docs/API.md` states the behaviour). Guide impact: verification status only
  ([install](../../guide/install-nex-core.md), [reinstall](../../guide/reinstall-keeping-state.md)). State owner: `core` (the host's disks and
  `/var/lib/nex/image/state.json` are the record; the installer keeps nothing).
- Resource rules kept: one VM at a time (`-smp 2 -m 2048`), heavy commands under `systemd-run --user --scope` (`CPUQuota=200%` / `300%`,
  `MemoryMax=4G` / `3G`, no swap) with `nice -n 19 ionice -c3`, containers `--cpus 2 --memory 4g`. Peak use was not sampled.

## What was built and how the lab differs from a release

- **Lab release (core, with `nex-core-local`).** A **lab-patched copy** of the lab-1 core release, not a release built by `image/build.sh`:
  `image/release.py base` and `release` (the product's erofs, verity and UKI step, in the pinned tools container) ran over the extracted lab-1
  tree plus the two source changes under "Root login" (the `login` package from the pinned snapshot, SHA-256 checked, and the sshd drop-in).
  The full `mkosi` rebuild with the changed `image/base.packages` was **not run** (it downloads the whole closure again), and the lock entry
  was taken from the snapshot's package index, not compared with a fresh build's `dpkg` list. The kernel and initrd are those of the lab-1 UKI.
- **ISO.** The lab ISO of lab 1 (`tests/integration/image_iso_lab.py`): stock Debian netinst initrd, the real installer pieces, a lab wizard
  stub that writes the same `config.ini` handoff. Lab-only additions in this lab: the stub can hold the selected disks' mirror open
  (`hold=1`, read-only assembled and mounted) and the ISO's lab hook carries the control shell as before. No full ISO and no net-installer ISO.
- **Update lab release 2** for the Secure Boot rollback run: the lab-1 release 2 with the freshly built `nex-update` (the engine fix) copied
  in and the release step rerun; release 1 is unchanged. Both UKIs signed with the development key by `image_sign_release.py`.
- The root password, host name and every key are lab values.

## Results (all virtual)

| # | Item | Result |
|---|---|---|
| 1 | Root login: cause found, fixed, regression test | **Pass** (see "Root login") |
| 1 | Root password on the serial console, in a VM | **Pass** (2-disk host first boot, both single-disk boots, after Repair): wrong password refused ("Login incorrect"), the installer's password opens a root shell |
| 1 | Root password over SSH (password only), in a VM | **Pass**: wrong password refused, right one opens a root shell |
| 1 | Root password through nex-core-local `/api/local/v1/session`, `/updates`, CSRF | **Pass** (forwarded host address `10.0.2.15:9443`): wrong password 401, right 200 with a CSRF token, `GET /updates` 200, `DELETE /session` 403 without and 200 with the token, 401 afterwards |
| 1 | `nex-core-local` `/healthz` on the host address (lab 1: not shown) | **Pass**: 200 from inside the guest on the host's own address and through the forwarded address |
| 2 | 2-disk RAID1 install and boot (both disks) | **Pass**: install 102 s; read-only erofs root, dm-verity `V`, `/etc` overlay, `/var` ext4 on md RAID1 (`degraded=0`), hostname, `br0`, SSH host key, `nex-core-local` active, payload hash |
| 2 | Boot with disk 0 alone, disk 1 alone | **Pass** both: array assembled degraded (`degraded=1`), `/var` on md, every check above, root login at all doors |
| 2 | `--assume-clean` decision | **Not used**, with an experiment: see "RAID1 resync" |
| 3 | Repair image on the installed 2-disk host: nex-data bytes | **Pass**: SHA-256 of the whole `nex-data` partition identical on both disks before and after; three known files hash the same |
| 3 | Slots and ESPs re-laid and bootable | **Pass**: ESP of disk 0 and slot A of disk 1 had been zeroed first; after Repair both ESPs hold the UKI, the host boots from both disks together and from each alone |
| 3 | Repair refused when nex-data is incomplete | **Pass**: one of two mirror members attached, refused after 85 s ("No complete Nex Core data volume was found ... No disk has been changed"), disk byte-identical (full SHA-256) |
| 4 | Unselected third disk unchanged | **Pass**: random data at start, middle and end; byte-identical (full SHA-256) after the install, and after the installed host booted with it attached |
| 4 | Install refused when a selected disk holds a mounted array | **Pass**: mirror assembled read-only and mounted before the installer started; refused after 88 s ("Could not stop md127, which holds a selected disk. No disk has been changed."), both disks byte-identical |
| 5 | Interrupted install, two cut points, then a fresh run | **Pass**: QEMU killed (`kill -9`) at the start of the disk layout and again at the start of the slot write, then a fresh installer run on the same disk completed (98 s) and the host booted with every check passing and root login at all doors; one in-guest `/healthz` probe raced the service start (see "Interrupted install") |
| 6 | Rollback under virtual Secure Boot: cause | **Product defect found and fixed** (see "Rollback") |
| 6 | Rollback during the trial boot under Secure Boot, PCR 11 | **Pass** (virtual): rolled back in the counted trial boot, the host rebooted into release 1, which ran again under Secure Boot; PCR 11 `ECE7213A...` -> `53DC7A5D...` (release 2) -> `ECE7213A...` after rollback |
| 7 | netinst variant | **Not run** (see "Not run") |

## Root login

Lab 1 left "root can log in with the password given to the installer" unverified: an SSH password login was rejected and the serial `login`
ended before the password prompt. Two independent causes, both in the image, neither in the installer's seeded shadow entry:

1. **The image had no `login` program.** `login` is Debian priority *required* but not *Essential* (it is `Protected: yes`), and
   `image/base.packages` did not list it, so mkosi never installed it. `dump.erofs` of the lab-1 `root.erofs` finds no `/usr/bin/login` and no
   `/usr/sbin/nologin`. The serial getty is `agetty -o '-- \u'`, which asks for the user name and then executes `/bin/login`; with the file
   missing the sign-in ends there, before any password prompt. Fix: `login` in `image/base.packages` and `image/packages.lock`
   (`1:4.16.0-2+really2.41.5-0+deb13u1`, source `util-linux 2.41.5-0+deb13u1`, already the locked util-linux; its dependencies are already in the
   lock). The Controller guest list (`image/controller/controller.packages`) lacked it too and got the same line; that guest was **not built or
   booted** here.
2. **SSH refused a root password.** sshd's default `PermitRootLogin` is `prohibit-password`, and the image shipped no
   `sshd_config.d` drop-in; the classic edition writes `00-nex-core.conf` (`PermitRootLogin yes`, `PasswordAuthentication yes`) from
   `late-install.sh`, and the guide promises "fresh installs allow root password SSH on the management network". Fix:
   `image/mkosi.extra/etc/ssh/sshd_config.d/00-nex-core.conf` with the same two lines.

Regression test: `tests/test_image_layout.py::test_root_can_sign_in_at_the_console_and_over_ssh` (the package lists, the lock and the drop-in).
Proof in a VM: the lab release with both changes, installed by the real installer, then the checks in the table above (`image_install_lab.py`,
`root_access`). The failing state was **not re-run in a VM** in this lab; the cause rests on the shipped image's contents and on lab 1's
observations.

Also observed, **not changed**: the installer stages a one-line `/etc/shadow` (root only) in the `/etc` overlay's upper layer, which hides the
image's own `shadow` (the booted host reports 31 `passwd` entries and 1 `shadow` entry). Nothing failed because of it (no ordinary account
exists, and daemons switch users without PAM), but later image changes to `shadow` can never reach such a host, and the 30 system accounts
have no shadow entry. A cleaner seed would carry only root's hash and let first boot set it into the image's `shadow` (`usermod -p`); that
needs an image rebuild and was not attempted.

## RAID1 resync

`nex-hostimage` creates the mirror without `--assume-clean` and still does not: the members are partitions of disks that held anything
before, so the two copies of every block never written differ, and a scrub (`checkarray`, which Debian schedules monthly) reports them as
mismatches that look like corruption to anyone reading `mismatch_cnt`. Measured in the guest, two 32 MiB loop members with different
content, scrubbed once: `--assume-clean` gives `mismatch_cnt=63488`, the default initial resync gives `0`. Skipping the resync would be
safe only for members known to be identical (for example zero-filled), and zeroing them costs the same I/O the resync does. The resync
runs in the background; the installer stops the array before it ends ("md: resync interrupted" in the installer log) and the first boot
finishes it ("md: resync done" 79 s after the kernel started in the lab, on sparse 25 GiB images). A comment in
`internal/hostimage/install.go` records the decision.

## Rollback

Lab 1 issued `nex-update rollback` while release 2 was in its counted trial boot (`phase=booted`, `nex-core_2026.10.2_2+2-1.efi`, not yet
blessed), sent it to a file nobody read, and waited 420 s for a reboot. Cause: **an engine refusal that contradicts the Updates panel and
the guide.** `Engine.Rollback` returned `409 already_armed` ("a release is armed; reboot or wait for the boot assessment") whenever an
armed record existed, and the record stays armed through the whole trial boot. The panel (`web/src/image-updates.tsx`) and
[Roll back a host update](../../guide/roll-back-a-host-update.md) offer **Roll back** in the phases `committed` and `booted`. Not the
guard (`guardTarget` was not reached) and not the harness's timing; the harness only hid the answer.

Fix (`internal/nexupdate/image.go`): in the phase `booted` the running update stands for the running release and the release it replaced is
the target; the abandoned update becomes the retained previous once the replaced release is blessed. A release that is armed but has not
booted yet is still refused with `already_armed`. Tests (`internal/nexupdate/image_fixes_test.go`):
`TestRollbackDuringTheTrialBootReturnsToTheReleaseItReplaced` fails on the old function body with
`already_armed: a release is armed; reboot or wait for the boot assessment` (run against the old body through a Go build overlay) and passes now;
`TestRollbackIsRefusedWhileAnUpdateIsArmedButNotBooted` keeps the other refusal. The harness (`image_sb_lab.py`) now reads the command's answer
and fails at once on a refusal instead of waiting.

Observed in the rerun (virtual Secure Boot: OVMF `secboot` code, Microsoft keys in db, the Nex development certificate in MokList; swtpm TPM2;
sha256 bank, `/sys/class/tpm/tpm0/pcr-sha256`; observation only, nothing sealed): release 1 boot 1 and boot 2 (same UKI) PCR 4 `0B4DEE1C...`,
PCR 7 `B5481123...`, PCR 11 `ECE7213A...`; release 2 after the update PCR 4 `655AF936...`, PCR 7 unchanged, PCR 11 `53DC7A5D...`; release 1 after
the rollback issued during release 2's trial boot (`phase=booted`, ESP `nex-core_2026.10.1_1.efi nex-core_2026.10.2_2+2-1.efi`) PCR 4 `0B4DEE1C...`,
PCR 7 unchanged, PCR 11 `ECE7213A...`, the release 1 value. `/etc` and `/var` data survived the update. The rollback command's own answer did not
reach the console (the host was already rebooting), so its acceptance is shown by the reboot into release 1, not by a printed result. This run used
the lab-1 release 1 unchanged and a release 2 with the fixed `nex-update`; PCR 7 differs from lab 1's `EF271D8A...` because the variable store was
rebuilt for this lab (same method, different file), and release 1's PCR 11 and PCR 4 equal lab 1's. Steps 2 of the harness (unsigned, tampered
UKI, Secure Boot off) were skipped with `--skip-negative`; they passed in lab 1.

## Interrupted install

Single disk, 25 GiB. The VM was killed when the installer's log showed `laying out` (85 s after the VM started, the new GPT was already on
the disk) and, on the same disk, when it showed `<disk>: writing root to nex-root-a` (85 s again: nex-data created, the slot write begun,
`/etc` not yet seeded, payloads not yet copied). After each cut the disk carried a partition table with all six Nex partitions and no
complete system. A third run of the unchanged installer on that disk (no wipe by the harness) completed in 98 s, and the host booted:
read-only erofs root, dm-verity `V`, `/etc` overlay, `/var` ext4, hostname, `br0`, SSH host key, payload hash, root login on the console, over
SSH and through the local API (all as in the table). The in-guest `/healthz` probe of that boot failed once with "connection refused" 12 s
after the kernel started, before the service listened; the forwarded `/healthz` check a minute later in the same boot answered 200. The probe
now retries for up to 40 s; the scenario was not re-run after that change. What a cut leaves behind besides the partition table (the state of
the ESP, slot and data volume at each cut) was not inspected.

## Not run, or not established

- **netinst variant.** Not run. It is produced only by the tail of `installer/build.sh`, after a full offline ISO build, which here needs the
  Controller appliance's Debian cloud base image (absent from the workspace), the appliance signing key (absent) and an HTTPS release URL
  (`NEX_NETINST_BASE_URL`, i.e. a published location; no publishing is done from here), and the download path needs an HTTPS host the
  installer environment trusts. The script-level tests of the net path (`tests/test_image_installer.py`) are the only check of it.
- A full `installer/build.sh` ISO and a full `image/build.sh` rebuild with the changed package list.
- The real interactive wizards (stubbed here as in lab 1), the Controller guest's console login, the failing state before the root-login fix
  in a VM, and every physical-hardware behaviour (firmware, Secure Boot keys, TPM, disks).

## Reproduce

```
python3 tests/integration/image_iso_lab.py --release REL --hostimage nex-hostimage --out nex-image-lab.iso
python3 tests/integration/image_install_lab.py --iso nex-image-lab.iso --work WORK --payload-sha SHA raid1 raid1-alone held-array repair repair-boot interrupted single bios
python3 tests/integration/image_sign_release.py --release REL --out SIGNED --key KEY --cert CRT
python3 tests/integration/image_sb_lab.py --rel1 SIGNED1 --rel2 SIGNED2 --unsigned-rel1 REL1 --keys DIR --trust T --vars-sb V --vars-off V0 --hostimage BIN --workdir DIR --skip-negative
```

Disk images, the ISO, the lab releases and logs stay in the gitignored `.cache/phase6/`; the disk images and ISO were deleted at the end. No
QEMU, swtpm or lab container was left running.
