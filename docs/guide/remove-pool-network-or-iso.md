# Rename or remove a pool VLAN network or an ISO

## Status

Development workflow for Nex Core 0.3.6 and a matching Nex Controller. A pool VLAN guest network
can be renamed or removed, and an imported ISO can be removed, from the pool's **Networks** screen
and the **ISO library** on its **Storage** screen. The pool master's Nex Core does the work, records it durably and
every member host converges to it on its next pass. Navigation on this page follows the development
build 0.4.8 console layout; see [Navigate the Controller console](navigate-the-controller-console.md).

Limits in this build:

- Only **pool VLAN** networks can be renamed or removed. A host's automatic untagged network, an
  existing bridge or a libvirt network is not Nex's to change and offers no buttons.
- A VLAN tag or uplink NIC cannot be edited. Changing either means removing the network and
  registering a new one, which gives it a **new identity**: every VM interface on the old network
  must be pointed at the new one.
- A member host running an older Nex Core keeps a removed bridge or ISO file until it is upgraded,
  and the master cannot detect that.
- Not verified on real hardware: no native host, no two-host lab and no real guest bridge teardown
  on an installed Nex Core were exercised for this workflow. See **Verification**.

## Prerequisites

- An administrator session in Nex Controller (the browser needs its session and CSRF token; both
  are handled by the UI). Every removal and rename is audited.
- The pool master must be **freshly connected**.
- No VM in the pool, on any host, may still use the resource — **including a shut-off VM**. A VM
  definition that names the network on a NIC, or the ISO in either CD drive, is enough to refuse
  the removal. Detach or eject it in each VM first (see
  [Edit a virtual machine](edit-virtual-machine.md)).
- Every ready member must have published its VM inventory at the current membership generation.
  This happens automatically within about a minute of a member becoming ready.
- For a member to converge, it must be reachable and able to reach the master's pool-peer
  listener, as for any other pool configuration ([Join a pool](join-a-pool.md)).

## Steps

### Rename a pool VLAN network

1. Sign in to Controller, choose the pool in the top-bar scope menu, then select **Networks** in
   the rail (`#/networks`). The **Networks** section lists the pool's networks.
2. Find the pool VLAN row (*Pool VLAN* under its name) and select **Rename** in the
   **Actions** column. If the button is disabled, its tooltip names the reason; see
   **Troubleshooting**. **About changing networks**, below the table, summarises what can be
   changed here.
3. The dialog **Rename `<name>`** explains that the pool master records the new name and every
   host picks it up on its next pass, and that the VLAN tag, the uplink NIC and the VMs on this
   network are unchanged. Edit **Network name** (up to 100 characters, no slashes or control
   characters) and select **Rename network**. **Cancel** or `Escape` closes without sending
   anything; the button stays disabled while the name is unchanged or invalid.

### Discard a registration the pool master refused

A pool VLAN network the pool master **refused** to register was never created on any host. It is only Nex Controller's record of the request, so it is not removed — it is
**discarded**, exactly as a failed ISO import already was.

1. With the pool chosen in the scope menu, select **Networks** in the rail and
   find the row whose task shows **failed**.
2. Its only action is **Discard failed registration**. Rename, Remove, Recheck, Update and Detach
   are not offered on a failed row, because there is nothing on a host to act on.
3. Confirm. Nex Controller deletes only its own record of the failed request and asks no host to do
   anything; the task and the audit history are kept.

If a host has since reported that registration as applied, the discard is refused with *a host
reports this registration as applied, so it is not a failed request record; remove it on the host
instead of discarding it* — and the row stays. That is the guard that stops a discard deleting a
live pool resource's row. A refused storage registration is not listed at all and has no Discard button; its reason is in **Tasks**. A repository a host does hold is removed with **Detach**, never with a
discard.

### Remove a pool VLAN network

1. With the pool chosen in the scope menu, select **Networks** in the rail and select **Remove**
   on the pool VLAN row.
2. The dialog **Remove network `<name>`?** states that the pool master removes this VLAN bridge
   from every host in the pool and records the removal durably, that Nex Core refuses while any VM
   still uses this network, running or stopped, and that **the management network is never
   touched**. If Controller's inventory shows VM definitions still using the network, a warning
   names how many and tells you to detach the network in each VM first.
3. Type the network name into **Network name** exactly as shown — no trimming, no case folding.
   The **Remove network** button arms only on an exact match, and pressing `Enter` in the field
   sends nothing.
4. Select **Remove network**.

### Remove an imported ISO

1. With the pool chosen in the scope menu, select **Storage** in the rail (`#/storage`; the older
   `#/settings/iso` link opens the same screen) and scroll to **ISO library** below the
   repositories. **About removing ISOs**, below the ISO table, summarises the rules below.
2. On a row whose transfer task shows `succeeded`, select **Remove ISO**. (A **failed** import is
   only a Controller request record; it keeps the separate **Discard failed import** action, which
   asks no host to do anything — the same model a refused network registration now
   follows, see [Discard a registration the pool master refused](#discard-a-registration-the-pool-master-refused).)
3. The dialog **Remove ISO `<name>`?** states that the pool master deletes this ISO file from its
   storage, that Nex Core refuses while the ISO is in any VM's CD drive, running or stopped, and
   that **no VM disk is touched**. Type the ISO name into **ISO name**, then select **Remove ISO**.

## Expected result

The dialog closes and a notice appears at the top of the section, for example *"Removal accepted for
`<name>`. The pool master removes the VLAN bridge from every host in the pool; the network
disappears from this list once the master reports the removal."* A rename says *"Rename accepted
for `<name>`. The new name appears once the pool master reports it."*

The row does **not** disappear immediately. Acceptance means the request reached the pool master's
Nex Core, not that the work is done. Watch the request on **Tasks** (the rail item, or the activity pill in the top bar); the row leaves the list
(or shows its new name) when the master reports the result, normally within a few seconds.

On member hosts the change is applied on the ordinary member pull pass — the same tick that pulls
shared storage, every ten seconds on a packaged host, so seconds rather than minutes; allow up to a
minute if a slow storage check delays the pass. A member deletes the guest bridge, its 802.1Q
sub-interface (only when no other Nex network still needs it) and its
`/etc/network/interfaces.d` entry, and never rebuilds them.

For an ISO a member drops only **its own record**. The file on shared storage is the pool's single
copy, so only the host that imported it — the master, for a pool library — unlinks it. Expect the
file to disappear when the master has converged, not when a member has.

Each member's result is visible in its **pool-network-reconciliation** and
**pool-media-reconciliation** health checks under **Health checks** on the **Overview** tab of
that host's panel (**Hosts** in the rail, then the host's row).

Nothing else changes: the uplink NIC, the management bridge, host addresses and routes, and VM
disks are never touched by either operation.

## Troubleshooting

- **The buttons are disabled.** The tooltip gives the reason, in this order: *Controller inventory
  may be out of date. Use Retry above to reload it before changing this pool resource.* — select **Retry** on the inventory alert
  or reload the page;
  *Wait for Nex Core to confirm this resource before changing it.* — the registration or import has
  not succeeded yet; *The pool master is not freshly connected. Reconnect it before changing this
  pool resource.* — see [Set up Nex Controller and enroll a host](manage-controller.md); *A request for this resource
  is already in flight. Wait for Nex Core to report its result.*
- **A host row shows no buttons, only "Host network · created and managed on `<host>`".** That
  network was created by the host itself, not as a pool VLAN, and cannot be removed here. Removing
  a host's automatic untagged network would only make the host register it again.
- **The task fails with "network cannot be removed while used by VM `<uuid>`" (optionally "on host
  `<host>`").** A VM definition still has a NIC on this network. A **stopped** VM counts: the
  definition is what matters, not whether the guest is running. Edit or delete that VM, then
  submit the removal again. The ISO equivalent is *"ISO cannot be removed while attached to VM
  `<uuid>`"* — eject it from the VM's CD or driver drive first.
- **The task fails with "pool VM inventory is incomplete; wait for every member to report before
  removing this network" (or "… this ISO").** The master has not heard from every ready member at
  the current membership generation, so it refuses rather than removing something a member's VM
  might be using. Bring the member back or wait for its next publication, then retry. A member
  that has dropped out of **ready** does not stop counting: the master keeps that member's last
  published VM list and still refuses while one of those VMs uses the resource. Only a *ready*
  member must additionally be at the current membership generation.
- **The task fails with "bridge `<b>` was not created by Nex; remove it by hand".** Nex proves
  ownership from the `/etc/network/interfaces.d/nex-<bridge>` entry it wrote. A bridge with no such
  entry, or one written for a different network, is left strictly alone; remove it yourself on the
  host if you created it.
- **The task fails with "network still has attached interfaces".** Something other than this
  network's own guest port is attached to the bridge — most often a running VM's tap device on a
  host that was not accounted for, or an interface an operator attached by hand. Detach it, then
  retry.
- **The task fails with "only pool VLAN networks can be removed".** The network is a bridge or
  libvirt network registration. It is also the refusal for renaming such a network.
- **The task fails with "network generation is no longer current; refresh and retry".** The network
  changed (usually a rename) after the list you acted on was loaded. Reload the page and submit
  again.
- **A member's `pool-network-reconciliation` check reads `failed: network <id> is still used by VM
  <uuid> on this host`** (the media check reads `failed: ISO <id> is still attached to VM <uuid> on
  this host`).
  That member is refusing to tear the resource down because one of its own VM definitions uses it.
  The master has already removed it; edit or delete that VM and the member converges on its next
  pass. Nothing is retried in a loop and no journal entry is written for a refusal the host can
  decide in advance — a VM still using the resource, a bridge Nex cannot prove it created, a port
  attached out of band, or an unreadable journal. A teardown that fails for an *unexpected* reason
  on the host is recorded once and retried at most once per hour, so after repairing the host
  condition allow up to an hour for the next real attempt.
- **A member's check reads `pool network <id> cannot be removed on this host: <reason>`** (or
  `pool ISO <id> cannot be removed on this host: <reason>`). The member ran the same checks the
  teardown itself runs before touching anything, and `<reason>` is any of the refusals listed
  above — `network cannot be removed while used by VM <uuid>`, `ISO cannot be removed while
  attached to VM <uuid>`, `bridge <b> was not created by Nex; remove it by hand`, `<b> exists but
  is not a bridge`, `network still has attached interfaces`, `only pool VLAN networks can be
  removed` or `managed journal is incomplete`. Fix the named condition on that host; the member
  converges on its next pass and writes no journal entry meanwhile.
- **Members' `pool-network-reconciliation` / `pool-media-reconciliation` checks all fail at once
  and nothing converges.** The master answers its pool network and ISO lists with `503 pool
  resource list unavailable: managed journal is incomplete` when it cannot read its own managed
  journal whole. Members deliberately keep everything they hold rather than read a short list as a
  removal. The master's own **managed-journal** health check names the journal entry it could not
  read, as described under
  [Set up Nex Controller and enroll a host](manage-controller.md#troubleshooting).
  The master refuses every managed operation in this state too, so this is a master repair, not a
  member problem. Repairing a damaged managed journal on a host is a manual recovery and is **not
  yet documented** as a procedure in this guide; treat it as an open item rather than improvising
  against the master's state directory.
- **A member's check reads `pool network <id> was removed on this host and is listed again by the
  master; remove it on the master and add it again`** (or `pool ISO <id> …`). A removal on a host
  is permanent: the resource ID is reserved for good, so that host can never hold that ID again.
  This happens when the master's record was restored from a backup older than the removal. Remove
  the resource on the master and register a new one; the new resource has a new identity, so
  re-point each VM's NIC (or CD drive) at it. The member writes nothing and retries nothing until
  you do.
- **A member shows nothing and keeps its bridge or ISO.** If that member runs an older Nex Core it
  does not understand removals and keeps what it has until it is upgraded; the master cannot detect
  this. If the member cannot reach the master, or the master answers with an error or a partial
  list, the member deliberately removes nothing — an uncertain list is never treated as "everything
  else is gone".
- **A guest bridge came back after somebody deleted it on a member by hand.** That is expected
  while the master still lists the network: the member re-creates a pool network it observes
  missing. Remove the network from the pool instead of deleting the bridge on a host.
- **The VLAN tag or uplink NIC is wrong.** There is no edit for either. Remove the network and
  register a new one with the correct uplink and tag, then re-point each VM's NIC at the new
  network. The new network has a new identity; the removed one's identity is never reused.
- **The reply was lost (browser closed, network blip) and you do not know whether it was
  accepted.** Submit the identical request again. The same unchanged request replays the original
  operation and its result instead of starting a second removal. Only a *changed* request (a
  different new name, for example) is a new request.
- **The removal was accepted but nothing happened.** Check **Tasks** for that operation.
  Nex Core does not report a removal as succeeded unless it completed; an interrupted teardown is
  re-run and settles, because every step is safe to repeat.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

Unit and race tests cover the Core teardown, ownership proof, dependency refusals, the master's
pool-wide refusal, member convergence, the Controller relay and the UI gating, and a
network-namespace test exercised a real bridge, VLAN device and ifupdown entry on a developer
workstation. The UI steps above were walked through in a browser against **stubbed** Controller
responses only. No native Nex Core host, no two-host pool and no real member convergence were
exercised, and no version-skew combination was tested. See the
[QA note](../qa/2026-09-19-pool-removal-convergence.md) for exactly what ran and what did not.

[Back to the user guide](README.md)
