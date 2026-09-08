# Phase 1 Debian and native libvirt verification

## Environment and method

Executed 2026-09-08 against the uncommitted Phase 1 development implementation,
using [the runnable smoke check](../../../tests/integration/phase1-debian.sh).
The check builds both Go binaries, creates a corresponding-source archive, and
runs them together over verified local HTTPS in a new disposable Docker container.
It reuses the API client/process helper from the main acceptance test but uses
Debian's actual `virsh` and `libvirtd`, with **no simulated libvirt command**.

The container uses ordinary Docker isolation, without privileged mode, host
networking, device passthrough, or host service access. Repository and build
artifacts are mounted read-only. Temporary certificates, administrator password,
single-use enrollment token, SQLite state and agent identity remain inside the
container and are removed with it. No existing container or installer lab is
modified. Debian packages install only inside this disposable container.

- Debian 13-slim registry manifest digest: `sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132`.
- Userspace: Debian GNU/Linux 13 (trixie), `DEBIAN_VERSION_FULL=13.6`.
- `libvirt-daemon` and `libvirt-clients`: `11.3.0-3+deb13u2`.
- `qemu-system-x86`: `1:10.0.11+ds-0+deb13u1`.
- Controller SHA-256: `52934ee98479a48b85f3a7ffd3b52fa26412f7dc79b38c25852fc167a0275a59`.
- Agent SHA-256: `ceb40b6a1259e92e6bd5c05efb0270b9f96159074f1ae5efbc89d8ef1f313ec5`.
- Corresponding-source archive SHA-256: `a5375ebfab7a033f39cfd839a545d5bedf87a40cab959ebd3ce6508c30f02726`.

Run from the repository root with Docker available directly or through `sudo -n`,
Go on PATH, at `.cache/toolchains/go/bin/go`, or selected by `GO`,
Python 3, a C compiler and network access for disposable package installation:

```sh
bash -n tests/integration/phase1-debian.sh
bash tests/integration/phase1-debian.sh
```

Both commands exited 0. The pinned image is pulled when absent locally; its
manifest digest was verified against the Docker registry on the test date.
Debian package versions are printed by each run because
APT security updates can change them even when the base image is pinned.

## Inspected results

The check bootstrapped an administrator, created a Site and Pool, issued a token,
and enrolled the native agent with a trusted controller certificate. It then
asserted the following through authenticated Controller API observations:

| Action | Actual observed result |
| --- | --- |
| Start actual `libvirtd` and agent | Fresh connection, healthy libvirt and host inventory checks, Debian 13 OS, nonzero CPU/RAM, NICs and disks; empty VM inventory. |
| Omit `/dev/kvm` device access | KVM check honestly reports `unsupported`. |
| Send `SIGSTOP` to disposable libvirtd | Libvirt check becomes `unknown` after its one-second probe deadline; native inventory stays healthy and heartbeat timestamps continue advancing. |
| Send `SIGCONT` | Libvirt observation returns to healthy. |
| Send `SIGTERM` to disposable libvirtd | Connection remains fresh while the libvirt check reports `failed`. |
| Start a new libvirtd | Libvirt check returns to healthy. |
| Stop the agent | Controller reports a stale management connection after its two-second freshness window. |

All five grouped PASS lines printed, including every assertion above. The script's
EXIT trap removed its disposable container and temporary build directory.

An earlier run failed because the then-current Controller rejected native CPU
flag inventory with HTTP 400. The Controller owner identified its too-small
virtualization-array bound; rebuilding after that correction produced the passing
run recorded here. That earlier failure is not counted as successful verification.

## Limits and impact

This validates Debian userspace, actual local libvirt API availability/failure,
bounded stalled subprocess handling and the Controller/agent observation path.
Docker shares the workstation kernel; CPU/RAM and sysfs observations describe
that environment, not certified physical Nex Core hardware. Libvirt emitted
expected absent-device/udev warnings in this constrained environment.

No guests were created or started. This is **not** KVM acceleration, VM persistence,
controller-outage guest survival, local-console recovery, systemd service lifecycle,
installer, physical disk, storage correctness or release certification evidence.
The separate process/browser acceptance report covers its own explicitly labeled
simulation and restart scenarios. Later implementation changes require rerunning
this check before assigning the same evidence to different binaries.

API impact: **existing contract** — the smoke uses the implemented administrator,
enrollment and authenticated agent-report contracts without changing behavior.
Guide impact: **none** for these test/evidence files; they record verification,
not a new operator workflow.
