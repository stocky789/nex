# Controller console redesign

Date: 2026-09-23
Status: implemented and reviewed; review fixes landed; awaiting owner commit. Owner: project owner.
Design contract: [Controller console design guide](../design/controller-console/README.md) and its
[reference](../design/controller-console/reference.html).

**Goal:** Replace the Controller's desktop-console shell (toolbar, resource tree, detail tabs,
bottom task strip) with the approved console layout: a labelled icon rail, a scope/search/activity
top bar, dense filterable lists with slide-over panels, a pool overview dashboard and stacked pool
settings. Every element of the reference ships, and every existing workflow stays reachable.

## Decisions

- **Decision: project owner, 2026-09-23** — the console layout and styling in the reference is the
  default for all Controller UI work from now on. It supersedes the layout parts of the
  [UI modernization plan](2026-09-23-controller-ui-modernization.md) (toolbar, resource tree,
  detail tabs, task strip) and of the [desktop layout plan](2026-09-09-controller-desktop-layout.md).
  The modernization's token discipline, square status badges, honest-state copy, confirmation
  dialogs and console focus mode carry over unchanged.
- **Decision: project owner, 2026-09-23** — every element in the reference is delivered at minimum,
  including elements that are not in the earlier plans. Elements whose Nex Core capability belongs
  to a later phase are built in the UI now with the real layout, disabled controls and a one-line
  "available with …" state, and are connected when that phase lands. This is a recorded exception
  to the rule against UI scaffolding for future phases; it does not reorder or authorize any Core
  phase work. The affected elements are listed under [Phase-gated elements](#phase-gated-elements).
- **Decision: project owner, 2026-09-23** — VM tags are added now as a real Nex Core capability
  (below), not as a Controller-side label store.
- Owner review feedback that became design rules: no coloured side stripes on any element, and
  tight corners (2 px controls, 3 px surfaces). See the design guide's
  [rules](../design/controller-console/README.md#rules-that-keep-it-looking-designed).

## Architecture invariant check

The redesign is presentation over data the Controller already fetches from hosts: `sites`,
`pools`, `hosts`, `vms`, `storage`, `networks`, `media`, `operations`, `audit`, host and VM `stats`,
pool `members`, `updates`. Filtering, grouping, search, KPI totals, the placement map and the
attention list are computed in the browser from those reads each time; nothing is stored.

New persistence is limited to per-browser `localStorage` conveniences: theme override, filter
sidebar collapsed, chosen columns, last-seen event time (for the rail dot), collapsed Overview
widgets. None of it describes a pool resource.

VM tags describe a pool resource, so they live in the VM definition on the pool master and a fresh
Controller recovers them by the normal VM inventory adoption. They are not stored in the Controller
database.

## API impact

`changed contract` — VM tags only: an optional `tags` array (lowercase `[a-z0-9-]`, 1–32 chars
each, at most 16, de-duplicated) added to the VM definition, accepted by the existing
`PUT /api/v1/vms/{id}` update with its request-id retry and asynchronous operation semantics,
carried by the Controller-to-master VM update command, persisted in the master's VM record,
reported in VM inventory, and returned on `GET /api/v1/vms`. Older Nex Core masters that do not
report the field return no tags; the UI then shows the tag editor disabled with "Requires Nex Core
0.4.8 or later on the pool master". Validation errors are 400 with the offending tag named.

Everything else: `existing contract` — the UI reuses current reads and writes unchanged. Pool
utilisation lines are drawn from the existing per-host `stats` series; no pool-level stats
endpoint is added. The placement-limit line is drawn only when Nex Core already reports an
admission or placement limit; otherwise the chart says "No placement limit reported".

## Information architecture

Routes stay hash routes. Existing deep links keep working.

| Route | Screen | Replaces |
|---|---|---|
| `#/` | Overview for the selected pool (site-wide totals when "All pools" is chosen) | Workspace, site and pool General pages |
| `#/vms`, `#/vms/{id}` | VM list; `{id}` opens the VM slide-over | Pool "Virtual machines" tab, VM page tabs |
| `#/hosts`, `#/hosts/{id}` | Host list; `{id}` opens the host slide-over | Pool "Hosts" tab, host page tabs |
| `#/storage` | Storage section, ISO library section | Pool "Storage" and "ISO library" tabs |
| `#/networks` | Networks section | Pool "Networking" tab |
| `#/backups` | Backup repositories and runs (phase-gated) | new |
| `#/containers` | Container services (phase-gated) | new |
| `#/tasks` | Task table | Task history page and bottom task strip |
| `#/events` | Event table from audit plus host/storage/backup conditions | pool/host/VM "Activity" tabs (still shown per resource in panels) |
| `#/settings`, `#/settings/{section}` | Pool settings: Overview, Members, High availability, Backup repositories, Updates (`storage`, `iso` and `networks` redirect to `#/storage` / `#/networks`; see [Owner feedback round](#owner-feedback-round-2026-09-23)) | Pool members UI, Updates page |
| `#/console/{id}`, `#/host-console/{id}` | Full-screen console focus mode (unchanged) | — |
| `#/sites/{id}`, `#/pools/{id}` | Set the scope, then show Overview | old site/pool pages |

Sites: the top-bar scope switcher lists sites and pools, and holds **Create site** and **Add host**.
Host actions (Refresh inventory, Re-enroll, Revoke access, maintenance) move into the host panel's
quick actions and the Members section.

## Every element and its data

Source column: **now** = existing API; **tags** = the VM tags change above; **P5/P6/P7/P9/P10** =
built now with the phase-gated state, connected when that phase lands.

### Shell

| Element | Source |
|---|---|
| Logo cell, labelled rail with fill-only current state, Events warning dot | now (dot: audit/conditions newer than the browser's last-seen time) |
| Scope switcher: site dot + name, pool name + host count, menu with sites, pools, Create site, Add host | now |
| Global search with `/` shortcut across VMs, hosts, IPs, settings sections | now |
| Activity pill with pulse and "N tasks running", opening Tasks | now (`operations`) |
| Theme toggle (system / light / dark) and account menu with Sign out | now |

### Overview

| Element | Source |
|---|---|
| KPI tiles: Hosts (online/total, stale badge, per-host status bar), Virtual machines (stacked state bar), CPU, Memory, Storage (capacity bar and change label) | now |
| Pool utilisation chart: CPU, memory, storage I/O lines, 1H/1D/1W/1M, placement-limit line | now (per-host stats); limit line only when Core reports one |
| Needs attention list: stale hosts, failed tasks, storage faults, hot VMs, available updates, backup failures, untested restores | now; backup rows P6 |
| Placement map: host columns, role, memory bar, VM chips by memory, stale hatch | now |
| Recent tasks with live progress | now |
| Backups, last 14 nights, last run, last restore test | P6 |
| Resilience: survives loss of one host (P9), fencing state (now), shared storage health (now), master (now) | mixed |

### VMs

| Element | Source |
|---|---|
| Filter sidebar: search, Group by host, facets Status / Pool / Host / Guest OS / Tags with counts, Clear filters, Customize columns | now; Tags facet from tags |
| View tabs All / Running / Needs attention / Stopped with counts | now |
| Table: checkbox, status dot + name, state, pool, host link, IP, guest OS, vCPU, memory, CPU bar, tags | now; tags |
| Bulk bar: Start, Shut down (now); Snapshot (P5); Migrate (P7); Clear | mixed |
| Create VM (opens the existing wizard) | now |
| Panel header: icon, name, state badge, host · pool links, close | now |
| Quick actions: Console, Start, Shut down, Migrate (P7); the remaining power actions (reboot, force stop, force reboot) in a More menu | mixed |
| Tabs: Overview, Performance, Snapshots (P5), Configuration, Activity | mixed |
| Overview: stale warning block, console preview with Open console, properties (guest OS, IP, vCPU/memory, disks, networks, guest agent, last backup P6, firmware, tags), install media controls | now; tags; P6 |
| Performance: CPU, memory, disk, network sparkline rows, link to full Stats range view | now |
| Snapshots list, Take snapshot, Revert | P5 |
| Configuration: name, start on boot, HA restart priority (P9), placement preference (P9), tag editor (tags), Edit hardware (existing editor), Delete VM (existing confirmation) | mixed |

### Hosts

| Element | Source |
|---|---|
| Table: status dot + name, pool, role badge, status with stale age, IP, CPU bar, memory bar, VM count, Nex Core version + Update badge, uptime | now |
| Panel header with Online/Stale badge, role · pool · IP line | now |
| Tabs: Overview, Hardware, Performance, Networking, Storage, Console, Activity | now (existing host components move in) |
| Stale explanation block | now |
| Hardware card (model), NIC port tiles by speed with legend, Interfaces & VLANs button | now (`hosts/{id}/hardware`, inventory NICs, `hosts/{id}/network`) |
| Disk tiles with role, memory meter, VMs on this host, properties (IP, CPU, Core version, uptime, fencing) | now |
| Quick actions: Refresh inventory, Console, Maintenance, Re-enroll, Revoke access | now |

### Settings, Storage, Networks, Backups, Containers, Tasks, Events

| Element | Source |
|---|---|
| Settings secondary nav with search, pool/master footer line | now |
| Networks section: name, VLAN, uplink, hosts with access, VMs; Create, Manage | now |
| Storage section: name, type, scope, used bar, hosts with access, status; Add storage, Manage; ISO library section | now |
| Backup repositories section: name, target, retention, last run, last restore test; Create, Policies, Restore | P6 |
| Members section: host, role, status, Core version, joined; Join host, Change master, Eject, fencing | now |
| High availability section | P9 |
| Updates section | now |
| Containers page: stacks and services with the agreed colourful application icons | P10 |
| Tasks table: task, by, when, result, progress, errors, retry links | now |
| Events table: event, source, when, result | now |

### Phase-gated elements

Snapshots (P5): bulk Snapshot, panel Snapshots tab. Backups (P6): Overview backup tile and
attention rows, "last backup" property, Backups page, Backup repositories section. Migration (P7):
bulk and quick Migrate. HA (P9): survives-loss-of-one-host, HA restart priority, placement
preference, High availability section. Container services (P10): Containers page. Each shows the
real layout, disabled controls, and "Available with <capability> (Phase N)". None shows invented
numbers.

## Delivery slices

Agents never commit. One writer per file; the owner of each file is named. Every slice runs
`npm --prefix web run typecheck`, `npm --prefix web test` and `npm --prefix web run build`, and UI
slices inspect browser captures in both themes at 1440×900 and 390×844 against the reference.

1. **Foundation** — owns `web/src/tokens.css`, `web/src/tokens.test.mjs`, `web/src/style.css`,
   `web/src/main.tsx`, `web/src/ui.tsx`, `web/src/icons.tsx`, `web/src/dock.ts`. New token values
   (contrast re-checked by the test), shell (logo, rail, top bar, scope switcher, global search,
   activity pill, theme toggle, account menu), route table above, a reusable slide-over `Panel`, and
   one module per screen (`overview.tsx`, `vm-list.tsx`, `host-list.tsx`, `settings.tsx`) with its
   final props, first rendering the existing components so every workflow works at the end of this
   slice. Deletes the toolbar, resource tree, menubar remnants and task strip.
2. **Overview** — owns `overview.tsx`, `overview.css`.
3. **VMs** — owns `vm-list.tsx`, `vm-panel.tsx`, `vms.css`, `managed.tsx`, `vm-edit.tsx`,
   `vm-create.tsx`, `console.tsx`, `stats.tsx`, `stats.css`.
4. **Hosts** — owns `host-list.tsx`, `host-panel.tsx`, `hosts.css`, `host-network.tsx`, `zfs.tsx`.
5. **Settings and sections** — owns `settings.tsx`, `settings.css`, `storage.tsx`,
   `pool-members.tsx`, `pool-promotion.tsx`, `pool-authority.tsx`, `updates.ts`; Storage, Networks,
   Backups, Containers, Tasks and Events screens.
6. **VM tags (Core first)** — owns the Go, protocol, OpenAPI and `docs/API.md` changes for the
   contract above, with contract tests for validation, authorization, retry with the same
   request id, and an older-master response. Runs in parallel with slice 1.
7. **Guide and QA** — owns `docs/guide/*` updates for the new navigation, the browser review
   harness (`tests/integration/ui_review_browser.py`, its fixture) and the phase 1 lab browser
   assertions, `docs/qa/controller-console-redesign/`, the change records and the changelog entry.
8. **Independent review** of slices 1–7 against this plan, the design guide and AGENTS.md.

Slices 2–5 start after slice 1; slice 3 wires the tag editor and facet after slice 6 reports.

## Deviations from this plan

Recorded after the independent review (2026-09-23):

- **Tasks have no generic retry.** There is no retry API, so a failed task links to the place its
  action can be sent again (the VM, **Storage**, **Networks**, the ISO library or the host it ran
  on) instead of offering a retry button.
- **Storage KPI change is sampled in the browser** since the page opened, because no API reports
  storage-use history; its label says "since opened" and nothing is stored. (The Hosts and VMs
  sparklines were replaced by status bars in the feedback round.)
- **Port tiles are coloured 10 GbE or faster versus slower**, since link speed is reported only
  where the kernel knows it; an interface without a reported speed is shown with the slower ones.
- **Members table gains Joined**, from the new Core-owned `members[].joined_at`
  ([record](../changes/2026-09-23-member-joined-at.json)); API impact: changed contract, additive.
- **Version:** this batch ships as development product 0.4.8, not 0.4.5, because 0.4.5 was
  already tagged ([record](../changes/2026-09-23-version-0.4.8.json)).

## Owner feedback round, 2026-09-23

The owner reviewed the delivered console and asked for seven changes. All seven are presentation
over the same host reports; no API, Core behaviour or Controller state changes.

1. **Storage and Networks as operational views.** Storage shows Type, Scope, Capacity, Health,
   per-host access lights, Last check and actions, with the ISO library below; Networks shows
   Name, VLAN, Uplink, per-host access lights, VMs (linking to the VM list's new Network filter),
   Status and Actions.
2. **Settings trimmed to pool configuration**: Overview, Members, High availability, Backup
   repositories, Updates. The old storage, ISO and networks settings links redirect.
3. **Containers under VMs in the rail**, as a smaller sub-item directly below VMs (not indented).
4. **Collapsible Overview widgets** with a one-line summary when collapsed, remembered per browser.
5. **Chart hover and smoothing**: crosshair and tooltip with keyboard and touch, monotone
   smoothing, and averaged buckets for the longer ranges.
6. **Long notes behind info disclosures** ("About …") instead of paragraphs under each table;
   the VM guest tools block becomes a compact status grid with "Why?" expanders.
7. **Members compacted**: a pool status strip and one row per host with short status checks and
   a Details expander, replacing the wide Fencing, Authority copy, Maintenance and Convergence
   columns.

Follow-ups the owner asked for during the same round: 1D/1W/1M are averaged to about one point
per 14 px so the lines read smoothly; table row actions stack as one column of equal-width
buttons; the Overview KPI tiles show a per-host status bar (Hosts), a stacked state bar (VMs),
and for CPU, Memory and Storage a capacity bar with a header change label and no trend line.

- **Decision: project owner, 2026-09-23** — Storage and Networks are operational views on the
  rail; Settings holds pool configuration only.
- **Decision: project owner, 2026-09-23** — Containers is a nested rail item under VMs.
- **Decision: project owner, 2026-09-23** — Overview widgets are collapsible but not collapsed by
  default.
- **Decision: project owner, 2026-09-23** — the utilisation chart averages samples into buckets
  for 1D, 1W and 1M and says so in its tooltip.

API impact: none — the round reuses the existing reads (`storage` with `member_access`,
`networks`, `vms`, pool `members`, host `stats`). State owner: none — the collapsed widgets are a
per-browser `localStorage` convenience (`nex.overview.collapsed`) describing no pool resource.
The patterns are recorded in the [design guide](../design/controller-console/README.md#components);
checks are in the [QA report](../qa/controller-console-redesign/checks.md#feedback-round).

## Acceptance

- Every element in the tables above is present, with phase-gated elements in their honest state.
- Every workflow reachable before the change is reachable after it (create site, add/enroll host,
  revoke/re-enroll, create/edit/delete VM, power actions, media, console, stats, storage, networks,
  ISO library, pool members, master change, fencing, maintenance, system disk replacement, updates,
  tasks, audit).
- No horizontal page scroll at 390 px; keyboard reachable rail, tabs, panel and dialogs; Escape
  closes the panel; focus returns to the row.
- Stale/unknown handling unchanged in meaning: stale host → VMs Unknown, actions disabled with reason.
- No `border-left`/`border-right` accents, no radius above 3 px except round dots, no gradients
  except the stale hatch; tokens test passes.
- VM tags survive a Controller replacement in the lab (delete the Controller database, re-adopt the
  pool master, tags return).
- Guide pages updated and walked through in the browser; captures inspected; results recorded.

## Action refresh audit — 2026-09-24

The project owner requested a Controller-wide audit after observing actions whose results
needed a browser refresh. Keep existing action callbacks and inventory polling; repair the
independently fetched pool, management-network, clustered-storage and hardware views.
Read-only polling must follow late Core outcomes even when the first response is idle,
catch up after browser visibility/reconnection, preserve drafts and errors, and never
resubmit an operation or infer completion from acceptance. Host hardware also rereads after
the selected host's inventory-refresh task succeeds.

API impact: **none** — only browser read scheduling and presentation change; existing
inventory, operations, pool membership, management-network, hardware and clustered-storage
requests and mutation contracts are unchanged.
Controller continues to display Core reports; no command, authorization or result schema
changes. State owner: **none** — no new durable state. Guide impact: **updated** in
[Controller management](../guide/manage-controller.md).
The scoped audit, regression results and simulated-browser limits are recorded in
[refresh verification](../qa/2026-09-24-controller-action-refresh.md); no phase gate changes.
