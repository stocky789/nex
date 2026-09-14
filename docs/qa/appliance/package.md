# Nex Core local package verification — 2026-09-08

## Status and artifact

Development verification only. Embedded Controller image signed with a **lab-only** key; no release
signing identity. Real host/VM acceptance is in the [disposable-host walkthrough](lab.md).

| Artifact | SHA256 |
| --- | --- |
| `dist/core-local/nex-core-local_0.1.0+lab6.1_amd64.deb` (711,653,696 bytes) | `679197247d98b5130fbdedcf415e0e24ce4235abf1150b3ccc906b7bf0a4304a` |
| Host source `dist/core-local-source-lab6.1.tar.gz` | `b4228bcd0aa3e5f8143e55ec9a8d9a5a7e0deed09c23897ca64c28387ef6a67c` |
| Embedded appliance image `dist/appliance-lab6` | `1238b82eb905c40c11b474b8bae7837f52c631296dfcc52d1d70bd57088b7a55` |
| Controller source | `d4fc22c13c6f160f03f59fe65892b10182045de5c6ba2b6a7c3d5dc3ce8221f3` |

- Controller source archive, `components.tsv`, notices, signed manifest and build inputs retained in
  the package with the host source archive. Packaged host snapshot matched current daemon/engine/
  UI/package/helper/API-reference sources.
- Container base `debian@sha256:f324c7ff54321e8d9c588493a20244965938ce0aa50bbd1022d38010e9ffc4b1`,
  `go1.24.4 linux/amd64`; build used Dockerfile/script from the host source archive; appliance
  verified against a separately supplied 32-byte Ed25519 public key.

## Rejected host-package candidate

`nex-core-local_0.1.0+lab6_amd64.deb` (SHA256 `7a76d00120f69ab49ada8bf472d1d6297f713e42f34fa7ec945285b79f6026d9`,
host source `2fb1751a1088df887f4995fc75646ae2db9439497b4e85c9916521d8ff18f139`) passed container
checks but **failed real VM startup** ([BUG-AP-0010](../../BUG_LEDGER.md)). Failed host and evidence
preserved; no manual permission workaround. Regression
`TestApplianceDirectoryTraversableUnderPrivateUmask` failed before and passed after;
`go test -race ./internal/vmhost` passed. lab6.1 contains the fix; image not rebuilt.

## Executed checks

- `python3 -m unittest discover -s tests -p test_core_local_package.py`: 7 passed, no skips (real
  OpenSSL password/TLS init, permissions, restart stability, changed-address SAN, configuration
  override, route discovery, offline recovery errors, image/signature rejection, uninstall policy,
  root-only recovery request forwarding). Docker stub checks staged nested mount (orchestration
  simulation); first real build hit [BUG-AP-0005](../../BUG_LEDGER.md).
- Real package build passed Go compile and frontend typecheck/build.
- [core-local-package.sh](../../../tests/integration/core-local-package.sh) (explicit `.deb`, fresh
  disposable `debian:13`, never installs on the workstation), wrapper runs `sudo -n docker "$@"`:

```sh
tests/integration/core-local-package.sh --deb dist/core-local/nex-core-local_0.1.0+lab6.1_amd64.deb --docker /tmp/nex-sudo-docker
```

Smoke steps: APT install with runtime deps; service enabled (no running systemd manager); first-boot
setup showed default-route IPv4 URL and fingerprint, daemon started directly; HTTPS certificate
valid for management IP, admin login/session, HTML at `/`, gzip source at `/source`, API docs; admin
secret absent from output and MOTD; removal kept configuration, auth state and storage sentinel;
container deleted.

| Package | Container | Result |
| --- | --- | --- |
| lab2 | Debian 13 build image and clean `debian:13` | Passed |
| lab6 | Fresh clean `debian:13` | All checks passed, exit 0 |
| lab6.1 | Fresh clean Debian 13, `umask 077` | All assertions passed, exit 0, none skipped; recovery helper help ran; preflight reported 2 vCPU/4096 MiB/32 GiB defaults and honest missing-bridge/KVM blockers |

## Limits

Not proven: booted systemd operation, real libvirt lifecycle, firmware, storage safety, browser,
enrollment, backup restoration, offline installation (APT downloads deps). Service start was
explicit; only enablement checked via systemd. Complete upstream corresponding-source retention and
redistribution approval remain open release gates.
