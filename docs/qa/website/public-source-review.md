# Website public-source review

Date: 2026-09-09
Scope: Git-visible files in `website/`, plus generated-output inspection and ignore checks.

The README contained internal deployment details. These were replaced with generic
build/deploy instructions and contribution guidance. Local environment files, npm
configuration, keys and logs are now ignored alongside existing build/dependency caches.
This does not publish the site or change the running private preview.

## Evidence

- Enumerated all 19 Git-visible website files, including lockfile, source, asset
  notices, README and PNGs. No credential was found in the reviewed files.
- Independent reviewer inspected all four Controller screenshots and the brand
  board. Screenshots show fictional inventory and have no ancillary metadata.
  The brand board retains generation provenance; no private deployment details
  were found in that metadata. Images were not edited or stripped.
- Inspected dependency download origins: all resolved lockfile packages use the
  public npm registry, without embedded credentials or URL query parameters.
- Inspected generated build and SvelteKit output for private/credential markers;
  no actual match was found. These outputs and dependencies are Git-ignored.
- The new public-file regression failed on the old README, then both checks passed:
  `python3 -m unittest discover -s tests -p 'test_website_public_files.py'`.
- `git check-ignore` confirmed environment/configuration/key/build/dependency paths
  are excluded. The regression enumerates tracked files as well, so force-adding an
  ignored sensitive input does not bypass it. Failure messages omit matched content.
- Both existing phase1 workflows run `python3 -m unittest discover -s tests -p
  'test_*.py' -v` on pushes and pull requests. No hosted CI run is claimed.
- Independent final review read the cleanup, regression and assessment, reran both
  checks and the documentation checker, and found no blockers. Documentation structure,
  scoped assessment references and whitespace checks passed locally; Git-range checking
  was not run on this uncommitted work.

API impact: **none** — contributor documentation, ignore rules and a local check;
no site behavior, Controller operation, Core command or observation contract changes.
Guide impact: **none** — operator workflows and the deployed static site are unchanged.

## Limits

This is a scoped review, not proof that every possible secret format can be detected.
The checker is a regression guard for known classes of mistakes; screenshots still
need real visual review and neutral source data. Generated artifacts must not be
published by copying the whole working directory.

This is not repository-wide or Git-history clearance. Other plans and QA evidence
outside `website/` retain local environment information and require a separate review
before making the complete repository public. Existing artwork terms also remain
separate from the source-code license; this privacy check changes no redistribution rights.
