# Phase I-3c Controller relay for a single host: results

Date: 2026-10-02. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), "Update pipeline, API and pool-safe
rollout" (Update API, Compatibility, work items 2 and 4). Slice I-3c: the host-local routes, a Controller relay for **one host**
(not pool rollout), the `image-update-v1` acknowledgement and a web panel. Pool rollout, `pool-update`, layers, the Controller
appliance swap, `PUT …/updates/settings`, media/import/component routes, `ReportResponse.controller` and `controller-epoch-v1` are
not in this slice; they were added afterwards (see phase3-layers-update.md, phase4-rollout.md, phase4-appliance.md).

**Environment: unit and HTTP-handler tests only.** No VM, no real host, no pool, no browser session against a running Controller.
Nothing below was observed on a running system. Resource limits: Go builds and tests ran under `systemd-run --user --scope
-p CPUQuota=400% -p MemoryMax=8G -p MemorySwapMax=0` with `nice -n 19 ionice -c3`, `GOMAXPROCS=4`, `go test -p 2`; the web tests and
`tsc` ran under the same wrapper; no VM and no subagent was started. Peak use was not sampled.

- API impact: **new contract** (see the change record `docs/changes/2026-10-02-image-update-controller.json`).
- State owner: **core**. The Controller stores no update data; it keeps the operations row it keeps for every host command.

## What exists

| Where | What |
|---|---|
| `internal/protocol/imageupdate.go`, `protocol.go` | command kind `host-image-update`, `ImageUpdateRequest` (+ `Valid`), `Command.image_update`, `ReportResponse.image_update` |
| `internal/agent/imageupdate.go`, `run.go`, `state.go`, `client.go`, `managed.go` | `ImageUpdater` hook (installed by `nex-core-local` when its engine exists); capability `image-update-v1` offered only while an engine is installed (v2 reports only); `Inventory.image` stripped from the report until the Controller acknowledges; readiness cleared on report failure, restart and Controller change; `ExecuteImageUpdate` runs the command on the engine and returns `succeeded`, or `failed` with `<code>: <message>` |
| `internal/localcore/service.go`, `updates.go` | the embedded agent shares the host's single engine; new `GET /api/local/v1/updates/operations/{id}` (reads `Engine.Operation`) |
| `internal/nexupdate/image.go` | `Engine.Operation(id)` accessor |
| `internal/controller/imageupdate.go`, `controller.go`, `identity.go`, `monitor.go`, `managed.go` | `GET /api/v1/hosts/{id}/updates` (host-reported projection), `POST /api/v1/hosts/{id}/updates/{action}` (relay, `request_id` replay, refusals the Controller can see), the `image_update` acknowledgement (sent only to a Core that offered the capability), inventory merge shows the host's latest `image` on every heartbeat |
| `web/src/image-updates.tsx`, `settings.tsx` | **Host images** list on Settings → Updates: running, staged, previous and available releases with slots, channel, rollback floor, state format, last operation and failure; Check, Download and stage, Reboot into update, Roll back, Cancel with action hints, confirmation for the two reboots and a graceful-guest-shutdown choice |

Route check: `GET/POST /api/local/v1/updates*` are registered by `nex-core-local`'s `Service` (`internal/localcore/service.go`), which
builds the engine at `Open` from the static os-release edition (`IMAGE_ID=nex-core`).

Decisions: the Controller's own APT routes (`GET /api/v1/updates`, `POST /api/v1/updates/apply`) and the Controller update panel are
**kept**. The plan retires them with the appliance swap (work item 5, not in this slice); until then the Controller has no other
way to update itself, and nothing in the new routes conflicts with them. `PUT …/updates/settings` was not cheap (URL validation and a
persistent write of `/etc/nex/updates.conf`) and was left out of this slice; it was added afterwards (phase3-layers-update.md).

## Verified (all automated)

| Check | Test | Result |
|---|---|---|
| Image host serves check → apply (stage only, nothing armed) → activate through the host-local HTTP routes with a signed fake source; operation lookup; auth; 404 for an unknown operation | `TestImageEditionHostServesTheWholeUpdateFlow` (`internal/localcore`) | pass |
| The embedded agent's relayed command reaches the same engine (activate armed sequence 2, reboot called, cancel after arming is refused with `already_armed: …`) | same test, via `agent.ExecuteImageUpdate` | pass |
| Relay maps each action and the request_id to the engine; invalid or unclassified failures never leak engine internals | `TestRelayedImageUpdateRunsOnTheHostsEngine` (`internal/agent`) | pass |
| `Inventory.image` and the capability are withheld until acknowledged; acknowledgement cleared on reset; capability absent without an engine | `TestInventoryImageRequiresControllerAcknowledgement` (`internal/agent`); mutation check: disabling the withholding makes it fail (`report 0 carried image = true`), then reverted | pass |
| The Controller acknowledges only a Core that offered the capability; projection equals the host report | `TestImageUpdateAckAndHostReportedProjection` (`internal/controller`) | pass |
| Command goes to its host only, with the right fields; replay is `200` with the original operation; a changed payload is `409`; refusals, strict body, CSRF | `TestImageUpdateRelaysToItsHostOnly`, `TestImageUpdateRefusals`, `TestImageUpdateNeedsCSRF` | pass |
| Action hints, request identity held across a retry, panel rendering from the host report; a new host report replaces a view the panel fetched earlier (the view is used only while the report it was read against is unchanged); a host that is no longer freshly connected gets every action disabled; the panel keeps reading while a submitted task is unfinished, not only while the host reports a running operation (mutation checks: ignoring the report identity and ignoring the task each fail the test, then reverted) | `web/src/image-updates.test.mjs` (also `settings`, `updates`, `ui`, `pool-members` web tests, `tsc --noEmit`) | pass |
| Contract | `scripts/generate_api.py --check`, `tests/test_api_contract.py` (74 tests) | pass |
| Packages | `go test ./internal/protocol ./internal/nexupdate/... ./internal/localcore ./internal/agent ./internal/controller`; `go test -race` of the update tests in nexupdate, localcore, agent and controller | pass |
| Docs | `scripts/check_docs.py --worktree`, `tests/test_repository_privacy.py` | pass |

`go vet ./internal/agent` reports an existing copied-lock warning in `backup_authority_transfer.go`; not touched here.

## Not run / limits

- **Not run:** any browser session against a running Controller (the panel was rendered with `react-dom/server` only), any VM, any real
  image host driven through the Controller, an interrupted-request or Controller-replacement drill against a real host, a pool.
- The `web/node_modules` of this worktree was absent; the web tests used a temporary symlink to another checkout's installed modules,
  removed afterwards.
- The panel's timers and effects (task polling, the final read after a task ends) were not run in a browser; only the pure decisions they use (`imageSnapshot`, `stillFollowing`) and the server-rendered markup are tested.
- Compatibility with a strict older Controller was argued from the withholding code and tested at the agent boundary (the field is
  absent from the report until acknowledged), not against an old Controller binary.
- The guide pages (`software-updates.md`, `roll-back-a-host-update.md`) describe steps that have **not** been walked through and say so.
