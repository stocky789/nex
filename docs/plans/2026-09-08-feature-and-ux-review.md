# Nex platform: feature and interface review

Reviewed: 2026-09-08. This document records research and phased requirements;
the [Phase 1 development UI and its browser evidence](2026-09-08-controller-foundation.md)
are now available. Remaining planned surfaces, including the host-local UI, are
not implemented or visually verified by this research.

This supplements the [implementation plan](2026-09-08-nex-platform.md). It separates useful operator features from the minimum needed to get Nex working. Everything here is a Nex requirement or proposal; nothing here promises feature or licensing parity with anything else.

Every proposal here is subject to the [required API impact check](../../AGENTS.md#required-api-impact-check);
a screen alone does not complete a workflow.

**API impact: none** — this planning revision maps UX proposals to future phases; no implemented
client-facing or Controller/Core interface changes. The planned operation flows are assessed below;
their contracts and runnable checks must ship with their implementation.

## UX opportunity and phase fit

**Planning revision, 2026-09-08:** use the supplied AI feedback as a product hypothesis: make
virtualization management easier to understand and operate, with clear consequences and recovery.
Appearance opinions in that input are subjective feedback, not verified findings.
Most of this work belongs in **Nex Controller**; Nex Core supplies the trusted host capabilities.
Keep the authorized standalone ISO slice unchanged and retain Sites → Pools → Hosts → VMs.

The table places refinements of existing workflows in their owning phases. Items explicitly marked
candidate remain follow-on proposals, not new first-release commitments.

| Supplied idea | Fit and timing | Minimum useful result and acceptance |
|---|---|---|
| Cleaner dashboard / cluster health | Pool overview, Phases 1–2; storage/protection in 4/6; HA in 9 | Prioritize hosts needing attention, fresh capacity, active/failed tasks, and next actions. Link to the affected object; show unknown/stale separately. Add backup and quorum information only when supported; no invented aggregate health score. |
| Responsive/mobile UI | Shell and each workflow from Phases 1–2 | Navigation, status, tasks, forms, and confirmations remain usable on a narrow touch screen and with a keyboard. Review at 360px and desktop widths; contain wide tables/console without overflowing the page. No native mobile app required. |
| Better VM creation wizard | Phase 2 | Use the staged creation flow below, preselect scope, explain CPU/RAM/disk/network choices, preserve inputs on back/error, and show capacity/compatibility blockers before submission. Reach the created VM and console through one durable job, including after refresh. |
| Visual networking | Read-only relationships in Phase 2; guided editing in Phase 4 | Show VM NIC → logical network → host bridge/VLAN → physical uplink where observed, with an accessible text/table equivalent. Start with a simple relationship view, not a freeform canvas. Phase 4 previews affected VMs and management access and tests staged apply/timed recovery. |
| Storage setup in plain English | Local selection in Phase 2; NFS registration in Phase 4 | Explain which hosts can use it, where data lives, capacity, and what host/storage loss means. Distinguish registration from formatting and shared access from independent backup; surface failed per-host access checks. |
| Sane homelab defaults | Phases 1–2, then each feature | Make a single-host pool normal; use the supported Linux/VirtIO defaults and existing guest network, with visible editable resource choices validated against host capacity. No default shared password, automatic disk erase, implicit management-network change, or unsupported HA promise. |
| Global search / command palette | Local list/tree search early; permission-scoped global search and palette are follow-on candidates | Search authorized objects across sites/pools as inventory grows. Start a palette with navigation and opening existing workflows; never bypass validation or confirmation. Test keyboard focus and forbidden-object leakage; no separate command execution engine. |
| Readable task/activity history | Durable records in Phase 1, object/recent views in 2, filters/notifications in 8 | Show actor, target, action, time, current step, outcome, and useful failure/next action. Global and object views use the same record; keep unknown outcomes visible and reconcile before retry. |
| Drag/drop ISO and template management | ISO upload in Phase 2; templates follow reliable VM creation | Drag/drop and an accessible file chooser use the same upload flow with explicit destination, size/free-space validation, progress, and interrupted-upload cleanup. Incomplete media cannot be selected. Templates/clones remain a separate follow-on capability with unique guest identity and safe in-use deletion rules. |
| Easy GPU/PCI passthrough | Agreed follow-on; excluded from initial release | Follow the [NVIDIA/AMD hardware passthrough gate](2026-09-08-nex-platform.md#guided-hardware-passthrough): guided discovery and assignment after validated isolation, exclusive ownership, reset/recovery, and protection of host-critical devices. Explain migration/backup limitations and verify on selected disposable hardware. |
| One-click backup policies | Phase 6, after backup/restore proof | Offer a short preset-based setup with explicit VM scope, independent repository, schedule/time zone, retention, and downtime/consistency review. Applying an already reviewed policy can be quick; do not guess the destination or promise an RPO. Show next run, missed/failed jobs, and last verified restore separately from backup success. |
| Simple cluster joining | Host enrollment in Phase 1 (each enrolled host becomes master of its own automatic pool), installer integration in 3, member join in 4 | Phase 4 “Add host to pool” joins an existing pool as a member under its master: select the pool, verify controller and host identity, show preflight and durable progress, and reconcile reconnect/retry without duplicate membership. Enrollment grants neither shared-storage access nor HA automatically. |
| Integrated dark mode | Theme-ready shell in Phases 1–2; optional dark-theme candidate after shell review | Use shared semantic surface/text/status tokens; retain approved light/blue branding as default. If delivered, provide an explicit light/dark/system preference and review forms, tables, dialogs, focus, and status contrast in both themes. Preserve colorful application icons in Phase 10. |

### Planned API flow assessment

Phase 1 APIs now exist; the [API reference](../API.md) identifies implemented routes
and schemas. Reuse those contracts where sufficient; references below to later
phases describe planned additions, not available endpoints. Presentation changes
alone need no extra endpoint.

| UX caller | Controller operation → Core command/observation → result | Planned contract treatment |
|---|---|---|
| Overview, tasks, responsive layout, themes | Read authorized inventory/health/operations → authenticated host observations and command results → timestamped status/task records | Phase 1 new contracts underpin these views; layout/themes reuse them without host commands. |
| Add host | Enroll into selected pool → establish scoped host identity and report inventory → membership/connection status | Phase 1 enrollment contract, integrated in 3/4; preserve token expiry, revocation, and duplicate/retry checks. |
| Host-local setup and recovery | Browser → local Core authorization/operation → agent/libvirt result; then Controller bootstrap/enrollment/adoption and reconnect reconciliation | Phase 3 new host-local API boundary; reuse Phase 1 enrollment and Phase 2 VM/console operations, extend handoff and local audit provenance without competing ownership. |
| VM wizard, ISO upload, defaults | Validate placement and media; create VM → validated host-side media handling and libvirt creation → durable result and observed VM state | Phase 2 new contracts; drag/drop reuses upload. Validate inputs at API boundaries; interrupted media must not become available. Template cloning extends this only in its follow-on slice. |
| Storage explanations and network view/editor | Read/register storage and networks; preflight/apply changes → per-host access/topology checks and staged network configuration → observed access and apply/recovery result | Phases 2/4 new contracts; include observed links needed for the view. Phase 4 owns network rollback, including controller loss during apply. |
| Backup policy shortcut | Validate/save policy and schedule jobs → existing backup/restore commands of Phase 6 → job, manifest, and verification records | Phase 6 new contracts; presets reuse policy operations, with missed-run and conflict behavior intact. |
| Global search / palette candidate | Query permission-scoped controller inventory or open an authorized workflow → no separate Core command → scoped objects or normal operation record | Assess a client search contract when implemented; reuse existing list operations if sufficient. Palette mutations use ordinary authorized operations. |
| Agreed passthrough follow-on | Discover/reserve/attach/detach device → capability/isolation checks and libvirt/device changes → reconciled ownership and VM configuration | Planned new contract in the hardware passthrough gate, reusing earlier inventory/VM operations; includes version/capability gating and failure/reconnect recovery on both sides. |

**Superseding decision, 2026-09-09:** pools are no longer selected or created by operators (each enrolling host becomes master of its own automatically created pool, tokens bind to a site), and **Add host** is implemented as address + root password with fingerprint confirmation pushed from the Controller, with the manual token as fallback; see [simple host enrollment](2026-09-09-simple-host-enrollment.md) and [pool master model](2026-09-09-pool-master-model.md). The Phase 4 “Add host to pool” member-join workflow above still stands.

Every operation-bearing slice retains API authentication/authorization, validation/errors, durable
results, safe retries and outage reconciliation, and Controller/Core compatibility checks from the
platform gate. Validate the UX through actual enroll → create → inspect failure → recover tasks,
recording completion, confusing choices, and errors; no claim of being easier than another product
without comparative user evidence.

## Established management patterns and the Nex decision

| Established pattern | Nex decision |
|---|---|
| VM hardware defaults, protection, scoped access, templates, migration checks, integrated backup/restore | Include safe lifecycle and clear task feedback early; add repeatable provisioning after the basic VM workflow |
| Dedicated backup verification, retention, and storage maintenance | Make recovery health visible; do not build a new deduplication engine for the first release |
| Pool membership, host capabilities, shared-storage access, maintenance | Keep the pool central to administration, while retaining Nex Controller's own authority model |
| Central operations, templates, backup policies, restore, controller/pool metadata backup | Make protection and recovery first-class workflows, with durable progress and clean-controller recovery |
| Consistent object navigation, tasks/events, compatibility checks, templates, maintenance and lifecycle management | Use context-preserving object pages and explain blocked actions before starting a job |

A Nex pool is a management grouping of hosts; it is not a scheduling or quota grouping, and it does
not imply shared storage. Snapshots alone do not imply a complete VM backup service.

## Feature priorities

### First: make one host usable

- Login, site creation, host enrollment (the first enrolled host becomes master of its automatic pool), observed host capabilities and freshness.
- VM create/install, ISO selection, console, power controls, disk persistence, and clear pending changes.
- Pool-owned local storage and explicit mapping to an existing guest network.
- Useful resource tables, task history, failure reasons, and deletion protection.
- No speculative dashboard, template pipeline, live network editor, HA system, or Ceph dependency.

### Then: complete the agreed platform

- Installable Nex Core ISO, a second host, and shared pool NFS storage.
- Full snapshot lifecycle, independent full backups, scheduling, retention, and guided restoration.
- Visible last backup/verification, controller configuration recovery, and a test restore onto another host.
- Live migration, compatibility preflight, host maintenance, evacuation, and tested upgrades.
- Scoped roles, useful notifications, and basic search/filtering before the first supported release.

### Follow-on candidates

| Candidate | Why it helps | Minimum boundary |
|---|---|---|
| Templates, full clones, cloud-init | Repeatable VM provisioning | Unique guest identity and credentials; linked clones later |
| Image import/export | Practical adoption from existing workloads | qcow2/raw first; explicit hardware mapping and isolated boot verification |
| Tags, saved filters, bulk operations | Manage more VMs without more navigation | Authorization and results per object; partial failure is visible |
| Guest tools and boot order | Better IP/status visibility and orderly host recovery | Capability detection; never conflict with HA ownership |
| Scoped API tokens and external login/MFA | Automation and delegated administration | Revocation, expiration, audit, and permission tests |
| Historical metrics and alerts | Diagnose degradation before outages | Bounded retention and meaningful thresholds, not unbounded telemetry |
| Automated restore drills and file restore | Faster proof and narrower recovery | Full-VM restoration works first; boot checks do not prove application health |
| Incremental/off-site protection | Smaller backup windows and better disaster recovery | Chain integrity, independent keys, retention dependencies, and source-loss tests |
| Rolling updates and placement rules | Predictable maintenance and workload distribution | Proven migration, spare capacity, and safe failure behavior |
| Ceph RBD | Additional shared-storage backend | Later integration; separate decision on operating Ceph itself |

These priorities are Nex recommendations. Their dependencies and acceptance gates live in the implementation plan.

## Interface direction: evidence and limits

The interface conventions this document assumes — a persistent infrastructure tree, scoped object
tabs, consistent tables with search and pagination, global and object task views, an alarms area,
and compatibility checks explained before a job starts — are long-established patterns in
infrastructure management interfaces, not observed Nex behavior.

No Nex interface was operated for this planning document. Every flow proposed below requires later
prototyping and actual browser QA before it is treated as available.

## Proposed Nex Controller UX

**Superseding layout decision, 2026-09-09:** project owner requested a Controller GUI
overhaul to a dense Windows desktop application shell. The
[desktop layout slice](2026-09-09-controller-desktop-layout.md) now owns the shell:
compact Windows-like menu/toolbar, left inventory tree, selected-resource header
and tabs, dense properties/tables, and bottom activity/status. The workflow
requirements below still apply, but the shell proposal below is no
longer the visual target. Preserve Nex branding, Sites → Pools → Hosts → VMs,
existing capabilities and the Core-local UI. **API impact: existing contract** —
presentation/navigation reuse current session, inventory, enrollment, host actions,
operations and audit without new agent commands. **Guide impact: updated** with
the implemented navigation and actual browser evidence; this direction does not
advance phase acceptance or establish QA.

### Audience and direction

Design for an infrastructure operator installing a host, finding a VM, investigating a failure, or recovering data. Desktop operation comes first, with usable narrow-screen status and recovery access. Favor legible tables, predictable controls, persistent context, and clear consequences. Follow the dense Windows desktop application layout described in the desktop layout slice while retaining Nex branding and capability boundaries.

The primary brand direction retains blue accents, white/platinum/grey surfaces, the graphite Nex wordmark, and white n on a blue icon. The [brand guide](../brand/README.md) adds supporting reference colors and an optional dark gradient without replacing that original identity. Keep a readable system sans and text/icons alongside status colors; use solid surfaces for dense operational content. UI token mapping, theme, and density still need review in the first design slice; brand approval is not a finished design system or implemented UI.

### Application layout

```text
Top: Nex Controller | compact menu row | icon action toolbar
Left: Sites → Pools → Hosts → VMs
Main: breadcrumb / object name / status / contextual actions
      scoped tabs
      table, details, console, or active workflow
Bottom: recent tasks / activity → complete history | connection/status bar
```

Use one canonical URL per object and preserve selected scope on refresh/back navigation. A VM URL uses its stable UUID, so migration does not break links. Pool storage is prominent in the pool page, including each resource's accessible hosts; do not bury it under a host-only settings page.

Initially search the current list/tree. Introduce permission-scoped global search once more resource types exist. Use ordinary pagination before custom virtualization or an advanced query language.

### Screens, introduced with their backend capability

| Phase | Surface | Essential content |
|---|---|---|
| 1 | Setup and enrollment | Create site; the first enrolled host becomes master of its automatic pool; enrollment instructions, connection status, useful next action |
| 1–2 | Pool | Overview, Hosts, VMs, Storage, Networks, Tasks; only expose functional tabs/actions |
| 2 | Host | Summary, capacity/freshness, VMs, existing network/storage access, tasks |
| 2 | VM | Summary, Console, Hardware, Tasks; current/pending settings and clear power state |
| 3, after Phase 2 | Host-local Core UI | First-server Controller deployment, existing-Controller enrollment, host/resident-VM status, basic local power/console controls and Controller recovery, available independently of central management |
| 4 | Storage details | Local/shared type, real capacity, accessible hosts, volumes, attachments, health |
| 5 | VM Snapshots | Time, consistency, disk coverage, restore/delete actions, consolidation status |
| 6 | Pool Backups and VM Backups | Repositories/policies, recovery points, last success/verification, restore and job details |
| 7 | Migration and maintenance | Destination suitability, blocking reasons, affected VMs, progress and outcome |
| 8 | Operations and access | Alerts, task/event filters, scoped roles, update/recovery status |
| 10, later | Pool Applications and container Infrastructure | Enable Container Services, deploy/manage applications, persistent volumes, recovery health, and protected worker VMs |

### Host-local setup, control and Controller deployment

**Agreed 2026-09-08; planned for Phase 3 after Phase 2 VM/console foundations.**
Opening an installed Core host's management address should show a small local
interface: authenticate, inspect this host, and choose
**Create a new environment**, **Connect to an existing Controller**, or
**Recover this host's Controller**. A single new server must be enough to deploy
the Controller appliance without an existing VM or central management service.

Use the approved Nex styling and shared working controls. Guide storage/network
selection, show preflight blockers and durable deployment progress, then link to
the verified Controller URL. Resident VM state, permitted power operations and
authorized consoles remain accessible locally during Controller outages; show
local observation freshness separately from central connection status. Confirm
disruptive actions with the exact target and explain blocked ownership or recovery
conditions. Central configuration and pool workflows remain in Controller.

The [Phase 3 bootstrap section](2026-09-08-nex-platform.md#host-local-ui-and-controller-appliance-bootstrap)
owns deployment, authentication, API handoff, authority, offline boot and recovery
requirements. Review actual desktop/360px and keyboard first-server, external
enrollment and outage/recovery flows when implemented. No host-local Nex UI was
operated for this planning decision. **API impact: none for this documentation;
new local API and reused/extended Controller/Core contracts are planned in Phase 3.**
**Guide impact: none now**; instructions and verified captures ship with implementation.

### Pool-first interface direction

**Decision (project owner, 2026-09-16):** follow the
[Core-first product direction](2026-09-08-nex-platform.md#core-first-product-direction).
Controller presents Core-reported placement eligibility, blockers and recovery limits; it does
not compute policy or run host workflows. Keep current host assignment and constraints visible
when automatic placement arrives. Distinguish requirements from preferences, and deployment
placement from recovery or ongoing balancing. Maintenance previews follow Core evacuation
capabilities and must not imply an unimplemented failure simulator.

The future application catalogue can include VM appliances and Docker applications with shared
discovery and progress views, while retaining their distinct lifecycle and protection controls.
Introduce it only after the corresponding Core capabilities and acceptance gates work; do not
add placeholder controls or change the current implementation queue. The platform plan owns
this direction and its dependencies. API impact: **none** and guide impact: **none** for this
planning clarification; future contracts and verified operator instructions ship with each slice.

### Managed container services: seamless and colorful

project owner's agreed extension is Docker Swarm inside Nex-managed worker VMs, with the complete operator workflow in Nex Controller. Do not use LXC or install application Docker workloads directly on Nex Core. This is a later milestone after the VM foundation; Ceph is not a prerequisite. The [container implementation phase](2026-09-08-nex-platform.md#10-managed-container-services-docker-swarm) owns architecture, dependencies, and safety gates; these are Nex design requirements, not claims about functionality that already exists.

- **Enable:** pool → Container Services → participating physical hosts, worker capacity, and supported pool storage → placement/prerequisite checks → review → tracked provisioning. Core provisions and maintains worker VMs, configures Swarm, and distributes managers across physical failure domains; Controller requests these operations through Core APIs and presents durable progress; show roles and placement without requiring routine SSH or Docker commands.
- **Deploy:** pool Applications → image/application name → ports, networks, replicas, resources, secrets, and persistent volumes → validation → review → tracked deployment. Pool Applications exposes services, their container instances, volumes, networks, and secrets with clear relationships and permissions. Never reveal secret values in routine details, logs, or task output.
- **Operate and protect:** provide logs, health, scaling, rolling updates, backup schedules/history, and guided restoration from the same application context. Show the actual supported consistency, downtime, last backup, and last verified restore. A worker-VM snapshot alone must not imply protection of externally mounted application data.
- **Storage and recovery:** offer dedicated application volumes on supported pool NFS storage only where worker access is verified; do not expose hypervisor disk directories or backup repositories to applications. Ordinary local Docker volumes are not portable shared storage. Explain that failover starts replacement containers and interrupts connections; it does not live-migrate container memory. Initially admit writable persistent applications only when validated for concurrent access or protected by enforced writer exclusion covering actual Swarm rescheduling and updates. Reject other writable stateful deployments until that safety is validated: disabling a Nex HA control does not stop Swarm from replacing tasks. Show blockers rather than promising safety from a single replica or shared mount alone.
- **Infrastructure:** retain a discoverable protected worker-VM view with role, physical host, capacity, health, and maintenance actions. Route deletion, resizing, and disruptive updates through dependency/quorum checks and a tracked drain/replacement workflow. Keep running-service state distinct from Controller reachability and Swarm-manager quorum; show stale or unknown observations honestly.

The Docker/Application area must remain **colorful, with attractive colored application and service icons**, not a monochrome infrastructure table. Preserve the [current Nex shell direction and approved logo shapes](../brand/README.md): use recognizable application icons and intentional flat color accents in application cards, service rows, and detail headers. This does not authorize replacing Nex's wordmark or changing its shell palette. Provide a consistent colored generic icon with a readable application name when no suitable icon exists; icon availability must never block deployment. Keep decorative application colors separate from functional health/status tokens, and pair status with text and distinct symbols. Validate contrast, keyboard access, small-icon legibility, and loading/missing-icon states in actual browser captures.

Introduce these screens in the same sequence as the backend: stateless deployment and recovery → supported shared volumes → application backup/restore → validated stateful failover. Acceptance includes completing enable/deploy/scale/update/restore without routine SSH, exercising blocked and interrupted operations, confirming worker-VM protection, and visually reviewing the colorful icons alongside error/stale states. Do not display unimplemented container controls in earlier VM milestones.

### Primary workflows

**Agreed follow-on: easy hardware passthrough.** Host → Hardware → guided readiness/preparation, then stopped VM → Hardware → Add physical device → review → tracked assignment. Give NVIDIA and AMD GPUs the same clear experience, with readable device names, companion functions, ownership, actionable firmware/isolation/reset blockers, reboot confirmation, and an undo/recovery path. Show host-bound placement and backup limitations before assignment; distinguish the browser console from physical GPU output. Supported workflows must avoid routine SSH or XML editing. The [hardware passthrough plan](2026-09-08-nex-platform.md#guided-hardware-passthrough) owns scope, planned new API contracts, and physical/browser acceptance gates; this documentation changes no runtime API and adds no controls to earlier milestones.

1. **Create VM:** start in a pool or host → name/install source → placement/resources → storage/network → review → create. Preselect the originating scope, show supported choices, and link the resulting durable job. The staged flow is a Nex proposal, not implemented behavior.
2. **Add storage:** pool → backend/location → participating hosts → access checks → review/register. Separate registration from formatting; registration must never silently destroy media.
3. **Migrate:** VM → destination host → compatibility preflight → required mapping → review → tracked migration. Explain incompatible CPU/network/storage/attached-device constraints beside the destination. Shared storage does not bypass preflight.
4. **Snapshot rollback:** VM → recovery point → affected disks/configuration and discarded changes → confirmation → tracked revert. Explain disk-only versus memory state and consistency.
5. **Backup restore:** repository/recovery point → new identity → target pool/host/storage → network mapping with isolated/disconnected default → review → tracked restore → validation. Original-VM replacement is a separate explicit choice.
6. **Host maintenance:** mark draining → display movable/blocked VMs → evacuate → verify empty → permit disruptive work. Never silently force-stop an immovable guest.

Migration and restoration descriptions are Nex proposals; they are not descriptions of an implemented wizard. Reuse the same validation and operations API as other clients.

### State, safety, and usability requirements

- Show running, stopped, unavailable/unknown, stale, and operation-in-progress distinctly. Never translate lost connectivity into a claimed powered-off state.
- Show management heartbeat age and individual service/storage check results separately. When HA is enabled, add pool membership/quorum and fencing/resource status; a healthy connection is not proof of quorum or safe recovery. Follow the [monitoring and authority contract](2026-09-08-nex-platform.md#health-monitoring-heartbeats-and-cluster-safety).
- Every mutation has a durable task with initiating user, object, time, progress/current step, result, and actionable error. Global and object task views reference the same record.
- Do not claim success on HTTP acceptance, fake progress percentages, or offer cancellation/retry unless the backend can honor it safely. Page refresh does not recreate the operation.
- Explain unavailable actions: unsupported configuration, permission, conflicting job, host state, or incompatible destination. Sensitive information remains permission-scoped.
- Confirmation names the target and consequence. VM deletion separates configuration removal from disk deletion; restore replacement identifies what will be overwritten.
- Tables use consistent status labels, sorting, filtering, units, selection behavior, and row actions. Long names remain discoverable; names alone are not unique identities.
- Empty states offer the next available action. Permission-limited empty states do not suggest actions the user cannot perform. Errors preserve form input.
- Use keyboard navigation, visible focus, labelled controls, accessible dialogs, adequate contrast, and non-color status cues. Console shortcuts must not trap browser navigation.
- Keep updates from moving the selected row or resetting form input. Task updates should not interrupt reading or repeatedly announce the entire page.

### Design and QA sequence

1. Begin the UI work with the shell, one pool/host inventory, VM details/console, creation flow, and task feedback. Use representative empty, connected, stale, failed, and unauthorized states.
2. Review a focused prototype before spreading the layout to storage/protection/migration. Capture real browser output and verify keyboard use, forms, overflow, and a narrow viewport.
3. Add each feature's screens when its backend gate passes. Do not ship placeholders that imply unimplemented protection or recovery support.
4. Before release, walk through enroll → create → console → snapshot/revert → backup/restore → migrate/maintenance. Include controller disconnection, insufficient storage, forbidden actions, and interrupted jobs.
5. Record captures and actual findings with the implementation slice. A mockup or a successful frontend build is not proof that Nex's workflows work.
