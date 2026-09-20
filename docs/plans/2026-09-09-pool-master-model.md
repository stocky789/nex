# Pool master model correction

Date: 2026-09-09
Status: Implemented and verified on 2026-09-09 (simulated browser suites plus a real disposable lab; see [lab evidence](../qa/pool-master/lab/checks.md)). Uncommitted.

## Decision

project owner decided that Nex pools follow the established master-and-members model:

- Administrators never create pools. There is no **Create pool** action and no manual
  pool-creation API.
- Every Nex Core host that enrolls becomes its own pool automatically. The pool is named after
  the host's hostname and that host is the pool's **pool master**.
- A pool has exactly one pool master; other hosts are **member hosts**. The pool master
  controls the pool.
- Sites still group pools. Enrollment tokens are bound to a **site**, not a pool.
- Hierarchy wording everywhere: **Sites → Pools (one master host + members) → Hosts → VMs**.

## What changed

Decision (project owner, 2026-09-10): pool and host IDs and the pool name are generated and kept by Nex
Core — the host generates its own ID, the pool master generates the pool ID and holds the pool
name — and a Controller adopts them unchanged. The Controller rows below are a mirror of what the
host reports, not the origin of pool identity; a separate code slice implements this and documents
the enrollment contract in [docs/API.md](../API.md).

- Controller: the `pools` table records a master host; enrollment creates the pool and host in
  one transaction and records a `pool.create` audit row; enrollment tokens and appliance
  bootstrap carry a site instead of a pool; adoption derives the pool from the host record;
  existing databases migrate (pools with hosts gain the earliest enrolled host as master, and
  empty hand-created pools without dependants are deleted).
- Core local UI/service: the deploy request and handoff no longer carry a pool name.
- Controller web UI: **Create pool** is removed. A site without hosts shows **Add your first
  host** with an **Add host** button; **Add host** is enabled whenever a site is selected and its
  form is titled **Add host to** the site name. The pool **General** panel shows **Pool master**;
  hosts show a **Pool master** or **Member** role badge; the tree suffixes the master with
  **· master**.
- Documentation: hierarchy wording in [AGENTS.md](../../AGENTS.md), [PRODUCT.md](../../PRODUCT.md),
  [README.md](../../README.md) and the [platform plan](2026-09-08-nex-platform.md); guide pages
  for [Controller setup](../guide/manage-controller.md),
  [appliance deployment](../guide/deploy-controller-appliance.md) and
  [VM creation](../guide/create-virtual-machine.md).

## Why

Manual pool creation added a step with no decision behind it: a single host could not be
enrolled without first inventing a pool, and a pool-bound token forced the operator to choose
a grouping before the host existed. Naming the pool after its first host and making that host
the master matches the master-and-members behavior operators already expect, keeps the
single-host case trivial, and gives Phase 4 a defined starting point for joining members.

## Not in this change (planned, Phase 4)

**Current Phase 4 status (2026-09-20): closed for disposable virtual-lab scope.** S1–S6 are
implemented. Reviewed native evidence includes the repeated G1 join/eject lifecycle and
missing-uplink recovery/cleanup, plus S6-14 boot restriction, refusal, rejoin and post-clear
start/stop safety. The final `f714` S6-18 strict harness is partial/FAIL after a different safe
eject refusal and did not observe timed suppression; its older scoped pass remains historical.
OD-6 is decided (project owner, 2026-09-20): the strict unreachable-ready-member rule
(fence or fail-closed). An authentic supported owner-conflict fixture is unavailable, and physical,
workload/duplicate-writer and packaged-host evidence remain permanent C-class limits for this lab
class. Closure does not authorize a release or claim those permanent limits.

At the time of this change, joining a second host to an existing pool as a member under its
master (“Add host to pool”), changing the pool master, and ejecting a member were
[Phase 4 multi-host pool work](2026-09-08-nex-platform.md#4-multi-host-pools-and-shared-storage).
Core peer join and configuration convergence are now implemented with bounded evidence. The
Controller join UI is no longer absent: the pool's **Hosts** tab adds a host through the master's
single-use invitation and one durable command. Reviewed G1 lifecycle and missing-uplink
recovery/cleanup evidence now supplements its unit and contract checks; full acceptance remains
open. A **planned
master change and a member eject** are also implemented in the working tree (closure slice S5,
2026-09-19), offered per member row on the same **Hosts** tab, with in-process multi-host, Controller
and contract evidence plus scoped native/browser evidence; full recovery acceptance remains open. **Fence-verified manual
promotion of a surviving member while the pool master is gone** is implemented in the working tree
too (closure slice S6, 2026-09-19), with scoped native evidence of external `virsh` power-off.
Only a `virsh` fence profile can be recorded, so a physical pool therefore cannot be fence-promoted, and a
fence profile must be recorded **before** the master is lost. What remains unavailable, and must not
be presented as available, is **automatic succession**: nothing elects a master by itself, and
every promotion is an operator naming one candidate.

Member convergence was add-only when this was written (found in review, 2026-09-19) and is no
longer. Decision: project owner, 2026-09-19 — pool-wide changes received by the master propagate
to members. Implemented in the same review cycle: the pool master removes a guest VLAN network,
renames one, and removes an imported ISO as durable Core operations
(`network-remove`, `network-update`, `media-remove`, capability `pool-resource-removal-v1`), and
members converge on their existing pull pass. A changed VLAN definition now has its journal-update
path: a member applies a higher master generation one step at a time, so it never reports a
generation it did not apply.

Two implementation choices carry that design; they are engineering decisions, not product ones,
and both follow the shared-storage detach path rather than inventing a second mechanism:

- **Rename only.** `network-update` changes the name and nothing else. The bridge name is derived
  from the uplink and VLAN tag and appears in every VM's domain XML, so changing either is a
  removal plus a fresh registration with a new identity; the payload cannot even express a
  re-plumb, and a member whose journalled uplink or VLAN differs reports
  `pool network <id> changed uplink or VLAN; remove and re-add it`.
- **Absence-based removal.** Tombstones stay off the peer wire; a member infers a removal from the
  resource's absence from an authoritative master list, as it already does for shared storage.
  What makes that safe is the authoritative-list gate (HTTP 200, decodable body, unexpired list
  context, every entry belonging to this pool) — any failure returns before the orphan loop, and
  teardown is refused while a local VM uses the resource.

Still open after that work:

- No native two-host walkthrough of a removal or a rename. Unit, race and namespace tests and a
  stubbed-API browser check are the only evidence
  ([QA](../qa/2026-09-19-pool-removal-convergence.md)).
- **Ejected-member cleanup remains out of scope**, unchanged even now that an eject exists
  (closure slice S5): an ejected member loses membership, so its converge pass returns before any
  teardown and it keeps what it applied. It re-initializes as the master of its own new
  single-host pool and keeps the bridges and ISO records it had applied; the guide says so.
- An **older member** silently keeps a removed bridge or ISO until it is upgraded; the master
  cannot detect it. Documented in the guide, not engineered around.
- A master **restored from a backup older than the removal** lists the resource again, so members
  re-apply it. That is the same exposure class as shared-storage detach and is accepted with the
  same default; removal is always dependency-checked, so it cannot orphan a running VM's bridge.
- **Pool VM owner records on the master are never pruned** (`state.VMs` in `pool-vms.json` keeps a
  deleted VM's record). The pool-wide removal check deliberately reads each host's published
  inventory (`state.Inventory[host].VMs`, replaced whole on every publication and
  generation-gated) instead, so it is correct today, but the unpruned owner records are a latent
  issue that needs its own slice.
- **No per-host "removal in progress" view.** Once the master's tombstone deletes the row there is
  nothing to hang one on, as with storage detach; a member that cannot finish reports through its
  existing `pool-network-reconciliation` / `pool-media-reconciliation` health check.
- **Repairing a damaged managed journal is undocumented.** A host that cannot read its managed
  journal whole refuses every managed operation and, since 2026-09-19, answers its pool network and
  media peer lists with `503`; its `managed-journal` health check names the entry it could not
  read. What an operator then does on the host is manual and has no documented procedure; the guide
  says so plainly rather than improvising one. Open.
- **Absence-as-removal across a master change: resolved in closure slice S5 (2026-09-19).** Raised
  in independent review, 2026-09-19; both effects are now handled rather than discovered later.
  The new master refuses to remove an ISO it holds only as an *adopted* record, with a message
  saying the file stays on shared storage and must be removed from the host that imported it — an
  honest refusal instead of a silent leak. *Decision: recorded default, 2026-09-19* — a demoted
  master keeps serving the ISO records it adopted from the old pool's shared storage, and the
  importing host removes the file; a pool-wide "re-home the importer" operation would be new
  product behaviour and is not invented here. The Controller's mirror rows for pool storage,
  networks and media are re-keyed to the new master host ID when the hosts report a master change,
  so they can be refreshed and tombstoned again; the re-key moves no content, and a fresh
  Controller still re-adopts from the hosts' reports.
- **The managed journal has no pruning.** Raised in independent review, 2026-09-19. The hourly
  converge operation IDs bound a permanently stuck resource to at most 24 journal entries a day
  (from 8 640), and the read-only dry runs write none at all, but nothing bounds the total: at the
  10 000-entry retention cap every managed operation on that host fails with
  `managed journal full; manual recovery required` and its pool network and media peer lists answer
  `503` permanently. Retained-entry pruning or a capacity health check needs its own slice.
- **The storage peer list is asymmetric with networks and media.** Raised in independent review,
  2026-09-19. `pool_storage.go` still discards `AppliedStorage`'s `managed-journal` check, so the
  repository peer list can be served short while an unrelated journal file is unreadable, and a
  test encodes that behaviour. The residual risk is lower — a member's detach is non-destructive,
  is refused while the repository is in use, and re-attaches on the next healthy list — but it can
  stop member VMs starting on that storage until the master is repaired. Whether to make it
  fail closed like networks and media is a project-owner decision, not an implementation choice.
- **Pool VM owner records on the master are never pruned**, as noted above: `state.VMs` in
  `pool-vms.json` keeps a deleted VM's record. Nothing reads it for the removal check, which uses
  each host's published inventory, so it is correct today and remains a latent issue.

## API impact

**Changed contract.** Affected operations, documented in the [API reference](../API.md):

- `POST /api/v1/enrollment-tokens`: body takes `site_id` (optional `host_id` for re-enrollment)
  instead of a pool; unknown site rejected.
- `POST /api/v1/agent/enroll`: fresh enrollment creates the host's pool with the host as master;
  re-enrollment keeps the existing host and pool. The enrollment response is unchanged.
- `POST /api/v1/appliances/bootstrap`: takes `site_name`, returns `site_id`; no pool name.
- `POST /api/v1/appliances/adopt`: `pool_id` optional and derived from the host; a mismatch is
  rejected.
- `GET /api/v1/pools`: returns `master_host_id`. `POST /api/v1/pools` is removed.
- Core local deploy/handoff: `pool_name` removed from the request and handoff state.

Managed resources, media and VM creation keep their existing pool/host contract. Controller and
Core must be upgraded together; an older Core local service that still sends `pool_name` is
rejected by the new bootstrap contract. A Controller appliance handoff that was interrupted
before completion and resumed after the upgrade cannot continue: its pool-bound bootstrap
record and token are removed by the migration, so the deployment must be restarted from the
Core host. Completed handoffs are unaffected.

## Guide impact

**Updated.** Pages above document the automatic pool, the pool master, site-bound tokens and the
**Pool master unknown** recovery. Their Verification sections link to the
[pool master checks](../qa/pool-master/checks.md) and the [real lab run](../qa/pool-master/lab/checks.md).

## Acceptance

- [x] Go race tests, API contract/generation checks and UI typecheck/build pass.
- [x] Browser walkthrough: site empty state, **Add host** form, enrollment creating the pool,
      **Pool master** panel and role badges, re-enrollment, revoked-master **Unknown** state.
- [x] Appliance deployment without a pool name reaches ready, enrolls and adopts.
- [x] Migration from an old-schema database assigns masters and removes orphan empty pools.
- [x] Guide pages re-verified and evidence recorded under `docs/qa/pool-master/`.

**Lab observation, 2026-09-09:** a revoked host keeps its automatic pool so re-enrollment restores
the same identity and pool. Deleting a permanently removed host together with its empty pool is
not implemented and is tracked with the Phase 4 host removal/eject work.
