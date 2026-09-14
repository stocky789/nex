# Independent fresh-install review

Date: 2026-09-09. Result: **no unresolved findings** within the
[fresh local installation scope](../../plans/2026-09-09-fresh-local-install.md). Reviewer inspected
source, artifacts, records and captures; did not operate the lab or change product code.

## Independently checked

- Hashed every artifact in [the manifest](artifacts.json); verified the Ed25519 signature and key
  equality with the trusted development key; compared 110 Controller/Core and 24 installer source
  files with the frozen snapshot; located and hashed the package and graphical initrd inside the ISO.
- Package: embedded appliance image and both runtime source archives match the signed build; Core
  executable, UI and API match [installed provenance](installed-provenance.json).
- Blank raw-file creation, zero pre-install allocation, QMP attachments, target/erase confirmation,
  GUI install and disk-only console; clean installer logo, deploy ready screen, guest UEFI boot menu.
- Browser and native records: consistent host/pool/VM identities; rejection/replay, outage with VMs
  running, local login, reboot persistence; [SSH access removed](retained-state.json).

## Corrections

- Harness expected the wrong replay response; now checks `200`/`vm_id`.
- Target guards now reject outside-cache or symlink targets and check the running QEMU raw disk;
  SSH uses a pinned host key with strict checking. No product change.
- Deployment guide package example corrected to `0.1.0+managed1`; installation report explains SSH
  removal. Both rechecked.

| Command | Result |
|---|---|
| `python3 tests/integration/installer-logo.py` (final initrd) | Passed |
| `python3 -m unittest discover -s tests -p test_fresh_install_lab.py` | 2 passed, incl. wrong QMP file/format rejection |
| `python3 scripts/check_docs.py --all` | Passed (structure only) |

## Limits

Accepts the disposable nested-KVM slice only: not physical hardware/LAN, UEFI host install, guest
OS/data persistence, central lifecycle/console, storage-failure recovery, redistribution or hosted
CI. The guest was stopped before reboot, so this is not guest autostart or live reboot recovery.
