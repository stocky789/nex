# Public repository privacy cleanup

Date: 2026-09-11. Scope: tracked and nonignored current repository files, with a
bounded read-only review of reachable Git history and public gallery images.

## Changes

Personal names in contributor rules, decisions, and plans now use role labels.
Private workstation paths, forge/account details, hostnames, and observed lab
identifiers are anonymized. Historical QA records retain their recorded outcomes;
identifiers were sanitized after execution, not collected in new runs. Consistent
aliases preserve relationships between resources. Artifact checksums and source
commit references remain for provenance.

Deployment has no personal-account fallback: the release workflow needs the
`NEX_DEPLOY_USER` secret, and APT bootstrap needs a validated `NEX_PUBLISH_USER`.
Installer examples and negative privacy fixtures use fictional identifiers.
`AGENTS.md` now requires public-safe documentation, examples, and evidence.

Required upstream notices, public dependency identifiers, and the organizational
public signing key remain. The four gallery images were visually inspected; their
network identifiers match deliberately simulated historical capture fixtures.

API impact: none — publishing configuration and repository content do not change
Controller/Core commands, client-facing endpoints, authorization, or observed-state
contracts. Guide impact: updated — publishing prerequisites and installer examples.
State owner: none — no product state or adoption record is introduced.

## Verification

Local environment: Linux development checkout, Python 3.14, Bash, and a C compiler.
Three independent audit lanes reviewed documentation, source/deployment code, and
history/artifacts; an independent reviewer rechecked the cleanup.

- Manual review found and removed personal names and private infrastructure labels.
  The repository regression rejects original personal home paths and observed QA IDs,
  and passes after sanitization. It checks tracked/nonignored file contents, filenames,
  and symbolic-link targets for home paths, credential patterns, and observed QA IDs;
  findings print only locations and categories. It retains no personal-name list or
  hashed identity list; arbitrary names still require the mandatory manual review.
- Two APT bootstrap checks run a temporary copy with temporary paths and command
  stubs. Missing/invalid account inputs stop before target creation; the configured
  account controls the rendered sudoers entry. They passed with an inherited account
  variable present, and failed against the previous script's missing-input behavior.
  This is a simulation, not a live server bootstrap or privilege-policy installation.
- The documentation worktree gate and API reference generation check passed.
- Installer hostname, disk, and compiled network integration checks passed after the
  fictional hostname substitution. Bash syntax and paired workflow equality passed.
- Both CI providers' existing Phase 1 unittest discovery includes the new regression
  files on pushes and pull requests. No hosted CI run or deployment was performed.
- The full local unittest discovery ran 135 tests and reported one setup error:
  `test_website_hosting` requires nginx, which is not installed locally. This is not
  a complete suite pass.
- Focused command: `python3 -m unittest tests.test_repository_privacy
  tests.test_apt_bootstrap_privacy tests.test_apt_publish tests.test_website_public_files
  tests.test_ci_security tests.test_api_contract tests.test_build_source -v` — 36 passed.

## Limits

This cleanup changes current files only. Reachable Git history still contains old
identities; old releases, remote caches, forks, and clones were not removed. Git
history and remote operations remain the repository owner's responsibility under
`AGENTS.md`. Ignored ISO/package caches and unreachable Git objects were not fully
inspected or rewritten; do not publish a wholesale workspace archive. Pattern
checks cannot establish the absence of arbitrary confidential information.
