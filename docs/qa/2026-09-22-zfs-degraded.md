# Degraded ZFS repository stays in service

Date: 2026-09-22. Status: slice 1 of the
[software RAID plan](../plans/2026-09-22-software-raid.md) verified in the disposable ZFS
lab. This report is evidence for that slice, not release clearance.

It reverses the accepted outcome recorded in
[ZFS provisioning rollout verification](2026-09-19-zfs-provisioning.md): a mirror with one
member offlined was reported unreadable and unwritable with `ZFS pool health is DEGRADED`.

## Scope and environment

- Disposable two-host QEMU lab (`core1` master, `core2` member) with virtual 8 GiB data
  disks; no workstation disk, bridge or libvirt object outside the lab is touched.
- Nex Core guests: Debian 13, `zfs-2.3.9-0+deb13u1` / `zfs-kmod-2.3.9-0+deb13u1`.
- Core and Controller built from the working tree at this change and deployed with the
  lab's own `deploy.sh`; UI built with esbuild into the lab's `runtime/ui`.
- Controller on `https://127.0.0.1:18443`; guests reach the workstation as `10.0.2.2`.
- Commands, reports and captures: `.cache/software-raid/A/` (untracked, disposable).
- Two disposable data disks were added to `core1` for this check
  (`virtio-NEX-CORE1-DATA-5`, `-DATA-6`); both were reported blank by Core discovery.

## Unit checks

- `go test ./internal/...`: passed, including the new
  `internal/vmhost/zfs_status_test.go`. Its fixtures under `internal/vmhost/testdata/`
  are two `zpool status --json` captures from a file-backed fixture pool on the lab host
  (a mirror with one member offlined, and a resilver in progress) and one derived file,
  `zpool-status-idle-derived.json`, built from a capture of the same pool by removing the
  in-flight resilver, because every capture of that pool had a resilver running.
- `go vet ./internal/...`: clean.
- `npm --prefix web run typecheck` and `npm --prefix web test`: passed, including the new
  `zfsPoolSummary` assertions in `web/src/storage.test.mjs`.
- `python3 scripts/generate_api.py --check`: current after regenerating the reference.

## Lab walkthrough

All steps went through the Controller API as an operator would
(`.cache/software-raid/A/degraded_test.py`); native state was read over the lab's ssh
helper.

1. **Provision a mirror.** `POST /api/v1/storage/discover` then
   `POST /api/v1/storage/zfs/provision` with both new disks and `layout: mirror` created
   `degraded-mirror` (`zfs-0ee83f5a54fe18bf46a2284ed27f73dd`), status `succeeded`.
2. **Healthy observation.** `GET /api/v1/storage` reported `access.status: healthy`,
   `readable`/`writable` true, and the new `pool` object with `state: ONLINE` and both
   leaves by their `/dev/disk/by-id/...-part1` paths with zero read, write and checksum
   errors (`report-healthy.json`).
3. **VM on the repository.** `POST /api/v1/vms` created `degraded-vm` with a 2 GiB thin
   qcow2 disk on the repository and started it; `virsh list` on the host showed it
   `running`. A 64 MiB random file was written into the dataset and its SHA-256 recorded.
4. **Offline one member.** `zpool offline <pool> virtio-NEX-CORE1-DATA-5` on the host.
   Native state became `DEGRADED` with `virtio-NEX-CORE1-DATA-5 OFFLINE`,
   `virtio-NEX-CORE1-DATA-6 ONLINE` and no known data errors (`native-offline.txt`).
5. **Controller while degraded** (`report-degraded.json`): `access.status: degraded`,
   `readable` and `writable` true, no error, and the `pool` object carrying
   `state: DEGRADED`, the offlined leaf by id, and the `status`/`action` sentences ZFS
   prints ("One or more devices has been taken offline by the administrator. …",
   "Online the device using 'zpool online' or replace the device with 'zpool replace'.").
   Under the previous behaviour this state reported unreadable and unwritable.
6. **Still in service while degraded.** `degraded-vm` stayed `running` throughout. A second
   64 MiB file was written into the dataset and read back, and the first file's SHA-256 was
   unchanged (`native-write-degraded.txt`). `POST /api/v1/vms` then created **and started**
   a second VM, `degraded-vm-2`, with a new thin disk on the degraded repository: placement
   and VM start accept a degraded pool.
7. **Browser review.** The pool's **Storage** page showed an amber `degraded` badge in
   **Core access** (distinct from the green `healthy` badge on the other repositories and
   from the red failure badge) and, in the host row, an amber
   `Pool degraded · /dev/disk/by-id/virtio-NEX-CORE1-DATA-5-part1 offline` line.
   Capture: `.cache/software-raid/A/shots/degraded-storage.png`.
8. **Repair.** `zpool online <pool> virtio-NEX-CORE1-DATA-5` resilvered 64.2 MiB in one
   second with zero errors and returned the pool to `ONLINE`. The Controller returned to
   `healthy` and reported the finished resilver in `pool.scan`
   (`RESILVER`/`FINISHED`, 64.1M examined, 0 errors) (`report-repaired.json`). Both files'
   SHA-256 sums were unchanged after the resilver (`native-verify.txt`). The host row read
   `Pool online · last resilver finished`
   (`.cache/software-raid/A/shots/repaired-storage.png`).

## Limits of this evidence

- The lab guests are intentionally blank VM images with no installed operating system, so
  "the VM keeps writing" is shown at the disk and repository layer — the VM stayed running,
  its repository stayed writable, host writes into the dataset succeeded and a second VM's
  disk was created and started on the degraded pool — not as a booted guest filesystem
  workload. The 2026-09-19 report has the same limit.
- The failure was injected with `zpool offline`, which is an administrative state. A
  physically removed or failing disk (`FAULTED`, `UNAVAIL`, `REMOVED`) was not produced on
  real hardware; those states remain inaccessible by design and are covered only by the Go
  unit tests.
- No `zpool replace` was exercised: replacing a failed member is slice 2 of the plan.
- The resilver captured here finished in about a second. An in-progress (`SCANNING`) scan is
  covered by a captured fixture in the unit tests, not by a Controller observation.
- Capture a genuine idle `ONLINE` pool for `internal/vmhost/testdata/` remains outstanding;
  `zpool-status-idle-derived.json` is derived from a resilvering capture, not an untouched
  idle-pool capture.
- RAIDZ layouts were not degraded in this run; the health mapping and pool detail do not
  depend on the layout, but that is inference, not evidence.
- The lab hosts, Controller and the two disposable VMs were left running at the end of this
  check.
