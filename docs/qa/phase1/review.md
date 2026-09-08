# Phase 1 independent implementation review

Date: 2026-09-08. Scope: uncommitted Phase 1 development implementation.

The reviewer had a read-only implementation lane, reported findings to the owning
agents, and independently inspected their corrections. The only reviewer-written
file is this report. Installer work was excluded. This review does not claim a
hosted CI run or approval of unrelated working-tree changes.

## Scope and impact

Reviewed Controller/API callers, certificate enrollment/rotation/revocation,
SQLite identity and operations, agent persistence/probes/reconnect behavior,
React administration workflows, API schemas/reference, source delivery, systemd
examples, operator instructions, and the process/browser and Debian smoke checks.
The implementation stays within Sites → Pools → Hosts and read-only inventory
refresh; it does not add VM mutations or automatic guest recovery.

API impact: **new contract** for the implementation, correctly recorded in the
[phase plan](../../plans/2026-09-08-controller-foundation.md). Both agent and
Controller implement authenticated enrollment, reports/results, rotation and
compatibility checks. Client administration uses session authorization and CSRF
protection. The [API reference](../../API.md), machine schema and generated HTML
cover the implemented routes. This review report itself changes no contract.

Guide impact: **updated** for the implementation. The
[operator guide](../../guide/manage-controller.md) describes the actual CLI/UI,
matching build paths, single-process state ownership, explicit rotation, the
ten-minute old-certificate overlap, revocation and deliberate re-enrollment.
This report adds verification evidence, not an operator workflow.

## Findings corrected and checked

| Finding | Inspected correction and regression evidence |
| --- | --- |
| An old request authorized before revoke/re-enroll could mutate the replacement identity. | Report and rotation recheck the exact certificate inside their transaction; `TestRevocationBetweenMiddlewareAndTransaction` invokes both handlers with stale credentials and checks rejection without credential/inventory changes. |
| Two replacement tokens could successively replace an already active identity. | Token consumption requires the selected host still be revoked; `TestEnrollmentRotationRevocationAndReplacement` covers the second-token rejection. |
| Probe timeouts at an unchanged sample timestamp were lost; stalled general inventory hid independent resource updates. | Per-check merge preserves timeout deterioration, independent VM/disk observations, and stale resource data during zero-sample restart; `TestMergeIndependentProbeAndTimeout` covers these paths and backward wall-clock correction. |
| Incoming inventory bounds rejected the workstation's 139 native CPU flags. | The compatible bound was increased, followed by native inventory integration verification. Invalid counts, duplicate checks and unsupported statuses have focused API checks. |
| Agent persistence failure could leave execution continuing with undurable command state. | Persistence failure terminates reporting; `TestRunStopsOnJournalPersistenceFailure` injects a rename failure and asserts exit after one request. |
| Healthy UI checks were compared with the wrong status, and the skip link changed the application route. | Health/freshness uses actual contract values; the skip link focuses main content. Browser acceptance exercises both. |
| Older browser reads could overwrite newer state; a delayed token could appear under another pool. | Reload/session generation guards and busy workflow guards prevent these races; browser checks delay and release the actual responses. |
| Source snapshots omitted the UI logo. | The allowlist includes the image, artwork terms and UI notices; source bundle tests check these inputs. |

Also inspected strict JSON EOF rejection, single-writer/private state protection,
schema rejection before migration, login work limits, durable debounced health
events, scoped operation results and idempotent command/result delivery.

## Independently executed evidence

- Go 1.27.1: `go test -race -count=1 ./internal/controller ./internal/agent`
  passed, Controller 8.550s and agent 1.113s. The subsequently added stale-credential
  transaction regression passed separately with the race detector in 2.494s.
- `python3 scripts/generate_api.py --check` reported the reference current;
  all five `test_api_contract.py` tests passed.
- Both `test_build_source.py` tests passed.
- The subsequent virtualization-feature filter passed independent
  `go test -race -count=1 ./internal/agent -run TestVirtualizationInventoryExcludesUnrelatedCPUFlags`
  in 1.007s; the inspected filter exposes virtualization features without claiming
  migration compatibility.
- Inspected actual desktop and 360px host captures from the browser acceptance
  run. No material clipping/layout defect was found; narrow task tables scroll.
  Browser interaction itself was executed by the separate acceptance agent;
  its actual run and final captures are recorded in
  [process/browser acceptance](acceptance.md).
- Re-inspected the final [360px host capture](captures/host-360.png) and
  [probe-timeout capture](captures/probe-timeout-desktop.png): the narrowed feature
  list is readable and a fresh connection visibly coexists with an unknown stalled
  libvirt check while other checks remain healthy.
- Read the Debian smoke script and [native results](debian.md): it operates only
  its disposable, unprivileged container, uses actual libvirt, and explicitly
  leaves KVM unsupported when no device is passed through. The reviewer did not
  independently rerun that container workflow.

## Final integration and documentation batch

Reviewed both Phase 1 workflows, the `.gitignore` addition, the phase plan,
change-assessment draft, and the appended `BUG-P1-0001` through `BUG-P1-0003` ledger
entries. Ledger IDs are unique. Both workflow locations run the same Go race,
Python, browser/process and Debian suites on pushes and pull requests. The native
smoke resolves Go from PATH or the configured toolchain and uses available Docker
directly or through noninteractive sudo; missing prerequisites fail rather than
silently skipping. `bash -n tests/integration/phase1-debian.sh` passed.

These workflow files require a compatible runner with a C compiler, Python/venv,
browser installation support, Docker access and network access. They do not
establish hosted execution. The evidence accurately distinguishes simulated
browser/process failures, actual container libvirt, and untested physical hosts.
The final assessment contains 68 unique existing files; an independent file inventory
found no omitted files under the implementation, UI source/public assets, systemd
or Phase 1 QA directories. The primary owner records the isolated documentation
range pass for those files plus the assessment in [final checks](checks.md).
The final plan accepts the development slice while explicitly retaining physical
host, service-deployment, installer and hosted-CI limitations.

## Conclusion and limits

No unresolved implementation blocker remains from this review after the listed
corrections. The primary owner's [final aggregate results](checks.md) record the
matching-source rebuild, documentation range check and walkthrough evidence.
The reviewer independently recomputed both binary digests and the 124,167,896-byte
source archive digest; all match that report. This supports the stated development
acceptance without representing the combined installer working tree as green.

Real systemd deployment, physical KVM/storage correctness, running-guest survival,
installer integration, Phase 0 completion and hosted CI enforcement are not proved
by these tests. Browser self-signed-certificate handling and simulated libvirt
failure inputs are identified in the acceptance report. The independent Debian
smoke supplies separate actual libvirt evidence, not a physical-host certification.
