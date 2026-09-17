# Phase 2 gate closed

Date: 2026-09-17. Decision: project owner — close Phase 2 now.

## Verdict

**Phase 2 (one-host VM management) is closed.**

Functional gate clauses passed on gate5 (`0.2.10~gate5`). The remaining blocker
(BUG-0256 duplicate hot-plug MAC) is lab-verified fixed on published **0.3.1**.
VirtIO driver media ships on **0.3.2** with retained Fedora corresponding source.
Owner decisions below are resolved for Phase 2 scope. Process leftovers (independent
review in this session, hosted CI, owner Git commit/push) are not product gaps and do
not keep the phase open.

## Evidence that closes the gate

| Requirement | Evidence |
|---|---|
| Create / install / console / reboot / persist / outage / reconnect | [gate5 report](checks.md) rows 3, 4, 6, 8, 9, 10, 14 |
| Row 11 / BUG-0256 (probe c2, no duplicate MAC) | [focused lab on 0.3.1](../2026-09-17-phase2-focused-lab.md); [BUG-0256](../../BUG_LEDGER.md) lab-verified |
| Storage + dedicated-memory admission | Same focused lab |
| VirtIO media on Core ISO | [0.3.2 ISO build](../iso/2026-09-17-core-0.3.2-build.md) |
| G6 external Controller replacement | gate5 report (partial: same-host redeploy deferred) |

## Owner decisions resolved (Phase 2)

1. **G6 same-host:** external replacement meets G6 for Phase 2; same-host appliance
   redeploy stays out of Phase 2 (Phase 3 / Controller replacement track).
2. **AGENT-1:** keep current probe behaviour for Phase 2.
3. **CORE-1:** accept current guest-tools / spice-vdagent tradeoff for Phase 2.
4. **Plan item 7:** current edit behaviour (refuse or queue as implemented) meets Phase 2.
5. **Plan item 8:** typed-name confirmation plus appliance protection counts as deletion
   protection for Phase 2.
6. **BUG-0246 old Controller packages:** superseded by 0.3.0+ publication; not a Phase 2
   product gate.
7. **Core refuse appliance Start:** Controller UI confirmation is enough for Phase 2.
8. **Release smoke start:** deferred as release-workflow improvement, not Phase 2.
9. **NIC editor model / open defect 5:** two-step add-then-remove workaround accepted for
   Phase 2; open defect 1 closed by BUG-0256. Cross-VM manual-MAC scan and pending-unplug
   UI are follow-ups, not Phase 2 blockers.

## Accepted residuals (not Phase 2 blockers)

Recorded on gate5; deferred as polish:

- Installer step-list blank gaps (open defect 2)
- Queued/failed create page residuals (open defect 3); gate5 captures already exist
- Console attach lag after Start (open defect 4)
- Single-interface model replacement 400 (open defect 5)
- Pool-level empty-state captures not re-taken in this close

## Process note

Independent review of the gate report was not available in the closing session (author
and closer are the same tooling path). Project owner closed on product evidence above.
Hosted CI and committing the tree remain owner Git operations after this change;
they do not reopen Phase 2.
