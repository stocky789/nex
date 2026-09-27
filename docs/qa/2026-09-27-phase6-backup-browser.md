# Phase 6 Backups UI browser walkthrough

Date: 2026-09-27. Real Controller↔Core path on the disposable recovery Core lab
(core1 pool), matching the style of
[docs/qa/2026-09-26-recovery-snapshot-browser.md](2026-09-26-recovery-snapshot-browser.md).

**Mode: real** (not MSW / fixture UI). Playwright drove Chromium against the live
Controller HTTPS UI; Core answered pool backup APIs on the enrolled master.

Controller A (product-lab ephemeral on loopback) was removed mid-session by a concurrent
Campaign A run. This walkthrough continued against **replacement Controller B** on the
same loopback host after Campaign A reported Controller B adoption **pass**. Private
captures remain under the ignored lab cache
(`.cache/phase6-backup-browser/captures/`); this report records behaviour only.

## Environment (sanitized)

| Item | Alias / note |
|---|---|
| Pool master | core1 (`nex-cluster-core1`) |
| Controller | Replacement Controller B (loopback HTTPS); UI from `web/dist` |
| Backup repository | `phase6-repo-nfs` (NFS; access `ok`) |
| Guests observed | `phase6-backup-src` (disks previously moved aside), `phase6-restore-new`, `phase6-campa-restore` (disks present), plus Campaign A restore guest |
| Adopted policy | `phase6-campa-policy` (Core-owned; visible after Controllers replacement) |

## Steps and results

1. Signed in as the lab administrator. Backups rail link visible; top bar showed the
   core1 pool scope.
2. **Settings → Backup repositories:** listed `phase6-repo-nfs` with access badge.
   Create form autofocused Name; Cancel closed without submit. **Test access** returned
   Controller HTTP `202` and settled succeeded.
3. **Backups rail → Recovery points:** table listed stopped points including
   `phase6-backup-src` / Campaign points. Section uses console section + records table;
   no coloured side-stripe classes observed on the section.
4. **Back up now** (3-step panel): selected `phase6-campa-restore` + `phase6-repo-nfs`,
   stepped to confirm. Primary **Back up now** accepted keyboard focus; Enter submitted
   POST `/vms/{id}/backup` → HTTP `202`. Controller operation `backup-now` ended
   **failed** with `backup recovery point already exists`. NFS repository directory for
   that operation id nevertheless contained `manifest.json` and `vda.qcow2` (complete
   published layout). Catalog list via Controller still showed the prior four points
   (new id not projected in the catalog GET used for the check). **Not counted as a
   settled backup-now pass.**
5. **Restore as new:** opened from a recovery-point row. Filled new name
   `browser-restore-*`, host, Local storage; stepped to confirm. Primary **Restore as
   new** accepted keyboard focus; Enter submitted restore POST → HTTP `202`. New shut-off
   guest appeared in inventory and on the host (`virsh`), with a 1 GiB qcow2 under the
   managed images tree. Controller operation `backup-restore-as-new` settled
   **needs_attention** (`pool-backup-runs/{id}.json` missing on the agent path) while a
   related `vm-create` showed succeeded. **UI + guest presence proven; restore run
   journal settlement not clean.**
6. **Policies:** tab listed adopted `phase6-campa-policy`. Create-policy wizard Save
   policy accepted keyboard focus; Enter submitted POST → HTTP `202`. API afterward
   listed two policies (adopted + `browser-policy-*`). Same-navigation table row count
   check saw one row (refresh timing); API evidence is the durable proof of create.

## Keyboard / focus

Backups UI uses multi-step panels rather than a `confirm-dialog` modal. Primary confirms
exercised with programmatic focus + Enter:

- Back up now (step 3)
- Restore as new (step 3)
- Save policy (step 3)

Create backup repository Name autofocus also checked.

## Console design (spot)

- Rail **Backups**, Settings **Backup repositories**, section headings, records tables,
  slide-over/panel restore — consistent with console patterns.
- Overview tile still shows placeholder copy “Available with backups (Phase 6)” while
  the Backups rail and Settings repositories are live — noted, not a pass.

## Limits

- Captures are lab-private / gitignored; they are not committed (untracked-image limitation).
- Accessibility and independent usability review remain open; keyboard focus on primary
  confirms is not a full a11y pass.
- Original product-lab source VM disks remain aside; Back up now against that guest fails
  missing-disk (observed on an earlier attempt). Settlement of browser backup-now against
  `phase6-campa-restore` failed with “recovery point already exists” despite a published
  NFS directory for that op id — catalog projection / idempotency needs follow-up.
- Restore-as-new guest exists with disks; restore run journal `needs_attention` leaves
  settlement incomplete.
- Online backup / quiesced mode not exercised (stopped guests only).
- Campaign B not part of this browser slice.

## Checks recorded

| Check | Result |
|---|---|
| Signed in; Backups rail | pass |
| Pool scope shows core1 | pass |
| Settings Backup repositories lists NFS repo | pass |
| Test access submitted (202) / create autofocus+cancel | pass |
| Recovery points list stopped catalog | pass |
| No coloured side stripe on recovery section | pass |
| Back up now confirm keyboard focus + submit 202 | pass |
| Back up now operation succeeded into catalog | **fail** (op failed; NFS dir present; catalog GET still prior four) |
| Restore as new confirm keyboard focus + submit 202 | pass |
| Restore as new guest listed + host disk present | pass |
| Restore as new run journal clean succeeded | **fail** (`needs_attention`) |
| Policies lists adopted policy | pass |
| Policy create submit 202 + API lists second policy | pass |
| Policies table row count immediately after create | fail (timing; API proves create) |
| No page JS errors | pass |

## API / guide / state

- API impact: **none** (browser exercise of existing contracts).
- Guide impact: **none** (contributor QA evidence only).
- State owner: **core** for backup repositories, catalog, policies, runs, and restored
  VM definitions on the pool master; fresh Controller B adopted them without the old
  Controller database.
