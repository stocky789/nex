# Console media: guest eject and shared-VM media checks

Date: 2026-09-15. Development working tree on Go 1.26.5, Debian-derived workstation (no lab host).
Covers BUG-0184 (a guest empties its own CD tray) and BUG-0185 (console eject/insert on a VM using
pool-shared storage). API impact: changed contract — `GuestInfo.media` observation; `vm-media` on a
shared VM is applied as the master-reserved `vm-update` transition under the same operation ID.

All results below are from simulated hosts: a private `virsh` stand-in for the Core engine and
admission tests, and a mock API with simulated RFB for the browser check. **No guest installation,
libvirt host, pool master or shared repository was exercised, and no hosted CI run is claimed.**

## Executed checks

- `go test -race -count=1 ./...`: passed (full suite, both CI providers run the same command).
- `go test ./internal/vmhost -run 'TestManagedMediaAfterGuestEject' -count=1`, new regression:
  - with the fix reverted in `changeMedia`, it failed — the eject short-circuited and called no
    `virsh` at all, leaving the persistent definition holding the ISO:
    `eject must clear only the persistent definition: "... domstate ... dumpxml ..."`;
  - with only the `mediaObserved` half reverted, it failed —
    `interrupted eject must not read an empty live tray as complete: {... Status:succeeded ...}`;
  - with both in place it passes, issuing `change-media <vm> sda --force --config --eject` (no
    `--live`, which real `virsh` refuses on an already-empty tray) and re-inserting the recorded
    ISO with `--force --live --config <path> --update`.
- `go test ./internal/agent -run 'SharedVMMedia' -count=1`: passed. With `PoolVMMediaCommand` and the
  admission change disabled, the three new tests failed, including
  `reconnecting the recorded ISO was refused: shared VM media changes require a durable pool
  definition transition`.
- `go test ./internal/vmhost -run 'TestObserveGuest|TestManagedApplyLive' -count=1`: passed
  (`media` observation values; `--config`-only eject when the guest already emptied the live tray).
- `go test ./internal/controller -count=1`: passed. `TestManagedWorkflowScopeRetryAndMedia` now also
  proves the report passes `media` through to `GET /api/v1/vms` without changing the recorded
  `media_id`, refuses an unknown value with 400 and accepts an older Core that sends none; with the
  validation removed it failed (`POST /api/v1/agent/report: got 200 want 400`).
- `npm --prefix web run typecheck`, `npm --prefix web test`, `npm --prefix web run build`: passed.
- `tests/integration/console_controls_browser.py --evidence .cache/console-controls-browser`:
  passed, including the new check *A guest-ejected tray is reported as ejected, the recorded ISO
  stays insertable, and reconnecting it clears the notice*. With `web/src/main.tsx` restored to its
  pre-fix state and the UI rebuilt, it failed:
  `Locator expected to have text 'demo-installer.iso (ejected in the VM)'`.
  Inspected `.cache/console-controls-browser/mock-media-guest-ejected.png`: the CD/DVD control shows
  `demo-installer.iso (ejected in the VM)`, the same ISO is selectable, and the status line reads
  *The VM ejected the disc. Insert it again to reconnect it.*
- `tests/integration/managed_vm_browser.py --simulated`: passed unchanged (six checks), so the media
  control change did not disturb the VM creation, naming or stale-observation workflows.
- `python3 scripts/generate_api.py --check`, `python3 -m unittest discover -s tests -p
  'test_repository_privacy.py'`, `python3 scripts/check_docs.py --worktree`: passed.
- `python3 -m unittest discover -s tests -p 'test_*.py'`: 249 tests, one environment error —
  `test_website_hosting` needs nginx, which this workstation does not have and CI installs. No other
  failure; it is unrelated to this change.

## Not verified

- No native guest-install walkthrough: the Debian installer *Select and install software* eject,
  the re-insert from the console and the installer's Continue path were not exercised on a host.
- No pool with shared NFS/SMB storage was used: the master reservation, commit and re-adoption of a
  media revision were exercised only through the Core-local master path with a simulated `virsh`.
- `virsh` behaviour on a live-empty tray is taken from the existing repository evidence recorded in
  BUG-0181/`TestManagedMediaChange` (real `virsh` refuses an eject on media that is already empty),
  not from a new observation.
- Hosted GitHub/Gitea Actions were not run; both workflows already run every command above except
  the focused `-run` invocations, which are subsets of `go test -race ./...`.
