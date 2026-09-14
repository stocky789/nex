# Network storage picker verification

Date: 2026-09-12. Source: `b0fa760` plus network-picker working-tree changes. Sanitized scenario
evidence, not an untouched capture of the private lab.

## Scope

BUG-0095 follow-up: SMB server/account/password → shares → folders; NFS server → exports → folders;
iSCSI portal → target. Manual entry remains; no new UI library. API impact: existing contract
(`storage/discover` relayed by Controller, probed by Core; validation now accepts ordinary SMB share
names and relative directories with spaces; no new state). Guide impact: updated.

## Automated results

| Check | Result |
|---|---|
| Updated browser check on old build | Failed: **Browse exports** missing (reproduces) |
| `npm run typecheck`, `npm run build`, `npm test` in `web` | Passed |
| `go test ./internal/protocol ./internal/vmhost ./internal/agent ./internal/controller` | Passed; new: spaced/hidden SMB shares, unsafe-name rejection, SMB directory args, NFS long-listing parse, spaced relative traversal, mount-unit source |
| `tests/integration/storage_repositories_browser.py` (built UI, fixtures) | Passed: root requests despite draft paths, SMB without share, NFS export base, nested nav, Up, Refresh, empty, failed requests, Escape/focus, keyboard, confirmation-only source change, registration, transient credentials; deferred replies: loading, disabled selection, cancel then late reply does not reopen, HTTP-200 failed result |
| `check_docs.py --worktree` (18 paths); API contract (21); privacy; `git diff --check` | Passed |

Both phase1 workflows already run the Go and storage-browser suites; no hosted CI. Independent
reviewer reran protocol/vmhost and browser suites, inspected captures, accepted (native args,
relative-directory safety, source validation, Core ownership).

## Native walkthrough (primary)

Chromium through the real HTTPS Controller, normal auth, existing disposable two-Core Debian lab. Pool
master ran real `smbclient`, `showmount`/`nfs-ls`, nonpersistent `iscsiadm` against the fixture
server; no mocks; existing workloads/registrations untouched. Core SHA-256
`0712b986e342ab03c78ce61444ac8a710c17ea34b3753c7742902f0704e5fb0e`.

1. SMB with only server/account/password: empty Share did not block; real share listed and opened.
2. `Team VMs` → `Nested Folder`, Up, selected another folder; Share and directory filled.
3. Reopen returned to shares; Escape kept selection and focus returned to Browse.
4. Wrong SMB password: safe in-dialog error with retry/cancel; corrected password recovered.
5. NFS: export chosen, spaced nested folders browsed, folder selected; export plus relative path kept.
6. iSCSI target discovered and IQN filled; exclusive-initiator confirmation unchecked; no login/mount/registration.
7. No JS errors; SMB shares, spaced SMB/NFS folders and SMB errors inspected; captures private in local lab evidence.

## Limits

Browsing/selection only: no new spaced-name registration or hidden-share mount natively (unit tests
only). Registration, VM writes and recovery: [earlier lab report](2026-09-12-bug-ledger.md). Results
capped at 64 (ceiling shown, no pagination). Not verified: NAS products, ACL profiles, hardware,
migration, HA, nested UEFI bug. Existing ISO predates these changes.
