# Phase I-3a host update engine: results

Date: 2026-10-01. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), "Update pipeline, API and
pool-safe rollout" (host pipeline, release artifacts and trust, compatibility guards, host-local update API) and "Image
architecture" (boot chain, rollback triggers). Slice I-3a: the host-side engine only.

**Environment: unit tests only.** Go tests with fake sources and file-backed disks (sparse files, a runner that executes
nothing, ESP directories standing in for mounted FAT file systems). No VM, no real block device, no firmware, no Secure Boot, no
systemd-boot and no `systemd-bless-boot` was involved. The boot loader's entry ordering and counter handling are re-implemented in
`hostimage.BootOrder` / `MarkTried` / `Bless` from the BLS boot-assessment description (tries-left entries and counterless entries
first, higher sequence first, exhausted entries last): **[INFERENCE]** until a QEMU/OVMF run checks them against the real loader.
Nothing below was observed on hardware or in a VM.

- API impact: **changed contract** (host-local `GET /api/local/v1/updates`, `POST …/updates/apply` on an image-edition host) and
  **new contract** (`POST …/updates/check`, `activate`, `rollback`, `cancel`; additive `Inventory.image` types and the
  `image-update-v1` capability constant, no producer yet). `docs/API.md`, `docs/api/local-openapi.json`, `docs/api/openapi.json` and
  the generated HTML were updated; `scripts/generate_api.py --check` and `tests/test_api_contract.py` pass.
- Guide impact: none yet (no workflow can be walked through; see the change record). State owner: Core
  (`/var/lib/nex/image/state.json`, `update` subtree; the disks are the truth).

## Progress log

- Session 1 (about 40 min): hostimage helpers and tests (first 10 min), nexupdate engine and tests, localcore routes, protocol
  types, API documents, change record, race run. No approach failed twice; design corrections are noted under "Found by the tests".

## What exists

| Where | What |
|---|---|
| `internal/nexupdate/trust.go` | `trust.json` (+`trust.sig`), `channel.json` (+`channel.sig`), `manifest.json` (+`manifest.sig`): Ed25519, raw signatures over exact bytes, raw 32-byte keys (base64 in trust.json); key `not_after` and `revoked`; channel `expires`; refusal `Error{code,message,status}` |
| `internal/nexupdate/source.go` | `Source` interface, `HTTPSource` (Range + If-Range, no private-URL rewrite), `DirSource` (mounted bundle, tests); `fetchBlob`: `.part` resume by re-hash and continue, restart without Range, final SHA-256 and size, one clean retry for a resumed part |
| `internal/nexupdate/image.go` | `Engine`: state file, trust chain, metadata checks in plan order, guards, reconcile against boot entries, `Status`, `Check` (60 s throttle), `Apply`, `Activate`, `Rollback`, `Cancel`, `request_id` idempotency, last 20 operations, resume of an interrupted apply by repeating its `request_id` |
| `internal/nexupdate/platform.go` | Edition check (os-release `IMAGE_ID=nex-core`), `ImageSettings` (no built-in base URL), `HostPlatform` (running root hash from `/proc/cmdline`, identity from `BUILD_ID`), safe tar extraction of the slot-image blob |
| `internal/nexupdate/stateformat.go` | `RequiredStateFormat`: port of `core_storage_format` / `has_core_v2_state` from `storage-downgrade-guard.py` |
| `internal/hostimage/update.go` | `Host.StageSlot` (prunes stale UKIs, picks the inactive slot by GPT GUID, S2 slot write), `Arm`, `Rollback`, `Entries`; `Bless`, `MarkTried`, `BootOrder`, `ParseUKIName`; `ESPSync` now ignores exhausted entries when it asks whether the running entry is blessed |
| `internal/localcore/updates.go`, `service.go` | Routes `GET /updates`, `POST /updates/{check,apply,activate,rollback,cancel}`; classic apt path unchanged on classic hosts |
| `internal/protocol/protocol.go` | `Inventory.Image`, `ImageStatus`, `ImageRelease`, `ImageOperation`, `ImageError`, `ImageUpdateCapability` |

## Verified (all by `go test`, unit level)

`go test ./internal/hostimage ./internal/nexupdate` and `go test -race ./internal/nexupdate ./internal/localcore ./internal/protocol`
pass. Python: `scripts/generate_api.py --check`, `tests/test_api_contract.py` (74 tests), `scripts/check_docs.py --worktree` and
`tests/test_repository_privacy.py` pass.

| Plan check | Test | Result |
|---|---|---|
| Tampered blob, bad signature, expired `channel.json`, replayed older pointer, `sequence < rollback_floor` each fail with a stable code and the host keeps its record | `TestCheckVerifiesAndRefusesBadMetadata` (14 cases incl. revoked and expired keys, wrong architecture, engine too old, bridge, unsupported component, no source, source down), `TestChannelReplayAndRollbackFloor` (incl. restart), `TestTamperedBlobIsNeverStaged` | pass; mutation check: disabling the replay check and the blob hash check made the matching tests fail |
| Trust rotation, replayed or unsigned trust | `TestTrustRotation`, `TestTrustRefusals` | pass |
| Stage never changes the next boot; activate is the only arming point | `TestApplyStagesWithoutArmingThenActivateCommitsAndRollsBack`, `TestStageArmBlessAndRollback` | pass |
| Resume | `TestDownloadResumesFromPartAndSurvivesRestart` (resume at the `.part` length), `…WithoutRangeSupportRestartsAndCorruptPrefixRecovers`, restart mid-download | pass |
| Crash at each step leaves the old version bootable | `TestCrashAtEveryUpdateStepLeavesABootableVersion` (after prune, torn slot write, half-written `.new` on the first and second ESP, after the first ESP is armed), `TestCrashDuringRollbackLeavesABootableVersion`, `TestFailuresAtEachStepLeaveTheOldVersionAndAreRetryable` (slot write fails, arm fails, restart): each disk's default entry names a slot that holds the matching bytes | pass |
| Failed boot returns to the old version, reported `boot_failed` | `TestFailedBootFallsBackAndIsReported`, `TestEngineOnFileBackedDisks` (three `MarkTried`, real slot writer on two disks) | pass |
| Rollback arms the retained previous UKI; refused after its slot was rewritten | `TestStageArmBlessAndRollback`, `TestEngineOnFileBackedDisks` | pass |
| `state_format` guard at stage, activate and rollback | `TestStateFormatAndMDGuards`, `TestRequiredStateFormat` (15 cases) | pass |
| md superblock gate (phase2-build.md defect 4) | `TestCheckMD`, `TestStateFormatAndMDGuards/md generation` | pass (gate logic only; no kernel pair was run, see below) |
| `request_id` idempotency | `TestRequestIDIdempotency`, `TestImageUpdateRoutes` | pass |
| Cancel and running-VM policy | `TestCancel`, `TestRunningVMsPolicy` | pass |
| HTTP routes, auth, refusal shape | `TestImageUpdateRoutes`, `TestClassicHostRefusesImageOnlyRoutes`, existing classic `TestLocalUpdatesAuthAndApply` | pass |

### Found by the tests

- A refusal for an incompatible manifest (engine, bridge, component) first ratcheted `channel_serial` because the compatibility check ran
  after the metadata was stored; it now runs before.
- A failed apply left the lifecycle phase at `checking`; it now returns to `idle` (or `staged`, or the earlier resting phase).
- `ESPSync` refused to propagate whenever any UKI on the booted ESP carried a counter, including the exhausted (`+0-3`) entry that
  rollback leaves behind; exhausted entries are now ignored for that decision.

## md superblock finding: the gate

Phase 2 recorded that an array created by a newer kernel (7.x stamped `logical_block_size` at offset 224 of the v1.2 superblock)
is refused by 6.12.107, so rolling back across such a kernel generation would leave `nex-data` unassemblable. Gate, in `checkMD`
(runs at stage, activate and rollback): the manifest's `compat.md_generation` (kernel md generation of the image) and
`compat.md_fallback_safe` (the publisher's assertion that the older kernel still assembles the array; CI must test it).
(1) A release that raises the generation without `md_fallback_safe` is refused (`md_generation_unsafe`): boot-count fallback bypasses
userspace, so refusing the raise is the only protection for it. (2) A target of a lower generation than the running one is refused
(`md_generation_downgrade`) unless the running release is `md_fallback_safe`. Both fields are additive to the plan's `compat` list and
need the publisher (`scripts/update-publish.py`, not written) to set them. **Not run:** an actual assembly of an array stamped by one
kernel under another; the finding itself was observed in the previous session.

## Open items / not run

- **Not run:** any VM or hardware boot; `systemd-boot` counting and `systemd-bless-boot` against the filenames the engine writes
  (names follow the BLS convention and sd-boot's version sort by os-release `VERSION` is assumed); power-cuts on a real block device or
  FAT file system (the "crash" tests stop at named points in the code: they model ordering, not a torn sector); Secure Boot; a real
  Range-capable HTTP server (HTTPSource has no test against `httptest`: only DirSource with injected faults ran).
- **Not wired (outside this slice's write set):** `Inventory.image` is defined but nothing fills or sends it; `nex-state-guard.service`
  (boot the recovery profile when committed `state_format` exceeds the image's); the image's `/usr/libexec/nex/nex-boot-check`,
  `boot-complete.target` and the `chrony-wait` finding from phase2-build.md; the pinned `/usr/share/nex/update/trust.json` in the image;
  the publisher scripts that write `trust.json`, `channel.json`, manifests and the slot-image tar; `GET …/operations/{id}`, `PUT …/settings`,
  media/import/components routes; pool rollout gates (activate and rollback evaluate none); Controller scope (`scope_unavailable`);
  layer components (`unsupported_component`: a release with a required layer cannot be installed by this engine yet).
- **Decisions to confirm:** the `slot-image` component is an uncompressed tar of the release directory (the plan names the component
  but not its packaging); the detached signature files are `trust.sig`, `channel.sig`, `manifest.sig` (the plan shows only `manifest.sig`);
  there is no built-in update base URL (`503 source_unconfigured`), the host reads `NEX_UPDATE_BASE_URL` or `/etc/nex/updates.conf`;
  root threshold is 1 signature (plan open question 3); `Stage` removes the retained previous UKI and drops the previous record at the
  moment the slot write begins (the slot is gone from then on), so `rollback` is only available between a commit and the next stage.
