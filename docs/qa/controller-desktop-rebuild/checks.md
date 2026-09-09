# Controller desktop ISO rebuild

Date: 2026-09-09
Status: Rebuild, native guest boot, package smoke and independent artifact verification passed.

## Scope

Matt requested rebuilding the Nex Core ISO after the Controller desktop-layout
change. Rebuild the Controller appliance, package it with the host-local service,
then select that exact package for the ISO. The installer workflow and existing
API contracts are unchanged. Unrelated website work is excluded from this slice.

API impact: **none** — artifact compilation/assembly ships the existing session,
inventory, enrollment, operation and observation contracts; no caller, Controller
operation, Core command/result or client API behavior changes in this rebuild.
Guide impact: **updated** — record availability of the matching desktop-layout
artifact set in the deployment guide and link executed evidence.

## Build inputs and verification

Use the existing Debian appliance and ISO recipes, pinned base digests, retained
source snapshots and existing private development signing identity. The private
key remains outside the repository and is not copied into images or logs.

The cached `debian-13-genericcloud-amd64-20260831-2587.qcow2` has SHA256
`85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3`.
Independent review computed its SHA512 and matched the freshly fetched official
[versioned HTTPS checksums](https://cloud.debian.org/images/cloud/trixie/20260831-2587/SHA512SUMS).
This is HTTPS checksum verification, not a detached Debian cloud-image signature.
The ISO builder separately checks the pinned Debian netinst SHA256 and signed
checksum list before using the cached base ISO.

## Results

All build stages exited zero: appliance Go compilation and UI typecheck/build,
libguestfs customization and qcow2 integrity, signed bundle verification, Debian
host-package compilation and UI build, then ISO assembly with the exact new package.
The final bundle retains the prior usability1 development public key; no new trust
identity is required for this build. The final manifest was signed with its matching
retained private key after assembly.

Artifact set: `0.1.0+desktop1` appliance and host package, included in
`dist/nex-core-0.1.0-alpha1-amd64.iso` (1,630,076,928 bytes).
The ISO's SHA256 is:

```text
9800dc2e67c6d3bd06dce84dd88a3917eac34b053dc5dcb5b1b23d4acfa3f214
```

`sha256sum --check nex-core-0.1.0-alpha1-amd64.iso.sha256` passed from `dist/`.
[Artifact hashes and build inputs](artifacts.json) retain the exact appliance,
package, source, manifest/signature/key and ISO digests. The previous local ISO
was preserved before replacement. Build logs remain locally under
`.cache/controller-desktop-rebuild/`.

The actual disposable native guest check passed with the finished image:

```sh
python3 tests/integration/appliance-dhcp-boot.py \
  --image dist/appliance-desktop1/controller.qcow2 \
  --evidence docs/qa/controller-desktop-rebuild/guest-boot \
  --docker /tmp/nex-rootauth-docker
```

[Guest results](guest-boot/results.json) confirm DHCP on the exact management MAC,
verified-hostname HTTPS readiness, wrong-password rejection, successful configured
administrator login, active services and removal of firstboot credentials and
sensitive cloud-init material. The test used a disposable KVM overlay and QEMU user
networking, then removed the overlay, seed and temporary secrets.

The actual package smoke also exited zero:

```sh
bash tests/integration/core-local-package.sh \
  --deb dist/core-local/nex-core-local_0.1.0+desktop1_amd64.deb \
  --docker /tmp/nex-rootauth-docker
```

Inside a fresh disposable Debian 13 container, it verified service enablement,
explicit service startup, TLS IP verification, native root PAM sessions, password
rotation and rejection of wrong/legacy/locked/expired credentials. It checked
blocked preflight, resource defaults, root GUI labels, source/API documents and
secret-free service logs. Removal retained configuration and a disposable storage
sentinel without legacy credentials. Container policy suppresses automatic init
service startup; the test explicitly starts the local service, so this is not
proof of installed-host systemd boot.

Independent review verified the final Ed25519 signature against the prior trusted
key, all image/source digests and 67 retained runtime/UI/build source files against
commit `1fb9782`. It extracted actual guest UI assets, confirmed the new menu/tab
layout, and matched them byte-for-byte to the package assets. The package contains
identical appliance bundle/source files and the prior trusted public key.
The package extracted from the final ISO has SHA256
`948c315241b40efc2d7bdd9fe6aece29b6ffc268ccedc8630c9c6b58f7864099`,
identical to the built/tested package. xorriso inspection retained bootable BIOS
isolinux and UEFI entries plus the hybrid MBR/GPT/APM structure. This is boot-record
inspection, not a new ISO boot or complete installation test.

The existing [desktop browser review](../controller-desktop-layout/checks.md)
applies to the byte-matching UI source/assets; a new browser walkthrough inside the
appliance was not run. Documentation structure and scoped assessment/link checks
passed locally; no committed-range or hosted CI execution is claimed.

## Limits

This is a local development rebuild, not an external distribution certification.
Complete Debian corresponding-source delivery, firmware/artwork clearance and
hosted CI remain outstanding. No live host is upgraded and no physical disk is
installed. A full fresh ISO installation and host-local Controller deployment/
enrollment walkthrough are not implied by artifact inspection or guest boot.
