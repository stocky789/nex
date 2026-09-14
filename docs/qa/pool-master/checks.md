# Pool master model verification

Date: 2026-09-09. Scope: [pool master model](../../plans/2026-09-09-pool-master-model.md). Result:
slice checks **passed**; two unrelated unit-test failures and native paths open (see Limits). Real lab
run: [lab/checks.md](lab/checks.md).

API impact verified: `POST /api/v1/pools` removed (404); `POST /api/v1/enrollment-tokens` takes
`site_id` (400 unknown site, 409 re-enrollment for a non-revoked host); `POST /api/v1/agent/enroll`
creates the pool with `master_host_id`; `GET /api/v1/pools` returns `master_host_id`.

## Environment

HEAD `1fb97824a9acdcfc25e071eb48c18e9645ca2c16` plus uncommitted tree. Local workstation; Go from
`.cache/toolchains/go` (system Go lacks the `LICENSE` that `build-source.py` copies); Playwright in
`.cache/phase1-qa-venv`; disposable HTTPS Controllers. **Simulation:** all browser-suite hosts —
`phase1.py` runs the real agent with fake `virsh`; `managed_vm_browser.py`, `host_iso_browser.py`,
`website_controller_captures.py` post scripted mTLS reports; `site_selector_browser.py` uses route
fixtures. No libvirt, storage or guest behavior established.

## Test changes

Integration tests no longer POST pools, pass `pool_id` to tokens or `pool_name` to deploy. New
assertions: pool route 404; unknown-site token 400; live-host re-enrollment token 409; enrollment pool
named after host, `master_host_id`, token site, `pool.create` audit; revoke/re-enroll keeps pool and
master; site empty state "Add your first host" with **Add host** and "No hosts in this site."; no
"Create pool"; File menu **Add host** enabled for a site; form "Add host to {site}"; pool page "Pool
master" and "The pool master controls the pool."; host badge "Pool master"; tree " · master" suffix.
`website_controller_captures.py` stale site-tree expectation now checks the site picker.

## Results

| Command | Result | Time |
|---|---|---|
| `go test -race ./...` (`-count=1`) | pass | 36 s |
| `python3 scripts/generate_api.py --check` | pass | 1 s |
| `python3 -m unittest discover -s tests -p 'test_*.py' -v` | **fail**: 70 ok, 1 error, 1 failure (unrelated, below) | 1 s |
| `npm --prefix web run typecheck && npm --prefix web run build` | pass | 2 s |
| `website_controller_captures.py --output … --evidence …` | pass (3/3) | 5 s |
| `phase1.py --browser` | pass (16/16) | 31 s |
| `managed_vm_browser.py --simulated` | pass | 36 s |
| `host_iso_browser.py` | pass | 7 s |
| `site_selector_browser.py` | pass | 3 s |
| `go build -o dist/nex-core-local ./cmd/nex-core-local && localcore_browser.py --blocked-only` (`--docker` via `sudo -n` wrapper) | pass (VM commands simulated) | 16 s |
| `python3 scripts/check_docs.py --all` | pass (structure only) | 1 s |

Unrelated failures: `test_website_hosting` needs nginx (not installed locally; CI installs
`nginx-core`); `test_all_runtime_api_routes_are_documented_and_implemented` fails on undocumented
`POST /api/local/v1/enrollment` from a separate in-progress slice. Result groups: [results.json](results.json).
CI workflows unchanged and need neither local workaround.

## Inspected captures

| State | Capture | Shows |
|---|---|---|
| Empty site (`phase1.py`) | `captures/empty-site-desktop.png` (not committed) | "Add your first host", automatic-pool text, **Add host** button and toolbar, "No hosts in this site."; no "Create pool"; status bar counts the other site's host (global) |
| Add host form | `captures/add-host-form-desktop.png` (not committed) | "Add host to Browser walkthrough", new-pool help, "Generate enrollment token" |
| Pool page (simulated) | `captures/pool-master-desktop.png` (not committed) | Pool named after host; "Pool master" metric and subtitle; tree " · master" (long name wraps) |
| Host badge (simulated) | `captures/host-master-badge-desktop.png` (not committed) | "Pool master" beside "fresh" |
| Tree suffix (demo fixture) | `captures/host-tree-master-suffix.png` (not committed) | "nex-core-01 · master" selected |
| Empty site 360 px (fixtures) | `captures/site-empty-360.png` (not committed) | No horizontal overflow |
| 500 fixture sites | `captures/tree-fixture-master-suffix.png` (not committed) | "host-000 · master" and guest |

`phase1.py` pool/host captures show the workstation hostname, so neutral fixture captures are kept;
the same assertions passed.

## Limits

- Native libvirt host, hardware and appliance bootstrap/adopt against real Core not run here;
  `managed_vm.py`, `fresh_install_browser.py`, `appliance-lab.py` updated but not executed (see
  [lab run](lab/checks.md)).
- Join, master change and eject not implemented. Hosted CI not run.
