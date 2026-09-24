# Nex Core network installer verification

Date: 2026-09-24  
Result: both final ISO variants passed unattended BIOS installation, first boot and reboot.  
Scope: development version 0.4.12; network installer alongside the full offline ISO.  
Status: original local build, binding checks and BIOS installation passed;
2026-09-25 corrected overlay passed disposable UEFI installation and first boot.
Rebuilt-ISO UEFI installation, physical hardware and hosted publication remain unverified.

## Environment and method

Checks used the working tree, the Debian ISO builder container and disposable QEMU/KVM
VMs. Diskless tests used Debian 13.6.0 installer userspace and test initrd overlays;
installation tests used newly created virtual disks. No physical disks, production hosts,
forge runs or external publication were exercised. Local logs and captures below are
untracked evidence; this report records their scope and observed results.

## Automated checks

- The primary reported the full Python suite: 414 tests ran successfully with one skipped host
  APT check. The corresponding real APT checks passed separately inside the Debian builder.
- The CI/publisher checks passed 32 tests, covering artifact bindings, payload publication,
  anonymous release reachability and workflow configuration. All 13 `test_ci_security.py`
  tests passed after BUG-0572 corrected two stale assertions to check the actual conditional
  GitHub/forge concurrency expressions. No hosted CI run is implied.
- The primary reran `docker run --rm --entrypoint python3 -v "$PWD:/work:ro"
  nex-iso-builder -m unittest discover -s tests -p 'test_netinst*.py'`: all 16 tests
  passed, none skipped. The eight installer tests within that run passed,
  including real APT planning before downloads, epoch/escaped filenames, installed-package
  skipping, preflight, reuse and the `in-target --pass-stdout` regression.
- `python3 -m unittest discover -s tests -p test_netinst_network.py`: four tests passed.
  The compiled C check executes the actual configuration entry point against simulated
  device/filesystem boundaries: missing/disabled management is rejected, optional arguments
  are normalized, failed preflight writes no answers or credential output, and the offline
  variant still works without activation. The GUI regression checks discovery-time copy.
- Native configuration-helper compilation with GLib/libcrypt and `-Wall -Wextra`, shell
  syntax checks and `bash installer/check.sh` passed. The text-wizard integration harness
  passed its `sh` scenarios; its local BusyBox `ash` scenarios were skipped because BusyBox
  was absent. Source-bundle tests (11), build-source tests (8), payload-efficiency tests (2),
  release-version tests (10) and repository-privacy check (1) passed.
- `python3 scripts/check_docs.py --worktree` passed for all 46 current changed paths.
  The final changed-path assessment must be rerun after the remaining edits. Structural
  and privacy checks supplement semantic/manual review; they do not establish installation.
- Independent review reported no outstanding code findings after download reuse, retry
  inventory and source-metadata corrections. Final artifact/installation acceptance remains
  separate from that source review.

## Native networking and download checks

The lab worker ran a diskless 512 MiB KVM VM with virtio networking, Debian 13.6.0's
kernel/initrd and NIC installer packages extracted from the same ISO. Native
`udhcpc -i eth0 -n -t 3 -T 3 -s /etc/udhcpc/default.script` returned 0 and configured
IPv4, the default route and DNS. Native Wget validated TLS and followed the pinned
Debian snapshot Release URL to HTTP 200. Log: `.cache/netinst-lab/dhcp-final.serial.log`.

The current `netinst-network.sh` then passed DHCP activation, switching to static IPv4
with gateway/DNS, and switching back to DHCP with an explicit DNS override. HTTPS reached
Debian after static setup and the DHCP retry. Log: `.cache/netinst-lab/helper-final.serial.log`.
These are actual helper executions in installer userspace, not physical NIC or tagged-VLAN proof.

The current fetch helper ran against a disposable TLS server with a synthetic 64 KiB
package. A disconnect after 32 KiB produced a real Range request at offset 32768, HTTP 206
and a matching final SHA-256. Repeating the helper sent no GET for the verified cached file.
A corrupt endpoint failed after exactly three attempts and left no final or partial payload.
A test CA was supplied; certificate verification remained enabled. Logs:
`.cache/netinst-lab/fetch.serial.log` and `.cache/netinst-lab/fetch-server.log`.
This proves transport resume/integrity behavior, not recovery of an entire interrupted install.

## GUI and offline installation

The network worker inspected real GTK captures in diskless KVM at 1280×800. Keyboard
navigation reached Network; netinst rejected continuing without a management adapter,
remained on Network with a visible error, and allowed Back to Host. The full-ISO variant
allowed offline continuation to Disk. Early captures revealed stale network copy; the
corrected wizard sets it after installation media discovery. The patched 0.4.12 wizard's
clean layout, corrected copy and rejection were inspected by the worker and primary in
`.cache/netinst-lab/ui/patched-{no-network,rejected}.png`.

A separate positive GUI check detected the virtio NIC automatically, obtained a real DHCP
preview, accepted management selection and advanced to Disk using keyboard interaction.
The worker inspected `.cache/netinst-lab/ui/virtio-{network,management,disk}.png`.
These captures use a compiled-wizard overlay, not the final rebuilt artifact; the VM was
shut down without disk writes. Earlier unpatched captures do not verify the correction.

The preliminary full ISO passed unattended BIOS installation without a NIC onto a new
24 GiB virtual disk, first boot and reboot. Checks covered ZFS DKMS/load, driver ISO
integrity, Core/appliance source identity, enrolled OVMF certificates, root-only login,
Nex identity surviving a base-files reinstall, and nested KVM guest boot with a persistent
domain. Logs: `.cache/netinst-lab/final-full-smoke/`. This is completed offline evidence
for that preliminary artifact, not a final netinst result.

## Final 0.4.12 build and integration findings

A fresh outer build rebuilt Core and the Controller appliance at 0.4.12, including both
native integration corrections below. The builder exited 0 and the primary ran
`scripts/release-publish.py --check` with both ISOs, both compliance manifests, shared
source and both payloads: all bindings passed without publication.

| Artifact | Bytes |
| --- | ---: |
| Full ISO | 2,433,024,000 |
| Netinst ISO | 917,831,680 |
| Shared corresponding-source bundle | 3,105,839,917 |
| Nex Core download, including appliance | 613,082,000 |
| Nex-built DLM download | 85,860 |

Netinst SHA-256: `167da5e0fd28a3af5d023ad50989915da0ef38d02d726be6d8ef2b3d18838ca3`.
Source SHA-256: `45f4095f46fa7bc210544de184784da98d7f07857395dac6c34fa9f94feacf1b`.
The manifest names 503 direct Debian snapshot packages, two Nex-hosted packages and one
direct Fedora driver ISO. APT selects the needed subset. One fresh ISO download and
install serves approximately 1.43 GiB from Nex versus 2.27 GiB for the full ISO
(about 37% less); upstream traffic is additional. BIOS and UEFI boot-menu captures
of the preceding netinst build were inspected.

The images use a disposable server URL and temporary appliance signing key; they are not
publishable release images. A test CA was added only to a separate smoke-test initrd.
The release workflow builds the configured publication URL into its own artifacts.

The serial integration run found and reproduced two defects:

- BUG-0571: newly installed NIC modules were absent from udev's cached indexes. The shared
  early hook now regenerates indexes, reloads udev and replays device events. A fresh serial
  run with the corrected hook passed discovery, DHCP and HTTPS preflight and entered
  partition/base installation without manual commands.
- BUG-0573: Debian's `in-target` wrapper sent the APT download plan to syslog, leaving the
  redirected plan empty. The command now uses native `--pass-stdout`; regression coverage
  reproduces the original failure. The corrected late-install retry downloaded and verified
  383 packages, skipped 122 already satisfied by the base, reused the verified driver ISO
  and completed native package/configuration checks including ZFS. Native finalization
  encountered a CD prompt after the test harness changed APT indexes; a fresh run uses
  the original media package for its base-files test instead. First boot/reboot acceptance
  is established by the subsequent clean final-artifact run below.

Both final ISOs completed clean unattended BIOS installation, first boot and reboot;
both smoke harnesses exited 0. The netinst run used only a test CA overlay, with production
installer scripts unmodified and no manual repairs. Logs are retained locally in
`.cache/netinst-lab/clean-final-smoke2/` and `.cache/netinst-lab/clean-final-full/`.
Both emitted `NEX_SMOKE_INSTALL_PASS`, first-boot and reboot pass markers, covering
ZFS DKMS/load, verified driver media, appliance/Core source equality, enrolled OVMF keys,
root-only login, host identity after base-files reinstall and nested KVM guest persistence.
The full ISO's VM had no NIC. Final netinst BIOS and UEFI menu captures were inspected.
Read-only extraction of the installed disk confirmed 383 selected packages: 381 direct
Debian snapshot URLs and two Nex-hosted payloads; 122 available packages were unnecessary.
Driver provenance records the pinned Fedora HTTPS URL. The embedded source archive matches
the final runtime installer scripts and the matching member of the public source bundle.
Temporary lab services and private keys were removed after verification.
Independent review verified both runs' pass markers,
artifact hashes and the full ISO's absent NIC.

The smoke harness now copies the original media's base-files package without changing
target APT indexes, and selects the running kernel when multiple kernels are installed.
BUG-0575 has a runnable shell regression with two kernel files; restoring the wildcard
reproduces its failure. `python3 -m unittest discover -s tests -p test_iso_smoke.py`
passed. These fixture changes do not alter the product images.

## Acceptance still required

### Published 0.4.12 UEFI regression, 2026-09-25

The published netinst ISO (`SHA-256 abe135e52c14ea5accd1fa2d8a6637f4f6cbfb5a59d69ded91ed7ac19fede5af`)
was booted in a disposable UEFI/KVM VM with a new 24 GiB virtual disk. A test-only
initrd overlay supplied valid wizard answers and sent sanitized diagnostic excerpts
to the serial console; the shipping network and partition scripts were unchanged.
The wizard's DHCP setup and first source check passed. Debian `netcfg` then removed
the live resolver. At partman's init hook all five source probes failed name
resolution, the hook returned 1, and Debian displayed its component menu. The
partman log contained only its opening lines; no partition was written.

A second disposable VM used the locally rebuilt GTK frontend and corrected
network/partition scripts overlaid onto the verified public ISO. Its partman
preflight passed, partitioning completed and base installation started. The
inspected capture showed Nex's progress panel with current installer activity.
A separate forced partman failure showed a Nex stopped screen with the failure
line in the visible log, no Debian component list or action buttons, and no
later component ran. The first capture exposed an old-log scroll defect; a
rebuilt frontend scrolled the failure log to its latest lines, confirmed by a
second inspected capture. The automated resolver fixtures and source checks
passed locally after the tracked correction. In the Debian builder,
`python3 -m unittest discover -s tests -p 'test_netinst*.py' -v` passed all 17
tests without skips; `bash installer/check.sh`, the installer handoff and shell
checks, the documentation gate and repository-privacy test also passed.

A fresh corrected-overlay UEFI VM then completed the installer and powered off.
The serial log contained the test install-pass marker after package configuration
and GRUB setup. Booting its disposable disk without the ISO loaded the installed
EFI boot path and emitted a first-boot pass marker. The test-only service checked
`ID=nex-core`, the Core package's installed status and a nonempty netinst manifest
before printing that marker. This verifies the corrected scripts and frontend
overlaid on the exact public ISO, not a newly mastered release image or a manual
graphical-wizard walkthrough. Rebuilt-artifact installation, physical hardware,
tagged VLAN, adverse-network recovery and forge publication checks remain open.

- Rebuilt-ISO UEFI installation, complete manual graphical-wizard installation,
  tagged VLAN, failed DNS/unavailable sources and interruption after disk writes
  remain distinct from the corrected-overlay run above. A restart is not rollback.
- Inspect a real forge run and retained artifacts after an owner-authorized push. Verify
  final redistribution source/notices and delivery under [licensing requirements](../../LICENSING.md).
  Physical hardware and public publication remain unverified.

## Impact review

API impact: none; local installer/build/download paths change, with no Controller/Core or
client-facing API changes. Guide impact: updated in the
[installation guide](../../guide/install-nex-core.md). State owner: none; no additional
pool state or Controller data. See the [scoped plan](../../plans/2026-09-24-net-installer.md).

Scoped handoff: 1632 lines added, 116 removed; net +1516 lines, excluding the concurrent
VM-navigation change. Changes remain unstaged; no commits or remote operations were made.
