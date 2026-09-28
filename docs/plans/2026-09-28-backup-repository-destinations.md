# Backup repository destinations and health

Date: 2026-09-28. Status: implementation plan; no new destination or automatic
repository health check is implemented by this document. Owner: project owner.

Parent: [recovery delivery](2026-09-25-recovery-snapshots-backups-restores.md).
This expands the NFS-only destination scope after the original Phase 6 disposable
close. It does not retroactively change that close record or close the [current
recovery candidate's open safety gate](2026-09-25-recovery-snapshots-backups-restores.md#recovery-delivery-snapshots-backups-and-restores).

## Product decision and operator model

An operator registers any supported destination as a **backup repository**. Core
checks that repository on a schedule, reports its current health and raises an
operator-visible alert when it goes offline. A backup job or policy names one
repository ID. Core uses that repository's destination for backup, restore,
catalog rebuild and retention; Controller does not copy data or run the job.
Repository choice remains explicit and visible in the job and restore review.

Support **NFS, SMB3, S3-compatible object storage, SFTP and FTP** in this
expansion. FTP offers explicit FTPS (TLS on control and data) and plaintext FTP.
The project owner explicitly requested plaintext FTP on 2026-09-28. Default to
FTPS; selecting plaintext requires an explicit insecure-mode choice and a clear
warning that credentials and backup data are exposed in transit. Do not silently
downgrade FTPS. A successful plaintext FTP test proves operability, not network
confidentiality or server authenticity. Apply the same backup integrity and
restore gates to both FTP modes.

All five kinds belong to one repository list, health view, job selector and
restore flow. Do not build a separate job scheduler, provider catalogue, cloud
account model or Controller-owned repository state. An external repository is a
separate failure domain from a VM's source disks; do not present a destination
on the same underlying storage as an independent backup.

## Current implementation and correction boundary

`internal/protocol/backup.go` accepts only `kind=nfs` and an NFS source/identity.
`internal/vmhost/managed_backup.go` mounts and verifies NFS and publishes a
recovery point through file sync and directory rename. Backup execution,
restore, catalog rebuild and pruning use filesystem paths in
`internal/agent/backup.go`, `internal/agent/backup_catalog_rebuild.go` and
`internal/agent/pool_backup.go`. Controller registration and
`web/src/backup-repositories.tsx` are NFS-specific. Core's scheduled jobs already
carry `repository_id`; `internal/agent/backup_schedule.go` needs no second
selection mechanism. The current access check is manual. The UI can display an
old successful check as current, so freshness must be corrected with this work.

The existing SMB VM-storage mount, identity and credential code in
`internal/vmhost/storage_mounts.go` and `internal/agent/pool_storage.go` is reusable.
It does not prove backup publication on CIFS. The current directory-sync rule
also differs from the SMB storage marker's handling of unsupported directory
sync. S3, SFTP and FTP are remote object/file transfers; they must not be made
to look like a POSIX mount merely to reuse NFS code.

## Core state and changed contracts

**Implementation API impact: changed contract.** Keep the existing Controller
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

Never put passwords, access keys, private SSH keys or FTP credentials in a
Controller command, `managed_resources` payload, inventory, authority page,
audit entry or error. Controller currently persists repository request bodies;
reuse/adapt its transient credential handoff and Core-private receipt pattern
before accepting secret-bearing kinds. Core securely delivers scoped secrets to
eligible executing members and promotion candidates so backups and restores
work without Controller or the old master. Define rotation, revocation and
failure after promotion; no stored secret may be recovered from a Controller
database. Pin SFTP host keys and validate FTPS certificates. S3 uses TLS with
verified endpoint identity. Plaintext FTP is the operator's explicit exception.

## Destination behavior and publication safety

Use the existing complete recovery-point manifest, immutable point identity,
artifact hashes, catalog digest, durable operation ID, restore pins and
retention rules for every kind. Put the smallest kind-specific storage actions
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
| FTP/FTPS | Use a dedicated account and repository root, passive transfer and explicit FTPS when selected. Plaintext requires the opt-in above. FTP rename/exclusive-create/durability are server dependent: define and test a supported server profile and refuse a server lacking the ownership/publication guarantees. A pre-provisioned exclusive root/owner marker may be required where standard FTP cannot safely claim one. Never infer completion from a successful transfer reply alone. |

For S3, SFTP and FTP, an immutable, validated manifest is the publication
boundary. Catalog rebuild lists only final manifests and verifies referenced
artifacts before adoption. Prune names one eligible recovery point and rechecks
pins and authority; it never deletes an unchecked prefix or directory tree.
Document backend limits such as unavailable capacity reporting honestly.
Archive classes requiring delayed retrieval are outside this synchronous
restore contract. Protocol service success without a restored VM is not
acceptance. [S3 consistency](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html),
[S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html),
[SFTP extensions](https://www.openssh.org/specs.html) and
[FTP TLS](https://www.rfc-editor.org/info/rfc4217/) are design references;
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
| S3 | Pinned S3-compatible service such as a disposable single-node MinIO container on persistent local guest storage, private bucket, scoped key and test CA/TLS. Signed PUT/GET/LIST and conditional-write checks precede Core registration. Do not back the object server with the NFS export. |
| SFTP | OpenSSH `internal-sftp`, dedicated confined account and key, pinned host key, separate root. Batch upload/download/list/rename/sync capability checks precede Core registration. |
| FTP/FTPS | Isolated `vsftpd` with dedicated account/root, passive port range and test certificate. Exercise explicit FTPS control/data TLS and separate opt-in plaintext FTP in the private network; check put/get/list/rename and interrupted transfer. |

After those **service smoke checks**, run the same **product test** separately
for NFS, SMB3, S3, SFTP, FTPS and plaintext FTP through authenticated Core APIs
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
For FTP additionally prove both FTPS refusal of TLS downgrade and explicit
plaintext behavior on the isolated lab network. Record recovery, expected
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
Close this expansion only when all requested kinds, including both FTP modes,
pass their local backup **and** restore gates with recorded evidence, and the
current recovery candidate's safety blockers are resolved. Report any server
profile limitation and never infer cloud-provider or physical hardware coverage
from this local lab.

**This planning change:** API impact: **none** — it changes no runtime endpoint
or payload; implementation requires the changed contracts above. Guide impact:
**none** — current operator steps remain NFS-only until new kinds actually work.
State owner: **none** for this document; planned product state is Core-owned as
specified above.
