# Phase I-0 image spikes: results

Date: 2026-10-01. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), section "I-0 spikes".
Scope run here: S1-S6 and S9. S7 (installer host) and S8 (full size budget) were not part of this run.
**Environment: simulation only.** Everything ran in disposable QEMU/KVM + OVMF VMs on raw image files under
`.cache/spike/` (gitignored). No real disk, firmware, TPM or Secure Boot hardware was used, so none of this
establishes physical-hardware behavior. The run was cut short at the owner's request: items below marked
**not run** were not run; nothing is reported as passed that was not observed.

- API impact: none. Guide impact: none. State owner: none (no product code; lab scripts only).
- Lab sources (throwaway, not product code): `image/spike/` (`tools/Dockerfile`, `mkosi/`, `build.sh`, `vm.py`,
  `lab.py`, `scripts/`). The per-spike orchestration was driven interactively through those helpers and is
  reproduced as commands below, not as a scripted suite.

## Tooling and inputs

| Item | Value |
|---|---|
| Tools | Debian 13 container `nex-spike-tools` (`image/spike/tools/Dockerfile`): mkosi 25.3, systemd/ukify/repart 257.13, erofs-utils 1.8.6, cryptsetup (veritysetup), sbsigntool, python3-virt-firmware 24.11, diffoscope 297, shim-signed. Workstation distribution was not Debian; no host package was installed. |
| Package source | `snapshot.debian.org` archive `20260929T000000Z` (trixie), set as `Mirror=` (mkosi 25.3 has no `Snapshot=` option) |
| Kernel | 6.12.111+deb13-amd64 (`erofs`, `dm-verity`, `overlay` are modules; `OVERLAY_FS_INDEX`, `METACOPY`, `REDIRECT_DIR` are all off in this config) |
| VM | QEMU 11.1 + KVM, OVMF 4M (`OVMF_CODE.4m` / `.secboot.4m`), virtio-blk, control shell on a second serial port |
| Image | `mkosi` `Format=disk`, `Bootloader=systemd-boot`, UKI; repart: ESP 512 MiB, `nex-root-a` 2 GiB erofs + `nex-verity-a` 64 MiB (`Verity=data/hash`), empty `nex-root-b`/`nex-verity-b` of the same sizes, `nex-data` 1 GiB ext4 |
| Build | `image/spike/build.sh <outdir> -f build` (about 2.5 min with a warm package cache, about 5.5 min cold) |

## Result table

| # | Result | Evidence (short) |
|---|---|---|
| S1 Slot discovery | **Pass, with a real caveat** | Repart produced the planned GPT; the root and verity partition UUIDs are exactly roothash bits 0-127 / 128-255. With only `roothash=` on the command line, `systemd-veritysetup-generator` picked `/dev/disk/by-partuuid/<hash[0:128]>` and booted. Release 1 in slot A and release 2 in slot B (selected only by which UKI's `roothash=`) each booted alone, from disk 1, from disk 2, and with both disks attached (6 clean boots, all `dm-verity` status `V`). **Caveat:** with duplicate partition UUIDs on two disks the `by-partuuid` link flips to whichever disk udev probed last, and the data and hash devices were sometimes taken from different disks (harmless only while the copies are identical). With one copy corrupted on disk 1, 1 of 4 boots failed (Job `dev-mapper-root.device` timed out, the verity-protected copy chosen was the bad one); the other 3 booted. See "Findings". |
| S2 Live slot rewrite | **Pass** | While `nex-data` (`/dev/vda6`) stayed mounted rw: `sfdisk --part-uuid` on the inactive slot (it prints "Re-reading the partition table failed: Device or resource busy" and exits 0), `udevadm trigger -s block` + `settle` refreshed `by-partuuid`, the new erofs and hash tree were `dd`-ed with `conv=fsync`, `veritysetup verify` passed, UUIDs flipped to the new roothash, the new slot was opened with `veritysetup open` and mounted ro, all without reboot; the next boot of the new UKI ran the new slot. `partx -u/-d/-a` worked on unmounted partitions but the kernel never carries the partition UUID (`/sys/class/block/*/uevent` has none): only udev/blkid reading the on-disk GPT matters. Crash cases (QEMU killed): C1 after UUID invalidation, C2 mid-write with invalidation first, C3 mid-write without invalidation, C4 after full write before the UUID flip: in all four the committed slot A booted. The retained old UKI of slot B failed fast and clean after C1/C2/C4 ("Timed out waiting for device dev-disk-by-partuuid-...", no matching partition) but after C3 the torn slot was matched by UUID and failed with `device-mapper: verity: data block 9 is corrupted` / `Buffer I/O error`. So the order "randomise the UUIDs first, write, verify, flip" fails cleanly; the naive order also fails but noisily. Power-loss torn writes below the virtio layer are **not** simulated (QEMU kill keeps the host page cache). |
| S3 `/etc` overlay | **Pass (initrd mount); confext-mutable rejected** | Chosen: a oneshot unit in the initrd (`Before=initrd-fs.target`, `RequiresMountsFor=/sysroot/var`) running `mount -t overlay overlay -o lowerdir=/sysroot/etc,upperdir=/sysroot/var/lib/nex/etc/upper,workdir=/sysroot/var/lib/nex/etc/work /sysroot/etc`. Recorded mount: `rw,relatime,lowerdir=/sysroot/etc,upperdir=...,workdir=...,uuid=on`; kernel defaults `index=off metacopy=off redirect_dir=off xino=off`. The lower was swapped across three different erofs images (uncompressed, lz4hc, lz4hc with a modified `/etc`) under the same upper: it always mounted; hostname, accounts, SSH host keys, libvirt storage pool, machine-id and a deleted `/etc/issue` (whiteout) all persisted. sshd host keys, libvirt first-start files (about 40 nwfilter XMLs), a storage pool and autostart, `interfaces.d`, `hostnamectl`, `localtime`, `useradd`, `chpasswd` all landed in the upper. Overlay options matrix with two lowers (same and different erofs UUID): `index=on` works for the same-UUID swap and **fails to mount** for a different-UUID lower (`overlayfs: failed to verify upper root origin`), so `index=on` must stay off. `metacopy=on` mounts but makes a chmod-only file follow the new lower's data (surprising, and the upper is no longer self-contained); `redirect_dir`, `xino` mount fine. Recommend keeping every option off, set explicitly. Drift confirmed: an upper `passwd`/`group` hides accounts added to the new lower, and an upper directory recreated after `rm -r` is opaque and hides new lower files, so system accounts must come from `sysusers.d` and the audit tool is needed. **systemd-confext mutable**: works only with `SYSTEMD_CONFEXT_MUTABLE_MODE=yes` (boot default came up read-only); `/etc` is mounted `nosuid,nodev,noexec` so `/etc/kernel/postinst.d/*` etc. return Permission denied; the merge happens after PID 1 started, so the hostname stayed `localhost` (static name `nex-lab`) and the machine-id differed from the one in the upper (`f78e...` vs `c450...`). Rejected. |
| S4 Firmware boot | **Partly run: removable-path fallback pass on OVMF; BLS counting pass** | Fallback: two ESPs, disk 1 first in boot order; baseline booted disk 1; disk 1's ESP wiped, OVMF booted the removable path on disk 2 (`/boot` on `vdb1`). **Not run:** deleted/corrupt `BOOTX64.EFI` on disk 1, NVRAM-entry cases, and any physical firmware. OVMF re-enumerates boot options each boot, so it is a weak proxy: `efibootmgr` per-ESP entries stay required until real firmware is tested. BLS counting (two ESPs with identical files, `nex-core_r1_1.efi` and `nex-core_r2_2+3.efi`, disk 1 first): good release booted, `systemd-bless-boot` ran by itself ("Marked boot as 'good'. (Boot attempt counter is at 1.)") and renamed the file on the ESP it booted from only; the other ESP still held `...+3.efi` (**the pair diverges, the converger is required**). Forced bad slot (one byte flipped in slot B on both disks, +3): three failed boots, each ended in emergency, the initrd drop-in rebooted, and the host reached the previous release in 67 s with `+0-3` on disk 1 and `bootctl list` showing `tries: 0 left; 3 done`. Disk 2 alone repeated the same three tries and also fell back (62 s), so a failover to the unconverged disk costs three extra failed boots. Sort order comes from the UKI's os-release version (`version: r2`), not the file name. |
| S5 Secure Boot | **Pass (VM, OVMF)** | Variables built with `virt-fw-vars --enroll-redhat --sb --add-mok <guid> uki.crt` (Microsoft 2011 CA in db, Nex cert in MokList). ESP: Debian shim (Microsoft UEFI CA 2011 signed) as `BOOTX64.EFI`, Debian-signed `systemd-bootx64.efi.signed` as `grubx64.efi`, MokManager, UKI signed with a throwaway Nex key (`ukify --secureboot-*`). Booted: `bootctl` "Secure Boot: enabled (user)", kernel "locked down from EFI Secure Boot". Same disk with Secure Boot off (default OVMF): booted, "Secure Boot: disabled". Same chain with an unsigned UKI under Secure Boot: not booted, shim fell into MokManager ("Enroll key from disk"). **Not run:** loading a Nex-signed module under lockdown, MokManager enrolment by hand, shim 2023-CA-only firmware, physical hardware. |
| S6 sysext layers | **Pass for the mechanism; real ZFS not built** | **Stand-in, not ZFS:** the layer shipped a copy of an in-tree module renamed `nexdummy` (a real out-of-tree build against the exact kernel was not run). `systemd-sysext merge` of a bare-erofs layer mounted by hand, live, no reboot, `unmerge` clean. A layer that ships only the module: merge works, `modprobe nexdummy` fails ("Module nexdummy not found in directory /lib/modules/...") because the base `modules.dep(.bin)` does not know it. A layer that also ships regenerated `modules.*` for base plus layer (`depmod -b <base+layer>`): `modprobe nexdummy` works, base modules still load, unmerge removes it again. Cost: the layer carries the index files, 5.9 MiB erofs for a 1-file layer; it is brittle in that any base module change must rebuild the layer (it already must, as the plan binds layers to one base build). `nex-cluster` (no module) merge was not run. |
| S9 Reproducibility | **Fail as built (roothash differs); cause identified; diffoscope not run** | Two builds, same pinned snapshot mirror, `SourceDateEpoch`, `Seed`, same package cache copy. `nexspike.vmlinuz` and `nexspike.initrd` are byte-identical. The root erofs/roothash and therefore the UKI differ (partition UUIDs `FD8DB4CC-...` vs `9F4393F3-...`). Comparing the extracted trees, every readable file is identical except the three `/etc/ssh/ssh_host_*_key.pub` files (and so their private keys, which root-only permissions made unreadable to my compare): the package postinst generates SSH host keys during the image build. Root-only files (shadow, libvirt config, credstore) could not be hashed here, so "every difference explained" is **not** proven. `diffoscope` is installed in the container but was not run (out of time). Fix to try: remove host keys in `mkosi.finalize` (and create them at first boot, as the plan already says), then rebuild twice and run `diffoscope`. |

## Measured sizes

| Object | Size |
|---|---|
| Root tree (extracted) | 407 MiB; `/usr/lib/modules` 107 MiB, `/usr/lib` 134 MiB, `/usr/bin` 99 MiB, `/usr/share` 54 MiB |
| `root.erofs` uncompressed (repart default) | 427,876,352 B (408 MiB); verity hash tree 3.38 MB |
| `root.erofs` `-zlz4hc,12` | 256,524,288 B (245 MiB), 7 s; verity 2.03 MB |
| `root.erofs` `-zzstd,level=15 -C65536` | 206,176,256 B (197 MiB), 109 s |
| `root.erofs` `-zlzma,level=9 -C65536` | 197,427,200 B (188 MiB), 14 s |
| Slot partitions in this lab | 2 GiB root + 64 MiB verity each (fixed; hash tree is built over the erofs size, not the partition) |
| UKI | 142,510,592 B (136 MiB): kernel 12.2 MB, initrd 130.3 MB (zstd). Three of them plus the mkosi leftovers (`vmlinuz`, `System.map`, `config` copied to the ESP) filled the 512 MiB ESP and an `mcopy` failed silently |
| Sysext stand-in layers | 8 KiB without indexes, 5.9 MiB with the full `modules.*` indexes |
| Full update (this package set, no ZFS/cluster/Nex packages) | lz4hc: 256.5 + 2.0 + 142.5 = about 401 MB; uncompressed: 427.9 + 3.4 + 142.5 = about 574 MB |

The package set is the spike set, not `installer/packages.txt`, so the root size is a floor for the final image.

## Commands used (reproduction sketch)

```
docker build -t nex-spike-tools image/spike/tools
image/spike/build.sh .cache/spike/build1 -f build          # S9: second run to .cache/spike/build2
# release set from the extracted tree (fixed -T/-U, fixed verity salt):
scripts/mkrelease.sh <label> '<shell edit of the tree>'      # mkfs.erofs + veritysetup format -> roothash
scripts/mkuki.sh <out.efi> <label> <roothash> [cmdline...]   # ukify build; SIGN=1 uses a lab key
scripts/esp.sh <img> ls|put|rm|mv|cat                        # mtools on the ESP at 1 MiB
# slot write (host side): dd erofs/hash to the slot partitions, sfdisk --part-uuid <root>/<verity> from the roothash
# live slot write in the guest: sfdisk --part-uuid; udevadm trigger -s block; dd conv=fsync; veritysetup verify
# initrd additions (appended with ukify --initrd): emergency.service.d drop-in ExecStart=systemctl --force reboot,
#   and nex-etc-overlay.service (the overlay mount above)
virt-fw-vars -i OVMF_VARS.4m.fd -o vars.fd --enroll-redhat --sb --add-mok <guid> uki.crt
sbsign / ukify --secureboot-private-key ... --secureboot-certificate ...
overlay matrix: mount -t overlay ... -o lowerdir=<erofs>/etc,upperdir=...,workdir=...[,index=on|metacopy=on|redirect_dir=on|xino=on]
sysext: depmod -b <base+layer root> <kver>; ship modules.* in the layer; systemd-sysext merge
```

## Findings the plan must absorb (all observed, not inferred)

1. **Duplicate partition UUIDs across mirrored disks are not deterministic.** `by-partuuid` points to the last
   probed disk and verity may take data and hash from different disks. Safe only while every copy is intact.
   A slot is torn or stale on one disk at exactly the moments that matter (update, disk replace). Mitigation:
   randomise the UUIDs of the slot on **all** disks first, write all, verify all, flip all; add `mdadm`-free
   verification before the UKI is armed; keep `nex-data` on md (the data label is also duplicated across the
   two lab disks). The md-in-initrd path was **not run**.
2. **UKI and ESP sizing.** mkosi's initrd includes every kernel module and firmware it finds (125 MiB zstd). Trim
   `KernelModulesInitrd` to what the root, data and md need and stop mkosi copying kernel/System.map/config to the ESP.
   Until trimmed, 512 MiB holds 3 UKIs; the plan's 1 GiB ESP holds about 6.
3. **Initrd emergency must reboot.** Without a drop-in a verity or missing-device failure stops at the emergency
   shell and the boot counter never advances. A `emergency.service` drop-in running `systemctl --force reboot` in
   the initrd made all counter and rollback runs work. The device timeout (default 90 s) dominates a failed try.
4. **Image first-boot behavior.** On an overlay `/etc` without a machine-id, systemd runs presets (disabling any
   unit not preset-enabled, including a symlink-enabled unit) and `systemd-firstboot` waits for a key on the console
   ("Press any key to proceed") until `systemd.firstboot=off` is on the command line. Ship preset files and the flag.
5. **Other image items found.** `systemd-remount-fs` fails on the erofs root (mask it or add a root fstab line);
   `libvirtd-tls/tcp.socket` fail without certificates; gpt-auto auto-mounts the ESP at `/boot` and
   `systemd-boot-update.service` runs `bootctl update` on it, which conflicts with Core owning the ESP;
   the build leaves SSH host keys in the image (also the S9 cause).
6. **Boot order of entries** follows the os-release `version` in the UKI. Put the sequence in that field.
7. **Boot-count state is per ESP.** `systemd-bless-boot` and sd-boot change only the ESP they ran from.

## Blockers and gaps

- Real ZFS (`zfs-dkms` against this kernel, signed module, load under lockdown) was **not built**; the first
  attempt did not start and there was no time to retry. S6 only proves the layer and `modules.dep` mechanism.
- `diffoscope` was not run; S9 is a failure to reproduce plus an incomplete explanation.
- Physical firmware: removable-path fallback, Secure Boot with a real MS-signed shim chain on hardware, MokManager
  enrolment and TPM are untested. Virtual labs cannot stand in for them (plan, Delivery phases).
- md RAID1 `nex-data` assembled in the initrd, libvirt VM start, and a power-cut at storage level were not run.
- S4 sub-cases (deleted/corrupt `BOOTX64.EFI`, NVRAM entries) and the `nex-cluster` layer merge were not run.
- No S7 (d-i vs live installer) or S8 headroom/16 GiB disk evaluation; only the sizes above were measured.
- Input pinning: only `Mirror=` is pinned to the snapshot; whether mkosi also pulled the unpinned security archive
  was not checked, and the second build reused a copy of the first build's package cache.

## Go / no-go for I-2

**Conditional go for starting I-2 design and the `image/` build; no-go for treating I-2 exit criteria as met.**
The load-bearing mechanisms the plan depends on were observed working in a VM: hash-selected slots with
verity, live inactive-slot rewrite with the data partition mounted, crash behavior that always leaves the
committed slot bootable, a persistent `/etc` overlay that survives lower swaps, BLS counting with automatic
fallback, Secure Boot through shim and MOK, and a live sysext with a `modules.dep` that works. None of them
needs a plan change except the items below. Before I-2 is declared done these must be closed:

1. Adopt the slot-writer ordering (invalidate UUIDs on all disks, write, verify, flip) and the no-duplicate-UUID
   risk decision for mirrors; run the md-in-initrd test.
2. Choose the initrd overlay unit (not confext) and keep overlay options explicit-off; add `sysusers.d`, the audit
   of shadowed files and the opaque-directory rule.
3. Ship the initrd emergency-reboot drop-in, trimmed initrd, presets, `systemd.firstboot=off`, and decide ESP
   ownership (disable gpt-auto ESP mount and `bootctl update`).
4. Build the real `nex-zfs` layer and load it under lockdown with a MOK-trusted key.
5. Fix the host-key reproducibility defect, then rebuild twice and attach a `diffoscope` report.
6. Test removable-path fallback and Secure Boot on physical hardware, and keep `efibootmgr` per-ESP entries
   until that passes.
7. Re-measure sizes with the real package set and raise the ESP and disk floor accordingly (plan open question S8).
