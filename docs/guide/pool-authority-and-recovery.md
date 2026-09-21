# Pool authority and recovery

## Status

Development product 0.4.1. This page covers what a pool member holds as a copy of the pool
master's authority and how to read that copy's state (pool detail → **Hosts** → the **Authority
copy** column), how to **change which host is the pool master**, how to **eject a host from a
pool**, how to **record a fence device**, how to **promote a surviving member when the pool master
is lost**, and what a **restricted** host is.

What exists today:

- **A planned master change, against a master that is still working.** You choose the member; the
  current pool master hands the authority over and every other host is told. See
  [Change the pool master](#change-the-pool-master).
- **Ejecting a member**, including one that is not reachable. See
  [Eject a host from a pool](#eject-a-host-from-a-pool).
- **The authority copy and its reporting**, which is what makes a master change safe: the member
  that becomes master activates the copy it already holds and has verified.
- **A fence-verified promotion of a surviving member when the pool master is gone**, which you
  start by hand and Nex Core proves by powering the old master off itself. It only works in a pool
  that recorded a **fence device** in advance, and in this build only a `virsh` fence device can be
  recorded, so only a disposable laboratory pool can be promoted this way. See
  [Record a fence device](#record-a-fence-device) and
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
  device recorded before the master was lost. Do not plan around anything happening by itself.
- **There is no force, no override and no "declare this host lost".** No button, no field and no
  waiting period substitutes for evidence, and none will be added. Where the product cannot prove
  something it says so and stops.
- **A member never acts on its copy by itself.** It stores it and reports that it holds it. No
  membership, virtual machine, storage, network or ISO record on a member is read from or changed by
  the copy, and holding a copy grants a member no permission to run anything. Only a master change
  or a promotion you asked for opens it.
- The copy is not health evidence. A host holding a current copy is not thereby a healthy host.

### What every ready member of a fenced pool can do

Read this before you record a fence device, and before you admit a host to such a pool.

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

## Prerequisites

- An administrator session in Nex Controller, and the pool open at its **Hosts** tab.
- A pool with at least one member besides the master. A single-host pool has no member to hold a
  copy, and the column is not shown when the master does not support the copy at all.
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
- A `virsh` fence device — an SSH login on a host running libvirt that can power off the target
  host's domain — and its SSH private key and known-hosts entry. Nex Core refuses a Redfish, IPMI
  or SCSI persistent-reservation device at the moment you record it, because this build cannot
  verify the identity of what it powered off through one. **A physical pool therefore cannot be
  fence-promoted in this build.**
- **To promote**: the surviving member fresh and active, a `ready` member of this pool, advertising
  `pool-authority-transfer-v1`, holding a verified copy of the pool records, and holding a fence
  device for the old master it can use. The pool master is deliberately **not** required to be
  reachable, and whether Nex Controller can reach it is never evidence either way: Nex Core probes
  it over the pool network itself and refuses the promotion if it answers and still holds the
  authority.

## Steps

### Read the authority copy

1. Open the pool and select the **Hosts** tab.
2. Read the **Authority copy** column for each member. The pool master's own row has no value:
   the master holds the authority, not a copy of it.
3. Read the state word:
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
5. Read the **Authority copy** entry in the same row's **Convergence** column. That is each host's
   own health judgement over its last two reporting intervals, and it can briefly disagree with
   the state word, which is a comparison of two reports. Both are shown on purpose; neither is
   reconciled into the other.
6. Read the sentence under the table: *The pool authority is at generation N.* That is the pool
   master's own number, reported by the master and by nobody else, and it is what each member's
   copy is measured against. When the master is not fresh the sentence instead says that the master
   is not reporting one, and that how far behind a copy is cannot be said. A member whose copy is
   older also shows *N generations behind* in its **Authority copy** cell.

### Change the pool master

Do this with the current pool master still working. It is a planned hand-over, not a recovery.

1. Open the pool, select the **Hosts** tab and check that the member you intend to promote shows
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

1. Open the pool, select the **Hosts** tab, open **Actions** on the member's row and select
   **Eject from pool**.
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

### Record a fence device

Do this **while the pool master is still working**, for every host you would ever want to promote
another host over — in practice, for the pool master itself. A fence device recorded after the
master is lost is impossible: it is part of the membership record the master keeps.

1. Open the pool, select the **Hosts** tab, open **Actions** on the host's row and select
   **Record fence device…**. The row's note says whether one is already recorded.
2. Fill in the `virsh` device: the **host running libvirt** that can power this host off, the
   **libvirt domain** of this host on it, the **SSH login** and **SSH port**, and the **SSH private
   key** and **SSH known hosts entry** for that login.
3. Select **Record fence device**. The key and the known-hosts entry are handed to Nex Core over a
   one-time encrypted delivery and are never stored by Nex Controller, never written to its
   database, never audited, never logged and never shown again.
4. Nex Core decides. It refuses to record a device it cannot verify — a Redfish or IPMI device
   with *BMC fencing identity verification is not implemented*, a SCSI persistent-reservation
   device with *SCSI persistent-reservation fencing is unsupported* — and it refuses to record a
   device whose credentials have not reached the master. A refusal arrives on the task, in Nex
   Core's own words.
5. Before you do this, read
   [What every ready member of a fenced pool can do](#what-every-ready-member-of-a-fenced-pool-can-do).
   Recording a device for the pool master also opts this pool into the restart rule described under
   [Restricted hosts](#restricted-hosts).

To remove a device, record an empty one from the same dialog. Nex Core retires the stored material
on whichever host is master, and never removes material another recorded device still names.

### Promote a surviving member

This is the recovery path for a pool master that is **gone**, not a tidier way to move the
authority. If the old master is still working, close the dialog and use
[Change the pool master](#change-the-pool-master) instead: it hands the authority over and powers
nothing off. Nex Core enforces that — it refuses a promotion whose old master answers and still
holds the pool's authority.

1. Open the pool and select the **Hosts** tab. When the pool master is not reporting, the tab opens
   with a plain sentence saying so and giving the time it was last heard from. Its virtual machines
   have **not** necessarily stopped; an unreachable host is unknown, never proven down.
2. Choose the surviving member to promote. Read its **Authority copy** column: a member with no
   copy, or an unverified one, cannot be promoted, and Nex Core says so rather than guessing.
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
     proceeds once you confirm that. If it has no usable fence device, the promotion is refused
     until you bring that host back or give it one.
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
   and whether a fence device recorded for it could be used; each affected virtual machine is
   listed with why.
   **A promotion can power off more than one host.** The list it names is the old pool master plus
   every other host that blocks the promotion and can be excluded by a fence — an unreachable
   member that could itself have been promoted is exactly such a host, because leaving it running
   while another host takes the authority is how two masters happen. Read that list before you
   confirm: it is what you are agreeing to, it is sent back with the request, and Nex Core checks
   it again against what it computes at the moment it acts. If the pool changed in between, the
   promotion stops and says so rather than powering off something you were not shown. A host that
   blocks the promotion and has **no usable fence device** cannot be excluded at all, and the
   promotion is refused until you resolve it another way.
5. **Step 2 — confirm the promotion.** It is offered only when every other check has passed, the
   exclusion is outstanding only because nothing has been fenced yet, and the promoted host holds a
   usable fence device and its credentials for **every** host on the list. Read it: Nex Core on the
   promoted host **powers those hosts off** through their recorded fence devices and verifies that
   each is off before it takes the authority. Virtual machines on the hosts that survive are not
   restarted and keep running.
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
— that is, when a usable fence device is, or recently was, recorded for it. A pool that never
recorded one restarts its master normally and keeps starting virtual machines, even with a member
down.

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
- The pool authority generation shown under the table is one higher than before.
- Pool changes that were refused while the change ran can simply be submitted again. They were
  refused, not queued, so nothing runs by itself afterwards.
- Losing or replacing Nex Controller during the change makes no difference. The two hosts finish it
  between themselves, and a fresh Controller shows the result once they report.

After an **eject**:

- The host is gone from this pool's **Hosts** tab and appears as its own single-host pool, in the
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
  master's own authority generation, under the table, is the absolute number to compare against;
  a member can also be shown as behind because another fresh member of the same pool is
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
  works if a fence device was recorded for the master **before** it was lost — and in this build
  only a `virsh` fence device can be recorded, so only a disposable laboratory pool can be promoted
  this way. If no usable fence device was recorded, the pool keeps running the virtual machines it
  has and has no master until the master comes back. Do not attempt to hand-edit a member into a
  master.

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
  moved under you. Reload and look again before retrying — that is the point of the message.
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
  addressed a host that has already given the authority away. Reload the pool.

### A promotion or a fence device is refused

- **"The pool master `<id>` is reachable and holds this pool's authority; change the pool master
  with a planned master change instead of promoting."** The old master is alive. Use
  [Change the pool master](#change-the-pool-master); nothing needs to be powered off.
- **"The pool master `<id>` has no recorded fence profile; record one before promoting, and note
  that BMC fencing identity verification is not implemented."** Nothing can be promoted over that
  host. A fence device can only be recorded while the master is reporting, so for this pool, now,
  there is no promotion path. Bring the master back.
- **"This host holds no fence credential for `<host>`; it cannot fence it, so it cannot be promoted
  over it."** A device is recorded but this member never received its material — usually because
  the member joined, or the device was recorded, after the last copy went out and the master was
  lost before the next one. Try another ready member; if none holds it, there is no promotion path.
- **"This fence profile cannot be verified by this host: BMC fencing identity verification is not
  implemented"** / **"… SCSI persistent-reservation fencing is unsupported."** This build records
  only `virsh` fence devices. A physical pool cannot be fence-promoted; see
  [Prerequisites](#prerequisites).
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
  paths. Browser review remains outstanding for the **Authority copy** column and stale state,
  **Pool master change** panel, recovery view, conflict banner, **Restricted** badge and other
  confirmation dialogs.
- **Fence evidence is still partial.** Reviewed disposable-lab evidence includes a scoped fence
  exclusion, externally observed power-off and ordinary rejoin. The f714 boot-window result also
  refused API and installed-hook starts and autostart before authority publication, then allowed a
  normal API start and stop after rejoin. It does not establish contradictory-owner recovery,
  duplicate-writer safety, every start path, physical fencing or permanently-dead-member recovery.
  The maintenance walkthrough's reboot/autostart case remains separate and open. See the
  [current native-acceptance record](../qa/2026-09-19-phase4-native-acceptance.md).
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
  Browser review remains open for the authority-copy column and stale state, transfer/recovery and
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
