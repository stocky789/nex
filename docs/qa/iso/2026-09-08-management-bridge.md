# Installer management bridge — 2026-09-08

Bug: BUG-0024 ([ledger](../../BUG_LEDGER.md)).

## Scope

Fresh GUI installs create native `br0` from the selected management NIC or VLAN; management IP,
DHCP, gateway and DNS apply to br0, whose MAC is pinned to that NIC. Other NICs keep their
configuration; offline mode creates no bridge; minimal text networking unchanged. No migration of
installed hosts.

## Regression and build

| Check | Result |
|---|---|
| Generated-configuration assertion before fix | Failed (exit 134, missing manual management VLAN port) |
| Strict C compile/run after fix | Passed: second NIC, static/DHCP, tagged/untagged, stable MAC, secondary DHCP, offline |
| `installer/check.sh`, handoff checks (mocked installer commands), nine local-package unit checks | Passed; not an installed boot |
| ISO rebuild, SHA256 `b56d4a7ce98ee05cfbc26743cf624e72df6790b3f361dc141c952b2571097faf` | Passed with hybrid BIOS/UEFI metadata; manifest includes bridge-utils `1.7.1-4+b1` and local package `0.1.0+rootauth1` |

Both installer workflows run the C regression and the isolated native check; hosted CI not executed.

## Native walkthrough

```sh
bash tests/integration/installer-bridge.sh --docker /tmp/nex-rootauth-docker
cc -Wall -Wextra -Werror tests/integration/installer-network.c \
  installer/gui/nex-network.c $(pkg-config --cflags --libs glib-2.0) \
  -o /tmp/nex-network-test
/tmp/nex-network-test
```

Isolated disposable containers with Debian bridge-utils 1.7.1-4+b1, ifupdown 0.8.44+deb13u1 and
dhcpcd-base 10.1.0-11+deb13u3 (installer providers); `vlan` omitted to prove included packages
suffice. Docker read-only IPv6 sysctl warnings are a fixture limit and establish nothing about IPv6.

## Native GUI review

Rebuilt ISO kernel/initrd, disposable QEMU/KVM, 1024×768, two NICs: `wizard-review.py --nics 2
--resolution 1024x768` passed timezone dropdowns, shared hostname validation, link refresh,
management selection and disk-confirmation rejection; disk not installed. Captures (not committed):
`management-bridge/review.png` shows `ens3 → br0 (via nex0): dhcp`; `invalid-network-hostname.png`
and `unchecked-confirmation.png` stay visible and block navigation. An earlier attempt used an
ambiguous mnemonic; the harness now selects the dropdown directly, UI unchanged. ISO checksum,
`check_docs.py --all` and whitespace checks passed (not range coverage).

## Final native result and independent review

Final `installer-bridge.sh` exited zero: all eight static/DHCP × untagged/VLAN42 × bringup/ifdown-up
cases passed, each with secondary VLAN43 DHCP and no management route/DNS takeover. Asserted bridge
MAC/port, address and default route only on br0, and local-service discovery returning the bridge/IP.
Independent review found no blockers and reran the C regression and docs checks.

Not established: full fresh install, external switch VLANs, physical hardware, production network migration.
