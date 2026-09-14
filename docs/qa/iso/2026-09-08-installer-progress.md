# Installer progress, completion and failure verification

2026-09-08, Nex Core 0.1.0-alpha1. Bug: BUG-0022 ([ledger](../../BUG_LEDGER.md)).

## Artifact and source checks

Final ISO SHA256 `e4ab0825ccec83adecaf9a2e9877bc77610c23bb2d03ac9ed69eb02677f22a3a`.

| Check | Result |
|---|---|
| Build, checksum, retained shell source, complete initrd | Passed |
| Pinned frontend and strict config compile; shell/phase, handoff, hostname/preseed, installer mode/syntax | Passed; preseed test requires the final note to stay unseen (no automatic acknowledgement) |
| Log reader in `installer-shell.py` | Passed: missing/empty files, bounded 128 KiB tail, invalid UTF-8. No new log sink or credential logging; existing local logs are not redacted by the viewer |

Both installer CI workflows run the source checks and native protocol test; hosted CI not run.

## Native protocol reproduction and regression

Diskless QEMU/KVM, shipping GTK frontend, test-only scripted debconf commands.

| Run | Result |
|---|---|
| Old initrd | Reproduced blank body after PROGRESS STOP (54 dark pointer pixels in the body region vs 2100 during progress); original regression failed on it |
| Earlier log-viewer capture | Showed startup entries, not latest; one-shot GTK idle callback now waits for layout before focus/scroll |
| Final, 1024×768, no disk | Passed: waiting content after component STOP and last task; error reason visible; Alt+V opened read-only log at latest entries incl. synthetic error marker; Ctrl+Home/Ctrl+End moved and restored the tail; Escape returned to unchanged error; Continue resumed real progress; completion page blocked until restart acknowledged; guest terminated |

Captures (not committed): `progress/baseline-blank.png`, `between-components.png`, `error-prompt.png`,
`error-log.png`, `error-log-closed.png`, `resumed-progress.png`, `installation-complete.png`.

## Actual installation and explicit restart

Preceding custom-completion ISO, SHA256 `dd6ac0344213e447a9cdf17cff1377e5a5f679208c40d97f0c046e7052015cd9`,
passed `wizard-review.py --resolution 1024x768 --nics 2 --capture-completion` (QEMU/KVM, 4 GiB, fresh
24 GiB raw disk): real installation reached the native final stage, stayed 3 s without acknowledgement,
then exited 0 under QEMU no-reboot after Restart now. The test hook only emitted a marker before the
final prompt. `completion-result.json` recorded all three assertions true. Capture
`progress/installed-complete.png` (not committed).

The final ISO adds diagnostics only; completion was not reinstalled on it. An intermediate ISO also
completed with no NICs after manually acknowledging the no-Ethernet prompt; the two-NIC run needed none.

## Limits

- Protocol test simulates component commands and synthetic log entries; no real failing disk or full install.
- Full install proves the ordinary final gate and restart only; Debian's completion gate is not host certification, and some final hooks log failures without aborting. Cancellation source-reviewed only.
- Cannot show errors after power loss, kernel crash or a dead graphical server. Missing log files report unavailable.
- Not verified: physical hardware, installed-host boot after restart, hosted CI.
- Docs structure/whitespace passed; independent source, capture, test and docs review found no blockers.
