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
- A presented `host_id` already on record, or a `pool_id` already held by another master
  host, is 409: a token holder cannot claim an existing identity. Only a token bound to a
  revoked host (`host_id` on the token) re-issues that host, and the request must present
  that host and its pool, or nothing (409 otherwise). Joining a second host to an existing
  pool is not available (Phase 4).
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

Core-local authenticated member join and NFS/SMB reconciliation are implemented;
see [Core pool peer join](#core-pool-peer-join-and-repository-observations).
Changing the pool master and ejecting a member remain planned, **not available**
Phase 4 work. This slice does not establish the full multi-host phase gate.

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
replace_host}` to the host's `POST /api/local/v1/enrollment` with the login response's
CSRF token. `ca_pem` is the Controller's own served HTTPS certificate (`-tls-cert`), which
the host pins for its Controller connection; it is not the host-identity CA. A Controller
without a configured served certificate returns 503 before contacting the host. A host-side 409 (already enrolled with this same Controller URL and trust) passes through with that host's
`host_id`; any other host-side failure returns 502 naming `controller_url` so the operator
knows which address the host tried to reach. Success returns `{host_id, pool_id}` (the
pool the host's enrollment created or kept, looked up from the `hosts` table) and audits
`host.add` against the site. The root password is used once for that single login and is
never stored, logged, or included in any error message. Any failure after a token is
minted best-effort deletes that token row, so a lost response and a safe retry never leak
an unused token; retrying the same call after a lost response is safe — the host's own
409 identifies the already-enrolled identity, so fetching inventory resolves the outcome
without a second side effect.

`localEnroll` is a host-local route (root session + CSRF, same as other mutations under
`/api/local/v1`) that receives that pushed request and calls the same `agent.Enroll` the
`nex-core-agent enroll` CLI uses. Attempts are serialized per host so a retried click
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
`needs_attention` retains the original VM/disk identity. Power actions use separate stable
request IDs; replay the same action to reconcile its journal, and inspect fresh VM inventory.
A completed graceful shutdown request does not establish that the guest has stopped.

`nex-core-local` owns the local listener and resumes the outbound enrolled agent. Do not run
a separate agent against the same identity directory. Configure it with these command flags;
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
console account `root` (SSH password auth remains off). This changes
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

## Managed VM creation

API impact: **new contract** for client storage/network registration, raw ISO upload,
stopped-VM creation and assigned-host media download; **changed contract** for the
existing VM list and capability-bearing agent reports/commands/results. Protocol version
remains `1`. Matching Core reports `managed-vm-v1` on each heartbeat; Controller rejects
new work for stale, revoked, wrong-pool or unsupported hosts and withholds managed commands
from reports lacking that capability. Older agents retain inventory refresh support; the
shared upgrade rule applies.

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
source, directory path, browsable state and an optional existing-repository flag—never passwords, device paths, mount options or command
output. Discovery cannot create a directory, mount/login a backend, retain an iSCSI node/session,
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
commands. The development-only `virsh` profile must bind to the same pinned SSH scope: its
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
VM disk references use that alias. Core never changes ZFS properties and detach removes only the
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
host and expected generation. Update changes typed configuration; recheck only observes it,
except that recheck (and Core's one-minute periodic lane) starts an existing, unchanged Nex NFS/SMB
mount unit when systemd reports it inactive, then verification decides health; an unmounted
repository reports `remote storage is not mounted`;
detach is refused while disks, retained disks, media or active work depend on it and never
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
dependency guard. Credential-only rotation may keep an attached receipt, but only after every VM
using the repository is shut off and no pending or `needs_attention` disk, media, or ISO work
refers to it. If Core cannot prove rollback restored the previous backend state, it reports
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

`GET /api/v1/storage/credentials?pool_id=<pool>&host_id=<host>` lists only Core-reported
redacted receipts currently eligible for explicit cleanup. Eligibility is an observation, not a
reservation: `POST /api/v1/storage/credentials/{receipt}/retire` queues a Core-only command
with `{request_id,pool_id,host_id}`, and Core rechecks that the receipt has no active or pending
repository references before deleting its local secret. A refused cleanup changes nothing. A
retired receipt cannot restore or recreate a secret through request replay; no Controller table
or secret state is used for cleanup.

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
replay identically. Controller rejects mixed shapes (400) and uplinks the host has not reported
as a physical NIC or bond (409). A duplicate uplink/VLAN or plain registration of the same
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
Registrations still cannot be deleted. See [the plan](plans/2026-09-09-pool-vlan-networks.md).

**Host-held pool state and Controller replacement (2026-09-09):** `POST /api/v1/agent/enroll`
from an already enrolled host targeting a *different* Controller URL or trust is a replacement:
the agent (`nex-core-agent enroll` or the local `POST /api/local/v1/enrollment`) takes a fresh
key and host identity there, keeps its state directory and managed journal, and drops pending
results for the previous Controller; `--replace-host` is refused in that case. Only the same
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
under ID `<operation-id>:start` with kind `vm-power` and status `failed`, and received queued
work from the Core command journal. A non-executing orphaned **non-storage** pending entry is
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
from that raw outcome, while a replacement Controller records an unknown operation as passive
`adopt-outcome:<host>:<operation_id>` history) and `missing_vms` (IDs of `vms` entries whose persistent domain
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
  sockets × cores × threads must equal `count`. `model` is `host-passthrough` (the default),
  `host-model`, or a model from the assigned host's read-only `Inventory.cpu_models` list. Core
  obtains that list from libvirt domain capabilities (`usable=yes` named models on the
  host's default q35 machine), rechecks the selected named model immediately before
  defining the VM, and writes it as an exact custom model with partial feature checking
  and fallback forbidden (not `check=full`, which refuses usable models under KVM). `max` uses libvirt's
  maximum CPU mode. Host passthrough is tied to that host's CPU; use a common baseline
  reported by every destination before planning a migration.
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
  defined and stopped, with `VM created but not started: <reason>` reported. Libvirt's own
  autostart replay at host boot (non-shared VMs) is outside this check. API impact: **changed
  contract** — same requests and results; the available-memory refusal moved from `vm-create`
  and stopped `vm-update` to starting, with the message above (BUG-0195).
- **`disks`.** One to eight. Each carries `storage_id`, a `name` unique within the VM
  (`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`; a name, not a path), `size_gib` 1–2048, `bus`
  `virtio|sata|scsi`, `thin` (`true` sparse, `false` preallocated) and `boot_order`. Disks are
  created inside the VM's exclusive directory on their storage target; Core checks exclusive
  ownership for every disk and free space per target, summing the disks that share one.
- **`nics`.** Zero to eight. Each carries `network_id`, `model`, `mac` and an optional `mtu`.
  `model` is `virtio|e1000|e1000e|rtl8139|vmxnet3` (`virtio` needs a guest driver). `mtu` is
  `0` for the network's own MTU and is **accepted only on `virtio`** (via `host_mtu`; other models
  refuse a value). A set `mtu` is between 1280 and 9216. An
  empty `mac` means the derived address (`protocol.DerivedMAC`: the `52:54:00` locally
  administered unicast prefix, two octets of `sha256(vm id)` and the interface index), which is
  stable per VM and interface and cannot collide within a VM. A manual `mac` must be six octets
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
- **Lifecycle.** For storage that is not pool-shared, `autostart` is the libvirt property
  (`virsh autostart`) set while the stopped guest is defined, so the host starts the VM on boot;
  it is not a start. A pool-shared VM has libvirt autostart disabled. Instead, its serialized
  Core worker makes at most one durable autostart attempt per VM per OS boot, recorded in the
  private Core state before observation or admission. It can start only a currently shut-off,
  already-recorded owner after fresh master admission, current storage/execution checks and a
  one-use Core permit. Agent restart, a later manual stop, master outage, failed validation or a
  failed start cannot create another attempt in that boot; this is neither HA nor failover.
  `start_after_create` is
  honoured by the host inside the same creation (changed contract, 2026-09-10): after every disk
  is created and the persistent definition is verified, the creation has **succeeded** whether
  or not the flag is set. Core then persists stage `starting`, issues the same single libvirt
  start `vm-power` uses and records the outcome (`started`, or the reason) in the entry's
  `start` field. That field is what makes the start happen once: a replayed command returns
  the durable result and never starts again, and a creation interrupted at the start step is
  reconciled by observing libvirt — `running` records `started`, anything else records the
  failure `interrupted before the automatic start completed`. A start that libvirt refuses
  leaves the creation `succeeded` and the VM defined with its disks, stopped, as a normal
  managed VM: power, console and media work on it and a replacement Controller adopts it. The
  host reports the failed start as its own outcome with ID `<operation-id>:start`, kind
  `vm-power`, status `failed` and error `VM created but not started: <reason>`, which the
  Controller records in Task history next to the succeeded creation; a failed or uncertain
  creation never boots. Controller issues no follow-on `vm-power` operation and no longer
  needs `vm-power-v1` for this; the operator sees the creation task and, only when the
  automatic start failed, one failed start task.

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
the VM is observed running, what needs it stopped; then freezes every automatic MAC into the
definition and queues `vm-update` at revision current + 1. One edit, power or media request per VM
is in flight at a time; a `vm-media` change updates the same current journal entry. Core classifies the difference from its journal with the shared
`NeedsStop` rule: name, CPU, memory, firmware, machine type, any boot order, an existing
disk's bus, an existing interface's network, model, MAC or MTU, and adding a SATA disk need a
shut-off guest, which is then redefined from the regenerated XML after new disks are allocated
and a `domrename`; a running guest takes disk and interface additions and removals
(`attach-device`/`detach-device --persistent`; a live detach completes only when the guest
releases the device, so a removal succeeds once persisted and the guest may keep the device
until its next restart), ISO insert, swap or eject
(`change-media --live --config`), description, guest OS hint and autostart. Disks are matched
by name and keep the target names journaled at creation; interfaces by MAC. Every VM now
carries one SATA CD drive, empty without an ISO, and q35 definitions carry spare PCIe root
ports for hot-plug. Removed disk files are detached and retained, never deleted. The
persistent definition and per-disk ownership are observed against the expected XML by device
identity; an interrupted edit is reconciled by observation only and otherwise reported as
`needs_attention`. The host's journal keeps every revision and reports the highest succeeded
one as the current definition; Controller refreshes its mirror from each report, so the
result is the host's word and a replaced Controller adopts the current definition and
revision. Older Core without `vm-update-v1` is refused with 409 at request time.

Evidence: [edit QA report](qa/vm-edit/checks.md).

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
swap discs mid-Setup, which is the workaround W3 recorded. `drive` names which drive a request
applies to: `install` (or omitted, which is what every Controller sent before this existed) or
`drivers`. `VMCreateSpec.drivers_media_id` is the durable record of the driver disc, journaled on
the pool master beside `media_id` and re-adopted the same way; it must differ from `media_id`.
Core resolves a drive to its libvirt target through the user aliases `ua-nex-cdrom-install` and
`ua-nex-cdrom-drivers`, so the target letters libvirt assigns never matter. A domain defined by
an older Core carries neither alias and exactly one cdrom, which is always the install drive, so
a host that predates this keeps working and simply has no drivers drive. A Controller must not
send `drive: "drivers"` to a host that does not report the capability.

**Changed contract, 0.2.8 — firmware, Secure Boot and TPM.** `VMBoot` gains `secure_boot`
(`on`, `off`, or omitted) and `tpm` (`2.0`, `none`, or omitted). Both are durable fields of the
VM definition on the pool master and return to a fresh Controller through the existing
inventory and adoption path; neither adds Controller-owned state.

Secure Boot is now stated explicitly in the domain rather than inferred by libvirt from
`firmware='efi'`. **Omitted means on, with the Microsoft keys enrolled** — exactly what libvirt
already selected for every UEFI VM defined before the field existed, so a definition re-sent
unchanged keeps the firmware its guest was installed with. `off` selects the plain OVMF build
with no enrolled keys, for unsigned boot media or drivers. Secure Boot requires UEFI; requesting
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

It answers **409** while the import is still queued or dispatched, **409** when another media
transfer holds the staging lock, and **409 when the import SUCCEEDED**: the ISO then lives on its
host, Core has no media-removal command, and Controller will not drop a record for bytes it
cannot delete. Removing a stored ISO therefore remains unavailable until Core gains that call —
this is a deliberate refusal, not an oversight. **404** for an unknown id.

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
are dropped, at most 32 addresses are reported, `os` is bounded to 4096 bytes and `hostname`
and interface names to 200; Controller refuses a report beyond those limits or with an
unknown `agent`, `clipboard`, `media` or `drivers_media` value (an absent `media` is an older
Core). `agent` is `connected`, `unresponsive`, `disconnected` or
`no-channel`; `clipboard` is `ready`, `waiting` or `no-channel`; `resize` is true only when the
guest can actually follow a console resize request — the VM has a VirtIO display **and** the
guest has opened the qemu-vdagent channel, which is the agent that applies the new resolution.
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
delays VM power states, and facts are dropped when the VM leaves the running list. This is
observation, not health: a connected agent proves nothing about guest applications.

Compatibility: shared upgrade rule; an older Core never sends the member and the UI shows guest
tools as unknown. No pool-scoped setting is introduced.
Evidence: [console features QA report](qa/console-features/checks.md).

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

Not yet delivered: no operator-facing surface renders these figures, so Phase 2 roadmap item 8
remains open. Unverified: no host has run this probe; the units and key names are read from
libvirt's reference, and `domstats` has not been confirmed on Core's read-only connection.

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

Controller apply writes `pending-update.json` under the state directory and returns
202; the root `nex-controller-update` path unit runs `/usr/local/lib/nex/apply-update`,
which runs `apt-get update` then `apt-get install --only-upgrade nex-controller` and
restarts the service; running outside `nex-controller.service` keeps apt from being killed by
that restart, and a failed upgrade restarts the service before reporting. If the helper or path unit is missing, apply
returns **503** instead of scheduling. Core apply starts the same work in the
background and returns 202; the UI polls GET until `phase` is `done` or `failed`.
Both sides re-read the installed version afterwards and fail the apply if it did not
advance, because apt exits 0 when nothing needed upgrading.

Packaged installs and current appliance images include the update helper so Controller VMs can
upgrade in place.

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
