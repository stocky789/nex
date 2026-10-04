# Immutable Nex Core

Status: **options approved by the project owner on 2026-10-02 (UEFI-only image edition; Debian stays the package source; one root
signature; no remote publishing); partly implemented, simulation-tested only.** Date: 2026-10-01. Scope: turn Nex Core from a mutable
Debian host managed by `apt` into an image-based host with a verified read-only root, atomic A/B updates with
automatic rollback, persistent state kept apart from software, optional features as signed layers, and a
reproducible, auditable image build. Debian 13 stays the package source; the immutable layer is built on it.
What exists and what does not is in [Implementation status (2026-10-02)](#implementation-status-2026-10-02); the sections below keep
their original proposal wording, and every `[INFERENCE]` marker there is an unverified claim unless that section says it was run.

- **API impact:** changed contract (existing update/status operations, host observed state) and new
  contract (image stage/activate/rollback, pool rollout job, layer inventory/ensure, on-demand payload fetch).
  Exact operations are in each section. The Phase I-1 package trim alone is `none`.
- **Guide impact:** updated. Install, update, recovery, verification, disk-replacement and storage pages change.
  The full list is in [Documentation checklist](#documentation-checklist).
- **State owner:** `core`. Slot/rollback state, layer enablement, rollout job, and payload inventory live on
  the host (pool-wide rollout state on the pool master). A fresh Controller recovers all of it by re-adopting
  the pool, so the replacement test holds. No Controller-owned data is added.
- **Architecture invariant check:** Core runs staging, verification, activation, rollback and the pool rollout.
  The Controller only submits requests and displays host-reported results, and no update step depends on it
  staying connected.

## Implementation status (2026-10-02)

Written from the QA records in `docs/qa/image/` (phase0, phase2-build, phase2-installer, phase2-layers, phase2-packaging, phase2-repro,
phase3-*, phase4-*, phase5-core-image and phase6-lab1 to lab4). **Every result is simulation only**: unit tests, fake libvirt and fake
peers, Docker builds, and disposable QEMU/KVM guests on one workstation, including three nested guests with a real Corosync cluster
([phase4-lab.md](../qa/image/phase4-lab.md), [phase6-lab3.md](../qa/image/phase6-lab3.md), [phase6-lab4.md](../qa/image/phase6-lab4.md)) and virtual
Secure Boot and a virtual TPM ([phase6-lab1.md](../qa/image/phase6-lab1.md)). No physical hardware, firmware, Secure Boot, TPM, md RAID mirror
on real disks or browser walkthrough of the new screens was involved. "Done" below
means the gate's items were observed in that environment, not that the phase is accepted.

| Phase | State | Evidence and what is missing |
|---|---|---|
| **I-0** | **Partly done** | Spikes S1-S6 and S9 ([phase0.md](../qa/image/phase0.md)) and S7 ([phase2-installer.md](../qa/image/phase2-installer.md)). **S8** (full size budget) not run. Owner decisions recorded: UEFI-only image edition, Debian stays the package source (policy wording now in `AGENTS.md`, `PRODUCT.md`, `README.md`), one root signature, no remote publishing, private hosts never in tracked files. **Still open:** CDDL module redistribution (layers are internal-lab only), Secure Boot posture beyond "supported, not required", release-key custody, reinstall versus conversion of existing dev hosts. |
| **I-1** Read-only classic | **Partly done** | Separate `/var`, read-only root with `/etc` and `/root` kept on `/var` by an initramfs script, package installs in a remount window, the host package trim and the system-disk replacement changes: change record `docs/changes/2026-10-01-readonly-classic-root.json`. Verified by Go unit tests, `installer/check.sh` and a simulated QEMU host (`readonly_root.py`) only; **not run:** a real Debian Installer run, the installed `nex-core-local` package, GRUB and UEFI boot of it, a real mirror rebuild. The classic edition keeps its `apt` update path, the Controller `.deb` and the single-disk appliance until the plan retires them. |
| **I-2** Image, layers, trim | **Partly done** | `image/` build, `internal/hostimage`, installer payload, layers and the double build: [phase2-build.md](../qa/image/phase2-build.md), [phase2-installer.md](../qa/image/phase2-installer.md), [phase2-layers.md](../qa/image/phase2-layers.md), [phase2-repro.md](../qa/image/phase2-repro.md) (base image byte-identical across two builds; layers and the Nex-built debs were not double-built). Not run: licence gate D5 inspection, physical install. |
| **I-3** A/B, verity, update API | **Partly done** | Engine, boot wiring, rollback, Controller relay, layers and offline media: [phase3-engine.md](../qa/image/phase3-engine.md), [phase3-boot.md](../qa/image/phase3-boot.md), [phase3-reproof.md](../qa/image/phase3-reproof.md) (its own "Not run" lists apply), [phase3-controller.md](../qa/image/phase3-controller.md), [phase3-layers-update.md](../qa/image/phase3-layers-update.md). VM proof on a single disk only; md mirror, Secure Boot and hardware not run. |
| **I-4** Appliance and pool rollout | **Partly done** | Coordinator ([phase4-rollout.md](../qa/image/phase4-rollout.md), fake peers) and the journaled appliance swap ([phase4-appliance.md](../qa/image/phase4-appliance.md), fake libvirt). **New:** the Controller guest image profile (`image/controller/`, `appliance/build.py --profile controller`), the deploy wiring (`core-local-setup configure --controller-image-sha256`, `nex-core-local --controller-system-image`) and a QEMU boot of the guest: [phase4-controller-image.md](../qa/image/phase4-controller-image.md). Three booted image hosts then ran a pool rollout, first without ([phase4-lab.md](../qa/image/phase4-lab.md)) and then with a real Corosync cluster, a master power cut and guest shutdown ([phase6-lab3.md](../qa/image/phase6-lab3.md), [phase6-lab4.md](../qa/image/phase6-lab4.md)), all virtual: no real watchdog or BMC (a lab Redfish endpoint stood in), and some scenarios are listed as not run in those records. **Not run:** a Core deployment of the Controller image, a swap or a restore on a real guest, fencing on physical hardware, the Controller UI in a browser against any of it. |
| **I-5 / I-5a** Secure Boot / TPM, Core in the image | **Partly done** | I-5a ([phase5-core-image.md](../qa/image/phase5-core-image.md)): an image that contains `nex-core-local`, booted once in QEMU/KVM. Secure Boot and TPM posture were exercised only virtually (OVMF variables, `swtpm`) in [phase6-lab1.md](../qa/image/phase6-lab1.md). The Controller guest image and its UKI are unsigned; its domain pins UEFI with Secure Boot off. No physical Secure Boot or TPM result exists. |
| **I-6** Local labs | **Partly done** | [phase6-lab1.md](../qa/image/phase6-lab1.md) and [phase6-lab2.md](../qa/image/phase6-lab2.md) (installer and boot scenarios), [phase6-lab3.md](../qa/image/phase6-lab3.md) and [phase6-lab4.md](../qa/image/phase6-lab4.md) (pool rollout). Simulation only; each record lists its own "Not run" items. |

### Review corrections (2026-10-03)

The immutable-branch review's 34 findings are corrected across arm/reboot recovery, offline
preflight, Controller replacement, pool-control replay, release publication, installer navigation
and lab evidence. [Verification](../qa/2026-10-03-immutable-review-fixes.md) records the actual
automated/native scratch/simulated browser checks and limits; no phase status or physical,
firmware, live-forge or redistribution gate is promoted by this work.

**API impact: changed contract.** Existing pool update commands, inventory and the signed
`update_rollout` section carry normalized control receipts, pending-result/reservation/rollback
state and the documented retry/admission errors. Execution and durable state remain on Core:
the master's `pool-update.json` and members' signed `pool-replica.json` carry the same history
through existing adoption. A fresh Controller obtains `Inventory.managed.update_rollout` over
the authenticated host API. No Controller-owned pool state or new endpoint is added; the
coordinated Core version requirement and old-master task-display limit are documented in
[API.md](../API.md) and the [pool update guide](../guide/update-a-pool.md).

**Guide impact: updated.** Installation/repair, update/retry, appliance recovery and publication
instructions now describe the checked boundaries. The source-only lab fixes do not imply a
new successful boot, restore or security observation.

Cross-cutting items:

- **CI:** `.github/workflows/image.yml` and `.gitea/workflows/image.yml` exist (forge-only jobs: layout tests, host image build and boot,
  tamper and A/B proof, Controller guest build and boot, double build). The inspected development run on 2026-10-04 passed
  layout checks but failed before boot/build proof: the two boot jobs lacked `/dev/kvm`, and reproducibility lacked host `rsync`.
  The prerequisite repair does not establish successful image builds, boots or reproducibility; a subsequent forge run is still required.
  **API impact: none** — CI execution only, with no Controller/Core or client contract change.
  **Guide impact: none** — installed operator workflows are unchanged. **State owner: none** — no durable product state.
  `.github/workflows/github.yml` also triggers on pushes to `dev` ([release channels plan](2026-10-02-release-channels.md)).
- **Controller update helpers:** removed only where the swap replaces them: the profile's cloud-init seed no longer carries
  `apply-update` or the update units, and the guest image has no `apt` or helper. **Still present, for the classic edition and the
  single-disk appliance:** `packaging/controller/{apply-update,nex-controller-update.path,nex-controller-update.service,postinst,prerm}`,
  `appendUpdateBootstrap` (single-disk seeds), the appliance build's copies, the Controller `.deb`, and the Controller's own APT routes
  (`GET/POST /api/v1/updates`, `internal/controller/updates.go`) and Settings panel. They go with the classic edition.
- **Deviations from the plan text:** the guest's `/etc` is the same persistent overlay as the host's (upper layer on the data disk), not a
  bind mount of `/etc/nex-controller`; `/etc/nex-controller` therefore lives on `controller-data.qcow2` through the overlay and recovery
  sees the same two guest paths. The release carries the Controller image as an on-demand `vm-image` component, `controller-system.img`,
  a raw GPT disk (ESP, erofs, verity) rather than a bare erofs.
- **Open:** deployment of the profile by the local deploy page end to end; a real swap (reflink checkpoint, libvirt redefine, readiness,
  restore); host-recovery export of a swapped Controller; the Controller UI for choosing the profile; the guide pages stay marked
  unverified; reproducibility of the Controller guest image (a double build was not run; the release step alone repeated byte-identical
  over one tree); source delivery for `image/` (the source archive does not contain it); layers' double build; the standalone Controller
  `.deb` decision (open question 7) and retirement of the classic edition.

## Feasibility verdict and how imaging works (revision 2026-10-01)

**Doable, as a staged programme, not one change.** Each mechanism ships today on Debian-based systems
(Garden Linux, ParticleOS; see the image section's evidence). What no one has shown is Nex's *combination*:
Debian Installer as installer, per-disk A/B slots, md RAID1 only for data, `/etc` overlay, sysext layers, and
Secure Boot through MOK. Nothing here has been run. Phase I-1 (read-only classic) is low risk and pays off
regardless, because the image needs the same `/var` split, persistent `/etc` and runtime-write fixes. Do not
start I-2 until the I-0 spikes below pass. The Controller-appliance immutability in I-4 is separable and may
slip without blocking an immutable Core.

### The model: five classes of bytes

| Class | Where | Written by | Update | Rollback |
|---|---|---|---|---|
| Image | root partition (erofs) + verity partition in the inactive slot, UKI on each ESP | installer or Core slot writer only, and only into the **inactive** slot | replaced whole | previous slot and UKI kept until the next stage |
| Layers | `/var/lib/nex/layers/store/`, merged over `/usr` by sysext | Core, signed, bound to one base build | rebuilt with every base | old layer kept until commit |
| Config | `/etc` overlay upper on `nex-data` | operator, Core, libvirt | survives; image defaults change underneath | survives; shadowed files reported |
| State | `/var` on `nex-data` (mirrored) | every running service | survives | survives; expand/contract rule |
| Payloads | `/var/lib/nex/payload/<sha256>/` | Core fetch | independent of image | independent |

Invariant: the running system never writes the slot it booted from. The only root-partition writer is Core,
into the inactive slot. The commit point is one atomic rename of a UKI on the ESP.

### What one release is

CI produces, per `sequence` N, one signed set (the manifest's `slot-image` component names all of it):

1. `root.erofs`, the root filesystem built from the locked package set (reproducible flags: fixed timestamp, UUID, ordering).
2. `root.verity`, the dm-verity hash tree over it, and the 256-bit `roothash`.
3. The UKI `nex-core_<label>_<N>.efi`: kernel, initrd, cmdline (including `roothash=<hash>`), os-release, microcode, signed with the UKI key. It is built **after** the hash exists, so the signature covers the root.
4. Layers (`nex-zfs.raw`, `nex-cluster.raw`) built against that exact base, plus `manifest.json` and `.sig`.

### Lifecycle

1. **Install.** d-i boots, the wizard collects answers, `nex-image-install` wipes the selected disks, lays the same GPT on each, writes slot A (erofs to `nex-root-a`, hash tree to `nex-verity-a`; the two partition UUIDs are set from the roothash), copies the UKI and boot files to **every** ESP, builds the `nex-data` mirror, seeds the `/etc` upper (hostname, network, root hash), and places payloads. Slot B is laid out empty.
2. **Boot.** Firmware → shim → sd-boot → signed UKI. The initrd finds the root partition whose UUID matches `roothash=`, opens verity, mounts erofs read-only, mounts `nex-data` as `/var`, mounts the `/etc` overlay, and switches root.
3. **Stage.** Core downloads and verifies the manifest, writes the new erofs and hash tree into the inactive slot **on every disk**, flips that slot's partition UUIDs to the new roothash, reads back and compares. The next boot is still the old version.
4. **Activate.** Core writes the new UKI to each ESP as `.efi.new`, renames it to `…+3.efi` (the commit point), and reboots.
5. **Assess.** `nex-boot-check` passes → `systemd-bless-boot` drops the counter → committed. Three failed tries → sd-boot picks the previous UKI, which still points at the intact old slot → `rolled_back`.
6. **Rollback / next update.** Rollback re-arms the retained UKI. The next stage overwrites the old slot.

### What "immutable" does and does not promise

Verity checks every block at read time, so offline tampering of a slot becomes an I/O error and boot falls
back. That is **tamper-evident only with Secure Boot on**; without it an attacker with disk access can replace
the UKI. With Secure Boot off, the benefit is atomic updates, rollback and a clean separation of state, not
security. A runtime root attacker can still write the raw partition, and verity turns that into a denial of
service, not a silent change. State this plainly in the guide; do not claim attestation before I-5.

### I-0 spikes (each needs a recorded pass/fail in `docs/qa/`)

| # | Question | Pass criterion |
|---|---|---|
| S1 | Slot discovery | Two `root-x86-64` partitions per disk; `roothash=` selects the right one; mirrored disks carry duplicate partition UUIDs and each boots alone |
| S2 | Live slot rewrite | Core rewrites the inactive slot and changes its partition UUIDs while `nex-data` is mounted (kernel learns via BLKPG/`partx`, no reboot); a crash between data write and UUID flip leaves the old slot booting |
| S3 | `/etc` overlay | Swapping the lower (a different erofs) under the same upper still mounts (overlay `index`, `metacopy`, `redirect_dir`, `xino` settings recorded); libvirt, sshd and network writes land in the upper; rollback keeps config; confext-mutable versus initrd unit chosen |
| S4 | Firmware boot | Removable-path boot falls through to disk 2 on the lab firmware; if not, Core must also create `efibootmgr` entries per ESP (add `efibootmgr` to base); BLS counting correct on a converged ESP pair |
| S5 | Secure Boot | Debian-signed sd-boot loads a Nex-signed UKI through MOK under OVMF; Secure Boot off also boots |
| S6 | sysext layers | `modprobe zfs` after a live merge. Module files in a layer need a `modules.dep` that covers base plus layer, or the layer's file hides the base's; if that is brittle, ship the modules in the base and gate only userland. `nex-cluster` merges with no reboot and no `/etc` defaults |
| S7 | Installer host | Either d-i runs the Go tool with the udebs it needs (GPT, `mdadm`, FAT/ext4 mkfs) and its menu is prunable, or the installer is a live image built by the same mkosi tooling. Choose by result; the sections below assume d-i |
| S8 | Sizes | Measured root, verity, layers and full-update size. Slot size has ≥1.5× headroom over the largest release (slots cannot grow without moving `nex-data`). The 16 GiB disk floor probably rises: `nex-data` must hold a staged update, the 1.6 GB of payloads and the default VM store |
| S9 | Reproducibility | Two independent builds compared with `diffoscope`; every difference explained |

### Environment to set up

- **Build host:** x86_64 Linux, KVM, and enough disk for the Debian snapshot mirror plus several multi-GB images. Tools: `mkosi` ≥25 (trixie has 25.3; use `ToolsTree=` pinned to Debian trixie so any workstation distribution works), `systemd` ≥257 (`ukify`, `systemd-repart`, `systemd-sysupdate` for development only), `erofs-utils`, `cryptsetup` (`veritysetup`), `sbsigntools`, `python3-pefile`, `dosfstools`, `mtools`, `qemu-system-x86`, OVMF with Secure Boot variables and `virt-fw-vars`, `swtpm` (I-5), `diffoscope`, `debian-archive-keyring`, plus the existing Go/Docker build tools.
- **Inputs:** access to `snapshot.debian.org`, and a mirror of the locked debs in release storage so builds do not depend on it.
- **Keys:** throwaway development keys generated in a lab directory, never committed. Production keys (UKI/Secure Boot, PCR policy, module, update metadata, apt) follow the key decisions below; CI uses PKCS#11 or a secret, and release builds fail closed without them.
- **Lab:** nested-KVM VMs (UEFI, two virtual disks each, three hosts for pool rollout), user-mode networking, TCG fallback. One physical UEFI host with two disks and a Secure Boot switch, and one with a TPM2, are needed for I-5; virtual labs do not stand in for them.
- **CI:** a forge runner with `/dev/kvm` for `image.yml` (heavy jobs skip on github.com per AGENTS.md), mirrored under `.gitea/workflows/`.
- **Hosting:** static HTTPS with `Range` support for the update tree (`trust.json`, channels, releases, blobs) and the retained source bundle.

**Plan-only change: API impact none, guide impact none.** This revision edits the plan and changes no contract.

## Decisions needed before Phase I-1 starts

**Superseded.** The approvals are recorded in `AGENTS.md` and the owner decisions in the QA records; phases I-1 to I-4 are built (see the
status table). Kept as the history of what was asked, not as open questions.

The plan recommends the options below. These change documented project policy or legal posture, so the
project owner decides; the plan does not assume them.

1. Approve the wording change to `AGENTS.md` (Debian packages/systemd, "Keep the Debian base intact"),
   `PRODUCT.md` and `README.md`: Debian remains the source of packages, but hosts are images, not `apt` systems.
2. Redistributing prebuilt, Nex-signed OpenZFS modules (CDDL) reverses `docs/LICENSING.md` ("no prebuilt module
   ships today"). Needs a legal review. Fallback: build the module on the host at stage time.
3. Image edition is UEFI-only. BIOS-only hosts keep the classic (Phase I-1) edition until retired.
4. Secure Boot: supported but not required for this release?
5. Reinstall existing dev hosts, or build an in-place conversion?
6. Release-key custody and whether the release key may live in CI.

The per-section "Open questions" lists below carry the rest; the consolidated table is at the end.

## Offline and network installer payloads

The offline ISO **keeps bundling** the VirtIO Windows driver ISO and the Controller appliance, as required.
**The network installer already downloads the VirtIO ISO** from the pinned Fedora URL
(`installer/netinst-manifest.py:51`, fetched by `installer/netinst-fetch.sh`, bundled copy removed at
`installer/build.sh:~735`). What it still does not do is download the ~750 MB Controller appliance separately:
that image travels inside the `nex-core-local` package. This plan makes the appliance and the VirtIO ISO two
content-addressed, signed payloads that the offline ISO carries and the netinst downloads, both stored outside
the verified root slot (`/var/lib/nex/payload/<sha256>/`). See the installer and composition sections.

## Reconciliation notes (read first)

The four sections were written in parallel against agreed interfaces. Where they differ, this table decides.

| Topic | Decision |
|---|---|
| Disk layout, boot chain, `/etc` strategy | **Image architecture** section is authoritative: UEFI-only, per-disk GPT with A/B slots and verity partitions, only `nex-data` mirrored. The installer section's md-over-slots wording follows it. |
| Phase numbering | The master phases are **I-0 to I-5** (see [Delivery phases](#delivery-phases)). The image section's phases 0-4 map: 0 = I-0, 1 = I-1, 2 = I-2 and I-3, 3 and 4 = I-5 and retirement of the classic edition. |
| Trust anchor for the appliance and layers | The **Updates** section's `/usr/share/nex/update/trust.json` replaces `release.pub`. Composition work item that keeps `release.pub` is superseded. |
| `nftables` | Kept. libvirt's network driver depends on `iptables \| nftables`, so dropping it just makes apt pick `iptables`. This corrects the earlier read-only audit. |
| `virtinst`, `sudo`, host `corosync-qnetd` | Dropped from the host image (no host callers; qnetd is Controller-hosted). |
| Appliance and VirtIO location | Outside the slot, in `/var/lib/nex/payload/<sha256>/` (image and installer sections agree; composition's `artifacts/` path name is superseded). |
| Layer store | `/var/lib/nex/layers/` with `enabled.json` (composition section), `state.owner: core`. |
| Section heading names | The Updates section links to the exact headings "Image architecture and filesystem layout" and "Image composition, optional feature layers, package trim and build reproducibility". Keep them when editing. |
| `/etc` upper path, state file | Installer-section text that says `/lib/nex/etc/…` or `/var/lib/nex/image-state.json` means `/var/lib/nex/etc/{upper,work}` and `/var/lib/nex/image/state.json` (image section). |
| Commit and migration rule | The updates section's expand/contract with pool-wide `finalized_sequence` replaces the installer section's per-host side journal and "operator confirms" commit. |
| Reproducibility gate | I-2 produces a `diffoscope` report and a recorded roothash comparison; byte-identical roothash is a hard gate before the first external release (I-3). |

## Delivery phases

Every gate follows AGENTS.md order: Core behavior, durable host state, host API, outage/recovery checks,
Controller UI, guide walkthrough. Virtual labs never establish physical Secure Boot, TPM or hardware behavior.

| Phase | Scope | Gate |
|---|---|---|
| **I-0** Measure and decide | Spikes S1-S9 above, installed-size baseline, d-i udeb pruning, owner decisions | Written go/no-go and measured sizes in `docs/qa/`; no product change |
| **I-1** Read-only classic | `/` mounted `ro`, persistent `/etc`, separate `/var`; apt only in a transitional remount window; safe package drops; netinst appliance split; fix every runtime write outside `/etc`,`/var`,`/run` | `findmnt` shows `ro`; existing suites pass; Controller replacement and reboot keep config |
| **I-2** Image, layers, trim | `image/` build, `internal/hostimage`, image installer, optional layers, prebuilt modules (owner-gated), reproducible build, manifest and source bundle | Offline and netinst UEFI install (single disk, RAID1); BIOS firmware refused before any write; `diffoscope` report on a double build; licence gate inspected |
| **I-3** A/B, verity, update API | Slot B, boot counting, rollback, tamper refusal, Core update/rollback API and UI | Interrupted-update, power-loss, rollback-preserves-config, state-version gate |
| **I-4** Appliance and pool rollout | Immutable Controller appliance, pool-master rolling update | Quorum kept, VMs not duplicated, rollout resumes after master change |
| **I-5** Secure Boot / TPM | Signed chain, measured boot, sealed secrets | Enforcing lab refuses unsigned slot; hardware result recorded separately |

## Image architecture and filesystem layout

### Decisions

**D1 — Mechanism (recommended): mkosi-built Debian 13 image, erofs root under dm-verity, Nex-signed UKI, systemd-boot, per-disk A/B slots with BLS boot counting. Core (Go) stages and writes slots; `systemd-sysupdate` is a format-compatible dev/recovery tool, not the runtime engine.** Debian stays the package source; the immutable layer is built on it (an explicit project-owner decision to record, per AGENTS.md "keep the Debian base").

| Option | Verdict and evidence |
|---|---|
| **mkosi + UKI + verity A/B** | Chosen. All parts are in trixie: systemd 257.13 ([page](https://packages.debian.org/stable/systemd)), `systemd-repart` ([page](https://packages.debian.org/trixie/systemd-repart)), `systemd-ukify` ([man](https://manpages.debian.org/trixie/systemd-ukify/ukify.1.en.html): `--microcode`, `--join-profile`, PCR signing), `systemd-boot` (ships `systemd-bless-boot`), `systemd-boot-efi-amd64-signed` ([wiki](https://wiki.debian.org/SecureBoot)), sysupdate in `systemd-container` ([contents](https://packages.debian.org/search?searchon=contents&keywords=systemd-sysupdate&mode=filename&suite=trixie&arch=amd64)), [`mkosi` 25.3-7](https://packages.debian.org/trixie/mkosi) (`Distribution=debian`, `SourceDateEpoch=`, `Seed=`, `ToolsTree=`; [man](https://manpages.debian.org/trixie/mkosi/mkosi.1.en.html)). The same shape runs on Debian today: [Garden Linux](https://docs.gardenlinux.org/explanation/secure-boot) (EROFS root, UKI, systemd-boot, TPM2-sealed `/var`) and [ParticleOS](https://github.com/systemd/particleos) (Debian is a supported base). The ISO build already pins `snapshot.debian.org` (`installer/build.sh:364-365,505-511`). |
| ostree / bootc | Rejected. trixie's `ostree` 2025.2-1 "does not affect the boot process" ([page](https://packages.debian.org/trixie/ostree)); no Debian `bootc` package, Debian bootc images are [community experiments](https://github.com/bootcrew/debian-bootc), and bootc's composefs backend is [experimental](https://bootc.dev/bootc/experimental-composefs.html). Needs an OCI pipeline and Nex-owned GRUB/initramfs glue. |
| RAUC | Rejected. [`rauc` 1.13](https://packages.debian.org/trixie/rauc) only installs slots: no builder, no booted-root integrity, efibootmgr/GRUB state instead of BLS counters, a second update agent beside Core. |
| Kairos / Elemental | Rejected. Kairos has a [Debian 13 flavour](https://kairos.io/docs/v3.7.2/architecture/meta) but is an edge-Kubernetes meta-distribution with its own agent; [Elemental](https://elemental.docs.rancher.com/) is a Kubernetes-managed stack on SLE Micro. Both duplicate Core's ownership. |
| Read-only root + overlay only | Not an end state: no atomic update or rollback, dpkg still mutates. It is Phase 1. |

**D2 — Bootloader: systemd-boot, UEFI only (not GRUB).** GRUB under shim verifies the kernel but not initrd or command line [INFERENCE], so a root hash on its command line is unauthenticated; a signed UKI covers kernel, initrd, cmdline and, via `roothash=`, the whole root. Cost: no legacy BIOS boot, which the installer supports today (`installer/gui/nex-config.c:206-210`). The classic edition stays the BIOS path (open question 2).

**D3 — Slots live on each disk, not under md.** Only `nex-data` is mirrored. Each disk carries its own ESP and both slots, so any one disk boots alone; repair is "re-lay GPT, copy verity-verified slots, `mdadm --add` data". Rejected: md under the slots (mdadm needed in early boot for root, and hash-based slot discovery below needs a plain GPT).

**D4 — `/etc` is an overlay (lower: image `/etc`; upper: `/var/lib/nex/etc/upper`).** Every Go config writer already targets `/etc` and needs no change: `internal/vmhost/managed.go:72` (`/etc/network`), `storage_native.go:71`, `storage_cluster_boot.go:24-25`, `pool_witness.go:21` (`/etc/systemd/system`), `storage_cluster.go:35` (`/etc/corosync`), `watchdog_arming.go:51` (`/etc/default/sbd`), `internal/nexupdate/nexupdate.go:32,126` (`/etc/apt`, `/etc/nex`). Mechanism: `systemd-confext` mutable mode (write routing in `/var/lib/extensions.mutable/etc`, [systemd 257](https://manpages.debian.org/trixie/systemd/systemd-sysext.8.en.html)) if Phase 0 shows it works with a base confext; otherwise a ~15-line initrd overlay mount unit. Rejected: stateless `/etc` rendered by Core (rewrites at least seven writers); keep as later hardening.

**D5 — ZFS is a kernel-locked `nex-zfs` layer, never DKMS on the host.** Built in CI against the exact image kernel from `zfs-dkms` 2.3.9 ([trixie](https://packages.debian.org/trixie/zfs-dkms)), signed with the module key, staged in the *same* transaction as the base so kernel and module never skew. **Conflict:** `docs/LICENSING.md:217-225` records that no prebuilt ZFS module is redistributed; shipping this layer supersedes that (open question 1). Fallback if refused: Core builds the module at stage time in a sandbox from a signed toolchain layer and signs with a per-host MOK key (weaker: the key sits in `/var`, as DKMS's does today).

### Disk layout

Identical GPT on every selected disk (replaces the recipe at `nex-config.c:241-245`; no BIOS-boot partition):

| # | Label | Type | Size | Content |
|---|---|---|---|---|
| 1 | `nex-esp` | EFI System | 1 GiB | FAT32: shim and signed sd-boot on the removable path (as `nex-config.c:251-253`), `/EFI/Linux/nex-core_<label>_<seq>[+N].efi`, `loader/loader.conf` |
| 2 / 4 | `nex-root-a` / `-b` | root-x86-64 | ≤4 GiB each [INFERENCE: measure in Phase 0] | erofs; partition UUID = roothash bits 0–127 |
| 3 / 5 | `nex-verity-a` / `-b` | root-x86-64-verity | 64 MiB | hash tree; UUID = roothash bits 128–255 |
| 6 | `nex-data` | Linux data (1 disk) / Linux RAID (mirror) | rest; ≥6 GiB at the 16 GiB floor | ext4, label `nex-data`, mounted `/var` from the initrd. Mirror: md RAID1 1.2, array `nex-data`, assembled by udev in the initrd |

Slot selection is stateless: the signed UKI's `roothash=` names the slot because the [Discoverable Partitions Spec](https://uapi-group.org/specifications/specs/discoverable_partitions_specification/) derives both partition UUIDs from the root hash. The UKI on the ESP is the update's commit point; Core invalidates a slot's label before rewriting it. No ESP in fstab and no `nofail` hacks: Core mounts each ESP transiently under `/run/nex/esp/<partuuid>`.

**Replaces `nex-sync-esp` and the APT hook:** a Go ESP converger in `internal/hostimage` finds ESPs by partition label (not md membership or fstab), writes temp-then-rename with the UKI last, verifies SHA-256, records per-ESP state. Triggers: update stage and arm; `nex-esp-sync.service` (After=`systemd-bless-boot.service`; propagates the blessed filename to the other ESPs and heals a replaced disk); `host-system-disk-replace-v1`. Deleted: `installer/late-install.sh:114-174` (helper, `99nex-sync-esp` hook, fstab `sed`) and `internal/vmhost/md_replace.go:241-305` (`grub-install`, `mkfs.vfat`, `mdSyncESPHelper`, `mdCopyESP`).

### Read-only, persistent and tmpfs

| Path | Class |
|---|---|
| `/` incl. `/usr`, `/opt`, `/usr/local` | erofs + verity, read-only |
| `/etc` | overlay on persistent upper (D4) |
| `/var` (`/var/lib/libvirt`, `/var/lib/nex*`, `/var/lib/nex-core-agent`, journal, `/var/lib/nex/image`) | `nex-data` |
| `/root` | symlink to `/var/roothome` |
| default VM store `/var/lib/libvirt/images` (`core-local-setup:122`) | `nex-data`; ZFS/NFS/iSCSI/GFS2 data pools are separate and untouched |
| `/tmp`, `/run`, `/run/issue.d` | tmpfs |
| VirtIO ISO, 877 MB (`bundled_media.go:24`, `late-install.sh:27-28`); Controller appliance, ~750 MB (`core-local-setup:14`) | out of the slots, into `/var/lib/nex/payload/<sha256>/`: no 2× duplication or re-download per update. Offline ISO bundles both; netinst downloads, as today |

No dpkg/apt database ships (`CleanPackageMetadata=yes`); `/usr/share/doc/*/copyright` and `/usr/share/nex-core/package-manifest.tsv` stay for `docs/LICENSING.md`.

### Boot chain, keys, TPM

Firmware → (Secure Boot on) Microsoft-signed shim → Debian-signed sd-boot → Nex-signed UKI (`ukify`: kernel, initrd, cmdline, os-release, microcode, SBAT merged) → verity root. Kernel arguments are baked in (`systemd.ssh_auto=no` from `late-install.sh:182-185`; `console=tty0 console=ttyS0,115200n8`); no cmdline editing. Secure Boot is supported, not required: when off the host works and reports `secure_boot=false`.

Five separate keys, offline or HSM, used by CI through PKCS#11: UKI/Secure Boot cert, PCR-policy key, kernel-module key, update-metadata key (updates section), apt GPG key (existing). Under lockdown, modules not signed by a trusted key are refused, and a MOK is how a third-party key is trusted; sd-boot with shim is supported in trixie ([wiki](https://wiki.debian.org/SecureBoot)). Physical hosts enrol the Nex cert once through MokManager (the installer prompts); VMs auto-enrol ([`SecureBootAutoEnroll=`](https://manpages.debian.org/trixie/mkosi/mkosi.1.en.html)). A Nex-signed UKI loaded by Debian's signed sd-boot through MOK is [INFERENCE] and a Phase 0 gate. Firmware lacking the 2011 Microsoft CA needs Debian's newer shim ([wiki](https://wiki.debian.org/SecureBoot)).

TPM2 (Phase 3, optional): the stub measures UKI parts, and a signed PCR 11 policy survives updates. Nex gets (a) optional TPM-sealed `nex-data`, which is out of scope today (`2026-09-22-software-raid.md:252-255`), and (b) self-reported `secure_boot`, `uki_sha256`, `roothash`, `pcr11` in host image status. Remote quote verification is an owner decision.

### Kernel, initrd, identity, recovery, rollback

- **Kernel and initrd** arrive only inside the image: no `update-initramfs` or `linux-image` postinst on a host. mkosi builds a systemd initrd with `erofs`, `dm-verity`, `overlay`, md and storage drivers (`erofs.ko` and `dm-verity.ko` are built as modules in trixie's 6.12 kernels, so the initrd must carry them). Microcode (`intel-microcode`, `amd64-microcode`) is a separate early initrd; runtime firmware stays in the root, keeping the exclusions at `installer/build.sh:464-468`.
- **Identity.** `/etc/machine-id` is empty in the image and written to the upper layer at first boot; `storage_native.go:317` and the sealed SMB key (`nex-core-local.service:18`) need it stable. `nex-firstboot.service` runs `ssh-keygen -A` once. Root hash, hostname, timezone and network files from the installer land in the upper layer (today `late-install.sh:228`). Fingerprints and machine-id are unchanged across update and rollback.
- **Local console recovery.** The sd-boot menu (tty and serial) lists current, previous and `recovery`, a second UKI profile (`ukify --join-profile`, `rescue.target`, no Core services, no VM autostart). Both slots bad: boot Nex media, **Repair image** re-lays GPT and slots and keeps `nex-data`.
- **Rollback triggers.** (1) Boot counter exhausted (`+3`, [BLS counting](https://systemd.io/AUTOMATIC_BOOT_ASSESSMENT/)): kernel panic (`panic=10`), initrd verity/mount failure (initrd emergency must reboot, not wait), failed `nex-boot-check.service`. (2) `nex-boot-check.service`, Before/RequiredBy `boot-complete.target`, tests only image-caused failure: `/var` and the `/etc` overlay writable, libvirtd answers, local `nex-core-local` health, ZFS module loads when a pool is configured. Never link or IP presence, quorum, peers or Controller. (3) Operator revert arms the previous UKI. A hang without panic needs an external reset, after which the counter decrements; systemd's watchdog stays off because sbd/wdmd may own `/dev/watchdog`. Rollback is an ordinary host reboot: VM start authority (fencing, sanlock, ownership hooks) is unchanged, so no new duplicate-writer path. Precedent: an unconfirmed management-network change self-reverts (`nex-core-local.service:22-25`).

### Phases

| Phase | Ships | Gate |
|---|---|---|
| **0 Spike** (lab VMs only) | mkosi image boots under OVMF with Secure Boot, verity, UKI via MOK, `/etc` overlay (confext vs unit), md data in the initrd, libvirt VM start, hash-selected slot among two same-type slots, duplicate partition UUIDs across mirror disks; slot and update size measured | written go/no-go |
| **1 Read-only classic** (first slice; Debian packages, GRUB, d-i, BIOS+UEFI kept) | new installs get a separate `/var` (md pair when mirrored) and root mounted `ro`; `/etc` and `/root` persist as binds of `/var/lib/nex/etc` and `/var/roothome` from an initramfs-tools `local-bottom` script; dpkg runs only inside a `remount,rw` window that `nex-root-ro.service` re-asserts | existing Phase suites pass on a read-only root; no runtime writer outside `/etc`, `/var`, `/run` |
| **2 Image edition** | `image/` build, `internal/hostimage`, image installer, A/B stage/arm/bless, `nex-zfs` layer (owner-gated), UEFI only | acceptance 1–9 |
| **3 Hardening** | Secure Boot ceremony, PCR policy/TPM options, reproducible-build gate, operator-owned keys | two builds, one roothash |
| **4 Retire classic** | delete the package-manager update path | owner approval |

### Work items

1. **Phase 0:** create `image/` (`mkosi.conf`, `mkosi.conf.d/`, `mkosi.repart/{10-esp,20-root-a,21-verity-a,30-root-b,31-verity-b,40-data}.conf`, `mkosi.extra/`, `mkosi.finalize`). Package list = `installer/packages.txt` minus `linux-headers-amd64`, `zfs-dkms`, plus `shim-signed`, `systemd-boot`, `systemd-boot-efi-amd64-signed`, `gdisk`, `dosfstools`, `systemd-repart`; local `PackageDirectories=` carry the Nex libvirt `+nex4`, rebuilt `dlm-controld` and `nex-core-local` debs. Record `docs/qa/image/phase0.md`.
2. **Phase 1:** `installer/gui/nex-config.c:206-257` (add `/var`, `ro` root; `installer/check.sh:123-139` and `tests/integration/installer-hostname.py` follow); `packaging/core-local/` new initramfs script and `nex-root-ro.service`; `internal/nexupdate/nexupdate.go:283-291,479-559` wrapped in the rw window (with the updates section); `tests/integration/readonly_root.py`.
3. **Phase 2:** `internal/hostimage/{layout,slot,esp,etc}.go` + tests; `image/mkosi.extra/usr/lib/systemd/system/{nex-boot-check,nex-esp-sync,nex-firstboot,nex-state-guard}.service` (`nex-state-guard` belongs to the updates section); rewrite `internal/vmhost/md_replace.go:187-196,241-305` and `md_replace_test.go` to re-lay GPT from the repart definitions, copy and verify slots, converge ESPs; image-edition branches and **Repair image** mode in `installer/late-install.sh`, `installer/build.sh`, `installer/netinst-manifest.py` (installer section); move payload paths (`bundled_media.go:24`, `core-local-setup:14`); move `postinst` behaviour into the build: static `os-release` instead of `dpkg-divert` (`:79-92`), `debian.sources` (`:34-49`), grub.d (`:97-111`), AppArmor via `libvirtd-apparmor-policy install <root>` (already root-prefixed, `libvirtd-apparmor-policy.py:16-17`).
4. **CI:** `.github/workflows/image.yml` and `.gitea/workflows/image.yml` (build, QEMU/OVMF boot, tamper, build-twice compare), heavy jobs skipped on github.com per AGENTS.md; `tests/integration/image_boot.py`, `tests/test_image_layout.py`.
5. **Source delivery:** extend `scripts/build-source-bundle.py` and the component manifest to the image closure.

### Risks

ZFS redistribution vs `docs/LICENSING.md:222-223` (blocks a Phase 2 release). Secure Boot enrolment friction on headless servers. UEFI-only excludes BIOS hosts. Overlay drift: a copied-up `passwd`/`shadow` hides new image accounts, so system accounts ship via `sysusers.d` and `nex-etc-audit` (in `internal/hostimage/etc.go`) reports shadowed image files. Full-image updates are large [INFERENCE: measure]. Debian security respins need an image rebuild cadence. Duplicate partition UUIDs across mirror disks may confuse `by-partuuid` consumers [INFERENCE]. mkosi is 25.3 in trixie (27 in testing): pin any newer build by hash after Phase 0.

### Acceptance checks

1. Build twice: identical roothash (Phase 3 gate; diffoscope report earlier).
2. QEMU+OVMF, Secure Boot on: `findmnt -no FSTYPE,OPTIONS /` is erofs `ro`; `dmsetup status` shows verity `V`; `touch /usr/x` fails; an `/etc` edit and `/var` data survive reboot.
3. Flip one byte in the active root slot: after three tries boot falls to the other slot; `bootctl list` shows `+0-3`; the host reports rolled back.
4. Power-cut at each update step (slot write, before UKI, half-written `.new`, after arm, before bless): the previous version boots every time.
5. Two-disk mirror, UEFI: detach each disk in turn and boot; run `host-system-disk-replace-v1`; both ESPs hold identical SHA-256 sets and both slots verify. Closes `docs/qa/iso/2026-09-22-installer-mirror.md:72-76`.
6. Controller deleted and quorum lost during a trial boot: `nex-boot-check` still passes and blesses; a failing libvirtd blocks the bless and the host reverts.
7. ZFS: module loads under Secure Boot lockdown after a kernel-changing update and an existing pool imports; an unsigned module is refused.
8. SSH fingerprints, machine-id, root login and hostname are unchanged across update and rollback.
9. Offline ISO installs the image edition with no network; the VirtIO ISO is under `/var/lib/nex/payload/`.

### Documentation, API, state

Guide: `install-nex-core.md`, `software-updates.md`, `replace-a-failed-system-disk.md`, `verify-nex-core.md`, `recover-without-controller.md`. Also `docs/API.md` (`replaceSystemDisk` behaviour note), `docs/LICENSING.md` (image artifacts, ZFS, firmware), `installer/README.md`, `docs/development.md` (mkosi build), CHANGELOG, `docs/DOCUMENTATION.md` assessment `updated`, and `docs/changes/<date>-immutable-image-*.json` with `state.owner: core`: record = GPT labels and ESP UKI names (the disk is the truth and self-describing), `/var/lib/nex/image/state.json` is a cache; a fresh Controller recovers it through host image status. Mark `2026-09-19-zfs-repository-provisioning.md:130-138` (DKMS abort, no module signing) and `2026-09-22-software-raid.md:200-205` (GRUB, `nex-sync-esp`) superseded for the image edition. BUG_LEDGER only if a bug is fixed.

**API impact: existing contract** for this section: `host-system-disk-replace-v1` keeps its request and result shape and changes internals; image status, stage, arm and rollback are the updates section's **new contract**. The Controller stores nothing; the replacement test holds because every fact is on the host's disks.

### Open questions for the project owner

1. Supersede `docs/LICENSING.md:222-223` and redistribute a prebuilt, signed `nex-zfs` layer, or require stage-time on-host builds?
2. Is the image edition UEFI-only, and until when does the classic edition stay for BIOS hosts?
3. Secure Boot trust root: Nex vendor cert via MOK, operator-owned keys, or both? Is Secure Boot required for support?
4. Is TPM-sealed `nex-data` encryption wanted, and is remote attestation ever a pool-admission rule?
5. Security respin cadence, and how many previous versions are retained.
6. Classic → image migration: reinstall keeping unselected data pools, or in-place conversion?

## Update pipeline, API and pool-safe rollout

Status: planned. Replaces the APT update model ([package-updates plan](2026-09-09-package-updates.md), now
historical) with Core-run atomic image updates on the slots, UKIs and boot counting defined in
[Image architecture and filesystem layout](#image-architecture-and-filesystem-layout), shipping the components defined in
[Image composition, optional layers, package trim and build reproducibility](#image-composition-optional-layers-package-trim-and-build-reproducibility).
Debian remains the package source of the *image build*; installed hosts never run `apt`. This also meets the platform
plan's "do not describe APT as transactional rollback" ([nex-platform.md](2026-09-08-nex-platform.md#8-release-and-operating-procedures), item 2).

**API impact: changed contract and new contract.** Changed: `GET /api/local/v1/updates`, `POST /api/local/v1/updates/apply`.
Removed: Controller `GET /api/v1/updates`, `POST /api/v1/updates/apply` (the Controller no longer checks an archive or updates
itself). New: the host-local, master, Controller and peer routes in [Update API](#update-api); the `pool-update` command; additive
`Inventory.image`, `Inventory.managed.update_rollout`, `ReportResponse.controller`; capabilities `image-update-v1`,
`pool-update-rollout-v1`, `controller-epoch-v1`.

### Decisions

| # | Decision | Rejected, and why |
|---|---|---|
| D1 | **Core's own Go engine** (`internal/nexupdate`, rewritten) fetches, authenticates, resumes, writes slots and arms boot entries, using systemd-sysupdate's *naming* (GPT labels, `+tries` UKI names) so standard tools still read the layout. | **systemd-sysupdate as engine.** Trixie has systemd 257.13 ([man page](https://manpages.debian.org/testing/systemd-container/systemd-sysupdate.8.en.html)); it verifies only a GPG-signed `SHA256SUMS` ([sysupdate.d(5)](https://manpages.debian.org/testing/systemd/sysupdate.d.5.en.html)): no expiry, anti-rollback counter or multi-component manifest; `acquire` (download without install) is "Added in version 260"; a partial download is flushed, not resumed. **apt:** not atomic; maintainer scripts and `installRecommended` mutate a live host (`internal/nexupdate/nexupdate.go:545-560`, `packaging/core-local/postinst`). |
| D2 | **New Ed25519 hierarchy**: offline root keys → delegated release key; wire form copied from the appliance manifest (raw signature over exact bytes, raw 32-byte key: `appliance/build.py:65-69`, `internal/vmhost/engine.go:380`). Separate from the UKI/Secure Boot, PCR, ZFS-module and apt keys. | **apt GPG key:** needs an OpenPGP dependency in Go and lives online on the publishing host (`packaging/apt/README.md`). **Appliance key:** the release job generates a throwaway key when the secret is unset (`.github/workflows/release.yml:110-125`). |
| D3 | **A signed, global, monotonic integer `sequence` orders releases; the label is display text.** Replaces runtime `dpkg --compare-versions` (`nexupdate.go:255-278`, BUG-0416), the publisher's `debian_sort_key` re-implementation (`scripts/release-publish.py:93-104`) and its lexical fallback (`scripts/apt-publish.py:384-397`). The publisher asserts that `sequence` order equals `dpkg --compare-versions` order of labels, keeping BUG-0416's one-engine rule. A rebuilt label (the workflow replaces a version's release, `release.yml:2-5`) gets a new sequence. | Parsing Debian syntax in Go (BUG-0416 "Avoid"); a `dpkg` subprocess inside a security-relevant rollback check. |
| D4 | **Stage never changes the next boot; Activate is the only arming point.** | Staging an armed entry: any unplanned reboot of a pool host would boot the new version outside a rollout. |
| D5 | **Rollouts are a Core operation on the pool master**: members first, master last, one host at a time, never forced. | Controller-driven rolling (breaks AGENTS.md:40-41); per-host timers (ignore quorum); unattended activation (never). |
| D6 | **Expand/contract state migration with integer `state_format`** replaces the `storage-format-v2/v3` APT guard. | A package guard: boot-count fallback bypasses userspace. |
| D7 | **Controller appliance = read-only verity system image + small persistent data disk, swapped by the Core that hosts it.** | In-guest `apt` (`packaging/controller/apply-update`): mutable root, self-update. |
| D8 | Downloads, offline media and the master's peer cache feed **one** verification path. | A separate offline format. |

### Release artifacts and trust

```
<base>/trust.json                  root-signed keys: not_after, revoked, version
<base>/<channel>/channel.json      release-key-signed: channel_serial, sequence, manifest_sha256, expires, min_sequence
<base>/releases/<sequence>/manifest.json + manifest.sig     immutable
<base>/blobs/sha256-<hex>          content-addressed; HTTP Range required
```

`manifest.json`: `format, sequence, label, architecture, created, min_from_sequence`,
`compat{update_engine, state_format, min_state_format, api_epoch, min_controller_api_epoch, mixed_pool, controller_first}`,
`components[]{id, kind: slot-image|layer|vm-image|iso|source|compliance, sha256, bytes, url, required, on_demand, requires_base, verity_root_hash}`.
Layers (including the kernel-locked `nex-zfs`) activate with their base in one transaction. The publisher refuses to sign
without `source` and `compliance` components whose hashes match the hosted files ([LICENSING.md](../LICENSING.md)).
Channels: ~~`dev` (every `main` push), `testing`, `stable`~~ **superseded by the [release channels plan](2026-10-02-release-channels.md) and `AGENTS.md`: `main` is `stable`, `dev` is the development channel, and only `stable` and `dev` are accepted**; promotion re-signs only `channel.json`.

Host checks, in order: root keys pinned in the verity root at `/usr/share/nex/update/trust.json` (newer chains persist in
`/var/lib/nex/update/trust.json`); `channel.json` signature, `expires` (stable 14 days, re-signed by a scheduled workflow),
`channel_serial` not older than stored, `sequence ≥ rollback_floor`; manifest hash and signature; every blob SHA-256. Failure is a
refusal with a stable `code`; the host keeps running. The private-HTTP URL remap (`nexupdate.go:104-136`) is deleted: signatures
carry integrity, so an HTTP mirror is acceptable.

### Host pipeline (Core only; no Controller needed)

State: the image section's `/var/lib/nex/image/state.json`, `update` subtree (Core-owned).

| Phase | Work | After crash, power loss or Controller outage |
|---|---|---|
| `checking` | fetch and verify metadata | repeat |
| `downloading` | blobs to `/var/lib/nex/image/staging/<sequence>/<sha256>.part`, `Range`+`If-Range`; source = channel, else the pool master's peer cache | re-hash the `.part`, continue at its length; no Range support restarts the file; final hash must match |
| `writing` | label inactive slot `_empty`, write erofs+verity to it **on every disk**, re-read, compare root hash | active slot never touched; an `_empty` slot is rewritten |
| `staged` | nothing armed; UKI stays in staging | next boot = old version |
| `armed` | UKI → `.efi.new` → fsync → rename to `nex-core_<label>_<sequence>+3.efi` on each ESP; `systemctl reboot` | rename is atomic per ESP; a disk booting without the entry boots its old, self-consistent slot; ESP convergence reconciles |
| `booted` | `nex-boot-check.service` (image-caused failures only; no bad-mark or reboot while `/run/nex/maintenance-hold` exists) → `systemd-bless-boot` ([boot assessment](https://systemd.io/AUTOMATIC_BOOT_ASSESSMENT/)); failure marks `bad` and reboots | three failed tries → systemd-boot picks the previous UKI → `rolled_back`, `code: boot_failed` |
| `committed` | previous UKI and slot kept until the **next stage** | — |

`rollback` arms the retained previous UKI. Every mutation takes `request_id` (= operation ID): exact replay returns the original
operation (`200`); a changed body is `409 request_conflict`; a second active operation is `409 update_in_progress`. Results
are kept (last 20). On a pooled host, local `activate`/`rollback` evaluate the rollout gates below from the verified replica;
v1 has no override.

### Pool rollout coordinator

Durable record `pool-update.json` in the master's agent store beside `pool-peer.json` (`internal/agent/poolca.go:24`), replicated as
new authority-replica section `update_rollout` (`internal/protocol/pool.go:1139`): a promoted master finds it `paused`
(`master_changed`) and an operator decides `resume` or `abort`. Hosts' progress is their own `state.json`. A fresh Controller reads
both from inventory.

1. **Preflight** (also `dry_run:true`): members `ready`, fresh, advertising `pool-update-rollout-v1`, same channel; membership generation
   stable, no master change in flight (`poolAuthorityWritable`, `internal/agent/pool_maintenance.go:120`), no other rollout;
   `min_from_sequence` met everywhere (else `bridge_required: <sequence>`); `state_format` guard; free space; Controller
   `api_epoch ≥ min_controller_api_epoch` (`controller_too_old`/`controller_unknown`); `mixed_pool:false` forces pool-wide
   `running_vms:"shutdown"`.
2. **Stage everywhere in parallel** (no disruption). Members prefer the master's peer blob cache and verify against the signed manifest
   (the master is untrusted for integrity), so an air-gapped pool imports once.
3. **Controller first** (`controller_first`): the host owning the Controller VM swaps its appliance image before any activation, keeping
   "upgrade Controller before Core" ([API](../API.md#trust-boundaries-and-compatibility)). A Controller outside the pool is updated first
   with `scope:"controller"` on its own host.
4. **One host at a time**, members by running-guest count then host ID, master last:
   - **Gate**: no other host in maintenance or mid-step; remaining `ready` voters of the Corosync nodelist (witness included in
     two-host pools) keep a majority, since votes do not shrink when a host stops (`internal/agent/pool_cluster.go:258-260`), else
     `quorum_margin`; clustered-storage pools need ≥3 voters; no non-terminal local operation on the host (`host_busy`). Reserved: refuse
     while HA is enabled until an HA-maintenance capability exists (HA is "planned and unavailable", [guide](../guide/README.md)).
   - **Maintenance** through the existing `pool-maintenance` command, then wait for watchdog teardown (`poolWatchdogFeeding` false
     everywhere, `pool_cluster.go:291`). A three-host armed pool disarms entirely meanwhile and re-arms after the host returns
     ([guide](../guide/host-maintenance-mode.md)).
   - **Guests** by `running_vms`: `refuse` (default) needs zero running guests, waiting up to `step_timeout` (6 h) then pausing;
     `shutdown` uses the existing graceful `vm-power` path, waits for libvirt `shut off`, records `stopped_by_update`, never force-stops.
     `evacuate` is rejected `evacuation_unavailable`: live migration and evacuation do not exist yet
     ([nex-platform.md](2026-09-08-nex-platform.md#7-migration-and-host-maintenance)).
   - **Activate** (peer step, idempotent on `(rollout_id, step_id)`), then wait `boot_deadline` (30 min: three tries plus firmware time).
     An unreturned host pauses the rollout `needs_attention`; nothing restarts elsewhere and no guest is assumed stopped.
   - **Confirm**: host `committed`; member `ready`, storage/network reconcile checks healthy, watchdog re-armed where the pool wants it. Then
     maintenance off, then `stopped_by_update` guests restart through normal master start admission on the **same host** (one owner
     epoch, no duplicate writable execution), then `min_soak` (60 s).
5. **Finalize** after every host commits: record `finalized_sequence`; only now may that release run irreversible state migrations.
   Any `rolled_back` or unconfirmed host pauses instead.

Controls: `pause` (after the current step), `resume`, `abort` (committed hosts stay), `rollback_host`. Controller loss or replacement changes
nothing. While the master reboots, coordination pauses and guest starts needing master admission wait; running guests are unaffected.

### Compatibility guards (replace `storage-format-v2/v3`)

- Delete `packaging/core-local/storage-downgrade-guard.py`, its APT hook (`packaging/core-local/postinst:22-25`), the marker files
  (`packaging/core-local/build-inside.sh:34-35`, `packaging/controller/build-inside.sh:28-29`), `prerm --check` and the marker paragraph of
  [ZFS provisioning](../API.md#zfs-provisioning).
- Port `core_storage_format` (`storage-downgrade-guard.py:71-94`: any ZFS record ⇒ 3, managed VM records ⇒ 2, unreadable ⇒ present) to Go
  `RequiredStateFormat`, run at stage, activate and rollback: `target.state_format < required ⇒ 409 state_format_downgrade`.
- **Expand/contract**: a new image reads old formats and writes only formats its predecessor reads until `finalized_sequence ≥ its
  sequence`, so automatic boot-count fallback is always safe. `nex-state-guard.service` (Before `libvirtd`/`nex-core-local`) boots the
  recovery profile when committed `state_format` exceeds the image's. CI fails any migration that is irreversible before finalize.
- `api_epoch` (strict report decoding, [API](../API.md#trust-boundaries-and-compatibility)) makes "Controller first" an enforced check;
  pool hosts may differ by one epoch unless `mixed_pool:false`.

### Controller appliance

A *controller profile* of the same image tooling: guest UEFI, read-only erofs+verity `system-<sha256>.img`, and persistent
`controller-data.qcow2` at `/var` holding only the Controller-scoped state recovery already enumerates (`/var/lib/nex-controller`,
`/etc/nex-controller`; `appliance/recovery.py:17`): no new Controller-owned data. Core's `vmhost` engine runs a journaled, resumable swap
under one operation ID, keeping the VM UUID and MAC:

1. Verify the component (its digest is in the release manifest; the separate appliance signature check, `engine.go:369-391`, is retired) and
   stage it under `/var/lib/nex/payload/<sha256>/`.
2. Graceful guest-agent shutdown (never force); `cp --reflink=auto controller-data.qcow2 …pre-<sequence>`.
3. Redefine the domain with the new system disk, start, probe the readiness path deployment already uses (`appliance/README.md:54-56`) for
   `controller_deadline` (10 min).
4. Success commits and keeps the old image until the next update. Failure restores the data checkpoint, redefines the old image, restarts
   and probes; if that fails too the VM stays stopped `needs_attention` (host-recovery export still applies).

Removed: `packaging/controller/{apply-update,nex-controller-update.path,nex-controller-update.service,postinst,prerm}` and
`appendUpdateBootstrap` (`engine.go:962-998`). The Controller ships only as a release component; a standalone `.deb` is not an update target.

### Offline and on-demand

`scripts/update-bundle.py` writes the online layout (metadata, `SHA256SUMS`, blobs for `--profile minimal|full`) to media labelled
`NEXUPDATE` or `/var/lib/nex/image/inbox/`. `GET /api/local/v1/updates/media` lists candidates read-only; `POST …/updates/import` verifies and
stages through the identical pipeline. A bundle's `channel.json` has its own `expires` (default 180 days); a clock earlier than `created` is
refused. Non-slot items (Controller image, Windows driver ISO) are `on_demand` components fetched to `/var/lib/nex/payload/<sha256>/` by
`POST …/updates/components/{id}/fetch`, extending the netinst model (`installer/netinst-manifest.py:51`); the offline ISO still bundles them.

**Implementation status, 2026-10-02 (Phase I-3d; unit-tested in simulation only).** The settings, media, import and fetch routes exist
on the host and are relayed by the Controller through the existing `POST /api/v1/hosts/{id}/updates/{action}` (`settings`, `media`,
`import`, `fetch`; host-scoped, relay only, nothing stored by the Controller). API impact: **new contract**
([docs/API.md](../API.md#image-edition-layers-settings-offline-media-and-on-demand-payloads-phase-i-3d)); state owner `core`
(`/etc/nex/updates.conf`, `/var/lib/nex/image/state.json`, `/var/lib/nex/layers/`, `/var/lib/nex/payload/`; a fresh Controller
recovers it from `Inventory.image`). Decisions applied: layers are staged with the base before the slot is written and recorded in
`enabled.json` only at activate (rollback switches them back); a layer's `requires_base` must equal the release's
`<label>-<sequence>` (`layer_base_mismatch`); an `on_demand` layer follows its base only where the operator enabled it, and
`on_demand` payloads carry a `name` and are placed at `/var/lib/nex/payload/<sha256>/<name>`; `channel_serial` is kept per update server
and channel (a bundle's channels each have their own, nothing is reset by a settings change; the rollback floor spans them); a host clock earlier than the manifest's
`created` is refused for every source. The `slot-image` stays a zstd tar, one root signature, rollback until the next stage; releases
are local, nothing is published remotely.

### Update API

Auth unchanged: host-local root PAM session + CSRF; Controller administrator session + CSRF; peer routes pinned mTLS from the master.
Refusals carry `code` plus one literal message, checked by contract tests (`tests/test_api_contract.py:544,727`). Async results are
`UpdateOperation{id, kind, phase, progress{component,bytes_done,bytes_total}, sequence, error{code,message}, started_at, finished_at}`.
`…` below abbreviates the row's own prefix.

| Route | Status | Request → result |
|---|---|---|
| `GET /api/local/v1/updates` | changed | `UpdateStatus{running, slots, channel, available{label,sequence,components,compat}, phase, rollback_floor, state_format}`; no side effects |
| `POST /api/local/v1/updates/check` | new | refresh metadata; `429` within 60 s; `503` source unreachable |
| `POST /api/local/v1/updates/apply` | changed | `{request_id, sequence?, scope:"host"\|"controller", activate:"stage_only"\|"reboot", running_vms}` → `202`; `controller` only on the host owning the Controller VM |
| `POST /api/local/v1/updates/{activate,rollback,cancel}` | new | `{request_id, running_vms?}`; `cancel` is refused once armed (`already_armed`) |
| `PUT /api/local/v1/updates/settings` | new | `{channel, base_url}`, host-scoped, reported in `Inventory.image` |
| `GET /api/local/v1/updates/media`, `POST …/import`, `POST …/components/{id}/fetch`, `GET …/operations/{id}` | new | as above |
| `GET /api/local/v1/pool/updates`, `POST …/pool/updates/rollouts`, `POST …/rollouts/{id}/control`, `GET …/rollouts/{id}` | new, master only | `{request_id, sequence, running_vms, dry_run?}`; `{request_id, action: pause\|resume\|abort\|rollback_host, host_id?}` |
| `GET /api/v1/pools/{id}/updates`, `POST …/updates/check`, `POST …/updates/rollouts`, `POST …/rollouts/{id}/control` | new Controller | relay `pool-update` (`202`); GET projects inventory |
| `POST /api/v1/pool/updates/step`, `GET /api/v1/pool/updates/status`, `GET /api/v1/pool/updates/blobs/{sha256}` | new peer | step actions `stage,drain,activate,confirm,undrain,rollback` |
| `GET /api/v1/updates`, `POST /api/v1/updates/apply` | **removed** | — |

Compatibility: Controller answers rollout routes `409` ("upgrade Nex Core") unless the master and every member advertise
`pool-update-rollout-v1` (the existing older-Core pattern, [API](../API.md#pool-cpu-baseline-and-explicit-vm-features)). Core withholds
`Inventory.image` and `Inventory.managed.update_rollout` until the Controller acknowledges `image-update-v1`, and the Controller sends
`ReportResponse.controller{api_epoch,sequence}` only after Core advertises `controller-epoch-v1` (the `detected-os-v1` pattern), so strict
older decoders never see new fields. Existing APT hosts have no in-place path (question 1).

### Work items

1. **Engine**: rewrite `internal/nexupdate` (metadata, trust, resume, slot writer, arm/rollback, `RequiredStateFormat`); delete apt code, `ControllerApplyReady`, progress files.
2. **Host API**: `internal/localcore/updates.go`, `service.go:217-218`; `Inventory.image` in `internal/protocol/protocol.go`.
3. **Coordinator**: `internal/agent/pool_update.go` (+ fake-peer test), `PoolCommand` actions (`internal/protocol/pool.go:1378`), peer handlers, replica section, `ReportResponse.controller`.
4. **Controller**: delete `internal/controller/updates*.go` and `controller.go:380-381`; add pool routes; delete `web/src/updates.ts`, `UpdatesPanel` (`web/src/settings.tsx:71`), `web/src/updates.test.mjs`; add an inventory-fed rollout view.
5. **Appliance swap**: `internal/vmhost` swap operation, two-disk deployment, controller profile; `appliance/recovery.py` and `host-recovery.py` for the data disk.
6. **Publishing**: new `scripts/update-publish.py` and `scripts/update-bundle.py`; delete `scripts/apt-publish.py`, `packaging/apt/` and the APT parts of `scripts/release-publish.py`; edit both `release.yml` copies (drop `nex-apt-publish`, Controller `.deb` build/smoke, APT reachability; add sign and publish to `dev`); add an `update-promote.yml` pair with manual approval; secret `NEX_UPDATE_RELEASE_KEY`, never in the repository.
7. **Tests**: delete `tests/test_apt_publish.py`, `tests/test_apt_bootstrap_privacy.py` and the guard cases in `tests/test_storage_packaging.py`; add `tests/test_update_publish.py` (sequence order equals `dpkg --compare-versions`, replacing the BUG-0416 regression); extend `tests/test_api_contract.py`; add a nested-KVM integration under `tests/integration/`.

### Risks

Lost or leaked root/release key (two root keys, `not_after`, rotation through signed `trust.json`, reinstall path); firmware POST longer than `boot_deadline`; an irreversible migration slipping past finalize; `shutdown` is real guest downtime until migration exists; the master is down during its own reboot; N hosts pulling multi-GB images (peer cache); clock skew against `expires`. A metadata-server outage blocks updates only.

### Acceptance checks (disposable labs only)

1. Single host: kill power during download, write and arm; each resumes or boots the old version. A fault-injected `nex-boot-check` failure returns to the old version with `boot_failed`; `rollback` works; a lower `state_format` target is refused.
2. Tampered blob, bad signature, expired `channel.json`, replayed older pointer and `sequence < rollback_floor` each fail with the documented `code` while the host keeps running.
3. Three-host nested pool, fencing armed, one running guest: `refuse` pauses naming the guest; `shutdown` completes and restarts it on the same host; members then master; never two hosts in maintenance; no watchdog reset; one writable owner throughout.
4. Kill then replace the Controller mid-rollout: the rollout completes and the new Controller reads it from the master. Kill the master during its step: `paused/master_changed`.
5. Controller swap succeeds; an injected readiness failure restores data and the old image; host-recovery export still works.
6. Offline import from `NEXUPDATE` media on a host with no route; peer-cache distribution to two members.
7. `python3 scripts/generate_api.py --check`, `python3 tests/test_api_contract.py`, `go test -race ./internal/nexupdate ./internal/agent ./internal/localcore ./internal/controller`, `python3 scripts/check_docs.py --worktree`, `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`.

### Documentation and state

- Guide: rewrite [software-updates.md](../guide/software-updates.md) and [publish-a-release.md](../guide/publish-a-release.md); add `update-a-pool.md` (rollout, `running_vms`, pause/resume/rollback) and offline steps; adjust [host-maintenance-mode.md](../guide/host-maintenance-mode.md), [deploy-controller-appliance.md](../guide/deploy-controller-appliance.md), [recover-without-controller.md](../guide/recover-without-controller.md) and the README index. A page claims verification only after a lab walkthrough.
- API: rewrite the "Software updates" section and the ZFS marker paragraph of `docs/API.md`; add a pool-rollout section; update `docs/api/openapi.json`, `local-openapi.json`, `peer-openapi.json`; regenerate HTML.
- `docs/DOCUMENTATION.md`: no rule change. New `docs/changes/` record: `guide.impact: updated`, `api.impact: changed contract`, `roadmap.impact: updated` (Phase 8, "Rolling updates" row), **`state.owner: core`**, `state.record`: per-host `/var/lib/nex/image/state.json` and the master's `pool-update.json` (replica section `update_rollout`); fresh-Controller path: `Inventory.image` and `Inventory.managed.update_rollout`. The Controller keeps no update data.
- `CHANGELOG.md` entry; `docs/BUG_LEDGER.md` only if a bug is fixed (BUG-0416's regression moves to `test_update_publish.py`, noted in the change record); `installer/README.md` and `docs/LICENSING.md` belong to the installer and composition sections.

### Open questions for the project owner

1. **Existing APT dev hosts:** reinstall and re-adopt (recommended; Nex is unreleased) or build an in-place conversion?
2. **v1 guest policy:** ship `shutdown` (downtime) beside `refuse`, or `refuse` only until migration lands?
3. **Key custody:** root threshold (1-of-2 vs 2-of-3), offline storage, and whether the release key may live in CI.
4. **Channels:** names, promotion approval, and whether hosts may auto-check and auto-stage (never auto-activate).
5. **Break-glass:** may a pooled host activate locally when its master is unreachable?
6. **Master update:** keep "last", or perform a planned `master-change` first?
7. **Standalone Controller `.deb`:** retire (assumed) or keep as an unsupported form factor?
8. **Retention:** how long must bridge releases (`min_from_sequence`) and old blobs stay hosted? Netinst pruning already shows the risk ([net-installer.md](2026-09-24-net-installer.md), "Release availability limit").

## Image composition, optional feature layers, package trim and build reproducibility

Scope: what is in the verified base, what is a separate signed layer or on-demand artifact, how
each is built reproducibly from Debian, and how today's install-time mutations disappear. Assumes
the image-architecture section's mkosi-built, dm-verity-protected, A/B base; the updates section
supplies the signed release manifest. Directory name `image/` is assumed; reconcile with that
section. **Decision (project owner, to record):** the immutable layer is built *on* Debian
(snapshot-pinned packages); AGENTS.md "keep the Debian base intact" is preserved.

### Decisions

**D1. Optional features = signed systemd-sysext layers, not image variants or runtime apt.**
Recommended: `nex-<feature>.raw` (erofs) sysext images, built in the same pipeline as the base,
bound to exactly one base build, stored on persistent state, merged by systemd at boot.
Rejected: (a) *separate image variants* (full/minimal): doubles the A/B/rollback matrix and still
cannot change without reinstall; (b) *runtime `apt-get`* (today: `internal/nexupdate/nexupdate.go:284`,
`installRecommended` `:531-559`): needs a writable root and live Debian reachability; (c) *portable
services/containers*: ZFS needs host kernel modules and cluster daemons need host integration;
(d) *confext-only*: sysext only merges `/usr`/`/opt`; `/etc` payloads of a sysext are ignored
([systemd-sysext(8)](https://www.freedesktop.org/software/systemd/man/latest/systemd-sysext.html)),
so layer `/etc` defaults are relocated at build (below) and confext is not needed in v1.

**D2. Enablement: built and signed at image build; stored at install (offline ISO carries all,
netinst fetches only what is selected); activated at runtime by Core, never by a package
manager.** Layers are *off* until a Core operation needs them (create a ZFS repository, join/form a
cluster-storage or watchdog pool). Core "ensures" the layer as the first step of that existing
operation, idempotently, no Controller involvement. First enable merges live (no reboot, no
maintenance mode); a layer *version* change always ships with a base update and activates at the
reboot; disable is refused while in use and takes effect at next boot. Layer-only live refresh is not
supported (sysext `refresh` briefly unmounts the overlay, per the same man page).

**D3. ZFS: prebuilt per-kernel modules inside `nex-zfs`, replacing `zfs-dkms` and headers.**
Rejected: keep DKMS on host (needs a writable root, compiler, headers on every host; DKMS failure
aborts install, `docs/plans/2026-09-19-zfs-repository-provisioning.md:130`); build on host from a
toolchain layer (hundreds of MB [INFERENCE], per-host signing). **Blocked on a licensing decision:**
`docs/LICENSING.md:217-225` records that no prebuilt ZFS module is redistributed; a prebuilt CDDL
module is a new redistribution posture (Debian ships only DKMS source: [Debian ZFS wiki](https://wiki.debian.org/ZFS)).
Fallback if the owner/counsel refuse: host-local build layer (Option B) — documented, not planned.

**D4. Controller appliance and VirtIO ISO are content-addressed artifacts outside the A/B root.**
They are ~1.6 GB (750,452,736 B appliance, `docs/qa/2026-09-18-iso-packaging-efficiency.md`; 877,373,440 B ISO,
`internal/vmhost/bundled_media.go:24-31`); in an A/B root every byte exists twice.

**D5. Reproducible build = pinned-input assembly, not "mkosi against a mirror".** mkosi 25.3 (Debian
trixie) documents `SourceDateEpoch=`, `Seed=`, `Overlay=`/`Format=sysext`, `LocalMirror=`,
`PackageDirectories=`, `CacheOnly=always`, and no `Snapshot=` setting
([mkosi(1) trixie](https://manpages.debian.org/trixie/mkosi/mkosi.1.en.html)). So Nex resolves against
`snapshot.debian.org` once, writes a hash lock, downloads exactly those debs, and builds offline from them.
Rejected: debootstrap scripts (reimplements mkosi sub-images/sysext/verity); live mirror.

### Composition

| Component | Content | Form |
|---|---|---|
| **base** (verity `/usr` + UKI, always) | qemu, libvirt (+nex), ovmf, python3, python3-virt-firmware, openssh-server, chrony, mdadm, ifupdown, bridge-utils, vlan, dhcpcd-base, nftables, genisoimage, dmidecode, pciutils, ca-certificates, **nfs-common, libnfs-utils, open-iscsi** (NFS is the first shared storage, AGENTS.md:149; iSCSI is used by `cluster-iscsi-session`), **swtpm\*, ipmitool** (small; probed on every host, `bmc_*.go`), nex-core-local | image |
| **`nex-zfs`** | zfsutils-linux, libzfs\*/libnvpair/libuutil, zed, prebuilt `zfs.ko`/`spl.ko` for the base kernel | sysext |
| **`nex-cluster`** | corosync, corosync-qdevice, pacemaker(+cli-utils), pcs, resource-agents, fence-agents-{redfish,ipmilan,virsh}, sbd, dlm-controld `+nex1`, gfs2-utils, sanlock, python3-sanlock, libvirt-daemon-plugin-sanlock | sysext |
| `nex-smb` (only if measured ≥ threshold) | cifs-utils, smbclient | sysext, else base |
| **Controller appliance** | `controller.qcow2`, `manifest.json`, `manifest.sig` | artifact |
| **VirtIO Windows ISO** | Fedora virtio-win 0.1.302, unmodified | artifact |

Rule: layer only if ≥ ~25 MB installed, or a separate licence posture (ZFS), or a default-off service
stack with its own attack surface (cluster). Everything smaller folds into base so there is no
extension machinery for 5 MB. Thresholds are set from step 0 measurements, not from this table.

### Layer format and lifecycle

- Image name `nex-zfs.raw`; `/usr/lib/extension-release.d/extension-release.nex-zfs` has `ID=nex-core`,
  `SYSEXT_LEVEL=<base BUILD_ID>`; base `os-release` carries the same `BUILD_ID`/`SYSEXT_LEVEL`.
  Never `VERSION_ID` (systemd only checks `SYSEXT_LEVEL` first, per man page). Every release
  rebuilds all layers against the new base.
- Each layer carries `/usr/lib/nex/layers/<name>.json`: `{name, requires_base, units_enable[],
  units_never_enable[], provides[]}`. Core reads it to run `systemctl preset`/start and to report capability.
- Persistent state (host-local, owned by that host's Core): store
  `/var/lib/nex/layers/store/sha256-<hex>.raw`; record `/var/lib/nex/layers/enabled.json`. Early
  `nex-layers.service` (`After=local-fs.target`, `Before=systemd-sysext.service`) re-hashes each enabled store file
  against the manifest digest, verifies the signed release manifest (same ed25519 scheme as the
  appliance, `internal/vmhost/engine.go:371-381`), and symlinks `/run/extensions/<name>.raw`; stock
  `systemd-sysext` merges. A failed check leaves the layer unmerged, reports it, and never blocks boot.
  Old versions are kept until the new slot is confirmed good (rollback safe).
- Layers merge in the main boot only (the man page: `/var`-resident extensions cannot merge in the
  initrd). Nothing in a layer may order before `local-fs.target`/`sysinit.target`; stock ZFS mount
  units do [INFERENCE], so the layer ships reset drop-ins. `layer-finalize` (item 6) asserts this.
- Build-time normalization of Debian maintainer-script side effects the sysext will not carry:
  `/etc/*` payload → `usr/share/factory/etc/*` + tmpfiles `C` rules; users/groups added by postinsts →
  `sysusers.d` with the UIDs from the layer chroot diff (built on top of the exact base tree with
  `BaseTrees=`+`Overlay=yes`, so no collision); `update-alternatives`/ld cache not relied on. Any
  unclassified `/etc` or `/var` file fails the layer build.

### ZFS module and Secure Boot

Build `nex-zfs` in the pinned builder from Debian source `zfs-linux` (the `zfs-dkms` source, 2.3.9-0+deb13u1
per `docs/LICENSING.md:227`) against the locked `linux-headers` of the base kernel; headers and toolchain
exist only in the build container. Sign modules with a persistent Nex module key held in CI secrets.
With Secure Boot, Linux lockdown refuses unsigned out-of-tree modules; Debian's documented path is a
Machine Owner Key ([Debian SecureBoot wiki](https://wiki.debian.org/SecureBoot)). Plan: installer enrolls
the Nex module certificate via `mokutil --import` only when firmware Secure Boot is on, with a console MOK
step; with Secure Boot off nothing happens. Host Secure Boot is unverified today (`docs/ACTIVITY_LOG.md:86-87`),
so this is a lab gate, not a claim. Obligations: exact `zfs-linux` source, build recipe, kernel source,
notices in the source bundle and compliance manifest.

### Mutations → build-time image steps or Core first boot

| Today | Becomes |
|---|---|
| `dpkg-divert` + write `/etc/os-release` (`postinst:79-92`), motd/issue sed (`:94-96`, `late-install.sh:189-195`) | Image build writes `/usr/lib/os-release` (`NAME="Nex Core"`, `ID=nex-core`, `IMAGE_VERSION`, `BUILD_ID`, `SYSEXT_LEVEL`) and banners. No divert. |
| `sed` contrib into `debian.sources` (`postinst:38-49`), writing `debian.sources`, cdrom sed (`late-install.sh:202-218`) | Deleted: no apt on the host. Contrib is a build-container setting. |
| `update-grub` + `grub.d/nex-core.cfg` (`postinst:98-111`, `late-install.sh:180-186`) | `systemd.ssh_auto=no` in the signed UKI cmdline; title from os-release. |
| AppArmor local include + `apparmor_parser` (`postinst:117-142`, helper takes `root` already) | Run `libvirtd-apparmor-policy $ROOT install` in the image build; profile reloaded by normal AppArmor boot. Per-VM profiles libvirt writes under `/etc/apparmor.d/libvirt` need persistent `/etc` — flag to image-architecture section. |
| unit disables (`postinst:54-75`, `late-install.sh:101,105`), `systemctl enable` (`:197`), `package-service-policy.sh guard/disable/runtime-qnetd` | `system-preset` files (enable libvirtd, ssh, chrony, nex-core-local; disable cluster/sbd/zfs-import-scan); `policy-rc.d` only in build chroot. |
| `corosync-pristine` (`late-install.sh:97`) | Deleted: layer's corosync sample lives in `usr/share/factory`; Core writes `/etc/corosync` on first cluster setup. |
| apt hook `91nex-core-storage-downgrade`, `storage-downgrade-guard` | Becomes an update-engine pre-activation check (updates section); the format-v2/v3 stamps (`build-inside.sh:34-35`) stay. |
| `nex-sync-esp` + `DPkg::Post-Invoke` + ESP fstab `nofail` edits (`late-install.sh:114-174`) | Logic moves into Core's update transaction / image-architecture ESP-per-disk handling (same remount/rewrite behaviour must be preserved; owned there). This section only removes the script. |
| `mdadm.conf` root-array record (`:112`) | Per-host input; owned by image-architecture (initrd assembly). Requirement: `mdadm` in initrd and base. |
| `/etc/nex/core-release` stamp + `preinst` guard | Deleted; `ID=nex-core` is the identity. `preinst`, `postinst`, `prerm` removed. |
| `install -d 0711 /var/lib/libvirt/images` (`:199`) | tmpfiles `d`. |

### Artifacts: bundled vs netinst (verified)

Offline ISO keeps bundling both (`build.sh:233-327`, `:534`; hard requirement). The installer places them in
`/var/lib/nex/payload/<sha256>/` (same filesystem choice as today's hard-link seed, `bundled_media.go`).
Netinst today: removes all repo debs and `media/virtio-win.iso.gz` (`build.sh:735`), adds the Fedora VirtIO
row (`netinst-manifest.py:51`) fetched by `netinst-fetch.sh` (`late-install.sh:29-50`); `nex-core-local`, **including the
750 MB appliance inside the deb**, is a "release payload" downloaded from Nex hosting
(`netinst-manifest.py:18-26`, `build.sh:733`). Still on the netinst ISO: Debian installer tree, Nex installer
scripts, `netinst.tsv`, notices, `nex-source.tar.gz`, package manifest. Change: the appliance leaves the deb and
becomes `controller-appliance-<sha256>.qcow2` plus signed `manifest.json/.sig` rows in `netinst.tsv`, fetched at
install like VirtIO and marked `on-demand` in the release manifest so Core can re-fetch or upgrade it later
(`appliance-fetch` Core operation, signature and digest verified by existing `verifyManifest`/`verifyImage`,
`engine.go:370-430`). The trust anchor is the updates section's `trust.json`, not `release.pub`; `nex-source.tar.gz`
(AGPL source offer) and notices stay in the verity base; the Controller update helpers are removed (updates section).
`core-local-setup` `BUNDLE` (`:14`, `:137-138`) points at the payload path.

### Reproducible build

1. `scripts/image-lock.py` resolves `base.packages` + `layers/*.packages` against the pinned Debian and
   security snapshots (as `build.sh:519-533` does), writes `image/packages.lock` (package, version, arch,
   sha256, source, origin, component). Fails on the denylist (the six firmware packages of `build.sh:466-468`,
   replacing prune-time removal), duplicate libvirt slots (port of `replace_matched_libvirt`, `build.sh:110-191`),
   and a `dlm-controld` whose `libstonithd56 (= 3.0.0-2)` does not match the locked pacemaker (`:538-545`).
2. Nex-built debs (libvirt `+nex4`, dlm `+nex1`, nex-core-local, ZFS modules) are built first in the
   pinned builder and entered into the lock by sha256 as `origin: nex`.
3. mkosi builds base and layers offline from the lock with `SourceDateEpoch`, fixed `Seed`, `CacheOnly=always`.
4. SBOM: SPDX JSON per component from the lock + Go `vendor/modules.txt` + npm lock, merged into the existing
   compliance manifest (`build.sh:719-722`). Source bundle (`scripts/build-source-bundle.py`) consumes the lock's flat repo.
Gaps found: `FROM debian:13` unpinned with live apt (`packaging/cluster-storage/Dockerfile:4-11`,
`packaging/core-local/Dockerfile:2-3`); `date -u` stamps (`build-matched-libvirt.sh:18`, `build-matched-dlm.sh:14`,
`build.sh:567`); `dch` stamps the current date; unclamped `.deb` mtimes. The appliance qcow2 (`appliance/build-inside.sh`
uses virt-customize) is a pinned *input*; its bit-reproducibility is explicitly not claimed.

### Package trim (verified from code)

- **Drop `virtinst`**: no caller; only `installer/README.md:236`, a comment `firmware.go:81`, test pin `iso-smoke.py:88`.
- **Drop `sudo`**: no host caller; `appliance/firstboot.py:41` and `docs/guide/deploy-controller-appliance.md:192` are the Controller VM.
- **Drop `corosync-qnetd` from hosts**: qnetd is Controller-hosted (`docs/guide/pool-authority-and-recovery.md:36,71`,
  `internal/controller/witness.go:57`, `appliance/build-inside.sh:23`); hosts run only `corosync-qdevice`.
- **Keep `nftables`** (corrects the earlier audit): `libvirt-daemon-driver-network` Depends `iptables | nftables`
  ([debian/control](https://sources.debian.org/data/main/libv/libvirt/11.3.0-3+deb13u3/debian/control), l.640-642); dropping it makes apt pick `iptables`, no saving.
- No `Depends→Recommends` demotion. On the apt model that would weaken `build-inside.sh:103`; at cutover the
  cluster/ZFS entries are *deleted* from `nex-core-local` Depends/Recommends (`:103-104`) because layers own them.
- Test pins to rewrite: `tests/test_storage_packaging.py:21-27,43,296-316,428-441`, `tests/integration/iso-smoke.py:88-98`.

### Work items

0. Measure first: `scripts/measure-footprint.sh` on a disposable lab host: `dpkg-query -W -f='${Installed-Size}\t${Package}\n'`
   rolled up per group (base/zfs/cluster/smb/dev-tools), plus `du -x` of `/usr/lib/modules`, `/usr/src`, `/var/lib/dkms`,
   `/usr/share/nex`. Record in `docs/qa/<date>-image-footprint.md`. Expected savings [INFERENCE until measured]:
   headers+DKMS toolchain+`/usr/src/zfs-*` (order of 100s of MB), virtinst closure (tens of MB), 1.6 GB of
   artifacts removed from each root slot (measured sizes above).
1. Phase 1 on the current apt model (shippable alone): drop virtinst/sudo/corosync-qnetd from `installer/packages.txt`,
   update pins and `installer/README.md:236` (guest creation through the Core API).
2. Create `image/{base.packages,layers/*.packages,denylist.txt,mkosi.conf,mkosi.images/*}`, `scripts/image-lock.py`, `image/packages.lock`.
3. Patch the recipes: `packaging/libvirt/build-matched-libvirt.sh`, `packaging/cluster-storage/{Dockerfile,build-matched-dlm.sh}`,
   `packaging/core-local/{Dockerfile,build-inside.sh}` (digest-pinned base, snapshot sources, `SOURCE_DATE_EPOCH`, deterministic changelog).
4. `build-inside.sh`: drop appliance copy (`:74-76,83` except `release.pub`), Depends/Recommends (`:103-104`); delete
   `packaging/core-local/{preinst,postinst,prerm,corosync-pristine}`; delete `nexupdate.go:531-559` (updates section owns the rest).
5. `image/mkosi.images/nex-zfs`, `scripts/build-zfs-modules.sh`, module-signing step.
6. `scripts/layer-finalize.py` (etc→factory, sysusers, unit-order check, extension-release, layer json, allowlist check).
7. Core: `internal/vmhost/layers.go` (inventory, ensure/disable, journalled), `nex-core-local layers activate`, `packaging/core-local/nex-layers.service`.
8. Artifacts: `bundled_media.go` path, `engine.go:963`, `core-local-setup`, `installer/netinst-manifest.py`, `netinst-fetch.sh:9,27`, `late-install.sh:27-60`, `build.sh:735-744`.
9. SBOM + `scripts/build-source-bundle.py` + `docs/LICENSING.md` (layers, ZFS module posture, mkosi/tools).
10. `tests/test_image_composition.py`: lock verifies against debs; layer package sets disjoint from base; no unclassified layer files; denylist enforced.

### Risks

ZFS licensing posture and Secure Boot MOK UX (headless hosts); layers rebuilt and re-downloaded every release;
sysext ordering (`zfs-mount`), UID/GID drift, hidden `/etc` writers; Debian snapshot availability (mirror the
locked debs in release storage); `nex-cluster` being the largest layer lengthens first pool join if downloaded on demand.

### Acceptance (all in a disposable nested-KVM lab; none run yet)

- Baseline: footprint table committed; Phase 1 closure shows `virtinst sudo corosync-qnetd` absent, `virsh net-start default` works.
- Reproducibility: two independent runners produce identical sha256 for the base root hash and every layer; `image-lock.py --check` clean; diffoscope output attached on any difference.
- Layer: `systemd-sysext status` empty on fresh host; enabling `nex-zfs` through Core → `modprobe zfs`, `zpool create`, survives reboot; same for `nex-cluster` (forms a two-host pool and GFS2) without a reboot.
- Tamper/mismatch: flipped byte in a store file, or a layer from another base build, is refused, reported, host and running VMs unaffected.
- Interruption: power-cut during layer download, between store write and `enabled.json` write, and during an A/B update that fails health check → retry converges; rollback boots old base with its old layer.
- Controller replacement: delete Controller, connect a new one; host inventory returns enabled layers and artifact store; Controller deploys from the stored appliance with no internet.
- Offline ISO installed with the network unplugged keeps VirtIO, appliance and all layers; netinst downloads VirtIO and appliance, `netinst.tsv` lists both, interrupted download resumes.
- Secure Boot lab: signed `zfs.ko` loads after MOK enrollment; an unsigned copy is refused.

### Documentation and impact

Update `docs/guide/install-nex-core.md` (variants, what is downloaded, artifact paths), `software-updates.md` (layers update with the base),
`storage-repositories.md` (ZFS needs the `nex-zfs` layer; failure text), `docs/guide/` new page "Optional host features" per
`docs/DOCUMENTATION.md` structure; `installer/README.md`, `docs/LICENSING.md`, `CHANGELOG.md`; a `docs/changes/*.json` record
(`state.owner: core`, record `/var/lib/nex/layers/enabled.json` on each host, re-reported through host inventory); `docs/DOCUMENTATION.md`
assessment per phase. BUG_LEDGER only for defects found. Guide impact: updated. Roadmap impact: none.

**API impact: new contract** — host layer inventory (read) plus ensure/disable operations and a `layer-unavailable` failure reason on existing
ZFS/cluster operations; plus an on-demand `appliance-fetch`. Core owns execution; Controller only asks and displays. Operation shapes
live in the updates/API section. Phase 1 trim: API impact none (no contract touched).

### Open questions for the project owner

1. Approve redistributing prebuilt, Nex-signed ZFS modules (changes `docs/LICENSING.md:217-225`), or require the host-build fallback?
2. Is Secure Boot for the host a supported claim in this release (it drives MOK enrollment and module signing)?
3. Should `sanlock` stay in the product while storage-lease fencing is "not an available operator workflow" (`pool-authority-and-recovery.md:78`)?
4. Confirm a Core host never serves as qnetd witness (dropping `corosync-qnetd` assumes it).
5. Should netinst fetch the appliance at install (recommended) or only when the operator first deploys a Controller?

## Installer, persistent host state, operations, verification, licensing, documentation and delivery phases

This section depends on two sibling sections and states what it needs from them: **Image architecture** fixes the per-disk GPT and the verity/boot-counting mechanism; **Updates** fixes the Core update/rollback API. Disk contract used here (from Image architecture, UEFI-only image edition): the same GPT on every selected disk — `nex-esp` (1 GiB FAT32), `nex-root-a` (≤4 GiB), `nex-verity-a` (64 MiB), `nex-root-b`, `nex-verity-b`, `nex-data` (rest; ext4 label `nex-data`, md RAID1 1.2 named `nex-data` when more than one disk) — no BIOS-boot partition, no swap, no partman or GRUB. Where this section names an operation, it is the interface this section requires, not a competing design. Phases are numbered **I-0…I-5** to avoid colliding with the product's Phases 0–10.

### D1 — Installer: keep Debian Installer as the host, replace its payload half

**Decision.** Keep Debian Installer (boot, hardware detection, firmware, `netcfg`, rescue) and the Nex GTK/text wizard unchanged. Replace everything from partitioning onward (partman recipes, `base-installer`, `apt-setup`, `grub-installer`, `late-install.sh`) with one Nex step, `nex-image-install`, that lays out the disks, writes the signed slot image and seeds persistent state.

*Rejected:* (a) let partman create the slots and keep d-i's debootstrap — writes a throw-away Debian base, and partman/RAID recipe quirks already caused BUG-0413 (`installer/gui/recheck.sh:23-25`); (b) replace d-i with a bespoke initramfs installer — re-implements NIC/firmware/disk detection and the pinned cdebconf frontend (`installer/README.md:59-60`) for no safety gain; (c) keep `apt install` in the installer and only mount root read-only — leaves the "installed system differs per host" problem unsolved (it is only a Phase I-1 stepping stone).

**Design.**
- Wizard → `/run/nex-installer/config.ini` → `nex-config --apply` is unchanged (`installer/gui/start.sh:32`, `installer/gui/nex-config.c:190-204` already stages hostname, hosts, network, keymap, timezone, root hash under `/run/nex-installer/target/etc`). `nex-config` stops emitting partman answers (`nex-config.c:206-267`) and emits `layout=single|raid1` plus the validated disk list; the ≥16 GiB, 10 % and explicit-erase-confirmation rules stay in `nex-config` as the authority.
- `installer/nex-image-install.sh` (POSIX sh, runs inside d-i) is a thin wrapper around the Go `internal/hostimage` tool, which is the same code Core's slot writer uses (one implementation, so install and update cannot diverge). It wipes only the selected disks (reusing `recheck.sh` RAID/LVM clearing), lays the GPT from repart definitions on every disk, builds the `nex-data` md RAID1 when two or more disks are selected, writes slot A plus its verity partition (slot B is laid out empty so Phase I-3 never re-partitions a live host), the ESP files on **every** ESP (replacing `grub-installer/bootdev` and the `nex-sync-esp` Post-Invoke hook, `late-install.sh:114-169`), and the installer-collected `/etc` (root hash, hostname, timezone, keymap, network) into `nex-data:/lib/nex/etc/upper` (D2).
- Payloads: `netinst-manifest.py` (`:51` already pins VirtIO) is generalised to `payloads.tsv`: `{signed image, Controller appliance qcow2, VirtIO ISO}` each with SHA-256 and signature. **Offline ISO** carries all three (VirtIO still gzip transport, appliance included); **netinst** downloads all three by hash through `netinst-fetch.sh` (pre-partition preflight kept). Appliance and VirtIO ISO land in `/var/lib/nex/payload/<sha256>/` on `nex-data`, not in the verity slot; a later Core operation (Updates section) refreshes them on demand. [INFERENCE: assumes Composition keeps the ~750 MB appliance out of the slot; otherwise each slot grows by that amount.]
- Rescue/serial entries (`installer/build.sh:647-650,691-692`) stay. The wizard gains **Repair image** next to **Erase and install**: it re-lays the GPT and slots and keeps `nex-data`, and is offered only when a complete `nex-data` partition or mirror is found. No factory-reset or secure-erase function exists for hosts today (only VM-disk wipe, `docs/guide/delete-virtual-machine.md:45`); "factory reset" = Erase and install. Local recovery: boot entries *current slot*, *previous slot*, *recovery (root login, verity still enforced)*, plus serial `ttyS0`. Because the image edition is UEFI-only, a host that boots in legacy BIOS mode is refused before any disk write.
- Installed-host identity: `/etc/nex/core-release` stamp (`late-install.sh:73`, enforced by `packaging/core-local/preinst`) moves into the image; `.deb` remains the build unit but is never run on the host.

**Work items.** I1 create `installer/nex-image-install.sh` (wrapper) and consume `internal/hostimage` (Image architecture lane); I2 change `installer/gui/nex-config.c` (emit `layout=single|raid1` and the repair mode instead of partman answers, `:206-267`) and `installer/gui/wizard-text.sh`/`.templates`, `nex-wizard.c` (mode choice); I3 reduce `installer/preseed.cfg` to root/locale answers and the `nex-image-install` hook; I4 delete `installer/late-install.sh` and `installer/packages.txt` consumption; I5 rewrite `installer/netinst-manifest.py`, `netinst-fetch.sh`, `installer/build.sh` (media assembly consumes the image artifact; prunes unused udebs — [INFERENCE] d-i menu items cannot be skipped by preseed, so Phase I-0 must prove udeb pruning), `installer/check.sh`; I6 update `installer/README.md`. Phase I-1 (read-only root and separate `/var` on the current d-i/GRUB path) changes only the `nex-config.c:206-257` recipe.

### D2 — Persistent host configuration

**Decision.** `/` is the verity-protected slot; `/etc` is an overlayfs whose lower layer is the slot's `/etc` and whose upper/work layers live on `nex-data` (`/lib/nex/etc/{upper,work}` on that filesystem, `/var/lib/nex/etc/…` once mounted), assembled in the initrd; `/var` is bound from `nex-data`. *Rejected:* a full persistent `/etc` copy (image defaults never reach hosts; unreviewable drift); relocating each file with tmpfiles symlinks (touches every Debian package's expectations). Rule from today's updater is kept: local edits win over changed image defaults (`nexupdate.go` keeps existing config on upgrade, `:280-284`); the update result lists shadowed files so a changed default is never silent.

| Data | Location after change | Writer |
|---|---|---|
| Hostname, hosts, timezone, keyboard, root hash (`/etc/shadow`) | `/etc` upper | installer, then operator/Core |
| Network: `interfaces`, `interfaces.d/`, `dhcpcd.conf`, `resolv.conf`, `.link` names | `/etc` upper | `internal/vmhost/management_network.go:423-432`, `vlan.go` (same-filesystem atomic rename works in the overlay upper) |
| corosync.conf, authkey, `/etc/default/sbd`, qdevice and cluster units under `/etc/systemd/system` | `/etc` upper | `storage_cluster.go`, `watchdog_arming.go`, `pool_witness.go` |
| libvirt domain XML (`/etc/libvirt/qemu`), AppArmor per-VM profiles | `/etc` upper | libvirt |
| TLS key/cert, Core agent state, pool journal, credentials, payloads | `/var/lib/nex*` | already state (`core-local-setup:72`, `nex-core-local.service` `StateDirectory`) |
| SSH host keys, `machine-id`, iSCSI initiator name | generated at first boot into `/etc` upper by `nex-firstboot.service`; build-time copies must not exist in the image | image build must delete them |
| mdadm.conf, per-host initramfs | none: generic `HOMEHOST <ignore>` config baked in the image; removes `late-install.sh:110-113,228-230` | image |

The ESP is not in fstab and `nex-sync-esp` is deleted: Core's ESP converger (image section) mounts each ESP transiently. Journald persists in `/var/log/journal`.

**Rollback safety rule.** The updates section's expand/contract rule applies: image N reads old state formats and writes only formats its predecessor reads until the pool-wide `finalized_sequence` reaches N, so an automatic boot-count fallback never meets state it cannot read. Running VMs and libvirt are never stopped by a refused commit.

**Replacement test.** Everything pool-scoped stays where it is today (pool master records); this change adds only host-local image/slot state, owned by Core. Checks: delete the Controller → fresh Controller adopts the pool with no re-entry; roll the host image back → network, corosync, sbd, SSH keys, root password and TLS identity are byte-identical.

**Work items.** P1 initrd overlay mount + `nex-firstboot.service` (image lane); P2 `packaging/core-local/postinst`: delete `dpkg-divert` os-release, `debian.sources` sed, `update-grub`, AppArmor rewrite (move to image build); P3 move `/usr/local` helpers; P4 `internal/nexupdate/nexupdate.go` runtime `apt-get` removed (Updates section).

### D3 — Operator ops on a read-only host

- Default: `/` and `/usr` read-only; `apt`/`dpkg` mutation fails with a pointer to this page. Curated diagnostics (`strace`, `tcpdump`, `ethtool`, `smartctl`, `ipmitool`, `iperf3`) are in the base image; extras ship as the optional signed *diagnostics* layer (Composition).
- **`nex-devmode`** (local root console or SSH, `root` policy unchanged — `00-nex-core.conf`, `late-install.sh:196`): mounts a **tmpfs-upper overlay over `/usr`** for a bounded time (default 4 h, vanishes on reboot), journal-logged; while active the host reports `developer_mode=true` in its observed state and is excluded from update commit and from Phase I-5 attestation. Persistent operator layers are *not* supported until the project owner decides key trust (O4). Containers on the host stay out of scope (AGENTS.md).
- **`nex-diagnostics`** (local CLI only, no API): writes `/var/lib/nex/diagnostics/nex-diag-<host>-<UTC>.tar.zst` with journal, slot/verity/boot-counter state, image manifest, `virsh list`, `corosync-quorumtool`, `mdadm --detail`; **excludes** `/etc/shadow`, SSH/TLS keys, corosync authkey, credential directories (AGENTS.md secret rule).

### D4 — CI and tests

**Existing checks that must change** (grep of `tests/`): `tests/integration/iso-smoke.py:88-101,151-152` and `fresh_install_host.py:254-255` (dpkg-query/ovmf version pin on the installed system → image manifest query), `tests/test_distribution_packaging.py:70,87,95` (installer stamp; updater "uses apt"), `test_iso_payload_efficiency.py`, `test_netinst.py`, `test_netinst_network.py`, `test_netinst_release.py`, `test_source_bundle.py`, `test_storage_packaging.py`, `installer/check.sh:291-293` (late-install assertions), `installer-disk.py`/`installer-handoff.py` (partman answers), `internal/nexupdate/nexupdate_test.go`, `internal/localcore/updates_test.go`. **Stay valid** (Debian guest + `.deb` as component unit): `core-local-package.sh`, `libvirt_recovery_ci.py`, `recovery_s0*`, `cluster_ha_lab.py`, `controller_replacement*`.

**New.** `tests/integration/immutable_lab.py` (pattern of `fresh_install_lab.py`: disposable lab directly under `.cache`, QMP, user-mode networking, TCG): (1) `/` mounted ro and every Core operation (network change, pool create, witness, storage, backup, md replace) passes; (2) A/B boot, forced bad slot → automatic fallback; (3) interrupted update killed at each stage (download, slot write, boot-entry flip, trial boot); (4) power loss via QMP reset under write-through disk cache — labelled *simulated*; (5) verity tamper: flip a slot byte offline, host refuses that slot and boots the other; (6) RAID1 member replacement with ESP/slot re-sync (supersedes `docs/qa/2026-09-22-system-disk-replace.md` layout); (7) three-VM pool rolling update with VMs running throughout, no dual writable VM; (8) Controller deleted before/during update, fresh Controller adopts. Unit tests (dependency-light, must pass the `github.yml` `tests/test_*.py` job): `tests/test_image_manifest.py` (binding, no throwaway key for release builds), `tests/test_boot_counting.py`, `tests/test_state_version_gate.py`, `tests/test_image_content.py` (no host keys, no machine-id, no apt lists in the built file listing). Reproducibility: build the image twice in the release job and compare SHA-256.

**Workflows.** Forge executes `.github/workflows/`; `.gitea/workflows/` copies stay in step, and every job in `.github` except `github.yml` carries `if: github.server_url != 'https://github.com'` (`AGENTS.md` CI rule). `installer.yml` cannot run ISO-level or privileged steps (its header, BUG-0043) — add only source-level checks there. Add `immutable.yml` modelled on `recovery-libvirt.yml:16-29` (forge-only, TCG, shared-runner concurrency group), mirror it in `.gitea`, and update `tests/test_ci_security.py`. If no runner qualifies, regression coverage is recorded **outstanding**, not skipped.

### D5 — Licensing and release evidence

- Today netinst fetches Debian packages from Debian (`LICENSING.md:87-91`); in the new model Nex hosts and redistributes every Debian binary in the image, for netinst **and** updates. Each published image therefore needs: `nex-core-<ver>-image-manifest.json` (image/verity-root hash, kernel, per-package version, source package, license, origin), the matching source bundle (`scripts/build-source-bundle.py` extended to refuse an image without it), notices in the image, and retention of the source for as long as the image is downloadable. Replace the netinst "fetched directly" wording and the pruning caveat (`docs/plans/2026-09-24-net-installer.md:94-99`).
- Signing: release builds fail closed without the image signing key (no throwaway-key fallback as the appliance build has); public key embedded in the signed boot chain.
- **OpenZFS (CDDL).** `LICENSING.md:217-225` records "no prebuilt ZFS kernel module is redistributed"; Debian ships ZFS as DKMS source in `contrib` ([Debian Wiki: ZFS](https://wiki.debian.org/ZFS)). A prebuilt `zfs.ko` in an image ends that position. Recommended: build it only as a separate, optional, kernel-locked ZFS layer with CDDL notices and source, built for internal labs until the project owner decides with legal review (O2); host-side DKMS is rejected because it needs the toolchain and module signing on the host.

### D6 — Documentation work list

Guide: rewrite `docs/guide/install-nex-core.md`, `software-updates.md`, `verify-nex-core.md`; update `replace-a-failed-system-disk.md`, `host-maintenance-mode.md`, `pool-authority-and-recovery.md`, `deploy-controller-appliance.md`, `guest-tools.md` (VirtIO path), `storage-repositories.md` (ZFS availability), `publish-a-release.md`, `change-management-network.md`; new pages `roll-back-a-host-update.md`, `reinstall-keeping-state.md`, `use-developer-mode.md`, `collect-host-diagnostics.md`; add all to `docs/guide/README.md`. `docs/API.md` + `docs/api/*.json` + `python3 scripts/generate_api.py` for changed update/status contracts; `docs/DOCUMENTATION.md` assessment; `installer/README.md`; `docs/LICENSING.md`; `CHANGELOG.md` per phase; `docs/qa/` walkthrough reports; `docs/changes/<date>-immutable-*.json` per phase with `state.owner: "core"` and `record` = "host image/slot state `/var/lib/nex/image-state.json` and persistent `/etc` overlay on each host; pool data stays in the pool master's existing records; a fresh Controller recovers it through existing host inventory"; `roadmap.impact: updated` when milestones change (`website/src/routes/roadmap/+page.svelte`). `docs/BUG_LEDGER.md` only for bugs found. **Wording that says "Debian packages/apt" needs a project-owner-approved edit** (not made by this plan): `AGENTS.md:130-131`, `PRODUCT.md:4,18`, `README.md:3`, `installer/README.md:241-246`, `docs/plans/2026-09-24-net-installer.md`, `docs/plans/2026-09-09-package-updates.md`, `website/src/routes/+page.svelte`.

### D7 — Phased delivery and gates

Every phase gate follows AGENTS.md order: Core behavior → durable host state → host API → outage/recovery → Controller UI → guide walkthrough.

| Phase | Scope | Gate (all observable, in a disposable lab) |
|---|---|---|
| **I-0** Measure and decide | Baseline installed size (`docs/qa/2026-09-18-iso-packaging-efficiency.md`), slot-size budget, d-i udeb-pruning spike, boot/verity spike, owner decisions O1–O9 | Plan, QA report with measured sizes, decisions recorded; no product change |
| **I-1** Read-only root on current packages | `/` mounted ro, `/etc` overlay, `/var` state partition, apt only in a transitional remount window; fix every runtime write outside `/etc`,`/var` | `findmnt -no OPTIONS /` contains `ro`; fresh install and all item (1) operations pass; Controller deleted → pool re-adopted; reboot keeps config; interrupted `.deb` update leaves a bootable host |
| **I-2** Prebuilt image, layers, trim | `internal/hostimage` writes slot A (slot B laid out empty); prebuilt modules, optional layers, trimmed set, reproducible build, manifest + source bundle | Offline and netinst UEFI install (single disk and RAID1), BIOS firmware refused before any write; double-build hash equal; image-content tests; licence gate D5 inspected; `apt install` no longer needed on host |
| **I-3** A/B, verity, Core update API | Slot B writes, boot counting, automatic fallback, tamper refusal, update/rollback API and UI | Items (2)(3)(4)(5)(6); rollback preserves config; kill Controller mid-update — Core finishes or reverts alone; state-version gate proven |
| **I-4** Appliance immutability and rolling updates | Appliance as signed payload with separate Controller state disk; pool master runs a one-host-at-a-time update job through maintenance/fencing rules | Item (7)–(8): quorum kept, VMs running throughout, no duplicate writable VM, update resumes after master change |
| **I-5** Secure Boot / TPM | Signed boot chain, measured boot, sealed secrets, attestation reporting | Enforcing Secure Boot lab refuses unsigned slot; physical hardware result recorded separately (virtual labs do not establish it) |

### Risks and decisions for the project owner

| ID | Risk / decision | Recommendation |
|---|---|---|
| O1 | Immutable layer built on Debian replaces the "packages/apt" delivery wording in `AGENTS.md:130-131` | Approve the wording after Phase I-0 |
| O2 | Prebuilt CDDL ZFS module redistribution | Separate optional layer, legal review before any external release |
| O3 | Nex now rebuilds and hosts an image for every Debian security fix (hosts no longer self-update from Debian); source retention follows image lifetime | Decide rebuild cadence and critical out-of-band SLA |
| O4 | Persistent operator-signed extension layers and their trust key | Not in I-1…I-4; revisit with I-5 |
| O5 | Legacy BIOS boot (`installer/README.md:160-167` supports it today) is dropped by the UEFI-only image edition | Confirm UEFI-only for the image edition; keep the BIOS d-i path only for Phase I-1 hosts |
| O6 | Minimum disk (16 GiB rule) with two slots + state, appliance and VirtIO payloads | Re-measure in I-0; raise minimum if needed |
| O7 | Diagnostics download from Controller UI needs an API | CLI-only until requested |
| O8 | Bit-for-bit reproducibility may not be achievable for the whole image (`installer/README.md:64-67`) | Require hash equality; document and bound any residual diff |
| O9 | Rolling-update job placement on the pool master | Core-owned, resumable; Controller only requests |

**API impact:** *changed contract* for the existing host update/status operations (`GET /api/v1/updates`, `POST /api/v1/updates/apply`, `/api/local/v1/updates`, `/api/local/v1/updates/apply`) — semantics defined in the Updates section; host observed state gains image slot, rollback and `developer_mode` fields (existing host inventory). Installer changes: none (local wizard → d-i → installed host; no Controller/Core call). `nex-devmode` and `nex-diagnostics` are local-console tools: none. **State owner:** `core` (host-local image/state records above).

## Documentation checklist

Per `AGENTS.md` and `docs/DOCUMENTATION.md`, documentation lands in the same change as each phase, never at the end.

- [ ] `docs/guide/`: `install-nex-core.md`, `software-updates.md`, `replace-a-failed-system-disk.md`, `verify-nex-core.md`, `recover-without-controller.md`, `deploy-controller-appliance.md`, storage pages for ZFS/cluster layers, plus new pages for rollback, developer mode and diagnostics. Walk each against a disposable lab.
- [ ] `docs/API.md` and the machine-readable contracts under `docs/api/` (`python3 scripts/generate_api.py`), `tests/test_api_contract.py`.
- [ ] `docs/LICENSING.md` (image manifest, source bundle, ZFS posture), `installer/README.md`, `appliance/README.md`.
- [ ] `AGENTS.md`, `PRODUCT.md`, `README.md` wording after the owner decision.
- [ ] `docs/changes/*.json` record per phase with `state.owner: core`; `CHANGELOG.md`; `docs/BUG_LEDGER.md` for bug fixes only.
- [ ] `python3 scripts/check_docs.py --worktree` and the repository privacy test.
