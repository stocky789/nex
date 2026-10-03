# Immutable review fixes — disposable verification

Date: 2026-10-03. Source: the `immutable` working tree based on
`dc8e5d01cbf7a09b0e93c4b16cdf8c8b537c6358`, declaring Nex 0.4.19, with uncommitted
review fixes. No release, phase acceptance, hosted CI result or hardware certification is implied.

## Scope and ownership

The batch addresses the 34 findings in the immutable branch review, plus defects found by
independent review and verification of those fixes. Core owns execution, arm/swap journals,
rollout progress and replicated control receipts. Controller submits commands and displays
host-reported state; its existing task/audit projection is not the pool's durable record.

API impact: **changed contract** for additive rollout control receipts, pending-control recovery,
authenticated recovery of the existing Controller task, and capacity/pending refusal codes.
Other installer, image engine, appliance, publisher, lab and build fixes reuse existing contracts
or have no Controller/Core API. The reference and all three machine-readable contracts were
updated together. Guide impact: **updated**. No milestone was promoted to accepted/built.

All implementation and independent-review workers reported `openai-codex/gpt-6.1-sol`.
No agent committed, staged, pushed, published artifacts remotely or operated a live host.

## Environment

- Linux x86-64; Go `go1.27.1-X:nodwarf5`, Python 3.14.7, Node 24.21.0, npm 12.1.0.
- Native Debian version comparison: `dpkg` 1.23.7.
- Chromium 150 for the real React panel; simulated API responses, not an enrolled host.
- Native temporary files, sparse regular-file disk images, process groups, signals and `flock`.
- Unit integration fixtures fake libvirt, peer responses, sysfs/mount observations and Docker.
  Those substitutes do not establish kernel RAID, QEMU boot, storage or firmware correctness.

## Executed checks

### Go behavior and contract checks

The affected packages were exercised with:

```sh
go test -p 2 -count=1 ./internal/hostimage ./internal/nexupdate ./internal/agent \
  ./internal/controller ./internal/localcore ./internal/vmhost ./internal/protocol \
  ./internal/rootfs ./cmd/nex-core-local ./cmd/nex-hostimage \
  ./internal/nexupdate/cmd/nex-update
```

Host-image, image-update engine, Controller, local Core, appliance, protocol, rootfs and all
three command packages passed. After the final mixed-snapshot capacity correction, the focused
`./internal/agent ./internal/localcore` run passed (51.565 s and 11.272 s), completing coverage
of the changed Go packages.

The recovery/capacity concurrency check also passed:

```sh
go test -race -p 2 -count=1 ./internal/agent ./internal/nexupdate \
  -run 'CoreContinues|UnacknowledgedControl|ControlCapacity|ConcurrentControl|NewRolloutCapacity|PendingResultReservation|PromotionPreservesMaintenance|ManagedPoolControl|InterruptedArm|InterruptedArmedApply|CancelStagedInterrupted|FinishedActivation|FailedNonArming|FullLengthCorrupt'
```

Results: agent 12.951 s, nexupdate 1.416 s; no reported race.

The tests cover interrupted arm publication and delayed reboot recovery, cancellation and
corrupted partial downloads; disk preflight before writes; stopped/autostarted appliance
checkpoint recovery; exact control replay, pending transport/ACK recovery, policy conflicts,
promotion and authority admission. They use disposable state files and fake host engines.

Verification caught and corrected regression-fixture mistakes: reading private durable fields
from the public status DTO, a non-monotonic signed channel serial, reusing a Go decode target
when an `omitempty` field disappeared, and a nonexistent test helper. These were not grounds
for weakening production validation or suppressing failed checks.

### CI lifecycle and release versioning

```sh
bash -n scripts/ci-heavy-step.sh
python3 -m unittest tests.test_ci_serialization tests.test_ci_security tests.test_release_version
```

Final frozen-source verification passed **56 tests in 20.616 s**, plus Bash syntax. Native and
mocked-Docker cases exercise exclusive workloads, failure status, ignored TERM, helper loss,
wrapper/queued SIGKILL/TERM, checkout-free gating, realistic monitor-mode profiles and teardown.
Cleanup retains the mutex until all verified workload-session members are non-live, across
different job-control process groups. Earlier 54-check results preceded the profile corrections.
The actual final native wrapper ran an unchanged `BASH_ENV` monitor-mode profile, printed
`ran-with-job-control-profile` and propagated its workload's exit **7**, with empty stderr.

The real `release-version.py` CLI produced:

```text
0.4.19-dev.999 0.4.19~~dev.999
0.4.19-dev.1000 0.4.19~~dev.1000
```

Native `dpkg --compare-versions` accepted the order
`~~dev.999 < ~~dev.1000 < ~alpha1 < ~rc1 < final` for the same numeric base.
When the stable fixture already published that base's alpha, the real CLI refused a development
build of its newer rc heading with exit 1 and requested the next numeric release heading.

A separate invocation of the actual CI wrapper ran two temporary native scripts against one
private lock directory. The waiter did not enter while the owner held it; observed events were
`owner-enter`, `owner-exit`, `waiter-enter`, `waiter-exit`, and both wrappers exited 0. An
exclusive-directory sentinel made overlapping execution fail rather than merely checking logs.
This proves the local native path, not a real Docker-host volume or forge queue.

### Actual installer and text-wizard smoke

A built `nex-hostimage` binary verified a checksum/verity-valid **nonbootable fixture release**,
then refused both `install` and `repair` against two hard-link aliases of one 32 GiB sparse
regular-file scratch disk. Both commands exited 1 with `disk ... is listed twice`; disk size,
nanosecond modification time and the complete sentinel bytes remained unchanged. No block
device, formatter, mount or real disk was selected. This proves alias preflight through the CLI,
not a successful installation or physical in-use-device handling.

```sh
python3 tests/integration/installer-text-wizard.py
sh installer/check.sh
```

Passed 26 native-`sh` wizard scenarios, two template checks and the installer shell/contract checks. The harness
executes the actual text wizard with a simulated debconf frontend and controlled device/network
tools, including distinct action/NIC choices, both Erase/Repair Back directions, offline
confirmation, netinst navigation, validation and handoff. BusyBox was absent: **ash was skipped**.
No booted ISO or physical console is implied.

The persistent-shell compatibility probe regression failed before subshell isolation in both
successful-mount and failed-mount cases, then passed with the fix. The focused metadata/ISO
safety check passed five tests. It executes the metadata reader as shell code over real fixture
files with fake mount tools; parent shell options, variables and EXIT trap survive, cleanup runs
at the probe boundary, and an invalid retained format never reaches synthetic writes.

### UI type/build and real browser surface

```sh
cd web
npm run typecheck
node src/image-updates.test.mjs
node src/pool-rollout.test.mjs
node src/channel.test.mjs
npm run build
```

All commands passed. The primary then bundled the actual `ImageUpdates` component with the
production token/component styles into a disposable page explicitly labeled **simulated API**.
At 1200 × 1000 in Chromium, the primary exercised and inspected:

- Keyboard-triggered Check: the failed task's `check_throttled` error appeared in a light-theme
  error banner, with the running image state and task link retained.
- Retry: the old banner disappeared while queued; `needs_attention` displayed its
  `command_not_received` message in the dark-theme warning banner.
- Success: the warning disappeared, running-image information remained visible, and the task
  link was keyboard-focusable. Enter navigated to `#/tasks`; the full Tasks screen was not part
  of this fixture and was not verified.
- Zero page/console errors after clean navigation. Three real document/style/script requests
  returned 200. API calls were simulated in JavaScript, so no backend networking was audited.

Inspected captures, retained locally but ignored by Git:
`2026-10-03-immutable-update-failed-light.webp`,
`2026-10-03-immutable-update-attention-dark.webp`,
`2026-10-03-immutable-update-success-dark.webp`.
The primary inspected these on the date above. They contain fictional example identifiers.
Per [documentation policy](../DOCUMENTATION.md#qa-raster-captures-are-untracked), a fresh checkout
cannot re-inspect those images; this written record is the reviewable evidence. The temporary
browser tab and HTTP server were closed.

### API reference consistency

```sh
python3 scripts/generate_api.py
PYTHONPATH=tests python3 -m unittest tests.test_api_contract
```

Passed: **82 tests**, 0.552 s. This checks reference/schema consistency, not host behavior.
The source-text test that guessed all runtime refusal codes was removed, rather than re-pinned;
behavioral Core/Controller tests cover the changed recovery and refusal paths.

### Combined Python verification

The affected modules ran in a disposable copy of 2,452 tracked/new source paths, excluding
ignored build outputs and preserving file modes/symlinks. This avoids the original tree's
pre-existing unreadable image-output directories during package vendoring; those original
outputs and their permissions were not changed. `PYTHONPATH=tests` supplies the netinst
test module's existing bare imports.

```sh
PYTHONPATH=tests python3 -m unittest \
  tests.test_release_version tests.test_ci_serialization tests.test_ci_security \
  tests.test_update_publish tests.test_apt_publish tests.test_gitea_release \
  tests.test_release_publish tests.test_image_lock tests.test_image_composition \
  tests.test_image_installer tests.test_image_installer_labs tests.test_lab_drivers \
  tests.test_host_recovery tests.test_netinst_release tests.test_core_local_package \
  tests.test_appliance_build tests.test_api_contract
```

The first combined run executed **340 tests in 529.170 s**: 339 passed and one native CI
workflow serialization subcase failed because its leader exited before `leader-ready`, with
empty stdout/stderr. Its return code was not retained. The original failure's cause remains
unproven; targeted Go/race checks ran concurrently, but concurrency is not established as the cause.

Instrumented investigation separately reproduced the real `setsid` conditional-fork branch
with a `BASH_ENV` profile enabling monitor mode: the old wrapper returned 0 instead of the
workload's 7, and ignored-TERM cancellation blocked its waiter/retained stdio. Internal
job-control/launcher corrections and two native/mocked-Docker regression methods were added.
The subsequent source-copy run passed **342 tests in 510.073 s** at code fingerprint
`5cdc80a619b9280da7ea924c6874be03c46b311d4bdc14d1e48402ddb6a66d0c`.
Existing fixture file/HTTP ResourceWarnings were emitted.

Later independent review found that the workload itself can re-enable monitor mode and create
another process group inside its owned session; group-only cleanup was still insufficient.
The corrected helper scans/signals the complete verified workload SID and retains the mutex
until no non-zombie member remains. The regression no longer disables monitor mode inside
the workload. Instrumented native before/after used the same genuine different-PGID/shared-SID
topology: before, a live ignored-TERM sleep retained stdio and blocked the waiter; after, the
waiter exited 0, the descendant was non-live and wrapper stdio closed.

The final **56 focused CI/version/security checks** and native wrapper smoke passed after this
last two-file correction. Independent re-review found no remaining actionable cancellation,
lock-lifetime, exit-status or release-contract defect. The 342-test combined run precedes that
correction; all other Python sources were unchanged. The entire combined run was not repeated
afterward, and the original intermittent startup cause remains unproven.

### Independent review and documentation gate

Sol reviewers independently traced update recovery, disk/appliance safety, pool control replay
and capacity, installer/UI/lab evidence, publication and CI/release lifecycle. Findings were
returned to their implementation owners and corrected. The final pool-control review found no
remaining actionable defects after per-VM capacity projection; it ran no checks.

Two additional read-only reviewers checked API/retry/state ownership and installation/recovery/
publication guidance against the actual implementation and this evidence. One identified an
incorrect `rollback_started` description: durable intent is not proof of a replica ACK or host
dispatch. All three schemas and generated references were corrected; re-review found no
remaining actionable mismatch. Both reviewers accepted the exact-path material assessment,
Core-owned record/adoption path and unchanged roadmap decision. They ran no checks.

```sh
python3 scripts/generate_api.py --check
python3 scripts/check_docs.py --worktree
```

Passed: all three generated HTML references were current; documentation structure and supplied
impact records passed for **92 uncommitted paths**. This is structural/consistency proof, not
physical workflow or real forge/Docker certification.

The required full-root privacy check is **blocked by 14 pre-existing indexed ELF wrapper-shell
dumps containing private environment paths**. They were initially archived byte-for-byte for
diagnostics; the two-test pass while those files were absent does not establish a clean original
repository. Index inspection showed they are unrelated tracked files, so they were restored
unchanged rather than silently deleting user-owned content. Private diagnostic copies remain
outside the repository. No ignore exception or privacy-test change was added.

The existing privacy scanner separately checked **all 92 assessed changed/new paths** and
found **zero findings**. This does not clean the unrelated dumps or any published Git history.
The wrapper-shell artifacts do not establish the intermittent CI startup failure's cause.

Final code-file fingerprint (same method as the combined run):
`c1e3f0268eba7fef1dc207c9752bed830aebe09ac1971bece0f619e1c609eb36`.

## Safety and untested limits

- No physical disk, RAID mirror, actual mounted/swap/holder block target, TPM, Secure Boot
  firmware, real libvirt appliance swap or reflink checkpoint was exercised in this batch.
- No newly rebuilt ISO, full QEMU rollout, format-2 downgrade/fallback boot or live Controller
  walkthrough was run. Default format-3 inputs now explicitly skip that incompatible negative
  proof; supplied format-2 metadata is validated before any synthetic write.
- APT metadata replacements are individually atomic, not collectively atomic. Interruption can
  leave mixed metadata/hash/signature errors until the same publication is retried. Old indexed
  package bytes remain available through replacement, including same-version rebuilds.
- CI locking is step-level, not FIFO/whole-job scheduling. Native and container locks are
  separate domains; separate Docker hosts do not share a volume. Existing job timeouts include
  queue waits. No real forge job or Docker-host helper was executed here.
- Core preserves every accepted control receipt and refuses unrepresentable new work rather
  than evicting replay history. The existing 1 MiB authority ceiling remains a finite limit.
- After promotion, the original Controller task can remain `needs_attention` if its owning
  old Core never reports again; the current master's durable receipt/rollout is authoritative
  and completes independently of that old task/journal.
- Except for the persistent-shell and instrumented monitor-mode regressions described above,
  newly added regression checks were not separately executed against pre-fix source. Independent
  review traced their consumer behavior; do not describe those checks as observed failing-before runs.
