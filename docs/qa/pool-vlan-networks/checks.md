# Pool VLAN networks: checks (2026-09-09)

Scope: [pool VLAN networks plan](../../plans/2026-09-09-pool-vlan-networks.md). Uncommitted tree
after `1fb9782`. Linux 7.2.3 (CachyOS), Go 1.27.1, repository UI toolchain, Playwright 1.62 Chromium
in `.cache/phase1-qa-venv`.

## Automated checks (all executed, all pass)

| Check | Command |
| --- | --- |
| Go incl. VLAN shape, rendering, Controller derivation/conflict/inventory | `go test -race ./...` |
| OpenAPI ↔ Go fields, command kinds | `python3 scripts/generate_api.py --check`; `python3 -m unittest discover -s tests -p 'test_*.py'` |
| UI | `npm --prefix web run typecheck && npm --prefix web run build` |
| Installer interfaces writer incl. `source` line | `cc … tests/integration/installer-network.c installer/gui/nex-network.c`, then run |

## Simulated and real-process checks

| Check | Observed | Evidence |
|---|---|---|
| `TestGuestVLANNetns` in `unshare -Urnm` (sysfs remounted, unprivileged, dummy NICs) | `d0.100` + bridge `d0v100` with port, links up, `interfaces.d/nex-d0v100` written, `source /etc/network/interfaces.d/*` appended after installer content; reapply idempotent; VM-create reverification passes. Refused without side effects: VLAN already on another bridge, untagged on addressed NIC, untagged on `inet dhcp` NIC, missing uplink, bridge or VLAN device as uplink. Untagged `d3v0` on unused up NIC succeeds; `/etc/network/interfaces` stays 0644. Bond member refused, bond accepted (log records if bonds were creatable). Simulation: not a physical NIC, trunk or reboot `ifup` ordering | [netns-test.log](netns-test.log) |
| `managed_vm_browser.py --simulated` (agent reports NIC `nex1`; marks all commands succeeded, no bridge creation) | Network form lists `nex1 · physical · up · MAC`, VLAN 100, derived bridge in help; table `VLAN 100 on nex1`, bridge `nex1v100`, VM count, host check succeeded; review shows `name · VLAN 100 on nex1`; four-page flow, keyboard, 360 px, lost-reply retry, stale/error injection passed. Captures inspected: `browser-simulated-network-form.png` (not committed), `browser-simulated-network.png` (not committed), `browser-simulated-review-desktop.png` (not committed) | [browser-simulated-results.json](browser-simulated-results.json) |
| Default networks and adoption: `TestHostReportedNetworksAreReadopted`, `TestDefaultUntaggedNetworkAutoRegistration`, vmhost `Applied()` journal test | One automatic `nex0 untagged` for `br0` (none for old agents; Nex VLAN bridge not auto-registered; failed auto registration not recreated; manual VLAN still registers); storage, networks, media, VM definitions from `Inventory.managed` adopted with IDs/names, invalid skipped, no duplicates | — |
| Native lab attempt | Blank-disk lab booted the rebuilt ISO to installer review; install not started, lab stopped. `tests/integration/vlan_lab.py` prepared, **not run** | [native/iso.json](native/iso.json); installer captures local only |
| Replacement enrollment: `TestEnrollWithReplacementController` (agent), `TestEnrollmentWithReplacementControllerSucceeds` (localcore) | Host switches Controller URL/trust in place, keeps state dir, drops pending results, refuses `--replace-host` across Controllers, 409 for same Controller | — |
| `tests/integration/controller_replacement.py` in `unshare -Urnm`, bridge `nex-test`, real binaries | Controller A enrolled agent, network applied (existing bridge, read-only); A stopped and data deleted; B empty on new port/cert; ordinary enroll to B (new host identity, same state dir); B lists network with original ID/name, actor `system`, request `adopt-network:…`, succeeded, no queued command or duplicate. As root with `--storage-root`: also storage, uploaded ISO record and new ISO transfer on B. Runs in both CI workflows | [controller-replacement.json](controller-replacement.json) |
| `tests/integration/controller_replacement_debian.sh` (Debian 13, libvirt 11.3.0, QEMU 10.0, `/dev/kvm`, container-only NET_ADMIN, named volume) | A defined a stopped VM via real agent; B adopted storage, network, ISO record, VM definition with IDs, observed `shut off`, created new ISO transfer and VM on adopted resources. Found BUG-0039. Both CI workflows; without `/dev/kvm` covers three non-VM kinds | [results](controller-replacement-debian.json), [environment](controller-replacement-debian-environment.txt) |

## Not exercised

- Packaged agent: applying a VLAN bridge on a real Debian host, `ReadWritePaths=/etc/network`,
  persistence across reboot, reporting `Inventory.managed`; browser **Add host** to a replacement
  Controller.
- Physical hardware, switch trunking, guest connectivity over a tagged VLAN.
- Browser "Other" (existing bridge / libvirt network) path after the form change.
- Guest tools: plan assessment only.
