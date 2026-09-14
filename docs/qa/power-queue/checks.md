# Queued VM power checks

Date: 2026-09-11. Development 0.1.0 working tree. No live infrastructure changed.

## Executed checks

- `go test ./...` and `go test -race ./internal/agent ./internal/controller ./internal/vmhost -run 'Queue|Power|Applied' -count=1`:
  passed. They cover: Start accepted behind an edit, same-request replay, database admission order despite clock changes,
  old Core capability refusal, and work delivered behind 100 capability-blocked requests.
- Core agent tests: durable FIFO admission, store reopen, Controller replacement, replay without input
  mutation, same-VM prerequisites. Execution tests: edit/power/media prerequisites; failure, uncertainty and
  missing/wrong-VM prerequisites without effects; self-reference rejection; exactly-once replay. The new rejection
  tests failed with the predecessor guard removed.
- Review found stale transport results masking engine recovery. A regression composes real engine promotion (simulated
  virsh) with stale uncertainty; the engine wins, and missing engine evidence stays uncertain. Adoption handles 16,384
  outcomes and rejects more than 30,000; lookups are batched below SQLite's parameter limit. A further regression prevents starvation.
- Web typecheck/test/build, `generate_api.py --check`, 21 API contract tests: passed.
- `tests/integration/phase1.py --browser` (real Controller and agent, simulated virsh, mocked power API): one Start
  behind an edit gives a queued task row with no dialog or retry after refresh. Host-unavailable keeps confirmation and retry
  identity. Existing keyboard/stale/narrow/session checks passed. Inspected `power-start-queued.png`.

Captures and results stay in local `.cache/power-queue-*` (workstation host labels), not published.
Both phase1 workflows run these suites; no hosted CI run.

## Ownership and limits

Core agent `state.json` holds command order and prerequisites, and the execution journal holds prerequisites and results.
A replacement Controller restores tasks as passive rows without redispatch. An HTTP 202 before delivery is not a Core receipt.

The single Core worker serializes host work, so a long task delays power. Queueing waits for task completion, not ACPI
shutdown. Nothing is retried automatically. Old Core keeps the busy guard. Native libvirt effects and a live queued
workload through Controller replacement were not exercised. Independent review passed after the fixes above.
