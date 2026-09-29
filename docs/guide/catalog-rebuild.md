# Rebuild the backup catalog

## Status

Development Phase 6 / S7. Core rebuilds the recovery-point catalog from validated repository
manifests after catalog loss. Protection markers and restore pins survive. Core
refuses unreadable published points and unresolved delete records rather than
discarding their catalog evidence.

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
   `{operation_id,repository_id}`. A `202/pending` response means Core accepted
   the scan. Poll `GET /api/local/v1/pool/backup-operations/{operation_id}`
   until it settles, then check `GET /api/local/v1/pool/backup-catalog`.
   Keep the same operation ID and body for an exact retry after a lost reply
   or Core restart.

## Expected result

Only manifests with valid identity and matching artifact digests are cataloged.
Import does not itself prove a point can boot after restore. Protected points keep
protection. Active restore pins on the same master remain and block prune.

## Troubleshooting

- Empty import: check repository mount, owner marker, and that point directories contain
  complete `manifest.json` plus matching disk digests.
- Missing keys / incomplete manifests: refused; fix the repository payload, then rebuild again.
- A pending operation with an error retains the last transient scan failure.
  Restore repository access, then retry the same operation ID and body. An
  unresolved delete intent requires the original backup's exact prune retry
  before rebuild, including on NFS and SMB3. Do not erase the intent just to
  make the scan pass.

## Verification

Unit tests cover import of validated manifests, refusal of incomplete ones,
and exact retry of a pending rebuild. Bounded Controller-absent NFS and SMB3
catalog rebuilds passed in a disposable lab. A pending S3 rebuild resumed
after a Core restart and settled. Later, with all pool members online, fresh
missing-index S3 and clean-repository SFTP rebuilds each restored one selected
point. A separate older SFTP repository correctly refused unresolved delete
records. Full destination and interruption acceptance remains open.
