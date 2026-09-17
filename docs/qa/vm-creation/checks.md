# Full VM creation system checks

Date: 2026-09-10. Scope: [full VM creation system slice](../../plans/2026-09-10-vm-creation-system.md)
(API impact: changed contract — `createVM`, VM inventory shape, `vm-create` payload).
Status: Go, contract and UI checks passed locally. The documentation gate failures were corrected in
the [independent review round](#independent-review-round--2026-09-10). Hosted `phase1` failed on
BUG-0047 (disk-name default). Lab integration runs and the Phase 2 acceptance gate were **not run**.
Sections before the review round cover the pre-review tree.

## Environment

Arch Linux workstation, kernel 7.1.4-arch1-1, Go 1.26.5, Node v22.22.3, npm 10.9.8,
Python 3.14.6. Uncommitted tree at `4d8ad13`. `/dev/kvm` was present, but libvirt and a Playwright
environment were absent, so no integration or browser run happened.

## Executed root checks

| Command | Result |
|---|---|
| `go build ./...`, `go vet ./...`, `git diff --check` | exit 0 |
| `go test -race -count=1 ./...` | exit 0; agent 2.653s, controller 90.263s, localcore 11.014s, nexupdate 1.072s, protocol 1.016s, vmhost 1.729s, websecurity 1.010s, wsock 1.012s |
| `npm run typecheck`, `npm run build` (in `web/`) | exit 0 |
| `python3 -m unittest discover -s tests -p 'test_*.py'` | exit 1: 86 tests, 1 error, 6 skipped. Every failure was environmental: no nginx (`test_website_hosting`), no `dpkg-deb`/`dpkg-scanpackages` (4 × `test_apt_publish`), no `dpkg-divert` (2 × `test_core_local_package`). All other tests passed. |
| `python3 -m unittest tests.test_api_contract -v` | 21 OK |
| `python3 scripts/generate_api.py --check` | both references current |
| `python3 scripts/check_docs.py --all` | exit 0 (does not check diff coverage) |
| Range-mode `check_docs` (emulated through `check_docs.check` with the worktree changed set) | 3 failures, corrected in the review round: `controller-console-login.json` `guide.pages` fell outside the range; the assessment was missing `docs/guide/README.md` and this report. The committed `4d8ad13` already failed `--all`, because `api.impact: "existing contract"` was not an accepted value. |

## What the passing Go tests actually cover

Checked by reading the test bodies:

- **Shared bounds:** both sides call `protocol.ValidateVMSpec`, and Controller adds a
  64-vCPU/262144 MiB API ceiling. Tests: `TestValidateVMSpec`, `TestValidateVMSpecMemoryModes`,
  `TestVMCPUTopology`, `TestDerivedMAC`, `TestManagedVMCommandValidation`.
- **Domain XML:** `TestManagedDomainXMLDevicesAndBootOrder` checks bus-mapped `vda`/`sda`/`sdb`, a
  single `virtio-scsi` controller, ISO `sdc` at boot order 1, no mixed `<os><boot>`, derived vs manual
  MAC and `e1000e`. `TestManagedDomainXMLMemoryModesAndTopology` checks dedicated → no balloon,
  maximum → `virtio` with `freePageReporting`, and BIOS/UEFI firmware.
- **Disk allocation:** `TestManagedDiskPlanSumsPerTarget` checks per-target sums, an exclusive
  `nex-vms/<vmid>`, stable `disk-N.qcow2` names and refusal of unregistered storage.
  `TestManagedDiskAllocationArguments` pins the thick/thin `qemu-img` argv only; real allocation was not run.
- **Per-disk ownership:** `TestManagedCreationEvidenceCoversEveryDisk` refuses missing evidence for any
  disk and ownership substitution for a disk, a second disk or the ISO.
- **Capacity:** `TestManagedCapacityUsesTheMemoryCeiling` covers both modes, the 2 GiB reserve and a
  vCPU ceiling; the API returns 409 over capacity.
- **Controller refusals:** `TestCreateVMSpecificationReferencesAndScope` covers 30+ invalid inputs,
  stale references (404), unverified registration (404), capacity/wrong-pool/stale-host/revoked-host
  (409) and `request_id` replay (200). Authorization and body limits: `TestManagedAuthorizationAndActualBodyLimits`.
  Follow-on start: `TestStartAfterCreateIssuesTheExistingPowerStart`. Autostart: `TestManagedAutostartObservation`.

## CI workflow coverage

GitHub and Gitea workflows differ only in action references, the `permissions` key and the expression
context. Both run Go race tests, which compile all `cmd/` binaries, plus UI typecheck/build, the API
check and discovery, `documentation.yml`, and `managed_vm_browser.py --simulated` (updated for two disks,
two NICs, topology, maximum memory, ISO-first boot, invalid-input refusal, lost-reply retry, 360 px).
Neither runs `go vet` or `git diff --check`. The VM journal round-trip runs only when the runner has
`/dev/kvm` (`controller_replacement_debian.sh`). No workflow was edited.

## OUTSTANDING

**Phase 2 closed 2026-09-17** ([close record](../2026-09-17-phase2-gate/CLOSED.md)).

Historical note: gate5 row 11 found BUG-0256; that fix is lab-verified on published 0.3.1.
Queued-create polish and related residuals are accepted Phase 2 polish limits, not open gates.

- ~~Phase 2 acceptance gate~~ closed (gate5 functional clauses + 0.3.1 focused lab + 0.3.2 VirtIO).
- Browser walkthrough: walked on gate5; queued-create residuals accepted at close.
- Lab integration covered by gate5 / focused lab where applicable (`managed_vm.py`,
  `controller_replacement.py --vm`, `fresh_install_browser.py`, and related labs). Hosted CI after
  owner push remains an owner Git operation and does not reopen Phase 2.
- Follow-ups outside Phase 2: Windows guests on physical hardware, migration and restore.

## Independent review round — 2026-09-10

Two independent reviewers (implementation and documentation) each found the same real defect:
**dedicated memory was never reserved.** Preflight compared one VM against total host RAM, so five
16 GiB dedicated VMs were accepted on a 16 GiB host, contradicting the wizard, guide and API text.

| Finding | Correction | Evidence |
|---|---|---|
| Dedicated memory not admission-controlled | `hostMemory` sums non-failed dedicated commitments from acceptance, plus a 2048 MiB reserve | `TestDedicatedMemoryIsAdmissionControlled`; mutation-checked (fails with the condition disabled); boundary 6144 accepted, 6145 refused |
| API reference named `adopt-vm-create:` | Corrected to the emitted `adopt-vm:` | `generate_api.py --check` passes |
| `guest_os` documented as driving VirtIO defaults | Corrected (bus/model are per device) in API docs, schemas, plan | source read |
| Guide never named **Add network interface** | Added | `docs/guide/create-virtual-machine.md` |
| Assessment missed changed paths | `docs/guide/README.md`, this report and the record were added to `paths` | emulated range check passes over 30 paths |
| Oversubscription warning / reclaim indicator documented but absent | Claims removed; recorded as follow-ups | `docs/plans/2026-09-10-vm-creation-system.md` |

The two out-of-scope `api.impact` rewrites were reverted. Their conflict with `check_docs.py`
predates this slice and is recorded in the plan's follow-ups. Everything under OUTSTANDING is unchanged.
