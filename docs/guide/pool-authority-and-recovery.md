# Pool authority and recovery

## Status

Development product 0.4.12. This page covers what a pool member holds as a copy of the pool
master's authority and how to read that copy's state (**Settings** → **Members** → each host's
**Authority copy** check), how to **change which host is the pool master**, how to **eject a host
from a pool**, how Nex Core **configures each host's own management controller** for fencing, how to
record one by hand, how to read each host's **Fencing** check and what to do about each thing it
says, what each host's
**watchdog** line means and what the pool's **Host fencing** setting does, how to **promote a
surviving member when the pool master is lost**, and what a **restricted** host is.

What exists today:

- **A planned master change, against a master that is still working.** You choose the member; the
  current pool master hands the authority over and every other host is told. See
  [Change the pool master](#change-the-pool-master).
- **Ejecting a member**, including one that is not reachable. See
  [Eject a host from a pool](#eject-a-host-from-a-pool).
- **The authority copy and its reporting**, which is what makes a master change safe: the member
  that becomes master activates the copy it already holds and has verified.
- **A fence device on every host that has a management controller, configured by that host's own
  Nex Core**, with no address and no password typed by anyone. See
  [Let Nex configure the management controller](#let-nex-configure-the-management-controller-the-normal-path).
- **Continuous checks that those devices still work.** Every host reads every other host's
  management controller every few minutes, without powering anything off, and the pool master
  reports whether each host could be shut out and by whom. See
  [Read each host's fencing](#read-each-hosts-fencing).
- **Each host's watchdog, reported, and the pool's Host fencing setting.** Nex Core finds the
  watchdog hardware on every host, forms the pool's own cluster, arms it where the pool's
  prerequisites hold, and gives the pool a switch that governs it. **An armed host that loses this
  pool's quorum resets itself.**
  See [Host fencing, and the watchdog line](#host-fencing-and-the-watchdog-line).
- **A fence-verified promotion of a surviving member when the pool master is gone**, which you
  start by hand and Nex Core proves by powering the old master off itself. It works where the pool
  has a **fence device** for the old master, recorded before it was lost, **or** where the old
  master had **armed its own watchdog** — in which case nobody powers it off and Nex Core waits out
  the watchdog's period instead. See
  [Set BMC access for a host](#set-bmc-access-for-a-host),
  [Host fencing, and the watchdog line](#host-fencing-and-the-watchdog-line) and
  [Promote a surviving member](#promote-a-surviving-member).
- **Restricted hosts**: a Core-enforced refusal to start anything or write anything to the pool
  record on a host that cannot prove it holds the pool's current authority. See
  [Restricted hosts](#restricted-hosts).

**Product rule (OD-6, project owner 2026-09-20):** during promotion, an unreachable ready member
that holds the pool's authority replica blocks the promotion until the same operation fences that
member. Silence is never agreement; the rule is fail-closed.

What does **not** exist:

- **There is no automatic succession.** No failover, no election, no quorum, nothing that picks a
  new pool master on its own. If the pool master stops answering, the pool keeps running the
  virtual machines it has but has no master until *you* promote one, and a promotion needs a fence
  device recorded before the master was lost, or the master's own armed watchdog and the waiting
  period that goes with it. Do not plan around anything happening by itself.
- **There is no force, no override and no "declare this host lost".** No button, no field and no
  waiting period substitutes for evidence, and none will be added. Where the product cannot prove
  something it says so and stops.
- **A member never acts on its copy by itself.** It stores it and reports that it holds it. No
  membership, virtual machine, storage, network or ISO record on a member is read from or changed by
  the copy, and holding a copy grants a member no permission to run anything. Only a master change
  or a promotion you asked for opens it.
- The copy is not health evidence. A host holding a current copy is not thereby a healthy host.

### What every ready member of a fenced pool can do

Read this before you admit a host to such a pool. It applies to every pool whose hosts have a fence
device, which since Nex Core configures management controllers itself is most of them — whether or
not you ever record one by hand, and whether or not **Host fencing** is on.

The host that fences the old pool master during a promotion is a **surviving member**, and it
cannot be handed the material to do that after the master is gone. So the pool master seals a copy
of each recorded fence device's credentials **for each ready member**, inside that member's copy of
the pool records, by the same mechanism that already carries the pool's sealed certificate
authority key. Nex Controller never holds that material, never stores it, never logs it and never
returns it, and a member stores it sealed — but a member *can* open what is addressed to it
whenever it likes. That is policy, not a lock.

The concrete consequence: **every ready member of a pool that has recorded fence devices is able to
power off every host those devices name.** It is the same trust boundary the pool certificate
authority key already draws — a member that can become master can already sign this pool's
authority — and it is inherent to fence-verified promotion, not a defect. Admit only hosts you
trust with that.

Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md). Every pool step below starts
in **Settings** → **Members** (`#/settings/members`) with the pool chosen in the top-bar scope
menu; this section held the pool's former **Hosts** tab, and a host panel's **Maintenance** quick
action opens it for that host's pool.

## Prerequisites

- An administrator session in Nex Controller, and the pool's **Settings** → **Members** section
  open.
- A pool with at least one member besides the master. A single-host pool has no member to hold a
  copy, and no host shows an **Authority copy** check when the master does not support the copy
  at all.
- The pool master and the member both advertising the `pool-authority-replica-v1` capability. A
  member without it holds nothing and is reported as **not supported — upgrade**.
- Both hosts able to reach each other on their pool peer addresses. The master pushes the copy to
  each member over the same pinned pool connection it already uses; the browser's own reachability
  proves nothing about it.

For a master change or an eject, additionally:

- The pool master, the selected host and **every ready member** advertising
  `pool-authority-transfer-v1`. Without it on the master no action is offered at all; without it on
  the selected host or another ready member the request is refused and the host to upgrade is named.
  A member that is not currently reporting is not required to have it — Nex Core checks every
  participant again anyway.
- For a **master change**: the pool master fresh and active, the selected host fresh and active, a
  `ready` member of this pool with a peer address, and its **Authority copy** at **current**. The
  host takes over the copy it already holds and has verified; a missing, unverified or older copy
  blocks the change on the host, not in the browser.
- For an **eject**: the pool master fresh and active, and its current VM inventory for the target.
  The host being ejected may be unreachable, but missing or stale target inventory refuses the
  eject because absence never proves a shared VM is gone.
- No other master change or eject already running on the pool.
- Nothing else changing the pool at the same time. A master change pauses every pool authority
  write until it finishes, and other operators' requests are **refused, not queued**.

For a fence device or a promotion, additionally:

- **To record a fence device**: the pool master fresh and active, because the fence device is part
  of the membership record the master owns. This is the whole point of recording it *early*: once
  the master is lost you cannot record one for it any more.
- For a device you type in yourself: the address of the host's **management controller**, an
  account on it with power control, and that account's password. Both **Redfish and IPMI** are
  usable. Nex Core refuses a SCSI persistent-reservation device outright. You do not normally need
  any of this: see
  [Let Nex configure the management controller](#let-nex-configure-the-management-controller-the-normal-path).
- **The controller must be reachable from the pool master when you record it.** Recording a Redfish
  device is a live operation: Nex Core connects, writes down the exact certificate the controller
  presents and asks it which machine it manages, and records nothing unless both succeed. A
  management controller's certificate is normally one it generated itself, which no browser or
  operating system trusts; Nex Core does not need it to, because from then on it accepts **only
  that certificate** and nothing else. The fingerprint is shown on the host's row so you can
  compare it with the one the controller's own page shows. If the controller is later replaced or
  regenerates its certificate, Nex Core refuses the fence and says so, and you record its access
  again. IPMI has no certificate; its own session authentication takes that role.
- The host must have **reported its own machine identity** (its SMBIOS system UUID) to the pool
  master at least once while it was running, and that identity must be **unique in the pool**. Nex
  Core reads it from the management controller before any power-off and refuses to power off a
  machine that does not match. A host running an older Nex Core that never reported one, a host
  whose firmware reports a placeholder rather than a real identity, and two hosts that report the
  same identity all count as *not reporting a unique hardware identity*: none of them can be given
  a management controller, and Nex Core says so.
- For a Nex Core that runs as a virtual machine in a disposable laboratory there is no management
  controller to ask, so a `virsh` device — an SSH login on a host running libvirt — stays available
  behind a link in the same dialog.
- **To promote**: the surviving member fresh and active, a `ready` member of this pool, advertising
  `pool-authority-transfer-v1`, holding a verified copy of the pool records, and able to shut the
  old master out — either through a fence device for it that this host can use, or, where the old
  master armed its own watchdog, by waiting out that watchdog's period (see
  [Host fencing, and the watchdog line](#host-fencing-and-the-watchdog-line)).
  The pool master is deliberately **not** required to be
  reachable, and whether Nex Controller can reach it is never evidence either way: Nex Core probes
  it over the pool network itself and refuses the promotion if it answers and still holds the
  authority.

## Steps

### Read the authority copy

1. Choose the pool in the top-bar scope menu, then open **Settings** → **Members**. Each host has
   one row with a strip of status checks under its name: **Connection**, **Membership**,
   **Maintenance**, **Authority copy**, **Convergence** and **Fencing**, each a light, the label
   and one word (**OK**, **Stale**, **Failed**, **Unknown**, **Not reported** and so on). Point at
   a check for its full sentence, or select **Details** at the end of the strip to open the row's
   details, where each check has its own entry.
2. Read the **Authority copy** check for each member. The pool master's row has no such check:
   the master holds the authority, not a copy of it; its details say *Holds the pool records*.
3. Open the member's **Details** and read the state under **Authority copy**:
   - **current** — the member holds the master's latest observed authority, verified against the
     pool's own certificate authority, and no other member of this pool is known to hold a newer
     one.
   - **behind** — the two reports do not agree on one copy number, the pool master's own authority
     generation is higher than one of them, or another member of this pool
     demonstrably holds a newer one. The row then also states both numbers, for example *This host
     reports copy 7; the pool master last saw it acknowledge 6*, and says which of the two records
     is the older one. This is normal for up to one reporting interval after a change.
   - **awaiting first copy** — no copy has landed yet. Normal for a short time after a host joins.
   - **unverified** — the member holds bytes it has not proven against the pool's certificate
     authority. Treat that member as holding nothing.
   - **refused** — Nex Core refused or failed the copy; the reason is shown under the state.
   - **not supported — upgrade** — the master or this member does not have the capability.
     Upgrade the named host.
   - **unknown** — no fresh evidence. This is the honest answer, not a fault: nobody currently
     reporting can say what that member holds. It appears whenever the pool master is not fresh,
     and for a member that is not fresh.
4. Read the times under the state — *Received …* is the member's own record of when it stored the
   copy, *Acknowledged …* is when the master last saw it acknowledge one.
5. Read the **Authority copy** entry under **Convergence** in the same details. That is each host's
   own health judgement over its last two reporting intervals, and it can briefly disagree with
   the state word, which is a comparison of two reports. Both are shown on purpose; neither is
   reconciled into the other.
6. Read **Authority generation** on the pool status strip above the host rows; select **About** on
   that strip for the sentence *The pool authority is at generation N.* That is the pool master's
   own number, reported by the master and by nobody else, and it is what each member's copy is
   measured against. When the master is not fresh the strip reads **Unknown** and the sentence
   instead says that the master is not reporting one, and that how far behind a copy is cannot be
   said. A member whose copy is older also shows *N generations behind* under **Authority copy** in
   its details.

### Change the pool master

Do this with the current pool master still working. It is a planned hand-over, not a recovery.

1. Open the pool's **Settings** → **Members** section and check that the member you intend to promote shows
   **Authority copy: current** and a fresh connection.
2. Open **Actions** on that member's row and select **Make pool master**. If the button is
   disabled, the reason is printed beside it — see [Troubleshooting](#troubleshooting).
3. Read the dialog. It names the current master and the new one and states what happens: pool
   settings, placements, joins and maintenance changes are **refused** while the change runs and
   nothing is queued behind them; running virtual machines keep running and can still be stopped;
   the pool, its networks, its storage, its ISO library and its virtual machines keep their
   identities and their names.
4. Type the **pool name** in the confirmation box, exactly as it is shown, and select
   **Make pool master**.
5. Watch the **Pool master change** panel that appears above the table. It shows the action, the
   target, the stage and the status, followed by one line per participating host with that host's
   *own* report. A host that is not reporting says **unknown** — that is honest, not a failure.
6. While the panel says the authority has not been handed over yet, a **Cancel this master change**
   button is offered. After the hand-off the panel says so and the button is gone: the change can
   only go forward.
7. When the change finishes, the master row moves to the new host by itself, from the hosts' own
   reports. There is nothing to confirm and nothing to re-enter.

### Eject a host from a pool

1. Open the pool's **Settings** → **Members** section, open **Actions** on the member's row and
   select **Eject from pool**.
2. Read the dialog. The host leaves this pool and becomes the pool master of its own new
   single-host pool, keeping its host identity, its local virtual machines and its local storage;
   the pool's shared networks, storage registrations and ISO library are removed from it.
3. Type the **host name** in the confirmation box, exactly as it is shown, and select
   **Eject from pool**.
4. Watch the **Pool master change** panel for the outcome. Nex Core refuses an unknown or
   shared-storage VM record, a pool-network or pool-ISO dependency, or a pool-shared storage
   reservation. A verified local-only VM is retained on the ejected host and rehomed with it into
   its new pool; nothing deletes it or changes its NICs to make the eject proceed.
5. A host that is not reachable can still be ejected. It is removed from the membership
   immediately, and it learns that it has been ejected the next time it can reach any host of the
   pool, or when the pool reaches it — then it re-initializes itself as its own pool.

### Let Nex configure the management controller (the normal path)

You do not normally record a fence device at all. Every host looks at its own management
controller from the inside, over the kernel's local IPMI interface, and configures it itself:

- it reads the controller's own LAN address and channel;
- it creates one account there, named `nexfence`, with a random 16-character password and
  **Operator** privilege — the lowest IPMI privilege that may power a machine off, and nothing
  more;
- it enables IPMI over LAN for that account on the channel a fence will use, and denies it
  access on every other LAN channel the controller reports. Channels that are not LAN — serial,
  IPMB — differ too much between firmware to constrain from the inside, so **check the account's
  privilege on every channel** when you commission real hardware;
- it selects the controller's strongest authenticated RMCP+ cipher suite (17, or 3 where 17 is
  not offered). A controller that offers only cipher suite 0 — no authentication at all — is
  refused, because anyone who could reach it could then power the host off;
- it offers the profile and the password to the pool master, which **proves the controller really
  manages that host's machine before it records anything**, exactly as it does for a device you
  type in yourself. The password goes into the master's private credential store and reaches the
  other members sealed with the rest of the pool records. It never reaches Nex Controller, the
  audit trail or any report.

This runs on every reconcile pass and needs no operator input. It is idempotent: a host that has
already done it does nothing, whatever happens to the pool master afterwards.

**This opts the pool into the restart rule.** Once any host has a fence device — one Nex Core
configured as well as one you typed in — a pool master that restarts refuses to start anything
until every ready member confirms that it did not take the pool over while the master was away.
That is not the **Host fencing** setting and turning that setting off does not change it: the rule
exists because a host that can be fenced can also have been fenced, and starting virtual machines
before that is settled is how two hosts end up writing to one disk. Read
[What every ready member of a fenced pool can do](#what-every-ready-member-of-a-fenced-pool-can-do)
before you admit hosts to such a pool, and note what it says about a member that is permanently
gone.

The host's row says what its own Nex Core found. The states are:

| The row says | What it means |
|---|---|
| Configured by Nex Core at *address* as `nexfence` | Done. Nothing to do. |
| This host has no local IPMI interface | No `/dev/ipmi0`: the host has no management controller, or is itself a virtual machine. Use **Set BMC access…**, or the `virsh` device for a laboratory host. |
| …refused in-band account management / …has no free user slot | The controller will not let Nex create an account from the inside. Create one yourself and use **Set BMC access…**. |
| …reports no LAN address on any channel / …offers no authenticated RMCP+ cipher suite | The controller's network side is not usable. Give it an address, or enable cipher suite 3 or 17, then use **Set BMC access…**. |
| An account named `nexfence` already exists … and Nex Core did not create it | Something else made an account under that name. **Nothing was changed.** Remove or rename it, or use **Set BMC access…**. A controller that refused Nex halfway through its own work is not normally this case: the host knows which slot it was using and finishes it on a later pass, waiting about five minutes before it touches a controller that refused it. It *is* this case if that host was then reinstalled before the pool recorded the device — both records of Nex's ownership are gone, so remove the leftover `nexfence` account on the controller by hand and the host will make its own again. |
| This host's fence device was set by an operator | What you typed wins. Nex Core leaves the controller alone. |
| …the pool master has not recorded it yet | The account exists but the pool does not have the profile: the master was unreachable, or refused it and said why. The host offers it again on its next pass. **This is not a usable fence device and is never shown as one.** |
| An operator withdrew this host's fence device | You cleared it. Automatic provisioning is off for this host until you turn it back on. If the sentence says the account is **still on the management controller**, the controller would not take it off; the host keeps trying, and you can remove it by hand. |

A withdrawal is the pool master's record, so the row shows it the moment you clear the device —
including for a host that is switched off and has not answered since. Both ways out stay on that
row: **Set BMC access…** to type a device in, and **Let Nex configure this controller again** to
hand it back to the host.

If you clear a Core-configured device and then record one by hand, the hand-typed device wins and
the row says so; the `nexfence` account is still removed from the controller.

**To withdraw a fence device Nex Core configured**, open **Actions** on the host's row, select
**Clear fence device…** and confirm. That is one action with one meaning: the pool master drops the
fence device, the host removes the `nexfence` account from its own controller, and it does not
configure that controller again until you select **Let Nex configure this controller again** on the
same row. Both the withdrawal and the permission are kept by Nex Core in the pool master's records,
not by Nex Controller, and survive a rebuilt Controller. A host cannot withdraw its own fence
device: it reports what it finds, and what the pool does about it is yours to decide.

The row's one entry point to a host's fence device is the same button either way: **Set BMC
access…** where Nex Core could not configure the controller itself, and **Clear fence device…**
where it did.

### Read each host's fencing

Each host's row in **Members** has a **Fencing** check. It answers one question per host: *if this
host were lost, could another host shut it out?* The check gives one word — **OK**, **Stale**,
**Failed**, **Unknown** or **Not reported** — and the row's **Details** → **Fencing** entry gives
the full answer from the table below. Every word of it comes from the pool master, which keeps the
fence devices and collects what the other hosts found. Nex Controller stores none of it.

**The hosts check each other, and never power anything off.** Every few minutes each host signs in
to every *other* host's management controller with the recorded account, asks it which machine it
manages and reads that machine's power state. That is the whole check. There is no path from it to
a power-off: a check cannot switch a machine off by accident, by a bug in this page, or at all. A
host never checks itself — the question is whether *somebody else* could shut it out.

| **Details** → **Fencing** says | What it means | What to do |
|---|---|---|
| **BMC · checked from 2 hosts** | A fence device is recorded and that many other hosts confirmed, just now, that its controller answers and names this machine. | Nothing. |
| **virsh lab device** | A laboratory `virsh` device is recorded. It is usable, but Nex Core does not check it continuously the way it checks a management controller. | Nothing, in a laboratory. Do not rely on it anywhere else. |
| **BMC · stale** | There is a device and the last check of it is older than Nex Core will vouch for (about fifteen minutes). Usually the other hosts stopped reporting, or the pool master restarted. | Check that the other hosts are reporting. It clears itself within a few minutes once they are. **Stale is never treated as ready.** |
| **Not protected — BMC not reachable from host-b** | `host-b` could reach the controller's address but got no answer. Network, or the controller itself. | Check that host-b can reach that controller's address. The entry names every host that tried. |
| **Not protected — …rejected the recorded account** | The controller no longer accepts the account the pool recorded. Somebody changed or removed it. | Let the host configure the controller again, or use **Set BMC access…** with a working account. |
| **Not protected — no other host has checked… yet**, right after you changed a device | Replacing a fence device discards the checks taken against the one before it, so the entry is honest about the new one from the moment you record it. | Wait one check interval. |
| **Not protected — …reports a different machine…** | The controller at that address now answers for a *different* machine. This is not a network fault and Nex Core will never power anything off on it. | Find out what changed — a reused address, a moved cable, a replaced board. Clear the fence device and record the right one. |
| **Not protected — …presented a different certificate…** | Only for a Redfish device you recorded by hand. The endpoint is not the one the profile is pinned to. | Compare the fingerprint on the controller's own page. If the change is yours, clear the device and record it again. |
| **Not protected — no fence device is recorded for this host** | There is nothing to check. | [Let Nex configure it](#let-nex-configure-the-management-controller-the-normal-path), or [set BMC access](#set-bmc-access-for-a-host). |
| **Not protected — an operator withdrew…** | You cleared this host's device deliberately. | Nothing, unless you want it back: **Let Nex configure this controller again**. |
| **Not protected — no other host has checked… yet** | The device is recorded and nothing has looked at it yet. A fresh pool, or a pool master that has just restarted. | Wait one check interval, about five minutes. It is honestly *unknown*, never *ready*. |
| **BMC · no other host to check from** | This pool has one host, so there is nobody to check it — and nothing to promote it over either. | Nothing. A single-host pool needs no fence device at all; it gets one when a second host joins. |
| **not reported** | This pool master's Nex Core is too old to report fencing readiness. | Upgrade the pool master. Until then nothing here knows, so nothing here claims it. |
| **unknown** | The pool master is not reporting. It keeps this record, so nothing else can answer for it. | See [when the pool master is gone](#promote-a-surviving-member). This does **not** mean the hosts cannot be shut out; it means nothing here knows. |

Under the first line, the same **Fencing** entry gives the reasons behind it, the recorded fence
device, the host's watchdog and management-controller access, and one line per checking host with
what it saw and when that was last confirmed.

On the pool status strip above the host rows, **Fencing** reads **OK (N of M)** or
**Not ready (N of M)**; **About** on that strip opens the pool's own line, *Fencing: ready for N of
M hosts*, which names the hosts that are not. The pool master counts it, over the pool's hosts: a
host you ejected is not one of them and is not counted. It is the number to look at before you rely
on a pool surviving the loss of its master. It does not decide anything by itself — the promotion
below decides, by fencing — but a pool master whose controller nobody can reach today is very
unlikely to be fenceable on the day it is lost, which is the whole reason to look.

**These checks decide nothing.** They are shown to you, not acted on. A promotion is decided by the
live fence Nex Core performs and verifies at the moment it promotes, exactly as it was before this
check existed: a host whose checks are stale can still be promoted over, and a host shown as
protected can still fail its fence on the day. The check exists so you find out now rather than
then.

**A pool master that restarts forgets these checks**, and says so rather than repeating what it
last knew: every host reads *no other host has checked… yet* for a few minutes and then goes back
to normal. The same is true of a Nex Controller you rebuild or replace — it asks the hosts, and
what it gets back is what they know now.

### Host fencing, and the watchdog line

A management controller is how one host shuts out *another*. A **watchdog** is how a host shuts out
**itself**: hardware on the machine resets it when Nex Core stops feeding a timer. That is the only
way to shut out a host that is hung rather than merely unreachable — nothing outside it can be
reached to ask.

**Nex Core arms these watchdogs itself**, where the pool's prerequisites hold, and a host that is
armed and loses this pool's quorum **resets itself**. What each host is doing is in its row's
**Details** → **Fencing** entry, under the fence device:

| The watchdog line says | What it means | What to do |
|---|---|---|
| *Watchdog · armed* (as the first line of the **Fencing** entry; the check reads **OK**) | This host is feeding a watchdog that will reset it if it loses the pool's quorum. If a management controller is also confirmed, the first line shows that instead and the watchdog gets its own line. | Nothing. |
| *Watchdog · armed (emulated)* | The same, on a host that is itself a virtual machine. The hypervisor really does reset it, so it counts for that host — but it is evidence about a hypervisor, never about hardware, and Nex Core labels every proof that rests on one. | Nothing, in a laboratory. On real hardware this should never appear. |
| *this host is not running this pool's Pacemaker cluster, so there is no quorum for it to lose* | The pool's cluster is not up on this host yet. A watchdog needs something to tell it that the majority has gone. | Nothing. Nex Core forms the pool's cluster itself within a reconcile pass. |
| *this pool is arming its hosts one at a time and it is `<host>`'s turn* | Arming restarts a host's cluster services, so exactly one host in a pool does it at a time. | Wait. If it does not move on, the named host's own row says what stopped it. |
| *this host is waiting for this pool's cluster to be quorate with every host in it before it arms* | The cluster is up but has not held one unchanged membership long enough. A host that armed into a cluster that was still forming would be inquorate from its first moment and would reset itself with nothing wrong. | Wait about a minute. If it persists, a host is missing from the pool's quorum. |
| *this pool runs clustered storage; watchdog fencing for it is not available yet* | A host has one cluster, and a clustered-storage repository already owns it with a policy that keeps an inquorate host running. Changing that under a mounted clustered filesystem is not something this release does. | Nothing. Such a pool is shut out through its management controllers. |
| *this pool has `<n>` hosts, more than the 5 Nex Core has exercised…* | Bigger than the pool sizes this has been tested on. No host is armed — and none is left out of the pool's quorum either, which would be worse. | Nothing, in this release. |
| *this pool's cluster keeps an inquorate host running instead of letting it reset itself…* | The cluster's own policy is to freeze a host that loses the majority rather than let it die. Measured: on such a cluster an armed watchdog never expires, so arming would promise a protection that does not exist. Nex Core refuses rather than claim it. | Nothing you can fix on the host today. See the limits below. |
| *this host arms its watchdog the next time its cluster starts…* | The cluster on this host is already running, and the watchdog daemon can only join a cluster as it starts. Nex Core will **not** restart a running cluster to arm a watchdog: doing that under a clustered filesystem is how data is lost. | Put the host in **maintenance mode** and restart it when you can. It arms as it comes back. |
| *another program on this host already holds its watchdog device* | Something outside Nex is feeding the timer. Two owners of one watchdog is worse than none. | Find and stop the other program, or leave this host to its management controller. |
| *only the kernel's software watchdog, which a hung kernel cannot run, so it does not count* | The host has `softdog` and no hardware timer. A software watchdog is a timer inside the kernel, and a kernel that has hung does not run it, so it is never proof of anything. **It is still a member of this pool's cluster and still reboots if it loses the pool's quorum** — Pacemaker reboots every host on the minority side, armed or not. What it cannot do is be *proved* excluded, so a promotion will not rest on it. | Turn the chipset watchdog on in the host's firmware, if it has one. Otherwise this host can only be *proved* shut out through its management controller — but it still reboots with the rest of a minority. |
| *Nex Core does not recognise this host's watchdog driver `<driver>`* | There is a device, but Nex Core has not classified that driver, so it counts as nothing. | Nothing you can fix on the host. Report the driver name so it can be classified. |
| *this host reports no watchdog device* | No watchdog is present or no driver is loaded. As above, the host is still one of this pool's cluster's votes and **still reboots if it ends up in the minority**; it simply cannot be proved excluded. | Check the host's firmware settings. |
| *a watchdog needs three or more hosts to have a majority side* | This pool has one or two hosts. A watchdog works by a host noticing it has lost the pool's majority, and two hosts have no majority. | Nothing. This is not a gap: two-host pools are shut out through their management controllers. |
| *this host armed its watchdog under an earlier record of this pool* | A host joined or left since this host last reported. The set of hosts that makes a majority has changed under it, so the old report is not evidence about the pool as it is now. | Nothing. It corrects itself within a reconcile pass. |
| *Host fencing is turned off for this pool* | The **Host fencing** switch is off. | Nothing, unless you want it on. |
| *this host does not report whether it has a watchdog; upgrade it* | That host's Nex Core is too old to look. | Upgrade the host. |
| *this host stops feeding its watchdog the next time its cluster stops…* | You turned host fencing off. The pool already records this host as not armed, so no promotion can rest on it — but the machine is still feeding the timer, and Nex Core will not stop a service under a running cluster to change that. | Put the host in **maintenance mode** and restart it when you can. Until then it still resets itself if it loses this pool's quorum. |
| *this host's watchdog driver was built with nowayout…* | The same, made permanent by the kernel: closing the watchdog device does not stop the timer on this host. | Reboot the host when you can. |

**The Host fencing switch** is under **About** on the pool status strip, and the strip's
**Host fencing** check reads **On** or **Off**. It is **on** for every pool, unless you turn
it off, and turning it on arms nothing by itself: Nex Core arms a host's watchdog only where every
line of the checklist beside the switch holds. The checklist is the pool master's, not this page's:

- **Hosts** — three or more, so that a split pool has a majority side.
- **Hardware watchdogs** — one on every host. It names the hosts without one.
- **Fence devices** — a management controller another host has confirmed, on every host. An armed
  watchdog never satisfies this line: it is about fence devices.
- **Pool quorum** — this pool's Corosync cluster, read from Corosync itself. It reads *this host is
  not running a Corosync cluster for this pool* where there is none, which is honest rather than a
  fault: with no cluster, nothing is armed and nothing resets itself.

**The pool's own cluster.** For a pool that has no clustered storage, Nex Core forms a small
Corosync and Pacemaker cluster of its own across the pool's hosts. Nothing runs on it. It exists so
that every host agrees, continuously, which side of a split is the majority — which is the only thing
a watchdog can act on. You do not create it, name it or configure it: it is the pool's membership,
and it is rebuilt from the pool master by any Nex Controller you install. The line beside the switch
says what it is doing — **forming**, **arming host 2 of 3**, **armed**, **degraded**, or **not
available** with the reason.

Two things about it are worth knowing before you plan work:

- **The pool's hosts are armed one at a time.** Arming a host restarts its cluster services, so the
  pool does one host, waits for it, and then does the next. Nothing that is running on the host is
  affected — this cluster carries no workloads — but the line will say *arming host 2 of 3* for a
  minute or two after you turn the setting on, after a reboot, or after a host joins.
- **The pool's cluster runs on the management network.** Interrupting a host's management network is
  therefore the same thing as partitioning it, and an armed host on the minority side of a partition
  resets itself. Nex Core **refuses a staged management-network change on a host that is armed** and
  tells you so. Put the host in **maintenance mode** first, or turn Host fencing off for the pool.
  The same applies to work you do outside Nex — a switch firmware update, re-cabling, a VLAN change:
  put the hosts in maintenance first.

**If a host is lost while the pool is putting its watchdogs down**, the remaining hosts stay armed
and the pool says so: *armed — this pool's teardown is blocked by `<host>`*. That is not a stall to
wait out silently. Putting a watchdog down means restarting that host's cluster services, and while
one host of three is missing, neither of the other two can do that without taking the other below
quorum — so both keep feeding, and both still reset themselves if they lose this pool's quorum. It
finishes on its own as soon as the missing host is back in the pool's cluster. If it is not coming
back, **eject it from the pool**: that takes it out of the quorum arithmetic and the remaining hosts
finish immediately.

**Maintenance mode is the supported way to do disruptive work.** A host entering maintenance stops
feeding its watchdog **before** it leaves the pool's cluster, and no host leaves while any host is
still feeding one — because a host that leaves early can take a still-armed host below quorum, and
that host would reset. Putting a watchdog down means restarting that host's cluster services, so it
happens one host at a time and takes a minute or two per host; the line beside the Host fencing
switch says which host it is waiting for. A three-host pool that puts one host in maintenance has
two hosts left, which has no majority side, so **the whole pool disarms** and says so; it arms
again, one host at a time, when the host comes back. Wait for the pool to report that no host is
armed before you start work that interrupts a host's network.

**One consequence worth planning around.** In a three-host pool, a *second* failure while one host
is already down takes the remaining hosts with it: two hosts of three are a minority the moment one
of them goes, so both reboot. That is the arithmetic of a majority and not a defect — but it means
**planned work on two hosts of three is not something to do one after the other**. Put the first
host in **maintenance mode** instead: that disarms the whole pool, deliberately, so the second
failure or the second piece of work costs nothing. The pool tells you it has disarmed, and arms
again when the host comes back.

**What arming means.** A host that is armed and loses this pool's quorum **resets itself, and its
running virtual machines stop**. A switch failure that splits a pool therefore **reboots the minority
side**. That is the trade: a host that resets itself is a host that provably cannot still be writing
to shared storage. It is written beside the switch because the switch is on by default, and you
should meet it before it can happen rather than after.

**To turn host fencing off**, clear the checkbox. There is no confirmation to answer, and it stops
nothing that is running. Within a reconcile pass the pool records every host as **not armed**, which
is what stops a promotion ever resting on one. The machines take a little longer: putting a watchdog
down means restarting that host's cluster services, so the pool does it **one host at a time**,
keeping every host in the cluster until the last device is down. Expect a minute or two per host,
and expect the rows to say *this host stops feeding its watchdog the next time its cluster stops*
while they wait their turn — **until a host's turn comes it still resets itself if it loses this
pool's quorum**, which is why the pool does not tear the cluster down underneath them. To turn it
back on, tick it again. The setting is the pool master's, like everything else on this page: it is stored
on the master, copied to every member with the pool's records, and a Nex Controller you rebuild reads
it back from the master. If the switch is missing and the page says the master is **too old to
report the host fencing setting**, upgrade the pool master.

**Promoting over a host that reset itself.** Where a host has no usable management controller but did
arm a watchdog, a promotion excludes it by that watchdog instead. Nobody powers it off: the surviving
host establishes that the host's own timer must already have reset it. That takes time, and Nex Core
**refuses the promotion until it has passed**, saying how much remains — *its watchdog resets it
after 1m0s out of this pool's quorate partition; 34s of that remains*. Wait and confirm again. The
clock restarts from zero if anything makes the answer uncertain: the host reappearing in the pool's
quorum, this pool losing quorum, or the surviving host's own Nex Core restarting — because an agent
that was not running cannot say what happened while it was not looking. A two-host pool never
produces one of these proofs at all.

**Limits you should know about in this release.**

- **No physical watchdog of any model has been tested.** Every measurement behind these timings was
  taken in nested virtual machines with an emulated watchdog. It proves the logic and the timing of
  the engine; it is not evidence about your hardware.
- **Two different things reset a host, and only one of them has been tested on your kind of
  hardware — neither on real hardware.** When a host loses the pool's quorum and is still running,
  Pacemaker itself reboots it, quickly; that is what every partition test measured. The watchdog
  matters for the case Pacemaker cannot handle — a host whose kernel has hung, which is also what a
  promotion's proof assumes. That path was tested by stopping the watchdog daemon on an armed host
  in a laboratory: the machine reset itself within 8 seconds of nothing feeding the timer, with the
  rest of the pool untouched. **But the timer was one a hypervisor emulates. No physical watchdog of
  any model has been tested, and a real kernel hang was not staged.** Treat the watchdog as unproven
  on your hardware until you have seen it, and prefer a management controller where you have one.
- **Pools that run clustered storage are not armed at all.** A host has one Corosync cluster, and a
  clustered-storage repository already owns it with a policy that keeps an inquorate host running
  rather than letting it reset. Changing that underneath a mounted clustered filesystem needs its
  own testing, so this release leaves those pools exactly as they were and says so on every row.
  Nex Core also **refuses to create a clustered-storage repository** on any pool it forms a
  watchdog cluster for — that is every pool of three to five hosts with Host fencing on, whether
  or not its hosts have finished arming. Turn Host fencing off for that pool first; the refusal
  says so.
- **A host on a cluster Nex Core does not own arms at its next cluster start.** Nex Core will not
  restart a cluster that is running anything. Use maintenance mode.
- **Pools of more than five hosts are not armed.** That is the largest pool this has been exercised
  on, not a limit of the underlying software. No host is silently left out of the pool's quorum.

### Set BMC access for a host

This is the fallback, for a host whose Nex Core could not configure the controller itself, and the
row offers it exactly there. Do it **while the pool master is still working**, for every host you
would ever want to promote another host over — in practice, for the pool master itself. A fence
device recorded after the master is lost is impossible: it is part of the membership record the
master keeps. A device you record here is never replaced by automatic provisioning.

1. Open the pool's **Settings** → **Members** section, open **Actions** on the host's row and
   select **Set BMC access…**. The row's note says whether a device is already recorded.
2. Fill in the controller's **address**, the **account** with power control and that account's
   **password**, then select **Detect**. The pool master probes the controller for Redfish and
   IPMI, picks the method that works, and fills in the kind and the **Redfish system identifier**
   or **IPMI plug**, with one line saying why. Account and password are optional for Detect, but
   without them a Redfish controller may not list its systems. If nothing answers, Nex Core's
   sentence is shown in the dialog and nothing is filled in; you can still choose **Redfish** or
   **IPMI** and type the identifier yourself. Detect records no fence profile. The master removes
   its temporary password after the probe, or expires it if the probe never runs.
3. Check the kind and identifier, then select **Set BMC access**. The password is handed to Nex Core over a one-time encrypted delivery
   and is never stored by Nex Controller, never written to its database, never audited, never
   logged and never shown again. The account name is not a secret and is shown on the host's row.
4. Nex Core decides, and for a Redfish device it decides by doing the work now: it connects to the
   controller, records the exact certificate it presents, signs in and asks which machine it
   manages, and compares the answer with what the host itself reported. Only then is the device
   recorded. **A controller that answers for a different machine is refused and nothing is powered
   off**, then or later. Nex Core also refuses a host that does not report a unique hardware
   identity (*this host does not report a unique hardware identity, so a management controller
   cannot be confirmed to be its own*), a device with no account named, a SCSI
   persistent-reservation device (*SCSI persistent-reservation fencing is unsupported*), and any
   device whose credentials have not reached the master. A refusal arrives on the task, in Nex
   Core's own words, and the password you typed is discarded on the master with it.
5. Read the **certificate** fingerprint now shown on the host's row against the one the
   controller's own web page reports. They must be the same. From here on Nex Core refuses to talk
   to anything else at that address.
6. For a laboratory host that runs as a virtual machine, select **This host runs as a virtual
   machine in a lab** in the same dialog and fill in the `virsh` device instead: the **host running
   libvirt**, the **libvirt domain** of this host on it, the **SSH login** and **SSH port**, and the
   **SSH private key** and **SSH known hosts entry** for that login.
7. Before you do this, read
   [What every ready member of a fenced pool can do](#what-every-ready-member-of-a-fenced-pool-can-do).
   Recording a device for the pool master also opts this pool into the restart rule described under
   [Restricted hosts](#restricted-hosts).

To remove a device, select **Clear fence device…** in the same dialog. Nex Core retires the stored
material on whichever host is master, and never removes material another recorded device still
names. For a device Nex Core configured itself, clearing it also removes the `nexfence` account from
that host's controller and stops it configuring the controller again, as above.

**Replacing a motherboard.** A host's machine identity comes from its board, so a replacement board
reports a new one. While a management controller is recorded for a host, Nex Core will not let that
host change the identity its fence device depends on — otherwise a faulty or compromised host could
make itself permanently un-fenceable simply by reporting something else. Clear the fence device,
let the host report itself once, then record the new controller's access. You have to record it
again in any case: the new board's controller has its own certificate and its own accounts.

### Promote a surviving member

This is the recovery path for a pool master that is **gone**, not a tidier way to move the
authority. If the old master is still working, close the dialog and use
[Change the pool master](#change-the-pool-master) instead: it hands the authority over and powers
nothing off. Nex Core enforces that — it refuses a promotion whose old master answers and still
holds the pool's authority.

1. Open the pool's **Settings** → **Members** section. When the pool master is not reporting, the
   section opens
   with a plain sentence saying so and giving the time it was last heard from. Its virtual machines
   have **not** necessarily stopped; an unreachable host is unknown, never proven down.
   If Nex Controller was replaced after the master was lost, enroll the surviving hosts first. The
   section then says the pool master *has never reported to this Controller* and lists the hosts as
   the survivors last recorded them, and a surviving member can be promoted as below.
2. Choose the surviving member to promote. Read its **Authority copy** check and details: a
   member with no copy, or an unverified one, cannot be promoted, and Nex Core says so rather
   than guessing.
3. Open **Actions** on that member's row and select **Promote to pool master…**.
4. **Step 1 — what Nex Core reports.** The dialog asks that host to check the pool and lists what
   it found. The answer comes on the host's next report, so it can take up to a minute; **Check
   again** repeats it. Nothing is recorded, nothing is queued, and nothing is powered off by this
   step. The checks are:
   - **Replica integrity** — this host holds a copy of the pool records that verifies against the
     pool's own certificate authority and is addressed to it. Nothing substitutes for this; a fence
     never does.
   - **Replica supremacy** — no host this one could reach holds a newer pool authority. If one
     does, it is named as the host to promote instead. Two more things stop the promotion here.
     A host that **answers and is already running a promotion of its own** blocks it — let that one
     finish or cancel it, rather than starting a second. And a ready member that **does not answer**
     blocks it: it holds a copy of the pool's records, so it could be promoted in exactly the same
     way, and two partitioned survivors would otherwise both become master. Such a host is not a
     dead end — it is added to the list of hosts this promotion powers off, and the promotion
     proceeds once you confirm that. If it has neither a usable fence device nor an armed watchdog
     of its own, the promotion is refused until you bring that host back or give it one.
   - **Accepted ownership** — every pool virtual machine is recorded the same way by the host that
     actually owns it. Where the two disagree, the virtual machine is named and the promotion
     stops until it is resolved. A host that cannot read its own record blocks it too: ambiguity
     blocks.
   - **Old master exclusion** — every host that blocks this promotion has been powered off through
     its recorded fence device and Nex Core has observed it. Before a promotion runs this is
     normally **unknown**, because nothing has been fenced yet. *Unknown is not "fine"* — it blocks
     a promotion exactly as a refusal does; it is a separate word only so you can tell a refusal
     from an outage.
   Each host that was probed is listed with whether it answered, whether it blocks the promotion,
   whether a fence device recorded for it could be used and **which method would shut it out**;
   each affected virtual machine is listed with why. Where the promoted host's own last
   non-destructive check of that fence device was failing or stale, it says so under that host.
   **That warning is advisory.** It is not a check and it stops nothing: Nex Core proves the fence
   itself when the promotion runs, and its own proof is what decides. It is there so that a fence
   device already known to be unreachable is not a surprise at the worst moment.
   **A promotion can power off more than one host.** The list it names is the old pool master plus
   every other host that blocks the promotion and can be excluded by a fence — an unreachable
   member that could itself have been promoted is exactly such a host, because leaving it running
   while another host takes the authority is how two masters happen. Read that list before you
   confirm: it is what you are agreeing to, it is sent back with the request, and Nex Core checks
   it again against what it computes at the moment it acts. If the pool changed in between, the
   promotion stops and says so rather than powering off something you were not shown. A host that
   blocks the promotion and has **no usable fence device and no armed watchdog of its own** cannot
   be excluded at all, and the promotion is refused until you resolve it another way.
5. **Step 2 — confirm the promotion.** It is offered only when every other check has passed, the
   exclusion is outstanding only because nothing has been fenced yet, and **every** host on the
   list can be shut out — the promoted host holds a usable fence device and its credentials for it,
   or that host armed its own watchdog and its waiting period has passed. Read it: Nex Core on the
   promoted host **powers off** every host on the list that has a fence device, and verifies that
   each is off, before it takes the authority; a host excluded by its own watchdog is not powered
   off at all, because its own timer has already reset it. Virtual machines on the hosts that
   survive are not restarted and keep running.
6. Type the **pool name** exactly as shown, tick **I understand this powers off:** — which names
   **every** host on the list, not only the old master — and select **Promote to pool master**.
   Both are required, and neither is evidence of anything: Nex Core produces and checks a fence
   proof for each named host itself, and nothing you type reaches any of those proofs. If the list
   changes when you re-run the check, the acknowledgement is dropped and you confirm the new list.
7. Watch the **Pool master promotion** strip. The promoted host runs the whole promotion itself and
   reports its own stage. Closing the page, or losing Nex Controller entirely, changes nothing. A
   status of **needs attention** means durable and still running, not failed.
8. A promotion **cannot be cancelled** at any stage. It has powered a host off; it can only go
   forward.
9. When it finishes, the master row moves to the promoted host from the hosts' own reports. The
   pool keeps its ID and its name, and every network, storage registration, ISO entry and virtual
   machine keeps its identity and its name.

#### When the old master comes back

It comes back as an ordinary member of the new master's membership, and it comes back
**restricted**: it starts nothing and changes nothing about the pool until it has re-pointed at the
new master, taken a verified copy of the pool records and reconciled. It cannot know by itself that
it was fenced — that evidence lives on the other hosts — so it refuses first and asks afterwards.
Any virtual machine it still defines that the pool now records elsewhere is named on its row, kept
defined, fenced off, and never started and never deleted there. Do not rebuild it, re-enrol it or
hand-edit it to hurry this along.

### Restricted hosts

A **Restricted** badge on a host's row, with the reason under it, means Nex Core on that host is
refusing to act on pool authority. While it is restricted the host **starts no virtual machine**,
autostarts nothing and makes no change to the pool record. Stopping a virtual machine, its console,
local recovery access at the host itself and the host's own reporting all keep working, and the
refusal holds with no Nex Controller connected at all — it is enforced by Nex Core, including by
the hypervisor hook, not by the interface.

There are two kinds, and the row says which:

- **Superseded** — this host found a higher pool authority elsewhere, which is what a fenced old
  master finds when it returns. *It clears by itself* once the host has rejoined the current pool
  master, taken its copy of the pool records and reconciled with it. A conflicting virtual machine
  named on the row keeps it restricted until the two agree where that machine lives.
- **Unconfirmed** — this host is a pool master that has restarted and cannot yet prove it is still
  the master. A master that was restarted might have been fenced and promoted away while it was
  off, and the evidence lives only on the other hosts, so it refuses first. *It clears by itself*
  once **every** other ready member answers and names this host as its master.

A restarted master only restricts itself when the pool actually opted into fence-verified promotion
— that is, when a usable fence device is, or recently was, recorded for it, **or** when it has
armed this pool's watchdog, which makes it excludable in exactly the same way. A pool with neither
restarts its master normally and keeps starting virtual machines, even with a member down.

For a fence-exposed multi-member master, Core withholds managed-VM autostart until this boot has
confirmed that host's authority. If the confirmation is missing or stale, the host fails closed.

When the restriction clears, Core restores the VM's requested libvirt autostart setting and then
starts, once for that boot, each virtual machine whose definition asks for autostart and which is
still shut off. This is what brings a machine on the host's **local** storage back after a reboot:
libvirt tries its own flag once, too early, while the host is still refusing, and cannot try again.
It is not a retry of anything else — a machine an operator started by hand and that was refused
earlier in the boot is not started for them, and a machine that was stopped deliberately stays
stopped. Every boot makes its own authority confirmation first, so a host that cannot confirm still
starts nothing.

#### A restricted master with a member that will never come back

This is a real limit, stated plainly because there is no way around it in the product.

If a pool opted into fence-verified promotion, its master restarts, and one other ready member is
**permanently** dead, the master stays restricted. It cannot start shared virtual machines and
cannot change the pool record until that member answers. There is no escape action — no force, no
override, no "declare this host lost", no timeout — and none will be added, because none of them
would be evidence. A point-in-time observation that the dead host is off does not prove it never
took the authority, and it does not survive that host being powered on again; accepting it would
let two hosts both believe they are master.

What to do: **bring the dead host back**, or **restore its Nex Core state from a backup** so it can
answer. Nothing else clears the restriction, and waiting does not clear it.

### Cancel a master change or an eject

Selecting **Cancel this master change** submits a cancellation. Nex Core decides whether it is
still possible, and it reports that decision itself rather than leaving you to guess from the
stage: the button is offered exactly while Nex Core says the transfer can still be cancelled.

A cancellation is accepted only **before the transfer's first irreversible step**, and never after:

- A **master change** can be cancelled while the authority is frozen, while the copy is being sent,
  and during the hand-off itself — where it succeeds only if the new master proves, from its own
  durable record, that it never took the authority. Once it has, the cancellation is refused with
  *the target has already activated this pool authority; the master change can only go forward* and
  the change completes.
- An **eject** can be cancelled until the membership without that host has been written. After
  that it is refused with *host `<id>` is already removed from this pool's membership; the eject can
  only go forward*, and the eject finishes: the remaining members are re-pointed and the ejected
  host is told to leave. A cancellation never puts the host back in the pool — rejoining it is a
  new [join](join-a-pool.md), not an undo.
- A **promotion** can never be cancelled, at any stage: *this pool promotion has powered the old
  pool master off; it cannot be cancelled and can only go forward.*

#### The master change handed over and the new master died

Once the authority has been signed over, a cancellation needs the new master to agree — and if that
host is gone, it cannot. Until something proves it is off, the pool's authority stays with it, and
the pool has no master that will act. The strip then offers **Resume on the current master…** on the
outgoing master's row. It is the same cancel step carrying one extra statement: that you agree to
**power the hand-off target off**. Type the pool name and tick the acknowledgement naming that host.

Nex Core refuses it unless all of this holds: the target really does not answer when it probes it;
you named exactly that host; this host is still the recorded master, still holds the pool's
certificate authority key and is not itself restricted; the target has a recorded fence device; and
Nex Core's own fence powers it off and verifies it. A host that was itself fenced and rebooted is
restricted, and is refused here — which is what stops a fenced host taking the pool back from the
host that fenced it. If the target is reachable after all, Nex Core says so and tells you to retry
the ordinary cancellation instead of powering anything off.

When it succeeds, this host is master again and reports *the target of this master change was
powered off and could not take the pool's authority; this host resumed it*. That is an **outcome,
not a failure**: the cancellation did what you asked, and it is shown as a completed cancellation
rather than a failed transfer. If the target later comes back, it comes back restricted and finds a
higher authority elsewhere, so it writes nothing.

Nothing is ever cancelled by a timeout, and a cancellation is never offered as a way to undo a
finished change. A cancelled master change is recorded as *the pool master change was cancelled by
an operator*; a cancelled eject as *the pool member eject was cancelled by an operator*.

## Expected result

- Every fresh member of a healthy pool settles at **current** within about one reporting interval
  of a change to the pool's configuration. The master rebuilds and re-sends its authority only
  when its content has actually changed, so a quiet pool stays at one number.
- A member that was unreachable catches up by itself when it returns; nothing has to be re-sent by
  hand.
- A member that has never acknowledged anything is reported at copy `0` — honestly, not as an
  assumption that it is up to date.
- Losing Nex Controller changes none of this. The copy is pushed by the pool master to its members
  over their own connection; Controller only reads what the two hosts report.

After a **master change**:

- The selected host is shown as the pool master, and the previous master as an ordinary member.
  Neither row had to be edited: both hosts reported it.
- The pool keeps its ID and its name. Every network, storage registration, ISO library entry and
  virtual machine keeps its identity and its name, and virtual machines that were running never
  stopped. Nothing has to be re-entered anywhere.
- **Authority generation** on the pool status strip is one higher than before.
- Pool changes that were refused while the change ran can simply be submitted again. They were
  refused, not queued, so nothing runs by itself afterwards.
- Losing or replacing Nex Controller during the change makes no difference. The two hosts finish it
  between themselves, and a fresh Controller shows the result once they report.

After an **eject**:

- The host is gone from this pool's **Members** section and appears as its own single-host pool, in the
  same site, with the same host name and the same host identity. Its local virtual machines and
  local storage are still its own.
- Its access to this pool's shared networks, shared storage and ISO library is gone.
- Rejoining it later is a new [join](join-a-pool.md), with everything a join requires.
  Its historical `ejected` membership does not prevent that new invitation; an active membership
  still does.

After a **promotion**:

- The promoted host is the pool master and the pool authority generation is one higher. The pool
  keeps its ID and its name, and every network, storage registration, ISO library entry and virtual
  machine keeps its identity and its name. Virtual machines that were running on the surviving
  hosts never stopped and were not restarted.
- The old pool master is off. Nex Core powered it off and verified that before it took the
  authority, and no other evidence would have done.
- When the old master returns it is an ordinary member carrying a **Restricted** badge until it has
  reconciled. See [When the old master comes back](#when-the-old-master-comes-back).
- Any virtual machine the old master still defines that the pool now records elsewhere stays
  defined there, fenced off, and is never started twice and never deleted.
- A fence device recorded for the promoted host itself still applies: the new master inherits every
  recorded device and re-seals their material for the members that remain.

## Troubleshooting

- **Every member shows *unknown*.** The pool master is not fresh. A copy that was current an hour
  ago is not current now, so nothing is claimed. Reconnect the master.
- **One member shows *unknown*.** That member is not fresh. The master's acknowledgement is its
  last observation, not present truth, so the pair cannot be judged.
- **A member stays *behind*.** Check which of the two numbers is lower: the row says whether the
  master's record of that host is the older one, or the host has not caught up with what the
  master recorded. A member that is not receiving copies usually cannot be reached by the master
  on its pool peer address; check that address and the network between the two hosts. The pool
  master's own **Authority generation**, on the pool status strip, is the absolute number to
  compare against; a member can also be shown as behind because another fresh member of the same pool is
  acknowledged at a higher number.
- **A member shows *refused*.** Read the reason on the row. A copy that names another pool, is not
  from the current master, is addressed to another host, is not signed by the pool's certificate
  authority, is incomplete, or is an older number than what the member already holds is refused
  rather than stored — the member keeps what it had. Persistent refusals mean the two hosts
  disagree about the pool they are in; do not try to fix that by rejoining a production host.
- **A member shows *unverified*.** It holds bytes it could not prove. Treat it as holding nothing
  and investigate the member's own pool certificate state.
- **"Not supported — upgrade".** The master records such a member at copy `0` with an
  upgrade-required reason. Upgrade that host.
- **An SMB repository is registered in the pool.** The stored credentials are not part of the
  copy, by design. Keep the credentials for any SMB repository recorded outside the pool, because
  a copy alone would not restore access to them.
- **If the pool master is lost.** There is still **no automatic succession**: nothing promotes a
  member for you, and nothing ever will without you asking. What exists is
  [Promote a surviving member](#promote-a-surviving-member), which you start by hand and which only
  works if a fence device was recorded for the master **before** it was lost, or if the master had
  **armed its own watchdog** before it was lost — in which case the promotion waits out that
  watchdog's period instead of powering anything off. If neither was in place, the pool keeps
  running the virtual machines it has and has no master until the master comes back. Do not attempt
  to hand-edit a member into a master.

### A master change or an eject is refused

The reason is printed beside the disabled button, or returned by Nex Core on the **Pool master
change** panel in its own words. What each one means:

- **"This host is already the pool master."** Pick another row.
- **"The pool master cannot be ejected; change the pool master first."** Move the authority to
  another host, then eject this one.
- **"The pool master is … , so this membership is not current; reload when it reports again."** The
  master is not answering. Nothing is offered against a membership nobody is currently confirming.
- **"Only a ready member with a peer endpoint can become the pool master."** The host is not a
  finished member of this pool, or has never advertised its pool peer address. Finish or repair its
  join first.
- **"The selected host must be fresh and active to become the pool master."** It is not reporting
  now. A host that cannot be reached cannot take the authority — but it *can* be ejected.
- **"… needs `pool-authority-transfer-v1` on …; upgrade `<host>`."** Upgrade the named host. Every
  ready member needs it, because a member that cannot be re-pointed would keep following a host that
  is no longer the pool master.
- **"Another pool authority transfer is already in progress on this pool."** Wait for the one on the
  panel to finish, or cancel it.
- **"The pool master reports membership generation N; reload the pool and retry."** The membership
  moved under you. Select **Refresh** in the **Members** section and look again before retrying — that is the point of the message.
- **"Pool authority is frozen for a master change; retry when it completes."** This is the one
  message you retry **unchanged** once the change finishes. Nothing was queued.
- **"Host `<id>` still owns pool VM records (…); move or delete them before ejecting it."** Move
  those virtual machines to another host of the pool, or delete them, then eject.
- **The target VM inventory is missing or not current.** Restore the target's Core reporting and
  wait for its current inventory. Do not eject on the assumption that an absent record is local.
- **"Host `<id>` still holds a pool-shared storage reservation on `<repository>`; let it finish
  before ejecting it."** Something on that host is still using pool-shared storage. Let it finish;
  do not force it.
- **"Host `<id>` has local VMs that depend on resources leaving with the pool (...); detach them
  from the pool network or ISO before ejecting it."** Edit the named VM to remove that pool VLAN
  or eject that pool ISO from its CD drive, then eject the host.
- **"The target has already activated this pool authority; the master change can only go forward."**
  The cancellation came too late. The change completes; there is nothing to repair.
- **"Host `<id>` is already removed from this pool's membership; the eject can only go forward."**
  The cancellation came too late. This says the master removed membership; it does not prove the
  ejected host has released its pool resources. Follow that host's own transfer row: `leave` means
  it is still working, and `needs_attention` names a local VM or other blocker. Once the cause is
  cleared, the host resumes and finishes by itself, with no master connection or manual state edit.
- **"Pool authority was handed to another host; this host is no longer the pool master."** You
  addressed a host that has already given the authority away. Select **Refresh** in the
  **Members** section.

### A promotion or a fence device is refused

- **"The pool master `<id>` is reachable and holds this pool's authority; change the pool master
  with a planned master change instead of promoting."** The old master is alive. Use
  [Change the pool master](#change-the-pool-master); nothing needs to be powered off.
- **"The pool master `<id>` has no recorded fence profile; record one before promoting."**
  Nothing can be promoted over that
  host. A fence device can only be recorded while the master is reporting, so for this pool, now,
  there is no promotion path. Bring the master back.
- **"This host holds no fence credential for `<host>`; it cannot fence it, so it cannot be promoted
  over it."** A device is recorded but this member never received its material — usually because
  the member joined, or the device was recorded, after the last copy went out and the master was
  lost before the next one. Try another ready member; if none holds it, there is no promotion path.
- **"This fence profile cannot be verified by this host: this host does not report a unique
  hardware identity, so a management controller cannot be confirmed to be its own."** Nex Core will
  not take an address on trust. Usually the host has not reported itself yet — it does so on its
  own, shortly after it starts — so wait a moment and record the controller again. A host running a
  Nex Core too old to report one needs upgrading first. If it persists, the host's firmware is
  reporting a placeholder identity, or two hosts of this pool report the same one; neither names a
  single machine, and neither can be fenced by a management controller. The same refusal shape
  carries *a management controller fence device needs the account Nex Core signs in with*, *virsh
  fencing needs an immutable domain UUID target* and *SCSI persistent-reservation fencing is
  unsupported*; see [Prerequisites](#prerequisites).
- **"This management controller presented a different certificate than the one recorded for it;
  record its access again after confirming the new certificate on the controller itself."** The
  controller answered, and it is not the one this device was recorded against. Either it was
  replaced or reset and regenerated its certificate, or something else is answering at that
  address. Open the controller's own page, confirm the certificate it reports, and record its
  access again. Nothing was powered off.
- **"This host reports the same hardware identity as `<host>`."** Two hosts report the same SMBIOS
  system UUID, so neither of them can be given a management controller: each would satisfy the
  other's identity check, and a mistyped address between them would power off the wrong machine.
  The row names the host that claims the identity. If it is not a host of yours, eject it; if it is,
  its firmware reports a placeholder and its vendor has to fix that.
- **"The management controller at this address reports a different machine than the host it is
  recorded for; nothing was powered off."** The address points at another machine — a reused
  address, a rebuilt rack, a mistyped digit. Nothing was fenced and nothing was promoted. Correct
  the address and record it again.
- **"This management controller did not answer"** / **"… rejected the recorded account"** / **"…
  did not report the machine it manages."** The controller is unreachable, the account or password
  is wrong, or it does not report its machine identity. Nothing was powered off. Fix the controller
  or the account and record it again.
- **"This Nex Core has no ipmitool, so it cannot ask an IPMI controller which machine it manages."**
  This host is older than the Nex Core that ships `ipmitool`. Use a Redfish device on it, or
  upgrade it.
- **"This host already has a fence device an operator set, so the one it configured for itself was
  not recorded."** What you typed wins, deliberately. Clear the device from the dialog first if you
  want Nex Core to configure the controller itself instead.
- **"An account named `nexfence` already exists on this host's management controller and Nex Core
  did not create it."** Nothing was changed on the controller. Remove or rename that account, or
  set the controller's access by hand.
- **"This host holds no fence credential for `<host>`; deliver it to this host before recording the
  profile."** The credentials did not reach the pool master. Record the device again, with its key
  and known-hosts entry, in one go.
- **"Pool host `<ids>` holds a newer pool authority than this host; promote it instead."** Promote
  the host it names.
- **"Pool VM `<ids>` is recorded differently by the host that owns it; resolve that before
  promoting."** The pool's record and the owning host disagree about a virtual machine. The dialog
  names each one and what the disagreement is. Resolve it on the owning host; a promotion never
  guesses which side is right.
- **"This host holds no pool authority replica; it cannot be promoted"** / **"This host's pool
  authority replica was never verified."** Pick a member whose **Authority copy** is a verified
  copy.
- **"The old pool master was not excluded: …"** / **"This promotion has no fence proof for the old
  pool master"** / **"This fence proof excludes another host"** / **"This fence proof belongs to
  another promotion"** / **"This fence proof is older than 15m0s; fence the old pool master
  again."** The fence did not produce proof Nex Core accepts. Fix what it named about the fence
  device and submit again; a proof is bound to one pool, one excluded host, one promotion and one
  authority generation, and expires, precisely so a stale or borrowed one cannot be presented.
- **"Only the pool master records a fence profile"** / **"An ejected host has no fence profile"** /
  **"No such member of this pool."** Record the device against a current member, from the pool
  whose master is reporting.
- **"This promotion will power off other hosts; confirm the hosts this pool reports before it can
  run."** The request reached Nex Core without the list of hosts you were shown. Re-open the
  promotion, read the list again and confirm it.
- **"This promotion must power off `<hosts>`, which is not what was confirmed; confirm the hosts
  this pool reports and try again."** The pool changed between the check and the act — a host came
  back, or another one stopped answering — so the set Nex Core would now power off is not the set
  you agreed to. It stops rather than powering off something you were not shown. Check the pool
  again and submit a new promotion.
- **"This promotion no longer needs to power off any host; review the pool and try again."** Every
  host that was blocking is now reachable and agrees. Nothing needs fencing, and quite possibly
  nothing needs promoting either: look at the pool before you act.
- **"Only a ready member of this pool can be promoted"** / **"The promotion candidate has not
  reported its pool membership; it cannot be promoted yet."** The host is not a finished member, or
  has not reported since it started. Wait for its next report, or pick another member.

### Two hosts claim to be the pool master

The panel shows a banner naming the hosts that report being this pool's master at no newer
generation than the host Nex Controller still treats as the master. Nothing is changed while they
disagree: Controller keeps the recorded master and shows the conflict rather than flapping between
two claims. Check both hosts before you act — a host that was isolated during a master change is the
usual cause, and it resolves itself once it can reach a host of the pool and receives a signed
statement at a higher generation.

### A host does not know it was ejected

That is expected while it cannot reach any host of the pool. It keeps running its own virtual
machines. As soon as it can reach one, it learns that its certificate is no longer admitted and
re-initializes itself as its own single-host pool. Do not re-enrol or rebuild it to hurry this up.

### The change is interrupted

Closing the browser, losing Nex Controller, or replacing it entirely changes nothing: the pool
master records the change durably before anything happens and the hosts carry it through by
themselves, resuming after a restart of either one. A status of **needs attention** on the panel
means exactly that — durable and still running — not that it failed. Nothing is ever abandoned on a
timeout, and there is never a moment when two hosts both accept pool authority writes.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

- **Partial native evidence; walkthrough remains open.** Retained disposable-lab runs now include
  scoped master-change interruption recovery: one source cycle at the durable activate-before-
  handoff boundary and one target cycle after durable master persistence. Both transfers converged
  with successful Controller operations. They do not establish VM-workload continuity, foreign-
  owner safety, all interruption stages or a returning-master case. A separate G4 browser session
  completed the typed eject confirmation, observed a terminal successful eject and the ejected
  host's new solo pool. A separate manual-promotion browser walkthrough exercised the typed pool
  confirmation and explicit fence acknowledgement, retained one queued operation through terminal
  success, and observed the requested candidate as fresh authoritative master at 1280 px and
  360 px. It does not establish returned-master recovery, resource reconciliation or all failure
  paths. Browser review remains outstanding for the **Authority copy** check and stale state,
  **Pool master change** panel, recovery view, conflict banner, **Restricted** badge and other
  confirmation dialogs.
- **Fence evidence is still partial.** Reviewed disposable-lab evidence includes a scoped fence
  exclusion, externally observed power-off and ordinary rejoin. The f714 boot-window result also
  refused API and installed-hook starts and autostart before authority publication, then allowed a
  normal API start and stop after rejoin. It does not establish contradictory-owner recovery,
  duplicate-writer safety, every start path, physical fencing or permanently-dead-member recovery.
  The maintenance walkthrough's reboot/autostart case remains separate and open. See the
  [current native-acceptance record](../qa/2026-09-19-phase4-native-acceptance.md).
- **Automatic fencing: no hardware evidence at all, and the browser review used mocked replies.**
  See the [automatic host fencing record](../qa/2026-09-22-automatic-host-fencing.md). Exercised in
  a headless browser against fixture data, and the captures inspected: the **Fencing** column in
  every state it can report (ready and checked from N hosts, stale, unreachable, no device,
  withdrawn, not checked yet, a master too old to report, and a single-host pool), the pool's
  *ready for N of M hosts* line, each host's **watchdog** line (not armed, armed, armed on an
  emulated device, software-only, unrecognised driver, no device, two-host pool, host fencing
  turned off, waiting for its cluster to stop), the **Host fencing** switch with its prerequisite
  checklist and the partition-reboot consequence beside it, and the promotion verdict's advisory
  lines with an ok, a failing and a stale proving host. Not exercised in a browser at all: the
  pool cluster's own states (forming, arming host N of M, degraded, teardown blocked). What runs
  behind those screens was proven only by Go and React tests and by **nested** virtual-machine labs
  with an *emulated* watchdog: **no physical management controller has been contacted, no host has
  been powered off, and no physical watchdog has been read, armed or expired.** Every promotion and
  every watchdog proof is test-only; no promotion and no proof ran in a lab, and the documented
  steps on this page for the fencing workflows have not been walked through against a running pool.
  Every timing quoted on this page comes from those nested runs.
- Covered by Go `-race` unit tests on Nex Core: signing and verification against the pool
  certificate authority, per-member sealing including failure with another member's key and at
  another copy number, refusal of tampered, truncated, re-addressed and out-of-order copies with
  the stored copy proven unchanged, the copy-number rules, crash-before-send persistence,
  idempotent re-sending, a lagging and an unreachable member catching up, proof that a copy grants
  no execution, both hosts' reported state, both health checks, and the size bound on the wire and
  on disk. Controller tests cover every state word, both disagreement directions, stale master and
  stale member, a missing capability on each side, and that reading the whole projection writes
  nothing to the Controller database.
- The master change and the eject are covered by Go `-race` tests that run several hosts **in one
  process** over the real mutual-TLS pool peer transport, with real certificates and real
  signatures: a three-host master change with the virtual machine identities proven byte-identical
  afterwards and placement, start permits, maintenance and the authority copy all working again;
  refusal of every pool authority write while frozen, with stop still permitted; interruption at
  each stage on each side, each converging on exactly **one** master; a host that handed the
  authority over refusing authority after a restart; an unreachable member catching up through the
  old master and then the new one; refusal of stale and unsigned statements; the full cancellation
  rule including the durable never-activate record; an eject with re-initialization as a new pool;
  both eject refusals; ejecting an unreachable member and its learning on return; and concurrent
  authority writes during a freeze, all refused. Controller tests cover the three routes, the
  refusal matrix, the capability gate, the projections, adoption of the new master and of an
  ejected host's new pool, and a Controller reopened from the same database mid-change. The
  interface helpers are covered by component tests.
- The promotion and the restriction are covered by Go `-race` tests that run several hosts in one
  process over the same real transport, with the fence itself replaced by a test double: the whole
  verdict matrix including a missing copy, a tampered copy, a reachable healthy old master, a
  member at a higher generation, a disagreeing owner and everything-but-the-fence passing (reported
  **unknown**, never **passed**); six separate refusals of things that are not proof — an
  acknowledged-only fence, a stale one, and ones bound to another operation, host, generation or
  digest; the destructive acknowledgement being mandatory; full promotions on a two-host and a
  three-host pool with virtual machine records proven byte-identical and certificates, peer
  addresses and the pool ID unchanged; interruption at every stage resuming to exactly one master;
  the restart rule with and without an unreachable member, and a single-host pool never restricted;
  a returning old master refusing starts, definitions and authority numbering while still allowing
  stops, and clearing only after it has reconciled; an ownership conflict keeping it restricted
  with the virtual machine named and its definition intact; the sealed fence material reaching only
  the member it is addressed to, refused for the wrong member, the wrong generation and tampered
  ciphertext, retired when the device is cleared, and proven absent from every log, result, verdict
  and reported status; and the deadlock of a restricted master with a permanently dead member
  pinned as a test, because it is a limit and not an accident. Controller tests cover the three
  routes, their refusal matrix, the credential never reaching the database, the audit trail or the
  response, the projections with a stale master, adoption of a promoted master, and a Controller
  reopened from the same database mid-promotion.
- **Found by an independent review of this work before release, and fixed with regressions that
  were observed failing first:** two partitioned survivors could both be promoted over the same
  dead master, producing two pool masters (now blocked, and the unreachable member is fenced by the
  same promotion); a promotion could have powered off a reachable, healthy master; two hosts could
  run concurrent promotions of the same pool; a pool master change handed to a host that then died
  killed a two-host pool permanently (now recoverable through **Resume on the current master…**);
  and the pool's virtual-machine owner records could still move while the authority was frozen for
  a master change. Each is pinned by a test named in the [bug ledger](../BUG_LEDGER.md).
- **A property of the design, not a defect, and worth knowing:** any ready member of a pool can
  open the fence material addressed to it, so **any ready member can power off any host that has a
  fence device recorded**. That is the same trust model as the sealed pool certificate authority
  key, it is inherent to fence-verified promotion, and it is policy rather than a lock. Admit only
  hosts you trust with it, and do not record a fence device for a host that does not need one.
- **A ceiling with no operator lever:** the pool's authority copy is bounded at 1 MiB. A pool whose
  records outgrow that reports the copy check as failed and can no longer be promoted; there is
  nothing in the interface to raise it.
- Automated tests and the reviewed 1280 px/360 px captures are not a complete walkthrough.
  Browser review remains open for the authority-copy check and stale state, transfer/recovery and
  conflict views, the restricted badge and the remaining confirmation states, as does any behaviour
  on real hardware.
- Offline contract checks in `tests/test_api_contract.py` hold both peer routes, the three
  Controller routes, their literal refusals, the payloads, the reported shapes and the state ladder
  to the implementation; see the
  [API reference](../API.md#member-authority-replica-peer-getpost-apiv1poolreplica),
  [Planned master change and member eject](../API.md#planned-master-change-and-member-eject-post-apiv1poolsidmaster-delete-apiv1poolsidmembershost_id)
  and
  [Fence-verified manual promotion and safe rejoin](../API.md#fence-verified-manual-promotion-and-safe-rejoin-post-apiv1poolsidmasterpromote-post-apiv1poolsidmembershost_idfence-profile).

[Back to the user guide](README.md)
