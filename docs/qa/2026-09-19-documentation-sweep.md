# Documentation truthfulness sweep — 2026-09-19

## Scope and method

Seven parallel audit lanes reviewed the current working tree against implementation,
API contracts, scripts and retained evidence; the primary reconciled cross-document claims,
public website copy, guide navigation and contributor templates. Existing unrelated working-tree
changes were preserved. This is a documentation audit, not renewed acceptance of every feature.

| Area | Coverage and evidence method |
|---|---|
| Plans | All 31 plans, partitioned between pool/storage, feature and delivery reviewers; current status compared with code and dated QA; planned requirements retained as planned. |
| Operator guide | All 19 Markdown pages including navigation; instructions compared with current UI/API/Core or delivery scripts and existing walkthrough limits. |
| API | Controller, local Core and peer route/reference coverage; all three OpenAPI contracts and generated references checked against handlers, protocol types and callers. |
| Historical evidence | Indexed 302 QA files at audit start: 120 Markdown reports and 182 supporting files. Reviewed date/scope/limit markers and current-sounding claims, with focused inspection of release, phase closure and recent recovery evidence. Not every screenshot/log was individually revalidated. |
| Change and bug records | Parsed all 329 existing change assessments and read their QA evidence references; checked ledger ID consistency and recent regression references against test source/workflows. Evidence paths were not all resolved against the tree: raster captures are untracked by design (see below), and at least one historical record — `docs/changes/2026-09-11-console-quality-lossy.json` — still cites `internal/vmhost/qemu_vnc_test.go`, which existed when the record was written and was removed with the later emulator restore. Historical execution was not repeated. |
| Project and delivery entry points | README, product principles, contributor/documentation rules and pointers, both PR templates, brand/artwork scope, installer/appliance/package/website READMEs and development/licensing guidance. Legal texts and third-party notices were preserved; no new legal certification is claimed. |
| Website | Home, roadmap and dated gallery claims reviewed; current copy rendered and exercised with the existing browser harness. |

An additional inline relative-link scan covered 190 Markdown files across documentation and
project/build entry points before this report was added. It found and corrected two incorrect
software-update guide links in nested QA reports. Documentation image references were checked
for local-file existence. This scan does not validate external URLs or every prose/code path.

## Corrections

- Replaced the guide index's obsolete Phase-1-only/installer-enrollment description with
  current development scope and page-specific verification limits. Listed ZFS provisioning
  and member configuration convergence without implying shared local disks or phase closure.
- Corrected VM creation planning: Core owns `start_after_create` inside `vm-create`;
  Controller does not schedule a second power request. Updated driver-disc instructions
  for Core-seeded Fedora virtio-win media while retaining the older-host import path.
- Synchronized ZFS plans, guide and API descriptions: new provisioned repositories are
  VM-disk-only, always host-local, and expose per-host GUID/access observations. Durable
  provisioning outcomes are not fresh health evidence. Existing-dataset registration is
  a separate path with its own capabilities and unchanged-property guarantees.
- Kept master promotion, authority replicas, safe rejoin, migration and HA visibly planned.
  The accepted manual-promotion/verified-fencing/preserved-identity decisions remain canonical
  in the platform plan; rejoin does not create another VM restart or movement authority.
- Corrected old handoffs and feature-plan status paragraphs that sounded current despite
  later implementations or Phase 2 closure. Preserved original dated tests and limitations.
- Updated delivery/version guidance: the current working-tree version is 0.3.5; internal
  image/kernel evidence does not by itself establish publication or external-release clearance.
  Removed stale package-source and private-site-role wording where scripts/evidence differed.
- Corrected website copy that still presented VM lifecycle as future work or made an
  unsupported blanket statement about public-build availability. The roadmap now distinguishes
  implementation/test evidence from publication and keeps incomplete member convergence
  among outstanding work. No deployment or publication occurred.
- Sanitized one historical website resolver target to a reserved example address and
  labeled the JSON as sanitized historical evidence. The sweep missed a second copy: the
  same public IPv4 literal remained hard-coded in `tests/integration/website_hardening_browser.py`.
  That script now reads the address from `NEX_WEBSITE_RESOLVE_IP`, defaulting to a reserved
  documentation address (RFC 5737), so no deployment address is hard-coded. Published Git
  history still contains the original literal in both files; removing it from the current
  working tree does not remove it from history, releases or cached copies, and no history
  rewrite was performed or is claimed here.
- Added state-owner and roadmap prompts to both PR templates to match the contributor gate.
  Corrected the documentation manual's obsolete `api.impact: updated` guidance: the current
  checker requires the contract-impact vocabulary for new assessments.

## Historical website screenshots

Decision: project owner, 2026-09-19 — the three website screenshots below are an accepted
historical evidence limitation; they are not to be recreated and are not outstanding work.
This does not claim the files exist or waive verification for new changes.

- `docs/qa/website-gallery-vms/gallery-1440.png`
- `docs/qa/website-roadmap/roadmap-1440.png`
- `docs/qa/website-roadmap/roadmap-360.png`

Corrected context, added after that decision: these three are not a special case. `.gitignore`
excludes `docs/qa/**/*.png`, `*.jpg`, `*.jpeg`, `*.webp` and `*.gif`, so **no** QA raster
capture is tracked in this repository. A scan of `docs/changes/*.json` on 2026-09-19 found 374
of 412 cited image paths absent from this checkout, across 39 records — including the roadmap
record's own `header-360.png`, `roadmap-768.png` and `roadmap-hover-1440.png`. The earlier
wording in this report, which said the limitation "applies only to these three screenshots" and
told reviewers not to flag missing captures, was wrong on both counts and is withdrawn.

The standing position is recorded in
[the documentation manual](../DOCUMENTATION.md#qa-raster-captures-are-untracked): cited captures
are generally not re-inspectable from the repository, and reviewers remain free to raise
evidence reviewability. The affected QA reports point to retained reports/results rather than
nonexistent image files; their historical changed-path lists remain intact. No screenshots were
fabricated or replaced with current captures. The new website walkthrough below proves only the
current local build and cannot repair the missing historical evidence.

## Executed checks

- `python3 scripts/generate_api.py --check`: all generated references current.
- `python3 -m unittest discover -s tests -p 'test_api_contract.py' -v`: 30 passed.
- Feature-plan reviewer ran `go test ./internal/controller ./internal/vmhost`: passed.
- `npm --prefix website run check`: zero errors and warnings.
- `npm --prefix website run build`: passed.
- Existing `tests/integration/website_browser.py` against the local static build: 13 result
  groups passed at 1440, 768 and 360 pixels, including keyboard navigation, disclosures,
  reduced motion, overflow, gallery and JavaScript-disabled navigation. The primary inspected
  the desktop roadmap and mobile header captures. Evidence is retained under
  `.cache/documentation-sweep-browser/`; it is new local website evidence only.

- Final `python3 scripts/check_docs.py --worktree`: passed for all 137 uncommitted paths,
  including existing work outside this documentation sweep. `--all` also passed its narrower
  structural scope; it does not establish working-tree coverage or resolve historical evidence.
- Final repository privacy test and `git diff --check`: passed. That test checks home paths,
  observed identifiers and credential patterns only; it has no IP-address pattern, so its pass
  is not evidence that the resolver address above was sanitized. That was confirmed separately
  with `git grep` for the literal.
  The supplemental link scan
  passed across 191 Markdown files, including this report, with no missing inline relative targets.
- Two independent reviewers checked the runtime-facing and delivery/history/website corrections.
  Their ZFS field-name and website-download wording findings were corrected and rechecked;
  the historical resolver sanitization was also independently checked. No outstanding findings
  remain in their reviewed scope. The owned local website preview server was stopped.

## Limits

No new host, storage, fencing, restore, migration, physical hardware or hosted-CI acceptance
was executed by this sweep. Historical results remain bound to their original versions and
fixtures. External publication status was not freshly queried; published-release statements
are limited to the recorded release evidence. The source/reference audit is broad but is not
proof that every runtime path behaves correctly. Remaining product and distribution gates
stay open in the platform and feature plans. Changes are local and uncommitted.
