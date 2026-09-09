# Add host by address: real lab verification

Date: 2026-09-09. Environment: the disposable nested-KVM lab `nex-fresh-lab` retained from the
[fresh install QA](../../fresh-install/checks.md) and the [pool master lab](../../pool-master/lab/checks.md):
Nex Core 0.1.0-alpha1 installed from the shipped ISO at `10.0.2.15`, the real Controller appliance
deployed through the GUI at `10.0.2.20:8443`, loopback forwards only. Nothing here touched
workstation disks, bridges or production systems.

Binaries: the appliance Controller, API reference and matching source archive were replaced in
place with a build of the **uncommitted working tree** (git HEAD `1fb9782` plus the pool-master
and add-host changes, including the served-certificate trust fix below) made inside
`debian:13-slim` (`x86 ISA needed: x86-64-baseline`); backups are at `/root/add-host-qa-backup-050014`
inside the appliance. The host's `nex-core-local` was already the working-tree build from the
pool-master lab (it includes `POST /api/local/v1/enrollment`). No package was rebuilt, so package
upgrade behaviour is **not** covered.

## Bug found before the walkthrough

Reading the appliance's certificate before testing showed it serves a self-signed
`CN=Nex Controller` HTTPS certificate, while the push path sent the Controller's host-identity CA
as the host's trust. The Go success test had hidden this by signing the test listener with that CA.
Fix: the Controller now pushes its own served certificate (`-tls-cert`, `Config.ServerCertPEM`) and
returns 503 without contacting the host when it has none. `TestAddSiteHostSuccess` now serves a
self-signed listener; with the old value it fails with `certificate signed by unknown authority`.

## 1. Acceptance script against the real host and appliance

`tests/integration/add_host_lab.py --host-address 10.0.2.15 --host-controller-url https://10.0.2.20:8443`
([results.json](results.json)): **12 checks passed**, run twice (before and after
the final UI copy build). Real Chromium against the appliance; no API mocking.

- Probe fingerprint matches the certificate the Nex Core host serves on its local API
- Wrong fingerprint is rejected with 502 before any credential is sent
- Wrong root password is rejected by the host and surfaced as 401
- Adding an already-enrolled host returns 409 naming the existing host_id
- Re-enrolling a host that is not revoked is refused with 409 before any network attempt
- Probe validates the address shape and reports an unreachable address as 502
- Existing host revoked through the Controller API
- Browser panel shows the probed fingerprint for confirmation before adding the host
- Browser re-enrollment succeeded and the password field is gone
- Re-enrolled host kept its identity and pool, remains pool master and reports fresh
- Repeating the push after success is refused with 409, no duplicate host
- Audit records host.add and token creation without any password

Captures inspected: [re-enroll form](browser-re-enroll-form.png) (password masked, Controller
address editable and set to the address the host can reach), [fingerprint confirmation](browser-fingerprint-confirmation.png)
(`sha256:6f92467a…`, `CN=Nex Core local management`, matching the certificate the host serves on
port 9443), [success](browser-re-enroll-success.png) (password field gone; host honestly shown as
`unknown` until its first report), [host fresh](browser-host-fresh.png) (Pool master badge, `fresh`).

## 2. Fresh enrollment of a new host

The lab has one Nex Core host, so a second isolated `nex-core-local` instance was started on it
(`10.0.2.15:9444`, private data/agent directories, own self-signed certificate, same PAM root
login) and pushed as a brand-new host ([fresh-enrollment.json](fresh-enrollment.json)):
probe returned that instance's certificate (`CN=Nex Core second instance QA`); add returned 200
with a new `host_id` and `pool_id`; the automatically created pool is named `nex-fresh-lab` with
`master_host_id` equal to the new host; repeating the push returned 409 `already enrolled` with the
same `host_id`; the new host reported `fresh` through its in-process agent; the root password does
not appear in the audit log. The instance was then stopped, its private state deleted and the host
revoked, leaving a revoked duplicate host and its empty pool in the lab Controller.

## 3. Restart and log hygiene

`systemctl restart nex-core-local` on the host: the service came back `active` and the re-enrolled
host reported `fresh` again within a minute. `journalctl -u nex-core-local` for the session contains
no line matching `token` or `password`.

## Cleanup

Temporary staging files remain in `/root/add-host-qa` on the host. The temporary root SSH drop-in
`/etc/ssh/sshd_config.d/00-0-pool-qa.conf` and `/root/.ssh/authorized_keys` were removed and `ssh`
restarted; `PermitRootLogin no` is the only remaining setting and the lab key is rejected.

## Not covered

Package upgrade, physical hardware, hosts on another L2 segment, an unreachable `controller_url`
from the host (only the Go test covers the 502 mapping), and the manual token fallback in this lab.
