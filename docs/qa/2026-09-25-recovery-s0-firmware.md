# Recovery S0: stopped UEFI, Secure Boot and virtual TPM state

Date: 2026-09-25. Status: **BIOS stopped, BIOS running-quiesced, UEFI Secure Boot-off, UEFI Secure Boot-on, and UEFI Secure Boot plus vTPM 2.0 profiles passed on the matched Core candidate.** This package-based disposable-lab evidence does not close S0 or establish product support by itself.

## Candidate and package provenance

The selected Core candidate ran `nex-core-local 0.3.6~recovery.s0` (`3c302fc06d96d7972b3b5da0eb1ca5255c16b8492d5969c585a2b9f2ed3d7eb4`). This was a Debian cloud-derived Core guest with the candidate package installed, not a Core ISO installation.

The tested runtime packages matched the ISO recipe's immutable Debian snapshot dated `2026-09-20`:

| Package | Installed / snapshot version | Cached `.deb` SHA256 |
|---|---|---|
| `ovmf` | `2025.02-8+deb13u1` | `78e0d54df11fc77406cb7a0bc9a39e5bca6d1cbe06556b91d9a73491c52decdf` |
| `swtpm` | `0.7.1-1.5` | `7a65c5254cdc592394751f10516b13aa26737b3fc3d79c6b12a8f6b4b12cc828` |
| `swtpm-tools` | `0.7.1-1.5` | `c4b9008ee54a59072e6e56afa2b044dd75cca8e3b3a8a366494a4fe42a9044db` |
| `swtpm-libs` | `0.7.1-1.5` | `3b5a5d896f53e8fa1ac80eba2b0eff757e9752850d2724ce0bf107b9906950fa` |

The comparison used temporary isolated apt source/list metadata for `https://snapshot.debian.org/archive/debian/20260920T000000Z/` and `https://snapshot.debian.org/archive/debian-security/20260920T000000Z/`; the candidate versions equaled the installed versions. It did not change the guest's normal apt sources. The ISO recipe is in [`installer/build.sh`](../../installer/build.sh) and the firmware package list in [`installer/packages.txt`](../../installer/packages.txt).

Before the matched lifecycle test, Core reported QEMU `1:10.0.13+ds-0+deb13u1` and Debian libvirt `11.3.0-3+deb13u3`. At a stopped boundary, the test upgraded exactly 15 selected installed libvirt packages from the canonical `final-66f0b77ea71c` build output to `11.3.0-3+deb13u3+nex1`; the apt simulation and install each reported 15 selected packages, zero new installs, and zero removals. The staged `.deb` hashes matched the build's `SHA256SUMS`; the private run record contains each package hash. The libvirtd PID changed after the package-triggered restart; exact process identifiers remain in private evidence. `python3 tests/integration/libvirt_readonly_policy.py` passed against `/usr/lib/libvirt/virt-aa-helper` after the restart. Twenty-four unrelated upgrades remained untouched.

The matched Core candidate reported `python3-virt-firmware 24.11-2`. `virsh domcapabilities --virttype kvm --arch x86_64 --machine q35` advertised EFI, Secure Boot loader support, TPM `tpm-crb`, emulator backend, and TPM 2.0. The installed enrolled-key firmware descriptor selected `OVMF_CODE_4M.ms.fd` with `OVMF_VARS_4M.ms.fd` and required SMM, matching the Core XML materialized by [`firmware.go`](../../internal/vmhost/firmware.go): q35, `firmware='efi'`, `secure-boot=yes`, `enrolled-keys=yes`, SMM on, UUID-keyed NVRAM, and `tpm-crb` / emulator / 2.0.

## Reproduction and observations

Run the firmware probe only against an explicitly assigned disposable Core candidate and a fresh ignored evidence directory:

```sh
python3 tests/integration/recovery_s0_firmware.py \
  --lab-dir .cache/recovery-s0-core.<selected-run> \
  --node core2 \
  --profile uefi-secure-boot-vtpm20 \
  --evidence .cache/recovery-s0-firmware-evidence.<unique-run>
```

`--profile` accepts `bios`, `uefi-secure-boot-off`, `uefi-secure-boot-on`, or `uefi-secure-boot-vtpm20`; the last is the default. Each profile boots a real disposable guest, confirms the requested firmware profile, and performs a stopped disk snapshot/revert; TPM device and secret checks apply to the vTPM profile. `--running-capture` is restricted to BIOS and adds a guest-agent-quiesced, running disk snapshot with post-capture guest mutation and revert verification. After libvirt returns, the probe queries QGA `guest-fsfreeze-status`, records it, and requires `thawed` before writing. A successful quiesced revert must explicitly request `--paused`, prove the domain is paused, stop it while still paused, prove it is shut off, and only then boot for verification; libvirt's default restored running state is not accepted.

The probe validates the disposable-lab marker and SSH key, verifies the cached Debian 13 generic-cloud base image SHA256 (`85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3`), and confines generated guest disk, NVRAM, TPM state, and snapshots to a UUID-scoped directory/domain. Failed or ambiguous libvirt operations preserve their artifacts. Raw guest output, identifiers, and paths remain in ignored `.cache` evidence.

The matched Core candidate's current profile results are:

| Profile | Result |
|---|---|
| BIOS stopped snapshot | Passed guest boot, marker rollback, snapshot delete, post-delete boot/recheck, and cleanup. BIOS materializes no NVRAM or TPM state. |
| BIOS running-quiesced snapshot | Passed `--quiesce`, QGA `thawed`, post-capture marker rollback, explicit `snapshot-revert --paused`, observed paused state, stop while paused, observed shutoff before guest verification, snapshot delete, post-delete boot/recheck, and cleanup. |
| UEFI, Secure Boot off | Passed UEFI guest boot, exact Core XML features `secure-boot=no` and `enrolled-keys=no`, stopped-state assertion immediately after native revert, paired NVRAM restore with matching hash/size/uid/gid/mode, future EFI entry absence after rollback and deletion, and post-delete guest recheck. The guest had no SecureBoot EFI variable and `mokutil` reported `This system doesn't support Secure Boot`; this is recorded as variable absence, not as a fabricated zero/disabled variable value. |
| UEFI, Secure Boot on, no TPM | Passed guest `SecureBoot` EFI variable value `1` and `mokutil` `SecureBoot enabled` at initial boot, after rollback, and after deletion/reboot. Native disk revert changed NVRAM; the probe verified the VM remained shut off before paired state replacement, which restored exact content and metadata. The future EFI entry was absent after rollback and deletion. Snapshot deletion and post-delete guest recheck passed. |
| UEFI, Secure Boot on, vTPM 2.0 | Passed initial guest profile checks, stopped-state assertion before paired disk/NVRAM/swtpm restore, Secure Boot checks, sealed test secret unseal, future EFI entry and TPM persistent-handle absence, snapshot deletion, and post-delete boot/recheck. Native disk revert changed both NVRAM and swtpm state; the stopped paired restore matched data and metadata. The test uses no PCR policy. |

The vTPM run first exposed fixture setup ordering. libvirtd started at 03:30 UTC, before the `tss` account was installed at 03:34 UTC. The first TPM setup created the root-owned mode-0755 CA lock and root-owned CA files at 03:40 UTC. The canonical libvirt package install restarted the daemon at 04:48 UTC; subsequent TPM setup used `tss`, which lacked access to those older runtime files. The test changed only the owner of the configured CA lock, signing key, issuer certificate, and serial file to `tss`, preserving their original group and mode. It did not change the root CA private key, package files, AppArmor policy, or product code. The matched vTPM profile then passed. This is a fixture-only ownership repair after daemon identity resolution changed, not a product support change.

The first BIOS running-capture pass used probe source SHA256 `47a7a015bc50dcde36b602cc3b62adbbb190b9163afa7f2366f411ed64867ed3`; it predates the explicit `--paused` revert assertion. Current probe SHA256 is `d429b53610b62f76e577b59f5090fc830a08eeb2f2ae1678d4a082569d3b410c`; the BIOS running branch and all UEFI profiles passed with this source, including the stopped-state assertion and future EFI-entry checks. Raw guest output, identifiers, and paths remain in ignored `.cache` evidence.

The previously executed UEFI/Secure Boot/vTPM profile reached the following checks. It predates the final owner-preservation hardening, current profile selector, and matched libvirt package, so its observations are historical evidence only:

| Check | Observation |
|---|---|
| Real nested guest boot | Debian generic-cloud guest booted with Core's materialized q35 UEFI / enrolled-key Secure Boot / SMM / vTPM 2.0 XML. Inside the guest, `/sys/firmware/efi` existed and `mokutil --sb-state` reported `SecureBoot enabled`. |
| TPM secret | The fixed disposable-lab test value was sealed under the TPM owner hierarchy using `tpm2_create`'s sealed-data mode (no PCR policy was supplied), then loaded and unsealed successfully. This demonstrates state continuity for this test profile, not PCR-bound sealing or arbitrary workload secrets. |
| Disk and firmware mutation | While the guest was booted after the stopped disk snapshot, the test wrote a post-point disk marker, provisioned a future persistent TPM object, and created a future EFI boot entry with `efibootmgr`. |
| Native disk snapshot revert | Disk marker returned to its baseline. Libvirt's native disk revert did **not** restore either external firmware state: NVRAM content hash changed and the swtpm permanent-state hash and size changed. NVRAM/swtpm files live outside the guest disk chain. |
| Explicit stopped paired-state restore | The probe restored saved NVRAM and swtpm state while the guest was stopped. On the next boot the baseline sealed value unsealed, the future TPM handle was absent, the future EFI entry was absent, and Secure Boot remained enabled. The run predates the final harness metadata-preservation assertion: its NVRAM bytes matched, but the copied file owner became root instead of the original libvirt-qemu owner. The harness now preserves and compares owner, group, and mode; this corrected version still needs a real rerun. |
| Snapshot deletion | **Failed and preserved artifacts.** libvirt 11.3 returned `block-commit: Could not open .../guest.qcow2: Permission denied` while deleting the external snapshot. No AppArmor workaround was applied. |

The evidence supports a design that captures and restores matching NVRAM and swtpm state alongside the stopped disk snapshot. A disk-only snapshot cannot be presented as complete firmware/TPM recovery. Paired restore must preserve data and filesystem metadata, and Core must recover the complete operation safely if any state component fails.

## Limits and remaining gate

This is one package-based candidate and one Debian cloud guest. It does not verify a Core ISO install, TPM PCR policy, physical firmware, Windows installation, cross-host restore, power-loss consistency, concurrent mutation, or a production storage backend. The patched libvirt helper/deletion path and all five firmware profiles passed in this disposable candidate after the fixture-only swtpm CA ownership correction. Results still require integration with the Core recovery contract, API failure/retry behavior, product implementation, and other S0 storage and restore gates. Keep the distinction between paired firmware-state restore and native disk-only revert. S0 and product support claims remain open.
