# Phase 1 independent implementation review

Date: 2026-09-08. Read-only reviewer; findings returned to owners, corrections re-inspected.
Installer work excluded. Result: **no unresolved implementation blocker**.

## Scope

Controller/API callers, certificate enrollment/rotation/revocation, SQLite identity and operations,
agent persistence/probes/reconnect, React admin workflows, API schemas/reference, source delivery,
systemd examples, operator instructions, and the process/browser and Debian smoke checks. Stays
within Sites → Pools → Hosts and read-only inventory refresh. API impact (new contract) is recorded
in the [phase plan](../../plans/2026-09-08-controller-foundation.md) and [API reference](../../API.md);
the [operator guide](../../guide/manage-controller.md) covers CLI/UI, rotation with ten-minute
old-certificate overlap, revocation and re-enrollment.

## Findings corrected

| Finding | Correction and regression |
| --- | --- |
| Request authorized before revoke/re-enroll could mutate the replacement identity | Exact certificate rechecked in transaction; `TestRevocationBetweenMiddlewareAndTransaction` |
| Two replacement tokens could successively replace an active identity | Consumption requires host still revoked; `TestEnrollmentRotationRevocationAndReplacement` |
| Timeouts at unchanged sample timestamp lost; stalled inventory hid independent updates | Per-check merge; `TestMergeIndependentProbeAndTimeout` (incl. backward clock) |
| Inventory bound rejected 139 native CPU flags | Bound raised; native integration verified; invalid counts/duplicates/statuses have API checks |
| Persistence failure left execution with undurable command state | Reporting terminates; `TestRunStopsOnJournalPersistenceFailure` |
| Healthy UI compared wrong status; skip link changed route | Contract values; skip link focuses main; browser acceptance covers both |
| Older browser reads overwrote newer state; delayed token under another pool | Generation and busy guards; browser delay/release checks |
| Source snapshot omitted UI logo | Allowlist includes image, artwork terms, UI notices; source bundle tests |

Also inspected: strict JSON EOF rejection, single-writer private state, schema rejection before
migration, login work limits, debounced health events, scoped operation results, idempotent
command/result delivery.

## Independently executed

| Check | Result |
|---|---|
| `go test -race -count=1 ./internal/controller ./internal/agent` | Passed (8.550s, 1.113s); stale-credential regression 2.494s |
| `generate_api.py --check`; `test_api_contract.py` | Current; 5 passed |
| `test_build_source.py` | 2 passed |
| `go test -race -count=1 ./internal/agent -run TestVirtualizationInventoryExcludesUnrelatedCPUFlags` | Passed 1.007s (no migration-compatibility claim) |
| Captures `captures/host-360.png` (not committed), `captures/probe-timeout-desktop.png` (not committed), desktop host | No material defect; fresh connection with unknown stalled libvirt visible |
| Debian smoke script and [results](debian.md) | Read only, not rerun |
| Both workflows, `.gitignore`, plan, assessment, `BUG-P1-0001`–`0003` | IDs unique; same suites on push/PR; missing prerequisites fail, not skip; `bash -n` passed; assessment 68 files, no omissions |
| Binary and 124,167,896-byte source digests | Recomputed; match [final checks](checks.md) |

## Limits

Real systemd, physical KVM/storage, guest survival, installer, Phase 0 and hosted CI not proved.
Workflows need a runner with C compiler, Python/venv, browser install, Docker and network.
