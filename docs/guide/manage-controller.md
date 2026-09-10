# Set up Nex Controller and enroll a host

## Status

This page covers the **Phase 1 development build** of Nex Controller and the Nex Core
agent. It provides administrator access, Sites → Pools (one master host + members) → Hosts,
secure enrollment,
observed inventory, health reporting, and read-only inventory refresh tasks.
Current Nex Core media includes an embedded agent; deploying the bundled Controller
from the local UI enrolls that host automatically. Other reachable hosts are added from
the Controller with **Add host** (address and root password, fingerprint confirmation);
the manual token procedure below is the fallback for hosts the Controller cannot reach.
Matching development builds add [local storage, existing networks, ISO upload and
VM creation](create-virtual-machine.md); that page has separate prerequisites and
verification evidence. Pools are created automatically: each enrolled host becomes the **pool master** of its
own pool, named after its hostname. Joining a second host to an existing pool as a
member, changing the pool master and ejecting a member are planned for a later phase
and are not available. Central power controls, consoles, backups, migration and HA
remain later features.

Use one Controller process with its SQLite database on local disk. That database is
authoritative only for sites, administrator accounts, audit history, enrollment tokens, the
Controller CA and, once they exist, backup jobs; everything about pools, hosts and VMs is
re-adopted from the hosts (see **Replace a failed Controller** under
[Troubleshooting](#troubleshooting)). This is a development
lab workflow, not a supported production deployment or a hardware compatibility claim.
Use a private management network or VPN and read the current
[web-access security behavior](secure-web-access.md); new security-build verification
is separate from the historical Phase 1 evidence below.

## Prerequisites

- A disposable Linux lab machine for Controller and an explicitly selected Debian 13
  host for agent evaluation; retain local recovery access to the host.
- Matching Controller, agent, and web assets from the same development build; see the
  [development instructions](../development.md). Keep Controller outside the host being installed.
- A Controller HTTPS address that the host can reach, a server certificate valid for that
  address, its private key, and a trusted copy of its issuing CA certificate. Obtain trust
  material through an authenticated channel; do not bypass certificate verification.
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
   Choose **Sign in** with the administrator account. The desktop layout has a menu row and
   a fixed command toolbar across the top, a **Resources** pane on the left, and details for
   the selected resource on the right. The toolbar always shows the same buttons, in groups:
   **Back** and **Forward** move through pages you visited; **Create site**, **Add host** and
   **New VM** create resources; **Start**, **Shut down**, **Reboot** and **Force stop**
   control the selected VM (see [Control VM power](vm-power.md)); **Refresh view** at the
   right end reloads Controller's records. A button that does not apply to the selected
   resource is greyed out, and hovering it shows why, for example
   `Select a virtual machine in Resources.` Select **Create site** and enter its name. Do not
   look for a pool-creation action: pools are created automatically when a host enrolls.
   Open the site dropdown directly below **Resources**. Its **Search sites** field
   narrows the choices inside the menu; choose a result to switch sites. The closed
   control shows only the selected site. The resource tree starts with that site's pools,
   and **Site overview** opens the selected site. A site without hosts shows
   **Add your first host** with an **Add host** button.
   Pools contain hosts and VMs; the pool master host is suffixed **· master** in the tree. Expand or collapse pools using their arrows;
   select a resource name to open it. **Search resources** filters within the selected
   site. Opening a direct pool, host or VM link selects its owning site automatically.
   Clear a search to restore its choices; switching sites changes the tree's scope.
   In the site menu, use Up/Down and Enter to choose a result. Escape closes the
   menu and returns focus to the site control; Tab moves to the next control.
   The **File** menu repeats the creation actions (**Create site**, **Add host**, **New VM**)
   for the selected scope; **View** opens the overview or task history and refreshes the
   displayed inventory; **Host** holds the actions for the selected host: **Refresh
   inventory**, **Re-enroll host** and **Revoke access**. Menu items that do not apply to
   the current selection are greyed out.
   Use Tab to reach menus and controls, Enter to open a menu, and Escape to close it.
   On narrow screens the resource pane is above the details area.
3. With the site selected, choose **Add host** (toolbar, File menu or the empty-state button).
   The form is titled **Add host to** followed by the site name; it explains that the new host
   becomes the master of a new pool in this site. Enter:
   - **Host address** — the host's IP or hostname (`10.0.0.5`), with an optional `:port`
     (default `9443`, the `nex-core-local` listener).
   - **Root password** — the host's current OS root password. It is sent once, over a
     certificate-pinned connection, and is never stored, logged, or echoed back.
   - **Controller address** — prefilled with this browser's address; edit it if the host
     must reach the Controller by a different name or IP. The help text under the field
     is a reminder that the host, not your browser, must be able to open this address.

   Select **Connect**. Controller opens an unauthenticated TLS handshake to the host and
   shows the certificate's **Address**, **Certificate fingerprint** (`sha256:…`),
   **Subject** and **Expires** date. Compare the fingerprint against the host's own console or install
   output before continuing — this confirmation is the only trust check before the root
   password is used, so do not skip it. Select **Confirm and add host** to proceed, or
   **Cancel** to change the address. On success the panel shows
   `Host <id> enrolled as master of a new pool; it appears in the tree after its first report.`
   (A re-enrolled host reads `re-enrolled and keeps its pool` instead.)
   and the password field is cleared.
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
   Keep `agent-state` across restarts. Remove the secret token file after successful
   enrollment. The agent connects outbound; it does not require an inbound management API.
5. After enrollment the site contains a new pool named after the host. Select that pool:
   its **General** panel shows **Pool master** with the hostname. Select the enrolled
   hostname; its header shows the **Pool master** role badge (the **Member** badge is
   reserved for hosts joined in a later phase). **General** shows **Host overview**
   and health checks: identity, last contact and observation times, CPU/RAM and OS/agent
   version. Select **Networking** for observed interfaces or **Storage** for observed
   disks; these are read-only observations. **Activity** shows recorded changes for the
   selected resource. Selecting another resource returns to **General**.
   Connection warnings remain visible on the host's General, Networking and Storage views.
   Open the **Host** menu, select **Refresh inventory** and follow its durable task in
   **Task history** to its reported outcome. **Recent tasks** at the bottom also shows up to five tasks for the
   selected scope; select its heading to collapse or expand it. **Refresh view** reloads
   Controller's records; **Refresh inventory** sends the existing read-only request to
   the selected host. Neither control changes VM power.
   A fresh heartbeat alone does not prove every capability is healthy. By default the agent
   reports every ten seconds, and Controller marks a connection stale after thirty seconds.
   Read **Last observed health** together with the connection and observation timestamps.
   Open **Task history** for **Activity and health history**, which records observed health
   changes and administrative actions separately from asynchronous tasks. This development
   view shows the newest 1,000 task and activity records without pagination; older records
   remain stored.
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
8. To revoke a host, select that exact host in Controller, choose **Revoke access** from the
   **Host** menu, type its hostname, then select **Confirm revoke access**.
   Revocation withdraws its management credential; it does not stop its VMs or erase disks.
   For deliberate re-enrollment, select **Re-enroll host** from the **Host** menu (it is
   greyed out until the host is revoked): the same Add host panel opens,
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
  while it is valid so the persisted request identity can be reconciled. If the token has
  expired, inspect Controller membership before issuing another token. Never clone the state
  directory to a second host to make enrollment succeed.
- **Add host: "host certificate fingerprint does not match":** the certificate presented
  during the add-host connection no longer matches the one you confirmed after **Connect**
  (the host was reimaged, its certificate rotated, or you are pointed at a different
  machine). Select **Cancel**, re-run **Connect**, and compare the new fingerprint against
  the host's own console output before confirming again. Controller aborts the connection
  at the TLS handshake, so the root password is never sent when this happens.
- **Add host: "host rejected the root password":** re-check the host's current OS root
  password; it may have changed since a prior attempt. Repeated failures are rate-limited
  by the host's own local login (429), which passes its message through unchanged.
- **Add host: 502 "host could not enroll with the controller at …":** the host authenticated
  but could not reach the Controller address you supplied at the field, or it rejected the
  token/CA. Confirm the Controller address is one the host's network can resolve and route
  to, and that no firewall blocks it; then use **Connect** again. No token is left behind
  after this failure.
- **Add host: "already enrolled":** the host is already enrolled with *this* Controller under
  another host record. To replace that identity deliberately, use **Re-enroll host** on the
  exact existing host record instead of **Add host**. A host enrolled with a *different*
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
- **Libvirt or a probe is degraded:** inspect the named capability, its error, and observation
  time. Check local permissions and service availability. A bounded probe timeout is distinct
  from Controller loss; independent reports can continue while that capability is unavailable.
  `run --probe-timeout 3s --probe-interval 10s --heartbeat 10s` shows the defaults; tune these
  only for measured lab needs and keep Controller's `-stale-after` longer than its heartbeat
  interval. Controller uses `-alert-debounce 5s` to require a health change to persist
  five seconds before recording its alert.
  Changing a timeout does not repair the underlying failed capability.
- **Pool master unknown:** the pool's **General** panel shows **Unknown** when the master
  host record is missing or revoked. The pool and its resources remain; re-enroll the same
  host with **Re-enroll host** to restore it. Changing a pool's master is not available
  in this build.
- **Credential revoked or expired:** use the deliberate revoke/re-enroll procedure for the
  same host. Do not create a second active identity as an automatic recovery shortcut.
- **Controller or agent state is lost:** stop and retain what remains before recovery. There is
  no in-product Controller backup/restore workflow or automatic identity recovery; for a
  host-deployed appliance, the Controller-scoped state (database, identity, TLS keys) is carried
  by the [recovery export and manual restore](deploy-controller-appliance.md#troubleshooting).
  Do not run two Controllers from copied state or copy an agent identity to another host.
- **Replace a failed Controller:** deploy or install a new Controller
  ([deploy the appliance](deploy-controller-appliance.md)), sign in, create a site, then connect
  each pool master with an enrollment token or the **Add host** address-and-root-password
  flow (step 3). Wait for the host to report; each pool returns with its original ID and
  name and its master with its original host ID, because the host keeps that identity and
  presents it at enrollment, and its storage registrations, networks, ISO records and VM
  definitions return with their original names and IDs. The Controller appliance VM itself returns too, as the
  protected **Controller** VM on the host that deployed it, and the host's failed or
  interrupted tasks reappear in **Tasks** with their original status and error. Verify each
  pool's **Storage**, **Networking**, **ISO library** and **Virtual machines** tabs and the
  host's checks: **managed-journal** and **managed-vms** must be healthy. Nothing is skipped
  silently: an entry the new Controller cannot take over shows as a `needs_attention` task
  whose error starts with `not adopted from host report:` and names the reason (an invalid or
  conflicting entry, a reference the host has not adopted, or a VM whose definition is
  missing from libvirt on the host); it turns `succeeded` by itself once the entry is adopted
  on a later report. A `failed` **managed-journal** check names a journal file on the host
  that could not be read; the other entries are still adopted. Limits: sites, administrator accounts,
  audit history, enrollment tokens and the Controller CA are not recovered and must be
  re-created; multi-host pool membership is not restored (joining members is not available
  in this build). Never run the old and new Controller at the same time. If you re-enroll a
  host with the *same* Controller under a new host record instead, its old records stay with
  the revoked host and are not moved; use **Re-enroll host**, which keeps the identity.

## Verification

**Replace a failed Controller (troubleshooting) — verified with real processes and libvirt:**
the [pool VLAN network checks](../qa/pool-vlan-networks/checks.md) record runs with real
`nex-controller` and `nex-core-agent` processes, including one in a Debian 13 container with
real libvirt and KVM: storage, a network, an ISO record and a stopped VM created through
Controller A all came back on a brand-new Controller B with the same IDs and names after one
ordinary enrollment, with no command dispatched and no duplicates, and new work on the adopted
resources succeeded. The appliance return, the failed task in **Tasks** and the
`needs_attention` record for an unadoptable entry are covered by the Go checks named in the
[API reference](../API.md#managed-vm-creation) and by the same replacement script, which
seeds a deploy journal and a failed journal entry on the test host before Controller B starts
(2026-09-10; see the change record for the run). Not walked through: the browser **Add host**
flow against a replacement Controller, the packaged agent on an installed host, and a real
appliance deployment followed by a Controller replacement.

**Pool master change verified (2026-09-09):** the [pool master checks](../qa/pool-master/checks.md)
(simulated host, Chromium) and the [real lab run](../qa/pool-master/lab/checks.md) (real appliance,
real agent enrollment through libvirt, in-place migration and a fresh deployment) cover steps 2, 3,
5 and 8: the site empty state, the **Add host** form with a site-bound token, automatic pool
creation with the **Pool master** panel and role badges, and re-enrollment keeping the host's
pool. Joining a member, changing the master and ejecting are not implemented.

**Add host by address (step 3) — verified in a real lab:** the
[add host lab checks](../qa/simple-host-enrollment/lab/checks.md) record a Chromium walkthrough
of this panel against the disposable nested-KVM lab: probe and fingerprint confirmation against
the host's real certificate, wrong fingerprint, wrong password, already-enrolled and
non-revoked re-enrollment errors, revoke and re-enroll through the panel with identity and pool
retained, a fresh enrollment that created its pool with the host as master, a local service
restart, and audit/log hygiene. Package upgrade, physical hardware and the manual fallback were
not exercised there; see the checks page. The
[implementation plan](../plans/2026-09-09-simple-host-enrollment.md#verification-status) lists
the Go, contract and simulated browser checks.

The [site selection walkthrough](../qa/site-selector/browser.md) verifies the
searchable selector and pool-root tree using an explicitly simulated 500-site
fixture, including deep links, empty/no-match states, keyboard and narrow layouts.
The [combined UI report](../qa/host-isos/checks.md) records the matching build,
existing Phase 1 regression and local Controller deployment evidence.

The [Controller desktop layout walkthrough](../qa/controller-desktop-layout/checks.md)
records verification of the current resource tree, menus, detail views, forms and
responsive layout. Historical captures below show the previous layout.

[Deployment usability checks](../qa/appliance/deployment-usability.md) record the current
DHCP/password/power UI changes, simulated browser checks and artifact availability limits.

The [security browser walkthrough](../qa/security/browser.md) separately verifies the new
cookies, sign-out, throttling, CSP and accelerated idle-expiry recovery against real HTTPS
services. Its virtualization is simulated; new package acceptance is tracked on the
[security access page](secure-web-access.md).

See the [Phase 1 walkthrough and verification limits](../qa/phase1/acceptance.md).
The local process/browser walkthrough passed bootstrap/login, site/pool creation
(pools were created by hand in that build),
enrollment, inventory refresh, revoke confirmation, explicit re-enrollment, stale/error
recovery, keyboard operation, and desktop/360px layouts. Its libvirt failure/stall inputs
were simulated; the browser accepted a temporary self-signed lab certificate while the
agent and API client verified trust. A separate local command walkthrough exercised bootstrap, enrollment,
`status`, default-timing reporting, rotation, and both process restarts with native host
observations and verified HTTPS. Site/pool/token creation in that command walkthrough
used the API of that earlier build, not browser interaction.

The [Debian/libvirt check](../qa/phase1/debian.md) separately exercised Debian 13 userspace,
actual libvirt availability, failure, stall, recovery, and continued observations in a disposable
container. It created no guests and shares the workstation kernel. Browser trust provisioning,
real systemd deployment, KVM acceleration, guest survival, physical storage, and
installer-to-agent integration remain outside this evidence.
