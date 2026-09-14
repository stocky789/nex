# Join an enrolled Nex Core to a pool

## Status

This development workflow joins an already enrolled Nex Core to another Core's pool. There is no
Controller GUI for joining a pool yet, so use the selected hosts' authenticated local HTTPS APIs.
The pool master owns membership and pool repository configuration; Controller later shows the
Core-reported result. Do this only in a disposable lab. Master replacement, member ejection, HA,
live migration and automatic restart remain unavailable.

## Prerequisites

- Two enrolled Nex Core hosts with matching development builds. Select one as the **master** and
  one as the **member**; do not attempt to join a host that already belongs to a different pool.
- Browser/local-root access to each host's HTTPS local API, its current local-session CSRF token,
  and reachability from member to the master's configured pool-peer HTTPS listener (default port
  `9444`). Verify the master's TLS identity before continuing.
- For shared VM storage after joining: an existing NFSv4 export or SMB3 share that both hosts can
  reach and that meets the storage prerequisites in [Register VM storage repositories](storage-repositories.md).
  Local OpenZFS and iSCSI remain single-host storage.

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
6. Register NFS or SMB storage on the master using the storage workflow. Each member pulls the
   master-owned desired record, verifies its own mount, QEMU access and a shared lock, then reports
   its actual access. Select the repository for a member VM only after its member access row is
   fresh, readable, writable, QEMU-writable and lock-verified.

## Expected result

The member has a Core-issued pool peer identity and the master reports it in membership inventory.
For an eligible NFS or SMB repository, Controller shows a per-host access observation. A member
cannot use that repository when its generation, identity, access, QEMU check or lock proof is
missing, stale or failed. This prevents an access timeout from becoming permission for another
writable VM execution.

## Troubleshooting

- **Join is refused (409):** check that both hosts are enrolled, that the master URL is a bare
  HTTPS origin, the pool CA and offer came from the selected master, and the offer has not expired
  or is bound to a different member/CSR. A joining Core must have no managed VMs and no remote
  storage; migrate/remove those workloads or detach remote storage before creating a fresh CSR and
  master offer after correcting the issue.
- **Join completed but local scope recovery is reported:** preserve the same Core state directory
  and restart the local service. Core retains pending rehome recovery for its local default
  resources. Do not repeat the join against another master or manually rewrite managed journals.
- **Peer connection fails:** verify the master peer listener, network path and pinned TLS identity.
  Do not bypass certificate verification or follow a redirect.
- **Member storage access is pending, stale or failed:** fix the member's server-side export/share
  access, identity, capacity, QEMU permissions or locking support. The member retries stored
  reconciliation after reconnect; wait for its new Core observation rather than registering a
  duplicate repository.
- **iSCSI or OpenZFS is missing on the member:** expected. These backends remain local or
  single-writer and are not copied to pool members.

## Verification

The join, member NFS/SMB access, outage recovery and fresh-Controller adoption were walked
through on two disposable Cores. Master change, member eject, migration, HA and physical hardware
were not. See [bug-ledger lab evidence](../qa/2026-09-12-bug-ledger.md#bug-0094-primary-native-pool-walkthrough).

[Back to the user guide](README.md)
