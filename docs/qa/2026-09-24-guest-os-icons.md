# Guest OS icons and detection verification

Date: 2026-09-24. Environment: development working tree; built Controller UI in
headless Chromium with simulated API inventory. No live host, VM, disk, network
or service was modified. No push or CI execution is claimed.

## Behavior and evidence

The VM list and Overview panel render locally bundled official-source icons in
18×18 CSS-pixel slots, retaining the guest-reported OS name. Unknown/missing IDs
and failed images use the monitor. Before guest facts arrive, configured Linux
and Windows families use family marks. Guest strings never become asset paths or
outbound URLs. Matching ignores ID capitalization, without guessing distro names
or inheriting `ID_LIKE` branding. Ubuntu flavours sharing `ubuntu` use Ubuntu.

The primary agent's Playwright walkthrough against the built app checked all
bundled mappings, image loading and dimensions, both themes at 1440×900,
1024×768 and 390×844, the VM Overview panel, Escape to close, an aborted image
request retaining the OS label, and empty inventory. Activating the Debian logo
with Enter opened a new tab without selecting the VM. That outbound URL was
intercepted with a test response. Large fixture lists were grouped by host so
all rows were rendered. Captures and a full asset contact sheet are local,
untracked artifacts under `.cache/browser-qa/guest-os/`, not shipped evidence.

The primary agent inspected the contact sheet and actual application captures.
Native image sizing preserves proportions; symbol/favicons replace unsuitable
wordmarks where available. Windows, Devuan and Nobara monochrome marks receive CSS theme
contrast. Review rejected Amazon Linux's tiny wordmark; its official RPM supplied
a compact bird symbol, displayed on a white tile without recoloring. No logo was redrawn. The source catalogue records
image treatment, attribution, exact official URLs and unresolved release terms.
CentOS editor workstation paths and AlmaLinux Adobe-only editing data were removed;
their visible paths/colors were retained. SVGs parse and contain no scripts,
event handlers, foreign objects, external resource references or workstation paths.

## Detection and API

A Luna agent traced QGA → Core observation → probe cache → inventory → Controller
validation → authenticated VM listing → UI. QGA's
[official protocol reference](https://www.qemu.org/docs/master/interop/qemu-ga-ref)
defines Linux/os-release IDs and Windows `mswindows`. Core already requested
`guest-get-osinfo`; this slice carries its optional `id` as `guest.os_id`, bounded
to 200 bytes. Existing name handling, Windows 11 build correction and host-stale
behavior remain in place. Controller adds no guest commands or durable state.

Independent review found an initial omission in the probe cache; the ID is now
retained and the two-probe regression detects removal of that assignment. Fixtures
cover common Linux IDs, Windows 10/11/Server, missing IDs, bounded hostile data,
cache replay, authenticated report → VM listing transport and oversized-ID rejection.
These establish parsing/transport, not installation and detection on every real OS.
Live Linux/Windows guests and Controller replacement were not exercised; no phase
or live-environment acceptance is claimed.

## Checks

The following checks passed in the shared working tree:

```sh
go test ./internal/vmhost ./internal/agent ./internal/controller -run 'TestObserveGuest|TestGuestOSInfo|TestGuestProbeCache|TestManagedWorkflowScopeRetryAndMedia|TestReportRejectsInvalidInventory' -count=1
python3 -m unittest discover -s tests -p 'test_api_contract.py'
python3 scripts/generate_api.py --check
npm --prefix web run typecheck
npm --prefix web test
npm --prefix web run build
python3 -m unittest discover -s tests -p 'test_repository_privacy.py'
```

All three Go packages and all 74 API-contract tests passed. UI assertions cover
fixed local assets, all mappings, exact dimensions, retained/escaped names,
case normalization, family hints and unknown/hostile IDs. Existing forge Go and
web jobs execute these suites; workflow files are not evidence of a CI run.

API impact: **changed contract** for the optional observation field; the coverage
extension uses that same contract. Guide impact: **updated**. State owner: **none**.
The official asset set is included under the project owner's explicit decision
to document unresolved terms for release review. That decision grants no
third-party rights; external artwork redistribution review remains outstanding.

Final coverage: **50 OS IDs, 48 distinct image files**, including generic Linux,
Windows and three BSDs. `python3 scripts/check_docs.py --worktree` passed for all
140 then-current uncommitted paths, and `git diff --check` passed. These structural
checks do not certify the unrelated concurrent changes or third-party rights.

The catalogue/component expansion relative to the earlier 12-image snapshot adds
1,979 and removes 92 text lines (**net +1,887**), plus 18 binary images. This count
covers the asset directory and shared guest-icon component/test; source SVGs and
retained licenses account for most additions. Shared API/guide/CSS edits and
assessment documents are excluded from that scoped count to avoid attributing
concurrent work to this slice. Changes remain unstaged.

Final independent Luna review inspected the refreshed 50-ID contact sheet, source
catalogue, observation flow and change assessment. It confirmed the Amazon and
Devuan corrections and reported no remaining functional, accessibility-link,
ID-flow or asset-safety findings. The reviewer also passed the guest-icon UI test,
focused three-package Go checks and generated-reference check. Licensing review
and live per-distribution guest verification remain the limits described above.
