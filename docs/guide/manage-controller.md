# Set up Nex Controller and enroll a host

## Status

Development build of Nex Controller and the Nex Core agent: administrator access,
Sites → Pools (one master host + members) → Hosts, secure enrollment, observed inventory,
health reporting and read-only inventory refresh. Deploying the bundled Controller from a Core
host's local UI enrolls that host automatically; other reachable hosts are added with **Add host**
(address, root password, fingerprint confirmation), and the manual token procedure is the fallback
for hosts the Controller cannot reach. Each enrolled host becomes the **pool master** of its own
pool, named after its hostname. [VM creation](create-virtual-machine.md) has separate
prerequisites. Controller-driven [member join](join-a-pool.md),
[planned master change, promotion and eject](pool-authority-and-recovery.md) are available as
development workflows. Their native acceptance and recovery evidence remain incomplete, so they
do not establish physical fencing or automatic-recovery acceptance. Phase 4 is
closed by project-owner decision; later defects are tracked as bugs.

Use one Controller process with its SQLite database on local disk. It is authoritative only for
sites, administrator accounts, audit history, enrollment tokens, the Controller CA and backup
jobs; pools, hosts and VMs are re-adopted from the hosts (see **Replace a failed Controller**
under [Troubleshooting](#troubleshooting)). Use a private management network or VPN and read
[web-access security behavior](secure-web-access.md). Navigation on this page follows the
development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).

## Prerequisites

- A disposable Linux lab machine for Controller and an explicitly selected Debian 13
  host for agent evaluation; retain local recovery access to the host.
- Matching Controller, agent, and web assets from the same development build; see the
  [development instructions](../development.md). Keep Controller outside the host being installed.
- A Controller HTTPS address that the host can reach, a server certificate valid for that
  address, its private key, and a trusted copy of its issuing CA certificate. The file given
  to `-tls-cert` should hold the server certificate followed by that issuing CA certificate:
  **Add host** sends that file's certificates to the host as its trust anchors for Controller.
  Obtain trust material through an authenticated channel; do not bypass certificate verification.
- A private directory on each machine for persistent state. Do not share an agent state
  directory between hosts: it contains that host's private identity and operation journal.
- Access to the host's read-only libvirt observations. Missing permissions, `/dev/kvm`,
  or libvirt support must remain visible as unavailable/degraded capabilities.

## Steps

1. Prepare Controller's HTTPS certificate and key, the built web assets, API documentation,
   and corresponding source archive using the development setup instructions linked above.
   Save a nonempty administrator password (maximum 1024 UTF-8 bytes, without line breaks
   or NUL) in a private file
   (`0600`), then start Controller from the repository root after building:

   ```sh
   ./dist/nex-controller \
     -data-dir controller-state \
     -listen :8443 \
     -tls-cert controller.pem \
     -tls-key controller-key.pem \
     -admin-username root \
     -admin-password-file admin-password \
     -ui-dir dist/web \
     -docs-dir dist/api \
     -source-archive dist/nex-source.tar.gz
   ```

   The first start creates the administrator. Subsequent starts retain the existing account;
   changing the password file does not reset its password. Keep the state directory on local
   disk and retain its database and private host certificate authority together. The host
   certificate authority is separate from the HTTPS server CA; never copy its private key
   to an enrolling host.
2. Open the Controller HTTPS address in a browser that trusts its server certificate.
   Choose **Sign in** with the administrator account. The console opens on **Overview**; the
   labelled rail on the left, the top bar's scope menu, search, activity pill, theme button and
   account menu are described in [Navigate the Controller console](navigate-the-controller-console.md).
   With no site yet, **Overview** shows **Start with a site** with a **Create site** button;
   **Create site** is also in the top bar's scope menu. Enter the site name and select
   **Create site**. Do not look for a pool-creation action: pools are created automatically when
   a host enrolls. A site without hosts shows **Add your first host** with an **Add host** button.
   Open the scope menu (the site name and pool name at the left of the top bar) to switch
   between sites and pools: each site is listed as `<site> · all pools`, followed by its pools.
   Opening a direct pool, host or VM link selects its owning site automatically.
3. With the site chosen in the scope menu, choose **Add host** (scope menu or the empty-state
   button).
   The form is titled **Add host to** followed by the site name; it explains that a new host
   becomes the master of a new pool in this site, and a host that already belongs to a pool
   keeps it. Enter:
   - **Host address** — the host's IP or hostname (`10.0.0.5`), with an optional `:port`
     (default `9443`, the `nex-core-local` listener).
   - **Root password** — the host's current OS root password. It is sent once, over a
     certificate-pinned connection, and is never stored, logged, or echoed back.
   - **Controller address** — prefilled with this browser's address; edit it if the host
     must reach the Controller by a different name or IP. The help text under the field
     is a reminder that the host, not your browser, must be able to open this address.
     A loopback or port-forwarded browser address such as `https://127.0.0.1:…` is never
     reachable from the host; replace it with the Controller's address on the management
     network. The host verifies Controller's certificate against this address. When the
     certificate names only a hostname, as a bundled appliance deployed with DHCP does,
     Controller also tells the host that hostname to verify by, so entering the appliance's
     IP address here works. A host running an older Nex Core does not accept that name:
     the panel then says so, and either upgrading the host or entering the hostname the
     certificate names, resolvable from the host, lets it enroll.

   Select **Connect**. Controller opens an unauthenticated TLS handshake to the host and
   shows the certificate's **Address**, **Certificate fingerprint** (`sha256:…`),
   **Subject** and **Expires** date. Compare the fingerprint against the host's own console or install
   output before continuing — this confirmation is the only trust check before the root
   password is used, so do not skip it. Select **Confirm and add host** to proceed, or
   **Cancel** to change the address. On success the panel shows
   `Host <id> enrolled in pool <pool>; it appears in the host list after its first report.`, tells
   you that making it a member of an existing pool is done from **Settings** → **Members**
   with **Add host to pool** (see [Join a host to a pool](join-a-pool.md)), and clears the
   password field. `<pool>` is the pool the host reports: a new pool for a new host, or the
   host's existing pool when it already belonged to one.
   The host then trusts exactly the certificates in Controller's `-tls-cert` file when it
   connects to Controller. With the server certificate followed by its issuing CA, the host
   accepts a renewed server certificate from that CA. A file holding only the server
   certificate pins that certificate: after replacing it, revoke and re-enroll each host (step 8).
   The chained-file case (a server certificate followed by its separate issuing CA) was
   exercised in the Phase 2 gate run on `0.2.10~gate4`: the host enrolled and reported fresh.
4. If the Controller cannot reach the host directly (an isolated management network, a
   firewalled interface, or a host behind NAT), expand **Manual enrollment** inside the
   same panel instead. Select **Generate enrollment token**. Treat the token as a secret:
   keep it out of shell history, logs, screenshots, and shared notes. It expires after ten
   minutes, is bound to this site, and is usable for one host identity. Make the matching
   `nex-core-agent` binary available on the selected host (the commands below assume it is
   on your `PATH`), then save the token in a private file readable only by the agent
   operator. Replace the address and file paths below with your lab values:

   ```sh
   nex-core-agent enroll \
     --controller https://controller.example.test:8443 \
     --ca-file controller-ca.pem \
     --token-file enrollment-token \
     --state-dir agent-state
   nex-core-agent status --state-dir agent-state
   nex-core-agent run --state-dir agent-state
   ```

   `enroll` verifies Controller's HTTPS certificate and creates the host identity locally.
   When `--controller` is an address the certificate does not name (a bundled appliance
   deployed with DHCP names only its hostname), add `--server-name` with the hostname the
   certificate carries; the agent keeps it for every later connection.
   Keep `agent-state` across restarts. Remove the secret token file after successful
   enrollment. The agent connects outbound; it does not require an inbound management API.
5. After enrollment the site contains a new pool named after the host. Choose that pool in the
   scope menu; **Settings** → **Overview** (`#/settings/overview`) shows **Pool** with the pool
   name and **Master** with the hostname, and the settings footer reads
   `<pool> · master <host>`. Select **Hosts** in the rail: the host's row shows the **Master**
   role badge (the **Member** badge is for hosts joined to an existing pool), its status, IP
   address, CPU and memory bars, VM count, **Nex Core** version and **Uptime**. Select the row to
   open the host panel. Its **Overview** tab shows the hardware model, network ports, disks,
   memory, the VMs on the host, the properties (IP address, CPU, **Nex Core**, **Uptime**,
   **Fencing**, **Maintenance**, host identity, last connection and inventory observation times)
   and **Health checks** with each check's last observed result. When the host reports a newer
   release than it runs, an **Image update** row shows a subtle **Update available** badge (or
   **Update staged** / **Update pending reboot** once the host has staged or armed one); the Hosts
   list, the pool **Members** list and the Overview placement cards show the same badge. It
   comes only from the host's own report: the Controller does not look for releases, so a host
   that has not reported its image update state shows nothing, and a host that is not connected
   shows its last report marked **(last report)**. Updating is done under
   [software updates](software-updates.md). The **Networking** tab shows observed interfaces and the **Storage**
   tab observed disks; these are read-only observations. The **Activity** tab shows recorded
   changes for the host. Press Escape or select the close button to return to the list.
   A stale host's **Overview** tab keeps a warning above the last reported values.
   Select **Refresh inventory** in the panel's quick actions and follow its durable task in
   **Tasks** (rail, or the activity pill in the top bar) to its reported outcome. A task that
   fails between refreshes is also announced in the notice bar, naming the task. The console
   reloads Controller's records every ten seconds; **Refresh inventory** sends the existing
   read-only request to the selected host. Neither changes VM power.
   You do not need to reload the browser after an action. An accepted request can still be
   running on Core; watch its reported task outcome. While visible, **Members**, host
   **Networking**, and clustered storage status also check for changes every ten seconds,
   including when the previous report showed no work in progress. Returning to the browser
   tab or reconnecting the browser triggers a fresh read of these views and the inventory.
   Open host hardware details refresh every minute and after a successful **Refresh inventory**
   task. Background reads preserve form entries and action errors; a failed read does not
   establish that a host operation failed or that retrying a destructive action is safe.
   If the Controller update request loses its response, the Updates page checks its status
   without sending the request again. If no completed result appears, select **Check** before
   deciding whether to try again.
   A fresh heartbeat alone does not prove every capability is healthy. By default the agent
   reports every ten seconds, and Controller marks a connection stale after thirty seconds.
   Read **Health checks** together with the connection and observation timestamps.
   **Overview** → **Needs attention** lists each unhealthy or stale host with Core's public
   reasons and links to its panel; it reads
   `No reported issues in the available inventory. Check Backups for run history.` when nothing needs attention.
   Stale data and revoked management access cannot establish whether workloads are running.
   Open **Events** in the rail for recorded health changes and administrative actions, kept
   separately from asynchronous tasks. This development view shows the newest 1,000 task and
   event records without pagination; older records remain stored.
6. To restart either application, stop it normally and start it with the same configuration
   and persistent state. Let observations refresh after reconnect. An interrupted or unknown
   task outcome needs reconciliation; do not treat a connection timeout as proof of failure.
7. To rotate a host certificate, first stop that host's agent process, then run:

   ```sh
   nex-core-agent rotate --state-dir agent-state
   nex-core-agent status --state-dir agent-state
   nex-core-agent run --state-dir agent-state
   ```

   Rotation preserves the host identity. If the response is lost, retry `rotate` with the
   same state directory within ten minutes, while the previous certificate remains accepted;
   do not remove the pending identity state. After that overlap expires, use the deliberate
   revoke/re-enroll procedure below. Certificates last ninety
   days; rotation is manual, so use `status` to check expiry and rotate before that date.
8. To revoke a host, open that exact host's panel from **Hosts**, select **Revoke access** in
   its quick actions, type its hostname, then select **Confirm revoke access**.
   Revocation withdraws its management credential; it does not stop its VMs or erase disks.
   For deliberate re-enrollment, select **Re-enroll** in the same host's quick actions (it is
   greyed out until the host is revoked): the same Add host dialog opens,
   titled **Re-enroll** with the host's name. If the host is reachable, enter its address
   and root password and use **Connect** / **Confirm and add host** as in step 3 — the
   revoked host keeps its existing pool, and Controller includes the host's ID automatically
   so the push targets that exact identity. If the host is not reachable from Controller,
   expand **Manual enrollment** and select **Generate enrollment token** on that host to
   obtain a token targeting it (the token is bound to the pool's site and the host keeps
   its existing pool), stop its agent, and run the enrollment command again with
   `--replace-host` and the exact existing host ID:

   ```sh
   nex-core-agent enroll \
     --controller https://controller.example.test:8443 \
     --ca-file controller-ca.pem \
     --token-file enrollment-token \
     --state-dir agent-state \
     --replace-host HOST_ID
   nex-core-agent run --state-dir agent-state
   ```

   Use the original Controller address, CA file contents, and state directory. A generic
   new-host token is not a replacement token. Verify the existing host returns with fresh
   observations and remove the token file after success.

## Expected result

The site contains one automatically created pool named after the host, whose **Pool master**
is that host, with one stable host identity and timestamped observations. Enrolling another
host creates another pool; it does not join the first.
Restarting either process retains membership and task history. Successful reconnect supplies
fresh observations; failed or timed-out checks remain distinguishable from lost connectivity.
A successful refresh means its read-only observation request completed, not that every host
capability passed or a VM was started. Revoked hosts cannot continue authenticated reporting.

## Troubleshooting

- **Sign-in fails:** check the original bootstrap username and password. Sessions expire after
  30 minutes without an authenticated state-changing request, with an eight-hour absolute
  limit. Read-only polling does not renew the session. The security update requires another
  sign-in; existing password hashes remain valid. See [web-access limits](secure-web-access.md)
  for login throttling and session capacity. This build has no password-reset or additional-user workflow;
  changing the bootstrap password file does not update an existing administrator.
- **TLS verification fails:** check the HTTPS name against the certificate, the host clock,
  certificate expiry, and the trusted CA file. Correct the trust/configuration problem;
  do not use an insecure TLS bypass. Existing agent state intentionally rejects a changed
  Controller address or CA file.
- **Enrollment fails or its response is lost:** retry with the same state directory and token
  while it is valid so the persisted request identity can be reconciled. During a replacement,
  Core keeps the prior working credential and the same pending CSR until the new credential is
  accepted, so do not delete or recreate the state directory between attempts. If the error says
  the state was published but durability is uncertain, do not immediately submit another
  enrollment: inspect the host's current local enrollment status and refresh its Controller
  inventory first, then retry only after those observations show which identity is active. If
  the token has expired, inspect Controller membership before issuing another token. Never clone
  the state directory to a second host to make enrollment succeed.
- **Add host: "host certificate fingerprint does not match":** the certificate presented
  during the add-host connection no longer matches the one you confirmed after **Connect**
  (the host was reimaged, its certificate rotated, or you are pointed at a different
  machine). Select **Cancel**, re-run **Connect**, and compare the new fingerprint against
  the host's own console output before confirming again. Controller aborts the connection
  at the TLS handshake, so the root password is never sent when this happens.
- **Add host: "host rejected the root password":** re-check the host's current OS root
  password; it may have changed since a prior attempt. Repeated failures are rate-limited
  by the host's own local login (429), which passes its message through unchanged.
- **Add host: "this host belongs to a pool whose master has not reported it yet" (409):** this is
  a **wait, not a failure**, and the dialog says so: nothing has failed, your entries are kept, and
  **Confirm and add host** is a one-click retry. It happens when you add a pool *member* to a
  replacement Controller before its pool **master** has reported the member. Connect the pool
  master first, or simply try again — usually within one report interval. The host's own words are
  kept under the message for diagnosis. (Earlier builds answered this with a `502` that read like a
  broken host.)

  **Adding the member first now works too**, and it used to deadlock. If a member reconnects to a
  replacement Controller before the master does, that member's report is what the Controller has,
  and it names the real pool master — so the master is accepted when it enrols, and the pool row
  moves to it on its own first report. Either order converges with nothing to re-enter. The
  Controller still reads only the pool's own record: a member cannot take a pool by claiming to be
  its master, because no host but the recorded master can produce the pointer that would allow it.
- **Add host: 502 "host could not enroll with the controller at …":** the host authenticated
  but could not reach the Controller address you supplied at the field, or it rejected the
  token/CA. Confirm the Controller address is one the host's network can resolve and route
  to, and that no firewall blocks it; then use **Connect** again. No token is left behind
  after this failure.
- **Add host: "already enrolled":** the host is already enrolled with *this* Controller under
  another host record. To replace that identity deliberately, use **Re-enroll** in the quick
  actions of the exact existing host's panel instead of **Add host**. A host enrolled with a *different*
  Controller (for example one you are replacing) is accepted by **Add host**: it takes a new
  identity here, keeps its local state, and its pool settings are adopted.
- **Add host: re-enrollment rejected (409):** re-enrollment by address only succeeds against
  a host that is currently **revoked** and whose pool belongs to the selected site. Revoke
  the host first, or select the correct site, before retrying.
- **Another agent process owns the state directory:** stop the running agent before using
  `status`, `rotate`, or `enroll`. The state lock prevents two local processes using one identity.
- **State directory/file permissions are rejected:** the directory must be private (`0700`)
  and `state.json` a private regular file (`0600`). Keep private keys out of diagnostic bundles.
- **Host is stale or disconnected:** check Controller reachability and both process logs from
  local access. A missed heartbeat means uncertain connectivity, not a stopped host or guest.
  Restore connectivity and wait for fresh observations; monitoring never authorizes VM restart.
- **Tasks says “Reobserved from Core history”:** this row is a historical Core outcome
  adopted after reporting or reconnection. Its displayed time is when Controller re-observed it;
  the original outcome time was not reported. It is not a new request and does not authorize a
  retry by itself.
- **Libvirt or a probe is degraded:** inspect the named capability, its error, and observation
  time. Check local permissions and service availability. A bounded probe timeout is distinct
  from Controller loss; independent reports can continue while that capability is unavailable.
  `run --probe-timeout 3s --probe-interval 10s --heartbeat 10s` shows the defaults; tune these
  only for measured lab needs and keep Controller's `-stale-after` longer than its heartbeat
  interval. Controller uses `-alert-debounce 5s` to require a health change to persist
  five seconds before recording its alert.
  Changing a timeout does not repair the underlying failed capability.
- **Pool master unknown:** **Settings** → **Overview** shows **Master** as **Unknown** when the
  master host record is missing or revoked. The pool and its resources remain; re-enroll the
  same host with **Re-enroll** in its panel's quick actions to restore it. For an intentional authority change, use the
  documented [planned master change or fence-verified promotion](pool-authority-and-recovery.md),
  never Controller replacement or host re-enrollment. Those development workflows remain subject
  to their stated acceptance limits.
- **Confirm and add host reports "Already enrolled":** the message continues
  `Host <id> still trusts a Controller at this address. Use Re-enroll host on that host, or check the Controller address.`
  The host is already enrolled with a Controller at the same address and with the same
  certificate authority, so nothing changed. Use **Re-enroll** in that host's panel, or, when
  connecting a replacement Controller, check that **Controller address** names the replacement.
- **Credential revoked or expired:** use the deliberate revoke/re-enroll procedure for the
  same host. Do not create a second active identity as an automatic recovery shortcut.
- **Controller or agent state is lost:** stop and retain what remains before recovery. There is
  no in-product Controller backup/restore workflow or automatic identity recovery; for a
  host-deployed appliance, the Controller-scoped state (database, identity, TLS keys) is carried
  by the [recovery export and manual restore](deploy-controller-appliance.md#troubleshooting).
  Do not run two Controllers from copied state or copy an agent identity to another host.
- **Replace a failed Controller:** install the new Controller somewhere other than the host
  that ran the failed Controller appliance: either the development setup in steps 1-2, or an
  appliance deployed on a separate clean Nex Core host that is not enrolled, as in
  [Deploy the Controller appliance](deploy-controller-appliance.md) (that appliance route has not
  been walked through for replacement). This build cannot deploy a new appliance on a host that is
  enrolled or already has an appliance deployment, and it cannot remove the old appliance there. First exclude the old
  appliance, as in [Deploy the Controller appliance](deploy-controller-appliance.md#troubleshooting):
  on the host's root console disable its autostart, shut it down, and wait until libvirt reports
  it `shut off`; an unreachable Controller is not proof that it stopped. Keep its disk. The new
  Controller later lists the old appliance as the protected **Controller** VM with **Start**
  available: **do not start it**. Starting it runs two Controllers for the same hosts. Selecting
  **Start** in that VM's panel opens a confirmation that reads "This is a Controller appliance. If another
  Controller now manages this host, starting it runs two Controllers; never do that. Recover it
  from the host-local page instead." Select **Cancel**. Then sign
  in to the new Controller, create a site, and connect
  each pool master with an enrollment token or the **Add host** address-and-root-password
  flow (step 3). Wait for the host to report; each pool returns with its original ID and
  name and its master with its original host ID, because the host keeps that identity and
  presents it at enrollment (a host enrolled before host-owned identity and never re-enrolled
  since presents no pool ID, so it lands in a new pool named after its hostname, which
  receives its resources), and its storage registrations, networks, ISO records and VM
  definitions return with their original names and IDs. The old Controller appliance VM returns too, as the
  protected **Controller** VM on the host that deployed it (observed shut off), and the host's failed or
  interrupted tasks reappear in **Tasks** with their original status and error. Replacing the
  Controller does not stop the host: a task still running when the old Controller went away
  finishes on the host. A task the host's agent was interrupted in (its process died) shows
  as `needs_attention` with *interrupted before completion; retry from a new request*: the
  host will not resume it. A VM creation or edit interrupted after its definition was written
  is checked by the host on its next reports and closes on its own once the definition
  matches. Otherwise check the resource it addressed under **Storage** (with its **ISO library**),
  **Networks** or **VMs**, and submit it again as a new request if it is
  missing. A VM whose automatic start failed shows a separate failed
  *VM created but not started* task; the VM itself is adopted and starts from its
  [Power controls](vm-power.md). With each pool chosen in the scope menu, verify **Storage**,
  (with its **ISO library**), **Networks** and **VMs**, and the host panel's
  **Health checks**: **managed-journal** and **managed-vms** must be healthy. Nothing is skipped
  silently: an entry the new Controller cannot take over shows as a `needs_attention` task
  whose error starts with `not adopted from host report:` and names the reason (an invalid or
  conflicting entry, a reference the host has not adopted, or a VM whose definition is
  missing from libvirt on the host); it turns `succeeded` by itself once the entry is adopted
  on a later report. A `failed` **managed-journal** check names a journal file on the host
  that could not be read; the other entries are still adopted. Limits: sites, administrator accounts,
  audit history, enrollment tokens and the Controller CA are not recovered and must be
  re-created. A pool member returns to its pool only after you also connect that member to
  the new Controller and its master reports it as a ready member; add new members through the
  Controller-driven [join workflow](join-a-pool.md), which asks Core to perform and retain the
  host-side membership operation. Never run the old and new Controller at the same time. If you re-enroll a
  host with the *same* Controller under a new host record instead, its old records stay with
  the revoked host and are not moved; use **Re-enroll**, which keeps the identity.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

The [Controller UI modernization checks](../qa/controller-ui-modernization/checks.md) (2026-09-23) walked the earlier chrome (header menu, task strip, off-canvas tree and narrow toolbar) in headless Chromium against simulated inventory and the phase 1 browser lab; no real host was involved.

The 0.3.2 local artifact lab (removed; see git history) checked
the exact Controller package in a disposable Debian VM: authenticated HTTPS,
service restart/crash recovery, same-version reinstall and reboot preserved a
site's identity. It used the packaged wrapper's configuration paths and API
requests, not a new browser or step-by-step walkthrough of this guide. This does
not establish cross-version upgrades, restored backups or current hosted CI.

Recorded checks and their limits:

- [Phase 1 acceptance](../qa/phase1/acceptance.md) and [Debian/libvirt check](../qa/phase1/debian.md).
- [Add host lab checks](../qa/simple-host-enrollment/lab/checks.md) — step 3 and revoke/re-enroll
  in a nested-KVM lab; the manual fallback and package upgrade were not exercised.
- [Pool master checks](../qa/pool-master/checks.md) and [lab run](../qa/pool-master/lab/checks.md).
- [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md) — `0.2.10~gate5`, 2026-09-17,
  disposable nested-KVM lab, headless Chromium. An appliance deployment was replaced by a
  Controller outside the host, running the gate5 `nex-controller` package binary with a new data
  directory and a `-tls-cert` file holding a server certificate followed by its issuing CA.
  Sign-in, **Create site** and browser **Add host** (step 3) re-adopted the same host, pool,
  storage, network, ISOs, VM identities, specifications and revisions with nothing re-entered;
  failed registrations returned only as failed tasks. A repeated **Add host** showed "Already
  enrolled" and kept it on screen. **Start** on the old appliance from the earlier layout's toolbar,
  Console tab and tree menu each asked for confirmation (and again after a host reboot), and edits and
  creates succeeded afterwards. The repository Controller replacement contract script also
  passed. Step 1 was not walked exactly as written: the binary came from the package rather than
  `make build` and listened on a loopback lab port. The host was added through a lab port forward,
  and the old appliance was shut down from an SSH shell rather than the physical console.
  Same-host appliance redeploy (blocked by the product) and backup restore were not exercised. The
  same replacement also passed in the earlier gate4 run.
- [Pool VLAN network checks](../qa/pool-vlan-networks/checks.md) — Controller replacement with
  real processes and libvirt.
- [Health reasons walkthrough](../qa/2026-09-12-bug-ledger.md), [site selector](../qa/site-selector/browser.md),
  [desktop layout](../qa/controller-desktop-layout/checks.md) and
  [security browser walkthrough](../qa/security/browser.md).
