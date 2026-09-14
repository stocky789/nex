# Public repository privacy cleanup

Date: 2026-09-11. Scope: tracked and nonignored files, plus bounded read-only review of reachable
history and gallery images.

## Changes

- Personal names replaced by role labels; workstation paths, forge/account details, hostnames and lab
  identifiers anonymized with consistent aliases. Historical QA outcomes kept; identifiers sanitized
  after execution. Checksums and commit references kept.
- Release workflow requires `NEX_DEPLOY_USER`; APT bootstrap requires a validated `NEX_PUBLISH_USER`.
  Installer examples and negative fixtures fictional.
- Upstream notices, public dependency identifiers and organizational signing key kept. Four gallery
  images inspected: network identifiers are simulated historical fixtures.

API impact: none. Guide impact: updated (publishing prerequisites, installer examples). State owner: none.

## Verification

Linux checkout, Python 3.14, Bash, C compiler. Three independent audit lanes (docs, source/deploy,
history/artifacts) plus an independent recheck.

| Check | Result |
|---|---|
| Repository privacy regression (contents, filenames, symlink targets; home paths, credential patterns, observed QA IDs; prints locations only; no name list) | Rejects original paths/IDs; passes after sanitization |
| Two APT bootstrap checks (temp copy, stubs) | Missing/invalid account stops before target creation; account controls sudoers; pass with inherited variable; fail on previous script. Simulation only |
| Docs worktree gate, API reference generation | Passed |
| Installer hostname, disk, compiled network checks; Bash syntax; paired workflow equality | Passed |
| Full `unittest discover` | 135 tests, 1 setup error (`test_website_hosting` needs nginx): **not a full pass** |
| `python3 -m unittest tests.test_repository_privacy tests.test_apt_bootstrap_privacy tests.test_apt_publish tests.test_website_public_files tests.test_ci_security tests.test_api_contract tests.test_build_source -v` | 36 passed |

Both CI providers' Phase 1 discovery includes the new tests; no hosted run or deployment.

## Limits

Current files only: history, releases, caches, forks and clones still hold old identities (owner's
responsibility). Ignored ISO/package caches and unreachable objects not fully inspected; do not
publish a workspace archive. Pattern checks cannot prove absence of arbitrary confidential data.
