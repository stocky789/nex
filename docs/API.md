# Nex API reference

Status: 2026-09-08 — living reference established; no Nex API is implemented yet.

This is the entry point for all Nex API calls, functions/commands, events, and streams:
client → Nex Controller and Nex Core agent ↔ Nex Controller. Maintain it in the same
change as API behavior, following the [required API impact check](../AGENTS.md#required-api-impact-check)
and [delivery gates](plans/2026-09-08-nex-platform.md#api-requirement-for-every-change-and-phase).

## Implemented API inventory

| Surface | Current implementation | Contract/reference |
|---|---|---|
| Client-facing Nex Controller API | None; controller remains planned | No routes or OpenAPI document yet |
| Nex Core agent ↔ Controller protocol | None; agent remains planned | No commands, messages, or schemas yet |
| Console, event, or other streams | None | No implemented stream protocol yet |

The current code is the standalone installer under `installer/`, with ISO checks under
`tests/integration/`. Debian/libvirt tools installed on a host are upstream capabilities,
not implemented Nex API calls. No Nex API server, API website, base URL, authentication
procedure, or runnable Nex API examples exist yet. This inventory excludes generated
ISO contents and build caches.

## Contract and website delivery

Starting with the first API in Phase 1:

- Store version-controlled OpenAPI descriptions for implemented HTTP APIs under `docs/api/`.
  Add links here to each actual contract and its generated reference when they exist.
- Generate a browsable API reference from those contracts and make it available with
  Nex Controller, including offline access. Record the actual documentation URL and
  generation/validation commands here. A separately hosted public API website can reuse
  the same output; deployment is not part of the current installer slice.
- Document agent commands, events, and console/other stream messages alongside their
  transport endpoints. Keep wire schemas in one authoritative location and link them
  from this reference; an HTTP handshake description alone is not a stream contract.
- Keep client-facing and internal agent surfaces distinguishable. Documenting an agent
  command does not make it a public client API or permit browsers to contact hosts directly.
- Generate schema/operation details from the authoritative contracts; keep guidance and
  examples alongside them. Do not maintain separate hand-copied endpoint catalogs.
- Validate contracts and generated output, and check that implemented routes/message
  handlers and documented operations cover each other. Run focused behavior checks
  against the implementation; schema validity alone does not establish correctness.
- Label reference output with the API and product versions it describes. Keep released
  references available, record additions/changes/deprecations/removals with release and
  migration guidance, and define supported Controller/Core version combinations.

No empty OpenAPI files or invented routes are needed before implementation starts.

## Required information for each operation

Each implemented call, function/command, event, or stream must document:

| Area | Required detail |
|---|---|
| Identity and availability | Stable operation/message name; purpose; implemented, deprecated, or removed status; introduced/changed version |
| Transport and direction | HTTP method/path or message type; sender/receiver; connection setup; client-facing or internal audience |
| Access | Authentication, authorization and site/pool/host/resource scope; credential lifecycle where relevant |
| Inputs | Parameters and payload schemas, required fields, units, defaults, limits, validation, and redacted examples |
| Outputs | Response/result/event schemas, status/error codes, error meaning, and success/failure examples |
| Execution | Immediate response versus durable operation completion, IDs, progress/result retrieval, conflicts, cancellation if supported |
| Safety and recovery | Side effects, destructive preconditions, idempotency, retry rules, deadlines, ambiguous outcomes, reconnect/reconciliation |
| Observations and streams | Freshness/order semantics, disconnect behavior and resumption where applicable; unknown state handling |
| Compatibility and evidence | Supported versions/capabilities, deprecation/migration guidance, implementation links, and runnable contract/integration checks |

Mark inapplicable fields with a reason. Never include real credentials in examples, or
enable documentation examples to perform destructive actions against live infrastructure.

## Planned coverage by phase

These are capability areas from the roadmap, **not available API calls or agreed route names**.
Choose exact operations and payloads when implementing the relevant phase.

| Phase | Coverage to add as implemented |
|---|---|
| 1 | Administrator access, sites/pools/hosts, enrollment and certificate lifecycle, inventory, heartbeats/health, command delivery/results, durable operations |
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

Agents initiate outbound authenticated HTTPS connections to Controller; command direction
does not imply inbound host listeners. VM disks, migration, and backup payloads use authorized
host/storage data paths rather than the Controller database/API. Local recovery and running
VMs must survive Controller outages. Later Swarm integration remains a separate managed-VM
boundary; it does not put Docker directly on Nex Core.
