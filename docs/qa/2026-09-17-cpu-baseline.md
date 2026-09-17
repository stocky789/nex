# Native pool CPU baseline checks

Date: 2026-09-17. Development working tree based on `18e7a46`; not deployed or released.

## Scope and environment

Core computes a common named CPU and explicit feature policies through native libvirt,
verifies it on every current pool member, and reports an observation. Selecting it saves
that definition in the existing Core VM records. No operating-system requirements or
CPU-model allowlist were introduced. Manual host-usable models remain available.

The browser walkthrough used a real HTTPS Controller and simulated mutually authenticated
Core reports. Its synthetic CPU definitions test contract/UI behavior, not hardware support.
Local captures and raw reports remain unpublished because they contain disposable resource
identities. Two independent reviewers inspected implementation lanes and a reviewer inspected
baseline and unavailable-state captures.

## Executed checks

- Focused Go checks for protocol, Controller, agent and vmhost CPU behavior passed. They cover
  feature bounds/policies, old-Core refusal, request replay, stopped-only updates, persisted
  feature rendering/drift, native comparison failure before start, and manual model behavior.
- Peer tests use real mutually authenticated HTTPS between disposable test peers with native
  CPU commands substituted. They cover membership generation, unavailable members, mixed
  vendors, exact comparison confirmation, unauthenticated requests and discovery invalidation.
- Browser acceptance `tests/integration/managed_vm_browser.py --simulated` passed. A new draft
  selected a reported model absent from the bare-model catalogue and retained its exact
  features/machine. A later failed report preserved the saved definition. A new unavailable
  baseline blocked progression until an explicit manual selection; report recovery did not
  replace that selection. `qemu64` and a newly discovered arbitrary model remained selectable.
- UI unit/SSR tests, TypeScript checking and production build passed. Four CPU walkthrough
  captures were inspected: baseline, saved-unavailable, new-unavailable and manual-preserved.
- The 29 API contract checks passed, including the new peer routes and additive report/spec
  fields. Generated API reference freshness and repository privacy checks passed.

## Native runtime

A disposable Debian 13 container with an init process and KVM access ran the exported native
CPU functions against libvirt `11.3.0-3+deb13u3` and QEMU `10.0.13`. One host observation and
two identical observations both produced `SierraForest-v1` with 196 explicit features; native
runtime comparison accepted each. Requiring the unavailable `3dnow` instruction was refused.
A second input with `avx` disabled produced a baseline retaining that negative override and
passed runtime comparison. This is one real host runtime plus a synthetically reduced second
CPU input, not a physical heterogeneous pool. The created container was removed.

## Final verification commands

- `go test -race -timeout 20m ./...`: passed; Controller package completed in 150.922 seconds, agent in 8.270 seconds and vmhost in 12.955 seconds.
- `python3 -m unittest tests.test_api_contract tests.test_repository_privacy`: 30 tests passed.
- `python3 scripts/generate_api.py --check`: all three generated references current.
- `python3 scripts/check_docs.py --worktree`: passed for the combined working tree.
- `git diff --check`: passed.

The UI owner ran `npm --prefix web run test`, `typecheck`, `build` and the complete simulated
managed-VM browser fixture successfully. Independent review corrections were verified before
handoff; no hosted CI result is claimed.

## Review corrections

Review caught a second bare-model availability check after successful native comparison in
Core. Explicit feature definitions now use the full native comparison; unmodified manual
models retain their existing availability check. Its regression uses a model marked unusable
without overrides and verifies that only the successfully compared explicit definition passes.
Review also identified repeated pool fanout and an incomplete-baseline report mismatch; these
are addressed by shared bounded discovery and refusal of incomplete successful observations.

## Limits

No Windows installation, heterogeneous physical pool, migration, host reboot or live deployment
was performed for this change. No result attributes the original installer reboot to a proven
CPU instruction fault. Pool CPU compatibility does not certify guest OS compatibility.
Native libvirt autostart bypasses the Nex start preflight. A compatible CPU baseline alone does
not establish storage, networking, firmware or runtime migration readiness. Future members can
be incompatible with already saved VM definitions; existing definitions never change with pool
membership. Hosted CI has not run for these uncommitted changes.

## CI regression coverage

Both `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` already execute the Go
race suite, Python contract tests, UI tests and simulated managed-VM browser fixture on pushes
and pull requests. No new test runner or dependency was introduced. Native host checks are
separate local evidence, not a claim of native CPU coverage on hosted runners.
