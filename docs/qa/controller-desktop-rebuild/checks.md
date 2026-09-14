# Controller desktop ISO rebuild

Date: 2026-09-09. Result: rebuild, native guest boot, package smoke and independent artifact
verification **passed**. Scope: rebuild appliance and host package with the
[desktop layout](../controller-desktop-layout/checks.md) and select that package for the ISO. API
impact: none (existing contracts shipped). Guide: deployment guide records the artifact set.

## Inputs

Existing appliance/ISO recipes, pinned base digests, retained source snapshots and private
development signing identity (key outside the repository). Cloud base
`debian-13-genericcloud-amd64-20260831-2587.qcow2` SHA-256
`85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3`; reviewer matched its SHA-512 to the
official [HTTPS checksums](https://cloud.debian.org/images/cloud/trixie/20260831-2587/SHA512SUMS) (no
detached signature). ISO builder checks netinst SHA-256 and the signed checksum list.

## Results

| Check | Result |
|---|---|
| Build stages (appliance Go and UI, libguestfs + qcow2 integrity, bundle signature, host package, ISO assembly) | All exit 0; prior usability1 public key retained; manifest signed after assembly |
| Artifacts | `0.1.0+desktop1` appliance and package in `dist/nex-core-0.1.0-alpha1-amd64.iso` (1,630,076,928 bytes), SHA-256 `9800dc2e67c6d3bd06dce84dd88a3917eac34b053dc5dcb5b1b23d4acfa3f214`; `sha256sum --check` passed; [artifacts.json](artifacts.json); logs in `.cache/controller-desktop-rebuild/` |
| `appliance-dhcp-boot.py --image dist/appliance-desktop-test/controller.qcow2 --evidence docs/qa/controller-desktop-rebuild/guest-boot --docker /tmp/nex-rootauth-docker` | Passed: DHCP on management MAC, verified-hostname HTTPS, wrong password rejected, admin login, services active, firstboot credentials and cloud-init secrets removed; overlay/seed/secrets removed ([guest results](guest-boot/results.json)) |
| `core-local-package.sh --deb dist/core-local/nex-core-local_0.1.0+desktop1_amd64.deb --docker /tmp/nex-rootauth-docker` | Passed in fresh Debian 13 container: enablement, explicit start (not systemd boot), TLS IP verification, root PAM, password rotation, wrong/legacy/locked/expired rejection, blocked preflight, defaults, GUI labels, source/API, secret-free logs; removal kept config and storage sentinel |
| Independent review | Ed25519 signature against prior key; image/source digests; 67 source files vs commit `1fb9782`; guest UI assets byte-match package and show new layout; package from ISO SHA-256 `948c315241b40efc2d7bdd9fe6aece29b6ffc268ccedc8630c9c6b58f7864099` identical to tested; xorriso: BIOS isolinux, UEFI, hybrid MBR/GPT/APM (inspection only) |

Browser review applies via byte-matching UI; no new in-appliance browser run. Docs checks passed locally.

## Limits

Local development rebuild: no distribution certification, Debian source delivery, firmware/artwork
clearance or hosted CI. No live host upgrade, disk install, ISO boot, or fresh install/deploy/enroll walkthrough.
