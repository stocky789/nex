# ISO packaging efficiency verification

Date: 2026-09-18. Development version: 0.3.3.

## Scope and comparison

Preserve every driver, installed capability and offline dependency while changing
only transport/storage representation. The earlier local 0.3.2 ISO measured
3,536,846,848 bytes. Its Windows driver ISO was 877,373,440 bytes and the retained
VirtIO source directory was 624,999,095 bytes, including the intact 290,400,672-byte
RPM and 334,598,423 bytes of extracted copies. These are historical local artifact
measurements, not a claim about a rebuilt 0.3.3 artifact.

## Package measurements

Repacking the existing 0.3.2 Core package with identical source archives shared
through a relative symlink reduced it from 721,343,768 to 597,048,620 bytes:
124,295,148 bytes saved. The build now rejects different Core/appliance source
archives before starting its container. An actual `dpkg-deb` build/extract
regression verifies that both installed paths resolve to the same archive.

Appliance image compression was evaluated and excluded. `qemu-img convert -c`
preserved the virtual disk bytes and reduced the qcow2 from 750,452,736 to
509,476,864 bytes, but the resulting Debian package was 623,451,824 bytes:
26,403,204 bytes larger than deduplication alone. The outer package compressor
already handles the disk well; the extra conversion is not part of the change.
These are isolated measurements using the previous local package, not the size
of the final newly built package or ISO.

## Verification status

The full `./installer/build.sh` completed successfully from the working tree based
on `64fbbbd`. The resulting 0.3.3 ISO is **2,399,469,568 bytes**, compared with the
previous local 0.3.2 ISO's 3,536,846,848 bytes: **1,137,377,280 bytes (32.16%) smaller**.
This compares two development versions, not bit-identical builds with a single
toggle; the isolated measurements above separate the principal packaging savings.

Final ISO SHA-256:
`055cc891209060ec569f5cedc1f9e22c0aba41041e722840d26770f21e59de1d`.
Matching source bundle: 2,937,505,009 bytes, SHA-256
`c023fdd365a1bfc2c80aec190a4b2b02e2eb1e6d52a30a763328a2c72059d25f`.
Both hashes were independently recomputed and matched the final compliance
manifest; both SHA-256 sidecars passed. The delivered source archive was also
listed and checked for the VirtIO notice, intact RPM, driver source ZIP and nested
QEMU source tarball. The separately delivered source bundle remains larger than 2 GiB;
shrinking the ISO does not establish that hosted upload limits are sufficient.

Inspection of the finished ISO found compressed Windows media of 248,119,863 bytes,
the original source RPM and notices, and no raw driver ISO or redundant extracted
VirtIO source files. Streaming the compressed payload from the actual ISO produced
the original pinned SHA-256. The new Core package is 599,351,336 bytes; actual
extraction confirmed the source symlink and identical resolved archive bytes.
The offline repository dropped from 512 to 452 package entries; the only removed
package name is the obsolete `ovmf`, superseded by the explicit pinned
`ovmf-generic`. The requested installation package list is unchanged.

Executed checks:

- 65 focused Python tests across ISO payload efficiency, source bundle,
  distribution packaging, Core package and appliance suites passed. The payload
  test executes the actual late-install shell block, including checksum mismatch,
  truncated transport and missing payload, and checks failed restoration cleanup.
- `bash installer/check.sh` passed, and the full build executed the decoder check
  against both real Debian Installer initrds.
- Installer shell, handoff, keyboard, QoL, hostname, link and disk integration
  scripts passed with their explicitly simulated process/device/GTK boundaries.
- `go test ./internal/vmhost -run '^TestSeedBundledMedia' -count=1` passed.
- Documentation worktree, privacy and whitespace checks passed before the build.
- Independent code, workflow and applicability review completed. Both providers'
  push/PR workflows execute the regressions; hosted runs were not triggered.

BIOS and UEFI menus booted from this final ISO and their captures were inspected.
The complete disposable smoke command passed:

```sh
python3 tests/integration/iso-smoke.py dist/nex-core-0.3.3-amd64.iso \
  --output .cache/iso-efficiency/offline-smoke \
  --kernel .cache/nex-iso/stage/install.amd/vmlinuz \
  --initrd .cache/nex-iso/stage/install.amd/initrd.gz \
  --uefi-code /usr/share/OVMF/OVMF_CODE_4M.fd
```

This created a fresh 24 GiB regular-file virtual disk, used KVM with 2 vCPUs and
3 GiB RAM, and gave the VM **no network interface**. Serial evidence recorded
`NEX_SMOKE_INSTALL_PASS`, followed by `NEX_SMOKE_PACKAGING_PAYLOADS_PASS` on both
first boot and reboot. The latter checks the complete installed driver ISO and
source RPM against their pinned hashes and compares both packaged Nex source paths.
The run also passed OVMF certificate checks, root-only account policy, Nex identity
through a base-files reinstall, nested KVM boot and the persisted reboot marker:
`NEX_NESTED_KVM_BOOT_PASS`, `NEX_SMOKE_FIRST_BOOT_PASS` and `NEX_SMOKE_REBOOT_PASS`.
All test VMs shut down; the disposable disk and local evidence remain under `.cache`.

Installation used the BIOS/kernel-driven unattended text harness; the UEFI check
was menu boot, not a new UEFI installation or guest Secure Boot certification.
No new graphical-wizard interaction, physical hardware, live infrastructure change
or publication is claimed. Raw local logs/captures remain untracked; this report
is a sanitized summary of those executions. Hosted CI remains unexecuted.

## API and state

API impact: none. Installed media paths, media import and source-serving contracts
remain unchanged; no Controller/Core calls or result formats change. State owner:
none. Packaging adds no durable product settings. No phase acceptance gate changes.
