# Workflow failure review — 2026-09-17

## Scope and evidence

Reviewed all ten workflow runs from the two pushes in the 04:05:19–04:25:19 UTC
window, including every job and logs from cancelled and successful jobs. This is a
sanitized summary of authenticated hosted logs, not an untouched log capture.
Private forge addresses, checkout paths, account details and execution identifiers
are intentionally omitted. The checkout matched the newer push.

## Observed failures

| Workflow / job | Earlier push | Later push | Actual failure |
| --- | --- | --- | --- |
| Installer / installer | Failed | Failed | `installer-shell.py` expected obsolete numeric wizard stages; language no longer belongs to stage zero. |
| Controller foundation / controller-browser | Failed before run cancellation | Failed | Clustered-storage mock did not establish the browser's signed-in marker, so navigation waited on a login page. |
| Controller foundation / go | Failed test before run cancellation | Failed | Earlier: `TestManagedWorkerRejectsDifferentHostWithoutEffects` exceeded a one-second test guard. Later: `TestHandoffAdoptsObservedApplianceAndResumesOffline` exhausted its shared 15-second service context. |
| Controller foundation / contracts | Passed | Failed | nginx accepted a connection before answering HTTP; `setUpClass` let `TimeoutError` escape instead of retrying readiness. |
| Controller foundation / phase1 | Cancelled | Failed | Aggregate gate correctly rejected the failed prerequisite lanes; no separate defect in the gate. |
| Website / website | Failed | Failed | Publish succeeded; the final configured external health URL returned HTTP 401. |

Both documentation runs and both UI build/test runs passed. The earlier release
and parts of Controller foundation were cancelled by the newer push's configured
concurrency policy; cancellation is distinct from the failures already present in
their logs. The later website-browser lane passed. The later Release run was still building
at the final read-only status check; its eventual result is not claimed here.

## Coverage limits

The host lanes reported success while explicitly skipping their privileged checks:
network namespaces and Docker workspace bind mounts were unavailable. Those runs
provide no guest VLAN, namespace Controller replacement, local-host browser,
Debian libvirt or libvirt replacement coverage. The installer lane is intentionally
source-only and does not establish ISO boot, disk installation or native GTK behavior.

Browser reruns use simulated Core replies or disposable local services. Go tests use
simulated host commands. Neither proves native storage, fencing, hardware or recovery
acceptance. No commit, push, live deployment or hosted rerun was performed for this fix.

## API, guide and durable-state assessment

API impact: **none** — corrections affect regression fixtures and CI verification;
no client-facing request, Controller operation, Core command or observed-result
contract changes. Guide impact: **none** for test corrections; operator behavior is
unchanged. State owner: **none**; no product state is added or moved.

## Local corrections and checks

The installer now asserts semantic stage names from its actual rail instead of old
indices. Browser fixtures seed the returning-session marker, select repository type
by label, and check current media/enrollment notices. The handoff test uses a
service-lifetime context while preserving production operation deadlines. The worker
identity test allows scheduling/startup overhead without weakening identity or
no-journal-effect assertions. nginx fixture readiness retries transient HTTP timeouts
and always releases its process, directory and connections, including setup failures.

Executed in the local Linux workspace with Go 1.27.1 (race enabled), Python 3.14.7,
installed nginx and the existing Playwright Chromium environment:

- `go test -race ./...`: passed across all packages (Controller 138.243 s); packages
  reported cached results where Go reused unchanged inputs.
- `python3 scripts/generate_api.py --check`: all generated references current.
- `python3 -m unittest discover -s tests -p 'test_*.py' -v`: 307 tests passed.
- `bash installer/check.sh`: passed.
- Installer workflow source regressions: `installer-shell.py`, compiled
  `installer-network.c`, `installer-handoff.py`, `installer-keyboard.py`,
  `installer-qol.py`, `installer-hostname.py`, `installer-link.py` and
  `installer-disk.py`: all passed.
- `npm --prefix web run build`: passed.
- All eleven controller-browser workflow scripts passed: `iso_url_browser.py`,
  `storage_repositories_browser.py`, `cluster_storage_browser.py`,
  `website_controller_captures.py`, `phase1.py --browser`,
  `console_controls_browser.py`, `managed_vm_browser.py --simulated`,
  `vm_edit_browser.py`, `host_iso_browser.py`, `site_selector_browser.py` and
  `form_select_picker.py`. Captures of cluster fencing, console media failure and
  enrollment were visually inspected. Evidence remains in ignored local cache directories.
- `go test -race ./internal/localcore -count=3`: passed. A temporary 16-second
  readiness-delay overlay failed with the original lifetime deadline and passed
  after its removal; this explicitly simulates a slow reply.
- `go test -race ./internal/agent -run '^TestManagedWorkerRejectsDifferentHostWithoutEffects$' -count=100`:
  passed. A temporary 1.5-second worker-start delay reproduced the original failure
  and passed after correction; the delay was removed afterward.
- Worker rejection, slow-command reporting/cancellation and nonblocking seed tests
  passed together under `-race -count=10`.
- Independent review and focused reruns of installer, nginx, worker identity and
  handoff corrections found no material defects; browser review caught an additional
  loading-state assertion that could pass on a transient initial render. The corrected
  check first verifies fresh-browser login without a session probe, then holds a real
  returning-browser session request. Its full `phase1.py --browser` rerun passed;
  independent inspection confirmed the recapture shows the actual connecting screen.

The existing push/PR workflows on both forges already execute all corrected tests.
No successful hosted rerun is claimed by these local results.

## Website health URL

Both deployments completed their local publish/health operation, but the configured
external endpoint returned 401. A read-only request to the default public homepage
returned 200. Those observations do not prove the protected endpoint was meant to
be public or authorize removing its access policy. The endpoint decision is pending:
use the public website address or authenticate the intentionally protected endpoint.
No endpoint secret or live access policy was changed, and a 401 remains a failure.
The Release workflow shares this health URL configuration.

Final documentation worktree, repository privacy and diff-whitespace checks passed.
