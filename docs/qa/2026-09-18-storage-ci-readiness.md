# Storage browser CI readiness

Date: 2026-09-18. Version: 0.3.4 development. Scope: browser test harnesses only.

## Observed failure

Read the actual hosted foundation job log. Repository browser line 244 failed the
`Open /exports` visibility expectation with a 5,000 ms deadline after reopening
the NFS dialog. Its final ARIA diagnostic contained the expected button. The Go,
contracts, web and website jobs passed; the aggregate gate failed with its browser
job. The separate release build was still running when checked. This does not
identify the exact runner scheduling cause or certify any release.

The unmodified repository browser scenario passed locally. Then a one-shot
5.5-second delay in discovery response delivery reproduced the same locator
failure before changing the expectation budget. The delay is intentionally retained
as a regression, alongside the existing Escape/late-close and credential races.

## Checks executed

Using the built Controller UI, local Playwright/Chromium and simulated API routes:

```sh
.cache/phase1-qa-venv/bin/python tests/integration/storage_repositories_browser.py --evidence .cache/storage-browser-delay-after
.cache/phase1-qa-venv/bin/python tests/integration/cluster_storage_browser.py
python3 -m unittest discover -s tests -p 'test_cluster_browser_timeouts.py'
python3 scripts/check_docs.py --worktree
python3 -m unittest discover -s tests -p 'test_repository_privacy.py'
git diff --check
```

Both browser scripts passed. The repository script completed 12 scenario groups
with no page errors, including delayed listing, Escape during a reply, old close
after reopening, NFS/SMB request construction, selection, error/empty states,
credential secrecy, typed detach confirmation and stale/unauthorized states.
The clustered script retained its delayed credential dispatch, stale-Core refusal,
unused-receipt warning, discovery cancellation and fence-result checks. The unit
check enforces consistent 30-second action, navigation and functional expectation
budgets in both scripts. Checks use conditions and actual captured request events;
no whole-test retries or unconditional readiness sleeps were added.

Both `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` already run
these browser scripts and discover the policy unit test on push and pull request.
The shared helper is loaded by each script in its separate Python process.

## Limits and impact

API impact: none. Guide impact: none. State owner: none. Only test synchronization
changes; Controller/Core behavior and operator workflows are unchanged. These
mocked checks establish no native storage, fencing or hardware correctness.
The fixed code has not been pushed and has no hosted rerun result.

## Follow-up fixture freshness

A subsequent hosted run included the readiness fixes and passed the repository
browser suite, then failed clustered VM storage selection with only the placeholder
option. The source commit matched the inspected worktree. The healthy observation
was created once at script startup; the selection happened beyond the product's
30-second freshness limit. This separate fixture-time dependency was missed by the
prior request-synchronization audit.

Ageing the original fixture by 31 seconds locally reproduced the exact assertion.
The correction uses a fixed browser Date of `2026-09-18T00:00:00Z` matching the
healthy fixture, with normal timers still running. The browser regression advances
Date by 31 seconds, verifies that the option disappears after inventory refresh,
then resets the scenario clock and verifies that it returns. No product guard or
API fixture response is modified to make stale data look current.

The full clustered browser script, `node web/src/storage.test.mjs`, and
`python3 -m unittest discover -s tests -p 'test_cluster_browser_timeouts.py'`
passed. Local logs are ignored under `.cache/cluster-freshness-before.log` and
`.cache/cluster-fixed-clock.log`. These remain simulated browser checks, not native
storage evidence or a hosted pass for the new correction.

A second full clustered run inserted a real 31-second `page.wait_for_timeout`
immediately before the VM-creation click in an in-memory copy of the harness.
That run also passed (log `.cache/cluster-fixed-clock-slow.log`), proving elapsed
wall time beyond the safety window does not expire the fixed snapshot. The extra
sleep was verification-only and is not added to CI. The normal harness retains the
controlled-clock expiry regression on every run.
