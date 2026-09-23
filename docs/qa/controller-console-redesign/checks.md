# Controller console redesign: checks

Date: 2026-09-23. Plan: [Controller console redesign](../../plans/2026-09-23-controller-console-redesign.md)
(slice 7, guide and QA). Design contract: [console design guide](../../design/controller-console/README.md)
and its [reference](../../design/controller-console/reference.html).
Change record: [2026-09-23-controller-console-redesign.json](../../changes/2026-09-23-controller-console-redesign.json).
Run by the slice 7 agent; captures inspected by the same agent. An independent review followed
(see [Independent review](#independent-review)); its fixes and the reruns after them are recorded
below.

## Environment

- Working tree at commit `116eda9` plus the uncommitted slices 1–6 (console UI, VM tags) and the
  concurrent host uptime / NIC link-speed change. Go 1.27.1, Node 24.21, headless Chromium through
  Python Playwright 1.62.
- **Review Controller:** a local Controller built from the working tree, listening on loopback
  port 18607 with a private data directory, a self-signed certificate and the web UI built into
  `.cache/slice7/dist`. **Every read is simulated:** the harness answers all `GET /api/v1/…` reads
  from the labeled fixture `tests/integration/fixtures/ui-review.json` (fictional names, RFC 5737
  addresses; two sites, two pools, four hosts with one stale, six VMs, tags on three VMs, one
  25 GbE port, host uptime). Sign-in, session and anything not intercepted go to the real
  Controller. No Nex Core host is behind the fixture, so consoles never connect and writes the
  walkthrough makes are answered by the script, not by a host.
- **Phase 1 lab:** `tests/integration/phase1.py --browser` starts its own disposable Controller
  and agent with temporary state and a simulated `virsh`. Nothing touches real hardware, storage
  or host services.
- Captures and logs are under `.cache/slice7/` on the machine that ran them and are not committed.

## Commands and results

| Command | Result |
|---|---|
| `npm --prefix web test` | exit 0, 20 test files passed |
| `python3 -m unittest discover -s tests` (includes repository privacy) | 399 tests, OK, none skipped |
| `python3 tests/integration/ui_review_browser.py --url https://127.0.0.1:18607 --password-file FILE --captures .cache/slice7/captures` | 391 captures (64 views × 1440×900, 1024×768, 390×844 × light and dark, plus 5 views at 1440×2000), **no page errors, exit 1** on 10 findings: two real defects (D1, D2 below) |
| `NEX_QA_CAPTURES=.cache/slice7/phase1-captures python3 tests/integration/phase1.py --browser` | **fails** at the network-error recovery step (defect D3). With a temporary two-line workaround (close the panel before **Retry**, then go back) the whole run passed: 22 of 22 result groups. The workaround is not committed; the committed test asserts the intended behaviour |
| `.cache/slice7/walkthrough.py` (guide walkthrough, uncommitted) | 21 of 21 steps passed, no page errors |
| `python3 scripts/check_docs.py --worktree` | passed for 89 uncommitted paths |

**After the review fixes** (run by the lead and the fix agents on the same machine; the same
simulated fixture and disposable labs):

| Command | Result |
|---|---|
| `python3 tests/integration/ui_review_browser.py …` | exit 0, no findings, no page errors |
| `python3 tests/integration/phase1.py --browser` (lead, log `.cache/final/phase1.log`) | exit 0, every result group passed |
| `NEX_QA_CAPTURES=.cache/final/phase1b … phase1.py --browser` (docs/test fix agent, after restoring the revoked-versus-stale assertions) | exit 0, 22 of 22 result groups passed, including the restored revoked-versus-stale check (log `.cache/final/phase1b.log`) |
| The ten CI browser checks of `.github/workflows/phase1.yml`, updated for the new shell: `iso_url_browser.py`, `storage_repositories_browser.py`, `zfs_provisioning_browser.py`, `cluster_storage_browser.py`, `website_controller_captures.py`, `console_controls_browser.py`, `managed_vm_browser.py --simulated`, `vm_edit_browser.py`, `host_iso_browser.py`, `site_selector_browser.py` | all pass locally in their simulated modes after the fixes; their native modes need a disposable lab and were not run |
| `go test ./internal/...`, `python3 scripts/generate_api.py --check`, `python3 -m unittest discover -s tests`, `python3 scripts/check_docs.py --worktree` | pass |

## What changed in the checks

- The review harness drives the new shell: overview (and a second site), scope menu, search
  results, account menu, Add host and Create site dialogs, the VM list (filters by tag and view,
  group by host, Needs attention, bulk bar and bulk dialog), Create VM wizard, every VM panel tab,
  the More menu, force-stop confirmation, Edit hardware, Delete VM confirmation, shut-off and stale
  VMs, the host list, every host panel tab for a fresh and a stale host, a degraded host, the
  revoke dialog, all eight settings sections, Storage, Networks and the ISO library with their
  forms, Backups, Containers, Tasks, Events, both console focus pages and an unknown VM link. It
  fails on page errors, a missing control and horizontal scroll of the document or the page pane.
  Each view starts from the default scope.
- The fixture's `secure_boot: "enrolled"` (refused by the Controller on `PUT`) is now `"on"`; the
  fixture gained VM tags, the `vm-tags-v1` capability, NIC `speed_mbps` and host `uptime_seconds`.
- The phase 1 browser assertions (`tests/integration/phase1_browser.py`) now prove the same things
  through the new UI: account menu links; site creation from the scope menu; the add-host probe and
  confirm; a delayed token not leaking into another scope (the scope now changes by navigation
  because the dialog is modal); unknown host link and unknown route; injected stale host recovering
  on the next poll (panel badge **Stale** → **Online**); master role; global search (`/`, case,
  no match, Escape, settings jump); host list selection and Escape; keyboard panel tabs; the
  rail / top bar / slide-over layout; activity pill to **Tasks**; Back and Forward; **Create VM**;
  the create placement warning; every VM power path from the panel and its **More** menu (cancel,
  Escape, rejected with same-ID retry, focus return, queued Start once); the BUG-0096 health
  semantics now asserted on the overview's **Needs attention** list; failed and stalled probes;
  **Refresh inventory**; typed revoke; 360 px panel fit and tabs without page scroll; management
  network CIDR refusal and rollback; skip link; stale host after stopping the agent; an older
  inventory response not undoing a revoke; **Re-enroll**; idle expiry and **Sign out** from the
  account menu. The old tree, toolbar, Actions menu and Tasks strip assertions were removed with
  those elements.

## What was inspected

The slice 7 agent opened and compared these captures with the reference (rendered at 1440×900):
overview (desktop dark, narrow light), VM list filtered, VM panel overview, VM Configuration with
the tag editor, host panel overview (fresh and stale, dark), host Storage tab, Settings Members
(mid and narrow), Tasks (narrow dark), VM console page (dark), Create VM (narrow), the phase 1
host at 360 px and the network-error state at 360 px. The overview matches the reference's
structure (KPI tiles, utilisation chart with range control, Needs attention, placement map with the
stale hatch, recent tasks, backups and resilience tiles with their phase lines). Status badges keep
the square dot-and-text form; no side stripes or rounded pills were seen.

Guide walkthrough (browser, simulated fixture): rail routes and current marking; Backups and
Containers disabled with their phase lines; scope menu contents and one-pool sites; search by IP,
no match, Escape and settings jump; activity pill; theme cycle kept per browser; account menu;
Events dot clearing; Escape closing VM and host panels with focus back on the row link; settings
search and footer; narrow **Filters**; tag add (lowercased, sorted), invalid tag message, remove,
**Save tags** refused then retried with the same `request_id` and the full definition (tags
`production, staging`, Secure Boot `on`); search and Tags facet; **Customize columns** kept after
reload; editor disabled with *Requires Nex Core 0.4.8 or later on the pool master.* when the pool
master lacks `vm-tags-v1`; VM quick actions, **More**, Console route, Snapshots and HA phase lines,
**Edit hardware**, **Delete VM**; bulk Start dialog listing skipped VMs; **Create VM**; Register
storage / network and Upload ISO forms, **Rename**, **Remove ISO**; host quick actions, `25G` port
tile and Uptime, Networking **Stage this configuration**, Storage **System disk mirror**,
**Maintenance** opening Settings → Members; Members **Actions** and **Add host to pool**; Updates
**Check**; the revoke dialog.

## Defects found by this slice, and their resolution

All eight were fixed in `web/src` after this slice; the reruns above cover them.

- **D1 — no global search or activity pill below 900 px.** Fixed: a **Search** button opens the
  field over the top bar, and the pill keeps the pulse and the running count.
- **D2 — Members table overflows.** Fixed: role and Nex Core version sit under the host name, so
  the row fits; `ui_review_browser.py`, which fails on sideways scroll of the page pane, passes.
- **D3 — Retry unreachable when inventory fails with a panel open on a phone.** Fixed: an open
  panel carries the inventory alert and its **Retry**. The committed phase 1 assertion for the
  intended behaviour passes.
- **D4 — narrow Tasks table** cut Result, When and error text. Fixed for narrow widths.
- **D5 — Hosts and Virtual machines KPI tiles had no sparkline.** Fixed with sparklines sampled
  in the browser since the page opened (no API reports that history; see the plan's deviations).
- **D6 — a host panel opened outside the current scope had no usage.** Fixed.
- **D7 — leftover old-layout wording** ("Task history", "Refresh the view", **Open Networking**,
  the Start VM note). Fixed: links read **Tasks**, the wizard says **Networks**, and **Console**
  opens a stopped VM's console page, which offers **Start VM**.
- **D8 — ISO library location.** Resolved in the plan's direction: the **ISO library** now also
  appears below the repositories on **Storage** (`#/storage`) and stays a Settings section.

## Independent review

Done after slice 7, by a reviewer separate from the slice agents, over the UI, Core, API and
documentation. Findings and resolution:

- **CI browser findings U1–U9**, all fixed in `web/src`: Retry hidden under a wide panel on an
  inventory failure (now shown inside the panel, host badge Unknown); scope menu wider than a
  360 px screen; one host report without `os_version` or a stats reply without `series` blanking
  the app (guarded, plus an error boundary per screen); the activity pill not counting `queued`
  tasks; the ZFS wizard comparing hosts by object instead of id; unnamed `Section` regions; long
  task ids overflowing the host panel; no notice after **Start VM** on the console page; host
  panel tabs wrapping at 1440 px.
- **Independent UI review findings**, all fixed in `web/src`: focus not returned after the shell
  dialogs; update status only read on two routes; missing Events current conditions, Storage KPI
  sparkline, phase-gated backup line in Needs attention, Members **Joined** column and failed-task
  links; VMs shown Running while the inventory load failed (now Unknown, with Console and power
  disabled); panel tabs without arrow-key handling; the Events dot lighting for ordinary changes;
  Settings Overview order; a stale host's memory counted in the Memory KPI; plus dead code,
  duplicated helpers, `rgba` literals (the tokens test now rejects `rgb`/`rgba`/`hsl`) and
  lowercase labels.
- **Version** — this batch needs Nex Core 0.4.8 while the changelog still said 0.4.5, which was
  already tagged. Resolved: a `0.4.8` changelog section, guide and installer stamps and
  field-level "Introduced in development product 0.4.8" in the OpenAPI descriptions
  ([record](../../changes/2026-09-23-version-0.4.8.json)).
- **Older-Controller wording** — the API reference and tags guide said an older Controller clears
  tags. It does not: it decodes host reports strictly and rejects the whole report, so the host
  goes stale until the Controller is upgraded. Corrected, with the pool-authority-move limit for
  tags added.
- **Members joined time** — the Members table needed a Core-owned join time. Resolved with
  `members[].joined_at` from the master's membership record
  ([record](../../changes/2026-09-23-member-joined-at.json)).
- **Weakened phase 1 check** — the revoked-host assertion no longer told revoked from stale.
  Restored: the Needs attention row is `bad` for revoked and `warn` for stale, and only the
  revoked host's panel shows *Access revoked. Re-enroll this host to resume reporting.* The new
  UI shows no "N checks need attention" count; the check instead asserts both affected checks
  land in one row.
- **Guide and plan drift** — narrow-screen search, ISO library location, stopped-VM console,
  Retry inside a panel, Tasks links and the Members columns corrected in
  [Navigate the Controller console](../../guide/navigate-the-controller-console.md) and
  [Join a pool](../../guide/join-a-pool.md); plan deviations recorded.

## Final verification

Run by the lead on the final working tree on 2026-09-23 (logs under `.cache/final/`, not
committed): `npm --prefix web run typecheck`, `npm --prefix web test`, `npm --prefix web run build`,
`go test ./internal/...`, `python3 scripts/generate_api.py --check` and
`python3 -m unittest discover -s tests` all exit 0. Every CI browser check run the way
`.github/workflows/phase1.yml` runs it exits 0: `iso_url`, `storage_repositories`,
`zfs_provisioning`, `cluster_storage`, `website_controller_captures`, `console_controls`,
`managed_vm --simulated` (19 groups), `vm_edit`, `host_iso` and `site_selector`, plus
`phase1.py --browser`. Two test lines were updated in that run: `managed_vm` expects the
sentence-case **Shut off**, and its stacked-chart pixel count reads three canvas columns and keeps
the fullest, because a vertical grid line can land on the single middle column it used to read.

## Limits

- All screens except sign-in run against the simulated fixture; the fixture gives three hosts the
  same NICs and address, so per-host networking differences are not represented. No real Nex Core
  host was used.
- Writes in the walkthrough (tags save, power) were answered by the script. The Controller-side
  tags contract is covered by `internal/controller/vm_tags_test.go`; the plan's acceptance item
  "VM tags survive a Controller replacement in the lab" has **not** been run.
- Consoles never connect; console behaviour relies on earlier console QA.
- Guide pages that are host-local, CLI or installer procedures were not walked; for Controller
  pages only the entry path and named controls were checked, not real outcomes on a host.
- The ten CI browser checks ran in their simulated modes only; `managed_vm_browser.py` native mode
  and any other native-mode lab checks were not run.
- `joined_at` is covered by Go tests only; no browser walkthrough of the **Joined** column against
  a real pool has run.
- Visual judgement is one agent's pass over a sample of the 391 captures, not every image.
