# Workflow regression evidence — 2026-09-18

Scope: 0.3.3 development checkout at `52c8f80`, incorporating the three-file PR delta
from `248e428` without changing Git history, plus the local fixes assessed here.

## Hosted observations

Read actual forge job logs for the latest PR and corresponding push. The PR foundation
workflow failed on the initial host fresh-badge assertion; the push foundation workflow
failed on clustered-storage initial navigation. Go, web, website and contract jobs passed
in the PR run, including the actual Controller package startup check. Later browser steps
were skipped after the failing assertions, so that workflow was not successful overall.
The push documentation gate also failed because the latest commit had no assessment for
`scripts/gitea-release.py`, `tests/test_gitea_release.py` and
`tests/integration/managed_vm_browser.py`. The PR documentation and installer checks passed.
A later runner cleanup timeout was secondary to the documentation failure.

## Local verification

- Release publisher: six Python unittest checks passed against a disposable simulated HTTP
  forge. Applying those tests to the previous publisher failed three checks. They cover
  draft creation, publication after upload, dropped upload retry and failed-upload cleanup.
- Clustered storage: full Playwright browser suite passed. A separate actual Chromium load
  with a deliberately delayed 4s asset timed out at 3.00s before the fix and loaded in
  4.02s after it. The permanent timeout regression failed before and passed after the fix.
  The fence-dialog capture was inspected for visible confirmation and disabled deletion.
- Enrollment/recovery: `phase1.py --browser` passed all 21 recorded checks using the local
  Playwright environment, built Controller/agent and simulated host boundary. The new test
  injects one stale observation, restores the real API and waits for the normal UI poll.
  The old 5s assertion failed deterministically; the corrected 15s assertion passed.
  The recovered host capture was inspected and showed fresh inventory and host details.
- Native VM mode: the permanent structural regression passes with the simulator guard
  and fails four unguarded accesses against the original harness. It does not execute VMs.
- Both providers' existing push/PR foundation workflows discover `tests/test_*.py` and run
  the affected browser scripts. No workflow trigger or gate was removed.

## Limits

The exact hosted scheduling and asset-load delays were not reproduced. No host freshness
policy changed. The native managed-VM harness's simulator-only fault injection is now
excluded in native mode; the complete native VM lab was not rerun for this change.
Local release checks simulate the forge and do not prove hosted publication or cleanup
under a killed runner. A cleanup request can itself fail and leave a draft.
Older forges may expose draft attachments through direct URLs; draft status is not an
access-control guarantee. Documentation worktree checks, repository privacy checks and
`git diff --check` passed. Independent code/documentation review completed.
Raw CI logs and browser captures contain local environment details and remain untracked.
No push or hosted rerun was performed; hosted acceptance remains pending.

API impact: none — release tooling and acceptance harnesses do not change any Nex API,
agent operation, authorization, result or adoption contract. State owner: none — no
product records change. Guide impact: updated release publication and recovery guidance.
No phase or roadmap acceptance gate is newly signed off.
