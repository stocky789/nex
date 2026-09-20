# Phase 4 closure: multi-host pools, authority transfer and acceptance

Date: 2026-09-19. Status: **Phase 4 acceptance closed 2026-09-20 for disposable virtual-lab
scope.** S1–S6 are implemented in the working tree; S7 recorded reviewed native and browser
evidence within that scope. Owner: project owner.
Parent: [platform plan](2026-09-08-nex-platform.md), section
[4. Multi-host pools and shared storage](2026-09-08-nex-platform.md#4-multi-host-pools-and-shared-storage)
and [Pool authority transfer and master succession](2026-09-08-nex-platform.md#pool-authority-transfer-and-master-succession).
Related: [pool master model](2026-09-09-pool-master-model.md),
[pool VLAN networks](2026-09-09-pool-vlan-networks.md),
[master-owned VM placement](2026-09-18-master-vm-placement.md),
[production-readiness handoff](2026-09-14-production-readiness-handoff.md).

## Purpose

Close Phase 4. Every slice below is Core-first: the pool master's Nex Core owns the durable
record and performs the operation; Nex Controller authenticates the operator, relays one
request and displays what Core reports. No slice adds a Controller table, cache or file that
holds the only copy of anything. Apply the
[architecture invariant](../../AGENTS.md#the-architecture-invariant-nex-core-does-the-work-nex-controller-only-asks)
to every review of this work.

**Status of the whole plan (2026-09-20).** S1–S6 are **implemented in the working tree and
unstaged**. S7 ran a disposable-lab campaign whose reviewed results are in the
[native acceptance report](../qa/2026-09-19-phase4-native-acceptance.md); historical raw results
remain under `.cache/phase4-acceptance/s7/`. The platform plan's Phase 4 gate and its initial
manual-recovery gate are **closed for disposable virtual-lab scope** with the permanent limits
named below. Owner decisions D1–D4 and OD-5–OD-6 were taken as their recommended defaults; all six
are recorded under [Decisions for the project owner](#decisions-for-the-project-owner). Each slice
below carries its own dated status block naming the deviations taken while implementing it.

**Acceptance snapshot (2026-09-20, final reconciliation).** S1–S6 remain implemented in the
unstaged development tree. Final source candidate `f714f71f` includes BUG-0372–0381; the full
Go race suite and Python suite (374 tests) passed. Reviewed native evidence includes the
three-host promotion, S5 interruption recovery, S6-12 pending-update recovery, all seven
BUG-0377 health checks, and the `2f50d2ae` management-network C4/C7–C12 baseline. G1 has
repeated UI eject/join cycles plus missing-uplink failure, autonomous recovery and cleanup;
G2–G4 have the scoped browser results recorded in the QA report. Final `f714f71f` S6-14 proves
boot-window API/hook refusals, timed autostart suppression, rejoin clearance and normal
post-clear VM start/stop. These results supersede the dated implementation-only or
not-yet-run status blocks below, without changing their historical provenance.

The older bounded S6-18 CHECKPLAN result passed within its recorded scope. The latest
`f714f71f` S6-18 strict harness is **partial/FAIL**: boot/API/hook refusals passed, but eject
safely refused for a different reservation reason and the harness stopped before timed
suppression. Its cleanup restored the lab; the separate S6-14 pass does not turn it into a
full S6-18 pass. Phase 4 and the initial manual-recovery gate are **closed for disposable
virtual-lab scope** (project owner, 2026-09-20). OD-6 is **decided**: the strict
unreachable-ready-member rule is the approved default — an unreachable ready member blocks
promotion and returned-master clearance unless the same operation fences it (fail-closed; silence
is never agreement). The authoritative public evidence and limits are maintained in the
[native acceptance report](../qa/2026-09-19-phase4-native-acceptance.md).

**Permanent limits (never claimed for this lab class).** Preserve OD-5's no-escape limit.
S6-12's contradictory-owner subcase and S6-16 lack authentic supported owner-transfer fixtures;
this does not authorize building migration or fabricating durable ownership conflicts.
Physical/BMC fencing, five-host scale, package-installed hook/AppArmor enforcement, broader
duplicate-writer and workload-continuity evidence, and remaining C-class matrix rows stay **open
forever** for disposable virtual-lab acceptance — they are not deferred work inside this scope.
Guide limits include hardware coverage, a current G2 maintenance reboot/autostart walkthrough,
planned-transfer terminal visual completion, and applicable promotion/transfer failure and recovery
paths. The G3 UI rollback was operator-requested; a separate GUI deadline-expiry walkthrough was
not run. Retain the original build and method boundaries when using historical evidence. Missing
raw missed-eject recovery provenance remains a limitation, not a newly executed result. Post-review
Core `327b7ec9…` was dual-deployed; ordered S2 native maintenance revalidation passed (prep, core
24/24, autostart 17/17, restarts 5/5, hook 9/9, hosts 9/9) — see the QA close note.

**About the line references in this document.** It was written before the slices were built, so
`file.go:NNN` citations describe the tree at planning time and several have drifted as the code
grew. They are kept as the record of what was read when the plan was written; where a slice's own
dated status block, `docs/API.md` or the code itself disagrees with a line citation, those are
authoritative and the citation is not. Symbol names, file names and behaviour statements were
re-checked against the tree during the closure documentation sweep; bare line numbers were not.

## Already implemented (do not re-plan)

Core-local member join (CSR/offer/join over the host-local API), member pull of pool-shared
NFS/SMB and eligible clustered iSCSI, guest VLAN networks and shared-ISO metadata with
per-member access results, rename/removal convergence with tombstones, and master-owned VM
placement admission. See `.cache/phase4-acceptance/code-map.md` for the verified file:line map
these slices build on.

## Scope

| Slice | Title | Size |
|---|---|---|
| S1 | “Add host to pool” from the Controller, plus membership and convergence reporting | L |
| S2 | Maintenance mode | M |
| S3 | Staged remote management-network edits with timed host-side rollback | L |
| S4 | Member authority replica | M–L |
| S5 | Planned master change and member eject | L |
| S6 | Fence-verified manual promotion and safe rejoin | L |
| S7 | Native acceptance in a disposable lab, guide walkthroughs, QA and status | L |

Out of scope, unchanged: automatic master succession and quorum election (platform plan’s
*Future automatic-succession gate*), automatic VM restart/HA (Phase 9), live migration
(Phase 8), cross-pool movement. S6 implements manual, operator-selected, fence-verified
promotion only; nothing in these slices elects a master by itself.

## Defects and gaps these slices must fix first

Found by reading the current tree. Each is assigned to the slice that must not build on top of it.

1. **`internal/localcore/pool.go:119` — join succeeds, response says it did not.** When
   `JoinPoolPeer` succeeds and `vmhost.RehomePoolScope` then fails, the handler returns `409`
   while the host *is* a member. Any caller that treats non-2xx as “not joined” desyncs.
   **S1** replaces this with a durable staged join whose stages are reported honestly
   (`joined`, `rehome-pending`, `ready`), and the local endpoint reports the same stages.
2. **`internal/localcore/pool.go:38-48` — join prechecks live only in the local HTTP handler.**
   The managed-VM and non-local-storage refusals are not enforced by `agent.JoinPoolPeer`, so a
   second caller (the S1 command path) would bypass them. **S1** moves them into one
   `agent.PoolJoinPrecheck` used by both callers (one guard, all callers).
3. **`internal/controller/managed.go:305-318` — VLAN uplink is validated against the master’s
   inventory only.** A member with different NIC names accepts `201` and fails asynchronously.
   **S1** adds the pre-registration per-member uplink surface (Controller-side projection of
   each host’s reported `Inventory.NICs`; the authoritative refusal stays on Core).
4. **`internal/agent/pool_storage.go:333,379,401,423` — the storage peer list discards
   `AppliedStorage`’s `managed-journal` check** and can be served short while networks and media
   (`pool_network.go:93`, `pool_media.go:85`) fail closed with `503`. Recorded as an open
   project-owner decision in the [pool master model](2026-09-09-pool-master-model.md); see
   owner decision D4. **S4** applies the chosen behavior, because a replica built from a short
   list would be worse than a short peer list. **Done (2026-09-19):** all four peer sites
   (`/access`, `/lock`, the list and the single-repository GET) now fail closed with
   `503 pool resource list unavailable: managed journal is incomplete`, matching networks and
   media; a member aborts at stage `master-list` and detaches nothing.
5. **`internal/agent/pool_vm.go:365` — `pool-vms.json` owner records are never pruned.** Latent
   today; it becomes a growth and correctness problem once the whole file is replicated. **S4**
   adds bounded pruning of records whose VM has a `vm-delete` tombstone in the owner’s published
   inventory, with the pool-wide dependency check still reading published inventory.
   **Done (2026-09-19):** a record is deleted only on positive evidence — the publishing host is
   its owner, that inventory is complete and at the current membership generation, it carries a
   succeeded `vm-delete` tombstone for the VM ID in the new `deleted` field of the member's peer
   VM inventory, the record has no pending update, and no other host's stored inventory still
   defines the VM ID. Absence is never read as deletion.
6. **`member_access` and converge status are memory-only (BUG-0158)** —
   `pool_storage.go:238-240,191-193`, `pool_network.go:25`, `pool_media.go:21`,
   `pool_network.go:459-462`. Not a bug to fix here: it is the reason a replica is never health
   evidence. **S4/S6** must depend on it explicitly (a promoted master re-collects fresh member
   observations before reporting health) and **S7** must exercise a master restart.
7. **Master change leaves two mirror artefacts** (raised in review, recorded in the pool master
   model): a promoted member holds *adopted* ISO records that never unlink the file, and the
   Controller’s mirror row for a pool VLAN is refreshed/tombstoned only by the recorded master,
   so the ex-master’s rows go stale. **S5** owns both: re-key Controller mirror rows to the new
   master on a reported master change (mirror-only, lane K), and document the adopted-ISO leak
   with a Core-side refusal rather than a silent unlink.

## Shared design decisions (routine implementation choices, already made)

- **Transport.** Controller→Core work uses the two transports that already exist and nothing
  new: the durable managed-command path (`protocol.Command` + `Kind` + `OperationID`, journaled
  at `internal/agent/state.go`/`managed.go:180-300`, terminal `protocol.Result`) for anything
  that must survive Controller loss, and the bounded in-memory broker pattern
  (`internal/controller/storage_cluster.go:17-65`) for the one call that must return a value in
  a single report round trip. The broker holds nothing after the round trip and writes nothing
  to SQLite.
- **Peer transport.** All Core-to-Core traffic reuses the existing pool mTLS mux
  (`internal/agent/peer.go:150-259`), `AuthenticatePoolPeer` / `AuthenticatePoolMaster`, and
  the existing clients `PoolPeerClient` (member→master) and `PoolMemberPeerClient`
  (`peer.go:355-459`, master→member, pinned to the member’s advertised `peer_origin`).
- **Durable state.** Everything pool-scoped is written by the pool master into the files that
  already exist in the Core state dir: `pool-peer.json` (`poolca.go:24`), `pool-vms.json`,
  `pool-storage.json`, plus two new files introduced here (`pool-replica.json` on members,
  `management-network.json` on each host). Atomic temp-file+rename saves as today
  (`poolca.go:69`).
- **Adoption.** Every new durable field is reported in `Inventory.Managed`
  (`protocol/protocol.go:99-119`, populated `run.go:223-231`) so a fresh Controller re-adopts it
  with the same identity and name. No Controller migration, export or recovery path is added.
- **Capability gating.** New constants in the existing style
  (`internal/protocol/*.go`, `"<feature>-v1"`), advertised from `run.go:~745` and checked by the
  Controller before it offers the control: `PoolJoinCapability = "pool-join-v1"`,
  `PoolMaintenanceCapability = "pool-maintenance-v1"`,
  `ManagementNetworkCapability = "management-network-v1"`,
  `PoolAuthorityReplicaCapability = "pool-authority-replica-v1"`,
  `PoolAuthorityTransferCapability = "pool-authority-transfer-v1"`. A missing capability on
  either peer blocks the operation with an upgrade-required message; it never silently degrades.
- **Idempotency.** Client `request_id` + server `operation_id` exactly as
  `replayResource` already defines them (`controller/managed.go:92-124`): exact retry `200`,
  changed retry `409`. Core-side durable stage machines are keyed by `operation_id` and are
  safe to re-execute.
- **Crypto.** Stdlib only (`go.mod` has one dependency, `go-sqlite3`). Key wrapping in S4 uses
  `crypto/ecdh` (from the member’s existing P-256 peer certificate key), `crypto/hkdf` (Go 1.24)
  and AES-GCM. No new dependency, no new PKI.
- **Never in the Controller database:** pool CA material, replicas, fence credentials, staged
  network configuration. Audit rows record the action and the actors, never the secret.
  **Correction (S1, implemented):** the join invitation is *not* invitation-free at rest. It
  reaches the joining host inside a durable `pool-join` command row, because the dispatch path
  reads the command back out of SQLite, so the invitation and pool CA sit in `managed_resources`
  from the `202` until the host collects them. They are scrubbed as soon as the operation leaves
  `queued`, and unconditionally once the row is 15 minutes old; the sweep runs at the top of all
  three pool-join routes, from a timer armed by the issuing request, and — **corrected after
  independent review (`BUG-0337`)** — from the Controller's health tick, at most once a minute,
  since none of the other triggers survives a restart. Residual risk, stated plainly: a single-use,
  15-minute, host- and pool-bound token is at rest for one heartbeat in the normal case, and for at
  most one minute past its window if the host never collects it. An uncollected expired join is now
  failed terminally rather than redelivered with empty fields (`BUG-0338`), and the joining host
  clears its own copy once the admission stage completes (`BUG-0350`). Redeeming it still requires the joining
  host's own CSR and reachability to the master, and the master binds that CSR at first redemption
  and refuses a second member. Removing the at-rest copy needs a command offer/handoff route of
  the kind the storage credential receipts use; S1 did not build one.

## Hot files and how to serialize them

One writer at a time. Prefer a new file per slice over editing a hot file; when a hot file must
change, make the edit the first task of that slice’s lane and hand it off before the rest of the
lane starts.

| Hot file | Why | Rule |
|---|---|---|
| `internal/protocol/pool.go`, `protocol.go` | wire types, envelope fields, capability constants | Lane C of the current slice only. Land the protocol edit **first** in the slice, in one commit-sized change, then unblock lanes K/U/D of the same slice and lane C of the next. |
| `internal/agent/peer.go` | peer mux dispatch (`:150-259`) | Add the route line only; handler bodies go in new files (`pool_join.go`, `pool_authority.go`). One writer per slice. |
| `internal/agent/run.go` | report assembly (`:203-260`) and tick loop (`:671-714`) | Append-only edits, one slice at a time; new work goes in a function defined in the slice’s own file. |
| `internal/agent/managed.go` | command switch (`:186-200`) | Add one `case`/branch per slice, one writer at a time. |
| `internal/controller/controller.go` | route table (`:271-340`) | Route lines only; handlers in new files (`pooljoin.go`, `poolauthority.go`, `hostnetwork.go`). |
| `internal/controller/managed.go` (1476 lines) | `placementHost`, `ready*ForHost`, mirroring | S2 and S5 both touch it: S2 first (placement refusal), S5 second (mirror re-key). Never concurrently. |
| `web/src/main.tsx` (2051 lines, one `App()`) | every screen is wired here | **Each UI slice adds its own file** (`web/src/pool-members.tsx`, `web/src/host-network.tsx`, `web/src/pool-authority.tsx`) and edits `main.tsx` only to import and mount one component (a handful of lines) plus a tab name. Those insertions are serialized: one UI lane at a time holds `main.tsx`, for minutes, not hours. |
| `docs/API.md`, `docs/api/openapi.json`, `local-openapi.json`, `peer-openapi.json` | living reference | Lane D of one slice at a time. Regenerate with `python3 scripts/generate_api.py` in the same change; `tests/test_api_contract.py` fails otherwise. |
| `tests/test_api_contract.py`, `docs/guide/README.md`, `CHANGELOG.md`, `docs/plans/2026-09-08-nex-platform.md` | shared indexes | Lane D of one slice at a time. |

## Execution order and pipeline

Sequential by slice: **S1 → S2 → S3 → S4 → S5 → S6 → S7**. S3 is independent of S4–S6 and may
be moved earlier or run on its own lane C if a second Core writer is available *and* it does not
hold `protocol.go` at the same time as another lane C.

Within the pipeline, while slice N’s lanes K (Controller), U (UI) and D (docs/tests) run, slice
N+1’s lane C (Core) may start, provided the protocol edit for slice N has already landed. S5
depends on S4’s replica being in the tree; S6 depends on S5’s transfer state machine. S7 starts
only when S1–S6 are in the working tree, but its lab construction (lane L) can be prepared any
time from `.cache/phase4-acceptance/lab`.

Every slice ends with an independent reviewer (a different agent) checking scope, the invariant,
the API impact decision, real evidence, and the privacy rules, before the next slice’s lanes
K/U/D start.

---

# S1 — “Add host to pool” from the Controller

**Status: implemented in the working tree, native acceptance outstanding (2026-09-19).** Core,
Controller, UI and documentation lanes are complete and unstaged. Deviations from the plan as
written: the joining host's staged join executes on the agent's report loop rather than the
defaults ticker (completing a join rewrites the pool identity the managed worker snapshots);
`PoolCommand` gained `MemberHostID` as a fifth field, for command routing; the client-facing
`POST` body gained an optional `master_url`, because a master of its own single-host pool has
never advertised a peer origin — Core records the supplied address as the master's own
`peer_origin` at the first invitation, so only the first join in a pool's life asks for it; and
the invitation's at-rest lifetime is as corrected under *Shared design decisions* above.
Verified by Go unit tests (`internal/agent`, `internal/protocol`, `internal/localcore`,
`internal/controller`, including `-race`), the UI component suite and
`tests/test_api_contract.py`. **Not** verified: a native two-host join driven from the Controller,
and a browser/360 px review of the Hosts tab — both are S7 work, and
`tests/integration/pool_join_browser.py` is still to be written.

**API impact: new contract** — Controller `POST /api/v1/pools/{id}/members`,
`GET /api/v1/pools/{id}/members`; Core command kind `pool-join`; Core-local
`GET /api/local/v1/pool`; report-envelope `PoolRequests`/`PoolResults` under
`pool-join-v1`; inventory `managed.join`. **Guide impact: updated.**
**State owner: core** — record: the master’s `pool-peer.json` membership and offer table, and
the joining host’s `pool-peer.json` pending-join record; a fresh Controller recovers both from
`Inventory.Managed.Membership` and `Inventory.Managed.Join`.

## Core behavior

The Controller must not drive CSR→offer→join. It makes exactly two calls: it asks the **master**
for a short-lived, single-use **join invitation**, and it hands that invitation to the **joining
host** as one durable command. The joining Core performs the whole exchange with the master peer
itself.

1. **Invitation (master).** New `agent.CreatePoolJoinInvitation(s, masterURL, memberHostID)` in a
   new file `internal/agent/pool_join.go`, reusing the existing offer record
   (`poolJoinOffer` in `poolca.go`, minted at `peer.go:67-114`) with `CSRHash` empty, which means
   *bind the CSR at redemption* instead of at issue. Returns `{master_url, pool_ca_pem,
   invitation, expires_at}`; 15-minute expiry and single use, exactly as today. The existing
   CSR-first offer path is untouched, so the documented host-local procedure keeps working.
   `acceptPeerJoin` (`peer.go:262-321`) is extended: when the stored offer has no `CSRHash`, it
   binds the presented CSR hash on first redemption and stores it, so the existing “same offer
   returns the same certificate, never a second member” replay rule applies unchanged.
2. **Precheck (joining host).** `agent.PoolJoinPrecheck(ctx, s, engine)` holds the refusals that
   currently live in `localcore/pool.go:38-48` (any managed VM; any non-local storage) plus the
   identity checks (enrolled; currently master of its own single-host pool; not already a member
   of another pool). Both the local endpoint and the command path call it.
3. **Durable staged join (joining host).** `pool-peer.json` gains
   `PendingJoin {operation_id, master_url, ca_pem, invitation, peer_origin, stage, attempts,
   last_error, updated_at}`. Stages: `precheck → csr → joined → rehomed → advertised → ready`.
   The agent executes it on receipt and re-attempts it from the existing defaults ticker
   (`run.go:193-238`) after a restart, a reboot or a Controller outage, under `poolPeerMu`, with
   the convergence single-flight guard (`run.go:685`, `poolReconcileRunning`) held so a pass
   cannot run against half-changed identity. A crash at any stage resumes at that stage; each
   stage is individually idempotent (`csr` reuses the persisted CSR at `peer.go:322-343`;
   `joined` replays the same invitation and gets the same certificate; `rehomed` re-runs
   `vmhost.RehomePoolScope`; `advertised` re-posts `peer_origin`).
4. **Compatibility report (master, before the operator confirms).** New peer endpoint
   `POST /api/v1/pool/compatibility` is *not* needed: the candidate is not yet a peer. Instead
   the Controller composes the preview from what both hosts already report in inventory —
   agent version, capability lists, NIC names versus each pool VLAN’s `uplink`
   (`protocol/managed.go` `NetworkRegistration`), CPU baseline
   (`Inventory.CPUBaseline`, `internal/agent/pool_cpu.go`), managed VMs and non-local storage —
   and labels each row `compatible` / `will not converge` / `refuses join`. This is presentation
   of host-reported facts; every refusal is still decided by Core.

## Host API

**Controller → master (value-returning, one report round trip).**
`ReportResponse.PoolRequests []protocol.PoolRequest` and
`ReportRequest.PoolResults []protocol.PoolResult`, gated by `PoolJoinCapability`, brokered by a
new `internal/controller/pooljoin.go` modelled line-for-line on
`internal/controller/storage_cluster.go:17-65` (in-memory map, one pending entry, result
channel, `504` with a Core-shaped body on deadline). `PoolRequest{request_id, pool_id, host_id,
action:"invite", member_host_id, master_url}` → `PoolResult{request_id, status, error,
invitation{master_url, pool_ca_pem, invitation, expires_at}}`. The invitation exists in
Controller memory only for the remainder of the HTTP request; it is never stored, logged or
audited.

**Controller → joining host (durable).** `protocol.Command{Kind:"pool-join", Pool:&PoolCommand{
Action:"join", ...}}`. `PoolCommand` (`protocol/pool.go:32-57`) gains `Invitation`,
`PoolCAPEM`, `MasterURL`, `PeerOrigin` and accepts `Action == "join"`; `Valid()` requires all
four for that action and keeps the existing rules for the other actions. Journaled by the
existing admission path (`state.go` `managedCommand`), terminal `protocol.Result`.

**Inventory.** `ManagedState.Join *protocol.PoolJoinStatus{operation_id, stage, status
(pending|succeeded|failed|needs_attention), error, observed_at}` on the joining host;
`PoolMember` already carries `status` and `error` on the master.

**Controller client-facing REST.**
- `POST /api/v1/pools/{id}/members` — body `{request_id, host_id, peer_origin}`. Admin session
  (`c.admin`), same-origin/CSRF as every other Controller mutation. Validates: pool exists; the
  named host is enrolled, fresh (`StaleAfter`), not revoked, currently the master of its own
  single-host pool; both hosts advertise `pool-join-v1`; `peer_origin` is a bare HTTPS origin.
  `202` with the operation; `409` for capability, freshness, identity or replay-with-changed-
  payload; `404` unknown pool/host. Retry with the same `request_id` and identical payload
  returns the original operation (`replayResource`).
- `GET /api/v1/pools/{id}/members` — projection only: the master’s reported
  `inventory.managed.membership`, each member’s `connection` freshness, its three convergence
  checks (`pool-storage-reconciliation`, `pool-network-reconciliation`,
  `pool-media-reconciliation`) and counts of `member_access` rows that are healthy / pending /
  failed / stale. Zero new storage. When the master is stale or unreachable the response says
  so and marks every derived field `unknown` rather than showing the last mirror as current.

**Core-local.** `GET /api/local/v1/pool` (authenticated local root session, existing `s.auth`)
returns `{pool_id, pool_name, role: master|member, master_host_id, members[], join:{stage,
status, error}}`. This closes the gap the code map records at
`.cache/phase4-acceptance/code-map.md:47`: membership is currently observable only through the
Controller, which a host-local recovery procedure cannot rely on.

**Errors, outage, versions.** A Controller that dies after the invitation call and before the
command leaves an unredeemed invitation that expires in 15 minutes — no state to clean up. A
Controller that dies after issuing the command changes nothing: the joining Core finishes and
reports on reconnect. A Core without `pool-join-v1` cannot be a master or a joiner through this
path; the Controller says “upgrade both hosts” and the host-local procedure remains documented.

## Interruption windows and safety

| Interrupted | Outcome |
|---|---|
| Controller dies between invite and command | Invitation expires unused. No membership change. |
| Joining Core dies after `csr` | Resumes with the same persisted CSR; the invitation is still unbound or bound to that same CSR. |
| Joining Core dies after the master issued the certificate but before its reply landed | Replay of the same invitation returns the same certificate and membership (`peer.go:287-296`); never a second member row. |
| Joining Core dies after `joined`, before `rehomed` | Host **is** a member and says so (`stage:"rehomed"`, `status:"needs_attention"`); the tick retries the rehome. Defect 1 above is fixed by this. |
| Master dies mid-exchange | Joiner retries; the offer is durable in `pool-peer.json`. |
| Both retried concurrently | `poolPeerMu` plus the single-use offer digest; the membership generation bump is inside the same lock. |

Safety properties: one membership row per host ID (`peer.go:305-308` refuses a second); host and
pool identities are host-generated and preserved; no pool is created; the joining host keeps its
host ID and local VMs are refused outright by the precheck, so no workload changes owner.

## Controller relay + UI

New `web/src/pool-members.tsx`, mounted from `main.tsx` as the pool detail **Hosts** view.
- Members table: host, role badge (reuses the existing **Pool master** / **Member** badges,
  `main.tsx:1757`), connection freshness, convergence summary, `member_access` failure count with
  the Core-reported message.
- **Add host to pool** button → dialog: pick an enrolled host that is master of its own pool,
  show the compatibility preview (versions, capabilities, NIC names vs each pool VLAN uplink,
  CPU baseline, blocking VMs/storage), enter the member’s peer origin, and confirm. The dialog
  states plainly that joining is one-way in this release and that a host with VMs or remote
  storage is refused.
- Progress is the reported `join.stage`, not a spinner: `pending`, `needs attention` with Core’s
  message and a retry note, `ready`. Stale master ⇒ every row `unknown`, no invented success.

## Tests

- Go (`internal/agent/pool_join_test.go`, new): invitation issue/expiry/single use; CSR bound at
  redemption; replay returns the same certificate; precheck refusals; every stage resumed from a
  truncated `pool-peer.json`; concurrent join attempts under `-race`.
- Go (`internal/controller/pooljoin_test.go`, new): broker round trip, deadline, result
  validation, `409` matrix, `GET` projection with a stale master.
- Contract (`tests/test_api_contract.py`): **done** — the three Controller routes, the local
  route, the new protocol fields and command payload, the documented refusal strings checked
  against the handler literals, and all three OpenAPI documents regenerated.
- Browser (`web/src/pool-members.test.mjs` **done** + `tests/integration/pool_join_browser.py`
  **outstanding**, simulated): empty state, compatibility preview with an incompatible uplink,
  refusal messages, needs-attention state, stale-master unknown state, keyboard and 360px review.
- Native: deferred to S7. Outstanding.

## Docs

`docs/API.md` (pool section), `docs/api/openapi.json`, `local-openapi.json`,
`docs/api/peer-openapi.json` (the redemption change), regenerate HTML;
rewrite `docs/guide/join-a-pool.md` around the Controller workflow with the host-local procedure
kept as the recovery path; `docs/guide/README.md`; `docs/changes/2026-09-XX-pool-join-ui.json`;
status lines in this plan, the platform plan’s Phase 4 implementation-status paragraph, and the
pool master model’s “Not in this change” section.

## Lanes (exclusive write sets)

- **C:** `internal/agent/pool_join.go` (new), `internal/agent/pool_join_test.go` (new),
  `internal/agent/peer.go` (mux line + `acceptPeerJoin` binding), `internal/agent/managed.go`
  (one branch), `internal/agent/run.go` (report field, tick resume), `internal/protocol/pool.go`,
  `internal/protocol/protocol.go`, `internal/localcore/pool.go`, `internal/localcore/service.go`.
- **K:** `internal/controller/pooljoin.go` (new), `pooljoin_test.go` (new),
  `internal/controller/controller.go` (route lines), `internal/controller/managed.go`
  (uplink-per-member projection only).
- **U:** `web/src/pool-members.tsx` (new), `pool-members.test.mjs` (new), `web/src/style.css`,
  and a short serialized insertion in `web/src/main.tsx`.
- **D:** `docs/API.md`, `docs/api/*`, `tests/test_api_contract.py`,
  `tests/integration/pool_join_browser.py` (new), `docs/guide/join-a-pool.md`,
  `docs/guide/README.md`, `docs/changes/*.json`, `CHANGELOG.md`, this plan.

---

# S2 — Maintenance mode

**Status: implemented in the working tree, native acceptance outstanding (2026-09-19).** Core,
Controller, UI and documentation lanes are complete and unstaged. Deviations from the plan as
written, all deliberate:

- **The membership generation is deliberately not bumped** when the flag changes. The plan's own
  Core-behavior paragraph requires the flag to stay outside the generation equality rule, so a
  drain cannot invalidate an in-flight placement, a published inventory or a peer generation
  check; the brief's "generation-bumped" wording was wrong and was not followed.
- **There is no `confirmation` field.** The Host-API sketch below asked for the host name when
  draining a host with running guests. Maintenance stops nothing that is running, so there is
  nothing to confirm against; Core accepts no confirmation and the Controller collects none.
  Instead the members projection reports that host's own guest list (`guests`, with
  `known:false` when the host is not reporting) and the UI dialog shows it before the operator
  agrees.
- **"Remaining guests reported" is not a new contract.** `pool-vms.json` has no wire projection,
  so the guest list comes from the Controller's existing per-host mirror of `inventory.vms`. A
  host that is not reporting has no guest list on the wire, and the UI says unknown.
- Core's own refusals surface as the operation's failed result with `conflict` marked in the
  members projection, never as a 409 of the submitting route: the command is durable and
  asynchronous. The route's own 409s are the Controller's pre-checks against host-reported state.

Verified by Go unit tests with `-race` (`internal/agent`, `internal/protocol`,
`internal/controller`), the UI component suite and `tests/test_api_contract.py`
(`PoolMaintenanceContractTests`). **Not** verified: a native multi-host drain, and a browser/360 px
review of the Maintenance column, its dialog and the placement refusal — both S7 work.

**API impact: new contract** — Controller `POST /api/v1/pools/{id}/members/{host_id}/maintenance`;
Core command kind `pool-maintenance`; `PoolMember.maintenance` in membership inventory.
**Guide impact: updated** (new page). **State owner: core** — record: `PoolMember.Maintenance`
and `MaintenanceSince` inside the master’s `pool-peer.json` membership, reported in
`Inventory.Managed.Membership` and recovered by any fresh Controller with no operator re-entry.

## Core behavior

A per-host durable flag held by the **pool master** inside the membership record it already owns.
It deliberately does **not** participate in the membership generation equality rule
(`protocol/pool.go:58-74` is unchanged), so toggling maintenance never invalidates in-flight
placements, inventory publications or peer generation checks.

While a host is in maintenance the master refuses, from its own record:
- new placement of a VM onto that host — `localPoolVMPlacement` (`placement.go:46-56`) and
  `poolVMPlacement` (`placement.go:122-129`), both refusing with a bounded message through the
  existing `placementError` sanitizer (`placement.go:111-120`);
- `start` admission for that host — the peer `case "start"` (`pool_vm.go:846`) and
  `poolVMLocalPlacement` (`pool_vm.go:1363`). **Correction (2026-09-19):** the line references above
  were stale, and `case "define"` (`pool_vm.go:815`) has no separate guard of its own. Definitions
  are covered because every placement and definition transition passes `poolVMPlacement`
  (`placement.go:122-129`), which is the single choke point; this is not a hole, but the plan's
  original wording implied a guard that does not exist at that site;
- autostart-by-placement — `ReconcilePoolVMAutostart` (`pool_vm.go:1449-1455`) skips the host and
  records why.

It does **not** stop running guests, block `stop`/`shutdown` (the stop asymmetry at
`pool_vm.go:912-918` is preserved), block local recovery, or block a member’s own convergence.
Remaining guests are reported from the master’s `pool-vms.json` published inventory
(`state.Inventory[host].VMs`), counted as running and assigned. Removal/eject (S5) refuses while
a host in maintenance still holds guests.

## Host API

- **Controller → master:** `protocol.Command{Kind:"pool-maintenance", Pool:&PoolCommand{
  Action:"maintenance", MemberHostID, Enabled, ExpectedVersion}}`. Master-only (the existing
  `commandHost` routing plus a `membership.MasterHostID == s.State.HostID` check, as
  `BeginPoolResourceRemoval` does at `managed.go:300-320`). Durable, idempotent by
  `operation_id`; enabling an already-enabled flag is a success, not a conflict.
- **Inventory:** `PoolMember.Maintenance bool`, `MaintenanceSince time.Time`.
- **Controller REST:** `POST /api/v1/pools/{id}/members/{host_id}/maintenance`
  `{request_id, enabled, confirmation}`. Admin session; `confirmation` must equal the host’s
  reported hostname when enabling on a host with running guests (same pattern as the existing
  destructive confirmations). `202`; `409` on missing `pool-maintenance-v1`, a stale or
  unreachable master, or an unknown member; `404` unknown pool/host.
- **Controller:** `placementHost` (`managed.go:46`) additionally refuses a maintenance target
  with a clear message, from the mirrored membership. This is presentation and early failure;
  the authoritative refusal is Core’s.
- **Outage:** the flag is Core state; a Controller outage changes nothing. A master restart
  reloads it from `pool-peer.json`.

## Interruption windows and safety

A crash between the master’s durable save and the Controller’s result leaves the flag set and the
task `pending`; the next inventory report shows the true value, and the exact retry returns the
original operation. A placement already admitted before the flag was set completes: maintenance
blocks *new* placements, and the plan says so in the UI. No dual-writer effect: maintenance never
moves ownership or issues a permit.

## Controller relay + UI

In `web/src/pool-members.tsx` (S1’s file; S2 extends it — same UI lane, sequenced after S1):
a **Maintenance** toggle per member with a confirmation dialog listing the guests still on the
host, and a persistent badge on the host row and host detail panel. Attempting to place a VM on a
host in maintenance is disabled in the picker with the reason shown, not silently filtered.

## Tests

- Go (`internal/agent/pool_maintenance_test.go`, new): flag persistence and reload; placement,
  define, start and autostart refusals; stop still permitted; idempotent enable/disable;
  membership `Valid()` unaffected; `-race`.
- Go (`internal/controller/`): `placementHost` refusal and message; `409` matrix.
- Contract: route, command payload, membership field.
- Browser: toggle, confirmation with remaining guests, disabled placement target with reason.

## Docs

`docs/API.md`; `docs/api/openapi.json`; new `docs/guide/host-maintenance-mode.md` (six required
sections per [docs/DOCUMENTATION.md](../DOCUMENTATION.md)); `docs/guide/README.md`;
`docs/changes/*.json`; platform plan item 5 status.

## Lanes

- **C:** `internal/agent/pool_maintenance.go` (new), its test,
  `internal/agent/placement.go`, `internal/agent/pool_vm.go`, `internal/protocol/pool.go`.
- **K:** `internal/controller/pooljoin.go` (same file, maintenance handler) or a new
  `poolmaintenance.go`, `controller.go` route line, `managed.go` `placementHost`.
- **U:** `web/src/pool-members.tsx`, its test.
- **D:** as S1’s D set, plus the new guide page.

---

# S3 — Staged remote management-network edits

**Status: implemented in the working tree, native acceptance outstanding (2026-09-19).** Core,
Controller, UI and documentation lanes are complete and unstaged, and the *Roll back now* defect
first recorded here has been fixed. Deviations and decisions taken while implementing, all
deliberate unless marked as a defect:

- **Interface renames are not part of a staged change.** `/etc/systemd/network/*.link` is never
  rewritten at runtime: a rename only takes effect on boot and could not be undone inside a
  rollback window, so the spec names interfaces the host already has. The `state.record` sentence
  below is narrowed accordingly; a reboot-scoped rename operation would be its own slice.
- **A reboot inside the window rolls the change back, and no trigger reads the clock.**
  **Corrected after independent review (`BUG-0340`):** the original design compared `/proc/uptime`
  with `applied_at`, which is a wall-clock comparison and failed in exactly the case the rollback
  exists for — a host that lost its time source with the change and came back with a wrong RTC.
  Apply now stamps `boot_id` and `applied_uptime_ns` into the record; "rebooted" is a boot-id
  inequality and "window spent" is boot-relative elapsed time within the same boot. `deadline`
  survives only as the operator-facing value, and `seconds_remaining` and `confirm` use the same
  helper so neither can disagree with the trigger. A record without those fields falls back to the
  previous comparisons.
- **`/etc/resolv.conf` must be a regular file.** All three targets are `Lstat`-checked, so a
  `systemd-resolved` symlink blocks every edit until it is replaced with a plain file. Nex Core’s
  installer writes a plain file. Documented as a limitation rather than special-cased, because
  silently skipping a file the rollback would have to restore is worse.
- **The rollback is armed three ways**: unconditionally at agent start, on every probe tick, and by
  an exact `time.AfterFunc` armed at apply. Only the durable record is the guarantee; the timer is
  an optimization that makes the deadline precise while the process lives. **All three run inside
  the agent process** (review finding P-1). Both units are now `Restart=always` rather than
  `on-failure`, which closes the crash, OOM-kill and clean-exit holes, because
  `RecoverManagementNetwork` runs unconditionally at start. **Named limit, not fixed:** a unit
  deliberately stopped or masked between apply and deadline, or a package upgrade that leaves it
  stopped, arms nothing until it is started again. A transient systemd timer would need a new
  subcommand, packaging changes and systemd to test against, and was not built.
- **Confirm requires Core-observed connectivity** over the new configuration — a successful
  authenticated Controller report or host-local API call since the apply **whose connection
  terminated on an address of `br0` itself, with a far end that is not this machine**. **Narrowed
  after independent review (`BUG-0342`):** the original rule accepted a loopback local call and a
  report that left over a second, unchanged NIC, so the gate proved nothing. An observation that
  now counts cannot be produced in-process, because every in-process lab runs on loopback.
- **`change_id` separates the staged change from the command ID.** The command’s `operation_id`
  stays unique per step (it is the managed journal’s replay key) while `change_id` names the change
  Core acts on. Without it every `apply`, `confirm` and `cancel` was refused; found by the
  cross-layer test, recorded as `BUG-0318`.
- **A staged change does not expire.** The interruption table below says a staged record “expires
  and is discarded at the deadline”; it does not. There is no deadline until a change is applied. A
  staged change is replaced by staging again, or discarded by `cancel`.
- **Core conflicts are never the submitting route’s HTTP status.** Every step is a durable command,
  so the `409`s the route returns are the Controller’s own pre-checks against host-reported state;
  Core’s refusals arrive as a failed operation whose error ends with `management network conflict`.
  The Host-API sketch below, which lists “a confirm after the deadline” among the route’s `409`s,
  is wrong in that respect.
- **Defect, fixed (`BUG-0327`, 2026-09-19):** the UI offered *Roll back now* on an applied change
  and submitted `cancel`, which Core refused on anything but a staged change, so an applied change
  could not be undone early from the Controller. `cancel` on an **applied, unconfirmed** change now
  performs the rollback — the same `rollbackLocked` restore the deadline would have run — with the
  reason `rolled back at the operator's request` and a durable `requested_rollback` bit set before
  the restore begins, so an interrupted rollback resumes as the same operator rollback. The bit is
  reported as `management_network.requested_rollback` and the agent emits **no** `management-network`
  health check for that case: an undo the operator asked for is an outcome, not a failure. Neither
  a new action nor a Controller-driven rollback was added; the rollback stays Core's and stays
  re-runnable, because it can cut the connection that asked for it. Covered by a cross-layer
  Controller→Core→engine test that was observed failing first; **still no native evidence.**
- **No native and no browser evidence.** `TestManagementNetworkNetns` has never executed (it skips
  without `ifupdown`), so the real `ifup`/`ifdown` bounce and the real rollback are unproven; S7
  owns both, together with `tests/integration/host_network_browser.py`, which is not written.

**API impact: new contract** — Controller `GET|POST /api/v1/hosts/{id}/network`; Core command kind
`management-network`; inventory `management_network`; capability `management-network-v1`.
**Guide impact: updated** (new page). **State owner: core** — record: each host’s own
`/etc/network/interfaces`, `/etc/dhcpcd.conf` and `/etc/resolv.conf`, plus
`<state-dir>/management-network.json` holding the staged change, the verbatim previous contents of
those three files, the chosen window and the rollback deadline. `/etc/systemd/network/*.link` is
**not** part of a staged change. Host-scoped, not pool-scoped; a fresh Controller reads the current
state from that host’s inventory `management_network`.

## Core behavior

There is **no runtime management-network editing in the product today**: the only renderer is the
installer’s GTK wizard (`installer/gui/nex-network.c:143-199`), which writes
`/etc/network/interfaces`, `/etc/dhcpcd.conf` and `/etc/resolv.conf` as whole files with MAC-bound
interface names and a `br0` management bridge. S3 re-implements that render in Go, from the same
rules, and adds the staged-apply state machine. The guest VLAN path is untouched: Core still only
appends `source /etc/network/interfaces.d/*` and never edits `interfaces.d/*` here
(`internal/vmhost/vlan.go:85-101,181`).

New `internal/vmhost/management_network.go`:
- `RenderManagementNetwork(spec)` — the Go port of the installer render, same file shapes and the
  same validation (`nex-network.c:72-112`): one management interface, dhcp xor static, CIDR and
  gateway parsing, DNS rules.
- `StageManagementNetwork(dir, spec, operationID, window)` — validates, snapshots the current
  rendered files, writes `management-network.json` `{operation_id, revision, stage:"staged",
  spec, previous{interfaces, dhcpcd, resolv}, deadline, applied_at, confirmed_at, error}`, writes
  nothing into `/etc`.
- `ApplyManagementNetwork(dir)` — swaps the files atomically, **reconfigures `br0` in place**
  (address, route, hardware address, DHCP client, and the uplink port only when the management
  interface or VLAN changes; `interfaces.d/*` bridges are not touched and no guest tap is ever
  detached), sets `stage:"applied"` and records `deadline`, `boot_id` and `applied_uptime_ns`
  before any file is written. As planned, this brought the interface down and up; see the
  correction under **Interruption windows and safety** and `BUG-0341`.
- `ConfirmManagementNetwork(dir, operationID)` — accepted only when Core has *also* observed its
  own connectivity over the new configuration since the apply: a successful authenticated
  Controller report, or a successful authenticated host-local API call. An operator confirmation
  without that observation is refused, because an operator can confirm from a path that is not
  the one being tested.
- `RollbackManagementNetwork(dir, reason)` — restores the snapshot and re-applies. Called by an
  in-process timer **and** unconditionally at agent start when an applied, unconfirmed record has
  spent its window. That second call is what makes the promise true with no Controller: a host that
  loses connectivity and is power-cycled rolls back on boot.
- **Correction (review finding D-1).** An earlier draft of this plan recorded as an open defect
  that "no command action reaches `RollbackManagementNetwork`, so an applied change cannot be undone
  early from the Controller". That has not been true since the operator-rollback fix (`BUG-0327`):
  `CancelManagementNetwork` rolls an applied change back through `rollbackLocked`,
  `ExecuteManagementNetwork` routes `cancel` to it, `hostNetworkRefusal` deliberately lets `cancel`
  reach a non-fresh host, and `TestHostNetworkOperatorRollbackEndToEnd` proves the round trip.
  `RollbackManagementNetwork` itself remains unreachable from any command path; only
  `RecoverManagementNetwork` and `cancel` call into the rollback.
- **Correction (review finding D-3).** The engine's own comment asserted that the spec "names
  interfaces that already exist on the host". Nothing enforced it. `checkManagementSpecAgainstHost`
  now does, at stage time (`BUG-0344`). A spec that simply **omits** a NIC carrying guest VLANs is
  still not refused — whether an omitted NIC means "leave alone" or "disabled" is a product
  decision and stays open.

Default window: 10 minutes, bounded 2–30, chosen per request. Ownership proof before any
destructive step follows the existing exact-match pattern (`vlan.go:43-65`): Core refuses to
overwrite an `/etc/network/interfaces` it did not render, and says so.

## Host API

- **Controller → host:** `protocol.Command{Kind:"management-network", ManagementNetwork:
  &ManagementNetworkRequest{operation_id, action: stage|apply|confirm|cancel, spec,
  expected_revision, window_seconds, confirmation}}`. Durable, journaled, idempotent per
  `operation_id`; `expected_revision` mismatch is a conflict (same rule as
  `ResourceLifecycleRequest`, `controller/managed.go:414-416`).
- **Inventory:** `Inventory.ManagementNetwork *ManagementNetworkStatus{revision, stage, spec,
  deadline, seconds_remaining, last_error, observed_at}` plus a `management-network` health check
  (`protocol.Check`) that reports `failed` after an automatic rollback until an operator clears
  it by a successful new apply.
- **Controller REST:** `POST /api/v1/hosts/{id}/network` `{request_id, action, spec,
  expected_revision, window_seconds, confirmation}`. Admin session. `confirmation` must equal the
  host’s reported hostname for `apply`. `202`; `409` for capability, staleness, revision
  mismatch, an apply with no matching staged revision, or a confirm after the deadline.
- **Outage/reconnect:** the Controller is never required. If it dies after `apply`, the host
  rolls back on its own deadline and reports it on reconnect. If the host is unreachable when the
  operator wants to confirm, nothing confirms and the host rolls back — the intended behavior.
- **Versions:** gated by `management-network-v1` on that host.

## Interruption windows and safety

| Interrupted | Outcome |
|---|---|
| After `stage` | Nothing in `/etc` changed. The staged record does **not** expire — there is no deadline until a change is applied; it is replaced by staging again, or discarded by `cancel`. Re-staging the same change ID with a *different* configuration is a conflict (`BUG-0345`). |
| During `apply`, mid-write | Files are written to temp paths and renamed; a crash leaves either the old or the new file, and the record’s `stage` tells Core which, so the start-time check completes or rolls back. |
| After `apply`, connectivity lost | Timer or next boot rolls back. This is the whole point of the slice and is exercised natively in S7. |
| After `confirm` | No rollback; record becomes `revision+1`, snapshot dropped. |
| Controller dies anywhere | Irrelevant to the outcome. |

Safety: the management bridge and host addressing are the host’s own; no pool-wide effect, and
guest VLAN bridges under `interfaces.d/` are never rewritten. **Corrected after independent review
(`BUG-0341`):** the original design said "no VM disruption beyond the interface bounce". The bounce
itself was the disruption — `ifdown br0` runs ifupdown's bridge hook, which deletes the bridge, and
every guest tap enslaved to it was cut off until its domain was restarted. `br0` is now
reconfigured **in place** with `ip(8)` and its uplink port moved only when the interface or VLAN
changes; `ifdown`, `brctl`, `ip link del` and `ip link set br0 down` are never run while the bridge
exists. **This is designed and unit-tested behaviour and has never been observed on a real host.**

## Controller relay + UI

New `web/src/host-network.tsx`, mounted as a **Network** panel on the host detail page. Shows the
current rendered configuration read from inventory, an edit form, and an explicit two-step flow:
**Stage** (diff preview, plus the preview the platform plan’s UX acceptance requires — which VMs
and which management access are affected), then **Apply** with a typed hostname confirmation and
a plain statement of the rollback window, then a live countdown with a **Keep this configuration**
button. Losing contact with the host during the countdown is displayed as exactly that —
`unknown`, with the deadline — never as failure or success.

## Tests

- Go (`internal/vmhost/management_network_test.go`, new): render equivalence against the
  installer’s rules for dhcp/static/bridge/VLAN-tagged management cases; validation refusals;
  stage/apply/confirm/cancel state machine; rollback on deadline; rollback at start after a
  simulated crash; refusal to overwrite a foreign `interfaces` file; `-race`.
- Go: command routing and inventory projection.
- Namespace check in the style of `TestGuestVLANNetns` (`internal/vmhost/vlan_test.go`) under
  `unshare -Urnm`: real apply and rollback on dummy interfaces. Labelled a namespace simulation,
  not a hardware test.
- Contract, browser (countdown, confirmations, unknown state), and the native rollback case in S7.

## Docs

`docs/API.md`; `docs/api/openapi.json`; new `docs/guide/change-management-network.md` with the
recovery procedure if the rollback itself fails (local console); `docs/guide/README.md`;
`docs/changes/*.json`; platform plan item 4 and UX acceptance status.

## Lanes

- **C:** `internal/vmhost/management_network.go` (+ test), `internal/agent/managed.go` branch,
  `internal/agent/run.go` report field, `internal/protocol/protocol.go` (status + capability),
  `internal/protocol/network.go` or a new `internal/protocol/hostnetwork.go`.
- **K:** `internal/controller/hostnetwork.go` (new) + route line.
- **U:** `web/src/host-network.tsx` (+ test), serialized `main.tsx` insertion.
- **D:** API/guide/change record/contract test, `tests/integration/host_network_browser.py` (new).

---

# S4 — Member authority replica

**Status: implemented in the working tree, native acceptance outstanding (2026-09-19).** Core,
Controller, UI and documentation lanes are complete and unstaged, together with defect 4 (D4, the
storage peer list now fails closed with `503 pool resource list unavailable: managed journal is
incomplete` at all four peer sites) and defect 5 (bounded `pool-vms.json` owner pruning on a
positive `vm-delete` tombstone carried by the new `deleted` field of the member's peer VM
inventory; absence is never read as deletion). Deviations from the plan as written, all
deliberate:

- **The push is driven by the probe tick, not synchronously by every authority write.** The master
  rebuilds its authority, digests it, and advances and persists the generation only when that
  digest changed. This avoids edits at dozens of write sites across hot files owned by other
  slices, is crash-safe and monotonic, and costs at most one probe interval of lag — harmless,
  because the replica is never authority and never health evidence. S5 can call
  `PushPoolAuthorityReplicas` directly where an exact-generation acknowledgement is required.
- **Acknowledged generations are memory-only observations** on the master, overlaid on the
  reported copy of the membership record (`replica_generation`, `replica_digest`, `replica_error`,
  `replica_observed_at`), exactly as `member_access` is. Writing them durably would put an
  observation into the membership record.
- **Inventory snapshots are not replicated**, only the VM ownership record: snapshots are
  observations, a promoted master re-collects fresh ones (BUG-0158), and excluding them keeps the
  replica well inside the 1 MiB bound. **SMB credential receipts are stripped**, as the storage
  peer list already strips them, so a future promotion holds an SMB repository record whose
  credential an operator must re-supply. S6 must either replicate a receipt reference or refuse
  promotion with that named blocker; this slice invents neither.
- **Nothing reports the master's own authority generation.** `pool-peer.json` holds it, but no
  `ManagedState` field carries it, so the Controller derives no absolute generation — that would
  be Controller-computed state. A member is shown as `behind` only when its two reports disagree
  or another fresh member of the pool is acknowledged higher. If S5/S6 need an absolute number,
  Core must report it.
- `internal/agent/poolca.go` (two fields) and `internal/agent/pool_vm_inventory.go` (the `deleted`
  field and the pruning call) were edited outside the lane's write set, because the plan names
  `pool-peer.json` as the durable record and defect 5 needs positive delete evidence on the wire.

Verified by Go unit tests with `-race` (`internal/agent`, `internal/protocol`,
`internal/controller`), the UI component suite and `tests/test_api_contract.py`
(`PoolAuthorityReplicaContractTests`). **Not** verified: a native multi-host push, including a
master restart (defect 6), and a browser/360 px review of the Authority copy column and its stale
state — both S7 work.

**API impact: new contract** — peer `POST /api/v1/pool/replica`; inventory
`managed.replica` and `PoolMember.replica_generation`. **Guide impact: updated** (recovery page).
**State owner: core** — record: the master’s authority generation in `pool-peer.json` and each
member’s `<state-dir>/pool-replica.json`; a fresh Controller reads the active master’s inventory
and stores none of it.

## Core behavior

The master maintains `AuthorityGeneration uint64` in `pool-peer.json`, incremented on every
durable authority write (membership change, maintenance toggle, `pool-vms.json` owner/epoch/
pending-update change, storage/network/media/native-cluster record change). On each change and on
the existing probe tick (`run.go:671-714`) it pushes to every `ready` member whose acknowledged
generation is behind.

Replica payload `protocol.PoolAuthorityReplica`:
`{pool_id, master_host_id, generation, created_at, membership, vms (the pool-vms.json
projection), storage, networks, media, storage_clusters, ca_key_wrapped, sections[]{name,
sha256}, signature}`. The signature is over the canonical JSON of everything except
`signature`, made with the pool CA key; members verify it against the pool CA they already hold
(`pool-peer.json.CAPEM`). `sections[]` digests let a member — and a later promotion check —
prove the replica is complete rather than truncated.

`ca_key_wrapped` is the pool CA private key wrapped **per member**: ECDH between the master’s CA
key and that member’s peer-certificate P-256 public key (`crypto/ecdh` via
`(*ecdsa.PublicKey).ECDH()`), HKDF-SHA256 (`crypto/hkdf`, Go 1.24) with a context string binding
pool ID, member host ID and generation, then AES-256-GCM. Stdlib only; no new dependency. A
member stores it sealed and can unwrap it **only** during a promotion it has been told to perform
(S6). File mode `0600` in the already-`0700` state dir.

Members write `pool-replica.json` atomically, accept only a **strictly higher** generation from
the current master, and never act on its contents. Rejections (bad signature, wrong pool, lower
or equal generation, missing section digest) are refusals with bounded messages, not silent
drops.

This slice also lands the two hygiene fixes from the defects list, because the replica is what
makes them matter: bounded pruning of `pool-vms.json` owner records for VMs with a delete
tombstone in the owner’s published inventory (defect 5), and the storage peer list’s
fail-closed behavior chosen in owner decision D4 (defect 4).

## Host API

- **Peer:** `POST /api/v1/pool/replica` on the **member**, master→member, authenticated by
  `AuthenticatePoolMaster` exactly as `placement.go:77` does, bounded body (1 MiB) and deadline,
  handled from a new `internal/agent/pool_authority.go` with one dispatch line in `peer.go`.
  Response `{host_id, generation, verified, error}`. Idempotent: re-pushing the same generation
  returns the same acknowledgement. Refusals are `409` with a sanitized message.
- **Inventory:** member reports `ManagedState.Replica *PoolReplicaStatus{generation, signed_at,
  received_at, verified, error}`; the master reports each member’s acknowledged
  `PoolMember.ReplicaGeneration` and `ReplicaObservedAt` — observed state, mirrored by the
  Controller, stored nowhere else. A new health check `pool-authority-replica` on both sides
  reports `unknown`/`stale` honestly when a push has not landed within two probe intervals.
- **Controller:** no new REST route; the existing `GET /api/v1/hosts` and S1’s
  `GET /api/v1/pools/{id}/members` carry the fields.
- **Versions:** `pool-authority-replica-v1` on both peers. A master with the capability and a
  member without it records the member as `replica_generation: 0` with an upgrade-required
  reason, and S5/S6 treat that member as unable to be promoted.

## Interruption windows and safety

A push interrupted mid-write leaves the previous replica intact (temp+rename). A master that
crashes after bumping `AuthorityGeneration` but before pushing simply pushes on the next tick;
generations are allowed to skip. A member that receives an out-of-order push rejects the lower
generation. The replica is never authority: nothing reads it except a promotion, and nothing
derives health from it (BUG-0158 stands — a promoted master re-collects fresh observations).

## Controller relay + UI

Presentation only, in `web/src/pool-members.tsx`: a per-member **Authority replica** column
showing generation and freshness, with `unknown` when the master is stale. No action.

## Tests

- Go (`internal/agent/pool_replica_test.go`, new): sign/verify round trip; wrap/unwrap with the
  real member key and failure with another member’s key; generation monotonicity; truncated
  section refusal; push retry idempotency; pruning keeps a VM referenced by published inventory;
  `-race` with concurrent authority writes and pushes.
- Contract: peer OpenAPI entry, inventory fields.
- Browser: the replica column, including its stale state.

## Docs

`docs/API.md` (peer section), `docs/api/peer-openapi.json`, `docs/api/openapi.json`;
new `docs/guide/pool-authority-and-recovery.md` (created here, completed by S5/S6);
`docs/changes/*.json`; platform plan “Member replica” status line.

## Lanes

- **C:** `internal/agent/pool_authority.go` (new), `pool_replica_test.go` (new),
  `internal/agent/peer.go` (one dispatch line), `run.go` (push tick + report),
  `internal/agent/pool_vm.go` (pruning), `internal/agent/pool_storage.go` (D4),
  `internal/protocol/pool.go`.
- **K:** projection fields in `internal/controller/managed.go` / `controller.go`.
- **U:** `web/src/pool-members.tsx`.
- **D:** API docs, contract test, guide page, change record.

---

# S5 — Planned master change and member eject

**Status: implemented in the working tree, native acceptance outstanding (2026-09-19).** Core,
Controller, UI and documentation lanes are complete and unstaged, together with defect 7 (both
halves: the new master refuses to remove an ISO it holds only as an adopted record, and the
Controller re-keys the pool's storage, network and media mirror rows to the new master host ID on a
reported master change). One durable, resumable Core state machine keyed by `operation_id`
implements both operations from a single command to the pool master, with every stage crash-
resumable on both sides and no Controller in the loop. Deviations from the plan as written, all
deliberate:

- **Step 5 carries the membership move in a small pool-CA-signed re-point notice, not a replica
  push.** A member cannot accept a replica from a host it does not yet consider its master
  (`AuthenticatePoolMaster`), so the signed notice moves the membership and the ordinary replica
  push follows immediately. For the same reason the peer transfer actions are
  `activate|abort|repoint|leave`, not the planned `prepare|activate|demote|leave`: there is no
  `demote` call, because the outgoing master demotes itself on the new master's signed re-point
  notice returned with the accepted activation — signed evidence rather than its own assumption.
- **The authority probe is `GET` (and `POST`), read-only, and doubles as a pull-based re-point
  offer.** A master answering a ready member that is behind returns a signed notice, which is what
  makes "catch up later from whichever host you can reach, without trusting a stale master" work as
  a pull. Only a signature at a strictly higher generation changes anything.
- **An ejected row is marked `ejected`, not deleted.** `AuthenticatePoolPeer` already requires
  `ready`, so the certificate stops being admitted either way, and keeping the row is what lets the
  pool tell a returning host *why* it is refused.
- **Storage reservation releases are never frozen.** Every other durable registry write on the
  master is refused while the freeze is held, through one guard in the managed worker plus three
  inside `pool_vm.go`'s own `poolVMMu` sections and one in `acceptPeerJoin`; freezing a release
  could strand a member's stop, so releases are deliberately outside it. Running guests are never
  touched and stop is never refused.
- **D3's recommended default was taken**: an ejected host re-initializes as the master of a new
  single-host pool named after its hostname with a new pool ID, keeping its host ID, its local
  storage and its local VMs, and the Controller adopts that pool from inventory under the same
  site. Eject is refused while the host still owns any pool VM record or holds a pool-shared
  storage reservation; both are Core's decision and arrive on the operation, not as a route `409`.
- **The adopted-ISO decision**: a demoted master keeps serving the ISO records it adopted from the
  old pool's shared storage, and the new master refuses to remove such a record with a message
  telling the operator the file stays on shared storage and must be removed from the host that
  imported it. A pool-wide "re-home the importer" operation would be new product behaviour and was
  not invented here.
- **The master now reports its own authority generation** (`managed.authority`), which S4 recorded
  as deliberately absent. That gives the member-replica `behind` ladder the absolute reference it
  lacked; the peer comparison stays, because the master's record can itself be the older report.
- `internal/vmhost/managed_media_adoption.go` (new, 13 lines) was added outside the Core lane's
  write set because `mediaAdopted` is unexported and defect 7's refusal must be decided from the
  journal.
- **Defect, found in review and fixed (`BUG-0331`, 2026-09-19):** `cancelPoolTransfer` gated only
  on a terminal stage and on the target having activated — the master-change rule — so a cancel was
  accepted for an eject at stages `remove`, `repoint` and `leave`, *after* the membership without
  that member had been written at the next generation. It set `stage: aborted` without restoring
  the membership and stopped the re-point/leave loop, so the ejected host might never be told to
  leave. One predicate, `poolTransferCancellable`, now both gates the cancel and is reported as
  `protocol.PoolTransferStatus.cancellable`, so the reported flag and the accepted cancel cannot
  drift: a master change is cancellable until the signed hand-off, an eject until the membership
  write, a promotion never. A cancelled eject also recorded the *master change* sentence and now
  says `the pool member eject was cancelled by an operator`. The UI reads the reported flag instead
  of a hard-coded stage list.
- **Core now reports `cancellable`** on `PoolTransferStatus` and `PoolPromotionStatus`, always
  present, which is an additive change to the S5 contract made by the S6 sweep.

Verified by in-process multi-host Go `-race` tests over the real mutual-TLS peer transport with
real certificates and signatures, Controller lab tests, the UI component suite and
`tests/test_api_contract.py` (`PoolTransferContractTests`). **Not** verified: any native multi-host
master change or eject, a host power-cycled during one, and any browser review of the transfer
panel, the master-conflict banner or either confirmation dialog — all S7 work.

**API impact: new contract** — peer `POST /api/v1/pool/transfer`, `POST /api/v1/pool/authority`;
Controller `POST /api/v1/pools/{id}/master`, `DELETE /api/v1/pools/{id}/members/{host_id}`;
inventory `managed.transfer`. **Guide impact: updated.** **State owner: core** — record: the
transfer record and frozen flag in each participant’s `pool-peer.json`, plus the replica from S4.

## Core behavior

One state machine implements both operations, following the platform plan’s six steps exactly.

`pool-peer.json` gains `Transfer *PoolTransferRecord{operation_id, kind: promote|eject,
target_host_id, stage, authority_generation, started_at, updated_at, fence_proof, error}` and
`AuthorityFrozen bool`. A single new guard `requirePoolAuthorityWritable(s)` is called by every
authority write already identified in S4; while frozen it refuses with
`pool authority is frozen for a master change; retry when it completes`. Running guests are
unaffected: power actions that do not change ownership, and the stop asymmetry at
`pool_vm.go:912-918`, stay permitted.

Stages, for `kind: promote` with a cooperating master:
1. `freeze` — master sets `AuthorityFrozen`, persists the transfer record.
2. `replicate` — master pushes the current replica (S4) to the target and requires an
   acknowledgement at exactly the current generation.
3. `fence` — skipped when the old master is cooperating. (Used by S6.)
4. `activate` — the target is told, over `POST /api/v1/pool/transfer` with
   `action:"activate"`, to unwrap the pool CA key, adopt the replica as its own durable
   authority at `generation + 1`, set itself `MasterHostID`, and sign the new authority.
5. `repoint` — the new master pushes the new signed authority to every member
   (`POST /api/v1/pool/replica`) and each member durably updates `MasterURL` and
   `Membership.MasterHostID`, accepting **only** a strictly higher generation.
6. `demote` — the old master is told `action:"demote"`, drops its CA key material, keeps its
   certificate and membership row, and becomes an ordinary member. If it is offline it is
   refused by the generation check when it returns (S6’s rejoin path).

For `kind: eject` the same machine runs with the master staying master: freeze, remove the
target’s membership row at generation+1, push to the remaining members, and tell the ejected
host to leave. An ejected host re-initializes as the master of a **new** single-host pool with a
new pool ID named after its hostname, keeping its host ID, its local storage and its local VMs
(`initPoolMaster`, `peer.go:34-47`); the Controller adopts the new pool from inventory. Eject is
refused while the target still owns any pool VM record, while it holds pool-shared storage
reservations, or while its membership status is not `ready` and it is reachable — the master
names the blocking guests, from `pool-vms.json`.

Defect 7 is owned here: on a reported master change the Controller re-keys the mirror rows for
pool storage, networks and media to the new master host ID so they can be refreshed and
tombstoned again (mirror-only, no new state), and the new master refuses a removal of an ISO it
holds only as an *adopted* record with a message telling the operator the file stays on shared
storage, instead of silently dropping the record.

## Host API

- **Peer `POST /api/v1/pool/transfer`** (master→target and new-master→old-master):
  `{operation_id, action: prepare|activate|demote|leave, authority_generation, target_host_id,
  fence_proof?}`. `AuthenticatePoolMaster` for the master-issued actions; an `activate` is
  accepted only when the caller is the recorded current master at the recorded generation, or
  (S6) when the request carries a verified fence proof. Bounded body, 20 s deadline, refusals
  `409` with sanitized text.
- **Peer `POST /api/v1/pool/authority`** (any peer → any peer, `AuthenticatePoolPeer`): returns
  `{host_id, role, authority_generation, replica_generation, replica_verified, frozen,
  observed_at}`. Read-only. It is the probe S6’s promotion proof depends on, and also what a
  returning old master uses to discover it has been superseded.
- **Controller REST:**
  - `POST /api/v1/pools/{id}/master` `{request_id, host_id, confirmation}` — planned master
    change. Relayed as `protocol.Command{Kind:"pool-transfer"}` to the **current master**.
  - `DELETE /api/v1/pools/{id}/members/{host_id}` `{request_id, confirmation}` — eject, relayed
    to the master the same way.
  - Both: admin session, typed-hostname confirmation, `202`, `409` for capability, freeze
    already in progress, blocking guests, stale master, or a target that is not a ready member
    with a current replica.
- **Inventory:** `ManagedState.Transfer *PoolTransferStatus{operation_id, kind, target_host_id,
  stage, status, error, observed_at}` from every participant, so the Controller can show one
  honest picture assembled from several hosts rather than a Controller-held job.
- **Outage:** the Controller is a relay. If it disappears mid-transfer, Core finishes or fails
  the transfer on its own and reports on reconnect. There is no Controller-side job or timer.
- **Versions:** `pool-authority-transfer-v1` required on the master, the target and every ready
  member; a member without it blocks the transfer with an upgrade-required message, because a
  member that cannot re-point would keep following the old master.

## Interruption windows and safety

| Interrupted at | Recovery rule |
|---|---|
| `freeze` | Frozen flag is durable; the master resumes or the operator cancels, which clears the flag. Authority writes are refused meanwhile, never silently queued. |
| `replicate` | Retry; the target acknowledges the same generation idempotently. |
| `activate`, before the target persisted | Old master is still master at generation N; the target has only a replica. Retry is safe. |
| `activate`, after the target persisted, before the master learned | Two hosts believe they are master — resolved by generation: the target is at N+1, the old master at N. Every member and every peer call accepts only the **highest** generation, and the old master’s first `POST /api/v1/pool/authority` probe or rejected replica push demotes it. No authority write can succeed at N once a member holds N+1. |
| `repoint`, partially delivered | Members at N keep following the old master until they receive N+1; the new master retries each tick. A member at N cannot be granted placement by the old master because the old master is frozen and, after demotion, refuses authority writes. |
| `demote` never delivered | Handled by S6’s returning-master path. |

Safety properties to prove in S7: never two hosts accepting authority writes at the same
generation; VM ownership, epochs and identities preserved byte-for-byte across the transfer; no
permit issued by a demoted master (permits are epoch-bound, `storage_guard.go:122`); pool ID,
resource IDs and names unchanged.

## Controller relay + UI

New `web/src/pool-authority.tsx`, mounted on the pool detail page. **Change pool master** and
**Eject member** actions with strongly worded confirmations that list what is frozen, what is not
(running guests keep running) and what blocks the operation. Live stage display assembled from
every participant’s reported `transfer` status, with `unknown` for any host that is stale.

## Tests

- Go (`internal/agent/pool_transfer_test.go`, new): the full six-step machine for promote and
  eject; freeze guard against every authority write; crash-and-resume at each stage; a member at
  N refusing N-1 and accepting N+1; eject refusal with guests; an ejected host becoming its own
  master with a new pool ID and its host ID unchanged; `-race`.
- Go (`internal/controller/`): route authorization, confirmation, `409` matrix, mirror re-key on
  a reported master change.
- Contract and browser as in earlier slices.

## Docs

`docs/API.md`, `docs/api/openapi.json` + `peer-openapi.json`,
`docs/guide/pool-authority-and-recovery.md` (master change and eject sections),
`docs/guide/join-a-pool.md` cross-links, `docs/changes/*.json`, platform plan “Transfer steps”
status, pool master model “Absence-as-removal across a master change” resolution.

## Lanes

- **C:** `internal/agent/pool_authority.go` (transfer machine; same file as S4 — sequenced after
  it), `pool_transfer_test.go`, `internal/agent/peer.go` dispatch lines,
  `internal/agent/managed.go` branch, `internal/protocol/pool.go`.
- **K:** `internal/controller/poolauthority.go` (new), `controller.go` routes,
  `managed.go` mirror re-key.
- **U:** `web/src/pool-authority.tsx` (new) + test, serialized `main.tsx` insertion.
- **D:** API/guide/change record/contract test.

---

# S6 — Fence-verified manual promotion and safe rejoin

**Status: implemented in the working tree, native acceptance outstanding (2026-09-19).** Core,
Controller, UI and documentation lanes are complete and unstaged. Change record:
`docs/changes/2026-09-19-pool-promotion-rejoin.json`. Deviations from the plan as written, all
deliberate:

- **No new peer route and no `activate` action carrying a fence proof.** The candidate is both
  driver and target, so the activation is an in-process call into the machinery S5's `activate`
  already shares. A self-signed hand-off notice would have added a signature that proves nothing:
  the proof is the fence. `internal/agent/peer.go` was not touched.
- **Two new command kinds, `pool-promote` and `pool-fence-profile`,** rather than overloading
  `pool-transfer`, because their routing targets differ — the promotion goes to the *candidate*,
  which is the one pool command besides `pool-join` not addressed to the master, and the fence
  profile goes to the master, which owns the membership record it lives in.
- **`PoolMember.PromotionReadiness` was not implemented.** It would be computed by the master,
  which in this scenario is by definition gone. The candidate-side `promotion-preflight` verdict
  replaces it and is strictly more informative; inventory carries `managed.promotion` and
  `managed.restricted` instead of `managed.promotion_readiness`.
- **The verdict is reported structurally, not inferred from check names.** `can_promote` is true
  only when the whole proof holds *including* the fence, so it is false in the normal ready state;
  `ready_to_fence` is the flag a caller offers the promotion on, beside `fence_recorded`,
  `fence_usable` and a bounded `fence_unusable_reason`.
- **The boot rule the plan did not define, made evidence-based.** A restarted master cannot know it
  was fenced, so it writes `Restricted{unconfirmed}` *before any start path is exposed* if and only
  if it is the master of its own durable membership, that membership has at least one other `ready`
  member, **and** it is still fence-promotable by somebody — a usable fence profile for itself is
  recorded now, or the durable `SelfFence{ClearedAt}` record says one was and some ready member may
  still hold a replica carrying it. It clears only when *every* other ready member answers naming it
  master at no higher generation. A pool that never opted in — today, every physical pool — restarts
  its master unrestricted, which is what keeps the cost of this rule proportionate. A single-host
  pool is never restricted.
- **Unverifiable fence profiles are refused at record time**, not merely at promotion time. That
  closes a version-skew dual-writer window: a profile in a membership therefore always came from a
  build that can verify it, and any build that can verify it also restricts on it. One predicate,
  `vmhost.PoolFenceUsable`, answers both "may this be recorded" and "could I have been promoted
  away", and `verifyFence` calls the same one, so they cannot diverge.
- **The fence credential travels, sealed per member, inside the replica** (`BUG-0332`). The host
  that fences is the *candidate*, and it cannot be given the material after the master it must
  fence is gone. It is sealed with the same primitive as the pool CA key under its own context,
  covered by the signature and excluded from the content digest; only
  `PoolMember.fence_credential_version` is replicated in the clear, so rotated material advances the
  generation. A profile cannot be recorded until its material has reached the master. **The
  owner-visible consequence** — any ready member can open what is addressed to it, so any ready
  member can power off any profiled host — is stated in the guide; it is the same trust model the
  sealed pool CA key already carries and is policy, not a lock.
- **Restricted hosts refuse *all* workload starts**, not only shared-storage ones, following the
  owner's words. The libvirt hook stays shared-only so local recovery access is never blocked.
- **A reachable owner that cannot read its own VM inventory blocks the promotion**, and a reachable
  host that names a master other than the old one blocks it. Ambiguity blocks. `vms_reported` was
  added to the authority probe so "I define nothing" is never confused with "I cannot tell".
- **Epoch is not cross-checked in accepted ownership**: the execution epoch is the master's number
  and an owner has never held it, so it could only be repeated back. Owner, presence and
  `spec.revision` are compared instead — a narrowing of check 3 as the plan wrote it.
- **The promotion verdict is deliberately not durable.** It is an observation with a lifetime and is
  recomputed, never stored, on Core or in the Controller.
- **The proposed dead-member escape was rejected as unsafe** and the deadlock is pinned by a test;
  see owner decision OD-5 below.

**Initial implementation verification limit (2026-09-19):** the implementation tests injected a
double where Core calls a fence, so those tests alone could not establish real fence behavior.
Subsequent scoped native evidence is recorded in the current acceptance report; it does not make
the remaining reboot, ownership-conflict, restricted-host or browser-review cases complete. S7
must not claim the OD-5 case as passed.

**API impact: new contract** — Controller `POST /api/v1/pools/{id}/master/promote/preflight`,
`POST /api/v1/pools/{id}/master/promote`, `POST /api/v1/pools/{id}/members/{host_id}/fence-profile`;
Core command kinds `pool-promote` (to the candidate) and `pool-fence-profile` (to the master); the
brokered pool-request action `promotion-preflight`; an additive extension of the existing peer
`GET /api/v1/pool/authority` (`restricted` always, `vms` and `vms_reported` with `?vms=1`) and **no**
new peer route; inventory `managed.promotion` and `managed.restricted`; health checks
`pool-restricted` and `pool-promotion`; `PoolPromotion*`, `PoolWrappedFenceCredential`,
`PoolRestrictedStatus.kind`, `PoolMember.fence`, `PoolMember.fence_credential_version`,
`PoolAuthorityReplica.fence_credentials`, `PoolTransferStatus.cancellable`, `PoolResult.promotion`;
and the members-projection additions `master_seen_at`, `promotion`, `last_promotion`, per-member
`transfer_supported`, `fence` and `restricted`, seven fixed `checks` entries and the corrected
member-own freshness rule. No new capability string: `pool-authority-transfer-v1` gates all of it.
**Guide impact: updated.** **State owner: core** — record: the candidate's `pool-replica.json`
promoted into its own `pool-peer.json` authority; the fence proof in that host's transfer record,
digest-bound to pool, excluded host, operation and generation and valid for fifteen minutes;
`Restricted{kind, reason, since, conflicts}` and `SelfFence{cleared_at}` in each host's own
`pool-peer.json`, written before any start path is exposed; the fence credential material sealed per
member in `PoolAuthorityReplica.fence_credentials`, with only a private `FenceCredentials{ref,
digest, version}` map on the master; and `PoolMember.fence` replicated with the membership, so a
promoted master inherits every profile and a fresh Controller re-adopts them from reported inventory.

**Automatic master succession and quorum election are explicitly out of scope.** They remain the
platform plan’s separately gated *Future automatic-succession gate*. Nothing here elects a
master; an operator selects one and Core refuses unless the proofs below hold. A two-host pool
never auto-elects, by design, and this slice does not change that.

## The replica-freshness and accepted-ownership proof (defined before implementation)

The platform plan requires this proof to be defined first. It is:

**Inputs.** The candidate’s `pool-replica.json`; a live `POST /api/v1/pool/authority` probe of
every host in the replica’s membership; the candidate’s own fresh local observations; and, if the
old master is unreachable, a fence proof.

**Checks, all of which must pass:**

1. **Replica integrity.** Signature verifies against the pool CA the candidate holds; `pool_id`
   matches; every declared section digest in `sections[]` matches the stored section; the
   membership names the candidate as `ready`. A missing or unverifiable replica blocks
   promotion. Fencing cannot substitute for it.
2. **Replica supremacy.** The candidate probes every reachable host. If any reachable host
   reports an `authority_generation` or `replica_generation` **higher** than the candidate’s, the
   promotion is refused and names that host as the better candidate.

   **Implemented stricter than this text, and PENDING OWNER CONFIRMATION (2026-09-19).** As
   written above, "unreachable hosts do not veto, but they force check 4" permits two masters, and
   an independent review reproduced exactly that: with the master dead and the two surviving ready
   members partitioned from each other, promoting each in turn made both of them pool master of the
   same pool (`BUG-0333`). An unreachable ready member that is not the old master holds this pool's
   authority replica, so it could be promoted in precisely the same way; its silence is evidence of
   nothing. The implemented rule is therefore: **an unreachable ready non-master member blocks the
   promotion unless this same promotion fences it**, in which case it joins `fence_host_ids` and
   check 4 covers it too; a blocking host with no usable fence profile refuses the promotion
   outright. A reachable host already running a non-terminal promotion of this pool also blocks, so
   two concurrent reachable candidates leave exactly one master.

   This is stricter than the text it replaces: it can refuse a promotion the original wording would
   have allowed, and it can power off a host the original wording would not have. **Approved (OD-6,
   project owner, 2026-09-20):** fail-closed; silence is never agreement. The API reference, the
   guide and the interface describe this as the product rule.
3. **Accepted ownership.** For every VM record in the replica’s `pool-vms.json`:
   - the owning host is reachable and its freshly published inventory (`pool_vm.go:632-666`)
     agrees with the replica on `host_id`, `epoch` and `spec.revision` → `confirmed`; or
   - the owning host is unreachable **and** fenced with a verified proof (check 4) → `fenced`,
     and the VM is recorded as *visible but not startable* until its disks are validated; or
   - the record has a `pending_update` that no reachable owner’s journal evidence can resolve →
     `blocked`. A `blocked` record does not prevent the promotion when its owner is fenced, but
     the VM stays refused for start and is shown as needing reconciliation, exactly as
     `pool_vm.go:774-838` already refuses every action while `pending_update` is set.
   Any `confirmed` mismatch — a reachable owner disagreeing with the replica — blocks the
   promotion outright. That is ambiguous authority, and the plan says ambiguity blocks.
4. **Old-master exclusion.** If the old master answers the authority probe and is cooperative,
   S5’s planned transfer is used and no fence is required. Otherwise a fence proof is required:
   `Status == "passed"`, `ExclusionVerified == true`, produced by an actual native fence and an
   independent power-off observation (`vmhost.VerifyStorageClusterFence`’s existing discipline,
   `internal/vmhost/storage_fence.go:43-86`), observed within 15 minutes, and bound to the
   promotion by target ID and the candidate’s authority digest — the same binding rule
   `StorageClusterConfig.FencingVerified()` already applies (`protocol/cluster.go:162-177`). A
   timeout, a heartbeat loss, a successful fence *request* or an operator assurance is never
   proof.

**What the operator sees before confirming:** the candidate and its replica generation; a row per
host with its reported generation, freshness and reachability; a row per VM with `confirmed`,
`fenced`, or `blocked: <reason>`; the fence target and method for the old master, with an
explicit destructive power-off confirmation; and every blocking reason in Core’s own words. After
promotion, pool health is `unknown` until fresh member observations arrive — never derived from
the replica (`member_access` is memory-only, BUG-0158).

## Core behavior

- **Fence profile per host.** Reuse `protocol.StorageClusterFence` (the closed, typed profile at
  `protocol/cluster.go:26-35`) as `PoolMember.Fence`, stored in the master’s membership and
  replicated, with credentials delivered through the existing Core-private fence-credential
  handoff (`internal/agent/storage_credentials.go:100-130`,
  `<state-dir>/storage-cluster/fence-credentials/`). Extract the middle of
  `VerifyStorageClusterFence` into a shared `verifyFence(ctx, stateDir, fence) (status, error)`
  and add `vmhost.VerifyPoolHostFence(ctx, stateDir, fence, hostID, authorityDigest)` on top —
  one shared implementation, two callers. See owner decision D2 for the hardware limit.
- **Promotion.** `POST /api/v1/pool/transfer action:"activate"` with the fence proof, driven on
  the candidate itself by a durable record keyed to the operation ID, running the checks above,
  then S5 steps 4–6 (`activate` at generation+1, `repoint` every reachable member, and refuse the
  old master by generation when it returns).
- **Safe rejoin of the returning old master.** At start, and on every failed authority write, a
  host that finds a valid higher-generation authority naming another master demotes itself
  automatically — identity, certificate and membership row unchanged — and sets a durable
  `Restricted{reason, since}` flag in its own `pool-peer.json`. While restricted, Core (not the
  Controller, and independent of any Controller connection) refuses: every VM start and define
  path, the execution permit hook (`internal/vmhost/storage_guard.go:131-174` — no permit is
  issued, so a direct `virsh start` or a libvirt autostart is refused with the existing message),
  pool autostart (`ReconcilePoolVMAutostart`), and any storage write to a pool-shared repository
  it no longer owns. Recovery access and local diagnostics remain. The flag clears only when the
  host has re-pointed to the current master, received a current replica, published a VM inventory
  the master accepts, and had every VM ownership conflict resolved by the master. A conflict — a
  VM the current master says lives elsewhere — leaves the host restricted and reports it; Core
  never starts a second copy.

## Host API

*As implemented (2026-09-19); the sketch below was written before the slice and is corrected in
place. The reference of record is
[`docs/API.md`](../API.md#fence-verified-manual-promotion-and-safe-rejoin-post-apiv1poolsidmasterpromote-post-apiv1poolsidmembershost_idfence-profile).*

- **Controller REST:** `POST /api/v1/pools/{id}/master/promote/preflight` `{host_id}` — one
  brokered report round trip to the candidate that stores, journals and audits nothing, answering
  the whole `PoolPromotionVerdict`. `POST /api/v1/pools/{id}/master/promote`
  `{request_id, host_id, confirmation, fence_confirmed}` — relayed to the **candidate**, not the
  old master. The typed confirmation is the **pool's** name, not the host's, matching the S5 master
  change; `fence_confirmed` is the separate explicit acknowledgement that the old master will be
  powered off. `202`, `200` on an exact replay, `409` on a changed payload. Core's own refusals are
  **not** the route's `409`: they arrive asynchronously on the operation and are surfaced verbatim
  in `last_promotion`.
- `POST /api/v1/pools/{id}/members/{host_id}/fence-profile` — set or clear a host’s fence
  profile, relayed to the master as a `PoolCommand`; credentials go through the existing
  credential handoff, never through this body.
- **Members projection:** `promotion_readiness` per member was **not** implemented — the master
  computes it and the master is what is gone. `GET /api/v1/pools/{id}/members` instead gains
  `master_seen_at`, the pool-level `promotion` (the candidate's own report) and `last_promotion`,
  and per member `transfer_supported`, `fence` and `restricted`, with three checks appended to the
  fixed list and the corrected member-own freshness rule.
- **Inventory:** `ManagedState.Restricted *PoolRestrictedStatus{kind, reason, since, conflicts[]}`
  and `ManagedState.Promotion *PoolPromotionStatus{operation_id, stage, status, error, verdict,
  cancellable}`.
- **Versions:** `pool-authority-transfer-v1`, unchanged — S6 adds no capability string.

## Interruption windows and safety

| Interrupted | Outcome |
|---|---|
| After the fence, before `activate` | Old master is off; the candidate still holds only a replica. Retry with the same operation ID re-uses the stored proof while it is inside its freshness window; an expired proof forces a new fence. |
| During `activate` | The candidate either persisted N+1 or did not; the retry is idempotent by operation ID. No member follows N+1 until it is pushed. |
| Old master returns mid-promotion | Its authority writes fail the generation check; it demotes and restricts itself. It cannot issue a permit, so no second writer exists even before it notices. |
| Candidate dies after promoting | It resumes as master from its own durable authority at start. Members at N re-point when it pushes. |
| Fence succeeded but the old master was, in fact, still running a VM | Impossible to distinguish by assertion, which is why the proof requires an independent observed power-off; S7 exercises a fence failure and requires the promotion to be refused. |

## Controller relay + UI

`web/src/pool-authority.tsx` (S5’s file) gains the promotion dialog described in *what the
operator sees*, and the host detail page shows a prominent **Restricted** banner with Core’s
reason and conflicts. No control in the UI can bypass a failed check; refusals are displayed, not
retried automatically.

## Tests

- Go (`internal/agent/pool_promote_test.go`, new): each proof check failing in isolation and
  blocking; supremacy refusal naming the better candidate; ownership mismatch blocking; fence
  proof freshness, digest binding and target binding; promotion with a fenced unreachable owner
  marking VMs visible-but-not-startable; restricted flag enforcement on every start path;
  automatic demotion on discovering a higher generation; conflict leaving the host restricted;
  `-race`.
- Go (`internal/vmhost/`): `verifyFence` extraction keeps the existing clustered-storage
  behavior (the existing `storage_fence` tests must pass unchanged) and
  `VerifyPoolHostFence` refuses an unsupported or unconfigured profile.
- Contract, browser (promotion dialog with every blocking reason, restricted banner).

## Docs

`docs/API.md`, both OpenAPI documents, `docs/guide/pool-authority-and-recovery.md` (promotion,
fence profiles, restricted hosts, what an operator does when promotion is refused),
`docs/changes/*.json`, platform plan’s *Initial manual-recovery gate* status, and an explicit
statement in the guide and the plan that automatic succession is not available.

## Lanes

- **C:** `internal/agent/pool_promote.go` (new), `pool_promote_test.go`,
  `internal/vmhost/storage_fence.go` (extraction) + `internal/vmhost/pool_fence.go` (new),
  `internal/agent/pool_vm.go` (restricted refusals), `internal/vmhost/storage_guard.go`
  (restricted refusal), `internal/protocol/pool.go`.
- **K:** `internal/controller/poolauthority.go`, `controller.go` routes.
- **U:** `web/src/pool-authority.tsx`, `web/src/pool-members.tsx`.
- **D:** API/guide/change record/contract test.

---

# S7 — Native acceptance, guide walkthroughs, QA and status

**API impact: none** — reason: acceptance, documentation and status work; it exercises the
contracts S1–S6 implement and adds no operation. Any defect it finds is fixed in the owning
slice’s lane with its own API assessment. **Guide impact: updated** (verification sections).
**State owner: none** — reason: no durable record is added; lab artefacts stay untracked under
`.cache/phase4-acceptance/`.

## Lab

Extend the existing two-host lab at `.cache/phase4-acceptance/lab` (core1 master, core2 member,
Controller on a loopback port, shared L2 segment, NFS exports, binaries built from this
worktree). Add a third host for the multi-host promotion cases; `fence_virsh` against the lab
hypervisor is the only fence profile that produces a real proof today (owner decision D2). Reuse
`tests/integration/storage_repositories_lab_boot.py` to stamp out host 3,
`tests/integration/cluster_ha_lab.py`’s lab construction and `ids.json` shape,
`tests/integration/controller_replacement.py` for adoption assertions, and
`tests/integration/storage_repositories_native.py`’s outage fixture. Every address in tracked
output is a reserved example address; raw captures stay untracked.

## Matrix

**Phase 4 Gate.** Join a disposable member from the Controller; prove the master reconciles every
applicable VLAN and storage registration with per-member success and actionable failure and no
Controller-held desired state; exercise an incompatible uplink, an unwritable export, interrupted
Core-to-Core delivery, member reboot/reconnect and Controller replacement; retry converges with
no duplicate bridge, mount, credential, registration or writer; VMs place on either host;
inaccessible storage prevents placement; shared free space is reported once; a disconnected host
never triggers a duplicate start.

**Initial manual-recovery gate.** Planned handover on a healthy pool; operator-selected,
fence-verified promotion in a two-host and a three-host pool; the returning old master rejoining
only after reconciliation; interruption at each of the six transfer steps; stale-host boot and
autostart refused; conflicting VM ownership refused. Prove throughout: no dual master, no
duplicate writer, unchanged pool and resource identities, preserved VM ownership and epochs,
unavailable disks reported without losing VM visibility, no health reported from a replica alone,
and fresh-Controller adoption of every pool setting.

**Open master-owned placement items** (from the [placement plan](2026-09-18-master-vm-placement.md)):
Controller replacement, native storage identity loss, membership change during placement, and
interrupted multi-host definition recovery.

**S2/S3 natives.** Maintenance mode blocking placement and start with guests still running and
surviving a Controller replacement; a management-network change that loses connectivity rolling
back on its own deadline with the Controller stopped, and again after a forced power cycle; and
**Roll back now** on an applied change actually restoring the previous configuration. The
`TestManagementNetworkNetns` path has never run; that historical test limit is distinct from the
later retained native C8 operator-rollback result.

**Defect re-runs.** Five of the eight defects in `docs/BUG_LEDGER.md` `BUG-0325`–`BUG-0332` were
found by the lane-A lab run against the *old* build and have only in-process evidence against their
fixes. S7 re-runs each on the new build: discarding a refused network and storage registration;
a placement against a member whose shared access is stale carrying a warning and no refusal;
re-adding a member to a replacement Controller answering a retryable conflict; a member whose VLAN
apply failed converging within one pull pass after the uplink is fixed, with its reported
observation time behaving as documented; and the packaged host's `ifupdown`, `bridge-utils` and
`vlan` dependencies confirmed with `apt-cache show` and `dpkg -L` on a Debian 13 guest, which no
machine used so far could do.

**What S7 must not claim.** The OD-5 case (a restricted master with a permanently dead member) has
no recovery and must not be presented as having one. Nothing may be recorded as verified that was
not executed. A test-double fence result alone never establishes fence proof; only retained native
evidence for the relevant operation can do so, and partial real-fence evidence still leaves the
manual-recovery gate open.

## Deliverables

A tracked, sanitized harness under `tests/integration/` for anything reusable; the QA record
`docs/qa/2026-09-19-phase4-native-acceptance.md` separating native, simulated and unexecuted
coverage; `docs/BUG_LEDGER.md` entries plus a regression check in **both**
`.github/workflows/` and `.gitea/workflows/` for every defect found (verify the existing Go and
Python suites are already run by both before adding a workflow change); guide verification
sections updated on every page S1–S6 touched; status updates in this plan, the
[platform plan](2026-09-08-nex-platform.md) Phase 4 section, the
[handoff](2026-09-14-production-readiness-handoff.md), the
[pool master model](2026-09-09-pool-master-model.md) and the
[VLAN plan](2026-09-09-pool-vlan-networks.md); `docs/changes/*.json`; and the gates
`python3 scripts/check_docs.py --worktree`, the privacy unittest, and
`go test -race` on every touched package with real recorded output.

A phase is complete only when the functionality, the safety and recovery checks, the
documentation and the UI review all pass. Anything unexercised is recorded as outstanding, not
waived.

## Lanes

Single lane (**L**) with one executor on the lab at a time, plus **D** for the tracked QA and
status files. No Core, Controller or UI writer runs against the same tree while a lab run is in
flight, except to fix a defect the run found — sequenced, one writer per file.

---

## Decisions for the project owner

**All six are decided. D1–D4 were taken as their recommended defaults on 2026-09-19 and are
implemented as written below; OD-5 was raised during S6 and answered by the project owner on
2026-09-19; OD-6 was answered on 2026-09-20.** They are kept here as the record of what was
chosen and why.

**D1 — Join invitation binding.** *Decision: recommended default taken (project owner, 2026-09-19), implemented in S1.*  The Controller-driven join needs a credential the joining host
redeems by itself. Today’s host-local flow binds the offer to the member’s CSR hash at issue,
which needs three ordered calls and would make the Controller drive the exchange.
*Recommended default:* a single-use, 15-minute invitation bound to the pool, the master and the
**member host ID**, with the CSR bound at first redemption. It is the same trust model as an
enrollment token, it travels only over the authenticated host channel, and the existing replay
rule (the same invitation always returns the same certificate) is preserved. *Alternative:* keep
CSR-first binding and accept a Controller-orchestrated three-step dance — rejected here because it
breaks the invariant.

**D2 — Fence profiles and the hardware limit for promotion.** *Decision: recommended default taken (project owner, 2026-09-19), implemented in S6, and enforced more strictly than proposed: Core now refuses to **record** a profile it cannot verify, not only to promote with one, which closes a version-skew window where a later build could promote over a profile this one calls unusable. Only `virsh` profiles can be recorded, so a physical pool cannot be fence-promoted; the limit is reported structurally as `fence_recorded`/`fence_usable` and is stated in the guide, the reference and the handoff.*  Promotion with an unreachable
master requires a verified fence proof. The existing implementation supports `fence_virsh`
(disposable-lab Core VMs) and explicitly refuses Redfish and IPMI because *BMC fencing identity
verification is not implemented* (`internal/vmhost/storage_fence.go:60-63`), and refuses SCSI
persistent reservations. *Recommended default:* reuse the existing closed fence profile and
credential handoff per host, ship manual promotion with the honest limit that a physical pool
cannot be fence-promoted until BMC identity verification is implemented, and record that as
outstanding in the plan, the guide and the handoff. *Alternative:* implement BMC identity
verification inside Phase 4 — a separate slice of real size, which would push the phase out.

**D3 — What an ejected host becomes.** *Decision: recommended default taken (project owner, 2026-09-19), implemented in S5.*  *Recommended default:* it re-initializes as the master of
a new single-host pool named after its hostname with a new pool ID, keeping its host ID, its
local storage and its local VMs; the Controller adopts that pool from inventory. Eject is refused
while the host still owns any pool VM record or pool-shared reservation. *Alternative:* leave the
ejected host inert until an operator re-enrolls it — rejected here because a Nex host is always
the master of its own pool.

**D4 — The storage peer list’s fail-open asymmetry.** *Decision: recommended default taken (project owner, 2026-09-19), implemented in S4: all four peer sites now fail closed with `503 pool resource list unavailable: managed journal is incomplete`.*  Recorded as an open owner decision in the
[pool master model](2026-09-09-pool-master-model.md): `internal/agent/pool_storage.go` discards
`AppliedStorage`’s `managed-journal` check, so a master with an unreadable journal entry can
serve a short repository list while the network and media lists fail closed with `503`.
*Recommended default:* make it fail closed like the others, in S4. The cost is that a damaged
journal stops member storage convergence until the master is repaired; the benefit is that a
member can never detach a repository because the master could not read its own journal — and once
that list also feeds a replica, fail-open is indefensible.

**OD-5 — Recovery for an opted-in pool whose master restarts while a member is permanently dead.**
Raised during S6. With a usable fence profile recorded for itself, a restarted multi-host master
restricts itself until *every* other ready member confirms it; if one member is permanently dead it
stays restricted, starting no shared-storage guest and writing no pool authority. The S6 lane
proposed an escape — fence the dead member, eject it, re-evaluate — and then rejected it as unsafe
with a concrete counterexample: B promotes itself while A is off, B dies, A boots, fences and ejects
B, clears its restriction and is master; B returns believing it is master at the same generation and
starts its guests. Two masters, two writers. A point-in-time observation that a host is off proves
it is not writing *now*, does not prove it never took the authority, and does not survive that host
powering on again, and generation cannot arbitrate because both minted N+1 from N. The deadlock is
pinned by `TestRestrictedMasterCannotEjectADeadMember`.

*Decision (project owner, 2026-09-19): the master **stays restricted**. There is no escape action,
and none may be added — no force, no override, no "declare host permanently lost", no timeout. The
documented recovery is to bring the dead host back or restore its Nex Core state. No route, field,
parameter, enum value or interface affordance may offer one, and **S7 must not claim this case**.*
The residual outage is bounded by the opt-in: a pool that never recorded a usable fence profile for
its master restarts it unrestricted and keeps starting guests with a dead member present.

**OD-6 — The stricter unreachable-ready-member promotion rule.** *Decision: recommended default
taken (project owner, 2026-09-20), implemented in S6.* The `replica-supremacy` check blocks a
promotion while a ready member is unreachable, unless that same promotion fences that member.
The plan's original check 2 said unreachable hosts do not veto; independent review demonstrated
that permits two masters. The implementation took the safe direction and is now the approved
product rule: powering off an unreachable but otherwise healthy member in order to promote another
is the default and only behaviour. Silence is never agreement; the rule stays fail-closed.

Everything else in this document is an implementation choice already made: transports, file
names and schemas, stage names, capability strings, crypto primitives, the ten-minute default
rollback window, lane partitioning and test placement.

---

# Independent review of S1–S6 (2026-09-19)

Two independent reviews were run against the finished, **unreleased** slices before any native
acceptance work. Neither reviewer wrote the code they reviewed.

| Review | Scope | Verdict as delivered |
|---|---|---|
| Core authority | S4–S6 on Nex Core: the authority freeze and hand-off, the replica ladder, promotion, the fence discipline, the restriction and boot rules | **Not safe to take into the native acceptance run as it stood.** Three findings reproduced: F1 (two partitioned survivors could both be promoted — a dual master), F2 (a hand-off to a destroyed target permanently killed a two-host pool), F3 (the pool's VM owner records moved during an authority freeze) |
| Controller and invariant | The Controller relays, S1–S3, and the architecture invariant | **The invariant holds**, with authentication, authorization, secret handling and injection clean; the Controller relay code keeps no stage, deadline, generation or change ID of its own. **Five blocking findings**: C-1 (adoption failed open), C-2 (confirm accepted an observation that proved nothing), C-3 (the management-network rollback used the wall clock), C-4 (apply and rollback destroyed the management bridge), C-8 (maintenance did not block a local-storage start), plus seven cheaper ones and nine plausible issues |

**Every blocking finding of both reviews is fixed**, each with a regression written and observed
failing first, each guard mutation-tested, and each recorded in `docs/BUG_LEDGER.md` as
`BUG-0333`–`BUG-0353`. Two further defects were found while fixing them (a promotion could have
fenced a reachable, healthy master; two hosts could run concurrent promotions of one pool), and one
cross-layer defect was found by the fix itself (the promote route could not drive a current Nex
Core). Two findings were **refuted with a pinning test rather than patched** — P-6 (`define` with
`Create=false` grants nothing) and P-8 (one process owns the agent state directory, enforced by an
exclusive `flock`, not by the unit file's `Conflicts=`).

The reviewers' own process note is worth keeping: three of the five blocking findings were invisible
to the green test suite, because nothing executed them. **Green was not proof here**, and S7's
native work is a prerequisite for these specific behaviours rather than a confirmation pass after
them.

## Known properties, assessed and not changed

These are consequences of the approved design, recorded so nobody re-discovers them as defects.

- **H1 — the sealed material is openable by any ready member.** The pool CA key and every fence
  credential are sealed *per member* inside that member's replica, so any ready member can open
  what is addressed to it, and therefore **any ready member can power off any host with a recorded
  fence profile**. The fence proof is a safety control against a partitioned honest host, never a
  security control against a member. It is policy, not a lock, and it argues against recording a
  fence profile for a host that does not need one. Stated in the guide and the API reference.
- **H3 — `SigningBytes` re-marshals the decoded struct.** Any future field added to
  `PoolAuthorityReplica` or `PoolAuthorityNotice` is therefore a hard cross-version break that the
  capability constants do not express. It fails closed — no promotion, no re-point — and the
  durable fix is to verify over the received bytes. Worth doing **before a second Nex Core version
  exists**.
- **H4 — one P-256 certificate key serves both ECDSA signing and static ECDH.** No practical attack
  here, but it is a key-separation deviation and is recorded as one.
- **H6 — the 1 MiB replica ceiling silently ends the ability to promote.** A pool whose authority
  outgrows `PoolReplicaLimit` reports the copy check as failed, honestly, but an operator has no
  lever to raise it. Stated in the guide.

## Named limits carried into S7 and beyond

None of these is a defect to be fixed quietly later; each is a stated limit that the reference, the
guide or both already carry.

**Closed (BUG-0387, 2026-09-20):** a drained **member** now refuses manual `virsh start` through
the StorageGuard hook reading verified `pool-replica.json`. Native lab evidence (2026-09-20, Core
binary sha256 327b7ec9…) confirms the hook maintenance sentence on a drained member. Guide and API
reference updated; this is no longer an open limit.

| Limit | Where it is stated |
|---|---|
| **OD-5** — an opted-in pool whose master restarts while a member is permanently dead stays restricted, with no escape action, and S7 must not claim the case | plan (above), `docs/API.md`, guide |
| **D2** — only `virsh` fence profiles can be recorded, so a physical pool cannot be fence-promoted by this build; unverifiable profiles are refused at record time | `docs/API.md`, guide |
| **The management-network rollback needs the agent running.** Both units are `Restart=always`, which covers a crash, an OOM kill and a clean exit; a unit deliberately stopped or masked between apply and deadline arms nothing | plan (S3), `docs/API.md`, guide |
| **A management-network spec that omits a NIC carrying guest VLANs is not refused** and leaves that raw device unconfigured at the next boot; "leave alone" versus "disabled" is an open product decision | plan (S3), `docs/API.md`, guide |
| **The 1 MiB replica ceiling** (H6 above) | guide |
| **The join command's journal entry on the host still contains the invitation.** `applyResponse` rejects a report on any byte difference between the journaled and a redelivered command, so scrubbing the host's copy alone would break reporting; it must land with a Controller-side change | plan (this section) |
| **OD-6** — `replica-supremacy` blocks promotion while a ready member is unreachable unless the same promotion fences it; fail-closed, silence is never agreement | plan (above), `docs/API.md`, guide |
