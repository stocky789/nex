# Phase 6 disposable-lab nex-core-local install (Path A)

Date: 2026-09-27. Scope: install `nex-core-local` on the existing disposable
recovery Core lab (fixture `cluster-ha-v1`, ignored `.cache/recovery-s0-core.*`
run directory) so Phase 6 product backup API smoke can proceed. Authorized
disposable lab only. Storage guest was not modified. No disks wiped. No Git
commit.

**Verdict: install succeeded on core1, core2, and core3.** Each host has
`nex-core-local.service` active and HTTPS listening on `:9443`. Product backup
API smoke ran later; see
[phase6 product backup lab](2026-09-27-phase6-product-backup-lab.md).

## Path A summary

1. Rebuilt worktree `nex-core-local` into ignored
   `.cache/phase6-s4-walkthrough/nex-core-local` (`go build … ./cmd/nex-core-local`).
2. Confirmed prebuilt package `nex-core-local_0.4.12_amd64.deb` exists.
3. Package contents: single management binary at `/usr/lib/nex/nex-core-local`
   (embedded agent). No separate `nex-core-agent` binary in the deb; standalone
   `./cmd/nex-core-agent` was not deployed.
4. For core1/core2/core3 only: SCP package + Phase 6 binary, ensure
   `/etc/nex/core-release`, noninteractive `apt-get install` of the deb, stop
   unit, replace `/usr/lib/nex/nex-core-local` with the Phase 6 binary, start
   unit, verify `is-active` and `:9443`.

## Lab targets

| Guest alias | SSH (loopback) | Install |
|---|---|---|
| core1 | `127.0.0.1:56209` | success |
| core2 | `127.0.0.1:58477` | success |
| core3 | `127.0.0.1:60563` | success |
| storage | `127.0.0.1:37101` | **not installed** (confirmed no Core binary) |

## Artifacts

| Artifact | Identity |
|---|---|
| Package | `nex-core-local` `0.4.12` `amd64` |
| Package SHA-256 | `c09eee0fdc1e5754de5f9ed575078f3c29a937d16a3e6351908b07e13fc82f53` |
| Packaged binary SHA-256 (inside deb) | `f7e813fdb5541a955131b8b8d0501c9f197c0f25f1b06b1d28b4898c432caa69` |
| Phase 6 overlay binary SHA-256 (initial Path A) | `edfe42c236050cf8c2908ef6c9b591265eb9a35416d5e12cce2f22142f6a9f55` |
| Phase 6 overlay binary SHA-256 (replace-cutover redeploy) | `49bffb9dd583eaf1a04013ab46196f88359b07fc8e76d06aa318b96f1d1f2e5c` |
| Build | `go1.27.1` worktree short rev `edfc14a` |

The running `/usr/lib/nex/nex-core-local` on all three Core guests matches the
**replace-cutover redeploy** digest above after the later rebuild/SCP/restart
(package binary was overwritten after apt; then overwritten again for product
backup smoke). See [phase6 product backup lab](2026-09-27-phase6-product-backup-lab.md).

## Per-host results

### core1 (`127.0.0.1:56209`)

- `/etc/nex/core-release` already present (`NEX_CORE=1`, `INSTALLED_BY=nex-core-iso`).
- `apt-get install` of `nex-core-local_0.4.12_amd64.deb` succeeded.
- Overlay install + `systemctl enable --now nex-core-local` succeeded.
- Observed: `dpkg` `ii nex-core-local 0.4.12`; service `active`/`enabled`;
  listen `10.0.2.15:9443` (`nex-core-local`); binary SHA-256 initially
  `edfe42c2…a9f55`, later redeployed to `49bffb9d…2e5c`.

### core2 (`127.0.0.1:58477`)

- Wrote disposable lab stamp `/etc/nex/core-release`
  (`NEX_CORE=1`, `INSTALLED_BY=nex-core-iso`) before apt (preinst requires it
  for fresh installs).
- Apt, overlay, and service verify matched core1 (success).

### core3 (`127.0.0.1:60563`)

- Same stamp write as core2, then apt + overlay + verify (success).

### storage (`127.0.0.1:37101`)

- Left alone. Spot-check: no `nex-core-local` package/binary.

## Notes

- Apt pulled cluster/HA client dependencies (pcs, pacemaker helpers, fence
  agents, and related packages) as declared by the package Depends. No apt
  failure occurred.
- Postinst diverted `/etc/os-release` and enabled Debian contrib for OpenZFS
  recommendations; guests remain disposable lab images.
- Local state dirs `/var/lib/nex/core-local` (TLS material present) and the
  embedded-agent default `/var/lib/nex-core-agent` are package/setup owned.
  Pool enrollment and managed VM inventory were not established in this step.

## Next steps (product backup API smoke)

Product backup API smoke (repository registration, stopped backup-now,
restore-as-new) was completed in
[phase6 product backup lab](2026-09-27-phase6-product-backup-lab.md). Remaining
open items: online crash backup live pass, policy schedule with Controller
absent, and Campaign A/B if still required for Phase 6 closeout. Keep storage
guest as NFS/SMB/iSCSI fixture only; do not install Core there.
