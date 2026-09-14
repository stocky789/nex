# Unresolved bug ledger implementation and local acceptance

Date: 2026-09-12
Status: scoped BUG-0093 through BUG-0096 implementation, independent review and
disposable local acceptance completed; BUG-0097 remains excluded.

## Authorized scope

Project owner requested completion of BUG-0093 through BUG-0096 using coordinated
Terra 5.6 medium workers, followed by independent review and primary-agent local
walkthroughs. BUG-0097, the nested UEFI crash, is explicitly excluded.

BUG-0094 brings forward the Core membership and peer reconciliation prerequisites
needed for pool-wide repository access. This does not establish completion of the
remaining Phase 4 gates, migration, master transfer, or HA. Preserve the existing
platform plan's requirement to reconcile applicable member configuration and never
report a member ready when required configuration is missing.

## Work ownership and acceptance

| Issue | Implementation lane | Required acceptance |
|---|---|---|
| BUG-0093 | Packaged Core service and confinement regression | Fresh packaged Core registers its ISO directory; fresh Controller adopts the identical Core record without manual registration |
| BUG-0094 | Core pool membership, peer authority, storage reconciliation | Two disposable Core hosts, real per-member access/failure, retry and Controller replacement; no second writable iSCSI owner |
| BUG-0095 | Core transient discovery, Controller transport, storage wizard | Authorized source/directory selection, manual fallback, empty/error/credential/stale/cancel paths in the real browser and native host lab |
| BUG-0096 | Health summary presentation and browser regressions | Current reasons, multiple affected checks, severity, stale/unknown/revoked states and direct detail navigation |

Work ownership follows [AGENTS.md](../../AGENTS.md#agent-workflow).

## API, guide and durable state

API impact: existing contract for BUG-0093 automatic storage inventory/adoption and
BUG-0096 check summaries; new/changed contracts for BUG-0094 Core peer membership,
repository reconciliation and observations, and BUG-0095 transient discovery.
Both ends, authorization, compatibility, bounded failure/cancellation and runnable
contract checks must ship with the API reference and machine-readable schemas.

Guide impact: updated for implemented discovery, member access and health workflows;
verification claims remain open until those actual steps have run.

State owner: Core. Existing default ISO registration stays in the Core managed
journal and returns through `Inventory.managed`. Pool membership, repository
generation, scoped member credentials and reconciliation outcomes must have durable
master-side records and a fresh-Controller inventory/adoption path. Discovery and
health presentation add no durable state; discovery results and passwords must not
enter Controller persistence or operation history.

## Local verification boundary

Use newly created disposable BIOS QEMU guests with private overlays, generated lab
credentials and loopback forwards. Existing workstation VMs, networks and storage
remain outside the test targets. Native services and backing fixtures run inside
the disposable guests. UEFI launch is excluded from these scenarios.

Checks and evidence follow [AGENTS.md](../../AGENTS.md#verification-and-bug-memory).

## Completed evidence

The [primary walkthrough and final checks](../qa/2026-09-12-bug-ledger.md) record
packaged ISO registration, native guided discovery, health failure/recovery,
NFS/SMB member VM execution, access and peer failure recovery, generation catch-up,
safe detach and fresh Controller adoption. Independent review accepted these bug
slices after the recorded corrections. This is not whole-Phase-4 acceptance.
