# Host ISO visibility and site selection

Date: 2026-09-09.
Status: Implemented, deployed and verified with independent review.

## Behavior and scope

Each host's **Storage** tab now includes **Host ISO images**, filtered by both
host and pool. Rows show destination storage/directory, size and transfer results.
**Transferred · last verified** describes the successful import record. Dispatched
is not completed; stale/revoked hosts, unavailable Controller data and unverified
storage do not establish availability. This uses existing Nex transfer records,
not a host filesystem scanner.

Below **Resources**, one site dropdown opens a menu containing **Search sites**. The
tree starts with the selected site's pools, then hosts and VMs. Resource search
is scoped to that site. Direct links select the owning site; **Site overview**
opens the current site's details. Global task history remains accessible.

API impact: **existing contract** — reuse authenticated site/pool/host/VM/media/
storage/operation reads. ISO verification still follows Controller import →
host-scoped Core transfer/validation → reported operation result. No endpoint,
command, permission or machine-readable schema changed.
Guide impact: **updated** — the [VM guide](../../guide/create-virtual-machine.md)
and [Controller guide](../../guide/manage-controller.md) explain the new views.

## Verification

The UI owner ran `npm --prefix web run typecheck` and `npm --prefix web run build`.
The final reviewed build has app.js SHA-256
`fd82675f5553f6ccfbe3f07a2fa8cb7e46769975622b137cacec3baf19887288`
and app.css SHA-256
`570292dc1d37ffc9825190e8a90b014c5eecac3db9d449271445ad2c5757882e`.

The focused [host ISO browser check](../../../tests/integration/host_iso_browser.py)
uses a real Controller and simulated mTLS agent, plus explicitly injected browser
responses for status/isolation/error cases. The [site selector check](../../../tests/integration/site_selector_browser.py)
uses the actual built UI with a labeled 500-site browser API fixture; it does not
establish backend or physical multi-site performance. Both existing CI providers
run these commands on pushes and pull requests. Existing Phase 1 browser assertions
were updated for the new hierarchy without removing enrollment/recovery checks.

[Host ISO results](simulated-results.json) and the
[500-site results](../site-selector/results.json) passed against the same final
bundle. See their [host ISO walkthrough](browser.md) and
[site selection walkthrough](../site-selector/browser.md) for injected cases,
keyboard/narrow capture review and explicit limits. The existing
[Phase 1 browser/process suite](../site-selector/phase1-results.json) also passed
all 16 result groups after updating its old all-sites-tree navigation assumptions.

The [popup Tab regression](../site-selector/popup-tab-regression.json) retains an
actual failing Chromium check and its passing correction. The listbox is excluded
from sequential keyboard focus; arrow navigation stays on the popup search input.
BUG-0036 records this test and both CI runners.

The [native browser check](native-results.json) passed against the same final
bundle on the retained disposable Controller. It opened the site menu, searched
and selected the current site by keyboard; both actual ISO records appeared under
host Storage with their destination, exact bytes and transfer tasks. Desktop and
360-pixel captures were inspected, including the
[opened dropdown](native-site-picker.png). No test created or changed lab resources.

The [final local deployment](site-dropdown-deployment.json) replaced only the
Controller's web assets and corresponding source archive. The packaged backend
binary stayed byte-identical. Served hashes matched the tested build; rollback
copies were retained and temporary transport/SSH access removed. The previous
[two-control deployment](deployment.json) is retained only as historical evidence.

No hosted CI run, new ISO build, production deployment or new physical storage
verification is claimed. This is a local UI update above the installed
`0.1.0+managed1` package. [Independent review](review.md) checks the implementation,
guide/API applicability, regression evidence and native results.

Documentation structure and scoped assessment coverage are checked using the
repository checker. Range verification uses an isolated Git snapshot, with
this batch based on the preceding accepted local snapshot and unrelated current
work preserved as baseline. It does not commit or publish the shared checkout;
local command/results are retained in the repository cache.
