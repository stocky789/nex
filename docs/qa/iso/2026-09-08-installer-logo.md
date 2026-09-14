# Nex Core installer logo verification

2026-09-08, Nex Core 0.1.0-alpha1 amd64 ISO, SHA256
`031c8963fa651a67c1fbaf590632838e9b07cbe71ea530145cac03ae6d2561a5`.
Bugs: BUG-0007, BUG-0008 ([ledger](../../BUG_LEDGER.md)). Later superseded by the
[transparent lockup cleanup](2026-09-09-logo-cleanup.md).

## Scope and method

Replaces the placeholder letter and system-font name with the approved Core lockup from
`docs/brand/nex-brand.png`: GTK crops 45,510 (400×96) and scales to 240×58; the raster is kept
in the modified frontend source archive. Vector master remains outstanding.

Built with `sudo -n docker` through the existing pipeline. Booted the staged kernel and complete
GTK initrd with the ISO attached: QEMU/KVM, 2 CPUs, 4 GiB, 800×600 VGA, no disk, no network. QMP
keys advanced English → United States → keyboard; native framebuffer captures inspected.

## Results

| Check | Result |
|---|---|
| Native render at 800×600 | Complete logo with Core descriptor; blue letter tile gone; title, keyboard list, sidebar and navigation visible; keyboard navigation advances (capture `logo/keyboard-800.png`, not committed) |
| First image boot | Failed: plain-text fallback (overlay archive lacked artwork directory entries); fixed, final boot recovered |
| `installer-logo.py` on complete concatenated GTK initrd | Passed: directory order and exact artwork bytes |
| `installer/check.sh`, `installer-shell.py`, ISO checksum, source archive artwork/shell comparison | Passed |
| `check_docs.py --all`, 13 checker unit tests, `git diff --check` | Passed; Git-range coverage and hosted CI not run |

## Limits

Native VM only, no physical hardware. No disk writes or installation on this rebuild
([custom installer QA](2026-09-08-custom-installer.md) has the prior full install). UEFI and
fallback not re-exercised on the final artifact (the failed boot did exercise text fallback).
Distribution licensing/source gates remain open in [licensing](../../LICENSING.md).
