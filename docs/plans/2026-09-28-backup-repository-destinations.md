# Backup repository destinations and health

Paused-work continuation: [remaining work and safe handover](2026-09-28-backup-repository-destinations-handover.md).

Date: 2026-09-28. Status: implementation plan; no new destination or automatic
repository health check is implemented by this document. Owner: project owner.

Parent: [recovery delivery](2026-09-25-recovery-snapshots-backups-restores.md).
This expands the NFS-only destination scope after the original Phase 6 disposable
close. It does not retroactively change that close record or close the [current
recovery candidate's open safety gate](2026-09-25-recovery-snapshots-backups-restores.md#recovery-delivery-snapshots-backups-and-restores).
The project owner's 2026-09-28 decision requires complete retained history and
safe promotion beyond 64 MiB. The current candidate implements incremental
v2 signed authority pages, removing the former 64 MiB hard transfer bound
without dropping retained runs, policies, repositories, points, pins or backup
operation receipts. Simulated greater-than-64 MiB transfer, promotion,
garbage-collection and race checks pass. A current-candidate three-Core
fixture registered NFS, replicated 1,052 directly seeded runs beyond 69 MB,
fenced the old master and promoted a member with the exact retained run-ID set,
repository and receipt. Authenticated Controller paging read all 1,052 runs
after promotion. This is bounded native authority proof; the seeded catalog
was empty at promotion, and the original
Controller promotion task initially remained `needs_attention` after Core
durably succeeded. A rebuilt Core/Controller pair later reconciled that exact
retained operation to `succeeded` without another fence or transfer. Payload
backup/restore after promotion later passed in a bounded NFS fixture as
described below. Independent review found an
older-Controller acknowledgement gap for that recovered receipt. Focused
red/green source checks and independent review passed for a Controller fix.
A later clean two-Core fixture ran a verified historical Controller through
that exact acknowledged-result gap, then replaced only Controller with the
current build. The original task settled from Core inventory without another
fence or transfer; details are in the continuation handover and QA report.
A post-promotion NFS backup initially published a complete manifest and two
matching artifacts while lagging-member authority admission left its run
pending. BUG-0687's exact-replay fix settled that original operation without
republishing; the retry returned HTTP 202/`succeeded` with one catalog point.
The exact old restore request initially registered a zero-NIC VM but remained
pending/`needs_attention` after authority admission. BUG-0689's reconciliation
fix settled that same operation after a matched-overlay restart. Its journal,
run and API reported `succeeded`; the shut-off VM started and stopped in
isolation, and a separate network-free read-only Linux guest read both disk
markers. The marker disks had no guest OS. The original 1,052 seeded run IDs
remained intact; the surviving member's signed generation 19 matched the
master's 1,054-run history. A later matched three-Core fixture fenced its old
master and retained all six repository, 15 policy, 25 point and 77 run IDs.
Fresh NFS, SMB3, S3 and SFTP probes passed on the promoted master. One new
two-disk backup and independent source-unavailable restore per kind succeeded;
each zero-NIC restored VM booted in isolation with expected system and data
markers. Exact replays created no duplicate runs or points, and the surviving
member verified the final signed generation and digest. Fresh Controller
requests then restored each point on the other member while source disks were
withheld. All four zero-NIC guests booted in isolation with expected system
and data markers and fresh boot timestamps; exact replay made no duplicate
run. NFS needed an exact-ID forced stop after ACPI timeout, and a shorter
initial SMB3 boot also needed a forced stop before a longer clean boot. This
closes bounded post-promotion backup and cross-host restore proof for all four
kinds, not the complete acceptance matrix.
A new Controller with an empty data directory and CA then enrolled the
promoted master and surviving member without the previous Controller database.
It adopted the exact six repository, 15 policy and 29 point ID sets and
reported an 89-run total,
completed one fresh backup per kind and independently restored a new NFS point
cross-host while source disks were withheld. That zero-NIC guest booted alone,
both markers matched, exact replay created no duplicate and the signed member
replica matched the promoted master. A complete two-page Controller read after
those operations matched all 94 Core-local run IDs. The fenced former master stayed off and
was not enrolled. The fresh Controller later restored retained SMB3, S3 and
SFTP points cross-host with the source disks withheld; each zero-NIC two-disk
guest booted alone, both read-only markers matched, and exact replay created
no duplicate. Repository and point ID sets stayed at six and 37; runs rose
from 98 to 101. Mixed-version replacement and the remaining fault/lifecycle
matrix are still required.
The replacement also exposed BUG-0696: its Members view retained the offline
former master's ID but linked to a missing Hosts panel, and older Core
membership had no saved hostname to show. The candidate now records an
optional hostname in Core membership at pool creation/join and leaves
unenrolled members unlinked. Legacy missing names remain IDs. A separate new
disposable pool then proved current Core saved both hostnames in master
membership and a verified signed replica; a fresh Controller with no old
database adopted only the live master and showed the offline member by Core
name and unknown connection, with no Hosts row. A later headless browser
capture showed that named offline member with unknown connection and no Hosts
link. Mixed-version native checks remain open. API impact: **changed
contract** for the additive peer join and Core membership fields; Controller
uses the existing members response name field. State owner: **Core** membership
on the master, replicated in signed authority and re-adopted from inventory.
Core now waits for live authenticated format support from every ready peer
before backfilling a legacy name, and waits for an explicit Controller
acknowledgement before reporting the field to a strict older Controller.
Lost-response join replay remains valid after a focused regression fix;
native mixed-version upgrade is still open.
Four staggered policies subsequently dispatched one succeeded backup and
point per destination on the promoted master with the fresh Controller
stopped. Core-local inventory ended at 37 points and 98 runs; the four test
policies were paused and the surviving member verified the final signed
authority. Missed Core-down slots and interruption remain open.
Five later new-ID registrations failed on missing NFS export, wrong SMB3/S3
credentials, wrong SFTP host key and S3 TLS hostname mismatch. Existing
four-kind access recovered, and the six repository, 37 point and 98 run IDs
were unchanged. BUG-0697 adds unused backup receipts to Core's existing
redacted support list and retire command, with durable replay tombstones.
Focused and race tests and independent source review passed. A later matched
current-source lab retired all four old failed receipts through the fresh
Controller, kept five active receipts ineligible, and confirmed that four-kind
access plus the exact repository, point and run ID sets survived.
The authority snapshot/generation race found in source review has a focused
red-before/green-after regression and fix; the resumed member replication
and promotion check passed in the disposable fixture.

## Product decision and operator model

An operator registers any supported destination as a **backup repository**. Core
checks that repository on a schedule, reports its current health and raises an
operator-visible alert when it goes offline. A backup job or policy names one
repository ID. Core uses that repository's destination for backup, restore,
catalog rebuild and retention; Controller does not copy data or run the job.
Repository choice remains explicit and visible in the job and restore review.

Support **NFS, SMB3, S3-compatible object storage and SFTP** in this expansion.
The project owner narrowed the scope to these four kinds on 2026-09-28 after a
disposable server profile failed the required publication checks. Do
not implement or document any other destination kind in this slice. FTP is
excluded.

All four kinds belong to one repository list, health view, job selector and
restore flow. Do not build a separate job scheduler, provider catalogue, cloud
account model or Controller-owned repository state. An external repository is a
separate failure domain from a VM's source disks; do not present a destination
on the same underlying storage as an independent backup.

## Implementation status and correction boundary

The development worktree now contains typed NFS, SMB3, S3 and SFTP repository
sources, Core destination storage implementations, credential handoff, health
probes and Controller form changes. These are implementation candidates, not
supported destinations. Source checks and selected service smoke checks are
recorded in [destination QA](../qa/2026-09-28-backup-repository-destinations.md);
bounded four-kind product backup and restore passed, while the complete
per-kind fault and lifecycle acceptance matrix remains open.
Native diagnostic probing found stale credential errors for transient S3/SFTP
member preparation and SMB3 identity conflicts despite a matching share
marker. Focused red/green source checks passed after persisting member-local
registration before probe and comparing stable SMB share/marker identity.
Matched native probes later reported the selected repositories readable and
writable on both members after SFTP, S3 and SMB3 outages; broader expiry and
browser recapture checks remain open.
An SFTP cross-host restore was refused by the member after submission; its
peer repository preparation source fix passed a focused regression. A later
fresh request stayed pending because reconciliation polled a missing member
operation; its peer 404 source fix passed focused red/green checks, while
the fresh native cross-host restore later succeeded on both hosts with
isolated disk-marker readback in a lab-only SFTP capability overlay. Broader
interruption and recovery checks remain open. An earlier Controller-absent SMB3 scheduled
run later settled succeeded with one catalog point in a selected disposable
capability-only lab; broader schedule and recovery acceptance remain open.
Later separate Controller-absent S3 and SFTP policy slots each published one
point and one succeeded run on matched disposable Core guests. Those policies
were paused afterward. Four-kind post-promotion dispatch later passed with
Controller absent; missed Core-down slots and interrupted dispatch remain open.
In a later service-down schedule check, S3 and SFTP each settled one failed
run and no point while its destination was offline; both probes returned
online after service recovery, and the existing 19 catalog points remained.
This does not prove missed Core-down slots, interrupted publication or alert
deduplication.
Later Controller-absent policy Run now and `keep_latest=1` retention passed
for all four kinds with protected older points retained. NFS and SMB3 passed
selected missing-index catalog rebuilds. A larger S3 scan exposed BUG-0695:
Core-local HTTP timed out and exact retry could not resume its pending receipt.
The changed Core returns `202/pending` and runs the bounded scan independently
of the request; the same S3 operation settled after a Core restart. Its
missing index had already been restored before that retry. The older SFTP
fixture contained incomplete delete records, so Core refused rebuild and the
selected local index was restored. A later full-pool check passed a fresh
missing-index S3 rebuild and a separate clean SFTP missing-index rebuild;
their selected points returned byte-for-byte. Interrupted prune recovery
remains open for the older SFTP repository.
BUG-0698 then found a separate interrupted NFS/SMB3 prune gap: partial
filesystem deletion blocked exact settlement and rebuild. A durable
destination delete intent now lets Core finish only a recorded prune, while
corrupt unrecorded points remain refused. Focused red/green and race checks,
the full agent suite and independent source review passed. A new disposable
fixture then used a documented ignored hook to stop Core with a partial NFS
and SMB3 prune; after process death and restart on the clean current-source
capability overlay, exact operations resumed once, rebuild passed and the
new/protected points and source disk hashes survived. Broader interruption
and final release proof remain open. API impact: **existing contract**
for retention and catalog-rebuild retry behavior. State owner: **Core** catalog
and run with a durable destination delete intent; guide impact: updated.
At that checkpoint, the operator guide retained NFS instructions only; it now
covers all four enabled destinations. Core's scheduled
jobs already carry `repository_id`; no second selection mechanism is needed.

Later bounded matched-build labs passed stopped two-disk backup and cross-host
restore for all four kinds after the original disks were made unavailable;
the NFS restore was repeated after the peer identity patch. Restored disks
were read through an isolated read-only Linux guest. All five registered
repositories, including two SMB3 shares, eventually reported both members
readable and writable after selected service outages. The fixture's source
VM disks were markers without a guest OS. A later selected low-resource lab
used a bootable two-disk zero-NIC guest: NFS, S3, SFTP and SMB3 each passed
online crash-consistent backup and independent cross-host restoration with
both original disks unavailable. Read-only marker hashes matched and each
isolated restored guest updated its boot marker. NFS also passed a bounded
lost-HTTP-reply exact retry. BUG-0691 corrected S3/SFTP QEMU staging under a
root-only path, and a deliberately inaccessible work-root failure settled one
failed run with no point. BUG-0692 corrected transient S3 peer readback
classification: the same member-completed restore later reconciled on the
master without a second VM. These are bounded local checks and do not prove
application recovery beyond the planted markers. The full per-kind lifecycle,
fault, promotion, and installed-package gates below remain open. These labs
used ignored capability overlays for SMB3, S3 and SFTP, and all selected
fixture processes were stopped afterward.
In a later matched Controller-absent fixture, all four kinds passed Core-local
test restore, isolated boot and two-disk marker readback, recorded `passed`
verification, and cleanup of the test VM and disks while retaining the point.
The selected S3 guest needed an exact-ID force stop after ACPI shutdown timed
out. Interrupted cleanup and application-level recovery beyond the planted
markers remain open. A read-only native SFTP VM/disk absence receipt was saved
after independent review identified that evidence gap.
A later matched disposable S3 Controller policy Run now returned HTTP 202,
settled succeeded and published one run/point; exact replay returned the same
operation. A second request discarded its HTTP reply, and exact replay found
the already dispatched operation, which also settled with one run/point.
Controller Run now was then exercised for NFS, SMB3 and SFTP: each submission and
exact replay settled one succeeded/published run and point. A live browser
SMB3 policy switch submitted HTTP 202 and Core reported the new repository
at revision 2. A separate live browser SFTP credential handoff returned
HTTP 201, but the queued repository registration failed on Core with no new
repository while a ready member guest was off. BUG-0694 traced the failure to
mandatory pre-commit credential delivery to that member. After a red/green
source fix and independent review, a matched disposable Core build passed a
second live browser SFTP credential handoff and registration with that guest
still off. The reconnecting guest later reported read/write access and a
verified signed replica at the master's generation with a repository manifest.
Credential rotation, broader post-promotion access faults and interruption
recovery remain open;
all fixture processes were stopped afterward.
An independent review found that terminal capture/upload failures could retain
full local staged disks. BUG-0693 now cleans up after a confirmed inactive
native job; its partial-conversion regression failed before and passed after
the fix. A selected disposable stopped NFS backup then injected a partial
conversion failure: one failed run, no staged directory or recovery point,
and exact failed-result replay. Online/interrupted-upload cleanup remains
open; the fault hook and fixture processes were stopped.

Existing SMB VM-storage mount and identity code is reused, but service mount
smoke does not prove backup publication or recovery on CIFS. S3 and SFTP use
remote object/file transfers and need selected server profiles that pass the
publication, crash-durability, prune and restore checks below. The worktree's
SFTP prune path has bounded lab evidence. Focused red-before/green-after
checks cover an interrupted S3/SFTP deletion-record hole. Matched native
permission faults after a durable delete record recovered exactly for S3 and
SFTP; abrupt deletion and crash-durability retests remain open. A follow-up
red/green SFTP check covers exact retry after a partially written completion
marker, with native remote interruption proof still open. Do not infer support from a typed
API, a visible form or a service probe.

## Core state and changed contracts

**Implementation API impact: new contract.** Keep the existing Controller
repository register/test/list operations, Core-local equivalents, peer dispatch,
backup job/policy `repository_id`, and inventory/adoption paths. Change the
repository schema to a validated kind-specific source and Core-observed identity;
preserve decoding and operation replay for existing NFS records. Add a backup
destination capability so Controller and Core refuse an unsupported kind or
member before dispatch. A mixed-version pool must not accept a job on a host
that cannot read its repository. Define typed source fields, required credential
mode, identity, health/freshness, error codes and capabilities in
`docs/api/openapi.json`, `local-openapi.json`, `peer-openapi.json`, and the generated
reference and `docs/API.md` in the implementation slice. Existing admin session,
CSRF, Core-local authentication and pinned peer identity remain the boundaries;
test invalid source, wrong pool, denied role, duplicate request ID, changed
payload on replay and old-version refusal.
The added authenticated Core peer repository-access POST checks exact member
destination/generation/identity or transiently prepares a missing local record
before probing, and a missing exact peer backup operation GET
now returns 404 for safe reconciliation. Existing Controller/Core report and
client API shapes are unchanged by those two peer fixes.

**Durable state owner: Core.** The pool master keeps repository ID, name, kind,
nonsecret source, credential reference, generation and last health transition in
`pool-backup-repositories.json`, replicated in signed pool backup authority. The
repository holds completed backup payloads and manifests. Existing master-owned
policies, runs, catalog, pins and receipts retain their records. A fresh
Controller reads all of these through authenticated master inventory and does
not require its predecessor's database. Current per-host access remains an
observation: after transfer it is unknown until the new master/member probes.
The last Core-owned offline/recovery event may be shown with its time, but must
never be presented as a fresh access result.

Never put passwords, access keys or private SSH keys in a
Controller command, `managed_resources` payload, inventory, authority page,
audit entry or error. Controller currently persists repository request bodies;
reuse/adapt its transient credential handoff and Core-private receipt pattern
before accepting secret-bearing kinds. Core securely delivers scoped secrets to
eligible executing members and promotion candidates so backups and restores
work without Controller or the old master. Define rotation, revocation and
failure after promotion; no stored secret may be recovered from a Controller
database. Pin SFTP host keys. S3 uses TLS with verified endpoint identity.

### NFS repository move safety — 2026-09-28

An NFS repository ID stays bound to its original source and observed
filesystem identity while recovery points refer to it. Core refuses a same-ID
source change before native preparation and an identity change under the
repository commit lock. Register and test a new repository ID for a move,
switch new jobs and Core-owned policies to it, and keep the old export and
record available for old-point restores. The existing Controller and Core
registration operations provide this refusal without changing their request
shape. The authenticated peer NFS payload now carries the master's observed
identity and requires matching updated peers.
The durable `pool-backup-repositories.json` record and authenticated master
inventory remain the adoption path. BUG-0690's source regression failed
before and passed after the fix. In a promoted disposable fixture, a same-ID
NFS source-change request and exact retry both returned HTTP 503; the durable
source, identity and generation stayed unchanged. Authenticated peer dispatch,
member access probes and promotion carry the master's NFS identity; a member
refuses a missing or different filesystem identity before the owner marker.
The focused peer adoption check passed. Matching updated Core peers are
required because older members derived identity from source alone. A matched
disposable fixture changed the export's kernel NFSv4 FSID; member access
failed and same-ID registration/exact replay returned HTTP 503. After the
original export returned, repository identity and generation stayed stable;
a retained old point passed test restore, two-disk marker readback,
verification and cleanup while the point remained. Separate current-master/
historical-member and historical-master/current-member fixtures later refused
a changed real filesystem identity before member work; each kept the exact
operation uncertain and restored a retained old point after the original
identity returned. Broader mixed-version recovery remains open.

### SMB3 credential rotation decision — 2026-09-28

Rotate SMB3 credentials by registering a **new repository ID** on a separate
share or directory, testing its access, switching Core-owned policies and new
jobs to that ID, then verifying a new backup and restore. Keep the old
repository and its credentials available for the old recovery points' restore
window. Revoking the external account before those points expire makes them
unavailable after active SMB sessions and cache are lost, but may not stop an
old-point restore immediately through an existing CIFS mount. Nex does not
automatically retire the old repository or remount an existing ID. Core
refuses same-ID SMB3 source or credential-reference
changes, so no in-flight backup or restore can see a changed mount under its
repository ID.
S3 and SFTP also refuse same-ID destination or credential-reference
replacement in the development candidate: retained points keep their exact
repository identity. A future move or rotation registers a new repository
ID and retains the old destination and credentials for old-point restores.
In the selected disposable Samba profile, disabling the old account/share
failed its access test but an existing mount still served an old restore.
After old-only unmount and Core restart, old health was offline, the new
repository stayed online and old restore failed before creating a VM; re-enable
and remount restored old-point access. This is profile evidence, not a claim
of immediate revocation on every SMB server.

**API impact for rotation: existing operation; changed policy update contract
in this slice.** The existing Controller policy `PUT` and Core-owned policy
upsert change `repository_id`; new ad hoc jobs name the new ID. The update now
requires the master's reported positive `expected_revision`, checked under
Core's authority lock. Missing/zero revision is refused at Controller input;
a stale revision fails the queued Core operation with a conflict. After an
uncertain reply, the UI retains the exact request and checks task status in
place before retrying. Existing repository registration and validation refuse
same-ID source/reference replacement. No new endpoint or Controller state is
needed. Create and pause contracts remain compatible.
**State owner: Core** in the existing `pool-backup-repositories.json` and
`pool-backup-policies.json` records, adopted by a fresh Controller through
authenticated master inventory. The policy switch action is implemented in
the development UI and reviewed with simulated API responses. A disposable
Controller API switch and old-point restore passed. Retained private fixture
receipts show a bounded new-point restore with exact marker readback while
the old account was disabled; this was a historical lab action inspected
later, without a new isolated boot. Broader revocation and live UI mutation
proof remain open.

## Destination behavior and publication safety

Use the existing complete recovery-point manifest, immutable point identity,
artifact hashes, catalog digest, durable operation ID, restore pins and
retention rules for every kind. Preserve the complete Core authority history
through paged signed transfer beyond 64 MiB and promotion; no retained records
may be dropped to make a destination usable. Put the smallest kind-specific storage actions
at the Core repository boundary: connect/verify, claim/verify owner, write and
read artifact, publish manifest, list completed points and delete a proven
eligible point. Do not fork the scheduler, restore workflow or manifest format
unless a backend requirement makes that necessary. Keep incomplete uploads
invisible to restore and catalog rebuild. On a lost reply, inspect the exact
operation and remote evidence before retrying; never publish two points or
release a restore/prune pin on timeout. Every read, write and prune rechecks
repository identity and current pool authority. Promotion must exclude the
former writer before the new master writes or prunes.

| Kind | Required Core proof before claiming support |
|---|---|
| NFS | Preserve mounted source/FS identity, owner marker, same-filesystem atomic publication and existing recovery checks. Re-run its regression as the baseline. |
| SMB3 | Reuse sealed SMB 3.1.1 mount and share-root identity marker with private credentials. Verify selected directory, QEMU write access and exact source on each host. Prove exclusive owner claim, file sync, publication visibility/durability, rename and reconnect behavior on the supported Samba profile; adapt publication if directory sync is unsupported. |
| S3 | Use a native Core object client, not FUSE. Bind endpoint, bucket and prefix; scope the key. Upload and verify immutable artifacts, create the manifest last with conditional write, then read/validate it before catalog settlement. Require tested conditional create, read/list visibility and writer fencing from each advertised S3-compatible service. Never treat an owner object alone as a distributed lock. |
| SFTP | Pin the SSH host key and confine the account to one repository root. Probe actual read/write/list/rename behavior. Use a server profile whose exclusive create, rename, sync and reconnect semantics pass fault tests, or use a manifest-last publication rule that does not depend on unproven atomic rename. |

For S3 and SFTP, an immutable, validated manifest is the publication
boundary. Catalog rebuild lists only final manifests and verifies referenced
artifacts before adoption. Prune names one eligible recovery point and rechecks
pins and authority; it never deletes an unchecked prefix or directory tree.
Document backend limits such as unavailable capacity reporting honestly.
Archive classes requiring delayed retrieval are outside this synchronous
restore contract. Protocol service success without a restored VM is not
acceptance. [S3 consistency](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html),
[S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html) and
[SFTP extensions](https://www.openssh.org/specs.html) are design references;
test the actual selected server implementations rather than assuming the
protocol name supplies their guarantees.

## Health, alerts and job selection

Core probes every registered repository at a bounded interval and timeout,
independently of Controller. Probe connection and exact remote identity, owner,
read/write/list and publication prerequisites using a disposable probe object
or directory with cleanup; do not count a TCP connection as healthy. Do not
overlap probes with destructive cleanup or make a probe block the scheduler.
Each host reports its own access; the master exposes the result and observation
time. The current state is **online**, **offline** or **unknown/stale**. A stale
last success is never online; an unreachable master makes Controller status
unknown, not proof of destination failure. Keep operation-start verification.

Core records a bounded offline/recovered transition with repository ID, kind,
time and safe error, deduplicated across repeated failed probes; a fresh success
clears the current offline state and records recovery. Controller shows the
Core-reported alert in the repository list and relevant backup/policy screens,
including failed/missed scheduled runs. Do not create a second Controller alert
source or make notification depend on its uptime. Show which hosts can actually
write and restore. Jobs keep the selected repository ID; Core refuses execution
when that destination is unavailable and retains an accurate failed/missed run.
The policy form warns on offline/unknown status but does not silently choose a
different destination.

## Disposable local lab and evidence

Use the existing `cluster-ha-v1` disposable fixture (`python3
tests/integration/cluster_ha_lab.py --prepare`) with three Core guests and a
separate storage guest on its private test network. Reuse the NFS/Samba setup
from `tests/integration/bug_ledger_lab.py`; add isolated services only to that
storage guest or another explicitly disposable guest. No plan authorizes a
live host, production share or destructive test. Keep generated credentials,
keys, certificates and raw outputs in ignored private lab paths; sanitize
public evidence. Pin and record service images/packages, Core/Controller binary
digests, environment and commands. Package/dependency changes require the
licensing and corresponding-source review in `docs/LICENSING.md`.

| Repository | Local service setup and first protocol check |
|---|---|
| NFS | Existing private NFSv4 export; verify exact mount/export and baseline product backup/restore. |
| SMB3 | Existing Samba on storage guest; dedicated share/account and Core private credential receipt; mount from two Core guests and verify marker, share identity and read/write. |
| S3 | Selected disposable S3-compatible service on persistent local guest storage, private bucket, scoped key and test CA/TLS. Signed PUT/GET/LIST, conditional-write, multipart and crash-durability checks precede Core registration. Do not back the object server with the NFS export. |
| SFTP | OpenSSH `internal-sftp`, dedicated confined account and key, pinned host key, separate root. Batch upload/download/list/rename/sync capability checks precede Core registration. |

After those **service smoke checks**, run the same **product test** separately
for NFS, SMB3, S3 and SFTP through authenticated Core APIs
and the Controller UI. For each: register and test; create a policy naming that
repository; run a stopped multi-disk full backup and an online supported-mode
backup; inspect the complete remote artifacts and run/catalog records; make
original disks unavailable; restore as a new stopped VM on another compatible
host/storage; boot in isolation and read back planted disk/application markers.
Run test restore and its verification/cleanup, explicit replacement on a
disposable VM, retention and protection, and catalog rebuild without Controller.
Verify scheduled dispatch still works while Controller is stopped, then install
a fresh Controller and confirm stable repo/policy/run/point IDs and histories.
Fence the old master, promote a member with verified authority, and repeat a
backup and restore. A service-level put/get or HTTP 200 is insufficient.

For **each kind**, inject wrong credentials, wrong server/bucket/share/root or
host key/certificate, full or read-only target, service loss during artifact
upload and publication, lost API reply, Core restart, stale health, and a
corrupt/missing artifact. Repeat the exact operation ID after ambiguity;
confirm prior good points remain, incomplete data is never restorable, catalog
rebuild refuses corruption, no duplicate writable VM appears, and prune cannot
race a pinned restore. Test concurrent writer/owner conflicts and member access.
Record recovery, expected
failures and supported-server limits per backend in `docs/qa/`; label service
smoke, fault injection and native product results separately. Re-run the
current Phase 6 interrupted-restore and authority-transfer gates on the changed
candidate before acceptance; older NFS evidence cannot certify it.

## Delivery and acceptance

This is **one destination expansion with one acceptance gate**, not a promise
that a repository kind is done when its form appears. Work may proceed in
parallel on typed contracts/secret custody, filesystem safety, remote transfer
backends, health projection and the disposable lab. Integrate each kind through
Core state → Core API → backup/restore/recovery → Controller UI/guide, then
run the common matrix above. Keep new kinds unavailable in the UI/API until
their Core capability and product lab gate pass. Update `docs/guide/` workflows
and `docs/API.md` in the same implementation slices; perform real browser review
of loading, empty, offline, stale, permission, credential and uncertain-result
states. Add focused contract and regression checks to the forge suites, inspect
actual CI results after an owner-authorized push, and obtain independent review.
Close this expansion only when all four requested kinds
pass their local backup **and** restore gates with recorded evidence, and the
current recovery candidate's safety blockers are resolved. Report any server
profile limitation and never infer cloud-provider or physical hardware coverage
from this local lab.

**This implementation candidate:** API impact: **new contract** — typed
repository sources, credential handoff, Core health and authority capabilities
change Controller/Core and client-facing backup repository contracts, and
Core peer member access adds an authenticated endpoint. The existing Core-local
catalog import/rebuild contract now returns `202/pending` before a long scan
settles; authenticated operation GET and exact retry use the same durable
receipt. Guide
impact: **updated** for current NFS health and policy warnings; new destination
instructions wait for the native product gate. State owner: **Core**, through
the durable records and authenticated inventory/adoption path above. The
[change assessment](../changes/2026-09-28-backup-repository-destinations.json)
and [QA report](../qa/2026-09-28-backup-repository-destinations.md) record the
candidate's actual checks and open gates.
