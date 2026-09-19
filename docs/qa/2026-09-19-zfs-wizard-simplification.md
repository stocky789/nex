# ZFS storage wizard simplification verification

Date: 2026-09-19. Scope: Controller UI only. The provisioning, discovery and lifecycle API
contracts are unchanged; this report is not acceptance of ZFS provisioning itself, which
stays with the [rollout verification](2026-09-19-zfs-provisioning.md).

## What changed

An operator walkthrough in a disposable two-host lab found the four-step inline wizard hard
to follow. The wizard is now a two-screen dialog: **Choose disks** (storage name, a tick per
host, a tick per spare disk, the resulting layout shown inline) and **Review and erase**
(unchanged typed by-id confirmation for disks holding old data). Two or more ticked hosts, or the
only host of a one-host pool, submit pool scope; one ticked host of several submits host scope.
Unavailable disks are listed with Core's reason behind a disclosure and cannot be ticked.
On the **Storage** page the repository list comes first, **ZFS storage by host** follows,
and **Clustered iSCSI / GFS2** and **Unused storage credentials** fold away until they hold
a configuration, a problem, or the operator opens them.

## Executed checks

Build under test: working tree on 2026-09-19, UI bundled with the repository's esbuild options.

- `npm --prefix web run typecheck` and `npm --prefix web test`: passed (10 suites), including
  new assertions for the displayed automatic layout, the layout fallback, scope derivation,
  that neither the step, the disk selection nor a typed erase confirmation is persisted (the
  replay signature is stored as a digest), and that the offered hosts are a discovery-time snapshot.
- `tests/integration/zfs_provisioning_browser.py` (Chromium, **simulated Core replies**):
  passed. It covers add-this-host targeting, unavailable disks not being selectable, typed
  confirmation gating, lost-reply replay with the same request identity, a resumed dialog
  re-running discovery with nothing ticked, keyboard host untick producing host scope,
  Escape/Cancel (and repeated Escape during a deferred submit), and loading, empty, partial and stale discovery states.
- `storage_repositories_browser.py`, `cluster_storage_browser.py` and
  `managed_vm_browser.py --simulated`: passed against the same build, confirming the folded
  panels and unchanged **Register storage** entry point.
- Real-Controller read-only walkthrough in the disposable lab (two QEMU guests, Debian 13,
  OpenZFS 2.3.9, virtual disks only): the primary inspected captures of the Storage page,
  the disk screen with three disks ticked across two hosts, and the review screen. Nothing
  was submitted from the redesigned dialog in this walkthrough.

## Limits

No native provisioning was submitted through the redesigned dialog by the agent; the
request body is asserted only against simulated replies. Narrow-viewport and screen-reader
behaviour of the dialog were not exercised. An independent read-only review found no path to
erase an ineligible or unconfirmed disk; its five findings (live-host scope drift, the stored
replay signature, native second-Escape close, untick wording and a checkbox description) were
corrected and the checks above rerun. A layout override with four or more disks has no browser case.
