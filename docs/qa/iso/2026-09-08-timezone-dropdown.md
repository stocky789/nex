# Timezone dropdown verification

2026-09-08, Nex Core 0.1.0-alpha1. Area and timezone dropdowns (default UTC, bundled identifiers,
existing backend validation) replace text entry; Test keyboard removed. Supersedes those controls in
[QoL QA](2026-09-08-installer-qol.md), whose captures remain historical.

## Artifact and checks

ISO SHA256 `f0387810a37eadd6b2340041423debf76bffff783b645ed776a540126bf167bd`.

| Check | Result |
|---|---|
| Checksum, complete initrd, retained wizard source | Passed |
| Native QEMU/KVM, test-only preparation marker: 800×600 no NIC and 1024×768 two virtio NICs, 4 GiB, disposable 24 GiB raw disks | Passed: arrows/dropdowns only chose Australia → Australia/Sydney, canonical in Review; no text typed; Test keyboard absent; password reset, shared hostname, disk rejection (first 1 MiB untouched); VMs stopped |
| Strict compile, `installer-qol.py` (failed previous source; checks noneditable controls, region-switch clearing/selection), `installer-keyboard.py`, `installer/check.sh`, `check_docs.py --all`, `git diff --check` | Passed; both workflows run them; hosted CI and range coverage not claimed |
| Independent source review | No material issue |

Captures (not committed): `timezone/timezone-dropdown.png`, `timezone-selected.png`, `review.png`.

No full installation or physical/UEFI walkthrough repeated.
