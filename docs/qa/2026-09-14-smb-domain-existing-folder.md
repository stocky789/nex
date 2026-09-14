# SMB domain and existing-folder picker

Date: 2026-09-14. Simulated Controller UI plus focused Core unit tests. Not a native TrueNAS or
hardware walkthrough.

## Scope

BUG-0179: optional SMB domain/workgroup on credentials; folder picker shows **E** for folders
with a `.nex-storage-marker`; the typed “Existing directory within share” field is removed.
API impact: existing contract (`storage/credentials` and `storage/discover` gain optional
`domain` and discovery `existing`). Guide impact: updated.

## Automated results

| Check | Result |
|---|---|
| `go test ./internal/protocol ./internal/vmhost ./internal/controller ./internal/agent -count=1` | Passed |
| `npm --prefix web run typecheck`, `npm --prefix web test`, `npm --prefix web run build` | Passed |
| `tests/integration/storage_repositories_browser.py` | Passed: Domain field, no typed share subdirectory, **E** on an existing folder, credentials include `domain` |
| Native SMB mount with a workgroup/AD domain | Not run |

## Limits

No disposable-Core or TrueNAS authentication was exercised. Marker probes are unit-tested with
mocked `smbclient` output only. NFS still has a typed directory field.

## Evidence

Browser capture: `.cache/storage-browser/smb-browser.png` from the integration check (local
cache; not committed).
