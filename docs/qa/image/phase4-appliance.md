# Phase I-4b Controller appliance swap and pool rollout view: results

Date: 2026-10-02. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), "Controller appliance" and "Pool rollout
coordinator" step 3 (Controller first). Slice I-4b: the journaled appliance swap in Core, the `scope:"controller"` update, the
controller_first step of the rollout, and the operator's rollout view.

**Environment: unit tests with a fake libvirt and fake peers only.** No VM, container, real libvirt, real guest, real pool, browser or
Controller appliance was used. The fake `virsh` is a shell script that keeps domain state, the persistent definition and the booted
system image in a temporary directory; the readiness probe is a Go function. Nothing below shows that a real guest shuts down through its
guest agent, that a reflink checkpoint works on the real storage filesystem, that libvirt accepts the redefined domain, or that a
verity system image boots. Resource limits: Go builds and tests ran under `systemd-run --user --scope -q -p CPUQuota=200% -p
MemoryMax=4G -p MemorySwapMax=0` with `nice -n 19 ionice -c3`, `GOMAXPROCS=2`, `go test -p 1`, one package set at a time; no subagent,
VM or container was started.

- API impact: **changed contract** (change record `docs/changes/2026-10-02-controller-appliance-swap.json`,
  [API.md](../../API.md#image-edition-controller-appliance-swap-and-controller_first-phase-i-4b)).
- State owner: **core** (the swap journal beside the deployment journal; the rollout's controller step in the master's `pool-update.json`).
- Guide impact: **updated**, all **unverified**: [Update a pool](../../guide/update-a-pool.md) is new and the
  [deploy guide](../../guide/deploy-controller-appliance.md) names the swap. No walkthrough was run.

## What exists

| Where | What |
|---|---|
| `internal/vmhost/appliance_swap.go` | `SwapAppliance`: the journaled, resumable swap (ownership, storage and checkpoint-room checks before anything is stopped; stage and verify, graceful shutdown, checkpoint, redefine, start, probe, commit; on failure restore the data checkpoint, redefine the old image, restart, probe, forcing off only a failed new image that has no working guest agent; double failure `needs_attention`); `HasControllerProfile` |
| `internal/vmhost/engine.go` | the two-disk profile: `Config.SystemImage`/`SystemImageSHA256`, read-only raw system disk plus `controller-data.qcow2`, UEFI, `checkDomain` shared by ownership checks; Deploy builds the profile |
| `internal/nexupdate/controller.go`, `image.go`, `offline.go` | `scope:"controller"` as operation kind `controller` (never moves the host lifecycle), `Appliance`/`ControllerProbe` hooks, the vm-image component staged under `/var/lib/nex/payload/<sha256>/`, `swapping` phase |
| `internal/localcore/appliance_probe.go`, `updates.go` | the pinned-certificate readiness probe, and the engine wired to the host's vmhost engine |
| `internal/agent/pool_update.go`, `pool_update_host.go`, `internal/protocol/poolupdate.go` | rollout phase `controller`, `PoolUpdateController`, step action `controller`, `controller_vm` and `controller_image` |
| `appliance/host-recovery.py`, `appliance/README.md` | host-recovery accepts the two-disk layout; the profile is documented |
| `web/src/pool-rollout.tsx`, `settings.tsx`, `settings.css` | the **Pool rollout** panel under Settings → Updates |
| `docs/API.md`, `docs/api/*` | contract and generated references |

## Verified (by automated tests only)

- `internal/vmhost` (fake libvirt): success keeps UUID and MAC and keeps the old image and checkpoint, the data disk is not rewritten by the
  swap, the image is read-only, one graceful `shutdown` and never `destroy`; the next swap prunes everything older than the previous image;
  readiness failure restores data and the old image and replays identically; double failure leaves the Controller stopped
  `needs_attention`; an unrestorable checkpoint is `needs_attention`; **interruption at every forward step (stage, shutdown, checkpoint,
  define, start, probe, commit) and at every restore step resumes to the same end state** without a second shutdown or checkpoint;
  exact replay is a no-op, a changed body conflicts, the same release under a new request ID is a no-op; a tampered or unstaged component
  is refused before the VM is touched; a refused or ignored graceful shutdown changes nothing and never forces; a swap cannot reuse the
  deployment's operation ID; a second unfinished swap is refused; a single-disk deployment and an absent deployment are refused; the
  profile domain passes the ownership check and a writable system disk does not.
- `internal/nexupdate`: `scope:"controller"` hands the verified component's digest and staged path to the swap, leaves the host's image
  lifecycle untouched, replays, passes the swap's refusal code through as `controller_<code>`, and is refused without an appliance engine,
  with `activate:"reboot"`, or for a release without a Controller image.
- `internal/localcore`: the readiness probe accepts only the pinned certificate and a 2xx answer.
- `internal/agent` (fake peers): no host activates before the Controller's host has swapped (the fake fails the test otherwise), under one
  step ID across coordinator restarts; a failed swap pauses `needs_attention` before any host is touched and resume asks again under a new
  attempt; a release without a Controller image skips the step; the host step maps onto a scoped apply and reports status truthfully.
- `host-recovery.py` ownership: the two-disk layout is accepted, a writable system disk or a foreign data disk is refused.
- Web: `pool-rollout.test.mjs` (controls, refusal parsing, request identity and replay, rendering of the record, the controller step,
  per-host steps, pause code and hint); `tsc --noEmit`.

## Review fixes (second review, fake libvirt and fake peers; each Go test failed with its fix reverted and passes with it)

- **Shared data disk.** The swap now refuses while another libvirt domain, running or defined, or a hardlink references
  `controller-data.qcow2`, and re-checks before the checkpoint (rolled back before the new image starts) and before a restore replaces the
  file (`needs_attention`, the disk is left as it is). `TestSwapRefusesADataDiskAnotherDomainReferences`,
  `TestSwapRechecksExclusivityBeforeCheckpointAndRestore`.
- **Durable, verified checkpoint and restore.** The copy is fsynced, read back byte for byte against its source and renamed with the directory
  fsynced; a corrupt copy stops the swap before the new image starts. `TestSwapCheckpointIsVerifiedAgainstTheDisk`. Real fsync/crash
  behaviour was not exercised.
- **Readiness names the image.** The running domain must be the owned layout booted from exactly the expected system image before the
  HTTPS answer counts, so the old Controller still answering after a definition that never took effect rolls back.
  `TestSwapReadinessNeedsTheExpectedImageToRun`. The Controller has no unauthenticated version route, so the booted image digest, not a
  Controller-reported version, is the identity; adding such a route is a Controller API change and was not made.
- **Controller scan failure.** A host whose Controller record cannot be read now fails its status (`controller_scan_unavailable`) instead of
  reporting no Controller, so the coordinator retries and eventually pauses `step_timeout` and never skips the step.
  `TestHostStatusFailsWhenTheControllerRecordCannotBeRead`.
- **Rollout panel.** One request at a time (`exclusive`), actions disabled while one is in flight, a failed read marks the master
  unreachable instead of showing the old record as live, a late read never replaces a newer report (`newerView`), and the panel re-reads
  every 15 s while idle. `pool-rollout.test.mjs` (`exclusive`, `newerView`); the idle re-read and the disabled buttons are not covered by a
  DOM test (the harness renders statically) and were not seen in a browser.

## Review fixes (third review, fake libvirt only; each Go test failed with its fix reverted and passes with it unless noted)

- **Autostart cannot skip the data restore.** A rollback restores the checkpoint when the persistent definition names the new image, not
  only when the journal recorded a start: Deploy sets `virsh autostart`, so a power loss between `define` and the journal write boots the
  new image with `Started` still false. `TestSwapRestoresDataWhenLibvirtAutostartedTheNewImage` edits the fake's state to model the
  autostart; it was not seen on libvirt.
- **A cancelled context is an interruption in every step.** It no longer journals a rollback (forward steps) or the terminal
  `needs_attention` (restore steps). `TestSwapCancelledContextInterruptsEveryStep` stalls the fake `cp`/`virsh` of the checkpoint, define,
  start, restore copy, redefine and restart steps, cancels the context while they run, and checks that the journal stays `running` at that
  step and that repeating the request finishes (completed, or rolled back with the data restored).
- **Staged image is fsynced** before the journal moves on, also when an earlier run placed it. The data is synced under its temporary name
  before the rename and the directory after it. `TestSwapSyncsTheStagedImageBeforeTheJournalAdvances` records the order through the
  `Engine.syncPath` seam (state of the files at each sync, and that both syncs precede the `swap_shutdown` step); it fails with the syncs
  removed or moved after the rename. It proves the order of calls, not that a real disk lost nothing: a crash that loses unsynced data was not produced.
- **A shutdown already under way is waited for** instead of failing the swap with the Controller about to power off.
  `TestSwapWaitsForAShutdownAlreadyUnderWay`. A guest that accepted the pre-swap request and has not stopped when the wait ends still fails
  the swap (`controller_shutdown_failed`, nothing changed, never forced) and may power off afterwards: documented in the guide, not prevented.
- **Admission runs the shared ownership guards** (`owned`: recorded paths and bridge, no symlinked disk, local ext4/XFS/Btrfs, plain qcow2,
  exclusive, power state) before the Controller is stopped. `TestSwapAdmissionRunsTheOwnershipGuards`; `Engine.statfs` is the test seam for the
  filesystem report.
- **Rollback of a new image with no guest agent.** The graceful shutdown of the failed guest, if refused or timed out, falls back to
  `virsh destroy` only when the running domain is booted from the new image and the checkpoint exists; an old image is never forced.
  `TestSwapRollbackForcesOnlyANewImageThatNeverBecameReady` (agent refuses, guest ignores the request, old image not forced). The destroy is
  the fake's; no real guest was forced off.
- **A replay answers with the first call's code.** The refusal code is stored in the journal; a staging failure that used to be labelled
  `shutdown_failed` is now `stage_failed`. `TestSwapReplayAnswersWithTheCodeOfTheFirstRefusal`.
- **Profile preflight needs the image file** and says what it checks. `TestPreflightBlocksAProfileWhoseSystemImageIsNotStaged`.
- **ext4 checkpoint.** The checkpoint is documented as a full copy where the filesystem cannot share extents, and the swap refuses with the new
  `controller_insufficient_storage` before the shutdown when `cp --reflink=always` fails and less than twice the data disk's allocated size
  is free. `TestSwapRefusesBeforeShutdownWhenTheCheckpointCannotFit` uses a fake `cp` and a fake filesystem report; real ext4 was not used.
- **An old image autostarted between the shutdown and the define is stopped again and the swap goes on** (`swap_checkpoint` or
  `swap_define`, after a power loss and reboot: libvirt autostarts the persistent definition, which still names the old image). Rolling back
  would not have been unsafe for the data (the definition still names the old image, so `Started` stays false and nothing is restored) but it
  reported `controller_rolled_back` for a new image that never ran, and nothing was attempted. Resume now stops that guest with the graceful
  request, only when both the persistent definition and the running domain name the old image and not the new one; it is never forced.
  At `swap_checkpoint` the checkpoint is then taken as usual. At `swap_define` the checkpoint predates what the old image wrote, and a
  rollback would give those writes up, so the step goes back to `swap_checkpoint`: the stale checkpoint is replaced from the quiescent
  disk (a byte-identical one is kept) and the swap continues. If the old image will not stop, the swap ends `failed`
  (`controller_shutdown_failed`, replays the same) with the definition and data untouched and the stale checkpoint removed; the old image
  keeps running. Any other running guest at those steps (a definition naming the new image) takes the existing rollback path, which
  restores the checkpoint. `TestSwapContinuesWhenLibvirtAutostartedTheOldImage` (both stages, new image ready and not ready: the
  checkpoint holds the old image's writes and a rollback keeps them), `TestSwapEndsWithoutChangesWhenARestartedOldImageWillNotStop`,
  `TestSwapRollbackForcesOnlyANewImageThatNeverBecameReady` ("never any other guest"). The autostart is modelled by editing the fake's
  state; it was not seen on libvirt. Not covered: the old image started and stopped again outside the swap before the resume, which leaves
  nothing running to notice and a checkpoint that may predate its writes.
- **Round-three leftovers:** a journal written before the refusal code existed replays as `controller_shutdown_failed`, the code its
  failures had (`TestSwapReplaysAJournalWithoutACode`). A copy that fails leaves no `.part` image and a `.img.part` left by a crash is
  pruned at commit (`TestSwapLeavesNoPartialImages`). A cancelled context during the admission reflink probe is an interruption, not a
  `controller_insufficient_storage` refusal (`TestSwapAdmissionInterruptedByCancelIsNotAStorageRefusal`).

## Not done (open)

- **The Controller guest image** was built and booted once afterwards: see [phase4-controller-image.md](phase4-controller-image.md). That record also lists
  what still did not run (a Core deployment of it, a real swap).
- **Removals not made, deliberately:** `packaging/controller/{apply-update,nex-controller-update.path,nex-controller-update.service,postinst,prerm}`,
  `appendUpdateBootstrap` (`internal/vmhost/engine.go`), the packaging and appliance-build copies of those helpers, and the Controller's own APT
  update routes and panel (`GET/POST /api/v1/updates`, `internal/controller/updates*.go`, `web/src/updates.ts`, `UpdatesPanel`). They remain the only way a
  single-disk appliance is updated. The guest image now exists, but the swap only covers the two-disk profile, so removing them would leave
  single-disk appliances and the classic edition with no update path; only the profile's seed stopped carrying them. Tests that pin them (`tests/test_distribution_packaging.py`, `engine_test.go`'s bootstrap test) are unchanged.
- Deploy wiring now exists (`nex-core-local --controller-system-image`, `core-local-setup configure --controller-image-sha256`; see
  [phase4-controller-image.md](phase4-controller-image.md)); the local deploy page itself still has no profile choice and no deployment ran. `Deploy`'s profile branch is not unit-tested end to end (its preflight needs KVM and a
  bridge); only the domain and ownership shapes are.
- A real run: reflink behaviour and the ext4 fallback on the host's storage, libvirt `define` of the edited XML, UEFI firmware selection (`firmware='efi'`), guest-agent
  shutdown timing, a forced stop of a real guest without an agent, the readiness probe against a real Controller, and the host-recovery export of a swapped Controller.
- A three-host nested pool with a Controller on a member; a browser walkthrough of the rollout panel; the guide pages remain unverified.
- Rollback of an already committed swap by the operator (the old image and checkpoint are kept; no route uses them yet).
- `ReportResponse.controller` / `controller-epoch-v1` (unchanged from the I-4a slice).

## Commands that ran

Run 2026-10-02 from the repository root, every Go, Python-with-load and Node command under the limits above:

- `go test -p 1 -count=1 ./internal/vmhost ./internal/nexupdate ./internal/localcore ./internal/agent ./internal/protocol ./internal/controller`: all six `ok`.
- `go vet ./internal/vmhost ./internal/nexupdate ./internal/localcore ./internal/agent ./internal/protocol`: one finding, in
  `internal/agent/backup_authority_transfer.go` (a copied lock value; not part of this slice, unchanged); nothing in the files of this slice.
- `python3 -m unittest tests.test_host_recovery`: 8 tests `OK`.
- `node src/pool-rollout.test.mjs`, `settings.test.mjs`, `ui.test.mjs`: pass; `tsc --noEmit`: exit 0 (a temporary `web/node_modules` link to an installed
  copy in a sibling worktree, removed afterwards).
- `python3 scripts/generate_api.py --check`: current; `python3 tests/test_api_contract.py`: 74 tests `OK`;
  `python3 scripts/check_docs.py --worktree`: passed; `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`: `OK`.

Third-review fixes, 2026-10-02, the `internal/vmhost` package only: `go test -count=1 ./internal/vmhost/...` `ok`; `go test -race -count=5 -run 'TestSwap|TestProfile' ./internal/vmhost/` `ok`;
`go vet ./internal/vmhost/` clean. The new swap tests were also run against the code with their fixes removed and failed there. No other package, Docker, KVM or QEMU was used.
