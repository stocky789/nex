# Phase I-6 local lab 3: pool rollout with a real Corosync cluster (QEMU/KVM)

Date: 2026-10-02. Follows [phase4-lab.md](phase4-lab.md) (its "did not run" and "partial" items). **Everything here is SIMULATION**: three nested
QEMU/KVM guests, a userspace L2 hub, a lab Redfish endpoint on the workstation, user-mode networking. Nothing establishes physical-hardware
behaviour. Peak CPU and memory were not sampled.

- API impact: none (no route, request or response changed). Guide impact: none; `update-a-pool.md` stays unverified. State owner: unchanged.
- Resource rules kept: one `systemd-run --user --scope` (`CPUQuota=600%`) around all QEMU, builds under `CPUQuota=200% MemoryMax=4G`, no swap,
  `nice -n 19 ionice -c3`, at most 3 VMs. **Deviation (approved by the main agent):** the scope was `MemoryMax=8G` and the host that carries the guest
  (`nexlab-b`) ran `-m 3328` instead of 2048 (3072 was tried first: MemAvailable 2498 MiB, below the 2560 MiB Core demands for a 512 MiB guest plus its
  2 GiB reserve). Hosts a and c ran `-smp 2 -m 2048`. Host b's disk was grown online from 14 to 24 GiB (sparse): Core needs guest disk + 4 GiB free.
- Every VM, the Controller, the update server, the hub and the lab BMC were killed (`pool_rollout_lab.py down`, `pgrep` showed none left, `docker ps`
  showed only the two unrelated containers that were already running); lab disks, build trees and release 3's partial output were deleted.

## Progress log

- 17:37 start; 17:38 deb built from the current tree (26 s); 17:46 release 1 + layer; 17:50 release 2; 17:55 hosts enrolled and joined (Controller route).
- 18:00-18:12 two layer defects and one agent defect found, fixed, layers rebuilt, hosts rebooted and re-ensured; 18:14 three votes.
- 18:25 quorum gate run; 18:29 refuse-with-guest paused; 18:33 release 3 build started for the shutdown and master-change runs, then stopped at the time box.

## Results (all virtual)

| # | Item | Result |
|---|---|---|
| 1 | Build the real `nex-cluster` layer (Debian `corosync corosync-qdevice pacemaker pacemaker-cli-utils pcs resource-agents fence-agents-* sbd dlm-controld gfs2-utils sanlock libvirt-daemon-plugin-sanlock`, snapshot 20260929) for releases 1 and 2 | **PASS** with the stock Debian `dlm-controld`/libvirt (not `+nex1`/`+nex4`; **those were not built**, so GFS2/dlm as the product intends them are **NOT RUN**: no `dlm_controld` was started, no GFS2 volume made) |
| 1 | Published as a layer component of release 1 and 2 (`update-publish.py publish --layer`, `requires_base` = `2026.10.N-N`) | **PASS** |
| 1 | Installed and live-merged: store files and signed manifest laid in the host's layer store (what an engine stage leaves; the lab installs release 1 from `nex-hostimage`, which stages no layers), `nex-core-local layers ensure nex-cluster`, `systemd-sysext status` shows `/usr nex-cluster`; layer rebuilt after the defects and re-ensured after a disable and reboot | **PASS** (the engine path `check/apply` with layers was not run on a host without a layer store: it answers `409 layers_unavailable` by design) |
| 1 | Corosync starts | **PASS after two layer fixes** (below); first attempt failed on all three hosts |
| 2 | Packaged `nex-core-local` deb in the image (no workstation binary bind) | **PASS**: `0.1.5~alpha1+img3` built from the current tree, installed by `image/build.sh base`; the hosts ran no other binary. The defect fix below is in the tree and **not** in these images |
| 3 | Real pool cluster through the product flow: Controller enrol + add-member, Core's `ReconcilePoolCluster` wrote the nodelist | **PASS**: 3 nodes, `Quorate: Yes`, expected 3, total 3, quorum 2 (`corosync-quorumtool -s` on each host), `Flags: Quorate WaitForAll`. Watchdog arming ("waiting for every host") was not observed to complete; sbd stayed inactive |
| 3 | Quorum gate, all three up | **PASS**: `dry_run` returned the plan c, b, a; the real rollout's first gate (host c) passed with 3 votes, c went through its whole cycle |
| 3 | Quorum gate, one host's corosync stopped | **PASS**: `corosync` stopped and runtime-masked on b (without the mask Core restarted it within seconds); `corosync-quorumtool -s` on the master: `Nodes: 2, Total votes: 2, Quorum: 2, Quorate: Yes` (a further stop would leave 1 < 2); the rollout paused `quorum_margin: host <b> is ready but not voting in this pool's quorate partition` at the gate of host c, before any host entered maintenance (`maint: []`). After unmask the cluster returned to 3 votes and `resume` continued |
| 4 | Master power cut mid-rollout, member promoted, `paused master_changed`, resume/abort | **NOT RUN**. A lab Redfish endpoint (kills QEMU on ForceOff) and the fence profile route were set up and the master's own profile was recorded, but no promotion was attempted: the time box ended. The earlier lab's "master restart, not a change" result stands |
| 5 | `running_vms: shutdown` with a managed guest, same-host restart, one owner epoch | **NOT RUN through the rollout.** Prepared and shown: a managed VM created through the Controller (`POST /api/v1/vms`, BIOS, 512 MiB, 1 GiB sata disk, default local storage) on b, a tiny guest (`tests/integration/pool_lab_guest.sh`: syslinux disk, the Debian kernel `pool_lab_guest.sh` downloads (6.12.107 in this run), busybox initramfs that waits for the ACPI power-button input and powers off) copied into the VM's qcow2 (lab shortcut), started by the product's `vm-power start`, and it **powers off about 2 s after `virsh shutdown`**. The rollout that would stop and restart it needed release 3 (sequence 2 cannot be started again once one member runs it: `release_unavailable`), whose build was cut off |
| 6 | Refuse-with-guest, rerun with a real cluster | **PASS**: with the managed guest running on b, c completed, then the rollout paused `running_vms: <b>: running guests block the update: <guest id>` after the shortened step timeout (100 s, `NEX_POOL_UPDATE_STEP_TIMEOUT` in a drop-in), b stayed in maintenance, the master was not touched; `abort` cleared b's maintenance and the guest kept running |

## Defects found and fixed

1. **The cluster layer could not start Corosync** (`Cannot chdir to state directory /var/lib/corosync`): a layer may not ship `/var`, Core runs
   `systemd-tmpfiles` after the merge, and the Debian packages create the folder in maintainer scripts. Fix: `image/mkosi.images/nex-cluster/mkosi.extra/usr/lib/tmpfiles.d/nex-cluster-state.conf`.
2. **Pacemaker could not start** (`Cluster user hacluster does not exist`): the overlay build output carried no `etc/passwd`/`group`, so `layer-finalize` generated no sysusers (the `adduser` in `pacemaker-common`'s postinst left nothing in the output; **root cause not found**). Fix: `usr/lib/sysusers.d/nex-cluster-accounts.conf` with fixed ids (`hacluster` 189, `sanlock` 179). Proven live on a host first (Pacemaker active, `crm_mon` answers), then in the rebuilt layer. Regression test: `tests/test_image_composition.py::test_cluster_layer_declares_the_state_folders_and_accounts_the_packages_made_in_their_scripts` (checks the files; the VM run is the behavioural proof).
3. **A fence-profile command stopped the master's agent.** `ExecutePoolFenceProfile` wrote the pinned certificate into the `PoolCommand` it shares with the agent's journal entry, so the Controller's replay of the same operation ID compared unequal and the agent exited with `controller reused command ID with different input` (the master then formed no cluster). Fix: work on a copy (`internal/agent/pool_promote.go`). Regression test `TestRecordingAManagementControllerLeavesTheJournaledCommandUntouched` (`internal/agent/pool_fence_replay_test.go`): fails on the old body (the command gains `tls_fingerprint`), passes now. **The lab images predate this fix.** To continue the lab the three stuck `pool-fence-profile` operations were set to `failed` in the lab Controller's database and the stale journal entry was removed from the master; those were lab repairs, and only the master's own profile had been recorded. Why the original command stayed `pending` for twelve minutes before the restart was not found.

Also seen, not changed: starting a managed guest after it had been stopped out-of-band with `virsh` is refused (`VM execution is not eligible to start`) until the product records a stop (`vm-power shutdown`); a 14 GiB lab host cannot create a VM (Core asks for the disk plus 4 GiB free).

## Not run

Master change and `master_changed` (item 4), the shutdown rollout (item 5), GFS2/dlm and the `+nex1`/`+nex4` builds, qdevice witness, watchdog arming, sbd, the engine's layer staging on an installed host, any physical hardware.

## Reproduce

```
# pool_rollout_lab.py keeps its state in .cache/lab4 (LAB), so the source archive and the Controller binary go there too
python3 scripts/build-source.py --output .cache/lab4/nex-source.tar.gz
python3 packaging/core-local/build.py --edition image --source .cache/lab4/nex-source.tar.gz --version 0.1.5~alpha1+img3 --output .cache/lab4/debs
mkdir -p .cache/lab4/bin && go build -trimpath -o .cache/lab4/bin/ ./cmd/nex-controller ./cmd/nex-hostimage
# per release N (label 2026.10.N, sequence N): image/build.sh base; image/mkosi.images/build-layer.sh nex-cluster; image/build.sh release
python3 tests/integration/pool_rollout_lab.py up --rel1 R1/release --rel2 R2/release --keys K --trust T --hostimage H   # R*/ hold nex-cluster.raw[.json], base.dpkg-list
for c in login enroll layer fence; do python3 tests/integration/pool_rollout_lab.py $c; done
tests/integration/pool_lab_guest.sh guest.raw   # downloads the kernel itself; the argument is the output disk, never an existing file
```
