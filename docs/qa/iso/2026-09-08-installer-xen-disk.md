# Xen disk and network indicator verification

2026-09-08, Nex Core 0.1.0-alpha1. Bug: BUG-0020 ([ledger](../../BUG_LEDGER.md)). Also adds the green
carrier indicator and removes hostname hover explanations (validation unchanged).

ISO SHA256 `6975c894703b1e7fb29bc65bd2b02caba0d63fc5403e962e88dce6bd29636ae2`.

| Check | Result |
|---|---|
| Build, checksum, retained wizard/backend source, complete initrd, strict GTK/backend compile | Passed |
| `installer-disk.py` (real backend validator, simulated stat/sysfs/mount) | Original rejects `/dev/xvda`; fix accepts Xen, SCSI, virtio, NVMe; rejects partitions, read-only/removable, installation media, missing/nonblock, changed capacity/device/serial |
| `installer-link.py`, `installer-hostname.py`, `installer-handoff.py`, syntax/mode | Passed; both installer workflows include the disk regression |
| Native `wizard-review.py`, 1024×768, two virtio NICs, QEMU/KVM, 4 GiB, fresh 24 GiB raw disk | Passed: green Link detected → neutral No link → green with partial IP kept; no hostname tooltip in source; qualified-name sync and invalid-input recovery; erase confirmation activation/reset/unchecked rejection without writes; fullscreen and timezone dropdown; VM stopped |
| Docs structure/whitespace; independent source, regression, docs and capture review | Passed; no blockers |

Captures (not committed): `xen-disk/nic-link-detected.png`, `nic-no-link.png`, `nic-link-recovered.png`, `checked-confirmation.png`.

Limits: Xen devices simulated; no real Xen install or install/reboot for this slice. Hosted CI and range coverage not claimed.
