# Installer DHCP preview — 2026-09-09

Selecting DHCP now displays searching followed by the server-returned IPv4 for the
selected adapter/VLAN, or a retryable no-address/no-link failure. The native BusyBox
client uses a custom ACK hook and never applies an IP, route or DNS setting. VLAN
checks create and remove only a temporary owned VLAN link. Final installed DHCP
configuration is unchanged and negotiates a fresh lease.

API impact: none — local GTK/helper behavior only; no Controller/Core API or agent
command changes. Guide impact: updated. No new product dependency; the shipped
installer BusyBox already contains udhcpc. Helper source is retained in the GUI source
bundle and installed into the initrd by the existing build recipe.

## Executed checks

Strict GTK2 C compilation passed. `installer/check.sh` and the existing carrier/probe
regression passed. The carrier test now confirms that DHCP work is triggered only by
link transitions rather than every periodic refresh.

```sh
bash tests/integration/installer-dhcp.sh --docker /tmp/nex-rootauth-docker
python3 tests/integration/installer-link.py
```

The final native DHCP script passed all six checks, independently rerun with exit zero:
actual untagged and VLAN42 DHCP ACKs returned the expected addresses; IPv4/IPv6
addresses/routes and resolver contents were unchanged; invalid NIC/VLAN and malformed
hook data were rejected; no-server timeout was bounded; cancellation left no child,
owned temporary VLAN or private working files. This used an isolated Debian13 Docker
network namespace with veth/dnsmasq, not a physical switch or live host network.
Both installer CI workflows invoke the native script and existing C/carrier/GUI checks
on push/PR. No hosted CI execution is claimed.

Independent source review identified transient DHCP text leaking into static editing;
clearing on mode changes corrected it before delivery. Source review also checked
stale-callback suppression, cancellation, child cleanup and unchanged configuration
persistence. Native GUI capture results and artifact details follow below.

## Native GUI and rebuilt ISO

The final rebuilt installer passed `tests/integration/wizard-review.py` using its
shipping kernel and GTK initrd in disposable QEMU/KVM at 1024×768 with two virtio
NICs. The test captures searching, a real QEMU DHCP address (10.0.2.15), disconnected
link and Retry, recovery after reconnect, and switching to a blank editable static
address. Existing hostname, fullscreen, timezone and disk-confirmation checks also
passed. No installation onto the disposable disk was performed.

Manually inspected final captures show readable status text and stable field geometry
while DHCP is pending/completes; Retry is disabled during the request. Independent
review also inspected these captures and confirmed the earlier findings resolved.

- [Searching](dhcp/dhcp-searching.png)
- [DHCP address](dhcp/dhcp-address.png)
- [Disconnected Retry](dhcp/dhcp-retry-no-link.png)
- [Recovered](dhcp/dhcp-recovered.png)
- [Switch to static](dhcp/dhcp-to-static.png)

Rebuilt `dist/nex-core-0.1.0-alpha1-amd64.iso` with the existing usability1 local
package and appliance. SHA-256 verification passed:

```text
08fa6a7c31df8d092a24ee0ce433e462969750fe96f1be1bae9ac6caca03ab66
```

Documentation structural checks and whitespace checks passed. Physical NICs, real
switch VLANs, external DHCP servers and a full installed-host walkthrough were not
tested in this slice. The DHCP address is a preview; the installed host requests
a fresh lease and its address can differ.
