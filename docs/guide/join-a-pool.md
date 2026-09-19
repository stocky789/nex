# Join an enrolled Nex Core to a pool

## Status

This development workflow joins an already enrolled Nex Core to another Core's pool. There is no
Controller GUI for joining a pool yet, so use the selected hosts' authenticated local HTTPS APIs.
The pool master owns membership and pool-wide configuration (shared storage, guest VLANs, and ISO
library entries on those shared repositories); members pull that configuration after join and
Controller later shows the Core-reported result. Do this only in a disposable lab. Master
transfer or replacement, member ejection, HA, live migration and automatic restart remain
unavailable. The planned manual, fence-verified promotion and safe member rejoin design is not
an operator procedure in this build; do not attempt it with host-local commands.

## Prerequisites

- Two enrolled Nex Core hosts with matching development builds. Select one as the **master** and
  one as the **member**; do not attempt to join a host that already belongs to a different pool.
- Browser/local-root access to each host's HTTPS local API, its current local-session CSRF token,
  and reachability from member to the master's configured pool-peer HTTPS listener (default port
  `9444`). Verify the master's TLS identity before continuing.
- For shared VM storage after joining: an existing NFSv4 export or SMB3 share that both hosts can
  reach and that meets the storage prerequisites in [Add VM storage repositories](storage-repositories.md).
  Local OpenZFS and iSCSI remain single-host storage. Pool guest VLANs registered on the master
  also reach members that have the same uplink NIC names (`nex0`, `nex1`, …).

## Steps

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
   `{master_url, pool_ca_pem, offer}`. A successful response returns the member `host_id` and the
   master's `pool_id`. If the peer reply is lost, retry the same request with the same offer and
   CSR: Core returns the original issued member certificate/membership rather than creating a
   second member. Obtain a new CSR/offer only after Core reports that the original join was refused
   or expired.
5. Reconnect both Cores to Controller and confirm the master-reported membership before placing a
   VM on the member. Controller availability is not required for the peer exchange or for a
   running member VM.
6. Pool-wide configuration already on the master is pulled by the member after join: NFS/SMB
   (and eligible clustered iSCSI) storage, VLAN guest networks, and ISO library entries on those
   shared repositories. The member materializes each locally, verifies access, then reports
   per-host status. Register additional NFS/SMB storage or VLANs on the **master** only, and
   rename or remove a pool VLAN or a shared ISO on the master too; members pull the same records
   and converge to a removal on their next pass. Select shared storage, a VLAN, or a shared ISO for a member VM only
   after that member's access row is fresh and healthy.

## Expected result

The member has a Core-issued pool peer identity and the master reports it in membership inventory.
For eligible NFS/SMB storage, VLANs and shared ISOs, Controller shows per-host access observations.
A member cannot use those resources when generation, identity, access or lock proof is missing,
stale or failed. Local storage, OpenZFS, exclusive iSCSI and host-local bridges stay on the host
that owns them.

## Troubleshooting

- **Join is refused (409):** check that both hosts are enrolled, that the master URL is a bare
  HTTPS origin, the pool CA and offer came from the selected master, and the offer has not expired
  or is bound to a different member/CSR. A joining Core must also have no managed VMs and no
  remote storage — that condition is not checked by the join itself but by the local resource
  rehome that runs immediately after it, so a host that breaks it joins and then reports the
  recovery message in the next bullet. Migrate or remove those workloads and detach remote storage
  before creating a fresh CSR and master offer.
- **Join completed but local scope recovery is reported:** preserve the same Core state directory
  and restart the local service. Core retains pending rehome recovery for its local default
  resources. Do not repeat the join against another master or manually rewrite managed journals.
- **Peer connection fails:** verify the master peer listener, network path and pinned TLS identity.
  Do not bypass certificate verification or follow a redirect.
- **Member storage access is pending, stale or failed:** fix the member's server-side export/share
  access, identity, capacity, QEMU permissions or locking support. The member retries stored
  reconciliation after reconnect; wait for its new Core observation rather than registering a
  duplicate repository.
- **Member VLAN is pending or failed:** the member needs the same uplink NIC name the VLAN uses
  (`nex0`, `nex1`, …). Fix the uplink or the switch trunk, then wait for the member to retry
  rather than registering the VLAN again on the member. A member that has already **failed** an
  apply retries that network at most once per hour, so allow up to an hour after the fix before
  the check changes; a network the member has not tried yet, and any refusal the member decides
  in advance, is handled on its next pull pass (ten seconds on a packaged host). The member's
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

## Verification

Peer unit tests cover member list/pull/access for shared storage, VLANs and shared-storage ISOs
([QA note](../qa/2026-09-19-pool-member-config-converge.md)). An earlier two-host native walkthrough
covered join and NFS/SMB access
([bug-ledger lab evidence](../qa/2026-09-12-bug-ledger.md#bug-0094-primary-native-pool-walkthrough));
re-running that lab for VLAN materialize and shared ISO adopt on the current build remains open.
Master change, member eject, migration, HA and physical hardware were not.

[Back to the user guide](README.md)
