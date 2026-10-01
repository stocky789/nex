# Phase I-2 layers: record

Date: 2026-10-01. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), "Image composition, optional
feature layers, package trim and build reproducibility". Owner decisions applied: `nex-zfs` prebuilt modules are for
internal lab use only (no external release before the licence decision), Debian stays the package source.

API impact: new host contract (layer inventory, ensure, disable, `layer-unavailable`), implemented in Core only;
the Controller is not involved. State owner: `core` (`/var/lib/nex/layers/enabled.json`). **Environment: simulation
only** (Docker builds, disposable QEMU/KVM guests); nothing here establishes physical-hardware behaviour.

Status is updated as work lands; anything not listed under "Observed" was not run.

## Observed (levels reached: 1, 2, 3, 5; level 4 not run)

| Level | Item | Result |
|---|---|---|
| 1 | `go test ./internal/vmhost -run Layer` (inventory, ensure, disable, activate, tamper, crash convergence, busy-command) | pass |
| 1 | `nex-core-local layers activate|list|ensure|disable` (new `cmd/nex-core-local/layers.go`) | exercised in the lab VM below |
| 2 | `scripts/layer-finalize.py` + `tests/test_image_composition.py` (16 tests: etc to factory, sysusers, enablement links, var/collision/unit-order refusals, package names, hostid) | pass |
| 3 | `scripts/build-zfs-modules.sh --kver 6.12.107+deb13-amd64 --dev-key`: `spl.ko.xz`, `zfs.ko.xz` from `zfs-dkms` 2.3.9-0+deb13u1 (Debian source `zfs-linux`), signed with a throwaway key under `.cache/image-keys/`; `modinfo` shows the dev signer and vermagic `6.12.107+deb13-amd64` | built |
| 3 | `image/mkosi.images/build-layer.sh nex-zfs` on a lab base tree: mkosi overlay install of `zfsutils-linux zfs-zed` + `nex-zfs-modules` (a local .deb that `Provides: zfs-modules`, so zfs-dkms is not pulled), `layer-finalize`, erofs `nex-zfs.raw` 5.0 MB, regenerated `modules.dep*` for base+layer | built |
| 3 | `nex-cluster` layer (`layer.json`, `layer.packages`, `mkosi.conf`) | **written, never built** (needs the Nex `dlm-controld +nex1` and libvirt `+nex4` debs, not available here) |
| 5 | Lab VM (QEMU/KVM, direct kernel boot of the lab base, kernel 6.12.107, no Secure Boot): `nex-core-local layers ensure nex-zfs` live-merged the sysext (`systemd-sysext status` /usr nex-zfs), loaded `zfs`/`spl`, `zfs version` 2.3.9, `zpool create` + `zfs create` succeeded; `layers disable` refused with `in_use` while the pool existed | pass |
| 5 | Reboot: `nex-layers.service` verified the store file and merged the layer before the rest of boot, `zfs` loaded, pool importable (`zpool import -d /var` found it; **automatic cache import was not shown**) | pass with that caveat |
| 5 | Tamper: one flipped byte in the store file, then `layers activate` run by hand in the same boot: it refused the file (`digest_mismatch`), removed the layer's `/run/extensions` link, exited 0 and reported in `/run/nex/layers/status.json`. **The layer stayed merged**: the same log's `layers list` still shows `"merged": true, "verified": false`, because `activate` only links and the merge from earlier in that boot was not undone. What is shown is refusal and reporting; it is **not** shown that a tampered store file is unmerged in a running system, nor, after a reboot, that the unit then leaves it out (by code, `nex-layers.service` runs `activate` before `systemd-sysext refresh`, so a refused layer has no link to merge; no tampered-file reboot was run). | refusal and report pass; unmerge not shown |
| 4 | `scripts/image-repro-check.sh` double build + diffoscope | **base image only**: see `phase2-repro.md` (byte-identical after two fixes). Layers were not double-built; still open. |

## Not run / limits

- Secure Boot: the dev-signed modules loaded with "signature and/or required key missing - tainting kernel" because
  the kernel had no MOK or enforcement; module-signature enforcement and an unsigned-module refusal were not tested.
- The lab base is a throwaway Debian tree with `BUILD_ID=SYSEXT_LEVEL=lab.1` and `ID=nex-core` appended, not ImageBuild-3's
  base; `release.pub` and the signed manifest in the VM are lab-generated. Real manifest signing/trust is the updates work.
- Layer package list: mkosi's overlay output has no dpkg database, so `layer-finalize` names packages from
  `usr/share/doc/<pkg>/copyright` (no versions). 6 packages were found for `nex-zfs`.
- Old layer versions are not garbage-collected (the plan keeps them until the new slot is committed); no store GC exists yet.
- Layers are not yet part of the release manifest/`release.py`; `build-layer.sh` writes `<layer>.raw.json` for it.

## Findings

- **Kernel pin.** `image/out/build1.log` (ImageBuild-3's first run) installed `linux-image-6.12.111+deb13-amd64` from the LIVE
  `security.debian.org` (mkosi 25.3 adds it). The pinned snapshot `20260929T000000Z` carries only 6.12.107+deb13. Modules must match
  the base kernel exactly, so the base has to come from the snapshot only.
- **DKMS flag.** `dkms build --kernelsourcedir ...` fails ZFS configure (needs the `-common` headers); plain `dkms build -k` works.
- **zfs-zed pulls zfs-dkms.** `zfs-zed` Depends `zfs-modules | zfs-dkms`; the layer provides `zfs-modules` through the local
  `nex-zfs-modules` .deb, so no compiler, headers or DKMS enter the layer.
- **Debian maintainer scripts leave per-host state and enablement links**: `/etc/hostid` (a shared factory copy would give all hosts
  one ZFS host id), `/etc/systemd/system/*.wants` links, `/etc/rc?.d`. `layer-finalize` drops them; Core's `units_enable` enables units.
- **Unit ordering.** Stock `zfs-mount.service` and `zfs-mount@.service` are `Before=local-fs.target`; reset drop-ins ship in the layer
  and `layer-finalize` refuses a layer without them. The reset also dropped the guarantee that pools mount before their consumers,
  so the drop-ins now order both mount units `Before=libvirtd.service nex-core-local.service` (2026-10-02 review fix;
  `tests/test_image_composition.py` pins the effective list). Not shown in a guest: that units merged mid-boot are started in the
  boot transaction that was already queued (the lab did not show an automatic cache import), nor the ordering itself.
- **Boot merge.** The stock `systemd-sysext.service` runs before `/var` is mounted, so it never sees a `/var`-resident layer, and
  ordering `nex-layers.service` after `systemd-tmpfiles-setup.service` made an ordering cycle with it. `nex-layers.service` now links
  and runs `systemd-sysext refresh` itself, with no sandboxing options (a private mount namespace hides the merge).
- **Tooling.** `cp -al` fails across the btrfs subvolumes of the work tree (reflink copies are used); mkosi needs `reprepro` in the
  tools image for `--package-directory` (asked ImageBuild-3 to add it to `image/tools/Dockerfile`); root-owned build products must be
  removed from inside the container.
