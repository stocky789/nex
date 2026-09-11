# Queued VM power checks

Date: 2026-09-11. Development 0.1.0 working tree. No live infrastructure was changed.

## Executed checks

- `go test ./...` and focused race checks (`go test -race ./internal/agent
  ./internal/controller ./internal/vmhost -run 'Queue|Power|Applied' -count=1`) passed. Focused queue/power checks verify accepted Start behind an edit,
  same-request replay, database-admission order despite clock changes, old Core capability
  refusal, and compatible work delivered behind 100 capability-blocked requests.
- Core agent tests verify durable FIFO admission, store reopening, Controller replacement,
  command replay without input mutation, and local same-VM prerequisites. Core execution
  tests cover successful edit/power/media prerequisites, failure/uncertainty/missing or
  wrong-VM prerequisites without effects, self-reference rejection and exactly-once replay.
  Restoring the missing predecessor guard made the new rejection tests fail before the fix.
- Review found stale transport results masking engine recovery. A regression now calls actual
  engine promotion with simulated virsh, composes its successful operation result with stale
  agent uncertainty, and verifies that engine truth wins. Missing engine evidence remains
  uncertain. Large-history adoption covers 16,384 terminal outcomes and rejects more than
  30,000; database lookups are batched below SQLite's parameter limit. Another regression prevents old-host capability filtering from starving work.
- `npm --prefix web run typecheck`, `npm --prefix web test`, `npm --prefix web run build`,
  `python3 scripts/generate_api.py --check` and all 21 API contract tests passed.
- `tests/integration/phase1.py --browser` passed using a real disposable HTTPS Controller
  and agent with simulated virsh. The browser's VM power scenario uses mocked VM/power API
  responses: with an existing dispatched edit, one Start POST produces a queued Recent tasks
  row, no dialog and no retry after refresh. Genuine host-unavailable rejection retains
  confirmation and the same retry identity. Existing keyboard, cancellation, stale/error,
  narrow-view and session-security checks passed. Inspected `power-start-queued.png`.

Browser artifacts stay in local `.cache/power-queue-captures` and
`.cache/power-queue-results.json`; earlier captures were in `.cache/phase1-captures`.
They include workstation-derived host labels, so they are not published as repository evidence.
Both GitHub and Gitea phase1 workflows already run the Go suites and this browser suite on
pushes and pull requests. No hosted CI run was invoked.

## Ownership and limits

Core's agent `state.json` retains command order and local prerequisites; the managed execution
journal retains explicit prerequisites and results. Inventory outcomes restore pending and
terminal tasks to a replacement Controller as passive task rows, without redispatch. A Core
receipt is required for host persistence; an HTTP 202 before delivery does not prove receipt.

The existing single Core worker serializes managed work across the host, so another long task
can delay power. Queueing waits for prior task completion, not guest ACPI shutdown. Genuine
failure or uncertainty is not automatically retried. Old Core retains the busy guard until
upgraded. Native libvirt power effects and a full live queued workload through Controller
replacement were not exercised here; fake-command and durable-store tests establish the
queue behavior, not hardware execution. Independent review passed after the review findings above
were corrected and the focused tests rerun.
