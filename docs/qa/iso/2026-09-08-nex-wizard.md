# Nex Core custom wizard verification

2026-09-08, Nex Core 0.1.0-alpha1 amd64. Bugs: BUG-0009 to BUG-0015 ([ledger](../../BUG_LEDGER.md)).

## Artifacts and scope

Final ISO SHA256 `00ab44d13f9977737dbad50ffb684e94019dded93007ccc1da2561d464d2d532`. Full install and
installed-host checks used the preceding build `03982470c044fb1babceb77ce452fa0696eb33f23d1047a84c1a924ac57e5836`;
the final change only keeps the confirmation field visible after an error, and SHA256 comparison
confirmed identical `nex-config`, network model, preparation/start/recheck and late-install scripts.
Native GUI regression was rerun on the final ISO.

Standalone Nex setup wizard; Debian stays the installation engine, including failure dialogs. GUI
defaults load from the initrd, skipping file-preseed's “Load debconf preconfiguration file” screen.
No promise of flicker-free firmware/video transitions.

## Native walkthrough

QEMU/KVM, 2 CPUs, 4 GiB, 800×600 VGA, new 24 GiB raw disk (serial `NEX-WIZARD-LAB`), two virtio NICs
(MACs ending `34:10`, `34:11`), actual BIOS ISO menu, QMP keyboard/mouse:

- Preparation completed; both NICs listed with name, MAC, driver, state.
- German keyboard: physical Y produced Z; password fields cleared. Back to US, disposable credentials entered. (Keyboard capture from the preceding build; final callback and initialization checks pass.)
- ens3 management DHCP with DNS `10.0.2.3`; ens4 `10.20.0.2/24`, VLAN 123, no gateway; switching adapters kept both.
- Invalid `10.20.0.999/24` rejected and correctable. No disk selection rejected; `/dev/vda` selected.
- Review showed all settings and names ens3 → nex0, ens4 → nex1. Wrong typed disk name rejected without writes; `vda` typed, erase/install selected.
- Installation ran and rebooted; disk booted with ISO detached; root login succeeded.

Captures (not committed): `wizard/welcome.png`, `keyboard-layout.png`, `network-invalid.png`, `review.png`, `confirmation-recovery.png`.

## Installed result

Hostname `nex-lab` and Nex Core identity. `libvirtd`, `ssh`, `chrony`, `networking` active at first
observation (later acceptance checks `libvirtd.socket`, since the idle service can stop). nex0
`10.0.2.15/24` by DHCP with the only IPv4 default route via `10.0.2.2`; nex1.123 up with
`10.20.0.2/24`, `ip -d link` 802.1Q VLAN 123; DNS override `10.0.2.3` retained.

Read-only `tests/integration/wizard-installed.sh` passed ([actual output](wizard/installed-check.txt)):
services/socket, IPv4 addresses, VLAN ID, default route, DNS readable by nobody, public file
permissions, no disposable password in installer logs. Management ping got two replies; second-NIC
frames carried VLAN 123; tagged peer ping failed as expected (no tagged peer): tagging, not switch
connectivity. Installation-time transient credential/log checks could not finish before automatic
reboot; installed-log checks are not inspection of the vanished initramfs. Source review and
simulated handoff tests cover private file handling and suppressed preseed input logging.

## Regression and review evidence

| Check | Result |
|---|---|
| `installer/check.sh` | Boot modes and safe minimal policy pass |
| `installer-shell.py` | Compiled classifier passes |
| `installer-network.c` (strict, umask 0077) | Invalid NIC settings, VLAN output, MAC binding, management/secondary DHCP policy, DNS override, permissions pass; secondary DHCP has output coverage, no second runtime lease |
| `installer-handoff.py` (simulated) | Seed import keeps earlier logs without the hash; partman hook propagates failure; caller umask restored |
| `installer-keyboard.py` | Layout, credential clearing, command failure, initialization pass |
| `installer-logo.py` | Complete initrd keeps artwork and parent paths; rejects overlay directories replacing base symlinks |
| `wizard-review.py` (final ISO, test-only preparation marker, no NIC, fresh 24 GiB disk, 800×600) | Rejects leaked raw PROGRESS protocol (negative control blocks success marker); Review content, wrong confirmation, recovery field visibility and untouched first 1 MiB pass. The old error-layout capture fails the visibility assertion (zero dark text pixels); guards these regressions only |
| `check_docs.py --all`, 13 checker unit tests, `git diff --check` | Passed; range coverage and hosted CI not run |

Both installer workflows run focused checks, ISO build and native GUI regression on push/PR; missing
Docker/QEMU fails explicitly. Independent reviews found and verified corrections for keyboard state,
private umask, preseed failure, initrd paths, network permissions and review/error rendering.

## Limits and impact

- Not verified: physical hardware, UEFI install, Secure Boot, switch VLANs, IPv6 editor, bonds, bridges, trunks, multiple VLANs per NIC, manual GUI partitioning.
- GUI controls IPv4 only; Debian IPv6 autoconfiguration stays enabled, including on a VLAN's untagged parent. IPv4 route ownership is not IPv6 isolation.
- Disk identity uses device number, capacity and serial if present; serial-less devices are weaker against same-size hot replacement. Do not hot-swap installation disks.
- Minimal text/serial paths unchanged. Source and artwork redistribution gates open in [licensing](../../LICENSING.md).
