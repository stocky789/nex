# Security package: real disposable appliance verification

Date: 2026-09-08. Clean deployment, security/outage checks, host autostart and post-reboot browser
verification passed. Disposable VMs shut down.

## Environment and artifact

- Private copy of the earlier disposable Core wizard disk: Debian 13, QEMU 11.1.1/KVM, 4 vCPU,
  8 GiB; new 80 GiB regular-file disk for local ext4 storage. Nested appliance on the guest's
  isolated `virbr0` NAT bridge, static `192.168.122.10/24`. Earlier appliance labs not reused.
- `apt-get install /root/security.deb` after refreshing apt; setup selected management `10.0.2.15`,
  storage `/var/lib/nex/appliances`, bridge `virbr0`. Service retained `UMask=0077`. Browser via
  loopback QEMU/SSH forwards; certificate fingerprints compared after reboot.
- Package, local binary, signed appliance, Controller binary and source hashes are in
  [compiler checks](compiler.md#final-controller-artifact) (package checks page removed; see git history);
  [installed artifact output](lab/installed-artifacts.txt) and [guest hashes](lab/guest-artifacts.txt)
  match them.

## Executed flow and results

```sh
.cache/phase1-qa-venv/bin/python tests/integration/appliance-lab.py \
  --lab-dir .cache/nex-appliance-lab.<redacted> \
  --evidence docs/qa/security/lab --deploy --packaged --security
```

Real HTTPS services and libvirt/QEMU, no HTTP mocks or guest patches.

| Check | Result |
| --- | --- |
| 16-code-point Unicode password (non-ASCII) | Accepted by GUI, API, NoCloud init and Controller login |
| Auth, anonymous inventory, CSRF, invalid input, modified signed manifest | Rejected before VM creation |
| Deployment progress | 0, 10, 92, 98, verified 100; one VM `private-id-037`, one Site, Pool, host, adopted Controller VM |
| Cookies | `__Host-`, Secure, HttpOnly, SameSite=Strict, root path (values not recorded) |
| Headers | no-store, nosniff, no-referrer, frame denial, CSP without unsafe-inline/eval; GUI works under it |
| Six bad logins with changing `X-Forwarded-For` | Controller 5×401 then 429; local 4×401 then 2×429 (after its successful login). Peer-based gate; no claim headers identify clients |
| Bootstrap config, password, cloud-init caches | Absent before reboot |
| Controller outage | Running VM and local access retained |
| Wrong typed shutdown confirmation | Rejected; graceful shutdown/cold start kept static network and identity without seed disk |
| Local management stopped | Stale observations, power controls disabled, Controller HTTPS up; restart reacquired lock, one VM, one deployment operation |
| Desktop and 390px layouts | No overflow or uncaught errors; loading, empty, invalid-login, outage states captured |

[Results](lab/results.json), [headers/cookies/rate limits](lab/security.json),
[deployment progress](lab/deployment.json), [Controller inventory](lab/controller-inventory.json),
[cold-start output](lab/controller-cold-start.txt). Captures `lab/deployment-ready.png` and
`lab/local-service-offline.png` are git-ignored and not in the repository.

## Host reboot and cleanup

Outer-host reboot changed boot ID and autostarted local management and the same appliance UUID
([output](lab/host-reboot.txt)). Sign-in accepted the Unicode password; Site/Pool/host/VM
identities, deployment TLS pin and `/source` archive retained; agent fresh, VM running
([assertions](lab/host-reboot-results.json), [inventory](lab/host-reboot-inventory.json)).
Capture `lab/host-reboot-controller.png` inspected (git-ignored).

Appliance shut down gracefully, outer host powered off, QEMU exited, disk preserved
([cleanup](lab/shutdown.json)). Earlier four appliance labs remained stopped.

## Limits

Nested KVM with isolated NAT, not physical hardware or production LAN. Separate-host recovery last
ran on appliance 6/package 6.1, not security1; no cross-version restore. Idle-session migration
and clock expiry covered by focused Go/browser checks, not a 30-minute wait. Release clearance and
hosted CI are separate gates.
