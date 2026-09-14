# Phase 1 Debian and native libvirt verification

Date: 2026-09-08. Result: **passed** (all five groups).

## Environment and method

[phase1-debian.sh](../../../tests/integration/phase1-debian.sh) builds both binaries and a source
archive and runs them over verified local HTTPS in a disposable unprivileged Docker container (no
host networking or devices; repo mounted read-only) with Debian's real `virsh`/`libvirtd` — **no
simulated libvirt**.

| Item | Value |
|---|---|
| Debian 13-slim manifest digest | `sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132` (verified against registry) |
| Userspace | Debian 13 (trixie), `DEBIAN_VERSION_FULL=13.6` |
| `libvirt-daemon`, `libvirt-clients` | `11.3.0-3+deb13u2` |
| `qemu-system-x86` | `1:10.0.11+ds-0+deb13u1` |
| Controller SHA-256 | `52934ee98479a48b85f3a7ffd3b52fa26412f7dc79b38c25852fc167a0275a59` |
| Agent SHA-256 | `ceb40b6a1259e92e6bd5c05efb0270b9f96159074f1ae5efbc89d8ef1f313ec5` |
| Source archive SHA-256 | `a5375ebfab7a033f39cfd839a545d5bedf87a40cab959ebd3ce6508c30f02726` |

Requires Docker (direct or `sudo -n`), Go (PATH, `.cache/toolchains/go/bin/go` or `GO`), Python 3,
a C compiler and network access. Package versions are printed per run (security updates change them).

```sh
bash -n tests/integration/phase1-debian.sh
bash tests/integration/phase1-debian.sh
```

Both exited 0; the EXIT trap removed the container and build directory.

## Results

After bootstrap, site/pool, token and trusted-certificate enrollment, observed via the Controller API:

| Action | Observed |
| --- | --- |
| Start `libvirtd` and agent | Fresh connection; healthy libvirt/inventory; Debian 13; nonzero CPU/RAM, NICs, disks; empty VM list |
| No `/dev/kvm` | KVM check `unsupported` |
| `SIGSTOP` libvirtd | Libvirt `unknown` after 1s deadline; inventory healthy; heartbeats advance |
| `SIGCONT` | Libvirt healthy |
| `SIGTERM` libvirtd | Connection fresh, libvirt `failed` |
| New libvirtd | Libvirt healthy |
| Stop agent | Stale after 2s freshness window |

Failed earlier run: Controller rejected native CPU flags with HTTP 400 (array bound); fixed, rerun
passed. Not counted as verification.

## Limits

Docker shares the workstation kernel; expected absent-device/udev warnings. No guests: not KVM
acceleration, VM persistence, outage guest survival, local console, systemd, installer, disk,
storage or release evidence. Rerun before attributing to different binaries. API impact: existing
contract. Guide impact: none (verification only).
