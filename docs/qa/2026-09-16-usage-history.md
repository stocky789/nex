# Usage history disposable-host verification — 2026-09-16

Plan: [usage history](../plans/2026-09-16-usage-history.md), step 4. Guide:
[Read VM and host usage history](../guide/usage-history.md).

## Build and environment

- Source: commit `ce0c83c` plus the **uncommitted** usage-history working tree, including the
  fixes for findings 1–3 and 5 and the realtime (`range=live`) and chart legend/tooltip/Realtime
  drawer additions (not a release). The tree also held unrelated uncommitted work from another
  session. Artifact SHA-256: controller `74004480…09a6`, agent `a57a2159…b27a`, `web/dist/app.js`
  `13ba8d80…945f` (full digests in [results.json](usage-history/results.json)). The unit's later
  `After=time-sync.target` change is not part of what the harness deploys.
- Host: private copy of a disposable nested-KVM appliance lab image (Nex Core 0.1.0-alpha1 base,
  Debian trixie, libvirt 11.3.0, 4 vCPU / 8 GiB virtual hardware). **Simulated hardware**, not a
  physical host. The agent under test was this tree's build, run as a systemd unit with a
  **1 s heartbeat** (default 10 s) to shorten the run.
- Controller: this tree's build on the workstation, fresh database, self-signed certificate.
- Guest: 2 vCPU, 1 GiB `maximum` memory mode (virtio balloon), 2 GiB thin virtio disk, NAT NIC,
  booted from the Debian 13.6 netinst ISO and left idle at its boot menu.
- Browser: headless Chromium 151 (Playwright), uPlot 1.6.32.
- Test tooling: `strace` was installed on the disposable host copy to count the agent's `virsh`
  executions (L3); tracing briefly slows the traced agent.

## Method

`GO=.cache/toolchains/go/bin/go .cache/phase1-qa-venv/bin/python tests/integration/usage_history_lab.py --copy-from <disposable nex-appliance-lab.*>`
(about 20 minutes). Evidence is sanitized: VM UUID, host ID and per-run suffixes are replaced with
`fixture-vm-01`, `fixture-host-01` and `<run>`. An earlier run of the same harness failed on its own
too-strict check (it compared the step still in progress at restart) and was discarded. The second
run completed and reported findings 1–3 below; after they were fixed, a third run with the same
harness verified them. A coordinator's intermediate rerun attempts failed at `agent enroll` without
its stderr; the harness now prints that stderr, and later runs enrolled normally, so that failure did
not reproduce and its cause is unknown. The realtime checks (L1–L6) were then added. Two runs
stopped on harness faults (a `pgrep` observable too slow to see sub-20 ms `virsh` calls, replaced by
`strace`; a tooltip selector matching a hidden realtime chart's tooltip). The next complete run
passed everything except L5 (finding 5). After finding 5 was fixed, the harness was run once more,
on 2026-09-17, with no extra VMs. The **table and evidence are from that final run**, in which every
check passed; L4 was not exercised (see below).

## Results

| # | Check | Result |
|---|-------|--------|
| 1 | Real `virsh list --uuid --name` prints `uuid name`; `virsh domstats --raw --nowait --list-running --cpu-total --vcpu --balloon --block --interface` prints `Domain: 'name'` blocks with `cpu.time`, `vcpu.*`, `balloon.*`, `block.*`, `net.*` ([list](usage-history/virsh-list-uuid-name.txt), [domstats](usage-history/virsh-domstats-list-running.txt)) | Pass |
| 1a | New VM definition carries `<stats period='5'/>` on its virtio balloon | Pass |
| 2 | After 150 s running: VM 10m has known points for every series (29–30 of 120 each, `memory.guest_used` 23); VM 24h has known minutes for every series; host 10m has 34–35 known points for CPU total and 4 cores, memory, 3 NICs (`nex0`, `nex1`, `virbr0`), 2 disks and load | Pass |
| 2a | Latency: first VM query 0.75 s (channel opened; heartbeat 1 s), repeat queries 0.002–0.003 s | Measured |
| L1 | VM `range=live` polled every 1 s for 20 s: `step_seconds` 1, 120 values per series, CPU/memory/disk/network groups; newest `cpu.total` known on 17/17 polls after the first 2 s; CPU 0.55–0.86 % (idle guest); latency median 0.002 s, max 0.003 s | Pass |
| L2 | Host `range=live` the same way: newest CPU known 17/17; CPU 2.0–3.5 %; 24 series including load; latency median 0.002 s, max 0.003 s | Pass |
| L3 | While the lease was active: 10 `virsh domstats … <vm-uuid>` executions by the agent in 10 s. After the last query + 2 min lease + 5 s: 0 in 10 s. `usage/` entries and sizes identical before and after (`host.rrd`, `vm-….rrd` only; minute-flush mtimes not compared) | Pass |
| L4 | Cap of 8 realtime objects → 429 through the Controller | **Not run** — needs 9 objects the Controller knows; the workstation memory budget allowed no extra guests. Covered by unit tests (`internal/agent/usage_history_live_test.go`, `internal/controller/stats_test.go`) |
| 3 | Force stop: last five 5 s CPU points `null`, earlier points kept (gap, not zero) | Pass |
| L5 | Realtime for the force-stopped VM after 5 polls: HTTP 200 with 0 known points (all nulls), while libvirt still returned `cpu.time` and `balloon.*` for the shut-off domain (`stopped_vm_domstats` in results.json) | Pass (fixed; failed in the previous run, see finding 5) |
| 3a | Agent restart: channel reopened (query 0.14 s); all 35 finished 24h points recorded before the stop still served | Pass |
| 3b | The minute the VM stopped in (partial step) after waiting past that minute plus one flush, then restarting the agent | Pass (fixed; failed in run 2, see finding 1) |
| 4 | Controller replacement (new port, certificate and empty database; ordinary re-enrollment): VM 24h first query 0.94 s; 0 of the earlier VM points and 0 of 192 finished host 24h points missing | Pass |
| 5 | `range=5m` → 400; unknown VM → 404; no session → 401 | Pass |
| 6 | Browser: VM Stats tab (10m, Stacked, 24h) and host Stats tab (5 charts) render real history with no page errors; screenshots inspected ([VM 10m](usage-history/vm-stats-10m.png), [VM stacked](usage-history/vm-stats-10m-stacked.png), [VM 24h](usage-history/vm-stats-24h.png), [host 10m](usage-history/host-stats-10m.png)) | Pass; findings 2–3 verified fixed |
| L6 | Browser Realtime drawer: no `range=live` request before the checkbox; after ticking it, Live badge shown and 14 live requests in ~11 s, realtime canvas changed over 3 s; hover on a 24h history chart shows the tooltip (time, series, values); clicking the CPU legend chip sets `aria-pressed=false` and strikes it through, clicking again restores it; collapsing the drawer removes the badge and 0 live requests followed in 6 s ([realtime](usage-history/vm-stats-realtime.png), [tooltip](usage-history/vm-stats-tooltip.png), [chip](usage-history/vm-stats-chip-hidden.png), [closed](usage-history/vm-stats-realtime-closed.png)) | Pass |
| 7 | Delete the stopped VM with its disks: `usage/vm-<uuid>.rrd` removed; its stats query then returns 404 | Pass |
| 8 | File sizes: `host.rrd` 244 316 bytes (24 series), `vm-<uuid>.rrd` 122 184 bytes (12 series); `usage` check `healthy` | Pass |

## Findings

1. **Product bug — a stopped VM's unfinished steps are never persisted.** A step is written only
   when a later sample for the same object arrives (`ringFile.finish` is called from `add`). A VM
   that stops receives no more samples, so the minute, hour and day it stopped in stay in memory
   indefinitely and an agent restart discards them. In this run the last running minute was
   queryable before the restart and absent after it, although the restart came more than one
   flush after that minute ended (`vm_last_running_minute_kept_after_restart: false`). The hour
   and day tiers are affected the same way, so a VM that ran for most of an hour and then stayed
   stopped loses that hour from the 30-day and 1-year views on the next agent restart; this goes
   beyond the documented "unfinished step lost on restart" limit. **Verified fixed in run 3:**
   Core now finishes elapsed steps at flush and query, and the stop minute was still served after
   the restart (`vm_last_running_minute_kept_after_restart: true`).
2. **UI — duplicate axis labels at tiny rates.** An idle NIC's network axis reads
   `1 B/s, 1 B/s, 1 B/s, 0 B/s, 0 B/s, 0 B/s`: the formatter rounds sub-byte ticks to whole bytes.
   **Verified fixed in run 3:** the same idle axis reads `0.00, 0.25, 0.50, 0.75, 1.00 B/s`.
3. **UI — isolated points are invisible.** At 24h, a single known minute between unknown minutes
   draws no mark (lines need two consecutive points), so short runs look like no data.
   **Fixed in run 3, visually confirmed only at small scale:** at 24h, single known points appear as
   small marks at the right edge of the Memory and Network charts; mark size was not otherwise
   measured.
4. **Observation, not verified as a regression:** the agent unit reported an 831 MB memory peak in
   the first (discarded) run, which included a 790 MB ISO upload through the agent; not
   investigated.

5. **Product bug (BUG-0255) — realtime reports values for a stopped VM.** For a shut-off domain, libvirt
   11.3 `virsh domstats --raw --nowait --cpu-total --balloon <uuid>` still returns `cpu.time`,
   `cpu.user`, `cpu.system` and `balloon.*`. In an earlier run, a never-started VM returned the same
   increasing `cpu.time` as the stopped one, so the counter is not the domain's own. The realtime
   sampler (`liveViews.sample` in `internal/agent/usage_history_live.go`) treats a block with
   `cpu.time` as running, so a stopped VM's realtime charts show small CPU rates (0–1 %) and its
   assigned memory instead of gaps. The stored-history sampler is not affected: it uses
   `--list-running`. Evidence:
   `stopped_vm_domstats` in [results.json](usage-history/results.json). **Verified fixed in the
   final run:** the sampler now requests `--state` and samples only `state.state=1`; the stopped VM's
   realtime answer had no known points although libvirt still returned the stale counters. Regression
   test: `TestLiveSampleTreatsOnlyARunningStateAsRunning` (`internal/agent/usage_history_live_test.go`).
6. **Observation (minor UI, open):** in the realtime drawer the Disk chart, with a second IOPS axis,
   fits fewer time labels than the other realtime charts: one label in the previous run, none in the
   final run's capture.
   **Fixed after the final run, not re-run on a host:** compact realtime charts now keep IOPS on an
   unlabelled scale instead of a second axis, and mocked-browser captures show two time labels on
   every compact chart at 700–1400 px widths.

## Untested limits

- Nested virtualization only; no physical host, NIC or disk counters.
- The guest was idle at the installer menu: no guest-generated disk or network load, so rates were
  small; rate accuracy under load is covered only by unit tests.
- Heartbeat 1 s: first-query latency with the 10 s default heartbeat was not measured.
- Only minutes of runtime: the 30-day and 1-year tiers, in-place slot writes over hours, and
  retention pruning were not exercised on the host.
- Hosted CI, multiple concurrent browser viewers, and a host over the series/VM caps were not run.
- The realtime object cap (L4), the 10-minute UI auto-off, and realtime under guest load were not
  run on the host.

## Outcome

Final run, 2026-09-17: every exercised check passed, including L5 after the finding 5 fix, and
findings 1–3 remain fixed. Still open: L4 (the 8-view realtime cap through a real Controller,
covered only by unit tests because the lab's memory budget allowed no extra guests), a host re-check of
observation 6's later fix, and the untested limits above. Finding 5 is recorded as BUG-0255.
