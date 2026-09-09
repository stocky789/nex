# Pool master model correction

Date: 2026-09-09
Status: Implemented and verified on 2026-09-09 (simulated browser suites plus a real disposable lab; see [lab evidence](../qa/pool-master/lab/checks.md)). Uncommitted.

## Decision

Matt decided that Nex pools follow the XenServer/XCP-ng model:

- Administrators never create pools. There is no **Create pool** action and no manual
  pool-creation API.
- Every Nex Core host that enrolls becomes its own pool automatically. The pool is named after
  the host's hostname and that host is the pool's **pool master**.
- A pool has exactly one pool master; other hosts are **member hosts** (XenServer calls them
  slaves). The pool master controls the pool.
- Sites still group pools. Enrollment tokens are bound to a **site**, not a pool.
- Hierarchy wording everywhere: **Sites → Pools (one master host + members) → Hosts → VMs**.

## What changed

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
the master matches the XenServer/XCP-ng behavior operators already know, keeps the single-host
case trivial, and gives Phase 4 a defined starting point for joining members.

## Not in this change (planned, Phase 4)

Joining a second host to an existing pool as a member under its master (“Add host to pool”),
changing the pool master, and ejecting a member are
[Phase 4 multi-host pool work](2026-09-08-nex-platform.md#4-multi-host-pools-and-shared-storage).
Until then every enrolled host remains the master of its own pool. Do not present these as
available.

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
