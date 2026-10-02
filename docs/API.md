# Nex API reference

Status: development product 0.1.0, HTTP API v1, Core report protocol versions 1 and 2.
Phase 1 implements administrator access, Sites → Pools (one master host + members) → Hosts,
certificate enrollment,
observations, and durable inventory refresh operations. The appliance bootstrap slice adds
host-local deployment/recovery and adoption of the existing Controller VM. The first managed-VM
slice adds local storage and existing-network registration, ISO transfer and creation of a
stopped VM; the VM creation slice replaces that flat shape with the full specification
(topology, memory modes, multiple disks and NICs, boot order, autostart).
The console slice adds ticketed browser/host-local VNC streams. Event streams remain
unimplemented. Verification limits are recorded in the [platform plan](plans/2026-09-08-nex-platform.md).

## Contract and generated reference

The [OpenAPI contract](api/openapi.json) is the authoritative HTTP operation/schema reference.
The [generated browsable reference](api/index.html) renders that same document, with no
external scripts, fonts, or network dependencies. Open the HTML alongside `reference.css`
and `openapi.json` for offline use. Controller serves it at `/api/docs/` and exposes the
contract at `/api/openapi.json`. These read-only routes require no login.

The [shared Go protocol](../internal/protocol/protocol.go) and
[managed-VM types](../internal/protocol/managed.go) define runtime message types.
The contract check compares every shared JSON field, required member, scalar/array/pointer
type, and every registered API method/path in both directions. HTTP request/response schemas,
authentication, statuses, limits, retries, and availability appear in the generated reference;
this page deliberately does not maintain another endpoint catalog.

Generate and verify from the repository root:

```sh
python3 scripts/generate_api.py
python3 scripts/generate_api.py --check
python3 -m unittest discover -s tests -p 'test_api_contract.py' -v
go test ./internal/controller ./internal/agent ./internal/localcore ./internal/vmhost
```

The Python checks establish reference/source coverage and generated-output freshness; the Go
checks exercise runtime behavior. Neither proves hardware integration. Per-feature test runs and
lab results live in `docs/qa/`.

The host-local API has a separate [OpenAPI contract](api/local-openapi.json) and
[offline generated reference](api/local.html). Its browser callers authenticate to the
selected Core host, independently of Controller availability; its credentials do not grant
Controller administration.

The internal Core-to-Core pool peer API has its own [OpenAPI contract](api/peer-openapi.json) and
[offline generated reference](api/peer.html). It is an mTLS contract between pool Cores on
`--pool-peer-listen` (default `:9444`), not a client or Controller API. All three references are
generated and checked by the commands above.

## Sites, pools and pool masters

Administrators create sites only. Pools are never created by hand and `POST /api/v1/pools`
does not exist: every Nex Core host that enrolls becomes the **pool master** of a new pool,
created in the same transaction as the host record, in the token's site. `GET /api/v1/pools`
returns `master_host_id`, and the audit log records `pool.create` next to `host.enroll`.
Enrollment tokens (`POST /api/v1/enrollment-tokens`) take `{site_id, host_id?}`: an unknown
site is 400, and re-enrollment (`host_id`) needs a revoked host whose pool belongs to that
site (409 otherwise). Re-enrollment keeps the host's existing pool. Managed resources, VM
creation and media keep the `pool_id` + `host_id` contract; the host must belong to the
selected pool.

### Host-owned identity (`POST /api/v1/agent/enroll`)

Pool ID, pool name and host ID are generated and durably kept by Nex Core, not by the
Controller. On its first enrollment the agent generates its host UUID and, as its pool's
master, the pool UUID and pool name (its hostname), persists them in its state before the
request, and presents them as `host_id`, `pool_id` and `pool_name`. The Controller adopts
them unchanged and the response echoes `host_id`, `pool_id` and `pool_name`, so a
replacement Controller that receives an ordinary enrollment from a pool master recovers the
pool with its original ID and name and the host with its original ID; adoption of the
managed journal then records the host's storage, networks, media and VMs under that same
pool and host.

Validation and conflicts:

- `host_id` and `pool_id` must be canonical lowercase UUIDs; the 64-hex form the Controller
  issued to hosts enrolled before host-owned identity is also accepted, because those hosts
  keep it. `pool_name` follows the name rule (nonblank, at most 200 bytes, no CR/LF/NUL) and
  defaults to the hostname. Anything else is 400.
- A member enrolling into a pool this Controller already knows is held with
  `409 pool identity is held; the pool master has not reported this ready member` until the pool's
  **recorded master** has reported a membership naming that host `ready`. There are two reports that
  lift it, because a replacement Controller may meet the hosts in either order: the recorded master
  naming **itself** master, and — the member-first case — the host that holds the pool row naming
  **the enrolling host** as pool master. Either way only the recorded master's own authenticated
  report is read, and the enrolling host must still be listed `ready`. The second form is what makes
  the documented member-first Controller replacement work: the member reconnects, creates the pool
  row with itself recorded as master, and its first report, which names another master, moves the
  pool row to that master at once (the disclaim arm below). When the recorded master has **never
  enrolled** with this Controller — the old master is gone — the evidence is instead the pool's
  record as its enrolled, unrevoked hosts report it: the enrolling host must be listed `ready` by
  **every** one of their memberships naming that master, so the other members can still enroll and
  the pool can be promoted, but no single member can admit a host. `POST /api/v1/sites/{id}/hosts` recognises this refusal by name and answers it as a
  retryable conflict rather than a transport failure.
- A presented `host_id` already on record, or a `pool_id` already held by another master
  host, is 409: a token holder cannot claim an existing identity. Only a token bound to a
  revoked host (`host_id` on the token) re-issues that host, and the request must present
  that host and its pool, or nothing (409 otherwise). To join a second host to an existing pool,
  use the Controller-driven [pool join workflow](guide/join-a-pool.md); Core performs and retains
  membership through its existing peer contract. Native acceptance and recovery limits for this
  development workflow remain open.
- The same key is the same host: a request whose CSR matches an active recorded credential
  (a retry after a lost response, even with a newly minted token, as the **Add host** flow
  does) returns that credential unchanged rather than a duplicate or a refusal. The existing
  identical token/CSR replay is unchanged.

Credential-replacement recovery uses this existing enrollment request and response shape; it
does not add an endpoint or field. Before submitting a replacement enrollment, Core durably
keeps the candidate key and CSR alongside its active credential. A rejected or lost response
therefore leaves the old Controller trust, key and certificate usable and retains the same
pending CSR for an identical retry. Core validates the returned host/pool identity and
certificate/key pairing before atomically publishing the complete replacement credential, so it
does not combine a new key or trust with an old certificate. If publication has already renamed
the new private state file but its final durability confirmation fails, the local enrollment
call returns an uncertain error and reports the state that is now visible; Controller must
reconcile the host's observed identity and inventory before retrying rather than treating that
error as proof that replacement failed.

### Controller qnetd witness registration (`GET|POST /api/v1/agent/witness/...`)

API impact: **new contract**, implemented for automatic Core registration and status. This is a
Controller-hosted Corosync qnetd endpoint and certificate signer; it does not make Controller the
owner of pool membership or a fencing decision. Controller keeps only its own runtime qnetd CA in
`/run`; that CA is recreated after runtime loss. Core's pool master owns the selected witness and
CA pin in `PoolMembership.witness`, replicates that authority through `PoolAuthorityReplica`, and
keeps each host's qdevice credential sealed to that host. A replacement Controller obtains pool
state from Core, but a fresh Controller runtime CA requires Core to re-enroll/reconfigure the
witness.

Both routes use the authenticated host's existing outbound mTLS identity (`HostCertificate`). The
host must be a fresh current pool master. `GET /api/v1/agent/witness/candidates` returns
`PoolWitnessCandidates{candidates}`. The Controller candidate carries its runtime CA PEM and
lowercase SHA-256 of the CA certificate DER; its address is empty so Core derives the qnetd target
from the pinned Controller URL. Same-site pool-master candidates are listed without a CA or pin and
are not usable by current Core. `POST /api/v1/agent/witness/register` accepts
`PoolWitnessRegistrationRequest{pool_id, cluster_name, fencing_generation, csr_pem}` and returns
`PoolWitnessRegistration{address, certificate_pem, ca_pem, ca_sha256}`; the Controller address is
empty for the same reason. Registration also requires the reported membership to match the
request's pool, master and fencing generation, plus the `pool-witness-v1` capability. The cluster
capability is advertised in Core's normal v2 reports, but not its legacy v1 reports. Core sends a
single `CERTIFICATE REQUEST` PEM block, including when native `certutil` generated a
`NEW CERTIFICATE REQUEST` block or the request is retried from its staged file. The cluster
name is `nexpool` followed by the first 128 bits of SHA-256 over `nex-pool-cluster\0` and the pool ID;
the PEM CSR must have a valid signature and matching common name. Registration refuses a name
already mapped to another enrolled pool. Repeating registration is safe: the native signer may replace that cluster's
certificate and the response contains a valid certificate under the current runtime CA.

Failures are `400` for malformed or invalid input, `409` for stale authority, generation, capability
or cluster-name conflict, `503` when the runtime qnetd CA, service or signing operation is unavailable,
and `500` for storage or unexpected operation failure. Core persists a pending witness selection
before signing and retries registration/reconciliation from its own service loop; the Controller
does not schedule or remember that work. Core checks the returned CA pin, native qdevice status and
the exact expected cluster and qnetd identity before it reports a witness ready. The Controller
projects the master's `PoolFenceSummary.cluster` in the existing pool-members response; it does not
turn qdevice quorum into a fence proof.

**Safety boundary.** The integrated two-host path can register qdevice credentials and report
`forming` or `degraded` status. It does not arm SBD or produce witness-loss exclusion proof: a
witness outage could remove quorum, so two-host watchdog fencing and automatic witness
promotion remain withheld. The separate native nested lab tested peer isolation while qnetd stayed
reachable; it did not test witness loss or establish how the survivor behaves after it. It was not a
physical-host or end-to-end promotion test. See the [two-host witness lab](qa/2026-09-24-two-host-witness-lab.md),
[Controller registration QA](qa/2026-09-24-controller-witness-registration.md) and
[runtime CA QA](qa/2026-09-24-qnetd-runtime-ca.md). These are development/lab results, not hardware
acceptance.

Compatibility: agents from before host-owned identity present no IDs; the Controller mints
a 64-hex host ID and pool ID and names the pool after the hostname, exactly as before, and
the response reports them for the agent to keep, so such a host presents them from then on
(a host that already holds a Controller-issued host ID keeps it and adopts the reported pool).
An older Controller rejects the new request fields with 400 (unknown field); Controller and
Core development builds must be upgraded together. Rotation responses carry only `host_id`.

Databases from the earlier manual-pool model are migrated on start: each pool with hosts gets
its earliest enrolled active host (or any host) as master, pools that never received a host
or resources are deleted, and pool-bound tokens/bootstrap records are recreated in the
site-bound form. Tokens expire within ten minutes, so this loses nothing durable.

Compatibility: an older Core local service whose handoff still sends `pool_name` to
`POST /api/v1/appliances/bootstrap` is rejected with 400 (unknown field); a handoff interrupted
across the upgrade must be restarted from the host. Older UI
builds that call `POST /api/v1/pools` or send `{pool_id}` to `POST /api/v1/enrollment-tokens`
receive 404 and 400 respectively.

Core-local authenticated member join and member pull of pool-shared NFS/SMB storage,
guest VLANs and ISO library metadata on those shared repositories are implemented;
see [Core pool peer join](#core-pool-peer-join-and-repository-observations).
Changing the pool master and ejecting a member are implemented development workflows. Native
acceptance, recovery evidence and browser walkthroughs remain open, so this slice does not
establish the full multi-host phase gate.

## Simple host enrollment (add host by address + root password)

API impact: **new contract** — `probeHost` (`POST /api/v1/hosts/probe`), `addSiteHost`
(`POST /api/v1/sites/{id}/hosts`), and `localEnroll` (`POST /api/local/v1/enrollment`).
These let an administrator enroll a reachable host by address and its root password
instead of running `nex-core-agent enroll` by hand; the token/certificate contract
underneath (`POST /api/v1/enrollment-tokens`, `POST /api/v1/agent/enroll`) is reused
unchanged. Full request/response schemas are in the
[Controller](api/openapi.json) and [host-local](api/local-openapi.json) contracts.

`probeHost` is admin-only and takes `{address}` (hostname/IP with optional port, default
`9443`). It performs a 10-second TLS 1.3 handshake with certificate verification disabled,
solely to capture the leaf certificate, and returns `{address, fingerprint, subject,
not_after}` (`fingerprint` is `sha256:` + lowercase hex of the leaf DER) for the operator
to visually confirm. It establishes no trust; 400 on an invalid address, 502 if the host
is unreachable or the handshake fails.

`addSiteHost` is admin-only and takes `{address, fingerprint, root_password,
controller_url, host_id?}`. It validates the site and, when `host_id` is set, the same
revoked-host-in-this-site rule as `createEnrollmentToken` (409 otherwise) — entirely
before any network call. It then opens its own outbound TLS connection to the host with
certificate verification replaced by a constant-time comparison of the live leaf
fingerprint against the confirmed one: a mismatch fails the handshake and returns 502
*before* the root password is sent. On a verified connection it signs in to the host's
`POST /api/local/v1/session` (401 passthrough on a wrong password, 429 passthrough on the
host's own login rate limit, 502 on any other transport failure), mints a token through
the same `mintEnrollmentToken` helper `createEnrollmentToken` uses (same expiry, hash, and
`enrollment-token.create` audit event), and pushes `{controller_url, ca_pem, token,
replace_host, server_name}` to the host's `POST /api/local/v1/enrollment` with the login
response's CSRF token. `ca_pem` is the Controller's own served HTTPS certificate
(`-tls-cert`), which the host pins for its Controller connection; it is not the
host-identity CA. `server_name` is sent only when that certificate does not name the host
in `controller_url` and does carry a DNS name: it is the certificate's first DNS name, and
the host verifies its Controller connection by it, exactly as the appliance deployment
handoff already tells the deploying host to (the appliance deployed with DHCP is minted
before it has an address, so its certificate names only its hostname while operators reach
it by IP). Without it that host fails with `x509: cannot validate certificate for <ip>
because it doesn't contain any IP SANs`. A Controller without a configured served
certificate returns 503 before contacting the host. A host-side 409 (already enrolled with
this same Controller URL and trust) passes through with that host's `host_id`; a host-side
400 after `server_name` was sent is an older host that does not know the field and returns
502 naming the certificate's name and the upgrade; any other host-side failure returns 502
naming `controller_url` so the operator knows which address the host tried to reach. Success returns `{host_id, pool_id}` (the
pool the host's enrollment created or kept, looked up from the `hosts` table) and audits
`host.add` against the site. The root password is used once for that single login and is
never stored, logged, or included in any error message. Any failure after a token is
minted best-effort deletes that token row, so a lost response and a safe retry never leak
an unused token; retrying the same call after a lost response is safe — the host's own
409 identifies the already-enrolled identity, so fetching inventory resolves the outcome
without a second side effect.

`localEnroll` is a host-local route (root session + CSRF, same as other mutations under
`/api/local/v1`) that receives that pushed request and calls the same `agent.Enroll` the
`nex-core-agent enroll` CLI uses (`--server-name` is the CLI's form of `server_name`). An
invalid `server_name` (not a DNS hostname, or an IP address) is a 400 before anything is
stored; a valid one is persisted with the identity and used for every later report. Attempts are serialized per host so a retried click
cannot race the embedded agent's on-disk state; the host-local handoff registry lock is
held only to look up or start the agent, never across the network call to Controller.
Already-enrolled-under-a-different-identity returns 409 `{error, host_id}`; a Controller
rejection (invalid/expired/reused token, bad CA, protocol mismatch) returns 502 with the
`agent.Enroll` error and leaves the host unenrolled. `GET /api/local/v1/host` now also
reports `host_id` and `controller` (empty strings when unenrolled) so a caller can see
current enrollment state without attempting one.

Retries and outages: `addSiteHost` performs no retry of its own — a lost HTTP response to
the browser is safe to retry because the host-side 409 makes re-enrollment idempotent and
no token is left dangling on any failure path. `localEnroll` relies on `agent.Enroll`'s
existing idempotent-retry behavior for a lost Controller response during the underlying
`POST /api/v1/agent/enroll` call. For a replacement, a rejected or lost response preserves
the old active credential and its pending CSR, so retry the same request with the same Core
state directory. A 502 reporting that the state was published but durability is uncertain is
different: query `GET /api/local/v1/host` and refresh the Controller's host inventory first,
then retry only after those observations establish which credential is active. Controller
outage during the host-local step surfaces as an ordinary 502 to the browser; no recovery
state is held only in Controller memory.

Version compatibility: this is new, additive surface. An older host without
`POST /api/local/v1/enrollment` cannot be added this way — `addSiteHost`'s push receives a
404/connection failure from that host and returns 502; use the manual token +
`nex-core-agent enroll` flow (unchanged) for such hosts. An older Controller without
`probeHost`/`addSiteHost` simply lacks the "Add host" UI path; its manual token flow and
`POST /api/v1/agent/enroll` are unaffected. Neither addition changes Core protocol version 1.
`server_name` is additive and conditional: an older host still enrolls whenever the
Controller's certificate names the address it is given, and when it does not, the operator
is told to upgrade that host or use an address the certificate names. An older Controller
never sends the field and an upgraded host does not require it.

## Controller appliance handoff

The host deployer uses the existing Controller administrator session and CSRF contract to
create an initial Site and a site-bound enrollment intent (`{request_id, site_name,
enrollment_token}` → `{site_id, expires_at}`). It persists a stable request ID and random
enrollment token before sending the bootstrap request. Identical retries return the same
site; changed payloads conflict. Enrollment itself reuses outbound verified HTTPS and host
certificate identity, including replay with the same persisted CSR; the appliance host
becomes the master of the pool created at enrollment.

After the enrolled host reports fresh successful libvirt inventory, appliance adoption
registers the existing VM UUID and assigned host, with a protected Controller role. The pool
is derived from the host record: `pool_id` is optional in the request, must match when
supplied (409 otherwise), and is always returned so the deployer can persist it for retries. It does not create a second VM. Conflicting ownership or an unobserved VM blocks
adoption. A stable adoption request ID makes a lost reply safe to reconcile. Adoption is not
one-time: the host's appliance engine keeps the deploy journal as the durable ownership
record, its agent reports every deployment in `Inventory.managed.appliances`, and each
managed report rebuilds the Controller's appliance record from it (2026-09-10; see
[Managed VM creation](#managed-vm-creation)). The handoff's `POST /api/v1/appliances/adopt`
therefore replays as already adopted (200) when the report has already inserted the row. Read-only
managed appliance inventory derives power state from fresh successful observations; failed,
stale or revoked hosts yield unknown, never permission to start another writer.

These administrator operations use HTTP API v1 and reuse Core protocol v1; they add no
central power commands. Deploy matching development builds: an older Controller without
the appliance routes cannot complete handoff. Keep the existing VM and its operation
record when handoff fails; restoring connectivity must not recreate the appliance.

On appliance deployments, the existing Controller static-file handler exposes the public
`GET /appliance-ready.json` resource after guest initialization, a verified local HTTPS
GUI response and guest bootstrap-secret cleanup. Its body is
`{"bootstrap_version":1,"status":"ready"}`. The pristine image must not contain it;
before initialization or on a non-appliance Controller it may return 404. This persisted
marker records first-boot completion, not continuous health or host adoption. Following
adoption, the host waits up to five minutes for the marker and GUI root to return HTTP 200
over pinned Controller HTTPS before reporting ready. API login alone cannot satisfy this
gate. The [Controller contract](api/openapi.json) records it as an auxiliary static resource,
separate from registered API operations.

## Host-local execution and service command

### Stopped snapshot development contract

Stopped creation, stopped revert, leaf delete, catalog metadata updates and Controller
relay routes have bounded Core/API evidence. Running create admits `crash` and `quiesced`
modes for supported BIOS guests; UEFI running capture is refused in this development
slice. These authenticated operator routes address managed pool VMs, separately from the
appliance-only `/api/local/v1/vms` routes:

| Method and route | Request and result |
|---|---|
| `POST /api/local/v1/pool/vms/{id}/snapshots` | `{operation_id,name,description?,mode?}`; `mode` is omitted/`stopped` for shut-off guests, or `crash`/`quiesced` for a running guest; `quiesced` requires a responding guest agent and does not silently downgrade; UEFI running capture is refused; `202` with the durable `Result` after Core saves the canonical current VM definition and request |
| `POST /api/local/v1/pool/vms/{id}/snapshots/{snapshot_id}/revert` | `{operation_id}` with a distinct stable operation ID; target descriptor and captured specification are resolved by Core; guest must be stopped |
| `POST /api/local/v1/pool/vms/{id}/snapshots/{snapshot_id}/delete` | `{operation_id}` with a distinct stable operation ID; leaf-only delete using public no-delete blockcommit, metadata delete, reference scan and unlink of proven-unreferenced overlays; guest must be stopped |
| `PATCH /api/local/v1/pool/vms/{id}/snapshots/{snapshot_id}` | `{name,description?}`; updates the master's catalog name/description only; capture digests and creation time stay fixed |
| `GET /api/local/v1/pool/vms/{id}/snapshot-operations/{operation_id}` | The retained `Result`, scoped to the same VM; `404` for an unknown or differently scoped operation |
| `GET /api/local/v1/pool/vms/{id}/snapshots` | The current master's compact snapshot catalog; unavailable authority returns an error, never a fabricated empty list |
| `GET /api/v1/vms/{id}/snapshots` | Controller view of the master's compact catalog for one VM |
| `POST /api/v1/vms/{id}/snapshots` | `{request_id,name,description?,mode?}`; Controller queues `snapshot-create` on the owner host with the same mode rules as the host-local create route |
| `POST /api/v1/vms/{id}/snapshots/{snapshot_id}/revert` | `{request_id}`; Controller queues `snapshot-revert` |
| `POST /api/v1/vms/{id}/snapshots/{snapshot_id}/delete` | `{request_id}`; Controller queues `snapshot-delete` |
| `PATCH /api/v1/vms/{id}/snapshots/{snapshot_id}` | `{request_id,name,description?}`; Controller queues `snapshot-metadata` |

The routes reuse host root sessions, same-origin validation, CSRF on POST and PATCH, bounded JSON and
unknown-field refusal. Operator input cannot select a different host, VM specification or disk
path. Operation IDs use `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. A create operation ID is also
the stable snapshot ID; revert uses a distinct operation ID and retains the target ID.
An exact retry returns the retained outcome; reuse for another VM, action, target or metadata
returns `409`. Revert does not accept caller-supplied configuration or native paths.
Catalog name/description updates refuse duplicate names and unresolved recovery reservations;
later revert/delete bind the updated descriptor digest.
An exact retry of `needs_attention` queues reconciliation of the original native journal.
A new operation cannot bypass an unresolved capture for the same VM. Invalid input returns
`400`; an unenrolled Core returns `409` with enrollment guidance; unavailable host/master state
returns `503`. A known empty catalog returns `[]`. A lost reply does not prove non-acceptance.

Only agent Run mutates the Core transport journal. One bounded background snapshot operation
per host leaves the ordinary managed worker available for unrelated controls. Pending snapshot
work blocks pool identity rewrites and conflicting VM/storage effects; emergency shutdown and
force-stop do not acquire a derived dependency on the snapshot. Durable native intent precedes
master reservation, and native execution requires exact persisted acknowledgement. Local
terminal success and release require matching master settlement. Controller owns none of this
state. See the [development workflow and verification limits](guide/vm-snapshots.md).

### Backup development contract (Phase 6 / S4–S5)

Core-owned NFS backup destinations, stopped and online full backup publication and
restore-as-new from local copies. Authenticated host-local routes:

| Method and route | Request and result |
|---|---|
| `GET /api/local/v1/pool/backup-repositories` | Master's durable NFS destination list |
| `POST /api/local/v1/pool/backup-repositories` | `{operation_id,repository}`; external NFS only; `200` with the saved repository |
| `POST /api/local/v1/pool/backup-repositories/{id}/test` | `{operation_id}`; checks the configured NFS source, mounted path, filesystem identity, and repository owner marker |
| `GET /api/local/v1/pool/backup-catalog` | Completed recovery-point index from `pool-backup-catalog/` |
| `GET /api/local/v1/pool/backup-runs` | Retained run records from `pool-backup-runs/` |
| `GET /api/local/v1/pool/backup-policies` | Master's durable policy list |
| `POST /api/local/v1/pool/backup-policies` | `{operation_id,policy}`; create/update; `pool_id` optional (filled from enrollment) |
| `POST /api/local/v1/pool/backup-policies/{id}/pause` | `{operation_id,paused}` |
| `POST /api/local/v1/pool/backup-policies/{id}/run-now` | `{operation_id,vm_id}`; `202` |
| `GET /api/local/v1/pool/backup-operations/{operation_id}` | Retained `Result` for backup-now or restore-as-new |
| `POST /api/local/v1/pool/vms/{id}/backup` | `{operation_id,repository_id,consistency?}`; `consistency` omitted/`stopped` for shut-off guests, or `crash` for online crash-consistent; `quiesced` is refused until proven; UEFI online is refused; `202` with durable `Result` |
| `POST /api/local/v1/pool/backup-restore` | `{operation_id,repository_id,recovery_point_id,new_vm_id,new_vm_name,storage_id}`; restore-as-new copies artifacts locally, reconstructs from the captured VM spec, places disks on `storage_id`, and registers a new stopped VM with disconnected NICs before succeeding |
| `POST /api/local/v1/pool/backup-restore-replace` | `{operation_id,repository_id,recovery_point_id,original_vm_id,storage_id,confirm_replace:true}`; retains originals, places restored disks, exclusive cutover updates the stopped definition (disconnected NICs), settles `replaced`/`succeeded`; pre-cutover failure keeps originals; mid-cutover failure is `needs_attention` with exact stage; never succeeds at cutover-ready alone; refuses powered/in-place overwrite |
| `POST /api/local/v1/pool/backup-protect` | `{operation_id,repository_id,recovery_point_id,protected}`; catalog + repository `.nex-protected` marker |
| `POST /api/local/v1/pool/backup-test-restore` | `{operation_id,repository_id,recovery_point_id,new_vm_id,new_vm_name,storage_id}`; `202` |
| `POST /api/local/v1/pool/backup-test-verify` | `{operation_id,repository_id,recovery_point_id,test_vm_id,result}` with `passed`\|`failed`; Core proves this is the current VM created by the exact successful test restore |
| `POST /api/local/v1/pool/backup-test-cleanup` | `{operation_id,repository_id,recovery_point_id,test_vm_id}`; removes that test staging only |
| `POST /api/local/v1/pool/backup-catalog/import` | `{operation_id,repository_id}`; merge validated manifests into catalog |
| `POST /api/local/v1/pool/backup-catalog/rebuild` | `{operation_id,repository_id}`; replace that repository's catalog entries from manifests |
| `GET /api/local/v1/pool/backup-status` | `{repositories,catalog,runs}` for Controller-absent recovery |

For Core-local repository registration/test and policy upsert/pause, a first
successful `200` returns the saved resource. An exact operation-ID replay after
restart or master promotion can return the durable `Result` instead if the
original response resource is no longer retained. Protect, test verification,
and catalog import/rebuild likewise return the durable `Result` on exact
receipt replay. Inspect its `status`; Core does not substitute a resource's
current definition for the original result.

Controller relay (administrator session, Origin and CSRF; Core remains authoritative):

| Method and route | Request and result |
|---|---|
| `GET /api/v1/pools/{id}/backup-repositories` | Master's inventory list; `503` when `backup_repositories_reported` is false |
| `POST /api/v1/pools/{id}/backup-repositories` | `{request_id,name,kind,source,id?}`; queues `backup-repository-register` on the master; `202` |
| `POST /api/v1/pools/{id}/backup-repositories/{repo_id}/test` | `{request_id}`; queues `backup-repository-test`; `202` |
| `GET /api/v1/pools/{id}/backup-catalog` | Master's recovery points; `503` when `backup_catalog_reported` is false |
| `GET /api/v1/pools/{id}/backup-runs` | Master's run records; `503` when `backup_runs_reported` is false |
| `GET /api/v1/pools/{id}/backup-policies` | Master's policies; `503` when `backup_policies_reported` is false |
| `POST /api/v1/pools/{id}/backup-policies` | Create policy; omitted `schedule_id` defaults to the policy ID; queues `backup-policy-upsert`; `202` |
| `PUT /api/v1/pools/{id}/backup-policies/{policy_id}` | Update policy; omitted `schedule_id` uses the master's reported schedule ID, or `503` if unavailable; queues `backup-policy-upsert`; `202` |
| `POST /api/v1/pools/{id}/backup-policies/{policy_id}/pause` | `{request_id,paused}`; `202` |
| `POST /api/v1/pools/{id}/backup-policies/{policy_id}/run-now` | `{request_id,vm_id}`; `202` |
| `POST /api/v1/vms/{id}/backup` | `{request_id,repository_id,consistency?}`; queues `backup-now` on the pool master with the VM owner host in the command; `202` |
| `POST /api/v1/pools/{id}/backup-restore` | `{request_id,host_id,repository_id,recovery_point_id,new_vm_id,new_vm_name,storage_id}`; queues `backup-restore-as-new` on the master, which dispatches to `host_id`; `202` |
| `POST /api/v1/pools/{id}/backup-restore-replace` | `{request_id,host_id,repository_id,recovery_point_id,original_vm_id,storage_id,confirm_replace}`; queues `backup-restore-replace` on the master, which dispatches to `host_id`; `202` |
| `POST /api/v1/pools/{id}/backup-protect` | `{request_id,repository_id,recovery_point_id,protected}`; `202` `{operation}` |
| `POST /api/v1/pools/{id}/backup-test-restore` | `{request_id,host_id,...}`; queues `backup-test-restore` on the master, which dispatches to `host_id`; `202` |
| `POST /api/v1/pools/{id}/backup-test-verify` | `{request_id,repository_id,recovery_point_id,test_vm_id,result}`; `202` `{operation}` |
| `POST /api/v1/pools/{id}/backup-test-cleanup` | `{request_id,repository_id,recovery_point_id,test_vm_id}`; `202` `{operation}` |
| `POST /api/v1/pools/{id}/backup-catalog/import` | `{request_id,repository_id}`; `202` `{operation}` |
| `POST /api/v1/pools/{id}/backup-catalog/rebuild` | `{request_id,repository_id}`; `202` `{operation}` |

Core advertises `backup-v1` in its protocol-v2 report. Controller acknowledges it
with `backup_support:{"capability":"backup-v1"}` in `ReportResponse`; Core withholds
backup inventory fields until this acknowledgement. Controller returns `409` for a
backup mutation targeting a host without `backup-v1` and leaves execution to Core.
A later authenticated `succeeded` or `failed` backup Result for the same
host and operation ID can settle an earlier Controller `needs_attention`
operation row. Backup run and catalog inventory alone cannot settle that row, and a foreign
host's Result is rejected. After Result acknowledgement, the executing Core
continues to report its Controller-owned backup Journal outcome in managed
inventory, including repository, policy, protect, catalog and test metadata
commands. A same-ID native VM-create outcome does not replace it, and
peer-dispatched member work is excluded. Controller may update only its own
matching nonterminal operation row from that explicit Core outcome.
For backup and restore work targeting a member, including scheduled and policy
run-now backups, the pool master uses the pinned pool peer listener
(`PoolMasterCertificate`) to read
`GET /api/v1/pool/backup/{operation_id}` before submitting backup-now or
test-cleanup through `POST /api/v1/pool/backup` with `{command,repository}`.
Restore-as-new, replace and test-restore use
`POST /api/v1/pool/backup/restore` with
`{command,repository,catalog_point}`. The member checks the
current master identity, derives its own NFS mount and filesystem identity from
the source-only repository registration, and returns
`{result,point?,run?,intent_sha256}`. The master compares the durable intent
digest with its reserved run before settling a result and uses the member run
to preserve the exact terminal stage. A new restore run pins the master's
catalog `manifest_sha256` before execution. The member verifies its own
repository manifest against that digest before native effects, and its
durable GET/POST reply includes `catalog_manifest_sha256`. The master checks
that digest against its pinned catalog point and the shared repository payload
before accepting a successful member result. A missing or different digest,
including a reply from an older member, leaves the run unresolved and its
point pin retained. An exact retry uses the same pinned digest; a changed
catalog point or unbound legacy run cannot authorize fresh restore effects.
A terminal run with an otherwise exact,
lagging `pending` or `needs_attention` command Result is retried; changed
intent, pool, owner host, repository, recovery point or VM target, and
contradictory status remain a refusal. The
command kind is `backup-now`, `backup-restore-as-new`, `backup-restore-replace`,
`backup-test-restore` or `backup-test-cleanup`; VM execution commands carry the
selected owner host ID. Test cleanup follows the successful test run's retained
owner host and removes only its stopped test VM and copied artifacts. A lost
response is reconciled with the same operation ID; changed command input is
refused.

Test verification of a member-owned VM uses authenticated master-to-member
`POST /api/v1/pool/backup/verify-test` with the operator request, exact
successful test run and master VM specification. The member requires its own
matching durable run and native creator/current identity. For `passed`, it
also checks current disk and firmware state; `failed` can be recorded for
damaged artifacts when the unchanged creator and definition are proven.
The read-only peer response is `{result,run,intent_sha256}` and must match the
master's reserved run before the catalog result is saved. Invalid input is
`400`, a caller other than the current master is `401`, and missing or changed
native provenance is `409`. Transport ambiguity leaves a durable metadata
receipt for exact retry; it cannot produce an unproved passing result.

After an as-new or test restore is admitted to the pool master's VM owner
record, a definite native refusal before any target effect may be settled as
`failed`. The executing Core first checks that its managed journal, libvirt
domain, target disk directories, NVRAM and TPM state are absent. If it can
prove this, it sends authenticated member-to-master `POST
/api/v1/pool/vms/{vm_id}` with `action: release-restore-placement`, the exact
`operation_id` and VM specification, and `placement_absent: true`. The master
requires a matching pending restore/test run and the same creator operation in
the untouched epoch-1 VM owner record before durably deleting that provisional
row. It replies `placement_released: true`, including on an exact replay after
a lost acknowledgement. Missing creator provenance in older records, a
changed owner, or uncertain native state leaves the restore at
`needs_attention` with its pin retained. The pool master's `pool-vms.json`
owns `create_operation_id`; signed authority transfer retains the owner record,
and fresh Controller adoption still reads the master's VM inventory. This is a Core peer
contract change; no Controller route or state is added.

For master takeover, the signed pool authority replica may carry
`backup_manifest` in place of inline `backup_authority`. The master sends each
page (at most 256 KiB, with an exact length in the manifest) to the member's authenticated
`POST /api/v1/pool/replica/backup-page` with `{replica,index,data}`; `data` is a
base64 JSON byte string. The member checks the signed replica, target, page
length and SHA-256 before staging a page. It accepts the final replica only
after all pages and the full backup authority digest validate. The manifest
binds counts for repositories, definitions, history, catalog, pins and
metadata mutation receipts. Missing or corrupt pages refuse the replica;
staged pages alone grant no authority. An index `-1` POST with empty `data`
returns `{next_index}` for the first absent or corrupt page, allowing an exact
retry to resume page transfer. The bundled authority has a 64 MiB
limit, at most 1024 pages, and bounded record counts (including at most
100,000 retained run, catalog, pin or metadata receipt records). A record must
fit one page. Crossing a bound refuses the replica without truncation. The
master's admission guard refuses new metadata that would cross the bound;
an older oversized history or failed replica push can still prevent a member
from taking over the complete history.
An encoder test reached 60 MiB, but a near-boundary native transfer/promotion
still needs disposable-lab proof.

The master checks signed-authority capacity before accepting new backup
metadata. Repository and policy mutations, run reservations and settlement,
point protection, and verification results can refuse capacity on their
existing Core API routes. A refused preflight does not create that metadata
record. Inspect retained runs and member replica status before retrying.

Durable master state: `pool-backup-repositories.json`, `pool-backup-catalog/`,
`pool-backup-runs/`, `pool-backup-policies.json`, `pool-backup-pins.json`, plus
repository `.nex-protected` markers beside recovery-point payloads. Inventory reports
`backup_repositories`, `backup_catalog`, `backup_runs` and `backup_policies` with matching
`*_reported` flags only after a complete master read.
The master run record carries `host_id` when an owner has been selected, so a
member operation retains its target across restart and authority transfer.
Controller retains those observations for display and adoption, never as catalog
authority. Stopped backup converts each disk with `qemu-img convert` to standalone
qcow2 (no backing links). Online backup uses libvirt `backup-begin --reuse-external`
with explicit per-disk targets prepared as `libvirt-qemu` mode `0600`; completed-job
statistics must report `success=1` (job disappearance alone is never success). Both
modes stage under the NFS root, validate a complete manifest (`complete=true`,
`success=1`) including matching firmware when present, then publish with
sync → rename → sync parent. Initial publication and an exact same-key retry
verify artifact digests before catalog settlement.
Exact retries reuse the retained operation; changed input returns `409`. A published
recovery-point directory for the same operation id settles succeeded without
re-publishing (exact same-key retry after a lost reply or interrupted catalog write).
Controller-dispatched backup and restore commands reserve `pool-backup-runs/` during
execution when admission did not already create that record. Owner
marker uses `O_CREAT|O_EXCL|O_NOFOLLOW`. Restore always copies artifacts locally
before opening them as VM disks. Restore replace retains original disk paths, places
restored disks beside them, switches the stopped definition (disconnected NICs) at
exclusive cutover, and clears the restore pin only after `replaced`/`succeeded`.
Pre-cutover failure leaves originals live; mid-cutover failure is `needs_attention`
with the exact stage. Powered or in-place overwrite is refused. Restore pins and
protect markers survive restart and catalog rebuild on the same master; active
restore pins are included in master authority transfer. UEFI NVRAM and emulated
vTPM state are restored into operation-specific paths and bound to the durable
managed VM definition; Core refuses missing or mismatched firmware artifacts
and verifies the restored state after native definition. Native libvirt/swtpm
recovery has not yet been reverified in a disposable guest after this change.
The master stores restore pins by operation ID. Multiple restores with distinct
target VMs may use the same immutable recovery point; retention and catalog
rebuild keep that point protected until every operation pin is cleared. A
same-target restore remains a conflict. Older point-keyed pin files are read
and reindexed by operation ID without discarding unresolved pins. Signed
authority transfer accepts multiple pins for one point but requires each
operation ID to be unique.
Successful test restores retain their point pin until exact test-VM cleanup;
terminal ordinary restore pins interrupted before release are reconciled from
their durable runs. After promotion, a pending test-cleanup receipt with no
local command is rehydrated only if exactly one successful test run reproduces
its intent hash. The recovered command uses the existing authenticated
owner-host and native creator checks; mismatch remains pending and pinned.
Its journal is internal to the promoted Core and does not claim the former
master's Controller operation result in a new host report.
On a promoted master, an exact retry of an inherited restore run returns its
durable outcome without enqueuing a new native command; a changed intent for
that operation ID returns a conflict. Fenced promotion marks inherited pending
runs and metadata receipts `needs_attention` and retains restore pins. A
planned master change or eject refuses unresolved backup journal work,
pending runs or pending metadata receipts before the first authority freeze.
On the original executing Core, a restore-as-new or test restore left at the
native `allocating` stage may resume only its exact retained operation and
target through the Core tick. Core rechecks the manifest, intent, owner and
native journal, copies verified artifacts to the same local operation path,
then observes the result before settling the run and releasing its pin. A
changed request or a later ambiguous native stage is not re-executed. This
does not authorize a promoted master to replay inherited native work.
New native allocation journals bind the manifest SHA-256 and disk descriptors
before placement. An older unbound `allocating` journal can be bound only
after Core proves complete matching local and placed disks, firmware,
unchanged target identity, and absence of a domain or TPM effect under the
native operations lock; otherwise it remains `needs_attention`.
Core CLI:
`nex-core-local rebuild-backup-catalog --agent-state-dir … --repository-id …`.

### Backup policies and schedules (Phase 6 / S6)

Core-owned job/schedule definitions, master scheduler and retention. Authenticated host-local routes:

| Method and route | Request and result |
|---|---|
| `GET /api/local/v1/pool/backup-policies` | Master's durable policy list from `pool-backup-policies.json` |
| `POST /api/local/v1/pool/backup-policies` | `{operation_id,policy}`; create/update; `pool_id` optional (filled from enrollment, must match when supplied); `200` with saved policy |
| `POST /api/local/v1/pool/backup-policies/{id}/pause` | `{operation_id,paused}`; toggles pause; recalculates next dispatch when resumed |
| `POST /api/local/v1/pool/backup-policies/{id}/run-now` | `{operation_id,vm_id}`; reserves one run without moving next dispatch; `202` |

Controller relay:

| Method and route | Request and result |
|---|---|
| `GET /api/v1/pools/{id}/backup-policies` | Master's inventory; `503` when `backup_policies_reported` is false |
| `POST /api/v1/pools/{id}/backup-policies` | Create; queues `backup-policy-upsert` on the master; `202` |
| `PUT /api/v1/pools/{id}/backup-policies/{policy_id}` | Update; queues `backup-policy-upsert`; `202` |
| `POST /api/v1/pools/{id}/backup-policies/{policy_id}/pause` | `{request_id,paused}`; `202` |
| `POST /api/v1/pools/{id}/backup-policies/{policy_id}/run-now` | `{request_id,vm_id}`; `202` |

Scheduler defaults: store UTC occurrence plus IANA time zone/rule; nonexistent local time is
missed; repeated local time runs once. After Core scheduler downtime, missed occurrences are
recorded (bounded) without catch-up storm; **Run now** is offered. Controller absence alone is
not scheduler downtime. Durable `backup_authority` on the signed pool authority replica
carries repositories, full policy/schedule definitions, retained run history and catalog
index entries for master takeover (NFS recovery-point payloads are never embedded). On
fence-verified promotion the survivor materializes those stores before advertising backup
ops; if the catalog index is empty but a registered repository mount is usable, Core
rebuilds the catalog from validated manifests. Retention prune never removes protected or
active restore pins; failed new backups must not prune prior good points.

### Authentication and execution

The local service requires TLS 1.3 and authenticates only `root` using the host OS password
through `/usr/lib/nex/nex-root-auth`, with PAM authentication and account checks. Use the password set during installation
or its current value after an OS password change. Wrong passwords, other usernames, locked
or expired accounts/passwords and helper failures (including a five-second timeout) are
rejected with a generic 401. Passwords must be 1–1024 UTF-8 bytes without NUL; invalid
lengths or NUL return 400 before invoking the helper.
Its `__Host-nex_local_session` cookie and session, Origin and CSRF rules are the shared rules
in [Trust boundaries and compatibility](#trust-boundaries-and-compatibility); Origin is checked
on login too. JSON request bodies are limited to 32 KiB and reject unknown fields. The former separate
local `admin` credentials have no fallback and are removed by package initialization.
New logins follow OS root password changes; active sessions retain the deadlines above.
Deploy matching local service, PAM helper/configuration and web assets on upgrade, then
sign in again as `root`. Controller credentials remain a separate account store (default
username `root`).

API impact: **changed contract** — `localLogin` now authenticates OS root and
`localLogin`/`localSession` return username `root`; routes and JSON field names remain
unchanged. No Controller operation, outbound agent command or Core protocol change.

Deployment uses an existing explicitly selected local storage directory and bridge,
with default resources of 2 vCPUs, 4096 MiB RAM and 32 GiB disk. The service verifies a
locally provisioned signed qcow2 artifact, persists one deployment intent, then calls
the shared [VM host engine](../internal/vmhost/engine.go). Its `Preflight`, `Deploy`,
`Operation`, `List` and `Power` functions supply validation, durable results and observed
ownership; `RemoveSeed` detaches and removes bootstrap material after verified handoff.
Host preflight checks capacity and signed manifest compatibility without hashing the full
image. Its `image_verification` field explains this boundary; deployment verifies the full
image SHA-256 and qcow2 safety before VM effects. A ready preflight is not image-integrity proof.
It never treats pool registration or Controller loss as proof of exclusive disk access.
Arbitrary resident guests are visible but cannot be powered through this appliance-only API.

Deployment `network_mode` accepts `static` or `dhcp`; omitted/empty means static for
existing clients, while the GUI defaults to DHCP. Static mode requires address/prefix,
gateway and DNS. DHCP rejects nonempty static settings and obtains them from the LAN.
The operation waits at `waiting_for_network` for up to five minutes for one usable IPv4
reported by the guest agent on the original owned VM and NIC. Missing or ambiguous
addresses become `needs_attention`; retry reconciles the same VM and rechecks its address.
The URL is absent until an address is known. Handoff verifies that address against the
original per-instance certificate and its hostname; it never trusts an arbitrary DHCP peer.

DHCP enrollment persists optional `controller_server_name` in agent state. Its existing
trusted roots verify that DNS SAN while HTTPS connects to the observed IP; absent names
preserve URL-host verification. `UpdateControllerURL` requires the agent's exclusive
stopped store, the original HTTPS port, an IP origin and successful hostname/trust checks
before saving only the address. An active agent with a changed lease reports
`needs_attention`: restart the local service. Startup refreshes the saved DHCP address
before resuming the agent, including ready deployments; an explicit retry also rediscovers
the original VM. This uses its saved certificate/name and enrolled identity, without the
bootstrap password or a new VM. There is no periodic lease monitoring after readiness;
a reservation is recommended for stable access.
Matching rebuilt appliance and host components are required; Core protocol v1 is unchanged.

Local power requests no longer require typing the host hostname. Clients confirm the
selected VM/action; the optional legacy `confirmation` value is ignored. Ownership,
authentication, CSRF and request-identity reconciliation remain enforced.

Poll the original deployment ID after a refresh or lost response. The retry operation
accepts `{}` and resumes that persisted intent without asking for its password again.
`ready` with progress `100` requires Controller enrollment/adoption and seed cleanup;
`booted` alone does not. The operation also reports the Controller certificate SHA-256
fingerprint for independent browser identity verification. Progress represents workflow stages, not elapsed time or bytes.
`needs_attention` retains the original VM/disk identity. A deployment interrupted before its VM
was defined (stage `copying_disk` or `creating_seed`), for which libvirt lists no domain with
its UUID or name, restarts its disk copy under the same UUID on retry or service start (BUG-0540). Power actions use separate stable
request IDs; replay the same action to reconcile its journal, and inspect fresh VM inventory.
A completed graceful shutdown request does not establish that the guest has stopped.

`nex-core-local` owns the local listener and resumes the outbound enrolled agent. Do not run
a separate agent against the same identity directory. Its Debian package declares `Depends` on
`ifupdown`, `bridge-utils` and `vlan` beside `iproute2`, because Nex Core executes `ifdown`/`ifup`
directly and writes `bridge_ports`/`bridge_stp`/`bridge_fd` and `vlan-raw-device` stanzas that those
packages' own ifupdown hooks implement; `tests/test_core_local_package.py` ties each declared
dependency to the construct that needs it, and see [licensing](LICENSING.md) for the aggregation and
manifest consequences. Configure it with these command flags;
the operator setup workflow belongs in the [user guide](guide/README.md).

| Flags | Contract |
|---|---|
| `-listen` | HTTPS address, default `:9443`; bind/firewall the selected management interface. |
| `-tls-cert`, `-tls-key` | Required host-local server certificate/key files; verify this host identity independently. |
| `-data-dir` | Private service state, default `/var/lib/nex/core-local`; exclusive process lock and durable deployment journal. |
| `-agent-state-dir` | Exclusive outbound agent identity, default `/var/lib/nex-core-agent`. |
| `-pool-peer-listen` | TLS 1.3 Core-peer listener for one-use pool joins, default `:9444`; it uses the pool CA and peer identity held in the agent state directory. |
| `-storage-dir`, `-bridge` | Selected local storage and existing management bridge; deployment never edits host networking. |
| `-image`, `-manifest`, `-signature`, `-trusted-key` | Provisioned qcow2 image, signed manifest, detached Ed25519 signature and independently trusted raw public key. |
| `-controller-system-image` | Controller system image: a signed `vm-image` release component staged under `/var/lib/nex/payload/<sha256>/`. With `-controller-system-image-sha256` it selects the two-disk read-only Controller profile and enables the `scope:"controller"` swap; without them the single-disk appliance deployment is unchanged. |
| `-controller-system-image-sha256` | SHA-256 of that image from the signed release manifest. |
| `-libvirt-uri` | Local connection, default `qemu:///system`; only this and `qemu:///session` are accepted. |
| `-prepare-recovery-file` | Root-only manual replacement preparation using a private JSON file; see the command contract below. Runs without opening a listener. |
| `-docs-dir` | Generated offline API reference directory, default `docs/api`; served at `/api/docs/`. |
| `-ui-dir`, `-source-archive` | Built host UI and matching Nex source; defaults `web/local-dist` and `dist/nex-source.tar.gz`. The command refuses to start without the source archive. |

The signed manifest requires format/protocol `1`, architecture `amd64` and image format
`qcow2`; its image SHA-256 and minimum resources are checked before deployment. Backing
chains, external data files and encrypted images are refused. This narrow API has no image
upload/download operation, generic VM creator, browser console or network editor. The
cloud-init seed's small `cidata` ISO is bootstrap transport attached to a disk appliance,
not a Controller operating-system installer ISO.

### Manual replacement and recovery commands

`nex-core-local -prepare-recovery-file PATH` accepts one regular `0600` JSON file of at
most 32 KiB. Its [machine-readable command contract](api/local-openapi.json) defines
`RecoveryPreparationRequest`: the deployment request fields and resource defaults, with
`site_name` absent or empty. Storage/bridge must exactly match the command's
selected engine configuration; request ID, network, password and resource validation reuse
ordinary deployment. Unknown fields, multiple JSON values and changed retry input are refused.

Run as root with the local service stopped on a clean replacement host: no existing agent
identity or normal Controller deployment. Preparation takes the same exclusive service lock,
persists `recovery-preparation.json`, and reconciles the original VM UUID on identical retry.
It verifies and boots the selected appliance, waits for pinned HTTPS readiness, then removes
the seed and retained temporary credentials. It skips Site creation, enrollment and
adoption. Successful stdout contains the shared `Operation` object with status
`recovery_prepared`, stage `awaiting_manual_restore` and progress `100`; this means an empty
replacement is prepared, not that Controller identity has been restored. Failures exit nonzero
and preserve the journal; inspect uncertain effects before retrying the same request. The
packaged wrapper is `/usr/lib/nex/core-local-setup prepare-recovery --request-file PATH`.

`/usr/lib/nex/host-recovery export|restore` runs as host root, using local libvirt guest-agent
commands independently of the Controller API. Both actions require `--vm` (canonical owned
appliance UUID), `--archive`, `--state-dir` (VM engine journal directory), and `--storage-dir`;
`--uri` only accepts `qemu:///system`. The helper takes the engine operation lock and checks
the journal against live and persistent VM definitions before stopping the guest's Controller
and initialization services. Export creates a new private archive and refuses to overwrite
an existing path. Successful actions leave Controller stopped. On failure, inspect its
actual state: failure can occur before the stop. Once stopped, the helper never restarts it.

Restore additionally requires a distinct canonical `--original-vm` and explicit
`--original-stopped-and-excluded`. For a locally owned original the helper requires observed
`shut off` state and disables its autostart. `--original-external` substitutes the operator's
explicit exclusion attestation for local observation when the original is on another host;
it does not fence, contact or verify that remote host. Unreachability is insufficient evidence.
Use `--replace-empty-controller` only to overwrite a verified empty initialized replacement;
management data or pending bootstrap credentials cause refusal.

The guest-side `/usr/local/lib/nex/controller-recovery export|restore ARCHIVE` requires root,
stopped Controller/initialization services and the exclusive Controller state lock. Restore
requires `--original-stopped-and-excluded`, and `--replace-empty-controller` for an initialized
empty target. It rejects unsafe archive members, missing identity files, checksum mismatch,
and a different matching-Nex-source SHA-256; this is an exact-build compatibility gate.
The archive contains database, host CA and TLS identity secrets. Partial archives are not
backups, and a timeout leaves an ambiguous result requiring inspection before retry.
These commands do not automatically restart Controller, enroll a replacement host, adopt
the replacement VM or prove a successful restore; verify the recovered identity separately.

API impact: **new contract** — host-local session, preflight, deployment/progress/retry
and owned power control; Controller bootstrap/adoption reuse outbound Core protocol v1.

## Trust boundaries and compatibility

Clients communicate with Controller over HTTPS. Administrator login creates a
`__Host-nex_session` cookie (`Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`, no `Domain`);
subsequent mutations also
send `X-CSRF-Token` from the session response. Browser origins must match the Controller
HTTPS origin. Administrator authority currently spans all sites and pools; scoped roles
remain Phase 8 work. Credentials and tokens must not enter logs or diagnostic artifacts.

The host-local `__Host-nex_local_session` cookie has the same attributes. Both administrator
services enforce a 30-minute idle deadline and eight-hour absolute
deadline, with at most 64 active sessions per service. An authenticated mutation refreshes
idle expiry after origin/CSRF validation, even if later input validation rejects that action;
GET/HEAD polling never extends it. Session response fields remain `username` and `csrf_token`,
without expiry timestamps. Controller sessions persist their deadlines; local sessions are
invalidated by process restart. Legacy `nex_session` and `nex_local_session` cookies are
refused. Upgrade invalidates old Controller sessions; sign in again on both interfaces.

Login admission uses the actual network peer IP, ignoring forwarded-IP headers: five
valid-length attempts per one-minute window, two concurrent password checks, and at most
4096 tracked peers per service. Rate, concurrency, peer-table or session-cap exhaustion
returns 429. No `Retry-After` is emitted because 429 can also indicate a busy password-check
slot; clients must use bounded retries. Invalid credential lengths return 400 before an
expensive password check; wrong credentials return 401. A reverse proxy shares its actual
peer limit across the clients it forwards.

New Controller administrator bootstrap passwords accept any nonempty valid UTF-8 value
up to 1024 UTF-8 bytes, without CR, LF or NUL. No minimum length or complexity rule applies.
Existing Controller password hashes remain usable. Host-local login uses the current OS
root password and does not provision a separate credential. Appliance deployment uses the
same Controller password validation; firstboot also applies that password to the guest
console account `root` and enables root password SSH on new appliances. This changes
browser authentication and bootstrap validation, not outbound host certificate identity,
agent commands or Core protocol version 1.

Both services send `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, restrictive camera/microphone/location
permissions and a same-origin content security policy that forbids framing, objects and
base URLs. HSTS is not enabled for this development workflow using self-signed certificates.
Verify the selected TLS identity independently; the headers do not establish certificate trust.

API impact: **changed contract** — administrator cookie names, idle expiry, login limits
and new-password validation changed on both services; routes and response fields are unchanged.

Core initiates outbound HTTPS to a separately verified Controller identity. Site-bound
single-use enrollment tokens expire after ten minutes. Enrollment issues a host-specific
client certificate; reporting and rotation require mutual TLS and current unrevoked host
identity. Host credentials cannot perform administrator calls or submit another host's
operation results. Revocation does not stop guests or remove local recovery access.

This development Controller accepts report protocol versions `1` and `2`; unsupported versions
are rejected. Version 2 adds the storage-repository acknowledgement before a Core advertises its
storage capability. Deploy matching development builds: report JSON is strict, so a new field can
be refused by an older Controller before any capability is evaluated. **Shared upgrade rule:**
upgrade Controller before Core, and deploy matching Controller/Core/web development builds;
capability gates below are additive, not a general mixed-version guarantee. The clustered
session-monitor extension is the one exception that projects additive fields away until
acknowledged. Sections below reference this rule rather than restating it. A future incompatible
change must record migration guidance and update both peers and their contract checks in one slice.
Reports now accept up to 64 advertised capabilities (previously 32), with each name still
limited to 100 UTF-8 bytes. Current Core can advertise 35 after capability negotiation;
an older Controller that rejects that follow-up report leaves the host stale and blocks
appliance adoption. Upgrade Controller before retrying the original deployment; do not
create another VM.

## Execution and observations

Controller-scoped configuration changes finish in their HTTP request. Inventory refresh is a durable operation:
clients persist a stable `request_id`, retrieve the same operation on retry, and inspect
operation state for completion. Agent reports transport commands and acknowledged results;
there is no separate command stream. `refresh-inventory` remains an observation request.
The managed-VM commands below cover registration, ISO transfer, definitions and power
operations: Core executes them while Controller relays requests and presents results.
Migration and automatic recovery retain their separate implementation and acceptance gates.

A host's management connection is independent of each check's health. A heartbeat without
new inventory does not validate old observations. Unknown/stale state cannot establish that
a VM has stopped or permit another writable execution. Local bounded probes and result
journaling let reporting and recovery continue independently of a stalled probe; controller
outage does not alter local guest execution. Process restart/reconnect requires fresh
observations and operation reconciliation. See the OpenAPI schemas for timestamp units,
nullable observations, ordering, command results, and acknowledgement fields.

Usable CPU models come from Core's bounded libvirt domain-capabilities observation.
That query uses a normal libvirt connection because libvirt rejects it on a read-only
connection; other inventory queries retain read-only connections. The command only
observes capabilities. Failures remain failed checks with unavailable model inventory;
Core does not substitute guessed models.

### Pool CPU baseline and explicit VM features

**API impact: changed Controller/Core contract and new Core peer contract.**
`vm-cpu-baseline-v1` advertises OS-independent native CPU baseline support. Guest OS values
and semantics are unchanged; there is no Windows-specific CPU policy or model filter.

Optional `VMCPU.features` carries at most 512 distinct `{name, policy}` entries. Names use
the CPU-model identifier syntax; policy is `require` or `disable`. Nonempty features require
a named model, excluding host modes and `max`. The selected model and features are fixed in
the existing Core managed journal and master `pool-vms` record `Spec`, and return through
inventory/adoption after Controller replacement. Omitting features preserves the previous
model-only semantics; existing definitions are never rewritten. A feature-bearing create or
update on older Core returns **409** with upgrade guidance after idempotent request replay.
Upgrade Controller first, then every Core in the pool.

Optional `Inventory.cpu_baseline` contains `model`, `features`, `machine`, membership
`generation`, `member_count`, `observed_at`, and optional `error`. This is a disposable
Core-master observation, not Controller-owned policy. The current inventory baseline uses
q35. Missing/error observations remain unavailable; an unreachable or non-ready member is
not silently omitted. New wizard drafts choose the fixed pool baseline when available.
When an updated Core cannot establish it, the operator must explicitly select a model.
All host-usable named models remain available manually, including `qemu64`.

The master collects native guest CPU definitions from every non-ejected pool member (at
most 64 membership records), refuses unknown endpoints, unavailable members or mixed CPU
vendors, asks libvirt for `hypervisor-cpu-baseline --features --migratable`, and compares the
exact result on every included member. Membership must remain unchanged. Single-host pools
use the same flow. Core compares explicit VM CPU definitions against the actual VM machine
before creation, stopped edits and Core-mediated starts. Native libvirt autostart bypasses
that preflight. CPU changes require a stopped VM. This does not certify guest OS requirements,
future pool members, or live migration; runtime, storage and networking compatibility still
matter, and migration is not implemented by this slice.

Core peer endpoints use the existing pool certificate identity and membership checks:

| Endpoint | Caller and result |
| --- | --- |
| `GET /api/v1/pool/cpu/capabilities?machine=q35&generation=N` | Master asks a member for `{generation,machine,cpu}`; `cpu` is native guest CPU XML. |
| `POST /api/v1/pool/cpu/compare` | Master sends `{generation,machine,model,features}` to a member; success echoes the exact verified definition. |
| `GET /api/v1/pool/cpu/baseline?machine=q35&generation=N` | Admitted member asks its master for the verified baseline observation. |

Machine must be `q35` or `pc`; generation must match current membership. Requests are
bounded to 30 seconds; comparison JSON is at most 128 KiB and rejects unknown fields or
trailing JSON. Read-only operations are safe to retry, with no durable job or VM mutation.
Responses are **200** JSON, **400** malformed input, **401** rejected peer, **405** wrong
method, or **409** changed/unknown membership or an unverifiable CPU. A failed observation
never authorizes a new baseline. See the [plan](plans/2026-09-17-cpu-baseline.md) and
[verification evidence](qa/2026-09-17-cpu-baseline.md).

### Audit resource IDs for VM requests (`GET /api/v1/audit`)

API impact: **changed contract** — the meaning of `resource_id` in `GET /api/v1/audit` for the
`vm-power`, `vm-media`, `vm-update` and `vm-delete` actions. These rows now carry the VM ID,
as `vm-create` and `console.open` already did, so a client can list one VM's activity by
matching `resource_id` to the VM ID. The response shape, authorization (administrator session)
and ordering are unchanged. Rows written by earlier Controller versions keep their previous
value (the operation ID for power, media and delete requests, an internal row ID for updates);
the request itself remains in `GET /api/v1/operations`. Audit history is Controller-scoped and
is not recovered from hosts by a replacement Controller.

## Managed VM creation

API impact: **new contract** for client storage/network registration, raw ISO upload,
stopped-VM creation and assigned-host media download; **changed contract** for the
existing VM list and capability-bearing agent reports/commands/results. Protocol version
remains `1`. Matching Core reports `managed-vm-v1` on each heartbeat; Controller rejects
new work for stale, revoked, wrong-pool or unsupported hosts and withholds managed commands
from reports lacking that capability. Older agents retain inventory refresh support; the
shared upgrade rule applies.

### Master-owned VM placement approval

API impact: **changed authenticated Core-peer contract; existing client create/update contracts**.
Before Controller queues any `vm-create` or `vm-update`, both the selected Core and its current
pool master must report `pool-vm-placement-v1`; otherwise the client receives `409` and no command
is queued. Controller keeps request syntax, administrator/CSRF authorization, UUID assignment and
request-ID replay, but does not decide resource readiness, capacity, CPU compatibility, VM identity
or edit eligibility from its mirrored inventory.

For each definition transition the pool master calls the selected ready Core at its advertised
origin through pinned mTLS private `POST /api/v1/pool/placement`, with the exact `Command`,
operation ID and current membership generation. That Core reads current native managed state for at
most 20 seconds and returns a master-accepted body bounded to 128 KiB:
`{generation,operation_id,vm_id,vm,storage,accepted:true}` after rechecking membership. The master
requires every echoed value and `accepted:true`, checks returned shared-storage identity against its
master record, then takes its durable locks, rechecks the same membership generation and persists
the existing `pool-vms.json` definition or pending revision before acknowledging the owner. No
durable VM lock spans the peer RPC. A missing acknowledgement from an older Core, timeout,
unavailable member, changed membership
or native refusal fails closed; no definition, reservation, permit, start or Controller-side
placement record is created. A `409` peer refusal contains only a sanitized JSON `{error}` reason,
at most 240 bytes, with CR/LF/NUL replaced; the master may surface that diagnostic in its own
refusal. `replay:true` says the target recognized an identical existing journal command and did
not repeat native checks, so it can confirm only an existing durable master record and never seed
one. Reconcile an interrupted reply through the same stable operation and durable master record;
never infer approval from a Controller mirror or retry a changed command.

An exact redelivery that already has a local `vm-create` or `vm-update` journal entry still needs
current placement admission. If admission is unavailable or refused, Core reports that original
operation `needs_attention` and does not call native execution or journal reconciliation with a
bare context. A fresh exact redelivery whose admission succeeds follows the normal native replay
path. This prevents a lost master/cluster fence from becoming permission to reconcile a possibly
mutating definition operation.

Core repeats native validation immediately before effects, so an observation that becomes stale
after master approval produces the command's normal failed or `needs_attention` result. This covers
creation, stopped edits and media changes that become durable definition transitions through
`vm-update`; a tray reconnect that leaves the definition unchanged remains ordinary media owner
admission. A replacement Controller re-adopts definitions from master Core inventory and does not
reconstruct placement decisions.

The browser uses its administrator session, exact HTTPS Origin and CSRF token. The shared
`Command` transports one storage, network, media or VM specification through the existing
outbound report response. `storage-register` and `network-register` check an existing
local directory or bridge/active libvirt network without formatting or configuring networks.

### VM storage repositories

API impact: **changed contract** — protocol v2 adds `storage-repositories-v1`, typed
`StorageRegistration` sources, `storage-update`, `storage-recheck` and `storage-detach`, and
the Core-only credential handoff. Core owns the durable repository journal, canonical identity,
generation, access observation and credential receipt. Controller holds only a disposable
projection recovered from `Inventory.managed.storage`; a new Controller does not need paths or
secrets re-entered.

### ZFS provisioning

API impact: **new contract** — `storage-zfs-provision-v1` adds read-only
`kind: "zfs-device"` discovery and administrator/Origin/CSRF-protected
`POST /api/v1/storage/zfs/provision`. The request targets a pool master and carries
`request_id`, `pool_id`, `name`, `scope` (`host` or `pool`) and explicit per-host
`hosts` selections. Controller submits one durable Core operation; it does not perform
or schedule member disk work. An older Core is refused, and a timed-out reply is not
permission to submit a different operation over the same disks. Every selected host must
also be fresh, active, in the selected pool and advertise `storage-zfs-provision-v1`;
otherwise Controller refuses the request with 409 before Core is asked. Core still makes
every provisioning decision; this is only an early refusal instead of a delayed
`needs_attention`. A later Core result may move a `storage-zfs-provision` operation from
`needs_attention` to `succeeded` when its durable native receipt verifies an interrupted
create; no other operation kind recovers that way, and Controller never infers success
from an adopted storage projection.

Core owns repository identity, selected device intent, native pool/dataset identities,
and per-host results. The master retains `zfs-pool.json`, lifecycle generations and
separate operation receipts in `zfs-lifecycle.json`, and its accepted command journal; target hosts retain native
`zfs-provision` receipts and managed registration records. Completed detach receipts
retain canonical tombstone metadata so a master without a local dataset can remove
Controller mirrors after interruption; a newer registration generation takes precedence.
Pool scope describes independent host-local zpools under one
repository identity, not shared disks; `shared` remains false. Provisioned ZFS
advertises VM-disk capability, with `iso: false`: the existing media-import contract
does not select a target member within a local-ZFS aggregate. Hosts with no selected
disks stay unconfigured. Device discovery is transient and does not grant permission:
Core rechecks device identity and use before destructive work. Selections include the discovery fingerprint alongside by-id path, serial and size;
old-data selections also include an exact by-id erase confirmation. Omit the per-host
layout for Core's default. An optional `repository_id` refers to an existing Core-owned
repository when configuring another host; it never allocates an identity in Controller.
The exact request and result schemas are in the [machine-readable contract](api/openapi.json).

The master publishes one `StorageRegistration` with `zfs_provisioned`, `zfs_scope`
and `zfs_host_access`; each access entry belongs to one host and carries that host's
native pool/dataset GUIDs, path, capacity, access and observation time. Members retain
local registrations under the canonical ID for local VM operations. Controller adopts
the master's aggregate and does not replace it with a member registration. Durable
`Managed.zfs_provisions` outcomes do not establish current writable access.

API impact: **changed contract** — a ZFS repository whose zpool is `DEGRADED` reports
`StorageAccess.status` `degraded` with `readable` and `writable` true. Redundancy is lost,
the pool still serves every read and write, and Core keeps accepting VM creation, start
and migration onto it until the failed device is replaced; `FAULTED`, `UNAVAIL`,
`SUSPENDED` and `REMOVED` remain inaccessible with the health in the error, as before.
Each `zfs_host_access` entry and `StorageAccess` also gains an optional `pool` /
`zfs_pool` object: Core's `zpool status` observation of that host's pool, carrying the
pool state, the `status`/`action` sentences ZFS itself prints, the running or last scrub
or resilver, and the leaf devices of the pool's vdev tree by their `/dev/disk/by-id` path
with their state and read/write/checksum counts. Only the devices nested under the pool's
root vdev are reported; class containers (`spares`, `logs`, `l2cache`, `dedup`, `special`)
beside the root are ignored so a hot spare is never counted as a failed data leaf.
Leaves under an in-flight `replacing` or activated `spare` vdev carry
`parent: "replacing"` or `parent: "spare"` so a second replace is refused before wipe and
the UI shows being replaced instead of Replace disk. Empty name or state leaves are
dropped. It is a transient observation. The Controller keeps it only in its disposable
Core-refreshed projection with the rest of the mirrored storage registration and derives
nothing from it; a fresh Controller sees it again on the next report. The additive
`zfs_pool` / `pool` observation fields are withheld from reports until the Controller
acknowledges `storage-zfs-replace-v1` through the existing `storage_repositories` support
shape — a strict older Controller therefore never receives those fields and does not 400
the whole report. The status value `degraded` itself is still sent once the host reports
storage repositories: an older Controller that does not recognise it keeps showing the
status text as reported.

API impact: **changed contract** — the provisioning layout rules widen and each host's
selection may carry hot spares. `mirror` now accepts two **or three** devices, so an
operator can add a third disk to a mirror before removing a suspect one, and `raidz3` is
accepted at five or more devices. `ZFSProvisionHost` gains an optional `spares` array of
the same `ZFSProvisionDevice` shape as `devices`: those disks are erased exactly like the
data disks, including the same eligibility, fingerprint and typed erase-confirmation rules,
are passed to Core as `spare` vdevs, and the pool is created with `autoreplace=on` so ZFS
resilvers onto a spare by itself when a data disk fails. Spares satisfy no part of the
layout rule — the data disks alone must satisfy it — so a mirror still needs two or three
`devices` however many spares accompany them. The field is optional and omitted when empty.
Hot spares and the widened layouts (three-disk mirror, raidz3) require `storage-zfs-replace-v1`
on the pool master and every selected host; the Controller refuses the request otherwise and
the wizard does not offer the Spare column or those layout radios without that capability. The
pool master also probes the selected member's replace peer route before posting a widened
selection, and refuses with `hot spares and widened ZFS layouts need storage-zfs-replace-v1 on
every selected host; upgrade Nex Core` when that Core is older, so an older Core never receives
those fields.

Strict older Controllers receive no ZFS-provision inventory until the upgraded Controller
acknowledges `storage-zfs-provision-v1`, and no `zfs_pool` / `pool` observation fields until
it acknowledges `storage-zfs-replace-v1`, through the existing `storage_repositories`
support shape. This is an additive capability acknowledgement with wire protocol 2;
upgrade Controller first. Debian packages retain the `storage-format-v2` compatibility
marker and add `storage-format-v3` for ZFS records. The APT guard requires v3 only for
retained ZFS state or a Controller with ZFS-capable enrolled hosts; v2-only state keeps
its prior compatibility. This is a package guard, not a protocol version increment or
permission to bypass it through manual binary replacement.

The existing-dataset registration contract below remains an advanced path. It does not
create or import a pool. Provisioning is a separate operation that creates a Core-owned
pool and `vms` dataset with safe native defaults; detach never destroys a pool or dataset.
Acceptance evidence and remaining native/delivery gates are tracked in
[ZFS rollout verification](qa/2026-09-19-zfs-provisioning.md).

### Replacing a failed pool disk (POST /api/v1/storage/zfs/replace)

API impact: **new contract** — Controller `POST /api/v1/storage/zfs/replace`, the durable
master command `storage-zfs-replace` carrying `ZFSReplaceRequest`, the pool-master-to-member
peer route `POST /api/v1/pool/zfs-provision/replace`, and the `storage-zfs-replace-v1`
capability. The pool's condition and the name of the failed device are read from the
[per-host ZFS observation](#zfs-provisioning) above; this operation adds no query of its own.

**State owner: Core.** The durable records are the pool itself — `zpool status` is what the
pool's device set *is* — the executing host's replacement journal, keyed on the operation ID
under its state directory, and the pool master's ZFS repository record, which the master
updates to name the disk the repository now holds. The Controller keeps only what it keeps
for provisioning: the request/operation relay row. A fresh Controller re-adopts the
repository from the master's inventory and sees the current disks and any resilver in the
next pool observation; nothing is re-entered.

**Authorization.** Administrator session, same HTTPS Origin and `X-CSRF-Token` at the
Controller. When the master forwards the work to a member, the member's peer route accepts
only the current pool master certificate and only a request addressed to its own pool and
host ID.

**Request.** `{request_id, repository_id, pool_id, host_id, old_device,
device:{by_id, serial, size_bytes, fingerprint, erase_confirmation?}}`. `old_device` is the
failed leaf's `/dev/disk/by-id` path exactly as the pool observation reports it. `device` is
one candidate from the host's own `POST /api/v1/storage/discover` listing with
`kind: zfs-device` — the same listing, eligibility rules, fingerprint and typed erase
confirmation the provisioning wizard uses; a device reported as holding old data must carry
`erase_confirmation` equal to its own `by_id`. The Controller allocates the operation ID;
`request_id` is the client's own retry key.

**Controller refusals.** `400` for a missing `request_id` (`a stable request_id is
required`), a repository, pool or host ID that is not an opaque ID (`ZFS disk replacement
requires a repository, pool and host`), a failed device that is not a by-id path (`ZFS disk
replacement names the failed device by its /dev/disk/by-id path`), a replacement equal to the
failed disk (`the replacement must be a different disk from the failed one`), a replacement
without a stable by-id path, serial, size or 64-hex fingerprint (`ZFS provision device is
invalid`), and an erase confirmation naming another device (`ZFS erase confirmation must name
the selected device`). `404` when the selected host is not a known host (`host not found`).
`409` for an unknown pool (`pool not found`), a pool whose `master_host_id` no longer resolves
to a host (`host not found`), a master or selected host that is revoked, stale or in another
pool (`the selected host must be fresh, active and in the selected pool`), a master too old
for ZFS work at all (`pool master does not support ZFS provisioning; upgrade Nex Core`),
either the pool master or the selected host lacking this capability (`replacing a ZFS disk
needs storage-zfs-replace-v1 on the pool master and the selected host; upgrade Nex Core`), and
a `request_id` already used with another payload. These are only the refusals the Controller
can already see from its own rows; every decision about the pool and the disks is Core's.

**Core refusals.** They arrive as the operation's result, not as an HTTP status. The master
reports `failed` when the command is not for its pool (`ZFS disk replacement targets another
pool`), when this host is not the master (`only the pool master may replace a ZFS disk`),
when its own repository record holds no such repository on that host (`this pool master has
no ZFS repository of that ID on that host`), and when the named host is not a ready member at
the current membership generation (`the selected ZFS host is not a ready current member`).
The executing host's own refusals come back as `needs_attention` carrying its sentence: the
named leaf is no longer in the pool (`<device> is no longer part of this pool; check the pool
before replacing a disk`), the named leaf is `ONLINE` (`<device> is online, so there is
nothing to replace`), the named leaf is already inside a replacing or spare vdev
(`<device> is already being replaced; wait for the resilver`), the replacement is already
an ordinary member of the pool without a matching running resilver (`this disk is already
part of the pool`), the replacement is smaller than the failed disk (`the replacement disk
is smaller than the failed disk (<new> bytes against <old>)`), discovery cannot re-list the
disk (`the replacement disk cannot be safely observed`), the replacement is not as it was
observed or is in use (`the replacement disk changed since it was selected, or is in use`,
`this host no longer reports the selected replacement disk`), it holds old data without a
typed confirmation (`the replacement disk holds old data and was not confirmed for erasure`),
or a step failed (`the replacement disk could not be cleared`,
`<old> could not be replaced with <new>` with ZFS's own reason when available). An unreachable or refusing member is reported by
the master as `the selected ZFS host is unreachable` or `the selected ZFS host refused the
disk replacement`. The master-to-member replace peer call uses a thirty-minute client budget
sized for wipefs and `zpool replace`, not the ordinary fifteen-second peer timeout, and the
member runs that work detached from the HTTP request context so a client disconnect cannot
cancel an erase already in progress; the host journal still records the outcome.

**Asynchronous result.** `202` returns `{repository_id, host_id, old_device, operation}`; an
exact repeat of a `request_id` replays that operation with `200`, and a different payload
under the same `request_id` is `409`. An accepted request is not proof that a disk was
changed: follow the operation, then the repository's pool observation for the resilver.

**What Core does.** The executing host observes the pool and the disk again, runs
`wipefs --all` on the confirmed replacement and then
`zpool replace <pool> <old-device> <new-device>`. The operation is done when `zpool replace`
returns. The resilver that follows is ZFS's own work: it is reported by the ordinary per-host
ZFS observation, it takes as long as the data on the pool needs, and ZFS retires the old
device itself when it completes. Nothing detaches the old disk, and nothing waits on the
Controller or an open browser. VMs on the pool keep running throughout.

**Retries and interruption.** The `request_id` replays the Controller's row. On the host the
operation ID keys the journal: a redelivered command replays its recorded outcome, a pool
that already holds the replacement is adopted as success only when the failed leaf is gone
or a RESILVER is still SCANNING onto that replacement — otherwise the disk is refused as
already part of the pool — an operation ID reused for a different repository, failed leaf or replacement is
refused (`this operation was already used for a different ZFS disk replacement`), and an
agent interrupted after the wipe resumes from the journal's `wipe-started` stage, where the
wiped disk can no longer match its fingerprint and the identity that survives a wipe — the
same `by_id`, serial and size — is checked instead, while erase confirmation is still
required if the live disk now holds old data. A replacement that is no longer exactly
as observed is `needs_attention` and is never erased again. After the host reports success
the master records the replacement disk on its repository record, so a re-adopting Controller
never sees a repository still naming a disk ZFS has retired; a failure to write that record
is `needs_attention` (`the disk was replaced but the repository record was not updated`) even
though the pool itself is correct.

Compatibility: shared upgrade rule — Controller before Core. The capability is checked on both
the pool master and the selected host before the command is stored, so an old Core is refused
at the API rather than sent a command it would reject. The exact request, response and refusal
shapes are in the [machine-readable contract](api/openapi.json) and the
[peer contract](api/peer-openapi.json).

Verification: Go unit tests for the host worker (the command sequence, the adoption of a pool
already resilvering onto the replacement, the `ONLINE` and missing-leaf refusals, the reused
operation ID and the interrupted-wipe retry), the master aggregate's pool, master, repository
and membership refusals, and Controller contract tests for the relay to the master, the exact
replay, the changed-payload conflict, the CSRF requirement, the old-Core refusal and invalid
input; the disposable-lab walkthrough is recorded in
[replacing a failed pool disk](qa/2026-09-22-zfs-replace.md).

### Transient Core storage discovery

API impact: **new client operation and changed Core report contract** —
`POST /api/v1/storage/discover` accepts an administrator/CSRF-protected
`StorageDiscoveryRequest` scoped to one eligible pool host. The Controller passes it once in
the authenticated protocol-v2 report response only after that Core advertises both
`storage-repositories-v1` and `storage-discovery-v1`; the Core returns the matching
`StorageDiscoveryResult` on its next report. This is a bounded request/response relay, not a
repository command or durable operation: Controller deletes its in-memory entry on delivery,
result, browser cancellation or 30 seconds, and neither side journals, adopts, audits or caches
candidates or supplied SMB/CHAP credentials. The Controller receives credential bytes only for
that relay; its database never contains them.

Core limits each observation to eight seconds. `200` returns a sanitized result (including an
empty or failed observation); `504` returns `status: stale` after the relay deadline. Invalid
scope, source selector, request ID or credential bounds is `400`; an inactive/wrong-pool Core,
missing capability or duplicate active request is `409`. Core rejects local symlinks, enumerates
unmounted NFS exports with `showmount`, uses an ephemeral mode-0600 SMB credential file for
`smbclient` source/share/directory browsing, sends iSCSI discovery only with nonpersistent
`iscsiadm` discoverydb settings, and lists already mounted ZFS sources. Results contain only kind,
source, directory path, browsable state and an optional existing-repository flag. The
`zfs-device` kind additionally returns operator-safe physical-device metadata and stable
by-id paths; it requires `storage-zfs-provision-v1`. Results never contain passwords,
mount options or raw command output. Discovery cannot create a directory, mount/login a backend, retain an iSCSI node/session,
change repository configuration or weaken later registration validation. A cancelled or stale
request must use a new request ID.

The network picker reuses this contract: omit `path` to enumerate an SMB server's shares
using the supplied account, optional domain and secret, or an NFS server's advertised exports. Supply the returned
absolute path to list its immediate directories. These are live remote observations, not
registered-repository inventory. Each result is capped at 64 candidates. SMB folder results may
set `existing` when Core sees a `.nex-storage-marker` in that folder; the flag is advisory and
does not skip registration identity checks. The client retains
the originally selected NFS export and submits a relative `directory` when registering a
nested selection; it must not replace that export with each subsequent listing root.
SMB share names support letters, numbers, internal spaces and `._$:()-`; leading/trailing
whitespace and command/unit syntax are rejected. SMB/NFS relative directories can contain
spaces; traversal, backslashes and control characters remain rejected. SMB browsing passes
the selected directory as a separate native argument, never an interpolated command. Marker
probes use a fixed `ls .nex-storage-marker` command with the folder in `-D`.
The clustered-GFS2 setup panel also reuses this existing request, always scoped to the selected
pool master, to enumerate and select an iSCSI target IQN for its portal. That observation neither
logs in nor mounts a target, and it does not provide the LUN number, WWID or filesystem UUID:
those remain required storage-inventory attestations in the cluster apply registration, which Core
validates on the selected members. It adds no clustered-storage discovery route or schema.
The wire shape and capability negotiation are unchanged; older Core builds may reject
newly supported names, so use matching Core and Controller builds for these sources.

### Failed request records, placement warnings and retryable enrollment conflicts

API impact: **changed contract** on the client-facing Controller API, implemented; **none** between
Controller and Core — no command, peer route or reported field changes. These are the response
changes from the Phase 4 defect batch; the defects themselves are `BUG-0325`, `BUG-0326`, `BUG-0329` and `BUG-0330` in
[the bug ledger](BUG_LEDGER.md). Durable state owner: **controller**, and only within the closed
Controller-scoped list — its own operation history and audit trail. No pool-scoped state is created,
kept or deleted by any of it.

**Discarding a registration a host refused.** A pool VLAN or storage repository registration the
pool master rejected published nothing on any host: it is this Controller's own request history, not
pool state, and no host holds a record a removal could be relayed to. It is therefore discarded
locally, exactly as a failed ISO import already was.

- `DELETE /api/v1/networks/{id}` on a registration whose operation `status` is `failed` and which no
  host reports as applied takes **no body**, relays nothing, deletes only the Controller's
  `managed_resources` row, keeps the operation and its audit history, audits `network.discard` and
  answers `200 {"id": "<id>", "deleted": true}`. The relayed pool-VLAN removal is unchanged for a
  registration the master reports as applied, and `PATCH /api/v1/networks/{id}` is unchanged: a
  failed request record cannot be renamed.
- `DELETE /api/v1/storage/{id}` is the storage twin and is **new**. Same shape, audit
  `storage.discard`; `404 no failed storage registration with this ID; an adopted repository is
  removed with detach` otherwise. A repository a host holds is removed with
  `POST /api/v1/storage/{id}/detach`, which is unchanged.
- Both refuse a row whose command a host has since reported as applied, with `409 a host reports this
  registration as applied, so it is not a failed request record; remove it on the host instead of
  discarding it`. That guard reads the *mirrored command's* status rather than the operation's,
  because a failed operation's row can legitimately become the mirror of a host-reported resource.
- `GET /api/v1/storage` and `POST /api/v1/storage`: a typed registration that has **failed** now
  carries `id` set to the Controller's own request-row key `pending-storage-<operation_id>`
  (previously `""`), which is the handle the discard takes. One that is still queued or dispatched
  still answers `id: ""` — Core allocates repository IDs and the Controller never mints one — and on
  success the row keeps Core's allocated ID exactly as before.
- Audit gains two actions: `network.discard` and `storage.discard`, each with the discarded
  registration's ID as `resource_id`.

**Advisory placement warnings.** `POST /api/v1/vms` may answer its `202` with an optional `warnings`
array of strings, omitted entirely when empty, and `GET /api/v1/operations` may carry the same array
on an operation whose `kind` is `vm-create` and whose `status` is `dispatched`. One entry per
required pool-shared repository, network or ISO whose access on the selected host this Controller's
mirror does not currently show as current:
`the pool master's last report does not show this host's access to <kind> <id> as current; the
request may sit dispatched until it does`, where `<kind>` is `storage repository`, `network` or
`ISO`. They are **advisory only**: no status changes, nothing is refused, and the pool master remains
the placement authority — a request warned about here is often accepted once the host reports working
access. The Controller deliberately does not veto on its mirror, which is a `-stale-after`-old copy
of the master's observation; doing so would make it a second placement authority. Nothing is stored:
the listing recomputes the warnings from the current mirror on every read, so they disappear by
themselves when the master reports working access, and a stored warning would go stale in exactly the
direction that matters.

**A retryable enrollment conflict.** `POST /api/v1/sites/{id}/hosts` used to flatten every non-200,
non-409 answer from the host's local enrollment endpoint into one `502`, including this Controller's
own refusal relayed back by the host. Adding a pool member to a *replacement* Controller before its
pool master has reported it is a documented, normal wait, so that one refusal is now recognised by
name and answered as the conflict it is:

```json
{"error": "this host belongs to a pool whose master has not reported it yet; connect the pool master first, or retry once the master has reported this member — usually within one report interval",
 "retryable": true, "host_error": "<the host's own error text>"}
```

`retryable` and `host_error` are new fields and appear only on this answer. Every other host-side
enrollment failure keeps `502 host could not enroll with the controller at …`, including the
enrollment endpoint's other `409`s (`host identity already enrolled`, `presented pool name does not
match the Core-reported pool`), which are not retryable waits. The Controller does **not** wait or
poll for the master's report inside the request: the master reports on its own schedule.

**An abandoned clustered-storage request is never a success.** `GET /api/v1/storage/cluster`,
`POST /api/v1/storage/cluster/apply` and `POST /api/v1/storage/cluster/verify-fence` answer
`499 the client disconnected before Core answered; retry with the same request_id` when the client
goes away while the handler waits on Core, instead of the bare `200` net/http emitted for a handler
that wrote nothing. No client reads it; the point is that an abandoned request is never in the
success range. The broker entry is released either way, so an exact retry with the same `request_id`
is accepted rather than refused as already active. This matches the pool-join relay's documented
`499`.

**Verification status.** Implemented and covered by Go `-race` Controller tests, each written and
observed failing before its fix: the network and storage discards including the applied-row refusal
and the previously unaddressable empty ID; a create against a member whose repository access is
stale warning without refusing, while a fresh member is not warned about; the relayed
`pool identity is held` refusal answering `409` with the guidance, `retryable` and the preserved
`host_error`, and a different host-side failure still answering `502`; and an abandoned
clustered-storage request answering outside the success range while an exact retry still succeeds.
Offline contract checks hold the routes, the schemas and the audit actions to the implementation.
**Not executed:** no native two-host lab rerun of any of these against the fixes, and no browser
review of the discard actions, the warning surfaces or the add-host retry banner.

### Controller-driven pool join (`POST /api/v1/pools/{id}/members`)

API impact: **new contract**, implemented. Three Controller routes, one Core command kind, one
host-local read route, two report-envelope fields, one inventory field and one capability. The
Controller makes exactly two calls and drives nothing: it asks the pool master for a single-use
join invitation, and it hands that invitation to the joining host as one durable command. The
joining Core performs the whole peer exchange with the master itself, over the pool mTLS peer
listener that the host-local procedure already uses. Nothing about membership is computed,
persisted or reconciled by the Controller; delete it and re-adopt the pool master and every
member, name, peer origin and interrupted join comes back from the hosts.

**Capability.** `pool-join-v1`, advertised in the protocol v2 capability list and absent from the
legacy v1 list. Both the master and the joining host must advertise it; otherwise the Controller
refuses with `409 adding a host to a pool needs pool-join-v1 on both hosts; upgrade both hosts`
and the host-local procedure below remains the documented alternative. A Controller that predates
this work sends no `pool_requests` and ignores `pool_results`, so an upgraded Core loses nothing;
an upgraded Controller sends the request only to a host that reports protocol v2 with the
capability, so an older Core is never sent a field it cannot decode.

**Invitation broker (Controller → master, one report round trip).** `ReportResponse.pool_requests`
carries `PoolRequest{request_id, pool_id, host_id, action:"invite", member_host_id, master_url}`
to the master; the master answers on its **next** report in
`ReportRequest.pool_results` with `PoolResult{request_id, pool_id, host_id, status, error?,
observed_at, invitation?}`, where `invitation` is
`{master_url, pool_ca_pem, invitation, expires_at}`. The broker holds one pending request in
Controller memory, waits 45 seconds (comfortably more than a heartbeat), writes nothing to the
database and keeps nothing after the exchange. Core refuses a request whose `host_id`/`pool_id`
is not this master or whose action is not `invite`. The Controller discards a result whose pool,
host or `request_id` does not match, or which succeeded without an unexpired invitation, and
answers `504` with a Core-shaped `PoolResult` body rather than the usual `{"error": …}`. A report
carrying more than 32 results is a malformed report (`400 too many pool results`). The same broker
carries `promotion-preflight` (answered with `promotion`) and `bmc-probe` (answered with
`bmc_probe`, waited on for 3 minutes); see the fence-verified promotion routes below.

**The invitation itself.** Single use, 15 minutes, bound to the pool, the master and the joining
host. It is the existing one-use offer record with no CSR hash: the master binds the presented CSR
at **first redemption** and stores it with the issued certificate, so the existing replay rule is
unchanged — the same invitation always returns the same member certificate and never admits a
second member, a different CSR on a redeemed invitation is refused, and an expired one is refused.
The Controller may still hand a joining host an invitation whose `expires_at` is up to two minutes
in the past by Controller wall clock, so ordinary clock skew does not force a full re-join; Core
remains the authority that refuses a truly expired invitation at redemption. The master also
records the `master_url` it issued an invitation for as its own member `peer_origin` when that is
empty. An invite with an empty `master_url` asks the master to work out its own origin from the
pool-peer listener it is configured with (`-pool-peer-listen`): always that listener's port, and
its address too when the bind is to a concrete address. Only a wildcard bind leaves the address
open, and then the master uses the source address of its default route, refused as `the pool
master cannot work out its own peer address because it has no default route` when there is none. The invitation's
`master_url` is always the master's recorded origin, and it is what the Controller forwards to the
joining host. Masters built before this rule refuse an empty `master_url`; supply one explicitly for
them.

**Durable command (Controller → joining host).** `protocol.Command{kind:"pool-join",
pool:{action:"join", request_id, member_host_id, master_url, pool_ca_pem, invitation,
peer_origin?}}`, journaled by the existing managed-command admission path, terminal
`protocol.Result`. It is the one pool command addressed to a host that is not the master.
`PoolCommand.Valid()` requires `request_id`, `member_host_id` (≤128), `master_url` (≤512),
`pool_ca_pem` (≤16384) and `invitation` (≤8192) for this action, with `peer_origin` optional
(≤512) and no membership record — the target is not a member yet, and the Controller never
composes a membership for it. The joining Core runs a durable staged join persisted beside its
pool CA (`precheck → csr → joined → rehomed → advertised → ready`), resumes at the stage still to
perform after a crash, reboot or Controller outage, and reports progress as
`inventory.managed.join` = `PoolJoinStatus{operation_id, stage, status, error?, observed_at}`.
`ready` is `succeeded`; a host that is already a member but has not finished rehoming or
advertising reports `needs_attention` and stays a member; a precheck refusal, or an exhausted
attempt budget at a **pre-admission** stage (`precheck`, `csr`), is `failed` rather than an endless
retry of an expired invitation. **Only pre-admission stages can end `failed`.** The `joined` stage
is the admission stage — the master commits the membership before its reply is delivered — so an
exhausted budget there can mean the host is already a ready member and only the reply was lost.
Reporting that as `failed` would leave the master saying member and the host saying not, with both
recovery paths shut, so it is `needs_attention` and the retry continues. What makes the truth
learnable is the redemption replay: the invitation's 15-minute TTL bounds only the **first**
redemption, and an offer that has already issued a certificate replays past its expiry, returning
the same certificate to the same host and public key. It admits nothing new — the CSR hash is bound
and the member row already exists. The joining host **clears the invitation from its own durable
record the moment the `joined` stage completes**; resuming from any later stage needs only the
master URL and the pool CA. The Controller retains `managed.join` alongside `managed.membership` in stored
inventory, so a replaced Controller adopts an interrupted join instead of losing it.

**Client-facing routes.** All three take an administrator session; the POST additionally requires
the same HTTPS `Origin` and `X-CSRF-Token`, like every other Controller mutation.

- `POST /api/v1/pools/{id}/members` — body
  `{request_id, host_id, peer_origin, master_url?}`. `master_url` is an optional override: omitted,
  the master supplies its own origin (above), and the web UI never sends it. `peer_origin` and `master_url` must be bare
  HTTPS origins (scheme, host and port only). A single trailing `/` is accepted and normalised
  away, exactly as the host-local route accepts it; the normalised, slash-less form is what is
  stored and forwarded, so `https://host:9444` and `https://host:9444/` are the **same** request
  for exact-retry replay rather than a changed payload. A path, query, fragment, userinfo or a
  non-HTTPS scheme is still `400`. `202` with `{host_id, peer_origin, operation}` — never the
  invitation, the pool CA or the master URL. An exact retry of the same `request_id` and payload
  returns the original operation with `200` and starts no second join; the same `request_id` with
  any changed field, `master_url` included, is `409 request_id already used with another payload`.
  `404` for `pool not found` / `host not found`. `409` for a host already in the pool, a master or
  joining host that is not fresh and active, a missing capability on either side, a host that is
  not the master of its own single-host pool, managed VMs, existing remote storage, an already
  active pool request, and for any
  refusal the master itself decided, relayed verbatim — a `failed` result that carries no message
  becomes `409 the pool master refused to issue a join invitation`, so a terse Core refusal is
  never an empty body. `504` on the broker deadline, with the
  Core-shaped body above; no command was created and retrying with the same `request_id` is safe.
  `499` when the client disconnects while the handler is waiting on the master's answer: nothing
  is written, the broker entry is released, no client reads the status, and the same `request_id`
  is safe to retry.
  `500` (`storage unavailable`, `inventory unavailable`, `pool join storage failed`) leaves the
  same retry safe. Acceptance is not membership: poll the members route and the operation.
- `GET /api/v1/pools/{id}/members` — projection only, zero new storage: the master's reported
  membership, each member's connection freshness, the four convergence checks
  (`pool-storage-reconciliation`, `pool-network-reconciliation`, `pool-media-reconciliation`,
  `pool-authority-replica`), counts of the master's `member_access` rows that are
  healthy/pending/failed/stale, per-member resource failures with the host-reported message, each
  member's own reported `join`, and the maintenance and authority-replica fields described in the
  two sections below. `authoritative` is true only while the master's connection is `fresh`; when
  it is not, the master's *observations* — `status`, `error`, `checks`, access counts, `failures`,
  `maintenance` and `maintenance_since` — are blank, `null` or `unknown` and must be shown as
  unknown rather than as the last mirror. `peer_origin` is part of the master's durable membership
  record rather than an observation of it, so it is reported either way; a quiet master does not
  make a member's address untrue. `pending` means the member has reported no access row for that
  resource yet; the master's own row carries no counts, because `member_access` is the master's
  record about its members. A `500 membership lookup failed` response means the membership
  lookup failed before a projection could be returned; this read-only request is safe to retry.
- `GET /api/v1/pools/{id}/join-candidates` — the compatibility preview for the dialog, composed
  from host-reported inventory only: connection, agent version, capabilities, existing
  membership, managed VMs, non-local storage, NIC names against each pool VLAN uplink, and CPU
  baseline. Each row is `compatible`, `will_not_converge` or `refuses_join`; `eligible` is false
  if and only if some row is `refuses_join`, and `will_not_converge` rows do not block a join.
  It is presentation, not authorization: Core decides every refusal again at submission.
  Each candidate also carries `peer_address` and `peer_port`: the host and port of the peer
  origin the candidate reports for itself in `inventory.managed.peer_origin` (an observation
  it works out from its configured peer listener at every report, the same way a master works
  out its own origin; the Controller mirrors it for display and stores it nowhere else), or
  `""` and Core's default port `9444` for a host that reports none. The dialog shows the
  operator only the address, prefilled, and composes the `peer_origin` it submits from the
  two; the POST contract above is unchanged. The join records the member's peer origin
  durably on the master as before.

**What is stored, and for how long.** The operation row, the managed-resource row (payload = the
client request verbatim, with no invitation) and one audit row (`action = pool-join`, actor = the
administrator, resource = the join record; no secret and no detail). The stored *command* does
carry the invitation and pool CA, because the dispatch path reads the command back out of the
database; both fields are scrubbed as soon as the operation leaves `queued`, and unconditionally
once the row is 15 minutes old. The scrub runs on Controller start, at the top of all three
pool-join routes (the POST, the members GET and the join-candidates GET), from a timer armed by the
issuing request, and — because none of those survives a restart — **from the health tick, at most
once a minute**, so the TTL holds in a Controller that restarted inside the window and was never
asked about pools again. That sweep timestamp is in memory only; nothing durable is added. A row
with an unparseable `created_at` is scrubbed rather than skipped. Residual risk, stated plainly: a
single-use, 15-minute, host- and pool-bound token sits in the Controller database for one heartbeat
in the normal case, and for at most one minute past its TTL if the joining host never collects it.
Redeeming it still requires the joining host's own CSR and reachability to the master, and the
master binds that CSR on first redemption and refuses a second member.

**A scrubbed invitation is never redelivered empty.** Redelivery reloads the command from
`managed_resources`, so a scrubbed `pool-join` would otherwise be re-sent with empty `invitation`
and `pool_ca_pem`, which the host must reject. What happens instead depends on what the row had
already done. A row scrubbed while still **queued** was never collected, so the operation is failed
terminally with `the join invitation expired before the host collected it; start the join again`,
visible on `GET /api/v1/operations`. A row scrubbed after **dispatch** is simply never re-sent:
failing it would declare a join legitimately in progress dead, and that join's own result settles
the operation.

**Interruptions.** A Controller lost between the invite and the command leaves an unredeemed
invitation that expires in 15 minutes and no membership change. A Controller lost after the
command changes nothing: the joining Core finishes on its own and reports on reconnect. A joining
Core interrupted after its CSR resumes with the same CSR; after the master issued the certificate
but before the reply landed, replaying the same invitation returns that same certificate and
membership. A rehome that has not finished is reported as `stage:"rehomed",
status:"needs_attention"` — the host **is** a member — and is retried by Core, never as a failure
that would tell an operator the join did not happen.

**Host-local view (`GET /api/local/v1/pool`).** Authenticated local session, read-only:
`{pool_id, pool_name, role: master|member, master_host_id, members[], join}`. It answers which
pool this host belongs to without a Controller, which is what a host-local recovery procedure
needs; `409 enroll this Core before reading its pool` before enrollment and
`503 pool membership is unavailable` when the record cannot be read, rather than an invented empty
pool. `POST /api/local/v1/pool/join` now returns `200 {host_id, pool_id, join}` and reports the
stage it reached; a `409` from it means the host did **not** join, and an unfinished rehome is no
longer reported as a conflict.

**Verification status.** Implemented and covered by offline contract checks
(`tests/test_api_contract.py`) and Go unit tests for the master's invitation and CSR binding, the
staged join and its resume from every stage, the precheck refusals, the concurrent-join race, the
broker round trip and deadline, the `400`/`404`/`409` matrix and both projections. A native
two-host join driven from the Controller, and a browser review of the Hosts tab, have **not** been
executed for this contract; see [Join a pool](guide/join-a-pool.md#verification).

### Member join time in the members projection (changed contract, 2026-09-23)

API impact: **changed contract**, additive — `GET /api/v1/pools/{id}/members` gains
`members[].joined_at`, an RFC 3339 UTC string: when the pool master admitted that member, or
created the pool for its founding host. It is the `observed_at` of the member's row in the master's
durable membership record (`PoolMember`), which Core sets on admission (and on re-admission after an
eject) and which no master change, eject or maintenance write rewrites. No Core or peer change:
every Core already records and reports it in `Inventory.managed.membership`. Like `peer_origin` it
is a record, not an observation, so it is reported whether or not `authoritative` is true; it is
`""` for a row with no recorded time (the synthesised single row of a master that reported no
membership). **State owner: core** — the pool master's membership record; a fresh Controller reads
it from the master's next inventory. Verification: `go test ./internal/controller -run
TestPoolMembersReportsWhenEachMemberJoined`, `go test ./internal/agent -run
TestNextPoolMembershipKeepsJoinTimes`. Introduced in development product 0.4.8.

### Host maintenance mode (POST /api/v1/pools/{id}/members/{host_id}/maintenance)

API impact: **new contract**, implemented. One Controller route, one Core command kind
(`pool-maintenance`), two membership inventory fields and one capability. Maintenance is a durable
per-host drain flag that the **pool master** keeps inside the membership record it already owns, in
its own `pool-peer.json`. The Controller relays one command and projects what the master reports;
it records no drain of its own, and a fresh Controller adopts the flag from
`inventory.managed.membership` with nothing to re-enter.

**Capability.** `pool-maintenance-v1`, advertised by the master in the protocol v2 capability list
and absent from the legacy v1 list. Only the **master** needs it, because the refusal that makes
the flag mean anything is the master's. Without it, `maintenance_supported` is false in the members
projection and no control may be offered; submitting anyway is
`409 maintenance mode needs pool-maintenance-v1 on the pool master; upgrade the pool master`.

**What maintenance does, and what it deliberately does not.** While the flag is set the master
refuses, from its own record: a new VM placement onto that host, `define` and `start` admission for
it, and autostart-by-placement, which skips the host and logs why. Every refusal **the master
itself decides** carries the one sentence `the selected VM host is in maintenance mode; take it
out of maintenance before placing or starting VMs on it`, whether it arrives in a command result
or a placement preflight, so a Controller maps it without parsing several.

Three other sentences exist, and each names a different actor on purpose — read the wording as
telling an operator *who* refused:

- The Controller's own pre-dispatch check on `POST /api/v1/vms`, `PUT /api/v1/vms/{id}` and
  `POST /api/v1/vms/{id}/media` says `the selected host is in maintenance mode; take it out of
  maintenance before placing or starting VMs on it`. It is **presentation and early failure
  only**, made from the master's reported membership so the operator is not offered a host the
  master will refuse; the authoritative refusal is still the master's, and it arrives as a failed
  operation result. The two sentences are deliberately not the same text.
- The drained host's own libvirt-hook refusal, described next, refuses a narrower action —
  a direct start, not a placement — and says `managed VM startup is refused while this host is in
  maintenance mode; take it out of maintenance before starting VMs on it`.
- Autostart-by-placement logs `pool VM autostart skipped: this host is in maintenance mode`
  rather than refusing anything.

Do not collapse these into one string. A single shared sentence would make an advisory Controller
refusal indistinguishable from the master's authoritative one, which is the distinction the
architecture rests on.

The drained host refuses starts **itself**, before any master round trip, which is what makes the
flag mean anything for the commonest shape of all — a guest on **local** storage, which needs no
permit from the master. The check sits at the same choke point as the restriction guard: `start`,
`wake` (an operator pressing Start on a suspended guest) and `vm-create` with `start_after_create`
are all refused, on local and shared storage alike, whether or not the master is reachable. The host
reads its own freshest **verified** membership copy — its own record on the master, the stored
signed authority replica on a member — so this maintenance refusal needs no master round trip.
Start, wake, reboot and media changes additionally require current master owner admission;
shutdown and force-stop remain available during master loss. Staleness on a member is at most one master probe interval while
the master is reachable, and as old as the last successful replica push while it is not.

Two further layers close the reboot path, which is the workflow maintenance exists for. The agent
**disables and verifies libvirt's `autostart` flag** for every managed pool domain. The flag
stays disabled when maintenance ends; Core's boot attempt uses current owner admission and
the desired value retained in the managed journal.
And Core's libvirt **`StorageGuard` hook refuses a managed domain on a drained host** before any
repository work. On the **pool master**, including every single-host pool, it reads `state.json` and
`pool-peer.json`; on a **member** it reads the verified `pool-replica.json` membership copy the
master pushes after a drain change, so a manual `virsh start` is refused on every pool host once
that copy is current. The hook holds with no Controller connected and fails closed on an unreadable
record. Autostart suppression is a separate layer that still applies while drained.

`reboot` and `force-reboot` are deliberately **allowed**: both require the guest to be `running`
there already, so they are not a new placement. It does **not** stop a running guest, block `stop`,
`shutdown` or `force-stop`, block host-local recovery, or block the member's own convergence — an
operator draining a host is usually about to power guests off, and drain intent is not a fence. The flag is
deliberately **outside the membership generation equality rule**: setting it does not bump the
generation, so draining a host cannot invalidate an in-flight placement, a published member
inventory or a peer generation check. Enforcement for anything the master decides is on the master, so it survives a
Controller outage or replacement unchanged; the host's own refusals above are the layer that covers
what never reaches the master. A
single-host pool's host **is** its master: draining it is allowed, and its own record then refuses
placements and starts on it.

**Durable command (Controller → master).** `protocol.Command{kind:"pool-maintenance",
pool:{action:"maintenance", request_id, membership:{master_host_id}, member_host_id, enabled,
expected_version?}}`, journaled by the existing managed-command admission path, terminal
`protocol.Result`. It is addressed to the **master**, never to the host being drained; entering or
leaving maintenance never contacts that host, so an unreachable member changes nothing.
`PoolCommand.Valid()` requires only `request_id`, `membership.master_host_id` and `member_host_id`
for this action and skips the membership rule, exactly as `join` does: the master reads its own
record, and a membership composed in the Controller could already be stale. `enabled` is a desired
state, not a transition, so re-sending it succeeds and leaves `maintenance_since` unmoved, which is
what makes an exact retry after a lost reply safe.

**Inventory.** `PoolMember.maintenance` (omitted when false) and `PoolMember.maintenance_since`
(always present, zero when not drained, in the manner of `observed_at`).

**Client-facing route.** Administrator session, same HTTPS `Origin` and `X-CSRF-Token`. Body
`{request_id, enabled, expected_version?}`; unknown fields are refused. There is **no
`confirmation` field**: draining stops nothing that is running, so there is nothing to confirm
against, and Core accepts none. The guest list an operator should see first is returned by the
members projection instead. `202 {host_id, enabled, operation}`; an exact retry of the same
`request_id` and payload returns the original operation with `200`. `400 pool, host and a stable
request_id are required`. `404 pool not found` / `host not found`. `409` for `the pool master must
be fresh and active to record maintenance`, the missing capability above, `the pool master has not
reported its membership; maintenance cannot be recorded yet`, `this host is not a member of the
selected pool`, `the pool master reports membership generation <n>; reload the pool and retry`, and
the shared `request_id already used with another payload` / `request_id already used for another
operation`. `500 storage unavailable` / `inventory unavailable` / `maintenance storage failed`
leaves the same `request_id` safe to retry. Audit: `action = pool-maintenance`,
`resource_id = pool-maintenance:<member host id>:<operation id>`; nothing in the request is secret.

**Asynchronous refusals are not an HTTP status.** The command is durable and asynchronous, so Core's
own refusals can never be the status code of the submitting request. They arrive as the operation's
`status:"failed"` with the master's text: `only the pool master records maintenance mode`, `no such
member of this pool`, `an ejected host has no maintenance state`, `pool membership is unavailable`,
`pool membership changed since this request was composed; review the pool and try again`,
`maintenance command carries no maintenance request` and the `PoolCommand.Valid()` texts. Exactly
two of those — `pool membership is unavailable` and `pool membership changed since this request was
composed; review the pool and try again` — are the **409 class**, and the members projection marks
them `last_maintenance.conflict = true` so the operator sees a conflict where they acted. A durable save
that may or may not have landed is reported `needs_attention` with `maintenance mode could not be
recorded durably`: the next inventory report states the truth and the exact retry is safe. A
recorded change also pushes the authority replica immediately rather than waiting for the next
probe tick, so a drained member starts refusing from its own verified copy within seconds. That
push is best effort and is **not** the operation's result (changed 2026-09-20; it previously
reported `needs_attention` with `maintenance mode is recorded but has not yet been replicated to
pool members`): the drain is complete when the master's own record is saved, which is the record
every placement, definition and start is refused by, and a member that was unreachable catches up
from the next tick exactly as it does for every other authority write. The lag is reported per
member in the projection's `replica` status and `connection`, not as a failed drain.

**Members projection additions.** Pool level: `maintenance_supported`. Per member: `maintenance`
(`true`/`false`, and `null` — unknown, never assumed false — whenever `authoritative` is false),
`maintenance_since`, `guests` (`{known,total,running,names}` from that host's own reported
`inventory.vms`, with `known:false` and empty counts whenever the host is not `fresh`, because a
host that is not reporting has no guest list, and `names` capped at 50), and `last_maintenance`
(the newest `pool-maintenance` operation this Controller holds for that member, with `conflict` as
above, or `null`). `guests` and `last_maintenance` are not master observations, so they are
reported whether or not the master is fresh.

**Placement presentation.** The Controller's `placementHost` pre-check additionally refuses a
target the master's mirrored membership reports as drained, with `409 the selected host is in
maintenance mode; take it out of maintenance before placing or starting VMs on it`, on
`POST /api/v1/vms`, `PUT /api/v1/vms/{id}` and `POST /api/v1/vms/{id}/media`. Only a **fresh**
master's record is used, and this is presentation and early failure only: the Controller decides
nothing about maintenance and Core refuses again.

**Verification status.** Implemented and covered by offline contract checks
(`tests/test_api_contract.py`) and Go `-race` unit tests for flag persistence and reload, the
placement/define/start/autostart refusals, stop still being permitted, idempotent enable and
disable, membership `Valid()` being unaffected, and the Controller's `409` matrix and projection. A
scoped native/browser walkthrough entered maintenance with a running VM, refused a start and
placement, then exited maintenance and completed normal start/stop. Its reboot/autostart case
remains open; that is distinct from the f714 returned-master restriction boot result. These results
do not close the Phase 4 gate; see the [current native-acceptance record](qa/2026-09-19-phase4-native-acceptance.md).

### Member authority replica (peer GET|POST /api/v1/pool/replica)

API impact: **new contract**, implemented. One peer route on the **member**, one signed payload,
one inventory field on each side, four observation fields on the master's membership record, one
health check and one capability. No Controller route and no Controller storage: the fields below
reach an operator through the existing members projection, read out of host reports.

**What it is for.** The pool master's durable authority — membership, the `pool-vms.json` owner
projection, storage, networks, media and native storage-cluster records, plus its own CA key sealed
per member — is copied to every ready member so that it is *recoverable* if the master is lost. It
is **never authority**: a member stores it, reports that it holds it, and acts on none of it. No
membership, VM, storage, network or media record on a member is read from or changed by a replica,
a replica grants no execution, and no health judgement is derived from one — a promoted master
re-collects fresh member observations rather than trusting replicated ones. Nothing in this release
promotes a member or opens the sealed key *merely by holding a replica*; the separate
[fence-verified promotion workflow](#fence-verified-manual-promotion-and-safe-rejoin-post-apiv1poolsidmasterpromote-post-apiv1poolsidmembershost_idfence-profile)
performs those actions only after its checks pass.

**Capability.** `pool-authority-replica-v1`, v2 list only, on both peers. A master with it and a
member without it records that member at replica generation `0` with `pool member does not support
pool-authority-replica-v1; upgrade it before relying on its replica`; the master never degrades to
an unsigned or partial copy.

**Peer route (on the member), `AuthenticatePoolMaster`, pinned mutual TLS.**

- `GET /api/v1/pool/replica` → `200 PoolReplicaStatus{generation, digest?, master_host_id?,
  signed_at, received_at, verified, error?}`, an empty object when the member holds nothing. This
  call is also how the master learns the member's certified peer public key for the wrap, from the
  pinned TLS leaf, so no public key is ever put on the wire or into the membership record.
- `POST /api/v1/pool/replica`, body ≤ 1 MiB (`protocol.PoolReplicaLimit`) with no trailing JSON →
  `200 PoolReplicaAck{host_id, generation, digest, verified, sanlock_host_generation?, corosync_key_ready?}`; `409` with the same shape plus a
  bounded `error` on refusal; `400 invalid pool authority replica`; `401 replica peer rejected`;
  `405` with `Allow: GET, POST`. Idempotent: the same generation with the same content returns the
  same acknowledgement. `sanlock_host_generation` is present only after the member joins the
  configured authority lockspace; an absent or zero value cannot acknowledge lease preparation.

**Payload.** `protocol.PoolAuthorityReplica{pool_id, master_host_id, generation, member_host_id,
created_at, membership, vms, storage, networks, media, storage_clusters, ca_key_wrapped?,
corosync_key_wrapped?, fence_credentials?, witness_credentials?, sections[], signature}`. `sections[]` is exactly
`membership, vms, storage, networks, media, storage_clusters`, in that order, each with the SHA-256
of its canonical JSON, so a truncated or reordered copy is refused rather than used. The signature
is ECDSA P-256/SHA-256 with the **pool CA key** over the whole replica minus `signature`; a member
verifies it against the pool CA it pinned
at join, which keeps verification working offline and after the signing master is permanently gone.
`member_host_id`, `created_at`, `generation` and the wrap are inside the signature, so a replica
cannot be re-addressed, renumbered or re-wrapped in transit. `ContentDigest` deliberately excludes
those, so it identifies the authority itself: every member of one generation reports the same
digest, and a re-push is recognizable as identical rather than as a conflicting second copy.

`witness_credentials[]` contains `PoolWrappedWitnessCredential{host_id, fencing_generation,
wrapped}`: the native qdevice PKCS#12 bundle sealed separately for each member and signed with
the replica. Controller clients never receive it. Optional `membership.storage_lease` holds the
exact NFS repositories, authority repository, timeout bounds and `prepared_generation`;
`membership.next_sanlock_host_id` prevents lease ID reuse. Each `PoolMember` carries
`sanlock_host_id` and `sanlock_host_generation` to bind later native lease observations to the
same member. These preparation records do not enable storage-lease takeover.

`corosync_key_wrapped` carries the master's 128-byte pool Corosync authentication key,
sealed for the addressed member with a separate `nex pool corosync key v1` context bound
to pool, member and replica generation. A member verifies the signature, opens and durably
stores the key before acknowledging a pool cluster replica; each host installs that same
key before starting Corosync. A missing or mismatched key blocks cluster formation. A
member uses the verified replica's current witness and missing peer addresses for pool
cluster configuration, including same-membership-generation witness updates, while
keeping its local maintenance, arming and peer identity record. The member's
`corosync_key_ready` receipt is mandatory whenever the master sends a pool key; an older
member that ignores the new field and omits the receipt cannot be counted as ready.
Upgrade both hosts to complete the pool cluster handshake.
The member refuses a missing key with `pool authority replica omitted the Corosync key`;
the master reports `pool member acknowledged the replica without the Corosync key` when
the receipt is absent. A retry uses the same signed generation and immutable key.

**Sealed CA key.** `ca_key_wrapped` is the pool CA private key wrapped for exactly one member:
ECDH between a fresh ephemeral P-256 key and that member's certified peer public key,
HKDF-SHA256 with the context `nex pool authority replica v1\0<pool>\0<member>\0<generation>`, then
AES-256-GCM with the same context as additional data. Stdlib only, no new PKI. A copy wrapped for
one member cannot be opened by another or replayed at another generation. Members store it sealed
at mode `0600` in the already-`0700` state dir; no code path in this release unwraps it.

**Durable records.** Master: `AuthorityGeneration` and `AuthorityDigest` in its existing
`pool-peer.json`. Member: `<state-dir>/pool-replica.json`, written atomically (temp file +
rename), holding the replica exactly as received plus its content digest, the member's own
`received_at` and `verified`. A push interrupted mid-write leaves the previous replica intact.

**Generation rules.** The master rebuilds its authority on the existing probe tick, and advances
and persists the generation **only when the content digest changed**, before pushing; it then
pushes to every ready member that is behind. Ordinary replication is tick-driven; operations
that require durable replica acknowledgements also push synchronously and wait for their exact
generation and content digest. A crash after the bump simply pushes on the next tick,
and generations may skip. A member accepts a higher generation, re-accepts the same generation with
identical content, refuses a lower one (`pool authority replica generation is older than the stored
replica`) and refuses the same generation with different content (`pool authority replica
generation was already stored with different contents`).

**Refusals** are bounded to 200 characters and collapse to `pool authority replica refused` if they
contain control characters: `pool authority replica names another pool`, `… is not from the current
pool master`, `… is addressed to another host`, `… is not signed by this pool's CA`, `… does not
declare every section`, `… section digest does not match`, `… VM ownership record is invalid`, `…
could not be stored`, `pool membership is unavailable`, `pool peer state is unavailable`, `stored
pool authority replica is unavailable`.

**Reported state.** A member reports `inventory.managed.replica` = `PoolReplicaStatus`. The master
overlays its own observations onto the reported copy of each member's membership record —
`replica_generation`, `replica_digest`, `replica_error`, `replica_observed_at`. Those four are
**memory-only observations**: they are never written into the durable membership file and are never
an input to the generation equality rule, and a member that has never acknowledged anything is
reported at `0`, honestly. Both sides also report a `pool-authority-replica` health check: on a
member `unknown`/`unknown` while awaiting the first push, `failed` with the refusal text, and
`unknown`/`stale` after two probe intervals (at least one minute) with no push and no master poll;
on the master, one check covering its ready members, `unknown` until a member has acknowledged
(a member's status read that matches the current generation and digest refreshes its
acknowledgement, so a steady pool stays `healthy` without re-posting),
`failed` with that member's refusal, and `unknown`/`stale` when an acknowledgement is old or behind
the master's current generation.

**No pool authority generation on the wire.** Nothing reports the master's own
`AuthorityGeneration`; the only master-reported numbers are the per-member acknowledgements. The
Controller therefore derives no absolute generation — inventing one would be Controller-computed
state — and the members projection carries none.

**Members projection additions.** Pool level: `replica_supported` (the master advertises the
capability). Per member: `replica`, which is `null` on the master's own row and otherwise
`{generation, digest, verified, received_at, acknowledged_generation, acknowledged_at, error,
state}`. The first four are the member's own report, the next two are what the master last observed
it acknowledge, and `error` is the master's reason when it has one and otherwise the member's own.
`state` is a first-match ladder over those two reports: `unknown` when the master or this member is
not `fresh`; `unsupported` when either lacks the capability; `refused` when either reports an
error; `awaiting` when both generations are `0`; `unverified` when the member holds a replica it
has not proven against the pool CA; `behind` when the two generations disagree or another `fresh`
member of this pool is acknowledged at a strictly higher generation; `current` otherwise. Freshness
deliberately outranks capability, because a stale host's capability list is a memory too. `behind`
with `generation` above `acknowledged_generation` means the **master's observation** lags, not the
member; both numbers are shown because the single word cannot say which side is stale. The peer
comparison is the only evidence a lagging member is lagging, precisely because no absolute
generation is reported.

**What is not replicated, and why.** Published per-host inventory *snapshots* (`state.Inventory`)
are observations, not authority, so they are excluded and a promoted master re-collects fresh ones;
this also keeps the replica well inside the 1 MiB bound. SMB credential receipts are stripped, as
the storage peer list already strips them, so a future promotion will hold an SMB repository record
whose credential an operator must re-supply. Acknowledged generations are observations and stay in
memory.

**Related hygiene landed with it.** `pool-vms.json` owner records are now pruned, but only on
positive evidence: the publishing host is the record's owner, its inventory is complete and at the
current membership generation, it carries a succeeded `vm-delete` tombstone for that VM ID in the
new `deleted` field of `PoolVMInventory`, and no other host's
stored inventory still defines that VM ID. Absence is never read as deletion. The pool storage peer
list now also fails closed with `503 pool resource list unavailable: managed journal is incomplete`
when the managed journal is incomplete, matching networks and media, so a member never converges
against a short list.

**Verification status.** Implemented and covered by offline contract checks
(`tests/test_api_contract.py`) and Go `-race` unit tests for the sign/verify round trip, per-member
wrapping including wrong-member and wrong-generation failures, tamper and truncation refusals with
the stored replica proven unchanged, the generation rules, crash-before-push generation
persistence, idempotent re-push, lagging and unreachable members, that a replica grants no
execution, both inventory projections, both health checks, the size bound on the wire and on disk,
the pruning matrix and the fail-closed storage list. Scoped native evidence includes signed replica
push, generation advance, lagging-member catch-up and re-earned observations after a master restart.
Browser review of the replica column and stale states remains incomplete. These results do not close
the Phase 4 gate; see the [current native-acceptance record](qa/2026-09-19-phase4-native-acceptance.md).

### Planned master change and member eject (POST /api/v1/pools/{id}/master, DELETE /api/v1/pools/{id}/members/{host_id})

API impact: **new contract**, implemented. Three Controller routes, one Core command kind, two peer
routes, one signed statement, two inventory fields and one capability. The Controller relays one
durable command to the pool's current master and stores nothing else: no stage, no timer, no
generation, no membership, no digest. Core runs the whole transfer itself, so a Controller that is
restarted, replaced or disconnected mid-transfer keeps nothing to recover and re-projects the
transfer from the participants' next reports.

**What it is for.** Moving the pool authority from the current pool master to a ready member of the
same pool, and removing a member from a pool. Both keep the pool, its networks, its storage
registrations, its ISO library, its virtual machines and their identities and names: nothing is
recreated and nothing has to be entered again. There is **no automatic succession for a planned master change**: this route is an operator action against a *cooperating* master. Core separately supports automatic promotion in eligible two-host pools through a recorded BMC fence profile; that path is not a request to this route. Promotion with an
unreachable master is otherwise the separate [fence-verified manual promotion workflow](#fence-verified-manual-promotion-and-safe-rejoin-post-apiv1poolsidmasterpromote-post-apiv1poolsidmembershost_idfence-profile). Automatic promotion is limited to the surviving member with a verified replica in a two-ready-host pool, after two qualifying observations at least 15 seconds apart, with Host fencing enabled and a recorded usable BMC profile for the old master. Storage-lease and witness-based promotion remain unavailable; the qdevice witness described below supplies no fence proof.
An in-progress automatic promotion checks the setting again on resume and before authority adoption.
If the candidate's membership or verified replica says Host fencing is off, it retains its durable
stage and proof, reports a refusal and does not activate; it can resume only after a verified on
record arrives and the normal fence proof still passes.

**Capability.** `pool-authority-transfer-v1`, v2 list only. Required on the pool master, on the
selected host and on every ready member, because a member that cannot re-point would keep following
a host that is no longer the pool master. A peer that answers `404`/`405` on either peer route is
reported as `pool peer <id> does not support pool-authority-transfer-v1; upgrade it before changing
the pool master`. The ejected host is deliberately **not** gated: it may be unreachable.

**Controller routes.** Administrator session, same HTTPS Origin and `X-CSRF-Token`, `{"error": …}`
bodies, `202` on acceptance, `200` on an exact replay of the same `request_id` and payload, `409` on
the same `request_id` with a changed payload.

- `POST /api/v1/pools/{id}/master` — `{request_id, host_id, expected_version?, confirmation}`, where
  `confirmation` is **the pool's name** exactly as `pool_name` reports it, compared
  case-insensitively after trimming. A master change pauses every pool authority write until it
  completes, so the pool is typed by hand.
- `DELETE /api/v1/pools/{id}/members/{host_id}` — `{request_id, expected_version?, confirmation}`,
  where `confirmation` is **the ejected host's name** (`members[].name`), compared the same way.
- `POST /api/v1/pools/{id}/master/cancel` — `{request_id, host_id, expected_version?}`, where
  `host_id` is the transfer's target (`transfer.target_host_id`). No typed confirmation: a cancel is
  the undo, and after the hand-off Core refuses it anyway. Two optional fields, confirmed **both or
  neither** (`400 powering the transfer target off is confirmed with both fence_confirmed and
  fence_host_ids naming that host once, or with neither`): `fence_confirmed` and `fence_host_ids`,
  which must name that one target. They are the operator's explicit agreement to power the target
  off, and Core uses them only in the one case they exist for — a handed-off target that has stopped
  answering, where this pool's authority would otherwise stay with a dead host. Core refuses them
  while the target is reachable (`pool peer <id> is reachable; retry the cancellation rather than
  powering it off`). Neither field is accepted on the master-change or eject route: each route has
  its own body struct, so a fence confirmation aimed at the wrong one is `400 invalid JSON request`.

All three produce exactly one `operations` row plus one `managed_resources` replay row of kind
`pool-transfer` addressed to the **current master**, and one audit row — `pool.master-change`,
`pool.eject` or `pool.transfer-cancel` against the pool ID, beside the automatic `pool-transfer`
row. Adoption of a completed master change writes `pool.master.adopt`; the new single-host pool an
ejected host re-initializes into is recorded with `pool.create`, the same action enrollment uses.
Nothing pool-scoped is persisted: not the generation, not the stage, not the authority, not the
membership. Every literal refusal of every route, with its status code, is in the
[generated reference](api/index.html).

Two refusals are deliberately **not** pre-checked: an eject blocked because the host still owns pool
VM records, and an eject blocked because it still holds a pool-shared storage reservation. Those are
Core's decision, and they arrive asynchronously on the operation as `last_transfer` rather than as a
Controller-invented HTTP status.

**The command.** `x-commands: pool-transfer`, payload `PoolCommand{request_id, action:
master-change|eject|transfer-cancel, membership.master_host_id, member_host_id, expected_version?}`.
Terminal results are `succeeded`; `failed`, which is a decision and means nothing moved; and
`needs_attention`, which is **not** an error but "durable and still running — the hosts finish it".

**Peer routes.**

- `POST /api/v1/pool/transfer`, body ≤ 256 KiB (`protocol.PoolTransferLimit`) with no trailing JSON.
  `{operation_id, action: activate|abort|repoint|leave, notice}`. `activate`, `abort` and `leave`
  are admitted only from the addressed host's current pinned pool master; `repoint` is admitted from
  any current ready member, because the host issuing it has just become the master and the addressed
  host does not know that yet — which is exactly what the signature proves. Every action is
  idempotent by `operation_id`. `200 PoolTransferReply`, `409` with the same shape plus a bounded
  `error` (including `the new pool master's backup catalog import is pending` while
  activation has committed but catalog reconstruction has not finished),
  `400 invalid pool transfer request`, `401 pool transfer peer rejected`, `405` with
  `Allow: POST`.
- `GET /api/v1/pool/authority` — the read-only authority probe, admitted to **any** holder of a
  certificate this pool's CA issued, **including one this pool has ejected**: an ejected host must
  be able to learn it is no longer a member, and it can only learn that from the pool that ejected
  it. `200 PoolAuthorityStatus`, `401 pool peer certificate is required`, `409 pool membership is
  unavailable`, `405` with `Allow: GET, POST`. The handler answers `POST` identically; nothing in
  Nex Core calls that form. The caller's own membership status is decided from the certificate the
  transport proved, never from a name in a body. A master answering a ready member that is behind
  also returns a pool-CA-signed re-point notice, so a host that missed a push converges from
  whichever host it can reach without trusting a stale one — a probe answer alone changes nothing;
  only a signature at a strictly higher generation does. `?vms=1` adds the host's own VM ownership
  records, which a later slice's promotion proof consumes.

**The signed statement.** `protocol.PoolAuthorityNotice{pool_id, operation_id, action: promote|eject,
from_host_id, to_host_id, target_host_id, authority_generation, membership, issued_at, signature}`,
ECDSA P-256/SHA-256 over the whole notice minus `signature`, made with the **pool CA key**. It is
used twice: the **hand-off**, signed by the outgoing master, naming the exact authority generation it
froze and the host it handed that generation to; and the **re-point**, signed by the new master with
the pool CA key it now holds, carrying the new membership at a strictly higher membership generation.
Every host — the outgoing master included — adopts a new master only from such a notice, verified
against the pool CA it pinned at join, and only at a strictly higher generation. No host ever takes
another host's word for who the master is.

**Why there is never a second master.** The freeze is persisted in `pool-peer.json` before anything
else happens, and every authority write reads it inside the same lock section that performs the
write, so after the freeze is durable no authority write can begin and any that began has finished.
While frozen the master cannot mint a higher generation. The replica is built after the freeze, and
the target must acknowledge **exactly** that generation, verified, before it may activate. The
master persists `handed_off` **before** it issues the signed hand-off, and `handed_off` refuses every
authority write for good — not only while frozen — including start permits, placement admission and
autostart. The target activates only on that signed hand-off, only against a verified replica at
exactly that generation, and only at generation + 1. Before the hand-off only the old master writes;
after it, only the new one.

**The abort rule.** A cancel is accepted only before the transfer's **first irreversible durable
step**, and Core reports that answer itself as `PoolTransferStatus.cancellable` rather than leaving a
caller to infer it from stage names. The flag is computed from the same predicate `cancelPoolTransfer`
is gated on, so the reported flag and the accepted cancel cannot drift, and it is always present, so
a caller never has to tell "not cancellable" from "not reported".

| transfer | cancellable while | first irreversible step | refusal after it |
|---|---|---|---|
| master change (`role: source`) | `freeze`, `replicate`, `activate` — including the hand-off window, where it succeeds only if the target proves under its own lock that it never activated | the pool-CA-signed hand-off | `the target has already activated this pool authority; the master change can only go forward` |
| eject (`role: source`) | `freeze`, `remove` — the stage names the step still to perform, so at `remove` nothing has been written | the membership without that member, written at the next generation | `host <id> is already removed from this pool's membership; the eject can only go forward` |
| promotion (`role: candidate`) | never | the fence: the old pool master has been powered off | `this pool promotion has powered the old pool master off; it cannot be cancelled and can only go forward` |

Before the hand-off a cancel clears the freeze and the current master stays master (`stage:
aborted`). After an eject's membership write the cancel is refused and the eject **completes** — the
remaining members are re-pointed and the ejected host is told to leave; the membership is never
restored and the host is never put back in the pool. A cancelled master change records `the pool
master change was cancelled by an operator`, a cancelled eject `the pool member eject was cancelled
by an operator`. Nothing is ever aborted on a timeout. A refusal at the freeze marks the record
terminal, so a refused transfer never blocks the next one.
An eject that would leave fewer than three ready hosts refuses while any membership row says a
watchdog is still feeding. Core rechecks immediately before the durable membership write, so a
feed that begins after the initial freeze on upgraded Core hosts cannot be missed; the transfer
remains before removal.
The maintenance operation also refuses a drain from three ready hosts to a configured two-host
witness while any pool watchdog still feeds; its response names the prerequisite to disarm.
The master also refuses a new watchdog feeding intent after Host fencing is off, that host is
draining, or the pool no longer has a supported three-to-five-host watchdog cluster. A member must record that intent
before it starts SBD.

**A cancel that Core performed is a success, not a failure.** A `transfer-cancel` that did what it
was asked returns `status: "succeeded"` with Core's sentence in `last_transfer.reason` and `error`
empty; only a refusal is `failed` with `error`. Forward-only refusals keep `conflict: true,
retryable: false`. A caller reads `reason` first and `error` as the fallback, so a cancellation Core
carried out can never be rendered as a failed transfer.

**Resuming a hand-off whose target died.** A master change that has already signed the hand-off
cannot be cancelled by agreement, because the target is the only host that could agree and it is
gone — and until something proves it is off, this pool's authority stays with it. So the source's
last resort is the `cancel` route carrying `fence_confirmed` and `fence_host_ids: ["<target>"]`.
Core refuses it unless **all** of: the target is probed and does not answer; the operator named
exactly that host; this host is still the recorded master, still holds the pool CA key and is **not
restricted**; the target has a recorded fence profile; and Core's own fence produces a verified,
digest-bound power-off of it. It then resumes at authority generation + **2**, because a target that
activated before it died minted exactly + 1 — so the resumed source supersedes it everywhere by the
same strictly-higher rule every host applies, and the returning target (restricted by construction,
since it was just fenced with its own profile) takes the superseded path and writes nothing. A host
that was itself fenced and rebooted is restricted and is refused here, which is what stops a fenced
host resuming over the host that fenced it. The transfer record ends `aborted` carrying `the target
of this master change was powered off and could not take the pool's authority; this host resumed
it`, and the cancel operation itself is `succeeded` with that sentence in `reason`.

**What is frozen, and what is not.** While a transfer holds the freeze the pool master refuses every
pool authority write — placements, VM definition and start admission, joins, maintenance changes and
pool storage/network/media registrations — with `pool authority is frozen for a master change; retry
when it completes`; nothing is queued behind it. Running guests are unaffected, stop, shutdown and
force-stop are never refused, local recovery access is untouched, and pool-shared **storage
reservation releases are deliberately never frozen**, because freezing a release could strand a
member's stop.

**What an ejected host becomes.** It re-initializes as the pool master of a **new** single-host pool
with a new pool ID, named after its hostname, keeping its host ID, its enrollment, its local storage
and its local virtual machines. The Controller adopts that pool from the host's own report, under
the same site, the way enrollment records a new host's pool — but **only after the old pool's master
has itself reported the eject**. An eject is the master's record, not the leaving host's, so while
another host is still the recorded master of the pool being left, that master's own mirrored
membership must exist and must no longer hold this host as a member (an eject marks the row
`ejected`, which counts as gone). Until it says so the Controller keeps the host where the pool says
it is; the leaver retries on every heartbeat and is adopted on the first report after the master
publishes the eject. Without that rule any enrolled host could move itself out of its pool in the
Controller's view simply by reporting a new single-host membership. The ejected row in the old pool is
marked `ejected` rather than deleted — `AuthenticatePoolPeer` requires `ready`, so the certificate
stops being admitted either way, and keeping the row is what lets the pool tell a returning host
*why* it is refused.

**Rejoining and retained records.** A succeeded `vm-delete` tombstone remains scoped to the old
pool when an ejected host becomes a single-host pool, so a same-ID rejoin publishes the deletion
and permits the old master to prune only its stale owner record. A live local VM remains with the
ejected host instead. For pool networks, shared repositories and shared ISOs, a leave-created
local removal carries Core-private, signed cleanup provenance. On a later authenticated rejoin to
the same pool, Core may reapply the same resource ID only when the membership and resource
generations prove it is newer; the succeeding reapply consumes that receipt. Other removals stay
effective. This is managed-journal behavior, not a new wire field or Controller record.

**Reported state.** Every participant reports `inventory.managed.transfer` = `PoolTransferStatus`
with its own `role` (`source`, `target` or `ejected`), stage, status and error. Beside
`cancellable` it reports `handed_off`, always present: the source's own durable answer to "have I
signed this pool's authority away?", so a caller never infers the irreversible step from a stage
name or a refusal sentence. Only the outgoing master records it; a target or ejected host always
reports `false`, and an older Core that omits the field is distinguishable from one that reports
`false`. It is projected verbatim into the pool-level `transfer` and each member row's `transfer`.
The master
additionally reports `inventory.managed.authority` = `PoolAuthorityRecord{generation, digest,
observed_at}` — the only absolute pool authority generation on the wire, which the replica ladder
below now compares against. Every participant also reports a `pool-authority-transfer` health check:
fresh `healthy` when its readable record succeeded or no transfer is active, `failed` with Core's
sentence when the record is `failed` or `needs_attention`, `unknown` while pending. A read failure
produces no synthetic healthy sample. That check reaches an operator through the
host's own reported check list, not through the pool members **Convergence** column, which shows
only the fixed `poolConvergeChecks` set.

A promotion can report `stage: catalog-rebuild` after the new master identity and backup
authority root are durable. This stage applies when the signed authority contains
repositories but no catalog entries. Core imports the catalog from every repository
before re-pointing members. An import error reports `needs_attention` and keeps the
same transfer stage; Core retries from that durable stage without activating a second
master or rolling back the committed authority. Operators must restore repository
access and check the transfer status before relying on the promoted catalog.

**Members projection additions.** Pool level: `transfer_supported` (the master advertises the
capability); `authority`, the master's own record mirrored verbatim, `null` when the master is not
fresh or reports none; `transfer`, the master's own report, `null` on the same terms, so a stale
master's last transfer is never shown as present truth; `last_transfer`, the newest `pool-transfer`
operation this Controller holds for the pool, with `reason` carrying Core's sentence for a
cancellation it performed, `conflict` marking refusals a synchronous route
would have answered with `409` and `retryable` marking exactly one sentence, the frozen-authority
one, which an operator retries unchanged; and `master_conflict`, whose rule is the exact complement
of the adoption rule.

**The adoption rule, and its fail-closed default.** A member's report is a payload that member fully
controls, so a claim of mastery is adopted only against the **recorded master's own** mirrored
membership, and the check is unconditional: with no mirrored membership at all there is no evidence
and the claim is refused. Two arms count as evidence. The first is the master change: the claimant's
own membership names **it** master at a *strictly higher* generation than the mirrored one. The
second is the **reconnect**: the recorded master's own mirrored membership already names the
claimant as pool master, in which case the two agree and the row moves at the same generation. No
host but the recorded master can produce that pointer, so a member cannot use either arm to take a
pool. Neither arm is a conflict. A third arm only gives the row away: when the **recorded master's
own** report names another host master of the same pool, the row moves to that host, even one that
has not enrolled here; this corrects a replacement Controller where a member reconnected first.
Mirror rows move only to an enrolled master, and a recorded master that enrolls late takes them
back at enrollment. While the recorded master has never enrolled, the mirrored membership the arms
compare against combines the memberships naming it that the pool's enrolled hosts reported: the
highest generation any of them claims, and only the hosts all of them list with the same status. So
a claim must be newer than every such report, and one member's report cannot widen the pool. Then `GET /api/v1/pools/{id}/members` lists that membership with `authoritative:
false`, so a surviving member can be promoted from the Hosts view.
What is left, and all `master_conflict` lists, is two cases: an **equal-generation rival**, a host
whose own membership names itself master at exactly the mirrored generation; and a **newer membership
naming a third host**, a host reporting a strictly higher generation for a master that is neither
itself nor the recorded one, which adoption cannot follow from there. A lower generation is never
listed, so a fenced old master reporting itself master at its old generation is not a rival claim at
all — it is projected as an ordinary member carrying its own `restricted` reason. `master_conflict`
is normally empty; when it is not, the Controller keeps the recorded master row and shows the
conflict rather than flapping, and acts on nothing. Per member: `transfer`, that
host's own report, `null` whenever the host is not reporting now — a participant whose row is `null`
while `connection` is not `fresh` is *unknown*, not finished.

**Replica ladder amendment.** With the master now reporting an authority generation, the `behind`
rule of the [member authority replica](#member-authority-replica-peer-getpost-apiv1poolreplica) gains
an absolute reference: a member is `behind` when the two reports disagree on one generation, **or**
the master reports an authority generation either number is below, **or** another fresh member of
this pool is acknowledged at a strictly higher generation. The peer comparison stays, because the
master's authority record can itself be an older report than a member's, and when `authority` is
`null` the ladder behaves exactly as it did before.

**Mirror re-key.** When the hosts report a master change — a report from the host its own membership
names as master, on either arm of the adoption rule above — the Controller moves its
`pools.master_host_id` row and re-keys the pool's storage,
network and media *mirror* rows to the new master host ID, because only the recorded master refreshes
and tombstones them. It moves no content: every field is replaced by the new master's next report.
Two hosts claiming mastery at the same generation change nothing. A pool master that adopted an ISO
record from shared storage rather than importing the file refuses to remove it, with a message saying
the file stays on shared storage and must be removed from the host that imported it.

**Verification status.** Implemented and covered by offline contract checks
(`tests/test_api_contract.py`, `PoolTransferContractTests`) and Go `-race` tests that run several
hosts in one process over the real mutual-TLS peer transport with real certificates and real
signatures: a three-host master change with byte-identical VM identities and working placement, start
permits, maintenance and replica flow afterwards; frozen-authority refusals including stop still
being permitted; interruption at every stage on each side converging on exactly one master; a handed-
over master refusing authority after a restart; an unreachable member catching up through the old
master and then the new one; stale and unsigned re-point refusals; the full abort rule including the
durable never-activate record; eject with re-initialization and eject refusals; ejecting an
unreachable member and it learning on return; and concurrent authority writes during a freeze all
refused under `-race`. Controller lab tests cover the routes, the refusal matrix, the capability
gate, the projections, adoption of the new master and of an ejected host's new pool, and a Controller
reopened from the same database mid-transfer. Scoped native evidence includes three-host
activate-before-persist and target-persisted interruption recovery, plus a typed browser eject to
the host's new solo pool. Browser review of the transfer strip, conflict banner and several
recovery/confirmation states remains incomplete; VM-workload continuity and broader failure paths
remain open. See the [current native-acceptance record](qa/2026-09-19-phase4-native-acceptance.md).

### Fence-verified manual promotion and safe rejoin (POST /api/v1/pools/{id}/master/promote, POST /api/v1/pools/{id}/members/{host_id}/fence-profile)

API impact: **new contract**, implemented. Three Controller routes, two Core command kinds, one
brokered pool-request action, one extension of an existing peer route, six new protocol types and
two new reported inventory fields. No new capability string: `pool-authority-transfer-v1` gates all
of it. The Controller stores **nothing** pool-scoped here — no verdict, no fence proof, no
restriction, no stage, no candidate, no generation — so a Controller restarted or replaced
mid-promotion knows nothing about it until the hosts report, and then re-projects it from their
reports.

**What it is for, and what it is not.** Promoting a *surviving member* to pool master when the pool
master is **gone**. It is not a tidier master change: Core probes the old master over the pool
network and refuses the promotion outright when it answers and still holds this pool's authority,
naming the [planned master change](#planned-master-change-and-member-eject-post-apiv1poolsidmaster-delete-apiv1poolsidmembershost_id)
instead. This endpoint is a **manual** promotion: one operator, one candidate, one typed pool name and one explicit power-off acknowledgement. Core also has a separate automatic path limited to eligible two-host pools with Host fencing enabled and a recorded BMC fence profile. It does not use watchdog, storage lease or qdevice witness, and it does not send a Controller command.

**Owner decision OD-5, and the absence of an escape.** An opted-in pool whose master restarts while
one other ready member is permanently dead **stays restricted**. No route, field, parameter, enum
value or interface affordance offers "start VMs elsewhere", "force", "override", "declare host
lost" or any timeout-based path, and **none may be added**. The recovery is to bring the dead host
back or restore its Core state. The reasoning is in the plan; the short form is that a point-in-time
observation that a host is off proves it is not writing *now*, does not prove it never took the
authority, and does not survive that host powering on again — accepting it constructs two masters.

**Proving the fence target is the right machine.** A fence profile is the same closed typed profile
clustered storage already uses. Two things bind a BMC fence to one machine.

*The certificate.* A management controller ships a self-signed certificate with no usable name, so a
chain proves nothing and skipping verification would send the password to whoever answers. When a
`redfish` profile is recorded, Core connects, captures the exact certificate, stores its leaf SHA-256
in `StorageClusterFence.tls_fingerprint` on the pool master and reports it so an operator can compare
it with the controller's own page. Every later connection must present exactly that certificate; a
different one refuses the fence with `this management controller presented a different certificate
than the one recorded for it; record its access again…`, which is deliberately not the
`did not answer` sentence. That pin is also what binds the identity read and the power-off to one
endpoint, so a name that resolves elsewhere between them cannot separate them. `fence_redfish`
cannot be bound to it — its hostname check cannot be satisfied by a factory certificate, and
`--ssl-insecure` would send the password to an unauthenticated peer — so **Core performs the Redfish
fence itself** over the pinned connection: the identity `GET`, a `ComputerSystem.Reset` `ForceOff`
`POST`, then a separate `PowerState` read as the observed-off evidence. Redirects are never followed.
`ipmilan` has no TLS and nothing to pin: RMCP+ authenticates its own session, and the power-off and
observed-off still run through the shipped `fence_ipmilan`.

*The machine.* Over that connection Core asks which system the controller manages — the Redfish
`ComputerSystem.UUID`, compared **exactly**, or IPMI *Get System GUID*, where either documented byte
order counts because the two specifications disagree about it — and compares it with
`PoolMember.system_uuid`, the SMBIOS system UUID the target host itself reported while it was alive.
**A mismatch, a host with no unique hardware identity and an unreadable controller each refuse the
fence and power nothing off.** Recording a `redfish` profile performs all of this live, so an
unreachable or wrong controller is refused while the master is still there to record a corrected one.

`ExecutePoolFenceProfile` **refuses at record time** every profile this build could not turn into a
proof, in the shape `this fence profile cannot be verified by this host: <reason>`, and releases the
password the credential handoff delivered when it does, so a refusal leaves nothing behind. The
check is enforced at record time deliberately: a profile in a membership therefore always came from
a build that can verify it, and any build that can verify it also applies the boot rule to it, so
there is no version-skew window where one build promotes over a profile another build treats as
unusable. It is reported structurally as `fence_recorded: false` / `fence_usable: false` with a
`fence_unusable_reason`, not only in prose.

`StorageClusterFence` gains `username`, the management-controller account (power control only) the
profile signs in with, and `tls_fingerprint`, which a client never supplies. The account's password
goes to Core over the existing one-time credential handoff as a `fence_bmc` credential and never
reaches the Controller. `PoolMember` gains `system_uuid`. A host records it at join and re-reports it
on every reconcile pass over the existing `POST /api/v1/pool/advertise` peer route, which now carries
`system_uuid` beside `origin` and records each independently, so a pool formed before this existed
gains the identity of every host that is still alive without re-joining anything. A member running an
older Core reports none, keeps working exactly as before, and simply cannot be given a
management-controller fence device. Both fields are recorded observations: neither moves a membership
generation. A fresh Controller recovers all of it from the master's reported inventory and stores
none of it.

**A host may not defeat its own fencing.** The advertise route is authenticated by the member
certificate and writes only the calling member's own row, and within that row the master refuses two
changes: replacing a `system_uuid` it already reported while a BMC fence profile is recorded against
it — otherwise a host a promotion must exclude could advertise a random identity and make every proof
for itself fail for ever — and claiming a `system_uuid` another member already holds, because an
identity two hosts report is not an identity. Known firmware placeholders (all-zero, all-`f`, and the
widely shipped `03000200-0400-0500-0006-000700080009`) are rejected outright. Replacing a motherboard
is: clear the fence device, let the host report, record the new controller's access.

**The Redfish power-off waits for the machine to go off.** `ComputerSystem.Reset` is asynchronous:
a controller answers and goes on reporting `PowerState: On` for seconds. Core therefore polls the
same pinned `ComputerSystem` every 2 seconds for up to 30 (monotonic) and only then reports the
exclusion as unconfirmed. For the same reason a **non-2xx answer to the Reset is not a failure**:
several controllers refuse `ForceOff` for a machine that is already off, which is exactly the case a
pool fence exists for, and Core's own polled `PowerState` reads decide. Identity is still proved
first, and nothing is powered off or reported excluded without it.

**Automatic in-band provisioning.** Each host configures its own management controller, so no
operator normally types a BMC address or password. On every reconcile pass `EnsureLocalBMCFence`
reads this host's own controller over `/dev/ipmi0` (`ipmitool -I open`): every LAN channel's
address, the RMCP+ cipher suite (17 where offered, else 3; suite 0 is never selected and never
accepted into a profile), and the controller's user table — read by `ipmitool`'s own row format,
and refused outright rather than guessed at if a line does not fit, because reading a slot as free
when it is not would hand Nex somebody else's account. **The chosen slot is recorded durably before
the first command that changes anything**, so a controller that refuses halfway through leaves an
account this host still recognises as its own and finishes on the next pass, instead of a stranded
`nexfence` account it would refuse to touch for ever. The account gets a random 16-character
password written to `ipmitool`'s own prompt on standard input — never a command argument —
**Operator** privilege (the lowest IPMI privilege that may issue Chassis Control), IPMI-over-LAN on
the fencing channel and explicitly no access on every other LAN channel the controller reports.
Channels that are not LAN are not touched; constraining them in-band differs by firmware, so the
hardware walkthrough checks the account on every channel instead.

The host then offers the profile and the password to the pool master: the master records its own, a
member posts `POST /api/v1/pool/fence-provision` over the existing pool mTLS. The master writes the
password into its private fence-credential store and records the profile through the same
`ExecutePoolFenceProfile` path an operator-entered profile takes, **including the live identity
proof**, so nothing that was not proved enters the membership. Until it is recorded there is no
fence device: the host keeps the account **and its password** in its own private fence-credential
store and offers exactly the same profile again on later passes, so a master that is away for an
hour does not mean an hour of password writes to the controller's flash.

`StorageClusterFence` gains `cipher` and `provisioned_by_core`, neither of which a client may supply
(400). `provisioned_by_core` is the durable marker that makes the rest safe: Core replaces an account
under its own name only when this host's own record or the master's membership row says Core created
it — a reinstalled host therefore still recognises its own account, and an account somebody else made
under that name is a reported conflict with nothing changed. An operator-entered profile always wins:
`ExecutePoolFenceProfile` refuses to replace one with a Core-provisioned profile (`this host already
has a fence device an operator set…`).

**One operator action, one meaning.** Clearing a Core-provisioned fence device — the existing
`POST /api/v1/pools/{id}/members/{host_id}/fence-profile` with a null fence — is what withdraws it:
the master clears the profile through the one path that clears a profile (so
`recordPoolSelfFenceExposure` still runs and a master is not left restricted after a restart) and
sets `PoolMember.bmc_provisioning_disabled`. The host reads that from the membership it already
holds, removes the account from its own controller and reports `removed`; a controller that refuses
is reported honestly and retried, and the operator's decision stands either way.
`POST /api/v1/pools/{id}/members/{host_id}/bmc-provisioning` is the other direction only — let this
host configure its controller again — and is relayed to the master with the same membership and
capability check a fence profile takes.

`PoolMember` gains `bmc` (`PoolHostBMC`: `state`, Core's `detail` sentence, `address`, `channel`,
`account`, `observed_at`), `bmc_provisioning_disabled` and `system_uuid_conflict_host_id`, the member
that already holds an identity a host's report was refused for, so the refusal names something an
operator can act on. **A host's report is an observation and nothing else**: it travels on the
existing advertise route, it is recorded in the calling member's own row, and it can neither delete
the fence device the pool recorded nor change the operator's decision — the same rule slice 1
established for `system_uuid`. A report identical to the one already recorded but for its timestamp
is **not written at all**, because the membership is signed and replicated and a settled pool must
not re-sign and re-push its whole authority replica every ten seconds because of a clock.

### Continuous non-destructive checks, and fencing readiness

A fence device recorded once and never tried again is a promise, not a protection. Every ready host
therefore reads **every other** host's fence device periodically — the management controller's own
report of which machine it manages, and that machine's power state, over the recorded profile and
the replicated credential — and tells the master what it found.

**Nothing on that path can power anything off.** The whole check is `vmhost.CheckPoolFenceTarget`:
the identity read the fence already performs, then a power read whose action is the literal
`status`. The caller cannot pass an action in, and a test asserts that a whole checking pass issues
no state-changing Redfish request and runs no fence agent with anything but `--action=status`.

**Cadence.** One pass per host every five minutes with up to a fifth of that in jitter, targets
taken one at a time (20 s each, 2 minutes for the whole pass) so a pool never opens many sessions on
one controller at once — management controllers have small session tables. The pass rides the
existing reconcile tick but runs on its own worker, so a pass spending its whole budget against
controllers that have gone away never delays the storage-cluster reconcile beside it; it
self-throttles to its own interval and stops with the agent. IPMI needs two short sessions per
target (the `Get System GUID` read and `chassis power status`, both with the password in
`IPMI_PASSWORD` and never in `argv`); Redfish needs one pinned connection for both.

**Where the password comes from.** The pool master reads it from its own fence-credential store. A
member holds no other host's material on disk — only the sealed copy inside its own signed authority
replica — so it unseals that copy **in memory** for the pass and discards it. Materialising other
hosts' fence passwords on every member so a read-only check could run would spread the secret across
the pool for no gain, so `CheckPoolFenceTarget` takes the secret as a parameter and never reads the
store itself.

**`POST /api/v1/pool/fence-checks`** (pool mTLS, member → master) carries one pass:
`PoolFenceCheckRequest{checks: [PoolFenceCheck{host_id, result, detail, power}]}`, at most 64, body
at most 64 KiB. The member certificate says who is calling; the report is credited to that host and
**an entry naming the caller itself is dropped**, because a host does not get to say whether it can
be shut out. `result` is closed: `ok`, `unreachable`, `credentials_rejected`, `identity_mismatch`,
`certificate_changed`, `tool_missing`, `no_profile`, `unusable`. A wrong machine and a changed
certificate are deliberately their own results rather than network faults.

**These are observations, and the master keeps them in memory.** They are overlaid on the membership
the master reports as `PoolMember.fence_readiness` — exactly as `replica_generation` already is — and
**never written into the durable membership record**, so a pool's signed authority does not move
because a check ran (a test runs twenty passes and asserts `pool-peer.json` is byte-identical
afterwards). A master that restarts has forgotten them and says so: every method reads `unknown`,
*not checked yet*, until the next pass re-establishes it within one interval. There is no new durable
store, and a fresh Controller recovers readiness from the master's reported inventory like everything
else.

**Freshness is the master's own clock.** It stamps `observed_at` when a result arrives, so nothing
depends on two hosts agreeing about the time, and marks a result `stale` once it has not been
confirmed for three intervals. **A stale check is shown as stale and is never readiness.**

`PoolFenceReadiness{ready, methods}` is the master's per-host answer, one
`PoolFenceMethod{kind, state, reason, checks}` per fencing method. `state` is `ready`, `not_ready`,
`unknown`, `no_peer` or `not_applicable`. A host is ready by BMC when its profile is recorded, its identity is
recorded and **at least one other ready host** has a fresh `ok` check against it; otherwise the
reason names the host that found the problem (*"this management controller did not answer (from
host-b)"*). `no_peer` is its own state because it is not a wait: a pool of one ready host has nobody
to be checked from, ever, and nothing to promote over either. Slice 2's provisioning states fold in
as reasons, including an operator's deliberate withdrawal, which is reported without an upgrade nag.
The watchdog method is the second entry in that list; see **Watchdog capability, and the Host
fencing setting** below.

**Readiness is an observation, and it gates nothing.** No decision path reads it. A promotion is
decided by `PoolFenceUsable` and by the live fence proof Core produces and verifies itself, exactly
as before this slice: a host whose checks are all stale is still promotable over, and a host
reported ready can still fail its fence when the moment comes. Readiness exists so an operator finds
out before that moment instead of during it. A compromised member can report a false result and move
another host's readiness either way, which is survivable precisely because nothing decides from it —
and it is why the next method must not change that.

Every result is bound to the profile it was taken against (`PoolFenceCheck.profile`, a digest of the
recorded kind, address, target, account, pinned certificate and cipher plus the hardware identity).
The stamp covers the credential too (`StorageClusterFence.credential_ref` and
`PoolMember.fence_credential_version`), because rotating only the password leaves every other field
identical and a check taken with the previous password would otherwise go on reading as ready. The
master drops results whose stamp is not the one its membership records now, so **replacing** a fence
device, or rotating its credential, immediately reads as *not checked yet* instead of inheriting the
old device's success.

**Version compatibility of the stamp.** `PoolFenceCheck.profile` is optional on the wire. A member
running a Nex Core from before the stamp existed sends a pass with none, and the master accepts the
pass and drops the unstamped results where stamps are compared — they read as *not checked yet*.
Refusing the whole report with 400 would lose every other host's result in it for the sake of one
field the master would drop anyway. An older *master* serves no such route at all, answers 404, and
reports no readiness, which the Controller shows as **not reported**.

`PoolMembership.fence_summary` carries `PoolFenceSummary{ready, total, unready, host_fencing,
prerequisites}` once for the whole pool, not per row — the count is Core's, over this pool's *ready*
members, so an ejected tombstone or a host that has not finished joining is neither counted nor
named. A client displays that number; it does not add up the rows it is about to render.

Also settled here, because a staleness gate depends on it: **`PoolHostBMC.observed_at` is when that
finding last *changed*, not when it was last confirmed.** An unchanged finding is deliberately not
re-reported (see above), so the field says nothing about whether the host is still reporting. Host
liveness is that host's own connection; fence-device freshness is `PoolFenceCheck.observed_at`.

The Controller relays and stores nothing: `poolMemberFence` gains `readiness` and
`provisioning_disabled` — the master's own recorded withdrawal decision, which the host row's actions
are now driven from rather than the host's echo of it, so a host that is down after its device was
cleared still shows the truth and still offers both ways out. A master too old to report readiness
leaves the field absent, which the UI shows as **not reported** and never as ready.

`PoolPromotionHost` gains `fence_kind`, the method a promotion would shut that host out with, and
`fence_check_warning`, **advisory only**: what the candidate's own last check of that device found
when it was failing or stale. The proof path is unchanged and still decides.

**What is not proven.** No physical management controller has been contacted and **no host has been
powered off**. Every Redfish behaviour above is exercised only against an `httptest` server, and
every `ipmitool` interaction — in-band provisioning and the LAN identity read alike — only against a
fake command runner, whose user-table fixture is `ipmitool` 1.8.19's own header string and row format
rather than a real controller's output. The in-band LAN side in particular is unexercised: a QEMU BMC
simulator has no network side at all, so the provisioned account has never authenticated over RMCP+.

**Where the fence credential lives, and who can open it.** The host that fences is the **candidate**,
and it cannot be handed the material after the master it must fence is gone. So the master seals a
copy of each profile's material **per member**, inside that member's replica, with the same
primitive as the pool CA key under a distinct context binding pool, member, generation, fenced host
and reference (`PoolWrappedFenceCredential`). The sealed blob is inside `SigningBytes`, so tampering
is detected, and excluded from `ContentDigest`, so one generation still has one digest for every
member; what makes rotated material advance the generation is `PoolMember.fence_credential_version`
in the replicated membership. Nex Controller never sees the material: it is delivered once over the
existing encrypted credential handoff, Core allocates its own private reference, and only that
opaque reference is written into the command — nothing reaches SQLite, the audit trail, a response
or a log, and `credential_ref` is never projected to a client. **A profile cannot be recorded until
its material has been delivered to the master** (`this host holds no fence credential for <host>;
deliver it to this host before recording the profile`), and the master keeps only a private digest
of it in its own `pool-peer.json`.

The owner-visible consequence, which belongs in the guide and not only here: **any ready member can
open the material addressed to it, so any ready member can power off any profiled host.** It is the
same trust model the sealed pool CA key already carries, it is inherent to fence-verified promotion,
and it is policy rather than a lock. Admit only hosts you trust with it.

**Controller routes.** Administrator session, same HTTPS `Origin` and `X-CSRF-Token`, `{"error": …}`
bodies. Every literal refusal with its status code is in the [generated reference](api/index.html).

- `POST /api/v1/pools/{id}/master/promote/preflight` — `{host_id}`. One brokered report round trip
  to the **candidate** (`PoolRequest{action: "promotion-preflight"}`), answered on that host's next
  report, deadline 45 s. **Nothing is stored, journalled or audited and no command is issued**, so
  it is safe to repeat. The pool master is deliberately not required to be fresh. `200 {host_id,
  promotion}` with the verdict mirrored verbatim; `504` carries a `PoolResult`, as the join relay
  does; `499` keeps an abandoned relay out of the success range.
- `POST /api/v1/pools/{id}/master/promote` — `{request_id, host_id, confirmation, fence_confirmed,
  fence_host_ids}`. `confirmation` is the **pool's name**, compared case-insensitively after
  trimming. `fence_confirmed` must be literally `true` and is the operator's separate
  acknowledgement that the named hosts will be powered off — a precondition and nothing else; Core
  produces and verifies every proof itself and an acknowledgement is never evidence.
  `fence_host_ids` is **required and non-empty**: exactly the set a *fresh* preflight put in
  `verdict.fence_host_ids`, each host once. The Controller computes, completes and corrects nothing
  — it sorts the set, so the same confirmation typed in another order is a replay rather than a
  second promotion, and forwards it verbatim. A missing, empty, blank or duplicated set is
  `400 this promotion powers hosts off; confirm in fence_host_ids exactly the hosts the promotion
  preflight named in verdict.fence_host_ids, each listed once`; a host this Controller mirrors in
  *another* pool is `400 host <id> is not a host of this pool; confirm only the hosts this pool
  reports`, while a host it has never heard of is forwarded — the mirror is not the pool's record,
  a partitioned host may be known only to the pool, and Core recomputes the whole set at run time.
  There is no `expected_version`: the
  generation to check against is the dead master's, and the candidate re-proves the whole verdict
  immediately before it activates. `202`, `200` on an exact replay, `409` on a changed payload.
- `POST /api/v1/pools/{id}/members/{host_id}/fence-profile` — `{request_id, fence, credential?}`.
  Relayed to the **master**, which owns the membership record the profile lives in, so this is the
  one route here that does require a fresh master. `fence: null` clears the profile. The stored
  replay payload is the client's profile **as sent**, without the credential and without the
  Core-allocated reference, so an exact retry stays byte-identical. `503 Core did not acknowledge
  fence credential delivery; retry with the same request_id` when the handoff is not acknowledged.
- `POST /api/v1/pools/{id}/members/{host_id}/bmc-probe` — `{request_id, endpoint, username?,
  credential?: {secret}}`, with `username` and `credential` both present or both absent. The Detect
  step of Set BMC access: one brokered pool request (`action: "bmc-probe"`, `PoolRequest{member_host_id,
  endpoint, username?, credential_ref?}`) to the **master**, which probes HTTPS Redfish and IPMI
  RMCP+ itself, from the host that would fence. Same gates as `fence-profile` (fresh master with
  `pool-authority-transfer-v1`, host in its reported membership). The password goes over the same
  one-time credential handoff with a probe-only `bmc_probe` purpose, bound to the same stable
  `request_id` and target member. Core verifies that scope and removes both private
  credential copies after the probe, including a failed one; a delivered probe that never runs
  expires after 10 minutes on Core's local probe tick or its next startup. A recorded fence
  credential cannot be used for Detect. `200 {host_id, status,
  error, bmc_probe}` whenever the master answers: `status: "failed"` carries Core's sentence in
  `error`, and may still carry `bmc_probe.methods` to say what did answer; `bmc_probe.recommended
  {kind, target_id, reason}` is what to record. The relay waits up to **3 minutes** (Core gives a
  probe 2, then answers on its next report) and extends the response write deadline to match;
  `504` with a failed `PoolResult` after that. No discovery result or pool setting is journalled,
  audited or stored; Core holds only the expiring private handoff until it can retire it. Detect
  is safe to repeat with a new `request_id`; recording the result is `fence-profile`, which proves the
  controller again. A result for another action that carries `bmc_probe`, or a probe answer that
  fails `PoolBMCProbeResult.Valid`, is discarded as if it had not arrived.

Audit: `pool.promote` and `pool.fence-profile` against the pool ID, beside the automatic
`pool-promote` / `pool-fence-profile` rows. **The preflight and the BMC probe audit nothing**,
because they change nothing. No credential material and no `credential_ref` is ever audited.

**Deliberately not pre-refused.** Everything about the old master — whether it answers, whether the
Controller can reach it, when it was last seen — is not evidence in either direction and refuses
nothing here. Neither does a missing or unusable fence profile, a tampered or lagging replica, a VM
ownership conflict, or a member holding a higher authority: all of those are in the preflight
verdict and are Core's decision at run time, arriving asynchronously on the operation and surfaced
verbatim in `last_promotion`.

**The commands.** `x-commands: pool-promote`, addressed to the **candidate** —
`PoolCommand{request_id, action: "promote", member_host_id, fence_confirmed, fence_host_ids}` — and
`x-commands: pool-fence-profile`, addressed to the **master** — `PoolCommand{request_id, action:
"fence-profile", membership.master_host_id, member_host_id, fence}`. No membership travels with a
promotion: the candidate reads the signed replica it already holds. Terminal results are
`succeeded`; `failed`, a decision meaning nothing moved; and `needs_attention`, which is **not** an
error but "durable and still running on Core". A cancel is never accepted for a promotion.

**What Core proves before it moves anything.** Four named checks, each `passed | blocked | unknown`,
where **`unknown` blocks exactly as `blocked` does** and is a separate word only so a refusal can be
told from an outage:

1. **`replica-integrity`** — the stored replica verifies against the pool CA this host pinned at
   join, declares every section, names this pool and this host, and carries a pool CA key this host
   can open. A fence never substitutes for it.
2. **`replica-supremacy`** — every host of the replica's membership is probed. Any reachable host
   holding a higher authority or replica generation blocks and is named as the better candidate. A
   reachable host that reports a non-terminal promotion of its own blocks too (`pool host <ids> is
   already performing a promotion of this pool; let it finish or cancel it before promoting another
   host`), so two concurrent reachable candidates leave exactly one master. An **unreachable ready
   member that is not the old master also blocks**: it holds this pool's authority replica, so it
   could be promoted in exactly the same way, and two partitioned survivors would otherwise both
   promote. The check reports `unknown` naming it (`pool host <ids> is unreachable and holds this
   pool's authority replica, so it could be promoted too; it must be fenced before this host can be
   promoted`), and passes only when this promotion has itself excluded that host by a verified,
   promotion-bound fence proof — which is what puts the host into `fence_host_ids` and forces check
   4 over it. A blocking host that cannot be fenced is a refusal an operator must resolve another
   way (`this host cannot fence <host>: <reason>, so it cannot be promoted while those hosts are
   unreachable`). **This is stricter than the original closure-plan wording.** *Decision: project
   owner, 2026-09-20 (OD-6).* The strict rule is approved: an unreachable ready member blocks
   promotion unless the same operation fences it; fail-closed — silence is never agreement.
3. **`accepted-ownership`** — every VM record of the replica is classified from evidence: confirmed
   by its reachable owner's own journal, fenced with its owner, or blocked. A reachable owner that
   cannot read its own inventory blocks it — ambiguity blocks — which is why the probe answers
   `vms_reported` separately from an empty `vms`.
4. **`old-master-exclusion`** — a fence proof **Core itself produced** for **every** host in
   `fence_host_ids`, not only the old master: an independently observed power-off of each recorded
   profile's target, `ExclusionVerified`, digest-bound over pool, excluded host, operation and
   generation (`PoolPromotionFenceDigest`), and at most `PoolPromotionFenceWindow` = 15 minutes old.
   Each proof is persisted before the host is acted on and a still-fresh one is reused on a retry.
   A returned fence agent status, a timeout, a missed heartbeat or an operator assurance cannot
   construct one. Before a promotion runs this check is normally `unknown`: Core fences and proves
   it during the promotion, never during a preflight. **Only `unknown` is tolerated** at the
   preflight stage of a running promotion — "nothing fenced yet". A `blocked` exclusion is a refusal,
   so a promotion aimed at a reachable, healthy master can never reach the fence stage. VMs owned by
   an excluded host are classified `fenced`, and the re-point skips every fenced host.

Core reports the readiness structurally so no caller infers it from check names: `can_promote` is
true only when the whole proof holds right now, fence included — so it is **false** in the normal
ready state — while **`ready_to_fence`** is the flag a client offers the promotion on. Beside them
`fence_recorded`, `fence_usable` and `fence_unusable_reason` (bounded at 200 characters, single
line) answer whether a profile exists and whether this host could turn it into a proof.

**Why there is never a second master.** The candidate holds no CA key and can write no authority
before activation. The promotion is refused if the old master answers and still holds authority, and
if any reachable host reports a higher authority or replica generation. The whole verdict is
re-proved immediately *before* activation, so a member that returned between the fence and the
activation stops it. Activation adopts VM ownership first, then makes this host master at
generation + 1 in one atomic `pool-peer.json` write; an interrupted promotion resumes at the stage
that did not complete and is idempotent by operation ID. Every other host adopts the new master only
from a pool-CA-signed notice at a strictly higher membership generation.

**The boot rule, and `Restricted`.** A rebooted master cannot know it was fenced: the evidence lives
only on other hosts. So, **before any start path is exposed**, a host writes
`Restricted{kind: "unconfirmed"}` if and only if all of: it is the master of its own durable
membership; that membership has at least one other member with status `ready`; and it is still
fence-promotable by somebody — a usable fence profile for itself is recorded now, **or**
`SelfFence` is still set, the durable record that a usable profile was recorded and some ready
member may still hold a replica carrying it. `SelfFence.ClearedAt` is dropped only once every ready
member has acknowledged a verified replica at or above that generation; acknowledgements are
memory-only, so a restart simply re-earns them. The restriction clears only when **every** other
ready member answers the authority probe naming this host master at no higher generation; one
unreachable member leaves it restricted — that member is exactly the host that could have been
promoted. A single-host pool has no such host and is never restricted, and a pool that never
recorded a usable profile — today, every physical pool — restarts its master unrestricted.

On discovering a higher authority a host demotes itself to `Restricted{kind: "superseded"}`; a
deliberate master change is exempt, because that host knows what happened. That restriction clears
after re-pointing, holding the new master's verified replica, having no VM the authority records
elsewhere, and having an inventory the master accepted. A conflicting VM keeps it restricted and is
named in `conflicts`; the local definition is fenced off, **never deleted and never started**.

While restricted, Core refuses every authority write, mints no generation, refuses every non-stop
workload start, starts nothing on autostart, and the libvirt hook refuses managed shared-VM starts
by reading `pool-peer.json` directly — so the refusal holds with no Controller connected, and fails
closed on an unreadable record. Stop, power-off, console, local recovery access and reporting are
untouched, and the hook stays shared-only so local recovery access is never blocked.

**Peer route extension.** `GET /api/v1/pool/authority` (unchanged auth: any holder of a certificate
this pool's CA issued) always answers `restricted`, and with `?vms=1` additionally answers
`vms: [PoolVMOwnership]` and `vms_reported`, which separates "this host defines no VM" from "this
host could not read its own record". No new peer endpoint.

**Reported state.** `inventory.managed.promotion` = `PoolPromotionStatus{operation_id, stage,
status, error, verdict?, cancellable, automatic?, observed_at}`, reported by the candidate; and
`inventory.managed.restricted` = `PoolRestrictedStatus{kind, reason, since, conflicts, observed_at}`,
reported by whichever host holds it. Health checks: `pool-restricted` — `failed` with Core's own
sentence while restricted; when Core can read a clear durable restriction, an explicit fresh
`healthy` sample replaces any prior failure. An unreadable peer record produces no synthetic clear;
an earlier Controller-stored check can remain until a later fresh report replaces it.
`pool-promotion` — a completed
promotion or readable absence of a promotion record reports fresh `healthy`, `failed` when failed
or `needs_attention`, `unknown` while running, and is absent only when Core cannot read its state;
`pool-authority-transfer` follows the same rule for a completed, failed, attention-needed or
in-progress transfer. Every one of the seven fixed checks reports a fresh `healthy` sample when
its readable Core state is successful, cleared or no longer applicable, so a Controller replaces any
prior failed or unknown projection. Core emits no synthetic healthy sample after a read failure;
the existing Controller merge can retain an earlier stored check until a fresh report replaces it.

**Durable schema.** `pool-peer.json` gains `Restricted{Kind, Reason, Since, Conflicts}` and a
private `BootAuthority{PoolID, HostID, BootID}` confirmation. The latter binds a fence-exposed
master's authority to its current boot so the local `StorageGuard` can refuse managed-VM starts
before the agent has otherwise resumed authority work; it is not Controller input or reported
inventory. It also gains
`Transfer.Fences` (one proof per excluded host, each bound to pool, host, operation and generation
and reused on retry while fresh), `Transfer.FenceAgreed` (the set the operator confirmed, re-checked
against Core's freshly computed set at both the preflight and the fence stage), `SelfFence{ClearedAt}` and a private `FenceCredentials{Ref, Digest, Version}` map that is
never replicated, reported or logged. `PoolMember.Fence` and `PoolMember.FenceCredentialVersion`
live in the master's durable membership and are replicated with it, so a promoted master inherits
every profile and a fresh Controller re-adopts them from `Inventory.Managed.Membership`. No new
file. **The verdict is deliberately not durable**: it is an observation with a lifetime and is
recomputed.

**Members projection additions.** Pool level: `master_seen_at` (RFC3339, `""` when the master has
never reported), projected from the `hosts` row this Controller already keeps and deliberately
**not** gated on freshness, because a stale-master banner needs exactly "has not reported since
`<master_seen_at>`"; `promotion`, the candidate's own reported promotion with the reporting host
added, `null` when no fresh host of this pool reports one — the Controller records no candidate and
shows the fresh host with the most recent report; and `last_promotion`, the newest `pool-promote`
operation, in the same shape as `last_transfer`, with `retryable` always false because nothing about
a promotion is retried unchanged. Per member: `transfer_supported`, that host's own advertised
capability; `fence` = `{kind, endpoint, configured, credential_version}` from the **master's**
membership, so `null` while the master is not fresh and never carrying `credential_ref` —
`credential_version` is `PoolMember.fence_credential_version` mirrored verbatim, `0` when no
material is recorded, and the number is the only signal that a rotation has actually been
replicated; the material itself never reaches the Controller; and `restricted`, that **host's
own** report. The `checks` array gains `pool-restricted`, `pool-promotion` and
`pool-authority-transfer`, appended to the fixed list in fixed order — `pool-storage-reconciliation`,
`pool-network-reconciliation`, `pool-media-reconciliation`, `pool-authority-replica`,
`pool-restricted`, `pool-promotion`, `pool-authority-transfer` — and a readable successful,
cleared or no-longer-applicable producer state is explicitly `healthy`. Core emits no healthy sample
after an unreadable-record omission; the existing Controller merge retains any prior stored check
until a fresh report replaces it.

**Freshness rule, corrected.** S4 and S5 gated every per-member field on `authoritative`, which is
right for a master-derived field and wrong for a member's own report — and wrong in exactly the
situation this slice exists for, because choosing a candidate means reading each surviving member's
own replica and restriction while the master is gone.

| Field | Source | Shown while the master is stale? |
|---|---|---|
| `status`, `error`, `maintenance`, `maintenance_since`, `access`, `failures`, `fence` | the master's membership or observations | **no** — unknown, `null` or empty |
| `peer_origin`, `joined_at` | the master's durable membership record | yes (unchanged) |
| `connection`, `guests`, `join`, `transfer` | the host's own report | yes (unchanged) |
| `restricted`, `checks`, `transfer_supported` | the host's own report | **yes (new)** |
| `replica.generation`, `digest`, `verified`, `received_at` and its own `error` | the member's own report | **yes (new)** |
| `replica.acknowledged_generation`, `acknowledged_at` | the master's observation | no — `0`/empty |

`replica.state` without a fresh master is limited to what the member alone proves — `unsupported`,
`refused`, `unverified`, `awaiting`, otherwise `unknown`. **`current` and `behind` are never claimed
without the master**: they compare against a number only the master reports. With a fresh master the
S4/S5 ladder is unchanged. `authoritative: false` already tells a client the master-derived half is
unknown, and every master-derived field is `null` or zero rather than a stale value.

**Adoption of a promoted master** needs no new code: it is the rule S5 already applies. The
Controller follows the host its own reported membership names as master at a *strictly higher*
membership generation and re-keys the pool's mirror rows to it; a promotion advances the generation,
so it is adopted exactly like a planned master change. The fenced old master's later reports name
itself master at its *old*, strictly lower generation, so they change nothing and are not listed in
`master_conflict` at all — it is projected as an ordinary member carrying its own `restricted`
reason, which is how an operator sees that it came back and is refusing to start anything.

**Verification status.** Implemented and covered by offline contract checks
(`tests/test_api_contract.py`, `PoolPromotionContractTests`) and Go `-race` tests that run several
hosts in one process over the real mutual-TLS peer transport with real certificates and real
signatures — **with a test double injected where Core calls the fence**. Covered: the whole preflight
verdict matrix (no replica, tampered replica, reachable healthy old master, a member at a higher
generation, a disagreeing reachable owner, everything-but-the-fence passing and reported `unknown`
rather than `passed`, no fence profile); six refusals of things that are not proof — an
acknowledged-only fence, a stale one, and proofs bound to another operation, host, generation or
digest; the destructive acknowledgement being mandatory; full promotions on a two-host and a
three-host pool with byte-identical VM records and unchanged certificates, peer origins and pool ID;
interruption at `preflight`, `fence`, `activate` and `repoint` resuming to exactly one master; the
boot rule with and without an unreachable member and a single-host pool never restricted; a
returning old master restricted before peer contact, refusing starts, defines, updates and authority
numbering while allowing stops, converging on signed evidence and clearing only after reconciliation;
an ownership conflict keeping it restricted with the VM named and the local definition intact; every
start path refused including autostart leaving no permit; the sealed credential round trip
master → replica → candidate asserting the exact identity bytes, refused for the wrong member, the
wrong generation and tampered ciphertext, retired on clear and replace but never while another
profile or a clustered-storage node still names it, and proven absent from logs, results, verdicts
and reported status; and the restricted-master-with-a-dead-member deadlock pinned as a test, because
it is a stated limit rather than an accident. Controller tests cover the three routes, the refusal
matrix including accepting a promotion while the old master is perfectly fresh *to the Controller*,
the credential never reaching the database, the audit trail or the response, the projection with a
stale master, adoption of a promoted master and of a returning restricted one, and a Controller
reopened from the same database mid-promotion.

**A promotion fences every host that blocks it, not only the old master.** The verdict's
`fence_host_ids` is the sorted set a promotion would power off: the old pool master, plus every
other host that `blocks` the promotion and that the candidate could exclude by a fence — an
unreachable member that could itself have been promoted is exactly such a host. Each probed host
carries its own `blocks`, `fence_recorded`, `fence_usable` and `fence_unusable_reason`, so a caller
can show precisely which hosts would be powered off and which of them cannot be. The operator
confirms that set, the command carries it back verbatim, and Core compares it against the set it
computes again at run time: a pool that changed since the preflight stops the promotion
(`this promotion must power off <ids>, which is not what was confirmed; confirm the hosts this pool
reports and try again`, or `this promotion no longer needs to power off any host; review the pool
and try again`) rather than quietly fencing something new. A host is never powered off without
having been named to the operator first. `ready_to_fence` is true only when the candidate holds a
usable profile and an openable credential for **every** host in that set.

Core refuses a `pool-promote` command that carries no confirmed set at all (`this promotion will
power off other hosts; confirm the hosts this pool reports before it can run`), and re-checks the
set both at the preflight stage and again inside the fence stage, so a pool that changed after the
operator confirmed stops the promotion before anything is powered off.

**Scoped native evidence, not phase acceptance.** Reviewed runs include a three-host promotion
with fence exclusion and externally observed power-off, returned-master restriction/rejoin, and
the f714 boot-window result: before authority publication, API and installed-hook starts and
autostart were refused; after ordinary rejoin, a normal API start and stop succeeded. This does
not prove contradictory-owner recovery, all start paths, duplicate-writer safety, physical fencing
or permanently-dead-member recovery. The G2 maintenance walkthrough proves enter/exit and normal
start/stop, but its reboot/autostart case remains open; it is distinct from the S6-14 restriction
boot result. See the [current native-acceptance record](qa/2026-09-19-phase4-native-acceptance.md).

### Watchdog capability, and the Host fencing setting

A management controller is how one host shuts out *another*. A watchdog is how a host shuts out
**itself**: the machine's own hardware resets it when Nex Core stops feeding a timer, which is the
only exclusion that still works when a host is hung rather than merely unreachable. This slice
detects and reports the device, arms it through the native `sbd` daemon where the pool's
prerequisites hold, records the armed report durably, and adds the `watchdog` proof kind that a
promotion may use to exclude a host that reset itself.

**Detection never opens the device.** On most drivers opening `/dev/watchdogN` *is* the arming — the
timer starts and the machine resets unless something keeps writing to it — so a host that has not
been told to arm a watchdog must not be reset by having been looked at. `vmhost.LocalWatchdog` reads
`/sys/class/watchdog/watchdogN` only: `identity`, `timeout`, `nowayout`, `state`, and the driver name
from the `device/driver` link. It names the device by its sysfs name, never by a path under `/dev`;
one test builds a fake sysfs tree and another asserts no string literal in the detection source names
a device node.

**Classification is by driver, in one list.** `iTCO_wdt`, `sp5100_tco`, `ipmi_watchdog`, `hpwdt`,
`wdat_wdt`, `i6300esb`, `ib700wdt`, `mei_wdt` and `sbsa_gwdt` are **hardware**; `softdog` is
**software**; a driver Nex Core does not recognise is **unknown** and counts as nothing until it is
classified. Three of those drivers — `i6300esb`, `ib700wdt` and the ACPI `wdat_wdt` — are real chips
and also what a hypervisor shows a guest, so a host whose DMI system vendor is a hypervisor reports
**`emulated`** instead. An emulated device really does reset that virtual machine, so Nex Core arms
it and a nested laboratory exercises the whole path; it is its own kind because a reset a hypervisor
performs is not evidence about hardware, and every proof, row and guide sentence resting on one says
so. A physical host never reports it. A
driver that registers with no parent device — the IPMI watchdog is one — is classified on the
identity string the kernel gives it instead. **Only hardware ever counts**: a hung kernel does not
run the kernel's own software timer, so `softdog` is reported and reported as not counting.
Decision: project owner, 2026-09-22. A host with several devices reports the one Nex Core would use
— the first hardware one — and how many it found.

**The host reports it; the master records it.** `PoolMember.watchdog` (`PoolHostWatchdog`: `kind`,
`device`, `driver`, `timeout_seconds`, `nowayout`, `in_use`) travels on the
existing `POST /api/v1/pool/advertise` peer route beside `system_uuid` and `bmc`, is recorded in the
calling member's own row, and is refused with 400 when the kind is outside the closed set or a string
is over length. It is a **capability**, not a running observation: it is written only when the
finding changes — so a settled pool does not re-sign and re-push its authority because a pass ran —
and it carries **no timestamp**, because nothing decides anything from how long ago a host last said
it. Whether a watchdog is *armed* is a different, freshness-bearing report and belongs with the
arming: `PoolMember.watchdog_armed`, below.

**The Host fencing setting is a decision, so it is durable.** `PoolMembership.host_fencing_disabled`
lives in the master's replicated membership with every other pool decision. It is recorded as the
negative so that an absent field is **on**: fencing is on for a pool that has never been told
otherwise, and a record written before the setting existed keeps the default with nothing to migrate.
Decision: project owner, 2026-09-22.
`POST /api/v1/pools/{id}/host-fencing` (`{request_id, enabled}`, administrator session, same origin,
CSRF) relays it to the master as one durable `pool-host-fencing` command whose `PoolCommand` action
is `host-fencing` and which names no member, because the setting is the pool's. `ExecutePoolHostFencing`
records it only on the master, only through `poolAuthorityWritable` — so a frozen or handed-over
authority waits out the transfer rather than writing into a generation somebody else is about to hold
— and treats it as a desired state: re-sending the state already recorded answers `succeeded`.
It needs `pool-host-fencing-v1` on the master; without it the Controller offers no switch and refuses
the request with 409 naming the upgrade. The Controller stores nothing: a fresh Controller recovers
the setting, like the rest of the membership, from the master's reported inventory.

**Turning it on arms nothing by itself.** The master reports its own prerequisite checklist with its
membership, so an operator meets it whether or not they ever open the setting:
`PoolFenceSummary.prerequisites`, one `PoolFencePrerequisite{name, state, detail}` per line, with
`name` closed to `hosts`, `watchdogs`, `bmc` and `quorum` and `state` one of `met`, `unmet` and
`unknown`. A line that is about particular hosts ends its `detail` before naming them and puts their IDs
in `hosts`, because a host ID is an opaque durable identifier and only a client knows the display
names. **Quorum is this pool's Corosync quorum**, read by the master from `corosync-quorumtool -p`:
`met` when the cluster is quorate and every ready host is in the partition, `unmet` when it is not
and naming the hosts outside it, and `unknown` — never read as met — where this host runs no Corosync
cluster at all, which is honest rather than a fault. The **fence-device** line is computed from the
fence-device method specifically and never from "any method is ready", so an armed watchdog can never
satisfy the prerequisite for arming watchdogs. The checklist is computed on the master from the
membership it owns and runs nothing.
Native qdevice's node ID 0 is a quorum vote, not a pool host; `LocalQuorum.hosts` excludes it,
so it cannot satisfy host-count or target-exclusion checks for the existing watchdog proof.

**Arming: the engine, and what it needs.** Nex Core does not feed a watchdog itself. It writes
`/etc/default/sbd` — `SBD_WATCHDOG_DEV=/dev/<sysfs name>`, `SBD_WATCHDOG_TIMEOUT=5`,
`SBD_PACEMAKER=yes`, no `SBD_DEVICE` (diskless watchdog-only mode) — and enables the packaged
`sbd.service`. **One hardware watchdog device, one owner:** sbd opens it, nothing else in Nex does,
and a device something else already holds open is reported and left alone rather than taken.

Three properties of that engine, all measured in a nested laboratory and none of them optional:

- **Diskless sbd needs Pacemaker.** sbd's only sources of truth are a shared block device and
  Pacemaker; with neither it starts, holds the watchdog open and **never expires it** — a host that
  reads as armed and could never be fenced. So Nex Core arms only where `pacemaker.service` is
  running and otherwise reports *this host is not running this pool's Pacemaker cluster*.
- **The cluster's `no-quorum-policy` must be `suicide`.** Measured both ways: with `freeze` a
  partitioned host was still running after two minutes, because freeze means "stay put" and sbd
  goes on feeding the watchdog; with `suicide` the same host left the quorate partition after 8.6 s
  and had reset and rebooted by 15.7 s, while the survivors stayed quorate and did not reset. So
  Nex Core reads the policy and **refuses to arm** a cluster that says anything else, rather than
  reporting a protection that does not exist. **The rest of what slice 4b settled here about
  `stonith-watchdog-timeout` — a bound of 30 s, and per-host BMC STONITH resources on the same
  cluster — is superseded by *The pool's own cluster* below: on a pool-owned cluster that property
  is not written at all and there are no STONITH resources.** The victim's reset does not depend on
  either: it is Pacemaker's own `no-quorum-policy`, and the lab's journal is that call —
  *"emerg: Signaling sbd to panic the system: Quorum lost"*. `PoolWatchdogBound`, the proof's own
  floor, is 60 s and never reads the CIB.
- **Arming happens only as a cluster starts.** Debian's `sbd.service` is `RefuseManualStart` and
  exists only as a dependency of `corosync.service`. Nex Core therefore enables it and then needs a
  cluster start. On a **pool-owned** cluster it performs that start itself, one host at a time (see
  *The pool's own cluster* below); on any other cluster — today, a clustered-storage cluster — it
  stops there and reports `ErrWatchdogArmingPending`, *put the host in maintenance mode and restart
  it*. **It never restarts a cluster that is running resources**, which is checked against the CIB
  and not assumed.
- **sbd waits for the cluster to form.** `SBD_DELAY_START=yes` makes sbd wait out the cluster's own
  message timeout before it begins feeding, so a cluster that is still forming is not reset while it
  forms. It is the second line of defence, not the first: the first is that Nex Core never enables a
  pool's cluster at boot.

Disarming is `systemctl disable` and **nothing else**: measured, disabling the unit under a live
cluster is inert (corosync, pacemaker and sbd all stayed active and the device stayed held), while a
`SIGTERM` left the unit `failed` on its `RequiredBy=corosync.service` edge — with no clustered
filesystem mounted, which is the case that matters and the one not measured. So a host that is still
feeding a watchdog reports `disarm pending` and releases the device at its next cluster stop,
mirroring the arming rule in the other direction; with `nowayout` only a reboot puts the timer down,
and Nex Core says so. The pool's durable record says *not armed* either way, which is what governs
whether a survivor may prove an exclusion. Disabling also runs on every host the pool has **not**
asked to arm, because Debian's `sbd` package leaves the unit **enabled on install** (measured) and it
is `RequiredBy=corosync.service`: an unmanaged sbd would otherwise be pulled into the next cluster
start with the distribution's configuration instead of Nex Core's. `nex-core-local`'s postinst
disables it unconditionally for the same reason, and never stops it.
Arming follows the Host fencing setting and the checklist and needs no command of its own: the
setting is the only decision and it already replicates. A host is never armed because of, and never
reset by, an agent restart, an upgrade, a join, an eject, a master change or a Controller outage —
sbd feeds the device, not the agent.

**The armed report.** `PoolMember.watchdog_armed` (`PoolWatchdogArmed`: `armed`, `kind`, `device`,
`timeout_seconds`, `fencing_generation`, `detail`, `observed_at`) travels on the same
`POST /api/v1/pool/advertise` route as a fifth key, is recorded in the calling member's own row, is
refused with 400 for an armed report whose kind could not reset a machine independently of its
kernel, and is written only when it changes. Unlike the capability beside it, it is **durable and
replicated**, because the host that has to read it is a promotion candidate whose master has gone and
which reads the signed replica it already holds. Its staleness rule is `fencing_generation` — the
pool membership generation the host armed at — and not a clock: a wall clock could not survive a lost
master, and the generation is exactly what changes when the set of hosts that makes a majority
changes. `observed_at` is when the finding last *changed*, as `PoolHostBMC.observed_at` is, and
nothing decides anything from it. An unarmed report carries no kind, device, timeout or generation,
so a stale record can never read as evidence.

**The watchdog method in the readiness shape.** Every host's `PoolFenceReadiness.methods` carries a
second entry, `kind: "watchdog"`, beside the fence device. It is `ready` only when the host has armed
an independent device, said so itself at this pool's current membership generation, and is inside a
quorate Corosync partition this master can read. Its other states:

- `not_applicable` — fewer than three ready hosts. Two hosts have no majority, so neither side of a
  partition may conclude it is the one that should keep running; those pools are fenced through their
  management controllers, and this is not something an operator can fix. It is not a wait and never
  reads as one. A pool of **one** host is told only that a watchdog needs three or more hosts, and
  not that it is fenced through its management controllers, because it needs no fence device at all.
  Decision: project owner, 2026-09-22.
- `not_ready` with Core's own sentence: the arming reason the host reported (no cluster, arming
  pending, the device already held by something else, sbd missing), *…only the kernel's software
  watchdog, which a hung kernel cannot run, so it does not count*, *…does not recognise this host's
  watchdog driver `<driver>`*, *this host reports no watchdog device*, *Host fencing is turned off
  for this pool*, *this host armed its watchdog under an earlier record of this pool*, *this pool's
  Corosync cluster is not quorate*, or *this host is not in this pool's quorate Corosync partition*.
- `unknown` — the host does not report whether it has a watchdog or whether it armed one, or this
  master cannot read the pool's Corosync quorum. A Core too old to report is *not reported*, never
  *none*.

**The `watchdog` proof kind.** `StorageClusterFenceStatus` gains `method` (absent for a fence device
Nex Core powered off and independently observed off, which is every proof there was before this;
`"watchdog"` for a host that reset itself) and `watchdog` (`PoolWatchdogExclusion`: `kind`,
`timeout_seconds`, `armed_generation`, `held_seconds`, `bound_seconds`, `partition`). A clustered
storage fence only ever produces the absent one.

Nobody performs this fence. The proving host establishes that the target's own timer must already
have reset it, and `PoolPromotionFenceVerified` re-checks all three conditions of the plan's rule
change every time the proof is used:

1. the target's own armed report is in the durable record, `armed`, of a kind that resets the
   machine independently of its kernel, with a non-zero timeout, **at the generation being
   promoted**;
2. the recorded partition is by the pool's Corosync votequorum, contains more than one host and does
   **not** contain the target — Nex's own heartbeats are never the input, because sbd resets a host
   because votequorum said so; and
3. `held_seconds >= bound_seconds >= 2 × timeout_seconds`, never less than a minute
   (`protocol.PoolWatchdogBound`). The hold is continuous on a **monotonic** clock, kept by the
   proving host's own reconcile pass, and **any flap restarts it from zero**: the target reappearing
   in the partition, this pool losing quorum, a gap in observation longer than three passes, or the
   proving agent restarting — an agent that was not running cannot say what happened while it was
   not looking.

Before the bound, Core refuses and says how long remains (*…has been out of this pool's quorate
partition for 31s of the 1m0s its watchdog needs; wait 29s and try again*), and the promotion
verdict's `old-master-exclusion` check carries the same wait per host. Proofs stay bound to pool,
host, operation and generation exactly as a fence-device proof is, and there is still no force and no
"declare host lost". A two-host pool can never produce one.

**Storage-lease preparation fields (inactive).** `StorageClusterFenceStatus` also has optional
`method: "storage-lease"` and `storage_lease` (`PoolStorageLeaseExclusion`) for typed native sanlock
evidence. The latter records `repositories[]` (repository ID/generation, identity digest,
lockspace, host generation/state, host-dead and held seconds), `host_lease_id`, `bound_seconds`,
watchdog kind/timeout and armed generation. Validation requires `HOST_DEAD` for every repository;
`HOST_FREE` and `HOST_FAIL` are not exclusion evidence. The Core hardware reset gate currently
withholds lease activation and proof production, so no promotion uses this method yet.

**Method choice in a promotion.** `poolPromotionFenceReadiness` answers for both methods in the
plan's order: the fence device where this candidate could use it — a usable profile and a credential
it can open — otherwise the target's own watchdog. `PoolPromotionHost.fence_kind` names the method
per host, so the UI shows what is about to happen rather than only that something will.

**Version compatibility.** A Core too old to report `watchdog_armed` leaves it absent, which is never
a watchdog proof and never readiness; a Core too old to accept the field ignores it, and the member
keeps reporting. An older master reports no `hosts` on a prerequisite, and the client shows the
sentence as written. `method` is absent on every proof an older Core produced, which is exactly the
fence-device meaning, so no proof changes meaning across an upgrade. Core accepts only an
absent method, `bmc`, or `watchdog` for implemented promotion proofs; it rejects unknown
methods instead of interpreting them as a fence-device proof.

### The pool's own cluster, and the arming rollout

API impact: **changed contract**, implemented. `PoolFenceSummary.cluster` (`PoolFenceCluster`),
`PoolWatchdogArmed.armed_at`, `PoolMembership.witness` (`PoolWitnessRecord`), per-member sealed
`PoolAuthorityReplica.witness_credentials` and the `pool-watchdog-cluster-v1` and
`pool-witness-v1` capabilities. The nodelist remains **derived** from membership; for an eligible
two-host pool Core also durably records the qdevice witness and replicates each member's sealed
credential. Controller stores no pool witness state. A fresh Controller obtains the membership and
status from the master report.

**When Nex Core forms one.** The supported watchdog cluster is a pool of three or more ready hosts,
at most `PoolWatchdogClusterHosts` (5) of them, with Host fencing on and **no clustered-storage
repository**. A two-ready-host pool with Host fencing on and no storage lease can separately
register Controller-hosted qnetd and configure native Corosync qdevice. That quorum vote is status
only for safety-proof purposes: `PoolFenceCluster` reports `forming` until qdevice and native
quorum are verified, then `degraded` while two-host SBD arming and witness-loss exclusion
proof remain withheld. Its optional `witness` field separately reports `forming` or `operating`
from the pool master's native qdevice identity, pinned CA, TLS and cast-vote checks; a voting
witness does not imply native quorum or an armed watchdog. Older Core reports omit the
field, and Controller passes it through without deriving witness health. A two-host
witness configuration omits `wait_for_all` so native quorum can return after qnetd
reconnects with one host down. The three-or-more-host watchdog configuration retains
`wait_for_all`. Updating an already running two-host Corosync configuration requires
a controlled service restart; a config reload did not clear the old runtime flag in
the isolated test. In the supported watchdog cluster every ready host that is not in
maintenance is included; the ring address is the host
part of the `peer_origin` the pool already records, the Corosync node name is the durable host ID
(which is what `corosync-quorumtool` reads back and what `pcmk_host_*` already matches), and the
node ID is a stable digest of that host ID, never reused for another host. A host the pool counts as
ready but has no address for stops the whole cluster being formed, with the reason: a host silently
left out of the nodelist is one the rest of the pool makes quorum decisions without. The master
writes the CIB policy — `no-quorum-policy=suicide` and `stonith-enabled=true` — and every host
reads `no-quorum-policy` back before it arms. `stonith-watchdog-timeout` is deliberately **not
written, and is removed wherever it is found**. Measured in the nested laboratory: Pacemaker
answers that property plus one node without sbd by logging *"emerg: Shutting down:
stonith-watchdog-timeout configured (30s) but SBD not active"* and taking the cluster down — and
the shutdown reaches nodes that are still feeding, whose sbd then expires rather than being
released. That is a healthy host reset, and **every** ordinary event on a pool's cluster produces
an unarmed node: forming it, the arming rollout, a teardown, and a host rebooting back into an
armed pool. What the property buys is a wait before Pacemaker recovers resources, and this cluster
has **none**; Nex's own proof bound (`PoolWatchdogBound`, 60 s, four times the measured worst case)
is what gates a promotion and is independent of the CIB. A host will not *restart* its cluster while the property is
still there — the teardown turn reads the CIB and waits — but nothing can strip it from a CIB
before Pacemaker is running, so a cluster that inherits one from an earlier Core or an
administrator is repaired by the master once it is up, not refused at the start. The property
belongs with the slice that puts DLM and GFS2 resources on a pool's cluster, where the wait means
something.

**Clustered storage and watchdog fencing exclude each other, in Core, both ways.** A pool with a
clustered repository is never given a watchdog cluster and reports *this pool runs clustered storage;
watchdog fencing for it is not available yet*; and `POST /api/v1/pools/{id}/storage-cluster`'s apply
is refused on a pool whose hosts are armed or whose watchdog cluster Nex Core is forming, naming the
way out (*Turn Host fencing off for this pool first*). Under both, one host has one
`corosync.conf` and `writeCorosyncConfig` refuses to take a file over from the other consumer, so
the interlock does not depend on an intent check being right. The reason is measured:
clustered storage writes `no-quorum-policy=freeze`, and a `freeze` host was still running two
minutes after it was partitioned.

**Nothing comes up armed.** Corosync and Pacemaker are **started by the reconcile and never enabled
at boot**, and `ApplyPoolCluster` disables `sbd.service` immediately before it starts them,
unconditionally. That is structural on purpose: sbd is still enabled from the last time this host
armed, on a `corosync.service.requires` edge that would pull it in, and the arming pass that would
otherwise have disabled it can fail to reach the master — which is exactly the state a host boots
into after a site power cut. This is the one hazard
with no failure behind it: a host whose cluster starts alone — a site power cut, the last host of a
pool rebooting — is inquorate from its first moment, and under `suicide` it would reset itself in a
loop while every host in the pool was healthy. `wait_for_all: 1` is set for the same reason in the
other direction: a partition that has never seen the whole pool must not decide it is the majority.

**Arming is a rollout, run by Core and never by a Controller.** A host arms only when it has watched
one unchanged Corosync membership, quorate and containing every host of the cluster, for a full
`PoolWatchdogBound` (60 s) — and only when it is **its turn**. Whose turn it is is derived from the
membership, not granted: the first ready host, in host-ID order, that has not reported itself armed.
There is therefore nothing to record, nothing to resume and no way for two hosts to hold it at once,
and the rollout survives a Controller that is not there at all. The host whose turn it is enables
sbd and restarts its own `corosync`/`pacemaker`; the others stay quorate throughout, and a host that
cannot arm holds the turn so the rollout stops there, visibly, with that host's own reason.
`PoolFenceSummary.cluster` reports `off`, `not_available`, `forming`, `arming` (with
`arming_host_id`), `armed` or `degraded`, with Core's own sentence.

**A teardown is the rollout in reverse, and that is a safety property, not tidiness.** Disarming
has latency: `DisarmLocalWatchdog` disables the unit and the device goes on being fed until that
host's cluster next stops. Expected votes do not shrink because somebody else stopped, so hosts
leaving in no order leave the last one standing inquorate and still feeding, and it resets — during
planned maintenance, or as the immediate consequence of turning the safety switch off. So
`PoolWatchdogArmed.feeding` reports the device separately from `armed`: **nobody leaves the
cluster while any host of it is still feeding**, and one host at a time puts its device down by
restarting its own cluster services while the rest stay quorate, in the same derived host-ID
order the arming rollout uses. A drained host stays in every survivor's nodelist until its own
record says it has stopped feeding, so the survivors never raise their expected votes above a host
that can still reset itself.

**A membership change is a reload.** Adding or removing a host rewrites the nodelist and runs
`corosync-cfgtool -R`, so Nex's ready count and votequorum's expected votes move together without
restarting a peer that is feeding a watchdog. A host entering **maintenance** stops feeding first and
leaves afterwards, as above; a three-host pool that loses one is two, which has no majority side,
so the whole pool disarms through the same ordered path and reports it rather than leaving two
hosts able to reset each other.

**The ring runs on the management network**, because that is where the pool's own traffic runs. A
staged management-network edit on an armed host is therefore a partition of that host, so
`POST /api/v1/hosts/{id}/network` **refuses to stage or apply while this host is feeding a
watchdog**, naming maintenance mode and the Host fencing setting as the ways out. The Host fencing
setting shows the same consequence beside the switch.

**`armed_at`, and the withdrawal window.** A survivor's armed record is only evidence about the
target as it is *now*. `PoolWatchdogArmed.armed_at` is the host's own clock at the moment it started
feeding a device, carried over unchanged while it goes on feeding the same one, so a host that
stopped and started its watchdog produces a record that **differs** from the one before — an
identical record is never written or replicated. A proving host refuses a watchdog proof against a
target that **joined its Corosync partition after the last armed record it holds**, because a host
that comes back comes back unarmed until the pool arms it again. Ageing the record out instead would
be wrong in the other direction: a legitimately partitioned host cannot refresh its report either,
so a freshness rule would refuse exactly the proofs the feature exists for. The cost of the rule
chosen is an honest refusal: a target that flaps while the pool has no master to re-confirm its
arming cannot be proved excluded until the pool confirms it again.

**Version compatibility.** A master without `pool-watchdog-cluster-v1` forms no cluster and reports
no `cluster`, which a client shows as no cluster line rather than as a cluster that is off. A member
too old to report `armed_at` leaves it zero, which is never treated as a re-arm; the proving host's
rejoin check then refuses after a rejoin until the record changes for another reason, which is the
safe direction.

**What is not proven.** **No physical watchdog device of any model has been read, armed or expired,
and no physical host has been reset.** Every timing above was measured in nested QEMU guests with an
emulated `i6300esb`; nested labs prove logic and timing, never hardware, and the acceptance gate
naming exact models is outstanding. **Pools with clustered storage are not armed at all**: the
`freeze` → `suicide` change under a mounted GFS2 filesystem, and whether DLM recovery on the quorate
side really waits out `stonith-watchdog-timeout`, are unmeasured and are their own slice and their
own decision. `stonith-watchdog-timeout=30s` and the 60 s proof bound rest on one nested-lab run of
each policy on a two-second poll. The arming rollout, the interlocks, the maintenance path and the
management-network refusal are covered by runnable checks and, where physical behaviour is involved,
by the nested laboratory recorded in the change record — not by a physical deployment.

### Staged management network edits (GET|POST /api/v1/hosts/{id}/network)

API impact: **new contract**, implemented. Two Controller routes, one Core command kind
(`management-network`), one inventory field, one health check and one capability. A host's own
management addressing is the one edit that can make the host unreachable, so the whole operation
belongs to the host: **Nex Core renders, applies, times and rolls back its own configuration**, and
Nex Controller submits one durable step at a time and shows what the host reports. The Controller
keeps no copy of the change, computes no countdown and is not required for the rollback to happen.

**Capability.** `management-network-v1`, advertised by the target host in the protocol v2
capability list and absent from the legacy v1 list, which also strips `management_network` from the
report. Without it, `supported` is false in the view and submitting anyway is
`409 staged management network edits need management-network-v1 on this host; upgrade Nex Core`.

**Observed DHCP values.** A fresh `GET /api/v1/hosts/{id}/network` returns the host's
`nics` including the live IPv4 address on `br0`. Its `management_network.observed_gateway`
is the live default IPv4 gateway on `br0` from the host route table, when present. Both
are observations, not configuration: the `current`/`spec` DHCP interface keeps `address`
and `gateway` empty, and POST still rejects them for DHCP. An older Core omits
`observed_gateway`; the Controller shows an unknown value rather than guessing. A stale
host report must not present these values as current. The field is additive in both the
Core inventory and Controller GET result; no command or retry behavior changes. Upgrade
Controller before Core because the older Controller strictly decodes Core reports.
The Hosts list and panel display the IPv4 address observed on `br0`, not a loopback or
unrelated interface address; absent observation displays an unknown marker.

**Durable state, and where it lives.** The host writes `<state-dir>/management-network.json`: the
operation ID of the change, its revision and stage, the spec, the verbatim previous contents of the
three files it replaces, the chosen window, the deadline, the applied/confirmed/connectivity
timestamps, and two host-local fields that make the rollback independent of the clock —
`boot_id` (`/proc/sys/kernel/random/boot_id` as read at apply) and `applied_uptime_ns`
(`/proc/uptime` at apply, nanoseconds). Both are `omitempty`, neither is reported to the Controller
and a fresh Controller needs neither: they are how the host measures its own window. A record
written without them behaves exactly as before. The configuration itself is the host's own `/etc/network/interfaces`,
`/etc/dhcpcd.conf` and `/etc/resolv.conf`, rendered as whole files with MAC-bound interface names
and a `br0` management bridge, exactly as the installer writes them. Guest VLAN bridges under
`/etc/network/interfaces.d/` are never read, written or brought down — the rendered file keeps the
`source` line that includes them — and `/etc/systemd/network/*.link` is never rewritten, so
**renaming an interface is not part of a staged change**: a rename only takes effect on boot and
could not be undone inside a rollback window. The spec names interfaces the host already has. The
record is projected into `Inventory.management_network`, which is the whole adoption path: a fresh
or rebuilt Controller reads the current configuration, the stage and any running deadline back from
the host with nothing for an operator to re-enter.

**Ownership proof and refusals before anything is touched.** Core refuses to overwrite a file it
cannot prove it rendered, by exact first-line match, the same discipline the guest VLAN path uses:
`/etc/network/interfaces` must begin with `# Nex Core: interface identity is bound by MAC in
/etc/systemd/network.` and `/etc/dhcpcd.conf` with `# Nex Core: only management DHCP owns routes
and DNS.`, so a freshly installed host passes with no state file at all and a hand-edited file is
refused with `<path> was not written by Nex Core or the Nex installer; edit it from the host
console instead`. A missing file is refused with `<path> is missing; Nex Core will not create a
management network configuration it cannot prove it rendered`. All three targets must be **regular
files**: `<path> is not a regular file (<mode>); replace it with a plain file before changing the
management network`. That last rule is a real limitation on a host where `systemd-resolved` owns
`/etc/resolv.conf` as a symlink; Nex Core's own installer writes a plain file.

**The four steps.** `stage` validates the spec, renders it, snapshots the three files and writes
the record — nothing in `/etc` changes, so a staged change costs nothing and can be replaced or
discarded. `apply` re-checks that the files still equal the snapshot, records the `applied` stage
**and its deadline before any file is written**, renames the three rendered files into place and
**reconfigures `br0` in place** — never `ifdown`/`ifup`, never `brctl`, never `ip link del` and
never `ip link set br0 down` while the bridge exists, because ifupdown's bridge hook deletes the
bridge and guest taps enslaved to it do not come back. Apply and rollback run the same helper over
the same parser, one reading the rendered files and the other the on-disk ones, so a rollback is
correct even after a crash before the file swap: `ip` brings the new uplink up, adds the VLAN
device when the spec tags it, enslaves it to `br0` and releases the old port, then sets the address,
route, hardware address and DHCP client (`dhcpcd -k` / `dhcpcd -n`). An unchanged uplink moves no
port at all; a guest tap is never detached. The three files are still rendered byte-for-byte, so the
next boot is unchanged. A failed write or an `ip`/`dhcpcd` failure rolls back immediately and
returns both errors. **Running VMs on the management bridge are, by design, no longer disturbed;
that has not been observed on a real host.** `confirm` keeps the change, clears the deadline, increments `revision` and drops the
snapshot. `cancel` does two different things, decided by the stage it finds. On a **staged** change it
discards it and nothing in `/etc` was ever touched. On an **applied, unconfirmed** change it
performs the rollback now, at the operator's request: the same `rollbackLocked` restore the
deadline would have run, with the reason `rolled back at the operator's request` and the durable
flag `requested_rollback` set before the restore begins, so a rollback interrupted before the
interface came back up resumes as the same operator rollback rather than as a failure. A
`confirmed` or already `rolled-back` change is still refused (`management network change <id> is
<stage> and cannot be cancelled`). Stage, apply, confirm and cancel are each idempotent for the
change they name: a repeated apply returns the original record with the original deadline, and a
repeated cancel of an already-undone change is the conflict above.

**The rollback is the host's, and it does not consult the clock.** An applied, unconfirmed change is
restored by the host itself when the window is spent, when an operator submits `cancel` against it,
and when the host finds that it **rebooted after the apply** — a reboot is not evidence that the new
configuration works, and the operator's window was spent while the host was down. Neither trigger
compares a persisted timestamp against `time.Now()`: "rebooted" is a **boot-id inequality**, and
"window spent" is boot-relative elapsed time (`uptime - applied_uptime_ns >= window`) within the
same boot. That is the whole point of the pair of fields above — a host cut off by its own change
typically loses NTP with it and comes back with a wrong RTC, which is exactly when the rollback has
to fire. `deadline` survives only as the operator-facing value, and `seconds_remaining` is computed
the same boot-relative way so the countdown cannot disagree with the trigger; `confirm` uses the
same helper, so a confirm can never be accepted for a change recovery is about to undo. A record
from before these fields falls back to the previous wall-clock comparisons. `/proc/uptime` counts
suspended time, so a suspended host spends its window and comes back rolled back — the safe
direction, since nobody confirmed. The check runs at agent start, on every probe tick and from an
exact in-process timer armed at apply; the durable record, not the timer, is the guarantee. The three triggers are
told apart on the wire by one durable bit: `management_network.requested_rollback` is true only for
the operator's own `cancel`, and the host emits **no** `management-network` health check for that
one, because an undo the operator asked for is an outcome and not a failure. An older Core reports
no such field, which reads as the automatic rollback it used to be. A rollback restores
the snapshot byte for byte and only leaves the `applied` stage once the interface is actually back
up, so an interrupted rollback is retried by the next pass instead of being forgotten — and a
rollback that has **started and not completed** is reported: a record still at `applied` carrying an
error emits a failed `management-network` check of its own, `restoring the previous management
network configuration has not completed: <reason>`, which the stage alone cannot distinguish from a
healthy applied change. Losing the Controller changes none of this.

**The one honest limit on the triggers.** All three run inside the agent process, so the rollback
needs the agent to be running. Both units are `Restart=always` (`packaging/systemd/nex-core-agent.service`,
`packaging/core-local/nex-core-local.service`), which closes the crash, OOM-kill and clean-exit
holes: `RecoverManagementNetwork` runs unconditionally at start, so the window is honoured late but
honoured. A unit deliberately **stopped or masked** between apply and deadline, or a package upgrade
that leaves it stopped, arms nothing until someone starts it again. No transient systemd timer is
installed. `confirm` is additionally refused until the host has **observed
its own connectivity over the new configuration**, and the observation is narrow on purpose: an
authenticated Controller report or an authenticated host-local API call since the apply whose
connection terminated on an address of the management bridge **itself**, with a far end that is not
this machine. A report that arrived over a second NIC, a call from loopback and a cached browser tab
are all paths that are not the one under test, and none of them counts: `this host has not yet been
reached over an address of br0 itself under the new configuration; a Controller report that arrived
over another interface, or a local API call from this machine, does not count`.

**Two identities in one request.** `operation_id` is the durable command's own, unique per step,
because it is the managed journal's replay key. `change_id` is the change Core acts on: the ID the
host recorded when it staged it and reports back as `management_network.operation_id`.
`ManagementNetworkRequest.Valid()` requires `change_id` for `apply`, `confirm` and `cancel` and
refuses a `stage` that names another change. The Controller fills it from the **host's own latest
report**, never from its own memory, so a replacement Controller resolves it identically; an
optional `change_id` in the request body must match that report or the request is a conflict rather
than a silent retarget.

**Client-facing routes.** Administrator session; the POST also requires the same HTTPS `Origin` and
`X-CSRF-Token`. `POST /api/v1/hosts/{id}/network` takes `{request_id, action, change_id?, spec?,
expected_revision?, window_seconds?, confirmation?}`; unknown fields are refused. `202 {host_id,
action, operation}`, and an exact retry of the same `request_id` and payload returns the original
operation with `200`. `400` for `host and a stable request_id are required`, the
`ManagementNetworkRequest` bounds, and an `apply` whose confirmation is not the host's own reported
name (`the confirmation must be the host name "<hostname>"`, compared trimmed and
case-insensitively here and again by the host itself). `404 host not found`. `409` for `this host
is revoked`, the missing capability above, `the host must be freshly connected before a management
network change is staged or applied`, `the host has not reported since the change was applied; it
cannot be confirmed until it reaches this Controller again`, `this host reports management network
revision <n>; reload the host network page and retry`, `this host reports no management network
change to <action>; reload the host network page`, `this host reports management network change
<id>, not the one you asked to <action>; it changed since you loaded it — reload the host network
page and retry`, and the shared `request_id already used with another payload` / `request_id
already used for another operation`. `500 storage unavailable` / `resource lookup failed` /
`operation lookup failed` / `management network storage failed` leaves the same `request_id` safe
to retry. **`cancel` has no freshness requirement** — it is the durable step that undoes a staged or an
applied, unconfirmed change on a host that has gone quiet, which is exactly when an operator most
needs it — while `confirm` requires freshness precisely because a host
that reached this Controller again is a host whose new configuration carries traffic. Audit:
`action = management-network`, `resource_id = management-network:<host id>:<operation id>`. The
spec is delivery data, not a secret, and is stored in `managed_resources` like any other command.

`GET /api/v1/hosts/{id}/network` returns `{host_id, hostname, connection, supported, state,
management_network, nics, check, last_operation}`. `hostname` is the host's own reported name,
which is the string an operator must type to apply. `state` is the record's stage, `none` when the
host has never staged a change, and **`unknown` whenever `connection` is not `fresh`**; the record
itself is still returned when it is stale, because a running rollback deadline must stay visible —
losing contact during the countdown reads as *unknown, with the deadline*, never as success or
failure. `management_network.current` is the live configuration parsed from the host's Nex-managed
`/etc/network/interfaces` and `/etc/resolv.conf` (and is reported with `stage: none` even when no
`management-network.json` exists yet), so the Controller prefills the edit form from what is on
disk rather than showing every interface as not configured. `management_network.spec` remains the
staged, applied or last change. `current` is absent when those files are missing or were not
written by Nex. `check` is the host's `management-network` health check and is `null` when the host is not
fresh, for the same reason; the host emits that check only as `failed`, while the record is
`rolled-back` (carrying Core's rollback reason) or while the record is unreadable (`the management
network record is unreadable`), so its absence means cleared. `nics` is the host's own reported
interface inventory, which is what a spec is built from, and `[]` before a first report.
`last_operation` is the newest `management-network` operation this Controller holds. `400 host
required`, `404 host not found`, `500 storage unavailable` / `operation lookup failed`.

**Asynchronous refusals are not an HTTP status.** Every step is a durable command, so Core's own
refusals arrive as the operation's `status:"failed"`. The conflict class is decided by one rule and
nothing else: the error **ends with** `management network conflict`
(`protocol.ManagementNetworkConflict`), which the Controller marks as `last_operation.conflict`.
Those texts are `a management network change is already applied and waiting to be confirmed or
rolled back`, `management network revision is <n>, not <m>`, `no management network change is
staged as <id>`, `no management network change is applied as <id>`, `management network change
<id> is <stage>`, `management network change <id> is <stage> and cannot be cancelled`, `the
rollback deadline for <id> has passed; the previous configuration is being restored`, `this host has not
yet been reached over an address of <bridge> itself under the new configuration; a Controller report
that arrived over another interface, or a local API call from this machine, does not count`,
`management network change <id> is already staged with a different configuration` (the same change
ID re-staged with a different spec: an exact retry replays the staged change, a different one is not
a retry at all), and `the host network configuration changed since this edit was staged; stage it
again`. Plain (non-conflict)
failures include `management network command carries no request`, `management network request does
not match its command`, `the confirmation does not name this host`, `this host cannot read its own
name to check the confirmation`, the spec validation texts, and `rollback window must be between
2m0s and 30m0s`.

**Stage-time refusals that read the host itself.** Before a change is staged Core checks the spec
against the machine, so an unusable spec is refused while nothing has been touched rather than
discovered at apply:

- `interface <n> does not exist on this host` and `interface <n> has hardware address <a> on this
  host, not <b>; the spec names a different card` — every NIC in the spec is checked against
  `/sys/class/net/<name>/address`. `br0` itself is refused as a NIC name by the validator
  (`interface name "br0" is not a usable interface name`), because it would render two stanzas for
  one device.
- `interface <n> carries registered guest networks (<ports>) and cannot be disabled; remove those
  networks first`, and `<port> is already the port of a registered guest network; the management
  interface cannot share it` — read from the host's own `/etc/network/interfaces.d/nex-*` stanzas,
  which are the durable record of a registered guest VLAN.
- `<port> is already a port of bridge <b>; free it before making it the management uplink`.
- `br0 does not record which port uplinks it, and guests are attached to it (<ports>); moving the
  management uplink would mean rebuilding the bridge and cutting them off. Change the uplink from
  the host console, or move those guests first` — the one case where the in-place reconfiguration
  cannot know which port to detach.

**A limit, stated rather than fixed.** A spec that simply **omits** a NIC that carries guest VLANs
is not refused: it loses its `iface … inet manual` stanza in the render, so the raw device is
unconfigured at the next boot. Whether an omitted NIC means "leave alone" or "disabled" is a
product decision, not an implementation one, and is open.

**The rollback window.** Chosen per staged change, default 600 seconds, bounded 120–1800 by Core;
the wire bounds it only at 0–3600 and `0` means the default, so an out-of-range value is an
asynchronous failure rather than a `400`. It is reported back as `window_seconds` so a staged
change can still say how long it will give after a page reload — `deadline` and
`seconds_remaining` exist only once the change is applied.

**Verification status.** Implemented and covered by offline contract checks
(`tests/test_api_contract.py`) and Go `-race` unit tests for the render, the validation table, the
stage/apply/confirm/cancel state machine with every refusal, deadline and reboot rollback with a
byte-identical restore, a rollback re-run after a failing `ifup`, crash windows before and after the
file swap, the foreign/missing/symlink/directory refusals, apply against changed files, the command
routing and inventory projection, the connectivity hooks, and cross-layer Controller-to-Core tests
that drive stage → apply → confirm, stage → cancel and stage → apply → cancel through the real HTTP
handlers into the real engine on a scratch `/etc`, with the result delivered on a later report so
the cut connection is modelled. The namespace test that would run real `ifup`/`ifdown`
(`TestManagementNetworkNetns`) **has never executed**: it skips without `ifupdown` installed. **No
native host run and no browser review have been executed.** The defect recorded here in an earlier
draft — *Roll back now* submitting a `cancel` that Core refused on an applied change — is fixed;
see `BUG-0327` in [the bug ledger](BUG_LEDGER.md). See
[Change a host's management network](guide/change-management-network.md#verification).

### Core pool peer join and repository observations

API impact: **new host-local/Core peer contract** — on an enrolled joining Core, local
`POST /api/local/v1/pool/join-csr` first produces the joining host's `{host_id,csr_pem}`. The
selected master then accepts `{member_host_id,member_csr_pem,master_url}` at local
`POST /api/local/v1/pool/join-offer` and returns the pinned pool CA plus a one-use offer. Finally,
the joining host calls local `POST /api/local/v1/pool/join` with `{master_url,pool_ca_pem,offer,peer_origin?}`.
All three calls use that Core's local root session, same Origin and CSRF boundary. The last call
pauses outbound reporting, then calls the master's Core-peer `POST /api/v1/pool/join` on its
configured pool-peer listener (default `:9444`) with an ECDSA P-256 CSR. The peer endpoint is TLS
1.3, pins the master's pool CA/certificate identity and accepts no redirect. Its only
unauthenticated peer exception is the unredeemed 15-minute one-use offer; it verifies the bound
host/CSR digest, atomically consumes the offer and advances the master-owned membership generation
before issuing the member certificate. A retry after acceptance must reconcile
`Inventory.managed.membership`; it must not resend the offer.

The master rejects malformed, expired, used-for-another-CSR or used-for-another-host offers. An
interrupted successful peer reply may be repeated with that same offer/CSR: the master returns the
same issued certificate and membership, never a second member. The joining Core persists its
pending CSR/key and pool rehome before changing scope. After a restart it resumes rehoming its
managed local defaults into the accepted pool; a rehome failure is an honest join-recovery error,
not permission to repeat the join with another identity. Joining is refused while the member has
managed VMs or remote storage, so this slice does not move workloads or storage during join.

Every enrolled Core is a pool master until it joins another pool: at agent startup a Core with no
membership record creates its pool CA and a one-member `PoolMembership` naming itself as the ready
master, and reports it in `Inventory.managed.membership`. A single-host pool therefore follows the
same master paths as a pool with members. A Core that is still the only member of its own pool may
join another pool, which replaces that record; a master with members or a member of another pool
may not. If the record cannot be written, pool storage and shared-VM operations stay refused.

The master retains pool CA material, offers and `PoolMembership` in its Core peer state; `peer_origin`
is an optional HTTPS advertisement recorded for an admitted member and shown as
`PoolMember.peer_origin`, never inferred from Controller connectivity. The member
retains its pool certificate/key and copied membership only after validating the master's signed
response. The Controller neither authorizes a membership mutation nor persists membership intent:
it adopts the Core-reported master membership and per-repository `member_access` observations.

The private peer `POST /api/v1/pool/vms/inventory` is a Core-to-master synchronization step,
not a public Controller endpoint or VM execution admission. It accepts only the pinned,
authenticated ready member identity. `204 No Content` means the master durably retained a complete
current-generation snapshot; `202 Accepted` means it durably retained the member's current partial
snapshot while it still waits for the other ready members. Neither response grants a start permit,
changes VM placement, or establishes that a guest is running. A stale snapshot receives `409` with
only the bounded authenticated master's `PoolMembership`. A member accepts that refresh only when
it is valid for its own pool, names the same master, keeps the member's own certificate fingerprint
unchanged, and has a strictly newer generation; it persists that membership and retries the inventory
once. Any invalid, foreign, identity-changing, non-newer or second stale reply remains refused.

**Failed start reconciliation (BUG-0644, development integration).** The existing authenticated
peer `start` action binds `operation_id` to the master's `pending_start_operation_id` and
increments the existing execution `epoch`. Core requires an exact operation ID and nonzero
epoch in the acknowledgement before native execution. Completion from `starting` to `running`
or `stopped` must match both values and the current owner. A stale completion cannot release
a later start. These fields live in the master's `pool-vms.json` and signed authority replicas;
Controller only displays host-reported state and outcomes.

Owner inventory can carry `start_outcomes` entries containing `vm_id`, `operation_id` and
terminal `status: failed`. An exact failed start plus fresh native shut-off observation can settle
the matching pending identity after a lost completion or Core restart. A fresh running
observation from the current owner can confirm present execution without granting another
start or changing an uncertain task outcome. Start-after-create uses the base create operation
ID rather than its synthetic display-task suffix. Its Core journal records whether failure
preceded native dispatch; missing or uncertain evidence cannot release a shut-off reservation.
Pending definition/recovery changes and unrelated failures also leave the start reserved.
Legacy `starting` records without a durable operation binding remain fenced.
Use matching development Core versions: an older peer's missing acknowledgement cannot
establish this contract. Emergency reducing-power requests remain independent of master
availability. Native acceptance of this correction is pending.

**Settling operations the master reserved but never saw finish (BUG-0536–BUG-0538).** A member
that refuses a `vm-create` or `vm-update` before executing it (for example because the master's
reply to `define` or `update-reserve` was lost) journals the operation as `failed` at stage
`validating`, exactly as Core records its own refusals; a redelivery returns that failure and never
executes. A `vm-create` interrupted while still `validating` settles as `failed` on its exact retry,
or on its own after about a minute without one, because Core saves `allocating` before the first VM-specific effect. The inventory carries that
proof to the master: `deleted` also lists a VM whose only create failed at `validating`, so the
master releases the owner record it took at admission, and the new optional `aborted` map (operation
ID → the same `FailedVMUpdateEvidence` `update-abort` uses) lets the master abort a matching
`pending_update` whose outcome it never received. Every other ambiguity still stays
`needs_attention`. Older members omit `aborted` and keep the previous behaviour.

API impact: **existing external contract; changed authenticated Core-peer contract** — a
pool-shared `vm-update` now advances the master-owned VM definition through a durable private
transition, rather than letting a member's local update leave the master's specification stale.
The owner first passes full managed-command validation and reserves exactly its current master
specification at revision *N* to one valid next specification at *N+1*. The master records only
`pending_update:{operation_id,next_spec}` at this stage; it does not change the committed
specification or autostart intent. It commits only read-only, successful local journal evidence
whose operation ID, previous specification and current specification exactly match that pending
record. A known failed journal result can abort the same pending transition only when it proves the
same prior and next specifications. Timeout, `needs_attention`, post-native/ambiguous failure,
missing evidence, stale owner, changed revision or malformed input retain the pending record and
leave the command/report `needs_attention`. Core settles such an unresolved remote-storage update
itself in its one-minute worker, on every pool host including a single-host master, once the entry
is a minute old and the VM's pool, host and execution context revalidate. It only observes: when
the persistent definition and state match the update, the journal records `succeeded` and the
master commits it from the next inventory evidence; when a shut-off VM's persistent definition is
still exactly its single succeeded predecessor after allocation or define, the journal records
`failed` (proven unapplied) and the master aborts it; a live attachment, running guest, rename or
any other difference stays `needs_attention`. It never defines, attaches, allocates or starts. An
unresolved update blocks only its own VM: the owner still publishes that VM, without update
evidence, and the rest of its pool VM inventory.

For the first local-to-pool-shared update, when no master record exists yet, `Previous` is only a
comparison value. The master seeds a record only from the authenticated owner's exact inventory
snapshot at the current membership generation, after that owner is `ready` and has a fresh local
runtime observation (`running` or `shut off`). An absent, stale, partial, foreign, duplicate or
mismatched inventory cannot adopt a definition. Once a VM has successfully used a registered
pool-shared repository, read-only managed journals keep it on this pool-owned path even if a later
definition removes its last shared disk or the repository is detached; a member-side cache or a
current disk scan cannot remove that fence.

`update-reserve`, `update-commit` and `update-abort` are pinned mTLS Core-peer actions, not
Controller/browser endpoints. After its durable save, the master reply must bind the VM ID, action,
operation ID, `update_accepted:true`, and attempted next revision; an empty, older-peer or
mismatched acknowledgement fails closed. While pending, competing lifecycle actions are blocked.
A lost commit response may later reconcile only through exact Core-side journal evidence for that
same operation, then report it succeeded under the original ID; raw inventory never rewrites the
master's definition. A pre-transition legacy update has the same bounded recovery requirement:
authenticated owner evidence of an exact successful *N* to *N+1* journal transition, never an
inferred inventory change. After a response-lost exact abort, the master retains one bounded
`last_aborted_update` receipt solely to acknowledge that same authenticated abort; a different
replay is refused, and the next reservation clears the receipt. Shared reboot is owner-authorized
without inferring guest state. A shared VM's media change takes the same transition: Core derives
the next definition from its own journal and applies a `vm-media` request that would rewrite the
definition (a different ISO, or an eject that clears one) as the `vm-update` at revision + 1 under
the original operation ID, so the master reserves and commits the exact next revision. A redelivered
operation reuses the command that attempt journaled rather than deriving a second revision. A media
request that leaves the definition unchanged — re-inserting the ISO it already names, to reconnect a
tray the guest emptied — keeps the `vm-media` path under the same owner admission as reboot, because
it changes no durable state. Admission still refuses a raw `vm-media` command that would rewrite a
shared VM's definition. The master keeps this state in its Core
`pool-vms.json`; a replacement Controller adopts the committed Core inventory and holds no durable
VM definition.

**VM deletion with retained snapshots (development integration).** Every pool VM delete,
including local-storage VMs and requests that keep disk files, requires the authenticated
owner's `delete-check` on `POST /api/v1/pool/vms/{vm_id}`. The current master checks the
exact owner and definition, refuses an unresolved transition or retained snapshot catalog,
and replies with the matching VM ID, action and `spec_match:true`. Missing or mismatched
acknowledgements, an unavailable master and frozen authority refuse deletion before native
changes. This check is read-only and can be retried; it does not remove the catalog or grant
permission to bypass native ownership checks. Native deletion rechecks retained snapshot
journals under its mutation lock, covering a capture that finishes after the master check.
Older peers without this action fail closed;
use matching development builds. The client-facing VM delete request is unchanged.

**Snapshot recovery authority (development integration; acceptance open).** The master reports
its durable pending reservations in `ManagedState.recovery_reservations` and retained descriptors
in `pool_vm_snapshots`. Their respective `recovery_reservations_reported` and
`pool_vm_snapshots_reported` flags are true only after a complete successful read. Omitted or
false means unknown, not empty. Members cannot publish a master catalog, and older hosts omit
these additive fields. Controller retains the authenticated observation for display and adoption.

New snapshot descriptors carry `dependencies` with sorted, bounded `storage_ids`,
`network_ids` and `media_ids`, derived from the exact captured VM specification. The master
retains them with the catalog and verifies them in settlement. Missing legacy dependencies
mean unknown and conservatively refuse destructive resource changes; they never mean an
empty set. Current VM edits do not discard a retained point's captured references. Ordinary
storage leases/starts and non-destructive metadata operations keep their existing contracts.

Network/media removal records an exact-operation mutation in the master's existing
`pool-storage.json` mutation ledger before native work. Every ready member must acknowledge
the exact marker-containing authority replica before removal can begin; a busy push, missing
or stale acknowledgement is not permission to proceed. Competing VM create/update and
snapshot admissions cannot add a reference to that resource until the outcome is definitive.
An uncertain native removal remains `needs_attention`; restart and exact-operation replay
retain its protection. Core's existing background worker revisits the exact saved removal
after rechecking current ownership, dependencies and replica acknowledgement. A definitive
journal result also lets Core retire its matching marker after an interrupted settlement,
without repeating native removal. A pre-execution refusal cannot release uncertainty from
an earlier attempt. Signed authority replicas include the bounded `resource_mutations`
section, and promotion preserves unresolved markers. Replicas missing that required section
cannot establish the new recovery contract. Use matching development Core versions and
upgrade Controller before Core for the expanded snapshot descriptor.

Each reservation binds `operation_id`, `kind`, `pool_id`, `vm_id`, `owner_host_id`, ownership
`epoch`, `spec_revision`, `spec_sha256`, `disk_identity_sha256` and `intent_sha256`. Snapshot
creation and revert also bind nonzero `owner_operation_order` from the owner's durable journal.
The last settlement's order fences older requests from the same continuing owner, including
after a start advances its execution epoch,
so a delayed request cannot acquire a new reservation after a later operation settles. The current
create path uses `snapshot-create` with optional `mode` (`stopped`, `crash`, or `quiesced`);
stopped `snapshot-revert` and leaf `snapshot-delete` use their own kinds, and catalog
metadata updates use `snapshot-metadata`. The master's existing `pool-vms.json` holds the
pending reservation, proposed create descriptor or master-derived revert result, retained
snapshot catalog and one terminal replay receipt. Native paths, complete capture intent and
progress remain in the executing owner's managed journal.

The authenticated owner reserves capture using the pinned mTLS `snapshot-reserve`
action on `/api/v1/pool/vms/{vm_id}`. The exact acknowledgement binds operation, VM, metadata,
capture time, owner, definition and immutable intent before native effects. Names are unique
within the VM and the catalog is bounded to 32 retained points. Snapshot identity is the original
operation ID. Members read the master's catalog through
`GET /api/v1/pool/vms/{vm_id}/snapshots`; they do not substitute a local native list when authority
is unavailable. Host-local operator ingress is documented under the snapshot development workflow.

The revert extension uses the same `snapshot-reserve` peer action with
`recovery_kind: snapshot-revert`, `snapshot.snapshot_id`, its exact internally resolved
`snapshot.target`, and `captured_spec` from the owner's immutable capture journal. The master
verifies captured specification/disk hashes and dependencies against its catalog, then derives
the next revision from its current VM record. It preserves current identity, name, description,
tags and autostart; captured hardware, disks, NICs, boot and media replace execution settings.
The reservation binds `target_snapshot_id`, `target_snapshot_sha256` and `result_spec_sha256`;
`pool-vms.json` retains the pending derived result specification for promotion and exact
settlement. A completed revert supplies `recovery_evidence.result_spec`, which must match that
result exactly. It updates the current VM definition and receipt without creating or removing
a catalog point. Unknown or partial effects keep the reservation. These development fields
require matching Core/Controller versions; native revert acceptance remains open.

Reservation and settlement responses additionally require every eligible ready member to acknowledge
the exact authority replica containing the pending reservation or terminal catalog receipt.
Core releases state locks during peer I/O, then rechecks current authority and the exact
transition before returning success. A single-host pool has no member acknowledgement to
await. Missing, lost or stale acknowledgement leaves the operation unresolved, including
when the master already saved it; peer `409` may therefore mean saved-but-unacknowledged
state, not proof of no effects. Retry the exact operation after connectivity recovers. A
successful asynchronous replication trigger alone does not satisfy this requirement.

In the development promotion correction, `PoolMember.replica_ack_excluded` records a
verified-fenced member's exclusion from snapshot reservation/settlement acknowledgements
and ordinary replica-push failure aggregation. Only supported
promotion can set it, using fresh fence evidence bound to the exact host, pool, operation and
authority generation. Mere unreachability cannot set it. The member remains in pool identity,
VM ownership and inventory completeness checks; Core still attempts replica delivery. This
does not relax destructive resource-removal acknowledgements, storage-lease preparation,
restarted-master confirmation, fence-profile retirement or fresh fencing in
a later promotion. The master clears it only after accepting current-membership VM inventory
and verifying an exact current-generation/content replica POST acknowledgement. Clearing
changes signed authority content, so subsequent operations require acknowledgements of the
new content. The durable field lives in `pool-peer.json` and its signed authority replicas;
Controller adopts the reported membership. Absent means false. Use matching development
Core builds; a prior promotion without this field remains conservative. The bounded
[r8 virtual-fence promotion/rejoin check](qa/2026-09-26-recovery-snapshot-promotion-r8.md)
passed; this does not establish independent physical fencing.

A duplicate name or full catalog returns an exact durable reservation with
`admission_refusal` set to `name-in-use` or `catalog-full`, and `snapshot_accepted:false`.
The owner must not execute native capture for that reservation. It proves absence of effects
and settles through the same `failed-before-effects` path; only then is the failure terminal.
Exact reservation retries preserve the refusal. Unknown responses and transport failures remain
unresolved rather than being interpreted as definitive rejection.

The pinned mTLS `snapshot-settle` action on `/api/v1/pool/vms/{vm_id}` carries one bounded
`recovery_evidence` object. The master validates the authenticated owner, current membership,
ownership epoch and pending reservation, and refuses frozen or handed-off authority, unknown
reservations or mismatched evidence with `409`. Only `snapshot_settled:true` with the exact
evidence echoed back acknowledges durable settlement. An exact retry returns the same retained
receipt. A generic `/inventory` HTTP `200` is not a local reservation-release acknowledgement.

Exact owner evidence carries `completed` or `failed-before-effects`, the reservation and
`journal_sha256`; completed creation also carries the matching snapshot descriptor. A timeout,
partial native result or generic inventory acknowledgement cannot release the local fence.
The owner requires acknowledgement of the exact durable master settlement before reporting a
terminal result. The digest binds journal evidence; it does not prove a working restore.
Snapshot creation preserves the VM specification revision. Retained descriptors include identity,
name, description, capture time and captured specification/disk identity digests.

Managed local-storage start, wake, reboot and media changes use current-owner admission just as
shared-storage VMs do; shutdown and force-stop remain available without a master round trip.
Pending recovery blocks conflicting VM operations, dependent storage changes and pool rehome.
Ejection of a member with retained snapshots is also refused until its catalog can move with
the VM; master-role transfer retains the catalog in the existing authority replica.
Controller replacement adopts the master observation and explicit owner results; absence of an
operation from inventory never turns an unresolved snapshot into success. Adopted Controller
operation IDs for snapshots are a deterministic host-scoped projection; the original Core
operation ID remains in the existing `adopt-outcome:<host>:<operation>` request key. This
prevents two hosts using the same client-supplied ID from hiding each other's history. Core
snapshot IDs and host-local status lookup continue to use the original operation ID. Native host-API,
outage and replacement acceptance remain open; this contract does not advertise completed
snapshot capability.

The master's Core peer listener also exposes pool-storage reconciliation only to an authenticated
member certificate: it serves desired NFS/SMB repositories and accepts that member's access result
at `/api/v1/pool/storage/{repository_id}` and `/access`. It never exposes a local ZFS or ordinary
iSCSI repository. For SMB, it transfers the secret only over this pinned mTLS connection and the
member immediately replaces it with a member-scoped Core-local receipt; the Controller never
receives it. A member persists the desired registration through its normal managed-storage journal,
prepares the mount as its QEMU identity, verifies a shared non-blocking filesystem lock against
the master, then reports identity/readable/writable/QEMU-writable/locking observations. The master
retains desired records durably; current member access observations live in master Core memory
and are rebuilt from authenticated reports after restart. A replacement Controller adopts their
inventory projection.

The same peer listener exposes pool-wide VLAN networks at `/api/v1/pool/networks` and
`/api/v1/pool/networks/{network_id}` (list/get) with `POST …/access` for per-member apply
status (uplink present, bridge name, generation, error). Only `kind=vlan` registrations are
listed; host-local bridges and libvirt networks are not. Members materialize the bridge locally
with the master's network ID, then report access. ISO library metadata for media whose
`storage_id` is pool-shared is listed at `/api/v1/pool/media` with the same access-report shape;
bytes ride the shared repository mount, and members journal an adopted `iso-import` once the
file is present at the listed size; every attach verifies the digest. Controller placement uses `member_access` the same way it does for shared storage.

The synchronous peer lock check uses `POST` and `DELETE` at
`/api/v1/pool/storage/{repository_id}/lock`, with the current `generation` and a
32-hex-character random `challenge`. It is bound to the authenticated member,
repository, generation, challenge and mode; POST requires current member eligibility.
`probe_mode=challenge-byte-v1` selects a nonzero one-byte OFD lock range derived
from the challenge on the existing `.nex-peer-lock` file. Version 1 hashes the
decoded 16 challenge bytes with SHA-256, reads its first eight bytes as big-endian,
clears bits 63 and 0, then adds one. The master acknowledges
that exact mode in `X-Nex-Pool-Probe-Mode`. Separate challenges can prove exclusion
and release without competing for byte zero. A rare range collision fails the
probe; it never grants access. Only lock-conflict errors establish exclusion,
and the member must also acquire the same range after master release.

An absent mode, or no mode acknowledgement from an older master, uses legacy byte zero (with
its contention limitation); an unexpected nonempty acknowledgement fails closed. Unknown
requested modes and malformed challenges are rejected. Leases are
process-local and expire after ten seconds if cleanup is lost; DELETE releases
only that member's exact lease. The master admits at most 64 simultaneous probe
leases; an exact held-lease replay remains idempotent at the limit, while a new
lease receives a lock-unavailable conflict until release or expiry frees capacity.
Periodic Core reconciliation retries with a fresh
challenge after failure or reconnect. These probe leases are neither durable pool
settings nor VM execution/fencing authority. Controller-facing schemas are unchanged.

`PoolStorageAccess` is an observation, not placement permission. The Controller permits an NFS or
SMB repository for a member VM only when the master has recently reported matching generation,
identity, readable, writable, QEMU-writable and locking fields without an error. Before a VM
write/start Core rechecks source identity and access; ambiguous ownership, a failed lock/fence or
replaced backing identity fails closed. The member pulls desired records on its own periodic Core
lane and retries stored reconciliation after an outage; Controller does not drive a retry loop.
There is no compatibility promise beyond matching development protocol v2 peers/capabilities.

Before a member executes a VM disk, ISO/media or related managed operation on reconciled NFS/SMB
storage, it reserves the repository and its observed generation at the master over peer mTLS.
The master accepts only the authenticated member's own reservation for a current non-tombstoned
NFS/SMB record; a pending source/credential/detach mutation, changed generation or invalid body
is refused. A failed local operation releases its reservation. A successful durable definition
keeps it until the member's healthy serialized managed journal proves no VM disk or ISO still
references that repository; then the member releases the unused reservation. This prevents a
master source change or detach from racing a member workload.

For a master NFS/SMB source update, credential change or detach, Core first writes a durable
mutation marker and refuses new reservations. It fails closed while any member reservation exists;
there is no dependency RPC that guesses whether a member workload stopped. On failure/restart the
marker/reconciliation state remains Core-owned and reports an honest unavailable result until the
normal serialized journal/retry path can converge. These contracts apply only to Nex-managed
repository references: generic native guest disks remain host-local and this slice neither
discovers nor fences them.

Members also report the existing `Inventory.checks` entry
`pool-storage-reconciliation`: it is `unknown` before the first reconciliation or after a stale
two-minute observation, `failed` after a Core reconciliation failure, and `healthy` only after a
fresh successful reconciliation. It adds no route or durable Controller state; the Controller
renders the Core observation and must not infer readiness when it is stale or unknown.
The free-form `error` reports only a bounded stage (master address/client setup,
repository-list retrieval, repository reconciliation, local observation, orphan detachment)
and safe outcome, never raw peer/command text. Deadline and cancellation outcomes are failures.
A fresh successful pass replaces the failed observation; healthy mount/access reports alone do
not prove reconciliation succeeded.

Members report `pool-network-reconciliation` and `pool-media-reconciliation` in the same
`Inventory.checks` list with the same `unknown`/`failed`/`healthy` meaning for pulling the
master's guest VLANs and shared ISO entries; the master reports neither. API impact: existing
contract — no route or field is added, and the process-local observation is not durable state.
A member re-creates a journalled VLAN guest bridge it observes missing **while the master still
lists that network**, and adopts or keeps a shared ISO whose file has the recorded size without
hashing it, since one probe timeout cannot hash a multi-GiB ISO (BUG-0541); every media attach
verifies the digest. Once the master has durably
removed the network, the member's journal holds a tombstone instead, so the bridge is torn down
and never rebuilt. A member's own refusal to remove (a local VM still uses the resource) appears
in these same two checks, appended to the outcome word from this member's own resource and VM
IDs; no remote text is ever rendered into a reported check.

### Core-owned clustered GFS2 iSCSI

API impact: **new contract** — an administrator can relay setup with
`POST /api/v1/storage/cluster/apply`, read the current master-owned result with
`GET /api/v1/storage/cluster?pool_id=...`, and request an actual target exclusion proof with
`POST /api/v1/storage/cluster/verify-fence`. These routes require the administrator session,
and mutations also require same-Origin CSRF. The Controller selects the fresh capable pool master
(`storage-clustered-iscsi-v2`); a client cannot choose the setup host. It retains request data only
for the active report round trip and removes it on delivery, result, cancellation or the 30-second
deadline. The Controller's 45-second HTTPS write deadline is only a delivery margin for that
30-second ambiguous relay response; it does not extend Core work. It never persists a cluster
configuration, job, credential, credential receipt or proof.

A clustered-storage configuration is a replicated pool-authority section, so `apply` and
`verify-fence` are authority writes and take the same guard every other registry write takes: a
master that is frozen for a master change, has handed its authority over, or is restricted starts
neither, and answers with the existing retryable sentence `pool authority is frozen for a master
change; retry when it completes`. `GET /api/v1/storage/cluster` is a read and is unaffected. The
guard is at the entry point, not at the resume, so work already in flight is never interrupted.

Clustered-storage protocol v2 has an explicit Controller-first acknowledgement. A newly started,
reconnected or replacement-enrolled Core sends a clean v2 preflight: it advertises neither the
base `storage-clustered-iscsi-v2` capability nor clustered result payloads. Only a matching Controller's
`ReportResponse.storage_clusters_support:{capability:"storage-clustered-iscsi-v2"}` enables the
Core to persist base readiness, advertise that capability and send its queued clustered results on
a later report. The response's `storage_clusters` JSON array remains the dispatch envelope (the
internal Go field name does not change the wire name).

The same existing `ReportRequest.capabilities` list offers the additive
`storage-cluster-session-monitor-v1` extension on every v2 preflight. A Controller returns that
exact value in `storage_clusters_support.capability` only when the offer is present; otherwise it
returns the base `storage-clustered-iscsi-v2` acknowledgement. A Core receiving the base value may
use the base clustered contract, but projects `session_monitor_ready` and members with
`stage:"monitor"` out of Controller reports. A Controller accepts those additions only when the
offer was present, so an older Controller acknowledges base v2 and never receives an undecodable
field; native peer state and the Core's durable operation are unchanged. Both acknowledgements are
Controller-connection scoped and reset on a cluster preflight failure, reconnect or Controller
re-enrolment. This is a scoped v2 acknowledgement, not a protocol-v3 bump.

Until that acknowledgement arrives, every clustered-storage route that requires the master or a
target Core (`GET /api/v1/storage/cluster`, setup, credential handoff and fence verification)
returns **503** `clustered storage capability negotiation is pending`, creating no broker request,
native work, credential offer or older-route fallback; retry the same request after the
acknowledged follow-up report. A Core that never offers the v2 preflight is an unsupported **409**.

Clustered iSCSI uses `source.iscsi.mode:"clustered_gfs2"` rather than the ordinary exclusive
profile. It requires a cluster ID, lock table and exactly three to five unique eligible Core IDs,
and rejects `exclusive_target`. Core accepts only an already-formatted GFS2 LUN whose lock table,
UUID, WWID and journal count match the registration; it never formats the LUN. The `apply` payload
has a stable request ID, pool ID, expected repository generation, typed registration and typed
cluster configuration. Core derives the native device and mount paths from its verified iSCSI
identity rather than trusting caller-supplied values. An identical request ID is restart-safe;
changed input is refused. `expected_generation` is optional: omit it or use zero for a candidate
repository before Core allocates its generation. A `200` gives the latest Core operation projection:
`queued`, `awaiting_fencing`, `verifying_fence`, `applying`, `failed` and `needs_attention` are not
a healthy multi-host mount. Initial setup remains `applying` until the master durably records a
private activation receipt bound to the exact operation/configuration and membership after fresh
health from every selected Core. The receipt is not a public field. Only after that completed
activation can Core report `degraded`: it is a current runtime observation of a fresh strict
ordinary-voter majority that includes the master, with exact membership/configuration/fence/CIB
bindings and the required per-current-member DLM/GFS2 clones, but without fresh health from every
member. It is not initial setup completion, HA, failover, ownership reassignment or a new client
request. A missing majority remains `needs_attention`; a legacy record without the private receipt
must requalify all selected Cores once and is never inferred from an old `healthy` status. Only the
Core-derived `healthy` state represents the current all-member readiness predicate. `degraded`
does not establish VM execution admission (not implemented). `504` is
ambiguous: Core work may continue, so read status instead of assuming cancellation.

API impact: **changed v2 clustered-storage contract** — each
`StorageClusterResult.operations[].members` item is a `StorageClusterMemberReport`, not a
flattened `StorageClusterStatus`. The wrapper carries the reporting `host_id`, nested `status`,
storage and membership generations, cluster ID, configuration digest, `health` stage (or the
negotiated `monitor` stage) and observation time. Core and Controller reject a blank, duplicate or
non-configured host and any binding that does not match the operation configuration. A client
expecting the former status-only member array is incompatible.

API impact: **changed additive clustered-status contract; no new Controller route** — the nested
`StorageClusterStatus.session_monitor_ready` is an authenticated member readiness attestation.
Core sets it only after the fixed local wrapper/CLI is installed and a successful live, exact native
session probe proves that adapter can monitor the configured portal/IQN. It is not aggregate
GFS2/cluster health and does not mean a transport clone already exists. Older members omit it
(false). It and the `monitor` stage reach Controller only after the session-monitor
acknowledgement above. The master requires fresh, exact configuration/generation/membership-bound
true reports from every configured member before first transport-clone creation or a recognized
legacy CIB upgrade; unknown, missing, old, mismatched or failed-monitor reports block. Runtime
health separately requires the active strict transport clone and its native session check. The
Controller only relays the status report and stores no readiness state.

The master persists its secret-free desired configuration, membership generation and operation
state in its Core `pool-storage-cluster.json` record. Members pull its projection and post their
generation-bound status over the existing pinned pool mTLS peer endpoint; the master records those
reports. The master generates one private Corosync auth key and projects it, plus addressed
secret-free metadata and recipient-specific private credential material, only across the pinned
Core peer channel; Controller reports, inventory, operation results and the Controller database
never carry that material. A fence request has a stable request ID. Before a destructive peer
action, both the master and its selected sole survivor durably record that ID and its binding; a
replay with changed input is refused. The private peer `POST /api/v1/pool/storage-cluster/verify`
requires that request ID. After reconnect, the master retrieves only request-bound receipts that
were actually executed locally by that sole survivor from its mTLS
`GET /api/v1/pool/storage-cluster/proof/{repository_id}` endpoint; it does not accept a peer claim
or synthesize a proof. Thus a lost peer reply is reconciled from Core state rather than treated as
success. A fresh Controller recovers this state through Core reports and peer reconciliation, not a
Controller database. Cluster setup stays blocked until Core sees the exact current membership as
healthy, quorate, Pacemaker-mounted and fence-verified. Clustered-iSCSI VM execution admission is
not available; an unavailable master, stale membership, missing proof or uncertain native state
fails closed.

When a clustered-storage operation is applied, Core installs its exact root-owned
`nex-cluster-storage-ready.service` and a Pacemaker ordering drop-in. At OS boot the unit runs the
same bounded local command exposed by both Core binaries:
`nex-core-agent cluster-storage-ready --state-dir /var/lib/nex-core-agent` or
`nex-core-local cluster-storage-ready --state-dir /var/lib/nex-core-agent` (the default state
directory may be replaced with the configured private absolute directory). The command accepts no
positional arguments and has a two-minute deadline. It reads a stable, private snapshot of the
existing Core state, membership and `pool-storage-cluster.json` record without taking the live
agent lock, contacting Controller or a peer, or writing the Pacemaker CIB. Before any iSCSI
connection it rejects an unsafe or changing snapshot, ambiguous projection, non-current ready
three-to-five-member membership, invalid registration/configuration, or missing recorded fence
verification. Failure prevents the dependent Pacemaker service from starting; success is only
bounded native preparation, not a report of healthy shared storage or VM execution admission.
Clustered-GFS2 native acceptance (failed-fence admission, outages/adoption, HA, failover) remains
open; results are in [concurrent storage verification](qa/2026-09-12-concurrent-pool-storage.md).

Storage-loss protection is an internal Core/OCF interface, not a Controller or operator API.
Both Core executables expose the root-only command
`cluster-iscsi-session --portal <canonical-host:port> --target <exact-IQN> --session-id
<nex-iscsi-full-SHA-256>`. `--validate-only` checks static parameters without querying iSCSI;
without it the command has a fixed eight-second, read-only `iscsiadm -m session -P 1` check. It
does not take a state directory or credentials and never discovers, logs in/out, mounts, formats or
changes authentication. The `nex-core-local` package delivers the fixed root-owned OCF adapter at
`/usr/lib/ocf/resource.d/nex/cluster-iscsi-session`, which invokes only
`/usr/lib/nex/nex-core-local cluster-iscsi-session`: `start` and `monitor` perform one bounded
check, while `stop` is a no-op. Its proposed monitor metadata is a five-second interval and
ten-second timeout, not a fencing guarantee. Do not invoke this internal command or adapter as an
operator recovery procedure.

API impact: **existing contract** — clustered fencing policy is Core-owned runtime safety
configuration, not a Controller setting or a new browser/API operation. The master requires the
exact Pacemaker properties `stonith-enabled=true`, `stonith-action=off`,
`startup-fencing=true`, and `no-quorum-policy=freeze`; a missing, duplicate or different value
blocks health. Each Core-owned fence device also requires the exact
`pcmk_reboot_action=off` mapping: an external DLM reboot request therefore maps to an off action,
while DLM fencing itself remains enabled. The explicit off actions preserve a Core that a real
proof deliberately powered off instead of allowing Pacemaker's default reboot action to return it
automatically. For an already accepted master operation, Core may reconcile this policy only after
it verifies current master/membership, exact durable configuration/digest/fence proofs, matching
managed registration, and exact owned CIB resources, fences and ordering. It no-ops when the
properties and every device mapping already match. There is no in-place upgrade of an older CIB
layout: a CIB missing any current device mapping, monitor timeout or transport resource, or a
wrong, duplicate, foreign or otherwise changed CIB, is refused without a write. A stale receipt or membership, failed
CIB read, or cancellation also performs no write or health promotion; policy reconciliation never
connects storage, mounts GFS2, or creates/reconciles CIB resources.

A durable `needs_attention` operation may be resumed by Core from the previously accepted,
secret-free Core record. That replay can repeat its bounded native setup stages (iSCSI preparation,
native cluster apply and GFS2/Pacemaker readiness checks) for the same durable identity; it is not
a new Controller request or reconstructed input. Its completed private activation receipt is copied
and checked rather than reconstructed or inferred from the retry. This preserves the distinction
between prior all-member activation and current health without authorizing an automatic fenced-host
return, guest restart or a new fence action. There is no automated cold-recovery workflow;
operators inspect the reported state and use the explicit proof/status workflow.

API impact: **existing contract** — no Controller state, endpoint or browser action is added for
native DLM/Pacemaker prerequisites. Clustered GFS2 fails closed when Core cannot validate a
package-aligned DLM fencing helper. The inspection is non-destructive: it reads the helper's
declared runtime `libstonithd` SONAME, requires exactly one loader-cache candidate of the same ELF
class/machine with the required exported APIs, and does not execute the helper or a fence agent.
An ABI mismatch blocks recovery; it is not a reason to substitute a library or acknowledge
fencing. Until Core reports fresh validated status, clustered health and execution admission
remain blocked.

Fence profiles are closed types (`redfish`, `ipmilan`, `virsh`, or `scsi_pr`), not arbitrary
commands. A BMC (`redfish` / `ipmilan`) `target_id` is the Redfish ComputerSystem `Id` or IPMI
plug: it follows DMTF DSP0266 §9.6.6 (no RFC1738 unsafe characters in an Id used as a URI
segment) and therefore allows period — Dell iDRAC uses `System.Embedded.1`. It is not validated
as a Nex opaque storage/host ID. The development-only `virsh` profile must bind to the same pinned SSH scope: its
`virsh_uri` is exactly `qemu:///system`, or
`qemu+ssh://<ssh_login>@<endpoint>[:<ssh_port>]/system`. The latter has the same login and endpoint
as the fence fields, permits an omitted URI port only when `ssh_port` is `22`, and rejects a
password, query, fragment or any other libvirt URI/path. Core materializes a one-host default-port
known-hosts entry written as `[DNS-or-IPv4]:22` to bare `DNS-or-IPv4` for OpenSSH lookup, while
preserving the supplied credential receipt and leaving aliases, IPv6, non-default ports and
ambiguous entries unchanged. `POST /api/v1/storage/cluster/verify-fence` names the repository and target host, carries
a stable `request_id`, and requires the literal confirmation `FENCE <host_id>`. The Controller
returns Core's immediate `accepted` lifecycle response and creates no durable fence intent itself.
Core then records the real cluster operation as `verifying_fence` with a request-bound,
`unverified` pending proof. Browser input cannot create a proof: the actual executor Core records
an exclusion observation whose optional `request_id` binds it to this destructive attempt, as well
as to the exact configuration generation and digest. An early failed observation is reported as
`uncertain`, not proof of exclusion. The same 30-second relay timeout is ambiguous and must be
reconciled through status before another destructive attempt. The UI closes and disables a target
after an unknown transport outcome until a successful Core status refresh. A refreshed `failed` or
`uncertain` proof can be manually reverified only with a new request ID and a new exact
confirmation; Core retains the old proof and never retries it automatically. A pending
`unverified` proof stays disabled. This is the existing recovery workflow, not a new endpoint.
For an activated operation, every member's health report must carry an exact `Status.Fences`
acknowledgement of that member's current passed exclusion proof: host, request, target, status,
exclusion result, configuration generation/digest and observation time all bind. A successful
direct re-verification immediately changes an otherwise all-member healthy operation to `degraded`
by invalidating the target's earlier health report; only a fresh target report with that exact proof
can restore `healthy`. Missing, stale, failed, pre-proof or mismatched reports do not count toward
health or the majority. This uses the existing report/status shape and retains the private
activation receipt.
Automatic ownership reassignment, migration, HA/failover, ext4/XFS
multi-writer use and arbitrary fence secrets/arguments remain unavailable.

`POST /api/v1/storage/cluster/credentials` delivers only one bounded secret to the selected master
through the existing authenticated one-time Core offer. Its request has a stable request ID, the
cluster request ID, pool/target scope, `chap`, `fence_bmc` or `fence_ssh` kind, and
`{username?,secret}` for CHAP/BMC, or `{username?,ssh_private_key,ssh_known_hosts}` for SSH
(each secret field at most 64 KiB). The response is only `{request_id,status:"accepted"}`.
The Controller zeroes the secret after one redemption/acknowledgement or the 15-second expiry and
persists neither it nor a receipt. Setup refers later only to the corresponding secret-free
credential request ID; Core resolves its own private alias and distributes peer material over
pool mTLS. A `503` acknowledgement timeout leaves delivery unknown: retry the same request ID.

`POST /api/v1/storage` keeps the v1 `{path}` local-directory form. Typed registrations select
`kind` and exactly one non-secret `source`: NFS `{server,export,directory?}`, existing mounted ZFS
`{pool,dataset,mount_path}`, SMB `{server,share,directory?}`, or iSCSI `{portal,target_iqn,lun,wwid,
filesystem_uuid,exclusive_target}`. Core allocates the repository ID and chooses the iSCSI
mount path under `/var/lib/nex-storage/<repository-id>`; client input must not supply it. It verifies the
source as its real QEMU identity, and reports canonical identity, capacity, access, capabilities
and generation. NFS uses a Core-owned hard NFSv4 mount; ZFS only recognizes an imported mounted
dataset and its GUID/health, then exposes its original `source.zfs.mount_path` through a
Core-owned bind alias at `/var/lib/nex-storage/<repository-id>` recorded as `StorageRegistration.path`.
VM disk references use that alias. Existing-dataset registration never changes ZFS properties and detach removes only the
alias; SMB uses an encrypted SMB3 mount with strict cache/locking; iSCSI
uses the stable WWID and an existing ext4/XFS filesystem. Registration never formats a LUN,
creates or imports a zpool, changes ZFS properties, creates a target/export, or configures
multipath. Each repository has one writable Core in this release; `shared` observation never
permits a second writer. Core rejects duplicate iSCSI target/portal sessions, including a
second LUN on the same session, and canonical WWID/filesystem aliases before initiator effects.
Generated mount paths do not distinguish ownership claims.

For a newly registered SMB repository, Core atomically creates a durable marker at the share
root and records its lowercase 32-hex `identity.smb.marker_id`. CIFS `filesystem_id` remains
diagnostic because it can change on reconnect; Core verifies the marker against the exact SMB
source and options before reuse. Existing legacy records without a marker are preserved without
auto-upgrade and fail closed under their prior strict filesystem-ID check.

For new NFS registrations, `identity.nfs.filesystem_id` is the NFSv4 kernel protocol profile
`nfs4:<server-address-hex>:<port-hex>:<fsid-major-hex>:<fsid-minor-hex>`, rather than a
`statfs` filesystem ID. Core binds the profile to the mounted path's exact `stat.Dev`, requires
one matching `/proc/fs/nfsfs/volumes` row, and separately verifies the requested NFS source and
hard NFSv4 mount options. A missing or ambiguous row fails closed; a zero protocol FSID is valid.
The initial server-address profile is part of the identity, so a changed address fails closed;
this release makes no NFS HA failover promise. Preserved older records that contain a `statfs`
identity are not rewritten and fail closed instead of being silently upgraded.

`POST /api/v1/storage/{id}/update`, `/recheck` and `/detach` carry a stable request ID, pool,
host and expected generation. Update changes typed configuration; recheck observes it, except that
recheck (and Core's one-minute periodic lane) starts an existing, unchanged Nex NFS/SMB mount unit
when systemd reports it inactive. Recheck also cycles a Nex-managed NFS/SMB unit when its mount
root reports a stale file handle and no disk, retained disk or ISO depends on it; a dependency is
named and nothing is remounted while it is in use. Verification then decides health; an unmounted
repository reports `remote storage is not mounted`;
detach is refused while disks, retained disks, unremoved media or active work depend on it and never
deletes backing data. HTTP `202` means Core work is queued. Retry an identical request ID to
recover an interrupted client reply; a changed replay or stale generation is `409`. For a
`source.iscsi.mode:"clustered_gfs2"` registration, these generic lifecycle mutations are
intentionally not a cluster lifecycle: every generic update, including a rename, becomes a failed
Core operation with `clustered iSCSI updates require a coordinated Core cluster operation`; generic
detach fails with `clustered iSCSI detach requires a coordinated Core cluster teardown` before a
native command. No coordinated update/teardown API exists in this slice. Recheck is observation
only and does not authorize execution; its only side effect is starting an inactive existing Nex
NFS/SMB mount unit, never rewriting, reloading, restarting or enabling it. Core blocks
disk, media and guest-start writes if its source identity has changed, a mount is missing,
read-only or inaccessible, or capacity is insufficient. It never writes into an unmounted
directory.

For non-clustered repositories, changing only a repository name is metadata-only: it preserves the existing Core credential and
does not reconnect the backend. Changing a source or detaching a repository uses the full
dependency guard. Detach also removes the Core-local secret for that repository when one is
attached (SMB password or iSCSI CHAP). Credential-only rotation replaces the attached receipt after every VM
using the repository is shut off and no pending or `needs_attention` disk, media, or ISO work
refers to it; the previous Core-local secret is retired when the new receipt is applied. If Core cannot prove rollback restored the previous backend state, it reports
`needs_attention`; it does not present that uncertainty as a safe failed update.

An identical Controller request replay only returns its original accepted operation; it does not
send a second storage command. Separately, Core's managed worker runs `ReconcileStorage` in its
periodic one-minute lane, outside heartbeat reporting and serialized with storage mutations: it
retries at most one due original `storage-*` journal entry in `pending` or `needs_attention`
state, retaining the original operation/result, and excludes definitive failures. This continues
while Controller is unavailable.

`POST /api/v1/storage/credentials` is administrator-session and CSRF protected. It accepts a
credential request ID, target pool/host, at least one of a new registration request ID or
existing repository ID, and `{username?,secret,domain?}`. Optional `domain` is the SMB workgroup or
AD domain used with the account. Controller retains the bytes only for the 15-second
bounded delivery window. The addressed authenticated Core redeems an opaque report offer,
persists its secret locally, then acknowledges a durable receipt; only then does the endpoint
return `201`. `503` means delivery is uncertain: retry the same request ID. Ordinary commands,
inventory, operations, diagnostics and Controller persistence contain only opaque receipts or
the redacted `credential_present` state. Older Cores remain protocol v1; an upgraded Core first
receives the `storage_repositories` v2 acknowledgement in its report response, persists that
fact, then advertises `storage-repositories-v1`.

After an interrupted credential request, `GET /api/v1/storage/credentials/{request_id}` with
the same pool/host and repository or registration-request scope returns Core-reported redacted
receipt metadata when delivery completed. It never returns the credential bytes.

`GET /api/v1/storage/credentials?pool_id=<pool>&host_id=<host>` lists Core-reported redacted
receipts currently eligible for cleanup. Eligibility is an observation, not a reservation:
`POST /api/v1/storage/credentials/{receipt}/retire` queues a Core-only command with
`{request_id,pool_id,host_id}`, and Core rechecks that the receipt has no active or pending
repository references before deleting its local secret. A refused cleanup changes nothing. A
retired receipt cannot restore or recreate a secret through request replay; no Controller table
or secret state is used for cleanup. Ordinary detach and credential rotation already retire the
attached secret on Core; this pair remains for support of leftovers that never bound to a
repository. The Controller UI does not expose unused-credential cleanup.

The package also installs a QEMU prepare hook that invokes the internal
`nex-core-agent storage-guard --state-dir <agent-state-dir>` (or
`nex-core-local storage-guard`), passing at most 1 MiB of domain XML on stdin. It only reads
the managed journal and native storage observations; it makes no libvirt calls and takes no
journal lock. It blocks managed disks when journal or repository validation is unavailable,
while leaving independent unknown appliance disks alone. This guard is a last admission check;
it does not prove fencing against an unmanaged initiator.

**Pool VLAN networks (changed contract, 2026-09-09):** `POST /api/v1/networks` also accepts
`kind: "vlan"` with `uplink` (host NIC or bond name, at most 10 characters) and `vlan`
(0–4094; 0 = untagged). `source` must be omitted; Controller derives the bridge name
`<uplink>v<vlan>` (for example `nex1v100`) before replay comparison, so identical retries
replay identically. A pool VLAN is master-owned desired state: the submitted `host_id` only
selects the pool, and the stored registration, the response and later reads carry the pool
master as `host_id` while members pull and materialize the bridge. Resending the identical
body — including the member `host_id` a client first sent — replays the same registration and
operation; it is never treated as a changed payload. The registration also carries
`generation` (the master-owned desired generation members reconcile against) and
`member_access` (the master's per-member observations of the applied bridge); both are absent
or zero on host-local registrations. `member_access[].observed_at` is when that member last
**evaluated** this network on its own host, not when it last sent a row. A member re-checks every
condition it can see without side effects — the uplink's existence and kind, whether it is a bond
member, whether the VLAN device is already claimed, whether the bridge name is taken — on every
pull pass, so a fix to any of those converges on the next pass and posts a fresh observation. A
failure the precheck cannot see is retried at most once an hour, and while the member is waiting
out that interval it leaves its previous row and `observed_at` in place rather than re-stamping a
result it did not re-check. So a failed member row whose `observed_at` stops advancing is a known
failure awaiting retry, not a new observation; the Controller's staleness gate requires
`error == ""` as well as freshness, so such a row is never treated as ready either way. Controller rejects mixed shapes (400) and uplinks the
host has not reported as a physical NIC or bond (409). A duplicate uplink/VLAN or plain registration of the same
derived bridge on that host is queued and refused by the host as a failed task (see
*Host-owned defaults and validation* below). The `network-register` command then creates the 802.1Q
sub-interface `<uplink>.<vlan>` and the bridge idempotently, persists an ifupdown stanza under
`/etc/network/interfaces.d/nex-<bridge>` (adding `source /etc/network/interfaces.d/*` to
`/etc/network/interfaces` when absent) and verifies the port is attached. It never modifies
the management bridge, host addresses or routes, and fails without changes when the uplink
is a bridge/VLAN device, the VLAN device already belongs to another bridge, or an untagged
request targets a NIC carrying host addresses. `NetworkRegistration` carries `uplink` and
`vlan`; `Inventory.nics[].kind` (`physical`, `bond`, `bridge`, `vlan`, `loopback`,
`virtual`) lets clients offer uplinks. The agent service needs write access to
`/etc/network`. Success proves the host bridge exists, not that the switch carries the VLAN.
Removing and renaming a pool VLAN are described next. See
[the plan](plans/2026-09-09-pool-vlan-networks.md).

**Pool network and ISO removal, and network rename (new contract, 2026-09-19).** API impact:
**new contract** — three Core command kinds (`network-remove`, `network-update`, `media-remove`)
carrying one new payload, two new client routes, a changed branch on an existing client route,
two new reported fields and a new capability. Peer routes keep their shapes but gained one
response: `GET /api/v1/pool/networks`, `GET /api/v1/pool/networks/{network_id}`,
`POST /api/v1/pool/networks/{network_id}/access`, `GET /api/v1/pool/media`,
`GET /api/v1/pool/media/{media_id}` and `POST /api/v1/pool/media/{media_id}/access` answer `503`
with the fixed body `pool resource list unavailable: managed journal is incomplete` when the
serving host cannot read its managed journal whole (an unreadable entry, or the 10 000-entry
retention cap). A member treats any non-200 as "remove nothing", so the converge pass fails and
nothing is torn down. The body names no file and carries nothing from the journal; the same
condition is visible on the serving host as its `failed` `managed-journal` check, which names the
unreadable entry, and that host refuses every managed operation while it lasts. A tombstone still
never travels on the peer wire — a member infers a removal from a resource's absence from an
authoritative master list, exactly as shared storage already does.

*Capability.* Core advertises `pool-resource-removal-v1` when it accepts the three kinds.

*Client routes* (administrator session, same HTTPS Origin, `X-CSRF-Token`; JSON rejects unknown
fields and bodies over 1 MiB):

| Route | Body | Target |
| --- | --- | --- |
| `DELETE /api/v1/networks/{id}` | `{request_id, pool_id, expected_generation}` | pool master |
| `PATCH /api/v1/networks/{id}` | the same plus `name` | pool master |
| `DELETE /api/v1/media/{id}` (succeeded import) | `{request_id}` | the reported owning host |

`host_id` is **not accepted** on any of them. For a pool VLAN the target is always the pool
master, resolved from the pool record (`master_host_id`), so a client-supplied host would be a
second opinion about who owns pool state; because unknown fields are rejected, sending one is
`400 invalid JSON request`. For an ISO the pool, owner and current generation all come from the
host-reported record, which is why that body carries nothing but `request_id`. The unchanged
failed/`needs_attention` discard branch of `DELETE /api/v1/media/{id}` still takes **no body**.

Acceptance is `202` with `{resource, resource_id, action, operation}`; a replay of the same
`request_id` with an identical body is `200` with the original operation and queues nothing, and
the same `request_id` with a different body is `409`. A lost reply is recovered by resending the
identical request, not by minting a new one.

*Network status codes and error texts:* `400 network, pool_id and stable request_id required`
(bad `{id}`, `request_id` or `pool_id`); `400 resource removal must not carry a name`;
`400 a network name must be nonblank, at most 200 UTF-8 bytes, without slashes or control
characters`; `401 administrator login required` / `session expired or invalid`;
`403 CSRF token required`; `404 network is not registered on this pool's master` (unknown,
another pool's, not yet succeeded, or an unreadable mirrored command); `409 pool master is
unavailable`; `409 host must be fresh, active, in the selected pool, and support managed-vm-v1`;
`409 the pool master does not support removing pool networks yet; upgrade Nex Core`;
`409 only pool VLAN networks can be removed` (also the rename refusal for a non-VLAN network —
one gate, one string); `409 network generation is no longer current; refresh and retry`;
`409 request_id already used with another payload` / `request_id already used for another
operation`; `500 storage unavailable` / `network operation failed`. A `PATCH` whose
`expected_generation` is absent or `0` never matches a live network's generation, so it is
refused by the generation check (409) rather than by shape validation.

*ISO status codes:* the discard branch is unchanged; for a succeeded import,
`409 the host that stores this ISO does not support removing imported ISOs yet; upgrade Nex Core` is answered
**before the body is read**, so an older client's bodyless call gets a useful answer;
`400 stable request_id required to remove an imported ISO`; `409 another media transfer is
active; retry shortly`; `409 this import is still running; wait for its task to finish`;
`409 host must be fresh, active, in the selected pool, and support managed-vm-v1`; `404 media not
found`; `500 ISO removal request storage failed`.

*Command payload.* `Command.resource_lifecycle` carries `ResourceLifecycleRequest`
(`resource` `network`|`media`, `action` `remove`|`update`, `resource_id`, `pool_id`, `host_id`,
`expected_generation` ≥ 1, `name` for a rename only), and `kind` is always
`resource + "-" + action`, so `media-update` cannot exist. Registration keeps the existing
`Command.network`/`Command.media` shape, whose resource ID Core reserves permanently; a lifecycle
change takes its own operation slot (`network-lifecycle:<id>:<operation>`,
`media-lifecycle:<id>:<operation>`), so a removal never collides with the registration that
created the resource. Replay is the existing `Execute` rule: the same operation ID with the same
command returns the stored result, and with different input is refused.

*Reported fields.* `NetworkRegistration` and `Media` gain `tombstone`, and `Media` gains
`generation` (zero reads as 1, as `NetworkRegistration.generation` already did). A tombstone row
in `Inventory.managed` carries only `id`, `pool_id`, `host_id` and the generation after the
removal. Controller deletes its mirror row for that reporting host only, so a member's tombstone
cannot delete the master's pool VLAN row, and nothing re-adopts the resource from an older
report. A fresh Controller connected to the master after a removal and a rename reproduces the
whole picture from the master's report: live rows come back with their Core IDs, names and
generations, and a tombstone row simply deletes nothing it never had.

*Where a refusal appears.* Core owns every refusal and its text; the Controller only relays.
A refusal reaches the operator as the operation's `failed` result — either as the host's
transport `Result` or, for a replacement Controller, through the inventory `ManagedOutcome`
(both admit the three kinds) — and is shown verbatim in Task history. The Core-owned texts are
`only pool VLAN networks can be removed`, `network generation is no longer current; refresh and
retry`, `ISO generation is no longer current; refresh and retry`, `network cannot be removed
while used by VM <uuid>`, `ISO cannot be removed while attached to VM <uuid>`, the pool-wide
`… on host <host>` variants, `pool VM inventory is incomplete; wait for every member to report
before removing this network|ISO`, `only the pool master may remove or rename a pool
network|ISO`, `bridge <b> was not created by Nex; remove it by hand`, `network still has attached
interfaces`, `pool network <id> changed uplink or VLAN; remove and re-add it`, `network is not
registered on this host`, `ISO is not imported on this host` and the unresolved-lifecycle guard
`network|media lifecycle operation is unresolved; inspect or reconcile it before retrying`.
A member's converge check carries three more Core-owned texts: `pool network <id> was removed on
this host and is listed again by the master; remove it on the master and add it again` and its
`pool ISO <id> …` variant (a removal on a host is permanent, so the ID can never be held again
there), and `pool network|ISO <id> cannot be removed on this host: <reason>`, where `<reason>` is
any refusal above plus `<bridge> exists but is not a bridge` and `managed journal is incomplete`.
An ambiguous timeout stays `dispatched` and is never turned into a success; teardown is
idempotent, so a re-run settles it rather than needing attention.

*Media file ownership.* `media-remove` unlinks the published file only when this host's live
`iso-import` record is its own import. An adopted record — a member's copy of a pool-shared ISO,
whose operation ID is the deterministic adoption ID — writes the tombstone and leaves the file to
the importing host, so a member never deletes the pool's single copy on shared storage.

*Master-side dependency check.* Before accepting a pool-wide removal the master reads each other
member's last published VM inventory (`state.Inventory[host].VMs`, replaced whole on every
publication) and refuses while any of those VMs names the network or ISO. A member that is **not
ready** still holds the resource: its last published snapshot keeps counting, because Core refuses
to record a snapshot from a host that is not ready. Only a *ready* member must additionally be at
the current membership generation, and a ready member that is behind gives
`pool VM inventory is incomplete; wait for every member to report before removing this
network|ISO`. This host's own VMs are covered by Core's local dependency check inside `Execute`,
so a single-host pool needs no published inventory at all.

*Member convergence.* Members converge on their ordinary member pull pass (the same probe tick as
shared storage, ten seconds on a packaged host). The pass tears nothing down unless the master's
list was **authoritative**: HTTP 200, a decodable body, an unexpired list context and every entry
belonging to this pool. Any failure, partial answer or foreign entry returns before the orphan
loop, so an unreachable or confused master never removes anything; a master that cannot read its
own managed journal whole answers `503` rather than a short list, and a member that cannot read
its own journal whole removes nothing either. Before any `Execute`, a member runs
`CheckNetworkRemoval` / `CheckMediaRemoval`, which share one implementation with the teardown, so
every predictable refusal (a VM dependency, a non-pool network, unproven bridge ownership, a
bridge that is not a bridge, an extra attached port, an incomplete journal) is reported in the
converge check and writes no journal entry. A teardown or apply that fails for an unexpected
native reason uses an operation ID that is deterministic within the UTC hour, so it records one
journal entry per hour and replays that stored result within the hour instead of writing one per
probe tick; the next hour's ID retries for real. A failed pool-VLAN `network-register` does not
reserve the network ID — the same registration may be retried under a later operation ID once the
member's uplink or trunk is fixed — while a `needs_attention` outcome stays reserved, as it does
for storage. A rename is
applied one generation at a time, so a member several renames behind never reports a generation it
did not apply.

*Version skew.* An older **master** does not advertise the capability, so the Controller refuses
with the upgrade text and queues nothing. An older **Controller** ignores the unknown `tombstone`
field and keeps a mirror row for a resource Core no longer has — the same exposure storage detach
already has, documented rather than engineered around. An older **member** keeps applying what the
master lists and never removes, so it silently retains a removed bridge or ISO file until it is
upgraded; the master cannot detect that. A newer member with an older master sees no removals at
all, so no orphan appears.

**Host-held pool state and Controller replacement (2026-09-09):** `POST /api/v1/agent/enroll`
from an already enrolled host targeting a *different* Controller URL or trust is a replacement:
the agent (`nex-core-agent enroll` or the local `POST /api/local/v1/enrollment`) takes a fresh
key and Controller credentials while preserving its Core-owned host and pool identities,
state directory and managed journal. Existing managed results are marked acknowledged;
ongoing Core recovery can publish a later result under its original operation ID. A
replacement Controller acknowledges unknown operation IDs without creating operations,
and adopts resources from fresh inventory. Acknowledging an unknown result does not
adopt its payload; resource identities and per-host outcomes come from Core inventory.
A known operation belonging to another host
is rejected. `--replace-host` is refused in this replacement flow. Only the same
Controller answers "already enrolled". Report bodies may be up to 16 MiB to carry the journal. API impact: changed
contract. `Inventory.nics[]` carries `master` (bridge/bond membership) and `parent` (VLAN raw
device). `Inventory.guest_networks[]` and the `GuestNetwork` schema are replaced by
`Inventory.managed`, a `ManagedState` object with `storage` (StorageRegistration), `networks`
(NetworkRegistration), `media` (Media) and `vms` (VMCreateSpec) lists, each at most 10000
entries and omitted when empty: every succeeded registration and VM definition from the Core
managed journal. Pool, host and resource IDs inside entries are host-owned and stable across
Controllers: a host's self-registered default entry carries its pool ID when the host knows it
(hosts enrolled with host-owned identity), otherwise its host ID as a placeholder until
adoption (see *Host-owned defaults and validation* below). Adoption records each entry under
the host's current pool and host, a no-op for hosts that presented their own identity at
enrollment. On each managed report Controller adopts, under
actor `system` with `request_id adopt-<kind>:<host>:<id>` (`storage-register`,
`network-register`, `iso-import`, `vm-create`), every entry it does not hold, reusing the ID
and recording the operation as already succeeded without dispatching a command. Entries that
duplicate an existing ID, storage path, network (kind, source) or VM name, and invalid entries,
are skipped. The default local storage **ISO** and the untagged default networks are
host-originated entries in that same journal (see *Host-owned defaults and validation* below),
so they reach a Controller only by this adoption path.
On the host, `vm-create` resolves storage, network and media references by
resource ID rather than by pool/host ID, so creation works after re-enrollment under a new
Controller.

**Adoption completeness (2026-09-10; API impact: changed contract, `Inventory.managed` and
`Inventory.checks`, protocol version unchanged, older Cores simply omit the new lists):**
`ManagedState` gains `appliances` (`Appliance`: the Controller appliance VMs the host
deployed, from the appliance engine's deploy journal under the nex-core-local data directory,
`/var/lib/nex/core-local/vmhost` by default; the standalone agent takes
`--appliance-journal-dir`), `outcomes` (`ManagedOutcome`: terminal managed journal entries, with the host's
operation ID, kind, resource ID, name, status
and error, plus two derived outcomes: a failed automatic start after `vm-create`, reported
under ID `<operation-id>:start` with kind `vm-power` and status `failed` for a confirmed
preflight refusal or `needs_attention` for an interrupted/unconfirmed native start, and received queued
work from the Core command journal. A non-executing orphaned **non-storage, non-snapshot** pending entry is
reported as `needs_attention` with `interrupted before completion; retry from a new request`.
Storage originals are different: Core's periodic recovery lane retries at most one due
`storage-*` entry per minute (see `ReconcileStorage` above). The host re-observes a
`vm-update` entry interrupted after its definition step at most once a minute and promotes it
to `succeeded` when the persistent domain matches. A remote-storage `vm-create` is deliberately
excluded from that generic promotion: the serialized Core recovery worker can only settle an
already-defined, observed **stopped** VM after current pool-owner, execution-admission and
storage checks. Its authenticated `define` peer check is read-only and compares the complete
original immutable VM specification (including pool, host, ID, revision and disks) with the
master's durable record. Recovery requires the explicit `spec_match:true` receipt: a mismatch,
invalid record, or missing receipt from an older master is refused before native observation and
remains `needs_attention`. It never recreates disks or a definition, replays the create, starts
the guest, deletes it, or changes a permit. A failed or unavailable revalidation remains
`needs_attention`; the Controller neither retries nor drives this recovery. The exact Core engine
outcome overrides stale transport-journal results; Controller updates its matching operation
from that inventory-reported `ManagedOutcome`, while a replacement Controller records an
unknown inventory outcome as passive `adopt-outcome:<host>:<operation_id>` history. This
is distinct from an unknown raw transport result, which is acknowledged without adoption) and `missing_vms` (IDs of `vms` entries whose persistent domain
`virsh list --all --uuid` no longer reports). Two host checks qualify the report:
`managed-journal` is `failed` when a journal file cannot be read — that file is skipped and
named in the error while every other entry is still reported, instead of the whole state
collapsing to empty — and `managed-vms` is `failed` naming the missing VMs, or `unknown` when
libvirt did not answer. Before reporting, the host re-observes `vm-create` entries an
interruption left in `needs_attention` after the definition step (at most once a minute) and
promotes them to `succeeded` when the persistent domain matches the journaled definition, so a
replaced Controller, which never re-sends the operation, still adopts the VM. On the
Controller, each managed report also: inserts the `appliances` row for a reported appliance
the healthy libvirt observation lists (keyed by VM UUID, moved to the reporting host when the
previous owner is revoked, audit `appliance.adopt`); records each outcome it does not hold as
an operation with the host's operation ID, kind, status and error (actor `system`,
`request_id adopt-outcome:<host>:<operation_id>`, audit `adopt.outcome`) and updates an
already-held nonterminal operation directly from Core's reported outcome; and never skips an
entry silently — an entry already held under the same ID and kind is current, and any other
entry it cannot adopt (invalid, conflicting ID/path/source/name, references not adopted on
the host, missing from libvirt, or an appliance the host does not observe) leaves one
`needs_attention` operation per host and entry (`request_id adopt-attention:<host>:<kind>:<id>`,
actor `system`, error prefixed `not adopted from host report:`; appliance records use kind
`vm-create` with a `Controller appliance:` prefix) and an audit entry `adopt.attention` naming
the reason. That record is marked `succeeded` once the entry is adopted on a later report.
Compatibility: an upgraded Core that reports `managed.appliances`, `outcomes` or
`missing_vms` to an older Controller is rejected with 400 on every report (shared upgrade rule).
Replacing a Controller means installing a new one and connecting each pool master (token, or
address plus root password); storage, networks, ISO records and VM definitions return with their
IDs and names. Controller-scoped data is not recovered: sites, administrator accounts and roles,
audit history, enrollment tokens and the Controller CA.
`iso-import` without `media.url` fetches only assigned media from the enrolled Controller over host mTLS;
redirects and another host's media are refused. Controller stages a raw upload bounded by available staging free space minus a 256 MiB
reserve, computes its size/SHA-256 and checks its ISO descriptor. There is no fixed
per-image GiB cap. Core checks size, hash and descriptor again before publishing
read-only media. See the OpenAPI operations for admission errors.

`POST /api/v1/media/download` accepts JSON `{request_id, storage_id, name, url}` with
administrator authentication, CSRF and same-origin protection. The selected verified storage
chooses the host and pool; a fresh host must advertise `iso-download-v1` in addition to
`managed-vm-v1`. Controller queues `iso-import` with `media.url`, zero size and empty hash.
Core performs the public HTTP(S) download, checks DNS and redirect destinations against
non-public addresses, omits Controller credentials and environment proxies, bounds bytes by
storage capacity, validates ISO9660 and records computed size/SHA-256 in its durable journal.
Controller never downloads or stages these URL bytes. URLs are limited to 4096 characters and
cannot contain credentials or fragments; client responses and applied inventory omit them.
Identical request retries return the same operation; changed payloads conflict. Poll media and
operations for completion: initial zero size and empty hash become host-reported metadata.
While a URL import is actively running, Core offers `iso-download-progress-v1` in a v2
report. Controller responds with `iso_download_progress: true`; until that acknowledgement,
Core withholds the new field so an older strict Controller can keep accepting reports.
Core then reports `Inventory.managed.iso_downloads[]`
with the assigned `operation_id`, `received_bytes`, and `total_bytes` when the public server
sent a positive `Content-Length`. Core samples the transfer in its own journal at most once
per second and sends the latest sample in its ordinary authenticated report. Controller
validates the report against that host's dispatched URL `iso-import` and exposes it only as
the latest host observation in `GET /api/v1/hosts[].inventory.managed.iso_downloads`.
An operation unknown to a replacement Controller has no task row to attach progress to;
Controller ignores that progress while accepting the report and later adopts a completed ISO.
The browser shows a percentage only with a known total; without one it shows received bytes
and an indeterminate bar. A stale host report is not live progress. A terminal or interrupted
Core import omits the entry on its next report; task status still decides whether the ISO is
usable. No URL or credentials enter the progress report, and Controller does not drive the
transfer. This is an additive Core report and client inventory contract. Readiness resets on
disconnect, Core restart and Controller replacement; an older Controller receives no unknown
progress field.
Older hosts reject admission and never receive URL commands. Core journal recovery handles
Controller outages and ambiguous interrupted imports require attention. A replacement Controller
recovers completed ISO identity, name, storage, size and hash from managed inventory.

`vm-create` preserves pool membership separately from host placement, allocates a stable
UUID and requires successful registrations/media for the same host and pool. Core rechecks
storage, networking, capacity and firmware, then creates every disk and defines a stopped
guest through system libvirt; only `start_after_create` makes it start that guest, once, after
the definition is verified. The shared host engine's
`NewManaged(stateDir)` selects its private journal;
`Execute(ctx, command, fetch)` validates scope and payload, serializes effects and returns
the durable `Result` separately from journal/persistence errors. The agent supplies the
authenticated staging fetcher; Core constructs a separate public URL transport. Only hosts advertising
`iso-download-v1` receive the optional public `media.url` source.
The existing VM list still includes adopted Controller appliances with
`role: controller` and `protected: true`; generic guests add `spec`, `operation_id`,
`status` and optional `error`, with `role: guest`. Creation status never substitutes for
fresh libvirt execution state.

**Automatic I/O defaults (existing contract, 2026-09-24):** `vm-create` and stopped
`vm-update` definitions use one shared IOThread (ID 1) for VirtIO disks and the VirtIO SCSI
controller; SATA disks and CD drives remain unthreaded. VirtIO NICs offer `min(cpu.count, 4)`
queues, omitting the override for one queue and leaving libvirt's backend selection intact.
There are no new request or inventory fields. The assigned Core records the exact expected
XML in its managed journal `Domain`, applies it through libvirt and verifies the observed
thread assignments and queue counts. The pool master's existing VM specification and adoption
path remain unchanged; Controller replacement does not regenerate the VM's devices.

Running updates preserve existing disk, SCSI controller and NIC tuning. New VirtIO disks and
new SCSI controllers receive the IOThread default; a new SCSI disk on an existing controller
inherits that controller's configuration. Core observes both live and persistent thread state
before attaching a device that references it. New NICs receive the queue default. Interrupted
operations retain the existing observation-before-retry semantics and do not count partially
applied changes as success. A stopped save applies the full defaults to an older definition;
agent upgrade, autostart and guest reboot alone do not. Older Core retains its prior defaults.
Authentication, authorization, idempotency and wire schemas are unchanged. Queue exposure does
not establish guest driver activation or improved workload performance; see
[validation evidence](qa/2026-09-24-automatic-vm-io.md).

API impact: **existing contract** — a terminal Core `vm-create` failure before a definition is
created remains task, audit and idempotent-replay data under an internal failed-create identity,
so it cannot collide with a Core VM identity. It is excluded from Controller VM-list, ownership,
placement and adoption-conflict queries; historical adoption relocates a conflicting failed row
before storing Core's succeeded definition. No route, request, response, authorization or version
changes.

**Full VM specification (changed contract, 2026-09-10):** `POST /api/v1/vms`, the `vm-create`
command payload and the VM list/report inventory carry `VMCreateSpec` in its complete shape
([plan](plans/2026-09-10-vm-creation-system.md)). Bounds below are enforced identically on
Controller and Core by `protocol.ValidateVMSpec`; existence of referenced resources, pool-wide
MAC uniqueness and host capacity stay with their owners.

- **Identity.** `name` (nonblank, at most 200 UTF-8 bytes; spaces, mixed case, dots,
  punctuation and Unicode are accepted, except `/`, control characters and XML-invalid
  U+FFFE/U+FFFF; uniqueness is the host's check, see *Host-owned defaults and validation*), `description` at most 4096 characters, and `guest_os` (`""`, `linux`,
  `windows`, `other`) recorded for display and guest-tools advice. It installs nothing and does
  not change the VM's devices: disk bus and NIC model are chosen per device.
  Names are preserved exactly in the Core managed journal and libvirt definition, including
  surrounding spaces; UUIDs remain the identity for operations and disk paths. Create/update
  reject invalid names with 400; Core validates the same rule before effects. Inventory adoption
  and refresh retain the host's name. Authentication, authorization, request replay and async
  results are unchanged. Upgrade Controller and Core together: older versions reject names
  outside the former hostname syntax; protocol v1 and payload fields are unchanged.
- **`cpu`.** `count` at least 1; Controller caps it at 64 and at host capacity. `sockets`,
  `cores` and `threads` all `0` select the documented default topology — one socket, `count`
  cores, one thread — instead of libvirt's own default; otherwise each must be at least 1 and
  sockets × cores × threads must equal `count`. For model-only requests, `model` is `host-passthrough`,
  `host-model`, or a model from the assigned host's read-only `Inventory.cpu_models` list. Core
  obtains that list from libvirt domain capabilities (`usable=yes` named models on the
  host's default q35 machine), rechecks a model-only selection immediately before
  defining the VM, and writes it as an exact custom model with partial feature checking
  and fallback forbidden (not `check=full`, which refuses usable models under KVM). `max` uses libvirt's
  maximum CPU mode. Host modes follow the host. New drafts use the reported pool baseline
  when available, freezing its model and optional `features` in the VM definition. Native
  feature comparison, rather than model-name membership alone, validates explicit baselines.
  Current-member compatibility is not a future-host or migration guarantee.
- **`memory`.** `size_mib` at least 512, capped by Controller at 262144, is the ceiling in both
  modes. `dedicated` reserves it: `<memory>`/`<currentMemory>` at the ceiling, no balloon device,
  and `<memoryBacking>` with `<allocation mode="immediate"/>`, `<locked/>`, `<nosharepages/>`.
  Core verifies the persistent backing and current allocation after definition. Physical RAM
  is allocated before execution and held without swapping or KSM sharing while QEMU runs;
  stopped VMs retain only their Nex capacity commitment. QEMU overhead is locked too; no guessed
  process hard limit is imposed. Existing VMs need updated Core, a full shutdown and a successful
  stopped `vm-update` save before starting. Legacy dedicated live edits are refused before device
  effects with a stop-and-save error. Agent upgrade, autostart or guest reboot alone does not
  rewrite existing definitions. These are existing request/result contracts, with unchanged
  authentication and retry handling; older Core cannot enforce this physical reservation. `maximum`
  caps without reserving: the same ceiling plus a virtio balloon with free page reporting, so the
  guest returns pages it frees. Reclaim needs the balloon driver running in the guest (built into
  Linux; virtio-win guest tools on Windows) — without it a guest drifts to its ceiling and holds
  it, and this slice has no pressure-driven reclaim daemon. Creation and stopped `vm-update`
  check CPU, KVM, machine capabilities and the total-memory dedicated promise, not memory free
  right now. Core checks current available memory (`MemAvailable` at least the ceiling plus the
  2 GiB host reserve, in both modes) only when it boots a shut-off VM — `vm-power` start,
  `start_after_create` and Core-coordinated pool autostart — and refuses with `insufficient
  available memory to start this VM including 2 GiB host reserve` before issuing `virsh start`.
  A start of a running VM stays a no-op; a refused pool autostart is not retried in that boot.
  A created VM whose automatic start is refused stays
  defined and stopped, with `VM created but not started: <reason>` reported. Managed pool VMs
  keep native libvirt autostart disabled; Core's once-per-boot autostart applies the same check.
  API impact: **changed
  contract** — same requests and results; the available-memory refusal moved from `vm-create`
  and stopped `vm-update` to starting, with the message above (BUG-0195).
- **`disks`.** One to eight. Each carries `storage_id`, a `name` unique within the VM
  (`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`; a name, not a path), `size_gib` 1–2048, `bus`
  `virtio|sata|scsi`, `thin` (`true` sparse, `false` preallocated) and `boot_order`. Disks are
  created inside the VM's exclusive directory on their storage target; Core checks exclusive
  ownership for every disk and free space per target, summing the disks that share one.
  Existing ownership checks refuse foreign image chains with external data files or encryption,
  including metadata omitted from domain XML, because exclusive access cannot be established.
  Managed chain checks also require the exact unique writable disk targets and sources from
  the journal; extra or duplicate writable disks are refused before native mutation.
- **`nics`.** Zero to eight. Each carries `network_id`, `model`, `mac` and an optional `mtu`.
  `model` is `virtio|e1000|e1000e|rtl8139|vmxnet3` (`virtio` needs a guest driver). `mtu` is
  `0` for the network's own MTU and is **accepted only on `virtio`** (via `host_mtu`; other models
  refuse a value). A set `mtu` is between 1280 and 9216. An
  empty `mac` means the derived address (`protocol.DerivedMAC`: the `52:54:00` locally
  administered unicast prefix, two octets of `sha256(vm id)` and the interface index), which is
  stable per VM and interface and cannot collide within a VM (on an edit, see
  [VM editing](#vm-editing) for how empty MACs are matched). A manual `mac` must be six octets
  in canonical lower-case colon form, unicast and not all zero; that canonical form is what lets
  the host check uniqueness against its journal and domain definitions by plain string comparison.
- **Boot order.** `0` means "not a boot device". Non-zero values are unique across every disk and
  `boot.media_boot_order`; gaps are allowed, and at least one boot device is required. Every VM
  has the CD drive, so `media_boot_order` does not require `media_id`: an empty drive keeps its
  place in the boot order, which is what lets a running guest eject. The install ISO must be
  imported and verified on the placement host; it may live on any storage that host can read and
  does not need to share a repository with the VM's disks. Order is per device because libvirt
  forbids mixing
  per-device boot with an `<os><boot dev>` list.
- **`boot`.** `firmware` `uefi` (the default; requires EFI firmware advertised by the host's
  `domcapabilities`) or `bios`. `machine` is empty for Core's default (q35) or a plain machine
  name (`^[a-z0-9][a-z0-9._-]{0,31}$`).
- **Lifecycle.** Managed pool VMs keep native libvirt autostart disabled, including on local
  storage. The desired `autostart` setting remains in Core's VM definition. Core disables and
  verifies the native flag when defining or updating a VM and during its autostart pass; the
  Controller appliance uses its separate lifecycle. Core's worker makes at most one durable
  autostart attempt per VM per OS boot, recorded before observation or admission, and only on
  a host that is neither drained nor restricted. A guest must be shut off and receive fresh
  current-owner admission from the pool master. Shared storage additionally requires current
  storage/execution checks and a one-use Core permit. Pending recovery refuses admission.
  Agent restart, a later manual stop, master outage, failed validation or a failed start cannot
  create another attempt in that boot; this is neither HA nor failover. Existing definitions
  with native autostart still enabled must have it disabled and verified before snapshot
  effects; an agent-side admission check alone cannot intercept libvirt's earlier boot replay.
  `start_after_create` is
  honoured by the host inside the same creation (changed contract, 2026-09-10): after every disk
  is created and the persistent definition is verified, the creation has **succeeded** whether
  or not the flag is set. Core then persists stage `starting`, issues the same single libvirt
  start `vm-power` uses and records the outcome (`started`, or the reason) in the entry's
  `start` field. That field is what makes the start happen once: a replayed command returns
  the durable result and never starts again, and a creation interrupted at the start step is
  reconciled by observing libvirt — `running` records `started`, anything else records the
  uncertain outcome `interrupted before the automatic start completed`. A start failure
  leaves the creation `succeeded` and the VM defined with its disks; a replacement Controller
  adopts that definition without treating creation success as proof of a start. The
  host reports the failed start as its own outcome with ID `<operation-id>:start`, kind
  `vm-power`, status `failed` for a preflight refusal or `needs_attention` for an interrupted
  or unconfirmed native request, and error `VM created but not started: <reason>`, which the
  Controller records in Task history next to the succeeded creation; a failed or uncertain
  creation never boots. Controller issues no follow-on `vm-power` operation and no longer
  needs `vm-power-v1` for this; the operator sees the creation task and, only when the
  automatic start failed or remains uncertain, one separate start task. The creation callback
  and owner inventory read this exact durable start outcome before settling its master
  reservation; uncertain starts remain fenced even if the latest observation is shut off.

`createVM` refusals, in the order they are evaluated: **400** for a client-supplied `id`, an
invalid `request_id` or name, any `protocol.ValidateVMSpec` violation (its message is returned
verbatim) and the Controller ceilings `cpu.count > 64` or `memory.size_mib > 262144`; **409** for
a replay whose payload changed, a host outside the named pool, a stale or revoked host, and
capacity above the host's observed
CPU or memory; **404** for any `disks[].storage_id`, `nics[].network_id` or non-empty `media_id`
that is not a registration this pool and host have verified. A **200** replay returns the
original VM and operation; a new request returns **202**. Name and MAC uniqueness and
dedicated-memory admission are not HTTP refusals: the host decides them and reports a failed
task (next section).

**Host-owned defaults and validation (changed contract, 2026-09-10):** Nex Core owns its default
resources and every admission decision its own record can answer; Controller only mirrors and
displays what the host reports. API impact: **changed contract** for `storage-register`,
`network-register` and `vm-create` results and for the corresponding client refusals; protocol
version and capabilities are unchanged.

- *Defaults are host-originated.* At agent start, and whenever a new bridge on a physical NIC or
  bond appears (checked once a minute from the same interface observation the inventory probe
  reports), Core self-registers `/var/lib/libvirt/images` as storage **ISO** and `<port> untagged`
  (kind `bridge`, `source` the bridge, `uplink` the port) for each such bridge, in its managed
  journal with host-generated IDs, through the same verification a Controller-issued
  registration gets. A journal entry that already holds that path or bridge — in any state,
  whoever issued it — suppresses the default, so a host that received Controller-issued defaults
  before this change gets no duplicates. Host-originated entries carry the host's pool ID
  when the host knows it (hosts enrolled with host-owned identity) and its `host_id`; a legacy
  host without a pool ID uses its host ID as the `pool_id` placeholder until adoption records
  the entry under its current pool, like every other journal entry.
  Controller never queues `auto-storage`/`auto-network` registrations any more.
- *Validation errors are failed results.* Core refuses, as a `failed` result with an
  operator-readable `error`: a storage path another non-failed registration on the host holds
  (`storage path <path> is already registered on this host as <name>`); a bridge (shared by
  kind `bridge` and `vlan`) or libvirt network another non-failed registration holds
  (`network <source> is already registered on this host as <name>`); a VM name another domain
  or non-failed `vm-create` entry holds; an effective MAC, manual or derived, that any domain
  definition or non-failed `vm-create` entry holds (`MAC address <mac> is already used by VM
  <name> on this host`); and dedicated memory that, with the dedicated memory already promised
  by every non-failed `vm-create` entry plus the 2 GiB host reserve, exceeds `MemTotal`
  (`dedicated memory exceeds what this host has left to promise; free a dedicated VM or use the
  maximum memory mode`). A failed registration or creation releases what it claimed. Controller
  no longer refuses these itself (the former 409s for a registered path/bridge, a reserved VM
  name, a used MAC and the dedicated-memory promise are gone); it records the host's result on
  the operation, and the existing operation views show it. Uniqueness is checked on the VM's
  host; a pool has one host until member joins exist, so this is the pool today.
- *Compatibility.* A Core older than this change does not
  self-register defaults, so a new Controller managing it would show no **ISO** storage or
  untagged network until the host is upgraded; and it does not honour `start_after_create`
  itself, so a VM created with that flag through such a host stays stopped. Both are accepted
  (decision: project owner, 2026-09-10); upgrade the host. An older Controller against a new Core keeps
  issuing its own defaults, which the host refuses as already registered.

API impact: **changed contract**. The VM creation request, the `vm-create` command payload and
the VM list/report inventory (`ManagedAppliance.spec`, `Inventory.managed.vms`) all carry the new
specification. Authentication, authorization, CSRF and origin checks, durable operations, replay
by `request_id`, result acknowledgement and audit attribution are the **existing contract** and
do not change; protocol version stays `1` and the capability stays `managed-vm-v1`. There is
**no compatibility shim**, because Nex is unreleased: no decode-only legacy fields, no
`managed-vm-v2` capability gate and no journal normalisation. Recreate VMs and managed journal
entries written in the earlier flat shape. The
Controller appliance is unaffected — it is built from `DeploySpec` through `makeDomain`, a
separate path from `VMCreateSpec`.

Every field of the specification is pool-scoped state, so its durable host-side record is the
**managed journal on the pool master**: each `vm-create` entry persists the full specification,
the generated domain XML and the path of every disk it created. Cores report succeeded entries in
`Inventory.managed.vms`, and a replaced Controller re-adopts them by resource ID (actor `system`,
`request_id adopt-vm:<host>:<id>`) with the VM's identity, name and shape intact and no
command redelivered; storage, network and media references resolve by resource ID on the host for
the same reason. No part of a VM definition exists only in the Controller database.

Core-reported historical outcomes are also adopted through the existing operation list, with
`request_id` exactly `adopt-outcome:<host-id>:<operation-id>`. They are passive history, never
redelivered commands. The Controller records its re-observation time because Core does not report
the original outcome time; task history labels those rows **Reobserved from Core history** rather
than presenting that timestamp as when the outcome occurred. This is an existing request-ID
contract and changes no Core report field or durable state.

Clients retain a stable `request_id` and exact payload. Replays return the original identity
and operation; changed payloads return 409. Upload replay rehashes the supplied bytes and
compares actual size/hash as well as storage and filename. The agent persists commands before
effects, executes mutations serially without blocking heartbeats, and journals results until
Controller acknowledges them. Restart reconciles original ownership instead of creating a
replacement. `needs_attention` is a terminal result for ambiguous effects that require
inspection; it does not authorize another instance or imply that a disk is safe to delete.
Definite failed storage/network checks may be resubmitted with a new request identity after
the cause is corrected; active or uncertain registrations retain their reservation.

There is no cancellation, deletion or automatic cleanup of uncertain Core disks in this
development workflow; central power requests and consoles have their own sections below. Controller staging is released after terminal
import results and orphaned upload files are cleaned at startup. Running VMs and local
recovery remain independent of Controller availability. The
[operator workflow](guide/create-virtual-machine.md) records availability and verification limits.

## Central VM power requests

API impact: **changed contract** — `POST /api/v1/vms/{id}/power`, `vm-power`,
`VMPowerRequest.after_operation_id`, `vm-power-queue-v1`, and managed task outcome adoption.

An administrator posts `{request_id, action}` with `start`, `shutdown`, `reboot`,
`force-reboot` or `force-stop`. Authentication, same-Origin/CSRF, fresh assigned host, pool and capability
checks are unchanged. `request_id` replay returns the original operation; changed input
under that ID is 409. With `vm-power-queue-v1`, another pending power, edit or media task
no longer causes a busy refusal: Controller sends the latest pending operation ID as
`after_operation_id` and returns 202. Older hosts retain the single-pending-request guard
and its 409 upgrade message; requests carrying a prerequisite are withheld if a host
stops advertising queue support. The browser closes confirmation after acceptance and
shows the queued task; it does not retry in a loop. A `start` that would boot a shut-off VM
fails before `virsh start` when the host's available memory cannot meet the VM's ceiling plus
the 2 GiB host reserve (see `memory` under VM creation); a start of a running VM is a no-op.

Controller dispatch preserves database insertion order, independent of wall-clock changes.
Core persists each command and a monotonic admission order in its private agent `state.json`
journal before running it through the existing single managed worker. Repeated delivery keeps
the same position. A power request without an explicit prerequisite also records the latest
pending same-VM operation locally, covering new requests after Controller replacement.
Core checks that the prerequisite's managed journal belongs to the same VM and succeeded;
missing, failed or uncertain prerequisites fail the power task before any effects. No
prerequisite IDs or unsafe failures are guessed or blindly retried.

Once Core has received the command, closing the browser, losing Controller connectivity,
restarting the agent or replacing Controller does not discard it. Enrollment retains managed
queue entries and their order, marks old transport results acknowledged, and reports queued
and terminal tasks through `Inventory.managed.outcomes`. A replacement Controller adopts
these as passive `adopt-outcome:<host>:<operation>` rows and follows Core's reported status;
it never redispatches adopted tasks. Terminal adopted results do not regress to queued.
Before Core receives a request, Controller acceptance is delivery-pending, not proof of
host persistence. Outcome adoption batches database lookups and accepts at most 30,000 outcomes (execution
entries, derived automatic-start results and additional queued commands). The 16 MiB report
body limit still applies. The existing 10,000-entry journal ceiling still requires manual recovery
when full; no automatic pruning or unbounded retry loop is added.

Core journals the stage before its single libvirt request. Start and force-stop are
idempotent against observed state; shutdown and reboot are ACPI requests; `force-reboot` is a
hard guest reset (`virsh reset`) and is accepted only while the VM is running. **Queue order
waits for earlier task completion, not guest shutdown:** an ACPI task can succeed while the
guest remains running. Check observed state before requesting a subsequent state change.
Paused or transitional states are refused as task errors. Interrupted requests reconcile
by observation only, never by blindly issuing another power effect; ambiguous outcomes

**Guest sleep (changed contract, BUG-0273, 2026-09-17).** Every Nex domain definition carries
`<pm><suspend-to-mem enabled="no"/><suspend-to-disk enabled="no"/></pm>`, which removes ACPI
S3 and S4 from the guest firmware tables so the guest offers no Sleep or Hibernate (the
Hyper-V approach; VMware's guidance is to disable guest power management). Existing VMs pick
this up when their definition is next written (an edit) and it takes effect at the next boot.
A domain that nevertheless reports `pmsuspended` (a guest that slept before this change, or
one that circumvented the tables) is an active execution: `ManagedVMState` observes it as
`running` for pool inventory and ownership, so one sleeping guest no longer fails the host's
inventory publication and blocks every other VM on the host. Power requests on it: `start`
issues `virsh dompmwakeup` instead of `start`; `shutdown` and `reboot` wake it first and then
send the ACPI request (a failed wake is `needs_attention`, as for any effect that crossed into
libvirt); `force-stop` and `force-reboot` act on the process directly. The console, VM edits
and deletion refuse a sleeping VM with a message naming Start as the way out. The private
Core-to-master execution admission gains a `wake` action beside `reboot`: it requires the
owner's `running` record, issues no start permit and changes no state or epoch, so a wake can
never admit a second execution; an older master rejects it as unsupported (fail-closed).
`GET /api/v1/vms` still passes the raw observed state `pmsuspended` through unchanged; the UI
names it "asleep" and offers Start.
remain `needs_attention`, including after `force-reboot` when observation cannot confirm the
reset completed (same interrupt ambiguity as reboot). A libvirt power-call error or timeout after the request could have
taken effect is also uncertain, including when resuming a command before its first effect.

API impact: **existing power contract** — for an exact locally managed, journal-owned pool VM,
`shutdown` and `force-stop` may proceed when live clustered-storage readiness or pool-peer
admission is unavailable. The reducing-only exception still requires the normal authenticated,
authorized power request, `ValidateManagedCommand`, and exact VM ID, pool ID and host ID matching
the local Core record. It retains the local journal, native validation and native observation; it
does not create a start permit or modify master ownership/placement state. It never applies to
start, reboot, force-reboot, autostart, create, update or media changes, all of which remain fail-closed.
No successful result is a claim that the guest is stopped: a frozen GFS2 path can still make the
native shutdown or destroy block or fail. On error or ambiguous outcome, Core retains the journal
result and a fresh reconnect inventory is authoritative; it must not infer a stop, transfer
ownership or retry a power-on action.
Preflight refusals remain failed. Same-command Core reconciliation observes state without
issuing the effect again; Controller request-ID replay alone does not schedule that reconciliation.
Genuine task failures require inspection before a new request.

Evidence: [power queue QA](qa/power-queue/checks.md), [console QA](qa/console/checks.md).

## VM editing

API impact: **new contract** — `updateVM` (`PUT /api/v1/vms/{id}`), the `vm-update` command
carrying the full `VMCreateSpec` at the next `revision`, the `vm-update-v1` host capability and
the `vm-update` operation/audit kind; **changed contract** — `VMCreateSpec` gains `revision`
(0 at creation), the VM list reports every interface's effective MAC, `boot.media_boot_order`
no longer requires `media_id`, and `Inventory.managed.vms` carries each VM's *current*
definition. Schemas, statuses and limits are in the generated reference.

Flow: an administrator puts the complete desired definition with a stable `request_id`.
Controller validates it exactly like creation against the pool and host minus the VM itself
(shape, references and observed capacity; name and MAC uniqueness against other VMs and dedicated-memory admission stay the host's, as for creation), refuses what no
edit may change (an existing disk's storage, size or provisioning; moving the VM) and, when
the VM is observed running, what needs it stopped. Interfaces are matched by effective MAC:
- when the request names **any** current interface by its effective MAC (the address the VM list
  reports; the bundled editor sends it for every existing interface), every interface with an
  empty `mac` is **new**. A client that sends one existing interface's MAC must send all of them,
  or an omitted interface is removed and replaced by a new one with a fresh address;
- when it names **none**, an empty `mac` is matched by position: an unchanged interface keeps the
  current address at its index and one appended past the current count is new, but removing an
  interface, or changing the network, model or MTU at an existing index, answers `400` "when
  removing, reordering or changing a network interface, give every existing interface its MAC
  address".

Controller then freezes every automatic MAC into the definition, drawing each new one from the
VM's derived range at the first index above the highest one the current definition uses that no
requested or current interface holds, or, when none above is free, the lowest free index (so a new
interface never inherits the address of one removed in the same edit, nor, unless the
highest-index interface was removed or index 255 is in use, one removed in an earlier edit), runs its stop-first check
on that frozen definition, the same one Core checks, and queues `vm-update` at revision current + 1. One edit, power or media request per VM
is in flight at a time; a `vm-media` change updates the same current journal entry. Core classifies the difference from its journal with the shared
`NeedsStop` rule: name, CPU, memory, firmware, machine type, any boot order, an existing
disk's bus, an existing interface's network, model or MTU, and adding a SATA disk need a
shut-off guest, which is then redefined from the regenerated XML after new disks are allocated
and a `domrename`; a running guest takes disk and interface additions and removals
(`attach-device`/`detach-device --persistent`; a live detach completes only when the guest
releases the device, so a removal succeeds once persisted and the guest may keep the device
until its next restart; while validating a running guest's edit, before allocating any disk or
changing any device, Core compares the live and persistent domains and reports the `vm-update`
`failed` with "network interface MAC <mac> is still attached to the running VM; wait for the guest
to release it or stop the VM, then save again" when an interface in the request has a MAC that is
still on a live interface the persistent definition no longer has; nothing changes, no disk is
allocated and the current revision stays current), ISO insert, swap or eject
(`change-media --live --config`), description, guest OS hint and autostart. Disks are matched
by name and keep the target names journaled at creation; interfaces by MAC, so a request that
changes an existing interface's MAC removes that interface and adds a new one, which a running
guest accepts rather than refusing. Every VM now
carries one SATA CD drive, empty without an ISO, and q35 definitions carry spare PCIe root
ports for hot-plug. Removed disk files are detached and retained, never deleted. The
persistent definition and per-disk ownership are observed against the expected XML by device
identity; an interrupted edit is reconciled by observation and otherwise reported as
`needs_attention`. The exception is a UEFI-to-BIOS change interrupted after its `undefine --nvram`
(journal stage `cleaning-nvram` or `defining`): Core confirms the domain is absent and defines the
journaled BIOS XML when the same `vm-update` operation is delivered again (Controller redelivers
only `queued` or `dispatched` operations) or, for a VM on shared storage, at the periodic remote
update reconcile. A local-storage edit already reported `needs_attention` with the domain absent
is only observed by background recovery, not finished, and needs inspection on its host. Core refuses with `VM definition changed during
firmware cleanup; inspect the VM on its host` rather than undefining a UEFI domain found there. A
stopped edit that renames the VM and changes Secure Boot runs the read-only Secure Boot checks
before `domrename`, so a refusal leaves the name unchanged, then edits the variable store and
defines under the new name; a failed read after the rename reports `cannot read VM firmware
variable store after rename`. The host's journal keeps every revision and reports the highest succeeded
one as the current definition; Controller refreshes its mirror from each report, so the
result is the host's word and a replaced Controller adopts the current definition and
revision. Older Core without `vm-update-v1` is refused with 409 at request time.

Evidence: [edit QA report](qa/vm-edit/checks.md).

### VM tags

API impact: **changed contract** — `VMCreateSpec` gains optional `tags` (JSON array of strings,
omitted when empty), carried by `createVM`, `updateVM`, the `vm-create`/`vm-update` commands, the
pool master's peer VM definition and `Inventory.managed.vms`, and returned in `spec.tags` on
`GET /api/v1/vms`; new host capability `vm-tags-v1`.

Each tag is 1 to 32 lowercase letters, digits or hyphens, at most 16 per VM. Controller sorts and
de-duplicates the list before validating it; `protocol.ValidateVMSpec`, which Controller and Core
both run, answers `400` `tag "<tag>" must be 1 to 32 lowercase letters, digits or hyphens`,
`a VM allows at most 16 tags` or (Core only, for an undeduplicated list) `tag "<tag>" is repeated`.
Tags are part of the complete definition: `updateVM` without `tags` clears them, so a client
editing other fields sends the current `spec.tags` back. They change no guest device and apply
to a running VM like the description. The usual `request_id` replay and `vm-update` operation
semantics apply unchanged.

Durable state is Core's: the host journals tags with each revision, the pool master keeps them
in its pool VM record (`Spec`), and a fresh Controller adopts them from the host's reported
definition like every other field. Nothing about tags is stored only by Controller. An older
Core would decode and silently drop the field, so nonempty tags need `vm-tags-v1` on both the
VM's host and its pool master; otherwise `409` "the VM's host or its pool master does not support
VM tags; upgrade Nex Core" (`updateVM`) or "the selected host or its pool master does not
support VM tags; upgrade Nex Core" (`createVM`), checked after the replay check. VMs on older
Core report no tags. An older Controller does not clear tags: it decodes host reports strictly
(unknown fields rejected), so it refuses the whole report of a host whose journal holds a tagged
VM, and that host goes stale there until the Controller is upgraded. Upgrade the Controller before
Core (the shared upgrade rule; a 0.4.8 Core's `uptime_seconds` and `speed_mbps` already make every
report unreadable to an older Controller).

Known limit: a planned master change or promotion to a host without `vm-tags-v1` can drop tags from
the new master's pool VM record, because that Core decodes the definition without the field. The
VM host's own journal keeps them. Upgrade every member before moving pool authority.

Evidence: `go test ./internal/controller -run VMTags`,
`go test ./internal/agent -run KeepsTags`,
`go test ./internal/vmhost -run 'NeedsStopClassifies|CurrentAndApplied'`.

## VM deletion

API impact: **new contract** — `deleteVM` (`DELETE /api/v1/vms/{id}`), the `vm-delete`
command carrying `VMDeleteRequest` (`vm_id`, `pool_id`, `host_id`, `delete_disks`), the
`vm-delete-v1` host capability, and `Inventory.managed.deleted_vms` tombstones.
Authentication, authorization, validation, errors, asynchronous result, replay and outage
behavior follow `powerVM` / `updateVM`.

Flow: an administrator deletes a successfully created guest with a stable `request_id`.
Controller refuses the Controller appliance, hosts without `vm-delete-v1`, unknown/stale
observed state, and any state other than `shut off`; one edit, power, media or delete
request may be in flight per VM. Core undefines the domain (`virsh undefine`, with
`--nvram` for UEFI), then either records every disk path as retained (`delete_disks=false`)
or unlinks those files (`delete_disks=true`). Succeeded deletes drop the VM from
`Inventory.managed.vms` and emit a tombstone in `deleted_vms`; Controller deletes its
`vm-create` mirror. HTTP `202` queues work; poll operations and the VM list for completion.
Older Core without `vm-delete-v1` is refused with 409 at request time.

Interrupted deletes (changed behavior, same contract). When the same `vm-delete` operation is
delivered again after an interruption at stage `undefining` or `wiping`, Core resumes it: a domain
that is still defined must be shut off (otherwise `needs_attention` with `stop the VM so its
interrupted delete can finish; observed state is <state>`), `undefine` repeats with the same flags,
and a requested wipe repeats over the journaled paths, ignoring files already gone. A **new**
`vm-delete` for that VM with the same `delete_disks` choice first finishes the earlier delete, whose
outcome becomes `succeeded` with its tombstone, and then fails with `VM is already deleted on this
host: an earlier interrupted delete has now finished` (or `an earlier interrupted delete for this VM
is still unresolved: <reason>`). A new request with a **different** `delete_disks` choice fails
without finishing the earlier delete, naming the earlier choice, so disks a later request asked to
keep are never wiped; re-send with the earlier choice or resolve the delete on the host. Core does
not resume an interrupted delete in the background without a request. Limit: a new `deleteVM`
reaches Core only while the VM is still defined and observed `shut off`. After `undefine` (every
interruption at stage `wiping`) the VM is no longer observed, so Controller refuses with 409
`stop the VM before deleting it`; such a delete finishes only by redelivery of the original,
still `dispatched` operation when the host reconnects, and one already reported `needs_attention`
needs inspection on the host. Disk files of a VM deleted
with `delete_disks=true` no longer count as `retained disk for VM <id>` dependencies of
`storage-detach`; a retaining delete still does. Verification: Go unit tests against a fake
`virsh` only; no real-libvirt walkthrough.

## Browser VM console

API impact: **new contract** — `openConsole` (`POST /api/v1/vms/{id}/console`),
`consoleStream` (`GET /api/v1/vms/{id}/console/{ticket}`, WebSocket), `agentConsole`
(`POST /api/v1/agent/console/{token}`, host mTLS upgrade), the `consoles` member of the
report response, the `console-v1` host capability, and `localConsoleStream`
(`GET /api/local/v1/vms/{id}/console`, WebSocket) on the host-local API. Schemas,
statuses and limits are in the generated references.

Flow: an administrator posts an empty object to `openConsole`; Controller verifies the VM
(adopted appliance or successfully created guest), its assigned host (fresh, unrevoked,
`console-v1`) and the observed `running` state, audits `console.open` and returns a
single-use ticket that lives 60 seconds in memory. The next report response to that host
carries `{token, vm_id}` once; Core resolves the VM's local VNC UNIX socket from the live
libvirt definition (a network display is refused) and opens an outbound mTLS HTTP/1.1
`Upgrade: nex-console` request to `agentConsole`. The browser opens the WebSocket at the
returned path; the same session user, same VM and an unconsumed ticket are required, and the
`Origin` header must match. Controller waits up to 20 seconds for the tunnel (504 otherwise)
and then relays raw RFB bytes as binary WebSocket frames until either side closes. The
browser client is bundled noVNC. Tickets, sessions and tunnels are not durable: a Controller
restart closes open consoles and the browser reports it; guests are unaffected. No pool-scoped
setting is introduced, so nothing needs a pool-master record.

Controller prefers HTTP/1.1 in TLS negotiation so these upgrades can hijack the connection;
API clients are unaffected. Compatibility: an older Core ignores the `consoles` member and never
reports `console-v1`, so Controller answers 409 for its VMs; an older Controller never sends
console requests. The host-local stream needs the local session cookie and matching `Origin`,
bridges any running local VM with a local VNC socket, and works without Controller.

Evidence: [console QA report](qa/console/checks.md).

Console features (2026-09-09, [plan](plans/2026-09-09-console-features.md)) use the
**existing contract**: clipboard text, key events, display resize requests and quality
settings are in-band RFB messages that the relay already carries unchanged. The host side is
the VM definition: every `vm-create` now defines, next to the guest agent channel, a
`qemu-vdagent` channel (`clipboard copypaste='yes'`, `mouse mode='server'`, target
`com.redhat.spice.0`), which is what QEMU renders as `-chardev qemu-vdagent,clipboard=on` and
bridges to the VNC server, plus a USB tablet and a VirtIO video model. `validateManagedDefinition`
still compares disks, NICs, OS/CPU/boot and the single VNC display, so definitions created
before this change remain valid; their guest observation reports `clipboard: no-channel` and
`resize: false`. VNC power control (`powerControl`) is deliberately not enabled.

## Browser host console

API impact: **changed contract** — `openHostConsole` (`POST /api/v1/hosts/{id}/console`),
`consoleStream` also serves `GET /api/v1/hosts/{id}/console/{ticket}`, and a console request
with an empty `vm_id` asks the agent for the host's own RFB endpoint. Introduces host
capability `host-console-v1`; reuses `agentConsole` and the report `consoles` member.

Flow matches the VM console: administrator posts an empty object; Controller verifies the
host is fresh, unrevoked and reports `host-console-v1`, audits `host.console.open`, and
returns a 60-second ticket. The next report delivers `{token}` (no `vm_id`); Core starts an
in-process host console session (a root shell rendered as RFB so the same noVNC client
works — no socket is opened for it, since only this same agent process ever dials it) and
opens the outbound `nex-console` upgrade. No pool-scoped setting is introduced.

`host-console-v1` is distinct from `console-v1` (which older Cores without a host shell also
report); a Core without it gets an immediate 409 "upgrade Nex Core" refusal.

The session uses RFB security None over an in-process `net.Pipe` with no reachable listener, so
no other local process (including a QEMU guest) can dial it. `FramebufferUpdateRequest` honours
the incremental flag and answers only when terminal content changed; `KeyEvent` tracks Control
and maps Ctrl+letter to its control byte and arrow/Home/End keys to escape sequences.

## VM media change (CD/DVD)

API impact: **new contract** — `mediaVM` (`POST /api/v1/vms/{id}/media`), durable command
`vm-media`, host capability `vm-media-v1`. Body:
`{request_id, action: insert|eject, drive?, media_id?}`
(`media_id` required for insert, forbidden for eject). Queued like `vm-power`; Core inserts or
ejects the SATA CDROM via `virsh change-media` (`--live` added while the domain is running,
always `--config`), attaching an empty tray first when the domain has none (stopped VMs only:
SATA cannot hot-plug), and journals the current `media_id` on the pool-master managed record
for re-adoption.

**Changed contract, 0.2.8 — a second CD drive.** Every VM defined by a host reporting
`vm-media-drivers-drive-v1` carries two SATA CD drives: the install drive, and a **driver disc**
drive for the disc an installer needs alongside its install media. A Windows guest installing to
a VirtIO disk needs the virtio-win ISO in that second drive; with one drive the operator had to
  swap discs mid-Setup, which is the workaround W3 recorded. Public 0.2.9 media does **not**
  bundle or seed virtio-win: the operator imports matching driver media as a normal `iso-import`
  on the placement host, then selects it as the driver disc. The existing wizard may only show or
  retain media that the host reports as a succeeded import; it does not pre-select a driver disc
  without such an operator import. The bundled-media rollout is blocked until complete corresponding
  source and a tested delivery path are retained for its GPL/LGPL contents. This changes no API
  schema or capability. Controller validates a non-empty `drivers_media_id` on `createVM` and
  `updateVM` (BUG-0228, below). `drive` names which drive a request
applies to: `install` (or omitted, which is what every Controller sent before this existed) or
`drivers`. `VMCreateSpec.drivers_media_id` is the durable record of the driver disc, journaled on
the pool master beside `media_id` and re-adopted the same way; it must differ from `media_id`.
Core resolves a drive to its libvirt target through the user aliases `ua-nex-cdrom-install` and
`ua-nex-cdrom-drivers`, so the target letters libvirt assigns never matter. A domain defined by
an older Core carries neither alias and exactly one cdrom, which is always the install drive, so
a host that predates this keeps working and simply has no drivers drive. A Controller must not
send `drive: "drivers"` to a host that does not report the capability.

**Driver disc on `createVM` and `updateVM` (existing contract, BUG-0228).** When
`drivers_media_id` equals `media_id`, `protocol.ValidateVMSpec` refuses the request with **400**
"the driver disc must differ from the install media" before the `request_id` replay check. Otherwise,
when `drivers_media_id` is set, Controller checks it after the replay check, so an identical replay
still returns the original operation (**200**) without re-checking: **409** "the selected host agent does not support a driver disc drive;
upgrade Nex Core" (on `PUT`: "the VM's host agent does not support a driver disc drive; upgrade
Nex Core") when the host does not report `vm-media-drivers-drive-v1`; **404** "the driver disc ISO
is not registered and verified on the selected host and pool" (on `PUT`: "… on the VM's host and
pool") for an unknown ID, an import that has not succeeded, or media on another host. An empty
`drivers_media_id` is still accepted on older hosts. When a Controller adopts a host-reported VM
whose `drivers_media_id` is not a succeeded import on that host, the VM is marked needs attention
with "ISO <id> is not adopted on this host", as for `media_id`.

**`drive` on `mediaVM` (changed contract, BUG-0235).** `POST /api/v1/vms/{id}/media` accepts an
optional `drive`: omitted, `""` or `install` addresses the install drive; `drivers` addresses the
driver disc drive. Controller checks it before the VM lookup (**400** "drive must be install or
drivers"), and for `drivers` requires the VM's host to report `vm-media-drivers-drive-v1` after the
`vm-media-v1` check (**409** "the VM's host agent does not support a driver disc drive; upgrade Nex
Core"). Insert still requires a succeeded `iso-import` on the VM's pool and host for either drive
(**404** "the install ISO is not registered and verified on the VM's host and pool", or "the driver
disc ISO …" for `drivers`), and is refused with **400** "the driver disc must differ from the
install media" when the ISO is the one the VM's other drive currently holds in the Controller's
VM record, since that spec would fail the shared VM validation and adoption. Core refuses the
same insert itself, from its own journaled VM definition and before any libvirt change, so an older,
third-party or stale Controller cannot make it record such a definition: the operation fails with
"that ISO is already in the install drive; the driver disc must differ from the install media" (or
"that ISO is already in the driver disc drive; the install media must differ from the driver disc").
Re-inserting the ISO a drive already holds into that same drive is allowed.
The drive is passed to Core as `VMMediaRequest.drive`; Controller records the result only from
Core's success report. Replay: a non-empty `drive` joins the stored request payload, so an
identical replay returns the original operation (**200**) and the same `request_id` with a
different drive is **409** "request_id already used with another payload". An omitted drive is not
stored, so requests stored before this change replay unchanged; `""` and `install` therefore count
as different payloads on replay. Compatibility: a Controller older than this fix rejects any body
with `drive` as **400** "invalid JSON request" (unknown field) — the web UI in 0.2.9 sends it for
every driver disc insert and eject, so those always failed there; a Core without the capability gets
the 409 and keeps its single install drive. Requests without `drive` behave as before on every
version.

**Changed contract, 0.2.8 — firmware, Secure Boot and TPM.** `VMBoot` gains `secure_boot`
(`on`, `off`, or omitted) and `tpm` (`2.0`, `none`, or omitted). Both are durable fields of the
VM definition on the pool master and return to a fresh Controller through the existing
inventory and adoption path; neither adds Controller-owned state.

Secure Boot is now stated explicitly in the domain rather than inferred by libvirt from
`firmware='efi'`. **Omitted means on, with the Microsoft keys enrolled** — exactly what libvirt
already selected for every UEFI VM defined before the field existed, so a definition re-sent
unchanged keeps the firmware its guest was installed with. `off` disables Secure Boot for unsigned
boot media or drivers while preserving an existing VM's NVRAM boot entries and enrolled keys. Secure Boot requires UEFI; requesting
it with BIOS is rejected. A Secure Boot domain also carries SMM, which OVMF needs to protect the
variable store.

`tpm: "2.0"` adds an emulated TPM 2.0 (`tpm-crb`, swtpm backend). It is refused when the host's
`virsh domcapabilities` does not offer the emulator backend, rather than defining a device that
cannot start. Windows 11 requires a TPM, and its absence is why the non-IoT editions could not
install at all (BUG-0196).

Changing Secure Boot or the TPM requires the VM to be shut off, like firmware and machine type.
**Removing a TPM is refused outright at any time**: anything the guest sealed to it, including a
Windows BitLocker key, would be lost, and libvirt cleans TPM state by iterating the devices the
persistent definition still carries, so a redefine without `<tpm>` would orphan that state with
nothing left to remove it (BUG-0214). Create the VM again without a TPM instead.

An eject first reads the CD drive from the domain definition — the live one while the VM is
running, the persistent one otherwise — and succeeds without calling `virsh` when the domain
has no CD drive or the tray is already empty; a tray holding either a `<source file>` or a
`<source dev>` counts as loaded. Nothing is attached to a VM without a drive just to eject
from it. When a running guest has emptied its own tray while the persistent definition still
names the ISO, the eject clears that definition alone (`--config`, no `--live`), because
`virsh` refuses an eject on a tray that already holds no media and a stale persistent source
would re-attach the ISO at the next start. For the same reason, an interrupted eject of a
running VM is reconciled as complete only when both the live tray and the persistent
definition are empty. An insert always issues the change, so re-inserting the ISO the
definition already names reconnects a tray the guest ejected. A live edit (`vm-update`)
changes each side only where it differs from the wanted media.

A VM on pool-shared storage takes the same media requests. Core applies a change that rewrites
the durable definition as the master-reserved `vm-update` transition described under
[Core pool peer join and repository observations](#core-pool-peer-join-and-repository-observations)
and keeps the lighter path for a request that
changes nothing, so the console and **Edit VM** reach the same durable record.

Boot order is not changed by a media change, because `virsh change-media` does not change it:
inserting does not give the CD drive a `bootindex`, and ejecting leaves every device's
`bootindex` as it was, the drive's own included. The journal and the Controller mirror
therefore record only `media_id`; an eject leaves the emptied drive at its `media_boot_order`
and every disk at its `boot_order`, exactly as the domain still holds them. That is also what
keeps an eject applicable to a running guest: a pool-shared VM applies its media change as the
`vm-update` transition, where a changed boot order is a `NeedsStop` reason. `ValidateVMSpec`
requires boot order values to be unique and positive but not contiguous, so a re-adopting
Controller does not reject a VM whose order has a gap.

An interrupted media change reconciles by observation. Core journals stage `changing-media`
together with the resolved media path in a single write immediately before the `virsh` call,
so a journal without that stage issued nothing and is simply re-applied on reconnect, while
one with it is compared against the observed CD drive: media equal to the recorded path (or an
empty tray for an eject) is reported succeeded, anything else keeps the operation for the
operator to inspect. A media change is never replayed blindly.

Compatibility: older Core without `vm-media-v1` is refused with 409.

**Changed contract, 0.2.8 — removing a failed ISO import.** `DELETE /api/v1/media/{id}`
(`deleteMedia`) removes the library record of an `iso-import` whose operation is `failed` or
`needs_attention`, so a download that failed does not occupy the library forever with no way to
clear it (BUG-0203). Administrator session, same Origin and `X-CSRF-Token`, audited as
`media.discard`. Only Controller-owned staging bytes of the failed upload are deleted; no Core
command is issued and no host state is touched.

It answers **409** while the import is still queued or dispatched and **409** when another media
transfer holds the staging lock. **404** for an unknown id.

**Changed contract, 0.3.5 — removing an imported ISO.** The former 409 for a **succeeded** import
is replaced by a relay: the same route now takes `{request_id}` and queues a `media-remove`
command to the host that stores the file, which unlinks it, journals a tombstone and propagates
it to members. An owning host without `pool-resource-removal-v1` is still refused, with
`the host that stores this ISO does not support removing imported ISOs yet; upgrade Nex Core`. See
*Pool network and ISO removal, and network rename* above for the full contract.

Retrying a failed download is `POST /api/v1/media/download` again under a **new** `request_id`;
no second endpoint was added for what an existing one already covers.

## Guest tools observation

API impact: **changed contract** — the report inventory member `vms[].guest` (`GuestInfo`)
and the `guest` check are new; `GET /api/v1/vms` passes `guest` through for a VM observed
running in a fresh, libvirt-healthy report and omits it otherwise. No route, command or
capability changes.

Core observes every running VM on each probe: the live definition gives the guest agent
channel state (`org.qemu.guest_agent.0`), the `qemu-vdagent` channel state and the video
model. When the agent channel is attached, Core asks the agent (`guest-get-osinfo`,
`guest-get-host-name`, `guest-network-get-interfaces`, each with a 1-second agent timeout)
and caches the answers per VM for one minute, retrying every ten seconds while the channel is
attached but unanswered. Agents are asked after every VM was observed, oldest cache first, and
only while the probe deadline leaves room for a question. Loopback and link-local addresses
are dropped, at most 32 addresses are reported, `os` is bounded to 4096 bytes, `os_id` to 200,
and `hostname` and interface names to 200; Controller refuses a report beyond those limits or with an
unknown `agent`, `clipboard`, `media` or `drivers_media` value (an absent `media` is an older
Core). `agent` is `connected`, `unresponsive`, `disconnected` or
`no-channel`; `clipboard` is `ready`, `waiting` or `no-channel`. `clipboard` is `ready` only
when the qemu-vdagent channel is connected **and** the guest agent channel is not
`disconnected`: libvirt reads the vdagent port as connected even in firmware, in GRUB or with
no OS, while the guest agent channel on the same virtio-serial bus stays disconnected
(BUG-0243). The tradeoff is that a guest running spice-vdagent without qemu-guest-agent reads
`waiting`. `resize` is true only when the guest can actually follow a console resize request —
the VM has a VirtIO display **and** `clipboard` is `ready` by that rule, since the vdagent is
the agent that applies the new resolution.
A VirtIO display alone only means the host would send the request; a guest without the driver
ignores it, and Nex advertising the control then does nothing (BUG-0201). The same live
definition reports `media`, and `drivers_media` for the second (driver disc) CD drive: `loaded`
when that drive's tray
holds a `<source file>` or `<source dev>` and is not `tray='open'`, `empty` when the drive
holds none, and `no-drive` when the VM has no CD drive; it is absent from Core builds that do
not observe it. A guest owns its tray while it runs — a Debian installer ejects the disc it
booted — so this is the only report of what the VM currently sees. It never changes the durable
`media_id`, which only a media request changes, and the console uses the difference to offer
the recorded ISO again. The probe is separate from the libvirt state probe, so a slow guest never
delays VM power states, and facts are dropped when the VM leaves the running list. `os_id` is
the QEMU guest agent's `guest-get-osinfo` `id`: on POSIX guests it is the identifier from
`/etc/os-release` (such as `debian` or `ubuntu`), while Windows reports `mswindows`. It is
optional and guest-reported; the display name remains in `os`. Nex uses the ID for known distro
artwork and uses a generic monitor icon when it is missing or has no bundled icon. The icon
lookup has no name-based heuristics. This is observation, not health: a connected agent proves
nothing about guest applications.

Core also retains the last successful OS identity for managed VMs as `DetectedOS`: `os`
(non-whitespace, at most 4096 UTF-8 bytes), optional `os_id` (at most 200 bytes), and
`observed_at` (the nonzero timestamp of that successful guest-agent response). A failed,
empty or missing response does not replace this history. Hostname, IP addresses, channel
states and application health are not retained as OS history.

API impact: **changed contract** — optional `Inventory.managed.detected_os` maps canonical
nonzero VM UUIDs to `DetectedOS` (at most 10000 entries); `GET /api/v1/vms` exposes the
corresponding optional `detected_os` object. The existing authenticated Core peer
`POST /api/v1/pool/vms/inventory` also accepts the optional map. No new route or command.
Core keeps local successful observations in `pool-vms.json` until normal inventory publication
can deliver them to the master. The master's VM owner records in that file are the durable
authority, included in existing authority replication; a replacement Controller recovers them
from the master's managed inventory. Controller projects only the enrolled, non-revoked pool
master's report with matching membership; it stores no separate OS history.

The peer's existing pool mTLS identity, ready membership and generation checks apply. Each
history key must name a VM in the publishing member's inventory owned by that member and pool;
invalid values are refused. Only a newer successful observation replaces the master's record.
Exact retries and an omitted map or entry preserve the existing history; a stopped or
unresponsive guest does not clear it. A managed VM deletion removes its owner record and
history. A lost Controller connection does not stop Core observation or peer publication.
Local persistence failure is logged and retried on a later successful guest-agent reading;
a retained-history read failure reports a failed `guest-os-history` check. Historical records
are limited to managed VM definitions; unmanaged guests do not gain durable OS history.

The client prefers live `guest.os`, otherwise displays `detected_os.os` as **last detected**
even for a stopped VM or stale host, and otherwise displays the configured OS family as
**configured**. Historical identity does not imply that guest tools are connected. Missing or
unknown reported IDs use the generic monitor icon. Compatibility follows the shared upgrade
rule: older Core versions omit history; the existing configured fallback remains available.
Core advertises `detected-os-v1`; an updated Controller acknowledges it with optional
`ReportResponse.detected_os: true` only on protocol v2 reports offering that capability.
Core withholds `Inventory.managed.detected_os` until acknowledged, so older Controllers
continue accepting reports without this additive field. Startup and re-enrollment clear the
acknowledgement; Core retains OS history independently of negotiation.

Upgrade Core across the pool before relying on retained OS history through master changes.
An older master ignores the optional peer field while the reporting host keeps its local
observations, but an older Core's VM record cannot preserve this history through transfer or
promotion. Mixed-version master changes therefore do not guarantee retention.
The original live guest observation evidence is in the
[console features QA report](qa/console-features/checks.md); that report does not verify durable
OS-history behavior.

## VM usage observation

API impact: **changed contract** — the report inventory member `vms[].usage` (`VMUsage`) and
the `usage` check are new; `GET /api/v1/vms` passes `usage` through for a VM observed running
in a fresh, libvirt-healthy report and omits it otherwise. No route, command or capability
changes.

Core samples every active VM on each probe with
`virsh domstats --raw --nowait --cpu-total --balloon --vcpu --block <uuid>`, one call per VM.
`--nowait` is what makes the query safe: without it libvirt waits for the domain's job, which
is held for the whole of the operation in progress — a Windows guest held one shutdown for over
three minutes (BUG-0213) — so the probe would stall behind exactly the VM an operator is
watching. With it, libvirt returns the counters it can read without the job (CPU time, balloon
allocation, vCPU count) and omits the ones behind the QEMU monitor (host RSS, block sizes).
That is why `memory_host_bytes`, `disk_used_bytes` and `disk_capacity_bytes` are nullable:
absent means the host could not read the number, which is never the same fact as zero.
`dominfo` is avoided here for the same reason the libvirt state probe avoids it.

`cpu.time` is a cumulative counter, so **Core keeps the previous sample per VM and reports the
rate itself**; a Controller that subtracted two counters would be deriving pool state the host
already knows. `cpu_percent` is whole percent of the VM's own allocated vCPUs averaged over
`sample_window_ms`, and is absent until a second sample exists — a VM that just started, an
agent that just restarted and a VM whose counter reset all report no rate for one probe rather
than a wrong one. A counter that went backwards, a non-positive window, a window older than two
minutes, and an implausible vCPU count are each refused rather than guessed at. The value is
clamped at 1000 because above that the arithmetic is reporting a broken counter, not a busy VM;
the multiplication is bounds-checked before it is performed, since an overflowed product lands
on a plausible small number rather than an obvious one. Controller refuses a report carrying a
rate with no window, a window above 300000 ms, or a percentage above the same clamp; a window
without a rate is accepted, because that is a host that measured a gap and declined to turn it
into a rate.

`memory_assigned_bytes` and `memory_maximum_bytes` are allocation, never usage. Nex does not
report memory used **inside** the guest: that needs balloon statistics a Nex VM definition does
not enable, and the guest agent has no command for it. Disk figures count only the VM's own
images, identified by the per-VM directory Nex writes them into; a mounted installer ISO and a
firmware variable store are not the VM's storage and are left uncounted rather than guessed at.

The probe is separate from the libvirt state probe, so a slow sample never hides VM power
states. A VM that cannot be sampled within the probe deadline re-reports its previous sample
unchanged with that sample's own `observed_at`, so a client sees an older number rather than a
gap; a sample older than two minutes is dropped instead. A VM libvirt declines to answer for is
skipped and the check stays healthy — the check fails only when active domains cannot be
listed, which is the condition the libvirt probe already fails on. Reporting a working host as
failed over a cosmetic counter would be worse than the missing number.

Compatibility: shared upgrade rule; an older Core never sends the member and clients show no
usage. Controller must be upgraded before Core, because report JSON is decoded with
`DisallowUnknownFields`. No pool-scoped setting is introduced: usage is observed state with no
durable record, recomputed from the host on every report.

The VM details panel renders this observation read-only: CPU is labelled as a percentage of
allocated vCPUs; memory allocation is distinguished from guest-used memory; and host memory and
disk metrics show `Not reported` when absent. A stale VM status still means the current execution
state is unknown. Verified 2026-09-16 against a disposable Nex Core/libvirt host and the deployed
Controller UI: a running VM reported CPU, allocation, resident memory and disk use/capacity, while
a shut-off VM reported no usage rather than zero.

## Usage history

API impact: **new contract** — Controller `GET /api/v1/vms/{id}/stats` and
`GET /api/v1/hosts/{id}/stats`, agent `POST /api/v1/agent/stats/{token}`, report response member
`stats_channels` (`StatsChannelRequest`), schemas `StatsHistory`/`StatsSeries`, and the
`usage-history-v1` capability. The `vms[].usage` observation above keeps its shape; Core now fills
it from the history sampler instead of per-VM probe calls.

**State owner: Core.** Each host samples its running VMs and itself every 5 seconds and keeps the
history in fixed-size round-robin files under the agent state directory (`usage/host.rrd`,
`usage/vm-<uuid>.rrd`): 5 s × 10 minutes (memory only), 1 minute × 24 hours, 1 hour × 30 days and
1 day × 1 year, each an average of the finer tier that ignores unknown samples. Completed coarse slots
are written in place; the file is rewritten atomically only on creation or when its series change. The Controller keeps no copy, so a replacement Controller shows
the same history as soon as the host is connected. Deleting a VM through Nex deletes its history;
other files untouched for a year hold no data and are removed. A VM's history stays on the host that
recorded it; live migration, when it exists, must carry the file.

VMs are sampled with one `virsh domstats --raw --nowait --list-running` call per tick, so the
BUG-0213 rule above still holds: no sample waits for a domain job. Counters become rates on the
host. A counter reset, a missing previous sample or a member libvirt did not return is recorded as
unknown (`null`), never zero, and a stopped VM records nothing. Guest-used memory needs the virtio
balloon statistics period, which VM definitions now set (`<stats period='5'/>` on the virtio
balloon); an existing VM gets it the next time its definition is saved, and the series stays
unknown until the guest's balloon driver reports.

**Host temperatures.** API impact: **existing contract** — the same host `StatsHistory` gains
series in group `temperature` with unit `celsius`; no field, endpoint or capability changes, and a
client that does not know the group ignores it. The host sampler also walks `/sys/class/hwmon` on
every tick and records each usable `tempN_input` as a gauge. A key is
`temp.<chip>-<device>.<n>`, built from the hwmon chip name and the sensor device's sysfs basename:
hwmon index numbers are assigned in probe order and change across boots, so they are never part of
a key. Labels come from the driver — one series per CPU package rather than per core, the block
device name for `drivetemp`, the NVMe device and model, the PCI address for GPU and NIC chips, and
otherwise the chip's own `tempN_label`. An absent, unreadable or implausible reading (below −40 °C
or above 150 °C, which some chips return for an unconnected input) is recorded as unknown, never
zero. At most 24 sensors are kept per host, registered ahead of the per-core CPU series so a large
host's cores cannot starve them. Over that budget the lowest-priority chips are dropped first —
CPU package and NVMe sensors are kept ahead of per-drive temperatures — and the survivors stay
sorted by key, so the same sensors survive from one boot to the next. A host with no sensors reports none, which is normal. SATA and SAS
disk temperatures need the kernel `drivetemp` module, which the Nex Core package loads through
`/etc/modules-load.d/nex-sensors.conf`; the agent never loads modules itself.

**Query path.** A host cannot be dialled by the Controller, so a query uses an outbound channel:
when no channel to the host is open, the Controller returns `stats_channels: [{token}]` in the next
report response (only to a host that offered `usage-history-v1`), the agent dials
`POST /api/v1/agent/stats/{token}` over its mTLS identity with `Upgrade: nex-stats`, and after
`101` the Controller writes HTTP/1.1 `GET /stats?object=vm&id=<uuid>&range=<range>` (or
`object=host`) requests that Core answers in order. The token is bound to the host that was asked;
another host's token gets 404. Queries on a channel are serialised, each with a 10 s deadline and a
4 MiB body limit. The channel closes after 5 minutes idle, on either restart, or on any error, and
the next query opens a new one; a query that finds a reused channel already closed retries once on
a new channel. The first query to a host can therefore wait up to one heartbeat
(10 s by default) plus the host's dial; it gives up after 20 s. Later queries are immediate.

Controller validates each reply before relaying it: object, id and range match the query, at most
128 series, and every series exactly the range's point count (`live` and `10m` 120, `24h` 1440,
`30d` 720, `1y` 365); anything else is `502`. Statuses:
`400` invalid range (`range must be live, 10m, 24h, 30d or 1y`); `404` unknown VM/host or no
history on the host; `409` host revoked, stale or without the capability. A stale host returns
`the host is not freshly connected; its usage history cannot be read now`; an older Core returns
`the host agent does not keep usage history; upgrade Nex Core`. `429` (realtime) the host already
samples 8 live objects; `503` host did not dial within 20 s; `504` query timed out. Queries are
read-only, so every retry is safe.

**Realtime (`range=live`).** A live query starts, or renews for two minutes, one-second sampling
of that one VM or host on its host, into a 120-slot memory-only buffer; the reply has
`step_seconds: 1`, ends at the current second, and has `null` for seconds not yet sampled. Live
sampling never writes to disk or to stored history. The lease is owned by the host: when queries
stop — a closed browser, a lost Controller — sampling ends within the lease plus one second, with
no clean-up request. VM samples use single-domain `virsh domstats --nowait --state` with a deadline
under one second, so a slow answer skips that second rather than overlapping. Only a domain whose
`state.state` is `1` (running) is sampled: libvirt still returns stale CPU and balloon counters for a
shut-off domain (BUG-0255). A stopped VM returns
`null`s; a VM the host does not know returns `404`. More than 8 distinct live objects on one host
return `429` with the host's message, which the Controller relays.

Compatibility: shared upgrade rule — Controller before Core, because report JSON is decoded
strictly; an older Core never offers the capability and the Stats tab asks for a Core upgrade.
A Core with `usage-history-v1` but built before realtime answers `range=live` with `400`.
No pool-scoped setting is introduced.

The **Stats** tab on VM and host pages charts CPU, memory, network and disk (and load for hosts)
with a range picker, a **Stacked** toggle, synced crosshair and drag-zoom, a value tooltip, legend
chips, live refresh at the 10-minute and 24-hour ranges, and a **Realtime** drawer that polls
`range=live` once a second.

Host fixes from review (no contract change): a stored tier ignores samples behind its current step
instead of resetting, so a backwards clock step keeps 24h/30d/1y history, but after a clock that ran
ahead is corrected those ranges store nothing until time reaches the stored step (BUG-0231; the agent
unit is ordered after `time-sync.target` to make that rare); an idle object stays in memory until its
open day step is written, so a stopped VM keeps its last day in `1y` (BUG-0230); the 256-VM cap
counts only VMs sampled in the last hour; and hosts keep at most 64 per-core CPU series.

Verification: Go unit and contract tests for the sampler, ring store, realtime sampling, query
handler, channel and relay; mocked-browser review of the Stats tab and Realtime drawer; and a final
[disposable-host run](qa/2026-09-16-usage-history.md) (2026-09-17, simulated nested-KVM host, 1 s
heartbeat) that passed every exercised check, including Controller replacement, realtime for a
running and a stopped VM, and lease expiry. Not exercised on a host: the 10 s default heartbeat
latency, 30d/1y tiers and retention, the 8-object realtime cap, and rates under load.

## Host hardware

API impact: **new contract** — Controller `GET /api/v1/hosts/{id}/hardware`, agent `GET /hardware`
on the existing stats channel, schema `HostHardware` (with `HardwareSystem`, `HardwareCPU`,
`HardwareMemory`/`MemoryModule`, `HardwareDisks`/`HardwareDisk`, `HardwarePCI`/`PCIDevice`,
`HardwareSensors`/`TemperatureSensor`) and the `host-hardware-v1` capability. No report member
changes: the data is static and several kilobytes, and the report is decoded strictly, so putting
it there would force a Controller-before-Core upgrade for no benefit.

**State owner: Core.** A host reads itself on request — `/sys/class/dmi/id` for system, board,
chassis and firmware; `/proc/cpuinfo` and the CPU topology sysfs for the processor; `/proc/meminfo`
plus `dmidecode -t memory` for the installed total and the module in each slot; `lsblk` for whole
disks; `lspci` for PCI devices; and the same `/sys/class/hwmon` walk as the temperature series for
the sensor list. Nothing is cached, sampled or persisted on either side, so a replacement
Controller sees the same report with nothing to re-enter. A section whose source is missing or
fails is returned `unavailable` with a short reason naming the tool; the other sections still
return. The tool's own error text is discarded: it can name paths, and which tool failed is what an
operator acts on. The whole report is bounded well inside the channel deadline, so a tool that
hangs costs its own section a reason rather than the request a timeout.

Each sensor carries the key of its temperature series with the chip's own `tempN_max` and
`tempN_crit`, so a gauge takes its scale and warning band from the hardware. A chip that publishes
no threshold reports none, and the gauge is then a plain value with no band.

**Query path.** The same outbound stats channel as usage history: the Controller writes
`GET /hardware`, so a host that offers either `usage-history-v1` or `host-hardware-v1` is sent a
channel token. Statuses: `404` unknown host; `409` revoked or stale
(`the host is not freshly connected; its hardware cannot be read now`) or an older Core
(`the host agent does not report its hardware; upgrade Nex Core`, which hides the panel rather than
alerting); `502` the host could not read its hardware, or the reply was malformed or over the
bounds (at most 64 memory modules, 128 disks, 256 PCI devices, 24 sensors — the same bound the host
enforces on itself); `503` the host did not dial within 20 s; `504` the host did not answer within
10 s. Read-only, so every retry is safe.

**Changed contract (2026-09-22): `md_arrays`.** The report gained a section for the Linux md
arrays the kernel has assembled (`HardwareMDArrays`, with `MDArray` and `MDMember`), read straight
from `/sys/block/md*/md/` — `level`, `raid_disks`, `degraded`, `sync_action` and the position in
`sync_completed` as `sync_percent`, and for each `dev-*` entry the member's device, `slot` and
`state`. `mdadm` is not parsed: the kernel's own files are the record, and reading them keeps the
section testable against a fixture tree. A host with no arrays reports an empty list, not an
unavailable section. The Controller re-checks the host's own bounds of at most 32 arrays of at most
64 members each and stores none of it, so a mirrored system disk and its rebuild are visible to a
replacement Controller the moment the host reports. The field is required on `HostHardware` and
the Controller always remarsals it, so an older Core that never sent arrays yields
`md_arrays: { arrays: null }` rather than an absent key; the host page treats that as
unreported and asks for a Core upgrade instead of claiming a single unmirrored disk. An empty
`arrays` list means the host reported and has no md arrays.

Serial numbers (system, board and disk) are returned to authenticated administrators only. They are
never logged and no diagnostic-bundle path collects the report.

`dmidecode` and `pciutils` are executed as separate GPL-2.0-or-later programs, not linked into Nex;
they are recorded in [LICENSING](LICENSING.md) for the component manifest and retained source.

Compatibility: shared upgrade rule — Controller before Core. An older Core never offers
`host-hardware-v1` and the Hardware panel is hidden without issuing a request. No pool-scoped
setting is introduced.

Verification: Go unit tests against fixture sysfs trees and stub tools (every section, a missing
and a failing tool, no sysfs or proc at all) and Controller relay tests for the refusals above;
browser review against mocked replies. **The real-hardware walkthrough belongs to the project
owner** (decision: project owner, 2026-09-22) and is outstanding: no sensor label, threshold,
`drivetemp`, `dmidecode` or `lspci` output here has been observed on a real machine.

### Link speed and uptime in host inventory (changed contract, 2026-09-23)

API impact: **changed contract**, additive — two optional report members, surfaced unchanged on
`GET /api/v1/hosts`: `inventory.nics[].speed_mbps`, the negotiated link speed in Mb/s from
`/sys/class/net/<name>/speed`, and `inventory.uptime_seconds`, whole seconds the host had been up
at `inventory.observed_at`, from `/proc/uptime`. Each is omitted when unknown: virtual and
link-down interfaces report `-1` or fail the read, and an unreadable `/proc/uptime` omits uptime.
Older agents omit both. **State owner: none** — observed state read on every inventory probe;
the Controller keeps only its existing copy of the last report. Compatibility: shared upgrade
rule — Controller before Core, because report JSON is decoded strictly: an older Controller
rejects the entire report of a Core that sends either field, and the host shows as stale until the
Controller is upgraded. Introduced in development product 0.4.8. Verification: agent parse
tests (`1000`, `-1`, missing file, `12345.67 54321.00`) and a Controller test that both fields
reach `GET /api/v1/hosts`.

## Replacing a failed system disk (POST /api/v1/hosts/{id}/system-disk)

API impact: **new contract** — Controller `POST /api/v1/hosts/{id}/system-disk`, the durable
managed command `host-system-disk-replace` carrying `SystemDiskReplaceRequest`, and the
`host-system-disk-replace-v1` capability. The mirror's own state is read from the
[host hardware report](#host-hardware); this operation adds no query of its own.

**State owner: Core**, host-local. The durable record is the md array superblocks on the disks and
`/etc/mdadm/mdadm.conf` written by the installer. Adding a member does not change the array's UUID,
so nothing rewrites `mdadm.conf`, and the kernel assembles the mirror at boot with no Nex code. The
Controller keeps only the task it keeps for every host operation. A fresh Controller sees the
mirror, its members and any rebuild in the hardware report.

**Request.** `{request_id, array, device:{by_id, serial, size_bytes, fingerprint,
erase_confirmation?}}`. `array` names one array as the report does (`md0`). `device` is one
candidate from the host's own `POST /api/v1/storage/discover` listing with `kind: zfs-device` — the
same listing, eligibility rules, fingerprint and typed erase confirmation the ZFS wizard uses; a
device reported `erase_required` must carry `erase_confirmation` equal to its own `by_id`.
Administrator session and `X-CSRF-Token`. `202` returns `{host_id, array, operation}`; an exact
repeat of a `request_id` replays that operation, and a different payload under the same
`request_id` is `409`.

**Refusals.** `400` for an array that is not an `mdN` name, a device that is not a
`/dev/disk/by-id/` path, a missing serial, size or 64-hex fingerprint, an erase confirmation
naming another device, or a host ID that is not an opaque ID (`system disk replacement requires
a host`). `404` unknown host. `409` for a revoked host, a host that is not freshly
connected, and an older Core: `replacing a system disk needs host-system-disk-replace-v1 on this
host; upgrade Nex Core`. Core revalidates everything itself when it executes and reports a failed
task with its own sentence: the arrays cannot be read (`Nex Core cannot read this host's RAID
arrays`), the named array is missing (`this host has no array <mdN>`), the array is not degraded
(`/dev/md0 is not degraded, so there is no member to replace`), it is not a RAID1 mirror, it has
no working member to copy from, the chosen disk already holds a working member
(`<disk> already holds a working member of <array>`), discovery cannot re-list the disk (`the
replacement disk cannot be safely observed`), the disk changed or is in use, its old data was not
confirmed for erasure, or it is smaller than the remaining system disk.

**What Core does.** It observes the array and the disk again, then `wipefs --all` on the
replacement, `sgdisk --replicate=<new> <surviving disk>` and `sgdisk -G <new>` so the copied
partition identifiers are unique, `udevadm settle`, and `mdadm --add <array> <new>-part<n>`.
Making the disk bootable then follows the host's firmware (`/sys/firmware/efi`), not whether an
ESP partition exists on the disk: the installer writes a 512 MiB ESP onto every mirror member on
both firmwares, but on BIOS that partition is left unreplicated for boot and Core only runs
`grub-install --target=i386-pc` on the disk itself. On UEFI firmware Core finds the surviving
disk's EFI system partition, formats the replacement's copy of it with `mkfs.vfat`, and populates
it the way the installer does — Debian's `grub-efi-amd64` writes only the one mounted ESP, so Core
runs the host's own `/usr/local/sbin/nex-sync-esp` copier, the same helper an APT hook re-runs
after every GRUB update, and copies what GRUB maintains in `/boot/efi` itself when that helper is
absent or reports that it copied nothing, including the removable fallback path the installer
seeds. The ESP is looked for only on the UEFI path. The kernel then rebuilds the mirror; the
host and its VMs keep running throughout and nothing waits on the Controller.

A host installed with a separate `/var` has two mirrors, one for `/` and one for `/var` over the
same disks. Before it erases anything, Core finds every other degraded RAID1 array on the named
array's disks and takes each one's partition number from that array's own working member, on
whichever disk it is. It then adds the replacement's matching partition to each of them in the
same operation: after the first array the replacement is no longer blank, so a second request for
the same disk would have nothing to wipe. A healthy array is never changed. If such an array has
no working member, or the replacement already holds a member of it, the operation is refused
before the wipe (`<array> has no working member to copy from`, `the replacement disk already holds
a member of <array>`). When an interrupted operation resumes, a replacement member that has failed
or is gone is reported (`the replacement disk's member of <array> has failed`, `the replacement
disk is no longer a member of <array>`) instead of being treated as repaired. On a host whose root
file system is mounted read-only, the BIOS `grub-install` runs between `mount -o remount,rw /` and
`mount -o remount,ro /`, which is also run when GRUB fails. The root's state is read, and both
remounts run, in the system manager that runs the other commands, not in the agent's own sandbox,
and one lock covers every such window on the host (a package install waits for it). This is an
internal change: the request, result, capability and refusal list above are unchanged except for
these added sentences (API impact: **existing contract**).

**Retries and interruption.** The operation is journaled on the host under its state directory,
keyed on the operation ID. A redelivered command replays its recorded outcome instead of acting
again. An array that already has the replacement as a member is adopted: the disk is never wiped a
second time and only the bootloader work is repeated. An agent interrupted after the wipe resumes
from the journal's `wipe-started` stage, where the wiped disk can no longer match its fingerprint,
and checks the identity that survives a wipe — the same `by_id`, serial and size — instead; nothing
else about the selection is relaxed. A member whose disk the kernel no longer exposes is the failure
being repaired, so it is passed over rather than turned into a refusal.

Compatibility: shared upgrade rule — Controller before Core. The capability is checked before the
command is stored, so an old Core is refused at the API rather than sent a command it would reject.

Verification: Go unit tests for the sysfs reader against healthy, degraded and rebuilding fixture
trees, and for the replacement's command sequence, every refusal above, the adoption path and the
interrupted-wipe retry; Controller contract tests for the relay, the replay, the CSRF requirement,
the old-Core refusal and invalid input; a UI check of the mirror states and the erase confirmation;
and a [disposable-host run](qa/2026-09-22-system-disk-replace.md).

## Optional feature layers (nex-core-local layers)

API impact: **new contract**, on-host only: the root command `nex-core-local layers` and its
[machine-readable command contract](api/local-openapi.json) (`x-commands` entry
`nex-core-local layers`, schema `LayerInfo`). It adds no HTTP route, no report-protocol command,
no `Inventory` field and no Controller behaviour; the Controller stores nothing about layers. State
owner: **core**. A layer is an optional feature set (`nex-zfs`, `nex-cluster`) shipped as a signed
systemd-sysext image that Core verifies and merges over `/usr`, so the base image stays small and a
host that never uses a feature never carries it.

| Subcommand | Behavior |
|---|---|
| `list` | One JSON array of `LayerInfo`, sorted by name: every layer the signed release manifest of the running base offers plus any layer recorded in `enabled.json`. It checks the store file's size, not its hash, so a status poll never reads the whole image. |
| `ensure NAME` | Makes the layer enabled and merged, idempotently. Order is the journal: the store file is proven against the signed manifest first, then `enabled.json` records the wish, then the layer is linked and merged, then sysusers, tmpfiles and a udev reload run and the layer's units are enabled. A crash anywhere repeats to the same end. |
| `disable NAME` | Refused with `in_use` while a kernel module of the layer has a reference count, one of its busy units is active, or one of its busy commands (for example an imported ZFS pool) reports something. Otherwise it disables the layer's units and records the layer as not wanted. The merge ends at the next boot; Core never drops a layer under running services. Disabling a layer that is not enabled succeeds. |
| `activate` | What `nex-layers.service` runs at boot. Re-hashes every enabled layer against the signed manifest and the running base build, links the good ones for systemd-sysext and removes the links of the rest, writes the volatile report `/run/nex/layers/status.json`, and **exits 0 even when a layer is refused**, so a bad layer never holds boot. |

**State.** `/var/lib/nex/layers/enabled.json` (format `1`: per layer `enabled`, `sha256`,
`base_build_id`, `changed`) is the host's record of what the operator wants. The store is
`/var/lib/nex/layers/store/sha256-<hex>.raw`; one signed release manifest per base build is kept under
`/var/lib/nex/layers/releases/<BUILD_ID>/`. All of it lives on the host's persistent data volume. A
replacement Controller never needs it, and a rolled-back or reinstalled host reads it again from its own
disk. `nex-layers.service` is conditional on `enabled.json` existing, so a host that never enabled a
layer, including every Classic-edition host, does not run it.

**Refusal: `layer-unavailable`.** Every operation that needs a layer reports the one stable code
`layer-unavailable`, followed by the layer name and a reason (`layer-unavailable: NAME: REASON[: detail]`),
so existing ZFS and cluster operations do not grow a new error per cause. Reasons:

| Reason | Meaning |
|---|---|
| `not_in_store` | The layer file has not been placed on this host. |
| `base_mismatch` | The layer was built for another base build. A layer is bound to exactly one base by `SYSEXT_LEVEL` equal to the base `BUILD_ID`, so a rolled-back base never merges the newer base's layer. |
| `digest_mismatch` | The store file is not the signed bytes. The layer is left unmerged. |
| `signature_invalid` | The stored release manifest is missing, or is not signed by a release key in the trust in force: the same `trust.json` chain the update engine verifies with (pinned `/usr/share/nex/update/trust.json` plus the adopted rotations). A revoked or expired release key, a key rotated out, or a broken trust chain refuses every layer. |
| `unknown_layer` | The signed manifest has no such layer, or the name is not a valid layer name. |
| `merge_failed` | systemd-sysext refused, the layer did not appear, or a unit of the layer could not be enabled. |
| `reboot_required` | Enabled and verified, but a live merge was not possible (another layer owns the overlay and `/usr` is held open); the next boot merges it. |
| `in_use` | `disable` was refused because something still uses the layer. |

What is not covered: nothing yet calls `ensure` from the ZFS or cluster operations, no HTTP or report
surface exposes `list`, and layers are not yet part of the update manifest. Those are later steps of the
[immutable Nex Core plan](plans/2026-10-01-immutable-nex-core.md). The lab evidence is in
[the layers record](qa/image/phase2-layers.md): simulation only (QEMU/KVM, no Secure Boot, no physical
hardware).

## Software updates

API impact: **new contract** — Controller `GET /api/v1/updates` and
`POST /api/v1/updates/apply`; host-local `GET /api/local/v1/updates` and
`POST /api/local/v1/updates/apply`. Schemas `UpdateStatus` and `UpdateApplyResult`
are in the generated references.

Updates are apt operations against the signed Nex archive, never bare `.deb` installs; apt owns
download, GPG verification and dependency resolution. Clients read the archive's signed
`Release`/`Packages` index. The publisher still writes `{repo}/updates.json` only for older
hosts that poll it; current clients do not depend on it.

Both sides read a package repo URL from `NEX_UPDATE_REPO`, else `/etc/nex/updates.conf`
(`repo_url=`), else `https://nexvirtualization.com/apt`. Plain HTTP repo URLs that
point at private or loopback addresses are treated as the public default. That URL is
written to `/etc/apt/sources.list.d/nex.sources` with
`Signed-By: /usr/share/keyrings/nex-archive-keyring.gpg`, which both packages ship, so
a repo changed after install takes effect without reinstalling. Core refreshes its system
APT index at most once every five minutes. Controller runs unprivileged, so it keeps the
same signed source and a five-minute APT index cache under its own state directory; the
root apply helper still uses the system source to install. A failed refresh is returned in
the JSON `error` field instead of being presented as an unavailable package. A package no
configured source offers is returned in
the JSON `error` field with HTTP 200. While an apply runs, GET also returns `phase`,
`progress` and `message` from `update-progress.json` in the service state directory.
`progress` reports fixed steps rather than download percentage.
The Controller's GET also returns `core_available_version`, the newest `nex-core-local` the
same archive offers, so the host page can compare each host's reported version with it; the
field is omitted when the archive does not offer the package.

Controller apply writes `pending-update.json` under the state directory and returns
202; the root `nex-controller-update` path unit runs `/usr/local/lib/nex/apply-update`,
which runs `apt-get update` then `apt-get install nex-controller` and restarts the service;
plain install also gives an appliance still running its bundled binary its first Controller
package. Running outside `nex-controller.service` keeps apt from being killed by
that restart, and a failed upgrade restarts the service before reporting. If the helper or path unit is missing, apply
returns **503** instead of scheduling. Core apply starts the same work in the
background and returns 202; the UI polls GET until `phase` is `done` or `failed`.
A phase left stuck by a service restart mid-install clears itself once the installed version
matches available, but never while that service is the apply still running: apt reports the
new version as soon as dpkg unpacks it, and `done` means the apply finished, not that the
package landed. Stopping the service waits for an apply it started rather than abandoning it.
Both sides re-read the installed version afterwards and fail the apply if it did not
advance, because apt exits 0 when nothing needed upgrading.

Packaged installs and current appliance images include the update helper so Controller VMs can
upgrade in place. A published artifact that predates this helper correction is not changed by this
source behavior; use the documented direct recovery until a rebuilt package is available.

### Image edition: host-local update engine (Phase I-3a)

API impact: **changed contract** — host-local `GET /api/local/v1/updates` and `POST /api/local/v1/updates/apply` answer
differently on an image-edition host; **new contract** — host-local `POST /api/local/v1/updates/check`, `…/activate`,
`…/rollback`, `…/cancel`; additive `Inventory.image` (booted, staged and previous release with slot letters, phase,
`rollback_floor`, `state_format`, `secure_boot`, latest operation and failure) and the `image-update-v1` capability in the host
report protocol. Image-edition hosts produce `Inventory.image` from the agent's `image` probe, which only reads the engine's
`state.json` record, the kernel command line and the firmware `SecureBoot` variable; the Controller-side `image-update-v1`
acknowledgement that withholds it from an older decoder is described under [Phase I-3c](#image-edition-controller-relay-for-one-host-phase-i-3c).
The settings, media, import and component-fetch routes are in [Phase I-3d](#image-edition-layers-settings-offline-media-and-on-demand-payloads-phase-i-3d); the pool rollout routes are
[Phase I-4a](#image-edition-pool-rollout-coordinator-phase-i-4a). The classic edition
(apt, described above) keeps today's behaviour until it is retired; a host chooses once, at start, by its static os-release
(`IMAGE_ID=nex-core` is the image edition). The media, import and fetch routes answer `409 unsupported_edition` on a classic host; its `PUT /api/local/v1/updates/settings` takes a channel ([Update channels](#update-channels)).
Schemas `ImageUpdateStatus`, `ImageUpdateOperation`, `ImageUpdateApplyRequest`, `ImageUpdateActionRequest` and
`UpdateRefusal` are in the generated host-local reference.

Core fetches and verifies everything itself; installed hosts never run `apt`. The update base URL and channel are
configuration only: `NEX_UPDATE_BASE_URL` / `NEX_UPDATE_CHANNEL`, else `base_url=` / `channel=` in `/etc/nex/updates.conf`; there is
no built-in URL, and an unset one is `503 source_unconfigured`. The default channel is `stable`. Plain HTTP mirrors are fine:
signatures carry integrity.

Metadata, all Ed25519 with raw 64-byte signatures over the exact bytes and raw 32-byte keys (base64 inside `trust.json`):

| File | Signed by | Host checks |
|---|---|---|
| `trust.json` + `trust.sig` | a root key of the trust in force | `version` only grows (`trust_replayed`), contains a usable root and release key; a newer document extends the pinned `/usr/share/nex/update/trust.json` chain and is kept in `/var/lib/nex/update/trust.<version>.json` (`trust_gap` if a version is skipped). The chain is re-verified link by link on every use, without asking whether the signing key is still inside its validity today; the newest adopted version is recorded and never goes back, so a chain that fails to verify or has lost documents is refused (`trust_invalid`) rather than replaced by an older trust |
| `<channel>/channel.json` + `channel.sig` | a release key | `expires` (`channel_expired`), `channel_serial` not below the stored one of this update server and channel and, at the same serial, the same `manifest_sha256` (`channel_replayed`), `sequence` not below `max(stored rollback_floor, min_sequence)` (`below_rollback_floor`) |
| `releases/<sequence>/manifest.json` + `manifest.sig` | a release key | SHA-256 equals the channel's `manifest_sha256` (`manifest_mismatch`), `architecture` `amd64`, `created` not later than the host clock (`clock_behind`), `compat.update_engine` not above this engine (`engine_too_old`), `min_from_sequence` (`bridge_required`), no required component of a kind this engine cannot install (`unsupported_component`; `slot-image`, `layer`, `source`, `compliance` and every `on_demand` component are fine), every `layer` built for this very release (`layer_base_mismatch`) |
| `blobs/sha256-<hex>` | the manifest's `sha256` and `bytes` | HTTP `Range` + `If-Range` resume of a `.part` file; a wrong final hash discards the part (one clean retry when it was resumed) → `blob_mismatch` |

A key past `not_after` or marked `revoked` no longer verifies new documents. Only a fully verified check updates the stored
`channel_serial` and `rollback_floor`, and it re-judges the metadata against the record under the final lock, so two overlapping checks
only ever raise them. The stored floor also binds `activate` and `rollback` (`below_rollback_floor`). `slot-image` is a zstd-compressed tar
of the release directory (`root.erofs`, `root.verity`, `roothash`, the UKI, `esp/`, `manifest.json`; component `encoding` `tar+zstd`; its
`sha256` and `bytes` cover the compressed bytes, so a resume continues the download as it is stored); after the blob hash matches it is
decompressed while it is unpacked (regular files, clean relative names only; the whole zstd frame including its checksum must be read),
authenticated again by the slot writer (every file hash, the root hash, the whole hash tree) and its root hash must equal the
manifest's `verity_root_hash` (`roothash_mismatch`, `image_invalid`).

`GET /api/local/v1/updates` returns `ImageUpdateStatus`: `running`, `slots` (role `booted`, `previous`, `staged` or `armed`, each
with label, sequence and root hash), `channel`, `available` (only when newer than the booted version), `phase`
(`idle`, `checking`, `downloading`, `writing`, `staged`, `armed`, `booted`, `committed`, `rolled_back`), `rollback_floor`,
`channel_serial`, `state_format` and the latest `operation` and `failure`. It reconciles the record with the boot entries on the disks:
an armed release whose UKI lost its counter is `committed` (the old version becomes `previous`); one whose entries all ran out of
tries is `rolled_back` with `failure.code` `boot_failed`. State is `/var/lib/nex/image/state.json`, `update` subtree, Core-owned; the
disks stay the truth and a lost file is rebuilt from them (the previous-version record is not: rollback then reports
`nothing_to_roll_back`).

| Route | Request → result |
|---|---|
| `POST /api/local/v1/updates/check` | refresh and verify metadata → `200` `ImageUpdateStatus`; `429 check_throttled` within 60 s of the last successful check (`Retry-After: 60`); `503 source_unreachable` / `source_unconfigured`; `502` for refused metadata |
| `POST /api/local/v1/updates/apply` | `{request_id, sequence?, scope, activate, running_vms}` → `202` `ImageUpdateOperation`, background: check, download, write the inactive slot on every disk. `activate: stage_only` (default) never changes the next boot; `reboot` also arms and reboots. `scope: controller` swaps the Controller appliance's system image instead (see [Controller appliance swap](#image-edition-controller-appliance-swap-phase-i-4b)); it is `409 scope_unavailable` on a host with no appliance engine |
| `POST /api/local/v1/updates/activate` | `{request_id, running_vms?}` → `202`; arms the staged release (its UKI `nex-core_<label>_<sequence>+3.efi` is written to a `.new` file, flushed and renamed on each ESP, beside the running version's) and reboots. The only call that arms |
| `POST /api/local/v1/updates/rollback` | `{request_id, running_vms?}` → `202`; arms the retained previous version and reboots. During the trial boot of an update (phase `booted`) it arms the release that update replaced (the running one is marked exhausted, and once the replaced release is blessed the abandoned update is the retained previous); while an update is armed but has not booted yet it is `409 already_armed` |
| `POST /api/local/v1/updates/cancel` | `{request_id}` → `200`; stops a running apply or discards a staged release; `409 already_armed`, `409 write_in_progress`, `409 nothing_to_cancel` |

`request_id` (8–64 letters, digits, `-`, `_`) is the operation ID: an exact replay returns the original operation with `200`, an
interrupted apply resumes when its `request_id` is repeated, a changed body is `409 request_conflict`, a second active operation is
`409 update_in_progress` (an interrupted apply must be resumed or cancelled first; the same refusal when another process, such as
`nex-update` on the host, holds the update lock `/var/lib/nex/image/update.lock`). Results are kept (last 20). `running_vms`
`refuse` (default) refuses to reboot while virtual machines are running (`409 running_vms`); `shutdown` accepts their downtime; the guests are listed again right before an `apply` with `activate: reboot` arms the staged release.
Pool rollout gates are not evaluated here (the coordinator is a later slice).

Refusals are `{ "code": "<stable code>", "error": "<one literal message>" }`; the host keeps running. Compatibility guards run at
stage, activate and rollback: `state_format_downgrade` (the target's `state_format` is below `RequiredStateFormat`, the Go port of
the retired storage guard: any ZFS record ⇒ 3, managed VM storage records ⇒ 2, unreadable ⇒ present), `state_format_too_old`, and
the md superblock gate — `md_generation_unsafe` (an image that raises the manifest's `compat.md_generation` must set
`compat.md_fallback_safe`, the publisher's assertion that the older kernel still assembles `nex-data`) and `md_generation_downgrade`
(no stage or rollback to a lower generation than the running one unless the running one was published `md_fallback_safe`). The two
`md_*` manifest fields are additive to the plan's `compat` list; see the
[phase 2 build record](qa/image/phase2-build.md) defect 4 for why.

### Image edition: Controller relay for one host (Phase I-3c)

API impact: **new contract** — Controller `GET /api/v1/hosts/{id}/updates` and `POST /api/v1/hosts/{id}/updates/{action}`
(`action` is `check`, `apply`, `activate`, `rollback` or `cancel`; the host-scoped `settings`, `media`, `import` and `fetch` are [Phase I-3d](#image-edition-layers-settings-offline-media-and-on-demand-payloads-phase-i-3d)), the managed command `host-image-update` carrying
`ImageUpdateRequest`, `ReportResponse.image_update` (the `image-update-v1` acknowledgement) and host-local
`GET /api/local/v1/updates/operations/{id}`. **Changed behaviour, no new field:** `Inventory.image` (defined in
[I-3a](#image-edition-host-local-update-engine-phase-i-3a)) is now withheld until the Controller acknowledges `image-update-v1`.
Schemas `HostImageUpdateRequest`, `HostImageUpdateAccepted`, `HostImageUpdateView` and `ImageUpdateRequest` are in the generated
Controller reference. `ReportResponse.controller` and `controller-epoch-v1` are **not** implemented. The Controller's own APT update
routes (`GET /api/v1/updates`, `POST /api/v1/updates/apply`, above) are kept: they update the Controller package of a classic
Controller, and the plan's appliance swap that retires them is a later slice.

Where the work happens. The host's update engine (`internal/nexupdate.Engine`, inside `nex-core-local`) fetches, verifies, writes the
unused slot, arms and rolls back, and keeps the record (`/var/lib/nex/image/state.json`); there is one engine and one writer per host.
The embedded agent, which runs in the same process, relays the Controller's command to that engine, and its result says only whether
the engine accepted the request: `succeeded`, or `failed` with the error `<code>: <message>` carrying the engine's stable code
(`update_in_progress`, `nothing_staged`, `already_armed`, `state_format_downgrade`, …) or `unsupported_edition: …` on a host without an
engine. Progress, the staged and previous releases, the rollback floor and the outcome come back in `Inventory.image`; a Controller that
disappears mid-update changes nothing on the host, and a fresh Controller shows the same state from the next report. The Controller keeps
only the task row it keeps for every host operation (`state.owner: core`).

| Route | Request → result |
|---|---|
| `GET /api/v1/hosts/{id}/updates` | administrator session → `200` `{host_id, connection, supported, image?}`: the host's last reported `Inventory.image` unchanged; `supported:false` and no `image` for a classic host or a Core not yet acknowledged; `404` unknown host |
| `POST /api/v1/hosts/{id}/updates/{action}` | `{request_id, sequence?, activate?, running_vms?}` (`sequence` and `activate` for `apply` and `import`; `running_vms` `refuse` (default) or `shutdown` for `apply`, `import`, `activate`, `rollback` and `cancel`; the I-3d actions take `channel`, `base_url`, `media` and `component`) → `202` `{host_id, action, operation}`; an exact replay of the same `request_id` is `200` with the original operation; a changed payload under the same `request_id` is `409`. Session, same-origin and `X-CSRF-Token` required. `409` for a revoked host, a host that is not freshly connected, or one that did not offer `image-update-v1` ("this host does not run the image edition or does not support image updates; upgrade Nex Core") |
| `GET /api/local/v1/updates/operations/{id}` | host-local root session → `200` `ImageUpdateOperation` by the `request_id` that created it; `404 unknown_operation` after the last 20 age out; `409 unsupported_edition` on a classic host |

Compatibility. An image-edition Core offers `image-update-v1` in every v2 report capability list only while its engine is running; a
Controller answers `image_update: true` only to a Core that offered it, and the Core clears the acknowledgement on restart, disconnect
or Controller replacement and then withholds `Inventory.image` again. The legacy v1 report never carries it. A strict older Controller
therefore never sees `inventory.image` or a `host-image-update` command, and the Controller sends no update command to a host that
did not offer the capability.

### Image edition: layers, settings, offline media and on-demand payloads (Phase I-3d)

API impact: **new contract** — host-local `PUT /api/local/v1/updates/settings`, `GET …/updates/media`, `POST …/updates/import`,
`POST …/updates/components/{id}/fetch`; Controller relay of the same four through the existing
`POST /api/v1/hosts/{id}/updates/{action}` (`action` `settings`, `media`, `import`, `fetch`; new optional body fields `channel`,
`base_url`, `media`, `component`) and the same fields on the managed command payload `ImageUpdateRequest`; additive
`Inventory.image.base_url` and `Inventory.image.media` (schemas `ImageMedia`, `ImageMediaChannel`) and, on `ImageUpdateStatus`
(`GET /api/local/v1/updates`), `base_url` and `media`; additive manifest fields (`Component.name`; `requires_base` and
`verity_root_hash` of a layer were already defined); `ImageUpdateOperation.kind` gains `import` and `fetch`, its phase `fetched`.
Schemas `ImageUpdateSettings`, `ImageUpdateMedia`, `ImageUpdateImportRequest` and `ImageUpdateFetchRequest` are in the host-local
reference. **State owner: Core.** Records, all host-local: `/etc/nex/updates.conf` (channel and base URL), the `update` subtree of
`/var/lib/nex/image/state.json` (last media scan, staged release with its layer files), `/var/lib/nex/layers/store`,
`/var/lib/nex/layers/enabled.json` and `/var/lib/nex/layers/releases/<BUILD_ID>/manifest.json{,.sig}`, `/var/lib/nex/payload/<sha256>/`.
A fresh Controller recovers what it displays from `Inventory.image` (`channel`, `base_url`, `media`, `phase`); the Controller stores
nothing, so replacing it loses nothing. Compatibility: the new fields are inside `Inventory.image`, which is already withheld until
the Controller acknowledges `image-update-v1`; a Core that predates these actions answers the relayed command with a failed result
(`unsupported_edition: …`), and a Controller that predates them never sends them.

**Layers travel with their base.** A release's `layer` components (`id` `nex-<name>`, `requires_base`, optional `verity_root_hash`,
`required`, `on_demand`) are verified with the manifest: a layer whose `requires_base` is not the release's `<label>-<sequence>`
(the BUILD_ID the image states) refuses the whole release with `layer_base_mismatch` (502), and the layer store refuses it again before
writing a byte. An apply, import or later stage downloads every layer that is not `on_demand`, plus each `on_demand` layer the
operator has enabled on the host, hash-checks it with the same Range resume as the slot image, and places it in
`/var/lib/nex/layers/store/sha256-<hex>.raw` together with the exact signed manifest bytes under
`/var/lib/nex/layers/releases/<BUILD_ID>/` (what `nex-layers.service` verifies at the next boot) **before** the inactive slot is
written, so a failed or interrupted layer download leaves the slot and the running host untouched (`blob_mismatch`,
`source_unreachable`, `layer_invalid`). Stage changes nothing about the running host: `enabled.json` is rewritten only when the
release is armed (`activate`, or `apply` with `activate: reboot`; `rollback` switches it back for the previous release), and then only
for layers the operator enabled. The layers of the base before the running one stay until the next stage succeeds (rollback is open
until then), cancelling a staged release prunes the staged release's layers, and nothing is removed when a retained manifest cannot be
read. A release that carries layers on a host without a layer store is `409 layers_unavailable`.

**Settings.** `PUT /api/local/v1/updates/settings` `{channel, base_url}` → `200` `{channel, base_url}`. `channel` is 1–32 lower-case
letters, digits or `-`, starting with a letter (`400 invalid_channel`); `base_url` is empty (clears the source: there is no built-in
default) or an `https` or explicit `http` URL without credentials, query or fragment, at most 2048 characters (`400 invalid_base_url`).
Plain `http` is allowed because signatures and blob hashes carry integrity. The values are written to `/etc/nex/updates.conf` (other
lines are kept) and take effect for the next check; the pointer history (`channel_serial`) belongs to the update server and channel it came
from, so a changed source starts empty, an earlier source keeps its own and a bundle's other channels never share the configured one's,
while the rollback floor is kept. `409 update_in_progress` while an operation or a check runs;
`409 settings_overridden` while `NEX_UPDATE_BASE_URL` or `NEX_UPDATE_CHANNEL` fix them in the service environment.

**Offline media.** `scripts/update-bundle.py` writes the online layout (`trust.json`, `<channel>/channel.json`,
`releases/<sequence>/`, `blobs/`, `SHA256SUMS`) to media labelled `NEXUPDATE` or into `/var/lib/nex/image/inbox/` (the inbox itself, or
one directory per bundle). `minimal` carries the slot image and the layers every update stages; `full` adds the source, compliance,
on-demand layers and on-demand payloads. `GET /api/local/v1/updates/media` → `200` `{media: [ImageUpdateMedia]}` is read-only (ids
`inbox`, `inbox/<name>`, `label`; nothing is mounted; channels come from the unverified `channel.json`) and is remembered in the
engine's record for `Inventory.image.media`. `POST /api/local/v1/updates/import` `{request_id, media, channel?, sequence?, activate?,
running_vms?}` is an apply whose source is the bundle: the same `fetchMetadata` path checks trust (a rotation in the bundle is adopted
as online), the bundle's own signed `channel.json` and `expires` (`channel_expired`), `channel_serial` and the rollback floor
(`channel_replayed`, `below_rollback_floor`), the manifest hash and signature, `created` against the host clock (`clock_behind`: a clock
earlier than the release was made is refused; this applies to online checks too), then every blob hash, layers, slot write and verity.
Unmounted `label` media is mounted read-only (`nosuid,nodev,noexec`) for the operation and released at its end. The channel is `channel`,
else the host's channel if the bundle has it, else the bundle's only channel (`channel_ambiguous`, `channel_not_in_bundle`). Every file is
opened inside the bundle without following a link out of it and without blocking: a FIFO, device or a link leaving the bundle is not a
channel (listing) and is `metadata_invalid` (import), so a hostile drive cannot hold the update lock.
Operation kind `import`; replay (`200`), conflict, interruption and resume by repeating the `request_id` are as for apply.

**On-demand payloads.** `POST /api/local/v1/updates/components/{id}/fetch` `{request_id, media?, channel?}` → `202` operation of kind
`fetch`, phase `fetched`: one `on_demand` component of the release the source offers (a manifest component with `on_demand` and a
`name`, for example the Controller image or the Windows driver ISO) is downloaded with the same checks to
`/var/lib/nex/payload/<sha256>/<name>`, extending the installer's payload layout; a file already in place that hashes right is not
downloaded again. `not_on_demand` (the slot image and layers come with a stage), `unknown_component`, `blob_mismatch`; the staged
release's phase is not touched. The media, import and fetch routes answer `409 unsupported_edition` on a classic host (settings takes a
channel there, see [Update channels](#update-channels)); all four need the root session and (for `PUT`
and `POST`) the CSRF token, and refuse unknown JSON fields.

### Update channels

Nex ships on two channels: **stable**, built from `main`, and **development**, built from `dev` as pre-releases of the next stable release
([release channels plan](plans/2026-10-02-release-channels.md)). A host's channel is Core state in `/etc/nex/updates.conf`; the Controller
reads it and keeps nothing.

- **Image edition** (unchanged contract): `channel` is set by `PUT /api/local/v1/updates/settings` `{channel, base_url}` and relayed by
  `POST /api/v1/hosts/{id}/updates/settings`; it is reported in `Inventory.image.channel`. The update tree holds one directory per channel
  (`<base>/<channel>/channel.json`). Core rewrites `channel` and `base_url` together, so a caller changing only the channel sends the host's
  current `base_url` back (the Controller UI does).
- **Package edition** (changed contract): the same route takes `{channel}` with `stable` or `dev` and answers `200` `{channel, base_url: ""}`.
  The channel is the archive the host reads: `repo_url` in `/etc/nex/updates.conf` becomes the archive root for `stable` and `<root>/dev` for
  `dev` (other lines are kept; the next check refreshes the package index from the new archive). `GET /api/local/v1/updates` reports the
  active `channel` (`UpdateStatus.channel`, host-local only). Refusals: `400 invalid_channel` (anything but `stable` or `dev`),
  `400 invalid_base_url` (a package host has no `base_url`), `409 settings_overridden` while `NEX_UPDATE_REPO` fixes the archive. The
  Controller does not relay it: `POST /api/v1/hosts/{id}/updates/settings` still answers `409` for a package host, and the Controller's
  own package source stays `repo_url`.
- A pool rollout still refuses hosts on different channels (`channel_mismatch`). Switching a host from development back to stable offers
  nothing until stable publishes a newer release than the host runs: apt does not downgrade by itself, and the image engine refuses a
  `sequence` below the host's rollback floor.

API impact: **changed contract** — host-local `PUT /api/local/v1/updates/settings` is now also answered by the package edition, and
`GET /api/local/v1/updates` of a package host adds `channel` (`UpdateStatus`); no new route, no Controller route change, no managed command.
Older Cores answer the package-edition `PUT` with `409 unsupported_edition`, and a client treats that as "channel not selectable". State
owner: `core` — `repo_url` (package) or `channel` (image) in `/etc/nex/updates.conf` on each host; a fresh Controller reads the image
edition's from `Inventory.image` and never needs the package edition's.

### Image edition: pool rollout coordinator (Phase I-4a)

API impact: **new contract** — host-local `GET /api/local/v1/pool/updates`, `POST …/pool/updates/rollouts`,
`GET …/rollouts/{id}`, `POST …/rollouts/{id}/control` (pool master only); Controller `GET /api/v1/pools/{id}/updates`,
`POST …/updates/check`, `POST …/updates/rollouts`, `POST …/updates/rollouts/{rollout_id}/control`; peer
`POST /api/v1/pool/updates/step`, `GET /api/v1/pool/updates/status`, `GET /api/v1/pool/updates/blobs/{sha256}`; the managed command
`pool-update` (`PoolCommand.action` `update-rollout` or `update-control`, additive `PoolCommand.update`); the capability
`pool-update-rollout-v1`; additive `Inventory.managed.update_rollout`; and the authority-replica section `update_rollout`. Schemas
`PoolUpdateRollout`, `PoolUpdateHost`, `PoolUpdatePause`, `PoolUpdateRequest`, `PoolUpdateStartRequest`, `PoolUpdateControlRequest`,
`PoolUpdateView`, `PoolUpdateAccepted` (Controller), `PoolRolloutRequest`, `PoolRolloutControl`, `PoolRolloutResult` (host-local) and
`PoolUpdateStep`, `PoolUpdateStepResult`, `PoolUpdateHostStatus`, `PoolUpdateTarget` (peer) are in the generated references.
**Not in this slice:** `ReportResponse.controller` and `controller-epoch-v1`, `PUT …/updates/settings`, offline media and import
(the settings, media and import routes are Phase I-3d; the appliance swap and the controller_first step are Phase I-4b below). The
Controller's `api_epoch` is therefore always unknown, so a release whose manifest sets `min_controller_api_epoch` above 0 is refused
`controller_unknown` until that report exists.

Where the work happens. A rollout is one durable job on the pool master's Core: `pool-update.json` in the agent store beside
`pool-peer.json`, written atomically before the coordinator acts, replicated to every member as the signed authority section
`update_rollout` (declared only while a record exists, so a pool that never rolled out keeps the section layout older members verify; the
content digest and signature cover it), and reported in `Inventory.managed.update_rollout` by the master only. The coordinator
(`internal/agent/pool_update.go`) runs on the host's own tick; a Controller is never in its loop. Delete the Controller and a fresh one
reads the same record from the master's next report (`state.owner: core`). Hosts' progress is each host's own `state.json`. A promoted
master adopts an active rollout **paused** with `pause.code` `master_changed`, whatever it was doing; an operator decides `resume` or
`abort`. A master that is not (or cannot prove it is) the pool master advances nothing.

Preflight (also `dry_run:true`, which records nothing and returns the host order): the host is the writable pool master; no other rollout
is `running` or `paused` (`rollout_in_progress`); HA is off (`ha_enabled`; there is no HA setting yet); `running_vms` is not `evacuate`
(`evacuation_unavailable`); every non-ejected member is `ready` (`member_not_ready`) and not in maintenance (`host_busy`), answers
(`host_unreachable`), advertises `pool-update-rollout-v1` (`upgrade_required`), follows the master's channel (`channel_mismatch`), runs no
other update operation (`host_busy`), has verified the release (`release_unavailable`; the host's own check refuses with its stable code,
so a release needing a bridge is `bridge_required`), would not lower `state_format` (`state_format_downgrade`), has the free space the
required components need (`insufficient_space`), and a `mixed_pool:false` release is accepted only with `running_vms:"shutdown"`
(`mixed_pool_requires_shutdown`: the plan's "forces `shutdown`" is a refusal here, so guest downtime is never implied); the Controller
`api_epoch` check is `controller_unknown` or `controller_too_old` when the manifest sets a minimum. The request ID is the rollout ID.

The coordinator then (1) asks every host to `stage` in the same pass (never arms the next boot), then (2) updates one host at a time,
members by running-guest count then host ID, the master last. Per host: **gate** — every other member `ready`, none in maintenance, the
remaining ready Corosync voters of the nodelist (witness included in a two-host pool) keep a majority since votes do not shrink when a
host stops, clustered storage needs three voters (`quorum_margin`; a pool that forms no cluster has no quorum to lose), HA off, the
host not busy; **maintenance** through the existing `pool-maintenance` command; **teardown** wait until no ready member's watchdog is
feeding; **drain** by `running_vms` (`refuse`, the default, waits up to `step_timeout` (6 h) then pauses `running_vms` naming the
guests; `shutdown` records the running guests as `stopped_by_update` on the host's record, then gracefully shuts down exactly those and
waits for them to be off; nothing is ever force-stopped); **activate** (peer step, idempotent on its step ID; the host engine keeps its
refuse policy as the last guard); **boot** wait `boot_deadline` (30 min) for the host to run the release, else pause
`needs_attention` (nothing restarts elsewhere and no guest is assumed stopped); **confirm** the host committed the release and is a ready
member; maintenance off; **restart** exactly the `stopped_by_update` guests on the same host through the normal start admission (one
owner epoch, no duplicate writable execution), retried until `step_timeout`; **soak** `min_soak` (60 s). After the last host,
**finalize** re-reads every host and only then sets `finalized_sequence` (the only point where that release may run irreversible state
migrations). A host that fell back (`rolled_back`) or does not confirm pauses the rollout instead. Each host record carries `attempt`
(`PoolUpdateHost.attempt`, omitted while 0): how often the rollout has sent that host through staging again after a rollback. It is part
of every `step_id`, so a repeated step in one attempt is a replay and the first step of a new attempt is not.

| Route | Request → result |
|---|---|
| `GET /api/local/v1/pool/updates` | host-local root session → `200` `{rollout}` (`null` if none); `409 not_pool_master` on a member |
| `POST /api/local/v1/pool/updates/rollouts` | `{request_id, sequence, running_vms?, dry_run?}` → `202` `{rollout}`, or `200` `{dry_run:true, plan:{sequence, order[]}}`; refusals `409 {code, error}`; `400 invalid_request` |
| `GET /api/local/v1/pool/updates/rollouts/{id}` | `200` `{rollout}`; `404 rollout_not_found` |
| `POST /api/local/v1/pool/updates/rollouts/{id}/control` | `{request_id, action: pause\|resume\|abort\|rollback_host, host_id?}` → `200` `{rollout}`; `pause` lands after the host being updated; `resume` re-checks membership, HA and readiness and starts fresh deadlines; `abort` keeps committed hosts and puts back maintenance and stopped guests of a host that had not activated; `rollback_host` needs a paused rollout and an activated host and arms its previous release |
| `GET /api/v1/pools/{id}/updates` | administrator session → `200` `{pool_id, connection, supported, rollout?}`: the master's reported record unchanged; withheld until `image-update-v1` is acknowledged |
| `POST /api/v1/pools/{id}/updates/check` | `{request_id, sequence, running_vms?}` → `202`: relays `pool-update` with `dry_run`; the outcome is the operation's result |
| `POST /api/v1/pools/{id}/updates/rollouts` | `{request_id, sequence, running_vms?}` → `202`; replay `200`; changed body `409` |
| `POST /api/v1/pools/{id}/updates/rollouts/{rollout_id}/control` | `{request_id, action, host_id?}` → `202` |
| `POST /api/v1/pool/updates/step` | peer, master only: `PoolUpdateStep{rollout_id, step_id, action: check\|stage\|controller\|drain\|activate\|confirm\|undrain\|rollback, sequence, vm_ids?}` → `PoolUpdateStepResult{done, code?, message?}`; `done:false` with no code means ask again with the same `step_id` |
| `GET /api/v1/pool/updates/status` | peer, master only → `PoolUpdateHostStatus` (engine phase, running and staged release, verified target, free space, running guests, busy) |
| `GET /api/v1/pool/updates/blobs/{sha256}` | peer, any ready member, served by the master only: `HEAD`/`GET` with `Range`; a staged file is served only when its content hashes to the requested digest |

The Controller answers the three POST routes `409` ("upgrade Nex Core on `<host>`") unless the master and every non-ejected member
advertise `pool-update-rollout-v1`, and also when the master is not fresh and active. `check` is the extra, dry-run route; the plan lists
`step` actions `stage,drain,activate,confirm,undrain,rollback` and the coordinator additionally uses `check`. Members read blobs from the
master's cache before their channel (`nexupdate.PeerFirstSource`, installed by `nex-core-local`); each blob is verified against the signed
manifest again, so the master is untrusted for integrity. Metadata still comes from the channel: an air-gapped pool needs the offline
import of a later slice for it. Core advertises `pool-update-rollout-v1` with `image-update-v1` (only while its engine is running), and
`Inventory.managed.update_rollout` is withheld, like `Inventory.image`, until the Controller acknowledges `image-update-v1`. An older
member whose replica verifier does not know the `update_rollout` section refuses a replica that carries one; a rollout is refused
before it starts unless every member advertises the capability.

### Image edition: Controller appliance swap and controller_first (Phase I-4b)

API impact: **changed contract** — `POST /api/local/v1/updates/apply` with `scope:"controller"` (was `409 scope_unavailable`); additive
operation kind `controller` and phase `swapping` in `ImageUpdateOperation`; peer `PoolUpdateStep.action` `controller`, additive
`PoolUpdateHostStatus.controller_vm`, `PoolUpdateTarget.controller_image`; additive `PoolUpdateRollout.phase` value `controller` and
`PoolUpdateRollout.controller` (`PoolUpdateController{stage, host_id, started_at, attempt, error}`). No Controller route changes: the
rollout view reads `GET /api/v1/pools/{id}/updates` and relays the existing pool routes. State owner: **core**. The swap's record is the
operation journal under `/var/lib/nex/core-local/vmhost/<request_id>.json` beside the deployment journal; the rollout's controller step
is part of the master's `pool-update.json` (replica section `update_rollout`). A fresh Controller reads the rollout from
`Inventory.managed.update_rollout`; the Controller keeps nothing.

Deployment profile. A Controller deployed with `vmhost.Config.SystemImage` is the two-disk profile: UEFI guest, a read-only raw
`system-<sha256>.img` (the verity system image) and a persistent `controller-data.qcow2` that holds only the Controller-scoped state
recovery already enumerates (`/var/lib/nex-controller`, `/etc/nex-controller`). The VM UUID and management MAC never change. A
single-disk qcow2 appliance cannot be swapped (`controller_not_profile`).

Swap. `scope:"controller"` runs on the host that owns the Controller VM, one journaled operation under the request ID. (1) The
release's `vm-image` component digest is the signed manifest's (the separate appliance signature check is not part of this path); the
component is staged under `/var/lib/nex/payload/<sha256>/` and placed read-only beside the VM after its digest is checked again
(`controller_digest_mismatch`). (2) The guest agent shuts the Controller down gracefully and the swap waits for `shut off`; it is never
forced (`controller_shutdown_failed`, nothing was changed). Before anything is touched, and again before the checkpoint and before a
restore replaces the data disk, no other libvirt domain, running or only defined, may reference `controller-data.qcow2` (directly or
through a backing chain) and no hardlink may alias it: otherwise the swap is refused, or rolled back before the new image starts. (3)
`controller-data.qcow2` is checkpointed with a reflink copy (`controller-data.qcow2.pre-<sequence>`) that is fsynced, read back against
the disk byte for byte and then renamed into place with its directory fsynced; a restore uses the same verified, durable copy. (4) The
domain is redefined with the new system disk and started. Ready means the running domain is the owned layout booted from exactly the
expected system image (the new one, or the old one after a rollback) and the Controller's readiness path (the pinned-certificate HTTPS
request deployment uses) answers, for `controller_deadline` (10 minutes); the Controller reports no version of its own, so the image
digest the domain boots is the identity checked. (5) Success commits and
keeps the old image and the checkpoint until the next swap. Failure shuts the failed guest down gracefully, restores the data
checkpoint, redefines the old image, restarts and probes it: the operation then ends `controller_rolled_back`; if that fails too the
VM is left stopped, `controller_needs_attention`, and host-recovery export still applies. Every step observes the real domain and files
first, so repeating the request ID after a crash resumes from the journal; a swap already running the release is a no-op. A second swap
is `controller_swap_in_progress` while one is unfinished and an unfinished swap cannot be cancelled (`swap_in_progress`).

controller_first. After staging everywhere and before any host goes into maintenance, the master's coordinator scans the hosts'
`controller_vm` and, for a release that carries a Controller image (`controller_image`), asks the owning host for one `controller`
step (`step_id` includes the swap `attempt`). A host that cannot be reached or cannot read its Controller record is retried, and pauses
the rollout `step_timeout` if that lasts; the step is never skipped on a failed scan. A refusal pauses the rollout `needs_attention`
with the code in `pause.message`; no host has been touched; `resume` asks for a new swap. A Controller outside the pool is updated first
with `scope:"controller"` on its own host. A pool with no Controller appliance, or a release without a Controller image, skips the step.

## Source access and availability

Controller's public `/source` route serves its configured matching Nex source archive and
returns an explicit unavailable response if no archive is configured. This is not a complete
ISO source-delivery claim: Debian, firmware, and other redistributed components retain their
own source/notice obligations under [licensing requirements](LICENSING.md).

Operator instructions belong in the [user guide](guide/README.md).

## Planned coverage by phase

These are capability areas from the roadmap, **not available API calls or agreed route names**.
Choose exact operations and payloads when implementing the relevant phase.

| Phase | Coverage to add as implemented |
|---|---|
| 2 (remaining) | Disk resize and related VM lifecycle follow-ups (creation, power, console, edit and deletion with optional disk wipe are implemented above) |
| 3 | Installer/first-boot enrollment integration and package/version compatibility |
| 4 | Multi-host pools (member join under the pool master, master change, member eject), shared NFS access per host, network changes and recovery, placement, maintenance and removal |
| 5 | Snapshot create/list/revert/delete, disk/configuration coverage and interrupted-operation recovery |
| 6 | Backup repositories/policies/jobs, manifests/catalogs, restore/verification, controller recovery |
| 7 | Migration preflight, relocation/live migration, evacuation, execution ownership and interrupted results |
| 8 | Scoped access, upgrades, operating/recovery procedures and supported-version reference review |
| Follow-on after 8 | MCP server shipped disabled by default: a visibly enabled Controller MCP service principal with a scoped administrator role, read-only inventory/operation tools, and explicitly confirmed existing Controller operations; no direct Core commands or MCP-held pool state |
| 9 | HA configuration/status, quorum/fencing observations and commands respecting pool-local authority |
| 10 | Managed node VMs, Swarm integration, applications/services, volumes/secrets, backup/recovery and lifecycle |
| Agreed follow-on: [hardware passthrough](plans/2026-09-08-nex-platform.md#guided-hardware-passthrough) | Device/group inventory and readiness, host preparation/undo, exclusive VM assignment/removal, reset and reboot observations; reuse earlier inventory/configuration/task contracts and add missing capabilities |

Agents initiate outbound authenticated HTTPS connections to Controller; the separate
host-local listener is for local browser setup/recovery, not inbound Controller commands.
VM disks, migration, and backup payloads use authorized
host/storage data paths rather than the Controller database/API. Local recovery and running
VMs must survive Controller outages. Later Swarm integration remains a separate managed-VM
boundary; it does not put Docker directly on Nex Core.
