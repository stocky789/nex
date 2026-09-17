# Follow-up workflow failures under runner load

## Hosted evidence

The next Controller foundation run included the previous regression repairs but failed
in three new places. This is a sanitized interpretation of authenticated job logs;
private forge identifiers and checkout paths are omitted.

- `TestRunStopsOnJournalPersistenceFailure`: expected a typed fatal persistence error,
  received `nil`. Its one-second context also covered agent initialization and the
  first report that injects the disk failure. Context cancellation normally returns
  `nil`; the exact hosted startup delay was not captured.
- Controller package: Go stopped the suite at its default ten-minute package deadline
  (600.246 seconds). The active test had run only three seconds; its stack was runnable
  inside the normal 600,000-iteration password hash used by test-client login. This
  supports cumulative suite-budget exhaustion rather than an observed deadlock.
- Process/browser acceptance: the restarted agent never reached fresh, healthy libvirt
  within the assertion deadline. The fixture imposed a 150 ms probe timeout rather
  than the production three-second deadline. Hosted output omitted the last inventory
  state, so exact probe timing cannot be reconstructed.

The aggregate gate correctly failed after these lanes. Contracts, UI build/tests and
website browser checks passed. The host lane passed with privileged checks skipped.
No failure here involved the public website URL.

## Corrections and regression evidence

The persistence test now has a thirty-second watchdog and an atomic report counter;
it still requires `persistenceError` and exactly one report. A temporary 1.5-second
server delay reproduced the original nil-error failure and passed with the correction.
The delay was removed. One hundred focused race repetitions and twenty repetitions
with journal replay and identity rejection passed; independent review ran ten more.

Both workflow copies now run `go test -race -timeout 20m ./...`. The package timeout
remains finite, all packages and race detection remain enabled, and production
password hashing is unchanged. The workflow regression failed on both old copies
and passed after correction. Twenty minutes is an increased CI allowance, not proof
that a hosted rerun will complete within it.

The process fixture uses the production probe deadline and retains a 250 ms successful
simulated virsh reply as a regression. That reply deterministically fails the old
150 ms deadline. Browser checks wait for observed failed, unknown and healthy probe
states instead of sleeping 700 ms; their actual UI assertions remain in place.
These tests execute in the existing browser lane on pushes and pull requests on both
forges. Simulation proves the deadline failure mechanism, not the precise historical
runner scheduling or native libvirt behavior.

## Scope and limits

API impact: **none** — test timing, fixtures and CI budgets only. No client request,
Controller operation, Core command, observed-result contract or durable state changes.
Guide impact: **none** — operator behavior and instructions are unchanged.
State owner: **none**. No product acceptance gate is closed by these fixes.

Another session concurrently edited workflow scheduling, host lanes, installer steps
and website checks. Those edits were preserved and are outside this failure-fix scope;
this slice changes only the Go timeout in the workflow files and adds its regression.
No commits, pushes, deployments or hosted reruns were performed by this session.

## Additional defect found locally

The first full local Go rerun discovered a media-capacity regression race: the test
claimed one byte beyond its own free-space snapshot, but unrelated file removal could
increase space before the handler checked. It then returned invalid-body 400 rather
than insufficient-space 507. The fixture now uses the existing sibling test's clearly
oversized `1 << 62` declared length, preserving the 507 assertion and low-space skip.
A temporary 32 MiB allocation released between snapshots reproduced the old failure
and passed with the corrected length; the injection was removed. Production upload
validation is unchanged.

## Executed checks

- Final `go test -race -timeout 20m ./...`: passed across all packages after the media
  fixture correction; unchanged packages used cached results where reported.

- Persistence test: 100 focused race repetitions passed; related journal/rejection
  tests passed twenty repetitions. Independent focused race review passed ten.
- Full `phase1.py --browser` with deliberate 250 ms successful probe replies: passed,
  including Controller/agent restart recovery, durable refresh, isolated stalled probes,
  browser failure/recovery states and re-enrollment. Independent review inspected logs
  and local captures; captures contain local inventory and are not published.
- Python discovery passed 308 tests before the other session removed one host-lane
  assertion; rerun against those concurrent edits passed 307 tests.
- Workflow budget regression failed both old workflow copies and passes after correction.
- Media workflow and actual-body-limit tests passed ten race repetitions (43.314 s);
  no skips were reported.
- Repository privacy check, generated API-reference check and documentation structural
  check (`--all`) passed.

At the latest worktree check, documentation assessment coverage remained missing for
four concurrently edited installer/website workflow paths outside this slice. Their
installer edits also left trailing blank lines reported by `git diff --check`.
These are preserved ownership conflicts, not failures of the runtime fixes above.
