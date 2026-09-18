# Master-owned VM placement admission

Date: 2026-09-18. Status: implemented for development; native lab acceptance remains open. This is the
placement-authority part of Phase 4, not closure of Phase 3, multi-host acceptance,
master transfer, migration or HA.

## Scope

Move the create/update placement decision from Controller's resource and capacity
mirrors to Core. Controller retains administrator authorization, request validation,
host/pool routing, existing edit request normalization and idempotency. Core remains
responsible for the final revision, resource and runtime checks before changing a VM.
Explicit host selection remains; this adds no scheduler or automatic relocation.

The selected Core asks the pool master to approve the exact VM definition. The
master obtains a fresh native preflight from that host over the authenticated peer
API, verifies current membership and its shared-storage registrations, and records
the owner/specification or update reservation before acknowledging admission.
The master uses the same checks for its own local placement. Local-storage VMs are
included in definition authority; their ordinary local power controls retain their
existing independence from the master. Shared-storage execution fencing remains.

Preflight observes existing storage, networks, media, capacity and runtime support;
it must not allocate disks, change networking, define a guest or reconcile journals.
The target repeats execution-time validation because capacity and native resources
may change after approval. Peer calls are bounded and run outside pool-state locks.

## Contracts and state

API impact: **new/changed contracts**. Add an authenticated Core-master-to-member
placement preflight and an explicit acknowledgement to definition admission on the
existing pool VM peer API. Add `pool-vm-placement-v1` capability negotiation. Missing
capability, unsupported peers, stale membership or missing acknowledgement must
block definition changes rather than silently use Controller admission.
Public create/update request shapes and operation result tracking remain unchanged;
Core placement rejections appear in task results. API documentation and schemas ship
with the implementation.

State owner: **core**. Reuse the master's `pool-vms.json` owner/specification and
pending-update records and each target's managed VM operation journal. There is no
new Controller placement table or policy. A replacement Controller adopts the same
IDs and definitions through the existing authenticated Core managed inventory;
fresh native observations, not retained snapshots, establish current readiness.

## Acceptance

- Native preflight uses a coherent journal snapshot and leaves VM disks, definitions
  and operation journals unchanged on success, rejection and cancellation.
- Master and member creation/update cover local and shared storage; mismatched
  identity, generation, scope, resource readiness and legacy acknowledgements fail closed.
- Lost replies and exact retries retain one owner/definition; changed retries cannot
  replace it. Failed/uncertain updates keep the existing commit/abort recovery rules.
- Controller resource/capacity mirrors cannot grant placement or reject otherwise
  well-formed requests solely because their observations are absent or stale.
- Local-only power recovery still works with the master unavailable; shared execution
  keeps its exclusive-owner safeguards and no partition starts a replacement writer.
- Existing browser flows display upgrade-required and Core rejection results honestly;
  updated guide instructions distinguish request acceptance from successful creation.
- Focused race/contract checks, documentation/privacy gates and independent review pass.
  Record actual native, simulated and unexecuted coverage separately in QA before closure.

Guide impact: **updated** — creation/edit/media prerequisites, upgrade order,
asynchronous errors and safe retry guidance. Broader fault, physical-storage,
Controller-replacement and Phase 4 acceptance remain separately tracked when not
exercised by this slice's checks.

## Recorded outcome

Controller, Core, peer contracts and guide changes are implemented. Full Go race
checks, a final agent race rerun, API/reference checks, simulated browser workflows
and documentation/privacy gates passed; see the [QA report](../qa/2026-09-18-master-vm-placement.md).
The [two-node native lab](../qa/2026-09-18-native-placement-lab.md) now covers local
master/member creation, editing and start, shared NFS placement, exact retries,
negative resource validation and service restart continuity. Remaining acceptance:
Controller replacement, native storage identity loss, membership changes during
placement and interrupted multi-host definition recovery; preserve the broader
fault/recovery matrix before declaring this slice accepted.
No Phase 3 or Phase 4 closure, release, hosted CI run or live deployment is claimed.
