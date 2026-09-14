# Installer DHCP preview — 2026-09-09

Bug: BUG-0025 ([ledger](../../BUG_LEDGER.md)). Selecting DHCP shows searching, then the server-returned
IPv4 for the adapter/VLAN or a retryable no-address/no-link failure. BusyBox udhcpc (already shipped)
with a custom ACK hook never applies IP, route or DNS; VLAN checks create and remove only a temporary
owned link. Installed DHCP config unchanged and takes a fresh lease. Helper source is in the GUI source bundle.

## Executed checks

```sh
bash tests/integration/installer-dhcp.sh --docker /tmp/nex-rootauth-docker
python3 tests/integration/installer-link.py
```

| Check | Result |
|---|---|
| Strict GTK2 compile, `installer/check.sh`, carrier/probe regression | Passed; DHCP triggers only on link transitions, not every refresh |
| Native DHCP script, isolated Debian 13 Docker namespace with veth/dnsmasq (independently rerun, exit 0) | All six passed: untagged and VLAN42 ACKs returned expected addresses; IPv4/IPv6 addresses/routes and resolver unchanged; invalid NIC/VLAN and malformed hook data rejected; no-server timeout bounded; cancellation left no child, VLAN or private files |
| Independent source review | Found DHCP text leaking into static editing; fixed before delivery. Also checked stale callbacks, cancellation, child cleanup, config persistence |

Both installer workflows invoke the native script and C/carrier/GUI checks; no hosted CI claimed.

## Native GUI and rebuilt ISO

`tests/integration/wizard-review.py`, shipping kernel and GTK initrd, disposable QEMU/KVM, 1024×768,
two virtio NICs: passed searching, real QEMU DHCP address 10.0.2.15, disconnected link with Retry,
recovery, switch to blank static address, plus hostname, fullscreen, timezone and disk-confirmation
checks. No install onto the disk. Final captures (root and independent reviewer inspected): readable
status, stable geometry, Retry disabled during request. Captures (not committed): `dhcp/dhcp-searching.png`,
`dhcp-address.png`, `dhcp-retry-no-link.png`, `dhcp-recovered.png`, `dhcp-to-static.png`.

Rebuilt `dist/nex-core-0.1.0-alpha1-amd64.iso` with the existing usability1 local package and
appliance; SHA-256 verified `08fa6a7c31df8d092a24ee0ce433e462969750fe96f1be1bae9ac6caca03ab66`.
Docs structure and whitespace checks passed.

Not tested: physical NICs, real switch VLANs, external DHCP servers, full installed host. The preview
address can differ from the installed host's fresh lease.
