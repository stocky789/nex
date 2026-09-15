# Media eject boot order checks (BUG-0188)

Date: 2026-09-15. Development working tree on Go 1.26.5, Debian-derived workstation (no lab host).
Covers BUG-0188: ejecting a disc from a running VM was refused with
`stop the VM first to change the boot order` because the durable record took the emptied CD drive
out of the boot order. API impact: existing contract (`POST /api/v1/vms/{id}/media`, `vm-media`,
`vm-update`); the recorded specification changes only `media_id`, as the published reference
already stated.

All results below are from the Go test suite with simulated hosts (a private `virsh` stand-in).
**No libvirt host, pool master, shared repository, guest or hosted CI run was exercised.**

## Executed checks

- `go test -race -count=1 ./...`: passed (full suite; both CI providers run the same command).
- `go test ./internal/protocol -run 'TestApplyVMMedia' -count=1`: passed. With the one-line fix
  reverted (`v.Boot.MediaBootOrder = 0` restored in `ApplyVMMedia`), both failed, reproducing the
  reported message:
  `media change classified stop-first: [stop the VM first to change the boot order]` and
  `eject: {Firmware:uefi Machine:q35 MediaBootOrder:0} [{... BootOrder:2}]`.
- `go test ./internal/agent -run 'TestSharedVMMediaChangeBecomesDurableDefinitionUpdate' -count=1`:
  passed. Its fixture is now install-media-first (`media_boot_order` 1, disk `boot_order` 2), the
  shape the create wizard produces when an ISO is chosen, and it asserts `NeedsStop` reports nothing
  for the derived `vm-update`. With the fix reverted it failed; with the fix but the old
  `media_boot_order` 0 fixture, the assertion passes vacuously, which is how the defect shipped.
- `go test ./internal/vmhost -run 'TestManagedRecordVMMediaKeepsBootOrderOnEject|TestManagedMediaChange' -count=1`:
  passed; both failed with the fix reverted. The journal after a running eject keeps the drive's
  boot order and clears `media_id` only.
- `go test ./internal/controller -run 'TestMirrorVMMediaKeepsBootOrderOnEject|TestMediaRequestsAreAuthorizedDurableAndDispatched' -count=1`:
  passed; both failed with the fix reverted. The Controller mirror matches the host record.
- `python3 scripts/check_docs.py --worktree`: passed (16 uncommitted paths; structural only).
- `python3 -m unittest discover -s tests -p 'test_*.py'`: 249 tests, one error —
  `test_website_hosting` needs nginx, which this workstation does not have and CI installs. That
  prerequisite is unrelated to this change. `test_api_contract` first failed on the `VMCreateSpec`
  copy in `docs/api/peer-openapi.json`, which must match `docs/api/openapi.json` verbatim; after
  syncing it and rebuilding `peer.html` with `scripts/generate_api.py`, it passes.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`: passed.

## Evidence that the record now matches the host

`virsh change-media` swaps the drive's `<source>` and leaves every device's `<boot order>` alone,
the CD drive's own included; libvirt's config-side disk update accepts only a media change for a
CDROM.

Previously recorded evidence from a real libvirt host, not a new run: the VM edit lab
([lab-results.json](../vm-edit/lab-results.json), 2026-09-10) created an install-media-first VM
(`media_boot_order` 1) and, in step *3-edit-running*, ejected its ISO from a **running** guest
through `vm-update` while leaving `media_boot_order` at 1 — the shape a media eject now takes.
The operation is recorded `succeeded`. That path ends in `observeDefinition`, which compares the
expected CD drive boot order against the host's persistent definition, so the host's domain
demonstrably still carried the emptied drive's `bootindex`.

Two existing repository checks depend on the same behaviour and still pass unchanged:
`TestManagedApplyLiveReconcilesDevices` builds the expected definition for a live eject from the
unchanged specification (cdrom keeps `<boot order>`, loses its source), and `validateManagedDefinition`
compares that expected boot order against the host's persistent definition after every update. A
record that zeroed the order would therefore have failed observation even if the stop-first check
had been relaxed.

## Not verified

- No native walkthrough: eject and re-insert from the console of a running install-media-first VM
  on a real host, with a pool-shared repository, was not exercised. Acceptance stays open.
- No UI checks were run: this change touches no web source. The guide troubleshooting entries added
  here describe an error produced by hosts older than 0.2.4 and were not reproduced in a browser.
- Hosted GitHub/Gitea Actions were not run; both workflows already run `go test -race ./...` and the
  `tests/` unittest suite, of which the focused `-run` invocations above are subsets.
- Repair of records already zeroed by an older Core is not implemented; the consequence and the
  operator remedy are recorded in the bug ledger entry.
