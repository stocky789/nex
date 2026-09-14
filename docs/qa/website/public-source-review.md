# Website public-source review

Date: 2026-09-09. Scope: Git-visible files in `website/`, generated output and ignore rules.
Internal deployment details in the README were replaced with generic instructions
([BUG-0029](../../BUG_LEDGER.md#bug-0029--website-source-readme-included-private-deployment-details));
environment files, npm config, keys and logs are now ignored. API/guide impact **none**.

## Evidence

| Check | Result |
| --- | --- |
| All 19 Git-visible website files enumerated | No credential found |
| Four Controller screenshots and brand board (independent reviewer) | Fictional inventory, no ancillary metadata; brand board keeps generation provenance with no private details; images not edited |
| Lockfile download origins | Public npm registry, no embedded credentials or query parameters |
| Generated build/SvelteKit output | No private/credential markers; Git-ignored |
| `python3 -m unittest discover -s tests -p 'test_website_public_files.py'` | Failed on old README, then both tests pass; enumerates tracked files so force-added inputs are caught; messages omit matched content |
| `git check-ignore` | Environment/config/key/build/dependency paths excluded |
| Independent final review | Reran checks and docs checker, no blockers; Git-range check not run |

Both phase1 workflows run unittest discovery on push/PR; no hosted run claimed.

## Limits

Scoped review, not detection of every secret format; screenshots still need visual review.
Do not publish by copying the working directory. Not repository-wide or Git-history clearance.
Artwork terms remain separate from the source license.
