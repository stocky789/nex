# ISO library independent of VM disks

Date: 2026-09-14. Controller and Core unit tests. Not a browser or disposable-host walkthrough.

## Scope

BUG-0180: a VM may attach an install ISO stored on any repository the placement host can read.
API impact: changed contract (Controller no longer returns 409 when the ISO and disks use
different storage). Guide impact: updated.

## Automated results

| Check | Result |
|---|---|
| `go test ./internal/controller ./internal/vmhost -count=1` | Passed, including `TestCreateAndInsertISOFromSeparateStorage`, `TestPublishedMediaPathIndependentOfDiskStorage`, `TestManagedMediaInsertFromSeparateStorage` |
| `npm --prefix web run typecheck` and `npm --prefix web test` | Passed |
| Browser Create VM / Edit / CD-DVD insert with ISO on a different repo | Not run |

## Limits

No disposable Core, NFS/SMB ISO library, or GUI capture. Host-readable ISO storage is still
required; an ISO imported on another host is still refused.

## Evidence

`TestCreateAndInsertISOFromSeparateStorage`, `TestPublishedMediaPathIndependentOfDiskStorage`,
`TestManagedMediaInsertFromSeparateStorage`.
