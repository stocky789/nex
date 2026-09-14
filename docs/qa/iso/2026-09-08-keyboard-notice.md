# Quiet keyboard initialization

2026-09-08, Nex Core 0.1.0-alpha1. Bug: BUG-0021 ([ledger](../../BUG_LEDGER.md)).

| Check | Result |
|---|---|
| Callback regression (simulated GTK/process) | Failed on old success notice; passes after fix. Success clears the message; failure still reports and blocks; both password fields still cleared |
| Strict GTK compile; ISO SHA256 `67a2a5bd32de453a576ce698a11fe507fd8628cb4b6a1079ceecfc1a759a9baa` checksum, complete initrd, retained wizard source | Passed |
| Native `wizard-review.py`, 800×600, QEMU/KVM, no NIC, 4 GiB, disposable 24 GiB raw disk | Passed: Welcome has no keyboard notice (capture `keyboard-notice/welcome.png`, not committed); timezone, password reset, hostname sync, unchecked disk rejection; VM stopped |
| Docs structure/whitespace | Passed |

Not tested: full installation, real keyboard hardware, hosted CI.
