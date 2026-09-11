# Dedicated physical-memory reservation checks

Date: 2026-09-11. Development working tree; not a release or deployed-host claim.
This report summarizes executed checks with fictional UI fixtures. Native measurements below
are transcribed from the disposable-lab test output; raw workstation paths, generated identities
and private lab keys are excluded. Values are not screenshots of the operator's host.

## Scope and sources

The shared Core XML builder sets immediate allocation, locked memory and no KSM sharing for
dedicated VMs, keeping ballooning disabled. Maximum stays on demand. Definition observation
checks backing/currentMemory. Older running dedicated VMs must stop and save before live edits;
no existing guest is stopped automatically. State remains in the Core journal and libvirt domain.

API impact: existing contract — client create/update/power, Core `vm-create`/`vm-update`/`vm-power`
and managed inventory keep the same fields, authentication and replay/result paths. The API
reference and OpenAPI descriptions explain the enforcement and old-Core/old-definition limit.
Guide impact: updated — creation, editing and guest-tools pages describe physical allocation,
shutdown release, overhead, failure and the existing-VM upgrade steps. Milestone status unchanged.

Research used primary sources:

- [Libvirt memory backing](https://libvirt.org/formatdomain.html#memory-backing): immediate
  allocation, locked memory and disabling KSM sharing.
- [Libvirt memory tuning](https://libvirt.org/formatdomain.html#memory-tuning): locking includes
  variable QEMU overhead; an undersized guessed hard limit can kill a VM. No speculative cap added.
- [QEMU invocation](https://www.qemu.org/docs/master/system/invocation.html): native memory locking.

## Executed automated checks

- `go test ./internal/vmhost -run 'TestManaged(DedicatedMemoryReservation|LegacyDedicatedLiveEditRequiresStop)$' -count=1`:
  failed before implementation (missing backing; legacy edit reached later validation), passed
  after. The regression checks backing removal/weakening, reduced guest allocation, normalized
  units, unchanged maximum mode and rejection before legacy live-edit effects.
- `go test -race ./internal/vmhost ./internal/agent ./internal/controller ./internal/protocol`:
  passed. Privileged opt-in tests are skipped in this workstation run; native memory evidence is below.
- `python3 scripts/generate_api.py --check`: both generated references current.
- `python3 -m unittest discover -s tests -p 'test_api_contract.py'`: 21 checks passed.
- `npm --prefix web run typecheck`, `npm --prefix web run test`, `npm --prefix web run build`:
  passed after final UI copy changes.

Both `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` already execute the focused
Go regressions through `go test -race ./...` on pushes and pull requests. No hosted CI run was
started. The privileged native test is opt-in and is not claimed as an executed hosted job.

## Native libvirt measurements

A new private Debian 13.6 cloud-image overlay ran under nested KVM with loopback-only SSH and
user networking. No existing lab or live host was changed. Environment: kernel
`6.12.107+deb13-cloud-amd64`, libvirt `11.3.0-3+deb13u2`, QEMU `10.0.11`.
The final native run used these SHA256 digests (working-tree inputs, not a release):

- `internal/vmhost/managed.go`: `0e93ce60f15a789bae6fac0e303ef06c15b466b94bbbb0e8fe35e279b0dea34a`
- `internal/vmhost/memory_libvirt_test.go`: `a570d880af86803139da7059d7cb3e0363a384e7b522735041412e16587134f0`
- Executed Go test binary: `606aae48272f2da6871ee5e7aaed911be561256cefe80f78a42c122eb94d10a9`

Compile the existing Go test binary with `go test -c ./internal/vmhost`, transfer it to an
explicitly disposable libvirt host, then run as root:

```sh
NEX_MEMORY_LIBVIRT_TEST=1 ./vmhost.test -test.v -test.run='^TestDedicatedMemoryLibvirt$'
```

The test generates its own UUIDs, defines 512 MiB VMs without host disks or networks, and starts
with `--paused`, before guest instructions can touch RAM. It checks live backing XML, validates
libvirt's normalized **inactive** XML through the production observer's validator, and reads
QEMU process memory from `/proc`. Measurements:

| Configuration | RSS (KiB) | Locked (KiB) | Swap (KiB) |
|---|---:|---:|---:|
| Original dedicated implementation | 44,140 | 0 | 0 |
| Fixed maximum | 44,564 | 0 | 0 |
| Fixed dedicated | 685,992 | 947,648 | 0 |

The original binary failed for absent dedicated backing; the final corrected binary passed all four
native subtests in 3.34 seconds. The dedicated process held the complete guest allocation
plus QEMU overhead before execution. Locked memory and RSS measure different things; neither is
an in-guest application-usage statistic.

Stopping each generated domain removed its QEMU process and released its memory. A separate
bounded QEMU process with 64 MiB RAM, an 8 MiB memlock limit and locking capability removed failed
immediately with `mlockall: Cannot allocate memory`; no host-pressure/OOM test was run. Domain
cleanup used only test-generated UUIDs and left no test domains. The disposable outer VM was
stopped after each run.

The fourth subtest executed the real `ManagedEngine`: registered private local storage, created
one thin 1 GiB disk and a stopped 512 MiB dedicated VM, then removed backing from its journal and
libvirt XML to simulate a pre-correction definition. It executed `vm-update` with revision 1 and
the same memory settings. The stopped save succeeded, retained the UUID and disk path, passed
persistent-definition validation and booted paused with 1,025,776 KiB locked. This verifies the
Core stopped-save upgrade path with real libvirt; legacy input is deliberately simulated.

## Browser review

Chromium exercised the actual React creation, details and edit components with fictional,
simulated props, including switching to Maximum and mobile layouts. It found no page errors or
horizontal overflow. Captures were visually inspected, including the Core upgrade/stopped-save
prerequisite added after independent review. This is a copy/layout review, not a Controller/Core
API walkthrough or a claim that the displayed fixture has locked RAM.

- [Creation memory options](create-memory.png)
- [Configured memory details on mobile](details-mobile.png)
- [Stopped editor on mobile](editor-mobile.png)

The editor fixture intentionally has no disks, so it shows validation and a disabled save button;
it does not prove the existing-VM save workflow.

## Limits and acceptance

The original memory defect and its native libvirt correction are reproduced and verified.
The existing-VM stopped save path passed through the real Core engine and libvirt using a
simulated legacy journal/domain. The browser save-to-Controller-to-Core upgrade was not exercised
as one connected workflow; the browser review and Core execution were separate checks.
No live operator VM was changed, no update was deployed, and no physical-machine, long-duration
memory-pressure, suspend/managed-save, migration or HA guarantee is established. The native locking
failure test exercises QEMU's resource failure, not a Controller/browser failed-start walkthrough.
QEMU locks its process overhead too; the 2 GiB admission margin is not a measured per-VM overhead
cap or a guarantee against all host pressure. Phase 2 acceptance remains open.

## Final review and repository gates

Independent review verified Core ownership, existing API contract, pre-effect legacy refusal,
mode behavior and documentation. It found a UI overclaim for old definitions; the details now
say configured policy and the UI exposes the updated-Core/stopped-save prerequisite. The
reviewer inspected the corrected mobile captures and the final native stopped-save test and
found no further issue. The final `go test -race ./internal/vmhost` also passed after the native
test addition (the privileged test is executed separately as documented above).

`python3 scripts/check_docs.py --all` initially passed documentation structure; it does not
assess diff coverage. A final repeat encountered the concurrently added
`docs/guide/storage-repositories.md` with required sections out of order; that unrelated page
is outside this change. `python3 scripts/check_docs.py --worktree` failed on unrelated concurrent storage
paths missing change assessments, including storage Core/protocol/controller code and packaging.
The dedicated-memory record covers this change's paths; no unrelated assessment was invented.
A later worktree repeat also encountered a concurrent storage-documentation link to the not-yet
present `docs/qa/storage-repositories/checks.md`. These repository gates remain blocked by
the in-progress storage batch.
`python3 -m unittest discover -s tests -p 'test_repository_privacy.py'` passed. Manual review of
new evidence found fictional fixture identities only; PNG chunks are IHDR/IDAT/IEND with no
text or EXIF metadata. `git diff --check` passed at verification time.
