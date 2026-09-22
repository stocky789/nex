# Nex Controller and Nex Core implementation plan

Date: 2026-09-08  
Status: Standalone installer alpha and Phase 1 Controller/agent development foundation have lab evidence. **Phase 2 closed 2026-09-17** by project owner decision: gate5 functional clauses plus BUG-0256 / admission lab on published 0.3.1 and VirtIO media on 0.3.2; see [Phase 2 close](../qa/2026-09-17-phase2-gate/CLOSED.md). Remaining delivery gates are tracked below.

Project planning files live in `docs/plans/`.

**CPU baseline reconciliation, 2026-09-17 (0.3.1 website/APT release):** Core dynamically discovers
and verifies a common named CPU with explicit features across all current pool members.
New VM drafts select this fixed definition; manual host-usable models remain available.
The existing Core VM journal and master `pool-vms` specification own the saved definition,
recovered unchanged through inventory by a fresh Controller. No guest-OS CPU policy or
hardcoded model catalogue was added. **API impact: changed contract** with new authenticated
Core peer routes; **guide impact: updated**. See the [CPU plan](2026-09-17-cpu-baseline.md)
and [native/runtime, browser and regression evidence](../qa/2026-09-17-cpu-baseline.md).
Phase 2 is closed separately ([close record](../qa/2026-09-17-phase2-gate/CLOSED.md)); this
CPU note does not establish migration/heterogeneous physical-pool acceptance. Upgrade
Controller before Core. The [0.3.1 release report](../qa/2026-09-17-nex-core-0.3.1-release.md)
records website/APT publication; no upgrade of existing operator hosts is claimed.

**Core project priority, 2026-09-09:** project owner confirmed successful installation and
authorized [the first managed VM creation slice](2026-09-09-first-managed-vm.md):
host-local storage, existing guest-network registration, ISO upload and VM creation
through Controller. A separate worker corrects the installer's distorted logo.
Website and release/download work retain their separate owners. API impact:
**new contract** for resource verification, media transfer and durable VM creation;
guide impact: **updated** with actual implementation and walkthrough evidence.
The complete Phase 2 lifecycle/console acceptance gate remains separate.

**Controller layout direction, 2026-09-09:** project owner redirected the next work to a
Windows desktop application overhaul of the Controller web GUI: compact menu/toolbar,
left inventory tree, selected-resource header and tabs, dense properties/tables,
and bottom activity/status. This supersedes the earlier browser-shell choice;
retain Nex identity and Sites → Pools → Hosts → VMs. Implement the
[desktop layout slice](2026-09-09-controller-desktop-layout.md) using existing
Controller capabilities. Core-local UI, installer work and phase acceptance gates
remain unchanged; this is not authorization to add Phase 2 features.
**API impact: existing contract** — preserve session, inventory, enrollment,
host-action, operation and audit APIs and existing Core commands/results.
**Guide impact: updated** — align navigation instructions with the delivered UI
and record actual browser walkthrough evidence before accepting the redesign.

**Pool model correction, 2026-09-09:** project owner decided that Nex pools follow the established
master-and-members model. Administrators never create pools; every enrolling Nex Core
host becomes the **pool master** of its own automatically created pool, named after
its hostname. Other hosts in a pool are **member hosts**.
Decision (project owner, 2026-09-10): pool and host IDs and the pool name are generated and kept by
Nex Core — the host generates its own ID, the pool master generates the pool ID and holds the
pool name — and any Controller adopts them unchanged; the Controller never creates or derives
pool identity. A separate code slice implements this and documents the enrollment contract in
[docs/API.md](../API.md).
Enrollment tokens bind to a site, not a pool. The BUG-0094 priority below brings forward
the member-join prerequisite for storage reconciliation; changing the master and ejecting a
member are implemented in the dirty working tree but remain Phase 4 acceptance work.
See the [pool master model decision](2026-09-09-pool-master-model.md).
**API impact: changed contract** — enrollment tokens, agent enrollment, appliance
bootstrap/adoption and the pool listing change; manual pool creation is removed.
**Guide impact: updated** — Controller, appliance and VM-creation pages describe
the automatic pool and pool master.

**VM creation before editing, 2026-09-10:** project owner directed that the **complete VM creation system**
is built before stopped-VM editing, so edit is written once against the final VM model instead of
against the single-disk, single-NIC shape and then reworked. The slice adds description and guest-OS
hint, CPU topology and model, a two-mode memory model (dedicated reservation or a maximum ceiling
that reserves nothing), multiple disks, multiple networks with automatic or manual MACs, boot order,
start-after-creation and autostart. Templates, clones, cloud-init, bulk creation, CPU weight/cap,
affinity and ACLs stay deferred with their existing reasons. Because Nex is unreleased, the VM
specification is replaced outright: no legacy fields, no capability gate, no journal migration.
project owner also decided the **Phase 2 acceptance gate is folded into this slice** rather than run
separately, so the new creation path is proven by a real ISO guest installation, console use,
reboot, persisted data and a Controller outage. See [the plan](2026-09-10-vm-creation-system.md).
**API impact: changed contract** — the VM creation request, `vm-create` command payload, VM list and
report inventory carry the new specification; authentication, operations, replay and audit are
unchanged. **Guide impact: updated** — the creation workflow, both memory modes and their limits,
and the guest-tools dependency for memory reclaim.

**Storage repository implementation priority, 2026-09-11:** project owner authorized Terra
5.6 medium workers to implement and locally test OpenZFS/ZFS, iSCSI, NFS and SMB VM repositories.
See the [storage implementation plan](2026-09-11-vm-storage-repositories.md) and
[current checks](../qa/storage-repositories/checks.md). The original repository slice consumes
existing storage; the later [ZFS provisioning slice](2026-09-19-zfs-repository-provisioning.md)
adds Core-owned creation from explicitly selected devices. Core keeps registrations, identities
and credentials; Controller submits operations and displays inventory.
Single-host code is in testing, not completed backend acceptance. Multi-host placement retains
Phase 4 ownership and exclusion gates; existing-target iSCSI consumption does not add SAN
provisioning. Disk formatting, storage-server administration, migration and HA remain outside scope.
**API impact: changed/new contracts** — typed registration, credential handoff, lifecycle,
protocol negotiation and inventory/adoption are implemented on both sides.
**Guide impact: updated** — storage registration and VM creation document the development workflow.
All durable repository state lives on the Core master's journal and private credential store.

**Unresolved bug priority, 2026-09-12:** project owner authorized completion and primary-agent
local walkthroughs for BUG-0093 through BUG-0096, explicitly excluding BUG-0097 nested UEFI.
The [acceptance plan](2026-09-12-unresolved-bug-ledger.md) brings forward only the Core-owned
membership, peer trust and safe NFS/SMB reconciliation needed for BUG-0094. Local ZFS and
ordinary-filesystem iSCSI remain single-host; master transfer, ejection, migration and HA are
not included. This scope decision does not mark Phase 4 or backend acceptance complete.
**API impact: changed/new contracts** — authenticated peer join, member storage reconciliation
and transient host discovery; existing inventory carries automatic ISO registration and health
reasons. **Guide impact: updated** — delivered workflows and executed evidence are recorded in
the storage, pool-join and Controller guides; outstanding checks stay explicitly open.

**Concurrent storage priority, 2026-09-12:** project owner authorized implementation
and a three-to-five-host lab for simultaneous NFS, SMB and iSCSI datastore access,
with two VMs per host. The [concurrent storage design](2026-09-12-concurrent-pool-storage.md)
brings forward native clustered-GFS2 iSCSI, required fencing/quorum and master-owned
VM execution exclusion. Ordinary ext4/XFS iSCSI stays exclusive. Automatic VM HA,
migration and failback remain separate gates. API impact: **changed/new contracts**
for cluster setup/status, peer reconciliation, shared profiles and VM admission;
guide impact: **updated** with implemented workflows and actual evidence before
acceptance. This authorization is not evidence of working clustered storage; current status
and open gates: [production-readiness handoff](2026-09-14-production-readiness-handoff.md).

## Core-first product direction

**Decision (project owner, 2026-09-16):** prioritize Nex Core as the product: the Debian host
platform implements pool capabilities and exposes them through its API. Controller is the
replaceable management UI/API that authenticates operators, submits requests and displays
host results. Apply the [Core-first acceptance order](../../AGENTS.md#core-first-delivery-and-acceptance)
to every slice, retaining the existing closed Controller-scoped responsibilities and explicit
backup/restore orchestration exception. Existing storage safety, pool authority and recovery
gates remain the priority; migration, HA and managed applications keep their dependencies.
This decision changes no milestone status or implementation queue.

Preserve these directions as the relevant phases are designed; do not add speculative fields,
endpoints, schedulers or UI controls now:

- **Pool-first workloads:** workloads belong to pools, with visible host assignments. Keep
  explicit placement until Core can safely select a host. Mandatory requirements (storage,
  networking, devices and compatibility) never silently relax; preferences may fall back.
  Pinning restricts placement and must expose its recovery limits. Initial placement, failure
  recovery and ongoing balancing are separate actions, not one implicit automation permission.
- **Explain before automating:** Core reports eligible hosts, blockers and observation freshness
  through its API; Controller renders those results instead of calculating eligibility itself.
  Extend existing compatibility checks in their owning slices. Add maintenance previews with
  migration/evacuation, not as an independent simulation project; revalidate before execution.
- **Policies remain host-owned:** future VM placement/protection policies and workload relationships
  must have a durable record on the pool master, recovered through inventory/adoption by a fresh
  Controller. Each implementation slice must name its concrete record and API contract before
  adding settings. Swarm retains container task scheduling; Core manages the supporting VM
  lifecycle and supported constraints. Preserve the Controller backup/restore exception.
- **Unified application catalogue, follow-on:** allow both VM appliances and Docker applications
  behind one discovery/deployment experience, using their existing lifecycle engines. Start with
  a small supported set whose deployment, updates and recovery are verified. VM image/bootstrap
  and Phase 10 prerequisites still apply; this adds neither LXC nor Docker workloads on Core.
  Deployed application identity/configuration must be recoverable from the pool, including its
  managed Swarm, without a sole Controller-held recipe or mapping. The
  [application catalogue design](#application-catalogue) in Phase 10 is the agreed shape
  (decision: project owner, 2026-09-22).
- **Honest recovery:** describe validated restart/replacement behavior and its limits per workload;
  do not promise uninterrupted applications from a generic HA switch. Reserve controls and
  recovery priorities follow proven HA. Automatic balancing, power/temperature optimisation,
  broad failure simulation and automatic dependency discovery remain deferred possibilities.

**API impact: none for this planning change** — no client-facing endpoint, Controller operation,
Core command or observed-result contract changes. Future placement explanations, policies,
maintenance previews and catalogue deployment require scoped API assessments and both-side
implementation in their owning slices. **Guide impact: none** — no implemented operator workflow
changes. **State owner: none for this documentation** — future durable pool records belong to
Core as described above; concrete records and adoption checks remain implementation prerequisites.

## Build order: basics first

Each milestone must work end to end before the next one expands the scope. The feature review is a source of follow-on features, not a requirement to build an enterprise feature set before Nex can run a VM.

| Order | Working result | Detailed phases |
|---|---|---|
| 1. Local foundation | Stock Debian runs a persistent guest through libvirt | 0 |
| 2. First managed VM | One controller, site, pool, host, local disk, VM, and browser console | 1–2 |
| 3. Installable host and Controller bootstrap | Nex Core installs; its local UI deploys a Controller VM or connects to an existing Controller | 3 |
| 4. Shared pool | Two hosts use pool-level NFS storage with explicit VM placement; master authority replicates and transfers with manual, fence-verified recovery first; automatic succession is separately gated | 4 |
| 5. Recovery | Snapshot rollback, independent backups, scheduling, and proven restores | 5–6 |
| 6. Mobility | Shared-storage live migration and host evacuation | 7 |
| 7. Supported release | Upgrade/recovery checks, access controls, and operator documentation | 8 |
| Later | Per-pool HA, managed Docker applications, and additional workflow features; Ceph when ready | 9–10 and follow-on backlog |

**Initial ISO slice, authorized 2026-09-08:** build and verify the first branded Nex Core ISO before starting the controller. Combine the host foundation in Phase 0 with the standalone installer portion of Phase 3; controller enrollment and Nex agent packaging remain deferred until those components exist. This changes delivery order, not the later acceptance requirements. The first controller demo uses existing host networking and local storage; a network editor, templates, advanced backup formats, HA, and Ceph do not block it. Security checks and data-loss prevention remain part of the feature they protect.

**Subsequent development decisions, 2026-09-08:** project owner authorized Phase 1 alongside
installer work; see its [implemented scope and development acceptance](2026-09-08-controller-foundation.md).
project owner then agreed to a host-local web UI on every Nex Core host,
with Controller appliance deployment and local recovery. Deliver this in
[Phase 3 after Phase 2 VM operations](#host-local-ui-and-controller-appliance-bootstrap),
before Phase 4 multi-host expansion. It is planned work, not an expansion of the
active standalone installer changes or a claim that local web control exists today.

**Appliance implementation priority, 2026-09-08:** project owner subsequently requested
building that host-local UI and a seamless Debian Controller virtual appliance now,
with parallel workers. The [appliance implementation slice](2026-09-08-controller-appliance.md)
brings forward its required local VM foundation and bootstrap from Phase 3;
general Phase 2 VM/console work and full phase acceptance remain separate gates.
Controller is a prepared cloud image with per-instance initialization, not an ISO.
API impact: **new contract** for local deployment/control and central bootstrap/adoption;
guide impact: **updated** with implementation and actual verification evidence.

project owner selected AGPL for original Nex code on 2026-09-08; apply AGPL-3.0-only to Nex code/build scripts while preserving upstream component licenses and handling artwork separately. See `LICENSE` and `docs/LICENSING.md` for the implementation of that decision.

**Repository decision, 2026-09-08:** a full Debian mirror is unnecessary for the first release. Bundle installation dependencies on the ISO and use Debian's signed repositories for upstream updates. Add a small signed Nex APT repository when Nex packages and updates exist; operating our own distribution does not require mirroring Debian. Exact source retention for redistribution is a separate release requirement below.

**API impact of the standalone ISO slice: none** — installer packaging, release identity, and repository configuration introduce no Controller/Core or client-facing API; enrollment remains deferred.

**Installer account decision, 2026-09-08:** ask only for a root password and its confirmation; skip ordinary user creation using Debian Installer’s native account policy. An installer hook rejects blank root passwords instead of Debian’s ordinary-user fallback; mismatched confirmation retains Debian’s retry. Keep system/service accounts and local root recovery access; root SSH remains disabled. **API impact: none** — boot menu → preseed → Debian account setup affects local OS credentials only; there is no Controller operation, Core agent command/result, or client-facing API in this flow. See the [root-only installer verification](../qa/iso/2026-09-08-root-account.md) for the exact rebuilt image, password-dialog checks, and installation results.

**Installer interface correction, 2026-09-08:** project owner clarified that the GUI must be a
custom Nex interface, not Debian's stock GUI. Retain the pre-install GUI/minimal choice,
but replace the graphical presentation with an original Nex shell: brand header,
section navigation, task heading, purpose text, question pane and action/footer layout.
Rebuild the pinned cdebconf 0.280 GTK frontend with this shell, preserving its question
handlers, password validation, disk selection/confirmation, progress and error paths.
Sections indicate the current question category, not completed acceptance checkpoints.
Minimal text and serial installation remain available; neither choice installs a desktop.
The earlier native-GUI choice did not satisfy the requested custom interface.
**API impact: none** — local boot menu → cdebconf GTK presentation → existing Debian
question/answer and installer operations → installed host; no Controller operation,
Core agent command/result or client-facing Nex API. The existing upstream frontend ABI
is pinned and checked during the build, rather than introducing a new local service.
**Guide impact: updated** — custom interface, navigation, limits and matching QA evidence.
See [custom GUI verification](../qa/iso/2026-09-08-custom-installer.md).

**Installer logo correction, 2026-09-08:** replace the placeholder typed n tile and
system-font product heading with the approved Nex Core lockup from the existing brand board.
Render the raster through native GTK image support and retain it with the frontend source.
No new logo design or dependency is needed; production vector masters remain outstanding.
**API impact: none** — local header rendering only; Controller/Core and client APIs are absent.
**Guide impact: none** — installation choices, controls and recovery steps are unchanged.
See [logo verification](../qa/iso/2026-09-08-installer-logo.md).

**Installer workflow correction, 2026-09-08:** project owner rejected the earlier custom shell as
still following Debian's question-by-question workflow. Replace the primary GUI flow with
original Nex GTK pages for welcome, host identity/access, detected NICs, explicit disk choice,
and a single review with an explicit erase checkbox naming the selected target. The earlier shell/logo QA remains valid
only for those historical artifacts. A safe embedded GUI seed avoids the routine debconf-file
loading page; preserve meaningful backend progress, errors and recovery prompts.

This slice now includes local installer network configuration: disabled/DHCP/static IPv4
per physical NIC, one tagged VLAN or untagged interface per NIC, an explicit management
adapter owning the default route/DNS, and MAC-bound installed names displayed before commit.
Apply these settings only to the installed system for first boot; discovery enables NICs only for carrier detection, without assigning addresses or
routes or starting DHCP during discovery. Selecting DHCP subsequently performs a bounded
lease preview, without applying addresses/routes/DNS; the installed host still negotiates
its own lease. Suppress automatic IPv6 addressing before enabling a NIC. The selected management NIC now creates a native `br0` on first boot. No bonds, custom
bridge editor, trunks, multiple VLANs per NIC or IPv6 editor are claimed.
This is not the later Controller network editor and does not change that phase's safety gates.
The GUI initially uses English, five keyboard layouts and supplied timezone identifiers.
Its disk path supports one nonremovable sd/vd/xvd/nvme whole disk of at least 16 GiB, ext4 and
explicit checkbox erasure consent; preserve the minimal text path for manual installation.

**API impact: none** — GUI → local validation/staged network files and Debian seed → native
installer → installed OS files; no Controller operation, Core agent command/observed result,
or client-facing Nex API exists in this flow. Local handoff validation is implemented in the
same slice; no service endpoint is introduced.
**Guide impact: updated** — replace obsolete GUI question-flow instructions with the actual
pages, NIC/VLAN constraints, disk confirmation and local recovery. Acceptance remains subject
to actual evidence in [Nex wizard QA](../qa/iso/2026-09-08-nex-wizard.md), including independent
review; earlier installer checks cannot establish the new workflow's completion.

**Installer usability correction, 2026-09-08:** project owner reported unused screen space and
hard-to-use timezone selection, and requested practical quality-of-life improvements.
project owner subsequently authorized a full modern, flat, professional visual overhaul. Replace
the old horizontal step strip with a compact vertical step list, a white form panel on a
neutral background, and persistent Back/primary-action navigation. Keep completed and active
steps distinct using text and symbols as well as color. Match the existing backend progress
shell's typography and palette while preserving its native error/recovery behavior.
Make the native wizard occupy the available X screen without relying on a window manager,
including resolution changes. Following project owner’s correction, use selection-only area/timezone dropdowns with UTC as
the default; remove the unwanted keyboard-test field and timezone text entry. Show
descriptive keyboard names and offer a password visibility toggle
that resets when leaving the host page. project owner also requested hostname editing on Network:
share the Host entry's value rather than introduce a second setting, retain the same
validation, and show the current name in review. Add contextual field help while retaining explicit
disk selection, checkbox erasure confirmation, credential validation and the existing network
limits. Use existing GTK controls and bundled timezone data; no new dependency or external
location service is required.

**API impact: none** — local GTK controls continue through the same private configuration,
validation and Debian installer handoff; no Controller operation, agent command/result or
client-facing API changes.
**Guide impact: updated** — document timezone dropdowns, keyboard selection, password visibility
and the revised screen/navigation structure in the installation guide. Actual artifact,
visual checks, regression
results and outstanding limits belong in [installer usability QA](../qa/iso/2026-09-08-installer-qol.md);
this scope decision does not establish those checks passed.

**Standalone alpha result, 2026-09-08:** branded BIOS/UEFI menus, offline BIOS installation, installed services, a nested libvirt KVM guest boot, host reboot persistence, and Nex identity preservation across a `base-files` reinstall passed on the final image. See the [exact artifact and QA evidence](../qa/iso/2026-09-08-alpha1.md). This completed the first ISO lab slice, not the full Phase 0/3 gates. **Final 0.2.9 lab result, 2026-09-17:** the exact release ISO (`4c44133a…2f2b9`) passed BIOS/UEFI menu boots, unattended offline UEFI installation, first boot and reboot under TCG; Secure Boot installation on a host, full guest networking/data persistence, physical hardware and controller enrollment remain outstanding.

**Debian licensing review, 2026-09-08:** retain AGPL-3.0-only for original Nex code. The current installer aggregates separate Debian components under their own licenses; this does not make the whole ISO AGPL. Copying upstream code, linking libraries, or adding plugins requires a separate compatibility review, especially for GPL-2.0-only code. See the [licensing review and release evidence](../LICENSING.md).

**External ISO distribution gate:** inspect every embedded component (base ISO, installer, added packages, and firmware), retain the exact required corresponding source and build/install material, preserve notices, verify source delivery, and complete firmware review. The 0.2.9 tooling inventories `.deb`/`.udeb` media components, retains matching source material, binds the final ISO and source bundle by SHA-256, and removes `dahdi-firmware-nonfree`, `atmel-firmware`, `bluez-firmware`, `firmware-sof-signed`, `firmware-b43-installer`, and `firmware-b43legacy-installer`. Its exact final artifact passed component/source, checksum, menu-boot and unattended install/reboot inspection; the ISO, matching source and manifest were published and independently verified over public HTTPS with signed 0.2.9 APT packages. The Nex artwork redistribution basis is recorded in `ARTWORK-LICENSE.md`. A generic Debian mirror link does not discharge Nex's source-delivery obligations. Future network services must provide AGPL source access where required.

The [feature and UX review](2026-09-08-feature-and-ux-review.md) records the established management patterns Nex adopts and the interface direction. Recommendations there do not silently expand the first milestone.

**UX planning revision, 2026-09-08:** emphasize understandable day-to-day virtualization management in Nex Controller. The [UX opportunity and phase fit](2026-09-08-feature-and-ux-review.md#ux-opportunity-and-phase-fit) maps the supplied suggestions to existing workflows and follow-on candidates. Build useful pool summaries, responsive forms, guided VM creation, readable tasks, safe defaults, and accessible ISO upload with Phases 1–2; explain storage/network consequences in Phases 2/4 and simplify reviewed backup policy setup in Phase 6. Global search/command palette, templates, optional dark theme, and GPU/PCI passthrough retain their stated follow-on boundaries. The current standalone ISO slice and first-release virtualization safety gates remain unchanged.

**API impact: none** — this UX roadmap revision changes no implemented API. The companion review traces planned UI callers through Controller operations, Core commands/observations, and results; implement the necessary contracts and API reference updates with each owning phase, not as documentation-only endpoints now.

## Goal and agreed scope

**Nex** is the parent brand for a Debian-based virtualization platform with two products:

- **Nex Controller:** centralized management, web UI, API, authentication, inventory, task coordination, and protection policies.
- **Nex Core:** the installable hypervisor host: Debian, KVM/QEMU, libvirt, host networking/storage, and the Nex Core agent.

Nex Core is our own **Debian-derived distribution**, not an installed system presented as stock Debian. It owns its release identity/version, installer, selected packages, defaults, branding, and release process. The installed `/etc/os-release` must identify Nex Core (`ID=nex-core`, `ID_LIKE=debian`), and that identity must survive base-package updates. Installer media, installed boot menu, and console identify Nex Core; Debian ancestry, `/etc/debian_version`, upstream notices, and the standard signed kernel remain intact. A custom kernel fork is not required for distribution ownership. Nex-specific packages/repositories follow their implementation milestones; do not imply they already exist.

Decision (project owner, 2026-09-09): Nex Core has exactly one version, the `nex-core-local` package version. Its postinst writes `/etc/os-release` (`PRETTY_NAME="Nex Core <version>"`) and refreshes the console banners on every install and update; installation media are named after the package they install; the UI shows that version and no separate agent version.

Both products share the Nex identity, with Core and Controller as product descriptors. project owner approved the graphite wordmark, continuous x, n icon, and platinum-led palette on 2026-09-08. project owner clarified that the original blue/white/grey treatment remains primary; the later reference colors extend the supporting palette without recoloring the logo or replacing the light presentation. The [brand guide](../brand/README.md) and selected board are the canonical identity reference; production vector assets remain to be prepared. Branding work does not change the implementation phases or imply released functionality.

The management hierarchy is **Sites → Pools (one master host + members) → Hosts → VMs**. A pool is created automatically when its first host enrolls; that host is the pool master. Storage sits at the pool level. Snapshots, backups, restoration, and live migration with compatible shared storage are required features. Start shared storage with NFS; implement Ceph later.

Also agreed: pool-level **Container Services** using Docker Swarm inside Nex-managed worker VMs, with provisioning and day-to-day operations entirely through Nex Controller. These are Docker application containers, not LXC; Docker runs inside the managed VMs rather than directly on Nex Core. This is a later milestone after the VM foundation. The application area must remain colorful, with attractive colored application/service icons; see the [UI plan](2026-09-08-feature-and-ux-review.md).

Use **Pool** as the product term for the host grouping previously called a cluster. A pool has exactly one pool master and zero or more member hosts; the master controls the pool. HA is a pool capability enabled later, not a prerequisite for membership. Do not introduce a second Cluster object beneath Pool without a concrete need.

## Ownership and navigation

```text
Nex Controller
└── Site: Sydney
    └── Pool: Production
        ├── Hosts
        │   ├── nex-01
        │   │   └── VM: database
        │   └── nex-02
        │       └── VM: application
        ├── Storage
        │   ├── nex-01-local       [accessible by nex-01]
        │   └── production-nfs    [accessible by nex-01 and nex-02]
        ├── Networks
        ├── Backup repositories and policies
        └── Applications             [later Container Services milestone]
            ├── Services and container instances
            ├── Persistent volumes, networks, and secrets
            └── Infrastructure       [managed Docker node VMs]
```

VMs belong to pools and have separate host assignments. The UI displays them beneath hosts and in a pool-wide list, including unassigned and unavailable VMs. Within-pool migration changes placement, not identity or ownership. The agreed cross-site migration follow-on preserves VM UUID while explicitly transferring pool ownership and host assignment at a reconciled cutover; it does not clone the VM or move the host between pools.

| Resource | Ownership and rules |
|---|---|
| Site | Administrative/location grouping containing pools; not a WAN HA boundary |
| Pool | Created automatically at first host enrollment and named after that host; exactly one pool master plus member hosts, VMs, storage registrations, logical networks, and protection policies |
| Host | One pool, as its pool master or a member; unique identity; observed resources, capabilities, and health |
| Storage | One pool; backend, capacity, capabilities, and verified access per host |
| Volume | One storage resource; tracks VM attachment and exclusivity |
| VM | One pool; stable UUID, desired configuration, assigned host, observed execution state |
| Snapshot | One VM; disk set, configuration, consistency mode, and backend metadata |
| Backup repository | Pool-level independent backup destination; capacity and credentials |
| Backup | Immutable completed recovery point with manifest, VM configuration, and disk data |
| Operation | Durable request, actor, target, progress, result, and recovery state |
| Application/service | Pool-owned deployment intent; Swarm manages replaceable tasks/container instances |
| Docker node VM | Pool-owned managed VM with manager/worker role and a physical-host assignment |
| Container volume | Pool-owned application data on a supported backend; scoped access and explicit sharing/exclusivity rules |

Pool ownership does not make local disks shared. A VM using host-local storage remains constrained to that host until an explicit supported disk transfer occurs. Shared capacity must not be counted once per attached host. Block removal of storage, hosts, or pools while dependent resources remain unless an explicit migration/export/deletion workflow resolves them.

Keep Nex pool IDs distinct from libvirt storage-pool IDs. Libvirt storage pools are host-side representations of the registered storage resources. [Libvirt storage model](https://libvirt.org/storage.html)

## Initial architecture and stack

| Component | Initial choice |
|---|---|
| Host OS | Debian 13 `trixie`, amd64; standard Debian kernel and packages |
| Controller and agent | Go; two executables in one repository |
| Virtualization integration | Official `libvirt.org/go/libvirt` bindings |
| UI | TypeScript and React, built as static assets served by the controller |
| Controller persistence | SQLite on local disk, one active controller process |
| Host persistence | Small durable agent operation journal and identity/configuration files |
| Host services | systemd; Debian packaging and APT updates |
| Browser VM console | noVNC through authenticated controller/agent forwarding |
| Initial VM disks | qcow2 on an ext4-backed local directory |
| First shared storage | Existing external NFS service registered at pool level |
| Networking | Linux bridges; one explicit owner of host network configuration |
| Installation | Debian Installer, preseed, and embedded Nex packages/dependencies |

SQLite is a deliberate single-controller starting point; do not place its database on NFS. Revisit the database and leadership design if concurrent controllers become a requirement. No message broker, plugin framework, custom hypervisor, custom distributed filesystem, or custom consensus protocol initially.

The Go libvirt bindings use CGo and the installed native library. Build and package against the supported Debian environment; this agent is not a completely static binary. [Official bindings](https://libvirt.org/go/libvirt.html)

Nex Controller is a replaceable management front end; the pool master's Nex Core is the durable source of truth for everything scoped to its pool (see [Replaceable Controller](#replaceable-controller--2026-09-09)). Controller holds user intent and authorization, and persists each accepted pool-scoped change on the master so a new Controller re-adopts it. The Controller `operations` table is a mirror of host-reported journal results, not Controller-owned state: the host journal is the record, and a fresh Controller rebuilds the history, including failed and needs-attention outcomes, on adoption (decision: project owner, 2026-09-10). Libvirt and agents report actual VM/runtime/storage state. Record drift and reconcile deliberately; neither stale controller records nor unsolicited host reports should silently authorize destructive changes.

Agents initiate outbound authenticated HTTPS connections to the controller for inventory, commands, and console forwarding. VM disks, migrations, and backup data travel directly between authorized hosts/storage endpoints rather than through the controller database or API.

During Phases 1–2, start the controller on a separate Debian machine or VM outside
the hosts being installed. Phase 3 adds a host-local bootstrap/recovery UI that
can deploy the Controller appliance on the first Core host without an existing
Controller. External deployment remains supported. Local libvirt, storage,
networking and recovery access must boot independently of Controller availability;
see the [bootstrap and ownership gates](#host-local-ui-and-controller-appliance-bootstrap).

## Health monitoring, heartbeats, and cluster safety

Use two layers: agent-to-controller monitoring from the first managed host, then pool-local cluster membership and HA when Phase 9 begins. A single-host pool needs monitoring without requiring quorum infrastructure.

| Mechanism | Responsibility | What it establishes |
|---|---|---|
| Health checks | Nex Core agent checks local services/resources and reports to Nex Controller | Whether a particular checked capability is working; not a blanket guarantee of host health |
| Heartbeats | Each enrolled agent regularly signals over its authenticated controller connection | Management reachability and report freshness; absence means uncertain connectivity, not confirmed host death |
| Cluster membership | Evaluate Corosync between hosts in an HA-enabled pool | Which hosts can participate in the current cluster communication group |
| Quorum | Established pool-local quorum mechanism | Whether that group has enough votes to authorize cluster decisions; not a health score |
| Resource monitoring | Evaluate Pacemaker monitors for HA-managed VMs and dependencies | Resource failures and policy-driven recovery under the pool's single HA authority |
| Fencing | Pool HA authority using validated fence devices/agents | Isolation of an uncertain host before recovery elsewhere can safely acquire writable ownership |

Corosync supplies membership/quorum facilities; Pacemaker coordinates resource monitoring and recovery with fencing. Evaluate these existing components instead of implementing a second peer-polling/consensus system in the Nex agent. [Corosync votequorum](https://github.com/corosync/corosync/blob/main/man/votequorum.5), [Pacemaker administration](https://clusterlabs.org/projects/pacemaker/doc/3.0/Pacemaker_Administration/pdf/Pacemaker_Administration.pdf)

### Monitoring contract

- Combine libvirt/service events with periodic bounded checks and inventory reconciliation; events improve responsiveness, periodic checks catch missed events.
- Start with agent/libvirt availability, KVM capability, observed VM state, disk capacity, and relevant network links. Add per-host storage access and mount health with shared storage; check expected read/write capability using a dedicated bounded probe where safe. A mounted path alone does not prove usable storage, and a network link alone does not prove end-to-end connectivity.
- Keep heartbeat delivery independent of potentially blocked storage/service probes. Give probes deadlines and bounded concurrency so a hung NFS mount cannot exhaust agent workers or conceal all other observations.
- Report each check's outcome, observation time, last success, and error. Distinguish healthy, degraded, failed, unknown, and unsupported checks from management connection state. A running VM does not establish that its guest application is healthy; guest-agent/application checks are separate capabilities.
- Configure heartbeat/check intervals, probe deadlines, stale thresholds, and alert debounce; choose and document defaults from lab measurements rather than promising arbitrary failover times. Allow realistic network/storage latency. Use monotonic elapsed time for local timeouts and receipt freshness, with wall-clock timestamps for operator history.
- Discard superseded/out-of-order observations and perform a fresh inventory reconciliation after reconnect or process restart. Do not mark every resource healthy merely because one heartbeat arrived.
- Block new operations that require a capability known to have failed or whose required state cannot be verified. Record alerts and actionable reasons; do not restart or relocate VMs as a side effect of ordinary monitoring. Repeated failures may debounce alerts but must not turn unknown state into a positive safety assertion.

### Authority and outage rules

The active pool master on Nex Core owns and persists pool membership, placement/recovery policy
and operation decisions; Nex Controller only requests changes and displays Core's observations.
The pool-local cluster layer supplies quorum, fencing and execution exclusion, and must be
integrated with the active master as one authority, never a competing scheduler. Members execute
authorized work, not independent relocation decisions. Controller connectivity is not a quorum
vote; a Controller or WAN outage alone must not remove the pool's decision-making authority.
*(Narrow exception. Decision: project owner, 2026-09-22: a Controller may act as the automatic
quorum-device witness that breaks a tie in a two-host pool, holding no durable pool state. Its
outage removes only automatic takeover, never a Core operation or the pool's authority; see
[two-host pool fencing](2026-09-22-two-host-pool-fencing.md#the-controller-as-witness-without-breaking-the-invariant).
Planned, not implemented.)*
The master is a transferable role, not a permanent dependency on one physical host; every
change of master follows the [pool authority transfer](#pool-authority-transfer-and-master-succession)
design. Without a provably valid master/ownership authority, block new starts and transfers and
apply the tested no-quorum policy: the native cluster freezes on quorum loss
(`no-quorum-policy=freeze`) and exclusion comes from verified external power-off fencing, not
from hosts fencing themselves. *(Amended 2026-09-22, project owner: pools of three or more hosts
may also use hardware-watchdog self-fencing under the proof rules in
[automatic host fencing](2026-09-22-automatic-host-fencing.md#rule-change). Slices 1-4c are
implemented in the working tree, with pre-merge review fixes applied 2026-09-22: a pool of three
to five hosts without clustered storage forms its own cluster and arms its hosts. Go tests and a
nested lab only — no physical watchdog has been read, armed or expired and no physical host has
been reset; clustered-storage pools and pools above five hosts arm nothing and report why.)*

Controller outage leaves running guests and established pool-local HA operating. Before HA exists, uncertainty results in alerts and restricted operations, never an automatic failover. With HA enabled, a missing heartbeat alone still cannot authorize a second VM start: the HA authority must establish quorum, required storage/capacity, and successful fencing of an uncertain previous owner.

Define and test no-quorum and fence-failure behavior before enabling HA, including any required freezing or stopping of existing resources; do not promise every guest keeps running through a pool partition. Failed or unconfirmed fencing must prevent recovery elsewhere. Routine monitoring does not introduce a competing restart path.

## Delivery phases and acceptance gates

### User guide requirement for every change and phase

**Agreed 2026-09-08:** maintain a [living user guide](../guide/README.md) from the first ISO onward,
as portable Markdown a future website can render. Every phase gate includes the guide gate in
[AGENTS.md](../../AGENTS.md#living-user-guide-and-documentation-gate) and
[DOCUMENTATION.md](../DOCUMENTATION.md). Hosted enforcement of the documentation CI checks
must be verified after repository and runner setup. API impact: none.

### API requirement for every change and phase

**Agreed 2026-09-08:** every change and phase gate applies the
[required API impact check](../../AGENTS.md#required-api-impact-check) and keeps the
[living API reference](../API.md) current. Phase 1 delivers the OpenAPI descriptions, generated
browsable reference and agent protocol documentation; later phases extend them. API impact: none.

### 0. Prove the supported host baseline

1. Prepare a disposable Debian 13 host with working hardware virtualization, or a nested lab with verified KVM acceleration.
2. Install `qemu-system-x86`, `qemu-utils`, `libvirt-daemon-system`, `libvirt-clients`, `virtinst`, `ovmf`, `openssh-server`, `nftables`, and `chrony`; verify exact dependencies in the Debian build environment.
3. Create a local directory storage pool and an isolated/NAT guest network. Start a test guest using libvirt.
4. Verify local/serial recovery access, SSH with individual credentials, and guest graphical/serial consoles. The guest agent belongs inside supported guest images, not merely on the host.

**Gate:** accelerated guest boots, receives networking, retains written data after guest/host reboot, and can be controlled locally.

### 1. Controller foundation and host enrollment

1. Create the Go controller and agent, minimal UI, schema migrations, systemd units, and development build commands.
2. Implement Sites, Pools, and Hosts, then bootstrap the first administrator without a shared default password. Pools are created automatically at enrollment, named after the enrolling host, which becomes the pool master.
3. Enroll using short-lived single-use tokens bound to a site. Issue a unique host certificate; support rotation, revocation, and re-enrollment. Verify controller identity during enrollment.
   **Simple enrollment, 2026-09-09:** the Controller can also push a minted token to a reachable
   host's own local API by address and root password, after operator fingerprint confirmation,
   instead of requiring a manual `nex-core-agent enroll` run; the token/certificate contract
   underneath is unchanged. See [the plan](2026-09-09-simple-host-enrollment.md).
4. Display CPU, RAM, virtualization features, the Nex Core version, NICs, disks, reachability, and last observation time.
5. Enforce administrator login and authorization in the API from the start; limit agent credentials to the enrolled host. Add scoped operator/viewer roles before the supported release in Phase 8.
6. Persist asynchronous operations and audit attribution. Serialize conflicting VM operations and use stable request/resource IDs. Reconcile unknown outcomes before retrying; timeout alone does not prove failure.
7. Implement the monitoring contract above: authenticated heartbeats, bounded local health checks, fresh/stale reporting, and reconnect reconciliation. Add checks alongside the resources introduced in later phases.
8. Deliver the living API reference, HTTP OpenAPI contracts, generated browsable documentation, and runnable contract/coverage checks described above; cover enrollment, inventory, monitoring, commands/results, and operations actually implemented in this phase.

**Gate:** create a site, enroll a host so it becomes the pool master of its automatically created pool, revoke the host, reject unauthorized requests, and restart either process without losing identity or duplicating operations. Interrupt the controller connection, fail a libvirt check, and stall a probe: show distinct stale/degraded outcomes, keep other reports responsive, and recover with fresh observations. Offline hosts show stale/unknown state; monitoring never issues an automatic VM restart.

### 2. One-host VM management

1. Register host-local storage at pool level with explicit host accessibility.
2. Register a pool network and map it to the host's existing guest network/bridge. Keep management connectivity intact; defer editing host interfaces to Phase 4.
   **Pool VLAN networks, 2026-09-09:** a pool network is name + uplink NIC name + VLAN ID; Core creates an additive per-VLAN guest bridge without touching the management bridge, so this does not wait for the Phase 4 staged-apply gate, which still covers host interface/address edits. See [the plan](2026-09-09-pool-vlan-networks.md), which also records the guest tools direction.
3. Upload a guest ISO with size/path validation and free-space checks. Create a VM with selected CPU, RAM, storage, disk, network, and firmware.
4. Implement list/details, start, graceful shutdown, explicit force-stop, restart, edit while stopped where required, and deletion with separate disk-retention choices.
5. Implement authenticated browser console sessions with short-lived authorization. Keep raw VNC endpoints inaccessible from untrusted networks.
6. Validate host capacity and storage/network compatibility before accepting placement. Expose progress and useful failure reasons.
7. Use VirtIO defaults for supported Linux guests; record CPU, machine, and firmware choices. Distinguish current hardware configuration from edits awaiting shutdown/restart. Show guest-agent availability rather than assuming it exists. **Guest tools observation and console features, 2026-09-09:** per-VM guest agent, OS, hostname, addresses and console channel readiness are reported and shown; see [the plan](2026-09-09-console-features.md).
8. Provide a scoped inventory table, VM summary, visible task history, fresh/stale status, and basic CPU/RAM/disk usage. Add deletion protection and keep force-stop visually separate from graceful shutdown. The detailed UI sequence is in the companion review.
   **Usage observation, 2026-09-16:** each active VM is sampled with `virsh domstats --raw --nowait`; Core reports CPU, memory and disk on host inventory, Controller passes them through on `GET /api/v1/vms`, and **VM details** renders the nullable values (fresh/stale on the panel heading; no separate sample-age row). `--nowait` is required, not an optimisation: a probe that takes the domain's job stalls behind a long shutdown on the very VM being watched. Disposable-host and real-browser verification passed for both a running VM's measurements and a shut-off VM's absent sample. See the [VM foundation debts](2026-09-14-production-readiness-handoff.md#vm-foundation-added-2026-09-16).
   **Usage history, 2026-09-17:** Core now also records VM and host history on each host (5 s, 1 min, 1 h and 1 day tiers, up to a year) and serves it, with an on-demand 1-second realtime view under a host-owned lease, through an outbound stats channel; the **Stats** tab charts it. State owner: Core; a replacement Controller shows the same history. Implemented in the working tree with simulated-host evidence; remaining checks are in the [usage history plan](2026-09-16-usage-history.md#remaining-work).
   **Host hardware and sensors, 2026-09-22:** Core discovers its own hardware and records hwmon temperatures in host usage history; the host **Stats** tab gains a hardware panel, gauges and a temperature chart. State owner: Core. Implemented in the working tree with simulated evidence only; the real-hardware walkthrough belongs to the project owner and is outstanding. See the [hardware and sensors plan](2026-09-22-host-hardware-and-sensors.md).
   **VirtIO driver media, 2026-09-17:** the second driver disc is complete. **0.3.2** ships
   Fedora virtio-win 0.1.302 on the Core ISO with retained Fedora corresponding source
   ([ISO build](../qa/iso/2026-09-17-core-0.3.2-build.md)); hosts seed `/usr/share/nex/media/virtio-win.iso`
   and the create wizard can use it. API impact: existing contract; no schema change.

**UX acceptance for Phases 1–2:** follow the [phase-fit criteria](2026-09-08-feature-and-ux-review.md#ux-opportunity-and-phase-fit) for enrollment, useful pool health, staged VM creation, safe homelab defaults, ISO drag/drop with a file-picker alternative, and read-only observed network relationships. Review real desktop/narrow-screen and keyboard operation, including empty, unauthorized, stale, failed, and interrupted states. Prepare semantic theme tokens while retaining the approved light default; a dark theme remains a follow-on candidate.

**Gate:** create and install a guest entirely through Nex Controller, use its console, reboot it, and verify persisted data. Controller outage leaves the VM running; reconnect refreshes inventory and resolves unfinished tasks.

**Phase 2 status — CLOSED 2026-09-17** (project owner). Gate5 functional evidence
(`[gate report](../qa/2026-09-17-phase2-gate/checks.md)`), BUG-0256 + admission on published
0.3.1 (`[focused lab](../qa/2026-09-17-phase2-focused-lab.md)`), VirtIO on 0.3.2
(`[ISO build](../qa/iso/2026-09-17-core-0.3.2-build.md)`). Owner decisions and accepted
residuals: `[close record](../qa/2026-09-17-phase2-gate/CLOSED.md)`.

- [x] Item 1: host-local storage registered and adopted with host accessibility (gate5 row 2).
- [x] Item 2: pool network mapped to the existing bridge, management connectivity unchanged (gate5 row 2). Limit: no real-switch VLAN test.
- [x] Item 3: ISO upload and VM creation with selected shape (gate5 rows 3, 4). Limits: host-side ISO free-space refusal not reached (Controller staging refused first); drag-and-drop used a synthetic DOM event with a real file.
- [x] Item 4: list/details, power, retain/wipe delete, and running-VM NIC edits (gate5 rows 11/15/16 plus 0.3.1 focused lab for BUG-0256). Limits: needs_attention variant not induced; single-interface model replacement still 400 (accepted residual); stop-first refuse-vs-queue accepted as implemented.
- [x] Item 5: ticketed browser console, no network VNC/SPICE listener (gate5 row 5).
- [x] Item 6: dedicated-memory and free-storage admission refusals proven on published 0.3.1 focused lab.
- [x] Item 7: guest-tools reporting shown in gate5; pending-unplug indicator deferred; CORE-1 tradeoff accepted for Phase 2.
- [x] Item 8: typed-name confirmation plus appliance protection accepted as deletion protection for Phase 2 (BUG-0253 fixed).
- [x] UX acceptance: Phase 2 closed with accepted residuals (empty-pool captures, queued/failed create polish, console attach lag) recorded in the close record — not blockers.
- [x] Gate: closed. Functional clauses on gate5; BUG-0256 lab-verified on 0.3.1; VirtIO on 0.3.2. G6 external replacement accepted for Phase 2; same-host redeploy deferred.

### 3. Nex Core packaging and installer ISO

1. Produce `nex-core-agent.deb`, a `nex-core` host metapackage, and `nex-controller.deb`.
2. Prove package installation and removal behavior on clean stock Debian before remastering media. Package removal must not remove guest disks.
3. Build a Debian Installer ISO with preseed, branded boot text, Nex packages, complete offline dependencies, and firmware for the supported hardware list.
4. Keep target-disk selection and erase confirmation interactive. Clearly separate OS installation from optional VM-data storage provisioning; do not format additional disks implicitly.
   **Software RAID, 2026-09-22:** a mirrored system disk (md RAID1) and degraded-but-usable ZFS repositories with a disk-replace operation are proposed, with Btrfs RAID provisioning recommended against, in the [software RAID plan](2026-09-22-software-raid.md). Awaiting project-owner decisions; nothing implemented.
5. Provide first-boot hostname, management network, controller address, and enrollment setup through a minimal local interface. Generate identity and SSH keys per installation.
6. Preserve the Debian signed boot chain, support UEFI/Secure Boot, and publish image checksums plus package/build manifests. Add legacy BIOS only if required by the initial hardware targets.
7. Use a signed APT repository for updates; the ISO and package install must produce the same host configuration.
8. Deliver the host-local web UI and Controller appliance bootstrap below, reusing the VM lifecycle and console operations proven in Phase 2.

**Gate:** install without internet, eject media, reboot, enroll, and create a VM. Test multiple disks, absent network, representative Intel/AMD hardware, and recovery access. No credentials or private host keys are cloned into the image.

Preseed supports package/repository and installation configuration. Offline operation still requires all necessary content on the media. [Debian preseed](https://www.debian.org/releases/trixie/amd64/apbs04.en.html), [Debian Live installer options](https://live-team.pages.debian.net/live-manual/html/live-manual/customizing-installer.en.html)

#### Artifact lab sign-off — 2026-09-18

The [0.3.2 artifact lab](../qa/2026-09-18-release-artifact-lab.md) signs off these
bounded Phase 3 checks for its recorded source/archive hashes, excluding later
storage edits. This is an unpublished local build, not full phase acceptance.

- [x] Real package/appliance/ISO builds, matching embedded source and package
  identity, signed appliance manifest and ISO/source-manifest hash binding.
- [x] Controller package installation, authenticated startup, service restart,
  crash recovery, same-version reinstall and reboot with preserved site identity.
- [x] Core package guard and service lifecycle with simulated installer preparation;
  separate exact-ISO offline BIOS/KVM installation, first boot and reboot.
- [x] Standalone bundled appliance boot, DHCP, pinned TLS, authentication and
  bootstrap-secret cleanup in a disposable VM.
- [x] BIOS/KVM and UEFI/TCG boot-menu review; nested KVM execution and persisted
  host marker/domain definition; Core service restart on the installed ISO host
  with explicitly configured loopback management in the NIC-less lab.
- [ ] Full Phase 3: exact ISO → local appliance deployment or external enrollment
  → first managed VM, multi-disk safety, interrupted installation/bootstrap and
  recovery, real management networking, representative Intel/AMD hardware and
  host Secure Boot. Current-build UEFI installation remains untested.
- [ ] Package removal preserving guest disks, cross-version upgrades and green
  hosted release checks are not established by this lab.

Phase 0 gains bounded nested-KVM evidence, not new guest networking/data-recovery
acceptance. Phase 8 remains open: same-version reinstall is not a populated-host
upgrade or a compatibility/recovery rehearsal. Existing Phase 2 closure and
Phase 4's separate acceptance remain unchanged.

API impact: **none** — evidence/status updates only; no client-facing or
Controller/Core operation, command, stream or result contract changes.
Guide impact: **updated**, verification evidence only. State owner: **none**.

#### Host-local UI and Controller appliance bootstrap

**Agreed 2026-09-08; Phase 3 requirements.** The bounded appliance/package,
local UI and separate-host recovery slice is implemented and lab-accepted in the
[appliance plan](2026-09-08-controller-appliance.md#acceptance-evidence). Exact current
ISO installation/service checks are signed off above; integrated bootstrap/enrollment
and the full Phase 3 gate remain open; see the
[prioritized handoff](2026-09-14-production-readiness-handoff.md#recommended-next-work).
The requirements below remain the acceptance target, not evidence that every path ran.

Every installed Nex Core host serves
a small local web interface for setup, host/VM status, basic local control and
recovery. A first-time operator needs one physical server and a browser, without
an existing VM or external Controller. The appliance acceptance record identifies
which host-served deployment and recovery paths were exercised and their limits.

Deliver after Phase 2 proves local VM provisioning, power operations and console
authorization, and alongside Phase 3 packages/first-boot integration. Reuse those
operations rather than building another VM-management implementation. Central
Sites/Pools, placement, storage policies and normal administration remain in Nex
Controller; the local UI acts only on its own host and eligible resident VMs.

**First-server workflow:** install Core → reboot → open the host address shown
on its local console → authenticate → choose one of:

| Choice | Planned behavior |
| --- | --- |
| Create a new environment | Select existing usable storage, Controller CPU/RAM, a supported management network/address and a new administrator credential; review, deploy and boot a verified Controller appliance. |
| Connect to an existing Controller | Verify Controller identity and use a short-lived site-bound enrollment token; the host becomes the pool master of a new pool; works with a Controller on another host, an external supported Debian machine/VM or a remote reachable network. **Implemented from the Controller side 2026-09-09:** **Add host** by address and root password pushes the token to this host's local API after operator fingerprint confirmation ([plan](2026-09-09-simple-host-enrollment.md)); the host-initiated form of this choice remains Phase 3 work. |
| Recover this host's Controller | Inspect the existing appliance, open its console and perform explicitly confirmed local power recovery without needing the Controller API. |

Bundle a versioned appliance with the installation media for offline bootstrap;
also allow a signed compatible download or local upload. Verify its signature,
integrity, format and resource requirements before import. Existing storage
selection does not format disks. The Phase 3 installation network profile must
provide a supported guest-capable management bridge at first boot, configured as
part of the reviewed installation choices before that network becomes live.
The appliance selects that existing bridge so its address is reachable from the
operator's browser. On an existing host without a suitable bridge, give an
actionable preflight blocker instead of silently reconfiguring its live management
interface. General network editing keeps its Phase 4 staged-apply/recovery gate.

Generate instance-specific keys and credentials on first boot. Use protected,
single-use bootstrap material to initialize the administrator, first Site
and host enrollment (which creates the host's pool automatically), then remove that material. Show durable progress and the
verified Controller URL. Adopt the already-created appliance into Controller
inventory with its stable VM UUID and host assignment; never recreate it during
handoff. Preserve local bootstrap operation IDs so browser refresh, lost replies,
host restart or retry cannot create duplicate appliances or memberships.

**Local control and authority:** expose current host health, resident VM state,
authorized consoles, and supported start/graceful-shutdown/explicit force-stop
actions through the same agent-side validation, operation journal and locks as
central requests. Record the local actor and reconcile local results and observed
state with Controller after reconnect; Core retains ownership of durable VM
configuration and Controller presents host-reported state. Local credentials and
console recovery must work during an outage.
Verify TLS identity from the local console, use per-installation authentication
without a shared default password, and scope the local HTTPS API to the management
interface with authorization, CSRF protection and short-lived console sessions.
Do not expose libvirt sockets or require public inbound host access for remote
Controller management; agents retain their outbound authenticated HTTPS flow.

An unreachable Controller is not permission to override VM placement, migration,
disk ownership or future HA fencing. Allow local starts only when this host's
exclusive execution/storage ownership is established; otherwise block with a
recovery reason. Serialize central and local operations, reconcile before central
automation resumes, and never let local controls force takeover of uncertain
shared disks. Later HA-managed resources must keep their separate authority gate.
Keep arbitrary VM creation/deletion, pool policies, migration and advanced storage
management in Controller; the bootstrap action creates only its appliance.

**Boot and recovery:** start the appliance through local libvirt autostart after
its local disk and network are ready. Its boot dependencies must not require a
Controller-managed service to be running first. Retain the local UI and console
when the appliance is stopped or broken. Protect the appliance from accidental
deletion and explain management interruption before its shutdown or host maintenance.
One Controller may manage multiple Pools; one per Pool is not required.

Store a complete, consistent Controller database/identity recovery export outside
the hosting server and test its recovery before claiming self-hosting recoverable.
Use SQLite's supported backup mechanism or a quiesced backup, with matching
configuration and identity material; copying a live database file without its
transaction/WAL state is not a valid export. Provide a
documented manual replacement-appliance import of that export in this slice (from Phase 6 the
same export carries the Controller-scoped backup schedules, jobs, run history and catalog);
include CA/identity keys and required configuration with secret protection, and
ensure the original instance cannot resume before activating its replacement.
A same-host snapshot is insufficient. This minimal manual Controller recovery
path is intentionally required in Phase 3; VM snapshots remain Phase 5 and
independent VM backups/policies remain Phase 6. Automatic failover and concurrent
active Controllers remain outside this slice. A single-server failure takes its
local Controller down; external deployment offers a separate failure boundary,
not automatic HA.

**API impact: new contract (planned implementation); none for runtime in this
documentation change.** Browser → authenticated host-local setup/control API →
agent/libvirt operation and durable result → local UI, followed by Controller
bootstrap/enrollment, appliance adoption and reconciliation. Reuse the Phase 1
enrollment/report/operation and Phase 2 VM/console contracts where sufficient;
extend both sides for bootstrap handoff and local-operation provenance as needed.
Specify validation/errors, scope, idempotency, pending/unknown results, outages,
reconnect ordering and Controller/Core/appliance version compatibility. Add local
HTTP and console contracts, machine-readable schemas and generated reference to
the [API reference](../API.md) with implementation; no endpoints are invented now.
**Guide impact: none now** — this is a future delivery decision. Implementation
must update installation, first-server setup, local control, external enrollment
and recovery guidance with actual walkthroughs in the same slice.

**Acceptance gate:** from an empty disposable server, install offline, deploy the
Controller through the local browser UI, create the first Site, enroll the first Host as pool master of its automatic Pool,
and reach central VM management without manually creating a VM elsewhere. Repeat
with an external Controller. Reboot the host with the Controller initially down;
verify independent appliance autostart, local UI/console access and stable IDs.
Interrupt download/import/first boot/enrollment and retry without duplication;
test invalid signatures, insufficient capacity, missing bridge, unauthorized
access, expired bootstrap material and incompatible versions. Stop Controller
and exercise permitted local controls; race them against central commands and
reject starts with uncertain/shared-disk ownership. Recover the exported database
and identity onto a replacement appliance with the original excluded, and verify
existing-host reconnection without duplicate control. Review desktop/360px and
keyboard flows, empty/loading/error/stale states and disruptive confirmations.
Include runnable API/failure checks, updated guide/reference, retained matching
appliance source/notices under the existing licensing gate, and independent review.
Full integrated acceptance remains open. The appliance plan and artifact lab
record bounded checks already passed; they do not close the remaining gates above.

### 4. Multi-host pools and shared storage

**Current acceptance status (2026-09-20): closed for disposable virtual-lab scope.** S1–S6 are
implemented in the dirty working tree. The final `f714` source validation passed `go test -race
-count=1 -timeout 20m ./...` and the 374-test Python suite; that is source evidence, not a release.
Reviewed native evidence includes the repeated G1 join/eject lifecycle, missing-uplink failure,
autonomous recovery and cleanup, and S6-14 boot-time restriction, API/direct-hook/authority-offer
refusals, autostart suppression, rejoin and post-clear normal start/stop. `BUG-0372` through
`BUG-0381` are fixed; the newest Core uses a private boot marker. The final `f714` S6-18 strict
harness is **partial/FAIL** because it received a different safe eject refusal for a live
shared-storage reservation and exited before timed-suppression observation. The older scoped S6-18
pass is separate historical evidence.

Phase 4 and the initial manual-recovery gate are **closed for disposable virtual-lab scope**
(project owner, 2026-09-20). OD-6 is **decided**: the strict unreachable-ready-member rule is the
approved default (fence or fail-closed; silence is never agreement). Permanent limits stay open
forever for this lab class: physical/BMC fencing, five-host scale, package-installed hook
enforcement, authentic owner-conflict S6-12/16 fixtures, OD-5 no-escape, and remaining C-class
matrix rows. No production-readiness or release claim follows from these results. Post-close S2
maintenance native revalidation on Core `327b7ec9…` passed ordered prep/core/autostart/restarts/hook/hosts
(see [native acceptance report](../qa/2026-09-19-phase4-native-acceptance.md)).

**Historical implementation snapshot (2026-09-19):** Core-local member join, member pull of pool-shared
NFS/SMB (and eligible clustered iSCSI), guest VLAN networks and ISO library metadata on those
shared repositories are implemented. Members materialize locally and report per-host access;
Controller mirrors observations only. The Controller-driven join (“Add host to pool” on the pool's
Hosts tab, with the master issuing a single-use invitation and the joining host running the
exchange itself) was implemented with unit and contract tests. At that date, its native two-host
walkthrough and browser review had not run. Also implemented in the working tree, with unit,
Controller and contract tests and at that date no native or browser evidence: **host maintenance
mode** (a drain flag the pool master keeps in its own membership record, refusing new placement,
definition and start admission and autostart-by-placement on that host, blocking no running guest
and no stop; since independent review the drained host also refuses starts, resumes and
start-after-create **itself**, before any master round trip, so a guest on its own local storage is
covered and the refusal holds with the master unreachable, and it suppresses libvirt's own
autostart flag while drained so a reboot does not return its guests — `BUG-0346`, `BUG-0347`),
the **member authority replica** (the master's signed, generation-numbered copy of its durable
authority, stored read-only by every ready member and acted on by none), and **staged remote
management network edits** (a host renders, applies, times and rolls back its own
`/etc/network/interfaces`, `/etc/dhcpcd.conf` and `/etc/resolv.conf`; the Controller submits one
durable step at a time, an unconfirmed change is restored by the host at its deadline or after a
reboot, and interface renames and guest VLAN bridges are deliberately out of scope). Since
independent review the host reconfigures `br0` **in place** rather than bringing it down and back
up, which used to cut off every guest attached to the management bridge (`BUG-0341`), and both
rollback triggers measure the window from the host's own boot rather than from the wall clock, so
a host that comes back with a wrong RTC still rolls back (`BUG-0340`). At that date the network
slice had no native evidence: its namespace test skips without
`ifupdown` and has never run, so the tests show which commands the host issues and nothing about
the kernel's reaction. Its *Roll back now* control, which submitted a step Core refused, now
performs the rollback on an applied, unconfirmed change and reports it as an operator rollback
rather than a failure (`BUG-0327`). Also implemented in the working tree:
**planned master change and member eject** (one durable, resumable Core state machine on the pool
master, a pool-CA-signed hand-off and re-point, a durable freeze that refuses every pool authority
write while it runs without stopping or blocking a running guest, an operator cancel until the
hand-off, an ejected host re-initializing as the master of its own new single-host pool, and a
Controller that relays one command and keeps no stage, generation or membership of its own). At
that date it had no native multi-host run or browser review. Also implemented in the working tree:
**fence-verified manual promotion and safe rejoin** (an operator names one surviving member; Nex
Core on that member proves replica integrity, replica supremacy and accepted VM ownership, produces
its own fence proof by powering the lost master off and observing it, and only then takes the pool
authority at generation + 1; a host that cannot prove it holds the pool's current authority
restricts itself before any start path is exposed and rejoins automatically only after it has
reconciled). It carries two honest limits that are part of the design and not defects: at that
date only a `virsh` fence profile could be recorded, so a physical pool could not be
fence-promoted — [automatic host fencing](2026-09-22-automatic-host-fencing.md) lifts that in the
working tree, where BMC identity verification (SMBIOS UUID reported over
`POST /api/v1/pool/advertise`, certificate-pinned Redfish and `ipmilan` profiles) and in-band
account provisioning let a physical pool record a BMC fence profile, though **no physical
management controller has been contacted and no host has been powered off**; and an opted-in pool
whose master restarts while one other
ready member is permanently dead **stays restricted**, with no escape action and none to be added
(owner decision OD-5, 2026-09-19) — the recovery is to bring that host back or restore its Core
state. Its initial evidence was in-process multi-host Go `-race` tests, Controller tests and
contract checks with a fence double. Later scoped native promotion observed external `virsh`
power-off, but that does not certify physical fencing, workload continuity or duplicate-writer
safety. Current reviewed evidence and its limits are in the
[Phase 4 native acceptance record](../qa/2026-09-19-phase4-native-acceptance.md). **The disposable
virtual-lab gate is passed** with documented permanent limits; physical/BMC, five-host,
package-install and authentic owner-conflict cases are not claimed. See [join guide](../guide/join-a-pool.md),
[maintenance guide](../guide/host-maintenance-mode.md),
[pool authority guide](../guide/pool-authority-and-recovery.md),
[management network guide](../guide/change-management-network.md) and
[QA note](../qa/2026-09-19-pool-member-config-converge.md).

1. Enroll a second host; join it to an existing pool as a member host under that pool's master (“Add host to pool”), provide master change and member eject through [pool authority transfer](#pool-authority-transfer-and-master-succession), and provide pool-wide inventory and explicit placement approved by the master. A join is a Core-to-Core convergence operation: the master retains the durable pool membership and every pool-wide configuration record, sends each applicable generation to the member, and retains that member's result. At minimum reconcile logical networks (including VLAN/uplink mappings), pool storage registrations and the member's real access state; materialize only configuration applicable to that member, never another host's local storage or runtime state. The Controller asks the master and displays durable per-member pending, succeeded, failed or needs-attention results; it neither stores a second desired-state copy nor runs the reconciliation. On member reconnect, reboot or interrupted join, the master resumes reconciliation from its durable record before reporting the member ready for placement on an affected resource. Require authenticated Core-to-Core calls, capability and identity checks, generation/idempotency rules, narrow member credential delivery, and safe retry/outage behavior. Until this phase every enrolled host remains the master of its own pool.
2. Add existing NFS storage through the pool UI. Validate access, write permissions, identity, capacity, and mount health per host before marking it usable.
3. Distinguish local/shared storage visibly. Track disk attachment and exclusive VM execution; never launch a second writer because a host stopped reporting.
4. Add logical LAN bridges/VLAN mappings with host compatibility checks. Do not assume bridge names mean equivalent connectivity. Remote host network edits require staged apply, explicit confirmation, and timed recovery if management connectivity is lost.
5. Add maintenance mode, with new placements blocked and remaining guests clearly reported. Refuse unsafe host removal.

**UX acceptance:** guide “Add host to pool” (joining an existing pool as a member under its master) through identity/capability checks; explain storage location, per-host access, capacity, and failure consequences. Pair network relationships with accessible details and a preview of affected VMs/management access; verify staged apply and timed recovery before offering remote edits. A visual view does not require a freeform network canvas.

**Gate:** join a disposable member to a configured pool and prove that the master reconciles every applicable logical network/VLAN and pool storage registration to it, recording per-member success and actionable failure without Controller-held desired state. Exercise incompatible or missing uplinks, unavailable/unwritable storage, interrupted Core-to-Core delivery, member reboot/reconnect and Controller replacement; retry must converge without duplicate bridges, mounts, credentials, registrations or writers. VMs can be placed on either compatible host, inaccessible storage prevents placement, shared free space is reported once, and a disconnected host does not trigger duplicate VM startup.

#### Pool authority transfer and master succession

**Decision: project owner, 2026-09-14.** Planned master change and member eject are implemented in
the working tree (status under *Transfer steps* below); manual promotion after permanent master
loss and automatic succession remain planned and not implemented. *(Status superseded 2026-09-19:
manual promotion is now implemented in the working tree too — see the note under the recovery
decisions below. Automatic succession is still planned and not implemented, and both gates stay
outstanding. The design decision this bullet records is unchanged.)* One design covers planned
master change, member eject, manual promotion after permanent master loss
([Phase 7](#manual-recovery-after-permanent-host-loss)) and automatic succession. Phase 7 and
[Phase 9](#9-optional-per-pool-ha) reference it; they do not redesign it.

**Recovery decisions: project owner, 2026-09-19.** The decisions themselves are unchanged and are
recorded below as the owner stated them. **Implementation status, 2026-09-19: fence-verified manual
promotion and safe rejoin are implemented in the working tree** — the per-item status lines under
*Member replica*, *Transfer steps* and *Restricted hosts* say what is covered and by what evidence.
Automatic succession remains planned and not implemented. The implementation-only evidence stated
below was superseded by scoped reviewed native results recorded in the
[Phase 4 native acceptance record](../qa/2026-09-19-phase4-native-acceptance.md), including
join/eject and promotion cases. Phase 4 and the initial manual-recovery gate are **closed for
disposable virtual-lab scope** (2026-09-20); automatic succession and physical-scale gates remain
planned/outstanding.

- **Preserve the pool.** Promotion changes the master, not the pool ID. Preserve VM,
  repository and network identities, names and configuration. Do not recreate the pool or
  show a returning stale master as a new independent pool. Healthy member workloads are not
  restarted merely because authority changes.
- **Manual promotion first.** An operator selects the surviving member and explicitly requests
  promotion. An uncooperative or unreachable old master requires verified fencing before
  activation; operator assurance, a missed heartbeat or a successful fence request alone is
  insufficient. Automatic master succession remains a separately gated future capability.
- **Automatic safe rejoin.** The original host may automatically rejoin as a member without
  another approval once Core validates its identity, accepts the current authority and
  reconciles VM ownership and storage. Until then block stale control operations, local and
  libvirt autostarts, other workload starts and conflicting storage writes. Enforce this on
  Core before exposing start paths, including during Controller outages. Conflicts or an
  inability to establish current authority leave the host restricted with recovery access;
  do not guess or start a second copy of a VM recovered elsewhere.
- **VM visibility is not disk availability.** Recover VM definitions from durable member
  replicas even if the failed host's disks are unavailable. Restart elsewhere requires
  accessible validated disks or a validated replica/restore plus exclusive ownership and
  compatible placement. Independent local ZFS repositories do not replicate VM data.
- **Rejoin and HA policy are separate.** Rejoin alone neither restarts nor moves VMs. Manual
  recovery uses the Phase 7 gate; automatic restart and optional return follow the configured
  pool/VM HA policies and Phase 9 safety gates. The existing default is to leave recovered
  VMs on their healthy host, with explicit manual return or opt-in automatic return. This
  discussion does not authorize general automatic load balancing or implement HA.

- **Member replica.** The master pushes a signed, monotonically generation-numbered replica of
  its durable pool authority to every ready member: the pool CA key encrypted for members,
  membership, VM ownership, epochs and pending updates (`pool-vms.json`), and the pool storage,
  network and native-cluster records. Members store it read-only and never act on it unless
  promoted; until then it is not an independent authority. Each Core reports its replica
  generation and freshness as observed state; Controller stores none of it. This is the
  recoverable copy required by Phase 7 item 2.
  **Historical implementation status (2026-09-19): implemented in the working tree, native
  acceptance outstanding.** The
  master signs with the pool CA key, seals that key per member (ECDH from the member's certified
  peer key, HKDF-SHA256 and AES-256-GCM, all stdlib), declares per-section digests so a truncated
  copy is refused, and pushes on its probe tick whenever its authority content digest changed or a
  member is behind. Members store `<state-dir>/pool-replica.json` at mode 0600 and act on nothing
  in it; no code path in this release unwraps the sealed key. Inventory snapshots and SMB
  credential receipts are deliberately not replicated, and no host reports the master's own
  authority generation, so the Controller derives none. Verified by Go `-race` unit tests,
  Controller tests and contract checks only. Later reviewed G1/S5/S6 evidence observes signed
  replicas during scoped lifecycle cases; complete replica-push and master-restart acceptance,
  including its remaining browser coverage, is still open.
- **Transfer steps**, with the same authority and exclusion checks for every trigger:
  1. For a cooperating master, freeze authority writes (new placements, membership,
     storage/network and VM ownership changes); running guests continue.
  2. Replicate to the target and confirm the current generation. If the master is unavailable,
     validate the surviving durable replica's freshness and accepted ownership state instead;
     define and test that proof before implementation. Missing or ambiguous authority records
     block promotion; fencing alone cannot make an incomplete replica authoritative.
  3. When the old master is not cooperating, obtain verified power-off through the existing
     fencing proof mechanism. A timeout or heartbeat loss is never proof. *(Amended 2026-09-22:
     a watchdog exclusion proof combines quorum, the target's own armed hardware-watchdog report
     and an elapsed bound; see [automatic host fencing](2026-09-22-automatic-host-fencing.md#rule-change).
     Implemented in the working tree (slices 4a-4c), with pre-merge review fixes applied
     2026-09-22; verified by Go tests and a nested lab only — no promotion has been driven by a
     watchdog proof anywhere, and no physical watchdog has been armed or expired.)*
  4. The target activates as master at generation + 1 and signs the new authority.
  5. Members re-point to the new master and accept only the higher generation.
  6. The old master is demoted, or refused by generation/epoch check when it returns, and
     automatically rejoins only as a member after the reconciliation gates above. Eject is the
     same flow with the target staying master and the ejected host's membership removed at the
     new generation.
  **Current status (2026-09-20): all six steps are implemented in the dirty working tree; full
  acceptance is outstanding.** Steps 1, 2, 4, 5 and 6 cover the planned master change and the member eject for a
  cooperating master. Step 3 and the promotion it gates are implemented too, as a candidate-driven
  machine rather than a master-driven one: the promotion command goes to the surviving member,
  which probes the old master itself, refuses outright if it answers and still holds the authority,
  and otherwise produces a fence proof it made itself — an independently observed power-off of the
  recorded profile's target, digest-bound to this pool, this excluded host, this operation and this
  generation, at most fifteen minutes old. A returned fence agent status, a timeout, a missed
  heartbeat or an operator assurance cannot construct one, and the whole proof is re-checked
  immediately before activation. Scoped native promotion has observed external `virsh` power-off;
  it does not establish physical-fence, workload-continuity or duplicate-writer safety. Two
  deliberate differences from the wording above: step 5's membership move is carried by a small
  pool-CA-signed re-point notice rather than a replica push, because a member cannot accept a
  replica from a host it does not yet consider master, with the ordinary replica push following
  immediately; and step 6 has no `demote` call — the outgoing master demotes itself on the new
  master's signed re-point notice, returned with the accepted activation, so it acts on signed
  evidence rather than on its own assumption. The read-only authority probe doubles as a pull-based
  re-point offer, which is how a host that missed a push converges from whichever host it can reach
  without trusting a stale one. The master now reports its own authority generation
  (`managed.authority`), which is the absolute reference the member-replica ladder previously
  lacked. Verified by in-process multi-host Go `-race` tests over the real mutual-TLS peer
  transport, Controller lab tests, UI component tests and contract checks. Later scoped native and
  browser evidence covers selected transfer, eject and promotion cases; the complete multi-host
  recovery matrix remains open.
- **Implementation note, 2026-09-19: replica supremacy was tightened after independent review, and
  the tightening is not yet confirmed by the project owner.** The owner's recovery decisions above
  are unchanged and are not restated here. What changed is a detail of how step 2's supremacy check
  treats a host that does not answer. The closure plan's original wording let an unreachable host
  pass that check and relied on step 3's fence to cover it. An independent review of the unreleased
  work reproduced the consequence: with the master dead and the two surviving ready members
  partitioned from each other, promoting each in turn made **both** of them master of the same pool.
  An unreachable ready member holds the same authority replica the candidate does, so it could be
  promoted in exactly the same way, and its silence proves nothing. The implemented rule now blocks
  a promotion while such a member is unreachable **unless the same promotion fences it**, in which
  case the operator is shown and must confirm every host that will be powered off, and the exclusion
  proof covers each of them. A blocking host with no usable fence profile refuses the promotion
  outright. This is stricter than the original wording and is **approved (OD-6, project owner,
  2026-09-20)**: fail-closed; silence is never agreement. See
  [the closure plan](2026-09-19-phase4-closure.md#decisions-for-the-project-owner).
- **Future automatic succession**, deferred beyond the initial manual-promotion slice,
  elects a new master only with a strict quorum majority of current
  members (counted by the established pool quorum layer, not a Nex consensus), a verified fence
  proof of the old master, and the most recent replica generation among the majority. Pools that
  cannot form a majority, including two hosts, never auto-elect and use manual, fence-verified
  promotion. *(Amended 2026-09-22, project owner: a two-host pool auto-elects its survivor when it
  holds a valid BMC, storage-lease or witness exclusion proof and passes the same replica-supremacy
  check; without such a proof nothing is automatic. See
  [two-host pool fencing](2026-09-22-two-host-pool-fencing.md#automatic-takeover); planned, not
  implemented.)* Succession does not restart VMs; automatic restart keeps the Phase 9 HA gates.
- **Health after promotion.** A new master re-collects fresh member access observations before
  reporting health (member access is memory-only, BUG-0158); the replica is never health evidence.
- **Fence credentials travel sealed, and every ready member can open its own copy.** The host that
  fences during a promotion is a surviving member, and it cannot be given the material after the
  master it must fence is gone, so each recorded profile's material is sealed per member inside
  that member's replica by the same primitive as the pool CA key. Nex Controller never holds it.
  The consequence is owner-visible and is stated in the guide rather than hidden: any ready member
  of a pool with recorded fence profiles can power off any profiled host, which is the same trust
  the sealed pool CA key already places in a member. Admit only hosts you trust with it.
- **Restricted hosts, and the limit that has no recovery.** A host that cannot prove it holds the
  pool's current authority writes a durable restriction *before* any start path is exposed, refuses
  every workload start and every authority write while keeping stop, console, local recovery and
  reporting, and clears only on evidence. A restarted master restricts itself only when its pool
  opted into fence-verified promotion by recording a usable profile for it. **Owner decision OD-5
  (2026-09-19):** if such a master restarts while one other ready member is permanently dead, it
  stays restricted. No force, override, "declare host lost" or timeout-based escape exists or may
  be added, because none of them is evidence — an observation that a host is off does not prove it
  never took the authority and does not survive it powering on again. The recovery is to bring the
  dead host back or restore its Nex Core state.
- **Placement approval is on the master.** Controller relays the operator's chosen host; the
  master Core validates and approves placement from its own records and fresh target-host
  preflight. Controller mirrors are presentation only. See
  [master-owned placement](2026-09-18-master-vm-placement.md); its native multi-host acceptance
  remains open.

**Planned API impact: new contracts** — Core peer replica push/ack; promotion, planned-transfer
and eject operations; succession election messages; Controller-facing
observed replica generation, transfer status and automatic-rejoin readiness/conflicts.
Controller submits the operator request to Core; Core validates fencing and replica freshness,
persists authority/ownership, reconciles returning members and reports results through
peer and inventory contracts. No Controller-owned recovery journal is introduced.
Master placement admission is now
implemented in the [placement slice](2026-09-18-master-vm-placement.md); its native
acceptance remains open. For the remaining contracts define authentication, generation/epoch checks,
safe retries, reconnect and version compatibility on both sides when implementing.
**State owner: core** — the durable record is the member replica on each Core; a fresh Controller
reads the active master's inventory.

**Initial manual-recovery gate — closed for disposable virtual-lab scope (2026-09-20):** scoped
reviewed evidence covers planned handover, operator-selected fence-verified promotion in two- and
three-host disposable pools, returning-master restriction and safe rejoin, and selected interruption
cases. Permanent limits (physical/BMC, five-host, package-install, authentic owner-conflict
S6-12/16, OD-5 no-escape, broader duplicate-writer/workload continuity) are not claimed. See the
[Phase 4 native acceptance record](../qa/2026-09-19-phase4-native-acceptance.md).

**Future automatic-succession gate — outstanding:** master power-off on three hosts, quorum
loss and fence failure, plus the same identity, rejoin and ownership checks above. This is
separate from both initial manual promotion and automatic VM restart acceptance.

### 5. Snapshots and rollback

1. First run a disposable snapshot/backup API experiment on Debian's installed libvirt/QEMU versions; record supported create, revert, delete, backup, and firmware-state combinations with a runnable reproduction. Implement create, list, named description, revert, and delete for that validated backend. Start with gracefully stopped guests and disk-only snapshots.
2. Capture all supported writable disks and corresponding VM configuration/firmware state. Reject unsupported disk, passthrough, or state combinations before modifying anything.
3. Revert requires an explicit confirmation describing discarded changes. Keep VM configuration and disks matched to the chosen recovery point.
4. Treat snapshot deletion/consolidation as a durable operation. Respect backing chains and active references; never unlink a file merely because metadata deletion succeeded.
5. Check capacity and handle interruption, partial multi-disk capture, and consolidation failure without labelling an incomplete snapshot usable.
6. Add running-guest disk snapshots once supported lifecycle tests pass. Show whether a snapshot is crash-consistent or guest-quiesced; filesystem freeze alone is not a guarantee of application consistency. Ensure thaw on failure.

**Gate:** write marker A, snapshot, write B, revert and verify A; repeat across multiple disks. Delete snapshots and reboot successfully. Interrupt snapshot/consolidation and recover without losing the current VM disk chain.

Snapshots are rollback points on the underlying storage and do not protect against losing that storage. Memory snapshots remain a later explicit capability; disk rollback is the initial guarantee. [Libvirt snapshot semantics](https://libvirt.org/formatsnapshot.html)

**Technology coverage, project owner requirement, 2026-09-12:** VM disk snapshots must not
require ZFS or Btrfs. Validate QEMU/libvirt qcow2 overlay-based snapshot lifecycles on eligible
local, NFS, SMB and exclusive-host iSCSI-backed filesystem repositories; publish a disk-format,
backend and installed-version capability matrix rather than assuming all combinations work.
Disk-only, guest-quiesced and future memory snapshots are distinct capabilities. Storage-native
ZFS/Btrfs snapshots are a separate later integration, described below; registering one of these
filesystems today does not enable that integration.

### 6. Backups, scheduling, and restoration

1. Add pool-level backup repositories, initially an external NFS-backed filesystem in a separate failure domain from primary VM storage. Separate repository credentials and permissions from guest access.
2. Implement full backups of gracefully stopped VMs first. Include every supported disk, VM configuration, required firmware/NVRAM state, format versions, checksums, and a standalone recovery manifest. Preserve vTPM state if supported; otherwise block backup of that configuration explicitly.
3. Export self-contained disk images rather than copies that still depend on source snapshot backing files. Mark backups complete only after data and manifest validation; incomplete uploads must never appear restorable.
4. Add online full backup using the supported libvirt backup API. Coordinate the disk set, report crash-consistent versus quiesced capture, and test guest shutdown or agent failure during backup. Do not copy a live disk with an ordinary file copy. [Libvirt backup API](https://libvirt.org/formatbackup.html), [live full backup guidance](https://libvirt.org/kbase/live_full_disk_backup.html)
5. Add pool policies selecting VMs, schedules, retention counts/age, bandwidth/concurrency limits, and per-VM overrides. Run the scheduler in the existing controller process (the explicit exception below). Record missed runs during controller outages; serialize conflicting disk operations.
6. Only prune completed, eligible backups after a successful new backup; respect protected recovery points and active restores. Do not add incremental chains until dependency-aware retention and recovery are implemented and tested.
7. Restore by default as a new stopped VM into selected pool storage on a compatible host. Generate a new identity/MAC and default to a disconnected or isolated network to avoid collisions with the original.
8. Provide explicit original-VM replacement with confirmation, proven exclusive ownership, and preserved existing disks until the restored guest is validated. Validate storage capacity, checksums, firmware, network mappings, and manifests before registration.
9. Support catalog import/rebuild from repository manifests and a documented recovery command so backups remain useful after losing the controller database. Provide encryption at rest through the repository/storage layer initially and document key recovery independently of the controller.
10. Back up and restore the controller database, configuration, and identity/key material using a consistent database backup mechanism. Restore only one authoritative controller and reconcile agents before accepting mutations.
11. Show last successful backup, last verification, retention preview, repository space, and failed/missed jobs. Add an explicit test-restore workflow; scheduled isolated boot verification can follow manual restore proof. Completed-backup records are not a claim of tamper-proof/WORM storage.

**Decision (project owner, 2026-09-10):** backup and restore orchestration is the explicit exception to the rule that Nex Controller never drives jobs: the Controller runs the scheduler, drives backup and restore jobs, and keeps their run history and the catalog. Backup repository registrations stay pool-scoped on the pool master and re-adopt like other pool settings. Schedules, jobs, run history and the catalog are Controller-scoped: a replacement Controller recovers them only from the Controller backup/restore in step 10 (the catalog can also be rebuilt from repository manifests, step 9); without that backup they are lost, while the repository registrations and every backup already written are not.

**UX acceptance:** provide a short preset-based policy workflow with explicit VM scope, independent repository, schedule/time zone, retention, and downtime/consistency review. Quick application of a reviewed policy reuses the same validated scheduler; display next run and failed/missed jobs without implying a successful job proves recovery.

**Gate:** back up a guest, make its original disks unavailable, restore to another compatible host/storage, boot it on an isolated network, and verify files/application data. Test corrupted/missing backups, full repository, interrupted jobs, retention, lost controller database, and snapshot-plus-backup interactions. Measure actual backup/restore time before promising RPO/RTO.

#### Changed-block tracking and incremental backup follow-on

**Agreed 2026-09-12; planned after verified full backup/restore, not in the first supported
release.** Make changed-block tracking (CBT) explicit rather than treating it as an implied
property of ZFS/Btrfs. Use libvirt disk checkpoints and QEMU dirty bitmaps to identify changed
VM disk regions for incremental backups. Validate persistent qcow2 bitmaps and distinguish
raw/transient tracking limits on the installed versions; neither an existing qcow2 image nor
a snapshot means tracking was enabled or remains trustworthy.
[QEMU dirty bitmaps](https://www.qemu.org/docs/master/interop/bitmaps.html),
[libvirt incremental backup handling](https://www.libvirt.org/kbase/internals/incremental-backup.html).

- Establish a verified full baseline, then publish validated incremental data and manifests
  that identify their exact baseline, dependencies, disks and matching VM/firmware state.
  Restores must depend only on retained backup material, not the original running VM or its
  snapshot chain. Retention must protect every ancestor needed by a retained recovery point.
- Track bitmap/checkpoint validity across backup failure, Core/QEMU restart, crash, disk resize,
  snapshot/revert/consolidation, migration, HA and failback. Preserve continuity only where proven;
  if tracking is absent, inconsistent or otherwise untrustworthy, block the incremental attempt
  and establish a new full baseline with an explained operator-visible result. Never silently
  interpret lost tracking as “nothing changed” or discard the last usable backup chain.
- Keep bitmap/checkpoint manipulation on Core, under the active master's VM/disk ownership and
  serialization rules. Preserve the existing explicit Controller backup-job scheduling/catalog
  exception; it does not authorize Controller to manipulate host bitmaps or become an HA owner.
  Recovery manifests and required keys must remain usable after loss of the original Core and
  Controller catalog, following the existing catalog rebuild and key-custody requirements.
- Present full/incremental selection, supported combinations, last successful backup and last
  tested restore clearly. Discover safe defaults and explain any full-baseline fallback, missing
  chain dependency or incompatible disk; do not expose bitmap identifiers as routine user input.

**Acceptance — outstanding:** perform full plus multiple incremental backups of a multi-disk
guest with identifiable writes, deletes and discards; restore selected recovery points on a
clean host after making original disks and the Controller catalog unavailable. Verify disk and
guest/application data, configuration and firmware state. Exercise corrupt/missing increments,
retention/pruning, full repository, interrupted publication and the bitmap lifecycle events
above, proving either correct continuity or safe full-baseline fallback. Require independent
safety review, runnable regressions in both CI providers and native restore evidence per supported
format/backend/version; a small transfer or successful backup task alone is not acceptance.

#### Storage-native snapshot integration follow-on

**Later, separately validated profiles:** include ZFS dataset/zvol and Btrfs subvolume snapshots
in the roadmap alongside QEMU VM snapshots. Assess native send/receive for subsequent incremental
off-host copies where supported; this is not the same mechanism as QEMU CBT and does not replace
the full VM recovery manifest. The current storage-registration slice still does not create or
alter ZFS pools/datasets or manage native snapshot lifecycles.
[OpenZFS snapshots](https://openzfs.github.io/openzfs-docs/Basic%20Concepts/Datasets/Snapshots%20and%20Clones.html),
[Btrfs subvolumes](https://btrfs.readthedocs.io/en/latest/Subvolumes.html).

Before enabling native operations, define explicitly managed storage scope and protect unrelated
data; a storage snapshot may cover multiple VMs, while nested Btrfs subvolumes are not captured
recursively. Coordinate all VM disks, metadata and required guest quiescing; expose honest
crash-consistent versus application-consistent limits. Do not assume access through NFS/SMB
grants control over a remote NAS's snapshot APIs. Validate create/revert/delete, space exhaustion,
retention dependencies, interrupted operations and clean-host restoration; test native transfer
interruption and source loss before advertising send/receive protection. No native integration
may bypass VM ownership, fencing or snapshot/backup concurrency checks.

**API impact of these roadmap additions: none; guide impact: none** — no runtime capability
changes. **Planned API impact: new/changed contracts** for snapshot/checkpoint capabilities,
backup lifecycle requests and Core-reported validity/results: UI → Controller authorization/job
orchestration within the existing backup exception → master-authorized Core disk work → durable
Core observations and backup manifests. Specify permission/scope validation, failure/retry,
cancellation, concurrency, reconnect and version compatibility on both sides; update API schemas,
reference and runnable negative-path tests plus the operator guide in each implementation slice.

### 7. Migration and host maintenance

Live migration over shared pool storage is part of the first supported release, not dependent on Ceph or HA. Shared disk access is necessary for this initial migration path, but CPU, networking, firmware, and runtime compatibility must also pass validation.

**CPU prerequisite update, 2026-09-17:** the fixed current-pool CPU baseline is implemented
for 0.3.1, with [bounded evidence](../qa/2026-09-17-cpu-baseline.md). It does not implement
migration. Destination compatibility must still be checked against each VM's saved CPU
contract along with the other requirements below; future members may be incompatible.


1. Implement controlled stopped-VM relocation first, with explicit disk transfer when storage is local.
2. Add live migration for supported shared-storage guests after checking CPU model/features, firmware, machine type, networking, storage access, and version compatibility.
3. Use encrypted migration transport and established libvirt operations. Reconcile source and target after interruption; do not equate a control-plane timeout with a successful transfer.
4. Implement host evacuation using those same operations. Prevent migration during conflicting snapshot, backup, restore, or disk-consolidation work.
5. Preserve VM UUID, pool ownership, disk references, and supported snapshot history. Reject unsupported combinations with an actionable reason.

The active pool master owns the durable relocation decision and coordinates source/destination
Core work; Controller loss must not interrupt an accepted move. Serialize migration, manual
recovery and later HA/failback against the same VM ownership record. After an ambiguous cutover,
reconcile both sides before another move or start; do not assume either success or rollback from
a timeout. Return moves use the current authoritative disks, backing chains and firmware state,
never an older copy left on the original host. HA restart after host death is a cold boot with
possible guest/application recovery, not live migration or guaranteed uninterrupted service.

**Gate:** migrate while a guest workload writes data; verify continuity and exactly one active execution owner. Disconnect the migration/control network mid-transfer and recover. Drain, reboot, and return a host to service.

[Libvirt migration requirements and transports](https://libvirt.org/migration.html)

#### Manual recovery after permanent host loss

**Agreed with project owner, 2026-09-12; planned, not implemented.** An operator must be
able to install a replacement Nex Core, connect it to the surviving pool/network storage,
select recoverable VMs left by a permanently failed host, and boot them without recreating
their configuration or restoring intact disks from backup. This is an explicit Phase 7
recovery workflow and Phase 8 release rehearsal, independent of optional automatic HA.
Phase 4 supplies pool authority, storage access and execution-ownership prerequisites.
NFS, SMB and exclusive-host iSCSI each require their own passing recovery gate; current
storage discovery or pool join does not provide this capability.

1. **Preserve recoverable VMs before failure.** Core must publish a versioned, integrity-checked
   recovery record with the surviving network storage, containing stable pool/VM identities,
   VM configuration, all disk references and backing-chain relationships, network mappings,
   and required firmware/NVRAM and supported vTPM state. Protect sensitive material with
   authenticated access and recoverable key custody outside the failed host. Specify atomic
   publication, configuration/data consistency, freshness and schema compatibility before
   implementation. Disk files alone are not complete recoverable VMs; missing, stale or
   unsupported state must be identified and block unsafe boot rather than guessed.
2. **Recover pool authority, including master loss.** Cover failure of a member, the master,
   and the only Core in a pool. A replacement Core gets its own host identity and recovers
   the original pool identity and accepted VM identities from trusted Core-owned recovery
   records. The master journal remains authoritative during normal operation; a lost master's
   authority is recovered from the surviving member replica through
   [pool authority transfer](#pool-authority-transfer-and-master-succession), which owns
   freshness/rollback protection and exclusion of a returning old master. A sole-host pool has
   no member replica and needs the recovery record above. Neither an empty Controller database
   nor a live old Core may be prerequisites.
   Do not turn recovery into ordinary creation of a second pool or a second VM identity.
3. **Discover and preflight.** Through Controller, ask Core to inspect explicitly selected
   storage and show recoverable VMs, their former owner, recovery-record status and blockers.
   Validate all disks/backing chains, storage identity and permissions, CPU/runtime/firmware,
   capacity, networking and required device mappings on the replacement. A VM with unavailable
   local disks or passthrough state is not recovered merely because one disk survives remotely.
4. **Prove exclusive ownership before boot.** Require explicit VM/target selection and
   confirmation plus verified fencing or physical/storage isolation of the old execution
   owner. An unreachable host, missing heartbeat, cleared lock or confirmation checkbox alone
   is insufficient. Core durably transfers ownership and prevents stale old-host autostart,
   local-console starts and competing recovery requests. Failed or uncertain exclusion blocks
   boot; recovery must not introduce a second restart authority alongside later pool HA.
5. **Handle each storage type safely.** NFS/SMB recovery reuses the surviving files only after
   per-host identity/access and exclusive-execution checks. iSCSI recovery is a controlled
   single-writer LUN handover: revoke/exclude the former initiator, verify the LUN/WWID and
   filesystem identity, then authorize and mount it on the replacement. Never mount an ordinary
   ext4/XFS LUN on two hosts, format it, or imply that this adds shared-block clustering or SAN
   provisioning. Target-side isolation prerequisites and supported handover mechanisms must be
   documented and tested; an unsupported target remains blocked, not silently treated as NFS.
6. **Adopt, boot and report.** Core records recovered configuration and new host assignment
   while preserving VM identity, then performs the confirmed boot and reports progress/results.
   A fresh Controller adopts the same pool/VM records through normal inventory. Operations must
   resume safely across Core/Controller restarts and lost responses without duplicate adoption
   or execution. This is crash recovery, not restoration of RAM or a promise of application
   consistency; show recovery limits and verify guest data after boot. Backups remain necessary
   for damaged/missing disks and loss of the storage server itself.

**Reliability and operator experience are acceptance requirements, not later polish.**
Project owner emphasized both on 2026-09-12. Prefer the fewest clear steps that preserve
all ownership and data-integrity checks; never trade safety for a one-click appearance.

- **Make readiness visible before a failure.** Core reports recovery-record freshness,
  completeness and missing prerequisites per VM. Show when it was checked, what needs
  attention and how to correct it; do not label an untested or stale VM “protected”. Preserve
  the last valid recovery record if publishing its replacement is interrupted.
- **Guide the operator through one workflow.** Use a clearly named recovery entry point:
  choose replacement host and storage → browse recognizable VM names and former owners →
  review automatic checks and required network/device mappings → confirm ownership transfer
  and boot. Discover and prefill safe values; do not require manual UUIDs, disk paths or VM
  configuration reconstruction. Keep technical details available without making them the
  default view. Explain any external NAS, key-recovery or fencing prerequisite before changes
  begin; Nex must not imply it configured hardware or target access it cannot control.
- **Explain blockers and consequences in plain language.** For each selected VM, show ready,
  checking, blocked or unknown based on Core's result, with the specific cause and next action.
  Identify the old/new host and affected disks at confirmation; state that recovery uses
  existing data, does not format storage, and cannot preserve the lost guest's memory.
  Recheck after remediation without making the operator start over or bypass a safety check.
- **Make interruption unsurprising.** Show real Core-reported stages and per-VM outcomes,
  not invented progress or blanket success for a partly recovered selection. Closing the
  browser, refreshing or losing Controller connectivity must not lose accepted work; reconnect
  to the same durable operation. Clearly distinguish safe retry, resumable work and a state
  requiring investigation. Explain when cancellation is safe and what has already changed;
  it must never silently undo a boot or release ownership. Keep secrets out of diagnostics.
- **Prove reliability and usability together.** Repeat full recovery on each supported backend
  and interrupt each state-changing stage, including record publication and ownership transfer;
  verify retained good records, safe resumption, disk integrity and exactly one writable owner.
  A first-time operator must complete the documented supported workflow without developer
  coaching, routine Core SSH or hand-editing configuration. Test keyboard navigation, focus,
  loading/empty/stale/error states and recovery after a corrected blocker. Record completion
  time, confusing steps, retries and assistance required; fix material usability failures and
  rerun before acceptance. Report VM boot separately from verified guest/application recovery.

**Planned API impact: new contract** — GUI discovery/preflight/confirm/status → Controller
administrator authorization and transient relay → authenticated Core recovery and pool-authority
operations → durable Core outcomes and normal inventory/adoption. Reuse existing storage
discovery, enrollment and operation contracts only where they cover the semantics; a directory
listing is not VM adoption. Define scope/identity checks, metadata trust, actionable errors,
idempotency, concurrent-attempt exclusion, cancellation boundaries, outage/reconnect behavior
and Controller/Core version compatibility on both sides. Core owns recovery work and its
durable record; Controller must not retain the only metadata copy or drive the recovery job.
Update API/reference/contracts and the operator guide with runnable authorization, invalid-input,
failure/retry and compatibility checks when implementing this slice.

**Acceptance gate — all outstanding:** create a multi-disk guest on each supported NFS, SMB
and iSCSI profile, write identifiable guest data, then make the original Core and its local
state unavailable. Install a clean replacement Core and use the GUI to discover, select,
adopt and boot the VM using surviving network storage and the documented recovery material
only; no manual reconstruction of VM definitions. Repeat for a dead member, dead pool master
and sole-host pool, including a fresh Controller with no prior pool inventory. Verify stable
pool/VM identities, disk chains, firmware/NVRAM, network mapping and guest data. Test missing,
corrupt, stale and incompatible metadata, missing disks/keys, inaccessible storage, incompatible
hardware, concurrent recovery attempts, interrupted ownership transfer and Controller outage.
Attempt a stale old-host/master return and local/autostart execution; prove exactly one writable
owner and safe refusal on failed fencing. For iSCSI prove old-initiator access is denied before
new writable access. Inspect GUI success, blocked, permission-denied and recovery states; record
actual downtime/data integrity, backend limits and independent API/safety review. Do not mark
the workflow supported until these gates pass; automatic HA remains Phase 9.

**API impact of this planning change: none** — no runtime caller or contract is changed.
**Guide impact: none** — no recovery control is available yet; this section specifies future
implementation and verification, not instructions operators can execute today.

#### Cross-site VM migration

**Agreed 2026-09-09:** Nex Controller must support moving a VM from one Site to a
different Site managed by that same Controller. The operator selects the destination
Site, Pool and Host, maps destination storage/networks, reviews downtime and policy
changes, then follows one durable migration operation. This is an agreed follow-on
after the Phase 7 migration foundation and Phases 0–8 safety/recovery gates, not a
new blocker for the first supported release. Independent-Controller transfers and
cross-site automatic HA are separate, unagreed capabilities.

1. Start with a controlled **stopped-VM transfer**, including encrypted disk transfer
   between authorized source/destination hosts or storage endpoints. Different sites
   need not share storage or a Layer 2 network. Preflight a reachable supported data
   path, bandwidth/space needs and supported resume behavior; refuse unsupported
   connectivity rather than silently routing disks through the Controller API/database.
   Show estimated downtime and transfer progress without promising WAN live migration.
2. Validate destination CPU/runtime, firmware/NVRAM and vTPM support, disk formats,
   backing chains/snapshot history, host storage access and capacity. Transfer every
   required writable disk and supported VM state with integrity validation; reject
   unsupported passthrough, external disks or state before stopping the source. A
   destination registration alone never establishes data completeness or readiness.
3. Require explicit mappings to destination pool storage and logical networks; equal
   bridge/VLAN names do not prove connectivity. Preserve VM UUID and guest identity;
   retain MAC addresses only after destination collision checks. Explain IP/subnet,
   routing, DNS and guest configuration changes and obtain an explicit cutover plan;
   use an isolated/disconnected target network until validation where required.
4. Preflight permissions in both sites/pools and review destination access, backup,
   retention and placement policies. Transfer pool ownership and host assignment only
   at the durable cutover, retaining migration/audit history. Existing recovery points
   remain in their original repositories with authorized recovery access; they do not
   move automatically or become accessible simply because the VM changed pools.
5. Prove the source is stopped and prevent its local autostart, HA or concurrent
   operations from starting it before authorizing target execution. Serialize against
   power, snapshot, backup, restore and other migration operations. Reconcile both
   hosts after timeout, restart or partition; an unreachable source is not permission
   to start the destination. Block managed Docker node VMs until their application,
   storage and quorum-aware relocation workflow is explicitly supported.
6. Retain source disks/configuration as a non-runnable recovery copy until destination
   integrity, boot and workload checks pass and cleanup is confirmed. Before target
   execution, cancellation may restore source ownership only after excluding target
   execution. Once the target has written data, stale source disks are not a rollback:
   require a reconciled reverse transfer or an explicit recovery-point restore with
   disclosed data loss. Never leave two independently runnable writable instances.
7. Consider cross-site live migration only as a later, separately validated extension
   with compatible storage transfer, networking, CPU/runtime and bounded workload
   convergence/downtime. No shared-storage, stretched-network or zero-downtime claim
   follows from merely managing both sites through one Controller.

**Planned API impact: new contract** — browser preflight/submit/status/cancel →
Controller authorization and relay → source/destination pool masters' durable reservations
and ownership handover → scoped Core prepare, transfer, validate, cutover and cleanup work →
observed execution/storage state and resumable operation results. Reuse the Phase 7
operation contract where it covers this flow; the source master coordinates until the
durable destination-master handover, with no Controller-driven job or competing owner.
Add destination-site/pool mappings and
cross-pool ownership semantics together on Controller and Core. Define mutual peer
authentication, short-lived transfer credentials scoped to the selected VM/disks,
input/error validation, idempotent retries, cancellation boundaries, reconnect
reconciliation and Controller/Core compatibility before shipping. Update the API
reference and machine-readable contracts with runnable authorization, invalid mapping,
failure/retry and version-mismatch checks in that implementation slice.

**Gate:** move a multi-disk VM between two separately networked sites with distinct
pool/storage registrations; verify stable UUID, destination ownership/permissions,
network mapping, firmware state, application data and exactly one execution owner.
Exercise limited bandwidth, dropped connections, full target storage, corrupt/partial
transfers, source/target/Controller restarts, cancellation around cutover, stale-source
restart attempts and recovery after target writes. Verify old backup access and new
protection policy application. Record actual downtime/data integrity and run the GUI
workflow with keyboard, error/stale/progress states; documentation and independent
API/safety review are required. Live migration needs its own workload-continuity gate.

**API impact of this planning change: none** — no implemented caller, command or
service API changes; the intended flow and future contract work are defined above.
**Guide impact: none** — the feature remains planned and unavailable; publish verified
operator migration/recovery instructions alongside its implementation.

### 8. Release and operating procedures

1. Test populated-host upgrades, controller schema migrations, and a defined controller/agent compatibility window. Keep updates explicitly on `trixie` until a separate major-upgrade path is validated.
2. Require maintenance/drain or explicit shutdown before disruptive host upgrades. Keep a previous kernel and rescue-console path; do not describe APT as transactional rollback.
3. Document host replacement/re-enrollment, controller recovery, backup restore, storage loss, and certificate recovery. Rehearse [manual recovery after permanent host loss](#manual-recovery-after-permanent-host-loss), including pool-master loss and replacement-Core adoption/boot from surviving NFS/SMB or exclusively handed-over iSCSI storage; do not substitute a healthy-source migration or backup restore for this gate.
4. Expose task failures, backup age, low capacity, degraded storage, host loss, and certificate expiry in the UI; provide useful diagnostic bundles with secrets removed.
5. Publish tested hardware/features, unsupported combinations, install/upgrade instructions, package manifests, checksums, and release notes.
6. Add scoped operator/viewer roles, testing API and console access across site/pool boundaries; retain administrator-only control of host enrollment, storage credentials, and identity configuration.
7. Provide basic search/filtering, contextual task/event history, and notification routing for actionable failures. Use bounded metric history before considering an external monitoring stack. Review UI keyboard access, contrast, long names, empty/error states, and permission-denied behavior with actual browser captures.

**Gate:** complete a clean install, upgrade, host-replacement, controller-loss, and VM-restore rehearsal using only the published instructions. [Debian upgrade/recovery guidance](https://www.debian.org/releases/trixie/release-notes/upgrading.en.html)

### Follow-on: MCP operator integration

**Agreed 2026-09-11:** after Phase 8 establishes scoped roles, stable supported-version
API compatibility, and recovery procedures, add an optional MCP server to Nex Controller
so an authorized AI agent can inspect and operate a cluster through the same Controller
operations as the UI. This is not a prerequisite for the first supported release and must
not become a second control plane: it is a stateless adapter, never a direct Core/libvirt
channel, scheduler, cache, or source of pool state.

Ship the MCP server with every Controller build, but leave it disabled by default. An
administrator must explicitly enable it in Nex Controller before it listens or accepts
agent credentials; it must be visibly shown as enabled in Controller. Model enablement as
an active MCP service principal with an existing scoped administrator role, not an
untracked deployment flag or pool setting. Thus a fresh Controller has MCP disabled; a
Controller backup/restore recovers it only as Controller-scoped administrator/authentication
state, and neither case affects a pool or its workloads.

Begin with read-only inventory and operation-status tools. Add mutating tools only for
existing, explicitly bounded Controller operations, with narrow agent credentials/scopes,
audit attribution, an explicit target and confirmation for disruptive or destructive work,
stable request IDs, and returned operation IDs that agents must inspect to completion.
Reuse Controller/Core validation, authorization, operation reconciliation, outage behavior
and host-side durable records; do not expose arbitrary HTTP forwarding, shell execution, or
raw Core commands. Define the agent credential issuance, expiry, revocation, scope isolation,
transport, tool schemas, confirmation behavior, and Controller/Core version compatibility in
the implementation slice, then update OpenAPI/API reference and the operator guide with
real authorization, failure/retry, Controller-replacement and browser/agent walkthrough evidence.

**API impact: new contract (planned feature); none for this roadmap change.** The MCP
transport and agent credential contract are new client-facing Controller interfaces. MCP
enablement is an explicitly activated Controller-scoped service principal under the existing
administrator-role exception; it reuses existing Controller-to-Core operations and introduces
no durable pool state.
**Guide impact: none.** MCP is not implemented; publish operator setup, permissions and
recovery guidance with the feature.

### 9. Optional per-pool HA

1. Evaluate Corosync/Pacemaker and fencing agents in a separate lab; establish one pool-local authority for HA-managed VM starts/stops. The controller must submit to that authority rather than compete with it.
   **Pool-owned cluster and host fencing, 2026-09-22:** Core forms and maintains one resource-free Corosync/Pacemaker cluster per pool and runs the watchdog arming rollout (`internal/vmhost/pool_cluster.go`, `internal/agent/pool_cluster.go`, `internal/agent/pool_watchdog_arming.go`), so item 1's evaluation is settled — do not add a second cluster layer. State owner: Core; the nodelist is derived from pool membership, with no durable cluster record. Implemented in the working tree, with pre-merge review fixes applied 2026-09-22, and proved by Go tests and a nested lab only. The gate items below stay OPEN: physical fence hardware, clustered-storage pools (which arm nothing and report why), pools above five hosts, HA-managed resources, quorum-loss/fence-failure policy and duplicate-writer proof. See the [automatic host fencing plan](2026-09-22-automatic-host-fencing.md).
2. Define membership, quorum, fence devices, storage prerequisites, restart policy, reserved capacity, and maintenance behavior. Begin with three test nodes; two-node arrangements require a separately validated quorum design.
3. Permit recovery elsewhere only after fencing proves the original execution cannot continue. A missing heartbeat is insufficient evidence.
4. HA-managed guests must not also have independent libvirt autostart paths that can race recovery. Coordinate startup after power restoration and controller reconnection.
5. Keep established pool HA functioning during controller outages. Controller redundancy is a distinct future feature.
6. Validate quorum membership, resource-monitor intervals, redundant cluster communication where required, fence-device reachability, and no-quorum/fence-failure policy. Publish membership/quorum/resource/fencing status to Nex Controller for visibility without making its availability necessary for local recovery.

**Master-controlled HA and optional failback — project owner requirement, 2026-09-12;
planned, not implemented.** Reliability and understandable recovery take priority over feature
count. The following are blocking requirements for HA, migration and disaster-recovery acceptance:

- **One authority, including after master failure.** All policy changes, placement decisions,
  recovery, evacuation and return moves are owned by the active pool master and persisted on
  Core. Master succession follows [pool authority transfer](#pool-authority-transfer-and-master-succession);
  do not build a second election or let Controller, member agents, libvirt autostart and the
  HA scheduler race each other. Test loss of the master during fencing,
  migration cutover and failback. A new master must recover the same operation/ownership state
  and reject stale-master/member commands before allowing further work. Never force a new
  master merely because the old one stopped responding.
- **Fencing must establish actual exclusion.** Validate the exact fence device, target identity,
  credentials and affected host/storage paths. Successful request submission or a management
  API acknowledgment is not proof that the old VM can no longer execute/write. Verify the
  required power/storage exclusion, including every supported initiator/write path; failed,
  delayed or ambiguous results block recovery. Test wrong-target refusal, device unreachability,
  lost replies, power restoration and stale-host local/autostart attempts. Publish supported
  fencing hardware/target combinations and limits; mocks or nested power-off tests alone do
  not certify physical fencing. No “force” checkbox may bypass unproven writable ownership.
- **Returning a host does not automatically move VMs.** Automatic safe member rejoin follows
  the authority-transfer decision above; VM restart and movement follow configured HA policy,
  not an independent rejoin action. Default to leaving successfully
  recovered VMs on their current healthy host. Offer an explicit return action and a separate
  opt-in automatic return policy tied to an operator-selected preferred host. Keep that policy,
  host identity, stabilization interval and retry/hold state in the master's durable pool/VM
  records so they survive Controller replacement and master succession. A replacement physical
  host requires explicit remapping; matching a hostname does not establish identity.
- **Failback uses the same safety checks as the initial move.** Require fresh ownership,
  storage/network/firmware/CPU compatibility, spare capacity and stable host health before a
  return. Respect maintenance mode and conflicting VM/storage work. Apply a tested stabilization
  interval and bounded retry policy to prevent repeated host flapping from bouncing VMs.
  Use validated live migration only where supported; otherwise disclose the required downtime
  and obtain explicit permission for a stopped move rather than silently rebooting a VM.
  Failed return attempts must preserve the current valid owner and data, or stop further
  actions while an ambiguous ownership result is reconciled—never start both copies.
- **Operators must understand the decision.** Show the current owner, preferred host, whether
  return is manual or automatic, readiness/fencing evidence freshness, and the exact reason a
  recovery/return is waiting or blocked. Before enabling HA/automatic return, show missing
  prerequisites and expected restart/downtime behaviour. Allow holding/cancelling a pending
  return at documented safe boundaries without disabling protection of the running VM.
  Report per-VM progress, retry limits and the next safe action; distinguish a booted VM from
  verified application recovery and never promise zero data loss or a recovery time without
  measured evidence for that workload/profile.

**Required failure matrix:** before acceptance, a named code owner traces every
start/stop/move/retry path through the same Core ownership authority and an independent safety
reviewer checks the evidence, under the [verification rules](../../AGENTS.md#verification-and-bug-memory).
Unavailable disposable-lab or physical-fence prerequisites leave the affected gate outstanding.

Repeat failover and opt-in return with a guest actively writing identifiable data on each
supported backend, checking exactly one writable execution owner throughout, current disk/chain
and firmware state, and guest/application integrity after recovery. Include abrupt host/master
loss; asymmetric management, cluster and storage partitions; loss/restoration of quorum;
unreachable, delayed, failed and misdirected fencing; stale observations/commands; process and
Controller restarts; concurrent migration/recovery/return requests; insufficient capacity;
storage identity changes; repeated host flapping; and return interrupted before, during and
after ownership cutover. Also prove that return stays disabled by default, explicit return and
opt-in automatic return obey prerequisites/downtime permissions, and the policy survives master
and Controller replacement. Record exact versions, topology, fence hardware, repetitions,
timings, assertions and untested limits. Independently review split-brain, data-loss and
usability outcomes; any duplicate writer, unverified exclusion or unexplained ownership state
blocks support for that profile. Keep single-host/manual recovery distinct from automatic HA:
no healthy quorum/fence configuration means no automatic restart elsewhere.

**Planned API impact: new/changed contracts** — UI policy/preflight/return requests → Controller
authorization/relay → active-master durable policy and operation → scoped member/cluster work
→ Core ownership, fencing, progress and outcome reports. Define master-identity/authority
validation, permissions, safe retries, concurrency, cancellation/cutover boundaries, reconnect
and version compatibility; update both Core and Controller contracts, reference and focused
negative-path tests in the implementation slice. Core's durable master journal and surviving
recovery records own the policy/operation state; a fresh Controller adopts it through inventory.
**API impact of this planning change: none; guide impact: none** — no runtime feature changes;
operator controls and verified guides remain future implementation work.

**Gate:** power off a host, partition management and cluster networks separately, lose quorum/storage, interrupt fencing, stop Nex Controller, and reconnect stale nodes. Demonstrate no duplicate writable VM instance, continued local HA during controller-only loss, and safe refusal when ownership cannot be proven. [Pacemaker administration](https://clusterlabs.org/projects/pacemaker/doc/2.1/Pacemaker_Administration/pdf/Pacemaker_Administration.pdf)

### 10. Managed Container Services (Docker Swarm)

**Agreed architecture:** Nex Controller requests Container Services through authenticated Core APIs. Core runs durable provisioning and lifecycle operations for Docker node VMs and integrates with Swarm inside those VMs; Swarm schedules application tasks. Controller displays progress and results. Users work with pool-level applications; node VMs remain visible in an Infrastructure view for diagnostics and controlled maintenance. No routine SSH, manual Swarm setup, or manual Docker commands are required.

This follows Phases 0–8 and the minimal VM image/bootstrap capability it needs. Stateless Swarm recovery does not require completion of VM HA in Phase 9. Stateful failover needs its own proven ownership/recovery gate and any fencing integration it depends on. Ceph is not a prerequisite; use supported NFS volumes initially.

#### Ownership, storage, and failure behavior

- Begin with one managed swarm per enabled pool. Controller authorizes operator requests and displays host-reported operation history. Core validates and persists desired application configuration, identities, node-VM relationships and durable provisioning progress on the pool master; define the concrete records and inventory/adoption contracts in each slice. Recover Swarm-owned runtime state through the managed swarm. Provisioning and reconciliation must survive Controller disconnection, and a replacement Controller must rediscover applications through Core APIs. Swarm owns service scheduling, replicas, rolling updates, and replacement tasks. Avoid a competing Nex container scheduler; retain the explicit Controller backup/restore orchestration exception.
- Provision manager/worker roles automatically. For an HA-enabled swarm use three managers on separate physical Nex Core hosts, enforce that separation during VM placement/migration/maintenance, and reserve sufficient resources. Managers may also run workloads in a small validated deployment; dedicated manager VMs can follow workload requirements.
- Swarm manager quorum is independent of hypervisor/pool HA quorum. Keep manager addresses stable and each manager's Raft state on its own node disk; do not share one Docker data directory between nodes. Loss of Nex Controller must not stop Swarm scheduling; loss of Swarm manager quorum prevents orchestration while existing tasks may continue. [Docker nodes](https://docs.docker.com/engine/swarm/how-swarm-mode-works/nodes/), [Swarm administration](https://docs.docker.com/engine/swarm/admin_guide/)
- Failover creates replacement containers on eligible nodes; it does not live-migrate their memory, retain process identity, or guarantee uninterrupted connections. Service identity is stable while task/container IDs change. Expose recovery progress and measured downtime honestly.
- Map supported pool storage to dedicated application volumes, initially NFS-backed paths/exports reachable from each eligible node VM. Preserve separate application/VM-data permissions and credentials; never expose an entire VM datastore to arbitrary containers. Local Docker volumes are node-bound unless explicitly configured for remote storage. [Docker volumes](https://docs.docker.com/engine/storage/volumes/)
- Validate backend access, capacity, permissions, and application filesystem requirements before placement. Mark each volume's supported concurrent-access or exclusive-writer mode; do not assume a shared filesystem makes the application safe for multiple writers. Ceph RBD support for VM disks does not automatically provide a shared container filesystem.
- Swarm `replicas: 1` and manager quorum do not establish storage fencing. An isolated old task may still write while a replacement starts. Admit stateful automatic failover only when application-native replication/leadership or storage/node exclusion actually protects every write path, including rescheduling and rolling updates. Until validated, reject that HA configuration with an actionable reason; a UI flag alone cannot constrain Swarm's recovery behavior.
- VM HA owns recovery of Docker node VMs; Swarm owns application tasks. Coordinate host maintenance, node draining, manager quorum, and fencing without both layers independently restoring application data or recreating node identities.
- Scope controller permissions to the pool and supported operations. Protect Docker APIs, registry credentials, application secrets, and node bootstrap material; no public Docker socket or unrestricted privileged/host-device access in the initial application service. Keep tenant/application boundaries explicit.

#### Application catalogue

**Decision (project owner, 2026-09-22):** applications are installed from a catalogue that is a
public Git repository, in the style of a home-lab app store. Nex maintains an official repository
covering the common home-lab set (web servers, media automation stacks, and similar); operators may
add community repositories. The catalogue also carries VM appliances, so one discovery and install
experience covers both workload kinds. Almost every operator will use this through Nex Controller,
and that is the intended experience; the design below exists so the Controller stays a replaceable
front end onto it.

- **The pool master fetches the catalogue, not Nex Controller.** Each catalogue repository is a
  pool setting recorded on the master (name, Git URL, optional public key), with the official
  repository as the default. A periodic job on the master (systemd timer) fetches every configured
  repository and keeps a local checkout; air-gapped pools point the URL at a mirror. Nex Controller
  asks the master for the available and installed applications and shows what it reports. Nothing
  about the catalogue, the installed set or "update available" is computed or cached only in the
  Controller.
- **One folder, one file, nothing to learn.** The repository root holds one folder per
  application, named after it, containing `nex.yaml` and an optional `icon.svg`. For a container
  application `nex.yaml` is a plain Docker Compose file (consumed by `docker stack deploy` as-is,
  testable with `docker compose up`) plus one top-level `x-nex` block, which Compose ignores by
  design: name, description, category, and `options`. Operator-fillable values use Compose's own
  `${VAR}` interpolation; `options` only adds each variable's label, type and default so the
  wizard knows what to ask. Types are a short fixed list (`storage`, `port`, `text`, `password`,
  `bool`); a `storage` option becomes a pool volume the wizard lets the operator pick or create,
  with `access: shared` marking concurrent-safe use. No version field: "update available" means
  the folder changed in the catalogue since install, which Git already records. A VM appliance
  uses the same file name with `kind: vm` in `x-nex` and an image reference with checksum plus
  hardware defaults in place of `services`. There is no template language and no second format
  underneath Advanced mode; it edits the same file.
- **Trust.** The official repository is signed: a release step signs an index naming the commit,
  and the matching public key ships in the Nex Core package. The master refuses an unsigned or
  badly signed fetch and keeps serving the last verified checkout with a visible warning. Community
  repositories are verified only when the operator supplies the maintainer's public key; their
  applications carry a clear community badge and source name. Rotating the official key is a Core
  package update; the private key is held by the project owner outside CI. Core resolves image
  tags to digests at install time and records the digest, so what runs is what was reviewed and
  updates are explicit.
- **Install and update are Core operations.** The Controller submits one request: catalogue
  application ID plus the operator's answers. The master validates against the manifest, renders
  `nex.yaml`, writes the durable application record (application ID, source repository and
  commit, answers, any advanced overrides, resolved digests, volumes), then deploys
  through the managed Swarm or libvirt. "Update available" is the master comparing that record
  with its current checkout; updating re-renders the base and re-applies the operator's overrides
  before Swarm's rolling update. Registry credentials and application secrets go to Swarm secrets
  with the durable copy on the master, following the storage cluster secrets pattern.
- **Basic by default, advanced by disclosure.** The manifest declares the few questions a normal
  operator answers (name, storage for config and data, port); everything else keeps sensible
  defaults inside the stack file. One install flow, with an Advanced disclosure on the review step
  that exposes the rendered Compose stack or VM definition for editing. Manual edits are stored as
  an overlay on the record so the wizard keeps working after them. Never two install paths.
- **Proof without a Controller.** The catalogue fetch, signature check, timer, Swarm client and
  application records all ship in the Nex Core package. A host-local Core CLI over the same API is
  built in the same slice as the API, not as an operator product but as the acceptance evidence
  that the master does the work: browse, install, observe an update and reboot the master with no
  Controller enrolled, then enrol a fresh Controller and see the Applications screen fill in with
  nothing re-entered.

#### Incremental delivery

1. **Enable Container Services:** choose pool, participating hosts, worker capacity, management/application networking, and supported storage. Preflight resources and connectivity; automatically create/bootstrap node VMs, form the swarm, and display durable progress. Make retries idempotent and offer cleanup of incomplete provisioning without deleting application data.
2. **Stateless application deployment:** image/registry credentials, resource limits, published ports, environment/configuration, secrets, replica count, and health check. Show application/service/container views, logs, health, scaling, version updates, and supported rollback. Configure service discovery/overlay networking and a tested reachable endpoint so losing a node does not leave users pointing only at that node's address.
3. **Persistent applications:** select existing or create dedicated volumes from supported pool storage; validate access on every eligible node VM. Initially admit writable persistence only for validated concurrent-safe applications or configurations whose enforced writer exclusion already covers Swarm rescheduling and updates. Reject other writable stateful deployments until slice 5 proves their safety; disabling an HA control is insufficient. Show attachment, capacity, consistency limitations, and replacement eligibility. An NFS mount succeeding is not admission evidence.
4. **Backup and restore:** integrate pool backup policies for application volumes and deployment configuration, with secure recovery of required secrets and registry/image references. Use application quiescing or a stopped-workload capture as appropriate; a worker-VM snapshot alone does not capture remotely mounted data. Restore as an isolated new application by default; replacing an original requires confirmed writer exclusion. Back up/recover Swarm manager state separately using Docker's supported process; prevent restored node disks from resurrecting stale managers or tasks into the live swarm.
5. **Validated stateful recovery:** select an explicit supported application/storage combination, implement required ownership enforcement, then test crash, partition, reschedule, update, and restore before offering automatic failover. Explain unsupported workloads in the UI.
6. **Operations and lifecycle:** drain/rebuild/replace nodes, preserve quorum and physical-host separation, rotate credentials, coordinate updates, and inspect infrastructure VMs through Nex Controller. Guard direct deletion of managed node VMs and provide intentional decommission/removal with clear effects and data-retention choices.
7. **Application catalogue:** the official signed repository as the default pool catalogue source, the `nex.yaml` format, master-side fetch and verification, the wizard-plus-Advanced install flow for container applications, update detection and rolling update, and the Core CLI proof. Community repositories and VM appliance entries follow once the container path is verified. Start the official repository with two or three applications proven end to end before filling it out.

**Acceptance gates:** each slice is usable entirely from Nex Controller. Deploy a stateless service, kill a worker and then its physical host, and verify replacement plus endpoint reachability with a surviving manager quorum. Disconnect Controller during Core provisioning, reconnect and retry without duplicate node VMs or applications; replace Controller and rediscover application identities, configuration, node relationships and operation results through Core inventory/adoption. Stop Nex Controller and confirm Swarm continues independently; lose manager quorum and report degraded orchestration honestly. Restore application data/configuration onto a new worker after making the original volume unavailable. For supported stateful HA, deliberately partition a writer and prove no unsafe concurrent access. Verify failure/retry/cleanup and manager replacement without duplicate node identities. Capture and review the colorful Applications UI, icon fallbacks, status clarity, keyboard access, and narrow-screen behavior. For the catalogue: reject a tampered or unsigned official repository and keep serving the last verified checkout; install, update and list applications with no Controller enrolled; replace the Controller and rediscover every installed application, its source and its overrides through Core inventory/adoption.

## Guided hardware passthrough

**Agreed 2026-09-08:** make assigning physical hardware to VMs easy from Nex Controller, with NVIDIA and AMD GPU passthrough as first-class Nex Core requirements. Deliver this as an agreed follow-on slice after the VM foundation and Phases 0–8; it does not expand the current standalone ISO slice or depend on HA, Ceph, or Container Services. Start with whole PCIe devices (GPUs and their companion functions, then validated USB controllers, NICs, and storage controllers); shared vGPU, SR-IOV, and individual USB-device forwarding need separate support gates.

### Operator workflow and distro preparation

1. **Discover:** Host → Hardware lists readable vendor/model, physical slot/PCI address, current driver, IOMMU group and companion functions, host use, VM owner, observation freshness, and readiness with actionable blockers. Distinguish CPU/platform IOMMU support (Intel VT-d or AMD-Vi) from GPU vendor; test NVIDIA and AMD cards independently of CPU brand.
2. **Prepare host:** provide a guided readiness check and explicit review of required boot/module/initramfs changes using Debian's standard kernel, VFIO, and libvirt. Explain firmware settings that require manual BIOS/UEFI changes; do not imply Nex can enable them remotely. Automate supported OS configuration without routine SSH, hand-edited GRUB, or XML. Select devices precisely, including identical GPUs, rather than claiming every device with the same vendor/product ID. Preserve prior configuration, require maintenance and confirmation for disruptive changes/reboots, verify after reboot, and provide a tested local rescue/undo path that works without Controller.
3. **Assign:** stopped VM → Hardware → Add physical device → select a ready device → review bundled functions, host impact, and placement/protection limits → tracked attach → boot and verify. Automatically propose required GPU audio/other companion functions and compatible guest firmware/machine settings, explaining any change to an existing guest. Use libvirt host-device assignment and enforce exclusive device/group ownership; refuse unsafe groups, host boot/data controllers, management NICs, and loss of the only usable recovery console. Single-GPU host use requires a separately verified alternative recovery path. Never bypass isolation with ACS override or VFIO no-IOMMU mode.
4. **NVIDIA and AMD:** provide the same guided experience and model-specific troubleshooting for guest driver installation, firmware/BAR compatibility, and reset/reuse failures. Whole-device passthrough uses VFIO on Core and the appropriate vendor driver inside the guest; do not require proprietary host GPU drivers by default. Publish exact tested GPU, motherboard/firmware, kernel/QEMU/libvirt, guest OS, and driver combinations; neither vendor-wide compatibility nor a one-boot success is sufficient evidence. Review licensing before bundling drivers, firmware, or workarounds. Keep a virtual recovery display where supported and explain that the browser console does not automatically mirror physical GPU output.
5. **Operate and undo:** show assigned, pending reboot, unavailable, and reset-failed states; support stopped-VM detach and deliberate return to host use. Serialize conflicting assignment operations, revalidate identity/group membership after hardware or firmware changes, and never bind a replacement device solely because it reused a PCI address. Failed resets must leave the device unavailable with a recovery action, never trigger an unconfirmed host reboot or silently assign it elsewhere.
6. **Placement and protection:** mark these VMs host-bound; initially block live migration, memory save/snapshot, and automatic HA relocation. Maintenance reports blocked VMs and offers explicit shutdown. Cold relocation or restore requires explicit compatible device remapping and fresh exclusivity checks; clones/restores must not automatically acquire the original hardware. Disk-only snapshots/backups require a validated combination and clearly exclude physical device state; reject passthrough storage whose data cannot be included in complete recovery rather than label an incomplete backup protected.

Implementation basis: Linux VFIO exposes devices with IOMMU protection and treats groups as isolation/ownership boundaries; libvirt supplies host inventory and guest device assignment. These are upstream mechanisms, not proof of Nex support. [Linux VFIO](https://docs.kernel.org/driver-api/vfio.html), [libvirt host devices](https://www.libvirt.org/drvnodedev.html), [libvirt device assignment](https://www.libvirt.org/formatdomain.html#host-device-assignment).

### API impact and acceptance

**API impact: new contract (planned feature); none for runtime in this documentation change.** No Nex APIs exist yet. The flow is Controller UI/client → authorized hardware inventory/preflight, host preparation/undo, and VM attach/detach operations → authenticated Core commands → agent/libvirt observations and durable results → Controller task/UI updates. Reuse the inventory, VM configuration, and operation contracts delivered by earlier phases; add only missing device identity/group, ownership, readiness, pending reboot, and reset/error fields and commands. Define host-administrator preparation rights and scoped VM/device-assignment rights, validation/conflict errors, idempotency, revalidation immediately before changes, and Controller/Core capability/version negotiation. During outages keep running assignments intact; reconcile the local journal, bindings, and libvirt state after reconnect before retrying ambiguous operations. Deliver both sides, machine-readable contracts, and the [API reference](../API.md) together.

**Gate:** on explicitly selected disposable physical hosts, complete clean Nex Core install → guided preparation → assign → guest driver/workload verification → repeated guest stop/start/reboot → host reboot → detach/return to host for at least one NVIDIA and one AMD GPU, testing supported Linux and Windows guests. Record exact tested combinations and limitations. Include disabled IOMMU, unsafe groups, identical GPUs, occupied/critical devices, failed reset, interrupted preparation, Controller/agent loss, rescue rollback, kernel upgrade, and clone/restore/migration blockers. Add focused API authorization, invalid-input, concurrent-claim, retry/reconnect, and version-mismatch checks. Review the real browser flow, including keyboard, empty/error/stale states and disruptive confirmations; no routine shell/XML edits on the supported path. Physical GPU testing remains outstanding until that evidence exists; nested ISO QA cannot satisfy this gate.

## Follow-on features from the platform review

These are prioritized candidates, not all prerequisites for the first supported release. Implement them as independent slices once their underlying operations work.

| Priority | Feature | Prerequisite and proof |
|---|---|---|
| Next | Templates, full clones, and Linux cloud-init | Reliable VM creation; clone boots with unique identity, MAC, and credentials |
| Agreed follow-on | Guided NVIDIA/AMD GPU and PCIe passthrough | [Hardware passthrough gate](#guided-hardware-passthrough); safe host preparation, assignment, recovery, and physical tests for both GPU vendors |
| Agreed follow-on | Site-to-site VM migration through one Nex Controller | [Cross-site migration gate](#cross-site-vm-migration); stopped transfer first, explicit target pool/storage/network mappings, stable identity and exclusive cutover |
| Next | qcow2/raw import and export | Validated untrusted image handling and explicit firmware/network mapping; imported guest boots |
| Next | Tags, saved filters, bulk actions | Stable inventory and per-object authorization; bulk result reports partial failures without hiding them |
| Next | Permission-scoped global search and command palette | Local search first; reuse inventory and existing workflows, preserve confirmation and keyboard focus, and test forbidden-object leakage |
| Next | Optional integrated dark theme | Reviewed shell and shared semantic tokens; retain light default and test both themes for contrast, focus, forms, and status clarity |
| Next | Guest IP/agent information and boot ordering | Guest-agent capability checks; startup/shutdown order tested, with HA ownership respected |
| Next | Historical metrics and tuned alerts | Freshness/time semantics and bounded retention; simulated outage/low-space events reach the operator |
| Next | Scoped API tokens and external login/MFA | Stable authorization/audit model; expiry, revocation, and scope isolation tested |
| Later | Scheduled isolated backup boot tests and file-level restore | Independent full-VM restore proven; boot success not treated as application validation |
| Later | CBT-backed incremental/off-site backups and retention protection | [Changed-block tracking gate](#changed-block-tracking-and-incremental-backup-follow-on); bitmap continuity or safe full fallback, dependency-aware retention and clean-host restores |
| Later | Storage-native ZFS/Btrfs snapshots and transfer evaluation | [Native snapshot gate](#storage-native-snapshot-integration-follow-on); explicitly managed scope, complete VM state and tested lifecycle/restore |
| Later | Rolling updates and pool package compliance | Migration/evacuation and tested Debian upgrade/recovery path |
| Later | Affinity, anti-affinity, HA admission, placement recommendations | Compatibility checks, capacity reservations, and HA safety; recommendations before automatic balancing |
| Later | Windows customization, vTPM, OVA/VMDK onboarding | Guest-driver/firmware/state protection and conversion compatibility tests |
| Later | Ceph RBD | Separate backend capability tests for placement, migration, snapshots, backup, and restore |

## Proposed repository layout

Create files as their phases begin, not empty scaffolding for the whole roadmap.

```text
README.md
go.mod
cmd/nex-controller/main.go
cmd/nex-core-agent/main.go
internal/controller/             # UI-facing API/auth, host requests, Controller-scoped data and backup jobs
internal/agent/                  # Core APIs, durable pool state/policies, libvirt and host operations
internal/protocol/               # shared request/response definitions only
web/                            # React/TypeScript UI
packaging/                      # Debian control files, services, defaults
installer/                      # ISO recipe, preseed, embedded repository
tests/integration/              # disposable-host scenario checks
docs/                           # operator and recovery documentation
docs/plans/                     # this plan and future project plans
```

Likely validation targets, introduced with the corresponding implementation:

- `go test ./...` for authorization, operation recovery, placement rules, and retention logic.
- UI typecheck and production build; focused browser checks for enrollment, VM console authorization, and restore selection.
- `tests/integration/vm-lifecycle.sh`: VM creation, data persistence, controller/agent restart.
- `tests/integration/snapshot-restore.sh`: multi-disk rollback, deletion, and interruption recovery.
- `tests/integration/backup-restore.sh`: independent restore and retention/corruption cases.
- `tests/integration/iso-install.sh`: offline UEFI installation and post-boot enrollment in a disposable VM.
- Later migration and HA scenarios on dedicated lab hosts; ordinary unit-test CI cannot prove fencing or hardware compatibility.

All destructive tests require explicitly selected disposable disks/VMs and must refuse to use production inventory.

## Scope boundaries and decisions to revisit

- First end-to-end demo: one site, one pool, one ISO-installed host, one VM managed and consoled through Nex Controller.
- First supported release: multi-host pools, local and NFS storage, complete snapshot lifecycle, scheduled full backups, verified restoration, shared-storage live migration, host evacuation, and tested upgrades. Complete Phases 0–8. Automatic HA follows its separate gate.
- Managed Docker Container Services are an agreed later extension (Phase 10), not a prerequisite for the first VM release; the colorful UI and full controller-driven lifecycle are part of that extension's acceptance criteria.
- Site-to-site VM migration is an agreed follow-on after Phases 0–8, managed by one Controller: stopped disk transfer and explicit pool-ownership cutover first. Cross-site live migration requires its separate gate; cross-site HA and independent-Controller transfer are not included.
- Ceph is an explicitly planned later backend: register RBD storage at pool level, provision scoped credentials, verify host access/health, then validate disks, snapshots, backups/restores, and live migration against its capabilities. Decide separately whether Nex manages an existing Ceph cluster or also deploys and operates Ceph; start with existing-cluster integration. Do not build a generic storage plugin framework in advance.
- Guided NVIDIA/AMD hardware passthrough is an agreed follow-on requirement with its [own workflow and acceptance gate](#guided-hardware-passthrough), outside the initial release; keep its host-bound placement and recovery limits explicit.
- No Ceph deployment/operation, iSCSI/SAN provisioning, memory snapshots, deduplication, incremental backup chains, cross-pool live migration, multi-controller failover, or automatic balancing in the initial release. Add each only with its required lifecycle/recovery tests. Controller *replacement* (below) is required for the first supported release; multi-controller failover is not.
- Cross-pool movement starts as an explicit backup import/restore workflow with destination authorization and storage/network remapping; it is not an ordinary host reassignment.
- Before Phase 0: choose lab machines, management subnet, guest OS test images, and usable disks. Default to amd64, UEFI, Linux guests, local qcow2, then external NFS.
- Before production protection: agree backup destination/failure domain, encryption/key custody, retention, downtime tolerance, and RPO/RTO targets. Before HA: agree fencing hardware, node count, and availability requirements.

Implementation should proceed phase by phase, with each gate demonstrated before dependent features are treated as supported.

### Replaceable Controller — 2026-09-09

project owner identified a gap in the development model: the Controller web UI/API must be a front end
onto the pool master's state, so a failed Controller can be replaced by installing a new one and
connecting each pool master to it, with every setting coming across. Decisions:

- **Source of truth:** every pool-scoped setting is persisted on the pool master's Nex Core in a
  durable, host-local record (today: the managed journal and `/etc/network/interfaces.d`) and is
  reported in inventory so a Controller that does not know it re-adopts it with the same identity
  and name. This applies to pool membership, networks, storage registrations, media, VM
  definitions and identity, later snapshot policies, and backup repository registrations; backup
  schedules, jobs, run history and the catalog are the agreed Controller-scoped exception
  (decision: project owner, 2026-09-10, Phase 6). Running workloads and local
  recovery never depend on the Controller (unchanged).
- **Replacement procedure target:** install a new Controller, connect each pool master (token or
  address plus root password), wait for adoption. No re-entry of pool settings. Controller-scoped
  data (sites, administrator accounts and roles, audit history, enrollment tokens, the Controller
  CA and, from Phase 6, backup jobs) is backed up and restored separately; until that exists, it is re-created by hand and this is
  documented as a limit.
- **Rule for every slice:** the [architecture invariant](../../AGENTS.md#the-architecture-invariant-nex-core-does-the-work-nex-controller-only-asks).
- **Status:** implemented for storage registrations, networks, media and VM definitions
  (`Inventory.managed`, adoption with preserved IDs); an enrolled host pointed at a different
  Controller takes a fresh identity there in place, keeping its journal. Evidence:
  [controller replacement checks](../qa/pool-vlan-networks/checks.md). Pool membership is also
  implemented: a fresh Controller adopts a connected member once its master reports it ready
  ([join walkthrough](../guide/join-a-pool.md#verification)). Open: master role transfer
  ([authority transfer](#pool-authority-transfer-and-master-succession)), later policies, and backup/restore of Controller-scoped data.
  The lost-Controller test in the Phase 6 gate becomes a **Controller replacement gate** for the
  first supported release: destroy the Controller appliance, deploy a fresh one, connect the
  master, and verify the pool's networks, storage, media, VMs and policies return with their
  identities and that no duplicate resources or writers are created. Known gaps: same-Controller
  re-enrollment under a new host record does not move records (use Re-enroll host);
  `needs_attention` VM creations are not in the journal report; silent skips of invalid
  journal entries have no audit entry yet.
  Status note, 2026-09-17: the gate's same-host appliance redeploy is blocked by the product in
  this build. A host that is enrolled or already has an appliance deployment refuses a new
  deployment, and no local route retires the old appliance. The guides now document replacement
  with a Controller installed outside that host, after excluding the old appliance (autostart
  disabled, observed `shut off`). A host-local appliance retire and redeploy contract is an open
  owner decision; the gate wording above is unchanged.
  Status note, gate5 (2026-09-17 UTC): external replacement with the gate5 Controller package re-adopted
  the host's IDs, names, specs, revisions, inactive XML and journal set with nothing re-entered; the
  gate stays open (same-host redeploy blocked, no backup restore; [gate report](../qa/2026-09-17-phase2-gate/checks.md#g6-in-detail)).

### Installer welcome copy clarification — 2026-09-08

Explain Nex Core’s Debian/KVM/libvirt host role and remind operators to deploy at least
one Nex Controller separately for central management of their environment. This is a
welcome-copy update, not Controller deployment or enrollment functionality.
API impact: none; no callers, operations, agent commands or handoff contracts change.
Guide impact: updated; the installation guide describes the welcome reminder.

### Installer hostname standards correction — 2026-09-08

Accept RFC 1123 ASCII hostnames and dot-separated qualified names, including digit-leading
labels, uppercase, internal hyphens, ASCII/Punycode spelling and one optional root dot.
Apply DNS limits of 63 bytes per label and 253 text bytes excluding that optional dot.
Keep private suffixes usable without DNS/public-TLD checks. Unicode text conversion is
not part of this ASCII hostname profile; use the ASCII IDNA representation.
Use shared validation in the GUI and protected backend. Store the first label as the
Debian short hostname, the remaining suffix as domain, and retain full/short aliases.
API impact: none; local installer settings remain private, with no Controller/Core or
client API operation changes. Guide impact: updated. Research and executed verification
belong in [hostname QA](../qa/iso/2026-09-08-installer-hostname.md).

### Installer live link indication — 2026-09-08

Replace raw operational state with observed carrier status. Probe detected device NICs,
read administrative/carrier state, and refresh the selected NIC while Network is open.
A failed or unavailable probe must not be reported as a disconnected cable. Preserve
in-progress form edits and distinguish link from IP/VLAN/gateway connectivity.
API impact: none; local observation only, no Controller/Core or client API changes.
Guide impact: updated; record results in [link-status QA](../qa/iso/2026-09-08-installer-link.md).

### Installer erase-confirmation clarification — 2026-09-08

Replace the confusing device-name typing field with an initially unchecked erase checkbox
naming the selected disk, followed by the existing explicit Erase disk and install action.
Returning to Review resets consent. Keep target selection, identity rechecks and all backend
validation. API impact: none; confirmation remains local to the installer. Guide impact:
updated, including the changed review step and safety behavior.

### Paravirtual disk eligibility and compact network feedback

GUI discovery and the backend now share disk-name eligibility, including paravirtual `xvd` devices.
The previous GUI offered `/dev/xvda` but the backend rejected its prefix. Existing whole-disk,
read-only, removable, installation-media, capacity and identity checks remain required.
A green light accompanies detected carrier; other states retain neutral lights and text.
Verbose hostname hover explanations are removed; validation and errors remain.
API impact: none; local installer discovery/validation and display use no Controller operation,
agent command/result or client-facing API. Guide impact: updated.
See [paravirtual disk QA](../qa/iso/2026-09-08-installer-xen-disk.md) for evidence and paravirtual runtime limits.

Keyboard startup feedback correction: successful layout application is silent; genuine
errors still block credentials and layout changes still clear both password fields.
API impact: none; local GTK setup only, no Controller/Core or client contracts. Guide impact: updated.

### Backend progress transitions

Keep a waiting panel visible when cdebconf stops a progress task or clears an answered
prompt before the next step arrives. Real progress, questions and errors replace it;
no artificial overall percentage or stale completed task is presented.
API impact: none — cdebconf presentation changes no Controller operation, agent command/result
or client-facing API. Debian installation commands and validation are unchanged.
Guide impact: updated; native transition evidence belongs in the installer QA report.

Completion behavior: the GUI leaves Debian's final successful-installation note unseen
so it waits for acknowledgement, with Nex completion copy and a **Restart now** button.
This uses the existing finish-install completion gate; final cleanup/unmount/reboot follow
acknowledgement. It is not a separate certification of installed-host functionality.
Native failure/recovery prompts remain. API impact: none; local preseed/frontend only.
Guide impact: updated alongside the transition fix.

Failure feedback: retain the backend reason and recovery controls under Nex failure
branding, with an explicitly opened read-only installer log viewer. Missing logs must be
reported honestly. This does not claim display recovery after a kernel crash or power loss.
API impact: none; local diagnostics display. Guide impact: updated in the same slice.

### Continuous Nex progress presentation correction

The separate waiting fallback did not solve the operator complaint: it visibly swapped
with the stock progress layout at every component boundary. Replace both presentations
with one persistent Nex panel, fixed geometry and heading, and real per-step status.
Only interactive questions/errors and completion replace the progress panel. Keep native
backend cancellation and question handling; do not invent an overall percentage.
API impact: none — internal cdebconf GTK rendering only; no Controller operation, agent
command/result or client-facing API changes. Guide impact: updated.

## Installer DHCP feedback — 2026-09-09

project owner requested immediate DHCP feedback for the selected installer NIC/VLAN: searching,
server-returned IPv4 or retryable failure. Use the existing BusyBox DHCP client with a
custom display-only hook; no route, address or DNS configuration effects. Cancel previews
on selection/mode/VLAN/page changes and remove only owned temporary VLAN links.
API impact: none — GTK invokes a local installer helper; no Controller/Core agent or
client-facing API contract changes. Guide impact: updated. Validation and remaining
limits: [DHCP preview QA](../qa/iso/2026-09-09-installer-dhcp.md).
