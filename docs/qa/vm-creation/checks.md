# Full VM creation system checks

Date: 2026-09-10. Status: Go, contract and UI checks passed on the workstation; the documentation
gate passes in `--all` mode but **fails in the range mode CI actually runs**; the browser
walkthrough, the disposable-lab integration runs and the Phase 2 acceptance gate were **not run**.
This is a workstation QA pass over an uncommitted working tree, not an independent review.

## Scope

The [full VM creation system slice](../../plans/2026-09-10-vm-creation-system.md) replaces the flat
single-disk `protocol.VMCreateSpec` with identity/guest-OS, CPU count with optional topology and
model, `dedicated` or `maximum` memory, 1–8 disks, 0–8 NICs with derived or manual MACs,
firmware/machine/per-device boot order, install media, autostart and start-after-creation; shared
bounds live in `internal/protocol` and are enforced identically by Controller and Core; the wizard
moves from `web/src/managed.tsx` into a new six-step `web/src/vm-create.tsx`.
API impact: **changed contract** (`createVM`, the VM inventory shape and the `vm-create` command
payload; `powerVM`/`vm-power` unchanged and reused for the follow-on start).
Guide impact: **updated** ([Create a virtual machine](../../guide/create-virtual-machine.md),
[Install guest tools](../../guide/guest-tools.md)).

## Environment

Arch Linux workstation, kernel 7.1.4-arch1-1. Go 1.26.5, Node v22.22.3, npm 10.9.8,
Python 3.14.6. Uncommitted working tree on `main` at `4d8ad13` (31 changed paths, nothing
committed by this pass). `/dev/kvm` present but **libvirt/`virsh` absent** and no Playwright
environment (`.cache/phase1-qa-venv` does not exist), so no integration or browser run was
possible here.

## Executed root checks

Every command below was actually run from `/home/matt/Projects/Nex` (or `web/`) against the final
working tree; results are the real output.

| Command | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0, no diagnostics |
| `go test -race -count=1 ./...` | **exit 0**, all `ok`: `internal/agent` 2.653s, `internal/controller` 90.263s, `internal/localcore` 11.014s, `internal/nexupdate` 1.072s, `internal/protocol` 1.016s, `internal/vmhost` 1.729s, `internal/websecurity` 1.010s, `internal/wsock` 1.012s; `cmd/nex-controller`, `cmd/nex-core-agent`, `cmd/nex-core-local` and `internal/version` have no test files |
| `npm run typecheck` (in `web/`) | exit 0 (`tsc --noEmit`) |
| `npm run build` (in `web/`) | exit 0 (`node build.mjs`) |
| `python3 -m unittest discover -s tests -p 'test_*.py'` | **exit 1** — 86 tests, 1 error, 6 skipped; see the environmental breakdown below |
| `python3 -m unittest tests.test_api_contract -v` | exit 0 — 21 tests, `OK`, including `ManagedVMContractTests.test_managed_request_fields_and_command_payloads_match_runtime` and both `test_generated_reference_is_current_and_offline` cases |
| `python3 scripts/generate_api.py --check` | exit 0 — `index.html is current`, `local.html is current` |
| `python3 scripts/check_docs.py --all` | exit 0 — `Documentation structure passed; historical references not resolved and diff coverage NOT checked (--all).` |
| `python3 scripts/check_docs.py --base … --head …` (the mode CI runs) | **not runnable here and failing when emulated** — see [Documentation gate](#documentation-gate-two-real-failures) |
| `git diff --check` | exit 0, no whitespace errors |

The single Python error and the six skips are environmental on this workstation and are not caused
by this change:

- `ERROR: setUpClass (test_website_hosting.WebsiteHosting)` — `RuntimeError: nginx is required; CI installs nginx-core`. nginx is not installed here; both workflows install `nginx-core`.
- 4 × `test_apt_publish.AptPublishTests` — `skipped 'dpkg-deb and dpkg-scanpackages required'`.
- 2 × `test_core_local_package.CoreLocalPackageTests` — `skipped 'dpkg-divert missing: Debian dpkg tools are required for this check'`.

Excluding `test_website_hosting`, every other test in the discovery run passed.

## What the passing Go tests actually cover

Verified by reading the test bodies, not by test name:

- **Shared bounds.** `protocol.ValidateVMSpec` is a single helper called from both sides —
  `internal/controller/managed.go:630` and `internal/vmhost/managed.go:230` (Controller adds its own
  `CPU.Count > 64` / `Memory.SizeMiB > 262144` API ceiling at `managed.go:407`). Exercised by
  `TestValidateVMSpec`, `TestValidateVMSpecMemoryModes`, `TestVMCPUTopology`, `TestDerivedMAC`
  (`internal/protocol/managed_test.go`, new file) and `TestManagedVMCommandValidation`
  (`internal/vmhost`).
- **Domain XML.** `TestManagedDomainXMLDevicesAndBootOrder` asserts three disks across two storage
  targets map to `vda`/`sda`/`sdb` by bus, that SCSI disks get exactly one `virtio-scsi` controller,
  that the ISO lands as `sdc` with boot order 1, that per-device `<boot order>` is **not** mixed with
  an `<os><boot>` list, and that NIC 0/2 get distinct derived MACs while NIC 1 keeps its manual
  `52:54:00:aa:bb:cc` with the `e1000e` model. `TestManagedDomainXMLMemoryModesAndTopology` asserts
  `dedicated` → `memballoon model="none"` with memory held, `maximum` → `virtio` with
  `freePageReporting="on"`, and iterates `bios` and `uefi` checking `OS.Firmware == "efi"` only for
  UEFI with an empty `OS.Boot` list.
- **Disk allocation.** `TestManagedDiskPlanSumsPerTarget` checks per-target byte sums
  (`fast` = 20+30+4 GiB, `bulk` = 100+4 GiB), the exclusive `…/nex-vms/<vmid>` directory per target,
  index-stable `disk-N.qcow2` names across targets, and refusal of unregistered storage.
  `TestManagedDiskAllocationArguments` pins the exact `qemu-img` argv for thick
  (`create -f qcow2 -o preallocation=falloc … 20G`) and thin (`create -f qcow2 … 40G`).
- **Exclusive ownership per disk.** `observeCreated` re-runs `exclusiveDisk` for every disk
  (`internal/vmhost/managed.go:1022`); `TestManagedCreationEvidenceCoversEveryDisk` proves a journal
  missing one disk's evidence is refused with `evidence unavailable`, and the tampering table covers
  `disk ownership`, `second disk ownership` and `ISO ownership` substitutions.
- **Capacity.** `TestManagedCapacityUsesTheMemoryCeiling` runs both modes through
  `managedMemoryFits` and refuses a ceiling that eats the 2 GiB host reserve, plus more vCPUs than
  host CPUs. At the API boundary the controller test's `over-capacity` case returns 409.
- **Controller refusals.** `TestCreateVMSpecificationReferencesAndScope` covers 30+ invalid-input
  cases (disk count/name escape/size/bus, boot-order gap and clash, CPU topology/model/floor/ceiling,
  memory mode/floor/ceiling, firmware, machine-type escape, guest OS, description length, NIC
  count/model, multicast/upper-case/short/repeated MAC, media boot order, client-supplied ID,
  invalid name), stale references (`storage`/`network`/`media` → 404), an unverified registration
  (404), over-capacity (409), `wrong-pool` (409), `stale-host` (409, "must be fresh") and
  `revoked-host` (409); replay of the same `request_id` returns 200 rather than a second create.
  Authorization and body limits for these routes stay covered by the pre-existing
  `TestManagedAuthorizationAndActualBodyLimits`. `TestStartAfterCreateIssuesTheExistingPowerStart`
  covers the follow-on `vm-power` start.
- **Autostart.** `TestManagedAutostartObservation`.

`TestManagedDiskAllocationArguments` pins the command line, not a real allocation: thick
preallocation, real free-space behaviour and real `virsh autostart` are host facts and remain
outstanding below.

## Documentation gate: two real failures

`scripts/check_docs.py --all` passes, but its own output says it does **not** check change coverage.
The mode both workflows run is `--base/--head`, which refuses a dirty worktree
(`range checks require a clean worktree`), so it could not be run directly here and this pass may
not commit. It was emulated by calling the checker's own `check_docs.check(root, changed)` with the
working-tree changed-path set (`git status --porcelain --untracked-files=all`), i.e. the same
assertions with a different source of `changed`. Three failures, all **outside this pass's write set**:

1. `docs/changes/2026-09-10-controller-console-login.json: guide updated requires pages changed in
   this range`. This working tree edits that already-committed record (its `api.impact` is corrected
   from `"existing contract"` to `"updated"`), which puts it in the range and makes `check_record`
   re-validate it there; its `guide.pages` names `docs/guide/deploy-controller-appliance.md`, which
   this range does not touch. Committing and pushing this tree as one change fails the Documentation
   job on both forges. `docs/changes/2026-09-10-default-iso-storage.json` gets the same correction
   but passes, because its `guide.pages` (`docs/guide/create-virtual-machine.md`) is in the range.
2. `changed paths missing documentation assessment: docs/guide/README.md`. `docs/guide/README.md` is
   modified (it rewrites the managed-VM scope sentence to cover multiple disks and NICs, memory
   modes, boot order and autostart) but appears in no changed record's `paths`. The cheap fix is
   adding it to `paths` in `docs/changes/2026-09-10-vm-creation-system.json`.
3. `changed paths missing documentation assessment: docs/qa/vm-creation/checks.md` — this report,
   added by this pass. Existing records list their `docs/qa/**` artifacts in `paths` (and usually in
   `verification.evidence`), so the same record needs this path added. Flagged rather than fixed:
   `docs/changes/2026-09-10-vm-creation-system.json` is owned by another writer.

Related, and pre-existing rather than caused by this slice: replaying `check_record` against the
**committed HEAD content** of both records fails with `api.impact must be updated or none`, because
`"existing contract"` is not one of the two values the checker accepts. So `check_docs.py --all`
fails on a clean checkout of `4d8ad13`; this working tree is what repairs it. No claim is made here
about what hosted CI did or did not report on that commit.

## CI workflow coverage (AGENTS.md requires both forges)

`.github/workflows/` and `.gitea/workflows/` were diffed. `installer.yml` is byte-identical;
`phase1.yml` and `documentation.yml` differ only in the action references
(`actions/checkout` vs `https://github.com/actions/checkout`), the `permissions` key
(`contents:` vs `code:`) and the `github.` vs `gitea.` expression context. Step contents are
otherwise identical, so every row below is genuinely present on both forges.

| Required check | Covered on both forges |
|---|---|
| Go tests | Yes — `go test -race ./...` (`phase1.yml`), which picks up the new `internal/protocol/managed_test.go` automatically |
| Go build | Effectively — no standalone `go build ./...` step, but `go test -race ./...` compiles every package and the three `cmd/` binaries are built explicitly |
| UI typecheck and build | Yes — `npm --prefix web run typecheck` and `npm --prefix web run build` |
| API check | Yes — `python3 scripts/generate_api.py --check` and `python3 -m unittest discover -s tests -p 'test_*.py' -v` (which runs `test_api_contract.py`) |
| Docs checker | Yes — `documentation.yml` on `push` and `pull_request`, plus `test_check_docs.py` |
| New six-step wizard browser walkthrough | Yes — `tests/integration/managed_vm_browser.py --simulated`, which this batch updated to drive two disks, two NICs, an explicit topology, maximum memory, an ISO-first boot order, in-place refusal of an invalid topology / duplicate disk name / malformed MAC, lost-reply retry, and a 360 px pass |
| **`go vet ./...`** | **No — neither forge runs it.** Passed locally here only |
| **`git diff --check`** | **No — neither forge runs it.** Passed locally here only |
| VM journal round-trip | **Conditional.** `phase1.yml` runs `controller_replacement.py` without `--vm`; the VM path comes from `controller_replacement_debian.sh`, which only adds `--vm` when `/dev/kvm` is a character device and otherwise prints `NOTE: /dev/kvm absent … VM definitions will not be covered by this run`. Coverage therefore depends on the runner and is not guaranteed |

No workflow file was edited by this pass. The `go vet`, `git diff --check` and conditional
journal-round-trip gaps are reported, not fixed.

## OUTSTANDING

Nothing in this section was executed. None of it may be described as verified.

- **The Phase 2 acceptance gate has NOT been run.** No real guest was installed from an ISO through
  Nex Controller; no reboot-and-verify-written-data check was done on either the guest or the host;
  the Controller was not stopped to confirm the VM keeps running; and no reconnect was exercised to
  confirm it refreshes inventory and resolves unfinished tasks. This needs a disposable nested-KVM
  lab and was not part of this implementation batch.
- **No browser walkthrough of the new six-step wizard was performed.** There is no Playwright
  environment on this workstation and no evidence directory for it. `managed_vm_browser.py` was
  updated for the new wizard but not executed, so validation, empty, stale, failed and interrupted
  states, narrow-screen and keyboard operation are unverified and no captures were inspected.
  `docs/qa/managed-vm/browser-simulated-results.json` is unmodified and records the **old** four-step
  flat single-disk flow; it is not evidence for this wizard.
- **No disposable-lab integration run.** `virsh` is absent here, so `tests/integration/managed_vm.py`,
  `controller_replacement.py --vm`, `fresh_install_browser.py`, `console_lab.py`,
  `console_features_lab.py` and `vlan_lab.py` were all updated for the new shape but not run. Real
  per-disk allocation, thick `falloc` preallocation, per-disk exclusive ownership against live
  libvirt, per-target free space, the `virsh autostart` property, `freePageReporting` reclaim in a
  running guest, the follow-on start against a real host, and the journal round-trip through a
  replaced Controller are all unverified.
- **The documentation gate fails in range mode** with the three errors named above. Until they are
  resolved the Documentation job will fail on both forges. Two of the three are one-line `paths`
  additions to `docs/changes/2026-09-10-vm-creation-system.json`
  (`docs/guide/README.md` and `docs/qa/vm-creation/checks.md`); the third needs a decision about
  `docs/changes/2026-09-10-controller-console-login.json`, whose `guide.pages` entry
  `docs/guide/deploy-controller-appliance.md` is not in this range.
- **`go vet ./...` and `git diff --check` are not in either CI workflow.** They passed here; that is
  a local result only.
- **No hosted CI run is claimed.** Nothing was committed or pushed by this pass, so no GitHub Actions
  or Gitea Actions run exists to inspect. Every result above is a local workstation result.
- **This is not the independent review** the plan requires. It is a check-and-evidence pass by the
  same batch; the independent implementation and documentation review is still open.
- Windows guests, physical hardware, migration and restore paths were not exercised.

## Independent review round — 2026-09-10

Two independent reviewers (implementation and documentation) reviewed the uncommitted tree.
Their most severe finding was the same one, found separately by both, and it was a real defect:

**Dedicated memory was never reserved.** Preflight compared a single VM's ceiling against the
host's *total* RAM and never summed what the host had already promised. Five 16 GiB dedicated VMs
were accepted on a 16 GiB host, while the wizard, guide and API reference all told the operator the
memory was "reserved from the host and never given to another VM". As shipped, Dedicated and
Maximum differed only in whether freed pages returned — not in reservation.

Corrections applied and verified:

| Finding | Correction | Evidence |
|---|---|---|
| Dedicated memory not admission-controlled | `hostMemory` sums this host's non-failed dedicated commitments; creation is refused when the request plus a 2048 MiB host reserve would exceed host RAM. Counted from acceptance, because creation leaves the VM stopped and observed memory proves nothing about what is promised. | `TestDedicatedMemoryIsAdmissionControlled`, `internal/controller/managed.go` |
| API reference named the wrong adoption `request_id` | `adopt-vm-create:` corrected to the emitted `adopt-vm:` in `docs/API.md`, `openapi.json` and `index.html` | `generate_api.py --check` passes |
| `guest_os` documented as driving VirtIO defaults | False — bus and model are per device. Corrected in `docs/API.md`, both schemas and the plan; the guide was already correct. | source read of `internal/protocol/managed.go`, `web/src/vm-create.tsx` |
| Guide never named **Add network interface** | Added; an operator following the guide can now add a second NIC | `docs/guide/create-virtual-machine.md` |
| Change assessment missed changed paths | `docs/guide/README.md`, `docs/qa/vm-creation/checks.md` and the record itself added to `paths` | range check passes over all 30 recorded paths |
| Oversubscription warning documented but absent | Claim removed from the schemas and the plan; recorded as an explicit follow-up | `docs/plans/2026-09-10-vm-creation-system.md` |
| Reclaim-availability indicator documented but absent | Claim corrected in the plan; recorded as an explicit follow-up | `docs/plans/2026-09-10-vm-creation-system.md` |

The regression test was mutation-checked: disabling the admission-control condition makes
`TestDedicatedMemoryIsAdmissionControlled` fail, and restoring it makes it pass. It exercises the
boundary in both directions (8192 promised + 2048 reserve leaves exactly 6144; 6145 is refused).

**Documentation gate, corrected.** Both CI workflows run `check_docs.py` in range mode
(`--base/--head`) only, never `--all`. Emulating range mode over this slice's 30 recorded paths
passes. The two out-of-scope edits that rewrote other slices' `api.impact` were reverted, which
removes them from this range; their pre-existing conflict with `check_docs.py:120` is recorded in
the plan's follow-ups for their owners. `check_docs.py --all` fails on those two records both with
and without this slice's changes — it is a HEAD condition, not one introduced here.

**Still outstanding after this round:** everything in the section above, unchanged. The Phase 2
acceptance gate — real ISO guest installation, browser console walkthrough, guest and host reboot
data persistence, and the Controller-outage test — has not been run and needs a disposable
nested-KVM lab. No hosted CI run is claimed.
