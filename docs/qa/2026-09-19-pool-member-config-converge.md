# Pool member config converge (networks + shared media)

Date: 2026-09-19

## Environment

Developer workstation unit tests (no two-host disposable lab in this slice). Run against the
uncommitted working tree on top of commit `5b094ab`; no build artifact, ISO or release version
is identified, and no host, hypervisor or network environment was involved. Test output was not
retained as an evidence file, so the results below are the author's record of the runs rather
than an inspectable artifact.

## Method

- `go test ./internal/agent -run 'TestPoolNetwork|TestPoolMedia|TestPoolStoragePeerPeriodic'`
- `go test ./internal/controller -run 'TestVLAN|TestMedia|TestPool|TestReady|TestAdopt|TestNetwork'`
- `go test ./internal/protocol ./internal/vmhost`

## Results

- Member peer list returns only pool VLANs (not host-local bridges).
- Member reconcile applies VLAN via stubbed materialize and posts access; master projects `member_access`.
- Member peer list returns only ISOs on pool-shared storage; reconcile adopts metadata and posts access.
- Existing storage peer reconcile and media download ownership checks still pass.

## Limits

- Native two-host join walkthrough (real uplinks, NFS/SMB mounts, ISO files) not re-run in this change.
- Join remains Core-local API only; Controller join UI is still outstanding.
- Backup repositories remain unimplemented (Phase 6).
- Website roadmap and Phase 4 plans updated 2026-09-19 to record guest VLAN/shared-ISO pull under
  the testing milestone: unit and peer checks passed; the native two-host walkthrough is open, as
  are staged management-network edits.
