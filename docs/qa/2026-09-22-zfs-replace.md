# Replacing a failed disk in a provisioned ZFS pool

Date: 2026-09-22. Status: slice 2 of the
[software RAID plan](../plans/2026-09-22-software-raid.md) verified in the disposable ZFS
lab. This report is evidence for that slice, not release clearance.

It continues [degraded ZFS repository stays in service](2026-09-22-zfs-degraded.md), which
made a `DEGRADED` pool usable and made Core report which leaf failed. This slice adds the
repair: `storage-zfs-replace-v1`, three-way mirrors, RAIDZ3 and hot spares.

## Scope and environment

- Disposable three-host QEMU lab (`core1` pool master, `core2` member, `core3` a separate
  one-host pool) with virtual 8 GiB data disks; no workstation disk, bridge or libvirt
  object outside the lab is touched.
- Nex Core guests: Debian 13, ZFS 2.3.9 built into the lab images.
- Core and Controller built from the working tree at this change and deployed with the
  lab's own `deploy.sh`; UI built with esbuild into the lab's `runtime/ui`.
- Controller on `https://127.0.0.1:18443`; guests reach the workstation as `10.0.2.2`.
- Driver, reports and captures: `.cache/software-raid/B/` (untracked, disposable). Retained
  there: the driver, the two Controller reports of section 1, the submission log of the
  interrupted run, and the five browser captures. Sections 2 and 3 have no other retained
  output; what that means is stated in each.
- Two more disposable data disks were added to `core2` for this check
  (`virtio-NEX-CORE2-DATA-5`, `-DATA-6`), so it had four blank disks.
- `core3` deliberately kept the previously deployed Nex Core, so it is a genuine host
  without the new capability.

## Unit and contract checks

These were rerun after independent review, with its fixes in place.

- `go test ./internal/...`: passed, including the new `internal/vmhost/zfs_replace_test.go`
  (command order, the ONLINE/absent/unreadable-pool refusals, the ineligible and
  unconfirmed replacement, resuming after an interrupted wipe and adopting a pool that is
  already resilvering onto the replacement), `internal/agent/zfs_replace_test.go` (master
  forwarding to a member over the peer channel, the repository record naming the new disk,
  the master-only and peer-authentication refusals) and
  `internal/controller/zfs_replace_test.go` (relay to the pool master, request replay,
  invalid input, the capability refusal and CSRF).
- `go vet ./...`: clean.
- `npm --prefix web run typecheck` and `npm --prefix web test`: passed, including the
  widened layout rules and the replacement body builder.
- `python3 scripts/generate_api.py --check`: current after regenerating the reference.

## Lab walkthrough

Every step went through the Controller API as an operator would
(`.cache/software-raid/B/replace_test.py`); native state was read over the lab's ssh helper.
Pool leaves are named exactly as Core's observation reports them, which for a provisioned
repository is the by-id path of the partition ZFS created on the selected disk.

### 1. Replace a failed mirror member on the pool master

The `degraded-mirror` repository from slice 1 (`virtio-NEX-CORE1-DATA-5` and `-DATA-6`) with
two running VMs on it.

1. `zpool offline …-DATA-5-part1` → the Controller reported the repository `degraded`,
   readable and writable, naming the offlined leaf.
2. `POST /api/v1/storage/zfs/replace` naming the ONLINE leaf `…-DATA-6-part1` →
   `needs_attention`, `"/dev/disk/by-id/virtio-NEX-CORE1-DATA-6-part1 is online, so there is
   nothing to replace"`. No command touched a disk.
3. `POST /api/v1/storage/zfs/replace` naming the whole disk `…-DATA-5` instead of the leaf
   the pool reports → `needs_attention`, `"… is no longer part of this pool; check the pool
   before replacing a disk"`. The failed disk must be named as the observation reports it.
4. The real replacement, `…-DATA-5-part1` → the blank `…-DATA-4`: operation `succeeded`,
   and `zpool status` showed `resilvered 129M in 00:00:04 with 0 errors`, the pool `ONLINE`
   with `…-DATA-4-part1` and `…-DATA-6-part1`, and `…-DATA-5` gone — ZFS retired the old
   disk itself; nothing detached it.
5. The Controller reported `healthy`, readable and writable, with the finished resilver in
   the pool detail (`.cache/software-raid/B/report-replaced.json`).
6. Both VMs stayed `running` throughout, and the md5 sums of every file in the repository
   (both VM disk images and the two files written in slice 1) were identical before the
   disk was offlined and after the resilver finished.

### 2. Interrupting the agent between the wipe and the replacement

With `/usr/local/sbin/zpool` temporarily shadowed by a wrapper that makes only
`zpool replace` hang, so the interruption lands exactly between the two destructive steps:

1. The replacement was submitted and the agent was killed (`systemctl kill -s SIGKILL`) as
   soon as its journal entry appeared.
2. The durable journal on the host read
   `{"stage":"wipe-started","status":"running", …}` with the exact request, and
   `zpool status` showed the pool unchanged: the disk had been cleared, the pool had not
   been touched.
3. systemd restarted the agent, the Controller redelivered the same operation, and the
   host resumed it from the journal: it did not ask for a new confirmation and did not
   erase the disk again. With the wrapper still failing, the outcome was
   `needs_attention`, `"… could not be replaced with …"`, recorded on the same operation.
4. With the wrapper removed, a fresh replacement of the same two disks succeeded (step 1.4
   above), so the interruption left the disk in a state an ordinary retry repairs.

**What this did not cover.** The wrapper made `zpool replace` hang before it did anything, so
the pool was never resilvering when the retry ran: the retry exercised the resume past the
wipe, not the adoption of a pool that is already resilvering onto the replacement. Independent
review then found that adoption could never match a real pool and that the retry would have
cleared the live replacement ([BUG-0445](../BUG_LEDGER.md)). It is fixed and covered by two
unit checks, one of them against a derived `replacing-0` observation decoded by the real
decoder. **Outstanding:** rerun this case on a host with the interruption placed after
`zpool replace` has started, and keep the raw output.

### 3. A three-way mirror, and a mirror with a hot spare, on the member host

**No raw artefacts were retained for this section.** The steps below were run and their
output read at the time, but only the summary was written down, and the lab has since been
shut down. Treat them as reported, not as evidence; **outstanding:** rerun with the command
output retained.

Both were provisioned through the master, which forwarded the work to `core2` over the peer
channel.

1. Three blank disks with `layout: "mirror"` → `zpool status` showed one `mirror-0` with
   three ONLINE leaves.
2. That repository was detached through the API, its pool destroyed and its disks cleared
   natively (the lab has no pool-destroy operation; pool destroy is out of scope in the
   plan), freeing the disks for the next check.
3. Two blank disks plus one `spares` entry → `mirror-0` with two leaves, a `spares` section
   with the third disk `AVAIL`, and `zpool get autoreplace` = `on local`. The spare counted
   toward no part of the layout.
4. A data disk was offlined and replaced through the API with a fourth disk: the operation
   `succeeded`, the pool resilvered and returned to `ONLINE`, the spare stayed `AVAIL`, and
   the pool master's durable repository record then named the replacement disk instead of
   the failed one.

### 4. Refusals the Controller answers directly

- `core3`, whose Nex Core does not advertise the capability: `409` —
  `"replacing a ZFS disk needs storage-zfs-replace-v1 on the pool master and the selected
  host; upgrade Nex Core"`.
- A failed device that is not a stable by-id path: `400` — `"ZFS disk replacement names the
  failed device by its /dev/disk/by-id path"`.
- A replacement without a discovery fingerprint: `400` — `"ZFS provision device is
  invalid"`.
- The same request without the CSRF token: `403` — `"CSRF token required"`.
- A repository ID the pool master does not own on the named host: the master refused it
  (`failed`) without reaching any host.

### 5. Browser walkthrough

Chromium against the lab Controller, captures in `.cache/software-raid/B/shots/`:

1. The degraded `spare-mirror` repository's host row offers **Replace disk**; the
   repository panel shows the pool state and the failed leaf.
2. The dialog names the failed disk (`… · offline`), lists the replacement candidates from
   the same discovery listing the wizard uses, marks a disk with old data as
   "Has old data — will be erased", and keeps the six unavailable disks collapsed.
3. **Erase disk and replace** stays disabled until the by-id name of the disk being erased
   is typed in full.
4. Submitting queued `storage-zfs-replace`, which completed as `succeeded`; the pool
   resilvered and returned to `ONLINE` with the new disk, the spare untouched.

## Outstanding

These are part of this slice's acceptance and have not been run. A workstation freeze caused
by the lab VMs stopped all lab work (project owner, 2026-09-22), so none of them could be
completed in this change.

- Rerun the interrupted-retry case with the interruption after `zpool replace` has started,
  proving the adoption path fixed in BUG-0445 on a real pool, and retain the output.
- Rerun section 3 (three-way mirror, hot-spare pool, replacement over the peer channel) with
  the raw output retained.
- Capture `zpool status --json` of a pool with a hot spare, a log device and a cache device,
  so Core can report which class a leaf belongs to instead of leaving spares unlabelled (see
  the limitation now recorded in the API reference and the plan).
- Capture a genuine idle `ONLINE` pool for `internal/vmhost/testdata/`, and a real
  `replacing-0` observation, replacing the two derived fixtures.
- Look at the reworked screens in a browser. The disk tables, the Spare column, the live
  layout summary, the layout fieldset, the degraded host row with its resilver percentage and
  the renamed confirm button were written and unit-tested after the browser captures in this
  record were taken, so every capture here shows the earlier layout. Their behaviour is
  covered by `web/src/zfs.test.mjs` and `web/src/storage.test.mjs`; their appearance is not.

## Untested limits

- **A hot spare was never seen to engage by itself**, and this was not retested after the
  review fixes. The lab image has no `zfs-zed`
  service and no `zinject`, so neither a real device fault nor an injected one could be
  produced; an administrative `zpool offline` does not engage a spare. What was verified is
  that Nex creates the spare vdev with `autoreplace=on` and that the Replace action repairs
  a spare-backed pool. Automatic spare engagement is ZFS behaviour Nex does not implement,
  and it remains unverified here.
- No real hardware, no physically removed disk and no disk that fails while being written
  to: every failure was an administrative `zpool offline` of a virtual disk.
- Guest-level "the VM keeps writing" is still not provable in this lab: the disposable VM
  images carry no operating system. Host-side file checksums and the VMs' running state are
  what was verified.
- RAIDZ3 was not provisioned in the lab: five free disks were never available on one host.
  It is covered by unit tests of the layout rule and the vdev arguments only.
- The first replacement (step 1) ran against a Core built before the pool master's record
  was taught that a pool leaf is a partition of the selected disk, so `core1`'s repository
  record still names the disk that was replaced; `core2`'s record, written after the fix,
  names the replacement. The pool itself is authoritative either way.
