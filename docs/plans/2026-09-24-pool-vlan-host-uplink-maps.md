# Pool VLAN per-host uplink maps

Date: 2026-09-24. Status: **decided / not implemented — plan complete**. Owner: project owner.
Parent: [platform plan](2026-09-08-nex-platform.md) Phase 4 (join reconcile including
“VLAN/uplink mappings”); extends [pool VLAN networks](2026-09-09-pool-vlan-networks.md) and
[Phase 4 closure](2026-09-19-phase4-closure.md). Related: [join guide](../guide/join-a-pool.md),
[pool master model](2026-09-09-pool-master-model.md).

## Problem

When a host joins an existing pool as a member, pool guest VLANs fail to converge if the
member does not have a physical NIC or bond with the **same name** as the master’s uplink.
Typical lab symptom:

```text
Networks: failed · pool-network-reconciliation failed: uplink nex2 does not exist on this host
network vlan1: uplink nex2 does not exist on this host
```

Join itself succeeds today (`will not converge`); membership and authority copy look fine while
Networks stay failed. The member pulls the master’s VLAN registration and tries to materialize
a guest bridge on `nex2`. There is no interface named `nex2` on that host, so apply refuses.
Operators are told to rename NICs or re-register the pool VLAN — a band-aid.

That half-joined state is unacceptable. A member whose guest networks are not in sync with the
pool will keep hitting placement, migration, VM create, and later container-network failures.
**A host must not become a pool member until every pool VLAN is mapped to a valid local uplink
and network converge can succeed.** Nex is a distro hypervisor: Core names NICs, the pool
master owns desired state, and join onboarding finishes networking — with a mapping UI only
when auto-configuration cannot do it alone.

### What already shipped (not this fix)

| Work | What it did | What it did not do |
| --- | --- | --- |
| BUG-0317 | Failed `network-register` no longer permanently reserves the ID | Invent a different uplink |
| BUG-0328 | Precheck + retry so a *present* uplink converges without the hour wait | Change the name-equality model |
| Phase 4 S1 join preview | Surfaces missing identical uplink as `will not converge` | Block join; map a local NIC |

### Superseded assumption

[Pool VLAN networks](2026-09-09-pool-vlan-networks.md) decided (project owner, 2026-09-09) that
a pool network is `name + uplink NIC name + VLAN ID` on the model where every host’s Ethernet
ports carry the same names. **Superseded 2026-09-24:** pool VLANs keep a shared identity
(`id`, `name`, `vlan`) and a canonical uplink for the master’s own apply; each host has a
durable local uplink map. Identical `nexN` names remain the happy auto-configure path, not a
requirement.

Phase 4 already required reconciling “logical networks (**including VLAN/uplink mappings**)”;
only a single pool-wide `Uplink` string shipped. This plan is that mapping, end to end.

## Architecture invariant

**State owner: core (pool master).** Per-host uplink maps are pool-scoped desired state on the
master. Members pull desired state over Core-to-Core peer APIs and apply guest bridges
**locally**. The Controller authenticates operators, calls master APIs, and renders UI; it is
never the durable store, never queues maps for later enrollment, and never runs or relays
converge as a Controller job.

- **Record:** each pool VLAN `NetworkRegistration` on the master, including embedded
  `host_uplinks`, plus existing peer access reporting.
- **Recovery:** a fresh Controller re-adopts networks and maps from the master’s
  `Inventory.managed` / pool network APIs with the same network IDs and names. Operators do
  not re-enter maps. Workloads do not stop.
- **Replacement test:** delete the Controller, connect the pool master — every VLAN and every
  host’s uplink map comes back.

Design order: Core resolve/apply → durable map on master → peer API → join refusal + UI →
outage checks → guide.

## Decisions (project owner, 2026-09-24)

1. **Refuse join until networking can sync.** A candidate is not eligible to join while any
   pool VLAN lacks a durable, inventory-valid uplink map for that host. Preview and Core both
   refuse. Half-joined members with failed `pool-network-reconciliation` are not an allowed
   steady state for new joins.
2. **Auto-configure first; mapping UI only on failure.** When the candidate already has a NIC
   or bond named exactly like the pool VLAN’s canonical uplink, the master writes an identity
   map with no modal. The mapping modal appears **only** for VLANs that did not auto-map (or
   whose auto-mapped NIC fails validation).
3. **Embed `host_uplinks` on the VLAN registration.** One durable record, one adoption path.
   No sibling map file.
4. **No mixed-version compatibility track.** Platform is lab-only with fresh installs; ship the
   new model as the only model. Do not build identical-name fallbacks for old peers.
5. **Remap is a first-class UI**, writing maps to the **pool master** via Controller→master
   API. Member Core applies on pull. Controller does not apply bridges or hold map state.
6. **Auto-discovery in UI.** NIC pickers suggest likely uplinks (link-up physical/bond first);
   the operator always confirms non-identity choices. Identity auto-map needs no confirm.
7. **Local bridges stay `GuestBridge(local_uplink, vlan)`.** Placement must not require
   `member_access.bridge == network.source`.
8. **Canonical `Uplink` / `Source` on the registration** remain the master’s (registering
   host’s) uplink and bridge for display and master apply. Maps override materialization on
   every other host.
9. **Unmapped host never silently uses the master’s uplink name.** Refuse apply with an
   actionable error.
10. **Who writes maps:** only the pool master records `host_uplinks`. Controller and UI call
    the master. Member inventory informs pickers; the member does not own the map.
11. **Remap with zero local VMs** on that network: tear down the local guest bridge and
    re-apply under the new local uplink, same network ID, generation bump. **With local VMs
    attached:** refuse until detached.
12. **Delivery:** one complete implementation of this plan (Core, API, join gating, auto-map,
    mapping modal, post-join remap, placement fix, tests, guide, changelog). Internal work
    order below is sequencing inside that single delivery, not separately shippable products.

## Goals

- Join cannot complete while any pool VLAN is unmapped or would fail `CheckNetworkApply` on
  the candidate.
- Auto identity map when names match; mapping modal only otherwise.
- Member converge uses the host’s mapped local uplink and reports local bridge access.
- Placement and host-access treat the member as ready on successful local access, not master
  bridge-name equality.
- Post-join remap UI for hardware changes, still master-owned.
- Controller replacement recovers maps from the master with no re-entry.
- Safe retry, reboot, interrupted peer pull; no duplicate bridges or writers.

## Non-goals

- Requiring identical `nexN` names as the long-term model (happy path only).
- Controller-held maps or Controller-driven converge loops.
- Proving the switch trunks the VLAN (success = bridge + port observed).
- Management-network staged apply / timed recovery.
- Auto-renaming host NICs during join.
- Editing VLAN **tag** in place (remove-and-re-register).
- Mixed-version peer fallbacks.
- Bonding wizard as part of join (picker may select an existing bond).

## End-to-end join flow

```text
Enroll host (unchanged)
        │
        ▼
Add host to pool → load candidates + pool VLAN list
        │
        ▼
Auto-configure pass (Controller asks master; master writes maps)
  For each pool VLAN:
    if candidate inventory has NIC/bond named N.Uplink
      → durable identity map host_id → N.Uplink (no modal)
        │
        ▼
Any VLAN still without a valid map?
  yes → mapping modal (suggested NICs, operator confirms)
  no  → skip modal
        │
        ▼
Re-validate: every pool VLAN has map + CheckNetworkApply against inventory
  fail → refuses_join / Confirm disabled, show why
  ok   → Confirm enabled
        │
        ▼
POST join (Core refuses again if maps incomplete or invalid)
        │
        ▼
Member pulls resolved networks, applies guest bridges, POSTs access
        │
        ▼
Membership ready for placement only when network access is healthy
  (and other existing ready conditions). UI does not celebrate a “joined”
  member whose Networks strip is still failed.
```

**Chicken-and-egg:** maps are written on the **master** against the candidate’s already-known
`host_id` **before** join completes. Inventory for validation comes from the enrolled host’s
reports (already available to the Controller/master path used by join preview today). Guest
bridge creation still runs on the member after peer membership exists; join eligibility is
“maps complete and apply would succeed,” and ready-for-placement requires observed success.

## Data model

On `protocol.NetworkRegistration` (pool VLAN, `kind: vlan`), embed:

```text
host_uplinks: []HostUplinkMap
HostUplinkMap { host_id string, uplink string }
```

Rules:

- `uplink` must pass existing uplink name validation (`ValidUplink` / IFNAMSIZ / kind rules).
- At most one row per `host_id` per network.
- Master host may omit its row; resolve falls back to `registration.Uplink`.
- Generation advances when canonical VLAN fields change **or** when any `host_uplinks` row
  for a host is added/changed/removed in a way that requires that host to re-apply.
- Tombstone / remove network clears maps with the registration (existing removal path).

Resolve for host `H` and network `N`:

1. If map row for `H` exists → `local = row.uplink`.
2. Else if `H` is the current master host → `local = N.Uplink`.
3. Else → refuse (`uplink map required for this host`).
4. `Source_local = GuestBridge(local, N.VLAN)`; apply/journal that shape under `N.ID`.

## Core behavior

### Master

- Persist `host_uplinks` in the managed network journal with the registration.
- API to put/patch/clear a host’s uplink for a network ID (authz: pool operator).
- On put: if that host’s inventory is known, validate with the same rules as
  `CheckNetworkApply` would (NIC exists, physical/bond, not slave, etc.) → 409 with clear
  error when invalid.
- Join admission (`pool-member-join` / membership accept path): refuse if any pool-shared
  VLAN (`IsPoolSharedNetwork`) lacks a valid map for the joining `host_id` against current
  knowledge, or if validation would fail. Do not accept “join now, map later.”
- Peer GET list/get: return each network **resolved for the calling peer host** (`uplink` and
  `source` local to caller) and include canonical fields needed for display (`name`, `vlan`,
  `id`, `generation`, and canonical master uplink as separate fields if useful — e.g.
  `canonical_uplink` / keep raw registration alongside resolved). Implementing change picks
  one JSON shape and documents it in `docs/API.md`; apply must never see the master’s NIC
  name when a map exists.
- Access POST: unchanged shape; store observation (existing memory-only limit remains).
- When a map changes for a host with no local VMs using the network: next member pull tears
  down old local bridge (existing remove/orphan paths keyed by network ID) and applies the
  new local uplink. If local VMs use it: refuse the map change.

### Member

- Pull resolved registrations; `CheckNetworkApply` + `ensureGuestBridge` on **local** uplink.
- Journal host-local `Uplink`/`Source` under the pool network ID.
- Report `member_access` with local `bridge`, `uplink_present`, generation, error.
- Precheck refusal when map missing or NIC missing — journal nothing; retry every pull tick
  (BUG-0328 pattern). Should be rare for **new** joins because join refused without maps;
  still required for remap races and reboot windows.

### Placement / Controller usability

Replace `member_access.bridge == network.source` with: generation match, no error,
`uplink_present`, non-empty bridge. VM attach continues to resolve by `NetworkID` to the
host-local journaled source. Update Controller helpers and UI (`networkUsable` and equivalents).

### Join preview

| Result | When |
| --- | --- |
| `compatible` | Every pool VLAN has a durable map for this host and inventory validates apply |
| `refuses_join` | Any pool VLAN unmapped, mapped NIC missing/wrong kind, or other existing join blockers |
| `will_not_converge` | **Not used for uplink/VLAN gaps anymore.** Reserve for non-network cases that still allow join if any remain; network uplink issues are always `refuses_join` |

`eligible` is false if any row is `refuses_join` (existing rule). Confirm stays disabled.

Auto-configure is invoked when the dialog loads a candidate (and again after modal confirm)
so preview rows flip to `compatible` without a second navigation when identity maps succeed.

## Auto-configure algorithm

Run on the **master** (Controller triggers via map API or a dedicated “prepare join maps”
call that only writes identity rows — implementing change may fold this into put-maps):

```text
for each pool VLAN N:
  if map already exists for candidate and still validates → keep
  else if inventory has physical/bond named N.Uplink → write identity map
  else → leave unmapped (modal required)
```

Do **not** auto-write a different NIC name without operator confirmation (even if only one
NIC is free). Suggestions belong in the modal, not in silent maps.

## API impact: changed contract

Reason: registrations gain `host_uplinks`; peer GET is host-resolved; join refuses without
maps; placement readiness changes; join-candidates uplink rows become refuse conditions;
map put/patch endpoints (or equivalent) are added.

### Controller ↔ master (illustrative; finalize names in implementing change)

- `GET/POST /api/v1/networks` (and get-by-id): include `host_uplinks`.
- `PUT` or `PATCH /api/v1/networks/{id}/host-uplinks/{host_id}` body `{ "uplink": "nex1" }`
  — master durable write; 409 on invalid uplink for that host’s inventory; 409 if VMs on
  that host use the network when changing an existing map.
- `DELETE` same path — clear map (only when allowed; join will refuse until set again).
- Optional batch: `PUT /api/v1/networks/host-uplinks` for join dialog “write all maps”
  before join POST (reduces round-trips). Still master-owned.
- Join candidates: uplink checks → `refuses_join` when unmapped/invalid; include enough
  detail for the modal (`network_id`, `name`, `vlan`, `canonical_uplink`, suggested NICs
  optional).
- Join POST: Core refuses if maps incomplete/invalid (do not rely on UI alone).

### Peer (member ↔ master)

- `GET /api/v1/pool/networks`, `GET …/{id}`: resolved-for-caller uplink/source.
- `POST …/access`: unchanged.

### Capabilities

Advertise the new behavior as part of the pool/network stack the join path already checks.
No mixed-version fallback branch — both hosts in a lab are fresh. If a peer truly cannot
speak the new shape, join refuses with upgrade messaging (same spirit as missing join
capability today).

Update [docs/API.md](../API.md) and `docs/api/openapi.json` in the implementing change.
Contract tests cover map put, join refusal, resolved peer GET, placement without
`bridge == source`.

## UI / UX

Minimal copy — labels and status, no tutorial prose.

### Add host to pool

1. Select candidate (existing).
2. System runs auto-configure (identity maps).
3. If any pool VLAN still unmapped or invalid → **Uplink mapping** modal:
   - One row per unresolved VLAN: name, VLAN tag, canonical uplink on master.
   - Select local physical/bond from candidate inventory.
   - Suggestions: link-up NICs/bonds first, then others; never pre-commit without confirm.
   - Confirm writes maps to master, refreshes preview.
4. Preview shows all network rows `compatible` or `refuses_join`.
5. Confirm join only when eligible. On success, member converge should clear Networks; if
   access still fails (e.g. trunk down), show the failure on the member — remap or fix
   hardware; do not pretend join is fully healthy.

### Remap (post-join)

- Entry: Networks host-access cell, or host networking / members detail when uplink failed
  or operator chooses **Change uplink**.
- Same picker pattern; writes master map; member pull re-applies.
- Refuse in UI when Core returns in-use.

### Register network

- Still choose uplink on the registering host (master).
- Immediately write that host’s identity map row (or rely on resolve fallback for master).
- Other members: until remapped, they cannot have been joined under the new rules without
  maps; adding a **new** VLAN after members exist requires each member to get a map before
  that VLAN is considered usable on them — either auto identity or remap UI. New VLAN
  registration flow: after create, for each ready member auto identity map where possible;
  surface members that need remap (those members are already in the pool — this is remap,
  not join refusal).

## New VLAN after members exist

Join gating covers **join**. For a VLAN registered later:

1. Master applies on itself with chosen uplink.
2. Auto identity map every ready member that has that NIC name.
3. Members without that name: network access fails with “set uplink for this host”; remap
   UI required. Placement on those hosts stays blocked for that network until mapped and
   converged. (Do not eject members.)

## Migration (existing lab pools)

On first master code that understands `host_uplinks`:

- For every pool VLAN and every current member + master: if inventory contains
  `registration.Uplink`, write identity map.
- If missing: leave unmapped — that host’s network access stays failed until remap. (Labs
  reinstall often; no mixed-version story.)

No Controller DB migration.

## Safety and recovery

- Incomplete master journal → fail closed on peer list (no false removals).
- Missing map / bad NIC → refuse apply; no journal; retry each pull.
- Map change with local VMs on that network → refuse.
- Interrupted pull / reboot / Controller replace → resume from master maps; idempotent apply.
- `member_access` memory-only across master restart: existing limit; members re-report.
- Unreachable host ≠ VMs stopped; map changes do not move execution.
- Never create two bridges for one network ID on one host; orphans follow master ID list.

## Acceptance gates

1. **Refuse join without maps:** candidate lacking `nex2` while pool VLAN uses `nex2`, and
   operator dismisses modal without mapping → Confirm disabled / join refused.
2. **Auto identity:** candidate has `nex2` → joins with no modal; Networks healthy.
3. **Heterogeneous map:** candidate has `nex1` only → modal → map `nex1` → join → member
   creates `nex1vN` → access healthy → place/start VM on member on that network ID.
4. **Placement:** member ready without `bridge == master source`.
5. **Remap:** change member uplink with no VMs → re-apply; with VMs → refuse.
6. **New VLAN later:** auto identity on members that share the name; remap UI for others.
7. **Controller replacement:** maps reappear; no re-entry.
8. **Interrupted pull / reboot:** no duplicate bridges; idempotent re-apply.
9. **Regression:** BUG-0317 / BUG-0328 tests still pass; new unit/contract tests for resolve,
   join refusal, peer resolved GET, placement rule, map in-use refusal.
10. **Guide + QA:** `join-a-pool.md` and related pages updated; disposable two-host walkthrough
    recorded under `docs/qa/`.

## Docs / guide / changelog

**Guide impact: updated** in the implementing change:

- `docs/guide/join-a-pool.md` — refuse join until uplinks mapped; auto-configure; mapping
  modal; remove “same NIC names” as the primary requirement; troubleshooting retargeted.
- Register / networks pages as needed for remap and new-VLAN-on-members.
- `remove-pool-network-or-iso.md` only if single-host uplink change story changes.

**Changelog:** e.g. “Pool VLANs map per host at join; different NIC names are supported.”

**Change record:** `state.owner: core`; durable `NetworkRegistration.host_uplinks` on the
pool master; adoption via inventory / network APIs; API `changed contract`; guide `updated`.

## Implementation work order (single delivery)

Do in order inside one change set / session of work; do not release a UI mapper before
placement and join refusal are correct.

1. **Protocol + journal:** `host_uplinks` on `NetworkRegistration`; validate; generation
   rules; master persist/load.
2. **Resolve + peer GET:** host-resolved uplink/source; member apply uses local; access
   reports local bridge.
3. **Placement readiness:** drop `bridge == source` requirement in Controller and UI helpers.
4. **Map API:** put/patch/delete (and optional batch); inventory validation; in-use refusal.
5. **Join admission + preview:** auto identity prepare; uplink gaps → `refuses_join`; Core
   join refuse without maps.
6. **UI:** join mapping modal (suggestions); post-join remap; new-VLAN member attention.
7. **Migration pass:** identity maps for existing matching inventories.
8. **Tests:** unit + contract + UI render/harness as applicable.
9. **docs/API.md, openapi, guide, changelog, docs/changes, check_docs, privacy.**

### Primary files (expected touch set)

- `internal/protocol/managed.go`, `internal/protocol/pool.go` (types, validation, GuestBridge)
- `internal/agent/pool_network.go` (+ tests)
- `internal/vmhost/vlan.go` (apply unchanged aside from callers passing local uplink)
- `internal/controller/pooljoin.go`, `internal/controller/managed.go` (+ tests)
- `web/src/pool-members.tsx`, `web/src/managed.tsx` / network host-access, VM create usability
- `docs/API.md`, `docs/api/openapi.json`, `docs/guide/join-a-pool.md`, `CHANGELOG.md`

Exact paths may expand; this is the core blast radius.

## Out of scope follow-ons

- Automatic NIC rename to pool names.
- LLDP / switch-fabric trunk proof.
- Per-host VLAN tag overrides.
- Creating bonds during the mapping modal.
- Mixed-version gradual rollout tooling.
