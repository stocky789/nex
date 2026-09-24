# BMC fence discovery (guided Set BMC access)

Status: implemented; lab acceptance outstanding (project owner direction 2026-09-24 — operators
enter a management-controller address and credentials; Nex picks the working method, Redfish vs
IPMI, and the system Id / plug). Core probe, Controller relay
(`POST /api/v1/pools/{id}/members/{host_id}/bmc-probe`), Detect in Set BMC access, API reference
and guide are in place. API impact: new contract. Durable state: none (see
`docs/changes/2026-09-24-bmc-fence-discovery.json`). Still open: the physical acceptance cases
below, and an interactive browser walkthrough against a live pool.

## Problem

Manual **Set BMC access…** today asks for kind (Redfish / IPMI), address, system Id or plug,
account and password. Operators who are not BMC-fluent do not know which protocol works, or that
Dell needs `System.Embedded.1` while others use `1`. In-band auto-provision already covers many
hosts; this plan is only for the fallback when Core cannot create a fence account itself
(`user_management_refused`, `no_local_interface`, `lan_disabled`, and similar).

Lab evidence (2026-09-24): a Dell iDRAC answered Redfish with `System.Embedded.1` and matching
UUID, while IPMI lanplus timed out (IPMI Over LAN disabled). Validation previously rejected that
Id (BUG-0546); that fix is separate and does not replace guided discovery.

## Architecture (non-negotiable)

Nex Core does the probe. Nex Controller only relays and shows results.

- A fresh Controller must recover nothing new: discovery is a one-shot operation result, not durable
  pool state. The durable record remains the fence profile on the pool master after a successful
  Set BMC access.
- The probe runs from a host that will later fence (normally the pool master), so reachability and
  TLS pins match production fencing — never from the operator's browser alone.
- Credentials for discovery use the existing one-time credential handoff; they are not stored on
  the Controller.

## Operator flow (implemented)

1. Open **Set BMC access…** on the host.
2. Enter the management controller **address** (and optionally account/password when needed for
   listing).
3. Select **Detect** (or equivalent). Core probes from the pool master:
   - HTTPS Redfish `/redfish/v1` (and `/Systems` once credentials are available)
   - IPMI RMCP+ power/status (lanplus)
4. UI shows what answered, recommends one method, and pre-fills system Id / plug when Redfish
   lists a single ComputerSystem (or a system whose UUID matches the host).
5. Operator confirms account/password if not already entered and submits **Set BMC access** as
   today — live identity proof unchanged.

No vendor branding in the UI (no "iDRAC" / "XClarity" product names as design sources). Detection
is by protocol behaviour, not by marketing name.

## Implemented API contract

- `POST /api/v1/pools/{id}/members/{host_id}/bmc-probe` accepts `{request_id, endpoint,
  username?, credential?: {secret}}` under the fence-profile session, CSRF and capability gates.
- Controller relays `bmc-probe` to the **master** with `member_host_id`, `endpoint`, optional
  `username` and a probe-only `credential_ref`; Core removes both private credential copies after
  the probe and expires an undelivered handoff on its own tick.
- `PoolResult.bmc_probe` reports `methods` and an optional `recommended` kind, target Id and
  reason. A failed probe carries Core's sentence in `PoolResult.error`. Neither side records
  discovery state; Set BMC access separately records the fence profile.

See `docs/API.md` and OpenAPI for the full request, result and error contract.

## Acceptance

- Dell-class BMC with Redfish only: detect recommends Redfish and pre-fills `System.Embedded.1`.
- BMC with IPMI only: detect recommends IPMI and plug `1` (or discovered plug).
- Both work: prefer Redfish when identity can be proved; say so in the UI in plain language.
- Neither works: one clear sentence (no protocol essay).
- Replacement test: Controller wipe does not lose a recorded fence profile; discovery itself
  leaves no Controller-owned state.
- Runnable contract tests for validation and probe result shape; disposable-lab or nested probe
  where possible. Physical BMC certification remains outstanding unless explicitly recorded.

## Out of scope for the first slice

- Changing in-band `nexfence` provisioning.
- Vendor-specific Redfish OEM actions beyond standard ComputerSystem Reset.
- Auto-enabling IPMI Over LAN on the BMC (firmware setting; operator or out-of-band tooling).
