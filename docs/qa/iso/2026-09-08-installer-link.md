# Installer link-status verification

2026-09-08, Nex Core 0.1.0-alpha1. Bug: BUG-0019 ([ledger](../../BUG_LEDGER.md);
[Linux operstates](https://docs.kernel.org/networking/operstates.html)). The same slice replaces
typed device names with an initially unchecked erase checkbox naming the disk and capacity;
consent resets on returning to Review or changing disks.

## Artifact and results

ISO SHA256 `4300dc11aa39df3d4ab835695855dfa8397ef4eee08c9d16c4a041f1cdd033d3`.

| Check | Result |
|---|---|
| Checksum, retained wizard source, complete initrd/scripts | Passed |
| Strict GTK compile, protected handoff, installer mode/syntax | Passed |
| `installer-link.py` (compiled status/refresh callbacks with simulated GTK/sysfs; real shell probe with simulated interfaces) | Passed: valid carrier, unknown/admin-down/malformed, speed, IPv6 suppression before enable, already-enabled interfaces preserved, suppression/activation failure |
| Negative control: temporary mutation replacing carrier with operstate | Regression failed as expected; live source untouched |
| QEMU/KVM 800×600, no NIC, 4 GiB, fresh 24 GiB raw disk | Passed: checked confirmation active; Review resets it; unchecked install rejected; first 1 MiB untouched; actions visible; VM stopped |
| QEMU/KVM 1024×768, two virtio NICs, QMP cable changes | Passed: Link detected → No link → Link detected with Static IPv4 and partial `172.` kept; no non-loopback addresses or routes after preparation; checkbox activation/reset and rejection without writes |
| Initial 1024×768 attempt | Test-only fix (select address-mode dropdown explicitly, not a duplicate mnemonic); same ISO, no product change |
| Independent source and native visual review; docs checks | No blocking issues; passed. Both workflows run the regressions; hosted CI and range coverage not claimed |

Captures (not committed): `link/nic-link-detected.png`, `nic-no-link.png`, `nic-link-recovered.png`,
`checked-confirmation.png`, `confirmation-reset.png`.

## Limits

Virtual link only: no physical cable, switch, VLAN or IP reachability. A virtual NIC can have carrier
while the hypervisor uplink is down. The probe keeps existing boot-environment configuration and
assigns no addresses (IPv6 autoconfiguration suppressed first); driver/probe failure shows Link
unknown. No full install/reboot repeated; staged target IP settings still apply after installation.
