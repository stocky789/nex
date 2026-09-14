# Add host by address: real lab verification

Date: 2026-09-09. Lab: `nex-fresh-lab` from the [pool master lab](../../pool-master/lab/checks.md)
(Core at `10.0.2.15`, appliance at `10.0.2.20:8443`, loopback forwards). Appliance Controller, API
reference and source archive replaced with an uncommitted-tree build (HEAD `1fb9782` + pool-master +
add-host incl. the trust fix below) from `debian:13-slim`; backups `/root/add-host-qa-backup-050014`.
Host `nex-core-local` was already the pool-master lab build (has `POST /api/local/v1/enrollment`).
No package rebuilt: **package upgrade not covered**.

## Bug found before the walkthrough

The appliance serves a self-signed `CN=Nex Controller` certificate, but the push sent the host-identity
CA as trust; the Go test hid it by signing its listener with that CA. Fix: push the served certificate
(`-tls-cert`, `Config.ServerCertPEM`), 503 without contacting the host if none. `TestAddSiteHostSuccess`
now uses a self-signed listener and fails on the old value (`certificate signed by unknown authority`).

## Results

`tests/integration/add_host_lab.py --host-address 10.0.2.15 --host-controller-url https://10.0.2.20:8443`:
**12 checks passed**, run twice (before/after final UI copy build), real Chromium, no mocking
([results.json](results.json)):

- Probe fingerprint matches the host's local API certificate; wrong fingerprint 502 before credentials
- Wrong root password 401; already-enrolled host 409 naming `host_id`; non-revoked re-enroll 409 before network
- Address shape validated; unreachable address 502
- Host revoked via API; browser shows fingerprint before adding; re-enrollment succeeded, password field gone
- Host kept identity and pool, remains master, reports fresh; repeat push 409, no duplicate
- Audit records `host.add` and token creation without password

Captures inspected (local only): `browser-re-enroll-form.png` (not committed) (masked password, editable
Controller address), `browser-fingerprint-confirmation.png` (not committed) (`sha256:6f92467a…`,
`CN=Nex Core local management`, port 9443), `browser-re-enroll-success.png` (not committed) (host `unknown`
until first report), `browser-host-fresh.png` (not committed).

| Check | Result | Evidence |
|---|---|---|
| Fresh enrollment: second `nex-core-local` on `10.0.2.15:9444` (private state, own cert, same PAM) | Probe returned `CN=Nex Core second instance QA`; add 200 with new `host_id`/`pool_id`; pool `nex-fresh-lab` with new host as master; repeat 409 same `host_id`; reported `fresh`; no password in audit. Instance stopped, state deleted, host revoked (revoked host and empty pool remain) | [fresh-enrollment.json](fresh-enrollment.json) |
| `systemctl restart nex-core-local` | `active`; host `fresh` within a minute; journal has no `token`/`password` lines | — |

## Cleanup and limits

Staging files remain in `/root/add-host-qa`. SSH drop-in `/etc/ssh/sshd_config.d/00-0-pool-qa.conf` and
`authorized_keys` removed; `PermitRootLogin no` only; lab key rejected.

Not covered: package upgrade, physical hardware, other L2 segments, unreachable `controller_url` from
the host (Go test covers 502 only), manual token fallback.
