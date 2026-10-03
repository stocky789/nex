# Phase I-3d layers in the update transaction, settings, offline media, on-demand payloads: results

Date: 2026-10-02. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), "Update pipeline, API and pool-safe rollout"
(Release artifacts and trust, Update API), "Offline and on-demand" and "Image composition" (Layer format and lifecycle). Builds on
[phase3-engine.md](phase3-engine.md) (engine) and [phase2-layers.md](phase2-layers.md) (layer manager). Owner decisions applied: `slot-image`
stays a zstd tar, one root signature, rollback only until the next stage, no remote publishing (releases are local trees), private hosts
never appear in tracked files (all examples use reserved example domains).

**Environment: unit tests and one scripted cross-check only.** Go tests with fake sources, directories standing in for the layer store, the
host inbox and the payload directory, and a platform with no disks; Python tests with real `openssl` and `zstd`. No VM, no real block device,
no labelled medium, no `mount`, no `systemd-sysext`, no boot of a new base was involved. Nothing below was observed on hardware or in a VM.

- API impact: **new contract** (`PUT /api/local/v1/updates/settings`, `GET …/updates/media`, `POST …/updates/import`,
  `POST …/updates/components/{id}/fetch`; Controller relay actions `settings`, `media`, `import`, `fetch` on the existing
  `POST /api/v1/hosts/{id}/updates/{action}`; additive `Inventory.image.base_url` and `.media`, `ImageUpdateRequest` fields, manifest
  `name`). Documented in `docs/API.md` (Phase I-3d), `docs/api/local-openapi.json`, `docs/api/openapi.json` and the regenerated HTML.
- State owner: **core**. Records: `/etc/nex/updates.conf`, `/var/lib/nex/image/state.json` (`update.media`, `update.staged.layers`),
  `/var/lib/nex/layers/{store,enabled.json,releases/<BUILD_ID>/}`, `/var/lib/nex/payload/<sha256>/`. A fresh Controller recovers what it shows
  from `Inventory.image`; it stores nothing (replacement test: nothing is lost because the Controller holds nothing).
- Guide impact: none yet. No operator workflow can be walked through without a booted image and a lab; the software-updates and
  publish-a-release pages are rewritten after a walkthrough (a page claims verification only after one).

## What exists

| Where | What |
|---|---|
| `internal/nexupdate/trust.go` | `Component.Name`; per-kind checks in `VerifyManifest`: a layer needs a `nex-<name>` id, size, URL and `requires_base` equal to the release's BUILD_ID `<label>-<sequence>` (`layer_base_mismatch`); an on_demand payload needs a plain file name |
| `internal/nexupdate/layers.go` | `stageLayers` (download and hash every layer that is not on_demand, plus enabled on_demand ones, then place them), `switchLayers` (arm and rollback), `pruneLayers` (commit, next stage, cancel), `LayerRef` in the release record |
| `internal/vmhost/layers.go` | `EnabledNames`, `StageRelease` (store files by digest, signed manifest bytes under `releases/<BUILD_ID>/`, never touches `enabled.json`, refuses a layer for another base before writing), `SwitchRelease` (rewrites only enabled layers), `PruneReleases` (keeps what bootable bases need; refuses to remove anything when a retained manifest is unreadable) |
| `internal/nexupdate/offline.go` | `ValidateSettings`, `SaveImageSettings`, `Engine.UpdateSettings`; `Engine.Media` (scan, remembered in the record), media ids `inbox`, `inbox/<name>`, `label`; `Engine.Import`; `Engine.FetchComponent`; read-only mount of labelled media for the duration of an operation |
| `internal/nexupdate/image.go` | `fetchMetadataFrom` (one verification path for the server and a bundle), `startJob` (apply, import and fetch share the operation, lock, idempotency and resume rules), `clock_behind`, layer staging before the slot write, switch at arm, `commit(ctx)` prune |
| `internal/nexupdate/cmd/nex-update` | `settings`, `media`, `import`, `fetch` subcommands (the host-shell path for the lab) |
| `internal/localcore/updates.go`, `service.go` | the four routes |
| `internal/protocol`, `internal/agent`, `internal/controller` | relay actions, `ImageMedia`, `Inventory.image.base_url` and `.media`; the agent relays through a separate `OfflineUpdater` interface |
| `scripts/update-publish.py`, `scripts/update-bundle.py` | `--layer`, `--on-demand-layer`, `--payload KIND:ID=FILE`; a minimal bundle carries the slot image and the non-on_demand layers |

## Verified

All runs under `systemd-run --user --scope` with `CPUQuota=200%`, `MemoryMax=4G`, no swap, `GOMAXPROCS=2`, `nice -n 19`, `ionice -c3`,
`go test -p 1`, named packages only, tiny test data (kilobyte files; no sparse image was read).

| Check | Test | Result |
|---|---|---|
| Layers stage with the base; stage leaves `enabled.json` alone; activate switches it; the previous base's layers stay until the next stage, then go | `TestLayersStageWithTheBaseAndSwitchOnlyAtActivate` | pass |
| Rollback switches the layer record back | `TestRollbackSwitchesTheLayersBack` | pass |
| A layer built for another base is refused (manifest level `layer_base_mismatch`, store level `base_mismatch`) and nothing is staged | `TestLayerBuiltForAnotherBaseIsRefused`, `TestLayerStageReleaseRefusesWithoutWritingAnything`, `TestImportRefusals/layer_for_another_base` | pass |
| Tampered layer bytes never reach the store or the slot | `TestLayerTamperNeverReachesTheStoreOrTheSlot`, `TestLayerStageReleaseRefusesWithoutWritingAnything` | pass |
| Interrupted layer download: slot not written, store empty, retry resumes at the part length | `TestInterruptedLayerDownloadResumesAndNothingIsHalfStaged` | pass |
| on_demand layer staged only where enabled | `TestOnDemandLayerIsStagedOnlyWhenEnabled` | pass |
| Store: stage idempotent, switch only enabled layers, prune keeps what bootable bases need, unreadable manifest stops the prune | `TestLayerStageRelease…`, `TestLayerSwitchRelease…`, `TestLayerPrune…` (internal/vmhost) | pass |
| Settings validation (https and explicit http, credentials, query, fragment, scheme, host, channel), persistence preserving other lines, restart, cleared URL gives `source_unconfigured`, new pointer history, env override, refused while running | `TestValidateSettings`, `TestUpdateSettingsPersistsAndStartsANewPointerHistory`, `TestUpdateSettingsRefusals` | pass |
| Media listing is read-only and recorded for the inventory | `TestMediaListsBundlesWithoutChangingThem`, `TestSettingsMediaImportAndFetchRoutes` | pass |
| Import stages through the apply pipeline with the server down, never arms, replay and conflict | `TestImportStagesThroughTheSamePipeline` | pass |
| Import refusals: unknown media, expired bundle pointer, clock earlier than `created`, tampered blob, tampered manifest, unsigned pointer, replayed older bundle, below the rollback floor, layer for another base; a refused bundle leaves the host as it was | `TestImportRefusals` (9 cases) | pass |
| Interrupted import resumes from the same `request_id`; another request is refused until then | `TestInterruptedImportResumesFromTheSameRequest` | pass |
| Review fixes: a FIFO or a link leaving the bundle is never read (no block, no update lock held) | `TestMediaThatIsNotRegularFilesIsRefusedWithoutBlocking` | pass; failed before the fix (blocked) |
| Review fixes: each source and each bundle channel has its own pointer history; a source switch interrupted before the record was reset is not judged against the old history | `TestEachSourceKeepsItsOwnPointerHistory`, `TestBundleChannelsKeepTheirOwnPointerHistory` | pass; failed before the fix (`channel_replayed`) |
| Review fixes: a settings change waits for a check in flight | `TestSettingsWaitForACheckInFlight` | pass; failed before the fix (settings written mid-check) |
| Channel choice in a multi-channel bundle | `TestImportNeedsAChannelWhenTheBundleHasSeveral` | pass |
| Fetch places the payload at `<payload>/<sha256>/<name>`, replays, does not download twice, leaves a staged release alone; refuses layers, the slot image, unknown components and tampered bytes; reads from a bundle | `TestFetch…` (3 tests) | pass |
| HTTP: session and CSRF on all four routes, unknown JSON field, replay and conflict, classic host `409 unsupported_edition` | `TestSettingsMediaImportAndFetchRoutes`, `TestClassicHostRefusesOfflineRoutes` | pass |
| Controller relay reaches only the addressed host with every field; malformed requests refused before queueing; classic host refused | `TestImageSettingsMediaImportAndFetchAreRelayedToTheHostOnly` | pass |
| Agent runs the four actions on the engine, carries the fields, passes the engine's refusal code, never forwards a malformed request; inventory maps `base_url` and `media` | `TestRelayedSettingsMediaImportAndFetchRunOnTheHostsEngine`, `TestInventoryImageReportsTheSourceAndMedia` | pass |
| Publisher: layers and payloads become signed components, a layer for another base, with the wrong bytes, a duplicate id, a bad payload spec are never signed; minimal and full bundle contents | `tests/test_update_publish.py` (3 new tests) | pass |

Mutation checks (each fix removed, the named test ran and failed, the fix was restored): the `requires_base` check
(`TestLayerBuiltForAnotherBaseIsRefused`, `TestImportRefusals`), switching at arm (`TestLayersStage…`), the `created` against clock check
(`TestImportRefusals`), pruning (`TestLayersStage…`), the pointer-history reset on a settings change
(`TestUpdateSettingsPersistsAndStartsANewPointerHistory`), the fetch kind guard (`TestFetchRefusals`), the on_demand-layer rule
(`TestOnDemandLayerIsStagedOnlyWhenEnabled`).

Cross-check with the real publisher (throwaway test, removed afterwards): `update-publish.py publish --layer … --on-demand-layer …
--payload vm-image:controller=…` and `update-bundle.py --profile full` wrote a tree and a bundle; the Go engine's `Check` listed all six
components (slot image, source, compliance, two layers, payload), `Media` found the bundle, and `FetchComponent` from the bundle placed
`controller.qcow2` under its digest with the exact bytes. This proves the two sides agree on the manifest form (fake release directory:
no verity was checked).

Package results: `go test -p 1 -count=1` for `./internal/nexupdate` (also `-race`), `./internal/vmhost`, `./internal/protocol`,
`./internal/agent`, `./internal/controller`, `./internal/localcore`, `./cmd/nex-core-local` pass; `python3 -m unittest tests.test_update_publish`
passes. `scripts/generate_api.py --check` is current. `tests/test_api_contract.py` and the final documentation and privacy checks are listed
in the hand-off of this slice.

## Behaviour changes worth knowing

- `clock_behind` applies to every source, online as well as offline: a manifest whose `created` is later than the host clock is refused. A
  host with a clock that runs behind its publisher's can no longer check; hosts run chrony, and the refusal says to set the clock.
- `channel_serial` counts one source's channel pointers, so the record keeps one history per update server and channel (a bundle's
  channels each keep their own). A settings change needs no reset and an earlier source keeps its history; the rollback floor (releases,
  not sources) spans them all. Hosts that already hold a single `channel_serial` from an earlier development build start the new
  histories empty.
- `ReleaseRecord` gained `Layers`, so records are no longer comparable with `==`.
- The update operation kinds are now `apply`, `import`, `fetch`, `activate`, `rollback`, `cancel`.

## Not run / open

- **Not run:** the real mount of labelled media (`hostMount`: `mount -o ro,nosuid,nodev,noexec`) and its release; the real
  `/dev/disk/by-label/NEXUPDATE` and `/proc/self/mounts` discovery beyond a synthetic mounts file; any VM or hardware boot of a base that
  was staged with layers; `nex-layers.service` verifying the staged `releases/<BUILD_ID>/manifest.json` at that boot (as written by this
  slice it verified with a raw `release.pub` key; a later change by another slice moved it to the engine's trust chain, so the two
  no longer need the same pinned key; that change was not re-verified here); a layer merge after the update; Controller UI and guide pages
  for settings, media, import and fetch.
- **Not decided here:** a layer that is `on_demand`, not enabled, can only be obtained by enabling it from a release that carries it as
  required; there is no fetch of a layer for the running base. `SHA256SUMS` of a bundle is listed (`has_sha256sums`) but not used as
  authority; signatures and blob hashes are.
- Pre-existing and not touched: `tests/test_api_contract.py` also fails for `PoolUpdateHost` (`attempt`) while the pool rollout slice
  adds that field to `internal/protocol` without its schema; that belongs to that slice.
