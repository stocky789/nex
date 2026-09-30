# Backup repository destination checks

Date: 2026-09-28, condensed 2026-09-30. Candidate: uncommitted development tree on
the `backup-repos` branch. This is a current-state summary of what was checked, what
is stale and what is still open. It is not a release acceptance and not an untouched
lab transcript. Names are fictional aliases; raw service output, credentials, host
identifiers, fixture paths and captures are excluded. The detailed per-checkpoint
narratives that used to live here were removed; the bug ledger entries and the
[change record](../changes/2026-09-28-backup-repository-destinations.json) keep the
regression evidence for each defect.

## Status in one paragraph

The tracked Core advertises NFS, SMB3, S3 and SFTP. Bounded disposable-lab runs passed
backup and restore for all four kinds, but every native result below was produced
**before** the BUG-0716 to BUG-0752 review fixes, the no-legacy cleanup and the
credential-sealing and generation-removal changes. Those later fixes have focused unit
tests only. No native, browser or forge CI run exists for the current tree, so the
destination expansion and the parent recovery gate are **not** accepted.

## Environment and method

- Disposable lab only: three Nex Core guests plus a separate storage guest on a private
  test network (the `cluster-ha-v1` fixture), a disposable Controller, and Chromium
  driven against it. No live host, production share or real hardware was used.
- Services: NFSv4 export and Samba on the storage guest; a pinned single-node archived
  MinIO profile and a pinned S3Proxy single-process profile for S3; OpenSSH
  `internal-sftp` (also size-capped and read-only variants) for SFTP.
- SMB3, S3 and SFTP native runs used ignored, lab-only capability overlays built from
  the source of the day. Local Core binaries were rebuilt from the then-current tree;
  which build ran which check is not recorded per line here.
- Fixture guests were zero-NIC two-disk VMs with planted disk markers. Restored disks
  were read back through read-only mounts and a separate network-free Linux guest.
  This proves the planted markers, not application recovery.
- Service smoke, fault injection and native product results are different evidence.
  A service-level put/get or HTTP 200 alone was never counted as a pass.

## Result matrix (native, pre-BUG-0716 build)

| Check | NFS | SMB3 | S3 | SFTP |
|---|---|---|---|---|
| Stopped two-disk backup and same-host restore | pass | pass | pass | pass |
| Online crash-consistent backup, cross-host restore with source disks unavailable | pass | pass | pass | pass |
| Test restore, isolated boot, marker readback, verification and cleanup (Controller absent) | pass | pass | pass | pass |
| Controller Run now, exact replay, one run/point | pass | pass | pass | pass |
| Scheduled run while Controller absent | not run separately | pass | pass | pass |
| `keep_latest` retention keeps protected points | pass | pass | pass | pass |
| Missing-index catalog rebuild without Controller | pass | pass | pass | pass |
| Fence old master, promote verified member, then backup and restore | pass | pass | pass | pass |
| Fresh Controller adoption of repositories, policies, runs and points | pass | not run separately | not run separately | not run separately |
| Service loss during upload or publication, then recovery | pass | pass | pass | pass |
| Interrupted prune and interrupted catalog rebuild recover on retry | pass | pass | pass | pass |
| Corrupt artifact refused by rebuild, repair recovers | pass | pass | pass | not run |
| Full or read-only target, protected restore still works | pass (capped) | pass (capped) | pass (capped) | pass (capped and read-only) |
| Cross-pool owner-registration race refused | pass | pass | pass | pass |
| Restore pin held against retention while copy is in flight | pass | pass | pass | pass |
| Same-ID replacement refused, retained points intact | pass | not run | source only | source only |
| Explicit replace on a disposable VM | not run | pass | pass | pass |
| Member access probes after outages | pass | pass (recovery delayed) | pass | pass |
| Old-account revocation | not applicable | partial (a mounted client kept working) | not run | not run |

Not listed means not run. "source only" means a focused red-before/green-after Go
regression without a native run. Bounded means one disposable fixture and profile.

Other bounded results:

- Post-promotion NFS backup initially could not settle while a lagging member refused
  authority admission; it later settled after the BUG-0687/0689 fixes, with one point
  and hashes matching. Complete retained history (about 1,050 seeded runs, over 64 MiB)
  replicated by signed v2 pages, promoted and paged to a fresh Controller. The seeded
  runs had empty catalogs; the payload backup and restore after promotion is separate.
- S3 provider limits: SeaweedFS lost acknowledged objects after a fixture stop and
  was unsupported. The archived MinIO profile refused registration when its wrong-ETag
  conditional delete deleted the sentinel; S3Proxy passed conditional create/delete,
  tombstone and multipart probes but enforces conditions in one process only. Degraded
  read quorum and real cloud providers were not tested.
- A disposable Controller policy switch from an old to a new SMB3 repository (revision
  1 to 2, stale revision conflict, exact retry) and old-point restore passed. It was
  reviewed retrospectively from private receipts for the disabled-account restore, not
  re-run with an isolated boot.
- Live browser review on a disposable Controller covered adopted points, repositories
  with Core-reported health, policies with an offline warning, the change-repository
  panel with confirmation and keyboard close, SFTP credential handoff and
  registration with a member off, a policy switch, restore as new with an accepted
  reply loss, typed-name replace and typed-ID cleanup, and a missing-CSRF 403. Captures
  are private and unsanitized; only the conclusions are recorded here. Role-scoped
  permission was not tested because only administrator sessions exist.
- The former "browser review with simulated API" checks are not evidence for the
  current UI and were removed.

## Local source checks

Last full run recorded for a tree before the BUG-0716 fixes: `go test ./...`, the Python
suite (568 to 571 tests, one skipped), web `npm test`, `npm run typecheck`,
`npm run build`, `python3 scripts/generate_api.py --check`, the documentation gate, the
privacy test and `git diff --check` passed in an unrestricted session. In a restricted
session, loopback and systemd sockets were denied: `go test ./...` failed at `httptest`
listeners and the Python suite ended with 15 errors and two failures, so only a
compile-only `go test ./... -run '^$'` passed there. Those runs establish local
source/build state only. Package timings, hashes and the Debian 13 package notice check
(all vendored license files matched the source archive; the Go module list matched) are
not repeated here; the notice check does not certify a signed package or ISO.

## Stale evidence

Everything in the matrix predates BUG-0716 to BUG-0752 (see the
[bug ledger](../BUG_LEDGER.md)) and the removal of old-version handshakes and fallbacks,
and must be rerun on the current tree. A lab
NFS or SMB3 mount unit written by an older build must be detached once before Core
prepares it again.

## Open gates

1. Rerun the common matrix above on the current tree for all four kinds, including the
   rows marked not run: SFTP corrupt-artifact handling, SMB3/S3/SFTP same-ID replacement,
   NFS scheduled run, and fresh-Controller adoption for SMB3, S3 and SFTP.
2. Real-clock missed-schedule behaviour (Core down over a slot), alert deduplication for
   offline and recovered health, credential rotation by new repository ID, and stale or
   unreachable rendering.
3. Abrupt crash-durability proof for SFTP and S3 prune, publication and marker writes
   (only in-process fakes and graceful or SIGKILL-at-service checks exist), and
   online-capture cleanup after an interrupted upload.
4. Real-host boot verification of systemd-creds sealed SMB3 mount credentials (TPM2 and
   host-key hosts, reboot remount).
5. Mixed-version and Controller-replacement-through-an-active-switch checks are out of
   scope under the no-legacy decision; a same-version replacement remains to be repeated.
6. Final enabled-build walkthrough of the guide, and live browser review of loading,
   empty, offline, stale, permission, credential, keyboard, uncertain-result and
   destructive-action states, with sanitized capture conclusions.
7. Independent review of the final tree, a signed package/ISO component and source
   review before distribution, and the forge CI run after an owner-authorized push.
   No CI result exists for this candidate.
