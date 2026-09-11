# Pool VLAN networks: checks (2026-09-09)

Scope: [pool VLAN networks plan](../../plans/2026-09-09-pool-vlan-networks.md). Development
tree after the controller desktop layout commit `1fb9782`, uncommitted working tree.
Workstation: Linux 7.2.3 (CachyOS), Go 1.27.1, Node/npm from the repository UI toolchain,
Chromium via the `.cache/phase1-qa-venv` Playwright 1.62 environment used by CI.

## Automated checks (all executed)

| Check | Command | Result |
| --- | --- | --- |
| Go services incl. new VLAN shape, rendering, Controller derivation/conflict/inventory tests | `go test -race ./...` | pass |
| Contract/tool checks (OpenAPI ↔ Go request fields, command kinds) | `python3 scripts/generate_api.py --check`; `python3 -m unittest discover -s tests -p 'test_*.py'` | pass |
| UI | `npm --prefix web run typecheck && npm --prefix web run build` | pass |
| Installer interfaces writer incl. new `source` header line | `cc … tests/integration/installer-network.c installer/gui/nex-network.c` then run | pass |
| Real bridge creation in a disposable namespace | see below | pass |

### Namespace check of real bridge creation

`TestGuestVLANNetns` ran inside `unshare -Urnm` with sysfs remounted, as an unprivileged
user, so nothing on the workstation's real networking was touched. Log:
[netns-test.log](netns-test.log). It observed, on dummy interfaces:

- VLAN device `d0.100`, bridge `d0v100`, port attached (`/sys/class/net/d0v100/brif/d0.100`),
  all links up, stanza written to `interfaces.d/nex-d0v100`, `source /etc/network/interfaces.d/*`
  appended after the untouched installer content; second apply idempotent; VM-create
  reverification passes.
- Refused without side effects: VLAN 200 already enslaved to another bridge; untagged on a
  NIC with an address; untagged on a NIC configured `inet dhcp` in the interfaces file;
  missing uplink; a bridge as uplink; a VLAN device as uplink.
- Untagged guest bridge `d3v0` on an unused NIC that is up (link-local IPv6 present) succeeds;
  `/etc/network/interfaces` keeps mode 0644.
- A bond member is refused and the bond itself is accepted as an uplink (when the namespace
  can create bonds; the log records whether it could).

This is a simulation of the host's network stack with dummy NICs. It proves the agent's
device handling and persistence logic, not a physical NIC, switch trunk, or reboot-time
`ifup` ordering on a real Nex Core host.

### Simulated browser walkthrough

`tests/integration/managed_vm_browser.py --simulated --evidence …` with a simulated mTLS agent
reporting one physical NIC `nex1` (results: [browser-simulated-results.json](browser-simulated-results.json)).
Inspected captures:

- [Register guest network form](browser-simulated-network-form.png): **Connection** lists
  `nex1 · physical · up · MAC`, **VLAN ID** 100, derived bridge name in the help text.
- [Pool guest networks table](browser-simulated-network.png): `VLAN 100 on nex1`, bridge
  `nex1v100`, VM count and succeeded host check.
- [VM review page](browser-simulated-review-desktop.png): guest network shown as
  `name · VLAN 100 on nex1`; the four-page creation flow, keyboard use, 360 px layout,
  lost-reply retry and stale/error injection checks passed as before.

The simulated agent marks every command succeeded; it does not execute bridge creation.

### Default networks and adoption of host-held pool state (later the same day)

Controller adoption tests in `internal/controller` (`TestHostReportedNetworksAreReadopted`,
`TestDefaultUntaggedNetworkAutoRegistration`) and the vmhost `Applied()` journal test cover:
one automatic `nex0 untagged` registration for `br0` (old agents get nothing, the Nex VLAN
bridge is not auto-registered, a failed automatic registration is not recreated, a manual VLAN
still registers), and adoption of storage, networks, media and VM definitions reported in
`Inventory.managed` with their IDs and names, invalid entries skipped, no duplication on a second
report. Not exercised natively: the packaged agent reporting `Inventory.managed`, and a
Controller replacement. No native Controller-replacement walkthrough has run.

### Native lab attempt

A new blank-disk lab under the repository cache booted the rebuilt ISO (`iso.json`) to the
installer review page (`installer-*.png`); installation was not started and the lab was
stopped. `tests/integration/vlan_lab.py` is the prepared native harness and has not run.

### Replacement enrollment

`TestEnrollWithReplacementController` (internal/agent) and
`TestEnrollmentWithReplacementControllerSucceeds` (internal/localcore) prove an enrolled host
switches to a Controller with a different URL/trust in place, keeps its state directory, drops
pending results, refuses `--replace-host` across Controllers, and still answers 409 for the same
Controller. No real two-Controller replacement has been walked through.

### Controller replacement with real processes

`tests/integration/controller_replacement.py` ran inside `unshare -Urnm` with sysfs remounted and a
namespace-private bridge `nex-test` (evidence: [controller-replacement.json](controller-replacement.json)).
Real `nex-controller` and `nex-core-agent` binaries built from this tree: Controller A enrolled the
agent; the agent applied a pool network (existing bridge, read-only check); Controller A's process
was stopped and its data directory deleted; Controller B started empty on a new port with a new
certificate; an ordinary `nex-core-agent enroll` against B took a new host identity in the same
state directory; B listed the network with the original ID and name under the new pool/host,
operation actor `system`, request `adopt-network:…`, status succeeded, with no queued or dispatched
command and no duplicate after further reports. Both CI workflows run this step. Run as root
with `--storage-root`, the same script also covers storage (real root-owned directory) and an
uploaded ISO record, and a new ISO transfer onto the adopted storage on Controller B
([controller-replacement.json](controller-replacement.json)).

**All four kinds with real libvirt and KVM:** `tests/integration/controller_replacement_debian.sh`
runs the same script inside a Debian 13 container (libvirt 11.3.0, QEMU 10.0, `/dev/kvm` passed
through, NET_ADMIN only inside the container, a named volume for storage). Controller A defined a
stopped VM through the real agent; Controller B adopted storage, network, ISO record and VM
definition with their IDs, observed the VM `shut off` from fresh libvirt data, and then created a
new ISO transfer and a new VM on the adopted resources
([controller-replacement-debian.json](controller-replacement-debian.json),
[environment](controller-replacement-debian-environment.txt)). This run found and fixed
BUG-0039 (blocking `virsh dominfo` probe). Both CI workflows run it; when the runner has no
`/dev/kvm` the step prints a notice and covers the three non-VM kinds. Not covered: the
packaged agent on an installed host, and the browser **Add host** flow against a replacement
Controller.

## Not exercised

- Native nested-KVM lab (`--native`) and the fresh-install lab: the packaged agent applying a
  VLAN bridge on a real Debian host, service `ReadWritePaths=/etc/network`, and bridge
  persistence across a host reboot.
- Physical hardware and switch trunking; guest connectivity through a tagged VLAN.
- The "Other" (existing bridge / libvirt network) path in the browser after the form change;
  it reuses the previous native evidence and the same API fields.
- Guest tools: assessed in the plan only; nothing implemented.
