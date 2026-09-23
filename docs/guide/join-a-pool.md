# Join a host to a pool

## Status

Development product 0.4.8. Add an enrolled Nex Core host to another host's pool from Nex
Controller: **Settings** → **Members** (with the pool chosen in the top-bar scope menu) →
**Add host to pool**. The pool master owns membership and
pool-wide configuration (shared storage, guest VLANs, and ISO library entries on those shared
repositories); the joining host performs the join itself and reports its own progress, and
Controller only asks and displays. The host-local procedure below remains available as the
recovery path when Controller is unavailable or the two hosts must be joined by hand.

Known limits in this release:

- **A join is not undone; it is reversed by an eject.** A host can be removed from a pool, and the
  pool master can be changed, from **Settings** → **Members** → **Actions** on the host's row — see
  [Pool authority and recovery](pool-authority-and-recovery.md). An ejected host becomes the master
  of its own new single-host pool rather than returning to the state it was in before the join, and
  putting it back is a new join. Automatic master succession, HA, live migration and automatic
  restart remain unavailable. Do this only in a disposable lab.
- On a same-pool rejoin, Nex Core reapplies shared networks, repositories and ISOs using their
  existing IDs only when it can prove that an earlier local removal was part of that host's eject.
  Do not manually edit host records to force resource recovery; the normal retry path is safe.
- A host that has any virtual machine, or any storage that is not local to it, is refused.
- The Controller-driven workflow has not yet been walked through on real hardware; see
  [Verification](#verification) before relying on these steps.

Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).

## Prerequisites

- Two enrolled Nex Core hosts in the same site, on matching development builds, both reporting
  the `pool-join-v1` capability. The Controller refuses the join and names the upgrade otherwise.
- The joining host must currently be the master of its own single-host pool (every enrolled host
  is), with no managed VMs and no remote storage registered.
- Both hosts connected and fresh in Controller: the master must answer within 45 seconds, and the
  joining host must be reachable to receive its command.
- Network reachability from the joining host to the master's pool-peer HTTPS listener (default
  port `9444`), and from the master back to the joining host's peer address. The browser's own
  reachability proves nothing about either.
- An administrator session in Controller.
- For shared VM storage after joining: an existing NFSv4 export or SMB3 share that both hosts can
  reach and that meets the storage prerequisites in
  [Add VM storage repositories](storage-repositories.md). Local OpenZFS and iSCSI remain
  single-host storage. Pool guest VLANs registered on the master also reach members that have the
  same uplink NIC names (`nex0`, `nex1`, …).

## Steps

1. Choose the pool that the host should join in the top-bar scope menu, then select **Settings**
   in the rail and **Members** in the settings navigation (`#/settings/members`). A pool status
   strip (**Authority generation**, **Fencing**, **Host fencing** and, where the pool has one, its
   **Watchdog cluster**) sits above one row per host the pool master reports. Each row shows the
   host name, its role (**Pool master** or **Member**), its Nex Core version (with **Update** when a
   newer one is available), **Joined** (when the pool master admitted it) and an **Actions** menu,
   then a strip of status checks: **Connection**, **Membership**, **Maintenance**,
   **Authority copy** (members only), **Convergence** and **Fencing**, each with one word such as
   **OK**, **Stale** or **Unknown**. **Details** at the end of the strip opens the full text for
   each check and the peer address. Maintenance is covered in
   [Put a pool host into maintenance](host-maintenance-mode.md); the authority copy each member
   holds is covered in [Pool authority and recovery](pool-authority-and-recovery.md). Both read
   *unknown* while the pool master is not fresh.
2. Select **Add host to pool**. The dialog lists the other enrolled hosts at this site; each is
   the master of its own single-host pool until it joins. A host that cannot join is shown as
   **cannot join** and cannot be selected. A host that has not enrolled yet is not listed: the
   dialog says *There is no other enrolled host at this site to add*. Enroll it first with
   **Add host** in the top-bar scope menu, then return here once it reports.
3. Select a host and read its compatibility preview. Each row is a check (connection, version,
   capability, membership, VMs, storage, uplink, CPU) with a result:
   - **compatible** — nothing to do.
   - **will not converge** — the host can still join; a pool setting that cannot be applied there
     (typically a pool VLAN whose uplink NIC name the host does not have) is reported against it
     afterwards.
   - **refuses join** — the join is refused. Fix the cause first.
   The preview is composed from what the hosts themselves report. It is not a promise: Nex Core
   checks every refusal again when the request is submitted.
4. Enter the **Peer address** of the joining host: the bare `https` address, with port and no
   path, that the pool master will use to reach it — for example `https://192.0.2.12:9444`.
5. Select **Add host to pool** to confirm. Controller asks the master for a single-use invitation
   and hands it to the joining host as one durable command. You are not asked for the pool
   master's address: the master records its own peer address, taken from the address its pool-peer
   listener is bound to, the first time a host joins, and every later join reuses it.
6. Watch the joining host's own reported progress in the members table: *Joining · next step: …*,
   then **Join complete**, **Needs attention** with the host's message, or **Join failed** with
   the reason. The page refreshes itself while a join is in progress. Closing the browser, or
   losing Controller entirely, does not stop or change the join.
7. Confirm per-member convergence before placing a VM on the new member. Pool-wide configuration
   already on the master is pulled by the member after join: NFS/SMB (and eligible clustered
   iSCSI) storage, VLAN guest networks, and ISO library entries on those shared repositories. The
   member materializes each locally, verifies access, then reports per-host status. Register
   additional NFS/SMB storage or VLANs on the **master** only, and rename or remove a pool VLAN or
   a shared ISO on the master too; members pull the same records and converge to a removal on
   their next pass. Select shared storage, a VLAN, or a shared ISO for a member VM only after that
   member's access row is fresh and healthy.

## Expected result

The member has a Core-issued pool peer identity and the master reports it in membership
inventory, with role **Member** and a fresh connection. For eligible NFS/SMB storage, VLANs and
shared ISOs, Controller shows per-host access observations. A member cannot use those resources
when generation, identity, access or lock proof is missing, stale or failed. Local storage,
OpenZFS, exclusive iSCSI and host-local bridges stay on the host that owns them.

The membership lives on the pool master, not in Controller. A replacement Controller connected to
the same hosts reads the same membership, names, peer addresses and any unfinished join back from
them, with nothing to re-enter.

## Troubleshooting

Messages below are the exact text the interface shows.

- **“adding a host to a pool needs pool-join-v1 on both hosts; upgrade both hosts”** — one side is
  an older build. Upgrade both, or use the host-local recovery path.
- **“only the master of its own single-host pool can join another pool”** — the selected host is
  already a member of a pool, or is a master with members of its own.
- **“joining a pool with managed VMs is not supported; migrate or remove workloads first”** —
  remove the workloads, then retry. Nothing has been started.
- **“joining a pool with existing remote storage is not supported; detach it first”** — detach the
  live remote repository, then retry. A repository already detached from Nex does not block a
  join; the candidate preview and Core use the same rule.
- **“pool master must be fresh and active before a host can join”** / **“the joining host must be
  fresh and active”** — wait for the host to report, or fix its connection. An unreachable host is
  never treated as proof that its VMs have stopped.
- **“the pool master cannot work out its own peer address because it has no default route”** —
  the master's pool-peer listener is bound to every address, so it finds the address other hosts
  reach it on from its default route. Give the master's management network a gateway, then retry.
- **“the pool master's peer listen address is not an address and port”** — the master was started
  with a `--pool-peer-listen` value that is not `host:port`. Correct the flag on the master and
  restart its Nex Core service, then retry.
- **“the pool master did not return a join invitation before the deadline; retry with the same
  request_id”** — the master did not answer within 45 seconds. Nothing was created and no
  membership changed. Select **Add host to pool** again with the same host and addresses: the
  request is repeated, never doubled.
- **Any failure on submit** — the dialog stays open and keeps the same request identity, so
  pressing the button again repeats the same request instead of starting a second join. Only
  editing the host or an address starts a new request.
- **“Needs attention”** — the host **is** a member, but a step after the join (rehoming its local
  resources, or advertising its address) has not finished. Core retries it on its own. Preserve
  the Core state directory and restart the local service if it persists; do not repeat the join
  against another master or rewrite managed journals by hand.
- **“Join failed”** — the join stopped **before any membership change**; the host is still the
  master of its own pool. Fix the reported cause and start again. This is now enforced rather than
  merely intended: only the stages before the master could have admitted the host can end as
  *failed*. Once the host has reached the admission stage, a repeated failure is reported as
  **needs attention**, never as *failed*, because the master commits the membership before its
  reply is delivered — so a lost reply must not be read as "nothing happened".
- **The join keeps retrying after the invitation's 15 minutes.** That is correct and it is how the
  truth becomes knowable again. The 15-minute limit bounds only the **first** redemption. An
  invitation that has already issued a certificate replays past its expiry, returning the same
  certificate to the same host — it admits nothing new and never creates a second member. Let it
  run rather than starting a fresh join.
- **“The join invitation expired before the host collected it; start the join again.”** — the
  invitation was never picked up by the joining host, so Controller ended the operation instead of
  redelivering an empty one. Nothing changed on either host. Select **Add host to pool** again.
- **Master not current** — when the pool master is not fresh, membership, convergence, failure
  detail and join progress are shown as **Unknown** rather than as the last known state, and the
  row details show no peer address. Restore the master's connection; do not read an unknown row as a
  healthy one.
- **Member storage access is pending, stale or failed:** fix the member's server-side
  export/share access, identity, capacity, QEMU permissions or locking support. The member retries
  stored reconciliation after reconnect; wait for its new Core observation rather than registering
  a duplicate repository.
- **Member VLAN is pending or failed:** the member needs the same uplink NIC name the VLAN uses
  (`nex0`, `nex1`, …). Fix the uplink or the switch trunk, then wait for the member to retry
  rather than registering the VLAN again on the member. The member re-checks the uplink on every
  pull pass (ten seconds on a packaged host), so a fix to the uplink, its type or its VLAN device
  clears the check on the next pass. If the member reports a failure that is **not** about the
  uplink itself, it retries that network at most once an hour, and its reported observation time
  stops advancing until it does — an unchanging observation time on a failed member row means the
  member is waiting for that retry, not that it just re-checked. The member's
  **pool-network-reconciliation** and **pool-media-reconciliation** health checks show whether
  its last pull from the master succeeded. A member re-creates a pool VLAN bridge that was
  removed by hand on its next pass, for as long as the master still lists that network; to take
  the network away from every host, remove it on the master instead — see
  [Rename or remove a pool VLAN network or an ISO](remove-pool-network-or-iso.md). The same
  applies to a shared ISO. A member refuses a removal one of its own VMs still uses and says so in
  the same two health checks, and a member that cannot reach the master, or receives a partial
  list, removes nothing. A master that cannot read its own managed journal whole answers the pool
  network and ISO lists with `503 pool resource list unavailable: managed journal is incomplete`,
  so no member removes anything until the master is repaired.
- **Shared ISO missing on a member:** the ISO must live on pool-shared storage the member has
  already mounted. Wait for media reconciliation after storage access is healthy.
- **iSCSI or OpenZFS is missing on the member:** expected. These backends remain local or
  single-writer and are not copied to pool members.

### If Controller is lost during a join

Nothing is lost and nothing needs re-entering.

- Lost **before** the host was asked to join: the invitation expires unused within 15 minutes and
  no membership changed. Repeat the workflow when Controller is back.
- Lost **after** the host was asked to join: the joining host completes the join by itself and
  reports the result when it reconnects. A rebuilt Controller re-adopts the pool, its membership
  and any unfinished join from the hosts.
- To check a host while Controller is unavailable, sign in to that host's local HTTPS service and
  read `GET /api/local/v1/pool`: it answers the pool, whether the host is master or member, its
  membership record and any staged join, from the host's own state.

### Recovery path: join two hosts without Controller

Use this when Controller is unavailable, or to recover a host by hand. It performs the same peer
exchange; the operator handles the CSR and the one-use offer instead of the Controller.

1. Sign in to each Core's local HTTPS service as `root`, then obtain its CSRF token from the local
   session response. Keep the master and member session cookies separate.
   If the development local UI does not expose this flow, use `curl` against the host-local API;
   for example, the member CSR request is:

   ```sh
   curl --fail --cacert member-ca.pem --cookie member.cookies \
     -H 'Origin: https://core-member.example.test:9443' \
     -H 'X-CSRF-Token: <member-csrf-token>' \
     -X POST https://core-member.example.test:9443/api/local/v1/pool/join-csr
   ```

   Authenticate first with the same local session contract used by the local UI. Do not use
   `--insecure`, place the root password in shell history, or reuse cookies/CSRF tokens between
   the two hosts.
2. On the member, call `POST /api/local/v1/pool/join-csr` with its local session, same HTTPS
   `Origin`, and `X-CSRF-Token`. Save the returned `host_id` and `csr_pem` only long enough to
   submit the next request.
3. On the selected master, call `POST /api/local/v1/pool/join-offer` with
   `{member_host_id, member_csr_pem, master_url}`. `master_url` is the bare HTTPS origin of that
   master's pool-peer listener, for example `https://core-master.example.test:9444`. Save the
   returned `pool_ca_pem` and one-use `offer`; it expires after 15 minutes and is bound to that
   member CSR.
4. On the member, call `POST /api/local/v1/pool/join` with
   `{master_url, pool_ca_pem, offer}`. A successful response returns the member `host_id`, the
   master's `pool_id` and the `join` stage reached. `stage: "rehomed"` with
   `status: "needs_attention"` means the host **is** a member and Core is still finishing the
   local rehome; it is not a failed join. If the peer reply is lost, retry the same request with
   the same offer and CSR: Core returns the original issued member certificate/membership rather
   than creating a second member. Obtain a new CSR/offer only after Core reports that the original
   join was refused or expired.
5. Reconnect both Cores to Controller and confirm the master-reported membership before placing a
   VM on the member. Controller availability is not required for the peer exchange or for a
   running member VM.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

- **G1 partial browser evidence.** Retained desktop and 360 px captures and raw requests bind a
  bootstrap join plus two typed eject/join cycles to accepted operations and terminal success.
  After each cycle, the returned host was ready and fresh with a verified current replica; NFS
  access and the shared ISO converged. A later exact native device capture found the expected VLAN
  device absent despite a fresh healthy network API report. The independently reviewed two-cycle
  rerun then bound each UI eject/join to terminal operations, current verified membership, healthy
  checks, and direct native VLAN bridge/child/port/stanza evidence. This closes repeated resource
  convergence. A separate bounded native run removed the joining member's fixture uplink: the
  typed UI join terminally succeeded, then Core reported the missing-uplink reconciliation failure.
  Restoring the uplink without another join autonomously recovered the member; direct native
  evidence confirmed its bridge, VLAN child, port and owned stanza before a terminal cleanup
  removed them from all participants while leaving their original uplinks present. The initial
  continuation's failed bridge-port assertion was a harness error and its corrected native capture
  is supplemental, not a rerun. Separately retained 1280 px and 360 px captures render the
  selected member's *will not converge* missing-uplink warning; later screenshots are not cited
  for it because they show a compatible preview and a CPU warning instead. This leaves hardware
  coverage unwalked. The implementation is also covered by Go unit
  tests (master invitation and CSR binding at redemption, staged join resumed from every stage,
  precheck refusals, concurrent joins under race detection, broker round trip and deadline, refusal
  matrix, both projections), UI component tests and offline contract checks. The 0.4.2 layout of
  the pool's former Hosts tab, now **Settings** → **Members** (row actions in an **Actions** menu, no master-only-impossible actions on the
  master's row, and the enroll-first empty state) was checked with component tests and simulated
  headless-browser captures of rendered markup, not against a live Controller. Network convergence,
  keyboard operation and hardware remain to be walked through.
- **The invitation at rest.** The single-use invitation and the pool certificate authority are
  scrubbed from the Controller database as soon as the joining host collects the command, and
  unconditionally once the row is 15 minutes old. Because none of the scrub's triggers survives a
  restart, the Controller also sweeps for them from its health tick, at most once a minute — so a
  Controller restarted inside the window still clears them. On the joining host the invitation is
  cleared from its own durable record the moment the admission stage completes; resuming a join
  from any later stage does not need it.
- **Recovery path (host-local steps):** peer unit tests cover member list/pull/access for shared
  storage, VLANs and shared-storage ISOs
  ([QA note](../qa/2026-09-19-pool-member-config-converge.md)). An earlier two-host native
  walkthrough covered join and NFS/SMB access
  ([bug-ledger lab evidence](../qa/2026-09-12-bug-ledger.md#bug-0094-primary-native-pool-walkthrough));
  that evidence predates the staged-join response change in this release, and re-running the lab
  for VLAN materialize and shared ISO adopt on the current build remains open.
- Migration, HA, automatic master succession and physical hardware were not verified. The master
  change and the eject are implemented; their own untested limits are recorded in
  [Pool authority and recovery](pool-authority-and-recovery.md#verification).

[Back to the user guide](README.md)
