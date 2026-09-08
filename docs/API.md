# Nex API reference

Status: development product 0.1.0, HTTP API v1, Core protocol version 1.
Phase 1 implements administrator access, Sites → Pools → Hosts, certificate enrollment,
observations, and durable inventory refresh operations. The appliance bootstrap slice adds
host-local deployment/recovery and adoption of the existing Controller VM. Central VM
mutations and console/event streams are not implemented. Verification limits are recorded in the
[platform plan](plans/2026-09-08-nex-platform.md).

## Contract and generated reference

The [OpenAPI contract](api/openapi.json) is the authoritative HTTP operation/schema reference.
The [generated browsable reference](api/index.html) renders that same document, with no
external scripts, fonts, or network dependencies. Open the HTML alongside `reference.css`
and `openapi.json` for offline use. Controller serves it at `/api/docs/` and exposes the
contract at `/api/openapi.json`. These read-only routes require no login.

The [shared Go protocol](../internal/protocol/protocol.go) defines runtime message types.
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

## Controller appliance handoff

The host deployer uses the existing Controller administrator session and CSRF contract to
create an initial Site/Pool and a pool-bound enrollment intent. It persists a stable request
ID and random enrollment token before sending the bootstrap request. Identical retries
return the same membership; changed payloads conflict. Enrollment itself reuses outbound
verified HTTPS and host certificate identity, including replay with the same persisted CSR.

After the enrolled host reports fresh successful libvirt inventory, appliance adoption
registers the existing VM UUID, selected pool and assigned host, with a protected Controller
role. It does not create a second VM. Conflicting ownership or an unobserved VM blocks
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
`site_name` and `pool_name` absent or empty. Storage/bridge must exactly match the command's
selected engine configuration; request ID, network, password and resource validation reuse
ordinary deployment. Unknown fields, multiple JSON values and changed retry input are refused.

Run as root with the local service stopped on a clean replacement host: no existing agent
identity or normal Controller deployment. Preparation takes the same exclusive service lock,
persists `recovery-preparation.json`, and reconciles the original VM UUID on identical retry.
It verifies and boots the selected appliance, waits for pinned HTTPS readiness, then removes
the seed and retained temporary credentials. It skips Site/Pool creation, enrollment and
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
same Controller password validation. This changes browser authentication and bootstrap validation,
not outbound host certificate identity, agent commands or Core protocol version 1.

Both services send `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, restrictive camera/microphone/location
permissions and a same-origin content security policy that forbids framing, objects and
base URLs. HSTS is not enabled for this development workflow using self-signed certificates.
Verify the selected TLS identity independently; the headers do not establish certificate trust.

API impact: **changed contract** — administrator cookie names, idle expiry, login limits
and new-password validation changed on both services; routes and response fields are unchanged.

Core initiates outbound HTTPS to a separately verified Controller identity. Pool-bound
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
there is no separate command stream. Only `refresh-inventory` is implemented centrally, with no VM
start, stop, migration, cancellation, or automatic recovery command.

A host's management connection is independent of each check's health. A heartbeat without
new inventory does not validate old observations. Unknown/stale state cannot establish that
a VM has stopped or permit another writable execution. Local bounded probes and result
journaling let reporting and recovery continue independently of a stalled probe; controller
outage does not alter local guest execution. Process restart/reconnect requires fresh
observations and operation reconciliation. See the OpenAPI schemas for timestamp units,
nullable observations, ordering, command results, and acknowledgement fields.

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
| 2 | Local storage and existing networks, ISO handling, VM configuration/lifecycle, placement checks, console authorization/streaming |
| 3 | Installer/first-boot enrollment integration and package/version compatibility |
| 4 | Shared NFS access per host, network changes and recovery, placement, maintenance and removal |
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
