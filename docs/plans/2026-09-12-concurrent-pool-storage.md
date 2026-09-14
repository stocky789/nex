# Concurrent pool storage

Date: 2026-09-12
Status: Authorized implementation in progress; native acceptance is open. Current
status and the single open-gate list: [production-readiness handoff](2026-09-14-production-readiness-handoff.md).
Parent: [platform plan](2026-09-08-nex-platform.md), Phase 4.
Evidence: [concurrent storage QA](../qa/2026-09-12-concurrent-pool-storage.md) and
[reliability follow-up](../qa/2026-09-14-cluster-storage-reliability.md).

## Decision and scope

Project owner requested simultaneous multi-host access to NFS, SMB and iSCSI,
including three to five Nex Core hosts in one pool with two VMs on each host.
The requested “bfs” is interpreted as NFS. This brings forward the shared-block
storage safety prerequisites, not automatic VM HA, migration or failback.
Ordinary ext4/XFS iSCSI remains exclusive to one host; no existing registration
is silently converted. Existing external storage is consumed, never formatted
by registration. A disposable test fixture may format only its newly created LUN.

Each VM has its own virtual disks. Concurrent datastore access does not authorize
two QEMU processes to write the same VM disk or make a guest filesystem clustered.
NFS and SMB retain their native protocol locking; neither is mounted as GFS2.

## Safety design

### Production-readiness follow-up — 2026-09-13

Project owner requested closure of the remaining gates before production use.
Acceptance requires executed evidence for an explicit supported deployment profile;
it is not permission to relabel development results or operate on live infrastructure.
Continue using selected disposable resources. The earlier nested-UEFI exclusion
remains in force and cannot become a UEFI or Secure Boot support claim. Do not
declare production support until the applicable implementation, native evidence,
independent review and external prerequisites are satisfied. The gate list is kept
in the [handoff](2026-09-14-production-readiness-handoff.md#open-work).

Two independent protections are required:

| Protection | NFS and SMB | Clustered iSCSI |
|---|---|---|
| Shared filesystem | Validated native client/server locking and durability | GFS2 with DLM, Corosync quorum and Pacemaker fencing |
| VM execution ownership | Durable pool-master assignment and guarded native start | Same ownership gate |
| Access admission | Exact source identity, QEMU permissions and current member checks | Same checks plus native cluster, fencing and exact GFS2 identity |

The new explicit iSCSI mode is `clustered_gfs2`; the existing mode defaults to
`exclusive`. Core verifies matching IQN, LUN, WWID, filesystem UUID, `lock_dlm`
lock table and sufficient journals on every eligible host. One native Corosync
configuration belongs to the pool: this is not another product hierarchy tier.
Each GFS2 repository has its own filesystem identity and lock-table resource.

Pacemaker alone controls GFS2 mounts, ordered after its cloned DLM resource.
Core establishes the required iSCSI sessions before cluster filesystem activation.
Native settings require fencing enabled, startup fencing and freeze on quorum loss.
Native fencing must explicitly power off, not use the default reboot action;
otherwise a target can return after Core's exclusion proof but before the requested
recovery step. Both configuration and readiness checks enforce this property.
DLM is a separate client of Pacemaker's fencing API and explicitly requests reboot;
the global default alone is insufficient. Every supported Core-owned device must
also map `pcmk_reboot_action=off`, retaining DLM fencing and returning the real
off action's result to its caller. This uses the native device parameter described
in [Pacemaker's maintainer guidance](https://lists.clusterlabs.org/pipermail/users/2024-December/036434.html),
not a successful response fabricated by Core. An exact recognized predecessor
with an absent mapping may receive a version-checked narrow CIB patch; conflicting,
duplicate or conditional mappings remain blocked (BUG-0134). API impact: existing
setup/status and admission contracts, with stricter native configuration checks.
Accepted exclusion proofs invalidate the target's earlier healthy report
immediately; a subsequent report must acknowledge the exact current proof before
it can restore healthy status (BUG-0137). This reuses existing `Status.Fences` and the
master's durable cluster operation, without a Controller cache or new schema.
Configuration changes cannot replace an unrelated system cluster or remount an
ordinary filesystem as a shortcut. Interrupted configuration remains visibly
incomplete and cannot grant writable readiness.
GFS2 resources must be created stopped and become startable only after their exact
DLM ordering constraint is observed. Reconciliation must not disable an existing
mounted filesystem or replace unrelated CIB configuration to repair a mismatch.
Members prepare their own sessions and Corosync services; only the pool master
reconciles shared Pacemaker resources and constraints. Health and VM admission
must use the local Core's actual credential state directory, not an assumed path.

Initial activation and degraded runtime are separate gates. The master records
completed initial activation only after every configured host supplies fresh,
matching healthy evidence. A durable internal activation receipt in the existing
`pool-storage-cluster.json` operation binds that evidence to the exact request,
repository, configuration and membership generation; it is not a Controller
setting. A fresh Controller obtains the operation's observed state from the
master and does not recreate this evidence. Legacy records without a receipt
must requalify all selected hosts once; historical status is not sufficient.
Exact retries after a runtime fault retain this receipt when reconstructing the
same durable operation. Replaying native setup must not silently return a formerly
qualified cluster to initial setup (BUG-0135); native restart/failure replay remains
an acceptance gate.
After activation, runtime readiness accepts only a proven ordinary-voter majority
of the unchanged configured membership, with matching native DLM/GFS2 membership
and fencing. Every VM admission still performs live native validation and retains
master-owned execution ownership. Degraded operation must be visible as degraded,
not confused with incomplete initial setup or automatic failover. BUG-0130's
native two-of-three reproduction and its corrected admission walkthrough are
required acceptance gates. API impact: existing admission contracts plus an
additive observed degraded status; private activation evidence stays Core-owned.

Host boot must also establish the exact local iSCSI/GFS2 identity before Pacemaker
starts. A Core-owned readiness command consumes the existing durable applying or
healthy configuration and fencing proofs without contacting Controller or peers.
It must work while the regular agent owns its writer lock, reject stale/conflicting
state before login, and never mount or start guests itself. Native service ordering
must fail closed on readiness failure; automatic iSCSI login alone is insufficient.
API impact: existing storage contract; the internal Core readiness CLI and generated
local service dependency require delivery/reference checks, not a new Controller job.

Fencing uses narrowly validated native device profiles, bound to exact pool hosts.
An unreachable host, expired timer, successful status query or operator checkbox
is not proof of fencing. Activation requires recorded actual exclusion tests;
failed or ambiguous fencing blocks admission. The fence path must remain usable
when its target or cluster network fails. Native fencing requirements follow
[Pacemaker's fencing guidance](https://clusterlabs.org/projects/pacemaker/doc/2.1/Pacemaker_Explained/html/fencing.html).
Disposable virtualization fencing proves only that laboratory profile, not a
physical device or production installation.

### Storage-loss protection correction

BUG-0138: one host's iSCSI path loss stalled all GFS2 monitors and scheduled all
three hosts for fencing. Do not weaken GFS2 fencing or lengthen its deadline to
conceal this result.

The correction adds a read-only transport monitor under Pacemaker, before each
affected GFS2 resource. The existing native iSCSI resource agent is unsuitable
for this role: its lifecycle can log out an entire session shared by multiple
LUNs, and its monitor accepts unknown connection state. These behaviors are
visible in the [upstream native agent](https://raw.githubusercontent.com/ClusterLabs/resource-agents/v4.16.0/heartbeat/iscsi).
Core therefore provides a small monitor-only OCF adapter using its existing
native command execution and endpoint parsing. It never discovers, logs in/out,
changes authentication, mounts or formats anything. Start and monitor perform
the same strict session check; stop has no transport side effect.

The Core-owned native CIB records canonical portal, IQN and their deterministic
session identifier; no credentials or new Controller state are introduced.
Repositories using different LUNs on the same session reuse one transport clone.
Each GFS2 clone requires both its transport clone and the existing DLM clone;
adding a transport must not restart the pool-wide DLM resource. Runtime checks
read the exact native session directly, not mutable pool health, membership
reports or fencing receipts, so a stale management report cannot itself fence
a healthy storage connection. Missing, ambiguous, unknown or recovering native
session state fails the check.

Only the current master may upgrade an exact recognized legacy CIB after every
member proves monitor support. Foreign resources, partial conflicting state and
uncertain writes are refused or reconciled before retry. Establish the transport
clone as active before adding an ordering constraint to an already mounted GFS2
resource; preserve existing mounts and the completed activation receipt.
API impact: changed contract through the additive observed
`StorageClusterStatus.session_monitor_ready` attestation, carried by existing
authenticated member reports and setup/status responses; old Cores omit it and
therefore cannot authorize monitor enablement. A fresh true report proves that
the installed fixed adapter and CLI successfully checked the exact live session;
it is neither a static package-presence flag nor proof of aggregate GFS2 health.
An internal Core CLI/OCF delivery interface also needs command and prerequisite
compatibility documentation. Durable state remains the master's existing storage
configuration and Core-owned native CIB; fresh Controllers obtain the existing
storage/setup inventory from Core.

BUG-0141 requires explicit mixed-version report negotiation: advertise
`storage-cluster-session-monitor-v1` through the existing capability list and
acknowledge it through the existing cluster-support capability value only when
offered. Without that acknowledgement, Core must project Controller reports into
the older shape, excluding readiness and monitor-stage extensions while retaining
their native Core records. Reset the acknowledgement with report preflight on
restart/reconnect/re-enrollment. API impact: changed contract using existing
envelope fields; no Controller-owned pool state or relaxed JSON decoding. Verify
new/new, new-Core/old-Controller and old-Core/new-Controller strict decoding and
restart recovery before accepting the report extension.

Live-write interruption (BUG-0143, BUG-0145, BUG-0146): do not substitute a
monitor-timeout hypothesis or reduce the workload to obtain a pass; preserve
failed-run evidence. The bounded disposable-IBLOCK dual-target interruption
scenario passed; earlier FILEIO failures, production HA, migration, physical SAN
and soak are not closed by it.

BUG-0144 uses the existing authenticated VM power contract to allow only exact
locally journal-bound shutdown/force-stop without live storage/master admission.
Start, reboot and ownership changes remain fail-closed. Stop completion must come
from actual observations, not a timeout or an admission callback. API impact:
existing contract with corrected stop-only handling; guide recovery instructions
are updated. Durable state remains Core's existing VM journal and master record.
Frozen I/O or lost quorum may still prevent completion and remain open.

BUG-0146 makes periodic member reconciliation quiet only for an exact owned
Corosync configuration with every required unit persistently enabled and active;
disabled or inactive units keep the recovery path and foreign content is refused.
BUG-0147 keeps unknown master storage status distinct from confirmed-empty
configuration in the browser; initial setup is offered only from confirmed status.
Both use existing contracts with no schema or durable-state change.

The proposed five-second interval and ten-second monitor deadline require native
latency and fault validation; they are not a guaranteed fencing bound. Acceptance
requires the exact isolated-path fault to exclude only its target before any
survivor GFS2 monitor fails, with independent power state, native failure history,
survivor I/O/hash checks and explicit target return. Also exercise a healthy-load
window and short interruption. Multipath, physical watchdog profiles and automatic
HA are not inferred from this single-path disposable test.

The master durably binds a VM identity to its execution host before start.
Native libvirt hooks and managed autostart use that admission, including starts
outside the Controller. A timeout never releases ownership. Confirmed stop and
reconciled native state are required before ownership can change. Controller loss
does not stop workloads; loss of master authority blocks new admissions without
claiming that running VMs have stopped. Automatic owner reassignment after host
loss remains outside this slice.

An uncertain remote creation may settle only through a serialized Core observation
of its exact stopped definition, current storage and existing owner. This preserves
the operation identity and cannot allocate, start or delete anything. Autostart claims
are local, durable and bound to the actual OS boot, so agent restart or uncertainty
recovery does not unexpectedly start a manually stopped guest. These recovery gates
must pass before native shared-storage acceptance closes.

Shared VM definition changes must reserve the exact next revision on the master
before native mutation. The existing `pool-vms.json` record retains the prior
definition and a pending update bound to its operation; it commits only against
matching authenticated successful journal evidence. A definitely unapplied failed
edit may release only its own reservation using matching failed-validation journal
evidence. Missing evidence, timeouts and uncertain native outcomes do not release
it. One bounded `last_aborted_update` receipt on the same master record permits
an exact lost-acknowledgement retry; the next reservation replaces that receipt.
Reconciliation uses the same operation and existing journals, including an
update that removes its last shared disk; it does not add a second member policy
store. Reboot and media actions retain master owner admission. Core reports must
not turn a local update result into confirmed success while the master transition
is unresolved. API impact: changed internal peer update transitions and explicit
durable acknowledgements; existing operator update/power/report APIs remain the
entry points.

Retained successful VM and storage-registration journals establish whether a VM
has ever used pool-shared disks, even after the last shared disk or registration
is removed. Such VMs retain master admission. A first local-to-shared update may
seed an absent master record only from the ready authenticated owner's exact
current-generation prior inventory and definite native state; caller-supplied
previous/next definitions are comparisons, never adoption authority. Only the
fresh reporting owner may reconcile execution state, preserving in-flight starts
and epochs; another member's report cannot replay stale cached stopped state
(BUG-0132, BUG-0133). Native disk-placement conversion remains open.

## State and API boundaries

API impact: **changed/new contracts**. Typed iSCSI registration and observed
identity gain the explicit clustered profile. Core-owned setup/status operations,
authenticated peer configuration reconciliation and VM execution admission are
new contracts. Existing registration, credential handoff, inventory and adoption
must carry the same identities without a second Controller model.

Guide impact: **updated** when the implemented setup, errors and recovery steps
are available for walkthrough; acceptance requires matching guide and API updates.

State owner: **core**. The pool master's existing managed storage journal remains
the repository authority. A separate Core-owned native-cluster configuration and
operation record owns the pool's requested setup; private credentials and Corosync
keys are stored separately with root-only permissions, not in commands, inventory,
logs or Controller SQL. The master also owns the VM execution assignment record.
Exact implemented filenames and inventory/adoption paths must be recorded in the
change assessment before acceptance. Member configuration is a generation-bound
replica, not an independent source of desired pool state. The planned
[member authority replica](2026-09-08-nex-platform.md#pool-authority-transfer-and-master-succession)
is also read-only and becomes authority only through that promotion design.

Controller authenticates and authorizes the operator and relays setup/status;
the Core continues accepted work after Controller disconnect. Retries use a
request identity and expected generation and reconcile ambiguous results. Peers
authenticate with pool mTLS, validate member identity and scope, and receive only
the configuration and secrets required for their role. Older hosts cannot accept
the new mode without the required contract and native support.

## Operator workflow

Keep existing server/share/export discovery. Select a pool and eligible hosts,
choose exclusive or clustered iSCSI explicitly, discover the target, and validate
the pre-existing filesystem identity. Show per-host prerequisites, fencing target
mapping and the consequence of each actual fencing test before activation.
Never show a successful configuration submission as a healthy shared datastore.

Setup progress, failed checks and the safe next action come from Core. Preserve
failed or uncertain status across refresh, retry and Controller replacement.
Do not ask operators to infer readiness from raw mount paths or command output.

Generic repository update (including rename) and detach are not cluster-coordinated
operations in this slice. Core must refuse them for GFS2 before native mutation;
otherwise a rename can diverge configuration generations, or an exclusive-iSCSI
detach can log out a Pacemaker-managed LUN. The UI explains this restriction and
offers read-only recheck/status instead. A future coordinated lifecycle must update
the master's durable cluster configuration and every affected member safely before
these controls are enabled. API impact: existing contract, explicit clustered-profile
refusals; no new teardown endpoint or manual single-host workaround is implied.

## Acceptance gates

Use an isolated disposable lab; preserve existing workloads and local networks.
Execute each backend scenario separately, not two VMs per backend mixed together:

1. Enroll three Core hosts into one pool; verify nested KVM and memory headroom.
2. Attach one shared iSCSI LUN to all three hosts using the new GFS2 profile.
3. Run two VMs per host, all six storing distinct disks on that same datastore.
4. Perform simultaneous writes, flushes and checksum verification inside all six
   guests; stop/restart and verify persisted contents.
5. Repeat the complete six-VM scenario on one NFS export, then one SMB share.
6. Reject duplicate VM execution, stale identities, unsupported filesystems,
   ineligible hosts, missing quorum and unverified or failed fencing.
7. Exercise real host exclusion during writes, lost cluster connectivity,
   fencing-path failure, returning stale nodes, DLM/Pacemaker restart and
   storage/session interruption. Verify exclusion and persisted checksums.
8. Exercise Controller outage/replacement and master outage. Confirm preserved
   records and honest blocked admissions, not implied automatic HA.
9. Walk through the actual GUI with loading, error, stale and recovery states;
   inspect screenshots and retain useful captures in the project evidence folder.

Unit tests or simulated fence responses cannot establish native storage correctness.
Physical fencing, five-host scale and unsupported storage appliances remain
explicitly unverified unless separately exercised.

BUG-0127 adds: use the installed resource agent's supported monitor budget without
weakening fencing, verify any existing-cluster operation update against exact
Core-owned intent, and test bounded storage interruption plus recovery. A timeout
change is not proof that the initiating transport stall is resolved.

BUG-0128 adds: compatible native DLM/Pacemaker packages, a non-fencing prerequisite
check for the helper's dynamically loaded library, and native recovery with a pool
member still powered off. Retain the matching Debian package sources/notices
separately from Nex; never mask an ABI mismatch with a library-name symlink or
bypass DLM exclusion.

## Reliability follow-up — 2026-09-14

Project owner requested Terra 5.6 workers to fix and exercise the handoff's
clustered-storage issues in the existing disposable lab. This does not expand
automatic HA, migration or release scope. Fixes must find the slow or duplicated
operation: do not increase deadlines, hide health failures, cache a healthy result,
or weaken identity, locking or fencing.

| Bug | Correction | API impact / state owner |
|---|---|---|
| BUG-0156 | Periodic repository validation no longer repeats cluster setup; the independent cluster worker performs it | existing / none |
| BUG-0158 | Member access observations are held in the master Core process, not flushed durably; restart shows unknown access until fresh authenticated reports arrive | existing / core (guide updated) |
| BUG-0159 | Remove the duplicated clustered QEMU access probe; NFS/SMB keep their wrapper probe | existing / none |
| BUG-0160, BUG-0161, BUG-0162 | Test-harness corrections only; no production gate inferred | none or existing / none |
| BUG-0163 | Peer probe proofs use challenge-derived nonzero byte ranges; absent mode keeps the legacy byte-zero proof in both directions | changed (Core-peer lock POST/DELETE mode) / none |
| BUG-0164 | Remove repeated iSCSI connection preparation after cluster start; full post-start validation retained | existing / none |
| BUG-0165 | Storage-only member and master reconciliation reads skip unrelated VM inventory | existing / none |
| BUG-0166, BUG-0169 | Share only simultaneous in-flight identical GFS2 metadata reads and file proofs; each caller keeps its context and validation | existing / none |
| BUG-0167, BUG-0168 | Per-Core held probe leaves for GFS2 and eligible NFS, with root-proof fallback | existing / none (leaves are regenerable) |
| BUG-0170 | Skip rewriting an already exact private SMB mount credential | existing / none |
| BUG-0171 | Skip Chmod only for a held exact-0600 file with the expected QEMU UID | existing / none |
| BUG-0172 | Extend held-leaf in-flight proof sharing to NFS | existing / none |
| BUG-0173 | Concurrent SMB probe leases share one open description, so closing one does not release another's range | existing / none |

Native results per bug are in the [reliability follow-up](../qa/2026-09-14-cluster-storage-reliability.md);
current status is in the [handoff](2026-09-14-production-readiness-handoff.md).
