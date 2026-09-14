# Installer usability verification

2026-09-08, Nex Core 0.1.0-alpha1. Bugs: BUG-0016, BUG-0017 ([ledger](../../BUG_LEDGER.md)).
Timezone entry and Test keyboard were later replaced by [dropdowns](2026-09-08-timezone-dropdown.md).

## Scope and artifact

Vertical step list, white form panel, fixed navigation, native scrolling, engine typography,
fullscreen sizing, searchable timezones, keyboard test, password visibility, and one shared hostname
buffer edited on Host and Network (Review and protected config use it).

Final ISO SHA256 `9b86f74ff4a63459373fecc495f55071513166aa24aa65b96d2074ffc10db8c1`. Retained GUI source
byte-identical to wizard source; complete-initrd artwork, script and merged-usr checks passed.

## Reproduced failures

| Failure | Result |
|---|---|
| Previous ISO at 1024×768 | Wizard stayed 800×600 with black margins (capture `qol/before-fullscreen.png`, not committed) |
| First timezone-completion build | String-matching checks passed but no popup in the native installer; native selection now in the walkthrough |

## Native walkthrough

`wizard-review.py` on the final ISO with extracted kernel/initrd and test-only preparation marker,
QEMU/KVM, 2 CPUs, 4 GiB, disposable 24 GiB raw disks, fresh output directories: default 800×600/no NICs,
then `--resolution 1024x768 --nics 2 --capture-progress` (writes only the test disk, after
rejection/no-write assertions and explicit confirmation). Both runs exited successfully:

- Wizard fills the display; fixed action area usable at both sizes.
- `nEw yOrK` → `America/New_York`; `sYdNeY` → `Australia/Sydney`, canonical in Review. Unsupported input blocked with a message.
- Show passwords reveals both entries; leaving Host and returning hides them.
- Invalid Network hostname rejected; `nexnetworkqa` updates Host and Review.
- Empty-NIC and two-NIC pages usable, scrolling at 800×600.
- Wrong disk confirmation leaves input usable and the first 1 MiB untouched.
- 1024 run reached **Install the base system** and kernel package configuration; VMs stopped. Not installation acceptance.

Captures (not committed): `qol/welcome-1024.png`, `network-1024.png`, `timezone-800.png`,
`hostname-and-password-reset-800.png`, `review-800.png`, `confirmation-recovery-800.png`, `installation-progress-1024.png`.

## Focused checks and review

| Check | Result |
|---|---|
| Strict native C compile; `installer/check.sh`, `installer-shell.py`, `installer-keyboard.py`, `installer-qol.py` (callbacks with simulated GTK: geometry, timezone case/space matching, both password fields) | Passed |
| `check_docs.py --all`, 13 checker unit tests, `git diff --check` | Passed; range coverage not claimed |
| CI | Both installer workflows run focused checks and native walkthrough at both sizes; hosted CI not run |
| Independent source review | Duplicate keyboard-test mnemonic corrected to Alt+Y; no other blocker |
| Independent final visual review | Both resolutions, hostname sync, timezone, password reset, confirmation recovery: no material findings |

## Limits

Hot-resize has callback coverage only. No full install repeated (see [wizard report](2026-09-08-nex-wizard.md)).
No physical hardware, UEFI, Secure Boot or new networking certified. IPv4-only editor, one VLAN per NIC
and whole-disk GUI limits remain.
