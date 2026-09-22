# Two-host pool fencing

Status: planned, not implemented. Decision: project owner, 2026-09-22. Follow-on to
[automatic host fencing](2026-09-22-automatic-host-fencing.md), which this plan extends; it
answers that plan's open decision 3 and does not restate its BMC or watchdog design.

## Problem

Two hosts have no majority. The parent plan's watchdog method needs a quorate partition that
excludes the target, and two hosts can never produce one: each side sees one vote of two. Without
a BMC such a pool therefore has no automatic proof that its peer is off, and the only remaining
path is the manual, fence-verified promotion that already exists.

The failure to prevent is a network split where both hosts are alive and healthy. Two masters, or
the same VM started twice against the same disks, must not be possible in any of these pools.
A two-host pool is the ordinary small deployment, so "use three hosts" is not an answer.

## Decision

Per pool, in precedence order. The first usable method wins and the others are not used:

1. **BMC power fencing** — the parent plan's method, wherever it is usable. It needs no majority
   and is unaffected by host count.
2. **Storage lease** — the default for two-host pools with shared storage, enabled with the pool's
   **Host fencing** switch, which defaults on. A host must hold a continuously renewed lease on
   the pool's shared storage to run VMs from it and to hold mastership. A host that cannot renew
   is reset by its own hardware watchdog; the survivor acts only after the lease has provably
   expired.
3. **Nex Witness** — for pools without shared storage. A third vote from outside the pair,
   selected automatically with no operator input.

A valid proof from any of the three lets the survivor of a two-host pool **promote itself
automatically**, with no operator click. Without a valid proof nothing is ever automatic: the pool
falls back to the existing manual, fence-verified promotion.

Storage unreachable means nobody takes over. That is acceptable: the VMs on that storage could
not run anywhere either. A software watchdog never counts, unchanged from the parent plan's
decision 2. There is still no force, override or "declare host lost".

## Design

### Storage lease

- **Engine.** Debian's `sanlock` with its watchdog daemon `wdmd`, the native lease manager built
  for leases on shared files including NFS. Nex writes a lockspace and per-VM resource leases on
  the pool's shared repository; it does not invent a lease protocol. `sanlock` is not in
  `installer/packages.txt` today and is added with a licensing review.
- **Held for what.** One lockspace per pool repository. The master holds an authority lease; each
  running VM's owner holds that VM's resource lease. Loss of renewal escalates through `sanlock`'s
  own path: kill the VM processes, and if that does not complete in time `wdmd` stops feeding the
  hardware watchdog and the host resets.
- **One watchdog device, one owner.** `wdmd` and Pacemaker `sbd` both take `/dev/watchdog`
  exclusively; a host cannot run both. The pool records which method owns the watchdog, and a host
  refuses to start the second one. Today the conflict cannot arise in a two-host pool: Nex Core
  renders a Corosync configuration in two places, clustered storage
  (`internal/vmhost/storage_cluster.go`, three to five nodes) and the pool watchdog cluster
  (`internal/vmhost/pool_cluster.go`, whose `RenderPoolCluster` refuses fewer than three hosts),
  and neither can exist with two hosts, so a two-host pool never runs `sbd`. The rule is
  stated anyway, because a pool that grows past two hosts must switch owner deliberately, not by
  race. A pool running clustered storage uses the parent plan's watchdog method and not this one.
- **Storage-lease exclusion proof.** Valid only when Core records all of:
  - the target's **hardware** watchdog was reported armed by the target itself, with its timeout,
    and that report is in the master's durable record at the current fencing generation;
  - the proving host holds the pool lockspace on the same repository and `sanlock` reports the
    target's host ID as free — that is, expired, not merely unseen — which `sanlock` grants only
    after its own host-dead interval;
  - that free state has held continuously, on a monotonic clock, for at least the configured
    lease fencing timeout, which is at least the lockspace's host-dead interval and at least twice
    the target's watchdog timeout; and
  - the proof is bound to this pool, this excluded host, this operation and this fencing
    generation, and is re-checked immediately before activation, as BMC proofs already are.
  A prover that cannot reach the storage produces no proof. An unreadable lease is not an expired
  lease.

### Nex Witness

- **Engine.** Debian's Corosync quorum device: `corosync-qdevice` on each pool host and
  `corosync-qnetd` on the witness, giving the pair a third vote. Nex Core wraps selection,
  registration and reporting; there is no Nex consensus code. Neither package is installed today.
- **Automatic selection, zero clicks.** The pool master picks its witness itself, in this fixed
  order, and records the choice:
  1. another pool's master in the same site, preferred because it keeps the tie-breaker entirely
     inside Nex Core and independent of the Controller; ties broken by lowest pool ID, so both
     hosts of the pair compute the same answer;
  2. the Nex Controller that the pool is enrolled with, which every pool has.
  There is no third step and no form. An operator-installed standalone witness package is a
  possible later addition; nothing in these slices needs it, because step 2 always exists.
- **Registration.** The witnessed pool's master registers with the chosen witness exactly as host
  enrollment works: a short-lived token, then mutual TLS under the pool CA. No operator credential
  form and no witness-side configuration.
- **Re-selection.** If the chosen witness stops answering, or its pool is removed, or a
  higher-preference candidate appears, the master re-runs the same deterministic choice and
  re-registers at a new fencing generation. Readiness is **degraded** from the moment the witness
  stops answering until a new one is registered and quorate — never a stale "ready". A pool with
  no quorate witness still runs; it only loses automatic takeover.
- **Witness exclusion proof.** Valid only when Core records all of:
  - the target's hardware watchdog armed report, as above, at the current fencing generation;
  - the proving host is in a quorate partition by the pool's own votequorum, that partition
    includes the registered witness and excludes the target, and the witness's certificate matches
    the fingerprint in the master's record at that generation;
  - that state has held continuously, on a monotonic clock, for at least the configured watchdog
    fencing timeout, itself at least twice the target's watchdog timeout; and
  - the same operation/host/generation binding and immediate re-check as above.
  A witness that is unreachable from both hosts yields no quorum and therefore no proof: the pool
  falls back to manual, fence-verified promotion. A witness never votes on content, never holds
  authority and can never promote anything.

### The Controller as witness, without breaking the invariant

A Controller may be the witness. It survives the replacement test because it carries nothing:

- The witness relationship is **pool state on the pool master** — witness kind, address,
  certificate fingerprint and the generation it was recorded at — like any other pool record.
- The witness side stores no durable pool data. `corosync-qnetd` keeps runtime membership only,
  and it re-accepts a registration presented under the witnessed pool's CA.
- A fresh Controller that adopts the pool is chosen again by the same deterministic rule and
  re-registered automatically, with no operator input and nothing to re-enter. During the gap the
  pool reports a degraded witness, which is the honest state.
- A Controller outage removes the tie-breaker and nothing else: no takeover happens, no VM stops,
  no pool setting is lost, and every Core operation — starts, stops, consoles, storage, local
  recovery — is unaffected, as the platform plan requires. The only capability that depends on the
  Controller being up is *automatic* two-host takeover, and only while the Controller is the
  chosen witness; manual fence-verified promotion still works without it.
- Two pools that witness each other create no durable dependency either, because neither stores
  the other's authority.

### Automatic takeover

In a two-host pool the survivor promotes itself at generation + 1, with no operator action, only
when all of these hold:

- it holds a valid BMC, storage-lease or witness exclusion proof of the other host, meeting every
  condition above, re-checked immediately before activation;
- the existing step 2 replica-supremacy check passes on its own durable replica — an incomplete or
  ambiguous authority record blocks promotion, and no proof can make it authoritative; and
- no other ready member is unreachable and unfenced. In a two-host pool the target *is* the only
  other member, so OD-6 is satisfied by the same proof.

Everything else about promotion is unchanged: the pool ID, VM, repository and network identities
are preserved, healthy workloads are not restarted, the old host restricts itself and rejoins as a
member, and automatic *VM* restart still follows the Phase 9 HA gates. Promotion is not a restart
policy.

### Pools with local storage only

With no shared storage the survivor cannot start the dead host's VMs at all — the disks are on the
dead host. VM-disk corruption is therefore not at stake, and two remaining risks are:

- **Split of the master role and the pool record.** Already handled by the existing rules, and no
  new mechanism is proposed. Authority is generation-numbered; members accept only a higher
  generation; promotion requires a fence proof of the old master and, under OD-6, of any
  unreachable ready member; a returning master restricts itself and rejoins as a member. A split
  without a proof produces no promotion, which is the correct outcome. A witness supplies the
  missing proof for exactly this case.
- **Replicated VMs.** ZFS replication does not exist and is out of scope in the ZFS repository
  plans; when it arrives, starting a replica is a start of that VM and must require an exclusion
  proof of the other copy's owner, because otherwise a split forks application data and duplicates
  the guest's IP and MAC.

| Situation | Automatic proof | Result |
|---|---|---|
| BMC present | BMC power fencing | Automatic proof and takeover; host count irrelevant |
| No BMC, shared storage | Storage lease | Automatic proof and takeover; storage down means nobody takes over |
| No BMC, local storage only | Witness quorum | Automatic master takeover; VMs stay with their disks |

### Rejected

- **More heartbeat links** (second ring, serial, direct cable): lowers the probability of a split,
  proves nothing when one happens. Not a proof source.
- **Deterministic self-reset** (the member always resets on link loss): safe, but the survivor may
  be the one that just lost its uplink, so it delivers no failover in the cases that matter.
- **Operator "declare host lost"**: forbidden by the parent plan and by OD-5. An assurance is not
  evidence, and it does not survive the host powering back on.

## Operator experience

- **Hosts tab, Fencing column** (same column as the parent plan): `Storage lease · held`,
  `Witness · quorate`, `Witness · degraded, re-selecting`,
  `Not protected — hardware watchdog missing`.
- **Pool summary** names the witness in use, for example `Witness: pool master of <pool>` or
  `Witness: Nex Controller`, and shows degraded plainly rather than as ready.
- **Pool setting: Host fencing**, already defaulting on, gains the lease repository and the
  witness in its prerequisite checklist, with the automatic-takeover consequence stated.
- No witness form, no credentials form, no selection dialog anywhere.
- Promotion already names the method that will fence each blocking host; it gains these two, and
  says when it happened automatically.

## State ownership

Core-owned. The pool master's durable record holds the lease repository and lockspace, the
watchdog owner per host, the witness kind, address, certificate fingerprint and the generation
each was recorded at, alongside the fencing setting and generation the parent plan defines.
Leases live on the shared storage itself; quorum state is runtime and reported. A Controller that
acts as witness stores nothing about the pool and is not consulted to recover any of it; a fresh
Controller recovers everything from master inventory and is re-selected as witness by the pool.

## API impact

**Planned: changed contract.** Host reports gain lease and quorum-device status and the watchdog
owner; the pool gains lease and witness records, a witness registration operation and an automatic
promotion result; proofs gain `storage-lease` and `witness` kinds; promotion verdicts name them.
The Controller gains a witness endpoint that runs `corosync-qnetd` for registered pools and holds
no pool state. Each slice defines authorization, validation, errors, retries, outage behaviour and
version compatibility, and updates [API.md](../API.md). **API impact of this planning change:
none. Guide impact: none** — no behaviour changes yet.

## Slices

These follow the parent plan's slice 4.

5. **Storage lease.** `sanlock`/`wdmd` packaging and licensing review, pool lockspace on a shared
   repository, VM resource leases required for start, lease-loss self-reset, the watchdog-owner
   rule, and the `storage-lease` proof kind.
6. **Nex Witness.** `corosync-qdevice` on hosts and `corosync-qnetd` on the Controller appliance
   and on pool masters, deterministic automatic selection and re-selection, registration under the
   pool CA, the `witness` proof kind, and the Fencing column and pool summary entries.
7. **Automatic takeover** for two-host pools, gated on a valid proof from slice 5 or 6 plus the
   existing supremacy checks, with its outage, partition and returning-host cases.

Each slice ships its guide update, API reference and checks, and follows core-first delivery.

## Acceptance gates

- Nested-lab partition tests on a two-host pool: cutting the link between hosts produces exactly
  one survivor that takes over, and never two masters or two running copies of a VM.
- Storage lease: the minority host resets within its bound; the survivor's proof is refused before
  the bound, with a software-only watchdog, with the target's armed report missing or stale, and
  when the prover cannot read the lockspace. Storage removed from both hosts yields no takeover.
- Witness: proof refused when the witness is unreachable, when its certificate does not match the
  recorded fingerprint, and when the partition is not quorate. Killing the witness never stops a
  running VM and never loses a pool setting; readiness goes degraded and a new witness is selected
  and registered without operator input.
- Controller as witness: stopping the Controller leaves every Core operation working and only
  removes automatic takeover; replacing the Controller re-establishes the witness automatically,
  and the replaced Controller holds no pool state to restore.
- Automatic takeover never fires without a re-checked valid proof, including when the old host
  returns mid-promotion; the returning host restricts itself and rejoins as a member.
- Honest limits: nested labs prove logic and refusal paths only. Watchdog reset timing and NFS
  lease expiry under real storage latency need physical evidence naming the exact hardware, server
  and mount options; do not claim a failover time from a nested lab.

## Decided

All five questions raised with this plan are decided (project owner, 2026-09-22) and are written
into the design above: storage lease as the two-host default under a **Host fencing** switch that
defaults on; the Controller allowed as witness under the no-durable-state rules; fully automatic
witness selection with deterministic re-selection; automatic takeover on a valid proof; and no
replication decision, because ZFS replication does not exist.

No open decisions remain. Two values are deliberately left to lab measurement rather than choice:
the lockspace host-dead interval and the witness fencing timeout, each bounded below by twice the
target's watchdog timeout, measured and recorded in the implementing slices.
