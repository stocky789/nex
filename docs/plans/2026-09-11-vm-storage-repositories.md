# VM storage repositories: NFS, OpenZFS, SMB and iSCSI

2026-09-12 scope expansion (project owner): the
[concurrent storage plan](2026-09-12-concurrent-pool-storage.md) supersedes the single-host
limits below only for its explicitly validated profiles; ordinary ext4/XFS iSCSI stays exclusive.

2026-09-12 UX correction (project owner): BUG-0095 requires browsing what the destination
server offers, not choosing from Nex registrations. SMB server/account/password must be
enough to open a share-and-folder picker; NFS server alone opens export-and-folder browsing;
iSCSI discovery fills available target details without guessing LUN identity or exclusivity.
Manual entry remains a fallback. API impact: **existing contract** for the picker, using
`POST /api/v1/storage/discover` and Core's transient result; Core still performs every probe.
Guide impact: **updated** in the storage repository guide. No new durable state is introduced;
accepted registrations remain in the pool master's Core storage journal and normal inventory
adoption path. This corrects the existing scope, not migration, HA or UEFI acceptance.

2026-10-03 scope correction (project owner): iSCSI target selection must also inspect the
selected target's LUNs and autofill every identity Core can detect. A single reported LUN is
selected automatically; multiple LUNs require explicit selection. Missing filesystem UUIDs
are not invented, and the exclusive-initiator attestation remains manual.
Core performs bounded read-only userspace iSCSI and native filesystem probing: no kernel
login/node, mount, formatting, writes, NAS configuration changes or disruption of existing
sessions. The Controller only relays the request and renders Core's transient observations.
API impact: **changed contract** — optional `target_iqn`, `iscsi_luns` results and
`storage-iscsi-inspect-v1` capability on the existing storage-discovery API/report exchange.
Guide impact: **updated** in [storage repositories](../guide/storage-repositories.md).
State owner: **none** — discovery remains transient; accepted registrations keep their
existing Core-owned journal/inventory adoption. Native read-only probing, partial/ambiguous
results, authentication, cancellation, compatibility and browser stale-result checks are
required before this correction is accepted. Existing storage/fencing phase gates do not change.


Date: 2026-09-11  
Status: Implementation authorized by project owner; single-host backend code and UI are in
review and disposable-lab testing. Acceptance remains open until the checks below pass.
Current results: [storage QA](../qa/storage-repositories/checks.md).  
Parent: [platform plan](2026-09-08-nex-platform.md), Phases 2–4.  
Review: Terra 5.6 medium workers reviewed code, roadmap and upstream storage constraints;
independent plan review accepted the corrected design on 2026-09-11. Local documentation
checks are recorded below.

## Outcome and scope

The required outcome is registration through Pool → Storage and VM disk selection, with Core
reboot and Controller replacement preserving registrations, credentials, identities and data.
Reboot and replacement validation remain open until recorded in the linked QA evidence.
Core connects, verifies, mounts, monitors and recovers storage. Controller authenticates the
operator, submits an API request and displays Core's observations and operation results.

Bring forward single-host repository integration after the existing VM lifecycle acceptance
checks needed by it pass. Preserve the open Phase 2 gate; do not treat dirty working-tree code
or planning as completed acceptance. Multi-host placement, member/master transitions and
shared-writer protection remain Phase 4 prerequisites. Snapshots, independent backups,
restoration and migration retain their own backend-specific Phase 5–7 gates.

Recommended sequence: common storage contract with NFS → local OpenZFS dataset → SMB3 →
exclusive-host iSCSI. Each backend delivers an actual end-to-end VM workflow before the next
expands it. Existing local directory storage remains supported.

| Backend | First supported repository layout | Access and limits |
|---|---|---|
| NFS | Existing external NFSv4 export, mounted by Core; existing qcow2 disk layout | One selected Core initially; shared filesystem access/placement only after Phase 4 exclusion and per-host checks |
| OpenZFS / ZFS | Existing imported/mounted dataset, or a Core-provisioned local zpool and `vms` dataset; qcow2 files | Local to its Core host; a ZFS pool is a disk aggregate, distinct from a Nex management pool |
| SMB | Existing external SMB3 share, mounted by Core; qcow2 files | One selected Core initially; exact client/server dialect, permissions, locking and durability must pass VM-I/O tests |
| iSCSI | Existing external target/LUN with an existing ext4 or XFS filesystem; Core logs in and mounts it for qcow2 files | Exactly one Core initiator has writable target access; ordinary filesystems must never be mounted by two hosts |

This is repository consumption, not a NAS/SAN administration product. Initial registration
never formats a LUN, imports a zpool, changes vdev topology, creates a target/export,
or destroys storage. The later Core-owned ZFS provisioning slice creates a new zpool only from
operator-selected devices with its own erase/retry gates; see the current ZFS plan below.
zvol/raw block VM disks, LVM-on-iSCSI,
multipath, SMB multi-host placement and target administration are follow-on slices with their
own checks. Native ZFS snapshot UI, replication, encryption-key management and deduplication
are also outside this first integration. These are proposed implementation boundaries, not
claims that the user requested their permanent exclusion.

Decision: project owner, 2026-09-19 — ZFS pool/dataset provisioning from operator-selected
devices, a pool-wide or single-host scope, and OpenZFS in the Core distribution are implemented
in the working tree with bounded lab evidence. [ZFS repository provisioning](2026-09-19-zfs-repository-provisioning.md)
supersedes the earlier ZFS boundaries here; its master-transfer and external-distribution gates remain open.

## What exists and what must change

The initial source review included unrelated ongoing VM, console, API and website changes.
The table records that starting point; implementation now extends these flows without replacing
the unrelated work. Current contracts are documented in [API reference](../API.md).

| Flow | Inspected implementation | Required change |
|---|---|---|
| Registration | `web/src/managed.tsx` → `GET/POST /api/v1/storage` in `internal/controller/managed.go` → `storage-register` | Replace path-only registration with a validated backend-specific source and Core-reported readiness |
| Protocol and dispatch | `internal/protocol/managed.go`, `internal/protocol/protocol.go`, `internal/agent/managed.go` | Extend registration, inventory and results; retain authenticated outbound command transport and host-owned execution |
| Durable state/adoption | `internal/vmhost/managed.go` journal and `Applied`; `internal/controller/managed.go` adoption | Preserve IDs/names/source/credentials on Core and reconstruct the Controller view without re-registration |
| Disk creation/edit | `managedDiskPlan`, `qemuCreateArgs`, managed create/update in `internal/vmhost/managed.go` | Keep qcow2 layout; resolve and revalidate backing identity/capabilities across every affected caller |
| Storage validation | `localStorage`, `trustedManagedPath`, `registrationConflict` | Current filesystem allowlist is ext4/XFS/Btrfs; permission, alias, mount identity and capacity checks need backend-aware handling |
| Media and lifecycle | Managed ISO import/URL download, VM power/media/update and local recovery paths | Trace every storage/path caller; a backend must work beyond the registration form and initial creation |
| Startup and delivery | Core systemd/package configuration, libvirt autostart and host-local appliance engine | Restore mounts/sessions before eligible guest start, retain independent local recovery and protect existing appliance behavior |

Do not merely append filesystem magic numbers to `localStorage`. A missing remote mount can
expose a writable local directory; writing there would silently put disks on the OS filesystem.
A successful root probe also does not prove that the QEMU service account can read/write the
share. Do not remove the existing local permission or path safety checks globally to accommodate
NFS root squash or SMB permission mapping.

Review [BUG-AP-0001](../BUG_LEDGER.md#bug-ap-0001--appliance-ownership-must-survive-management-revocation):
management revocation and lost contact do not establish storage fencing.

## Host-owned records and recovery

**Planned state.owner: core.** Reuse the managed operation journal under
`/var/lib/nex-core-agent/managed-vms/` for durable accepted intent/results. Extend its storage
record with backend kind, canonical source identity, display name, stable repository ID,
selected/eligible host IDs, configuration generation and credential reference. Core creates
resource identity; a Controller request ID is only an idempotency key. Do not add a second
independently authoritative registry or put an authoritative pool setting in Controller SQL.

The authoritative repository record is a projection of **Core journal events**, not another
mutable registry: successful register, configuration/credential update and detach events carry
the repository ID and committed generation. Core serializes each mutation, checks the expected
generation, and journals its intent before native side effects; only a reconciled success advances
the projection. Failed/pending events remain operation history and cannot overwrite active state.
A detach success is a tombstone that prevents old registration events from reappearing. `Applied`,
`managedStorage`, conflict checks and adoption must use the same projection, including tombstones
so Controller removes obsolete mirrors. Recheck appends an observation/result without changing
configuration generation. Retained disk/media references continue to block detach.

For new registration, Controller supplies only the request/operation correlation ID; Core allocates
and durably records the repository ID before any side effect. A lost reply recovers that same ID
from the journal. Controller's existing ID allocation and optimistic resource row become a pending
request until Core reports its ID. Existing successful local registrations project as generation 1
with their IDs unchanged; no journal rewrite or destructive reset. Storage-ID lookup must never
select the first historical registration and ignore subsequent changes or tombstones.

Keep storage secrets in root-only Core-local files under a dedicated directory in the agent
state tree, referenced by the journal rather than embedded in commands/results retained by
Controller. Key custody, if at-rest encryption is used, stays on Core. In a single-host pool that
Core is the master.

**Credential handoff choice:** reuse the authenticated outbound agent connection with a separate,
transient handoff, before submitting the durable registration/update command. The administrator
submits credentials plus a unique credential request ID to a write-only Controller endpoint protected
by session, Origin/CSRF and pool authorization. Controller holds the bytes only in bounded memory
for a short deadline; it never calls the normal command/resource persistence helper with them.
The addressed Core receives an opaque handoff handle through its outbound exchange, fetches the
payload over its authenticated connection, validates pool/host/request scope, atomically persists
the secret and receipt on Core, then acknowledges that receipt. Handles expire and only the
addressed authenticated Core can redeem them; retry by that Core is permitted until acknowledged.

Only Core's durable acknowledgement lets Controller return credential-ready success. Offline Core,
expired handoff or Controller loss before acknowledgement produces unavailable/uncertain delivery,
not an accepted repository operation. The client can retry the same credential request ID; Core
returns the existing receipt and never creates a second secret. Conflicting replays are rejected
using a Core-private keyed comparison, without publishing a password digest. If the acknowledgement
or browser reply was lost, a scoped status request recovers the receipt through Core inventory.
A new Controller can discover those receipts too; it does not need the credential bytes again.

Normal durable storage commands contain only the opaque Core receipt/reference; Core verifies that
it belongs to the intended repository/pool and keeps the previous credential active until a rotation
succeeds. Unreferenced receipts are visible for explicit safe cleanup and never implicitly attached
to a different repository. Test the full request/body, database, queue, report, diagnostic and log
boundaries for leakage, plus loss before delivery, after persistence, after acknowledgement and
during rotation. No accepted storage operation depends on the transient broker staying alive.
In Phase 4 the master keeps the durable registration and recoverable secret; authorized members
receive only the material needed for their own access. Never return a secret in ordinary
inventory, generated diagnostics, logs, task text or API reads.

Native mount units, iSCSI node/session configuration and ZFS dataset properties are operational
host state. The journal records what Core owns and can reconcile; preserve administrator-owned
configuration. Registration must not adopt or overwrite an unrelated existing unit/mount.
Core keeps iSCSI ownership records in its private `iscsi-owners` state subdirectory, separate
from the native initiator's scanned node directory; preparation and detach use the same
configured state root. These records bind cleanup to the selected LUN and filesystem identity.
The existing serialized Core worker periodically reconciles one due unresolved storage journal,
using the original accepted command and choosing the oldest observation first. It excludes
definitively failed work and runs outside heartbeat reporting. Core's durable recovered outcome
updates the original Controller task projection; replaying a client request does not itself run
a new operation. This recovery continues without Controller availability.

Adoption path: Core `Applied` → `Inventory.managed` and per-host observations → authenticated
agent report → Controller's existing adoption/list flow. Extend that path with stable backend
identity, credential-present status, capability/health and durable failure outcomes. A fresh
Controller recovers the same repositories and VM references without asking for share passwords,
CHAP secrets or mount paths. Do not transfer secrets to it to achieve adoption.

When members become available, the pool master must drive member work and retain its results
through an authenticated Core-to-Core contract; the Controller must not become a multi-host
storage job runner. Until that Phase 4 contract exists, reject multi-host attachment. Master
replacement must transfer/recover these records and secret custody before accepting mutations.

**Planned host-loss recovery requirement, 2026-09-12:** network storage must eventually support
[manual replacement-Core recovery](2026-09-08-nex-platform.md#manual-recovery-after-permanent-host-loss)
of surviving VMs, including loss of the pool master or sole host. Core-owned recoverable VM/pool
metadata, required firmware state and safe ownership transfer are prerequisites, not capabilities
provided by this storage-registration slice. NFS and SMB require per-host checks and exclusion of
the former VM owner; ordinary-filesystem iSCSI requires exclusive LUN handover, never concurrent
mounts. The linked Phase 7/8 acceptance gate covers each backend separately and remains outstanding;
automatic HA is not required for the manual workflow. API impact of this clarification: **none**;
the linked plan defines the future Core contracts and guide work.

## Common behavior and safety

- Identify a repository by source identity, not just a mount path or hostname. Record NFS
  server/export identity, SMB server/share identity, ZFS pool/dataset GUIDs, or iSCSI target IQN,
  LUN identity/WWID and filesystem UUID. Reject aliases/overlapping registrations where identity
  or exclusivity cannot be established. Report capacity once per backing repository; do not add
  the same free space once per host or per ZFS dataset sharing a zpool.
- Separate intended configuration, operation completion, current access and observation freshness.
  Report per-host reachable/readable/writable status, total/available capacity, effective quota,
  capabilities, observation time and actionable failure. An old successful registration is not
  a permanent placement approval. Controller displays Core's decision; Core rechecks at use.
- Use native mount/session tooling with argument arrays, strict typed validation and controlled
  mount locations. No arbitrary shell fragments, mount options or device paths from the client.
  Validate at both API and Core trust boundaries. Secret files use restrictive modes; avoid
  command-line passwords and accidental disclosure through subprocess errors.
- Match source and mount identity immediately before every disk/media mutation and start, and
  prevent mount replacement during the operation. A disappeared/replaced mount, read-only export,
  quota exhaustion or identity mismatch blocks new writes; never fall back to an empty mountpoint.
- Test access as the actual QEMU identity as well as Core. Account for NFS UID/GID/root-squash
  behavior and SMB ownership mapping without world-writable directory workarounds.
- Bound probes independently of heartbeats and storage mutation work. Kernel I/O can remain
  blocked despite a userspace timeout: cap outstanding probe processes, stop scheduling more
  on that repository, and report unknown/degraded while unrelated hosts/guests stay observable.
- Registration/reconnect never starts a second VM writer or forcibly unmounts active storage.
  Before Phase 4, enforce one assigned Core and reject a repository already owned by another
  pool/host where detected; server-side access restriction is a deployment prerequisite.
  Do not claim safety against an unmanaged initiator that retains access.
- Provide recheck, credential replacement and safe detach/unregister. Block detach while VM
  disks, retained disks, media, protected appliance disks or operations depend on the repository.
  Unregister removes only Nex-owned configuration after dependency checks; it never erases data.
  Failed/ambiguous operations reconcile native state and the journal before retry or cleanup.
- Keep mounts/imports/session recovery and guest boot ordering independent of Controller.
  Cover native libvirt autostart and local power/recovery, not only Controller-issued starts.
  Storage unavailable at boot prevents affected guests from starting, while local recovery and
  unrelated local-storage guests remain usable. No force import or cross-host takeover on timeout.

## Delivery slices and acceptance

### 0. Establish the common contract and baseline

Trace the current accepted create/edit/delete, media, power, bootstrap and adoption callers.
Retain one local-directory regression path. Define the typed backend fields and capabilities,
secret handoff, operation states and repository identity rules before implementing new mounts.
Reuse existing journal/command/UI/task machinery; no plugin framework or new scheduler.

Close the applicable Phase 2 guest install, disk persistence, local recovery and Controller
outage checks before accepting backend VM behavior. Existing in-flight owners retain their files;
partition implementation ownership around protocol, Core, Controller/UI, packaging and test evidence.
Shared contract edits land first; consumers implement against that agreed shape.

### 1. NFS and the usable repository workflow

Add existing NFSv4 export registration, Core-managed persistent mounts, QEMU access/capacity
checks, health reporting, VM creation/edit/media/power and safe removal. Use hard-mount data
semantics; do not substitute soft mounts to make a UI timeout look successful. Specify the exact
supported NFS version and mount options in package/contract tests and operator documentation.

Native testing found that Linux NFS `statfs` reports an unavailable zero filesystem identifier,
which falsely collided for distinct exports. Use the mounted device's NFSv4 protocol filesystem
ID from the kernel volume table, namespaced by the connected server address and port, after
verifying the exact mount/source. Missing or ambiguous kernel identity blocks use. Earlier
unshipped statfs-only records remain preserved and unavailable; never silently rewrite their
identity. This initial profile does not promise transparent server-address failover.

Gate: register → upload/select ISO → install a guest with disks on NFS → write and sync data →
reboot guest and host → replace Controller → recover the same inventory and guest data.
Inject missing export, root squash/permission failures, server outage/restart, full/quota-limited
storage, mount loss/replacement and interrupted registration. Heartbeats must remain usable.
Reject a second assigned writer; multi-host placement remains closed until Phase 4 proves it.

### 2. Local OpenZFS dataset

Recognize an existing imported/mounted ZFS filesystem dataset by GUID and host, use its mountpoint
for qcow2 files, and report zpool health and dataset quota/available capacity. Require keys already
available through host boot configuration for any encrypted dataset admitted; otherwise report
locked/unavailable and block use. No force import, pool upgrade or changes to existing properties.

Packaged-service testing showed that an arbitrary existing dataset mountpoint is read-only inside
the agent's strict filesystem sandbox. New registrations therefore need a Core-owned host-systemd
bind mount under the existing writable repository root, preserving the original dataset and its
properties. Core records that stable alias for VM disk paths and verifies both source GUIDs and
the alias before use. Detach removes only the owned alias. Earlier unshipped lab records using
direct paths are preserved and reported unavailable; this change does not silently rewrite VM
disk references or introduce a data-migration feature.

Keep synchronous-write semantics intact; never set `sync=disabled`. Do not promise thick/reserved
capacity solely because qcow2 fallocate succeeded: validate reservation/quota behavior on the
supported ZFS version or reject that provisioning mode with a clear reason.

Gate: VM lifecycle/data/replacement tests plus dataset missing/renamed/unmounted, locked dataset,
quota/full zpool, degraded/read-only pool, host reboot and kernel/package upgrade with rollback.
Native ZFS snapshots do not establish complete multi-disk VM snapshots or independent backups.

### 3. SMB3 share

Reuse the filesystem repository flow with Linux CIFS mounts and scoped credentials. Start with
one Core writer host and a tested SMB3 server/client profile; require strict cache/locking and
flush semantics. No SMB1, `cache=loose`, `nobrl`, `nostrictsync`, silent guest fallback or disabling locks for speed.
SMB authentication and transport protection policy must be explicit and tested; use encrypted
SMB3 transport for the supported initial profile. Keep arbitrary server tuning outside Nex.

Restart testing showed that CIFS `statfs` filesystem IDs can change while the same Samba share
remains intact. New registrations therefore create or reuse a small durable `.nex-storage-marker`
file at the verified share root. Core records its opaque ID in the master's journal, checks it
before use and uses it to reject duplicate backing aliases; volatile filesystem IDs are diagnostic.
The marker survives detach and Controller replacement. Missing, replaced or unsafe marker files
block use. Earlier unshipped records retain their strict legacy identity checks and are preserved;
this does not silently rewrite an existing repository's identity after a mismatch.

Gate: QEMU guest sync/data checks, concurrent-open refusal, permission mapping, credential change,
server restart/reconnect, share disappearance, full storage, host reboot and Controller replacement.
Test thick/thin allocation separately; reject a mode the server cannot implement faithfully.
Do not advertise continuous availability, transparent failover or multi-host live VM placement
from a successful mount; these require separate server-specific evidence.

### 4. Exclusive-host iSCSI LUN

Discover/login using the native initiator against an explicitly selected target/LUN. Verify
expected WWID, filesystem UUID/type, absence of conflicting mounts/holders and target-side
access restricted to the selected Core initiator before using the existing filesystem. Resolve
stable device identity after every login/reboot; never bind a repository to a changing `/dev/sdX`.
Reject blank LUNs with a preparation prerequisite; do not format them. Reject multipath or
ambiguous duplicate paths until a separately tested multipath slice exists.

Single-initiator target ACLs are required admission evidence; CHAP alone is authentication, not
exclusive access. Where generic discovery cannot verify target ACLs, require explicit operator
attestation of dedicated mapping and mark the external enforcement boundary honestly. This is
host-bound block storage accessed over the network, not shared VM placement.

The initial implementation allows one repository per target/portal session. Duplicate session
claims and canonical WWID/filesystem aliases are rejected before login, independent of
Core-generated mount paths. This avoids one repository detaching another repository's session;
multi-LUN session sharing needs separate ownership and recovery evidence.

Gate: VM lifecycle/data/replacement tests plus wrong/reused LUN, changed WWID/UUID, existing mount,
wrong CHAP, target restart, link loss during guest writes, full filesystem and boot session order.
Logout/detach is forbidden while any dependent disk is active or retained. No other Core receives
writable access to the same ordinary ext4/XFS filesystem.

### 5. Phase 4 shared placement gate

Enable NFS on multiple pool members only alongside durable master/member authority, authenticated
host-to-host execution/results, per-host verification and writer exclusion covering local starts,
libvirt autostart, Controller commands, partitions and reboots. Evaluate native libvirt lock
management against the exact server/filesystem; demonstrate behavior, not just configuration.
Never use Controller liveness or an in-memory reservation as exclusion proof.

Gate: two hosts can use the same repository with distinct VM disks; the same writable VM/disk
cannot run twice during simultaneous requests, lost contact, process restart or stale adoption.
Prove source identity across hosts and capacity counted once. NFS shared access alone neither
completes migration nor grants automatic failover. SMB shared placement and iSCSI shared-block
storage require additional designs and remain unavailable in this sequence.

## API and documentation impact

**API impact: changed contract**, plus **new contract** for protocol negotiation, credential
handoff/recovery, storage update/recheck/detach. Both Controller and Core implement the change;
[API reference](../API.md) and its machine-readable contract describe the implemented shapes.

Trace: Pool Storage/VM forms → Controller session/Origin/CSRF and pool authorization → existing
outbound agent command transport → Core journal/native storage operation → Core inventory/results
→ Controller adoption, storage list and task view. Extend `GET/POST /api/v1/storage`,
`storage-register`, `StorageRegistration`, `Inventory.managed` and VM/media storage validation;
add only missing lifecycle operations using existing operation status/replay machinery. New routes and schema fields are defined together with both implementations in the API reference.

Require administrator-only repository/credential mutations with cross-pool denial, typed source
validation, redacted errors and explicit unsupported/unavailable/conflict outcomes. Repeated
request ID plus identical intent returns the same host outcome; changed intent conflicts. Core
journals progress before side effects, retains uncertain results, and reconciles on restart or
reconnect rather than logging in/mounting/allocating again blindly. Disconnection of Controller
must not cancel already accepted host work or erase its result.

**Version choice:** introduce protocol version 2 plus `storage-repositories-v1` for the new
storage report/command shape. Upgrade Controller first: it accepts existing version-1 Core reports
and version-2 reports through explicit version-specific decoding; it dispatches new storage commands
only to version-2 peers advertising the capability. Core upgrade preflight requires a positive
version-2 support response from Controller before activation. Older Controller rejection or an
unreachable preflight blocks that upgrade; do not optimistically send new fields to its strict decoder.
This is a coordinated upgrade policy, not arbitrary mixed-version support.

Version-2 Core keeps existing local journal/resource IDs and projects legacy records as above.
Nex refuses APT removal or downgrade to a package without `storage-format-v2` while Core
retains version-2 repository events, credential receipts or tombstones, or while Controller
has an enrolled `storage-repositories-v1` Core. Detaching does not erase history to bypass
this rule. The guard covers APT and package removal; manually installing an older package
through `dpkg` can bypass it and is unsupported. Restore a matching `storage-format-v2`
package before management use. This is the enforceable package-manager scope, not protection
against an administrator replacing binaries directly. Retain a known-good version-2 artifact
for kernel/package recovery. Running VMs continue through failed management upgrades.
Test the refusal paths, legacy records and positive version-2 recovery; never reinterpret a
new backend as a local directory.

Implementation updates [API reference](../API.md), machine-readable contracts and generated
reference in the same slice. **Guide impact: updated** — [storage registration](../guide/storage-repositories.md), guide
navigation and VM creation describe the development workflow and link its actual verification.
Backend availability remains qualified by the open acceptance gates.

## Packaging, evidence and readiness

Use native Debian packages and existing Go/process integration. Assess `nfs-common`, `cifs-utils`,
`open-iscsi` and `zfsutils-linux`/`zfs-dkms` plus matching headers against the actual Nex package,
ISO and systemd sandbox rules. The installer package list and Core package dependencies now
include `nfs-common`, `cifs-utils`, `open-iscsi`, `zfsutils-linux` and `zfs-dkms` with matching
kernel headers. OpenZFS is bundled for the ZFS delivery slice; its installed-host and
kernel-upgrade paths have bounded disposable-lab evidence.
Do not assume service permissions allow mounting/device discovery just because tools exist.

OpenZFS adds kernel-module delivery requirements: exact Debian package/source licenses,
CDDL/GPL redistribution review, DKMS build against supported kernels, Secure Boot signing/key
handling where enabled, offline dependencies, upgrade failure and known-good boot recovery.
Record evidence under [licensing/source requirements](../LICENSING.md). This plan is not a
licensing clearance or a Secure Boot claim.
No ZFS root installer conversion or custom kernel is required by the proposed dataset integration.

For every backend, retain exact tested Nex/kernel/libvirt/QEMU/client/server versions, lab scope,
commands, observed results and sanitized captures, under the
[verification rules](../../AGENTS.md#verification-and-bug-memory).

Browser acceptance covers registration/recheck/removal, VM disk selection and mixed repositories,
keyboard/focus, empty/loading/error/stale states, permission denials and actionable failures.
Show backend, local/remote versus shared eligibility, accessible host, usable capacity and last
verification. The storage target picker consumes Core capabilities, including permitted disk
provisioning and ISO support; avoid optimistic frontend guesses.

Before calling a backend supported, independently review its API/state ownership, security,
retry/outage behavior, actual UI captures, documentation walkthrough and guest data evidence.
Later snapshot/backup/restore/migration must pass their own checks for every enabled backend;
do not claim every repository supports every operation just because it holds qcow2 files.

The published roadmap now lists the four single-host repository integrations as in testing.
Multi-host shared placement remains a separate outstanding Phase 4 deliverable.

### Historical planning verification

The pre-implementation plan review (2026-09-11) established design seams and gates only, not
backend behavior. Current evidence is in [storage QA](../qa/storage-repositories/checks.md).

### Primary technical references

- [Libvirt storage backend model](https://libvirt.org/storage.html).
- [Libvirt lock management](https://www.libvirt.org/kbase/locking.html).
- [Linux NFS client options](https://manpages.debian.org/trixie/nfs-common/nfs.5.en.html).
- [Linux CIFS mount behavior](https://manpages.debian.org/trixie/cifs-utils/mount.cifs.8.en.html).
- [OpenZFS Debian installation](https://openzfs.github.io/openzfs-docs/Getting%20Started/Debian/index.html).
- [OpenZFS dataset properties](https://openzfs.github.io/openzfs-docs/man/master/7/zfsprops.7.html).
- [Open-iSCSI initiator documentation](https://github.com/open-iscsi/open-iscsi/blob/master/README).
