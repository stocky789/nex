# Pool master model verification

Date: 2026-09-09
Status: Local verification of the uncommitted pool-master slice passed; two unrelated
unit-test failures and the native/hardware paths remain open (see Limits).

## Scope and reference

Verify the [pool master model](../../plans/2026-09-09-pool-master-model.md): administrators
never create pools, every enrolling Nex Core host becomes the master of an automatically
created pool named after its hostname, and enrollment tokens bind to a site. This record
covers the updated process/browser integration checks and the inspected captures for the
new empty-site, Add host, Pool master and role-badge presentation.

API impact: **changed contract** (verified here through the integration harness):
`POST /api/v1/pools` removed (404), `POST /api/v1/enrollment-tokens` takes `site_id`
(400 for an unknown site, 409 for a re-enrollment `host_id` that is not revoked),
`POST /api/v1/agent/enroll` creates the pool with `master_host_id`, and `GET /api/v1/pools`
returns `master_host_id`. Guide impact: **updated** by the documentation owner; guide
Verification sections point here.

## Environment and method

- Git HEAD `1fb97824a9acdcfc25e071eb48c18e9645ca2c16` plus the **uncommitted working tree**
  (backend, UI, docs and tests for this slice).
- Local Linux workstation; Go from the vendored toolchain `.cache/toolchains/go` (the system
  Go lacks the `LICENSE` file that `scripts/build-source.py` copies); Playwright Chromium in
  `.cache/phase1-qa-venv`; disposable local HTTPS Controller processes with temporary SQLite
  state. No live host, disk or network was changed.
- **Simulation:** every host in the browser suites is simulated. `phase1.py` runs the real
  agent binary against a fake `virsh`; `managed_vm_browser.py`, `host_iso_browser.py` and
  `website_controller_captures.py` post scripted mTLS reports; `site_selector_browser.py`
  uses route fixtures. Real Controller code, TLS/mTLS enrollment and the built React UI are
  exercised; no libvirt, storage or guest behavior is established.

## Test changes

`tests/integration/*.py` no longer POST `/api/v1/pools`, pass `pool_id` to enrollment
tokens or send `pool_name` to deploy/bootstrap. Fixtures enroll a host and read its pool
from `GET /api/v1/hosts`/`/api/v1/pools`. New assertions: the manual pool route returns
404; a token for an unknown site returns 400; a re-enrollment token for a live host returns
409; the pool created by enrollment is named after the host, lists it as `master_host_id`,
belongs to the token site and has a `pool.create` audit row; after revoke/re-enroll the
host keeps its pool and master role; the site empty state reads "Add your first host" with
an "Add host" button and "No hosts in this site." in the tree; "Create pool" no longer
exists; the File menu "Add host" is enabled for a selected site; the form title is
"Add host to {site}"; the pool page shows "Pool master" with the hostname and "The pool
master controls the pool."; the host badge reads "Pool master" (no "Member"); the tree
suffixes the master with " · master". `website_controller_captures.py` also had a stale
expectation for a site tree link (removed earlier by the site selector); it now checks the
site picker option.

## Commands and results

| Command | Result | Time |
|---|---|---|
| `go test -race ./...` (re-run with `-count=1`; first run was fully cached) | pass | 36 s |
| `python3 scripts/generate_api.py --check` | pass | 1 s |
| `python3 -m unittest discover -s tests -p 'test_*.py' -v` | **fail** (70 ok, 1 error, 1 failure; see below) | 1 s |
| `npm --prefix web run typecheck && npm --prefix web run build` | pass | 2 s |
| `GO=$PWD/.cache/toolchains/go/bin/go .cache/phase1-qa-venv/bin/python tests/integration/website_controller_captures.py --output .cache/website-controller-captures/images --evidence .cache/website-controller-captures/results.json` | pass (3/3) | 5 s |
| `GO=... NEX_QA_CAPTURES=.cache/phase1-captures .cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser` | pass (16/16) | 31 s |
| `GO=... .cache/phase1-qa-venv/bin/python tests/integration/managed_vm_browser.py --simulated --evidence .cache/managed-vm-browser` | pass | 36 s |
| `GO=... .cache/phase1-qa-venv/bin/python tests/integration/host_iso_browser.py --evidence .cache/host-iso-browser` | pass | 7 s |
| `.cache/phase1-qa-venv/bin/python tests/integration/site_selector_browser.py --evidence .cache/site-selector-browser` | pass | 3 s |
| `go build -o dist/nex-core-local ./cmd/nex-core-local && .cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py --blocked-only` (with `--docker` pointing at a `sudo -n docker` wrapper because the local user is not in the docker group) | pass (VM commands simulated) | 16 s |
| `python3 scripts/check_docs.py --all` | pass ("Documentation structure passed; historical references not resolved and diff coverage NOT checked") | 1 s |

Unit-test failures, both unrelated to this slice and left in place:

- `test_website_hosting.WebsiteHosting` errors in `setUpClass` because nginx is not
  installed on this workstation; CI installs `nginx-core`.
- `test_api_contract.LocalAPIContractTests.test_all_runtime_api_routes_are_documented_and_implemented`
  fails because `POST /api/local/v1/enrollment` is implemented but not yet documented by a
  separate in-progress local enrollment slice.

The exact result-group strings for each suite are in [results.json](results.json).
The CI commands in `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` are
unchanged; CI uses `actions/setup-go` (a toolchain that ships `LICENSE`), installs `nginx-core`,
and uses the runner's docker, so the local `GO`/docker workarounds are not needed there.

## Inspected captures

| State | Capture | What it shows |
|---|---|---|
| Empty site (real Controller, `phase1.py`) | [empty-site-desktop](captures/empty-site-desktop.png) | Site "Browser walkthrough" with "Add your first host", the automatic-pool explanation, an "Add host" button, "No hosts in this site." in the tree and "Add host" on the toolbar; no "Create pool" anywhere. The status bar counts the other site's host (global counter). |
| Add host form | [add-host-form-desktop](captures/add-host-form-desktop.png) | "Add host to Browser walkthrough", help text "The new host becomes the master of a new pool in this site…", "Generate enrollment token". |
| Pool page (simulated host) | [pool-master-desktop](captures/pool-master-desktop.png) | Pool "simulated-browser-host" named after its host; "Pool master simulated-browser-host" metric; subtitle "The pool master controls the pool."; tree "simulated-browser-host · master" (the long name wraps onto two lines in the sidebar). |
| Host badge (simulated host) | [host-master-badge-desktop](captures/host-master-badge-desktop.png) | Host overview badge "Pool master" beside "fresh". |
| Host page, tree suffix (demo fixture) | [host-tree-master-suffix](captures/host-tree-master-suffix.png) | Pool "nex-core-01" containing "nex-core-01 · master" selected; "Pool master" badge. |
| Site empty state at 360 px (route fixtures) | [site-empty-360](captures/site-empty-360.png) | Narrow layout of "Add your first host" and "No hosts in this site." without horizontal overflow. |
| Tree with 500 fixture sites | [tree-fixture-master-suffix](captures/tree-fixture-master-suffix.png) | "Pool 000" containing "host-000 · master" and its guest. |

The `phase1.py` captures of the pool and host pages show the workstation hostname (the
real agent enrolls with `os.Hostname()`), so the neutral simulated/fixture captures are
kept here instead; the same assertions passed in the `phase1.py` run.

## Limits

- Real Nex Core hardware, a native libvirt host, and the appliance handoff
  (`appliances/bootstrap` and `appliances/adopt`) against a real Core host were not run.
  `managed_vm.py`, `fresh_install_browser.py` and `appliance-lab.py` were updated to the
  new contract but not executed; their disposable labs remain outstanding.
- Joining a member host to an existing pool, changing the pool master and ejecting a member
  are not implemented; nothing here verifies them.
- Hosted GitHub/Gitea Actions runs were not executed for this uncommitted change.
- The two unit-test failures above are outside this slice and stay open.

## Real lab verification

A later pass exercised the change against the retained disposable nested-KVM lab with a real
Controller appliance, real agent enrollment through libvirt, an in-place migration and a fresh
GUI deployment: see [lab/checks.md](lab/checks.md).
