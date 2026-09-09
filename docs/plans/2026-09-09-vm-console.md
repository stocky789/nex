# Browser VM console

Date: 2026-09-09
Status: implemented in Controller and the host-local page; verified in a disposable nested-KVM lab.
Central power controls followed in [VM power](2026-09-09-vm-power.md), so the "start it on its host" caveat below no longer applies.

## Scope and decisions

Matt asked for console access to VMs in both Nex Controller and the local Nex Core page.
This delivers the Phase 2 console item from the [platform plan](2026-09-08-nex-platform.md#2-one-host-vm-management):
authenticated browser console sessions with short-lived authorization, raw VNC never
exposed on the network. Central power controls, stopped-VM editing and deletion remain
the next slice, so a Controller-created VM is still started on its host for now; the
guide says so plainly.

Design (the shortest working path with the existing pieces):

- Every Nex-defined VM already has a VNC display on a libvirt UNIX socket. Core resolves it
  from the live domain XML and refuses anything else.
- Core stays outbound-only. Controller hands a single-use token to the host in the next
  report response; the host dials back over its enrolled mTLS identity with an HTTP/1.1
  `Upgrade` and the two hijacked connections are joined to the administrator's WebSocket.
- The browser client is noVNC (MPL-2.0, bundled unmodified; see [licensing](../LICENSING.md)).
  The server-side WebSocket framing is ~200 lines of original stdlib Go in `internal/wsock`,
  so no Go dependency was added.
- The host-local page bridges the same socket directly for any running local VM, since
  root on the host already owns them; it needs no Controller.
- Sessions are in-memory only: a Controller restart ends consoles and the browser says so.
  They are not pool-scoped settings, so no pool-master record or re-adoption path is needed.

API impact: **new contract** — `POST /api/v1/vms/{id}/console`, `GET /api/v1/vms/{id}/console/{ticket}`
(WebSocket), `POST /api/v1/agent/console/{token}` (host mTLS upgrade), `consoles` in the
report response, the `console-v1` capability and `GET /api/local/v1/vms/{id}/console`.
Recorded in the [API reference](../API.md#browser-vm-console) and both OpenAPI contracts.
Older Core ignores the new report member and is refused with 409 for consoles; older
Controller never issues console requests.

Guide impact: **updated** — [Open a VM console](../guide/vm-console.md), guide navigation,
and the creation page's availability note.

## Acceptance

- [x] Controller issues audited, session-bound, single-use 60-second tickets only for a
      known VM on a fresh, unrevoked, console-capable host observed running.
- [x] Host attaches only over its enrolled mTLS identity; other hosts, expired or reused
      tokens and duplicate tunnels are refused; the browser stream requires the same
      session user, VM and Origin, and times out honestly without a host tunnel.
- [x] Raw RFB bytes relay both ways; closing either side closes the other.
- [x] Host-local page bridges a running VM's local VNC socket with session and Origin checks.
- [x] Go, contract and documentation checks pass; both CI providers already run them.
- [x] Browser walkthroughs of both consoles against a running guest in a disposable
      nested-KVM lab, including blocked and disconnected states, with inspected captures.
- [x] Independent review of implementation and evidence.

Evidence: [console QA report](../qa/console/checks.md).

## Out of scope

Central power controls, multiple-viewer policy, console session listing/termination,
clipboard integration, serial consoles, recording, and role-scoped console permissions
(Phase 8). Physical hardware and hosted CI execution are not claimed.
