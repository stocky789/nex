# Phase I-5a: an image that contains nex-core-local, booted once

Date: 2026-10-02. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md) (Mutations table, "Artifacts", "Reproducible
build"). Closes the gaps recorded in [phase2-repro.md](phase2-repro.md) ("Nex-built debs absent") and
[phase3-boot.md](phase3-boot.md) ("`nex-core-local` is not in the image").

**Environment: simulation only.** One QEMU/KVM guest (`-smp 2 -m 2048`, user-mode networking) with OVMF, one 14 GiB sparse virtio disk, Secure
Boot off; the build ran in Docker containers on one workstation. Everything ran under `systemd-run --user --scope` (build steps
`CPUQuota=200% MemoryMax=4G`, the VM `CPUQuota=300% MemoryMax=3G`, no swap), `nice -n 19 ionice -c3`, containers `--cpus 2 --memory 4g`,
one VM at a time. Peak use was not sampled. The VM disk and the intermediate trees were deleted afterwards; no VM or container started by
this work is left running.

- API impact: none (no route, request or response changed; the harness calls the existing host-local contract). Guide impact: none (no
  operator step changed; the image is not yet installable by operators). State owner: none new; the image holds no state, the host
  keeps its TLS certificate and agent state on `nex-data` (`/var/lib/nex/core-local`, `/var/lib/nex-core-agent`).

## What was built

| Object | Result |
|---|---|
| `nex-core-local_0.1.5~alpha1+img1_amd64.deb` | 137,984,132 B, sha256 `fe03c13b1b1bb35c6c8f76b23164cfd43efcb58baa9ef569a944d7e72c122d9b`; 50 s in the pinned builder |
| Image `core1` sequence 1 | `root.erofs` 465,227,776 B, `root.verity` 3,674,112 B, root hash `aad9e417857037c0da79b6917826664acc930ae763670eedc9ffe333e8c587be`, UKI 51,223,040 B; 175 s for `image/build.sh all` with a warm package cache |
| `image/packages.lock` | 318 packages: the 317 Debian packages unchanged plus `nex-core-local ... origin=nex sha256=<deb>`. `scripts/image-lock.py check` matched all 318 against the build's package list |

The deb is built by `packaging/core-local/build.py --edition image --source <nex-source.tar.gz> --version ...`, which the image build consumes
through `NEX_PACKAGE_DIRS` (the existing `image/build.sh` hook; `build.sh` needed no change). The package:

- has **no maintainer scripts**, no apt source/keyring/hook, no initramfs hook, no `nex-root-ro.service`, no `/etc/nex/core-release` stamp,
  no os-release divert, no `libvirtd-apparmor-policy`, `storage-downgrade-guard` or `corosync-pristine` (the image has no AppArmor package and
  no apt; the plan's Mutations table retires each of these);
- is enabled by a **vendor wants link in `/usr`** (`multi-user.target.wants/nex-core-local.service`), exactly like `nex-layers.service`, so the
  verified slot carries the enablement and a first-boot preset pass cannot drop it. Nothing writes to the root at install or boot;
- drops the cluster, ZFS and SMB packages from `Depends:` and has no `Recommends:` (layers own them): `Depends` is the 22 base-provided
  packages, so the Debian closure of the image did not change;
- carries the AGPL source archive as a regular file at `/usr/share/nex/core-local/nex-source.tar.gz` (what `--source-archive` serves) and no
  Controller appliance, no appliance update helpers and no `release.pub`: the host deploys the Controller from the signed system
  image (`core-local-setup configure --controller-image-sha256`), not from the single-disk appliance.

Reproducibility inputs changed: the builder now starts from the same digest-pinned `debian:trixie-slim` as `image/tools/Dockerfile`, takes
packages from the `image/snapshot` archive date (`build.py` passes `--build-arg SNAPSHOT=`; the Dockerfile refuses to build without it),
gets Node and npm from one upstream archive pinned by SHA-256 (as Go already was; Debian's `npm` pulls about 300 `node-*` packages), builds Go
with `-trimpath -buildvcs=false -ldflags -buildid=`, and `SOURCE_DATE_EPOCH` (from `image/mkosi.conf`) clamps the package's file times.
**The deb was built once; a double build of it was not run, so its bit-reproducibility is not shown.** The Go module and `npm ci` downloads are
integrity-checked by `go.sum` and `package-lock.json` but come from the live registries.

## VM proof: what ran

`tests/integration/image_core_api.py --release <out>/release --label core1` installs the release on a fresh single disk (`nex-hostimage
install`, same container as `image_boot.py`, seeded with a hostname and the lab control shell on the second serial port), boots it, sets a
random root password in the guest, and drives the host-local API from inside the guest with a cookie-jar client that sends the same `Origin`
a browser would. Run 3 of 3 (below) is the record: **24 checks passed, 0 failed**; install 3 s, boot to shell 8 s, second boot 8 s.

| Check | Observed |
|---|---|
| root | `erofs` with `ro`; `touch /usr/lab-write` refused; `dmsetup status root` is a verity target, status `V` |
| `nex-core-local` | `active` and `enabled`; enabled by the `/usr` vendor link; `/var/lib/dpkg` absent; `/healthz` answers 200 after boot (polled every 3 s) |
| libvirtd | `virsh -c qemu:///system version` reports `Running hypervisor: QEMU`; unit `active` |
| `nex-layers.service` | `ConditionResult=no`, `ActiveState=inactive`, `Result=success`: skipped by `ConditionPathExists=/var/lib/nex/layers/enabled.json`, not failed. **No layer was enabled**, so the activate path (verify, link, `systemd-sysext refresh`) did not run here |
| `GET /api/local/v1/updates` | `401` without a session; `401` for a wrong root password; `200` after `POST /api/local/v1/session` with the root password. Body: `running:false` (no update operation in flight), `phase:idle`, `state_format:0` (what retained state requires; nothing is retained), `channel:stable`, **one slot** `{role:booted, label:core1, sequence:1, root_hash:aad9e417...}`; the root hash equals the `roothash=` on the kernel command line and no other slot exists on a fresh install |
| CSRF | `POST /updates/check` without `X-CSRF-Token` is `403`; with it, it reaches the engine and answers `503 source_unconfigured` ("no update base URL is configured"), the correct answer for a host with no update source. Without an `Origin` header the first run was refused `403 same-origin request required`, which is the documented boundary |
| `GET /api/local/v1/host` | `200` |
| failed units | only `ipmievd.service` (no BMC in QEMU) |
| cold boot | `poweroff` then a new QEMU process on the same disk and variable store: `/healthz` answers again, the TLS certificate hash is unchanged, the root login and the slot list are identical |

Not shown by the identity row: the agent state directory held no identity file because the host was not enrolled, so only the certificate
persistence is evidenced. `nex-boot-check` was not asserted (a boot without a counter does not run it, see [phase3-boot.md](phase3-boot.md)).

### Run history

Run 1 (10 failed) and run 2 (3 failed) were **harness defects only**; the product behaved identically: the client sent no `Origin`
(`403 same-origin request required`), and the harness expected `running:true` and `state_format:3`, which are not what those fields mean
(`running` is an in-flight operation, `state_format` the retained-state requirement). Both are fixed in the script. Run 3 passed.

## Defects found and fixed

1. **The source archive omitted the package's own preset.** `scripts/build-source.py` copied `packaging/core-local` files by suffix and
   `.preset` was missing, so the first deb build from a fresh archive stopped at `install: cannot stat '.../30-nex-core-local.preset'`
   (the file was added with the layers work). Fixed: `.preset` added to the suffix set. This also affects the ISO build, which uses
   the same archive. `tests/test_build_source.py` still passes.
2. The package builder reached the live archive (`FROM debian:13`, `apt-get update`) and Debian's `npm`; pinned as described above. Not a
   boot defect, but the plan's "Gaps found" list (`packaging/core-local/Dockerfile:2-3`) is now closed for this recipe; the libvirt and
   dlm recipes still have it.
3. A first attempt that kept Debian's `npm` in the builder took over 8 minutes of apt downloads (snapshot.debian.org answers about one file
   per second), which is why the upstream Node archive is used.
4. `scripts/image-lock.py` had no way to lock a package that is not from the snapshot (the plan's `origin: nex`). It now records
   `origin=nex` and the deb's SHA-256 for a built package that has a `.deb` in `--debs`/`$NEX_PACKAGE_DIRS`, and `check` fails when the deb
   changed or the build installed another version than the deb. Tests: `tests/test_image_lock.py`.

No defect was found in `nex-core-local`, the unit wiring or the image units: after the package was installable, the service, libvirtd, the
layers unit and the API behaved as designed on the first boot.

## Matched libvirt and dlm: not built

`packaging/libvirt/build-matched-libvirt.sh` (libvirt `11.3.0-3+deb13u3+nex4`, five patches) and
`packaging/cluster-storage/build-matched-dlm.sh` (dlm `+nex1`) were **not built**, deliberately, and the image carries Debian's stock
`libvirt-* 11.3.0-3+deb13u3` from the snapshot. Reason: each is a full Debian source rebuild (`build-dep` plus `dpkg-buildpackage`, hundreds
of packages fetched from a source archive that answers about one file per second, as seen above) and does not fit the 60-minute box next to the
deb build, the image build and the VM proof; the box was spent on the package and the boot. Consequences for the lab:

- Recovery-snapshot safety fixes carried by the `+nex4` patches (unique revert overlay, partial backing chain, QMP JSON depth, read-only
  revert media) are **absent**; snapshot, revert and backup scenarios on this image do not test the shipping libvirt.
- The `readonly-disk-apparmor` patch does not apply to this image (no AppArmor package); that mismatch with the plan's "run
  `libvirtd-apparmor-policy` in the image build" step is still open.
- dlm belongs to the `nex-cluster` layer, not the base; the layer's own build is not part of this work.
- The matched debs are untouched in the lock: the entry will read `origin=nex` as soon as their debs are in `NEX_PACKAGE_DIRS`.

## Readiness for a three-host pool lab

The image is ready for a **single-host** lab: it boots, serves the root-authenticated local API, reports its slot and answers libvirt. What a
3-host lab still needs, from reading [join-a-pool.md](../../guide/join-a-pool.md), [API.md](../../API.md#core-pool-peer-join-and-repository-observations)
and the existing labs (`tests/integration/add_host_lab.py`, `pool_continuity.py`; `fresh_install_lab.py` is the ISO-installer lab and is not a
pool script):

1. **Three installs of one release** with distinct hostnames (the lab seed sets one fixed hostname today) on three disks, and an L2 network
   between the guests. QEMU user-mode networking cannot join guests to each other: use one `-netdev socket`/tap bridge with a management
   address per guest (the lab shell's static `10.0.2.15` is per-VM and would collide). Core takes its management IP from the default-route
   interface (`core-local-setup init`) or from `/etc/nex/core-local.env`; a host with no IPv4 default route loops in `Restart=always` and the
   master cannot compute its pool-peer address. The real installer stages `/etc/network/interfaces` (ifupdown), which the lab seed does not.
2. **A root password per host** (the lab sets one in the guest; the installer sets it for real). Local login is root only, PAM `nex-core-local`.
3. **A Controller** for enrollment (a pool is created by enrolling: every enrolled host becomes the master of its own pool). Either an existing
   Controller VM or the Controller image deployed on one host: `core-local-setup configure --controller-image-sha256 <digest>` needs the
   `controller-system.img` payload under `/var/lib/nex/payload/<sha256>/` (`image/build.sh controller`), which this image does not carry and this
   work did not build. Enrolment is `POST /api/v1/sites/{id}/hosts` with the host address and root password (see `add_host_lab.py`), so the
   Controller must reach each host's `:9443` and each host must reach the Controller.
4. **Join** hosts 2 and 3 to host 1's pool: from the Controller (Settings, Members, Add host to pool) or by hand with the host-local calls
   `join-csr` (member), `join-offer` (master), `join` (member), each with that host's own session, `Origin` and `X-CSRF-Token`. The master's
   pool-peer listener (`:9444`) must be reachable from the members and the other way round; members must have no VMs and no remote storage.
5. **Layers are not available**: no `nex-cluster` layer was built or enabled, so no corosync/pacemaker/dlm, GFS2 or HA; shared NFS storage
   uses the base's `nfs-common`. Live migration is out of scope until the matched libvirt is in the image.
6. **Update trust**: this image was built without `NEX_UPDATE_TRUST`, so it cannot verify an update (`POST /updates/check` answers
   `source_unconfigured`). A rollout lab needs two releases signed by `scripts/update-publish.py` with the trust pinned into both images, as in
   `tests/integration/image_update.py`.

## Limits

Single disk, Secure Boot off, QEMU only; no enrolment, pool, Controller, VM or layer was exercised on this image; no restart of
`nex-core-local` under load; the `systemd-sysext` merge path was not run; no second boot with a counted entry (`nex-boot-check`); the deb was
not double-built; the matched libvirt and dlm were not built (above).

## Reproduce

```
# source archive, deb, image (all capped as in the header)
python3 scripts/build-source.py --output .cache/core-image/nex-source.tar.gz
python3 packaging/core-local/build.py --edition image --source .cache/core-image/nex-source.tar.gz \
  --version 0.1.5~alpha1+img1 --output .cache/core-image/debs
NEX_PACKAGE_DIRS=$PWD/.cache/core-image/debs NEX_IMAGE_OUT=$PWD/.cache/core-image/out NEX_LABEL=core1 NEX_SEQUENCE=1 image/build.sh all
python3 scripts/image-lock.py write --debs .cache/core-image/debs .cache/core-image/out/base.dpkg-list   # then: check
python3 tests/integration/image_core_api.py --release .cache/core-image/out/release --label core1
```

## Changed files

`packaging/core-local/{Dockerfile,build.py,build-inside.sh}`, `scripts/build-source.py` (one suffix), `scripts/image-lock.py`,
`image/packages.lock`, `tests/integration/image_core_api.py`, `tests/test_image_lock.py`, `tests/test_image_core_package.py`, this file.
