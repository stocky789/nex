# Nex API reference

Status: development product 0.1.0, HTTP API v1, Core protocol version 1.
Phase 1 implements administrator access, Sites → Pools (one master host + members) → Hosts,
certificate enrollment,
observations, and durable inventory refresh operations. The appliance bootstrap slice adds
host-local deployment/recovery and adoption of the existing Controller VM. The first managed-VM
slice adds local storage and existing-network registration, ISO transfer and creation of a
stopped VM; the VM creation slice replaces that flat shape with the full specification
(topology, memory modes, multiple disks and NICs, boot order, autostart).
The console slice adds ticketed browser/host-local VNC streams. Central power controls and event streams remain unimplemented. Verification limits are recorded in the
[platform plan](plans/2026-09-08-nex-platform.md).

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

The Python checks establish reference/source coverage and generated-output freshness.
The Go checks exercise authentication, enrollment, operations, and recovery; structural
coverage alone cannot prove runtime behavior or successful hardware integration.

The host-local API has a separate [OpenAPI contract](api/local-openapi.json) and
[offline generated reference](api/local.html). Its browser callers authenticate to the
selected Core host, independently of Controller availability; its credentials do not grant
Controller administration. Both references are generated and checked by the commands above.

## Sites, pools and pool masters

Administrators create sites only. Pools are never created by hand and `POST /api/v1/pools`
does not exist: every Nex Core host that enrolls becomes the **pool master** of a new pool,
created in the same transaction as the host record, in the token's site and named after the
host's hostname. `GET /api/v1/pools` returns `master_host_id`, and the audit log records
`pool.create` next to `host.enroll`. Enrollment tokens (`POST /api/v1/enrollment-tokens`)
take `{site_id, host_id?}`: an unknown site is 400, and re-enrollment (`host_id`) needs a
revoked host whose pool belongs to that site (409 otherwise). Re-enrollment keeps the host's
existing pool. Managed resources, VM creation and media keep the `pool_id` + `host_id`
contract; the host must belong to the selected pool.

Databases from the earlier manual-pool model are migrated on start: each pool with hosts gets
its earliest enrolled active host (or any host) as master, pools that never received a host
or resources are deleted, and pool-bound tokens/bootstrap records are recreated in the
site-bound form. Tokens expire within ten minutes, so this loses nothing durable.

Compatibility: Controller and Core development builds must be upgraded together. An older
Core local service whose handoff still sends `pool_name` to `POST /api/v1/appliances/bootstrap`
is rejected with 400 (unknown field); a handoff interrupted across the upgrade must be
restarted from the host. Older agents are unaffected because `POST /api/v1/agent/enroll` is
unchanged. Older UI builds that call `POST /api/v1/pools` or send `{pool_id}` to
`POST /api/v1/enrollment-tokens` receive 404 and 400 respectively.

Planned, **not available**: joining a second host to an existing pool as a member host,
changing the pool master, and ejecting a member (Phase 4 multi-host pools). A member host is
what XenServer calls a slave.

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
`POST /api/v1/agent/enroll` call. Controller outage during the host-local step surfaces as
an ordinary 502 to the browser; no partial state is left keyed only on Controller memory.

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
adoption. A stable adoption request ID makes a lost reply safe to reconcile. Read-only
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
Its `__Host-nex_local_session` cookie is `Secure`, `HttpOnly` and `SameSite=Strict`,
with `Path=/` and no `Domain`;
all mutations require the exact HTTPS `Origin`, including login, and authenticated
mutations also require `X-CSRF-Token`. Sessions have a 30-minute idle limit and eight-hour
absolute limit. Only authenticated mutations refresh the idle deadline; GET/HEAD polling
does not. Local sessions are held in memory and also expire on service restart.
JSON request bodies are limited to 32 KiB and reject unknown fields. The former separate
local `admin` credentials have no fallback and are removed by package initialization.
New logins follow OS root password changes; active sessions retain the deadlines above.
Deploy matching local service, PAM helper/configuration and web assets on upgrade, then
sign in again as `root`. Controller `admin` credentials remain separate.

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
Guide impact: **updated** — appliance setup, progress and recovery need the matching user
guide and executed walkthrough evidence; schema checks do not establish real guest deployment.

## Trust boundaries and compatibility

Clients communicate with Controller over HTTPS. Administrator login creates a
`__Host-nex_session` cookie (`Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`, no `Domain`);
subsequent mutations also
send `X-CSRF-Token` from the session response. Browser origins must match the Controller
HTTPS origin. Administrator authority currently spans all sites and pools; scoped roles
remain Phase 8 work. Credentials and tokens must not enter logs or diagnostic artifacts.

Both administrator services enforce a 30-minute idle deadline and eight-hour absolute
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
console account `admin` (root remains locked; SSH password auth remains off). This changes
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

Only protocol version `1` is supported by this development Controller/Core pair; unsupported
versions are rejected. Deploy matching development builds. There is no promised mixed-version
upgrade window yet. Preserve references with release tags; a future incompatible change must
record migration guidance and update both peers and their contract checks in one slice.

## Execution and observations

Configuration changes finish in their HTTP request. Inventory refresh is a durable operation:
clients persist a stable `request_id`, retrieve the same operation on retry, and inspect
operation state for completion. Agent reports transport commands and acknowledged results;
there is no separate command stream. `refresh-inventory` remains read-only; the managed-VM
commands below add registration, ISO transfer and stopped-VM creation. No central VM start,
stop, migration, cancellation, or automatic recovery command is implemented.

A host's management connection is independent of each check's health. A heartbeat without
new inventory does not validate old observations. Unknown/stale state cannot establish that
a VM has stopped or permit another writable execution. Local bounded probes and result
journaling let reporting and recovery continue independently of a stalled probe; controller
outage does not alter local guest execution. Process restart/reconnect requires fresh
observations and operation reconciliation. See the OpenAPI schemas for timestamp units,
nullable observations, ordering, command results, and acknowledgement fields.

## Managed VM creation

API impact: **new contract** for client storage/network registration, raw ISO upload,
stopped-VM creation and assigned-host media download; **changed contract** for the
existing VM list and capability-bearing agent reports/commands/results. Protocol version
remains `1`. Matching Core reports `managed-vm-v1` on each heartbeat; Controller rejects
new work for stale, revoked, wrong-pool or unsupported hosts and withholds managed commands
from reports lacking that capability. Older agents retain inventory refresh support.
Upgrade Controller before Core: older Controllers reject the new report fields through
strict JSON decoding. This is an additive capability gate, not a general mixed-version
upgrade guarantee.

The browser uses its administrator session, exact HTTPS Origin and CSRF token. The shared
`Command` transports one storage, network, media or VM specification through the existing
outbound report response. `storage-register` and `network-register` check an existing
local directory or bridge/active libvirt network without formatting or configuring networks.

**Pool VLAN networks (changed contract, 2026-09-09):** `POST /api/v1/networks` also accepts
`kind: "vlan"` with `uplink` (host NIC or bond name, at most 10 characters) and `vlan`
(0–4094; 0 = untagged). `source` must be omitted; Controller derives the bridge name
`<uplink>v<vlan>` (for example `nex1v100`) before replay comparison, so identical retries
replay identically. Controller rejects mixed shapes (400), uplinks the host has not reported
as a physical NIC or bond (409) and a duplicate uplink/VLAN or plain registration of the same
derived bridge on that host (409). The `network-register` command then creates the 802.1Q
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
managed journal. `pool_id`/`host_id` inside entries are the creating Controller's and are
rewritten on adoption; resource IDs are stable. On each managed report Controller adopts, under
actor `system` with `request_id adopt-<kind>:<host>:<id>` (`storage-register`,
`network-register`, `iso-import`, `vm-create`), every entry it does not hold, reusing the ID
and recording the operation as already succeeded without dispatching a command. Entries that
duplicate an existing ID, storage path, network (kind, source) or VM name, and invalid entries,
are skipped. The default local storage **ISO** for `/var/lib/libvirt/images`
(`request_id auto-storage:<host>`) and the untagged default network `<port> untagged` for each
host bridge whose port is a physical NIC or bond (`request_id auto-network:<host>:<bridge>`,
kind `bridge` with `uplink` set) are created by Controller under actor `system` when absent; a
registration for that path or bridge in any state, including failed, suppresses re-creation.
On the host, `vm-create` resolves storage, network and media references by
resource ID rather than by pool/host ID, so creation works after re-enrollment under a new
Controller. Result: replacing a Controller means installing a new one and connecting each
pool master (token, or address plus root password); storage, networks, ISO records and VM
definitions return with their IDs and names. Not recovered, because it is Controller-scoped:
sites, administrator accounts and roles, audit history, enrollment tokens and the Controller
CA. Each pool master lands in a newly created pool named after its hostname on the new
Controller, as enrollment already does.
`iso-import` fetches only the assigned media from the enrolled Controller over host mTLS;
redirects and another host's media are refused. Controller stages a bounded raw upload,
computes its size/SHA-256 and checks its ISO descriptor. Core checks all three again before
publishing read-only media. See the OpenAPI operations for upload limits and errors.

`vm-create` preserves pool membership separately from host placement, allocates a stable
UUID and requires successful registrations/media for the same host and pool. Core rechecks
storage, networking, capacity and firmware, then creates every disk and defines a stopped
guest through system libvirt. It does not start the VM. The shared host engine's
`NewManaged(stateDir)` selects its private journal;
`Execute(ctx, command, fetch)` validates scope and payload, serializes effects and returns
the durable `Result` separately from journal/persistence errors. The agent supplies the
authenticated media fetcher; commands cannot supply a download URL.
The existing VM list still includes adopted Controller appliances with
`role: controller` and `protected: true`; generic guests add `spec`, `operation_id`,
`status` and optional `error`, with `role: guest`. Creation status never substitutes for
fresh libvirt execution state.

**Full VM specification (changed contract, 2026-09-10):** `POST /api/v1/vms`, the `vm-create`
command payload and the VM list/report inventory carry `VMCreateSpec` in its complete shape
([plan](plans/2026-09-10-vm-creation-system.md)). Bounds below are enforced identically on
Controller and Core by `protocol.ValidateVMSpec`; existence of referenced resources, pool-wide
MAC uniqueness and host capacity stay with their owners.

- **Identity.** `name` unique within the pool (`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`, reserved
  before dispatch), `description` at most 4096 characters, and `guest_os` (`""`, `linux`,
  `windows`, `other`) recorded for display and guest-tools advice. It installs nothing and does
  not change the VM's devices: disk bus and NIC model are chosen per device.
- **`cpu`.** `count` at least 1; Controller caps it at 64 and at host capacity. `sockets`,
  `cores` and `threads` all `0` select the documented default topology — one socket, `count`
  cores, one thread — instead of libvirt's own default; otherwise each must be at least 1 and
  sockets × cores × threads must equal `count`. `model` is `host-passthrough` (the default) or
  `host-model`, which is what keeps later migration to a non-identical host possible.
- **`memory`.** `size_mib` at least 512, capped by Controller at 262144, is the ceiling in both
  modes. `dedicated` reserves it: `<memory>`/`<currentMemory>` at the ceiling, no balloon device,
  and the whole amount counted against host capacity and never handed to another VM. `maximum`
  caps without reserving: the same ceiling plus a virtio balloon with free page reporting, so the
  guest returns pages it frees. Reclaim needs the balloon driver running in the guest (built into
  Linux; virtio-win guest tools on Windows) — without it a guest drifts to its ceiling and holds
  it, and this slice has no pressure-driven reclaim daemon. Capacity preflight uses the ceiling
  in both modes, so a VM that cannot reach its maximum is refused.
- **`disks`.** One to eight. Each carries `storage_id`, a `name` unique within the VM
  (`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`; a name, not a path), `size_gib` 1–2048, `bus`
  `virtio|sata|scsi`, `thin` (`true` sparse, `false` preallocated) and `boot_order`. Disks are
  created inside the VM's exclusive directory on their storage target; Core checks exclusive
  ownership for every disk and free space per target, summing the disks that share one.
- **`nics`.** Zero to eight. Each carries `network_id`, `model` `virtio|e1000e` and `mac`. An
  empty `mac` means the derived address (`protocol.DerivedMAC`: the `52:54:00` locally
  administered unicast prefix, two octets of `sha256(vm id)` and the interface index), which is
  stable per VM and interface and cannot collide within a VM. A manual `mac` must be six octets
  in canonical lower-case colon form, unicast and not all zero; that canonical form is what lets
  Controller enforce pool-wide uniqueness by plain string comparison.
- **Boot order.** `0` means "not a boot device". Non-zero values are unique across every disk and
  `boot.media_boot_order` and must number the boot devices `1..N` without gaps; at least one boot
  device is required. A non-zero `media_boot_order` requires `media_id`, and the install ISO must
  reside on a storage target the VM uses. Order is per device because libvirt forbids mixing
  per-device boot with an `<os><boot dev>` list.
- **`boot`.** `firmware` `uefi` (the default; requires EFI firmware advertised by the host's
  `domcapabilities`) or `bios`. `machine` is empty for Core's default (q35) or a plain machine
  name (`^[a-z0-9][a-z0-9._-]{0,31}$`).
- **Lifecycle.** `autostart` is the libvirt property (`virsh autostart`) set while the stopped
  guest is defined, so the host starts the VM on boot; it is not a start. `start_after_create` is
  **not** part of creation: when the create command succeeds, Controller issues the existing,
  proven `vm-power` `start` as a second operation. The managed create engine still never starts a
  guest, so a failed or uncertain creation never boots and the operator sees two honest tasks.
  The follow-on carries actor `system` and the stable `request_id start-after-create:<vm id>`, so
  it is one durable attempt and not a retry loop. If the host does not report `vm-power-v1`,
  Controller records that `vm-power` operation as `failed` with the reason instead of queueing a
  command: the VM exists, stopped, and the operator starts it explicitly. Adoption by a replaced
  Controller clears `start_after_create`, so re-adopting a pool never powers guests on.

`createVM` refusals, in the order they are evaluated: **400** for a client-supplied `id`, an
invalid `request_id` or name, any `protocol.ValidateVMSpec` violation (its message is returned
verbatim) and the Controller ceilings `cpu.count > 64` or `memory.size_mib > 262144`; **409** for
a replay whose payload changed, a host outside the named pool, a stale or revoked host, a
`media_id` on a storage target none of the VM's disks use, capacity above the host's observed CPU
or memory, a VM name already reserved in the pool, and a manual `mac` already used by another VM
in that pool (including another VM's derived addresses); **404** for any `disks[].storage_id`,
`nics[].network_id` or non-empty `media_id` that is not a registration this pool and host have
verified. A **200** replay returns the original VM and operation; a new request returns **202**.

API impact: **changed contract**. The VM creation request, the `vm-create` command payload and
the VM list/report inventory (`ManagedAppliance.spec`, `Inventory.managed.vms`) all carry the new
specification. Authentication, authorization, CSRF and origin checks, durable operations, replay
by `request_id`, result acknowledgement and audit attribution are the **existing contract** and
do not change; protocol version stays `1` and the capability stays `managed-vm-v1`. There is
**no compatibility shim**, because Nex is unreleased: no decode-only legacy fields, no
`managed-vm-v2` capability gate and no journal normalisation. Upgrade Controller and Core
together and recreate lab VMs and managed journal entries written in the earlier flat shape. The
Controller appliance is unaffected — it is built from `DeploySpec` through `makeDomain`, a
separate path from `VMCreateSpec`.

Every field of the specification is pool-scoped state, so its durable host-side record is the
**managed journal on the pool master**: each `vm-create` entry persists the full specification,
the generated domain XML and the path of every disk it created. Cores report succeeded entries in
`Inventory.managed.vms`, and a replaced Controller re-adopts them by resource ID (actor `system`,
`request_id adopt-vm:<host>:<id>`) with the VM's identity, name and shape intact and no
command redelivered; storage, network and media references resolve by resource ID on the host for
the same reason. No part of a VM definition exists only in the Controller database.

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

API impact: **new contract** — `powerVM` (`POST /api/v1/vms/{id}/power`), the `vm-power`
command with its `power` payload (`VMPowerRequest`) in the report response, the
`vm-power-v1` host capability and the `vm-power` operation/audit kind. Schemas, statuses and
limits are in the generated reference.

Flow: an administrator posts `{request_id, action}` with `start`, `shutdown`, `reboot` or
`force-stop`. Controller verifies the VM (adopted appliance or successfully created guest),
its assigned host (fresh, unrevoked, in the VM's pool, reporting `managed-vm-v1` and
`vm-power-v1`) and that no other power request for that VM is queued or dispatched, then
stores a durable operation exactly like other managed commands; the same `request_id`
replays the same operation. Core journals the stage before issuing the single libvirt
request (`start`, `shutdown`, `reboot`, `destroy`): start and force-stop are idempotent
against the observed state, shutdown and reboot are ACPI requests the guest may ignore, and
paused or transitional states are refused. An interruption around the request is reconciled
by observation only: `succeeded` when the observed state proves the outcome (running after
start, shut off after shutdown or force-stop), otherwise `needs_attention`; a request is
never re-issued. The observed VM state in host reports, not the task result, says what
happened. Power requests are not pool-scoped settings, so they are excluded from the applied
state a replaced Controller re-adopts. Older Core ignores the command with an
"unsupported agent command" failure only if the capability gate is bypassed; Controller
refuses with 409 when `vm-power-v1` is absent.

Checks: `internal/vmhost` (idempotent start/stop, refused shutdown, invalid action, paused
guest, both interruption points) and `internal/controller` (404/400/409 refusals,
capability gate, replay, pending-request conflict, dispatch payload, result, no leakage into
the VM list); the [console QA report](qa/console/checks.md) records the lab walkthrough
of Start, Reboot and Force stop from the browser.

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

Checks: `go test ./internal/wsock ./internal/vmhost ./internal/controller ./internal/agent ./internal/localcore`
cover framing, socket resolution, ticket authorization (unknown VM, stale host, missing
capability, stopped VM, wrong origin, wrong host, reuse, timeout) and end-to-end relay;
the [console QA report](qa/console/checks.md) records the disposable-lab walkthrough.

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
unknown `agent`/`clipboard` value. `agent` is `connected`, `unresponsive`, `disconnected` or
`no-channel`; `clipboard` is `ready`, `waiting` or `no-channel`; `resize` is true for a
VirtIO display. The probe is separate from the libvirt state probe, so a slow guest never
delays VM power states, and facts are dropped when the VM leaves the running list. This is
observation, not health: a connected agent proves nothing about guest applications.

Compatibility: upgrade Controller before Core. An older Controller rejects the new report
member through strict JSON decoding (the managed-VM rule); an older Core never sends it and
the UI shows guest tools as unknown. No pool-scoped setting is introduced.

Checks: `go test ./internal/vmhost` (definition devices, observation parsing, invalid IDs and
definitions, address filtering), `./internal/agent` (snapshot attaches facts to running VMs
only), `./internal/controller` (VM list passes `guest` through and omits it for stopped VMs);
`tests/test_api_contract.py` (schema parity); the
[console features QA report](qa/console-features/checks.md) records the disposable-lab
walkthrough with a Debian 13 guest.

## Software updates

API impact: **new contract** — Controller `GET /api/v1/updates` and
`POST /api/v1/updates/apply`; host-local `GET /api/local/v1/updates` and
`POST /api/local/v1/updates/apply`. Schemas `UpdateStatus` and `UpdateApplyResult`
are in the generated references.

Updates are apt operations against the signed Nex archive, not bare `.deb`
installs. Nex Core is a distribution: apt owns download, GPG verification and
dependency resolution, so a release that adds a dependency installs cleanly instead
of leaving dpkg half-configured. Clients read the archive's signed `Release`/`Packages`
index and no longer fetch `{repo}/updates.json`. The publisher still writes that file
for hosts released before this change, which poll it to discover releases; it is a
compatibility shim, not a contract current clients depend on.

Both sides read a package repo URL from `NEX_UPDATE_REPO`, else `/etc/nex/updates.conf`
(`repo_url=`), else `https://nexvirtualization.com/apt`. Plain HTTP repo URLs that
point at private or loopback addresses are treated as the public default. That URL is
written to `/etc/apt/sources.list.d/nex.sources` with
`Signed-By: /usr/share/keyrings/nex-archive-keyring.gpg`, which both packages ship, so
a repo changed after install takes effect without reinstalling. GET refreshes the
package index at most once every 5 minutes (stamped at `/var/lib/nex/apt-refresh.stamp`,
because the UI polls GET while an apply runs) and compares the installed Debian package
version against apt's candidate. A package no configured source offers is returned in
the JSON `error` field with HTTP 200. While an apply runs, GET also returns `phase`,
`progress` and `message` from `update-progress.json` in the service state directory.
`progress` reports fixed steps rather than download percentage.

Controller apply writes `pending-update.json` under the state directory and returns
202; the root `nex-controller-update` path unit runs `/usr/local/lib/nex/apply-update`,
which runs `apt-get update` then `apt-get install --only-upgrade nex-controller` and
restarts the service. Running outside `nex-controller.service` keeps apt from being
killed mid-transaction by the restart its own postinst triggers, and a failed upgrade
restarts the service before reporting. If the helper or path unit is missing, apply
returns **503** instead of scheduling. Core apply starts the same work in the
background and returns 202; the UI polls GET until `phase` is `done` or `failed`.
Both sides re-read the installed version afterwards and fail the apply if it did not
advance, because apt exits 0 when nothing needed upgrading.

Checks: `go test ./internal/nexupdate ./internal/controller ./internal/localcore`
(update-focused cases, including that apply invokes apt and never `dpkg -i`).
Operator UI is under Updates on Controller and the host-local page. Packaged installs
and current appliance images include the update helper so Controller VMs can upgrade
in place.

## Source access and availability

Controller's public `/source` route serves its configured matching Nex source archive and
returns an explicit unavailable response if no archive is configured. This is not a complete
ISO source-delivery claim: Debian, firmware, and other redistributed components retain their
own source/notice obligations under [licensing requirements](LICENSING.md).

API impact: **new contract** — client administration and Core enrollment/report/rotation
are implemented together, with generated reference and focused coverage checks.
Guide impact: **updated** — operator instructions belong in the [user guide](guide/README.md).

## Planned coverage by phase

These are capability areas from the roadmap, **not available API calls or agreed route names**.
Choose exact operations and payloads when implementing the relevant phase.

| Phase | Coverage to add as implemented |
|---|---|
| 2 (remaining) | Stopped-VM editing/deletion with disk retention (full VM creation, power requests and console streaming are implemented above) |
| 3 | Installer/first-boot enrollment integration and package/version compatibility |
| 4 | Multi-host pools (member join under the pool master, master change, member eject), shared NFS access per host, network changes and recovery, placement, maintenance and removal |
| 5 | Snapshot create/list/revert/delete, disk/configuration coverage and interrupted-operation recovery |
| 6 | Backup repositories/policies/jobs, manifests/catalogs, restore/verification, controller recovery |
| 7 | Migration preflight, relocation/live migration, evacuation, execution ownership and interrupted results |
| 8 | Scoped access, upgrades, operating/recovery procedures and supported-version reference review |
| 9 | HA configuration/status, quorum/fencing observations and commands respecting pool-local authority |
| 10 | Managed node VMs, Swarm integration, applications/services, volumes/secrets, backup/recovery and lifecycle |
| Agreed follow-on: [hardware passthrough](plans/2026-09-08-nex-platform.md#guided-hardware-passthrough) | Device/group inventory and readiness, host preparation/undo, exclusive VM assignment/removal, reset and reboot observations; reuse earlier inventory/configuration/task contracts and add missing capabilities |

Agents initiate outbound authenticated HTTPS connections to Controller; the separate
host-local listener is for local browser setup/recovery, not inbound Controller commands.
VM disks, migration, and backup payloads use authorized
host/storage data paths rather than the Controller database/API. Local recovery and running
VMs must survive Controller outages. Later Swarm integration remains a separate managed-VM
boundary; it does not put Docker directly on Nex Core.
