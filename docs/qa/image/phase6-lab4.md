# Phase I-6 local lab 4: master power cut and guest shutdown through a pool rollout (QEMU/KVM)

Date: 2026-10-02. Follows [phase6-lab3.md](phase6-lab3.md) (its items 4 and 5 did not run). **Everything here is SIMULATION**: three nested
QEMU/KVM guests, a userspace L2 hub, a lab Redfish endpoint on the workstation, user-mode networking. Nothing establishes physical-hardware
behaviour. Peak CPU and memory were not sampled. Hosts are called a, b and c (a was the pool master, b carried the guest); no host or pool
identifiers are recorded.

- API impact: none (no route, request or response changed). Guide impact: none; `update-a-pool.md` stays unverified. State owner: unchanged.
- Resource rules: the lab's QEMU processes ran in `systemd-run --user --scope` (`CPUQuota=600% MemoryMax=8G MemorySwapMax=0`,
  `nice -n 19 ionice -c3`), at most 3 VMs (hosts a and c `-smp 2 -m 2048`, b `-m 3328`); builds, Go binaries and the guest disk under
  `CPUQuota=200% MemoryMax=4G`, docker `--cpus 2 --memory 4g`. **Deviation:** host a was restarted three times (after the boot loop, and
  after the power cut) in its own scope (`CPUQuota=200% MemoryMax=3G`) because a scope ends with its commands; there were never more than
  three VMs, one of which was briefly a throwaway boot of a's disk (`-snapshot`) to tell a bad disk from a bad launch. Host b's disk was
  grown online from 14 to 24 GiB by hand (below). Every VM, the Controller, the hub, the update server and the lab BMC were killed
  (`pool_rollout_lab.py down`; `pgrep` found none left, `docker ps` showed only the two unrelated containers that were running before); the lab disks, build trees,
  releases, Go binaries and the guest disk were deleted (the root-owned build output with a container).

## Progress log

- 18:35 start; the deb, release 1, 2 and 3 (one `nex-core-local` deb, `0.1.5~alpha1+img4`, built from the current tree, including the
  fence-replay fix; one base, one `nex-cluster` layer and one release each, about 5 min per release with a cold package cache) built by 18:47.
- 18:48 hosts installed (the rollout step timeout, 120 s, is seeded into the image's `/etc` overlay at install: no later repair), guest disk built.
- 18:51 host a stuck in a boot loop at the boot menu (below), found at 18:55, harness fixed, a restarted.
- 19:02 three hosts up, layer merged; 19:06 enrolled and joined; corosync would not start (below), marker written by hand, 19:08 three votes
  and fence profiles recorded through the Controller. 19:11-19:26 the managed guest took four tries (below). 19:27 rollout started.
- 19:29 quorum gate check, resume; 19:31 host c done; 19:33 b stuck in teardown (below); 19:36 master power cut, c promoted 19:36:57;
  19:37 `master_changed`, resume; 19:38 old master back; 19:39 guest restarted; 19:40 abort; 19:41 everything killed and deleted.

## Results (all virtual)

| # | Item | Result |
|---|---|---|
| 0 | Images rebuilt from the current tree (deb with the fence-replay fix, 3 releases, one `nex-cluster` layer each, pinned dev trust) | **PASS** |
| 0 | Three-host pool with a real Corosync through the product flow (Controller enrol + add-member, Core's `ReconcilePoolCluster`) | **PASS after one hand-made marker**: `Nodes: 3`, `Quorate: Yes`, expected/total votes 3, quorum 2 on every host. Without the marker Core refused the layer's vendor `corosync.conf` as "administrator Corosync configuration" (defect 4); the product fix is in the tree and **not in these images** |
| 6 | Quorum gate, re-run on fresh images | **PASS**: with b's `corosync` stopped and runtime-masked (`Nodes: 2, Quorate: Yes`) the rollout started, staged everywhere and paused `quorum_margin: host <b> is ready but not voting in this pool's quorate partition` at c's gate, before any host entered maintenance (`maint: []`). After unmask 3 votes returned and `resume` carried on into c's cycle |
| 4 | Master power cut mid-rollout, member promoted through the product flow, `paused master_changed` on the new master, resume and abort | **PASS**, with the notes below. Rollout `running_vms: shutdown`, release 2 from release 1, order c, b, a. c finished (`done`); b was in maintenance, stage `teardown`; a, the master, was power-cut (`kill -9` of its QEMU, 19:36:06). Through the Controller: promotion preflight on c (`ready_to_fence: true`, fence list = a only, `replica-integrity`, `replica-supremacy`, `accepted-ownership` passed, `old-master-exclusion` `unknown` as it must be before the fence), then `POST .../master/promote` with the verdict's own fence list and the pool's typed name. c reported itself pool master 35 s after the preflight began (19:36:22 to 19:36:57; the first poll still saw a, the next c). On c the rollout read `paused/hosts/master_changed: the pool master changed during the rollout; resume or abort it`, with b still in maintenance and the stages as the old master had left them. `resume` through the Controller (the operation was addressed to c) turned it `running` and b went on from `teardown`. `abort` through the Controller (to c) then gave `aborted/hosts`, maintenance cleared, and the Controller's `GET .../updates` showed the same record. **Not run:** abort *directly from* the `master_changed` pause (the abort here was of the resumed, running rollout; the code path is the same `Control` call); a cut earlier than the first reboot of a member, or while a member is down (a promotion over a rebooting member would fence it) |
| 5 | `running_vms: shutdown` through the rollout with a Nex-managed VM: graceful stop, restart on the **same** host after confirm, one owner epoch, never two writable instances | **PASS**, in the same rollout as item 4 (run by the **new master c** after the promotion). Guest: Controller-created managed VM on b (BIOS, 512 MiB, 1 GiB sata disk, no network), a 64 MiB tiny guest in its qcow2, started by the product's `vm-power start`; running with owner epoch 3 before the rollout. The rollout recorded `stopped_by_update: [guest]` on b, then (after a had come back and could acknowledge the replica) the guest stopped itself (QEMU log: `terminating on signal 15 from libvirtd`, `shutting down, reason=shutdown`, which libvirt logs for a guest-initiated poweroff because it runs QEMU with `-no-shutdown`; a destroy logs `reason=destroyed`) at 19:38:38, about 16 s before b's new kernel started (19:38:54, release 2); b confirmed (19:39:03), maintenance cleared (19:39:12), and the guest started again at 19:39:20, on b. Core's managed journal on b holds exactly one `vm-power` `shutdown` and three `start` entries: two sent by the Controller (64-hex operation IDs, one of them refused for memory) and one with a 32-hex ID, the form `poolUpdateOpID` makes, as the `shutdown` has. Owner epoch on the master c after the restart: **4** (one new epoch for the one restart; 3 before). `virsh list --all` on a and c showed no domain; only b ran the guest. a's own stale pool record still says epoch 3 (it was fenced and is no longer authoritative). **Not shown:** that the QEMU `reason=shutdown` came from the drain step rather than from the host's reboot, except by the journal ID and the timing (the guest was gone 16 s before the kernel restarted); the guest's own console was not captured |
| 6 | Refuse-with-guest, re-run on fresh images | **NOT RUN** (time box). Passed in [phase6-lab3.md](phase6-lab3.md) on the earlier images; nothing here changes that |
| 4 | Safe rejoin of the old master | Seen, not the goal: a was started again from its disk at 19:38 (release 1), booted, joined the lab shell and the pool again with a stale record; the coordinator then replicated to it and moved on. No further check was made of its restricted/rejoin state |

## Defects found

1. **A boot loop that was the harness's own keystrokes.** `up` waited for a's lab shell first and probed its second serial port every
   couple of seconds from the first instant. The firmware reads that port as keyboard too, so the probes drove systemd-boot's menu
   ("Reboot Into Firmware Interface") and a booted nothing for seven minutes; a throwaway boot of the same disk with plain arguments came
   up in seconds, which told the disk from the launch. Half-lines from the probes also left the lab shell in an unterminated `$(`. Fix:
   `Host.booted()` (the console shows kernel output after the last firmware start) gates every probe in `wait_shell`
   (`tests/integration/pool_rollout_lab.py`). Not covered by an automated test; the proof is the run after it.
2. **Harness checks and commands that had gone stale:** the member check of `enroll` read an old response shape; `guest` used the pool a
   joined host was enrolled into instead of the master's pool, kept an id before the VM existed, and did not stop on a refused create;
   `promote` had the same pool-id slip. Fixed. A host needs its disk bigger than the 14 GiB default to create a VM (Core wants disk
   plus 4 GiB free): the host entry for b now carries a 24 GiB disk at install; **in this lab it was grown by hand** (QMP `block_resize`,
   `sfdisk`, `partx`, `resize2fs`), since `up` had already run. One chained `sh` command of `guest` (download the guest disk, find the
   disk path with `virsh domblklist | awk`, `qemu-img convert`) timed out once in the harness; the same steps by hand ran in under a
   second each; **cause not found**.
3. **The seed.** The 120 s step timeout is now part of the install seed (`--etc-seed`), and the `dev` subcommand with its bind-mounted
   workstation binary is deleted: these hosts ran only the packaged binary.
4. **The layer's vendor `corosync.conf` was refused on an image host.** The layer ships Debian's conffile read-only under
   `/usr/share/factory` and the merge copies it into `/etc`; Core replaces a vendor file only when packaging's marker
   (`corosync-unconfigured`, written by an apt postinst) binds to its bytes, and nothing writes that marker on an image host, so
   `ReconcilePoolCluster` returned "refusing to replace administrator Corosync configuration" on every pass and no cluster formed (the earlier lab
   escaped it because the cluster formed before the layer was merged). Fix: `corosyncUnconfiguredMarker` also accepts a file equal to the layer's
   read-only factory copy (`internal/vmhost/storage_cluster.go`). Regression test
   `TestCorosyncUnconfiguredMarkerAcceptsOnlyTheLayersFactoryCopy` (accepts the factory copy, refuses an edit, refuses with no factory copy and
   no marker); it was **not** run against the old body, and the fix is **not** in these images (the marker was written by hand on each host).

## Seen, not changed

- **A stopped Pacemaker leaves the host's watchdog record stuck at `feeding: true`.** To give b the 2560 MiB Core demands before it starts a
  512 MiB guest (b's `MemAvailable` was 2517 MiB with Corosync at 219 MiB resident; stopping Pacemaker and pcsd gave 2634 MiB) Pacemaker was stopped
  on b by hand. b then reported `armed: false, feeding: true, "this host is not running this pool's Pacemaker cluster..."`, and the
  rollout's `teardown` step ("no ready member's watchdog is feeding") paused `step_timeout` (120 s) on it. Putting Pacemaker back let Core's own teardown
  proceed; the lab then went on. The cause is my intervention, but a host whose Pacemaker stops while its record says it feeds is a state the
  teardown wait cannot leave.
- **The guest drain waits for the replica to reach every member**, a dead old master included: after the promotion b sat in `drain` with the
  guest still running until a was started again; this is the design's guard ("a master lost after that point must not leave guests nobody owes a
  restart"), and what the operator sees is a rollout that is `running` but not moving. A rollout that is waiting for a fenced host could say so.

## Not run

Refuse-with-guest on the new images, abort straight from `master_changed`, a master cut at `b:soak` (the harness has `--cut-master-at` for it; the
cut here came at b's `teardown` because b was stuck there), GFS2/dlm and the `+nex1`/`+nex4` builds, qdevice witness, sbd/watchdog arming checked
by hand, the engine's layer staging on an installed host, any physical hardware, peak resource use.

## Reproduce

```
python3 scripts/build-source.py --output .cache/lab4/nex-source.tar.gz
python3 packaging/core-local/build.py --edition image --source .cache/lab4/nex-source.tar.gz --version 0.1.5~alpha1+img4 --output .cache/lab4/debs
# per release N (label 2026.10.N, sequence N): image/build.sh base; image/mkosi.images/build-layer.sh nex-cluster; image/build.sh release
go build -trimpath -o .cache/lab4/bin ./cmd/nex-controller ./cmd/nex-hostimage
pool_rollout_lab.py up --rel1 R1/release --rel2 R2/release --keys K --trust T --hostimage H      # inside one capped scope
pool_rollout_lab.py login; enroll; layer; fence        # images without the vmhost fix also need corosync-unconfigured written by hand (sha256 of /etc/corosync/corosync.conf, mode 0600)
tests/integration/pool_lab_guest.sh guest.raw; pool_rollout_lab.py guest guest.raw
pool_rollout_lab.py rollout --running-vms shutdown --seconds 900      # then: cut 1; promote 3; control resume; watch; control abort
```
