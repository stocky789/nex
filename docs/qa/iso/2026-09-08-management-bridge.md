# Installer management bridge — 2026-09-08

## Scope

Fresh GUI installations create native `br0` from the selected management NIC or
its selected VLAN. The management IP, DHCP client, gateway and DNS policy apply to
br0. Its MAC is pinned to the selected NIC. Other NICs retain their configuration;
offline mode creates no bridge. Minimal text networking is unchanged. This does not
migrate an already installed host or change the workstation network.

API impact: existing contract — automatic local-service discovery reads the default
route's bridge and IP; existing local preflight/deployment contracts consume that bridge.
No new Controller operation, agent command or API shape. Guide impact: updated.

## Regression and build

The generated-configuration assertion failed before the fix (exit 134, missing manual
management VLAN port). Strict C compilation and execution now pass with selected
second NIC, static/DHCP, tagged/untagged, stable MAC, secondary DHCP and offline cases.
Both installer workflows run the C regression and now invoke the isolated native check.
Hosted CI has not been executed.

`installer/check.sh`, installer handoff checks and nine local-package unit checks passed.
The handoff uses mocked installer commands; it is not an installed-boot test.

Rebuilt ISO `dist/nex-core-0.1.0-alpha1-amd64.iso` SHA256:
`b56d4a7ce98ee05cfbc26743cf624e72df6790b3f361dc141c952b2571097faf`.
The manifest includes bridge-utils `1.7.1-4+b1` and root-login local package
`0.1.0+rootauth1`. The ISO build completed successfully with hybrid BIOS/UEFI metadata.

## Native walkthrough

Runnable native check (all networking changes are in isolated disposable containers):

```sh
bash tests/integration/installer-bridge.sh --docker /tmp/nex-rootauth-docker
cc -Wall -Wextra -Werror tests/integration/installer-network.c \
  installer/gui/nex-network.c $(pkg-config --cflags --libs glib-2.0) \
  -o /tmp/nex-network-test
/tmp/nex-network-test
```

Native checks use Debian bridge-utils 1.7.1-4+b1, ifupdown 0.8.44+deb13u1 and
dhcpcd-base 10.1.0-11+deb13u3, matching the installer providers. The fixture omits
`vlan` to verify that the actual included packages suffice. Native network namespace
simulation tests generated configuration; Docker read-only IPv6 sysctl warnings
are a fixture limitation and do not establish IPv6 behavior.

No full fresh installation, external switch VLAN connectivity, physical hardware or
production network migration is established by configuration generation or containers.

## Native GUI review

The rebuilt ISO kernel/initrd ran in a disposable QEMU/KVM VM at 1024×768 with two
NICs. `wizard-review.py --nics 2 --resolution 1024x768` passed native interaction,
timezone dropdowns, shared hostname validation, link refresh, management selection
and disk-confirmation rejection. [Review capture](management-bridge/review.png) was
inspected: `ens3 → br0 (via nex0): dhcp` accurately shows the selected network.
[Invalid hostname](management-bridge/invalid-network-hostname.png) and
[unchecked erase confirmation](management-bridge/unchecked-confirmation.png) remain
visible and block navigation/installation. The disposable disk was not installed.
An earlier test attempt used an ambiguous keyboard mnemonic; the harness now selects
the management dropdown directly. The successful run used the shipping UI unchanged.

ISO checksum verification, documentation structure/link checks (`--all`) and whitespace
checks passed. `--all` is not committed-range coverage; no hosted CI run is claimed.

## Final native result and independent review

The final immutable `installer-bridge.sh` run exited zero: all eight combinations of
static/DHCP, untagged/VLAN42 and initial bringup/ifdown-up recovery passed. Every case
also exercised secondary VLAN43 DHCP without management route/DNS takeover. Assertions
verified bridge MAC/port, host address/default route solely on br0 and actual local-service
discovery returning the bridge/IP. Evidence was inspected from the completed native run.

Independent review found no blockers in staged networking, existing API contract,
workflows, documentation or inspected GUI capture; it separately passed strict C
regression and documentation checks. Full installed-boot/physical-network limits above
remain open; this acceptance covers the generated configuration and native isolated flow.
