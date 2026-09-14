# Continuous Nex progress verification

2026-09-08, Nex Core 0.1.0-alpha1, ISO SHA256
`e252ca7fe1402d6b3e6d9ce61e98c969e7ca1f29f61addc4ab9ca12a06bf5135`. Bug: BUG-0023 ([ledger](../../BUG_LEDGER.md)).

## Results

| Check | Result |
|---|---|
| Build, checksum, retained frontend source, complete initrd | Passed |
| Log reader/phase, generated preseed, installer mode/syntax | Passed |
| Old ISO against new flat-track/geometry assertion | Failed as expected (passed the earlier nonempty-panel check; active screen used a different stock bar and spacing); baseline kept separate |
| Diskless QEMU/KVM protocol walkthrough, 1024×768, fully automated | Passed: flat track at (162,235)–(1006,245) in active, gap, resumed and final-gap captures; 40% and 60% fill from component values; gaps cleared fill but kept the track; heading and power reminder fixed; no stock beveled bar or italic status |
| Failure log opened and scrolled → unchanged error → acknowledged → resumed | Passed; a second error during progress returned to the same 60% panel |
| Custom completion | Blocked until Restart now; no GTK critical messages; guest stopped |
| Exploratory run | Needed test-only fixes (click native Continue, not Alt+C; exclude moved pointer from reminder comparison); final run had no manual input or product change |
| Docs structure/whitespace; independent source and native visual review | Passed; no blocking issues |

Captures (not committed): `continuous-progress/active-progress.png`, `between-components.png`,
`resumed-progress.png`, `nested-resumed-progress.png`, `installation-complete.png`.

## Limits

Protocol commands simulate component boundaries: no physical display or complete installation;
backend actions unchanged. No install/reboot repeated; completion evidence is in
[progress QA](2026-09-08-installer-progress.md). Cancellation was source-reviewed, not activated.
Hosted CI not run; both installer workflows invoke the regression.
