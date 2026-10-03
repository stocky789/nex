# Phase I-4c: pool rollout on three booted image hosts (nested lab)

Date: 2026-10-02. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), "Pool rollout coordinator", acceptance
checks 3 and 4 of the Updates section. Follows [phase4-rollout.md](phase4-rollout.md) (fake peers only) and
[phase5-core-image.md](phase5-core-image.md) (one booted host).

**Environment: simulation only.** Three QEMU/KVM guests (`-smp 2 -m 2048`, OVMF, Secure Boot off, three fresh sparse 14 GiB raw disks),
nested on one workstation, all started inside one `systemd-run --user --scope -p CPUQuota=600% -p MemoryMax=7G -p MemorySwapMax=0`
with `nice -n 19 ionice -c3`; the builds ran under `CPUQuota=200% MemoryMax=4G`, installs in a `--cpus 2 --memory 4g` container. No
Nex-built libvirt, no Corosync, no layer, no BMC and no real watchdog. Peak CPU and memory were not sampled. Every VM, the Controller
process and the hub/server processes were killed with `pool_rollout_lab.py down` and `pgrep` showed nothing left; the disks, the
Controller data directory and the build trees were deleted (the passwords were random per run and lived only in that directory).

- API impact: **none** (no route, request or response changed). Guide impact: **none**; the walk-through below is not an operator
  procedure (it uses a hand-run workstation Controller and lab shells), so `update-a-pool.md` stays **unverified**. State owner: unchanged
  (`pool-update.json` on the master).
- **Later changes are not covered by this run.** A branch review afterwards changed the coordinator (hosts already on the release are
  skipped instead of refused, abort and resume handling, guest-step refusals, step deadlines, replication on every stage change; see
  [phase4-rollout.md](phase4-rollout.md)) and added a `current` list to the dry-run plan. None of that was re-run in this lab; the
  observations below describe the build that ran then, and the later behaviour is fake-peer test evidence only.

## What was built and run

| Object | Result |
|---|---|
| Release 1 / 2 | `image/build.sh all` twice from the 14:14 image-edition `nex-core-local` deb (the one with the coordinator, [phase5-core-image.md](phase5-core-image.md)): labels `2026.10.1` (sequence 1, root hash `5845e7dc...`) and `2026.10.2` (sequence 2, root hash `8477a9b2...`), `NEX_UPDATE_TRUST` = the development trust of `scripts/update-publish.py keygen`, pinned into both. Published with `update-publish.py publish --channel dev` into a static tree served by a local range-capable HTTP server; the hosts reach it at `10.0.2.2` through QEMU user networking (`/etc/nex/updates.conf` seeded by `nex-hostimage install --etc-seed`). About 2 min per image with a cold package cache. |
| Hosts | `nexlab-a/b/c`, one install each (6-8 s), distinct random root passwords, each with a user-mode NIC (10.0.2.15; the workstation is 10.0.2.2) and a second NIC on a userspace L2 hub (QEMU `-netdev stream` to a Python frame-flooding hub on a unix socket; no root, no tap) with management IPs 10.77.0.11-13 and the default route on that NIC, so Core takes it as its address. Boot to shell about 8 s. |
| Controller | the real `nex-controller` binary (`go build ./cmd/nex-controller`) as a workstation process on `127.0.0.1:18443`, reached by the hosts as `https://10.0.2.2:18443`. The Controller *guest image* was not built. It enrolled the three hosts through `POST /api/v1/sites/{id}/hosts` (each reached over a loopback forward `127.0.0.1x:9443` to the host's management IP; Core listens on its management IP only, not `0.0.0.0`) and hosts 2 and 3 were joined to host 1's pool through `POST /api/v1/pools/{id}/members` (the Controller-driven join, not the hand-run `join-csr`/`join-offer`/`join` calls). Three `ready` members, master = nexlab-a. |

`tests/integration/pool_rollout_lab.py` is the harness: `up`, `enroll`, `dev`, `watch`/`api`/`capi`/`sh`, `rollout`, `resume`, `down`.
**The `rollout` subcommand was written after the run from the throwaway driver that actually ran it, and was not itself executed**; the
numbers below came from `up`, `enroll`, `dev`, `watch`, `api`/`capi` and that driver.

## Acceptance items

| Item | Result |
|---|---|
| Dry-run preflight over the real peer transport | **Passed.** `POST /api/local/v1/pool/updates/rollouts {sequence:2, dry_run:true}` on the master: 200, `plan.order = [member c, member b, master a]` (idle member first, the member with a running guest next, master last); nothing recorded. |
| `running_vms: refuse` with a running guest: pause naming the guest | **Passed**, with the six-hour `step_timeout` shortened to 90 s through `NEX_POOL_UPDATE_STEP_TIMEOUT` (see below). Started through the Controller (`POST /api/v1/pools/{id}/updates/rollouts`, 202). Host c (no guest) went through the whole cycle; host b, which ran an unmanaged libvirt domain `lab-guest` (UUID `7b0f3b3a-...a002`), stayed in `drain` and 90 s later the rollout paused `running_vms: <b host id>: running guests block the update: 7b0f3b3a-0000-4000-8000-00000000a002`. Host b stayed in maintenance while paused; the master was not touched. |
| `running_vms: shutdown` (guest stopped, restarted on the same host) | **Did not run in this lab; ran later in [phase6-lab4.md](phase6-lab4.md)** (virtual, PASS: a Nex-managed guest stopped gracefully through the rollout and started again on the same host, owner epoch 3 to 4). Refuse-with-guest was rerun in [phase6-lab3.md](phase6-lab3.md) with a real cluster and passed. A guest the coordinator can stop and restart has to be a Nex-managed VM with an OS that answers ACPI shutdown; creating one needs a storage repository, a network and a bootable ISO through the Controller, and there was no time to prepare them here. The unmanaged domain used above would not have been stopped by `vm-power` (not a managed VM). After the pause the guest was destroyed by hand (my own test domain) and the rollout resumed with `refuse`; no guest was restarted by the rollout. |
| Members then master; never two hosts in maintenance | **Passed (polled).** Order observed: c, b, then master a, each `staged, drain, activate, boot, confirm, release, restart, soak, done` (as the master's record named the stages; `teardown` is passed through quickly). The master's membership was read about every 3 s: never more than one host in maintenance; the flag cleared at `release`. Between polls a shorter overlap cannot be excluded; the fake-peer test asserts it at every step. Host c: `activate` at 15:06:00, back and `confirm` about 25 s later, 60 s `soak`. All three hosts finished `booted 2026.10.2 / previous 2026.10.1`, phase `committed`; rollout `finalized`, `finalized_sequence: 2`. |
| Quorum margin respected | **Not exercised in this lab; later exercised with a real Corosync in [phase6-lab3.md](phase6-lab3.md) (passes with 3 votes, refuses with one host not voting).** The image has no Corosync, so there was no quorum to read. The lab found a defect here (below): the gate refused the whole pool; with the fix it correctly treats a pool without cluster software as having no quorum to lose. The vote arithmetic is still only fake-peer tested. |
| Kill the Controller mid-rollout | **Passed, with a caveat.** `SIGKILL` of the Controller process at 15:14:27 while host b was in `soak`; the rollout carried on (b `done` 15:14:51, a `teardown` 15:14:54, ..., `finalized` 15:17:10) with the Controller down for 27 s. The Controller was started again **on the same data directory** (not an empty one), and `GET /api/v1/pools/{id}/updates` returned `state: finalized, finalized_sequence: 2` and the three host stages equal to the master's own record. A Controller with an empty database cannot be pointed at these hosts without re-enrolment, so "a fresh Controller reads it from the master" was not proven separately from the Controller's retained inventory. |
| Kill the master during its step | **Partly here; the master-change case ran later in [phase6-lab4.md](phase6-lab4.md)** (virtual, PASS: master power-cut, a member promoted through the Controller, `paused master_changed` on the new master, resume and abort). The master VM was power-cut (`kill -9` of QEMU) 6 s into its `boot` stage (after it had armed and rebooted into release 2). It restarted, the rollout record was read from disk and the rollout went on to `confirm`, `release`, `restart`, `soak`, `done`, `finalized`; no step repeated destructively. This is a master crash and restart, **not** a master change. Killing the master in the `activate` stage, before the new release was armed, was also not run. |

## Defects found

1. **Quorum gate refused every pool without cluster software (`quorum_margin`).** After staging, the first member's gate stopped with
   `this pool keeps a Corosync cluster and the master cannot read its quorum`: a three-host pool intends a cluster (`poolClusterIntent`),
   and the image without the `nex-cluster` layer has no `corosync-quorumtool`, so `LocalQuorum` failed with `exec.ErrNotFound`, which the
   gate treated as an unreadable quorum. A three-host image pool could never leave `quorum_margin`. Root cause: the gate did not tell "no
   cluster software, so no cluster, no votes, no watchdog fed" from "cluster software that cannot be read". Fix (`internal/agent/pool_update.go`
   `poolUpdateQuorumGate`): `exec.ErrNotFound` with no clustered storage means nothing to lose; an installed-but-unreadable Corosync and clustered
   storage still refuse. Regression test: the last case of `TestQuorumGateVoteArithmetic` (observed to fail with the fix disabled).
   The paused rollout resumed after the fixed binary was installed and Core restarted (the pause is in the persisted record).
2. **Six-hour `step_timeout` cannot be watched in a lab.** Not a defect; the lab needed `NEX_POOL_UPDATE_STEP_TIMEOUT` (a Go duration of at
   least one second, read once when the coordinator starts, otherwise six hours). It is an operator-visible knob only in the sense that root can
   set it in the unit environment; it is not documented as a setting and no API reports it.

## Lab-only shortcuts (the hosts did not run the shipped binary)

The step-timeout override and the gate fix were not in the images (they were built from the 14:14 deb, before either change, and a rebuild was not affordable). The binary was built on the
workstation (`go build -trimpath`), fetched by each guest from the workstation, and **bound over the read-only `/usr/lib/nex/nex-core-local`
by a unit in `/etc/systemd/system`** (`nex-lab-bind.service`, `Before=nex-core-local.service`, re-made at every boot, so both releases and every
reboot of the rollout ran that binary) with a drop-in setting the step timeout to 20 min and then 90 s. So the rollout coordinator, the host steps
and the guest handling that ran were the workstation build of the current tree, not the packaged deb; the engine, the verity root, the boot
assessment and the image update path were the images'. Every rollout run here used a workstation build: the first pause (defect 1) came from a build with the step-timeout override but without the gate fix, so the packaged deb was never the coordinator in this lab. The step timeout was 90 s
for the whole run, which any "cannot be reached" step would also have hit; none did.

## Not run / open

- `running_vms: shutdown` on a managed guest (guest stopped and restarted on the same host) and the real default `localPoolUpdateGuests`
  power path; the refuse path only read `virsh list --uuid`.
- Quorum margin with a real Corosync; clustered storage; watchdog re-arm.
- Master change (`master_changed` after a promotion) and a master kill before its new release is armed.
- A rollout started from the hand-run host-local routes with a Controller that was never present; a Controller with an empty database.
- `scope:"controller"` and `controller_first` (no Controller appliance on any host); air-gapped metadata; a rollout of a release with a
  state-format change.
- A rollback of a failed host (the boot counter, `rolled_back`), the soak/health checks beyond membership, and more than one rollout in a
  row. Peak resource use. The `rollout` subcommand of the harness.
- Matched libvirt and dlm are still not in the image ([phase5-core-image.md](phase5-core-image.md)).

## Commands that ran

All from the repository root, heavy ones under the caps above (`GOMAXPROCS=2`, `CPUQuota=200%`, `MemoryMax=4G`): `image/build.sh all` twice,
`go build ./cmd/nex-controller ./cmd/nex-core-local`, `go test -p 1 -count=1 -run 'TestQuorumGate|TestRollout|TestGate' ./internal/agent`,
`pool_rollout_lab.py up` (in the 600%/7G scope), `resume` (after a scope was killed by a tool timeout), `enroll`, `dev`, `watch`, `api`/`capi`/`sh`,
`down`. Progress was logged in the log below.

## Progress log

- 14:23 start; releases 1 and 2 built by 14:30 (first `up` stopped when `/healthz` did not answer: Core listens on the management IP only,
  forward retargeted live through QMP `hostfwd_add`); 14:47 enrolled and joined; 14:57 dev binary and step timeout; 15:00 first rollout
  paused `quorum_margin` (defect 1); 15:05 fixed binary, resumed; 15:09 `running_vms` pause; 15:13-15:17 b and a, Controller kill,
  master power cut, finalized; 15:20 down, pgrep clean, disks deleted.
