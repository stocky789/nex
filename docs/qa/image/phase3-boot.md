# Phase I-3b boot wiring and A/B rollback in a VM: results

Date: 2026-10-01 to 2026-10-02. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), "Update pipeline, API and
pool-safe rollout" (host pipeline, `booted` and `committed` phases) and "Image architecture" (rollback triggers). Slice I-3b wires
the I-3a engine ([phase3-engine.md](phase3-engine.md)) into the image and proves update, fallback and rollback in a VM.

**Environment: simulation only.** One QEMU/KVM guest at a time (`-smp 2 -m 2048`, user-mode networking) with OVMF, a single 14 GiB
virtio disk, Secure Boot **off** (the OVMF default). No physical firmware, TPM, Secure Boot, md RAID mirror or second disk was
involved. Release images were built by `image/build.sh` in the pinned tools container; the guest ran the real `nex-update` binary
(the I-3a engine, `HostPlatform`, `systemctl reboot`) against a local static HTTP server. All development keys are throwaway files
kept outside the repository (`.cache/`, gitignored).

- API impact: **existing contract**. `Inventory.image` (defined in I-3a, `image-update-v1`) gains one additive optional field,
  `secure_boot`, and now has a producer (agent probe `image`, image hosts only). No route, request or response changed.
  `docs/API.md` and `docs/api/openapi.json` were updated; `scripts/generate_api.py --check` and `tests/test_api_contract.py` pass.
- Guide impact: none (no operator workflow exists yet; see the change record). State owner: **core** (the disks and
  `/var/lib/nex/image/state.json`; the Controller stores nothing).
- Resource limits used: container builds under `systemd-run --user --scope -p CPUQuota=400% -p MemoryMax=8G -p MemorySwapMax=0`
  with `nice -n 19 ionice -c3` and `docker --cpus 4 --memory 8g`; `GOMAXPROCS=4`, `go test -p 2`; one VM at a time, each wait capped at
  480 s. Peak use was **not sampled**. Every VM was killed by the harness; no container was left running; the 14 GiB lab disk was
  deleted after the last run.

## What exists

| Where | What |
|---|---|
| `image/mkosi.extra/usr/lib/systemd/system/nex-boot-check.service` | `After=` only the units it judges (`nex-state-guard`, `libvirtd`, `nex-core-local`); `RequiredBy=boot-complete.target` through a static `boot-complete.target.requires/` link (not a preset); never ordered after `chrony-wait`, `time-sync.target` or `network-online.target` |
| `image/mkosi.extra/usr/libexec/nex/nex-boot-check` | image-caused failures only (`/var` and `/etc` overlay writable, libvirtd answers, `nex-core-local` when enabled, `nex-state-guard`, `boot-check.d/*`); on a counted boot `systemd-bless-boot bad` + reboot; no mark or reboot while `/run/nex/maintenance-hold` exists. New: `NEX_BOOT_CHECK_TRIES` (default 20) and `NEX_BOOT_CHECK_WAIT` (default 10 s) |
| `nex-state-guard.service` + `libvirtd.service.d`, `nex-core-local.service.d` drop-ins | `nex-update state-guard` fails when `RequiredStateFormat("/")` exceeds `NEX_STATE_FORMAT` of the image's os-release; libvirtd and nex-core-local `Requires=` it, and `nex-boot-check` reads its failure, so a counted boot of an image that cannot read the state falls back |
| `nex-update-reconcile.service` | `nex-update status` after every boot (After bless and boot check): folds the boot assessment into the engine record so `Inventory.image` is current without an operator |
| `internal/nexupdate/cmd/nex-update` | on-host CLI over the same `Engine` (`status`, `check`, `apply`, `activate`, `rollback`, `cancel`, `state-guard`); installed at `/usr/bin/nex-update` |
| `image/build.sh`, `mkosi.conf`, `mkosi.postinst.chroot` | `NEX_UPDATE_TRUST` pins `trust.json` at `/usr/share/nex/update/trust.json` inside the verity root (warns when unset); `NEX_STATE_FORMAT` (default 3) lands in os-release; `nex-update` is built and installed beside `nex-hostimage` |
| `scripts/update-publish.py` | `keygen`, `trust`, `publish`, `promote`: signs `trust.json`, `channel.json`, `manifest.json` (Ed25519, raw signature over the exact bytes), stores the tar `slot-image` blob and the source and compliance blobs content-addressed; fails closed without a key, without source or compliance files, on an incomplete release directory, and when sequence order disagrees with `dpkg --compare-versions` order |
| `scripts/update-bundle.py` | offline bundle of one release (`minimal` or `full`), `SHA256SUMS`, its own re-signed `channel.json` expiry |
| `internal/agent/image_status.go` | `Inventory.image` producer: running release from `/proc/cmdline` and os-release, retained/staged/previous slots, phase, `rollback_floor`, `state_format`, operation and failure from the engine's `state.json` (read-only: `nexupdate.ReadStatus`), `secure_boot` from the firmware `SecureBoot` variable |
| `tests/test_update_publish.py`, `internal/agent/image_status_test.go`, `tests/integration/image_update.py` | publisher tests, producer mapping tests, the VM proof below |

## VM proof: what ran

`tests/integration/image_update.py` builds the layout below and drives the guest through a root shell on its second serial port. The
final run (7 of 7) is the record; all 32 checks passed, none failed. Releases: label `2026.10.1` sequence 1 (root hash `2290c0b9...`) installed by
`nex-hostimage install`, and label `2026.10.2` sequence 2 (root hash `af0be279...`), both with the same pinned trust, published to
a local HTTP server that honours `Range` and `If-Range`. Each is a full base rebuild (2.3 minutes with a warm package cache).

| Step | Observed (final run) |
|---|---|
| Boot of release 1 | `/` read-only erofs; `trust.json` present in the verity root; engine `idle`; `nex-boot-check` Requires/RequiredBy `boot-complete.target` and ordered after `nex-state-guard`, `libvirtd`, `nex-core-local`; `systemctl list-dependencies --after nex-boot-check` (19 units) names no `chrony-wait`, `time-sync.target` or `network-online.target`; `nex-update state-guard` passes on fresh state |
| `check` over HTTP | release 2 offered (verified trust, channel, manifest) |
| Tampered blob (one byte flipped at offset 4096 of the served blob) | `apply` refused with `blob_mismatch`, nothing staged, release 1 kept running |
| Dropped download (connection cut after 120 MiB) | `source_unreachable`, 125,829,120-byte `.part` kept; a second `apply` continued with a `Range` request at a non-zero offset (server log; the offset itself was not recorded) |
| `stage_only` | release 2 written to slot `b`, phase `staged`; no release 2 UKI on the ESP: the next boot is still release 1 |
| Fault-injected failing `nex-boot-check` | the fault is a config change in the persistent `/etc` (libvirtd and its sockets masked, `NEX_BOOT_CHECK_TRIES=3`, `NEX_BOOT_CHECK_WAIT=5` in a drop-in). `activate` armed `nex-core_2026.10.2_2+3.efi` and rebooted; release 2 booted (counter `+2-1` while checking), the check failed three times, `systemd-bless-boot bad` ("Marked boot as 'bad'. (Boot attempt counter is at 1.)") and the guest rebooted into release 1. Engine: `rolled_back`, `failure.code boot_failed`; ESP: `nex-core_2026.10.1_1.efi nex-core_2026.10.2_2+0-3.efi` |
| Data after the failed trial | `/etc` and `/var` markers, SSH host key and machine-id identical to boot 1 |
| Fault removed, `apply -activate reboot` | slot `b` rewritten, release 2 booted (`BUILD_ID 2026.10.2-2`, running root hash equals the signed manifest's `verity_root_hash`), `nex-boot-check` passed, `systemd-bless-boot` "Marked boot as 'good'", ESP entry `nex-core_2026.10.2_2.efi` without a counter, engine `committed` with release 1 as `previous` |
| Data after the update | `/etc` and `/var` markers, SSH host key and machine-id identical; `/` still read-only erofs |
| `rollback` | release 1 booted again (its retained UKI re-armed), `/etc` and `/var` markers intact; release 2 remains as `previous` |

Timings: install of release 1 4 s; first boot to a shell 10 s; failed trial round trip about 105 s; update including a 370 MB download
over user-mode networking, slot write and reboot about 112 s.

### Run history

Seven runs were needed. Runs 1-6 failed on **lab harness defects only**, each fixed in `image_update.py`: (1) the first line typed after the
shell starts is dropped, (2) the shell printed a prompt that broke JSON parsing, (3) `systemctl` paged its output, (4) the ESP is
auto-mounted at `/boot`, not mountable a second time (see findings), (5) in run 5 the rollback boot's console marker never reached
the control socket although the boot completed (mechanism not established; the harness now asks the shell for its boot id until an unseen
one answers), (6) late replies of an earlier boot were taken for the new boot (the harness now keeps every boot id it has seen). The
product behaved identically across runs 4-7 up to the step each stopped at.

## Findings

- **The boot check fails a counted boot at once, not after three boots.** The I-3a script marks the entry bad when its own retry window
  ends, so a failed check costs one boot, not three. "Three tries" in this record are three check attempts inside one boot. The
  three-boot counter path (sd-boot exhausting `+3` after panics or hangs without the check running) was **not run**.
- **gpt-auto mounts the ESP at `/boot`** (`boot.mount`, an automount) on the image, contrary to the plan's "Core mounts each ESP
  transiently". The engine, `nex-esp-sync` and the harness all worked with it mounted; it is not fixed here (masking it changes the
  generator behaviour, or needs `systemd.gpt_auto=no`, which changes the signed command line).
- The boot check is not run on boots without a counter (nothing pulls `boot-complete.target` except `systemd-bless-boot`, which only
  exists for counted entries), so a blessed image is not re-assessed on later boots.
- `nex-update-reconcile.service` made the record current after each boot; it was not the only path (the harness also ran `status`).
- Interpretation of the fault: a masked libvirtd lives in `/etc`, which the overlay keeps across slots, so release 1 also ran with it masked
  after the fallback (an uncounted boot, nothing assessed it). It is a faithful "image start fails the libvirtd check" injection,
  not a defect shipped in a release.

## Not run / not wired

- **Not run:** the state guard in a VM (it passes on fresh state; the refuse-and-fall-back path with a retained state newer than the image's
  `NEX_STATE_FORMAT` was not exercised: the image bakes 3, the maximum the code knows); counter exhaustion without a check failure (panic, hang,
  initrd verity failure); power cuts during download, slot write and arm (the unit tests model the order, no VM cut power); two-disk RAID1
  mirror updates; Secure Boot and signed UKI; the host-local HTTP API on a guest (`nex-core-local` is not in the image; the engine was driven
  through `nex-update`, which shares the engine but not the HTTP layer, authentication or CSRF); the Controller-side
  `image-update-v1` acknowledgement that withholds `Inventory.image` from an older Controller (the producer attaches it on image hosts
  unconditionally); the agent probe itself in a VM (covered by unit tests on its mapping and the `secure_boot` parsing; the `ReadStatus` read
  path ran only in the engine's own `status`); a pool, a Controller, layers, offline bundle import on a host (`update-bundle.py` is
  covered by `tests/test_update_publish.py` only).
- The lab `source` and `compliance` components are stand-ins (a tarball of `internal/nexupdate` and the build's package manifest), not the
  AGPL source bundle; the licence gate in the publisher checked that files exist and hash, nothing more.
- Time-box: planned 40 minutes; the work took roughly 90 minutes of wall time (the seven VM runs and two image builds dominated). No
  approach failed more than twice except the harness defects above, each a different fix.
