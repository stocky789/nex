# Pool VLAN networks and guest tools direction

Status: implemented for one-host pools on 2026-09-09; guest tools are assessed here, and
slice 1 (observe) is implemented in [console features](2026-09-09-console-features.md). Owner: project owner. Parent: [platform plan](2026-09-08-nex-platform.md), Phase 2 item 2
and Phase 4 item 4.

## Problem

Registering a guest network meant typing an existing bridge or libvirt network name per host.
project owner found that too complex: operators think in "VLAN 100 on the trunk port", not in bridge
names, and VM creation should pick a network by name.

## Decision

A pool network is **name + uplink NIC name + VLAN ID** (0 = untagged), on the model where every
host's Ethernet ports carry the same names (project owner, 2026-09-09). Nex Core
already names NICs `nex0`, `nex1`, … by MAC binding, so the uplink name identifies the same
physical network on every host. Core materializes the network as one Linux bridge per
(uplink, VLAN), named `<uplink>v<VLAN>` (for example `nex1v100`) over the 802.1Q
sub-interface `nex1.100`, persisted under `/etc/network/interfaces.d/nex-<bridge>`.

This is deliberately additive: the management bridge `br0`, the untagged path of the
management NIC, host addresses and routes are never modified, so applying a guest VLAN cannot
cut off management access. It therefore does not need the Phase 4 staged-apply/timed-recovery
gate, which still applies to editing host interfaces, addresses, bonds or the management VLAN.
Core refuses, without touching anything, an uplink that is a bridge/VLAN device/loopback, a
VLAN device already enslaved to another bridge (for example the management VLAN), and an
untagged registration on a NIC that carries host addresses or is configured with an address
in `/etc/network/interfaces`. A succeeded check proves the bridge exists with its port; it does
not prove the switch trunks that VLAN.

Existing shapes remain available under "Other" in the form: an existing bridge or an active
libvirt network by name. Removal of a network registration is not implemented (no registration
kind has deletion yet); the bridge stays until an operator removes its interfaces.d file.

Bridge VLAN filtering on `br0` was rejected for now: it would require enabling filtering on
the management bridge and per-tap `bridge vlan` hooks that libvirt does not manage.

## Default network and Controller recovery (project owner, 2026-09-09)

project owner asked why no untagged (VLAN 0) network exists by default, why one VLAN shows two host
interfaces, and whether everything comes back from the pool master if Controller dies.

- Every host bridge on a physical NIC or bond gets an automatic pool network
  `<port> untagged` (kind bridge, uplink recorded). Operators should never have to register
  the management LAN. Revised 2026-09-10 (architecture invariant): the host self-registers it
  in its managed journal, with host-generated IDs, at agent start and when a new uplink
  appears; Controller only adopts it. Registration conflicts and VM name/MAC/dedicated-memory
  admission are likewise the host's checks, reported as failed tasks.
- The 802.1Q sub-interface plus per-VLAN bridge is ordinary Linux plumbing: one bridge per
  VLAN. The host page now shows physical NICs and what they carry;
  bridges, VLAN devices and VM ports are folded into an "All interfaces" list.
- Core reports every succeeded registration and VM definition from its durable journal
  (`Inventory.managed`: storage, networks, media, VMs) and Controller adopts unknown ones
  with their IDs and names under actor `system` (`adopt-<kind>:<host>:<id>`); Core resolves
  VM-create references by resource ID so creation works under a new Controller. All four
  resource kinds therefore come back once the master connects to a replacement Controller,
  as the [replaceable Controller decision](2026-09-08-nex-platform.md#replaceable-controller--2026-09-09)
  requires. Still open: pool membership and master role for multi-host pools (Phase 4) and
  backup/restore of Controller-scoped data (sites, accounts, audit, tokens, CA).

## API impact: changed contract

- `POST /api/v1/networks` accepts `kind: "vlan"` with `uplink` and `vlan`; Controller derives
  `source` (the bridge name) so replays compare identically, validates the VLAN range and
  uplink name length (IFNAMSIZ), and rejects uplinks the host has not observed as a physical
  NIC or bond (409). Mixed shapes are 400. A VLAN registration and a plain bridge registration
  of the same bridge name conflict (409).
- `NetworkRegistration` (list, command payload) gains `uplink` and `vlan`.
- `network-register` command: Core creates and persists the bridge idempotently; reconnect
  reconciliation re-applies it. Domain XML attaches VLAN networks as `type="bridge"`.
- `Inventory.nics[].kind` (physical | bond | bridge | vlan | loopback | virtual) is added so
  the form can offer uplinks. Upgrade Controller before Core, as already documented.
- Agent service: `ReadWritePaths=-/etc/network`; installer writes
  `source /etc/network/interfaces.d/*` and Core appends it on already-installed hosts.

Reference: [docs/API.md](../API.md#managed-vm-creation), `docs/api/openapi.json`.

## Checks

- `go test ./internal/vmhost ./internal/controller`: shape validation, ifupdown stanza
  rendering, domain XML, Controller derivation/conflict/inventory rules.
- `TestGuestVLANNetns` under `unshare -Urnm` with sysfs remounted (CI: `sudo unshare -nm`):
  real dummy uplink, VLAN device, bridge creation, persistence, idempotent re-apply, and six
  refusal cases that leave existing devices untouched. This is a namespace simulation of the
  host, not a physical switch test.
- Simulated browser walkthrough registers VLAN 100 on the simulated host's `nex1` and creates
  a VM on it (`tests/integration/managed_vm_browser.py --simulated`).
- Not run: native lab (`--native`) and physical hardware; see the guide's verification section.

## Follow-on

- Phase 4 member join: check that a joining host observes every uplink name used by the
  pool's VLAN networks and materialize the pool's networks on it; report per-host status.
- Network removal with in-use protection; per-VM multiple NICs; bonds as uplinks in the UI.
- Show guest-reported IPs per network once guest agent reporting lands (below).

## Guest tools assessment (requested 2026-09-09)

Question: can Nex ship its own guest tools so Controller can change a VM's IP address on
Linux and Windows guests?

Answer: yes, and the transport already exists. Every managed VM is defined with the
`org.qemu.guest_agent.0` virtio-serial channel, and the appliance deployment already calls
`guest-network-get-interfaces` through it. The QEMU guest agent (`qemu-guest-agent` on
Debian/Ubuntu/RHEL family; included in the virtio-win guest tools installer on Windows)
provides, on both platforms: OS/hostname/interface/IP reporting, `guest-exec` /
`guest-exec-status` for running a command, `guest-file-*`, freeze/thaw for consistent backups,
and graceful shutdown. The channel is host-local, so authentication and authorization stay at
the Controller API boundary and the agent command path (host mTLS), with no new guest-facing
network service.

Recommended slices, in order:

1. **Observe:** Core reports per-VM guest-agent availability, OS, hostname and IP addresses;
   Controller shows them on VM details and next to each pool network. Read-only; no new guest
   software beyond qemu-guest-agent.
2. **Configure IP:** Controller operation "Set guest address" (interface, IPv4/prefix,
   gateway, DNS or DHCP) executed through `guest-exec`: Windows via PowerShell
   (`New-NetIPAddress`/`Set-DnsClientServerAddress`), Linux via a small distro-aware script
   shipped as a **Nex Guest Tools** package (deb/rpm depending on qemu-guest-agent) that
   handles netplan, NetworkManager and ifupdown. The Windows package bundles virtio drivers,
   qemu-ga and that script. Confirm before applying; the console remains reachable through the
   host if the guest address change goes wrong.
3. **First boot:** cloud-init / cloudbase-init NoCloud seed for templates, reusing the seed
   ISO code from appliance deployment.

A custom Nex protocol inside the guest is not needed; write one only if guest-exec turns out
to be blocked on a supported guest OS by policy.
