# Add host: enrollment against a DHCP appliance, and making enroll-then-join obvious

Date: 2026-09-23. Status: **all three slices implemented in the working tree** (BUG-0531,
BUG-0532, and the address prefill). Owner: project owner.
Parent: [platform plan](2026-09-08-nex-platform.md); related:
[pool master model](2026-09-09-pool-master-model.md), [Phase 4 closure](2026-09-19-phase4-closure.md).

## What happened

Adding a second host to a pool failed twice over, in the project owner's lab, on a bundled
Controller deployed with DHCP:

1. **Add host to pool** on the pool listed no host to add. The operator expected that dialog
   to take a fresh host's IP. It cannot: the Controller has no host discovery, and every
   enrolled host is already the master of its own one-host pool (decision: project owner,
   2026-09-09). The dialog only merges one of those pools into another. Enrollment is the
   site's **Add host**, which the dialog's empty state mentioned but nothing led to.
2. **Add host** with the appliance's IP as the Controller address failed with `x509: cannot
   validate certificate for <ip> because it doesn't contain any IP SANs` (BUG-0531).

## Decisions

- **Keep the model.** Enrollment and join stay two operations; no enrollment straight into a
  pool. A Controller that had to remember "join pool X once host Y reports" would drive a
  multi-step operation across the host's first report, which the architecture invariant
  forbids. Decision: project owner, 2026-09-23.
- **Keep the label "Add host to pool".** The dialog's content carries the meaning; renaming
  would touch the guide and four plans for no clearer word. Decision: project owner, 2026-09-23.

## Slice 1: certificate server name (done)

The appliance deployed with DHCP is minted before it has an address, so its certificate names
only its hostname. The deploying host is told that name to verify by; hosts added later were
not. `POST /api/local/v1/enrollment` and `nex-core-agent enroll` now take the name, and
`addSiteHost` sends its certificate's first DNS name only when the certificate does not name
the Controller address. API impact: changed contract (host-local enrollment). State owner:
core, in the host's agent state. Evidence and limits: BUG-0531 in the bug ledger.

## Slice 2: make enroll-then-join obvious (done)

All UI text and placement in `web/src/pool-members.tsx` and `web/src/main.tsx`; API impact
none, state none.

1. The dialog opens with the model: "This pool grows by joining a host already enrolled in
   this site. Every enrolled host starts as the master of its own pool."
2. With nothing to list, the dialog shows one action, **Enroll a new host…**, which closes it
   and opens the site's Add host form (the pool members panel receives a callback from the
   page; it does not know about routes).
3. The **Peer address** field and the confirm button render only once a candidate is
   selected, so an empty dialog offers nothing that reads as "type an IP here".
4. The enrollment form's success line gains: "To make it a member of an existing pool, open
   that pool, then Hosts, then Add host to pool."
5. Guide: `join-a-pool.md` step 2 and the Add host section of `manage-controller.md`, with a
   browser walkthrough of the empty, selected and success states as the documentation gate
   requires.

Acceptance: an operator who has enrolled one host and installed a second can reach the
second host's enrollment from the pool dialog without reading the guide, and the join dialog
never shows an address field before a host is chosen. Evidence: BUG-0532 in the bug ledger
(render test and the browser harness in `tests/integration/site_selector_browser.py`).

## Slice 3: the operator sees only the address (done)

Decision: project owner, 2026-09-23. The scheme is always https and the port is the host's
own, so neither belongs in the dialog; the operator sees and, if ever needed, edits only the
address.

- **Core** reports `inventory.managed.peer_origin`: its own peer address worked out at report
  time from its configured peer listener, the way a master already works out its own. It is
  an observation, not a record; the join still records the member's peer origin durably on
  the master. `DefaultPoolPeerPort` (9444) moved to the protocol package so agent and
  Controller share one value.
- **Controller** mirrors that field with the other per-host observations it keeps for
  display, and the join preview carries `peer_address` and `peer_port` per candidate (the
  default port for a host that reports none). The POST join contract is unchanged.
- **Dialog**: choosing a host prefills **Address**; the origin is composed from the address
  and the candidate's port. Anything but a bare address is refused, never guessed at.

API impact: changed contract, additive (Core inventory field and join-candidates fields).
State owner: none (observation; the durable record is the existing membership entry).
Evidence: agent, Controller and render tests plus the browser harness, all named in the
change record.
