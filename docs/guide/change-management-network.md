# Change a host's management network

## Status

Development product 0.4.17. Change a Nex Core host's own management addressing — which interface
carries management, DHCP or a static address, an optional management VLAN tag, the default gateway
and the DNS servers — from Nex Controller, in a staged flow with a timed rollback the **host**
performs by itself: **Hosts** in the rail → the host's row → the panel's **Networking** tab → the
**Management network** panel.

This is the one edit that can make a host unreachable, so the whole operation belongs to the host:
Nex Core renders the configuration, applies it, times it and restores the previous one if the
change is not confirmed. Nex Controller submits one step at a time and shows what the host reports.

Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).
If Controller is restarted, replaced or simply switched off in the middle, the host still finishes
or rolls back on its own, and a replacement Controller reads the whole state back from the host.

What this workflow is **not**, in this release:

- **It does not rename interfaces.** The interface names on the host (`nex0`, `nex1`, …) are the
  installer's and are bound to hardware addresses in `/etc/systemd/network/*.link`. Those files are
  never rewritten here, because a rename only takes effect on the next boot and could not be undone
  inside a rollback window. Rename interfaces by reinstalling or from the host console.
- **It does not touch guest networking.** Pool VLAN networks and their guest bridges live under
  `/etc/network/interfaces.d/` and are never read, written or brought down by this change. Running
  VMs keep running. The management bridge is **not** taken down and rebuilt: Nex Core reconfigures
  it in place, so a VM attached to the management bridge itself keeps its connection to that bridge
  and only sees the brief gap between the old address being removed and the new one being added,
  while a VM on a guest VLAN bridge is not affected at all. **That is how it is designed and how it
  behaves in the tests; it has not been observed on a real host** — see
  [Verification](#verification).
- **It does not leave an interface you leave out of the spec alone.** A NIC you simply omit loses
  its entry in the rendered configuration and is unconfigured at the next boot. List every
  interface the host reported, even the ones you are not changing.
- There is no IPv6 configuration, no bonding editor, no static routes and no multi-host or
  pool-wide network edit. One host at a time.

## Prerequisites

- An administrator session in Nex Controller, and the host's panel open at its **Networking** tab. All
  administrator sessions have Controller-wide scope; there is no narrower role for this action.
- The host **connected and fresh**, and advertising the `management-network-v1` capability. Without
  it the panel says *This host does not support staged management network edits. They need
  management-network-v1 on this host; upgrade Nex Core.* and offers no control.
- **Console access to the host, arranged before you start.** Physical access, a BMC/IPMI console or
  the **Console** tab of the host's panel in Nex Controller. The automatic rollback is designed to bring the
  host back without it, but a console is the only recovery if the rollback itself fails.
- **The host must not be feeding a watchdog.** A pool's Corosync cluster runs on the management
  network, so interrupting it on an armed host is the same thing as partitioning that host, and an
  armed host on the minority side resets itself. **Host fencing** is on by default, so on a pool of
  three to five hosts with no clustered storage expect this to apply. Put the host in
  **maintenance mode**, or turn Host fencing off for the pool, and wait until the pool reports the
  host as not armed before you stage anything. See
  [Host fencing, and the watchdog line](pool-authority-and-recovery.md#host-fencing-and-the-watchdog-line).
- The host's `/etc/network/interfaces` and `/etc/dhcpcd.conf` must be the ones Nex Core or the Nex
  installer wrote. Nex Core refuses to overwrite a file it cannot prove it rendered, and says which
  one. A freshly installed host always passes.
- `/etc/resolv.conf` must be a **regular file**, not a symlink. On a host where `systemd-resolved`
  owns it as a symlink, every management network change is refused until it is replaced with a
  plain file. Nex Core's installer writes a plain file.
- The new addressing must actually work on the switch port: the VLAN tag must be carried, the
  address must be free, and the gateway must be inside the new subnet. Nex Core validates the
  *shape* of the configuration, not the cabling.
- Know how long you will need. The rollback window is 2–30 minutes, and 10 minutes by default.

## Steps

1. Select **Hosts** in the rail, select the host's row to open its panel, and select the
   **Networking** tab (the **Interfaces & VLANs** button on the panel's
   **Overview** tab opens it too). The **Management network** panel is above the
   read-only *Network interfaces* list. Its badge shows the current state — *no change staged*,
   *staged, not applied*, *applied, waiting to be confirmed*, *confirmed*, *cancelled*, *rolled
   back automatically* or *rolled back at your request* — and the configuration revision.
2. Fill in the **Interfaces** table. When Nex manages this host's network files, the table and
   DNS servers start filled from what the host reports as its current configuration; change only
   what you intend to. One row per physical or bonded interface the host reported:
   - **Management** — the radio that marks the one interface carrying management. Exactly one.
   - **Addressing** — *Not configured*, *DHCP* or *Static*.
   - **VLAN** — the management VLAN tag, or blank for untagged.
   - **Address** — enter an IPv4 address with its prefix, for example `192.0.2.10/24`, for Static. With DHCP, the disabled box shows the host's currently reported address when available.
   - **Gateway** — enter one only on the management interface, inside its own subnet, for Static. With DHCP, the disabled box shows the host's currently reported default gateway when available. An unreported value says *Not reported*; refresh after a lease change. The boxes do not set DHCP values.
3. Under **Name servers and rollback**, enter **DNS servers** (IPv4, space- or comma-separated, at
   most eight) and the **Rollback window (minutes)**: a whole number from 2 to 30. Leave DNS blank
   for DHCP to use its normal resolver handling; Nex Core preserves the current resolver file until
   the DHCP hook replaces it. This is how long you will have to confirm the change after applying it.
4. Select **Stage this configuration**. Nothing on the host changes: staging records what you
   propose and snapshots the files it would later replace. It does not apply a second network
   configuration or make a staging network change.
5. Review the staged change. The panel now shows *Staged change — review before applying. Nothing
   on the host has changed yet.* with a row per interface comparing **Now (reported by the host)**
   with **Proposed**, the DNS servers, and the window the host recorded. Select **Discard staged
   change** if it is wrong; staging again replaces it.
6. Select **Apply…**. A dialog opens headed *Apply the management network change on &lt;host&gt;*,
   warning that **&lt;host&gt; may become unreachable**, repeating the diff, and stating that the
   host restores the previous configuration by itself after the window unless you confirm it, and
   also if it reboots first.
7. Type the host's name exactly as the dialog shows it, then select **Apply and start the
   countdown**. The typed name is checked by Nex Controller *and again by the host itself*, against
   its own hostname.
8. The host writes the new files, reconfigures the management bridge in place — address, route,
   hardware address, DHCP client, and the uplink port only when the interface or VLAN changed — and
   starts its own countdown. Nex Controller shows *Rolls back in M:SS unless you confirm it.* while the host is
   reporting, and *Waiting for the host to reconnect over the new configuration.* while it is not.
   **Losing contact here is expected** if the address changed: nothing is wrong, and the deadline
   is still running on the host.
9. Reach the host on its **new** address. If Nex Controller uses the management network, the host
   reappears there by itself; otherwise open the new address in a browser, or wait for the next
   report.
10. Select **Keep this configuration** once the host is fresh again. Nex Core refuses to confirm
    until it has itself observed traffic over the new configuration — a successful report to this
    Controller, or a successful signed-in call to the host's own local service — so confirming from
    a second network path or a cached tab cannot keep a broken configuration.
11. The badge becomes *confirmed*, the countdown stops, the revision increases and the snapshot of
    the previous configuration is dropped.

To change nothing and walk away after step 7, do nothing: the host restores the previous
configuration at the deadline and reports *rolled back automatically* with Nex Core's reason. To
undo it straight away instead, select **Roll back now**; the host restores the previous
configuration at once and reports *rolled back at your request*.

## Expected result

- While staged: nothing on the host has changed. The badge reads *staged, not applied*, and
  `/etc/network/interfaces`, `/etc/dhcpcd.conf` and `/etc/resolv.conf` are untouched.
- After applying: the host is on the new addressing, the badge reads *applied, waiting to be
  confirmed*, and a deadline is running **on the host**, not in your browser or in Controller.
- After confirming: the badge reads *confirmed*, the revision has increased, and the panel lists
  the management configuration the host now reports. The read-only *Network interfaces* list below
  shows the new addresses at the host's next report.
- Guest VLAN bridges and running VMs are unaffected throughout, and so is any VM attached to the
  management bridge: the bridge is reconfigured, never rebuilt, so guest taps stay attached to it.
  This is designed and tested behaviour, **not yet observed on a real host**.
- A replacement Nex Controller, connected to the same host afterwards, shows the same state: the
  configuration, the stage and any running deadline all come from the host.

## Troubleshooting

**Every refusal you can see, and what it means.**

- *This host does not support staged management network edits. They need management-network-v1 on
  this host; upgrade Nex Core.* — the host's Nex Core is older than this feature.
- *The host must be freshly connected before a management network change is staged or applied.* —
  staging and applying need a reporting host. Discarding a staged change does not.
- *The host has not reported since the change was applied; it cannot be confirmed until it reaches
  this Controller again.* — expected between applying and the host reappearing. Wait, or reach the
  host on its new address.
- *This host is feeding a watchdog that resets it if it loses this pool's quorum, and this pool's
  cluster runs on the management network. Turn Host fencing off for this pool, or put this host in
  maintenance, before changing its management network* — both staging and applying are refused
  while the host is armed, because changing this network partitions it and an armed host on the
  minority side resets itself. Do one of the two things the message names and wait until the pool
  reports the host as not armed; putting a watchdog down means restarting that host's cluster
  services, so it takes a minute or two. See
  [Host fencing, and the watchdog line](pool-authority-and-recovery.md#host-fencing-and-the-watchdog-line).
- *Nex Core could not read whether this host is feeding a watchdog, so it will not change the
  network its pool's cluster runs on* — the same guard, when the host could not answer the question
  about itself. It fails closed. Check that the host's own services are healthy, then retry; if it
  persists, turn Host fencing off for the pool or put the host in maintenance.
- *This host is revoked.* — the host's enrollment was revoked; nothing can be sent to it.
- *This host reports management network revision N; reload the host network page and retry.* — the
  host's configuration moved while the page was open. Reload and start again.
- *This host reports no management network change to apply/confirm/cancel; reload the host network
  page.* and *This host reports management network change &lt;id&gt;, not the one you asked to …* —
  the staged change you were looking at is gone or has been replaced. Reload.
- *The confirmation must be the host name "&lt;hostname&gt;".* — the typed name did not match the
  host's own reported name. Leading and trailing spaces and letter case are ignored.
- *&lt;path&gt; was not written by Nex Core or the Nex installer; edit it from the host console
  instead.* — the file was hand-edited. Nex Core will not overwrite it. Edit the management network
  from the host console, or restore a Nex-rendered file there first.
- *&lt;path&gt; is missing; Nex Core will not create a management network configuration it cannot
  prove it rendered.* — one of the three files does not exist.
- *&lt;path&gt; is not a regular file (…); replace it with a plain file before changing the
  management network.* — almost always `/etc/resolv.conf` as a `systemd-resolved` symlink. Replace
  it with a plain file on the host console, then retry.
- *A management network change is already applied and waiting to be confirmed or rolled back.* —
  finish the change in flight first.
- *The host network configuration changed since this edit was staged; stage it again.* — the files
  on the host no longer match the snapshot taken at staging, so the rollback could not be trusted.
- *The rollback deadline for &lt;id&gt; has passed; the previous configuration is being restored.*
  — you confirmed too late. The host is restoring the previous configuration; stage the change
  again.
- *This host has not yet been reached over an address of `br0` itself under the new configuration;
  a Controller report that arrived over another interface, or a local API call from this machine,
  does not count.* — Nex Core has not seen the **management bridge** carry traffic to or from
  somewhere else, so it will not keep the change. This is the check that stops a confirmation from
  a path that is not the one under test: a second network card, a call made on the host itself, or
  a browser tab that still works because it never left. Wait for the host to report over the new
  address, or reach its local interface from another machine, and confirm again.
- *Management network change &lt;id&gt; is already staged with a different configuration.* — the
  same change was submitted again with different settings. That is not a retry, so it is refused
  rather than silently reporting on the change already recorded. Discard the staged change and
  stage the new one.
- *Rollback window must be between 2m0s and 30m0s.* — the window is out of range.
- *Interface &lt;n&gt; does not exist on this host.* and *Interface &lt;n&gt; has hardware address
  &lt;a&gt; on this host, not &lt;b&gt;; the spec names a different card.* — the configuration names
  a card this machine does not have, or names the wrong one. Reopen the host panel's **Networking** tab so the
  interface list is the host's current one.
- *Interface &lt;n&gt; carries registered guest networks (…) and cannot be disabled; remove those
  networks first.* and *&lt;port&gt; is already the port of a registered guest network; the
  management interface cannot share it.* — a pool VLAN network is using that interface. Remove the
  pool network first, or choose another interface for management.
- *&lt;port&gt; is already a port of bridge &lt;b&gt;; free it before making it the management
  uplink.* — the interface you chose is already attached to another bridge on the host.
- *`br0` does not record which port uplinks it, and guests are attached to it (…); moving the
  management uplink would mean rebuilding the bridge and cutting them off.* — the host's bridge was
  built by hand, so Nex Core cannot tell which port to move without destroying it. Change the uplink
  from the host console, or move those guests first.
- *Interface name "br0" is not a usable interface name.* — `br0` is the management bridge Nex Core
  renders; it is not an interface you list.

**When the host rolls back by itself.** In all three of these cases, with no Controller involved:

- the rollback deadline passes without a confirmation;
- the host **reboots** after applying and before confirming — a reboot is not evidence that the new
  configuration works, and the window was spent while the host was down;
- applying fails: a file cannot be written, or an `ip`/`dhcpcd` step for the bridge fails;
- you select **Roll back now** on an applied change.

Neither the deadline nor the reboot check reads the clock. The host records its boot identity and
its own uptime at the moment it applies the change, so "did this host reboot" is a boot-identity
comparison and "has the window been spent" is measured from uptime within the same boot. A host cut
off by its own change usually loses its time source with it and comes back with a wrong clock —
which is exactly when the rollback has to fire, and it does. The countdown you see is computed the
same way, so it cannot disagree with what the host will do. A host that is **suspended** spends its
window while suspended and comes back rolled back; nobody confirmed, so that is the safe direction.

**One limit worth knowing before you apply.** All three triggers run inside the Nex Core agent, so
the rollback needs that service to be running. Its unit is `Restart=always`, and the recovery check
runs unconditionally when it starts, so a crash, an out-of-memory kill or a clean exit means the
window is honoured late but honoured. If the service is deliberately **stopped or masked** between
applying and the deadline, nothing is armed until it is started again. Do not stop Nex Core during
a rollback window.

Afterwards the panel shows *rolled back automatically* with Nex Core's reason, and the host reports
a failing `management-network` health check until a later change succeeds — **except** when you
asked for the rollback yourself. Then the panel shows *rolled back at your request*, says that
nothing failed, and the host reports **no** failing check: an undo you asked for is an outcome, not
a fault. Either way the restored files are byte-identical to the snapshot, and if the rollback is
interrupted half way the host retries it at its next start and on every probe pass until the
interface is back up — still as the rollback you asked for, not as a failure.

**"Discard staged change" and "Roll back now".** Both are the same durable step, and Nex Core
decides what it means from the stage it finds. *Discard staged change* cancels a change that has not
been applied; nothing in the host's configuration was ever touched. *Roll back now* is offered on an
applied, unconfirmed change and restores the previous configuration immediately, without waiting for
the deadline. Both are durable commands, so they also work on a host that has gone quiet and run
when the host next reports — which is exactly when you are most likely to need them. A change that
is already *confirmed*, *cancelled* or *rolled back* cannot be cancelled again, and Nex Core says
so rather than doing anything. Rolling back can cut the connection that asked for it; that is
expected, and the host finishes the rollback by itself.

**You are locked out of the host.** The automatic rollback is the designed recovery and needs
nothing from you. If the host is still unreachable after the window has passed plus a minute or
two, the rollback itself failed. Recover from the host's own console — physical, BMC, or the
**Console** tab of the host's panel in Nex Controller if the Controller can still reach it:

1. Sign in on the console as `root`.
2. Read `/var/lib/nex-core-agent/management-network.json` (the agent state directory on a standard
   install). Its `stage` says what the host thinks it is doing, `error` says why a rollback
   stopped, and `previous` holds the verbatim contents of the three files as they were before the
   change.
3. Restore those contents into `/etc/network/interfaces`, `/etc/dhcpcd.conf` and
   `/etc/resolv.conf`, then bring the management bridge back. Rebooting the host is the safest way.
   If you run `ifdown br0 && ifup br0` by hand instead, be aware that it **destroys and recreates
   the bridge**: any VM attached to the management bridge loses its connection until its domain is
   restarted. Nex Core's own apply and rollback never do this.
4. Restart the Nex Core service (`systemctl restart nex-core-local`) and check that the host
   reports again. Diagnose with `journalctl -u nex-core-local`.

Nex Core never overwrites a file it did not render, so anything you edit by hand on the console is
left alone afterwards — and a later staged change is refused until a Nex-rendered file is back.

**Nex Controller is lost during the change.** Nothing changes on the host: it finishes its own
countdown and confirms nothing, so an unconfirmed change is rolled back. A new or rebuilt
Controller connected to the same host shows the record, the stage and any remaining deadline as
soon as the host reports; there is nothing to re-enter and no export to keep.

**The change did what you asked and the host is still unreachable.** The configuration is valid but
the network is not what you assumed — wrong VLAN on the switch port, an address already in use, a
gateway that does not route. Do not confirm. Let the host roll back, then correct the plan.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

- **G3 browser walkthrough.** In a disposable-host browser session, an operator staged, applied
  and confirmed the documented management-network baseline through this panel, then staged and
  applied a second safe change and selected **Roll back now**. Browser-fetched authoritative state
  reported the confirmed baseline at revision 25, then the second operation as rolled back with
  `requested_rollback=true`; the observed bridge returned to the confirmed baseline. Desktop and
  360 px captures were inspected, including the typed-hostname apply dialog, countdown and restored
  state. This covers operator-requested rollback only. Deadline expiry and reboot recovery remain
  separate native C7/C12 evidence, not part of the browser walkthrough.
- **DHCP display check (2026-09-25).** A browser fixture using the actual panel and a simulated
  fresh Core report showed the disabled address and gateway boxes populated, cleared them on a
  failed report, and hid the form for a stale host. It did not obtain a physical DHCP lease or
  exercise a full Controller/Core connection. See the [DHCP display QA note](../qa/2026-09-25-management-dhcp-display.md).
- **The namespace test has never executed.** The check that would run real interface commands on
  dummy interfaces skips on every machine used so far, because `ifupdown` is not installed there.
  Final-candidate native evidence separately covers DHCP with no DNS override, operator rollback,
  staged cancel, stopped-agent deadline recovery, guest-VLAN preservation through apply/confirm,
  and rollback while Controller is unreachable. It also covers clock-skew rollback after a
  disposable-host-only temporary systemd prestart fault placed the guest clock 3,600 seconds behind
  before Core began: the host rolled back in 0.1 seconds, restored management files byte for byte,
  and the temporary clock configuration, guest definition and services were restored. No product
  code changed. Earlier RTC-clock attempts failed to create the required skew and are not evidence.
  The stopped-agent case used the lab service's `Restart=on-failure` setting and therefore does
  not validate packaged-service crash/restart behavior. The first Controller-unreachable attempt
  left only a harness firewall rule; it was removed, and the final guarded rerun passed. This does
  not cover a complete final-candidate replay.
  The tests still prove *which* commands Nex Core runs and that the destroying ones are never among
  them; the remaining limits must not be claimed as complete host coverage.
- **Fixed in this build:** *Roll back now* on an applied change used to submit a step Nex Core
  refused, so an applied change could not be undone early. It now performs the rollback. The fix is
  covered by a cross-layer test that drives stage → apply → cancel through the real handlers into
  the real engine and by the G3 browser walkthrough above.
- Covered by Go `-race` unit tests on Nex Core: the render against the installer's rules, the
  validation table, the whole stage/apply/confirm/cancel state machine with every refusal, rollback
  on the deadline and after a reboot with a byte-identical restore, the rollback firing correctly
  with a wrong clock after a reboot and not firing early on a clock jump, a rollback retried after a
  failed `ip` command, the bridge never being destroyed and no guest tap ever being detached across
  confirm, deadline rollback and operator rollback, an unchanged uplink moving no port, confirmation
  being refused for an observation that did not terminate on the management bridge, re-staging the
  same change with a different configuration being a conflict, crash windows before and after the
  file swap, and the refusals for a foreign, missing, symlinked or non-regular file. A cross-layer test drives stage → apply → confirm and
  stage → cancel through the real Controller HTTP handlers into the real Nex Core engine on a
  scratch `/etc`. The earlier UI behavior was covered by server-rendered component tests; the
  DHCP display check above adds a narrow browser fixture.
- Offline contract checks in `tests/test_api_contract.py` hold the two routes, their refusals, the
  command and the inventory projection to the implementation; see the
  [API reference](../API.md#staged-management-network-edits-getpost-apiv1hostsidnetwork).

[Back to the user guide](README.md)
