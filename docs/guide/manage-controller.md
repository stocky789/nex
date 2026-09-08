# Set up Nex Controller and enroll a host

## Status

This page covers the **Phase 1 development build** of Nex Controller and the Nex Core
agent. It provides administrator access, Sites → Pools → Hosts, secure enrollment,
observed inventory, health reporting, and read-only inventory refresh tasks.
The standalone Nex Core installer alpha does not install or enroll this agent automatically.
VM management, consoles, managed storage, backups, migration, and HA remain later features.

Use one Controller process with its SQLite database on local disk. This is a development
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
     -admin-username admin \
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
   Choose **Sign in** with the administrator account. The desktop layout has a command
   toolbar across the top, a **Resources** pane on the left, and details for the selected
   resource on the right. Select **Create site**, enter its name, then select
   **Create pool** within that site and name the pool.
   Expand or collapse sites and pools using their disclosure arrows; select their names
   to open them. **Search resources** filters the tree by site, pool or host name and
   keeps matching hosts under their site and pool. Clear the search to see everything.
   The **File** menu also provides creation/enrollment actions for the selected scope;
   **View** opens the overview or task history and refreshes the displayed inventory.
   Use Tab to reach menus and controls, Enter to open a menu, and Escape to close it.
   On narrow screens the resource pane is above the details area.
3. Select the intended pool, **Add host**, then **Generate enrollment token**. Treat the token as a secret:
   keep it out of shell history, logs, screenshots, and shared notes. It expires after ten minutes
   and is usable for one host identity; it does not grant access to another pool.
4. Make the matching `nex-core-agent` binary available on the selected host
   (the commands below assume it is on your `PATH`), then save the token in a private file
   readable only by the agent operator. Replace the address and file paths below with your lab values:

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
5. Return to the pool and select the enrolled hostname. **General** shows **Host overview**
   and health checks: identity, last contact and observation times, CPU/RAM and OS/agent
   version. Select **Networking** for observed interfaces or **Storage** for observed
   disks; these are read-only observations. **Activity** shows recorded changes for the
   selected resource. Selecting another resource returns to **General**.
   Connection warnings remain visible on the host's General, Networking and Storage views.
   Select **Refresh inventory** and follow its durable task in **Task history** to its
   reported outcome. **Recent tasks** at the bottom also shows up to five tasks for the
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
8. To revoke a host, select that exact host in Controller, choose **Revoke access**, type
   its hostname, then select **Confirm revoke access**.
   Revocation withdraws its management credential; it does not stop its VMs or erase disks.
   For deliberate re-enrollment, select **Re-enroll host** then **Generate enrollment token**
   on that host to obtain a token targeting it, stop its agent, and run the enrollment command again with `--replace-host` and the exact existing
   host ID:

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

The selected pool contains one stable host identity with timestamped observations.
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
- **Credential revoked or expired:** use the deliberate revoke/re-enroll procedure for the
  same host. Do not create a second active identity as an automatic recovery shortcut.
- **Controller or agent state is lost:** stop and retain what remains before recovery. This
  phase does not provide a controller backup/restore workflow or automatic identity recovery.
  Do not run two Controllers from copied state or copy an agent identity to another host.

## Verification

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
The local process/browser walkthrough passed bootstrap/login, site/pool creation,
enrollment, inventory refresh, revoke confirmation, explicit re-enrollment, stale/error
recovery, keyboard operation, and desktop/360px layouts. Its libvirt failure/stall inputs
were simulated; the browser accepted a temporary self-signed lab certificate while the
agent and API client verified trust. A separate local command walkthrough exercised bootstrap, enrollment,
`status`, default-timing reporting, rotation, and both process restarts with native host
observations and verified HTTPS. Site/pool/token creation in that command walkthrough
used the API, not browser interaction.

The [Debian/libvirt check](../qa/phase1/debian.md) separately exercised Debian 13 userspace,
actual libvirt availability, failure, stall, recovery, and continued observations in a disposable
container. It created no guests and shares the workstation kernel. Browser trust provisioning,
real systemd deployment, KVM acceleration, guest survival, physical storage, and
installer-to-agent integration remain outside this evidence.
