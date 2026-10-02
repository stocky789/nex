# Phase I-4c Controller guest image profile, deploy wiring, CI: results

Date: 2026-10-02. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), "Controller appliance" and "Delivery phases"
I-4. Closes the first open item of [phase4-appliance.md](phase4-appliance.md): the guest image that fits Core's two-disk profile.
Owner decisions applied: UEFI-only image edition, Debian stays the package source, one root signature (no separate appliance signature
for this profile), no remote publishing, private hosts never in tracked files.

**Environment: one workstation, Docker builds and one QEMU/KVM guest at a time** (`-smp 2 -m 2048`, user-mode networking, OVMF with no
keys enrolled, i.e. Secure Boot off). No libvirt, no Core host, no deployment through the local deploy page, no swap on this guest, no
pool and no browser were used. Resource rules honoured: image builds under `systemd-run --user --scope -p CPUQuota=200% -p MemoryMax=4G
-p MemorySwapMax=0 nice -n 19 ionice -c3`, containers `--cpus 2 --memory 4g`, VMs under `CPUQuota=300% MemoryMax=3G`, `go test -p 1`; the
data and seed disks were a 2 GiB sparse qcow2 and an 8 MiB FAT image in `/tmp`, deleted afterwards, as were the build trees (about 2.5 GiB).

- API impact: **none** (a `nex-core-local` command-line flag pair, a `core-local-setup` option and the libvirt domain XML of the profile; no
  HTTP or RPC route, report field or protocol type). Change record `docs/changes/2026-10-02-controller-guest-image.json`.
- State owner: **core** (the data disk's checkpoint and journal are Core's; the Controller keeps its own two enumerated paths only).
- Guide impact: [Deploy the Controller appliance](../../guide/deploy-controller-appliance.md) updated, **unverified** (no walkthrough).

## What exists

| Where | What |
|---|---|
| `image/controller/` | the controller profile of the image tooling: `mkosi.conf` (same Debian trixie snapshot, mkosi, `Seed`, trimmed initrd module list incl. the SATA seed path), `controller.packages` (the packages `appliance/build-inside.sh` installs, plus systemd-boot, `netplan.io`), `mkosi.extra` (fstab `/dev/vdb /var ext4 x-initrd.mount,x-systemd.makefs`, sshd drop-in, ssh host-key generation, cloud-init `users: [root]`, `systemd-boot-random-seed` masked), `mkosi.postinst.chroot` |
| `image/build.sh controller`, `image/release.py controller` | tree via mkosi, then erofs + dm-verity (same fixed UUIDs and salt as the host), UKI with `roothash=`, systemd-boot, and a raw GPT disk `system.img` (ESP, erofs root and verity partitions typed and identified from the root hash so `systemd-gpt-auto` finds them). The host image's initrd `/etc` overlay unit is reused as is |
| `appliance/build.py --profile controller`, `appliance/build-controller-payload.sh` | compiles the Controller and UI in the pinned builder container, lays out the payload tree, runs `image/build.sh controller`, copies `controller-system.img` and writes `vm-image.json` (digest, root hash, the `--payload vm-image:controller=FILE` argument for `scripts/update-publish.py`); no `manifest.sig` |
| `appliance/firstboot.py` | writes the readiness marker through a link into `/var/lib/nex-controller` (the root is read-only) |
| `internal/vmhost/engine.go` | profile seed no longer carries the update helper; profile domain pins UEFI with `secure-boot` and `enrolled-keys` off |
| `cmd/nex-core-local`, `packaging/core-local/core-local-setup` | `--controller-system-image(-sha256)`; `configure --controller-image-sha256 SHA256` (kept across reconfiguration; `""` returns to single-disk) picks `/var/lib/nex/payload/<sha256>/controller-system.img` |
| `tests/integration/controller_image_boot.py` | the QEMU boot, persistence and tamper test below |
| `.github/workflows/image.yml`, `.gitea/workflows/image.yml` | forge-only image build, boots, tamper, double build; `tests/test_ci_security.py` pins their shape |

## Observed: boot of the built image (final run)

System image `controller-release/system.img` built by `image/build.sh controller` with `NEX_LABEL=0.1.0-dev NEX_SEQUENCE=1` from the
payload built by `appliance/build.py --profile controller` (527 MB raw: 128 MiB ESP, 370 MiB erofs, 3 MiB verity). The guest was booted the way
the vmhost engine defines the VM: system disk raw and read-only on virtio, blank qcow2 data disk as the second virtio disk, NoCloud seed (FAT,
label `cidata`) on SATA, UEFI, virtio NIC with DHCP, guest agent channel.

- First boot: the Controller answered `GET /appliance-ready.json` over HTTPS **with only the deployment's own certificate trusted** and
  returned `{"bootstrap_version":1,"status":"ready"}`. In the guest (through the guest agent): `/` is `erofs ro`, `/etc` is `overlay`, `/var` is
  `/dev/vdb ext4` (made by the initrd on the blank disk), `touch /usr/x` fails with "Read-only file system", `apply-update`,
  `nex-controller-update.path` and `apt-get` are absent, `/var/lib/nex-controller` holds only the Controller's files, no failed units.
- Reboot on the same data disk (seed still attached): ready again, `/etc/nex-controller/tls.key` and `appliance-initialized` kept, `bootstrap.json`
  gone (persistence of the data disk and of the overlay, including the hostname, ssh host keys and the netplan configuration).
- Tampered system disk (one byte of the root partition flipped): see "Tamper result" below.

**Tamper result:** the guest never became ready. The serial console shows `device-mapper: verity: 253:2: data block 1 is corrupted`, `Buffer I/O error on dev dm-0`,
`reached maximum errors`, then `Dependency failed for sysroot.mount` and `initrd-root-fs.target`; the VM then rebooted (`panic=10`, qemu `-no-reboot`) and qemu
exited. The full test run (`controller image boot test: PASS`, all `ok` lines above) used a weaker console assertion (the word "verity", which every
boot prints); it was tightened afterwards to the lines above and the tamper phase alone was re-run once by hand to read them, not re-run through the
tightened test.

## Defects found by the boots and fixed (each cost one rebuild; the regression-prone ones have tests in `tests/test_image_layout.py`)

1. `release.py base()` shadowed its output directory with the `veritysetup` output (first build failed after the tree was built).
2. `mkfs.vfat --invariant` rejects an empty `SOURCE_DATE_EPOCH` (which `image/build.sh` exports when unset).
3. The initrd overlay's `initrd-fs.target.wants` entry was copied as a file instead of a link; systemd ignored it and `/etc` stayed read-only.
4. `ssh.service` failed (no host keys; the image carries none): `ssh-keygen -A` before `sshd -t` in a drop-in.
5. cloud-init was disabled by its generator: the initrd lacked the SATA and sd/sr drivers, so the seed device did not exist when `ds-identify` ran
   (a libvirt seed is a SATA CD-ROM too). Modules added to the initrd list.
6. `systemd-boot-random-seed.service` failed on the read-only ESP: masked.
7. cloud-init created the `debian` default user below the read-only `/home` and failed its network and main stages: `users: [root]`.
8. The renderer override to `networkd` wrote `Name=nex0` without a rename rule, so the first reboot had no network: `netplan.io` is installed and
   regenerates the rename every boot.

## Not run / not done

- **No Core deployment:** the profile was never created by `Deploy` through libvirt; no swap, reflink checkpoint, redefinition, readiness probe or
  restore touched this image; the libvirt firmware selection with the pinned features was only unit-tested (`appliance_swap_test.go`), not
  started by libvirt. The test VM's NoCloud seed reproduces the engine's seed by hand.
- **Reproducibility of the guest image:** no double build. Only `release.py controller` was repeated over one mkosi tree: identical `system.img`
  digest across three runs. One earlier pass over the first tree printed a different root hash than later passes over what should have been the
  same tree; the cause was not found (the tree was rebuilt after fixes, so the two were not proven to be the same tree).
- **Controller `.deb` and classic single-disk appliance, the Controller's APT routes and panel, `appendUpdateBootstrap` for single-disk seeds:**
  deliberately **kept**: the classic edition and the single-disk appliance still depend on them and the swap does not exist there.
- **Source delivery:** `scripts/build-source.py` does not put `image/` into `nex-source.tar.gz`; the guest ships the Nex source and the Debian
  package inventory (`components.tsv`) but not the image tooling.
- **CI:** the workflows were never run on a forge; only their structure is tested. Secure Boot, TPM, physical hardware, the Controller UI against this guest and
  the `core-local-setup` option on a real host were not exercised. Guest `corosync-qnetd` was enabled; its quorum role was not tested.

## Commands that ran

All under the limits above, from the repository root, 2026-10-02:

- `python3 appliance/build.py --profile controller --version 0.1.0-dev --sequence 1 --docker <wrapper adding --cpus 2 --memory 4g>`: payload and mkosi tree
  built; the release step failed (defect 1). After that `image/build.sh controller` (with `NEX_CONTROLLER_PAYLOAD`) was re-run after each fix, four times in all.
  The `build.py` path after the payload step (`vm-image.json`, cleanup) was **not completed end to end**; `vm-image.json` generation was not exercised.
- `python3 tests/integration/controller_image_boot.py --release <controller-release>` with `--skip-tamper` and `--keep` for the first runs, the full
  run last.
- `go test -p 1 -count=1 ./internal/vmhost ./cmd/nex-core-local`: `ok`.
- `python3 -m unittest tests.test_core_local_package tests.test_appliance_build tests.test_image_layout tests.test_ci_security`: see the final yield of this
  slice for the last run.
