# Nex Core local package verification — 2026-09-08

## Status and artifact

Development verification only. The package embeds a Controller image signed with an
independently supplied **lab-only** key; no release signing identity is configured.
The corrected development candidate below keeps the service's private `UMask=0077`
and explicitly sets QEMU's owned VM directory to `0711`. It reuses the definitive
lab6 appliance. Package/container checks are recorded below; real host/VM acceptance
belongs to the separate disposable-host walkthrough.

- Artifact: `dist/core-local/nex-core-local_0.1.0+lab6.1_amd64.deb`, 711,653,696 bytes.
- Package SHA256: `679197247d98b5130fbdedcf415e0e24ce4235abf1150b3ccc906b7bf0a4304a`.
- Host source: `dist/core-local-source-lab6.1.tar.gz`, SHA256
  `b4228bcd0aa3e5f8143e55ec9a8d9a5a7e0deed09c23897ca64c28387ef6a67c`.
- Embedded appliance: `dist/appliance-lab6`, image SHA256
  `1238b82eb905c40c11b474b8bae7837f52c631296dfcc52d1d70bd57088b7a55`.
- Controller source SHA256:
  `d4fc22c13c6f160f03f59fe65892b10182045de5c6ba2b6a7c3d5dc3ce8221f3`.
  Its independent source archive, `components.tsv`, notices, signed manifest and build
  inputs are retained in the package alongside the separate host source archive.
- Compared packaged host snapshot with current local daemon/engine/UI/package/helper
  and API-reference sources: no build-input drift.

- Container base inspected as `debian@sha256:f324c7ff54321e8d9c588493a20244965938ce0aa50bbd1022d38010e9ffc4b1`; compiler reported `go1.24.4 linux/amd64`.
- Build used the Dockerfile/build script extracted from that host source archive,
  Debian 13 Go/C toolchain and the signed appliance verified against a separately
  supplied 32-byte Ed25519 public key.

## Rejected host-package candidate

`nex-core-local_0.1.0+lab6_amd64.deb` passed its container checks but **failed real VM
startup**. Its SHA256 was
`7a76d00120f69ab49ada8bf472d1d6297f713e42f34fa7ec945285b79f6026d9`, with host-source
SHA256 `2fb1751a1088df887f4995fc75646ae2db9439497b4e85c9916521d8ff18f139`.
The service's `UMask=0077` reduced a requested `0711` VM directory to `0700`, so QEMU
could not traverse it. The failed disposable host and its evidence were preserved;
no manual directory-permission workaround was used to declare it successful.

The engine owner reproduced this through
`TestApplianceDirectoryTraversableUnderPrivateUmask`: it failed before the fix
(mode `0700`) and passed after explicit directory chmod. `go test -race ./internal/vmhost` passed. That regression also checks disk, seed and user-data files
stay `0600` and private state/seed directories stay `0700`. The service umask was not
weakened. Version `0.1.0+lab6.1` contains the correction; the signed guest image did
not need rebuilding.

## Executed checks

`python3 -m unittest discover -s tests -p test_core_local_package.py` passed all seven
checks, without skips. They exercised real OpenSSL password/TLS initialization,
private permissions, restart stability, changed-address certificate SAN, explicit
configuration overriding automatic address selection, management route discovery,
offline recovery errors, image/signature rejection, preserved uninstall policy, and forwarding the root-only
recovery preparation request through the configured daemon arguments.
A Docker stub checked staged nested mount existence; this is orchestration simulation,
not container execution. The initial real build exposed a read-only parent mount
preventing Docker from creating its nested bundle mountpoint; creating that directory
before mounting fixed the failure, and the focused check detects its absence.

The real package build completed successfully after Go compilation and frontend
TypeScript/build checks. A disposable Debian 13 builder-container smoke then:

1. Installed the actual package through APT with its runtime dependencies.
2. Verified the service was enabled for first boot, without a running systemd manager.
3. Ran first-boot setup and observed the container's default-route IPv4 URL and certificate
   fingerprint; started the installed daemon directly.
4. Verified the HTTPS certificate against its management IP, successful administrator
   login/session, local-host HTML at `/`, matching-source gzip at `/source`, and API docs.
5. Confirmed the administrator secret was absent from daemon output and the MOTD.
6. Removed the package and verified configuration, authentication state, and a disposable
   storage sentinel survived. The container was deleted afterward.

The exact retained smoke is [core-local-package.sh](../../../tests/integration/core-local-package.sh).
It requires an explicit `.deb`, creates a fresh disposable `debian:13` container, and
never invokes installation on the workstation:

```sh
tests/integration/core-local-package.sh --deb dist/core-local/nex-core-local_0.1.0+lab6.1_amd64.deb --docker /tmp/nex-sudo-docker
```

An earlier lab2 package was checked in both the Debian 13 build image and a clean
`debian:13` container. The retained script then ran against the earlier lab6 package in a fresh clean
`debian:13` container and passed every check with exit status 0. The corrected
`0.1.0+lab6.1` package was then rerun in another fresh clean Debian 13 container
with explicit `umask 077`; every assertion passed with exit status 0. This establishes the declared runtime dependency set without relying
on extra build-tool packages. The final run also executed the installed recovery
helper's help command and verified authenticated host preflight reports the
2-vCPU/4096-MiB/32-GiB defaults and honest missing-bridge/KVM blockers.

The corrected candidate command was exactly the command above, using a wrapper that executes
`sudo -n docker "$@"`. No assertions were skipped. The temporary container was
removed on completion.

## Limits

These checks do not prove booted systemd operation, real libvirt VM lifecycle, firmware,
storage safety, browser interaction, Controller enrollment, or backup restoration.
Container service startup was explicit; only enablement was checked through systemd.
The package downloads dependencies through Debian APT and does not alone establish
offline installation. No live host installation or network changes were performed.
Matching Nex source is retained, but complete upstream corresponding-source retention
and external redistribution approval remain separate open release gates.
