# Snapshots tab browser walkthrough (S3)

Date: 2026-09-26. Disposable recovery Controller on port 36704 with rebuilt `web/dist`
(running mode selector) and the lab Core pool. Private captures remain under the ignored
lab cache; this report records behaviour only.

## Steps and results

1. Signed in as the lab administrator.
2. Opened the running managed BIOS guest Snapshots tab.
3. Confirmed the Mode control offers Crash-consistent and Quiesced.
4. Confirmed the catalog listed the earlier Core quiesced points.
5. Submitted Take snapshot with Mode Crash-consistent and a unique name. The Controller
   operation reached terminal `succeeded` and the master's catalog listed
   `browser-crash-6245` (API confirmation after UI submit).
6. After stopping that guest, the Snapshots tab hid Mode. Rename of `browser-crash-6245`
   to `browser-crash-6245-renamed` settled in the master catalog from the browser form.
7. On never-started BIOS guest `s3-browser-clean5` (matching-disk create, two retained
   points), browser Revert of `s3-c5-base` queued and settled `succeeded`
   (`snapshot-tbIymj4Ey-ouzHE-Rp-DUUKsNUxdgA_mdyPb4mMB9ac`).
8. After a new leaf `s3-c5-leaf2`, browser Delete queued and settled `succeeded`
   (`del353b6c10b842a89784f6e6c9`); master catalog retained only `s3-c5-base`.
9. On the VMs list, selected shut-off matching-disk guests `s3-browser-clean5` and
   `s3-bios-qga-052ee6`, chose **Snapshot**, entered shared name `bulk-snap-11665`, and
   confirmed. Both Controller create POSTs returned `202`; both master catalogs listed
   `bulk-snap-11665` after settlement. An earlier attempt on damaged clean3/clean4 fixtures
   correctly submitted the same UI path but Core refused ownership (fixture limit).

## Limits

- Earlier browser leaf delete/revert on seeded guests failed Core ownership validation
  when lab disks were replaced outside normal create; that fixture limit is closed for
  matching-disk guests after the CDROM driver-type ownership fix.
- Accessibility and independent usability review remain open; role/keyboard focus
  checks on the Snapshots section passed in lab Playwright, but that is not a full
  accessibility or usability review.
- Captures are lab-private; they are not committed.
- Controller UI enables Revert/Delete when the observed guest state is `shut off`.
  Never-started pool state `defined` is reported that way in this lab; Core also
  accepts `defined` for stopped snapshot revert reservations.
- Bulk Snapshot is stopped-only; running capture still uses each VM's Snapshots tab.

## Checks recorded

- logged in
- running guest shows crash/quiesced mode
- catalog lists quiesced point
- browser crash create succeeded into master catalog (`browser-crash-6245`)
- stopped guest has no mode selector
- browser rename settled (`browser-crash-6245-renamed`)
- browser revert on defined/shut-off matching-disk guest succeeded (`s3-c5-base`)
- browser leaf delete on matching-disk guest succeeded (`s3-c5-leaf2`)
- Snapshots section exposes labelled Name/Description fields and Rename/Revert/Delete
  buttons by role; keyboard focus reaches the create Name field
- Snapshots Rename/Revert/Delete buttons expose named aria-labels (e.g. `Rename s3-c5-base`)
- VMs-list bulk Snapshot on two matching-disk shut-off guests settled
  `bulk-snap-11665` in both catalogs
- Bulk Snapshot dialog Name autofocus, Escape/Cancel close, and Will be requested status
- Mixed selection: running guest skipped with stop-it-first while shut-off remains eligible
- Snapshots Revert/Delete open console confirm-dialog (aria-labelledby `snap-confirm-title`);
  Cancel closes without `window.confirm`
- Snapshots disabled controls expose `title` why; confirm dialog keeps Requesting… through
  submit, restores focus to the opener, and sets aria-describedby
