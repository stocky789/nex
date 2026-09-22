# Automatic host fencing

Status: slices 1-4 implemented. Slice 4c makes a pool own its Corosync/Pacemaker cluster, so a
pool of three or more hosts **without clustered storage** arms by itself; pools with clustered
storage stay exactly as they were and report why. Everything is proved in a nested lab and by
runnable checks only: **no physical watchdog has been armed and no physical host has been reset**.
Decision: project owner, 2026-09-22.

## Problem

Fencing proves that a lost host can no longer run or write anything. It gates manual promotion
after master loss today, and clustered storage and per-pool HA later. The shipped workflow is
**Record fence device…**: per host, an operator pastes an SSH private key and known-hosts line for
a libvirt host that runs this host as a VM. Only that `virsh` profile can be recorded, because
Core cannot verify the identity of a BMC target, so **a physical pool cannot be fenced at all**.
It also depends on the operator knowing to record a device before the master is lost.

Nex ships the host distribution, so Core can find and configure fencing itself instead of asking.

## What established platforms do

Established hypervisor platforms do not ask operators for fence devices. They combine pool quorum
with **watchdog self-fencing**: a host that loses quorum stops feeding its watchdog and resets,
and the quorate side waits out a known bound before recovering its workloads. One variant adds a
heartbeat disk on shared storage, which also serves two-host pools. BMCs are used for remote
power-on, not fencing. Operators see a per-pool HA switch, quorum and fencing status, and a
per-VM recovery mode — no credentials or device forms.

Nex adopts that convention and adds BMC fencing where hardware has a BMC, which the distribution
can configure without operator input.

## Decision

Two fencing methods, both configured by Core with no operator-entered secrets:

1. **BMC power fencing**, discovered and provisioned in-band by each host's own Core.
2. **Watchdog fencing** through the native Pacemaker `sbd` watchdog mode, for pools of three or
   more hosts with a pool quorum.

A host is **fence-ready** when at least one method is usable for it. The manual dialog is removed
from the normal path; a manual BMC form remains only where in-band provisioning is impossible.

### Rule change

This replaces two existing rules, for the watchdog method only:

- *"exclusion comes from verified external power-off fencing, not from hosts fencing themselves"*
  ([platform plan](2026-09-08-nex-platform.md#authority-and-outage-rules)); and
- *"A timeout or heartbeat loss is never proof"*
  ([transfer steps](2026-09-08-nex-platform.md#pool-authority-transfer-and-master-succession)).

A timeout alone still proves nothing. A **watchdog exclusion proof** is valid only when all of the
following hold, and Core records each part in the proof:

- the target's **hardware** watchdog was reported armed by the target itself, with its timeout,
  and that report is in the master's durable record at the current fencing generation;
- the proving host is in a quorate partition, by the pool's Corosync quorum, that excludes the
  target; and
- that quorate-without-target state has held continuously, on a monotonic clock, for at least
  the configured watchdog fencing timeout, which is at least twice the target's watchdog timeout.

Everything else stays: BMC proofs keep the existing identity-then-off-then-observed-off
discipline, proofs stay bound to operation, host and generation, OD-5 and OD-6 are unchanged,
and there is still no force or "declare host lost".

## Design

### BMC power fencing

- **Identity.** Core records the host's SMBIOS system UUID in the master's membership record at
  join, while the host is alive. A BMC proof requires the BMC to report the same UUID over the
  network before power-off: IPMI *Get System GUID* (accept both documented byte orders) or the
  Redfish `ComputerSystem.UUID`. A mismatch refuses the fence. This is the missing check that
  keeps `PoolFenceUsable` refusing IPMI and Redfish today.
- **Discovery.** Core reads its own BMC's LAN address through the local IPMI interface
  (`/dev/ipmi0`). The Redfish host interface (SMBIOS type 42) is a later addition.
- **Provisioning.** Core creates a dedicated fence account on its own BMC in-band, with power
  control privilege only and a random password, and records it as the host's fence profile. The
  credential lives in the existing Core fence-credential store and replicates sealed to members by
  the existing mechanism. An existing account under the Nex name is replaced only if Core created
  it (recorded marker); otherwise Core reports a conflict and changes nothing.
- **Continuous checks.** Each ready member periodically reads every other host's BMC power status
  and system GUID, without powering anything off, and reports the result to the master. The
  master reports readiness per target and per fencing host (for example, "BMC not reachable from
  host-b"). A stale check is shown as stale, never as ready.
- **Manual fallback.** Where no local IPMI interface exists or the BMC refuses in-band user
  management, the host row offers **Set BMC access…**: address, username, password. The same
  UUID identity check applies. The `virsh` profile stays available only for virtual labs.
- **Packaging.** `ipmitool` is in `installer/packages.txt` and in the `nex-core-local` package's
  `Depends`, and `/etc/modules-load.d/nex-ipmi.conf` loads `ipmi_si` and `ipmi_devintf` at boot so
  `/dev/ipmi0` exists. The licensing review is in [LICENSING.md](../LICENSING.md), including what
  is still outstanding. `fence-agents-ipmilan` and `fence-agents-redfish` already ship.

### Watchdog fencing

- **Detection.** Core detects a hardware watchdog (chipset TCO, AMD SP5100 TCO, IPMI watchdog)
  and reports it with its timeout. The kernel `softdog` is reported but does not count: a hung
  kernel cannot reset itself.
- **Engine.** Pool-wide Corosync membership and quorum plus Pacemaker and `sbd` in diskless
  watchdog-only mode. Each host has exactly one Corosync configuration, and the clustered-storage
  repository already owns it, so the pool cluster **is** that cluster: a pool has one
  Corosync/Pacemaker cluster, and clustered storage runs on it. Slice 4b settled the properties
  in a nested lab; see the slice 4b measurements in the [QA evidence](../qa/2026-09-22-automatic-host-fencing.md).
- **Consequence the operator must see.** An armed host that loses quorum resets itself and its
  running VMs stop. A switch failure that splits a pool therefore reboots the minority side.
  Watchdogs are **armed by default** in pools of three or more hosts whose hosts have hardware
  watchdogs; the **Host fencing** pool setting turns them off. The consequence and the
  prerequisites are shown whether or not the operator ever opens that setting.
  Decision: project owner, 2026-09-22.
- **Two-host pools** have no majority, so a watchdog-majority proof is never valid at two hosts.
  This plan fences them by BMC. Methods for a two-host pool with no usable BMC — a storage lease,
  then an automatic witness — are in the follow-on
  [two-host pool fencing plan](2026-09-22-two-host-pool-fencing.md); BMC still wins wherever it is
  usable.

### Operator experience

- **Hosts tab:** a **Fencing** column replaces the row action, for example
  `BMC · checked from 2 hosts`, `Watchdog · armed`, `Not protected — no BMC, watchdog is software only`.
  **Set BMC access…** appears only where Core could not provision in-band.
- **Pool summary:** "Fencing: ready for 3 of 3 hosts", linking to the reasons.
- **Pool setting:** **Host fencing** on/off, with Core's prerequisite checklist (hosts, quorum,
  hardware watchdogs, BMC readiness) and the partition-reboot consequence. It defaults to on where
  the prerequisites hold. BMC discovery and provisioning run regardless of this switch; arming
  watchdogs and opting into the restarted-master restriction follow it.
- Promotion shows which method will fence each blocking host.

## State ownership

Core-owned. The master's membership record holds each host's SMBIOS UUID, BMC profile, watchdog
capability and armed report, and the pool's fencing setting and generation. Credentials stay in
the Core fence-credential store and replicate sealed to members. Readiness checks are Core
observations reported through the master. A fresh Controller recovers all of it from master
inventory; Controller stores nothing.

## API impact

**Planned: changed contract.** Host reports gain SMBIOS UUID, BMC discovery and provisioning
result, watchdog capability and fencing readiness; the fence profile gains a Core-provisioned
BMC kind; the pool gains a fencing setting; promotion verdicts name the method per host; proofs
gain a `watchdog` kind. Controller relays the setting and the manual BMC form and displays Core's
reports. Each slice defines authorization, validation, errors, retries, outage behaviour and
version compatibility, and updates [API.md](../API.md). **API impact of this planning change:
none. Guide impact: none** — no behaviour changes yet.

## Slices

1. **BMC identity and manual form** — *implemented, 2026-09-22*. `PoolMember.system_uuid` in the
   master's membership record, reported by each host over the existing `POST /api/v1/pool/advertise`
   peer route at join and on every reconcile pass (so existing pools gain it without re-joining);
   the identity check in both BMC proof paths; `PoolFenceUsable` accepting `redfish` and `ipmilan`
   for a host that has reported a UUID and a profile that names an account; and **Set BMC access…**
   as the normal path, with the `virsh` device behind a link in the same dialog for a Core that
   runs as a lab VM. `StorageClusterFence` gained `username`.
   What this slice settled, after independent review:
   - **A BMC profile is pinned to a certificate.** A controller's certificate is self-signed with
     no usable name, so a chain proves nothing and skipping verification would send the password to
     whoever answers. Core captures the leaf SHA-256 when the profile is recorded, keeps it in
     `StorageClusterFence.tls_fingerprint` on the master, reports it so an operator can compare it
     with the controller's own page, and refuses every later connection presenting anything else
     with its own sentence rather than as a network fault. That pin also binds the identity read
     and the power-off to one endpoint, so a name that resolves elsewhere between them cannot
     separate them.
   - **Core performs the Redfish fence itself**, over that pinned connection: identity `GET`,
     `ComputerSystem.Reset` `ForceOff`, then a separate `PowerState` read. `fence_redfish` cannot
     be bound to the pin and `--ssl-insecure` would defeat the point, so it is not used for a pool
     fence; its rendering survives only for the clustered-storage Pacemaker STONITH renderer.
     Redirects are never followed — a controller that redirects the identity read would have Core
     read one machine and power off another. IPMI has no TLS, keeps `fence_ipmilan`, and
     authenticates its own RMCP+ session.
   - **Identity is compared exactly for Redfish**; only the IPMI GUID accepts either documented
     byte order, because only the IPMI and SMBIOS specifications disagree about it.
   - **A host may not defeat its own fencing.** It may not replace a reported `system_uuid` while a
     BMC profile is recorded against it — clearing the profile is the motherboard-replacement path
     — and may not claim an identity another member holds. Known firmware placeholders, including
     the widely shipped `03000200-…`, are not identities. All three surface as one sentence: *this
     host does not report a unique hardware identity*.
   - The BMC account name lives in the profile (not a secret, and shown); its password goes to the
     existing Core fence-credential store as a `fence_bmc` credential and is **released again when
     a profile is refused**. Recording a Redfish profile is a live Core operation, so an
     unreachable or wrong controller is refused while the master is still there to record a
     corrected one. Clustered-storage fences, which have no membership record to read a UUID from,
     still refuse BMC profiles exactly as before.

   Left open by this slice, and closed by slice 2 except where noted: `ipmitool` was not in the
   host image; the Redfish power-off had no settle window and an already-off target could not be
   fenced; a refused hardware identity did not name the member holding it. No physical controller
   has been contacted — every check is still against fakes.
   The pin is trust-on-first-use over the pool network at the moment of recording, closed by the
   operator comparing the reported fingerprint; slice 2's in-band provisioning should read the
   certificate locally instead, through the Redfish host interface. The Controller does not project
   `system_uuid`, so the **Set BMC access…** action is offered for every host and a host without a
   unique identity learns so from Core's refusal rather than from a disabled button. Whether a host
   runs as a VM is not reported, so the lab `virsh` device is reached by an operator statement in
   the dialog rather than by a host signal.
2. **In-band discovery and provisioning** — *implemented, 2026-09-22*. `ipmitool` and the kernel
   IPMI modules are packaged after the licensing review. On every reconcile pass a host reads its
   own controller over `/dev/ipmi0`, and the master records what it offers.
   What this slice settled:
   - **The automatic profile is `ipmilan`.** In-band IPMI creates an IPMI user, which on most
     controllers is also a Redfish user but not reliably, so the profile Core records is the one it
     knows it created. IPMI has no TLS and nothing to pin; the password never reaches argv
     (`IPMI_PASSWORD` for the identity read, `ipmitool`'s own prompt on standard input when the
     account is created). Cipher suite 0 is never selected and never accepted into a profile: Core
     takes 17 where the controller offers it and 3 otherwise, and records the choice in
     `StorageClusterFence.cipher` so every later fence uses the suite the identity was proved over.
   - **Operator privilege, not Administrator.** IPMI 2.0 requires Operator for the Chassis Control
     command, which is the whole of what a fence does; a User-privilege account can read the system
     GUID and the power state but cannot power a host off.
   - **The slot is recorded before anything is written.** Discovery and the slot decision are one
     half and the writing is the other, so a controller that refuses in the middle — busy, mid
     firmware update, out of resources — leaves an account this host still knows is its own and
     finishes on a later pass. The reference to the credential the account's password is written
     under is recorded in the same write as the slot, so an interrupted pass leaves nothing
     unnamed, and a controller that refuses the write is left alone for one check interval before
     the host tries again. The created marker is durable and Core-owned in two places,
     deliberately: the host's own `pool-peer.json` holds the channel, the slot and the profile it
     has offered but the master has not recorded; the master's membership holds
     `StorageClusterFence.provisioned_by_core`. Either is evidence the account is Nex's, so a
     reinstalled host that rejoins recognises its own account, and an account somebody else made
     under the Nex name is a reported conflict with **nothing changed**. A free slot is only ever an
     empty one, and a user table Core cannot parse is refused rather than guessed at.
   - **An operator-entered profile always wins.** `ExecutePoolFenceProfile` refuses to replace one
     with a Core-provisioned profile, and a host with one reports `manual_profile` and leaves its
     controller alone.
   - **A half-finished provisioning is never a fence device.** A master that was unreachable
     mid-way leaves `pending_master`, and the host keeps the account **and its password** in its own
     private fence-credential store and offers the same profile again — no second account, **no
     password written to the controller twice for the same account**, and nothing that looks usable.
   - **One operator action, one meaning.** Clearing a fence device Nex Core provisioned is what
     withdraws it: the master clears the profile through the one path that clears a profile — so a
     master is not left self-fence-exposed and restricted after a restart — and records
     `PoolMember.bmc_provisioning_disabled`. The host reads that from the membership it holds,
     removes the account from its own controller and reports it; a controller that refuses is
     reported honestly and retried. Letting the host configure its controller again is the only
     request in the other direction.
   - **A host reports observations and decides nothing.** A member cannot delete the fence device the
     pool recorded for it by describing itself, which is the rule slice 1 established for
     `system_uuid` applied to the same door; and an unchanged report is not written at all, so a
     settled pool does not re-sign and re-push its whole authority replica every ten seconds because
     a timestamp moved.
   - Slice 1's verified-open findings are closed here: the Redfish fence polls `PowerState` until
     the machine is off (2 s apart, 30 s monotonic bound) instead of reading once; a non-2xx
     `ComputerSystem.Reset` no longer fails the fence, because an already-off target is exactly the
     case a pool fence exists for and Core's own polled reads decide; a refused `system_uuid` now
     records `PoolMember.system_uuid_conflict_host_id`, so the operator can see which member claims
     the identity and eject it; one pinned transport and one password read serve a whole fence
     operation and its idle connections are closed; and the promote path checks mastership before it
     contacts a controller or sends a password.

   Left open by this slice: channels that are not LAN — serial, IPMB — are not constrained in-band,
   because which commands a controller accepts for them differs by firmware; the account is given
   Operator on the fencing LAN channel and no access on every other LAN channel, and the guide tells
   the operator to check every channel when commissioning hardware. **No physical controller has
   been contacted and no host has been powered off** — every `ipmitool` interaction is against a fake runner and every Redfish one against an
   `httptest` server. A QEMU BMC simulator would prove the in-band commands but has no LAN side, so
   it cannot prove that the account Core created can power a machine off over the network; that
   needs hardware. The Redfish Host Interface (SMBIOS type 42) is still not built, so **Redfish
   automatic pinning without first-use trust depends on it**; the automatic profile is `ipmilan`,
   which has no certificate to pin, and a manually recorded Redfish profile is still pinned on first
   use with the operator comparing the fingerprint. No browser review was run.
3. **Continuous non-destructive checks**, the Fencing column and the pool summary —
   *implemented, 2026-09-22*. Every ready host reads every other host's fence device
   periodically and reports what it found; the master turns those into a per-host, per-method
   readiness and reports it with its membership.
   What this slice settled:
   - **A check cannot power anything off, structurally.** The whole check is
     `vmhost.CheckPoolFenceTarget`: the identity read a fence already performs, then a power read
     that is a Redfish `GET` or `ipmitool chassis power status` and nothing else. One test asserts
     it per call over both protocols; another asserts it over a whole pass that also walks past a
     laboratory `virsh` device, which a pass never reaches for at all.
   - **A member holds no other host's fence password, and still must not.** The material for every
     host is sealed inside each member's own signed replica, and that is where a check opens it —
     in memory, for the pass, then discarded. The alternative, writing every host's fence password
     onto every member so a read-only check could run, would spread the secret across the pool for
     nothing; so `CheckPoolFenceTarget` takes the secret as a parameter, and the IPMI power read
     takes it through `IPMI_PASSWORD` exactly as the identity read does. Independent review found
     this by running the pass unstubbed: before it, only the master could complete a check and the
     master was reported *Not protected* for ever.
   - **A result is evidence about the device it was taken against.** Each carries a stamp of the
     recorded profile and identity, and the master drops results that do not match what it records
     now, so a replaced fence device reads as *not checked yet* rather than inheriting the previous
     device's success.
   - **The pool's count is Core's.** The master counts its own ready hosts and reports
     `ready of total` with the hosts that are not; an ejected tombstone and a host that has not
     finished joining are neither counted nor named. A client displays that number and adds up
     nothing.
   - **Readiness gates nothing, deliberately.** No decision path reads it: a promotion is still
     decided by `PoolFenceUsable` and the live fence proof. That is what makes a lying member
     survivable, and it is the boundary slice 4 must keep.
   - **Readiness is an observation, and lives in the master's memory.** It is overlaid on the
     membership the master reports, exactly as `replica_generation` already is, and never written
     into the durable record — a test runs twenty passes and asserts `pool-peer.json` is
     byte-identical afterwards. A master that restarts reports every method as *not checked yet*
     until the next pass rather than keeping a durable store of something one interval
     re-establishes; a fresh Controller reads readiness from master inventory like everything else.
   - **Freshness is the master's own clock**, stamped when a result arrives, so it never depends on
     two hosts agreeing about the time. Three intervals without confirmation is stale, and a stale
     check is shown as stale and is never readiness. This is also where slice 2's
     `PoolHostBMC.observed_at` is settled: it is when a finding last *changed*, not when it was
     last confirmed, and the contract and the guide now say so.
   - **A host does not get to say whether it can be shut out.** A member's report is credited to
     the member that sent it and an entry naming itself is dropped — slice 1's `system_uuid` rule
     and slice 2's fence-device rule applied to the same door. Readiness needs at least one *other*
     ready host's fresh `ok` check.
   - **Cadence over completeness.** Five minutes with a fifth of that in jitter, one target at a
     time, 20 s per target and 2 minutes per pass, riding the existing reconcile tick on its own
     worker so a pass spending its whole budget never delays the storage-cluster reconcile beside
     it. What a pass could not reach keeps its last result and goes stale, which is true.
   - **A pool of one host is not waiting.** It has nobody to be checked from, ever, and nothing to
     promote over either, so it says so in its own state rather than in the language of a pending
     check.
   - **The readiness shape is a list of methods** so watchdog self-fencing is a second entry beside
     the fence device, with no contract change. No watchdog code or placeholder exists.
   - **Promotion is advised, never decided.** The verdict names the method each blocking host would
     be shut out with, and warns where the candidate's own last check of that device was failing or
     stale. The proof path is untouched.
   Slice 2's verified-open findings are closed here: the master's withdrawal decision is projected
   and drives the row's actions instead of the host's echo, so a host that is down after a clear
   shows the truth and still offers both ways out; a host's local BMC record is stamped with its
   pool, so a re-joined host configures its controller again without a second account; the pending
   password script is retired when the account is removed; a manual profile wins over a withdrawn
   automatic one; the latent advertise drop is fixed; the reinstall-after-a-half-finished-pass case
   is documented in the guide's `name_conflict` row; the **Clear fence device…** button no longer
   opens an empty **Set BMC access** form; and the inert generation assertion is replaced by a
   byte-identical durable-record assertion.

   Left open by this slice: **still no physical management controller and no host powered off** —
   what real firmware does under a check every five minutes from every other host in a pool, its
   session table in particular, is unproven, and the cadence is a judgement rather than a
   measurement. IPMI needs two short sessions per target per pass because `ipmitool` cannot issue
   `mc guid` and a chassis power status in one invocation. The browser review of the Fencing column
   was against mocked API data in a fixture page, not a running Controller against real hosts.
4. **Watchdog fencing**, delivered in three parts.

   **4a — hardware watchdog detection, the Host fencing setting, the watchdog method** —
   *implemented, 2026-09-22*. Nothing is armed and no watchdog method can be `ready`, so N-of-M
   readiness is unchanged by it.
   What this slice settled:
   - **Detection never opens the device.** On most drivers opening `/dev/watchdogN` *is* the
     arming, so a host that has not been told to arm a watchdog must not be reset by having been
     looked at. `vmhost.LocalWatchdog` reads `/sys/class/watchdog` only and names the device by its
     sysfs name; one test builds a fake sysfs tree and another asserts no string literal in the
     detection source names a path under `/dev`.
   - **Classification is by kernel driver, in one list.** `iTCO_wdt`, `sp5100_tco`,
     `ipmi_watchdog`, `hpwdt`, `wdat_wdt`, `i6300esb`, `mei_wdt` and `sbsa_gwdt` are hardware;
     `softdog` is software; anything else is unknown and counts as nothing until it is classified.
     A driver that registers with no parent device is classified on its identity string instead.
     Only hardware ever counts (decision 2), and `softdog` is reported precisely so an operator
     sees why their host is not protected.
   - **A watchdog is a capability, not a running observation.** `PoolMember.watchdog` rides the
     existing advertise route beside `system_uuid` and `bmc`, is recorded in the calling member's
     own row, is written only when the finding changes, and carries **no timestamp** — nothing
     decides anything from how long ago a host last said it. There is deliberately no armed field
     that nothing sets; an armed report needs its own freshness rule and belongs with the arming.
   - **The setting is a decision, so the durable field is the negative.**
     `PoolMembership.host_fencing_disabled` makes an absent field mean *on*, which is the default
     (decision 1) and which a pool recorded before the setting existed keeps with nothing to
     migrate. It is master-owned, replicated with the pool's records, changed through
     `POST /api/v1/pools/{id}/host-fencing` relayed to the master, refused while the pool authority
     is frozen, and idempotent as a desired state. Gated by `pool-host-fencing-v1`.
   - **Turning it on arms nothing by itself**, so the master reports its own prerequisite checklist
     — hosts, hardware watchdogs, confirmed fence devices and quorum — with its membership, and an
     operator meets it whether or not they open the setting. **Quorum is honestly `unknown`** until
     4b provides it; claiming it met would read as though arming were one switch away.
   - **A watchdog method that can never be ready still has to say something true.** At two hosts it
     is `not_applicable`, which is neither a wait nor something an operator can fix (decision 3);
     otherwise it names what is missing, or that the hardware is present and simply not armed yet.
     The Fencing column shows it on its own line and the badge is never changed by it.
   - **The consequence is beside the switch, not behind it**, because the setting defaults to on:
     an armed host that loses quorum resets itself and its running VMs stop, and a switch failure
     that splits a pool reboots the minority side. Turning the setting off needs no destructive
     confirmation — it only stops Nex Core arming anything.

   Slice 3's verified-open findings are closed here: a fence credential is **parsed and never
   executed**, so the five-minute unattended check loop no longer runs a shell on bytes a master
   sealed into a member's replica; the profile stamp covers the credential, so rotating only a
   password drops the checks taken with the previous one; the safety comment naming the deleted
   `fenceTargetPower` now names the code that actually keeps the guarantee; an unstamped check from
   an older Core degrades to *dropped* rather than failing the whole pass with 400, and the
   compatibility note says so; and the pool fence summary moved off the master's row to
   `PoolMembership.fence_summary`, projected once for the pool through `poolMembers`.

   Left open by 4a: **no physical watchdog device of any model has been read** — detection is
   exercised against fake sysfs trees only, and the acceptance gate's requirement to name the exact
   models tested is outstanding. The hardware-driver list is a judgement from kernel driver names,
   not a survey of shipped hardware; an uncommon driver reads as unknown, which is the safe
   direction. Whether the Host fencing setting should also govern *opting into the restarted-master
   restriction* was checked and deliberately not wired: that restriction is driven by whether a
   host has ever been fence-exposed, is enforced by Core before any start path, and has no opt-in
   of its own, so making it follow this setting would change a safety refusal that exists
   independently of watchdogs. It belongs with 4b, where arming actually happens.

   **4b — the engine, arming and the proof** — *implemented, 2026-09-22*. Arming, the armed
   report, the `watchdog` proof kind and the real quorum prerequisite. **The pool does not yet
   form its own Corosync/Pacemaker cluster**; see the open item at the end of this slice.
   What this slice settled:
   - **The engine is `sbd` in diskless watchdog-only mode, and it needs Pacemaker.** sbd's only
     sources of truth are a shared block device and Pacemaker (`-P`). With neither, measured in
     the lab, it starts, holds the watchdog open and **never expires it** — the worst possible
     failure, because the host reads as armed and cannot be fenced. So the configuration Nex Core
     writes is `SBD_PACEMAKER=yes`, no `SBD_DEVICE`, `SBD_WATCHDOG_DEV=/dev/<sysfs name>` and
     `SBD_WATCHDOG_TIMEOUT=5`, and a host whose Pacemaker is not running is **not armed** and
     says so.
   - **`no-quorum-policy=freeze` must become `suicide`, and this was measured both ways.** With
     `freeze` a partitioned host was **still running after two minutes**: freeze means "stay put",
     sbd's Pacemaker servant sees a healthy node and goes on feeding the watchdog. With
     `suicide`, the same host left the quorate partition 8.6 s after its cluster link went down
     and **had reset itself and rebooted by 15.7 s**, while the two survivors stayed quorate and
     did not reset. So `freeze` and a bounded self-reset are mutually exclusive, and the plan's
     expectation that sbd's properties would *replace* freeze is right — it is replaced by
     `suicide`, not made redundant. Clustered storage's current `freeze`
     (`storageClusterFencingProperties`) therefore has to change with it, in the slice that makes
     the pool own the CIB. **Until then a host whose cluster still says `freeze` must not be
     armed**, because it would read as protected and never reset; `ArmLocalWatchdog` reads the
     policy and refuses. The quorate side's own bound is `stonith-watchdog-timeout`, which is what
     DLM recovery waits out before treating a node with no other fence device as fenced — and it
     must be derived from the **measured worst case**, not from twice the watchdog timeout. The
     lab's own `10s` was wrong: the measured reset completed at 15.7 s, so a quorate side using
     `10s` would have declared the node fenced about five seconds before it actually reset.
     **Settled: `stonith-watchdog-timeout = 30s`** with `SBD_WATCHDOG_TIMEOUT = 5` — six times
     the watchdog timeout (sbd requires strictly more than it, and recommends a multiple), about
     four times the measured 7.1 s between leaving the partition and resetting, and twice the
     measured 15.7 s end to end. Nex Core's own proof bound (`PoolWatchdogBound`, floored at
     60 s) is deliberately larger again, so a proof can never be granted before the quorate side
     would even have acted. Both numbers rest on one nested-lab run and must be re-measured on
     physical hardware.
     `stonith-enabled=true` and `have-watchdog` (which Pacemaker sets itself once sbd is present,
     and which the lab confirmed as `true`) are unchanged.
     **Superseded by 4c:** the `stonith-watchdog-timeout = 30s` settled here is not written at all
     on a pool-owned cluster, and there are no per-host BMC STONITH resources on one. See 4c.
   - **Arming can only happen as a cluster starts.** Debian's `sbd.service` is
     `RefuseManualStart`/`RefuseManualStop` and exists only as a dependency of `corosync.service`.
     So Nex Core enables it and, where the cluster is already running, **stops there** and reports
     *arming pending — put the host in maintenance mode and restart it*. It never restarts a live
     cluster to arm a watchdog: doing that under DLM/GFS2 is how data is lost. This is the
     non-disruptive rollout rule the brief required, grounded in what the unit actually does.
   - **Disarming is a clean SIGTERM.** sbd closes the device with the kernel's magic character and
     the timer stops; measured in the lab, the host did not reset. Where the kernel was built with
     `nowayout` that close does not stop the timer, so Nex Core **refuses to try** and reports that
     the host stays armed until it is rebooted, rather than resetting a machine to get out of it.
   - **The armed report is durable, the capability is not.** `PoolMember.WatchdogArmed` carries
     the device class, the timeout and **the pool membership generation the host armed at**, and
     is written only when it changes. Staleness is that generation, not a clock: the host that has
     to read it is a promotion candidate whose master has gone, and it reads it out of the signed
     replica it already holds. A wall clock could not survive that. A pool has **two** generation
     counters — membership and authority — and they diverge immediately, so the generation is
     read off the member record itself (`PoolMembership.Valid` pins every member's to the
     membership's) and is never a parameter a caller could get wrong. Independent review found
     that it was one: readiness stamped and checked the membership generation while the promotion
     path passed the authority generation, so a pool would have reported **Watchdog · armed** and
     then refused to promote. A test now drives `poolPromotionFenceReadiness` and
     `fencePoolExcludedHosts` rather than the proof function directly, which is what let it
     through.
   - **"Armed" means armed and still verified.** Every pass re-checks Pacemaker, the cluster's
     policy and sbd's configuration, and **the verdict decides the report**: a host whose
     re-verification fails withdraws its own armed report, stripped of the kind, timeout and
     generation a proof could rest on. The live case is an operator creating a clustered-storage
     repository, whose `no-quorum-policy=freeze` lands on the same cluster; without the
     withdrawal every host would still read armed and the survivors would prove exclusions
     against hosts that had been measured still running after two minutes.
   - **The record goes before the act, in both directions.** A host is recorded armed only after
     it is verified armed, and a host stops feeding its watchdog only after the pool's durable
     record says it is not armed. If that record cannot be written — the master is unreachable,
     which is exactly the state a proof is used in — the host **stays armed** and says so.
     Staying armed is always the safe error.
   - **A blind window is a flap.** The continuity clock restarts on any observation gap longer
     than a quarter of the shortest bound (derived from it, so the two cannot drift apart) and on
     any change to Corosync's own ring identifier, which moves on every membership event and
     catches a target leaving and rejoining between two readings. The quorum reading was moved
     off the cluster-reconcile worker onto its own tick for the same reason: that worker can
     spend minutes in a conversation with a management controller.
   - **Disarming never disturbs a running cluster.** Measured: `systemctl disable` under a live
     cluster is inert, while a `SIGTERM` left the unit `failed` on its `RequiredBy=corosync.service`
     edge — with no clustered filesystem mounted, the case that matters and the one not measured.
     So a disarm disables the unit and reports `disarm pending`; the device is released at the
     next cluster stop, mirroring the arming rule. Disabling also runs on hosts the pool never
     asked to arm, because **Debian's sbd package leaves the unit enabled on install** (measured)
     and `nex-core-local`'s postinst therefore disables it unconditionally: an unmanaged sbd
     would otherwise be pulled into the next cluster start of an existing clustered-storage host
     with the distribution's configuration instead of Nex Core's.
   - **Quorum is read from the engine that decides it.** `corosync-quorumtool -p`, parsed for
     `Quorate:` and the membership names — the last field of each row, because a pool with a
     quorum device gains a column before the name. Nex's own heartbeats are never the input: sbd
     resets a host because votequorum said so, so the survivors must read the same votequorum.
   - **The `watchdog` proof measures all three conditions and records each one.** The target's own
     armed report at this generation; a quorate partition, by Corosync, that excludes the target
     and has more than one host in it; and a continuous monotonic hold of at least twice the
     target's timeout, never less than a minute. Any flap restarts the clock — the target
     reappearing, quorum lost, or the proving agent restarting, because an agent that was not
     running cannot say what happened while it was not looking. Refusal before the bound says how
     long remains. No force, no override, and a two-host pool never produces one.
   - **Emulated watchdogs are their own class.** `i6300esb`, `ib700wdt` and the ACPI `wdat_wdt`
     are real chips and also what a hypervisor shows a guest, so the driver name cannot tell them
     apart. A host whose DMI vendor is a hypervisor reports `emulated`: Nex Core still arms it, so
     a nested lab exercises the whole path, and every proof, row and guide sentence resting on one
     says it is evidence about a hypervisor and not about hardware. A physical host never reports
     it.

   Left open by 4b, and material:
   - **Nex Core writes no cluster property.** `no-quorum-policy=suicide`,
     `stonith-watchdog-timeout` and the `have-watchdog` check belong to the pool's CIB, which the
     pool does not own yet; the lab set them by hand. Until Nex Core writes them, arming refuses
     on any cluster that does not already say `suicide`, which today is every clustered-storage
     cluster. **That means the practical effect of this slice is that nothing arms by itself
     anywhere**, which is safe and honest but is not the feature; the slice below is what closes
     it.
   - **Nex Core does not form a pool's Corosync/Pacemaker cluster.** Arming happens where one is
     already running, which today means a pool with clustered storage. Making the pool own
     `corosync.conf` and the CIB properties — ring addresses from `PoolMember.PeerOrigin`, the
     authkey sealed per member through the existing credential replication, node names that are
     already the durable host IDs, join/leave/master change, and clustered storage becoming a
     consumer rather than the owner — is a slice of its own. It was **not** attempted here
     because a half-owned `corosync.conf` with two writers is worse than none. The 3–5 node limit
     in `RenderStorageCluster` is a clustered-storage limit (GFS2 journals), not a votequorum one;
     a pool cluster should take every ready host and report a ceiling rather than silently
     excluding hosts.
   - **No physical watchdog of any model has been read or armed, and no physical host has been
     reset.** The acceptance gate naming exact models stays **OPEN**. Every timing here is from
     nested QEMU guests with an emulated `i6300esb`.
   - **Arming default.** Owner decision 1 is on by default and the code implements that. Because
     nothing arms until a pool has a cluster, and because no physical hardware has been tested,
     the practical effect today is that only a clustered-storage pool arms. The owner should
     decide whether that stays on by default once the pool cluster exists and before physical
     evidence.

   **4c — the pool owns its cluster** — *implemented, 2026-09-22*. Nex Core forms and maintains
   one resource-free Corosync/Pacemaker cluster per pool, so a plain pool arms by itself.
   **Owner decisions, 2026-09-22:** (1) build this for pools of three or more hosts **without
   clustered storage** first; pools with clustered storage keep reporting *not armed* with the
   reason until a separate GFS2/DLM slice. (2) Host fencing **stays on by default** — chosen over
   the implementer's and the reviewer's recommendation of off-until-hardware-proven, so this slice
   closes every known way default-on could reset a healthy host or grant a proof against a host
   that will not reset.
   What this slice settled:
   - **The nodelist is derived, never a second source of truth.** Every host renders the pool's
     `corosync.conf` from the membership it already holds: ring addresses from `PoolMember.PeerOrigin`,
     node names that are the durable host IDs (which `corosync-quorumtool` reads back and
     `pcmk_host_*` already matches), and node IDs that are a stable digest of the host ID and are
     never reused for another host. There is no cluster record and nothing for a Controller to keep
     or recover; a fresh Controller asks the master for its membership as it does for everything.
     `RenderStorageCluster` and the pool cluster are now two callers of one renderer and **one
     writer**, `writeCorosyncConfig`, which refuses to take a file over from the other consumer.
   - **Nothing comes up armed, and that is what closes the cold-start hazard.** Corosync and
     Pacemaker are **started by the reconcile and never enabled at boot**, and `ApplyPoolCluster`
     disables sbd immediately before it starts them, unconditionally. Independent review found the
     first attempt at this resting on the arming pass having reached the master first, which a host
     booting after a site power cut cannot do — so it is structural now, in the one function that
     starts a cluster. The hazard this removes is the only one in the feature
     with no failure behind it: a host whose cluster starts alone — a site power cut, the last host
     of a pool rebooting — is inquorate from its first moment and, under `suicide`, resets itself in
     a loop while the whole pool is healthy. `SBD_DELAY_START=yes` and votequorum `wait_for_all` are
     the second and third lines of defence, not the first.
   - **Arming is a rollout Core runs, one host at a time, with no grant to keep.** A host arms only
     after it has watched one unchanged Corosync membership, quorate and containing every host of
     the cluster, for a full `PoolWatchdogBound`; and only when it is its turn. The turn is
     **derived** from the membership — the first ready host, in host-ID order, that has not reported
     itself armed — so there is nothing durable to add, nothing to resume, and no way for two hosts
     to hold it at once. The host whose turn it is restarts its own cluster services, which is the
     only way Debian's `RefuseManualStart` sbd can start; the others stay quorate throughout. A host
     that cannot arm holds the turn and the rollout stops there, visibly, with that host's own
     reason: fewer armed hosts is less protection, never a false proof.
   - **`stonith-watchdog-timeout` is not written at all, and is removed where it is found. The lab
     is what settled it, over three findings.** Pacemaker answers that property plus one node
     without sbd by logging *emerg: Shutting down: stonith-watchdog-timeout configured (30s) but
     SBD not active* and taking the cluster down — and the lab measured that shutdown reaching a
     node that was still feeding, which reset. Two attempts to order the write safely each left
     another ordinary event that produces an unarmed node: forming the cluster, the rollout, the
     teardown, and a host rebooting back into an armed pool. The property buys a wait before
     Pacemaker recovers **resources**, and a pool's cluster has none by construction, while Nex's
     own 60 s proof bound — which gates promotion and is independent of the CIB — is untouched. So
     it is not written and it is removed. A host also will not *restart* its cluster while the
     property is still there, which is the guard that matters for a teardown; nothing can strip it
     from a CIB before Pacemaker runs, so an inherited one is repaired by the master once the
     cluster is up rather than refused at the start. It belongs with the slice that puts DLM and
     GFS2 on a pool cluster, where the wait means something.
     Decision: project owner, 2026-09-22 — the removal is ratified on the reviewer's decisive
     reason: the property's only function is to let the quorate side assume a watchdog-only node is
     down so it can recover that node's **resources**, and a pool's cluster is resource-free by
     construction and structurally refuses to be anything else, so it bought nothing here while the
     lab measured it costing a healthy-host reset through a mechanism four ordinary events trigger.
   - **Restarting a cluster is safe here because it runs nothing, and that is checked.**
     `RestartPoolClusterForArming` refuses any CIB with a resource in it, so this can never become
     the thing that restarts Corosync under a mounted clustered filesystem.
   - **Clustered storage and watchdog fencing exclude each other, in Core, both ways and tested.** A
     pool with a clustered repository is never given a watchdog cluster; creating one on a pool whose
     hosts are armed is refused, naming the way out. Under both, the single-writer rule on
     `corosync.conf` means the interlock does not depend on an intent check being right. The reason
     is measured: clustered storage writes `no-quorum-policy=freeze`, and a `freeze` host was still
     running two minutes after it was partitioned.
   - **A membership change is a reload, not a restart.** The nodelist is rewritten and
     `corosync-cfgtool -R` applied, so Nex's ready count and votequorum's expected votes move
     together without restarting a peer that is feeding a watchdog.
   - **A teardown is the rollout in reverse, and that is a safety property.** Independent review
     found that turning Host fencing off, or maintenance on one host of three, reset a host:
     disarming has latency — the device is released only when that host's cluster next stops — and
     expected votes do not shrink because somebody else left, so the last host standing was
     inquorate and still feeding. `PoolWatchdogArmed.feeding` now reports the device separately
     from `armed`; **nobody leaves the cluster while any host of it is still feeding**, and one
     host at a time puts its device down by restarting its own cluster services while the rest stay
     quorate, in the same derived host-ID order the arming rollout uses. A drained host stays in
     every survivor's nodelist until its own record says it has stopped feeding, which is the same
     defect with a different trigger in a pool of four or five.
   - **Maintenance mode is the supported way to do disruptive work.** A host entering maintenance
     stops feeding first and leaves afterwards. A three-host pool that loses one has no majority
     side, so the whole pool disarms through that ordered path and reports it — deliberately,
     rather than reaching for `last_man_standing` or a reduced `expected_votes`, which would buy
     availability by reintroducing the split-brain the feature exists to prevent.
   - **The ring runs on the management network**, so a staged management-network change on an armed
     host is a partition of that host. Core refuses to stage or apply one while this host is feeding
     a watchdog, and the guide and the setting say so.
   - **The withdrawal window is closed by a rejoin rule with a producing half.**
     `PoolWatchdogArmed.armed_at` makes a new spell of arming a *different* record, and an armed
     host that finds itself on a Corosync membership it has not verified on **re-verifies and
     re-reports** — so a host that rejoins the pool re-confirms through production code, once per
     membership event, and a settled pool writes nothing at all. A proving host refuses a proof
     against a target that joined its partition after the last armed record it holds. Independent
     review found the first attempt refusing **every** proof for ever: a Corosync ring change is a
     membership event anywhere in the pool, it was treated as every present host rejoining, and the
     only re-confirmation in the tree was a test helper — so the pool's own arming rollout poisoned
     itself. A membership event this host watched is now kept apart from a window it did not watch,
     and the same rejoin condition is applied by `PoolWatchdogUsable` as by the proof, so readiness
     and the promotion cannot disagree. A freshness rule on `observed_at` would have been wrong in
     the other direction: a legitimately partitioned host cannot refresh its report either.
   - **Two 4b review findings closed with it:** the postinst now guards on the
     `# Managed by Nex Core` marker in `/etc/default/sbd`, so an ordinary package upgrade cannot
     silently disarm an armed pool (N2); the arming pass moved onto its own tick ahead of the
     cluster pass, so notice latency is bounded by the probe interval rather than by a conversation
     with a management controller (N3); an `Observed` quorum reading with no ring identifier now
     counts as an observation gap rather than degrading the flap detector silently (N5); and the gap
     is half the minimum bound rather than a quarter, which leaves real margin over the reading
     cadence without letting a window hide a full expiry (N6).

   What independent review found, and what the lab found, are recorded in
   `docs/changes/2026-09-22-fencing-slice4c-pool-cluster.json`. Two of them are worth naming here
   because they are properties of the platform rather than of this code: **Pacemaker takes a node
   down when `stonith-watchdog-timeout` is set and that node has no sbd**, measured in both
   directions (writing it on a freshly formed unarmed cluster, and leaving it on as the last host
   put its watchdog down), so Nex Core writes it only when every node is armed and **unsets it
   again** as soon as the pool stops being fully armed; and **a disarm has latency**, because the
   device is released only when that host's cluster next stops, which is why a teardown is ordered
   and why `PoolWatchdogArmed.feeding` exists.

   - **The cold start was settled by measurement, and the reason is Pacemaker's own.** A host that
     starts this cluster alone — peers down, `no-quorum-policy=suicide` already in its persisted
     CIB, sbd disabled — does **not** panic: Pacemaker refuses to apply the policy to a cluster that
     has never had quorum and says so in the journal, *"Resetting no-quorum-policy to 'stop':
     cluster has never had quorum"*. votequorum's `wait_for_all: 1` is what makes a lone host's
     membership never-quorate, so the property added to stop a starting minority forming a false
     majority is also what holds the suicide back on a cold start. It held with `stonith-enabled`
     both true and false, for different stated reasons, over 165 s with an unchanged boot ID
     ([QA evidence](../qa/2026-09-22-automatic-host-fencing.md)). Independent review named this as the
     last plausible way default-on could reset a healthy host; it is closed by measurement, not by
     argument, and `wait_for_all` must not be removed.
   - **A teardown blocked by a lost host is answered by reporting plus ejection, not by unblocking
     it.** One host at a time can put its device down only while the rest of the cluster is there to
     stay quorate without it. With a host missing that is impossible in a three- or four-host pool —
     the arithmetic refuses at 3 nodes with 2 present and at 4 with 3, and permits only at 5 with 4 —
     and refusing is correct: either survivor leaving would take the other, still armed, below
     quorum. So nothing here unblocks the teardown. What changed is that the pool now says
     *armed — this pool's teardown is blocked by `<host>`* instead of reading as progress, and the
     guide tells the operator the two ways out: the host comes back, or it is **ejected from the
     pool**, which takes it out of the quorum arithmetic and lets the rest finish at once.
   - **A hung cluster stop is bounded and never escalated.** Measured repeatedly in the lab:
     `systemctl stop pacemaker.service` can hang indefinitely on these guests. The stop now has its
     own 20 s bound; on timeout the host stays a member and stays feeding, reports *this host's
     cluster services did not stop when asked*, and the pool reports the stall naming it. sbd is
     never signalled to get out of it, because a host still feeding inside a quorate cluster is safe
     and one whose daemon has been killed is not.

   Left open by 4c, and material:
   - **No physical watchdog of any model has been read, armed or expired, and no physical host has
     been reset.** Every timing is from nested QEMU guests with an emulated `i6300esb`.
   - **Pools with clustered storage are not armed**, and the `freeze` → `suicide` question under a
     mounted GFS2 filesystem — including whether DLM recovery on the quorate side really waits out
     `stonith-watchdog-timeout` — is unmeasured. It needs a GFS2 lab with I/O in flight and its own
     owner decision.
   - **Two-host pools** are unchanged and are the follow-on
     [two-host pool fencing plan](2026-09-22-two-host-pool-fencing.md).
   - **Pools above five hosts** are reported and left unarmed. That is this code's tested ceiling,
     not a votequorum one.

Each slice ships its guide update, API reference and checks, and follows core-first delivery.

## Acceptance gates

- Wrong-target refusal: a BMC reporting a different UUID is never powered off.
- Unreachable, credential-rejected and in-band-locked BMCs are reported, never shown as ready.
- Provisioning is idempotent across retries, reboots and master changes, and never takes over an
  account Core did not create.
- Watchdog: a partitioned minority host resets within its bound — **met in a nested lab only**,
  with an emulated `i6300esb`; the quorate side's proof is refused before the bound, with a
  software-only watchdog, without quorum, and for a target whose armed report is missing or
  stale-generation, and two-host pools never produce one — **met by test**
  (`internal/agent/pool_watchdog_proof_test.go`), including every flap that restarts the clock and
  an agent restart.
- Arming and disarming reset nobody, and neither do an agent restart, an upgrade, a join, an
  eject, a master change or a Controller outage — **met by test** for the state machine
  (`internal/agent/pool_watchdog_arming_test.go`, `internal/vmhost/watchdog_arming_test.go`) and
  **met in a nested lab** for arming, an unrelated service restart and a clean disarm. sbd feeds
  the device, not the agent, so an agent that stops starves nothing.
- Promotion and clustered storage work unchanged with both methods; no duplicate writer in any
  case of the Phase 9 failure matrix that applies — **partly open**: the existing promotion and
  clustered-storage tests pass unchanged, and `no-quorum-policy=freeze` is deliberately kept, but
  no clustered-storage pool has been run with sbd armed.
- Controller replacement recovers every fencing record and readiness state from the hosts —
  **met by test**: the armed report is in the master's durable replicated membership and the
  Controller stores nothing.
- Physical-hardware evidence names the exact BMC and watchdog models tested — **OPEN**. No
  physical management controller has been contacted, no host has been powered off, and no
  physical watchdog of any model has been read or armed. Nested labs prove logic and timing only,
  and every record here says so.
- Nex Core forms a pool's own Corosync/Pacemaker cluster for a pool without clustered storage —
  **met by test** (`internal/vmhost/pool_cluster_test.go`, `internal/agent/pool_cluster_test.go`)
  and **met in a nested lab** (the formation steps in the [QA evidence](../qa/2026-09-22-automatic-host-fencing.md):
  `formed-unarmed`, `armed-wd1`, `armed-wd2`, `armed-wd3`, `armed-settled`): the cluster forms
  quorate with nothing armed, arms one host at a time with no boot ID changing and all three
  quorate at every step, and the quorate side's bound is written only after the last host arms.
- Arming and disarming reset nobody, and neither does a single-host reboot or a whole-pool cold
  start — **met in a nested lab**, driven in the code's own order with nothing hand-ordered:
  the [QA evidence](../qa/2026-09-22-automatic-host-fencing.md) steps `teardown-disarmed-*` and
  `maintenance-*` (all three devices put down one at a time with the cluster up and quorate on all
  three hosts throughout, nobody reset) and `reboot-rejoined-unarmed` (a host rebooting back into a
  fully armed pool rejoins unarmed and nobody resets). The whole-pool cold start is
  `evidence-scenarios.json` step `cold-start` (all three back with nothing running, no further boot
  ID change two and a half minutes later), and the single-host reboot is that file's
  `single-host-reboot`. **`evidence-teardown.json` is superseded**: its teardown was made safe by
  hand and is not evidence about the code's order.
- **The watchdog-expiry path is proven on an emulated device; no PHYSICAL watchdog model is proven
  — OPEN.** Every reset measured during a partition was **Pacemaker's own panic** — the raw journal
  reads *"emerg: Signaling sbd to panic the system: Quorum lost"*, then *"emerg: Panicking directly
  because couldn't signal sbd"* — so none of those resets travelled through the watchdog device, and
  for three revisions the mechanism the `watchdog` proof actually rests on had never been exercised.
  It is now: `SIGSTOP` on an armed host's sbd processes, with the pool quorate throughout so no
  no-quorum-policy could apply and Pacemaker unable to signal a stopped daemon, **reset that host
  within 8 s against a 5 s device timeout**, its two survivors untouched, and its previous boot's
  journal ends mid-stream with no `emerg`, no panic and no shutdown trace — the signature of the
  device resetting the machine
  (the watchdog-expiry measurement in the [QA evidence](../qa/2026-09-22-automatic-host-fencing.md)).
  **Still OPEN:** the device is QEMU's `i6300esb`. No physical chipset or BMC watchdog of any model
  has been read, armed or expired, on any machine, under any load — and a real kernel hang was not
  staged, since `SIGSTOP` stops sbd and not the kernel. So this shows the device expires and resets
  when nothing feeds it; it does not show a hung kernel reaching that state on your hardware.
- A partitioned minority host resets within its bound and the survivors do not — **met in a nested
  lab, by Pacemaker's panic and not by watchdog expiry** (see the OPEN gate above):
  `evidence-code-order.json` step `partition-no-bound` (reset at 17.9 s with **no**
  `stonith-watchdog-timeout` on the cluster, both survivors quorate and unchanged, the target's own
  journal reading *emerg: Signaling sbd to panic the system: Quorum lost*), and
  `evidence-scenarios.json` step `master-partitioned`, a genuine three-host pool in which the
  master reset at 17.9 s while the two survivors stayed quorate (Nodes 2, Total votes 2) and the
  old master returned unarmed with no second reset. The **proof and the promotion over it are
  met by test only**; no proof and no promotion ran in the lab.
- The clustered-storage interlock, both directions; the drained host leaving before the survivors
  reload it out; the management-network refusal; the one-at-a-time rollout and teardown; the rejoin
  rule on a proof and its agreement with readiness — **met by test**.
- **OPEN:** a pool with clustered storage arming at all. It keeps `no-quorum-policy=freeze`, is
  never given a pool-owned cluster and reports why; the `freeze` → `suicide` change under a mounted
  GFS2 filesystem is a separate slice with its own lab and its own owner decision.
- **OPEN:** physical hardware. No physical BMC or watchdog model has been tested; every number in
  this plan comes from emulated `i6300esb` devices in nested guests.

## Decisions

Decision: project owner, 2026-09-22.

1. **Watchdog arming default.** **Armed by default** in pools of three or more hosts whose hosts
   have hardware watchdogs. The **Host fencing** pool setting turns it off. The partition-reboot
   consequence is still shown.
2. **Software watchdog.** `softdog` **never counts** as proof. It is reported, and reported as not
   counting.
3. **Two-host pools.** **BMC fencing in this plan**, and a two-host pool **never** produces a
   watchdog-majority proof. Methods for a two-host pool without a usable BMC are settled in the
   follow-on [two-host pool fencing plan](2026-09-22-two-host-pool-fencing.md); BMC still wins
   wherever it is usable.
4. **BMC account creation.** **Automatic**, reported per host, and removable from the host row.

5. **Pool-cluster scope.** Build the pool-owned cluster for pools of **three or more hosts without
   clustered storage** first. A pool with clustered storage keeps reporting *not armed* with the
   reason until a separate GFS2/DLM slice.
6. **Host fencing stays on by default**, over both the implementer's and the reviewer's
   recommendation to ship it off until physical hardware evidence exists. Slice 4c therefore closes
   every known way default-on could reset a healthy host or grant a proof against a host that will
   not reset; the physical-hardware gate stays open.
7. **OPEN — `stonith-enabled` on a pool's cluster.** Shipped as `true`, because every partition
   measurement in this plan was taken under it. Measured both ways in
   `evidence-r1-r2-probes.json`: under `false` an armed host still self-fenced at 17.9 s, and
   `false` also silences the permanent scheduler error Pacemaker logs on **every healthy pool**
   for having a watchdog and no STONITH resource — an operator-visible cost of the shipped value.
   A pool's cluster is resource-free, so neither value gives it a fencing action to run. One
   property to change; the choice is the project owner's.

That seventh item is the one decision still owed by the project owner on this plan.
