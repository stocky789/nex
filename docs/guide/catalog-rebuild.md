# Rebuild the backup catalog

## Status

Development Phase 6 / S7. Core rebuilds the recovery-point catalog from validated repository
manifests after catalog loss. Protection markers and restore pins survive. Incomplete or
corrupt points are skipped.

## Prerequisites

- Pool master with the backup repository mounted
- Matching Nex Core build exposing catalog rebuild
- Root or authenticated local API access when Controller is absent

## Steps

1. **Controller UI:** open **Settings** → **Backup repositories**, then **Import points** on
   the repository row to merge validated manifests into the catalog.
2. **Controller API (rebuild after catalog loss):**
   `POST /api/v1/pools/{id}/backup-catalog/rebuild` with
   `{request_id,repository_id}`. Use `.../import` to merge without clearing existing entries.
3. **Core CLI** on the pool master:
   ```text
   nex-core-local rebuild-backup-catalog \
     --agent-state-dir /var/lib/nex-core-agent \
     --repository-id <repository-id>
   ```
   Prints JSON with `imported` count and imported points. Exit nonzero on failure.
4. **Authenticated local API:**
   `POST /api/local/v1/pool/backup-catalog/rebuild` or `.../import` with
   `{operation_id,repository_id}`.

## Expected result

Only manifests with valid identity and matching artifact digests are cataloged.
Import does not itself prove a point can boot after restore. Protected points keep
protection. Active restore pins on the same master remain and block prune.

## Troubleshooting

- Empty import: check repository mount, owner marker, and that point directories contain
  complete `manifest.json` plus matching disk digests.
- Missing keys / incomplete manifests: refused; fix the repository payload, then rebuild again.

## Verification

Unit tests cover import of validated manifests and refusal of incomplete ones. Full
Controller-absent lab rebuild remains **unexecuted**.
