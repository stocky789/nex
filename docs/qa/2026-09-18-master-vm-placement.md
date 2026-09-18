# Master-owned VM placement checks

Date: 2026-09-18. Version: unreleased development working tree after 0.3.1.
Scope: [master placement admission](../plans/2026-09-18-master-vm-placement.md).
This report distinguishes native helper tests, authenticated peer tests and browser
simulation. It does not close Phase 3 or Phase 4.

## Environment and method

Local Linux development environment, Go 1.27.1, Python 3.14.7, Node 22.22.3,
npm 10.9.8 and Playwright 1.62.0 Chromium. Tests use temporary state directories,
loopback TLS peers and native-command fixtures. No existing VM, storage repository,
physical disk or live host was modified.

## Results

- `go test ./internal/controller`: passed. Focused cases prove that absent Controller
  resource mirrors do not veto valid create/update requests, while authorization,
  selected-host/master capability, pool routing and exact retry checks remain.
- `go test ./internal/vmhost -run 'TestCheckVMPlacement|TestManagedCapacity|TestManagedUpdateLayoutKeepsExistingDisks' -count=1`:
  passed. Native helper tests cover resource scope, capacity, CPU support, malformed
  existing layouts, cancellation and exact journal replay without disk allocation or
  journal changes. Native platform commands are fixture-controlled, not real storage proof.
- `go test ./internal/vmhost -count=1` and focused placement/default-resource race
  checks: passed after isolating the default-resource test from an existing libvirt
  directory (BUG-0278); no production default changed.
- `python3 scripts/generate_api.py --check`: passed; all generated references current.
- `python3 -m unittest discover -s tests -p 'test_api_contract.py'`: 29 passed.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`: passed.

`go test -race -timeout 20m ./...` passed across all packages. After the final
agent-only replay safety corrections, `go test -race ./internal/agent -count=1`
passed again. The worktree documentation gate and diff checks passed.

Authenticated peer and worker regressions cover exact reply binding, unauthenticated
callers, stale membership, absent replay owner/reservations and unavailable master.
A clustered-storage exact replay with refused admission preserves `needs_attention`
without executing native reconciliation; a fake `virsh` sentinel proves the command
was not invoked. Changed media requests cannot reuse a journalled operation as a
different definition. Independent safety review passed after the corrections, with
no remaining code safety findings. Separate contract/documentation and browser/guide
reviews passed; native acceptance limitations below remain open.

## Final handoff check

Independent review rejected the late namespace/chroot fixture as unsuitable for the
ordinary Go suite: it depended on user namespaces, mount permissions and the
workspace filesystem. It has been removed. Portable final-admission checks in
`TestLocalPlacementPersistsExactOwnerAndUpdateReservation` instead exercise exact
owner persistence, unchanged create retries, exact pending update retries, changed
payload rejection and stale membership rejection without modifying durable state.
Independent review approved the replacement; targeted local-admission race checks
and `go test -race -timeout 5m ./internal/agent ./internal/vmhost` passed. API/reference,
worktree documentation, privacy and diff checks passed again.

They use temporary Core records and a supplied preflight result; they do not claim
to execute native storage preflight. Native helper success remains separately tested.

## Browser walkthrough

Executed with an actual Controller process and Chromium against a simulated Core:

```sh
.cache/placement-qa-venv/bin/python tests/integration/managed_vm_browser.py --simulated --evidence .cache/placement-browser
.cache/placement-qa-venv/bin/python tests/integration/website_controller_captures.py --output .cache/placement-website/images --evidence .cache/placement-website/results.json
```

Both commands exited successfully. The managed VM walkthrough exercised creation,
interrupted-response retry, missing `pool-vm-placement-v1` rejection before dispatch,
and an asynchronous simulated Core placement rejection. The failed task displays
**Creation failed**, the Core error and **Task history**. The website capture harness
also completed with the capability advertised by its simulated current Core.

The reviewer visually inspected the transient captures
`.cache/placement-browser/browser-simulated-core-rejected-placement.png` and
`.cache/placement-website/images/vms-desktop.png`. Captures are local test artifacts,
not published screenshots or evidence of native multi-host execution. Independent
review corrected guide wording: master capability is enforced when saving, and the
required capability concerns the selected host and pool master.

## Limits and next acceptance

The legacy media peer fixture now exercises an exact interrupted update reservation;
it does not establish fresh native NFS media admission. An unmounted NFS fixture is
correctly rejected by the new native preflight instead of treating journal health
as native readiness. Native helper success paths and peer orchestration are tested
separately.

The subsequent [two-node native lab](2026-09-18-native-placement-lab.md) exercised
real nested KVM/libvirt, local-storage create/update/start on master and member,
shared NFS member placement, exact retries, negative resource checks and service
restart continuity. It found and fixed two storage defects. This supersedes the
initial lack of a disposable native environment; see that report for exact artifacts,
executed outage checks and remaining limits.

Controller replacement, shared-storage identity changes, injected membership changes
during a real native operation and interrupted multi-host recovery remain open.
Physical storage, fencing, mixed-host CPU compatibility, migration and HA are not
certified. The existing Phase 4 fault/recovery gates remain open.
Hosted GitHub and Gitea runs were not executed; both existing push/PR workflows run
`go test -race -timeout 20m ./...`, which includes the added Go regressions.
