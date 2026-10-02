# Phase I-4a pool rollout coordinator: results

Date: 2026-10-02. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), "Pool rollout coordinator",
"Compatibility guards", "Update API" (pool, peer and Controller rows). Slice I-4a: the coordinator in Core, with the Controller relay
and the host-local routes. The Controller appliance swap, `scope:"controller"`, the UI and the guide page are **not** in this slice.

**Environment: unit and HTTP-handler tests with fake peers only.** No VM, no container and no real pool was used (the owner's resource
cap rules out a three-host lab). The fake host stands in for a host's image engine and its guests; time is a fake clock. Nothing below
was observed on a running system, and nothing here shows that real hosts reboot, return, re-arm a watchdog or restart a guest.
Resource limits: Go builds and tests ran under `systemd-run --user --scope -p CPUQuota=400% -p MemoryMax=8G -p MemorySwapMax=0` with
`nice -n 19 ionice -c3`, `GOMAXPROCS=4`, `go test -p 2`; no subagent was started. Peak use was not sampled.

- API impact: **new contract** (see the change record `docs/changes/2026-10-02-pool-rollout-coordinator.json` and
  [API.md](../../API.md#image-edition-pool-rollout-coordinator-phase-i-4a)).
- State owner: **core**: `pool-update.json` on the master, replicated as authority section `update_rollout`; the Controller keeps nothing.
- Guide impact: **none yet**, deliberately: no operator workflow has been walked through, so `update-a-pool.md` is **open** and is
  due with the Controller UI and a lab walkthrough.

## Progress log

- 00:45-00:57 (about 12 min): read the plan and code; wrote the protocol types, the `PoolCommand` actions, the optional replica
  section, the coordinator and the host side; package built.
- 00:57-01:13: wiring (managed command, peer routes, capability, inventory, replica build, promotion adoption, coordinator tick),
  fake-peer tests (first run: 7 failures, all test-fixture or design findings listed below), host-step tests, localcore routes and test,
  Controller relay and test, API contract and `docs/API.md`, change record, CHANGELOG. Total about 30 min of the 40 min box.

## What exists

| Where | What |
|---|---|
| `internal/protocol/poolupdate.go`, `pool.go`, `protocol.go` | `PoolUpdateRollout`/`Host`/`Pause`, `PoolUpdateRequest`, `PoolUpdateStep`/`StepResult`, `PoolUpdateHostStatus`/`Target`, capability `pool-update-rollout-v1`; `PoolCommand` actions `update-rollout` and `update-control` with `update`; replica field `update_rollout` and the section of the same name, declared only while a record exists; `ManagedState.update_rollout` |
| `internal/agent/pool_update.go` | `pool-update.json` store; `Start` (preflight, dry run, idempotent on the request ID), `Control` (pause, resume, abort, rollback_host), `Advance` (the step machine, one unit of work per pass, persisted before acting), the quorum gate, `AdoptPoolUpdateOnPromotion`, the live peer transport, `ExecutePoolUpdate` (the relayed command), `RunPoolUpdateCoordinator` (started by the agent's `Run`) |
| `internal/agent/pool_update_host.go` | `PoolUpdateHostStatusFor`, `PoolUpdateHostStep` (check, stage, drain, activate, confirm, undrain, rollback on the host's engine and guests), `PoolUpdateHandler` (the three peer routes), the verified blob cache, `PoolUpdatePeerSource` |
| `internal/nexupdate/source.go` | `PeerFirstSource`: a member reads blobs from the master's cache, then its channel |
| `internal/localcore/pool_updates.go`, `updates.go`, `service.go` | the four master routes; the live engine wraps its source with the peer cache |
| `internal/controller/poolupdate.go`, `identity.go` | `GET /api/v1/pools/{id}/updates`, `POST …/check`, `…/rollouts`, `…/rollouts/{rollout_id}/control`; the retained inventory keeps `update_rollout` |

## Verified (all by `go test`, fake peers)

`go test ./internal/protocol ./internal/nexupdate/... ./internal/localcore ./internal/controller ./internal/agent` pass; the new tests
also pass under `-race`. `scripts/generate_api.py --check`, `tests/test_api_contract.py` (74 tests), `scripts/check_docs.py --worktree`
(233 uncommitted paths; its own note says semantic review is still required) and the repository privacy test pass.

| Plan check | Test | Result |
|---|---|---|
| Members by running-guest count then host ID, master last; one host in maintenance and one host down at a time (the fake fails the test the moment two hosts are in maintenance, or the voters left would not be a majority, at every step and every pass); guests started exactly once, on the host that ran them; no host left in maintenance; `finalized_sequence` set | `TestRolloutUpdatesOneHostAtATimeMasterLast` | pass |
| Crash and restart of the coordinator: a fresh coordinator is built for every pass; and a reply lost after `activate` (a fresh coordinator repeats the delivered step with the same step ID) | `TestRolloutResumesAfterEveryCoordinatorRestart`, `TestRolloutStepsAreIdempotentAcrossACrashAfterActivate` | pass |
| Preflight: evacuate, missing capability, channel mismatch, `bridge_required`, `state_format_downgrade`, free space, release not offered, `mixed_pool:false` without shutdown, Controller epoch unknown, HA on, a host already in maintenance; a refusal records nothing; dry run records nothing | `TestPreflightRefusals` (11 cases), `TestDryRunChecksEverythingAndRecordsNothing` | pass |
| Request ID idempotent; a second rollout refused while one is active; a changed body under the same ID refused | `TestStartIsIdempotentAndExclusive` | pass |
| `refuse` waits `step_timeout` then pauses naming the guest; the blocked host and the master are not touched; resume completes | `TestRefusePolicyWaitsThenPausesNamingTheGuest` | pass |
| A host that does not return: `needs_attention` at `boot_deadline`, nothing restarted elsewhere, maintenance kept, the stopped guest remembered | `TestHostThatDoesNotReturnPausesNeedsAttention` | pass |
| A host that fell back to the old release pauses and blocks finalize | `TestRolledBackHostPausesAndBlocksFinalize` | pass |
| Quorum-margin gate reads the master's live Corosync, not membership rows: votes present against the majority needed, a voting witness only when it really votes, every other nodelist host in the quorate partition (a ready host that is partitioned or down is refused although its row says ready), clustered storage needs three voters, an unreadable quorum on a clustered pool is refused; another host not ready refuses before maintenance | `TestQuorumGateReadsLiveVotes`, `TestQuorumGateVoteArithmetic`, `TestGateRefusesWhenAnotherHostIsNotReady` | pass |
| Engine request IDs: every step ID is `ru-` and 40 hex digits of a SHA-256 over (rollout, host, stage, attempt), so it is valid for any rollout and host ID, bounded, identical on a repeat and different per attempt; the fake engine and fake peers refuse an ID the real engine would (`nexupdate.ValidRequestID`) | `TestStepIDsAreValidBoundedAndDifferPerAttempt` and every rollout test | pass |
| The master reads a member's status and step answers over the real pinned-mTLS peer transport and decodes them before returning (no zero value from a copy taken before the decode) | `TestLivePeersReadTheMembersAnswerOverTheRealTransport` | pass |
| The gates run again, under the coordinator lock, immediately before maintenance is entered and before an unarmed host is activated, and on resume for the host that is next | `TestGatesAreRunAgainBeforeMaintenanceAndBeforeActivate`, `TestResumeRunsTheGatesAgain` | pass |
| The rollout record naming the guests a host's update will stop is acknowledged by every ready member that could take the pool over before the first guest is asked to shut down; with a member unacknowledged nothing is stopped | `TestGuestsAreNotStoppedBeforeTheRecordIsAcknowledged` | pass |
| A promotion writes the inherited rollout (paused `master_changed`) before the write that makes the host master, and removes it again if the promotion fails | `TestPromotionRecordsTheInheritedRolloutBeforeItPublishesTheMaster` | pass |
| Resume of a rolled-back host stages it again under a new attempt (new step IDs), so the engine's remembered operations of the failed attempt are not replayed | `TestResumeAfterRollbackStagesAgainUnderANewAttempt` | pass |
| Controls: pause lands after the host being updated, resume, `rollback_host` (paused rollout, activated host only), abort puts maintenance back and keeps committed hosts, a new rollout is possible after an abort | `TestControlsPauseResumeAbortRollback` | pass |
| Master change: a promoted master adopts an active rollout paused `master_changed` without rewriting host progress; a finished rollout is not resurrected; a former master advances nothing | `TestPromotedMasterFindsRolloutPausedMasterChanged` | pass |
| Membership generation change pauses | `TestMembershipChangePausesTheRollout` | pass |
| `update_rollout` is a replicated section only while a record exists; a tampered section fails verification; the content digest covers it | `TestRolloutIsAReplicatedAuthoritySection` | pass |
| Controller absence: with no Controller configured the rollout completes and the inventory projection is the master's own record | `TestRolloutNeedsNoController` | pass |
| Host steps: `check` re-checks only when the release is not offered; `stage` never arms (the fake engine panics on any `Activate:` other than `stage_only`) and reports the engine's failure code; `activate` keeps the engine's `refuse` policy and does not re-arm an armed host; `confirm` needs committed on the target and reports a fallback as `rolled_back`; `drain`/`undrain` touch only the listed guests, repeat the same operation ID, and retry an admission refusal instead of failing | `TestHostStepsMapOntoTheEngine`, `TestHostGuestStepsTouchOnlyTheListedGuests`, `TestHostStatusReportsTheEngineAndGuests` | pass |
| Peer blob cache serves only a staged file whose content hashes to its name | `TestPeerBlobCacheServesOnlyVerifiedContent` | pass |
| Host-local routes: session and CSRF, validation, stable codes, `404`, strict body | `TestPoolRolloutRoutes` | pass |
| Controller relay: command goes to the master only with no composed membership; replay `200`, changed body `409`; dry run and controls carry the right fields; refusals (`400`, `404`, `409` "upgrade Nex Core" when a member lacks the capability, stale master); a fresh Controller reads the master's record | `TestPoolUpdateRelaysToTheMasterOnly`, `TestPoolUpdateRefusals`, `TestPoolUpdateProjectsTheMastersRecord` | pass |

Mutation checks (made, observed to fail, reverted): disabling the margin and clustered-storage conditions in the quorum gate failed
`TestQuorumGateReadsLiveVotes`; removing the maintenance release after confirm failed `TestRolloutUpdatesOneHostAtATimeMasterLast`.
Review-fix mutation checks (each made, observed to fail the named test, reverted): a slash-joined step ID (every rollout test), a step ID without the attempt (`TestStepIDsAreValidBoundedAndDifferPerAttempt`, `TestResumeAfterRollbackStagesAgainUnderANewAttempt`), a reply copied before it is decoded (`TestLivePeersReadTheMembersAnswerOverTheRealTransport`; the gc compiler happens to evaluate the call first, so the plain `return out, f(&out)` form passed only by evaluation order), the nodelist-partition condition (`TestQuorumGateVoteArithmetic`, new case with votes to spare) and the votes-remaining condition, the gate removed before maintenance, before activate and on resume, the pre-shutdown replication, the promotion writing the rollout after the master record, and the attempt increment on resume.

### Found by the tests

- The first fake-peer run showed that a host with a running guest is ordered *after* idle hosts (by design); the tests had assumed
  another order. No code change.
- With two hosts and no witness the pool forms no Corosync cluster, so the gate has no votes to lose and allows the update (the fake's
  own quorum check had assumed otherwise). The gate refuses on votes only where a cluster (or clustered storage) exists.
- A Controller control test that looped on redelivered commands hung for five minutes; commands are redelivered until a result is
  reported. Test only.
- The Controller's retained inventory is a hand-copied subset of `ManagedState`; `update_rollout` had to be added there or a fresh
  Controller would not have read it back.

## Decisions and deviations from the plan text

- **Replica section.** `update_rollout` is appended to the declared section list only while a record exists, so the 7-section layout
  older members verify is unchanged for a pool that never rolled out (the same compatibility rule `backup_authority` follows). The
  rollout is refused before it starts unless every member advertises the capability.
- **`mixed_pool:false`** is a refusal (`mixed_pool_requires_shutdown`) unless `running_vms` is `shutdown`, not a silent override; the
  pool-wide quiesce the plan's "forces" wording could mean is **not** implemented: hosts still update one at a time.
- **`check`** is an extra peer step action (the plan lists six). It is how preflight makes each host verify the release.
- **Controller `api_epoch`** is always unknown (no `ReportResponse.controller`), so `controller_unknown` is the answer for any release
  that sets `min_controller_api_epoch`; HA has no setting, so its check is a seam that is always false.
- Transient gate refusals (`quorum_margin`, `host_busy`, `ha_enabled`) pause the rollout for the operator instead of waiting silently.
- **Review fix (second review).** The host status now fails with `controller_scan_unavailable` when its Controller appliance record cannot
  be read, instead of reporting no Controller, so the `controller_first` scan retries and pauses `step_timeout` rather than skipping the step
  (`TestHostStatusFailsWhenTheControllerRecordCannotBeRead`, failed before the fix). The rollout panel refuses concurrent submissions and
  shows an unreadable master as unreachable (`pool-rollout.test.mjs`).

## Not run / not implemented (open)

- **Any VM, real image engine, real libvirt guest, real reboot, real boot-count fallback, real Corosync or watchdog.** The default
  guest operations (`localPoolUpdateGuests`: `virsh list`, admission, shutdown, start through `AdmitPoolVMExecution`) were never run.
- **The HTTPS peer transport** (`livePoolUpdatePeers`, `PoolUpdateHandler` behind pinned mTLS, `PoolUpdatePeerSource`) was compiled and
  route-checked by the contract test but never exercised over a connection. Replica push and member verification of the new section on
  a real member were checked only by building and verifying the replica value; the promotion call site in
  `adoptPoolAuthorityFromReplica` ran in the existing promotion tests but not with a rollout record present.
- **Confirm** checks only that the host committed the target release and is a ready member. The plan's "storage/network reconcile checks
  healthy, watchdog re-armed where the pool wants it" are **not implemented**. The watchdog teardown wait only reads membership.
- **Host-local `activate`/`rollback` on a pooled host do not yet evaluate the rollout gates** (plan: "from the verified replica"); an
  operator can still arm a pooled host by hand through the I-3 routes.
- `controller_first` and `scope:"controller"` (the appliance swap), air-gapped metadata distribution (the peer cache serves blobs only),
  the Controller UI, the guide page `update-a-pool.md`, and the Phase 8 roadmap row are later work.
- The three-host nested lab of the plan's acceptance check 3 and 4 did not run **in this slice**; it ran afterwards, with its own limits and
  one defect found: see [phase4-lab.md](phase4-lab.md). Everything above is still fake-peer evidence.
