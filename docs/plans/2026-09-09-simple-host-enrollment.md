# Simple host enrollment (Add host by address + root password)

Date: 2026-09-09
Status: Implemented and covered by Go/Python/simulated-browser checks; disposable-lab
walkthrough against a real host passed on 2026-09-09; see the verification status below.

## Goal

Replace the operator workflow of minting a single-use enrollment token in the Controller
UI and manually running `nex-core-agent enroll` on the host, with the Controller pushing
enrollment directly: the operator types the host's address and root password in the
**Add host** panel, the Controller pushes the existing token contract to the host over
the host's own local HTTPS API (`nex-core-local`, port 9443), and the host enrolls itself.

## Agreed design

- **Why the host-local API instead of SSH:** `nex-core-agent enroll` needs the token,
  Controller URL and CA on the target host's filesystem. Root SSH login is disabled by
  the installer (see [`installer/late-install.sh`](../../installer/late-install.sh) and
  [install guide](../guide/install-nex-core.md)), so the Controller cannot reach the host
  by SSH to place them. `nex-core-local` already runs a root-authenticated HTTPS API on
  every Nex Core host; reusing it avoids re-enabling SSH or adding a second transport.
- **Why fingerprint confirmation is mandatory:** the Controller has no prior trust
  relationship with an unenrolled host, so the initial TLS connection cannot be verified
  against a CA. Instead, `POST /api/v1/hosts/probe` captures the host's certificate over
  an unauthenticated handshake and shows the operator its SHA-256 fingerprint, subject and
  expiry so they can compare it against the host's own console/install output before any
  credential is sent. The subsequent `POST /api/v1/sites/{id}/hosts` call re-verifies that
  exact fingerprint with a constant-time comparison during its own outbound handshake —
  a mismatch fails the handshake and aborts before the root password reaches the wire.
- **The underlying token contract is unchanged.** `POST /api/v1/enrollment-tokens`,
  `POST /api/v1/agent/enroll`, and site-bound/revoked-host re-enrollment rules are reused
  exactly; this feature only adds a Controller-initiated way to deliver that same token to
  a host that can be reached over HTTPS, plus a host-local endpoint to receive it. The
  previous manual flow remains available (now inside a collapsed "Manual enrollment"
  disclosure) for hosts the Controller cannot reach — an isolated network segment, a
  firewalled management interface, or a host behind NAT.

## Scope

In scope:
- Host-local `POST /api/local/v1/enrollment` (internal/localcore/enroll.go): accepts a
  pushed `{controller_url, ca_pem, token, replace_host?}`, serialized per host, calling the
  existing `agent.Enroll`.
- Controller `POST /api/v1/hosts/probe` and `POST /api/v1/sites/{id}/hosts`
  (internal/controller/addhost.go): certificate probe, then login + token mint + push.
- Controller UI "Add host" panel rework: address/password/controller-address fields,
  Connect → fingerprint confirmation → Confirm and add host, with the manual token flow
  kept as a documented fallback.

Out of scope (unchanged): joining a second host to an existing pool as a member, changing
a pool's master, ejecting a member (Phase 4), and any change to the token/certificate
contract itself.

## API impact

**New contract.** Three additions, one on each side of the existing enrollment split:

| Operation | Contract |
|---|---|
| `POST /api/local/v1/enrollment` | New host-local route (`localEnroll`), admin-session + CSRF, delivers a token pushed by a Controller to the local agent. |
| `POST /api/v1/hosts/probe` | New Controller route (`probeHost`), admin-only, unauthenticated-TLS certificate capture for operator confirmation. |
| `POST /api/v1/sites/{id}/hosts` | New Controller route (`addSiteHost`), admin-only, drives login + token mint + push against a confirmed host. |

The token issuance itself reuses `c.mintEnrollmentToken`, refactored out of the existing
`c.token` handler so both the manual and address-based paths share one implementation,
one audit event shape, and one site/revoked-host validation rule
(`c.validateEnrollmentTarget`). No change to `agent.Enroll`, `POST /api/v1/agent/enroll`,
the tokens table schema, or Core protocol version 1.

## Guide impact

**Updated.** [`docs/guide/manage-controller.md`](../guide/manage-controller.md) now
documents "Add host" as the primary path (address, root password, controller address,
fingerprint confirmation, success, and the wrong-password/fingerprint-mismatch/
host-unreachable/already-enrolled/revoked-host-re-enrollment error cases) and keeps the
manual token + `nex-core-agent enroll` procedure as the documented fallback for hosts the
Controller cannot reach.

## Acceptance criteria

- Operator can add a reachable host by typing its address and root password, without
  running any command on the host, and see it appear as the master of a new pool in the
  selected site after its first report.
- A fingerprint mismatch, wrong password, unreachable host, already-enrolled host, and
  re-enrollment of a revoked host each surface a distinct, honest error before or without
  side effects (no unused token rows, no partial enrollment).
- The manual token flow keeps working unchanged for hosts the Controller cannot reach.
- `docs/API.md`, both OpenAPI contracts and their generated HTML are current; the
  documentation checker and contract tests pass.

## Verification status

Recorded 2026-09-09.

- Go: `go test -race ./...` passes, including the Controller add-host tests (real TLS listener,
  fake host-local double that calls `agent.Enroll`) and the host-local enrollment tests. The
  Controller test listener is now self-signed and not chained to the host CA, which caught the
  trust bug below.
- Contracts and docs: `python3 scripts/generate_api.py --check`, the Python unit suite (DTO and
  route coverage for both contracts), and `python3 scripts/check_docs.py --all` pass.
- UI: typecheck and build pass; `tests/integration/phase1.py --browser` exercises the panel with
  stubbed responses (simulated) alongside the manual fallback.
- Real lab: `tests/integration/add_host_lab.py` passed 12 checks against the disposable nested-KVM
  lab (real Nex Core host, real Controller appliance running the working-tree binary), plus a
  fresh-host enrollment through a second local service instance, a service restart, and log/audit
  hygiene. Evidence and limits: `docs/qa/simple-host-enrollment/lab/checks.md`.

Bug found and fixed during lab preparation: the push sent the host-identity CA as the host's
trust; the appliance serves a self-signed HTTPS certificate, so enrollment would have failed TLS.
The Controller now pushes its served certificate (`-tls-cert`) and answers 503 before any host
contact when none is configured.

Outstanding: package upgrade path, physical hardware, hosts on a different L2 segment, and the
manual token fallback in a real lab.
