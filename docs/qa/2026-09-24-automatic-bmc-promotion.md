# Automatic BMC promotion checks

## Scope

Development build, 2026-09-24. This record covers Core's two-host automatic promotion decision and the Controller display of Core's `automatic` status flag. These are simulated checks; no physical BMC or host was contacted.

## Method and results

- `go test ./internal/agent -run '^TestAutomaticPoolPromotion' -count=1` — passed. The focused tests cover unsafe preconditions, the old host returning, a Store reload after durable fence proof without a second fence, and a Host fencing opt-out propagated after that proof that pauses activation. Test fakes simulate hosts and fencing; this is not a hardware fence or outage test.
- `go test -race ./internal/agent -run '^TestAutomaticPoolPromotion' -count=1` — passed after the durable retry and Host fencing opt-out checks were added.
- `go test ./internal/agent ./internal/protocol ./internal/controller` — passed after the automatic code change.
- `go test ./...` — passed after the automatic, qdevice-parser and packaging changes were present in the shared worktree.
- `python3 -m unittest tests.test_api_contract` — 74 tests passed.
- `python3 scripts/generate_api.py --check` — passed after the duplicate-description cleanup; `index.html`, `local.html`, and `peer.html` are current.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'` — passed.
- `node src/pool-promotion.test.mjs` in `web/` — passed per the focused UI test run for this change; verifies rendering the automatic indicator from reported status.
- `npm run typecheck` in `web/` — passed.
- `npm run build` in `web/` — passed in the UI verification run for this change.
- Chromium component rendering with a simulated Core promotion report — the **automatic** label was visible and the screenshot was inspected. The capture remains in ignored local `.cache/automatic-promotion-browser/promotion.png`; it is not committed evidence. This was a component check, not a full Controller workflow.
- `python3 scripts/check_docs.py --worktree` — passed for the shared worktree after the installer assessment was added. This is a structural documentation gate, not semantic acceptance.

## Limits and outstanding checks

The local Controller browser walkthrough below used fixture replies and was not a host-backed workflow. No physical management controller, power-off, host reboot, failover outage, or workload recovery was exercised. The tests do not establish hardware fencing correctness or automatic VM restart; Core does not automatically restart VMs. Storage lease and Nex Witness do not trigger automatic promotion and remain planned.

## Local Controller browser walkthrough (2026-09-24)

- **Setup:** built the current development UI with `npm run build`, then started `cmd/nex-controller` on loopback with a fresh private temporary state directory, a disposable administrator password, and a temporary self-signed TLS certificate. Signed in through the real Controller login in Chromium 153.0.8010.12 at 1440×900. Playwright supplied the repository's fictional UI inventory as read-only API replies, with the promotion report set to `stage=activate_authority`, `status=pending`, `automatic=true`, and `cancellable=false`. No Core host was connected.
- **Reported status:** reran this capture with exactly two pool members, both reported healthy and fresh, each with fixture-reported ready Redfish checks from its peer; Host fencing was reported on and fencing readiness was 2 of 2. The page showed **2 hosts**, **Members 2**, **Fencing OK (2 of 2)**, and **Host fencing On**, plus **Candidate lab-node-02 · stage activate_authority · status pending · automatic**, the report time, and Core's cannot-cancelled message. Inspected `.cache/automatic-promotion-browser/automatic-status.png`.
- **Stale report:** supplied a stale, non-authoritative membership report and an old master observation time. The page showed **Not current**, explained that the pool master's state is unknown, and retained the candidate's reported automatic status. Inspected `stale-status.png`.
- **Read error:** made the local fixture return HTTP 503 for the membership read. The page showed the pool-master read failure and **Unavailable**, with no previous membership presented as current. Inspected `read-error.png`.
- **Loading:** delayed the fixture membership GET by five seconds. Before the reply, the Members page showed **Loading…** and **Asking the pool master for its membership…**, with **Refreshing…** disabled. Inspected `loading.png`; browser page errors: none.
- **Keyboard:** focused **Refresh** and pressed Enter; one `GET /api/v1/pools/{id}/members` request followed. Browser page errors: none.
- **Limits:** all pool and Core replies were simulated fixture data, including the two-host readiness report. The stale, read-error and loading captures use the earlier generic three-member fixture and do not claim promotion eligibility. No automatic promotion, fence proof, physical BMC, host power-off/reboot, failover outage, VM restart or workload recovery occurred. The screenshots are ignored local QA captures, not committed evidence.
