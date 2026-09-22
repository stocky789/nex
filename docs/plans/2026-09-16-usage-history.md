# Usage history and Stats tabs

Status: published in 0.3.0. The following 2026-09-17 working-tree status was superseded by
commit `16478a9` and tag `v0.3.0`. Unit, contract and mocked-browser checks pass, two
independent reviews' findings are fixed, and the final disposable-host run
([QA record](../qa/2026-09-16-usage-history.md), simulated nested-KVM host, 1 s heartbeat)
passed every exercised check for stored history, realtime, Controller replacement and the Stats
and Realtime UI. Open: see [Remaining work](#remaining-work).

## Goal

Operators see how a VM or host has used CPU, memory, network and disk over time, from the
last 10 minutes back to a year, on a **Stats** tab for each VM and host. Each tab shows four
charts, a range picker and a **Stacked** toggle. Hovering one chart moves the crosshair on all
four and shows values; dragging zooms in; clicking a legend entry hides or shows that series;
short ranges refresh while the tab is open. This replaces the single current-value usage panel
as the main way to read usage; the **General** tab keeps its current-value summary.

## Owner decisions, 2026-09-16

- Retention: four tiers kept on the host, at a fixed size per VM or host:
  5 s × 10 min (120 points), 1 min × 24 h (1440), 1 h × 30 days (720), 1 day × 1 year (365).
- Charts use uPlot (MIT, a single small dependency) rather than hand-built SVG.
- First slice includes synced hover and drag-zoom, legend toggles, and live refresh.
  CSV export is not in scope.
- VMs and hosts are delivered together.

## Architecture invariant

History is observed state. **State owner: core.** The host that samples a VM or itself
writes the history to its own agent state directory, and a Controller only relays a query.
The replacement test holds: delete the Controller and install a new one, and every chart
comes back from the hosts with no gap caused by the Controller. The Controller keeps no copy,
cache or schedule.

- Record: `<state-dir>/usage/host.rrd` and `<state-dir>/usage/vm-<uuid>.rrd` on each host.
- Recovery: a fresh Controller queries the host once that host reports the stats capability.
- A VM's history lives on the host that ran it. Nex has no live migration yet. When migration
  lands, it must carry the VM's history file with it; this is a recorded requirement of that
  phase, not something this slice builds.
- Deleting a VM through Nex deletes its history file. Any other history file untouched for more
  than a year holds no data in any tier and is removed.

## Core behavior

### Sampling

One sampler loop in the agent, independent of the health probes, ticks every 5 s:

- VMs: a single `virsh domstats --raw --nowait --cpu-total --vcpu --balloon --block --interface`
  call covering every running domain (no per-VM process). `--nowait` stays mandatory for the
  reason recorded against BUG-0213.
- Host: `/proc/stat` (total and per core), `/proc/meminfo`, `/sys/class/net/*/statistics` for
  physical NICs and bridges Nex manages, `/proc/diskstats` for whole block devices.
- The existing `usage` probe stops running its own virsh calls and reports the sampler's latest
  values, so there is one sampling path, not two.

Counters become rates on the host (CPU percent, bytes/s, operations/s), as today. A counter that
goes backwards, a missing previous sample or an absent member records **unknown**, never zero;
a stopped VM records nothing, which charts as a gap.

Series per VM: CPU percent total and per vCPU; memory assigned, host resident, and guest used
when the balloon reports it; per disk read/write bytes/s and IOPS; per interface rx/tx bytes/s.
Series per host: CPU percent total and per core; memory used/total; per NIC rx/tx bytes/s; per
block device read/write bytes/s and IOPS; load average.

Guest-used memory needs the virtio balloon statistics period. New and edited VM definitions set
`<memballoon><stats period='5'/>`; the sampler never changes a running domain to enable it.
An existing VM gets the period the next time its definition is saved. Until the guest's balloon
driver reports, the guest-used series is unknown (a gap).

### Storage format

Plain Go, no new dependency. One file per object: a small JSON header (series names, units,
tier definitions, last update) followed by fixed-length float32 rings, NaN for unknown. Each tier
stores the average of the finer tier's samples, ignoring unknown values; a slot is unknown only
when all its inputs were.

- The 5 s tier is memory-only. An agent restart loses at most 10 minutes of 5 s detail; the
  1-minute tier still covers that period once the next minute completes.
- Completed coarse slots are written in place at fixed offsets; the file is rewritten atomically
  only when it is created or its series change. This keeps writes to bytes per minute per VM
  instead of whole files (review finding: whole-file rewrites wore boot media). A partial hour or
  day being averaged is lost on agent restart.
- A series appearing (hot-added disk or vCPU) rewrites the file with the new series filled
  unknown. A series that disappears is kept until its data ages out of the 1-year tier.
- Size: about 10.6 KB per series; a typical VM (4 vCPU, 2 disks, 2 NICs) is under 300 KB.
  Files and series counts are capped (256 VMs, 128 series per file); overflow is logged, never a
  crash. It is not reported as a failed check: the monitor has no degraded state, and failing
  the `usage` check would hide every VM's usage and mark the host unhealthy.
- ponytail: averages only. A 1-day point hides short spikes; add a max consolidation if
  operators need peak detection.

## Host API

### Query channel

The Controller cannot connect to a host; the agent dials out. Stats use the pattern the console
already uses, kept open for reuse:

1. The browser asks the Controller for a VM's or host's stats.
2. If no stats channel to that host is open, the Controller queues a `stats_channel` request
   delivered in the next report response (at most one heartbeat, 10 s by default).
3. The agent dials `POST /api/v1/agent/stats/{token}` over its mTLS identity and upgrades the
   connection. The Controller then sends HTTP/1.1 requests over that connection and the agent
   answers them with a local handler.
4. The channel closes after 5 minutes without a query, on agent or Controller restart, or on
   any protocol error. The next query opens a new one.

So the first chart load for a host can take up to one heartbeat; later loads and live
refreshes are immediate while the tab is used. The UI says "Connecting to host…" during the
first load rather than showing an empty chart.

### Query contract

`GET /api/v1/vms/{id}/stats?range=live|10m|24h|30d|1y` and
`GET /api/v1/hosts/{id}/stats?range=live|10m|24h|30d|1y`, admin-authenticated (`live` is described
under [Realtime drawer](#realtime-drawer-owner-request-2026-09-16)).

```json
{
  "object": "vm", "id": "<uuid>", "host_id": "<host>",
  "range": "24h", "step_seconds": 60, "start": "2026-09-15T10:00:00Z",
  "series": [
    { "key": "cpu.vcpu.0", "group": "cpu", "label": "vCPU 0", "unit": "percent", "values": [3.5, null] }
  ]
}
```

`values` has one entry per step from `start`; `null` is unknown or not running. Groups are
`cpu`, `memory`, `network`, `disk` (plus `load` for hosts).

Errors: 404 unknown object; 409 host revoked, stale, or without the `usage-history-v1`
capability ("upgrade Nex Core"); 503 host did not open a channel within 20 s; 504 query timed out
(10 s); 502 host error or reply outside the contract; 429 (realtime only) the host already samples
8 live objects. Queries are read-only, so retries are always
safe, and the Controller retries once on a new channel when a reused channel turns out closed. A VM whose host changed since its
history was recorded returns only what the current host holds.

Compatibility: Controller must be upgraded before Core (strict report decoding). An older Core
never advertises the capability, and the Controller shows "Upgrade Nex Core to see usage
history" instead of charts.

## Controller UI

- New **Stats** tab on VM and host pages, between General and Console.
- Series keys: `cpu.total`, `cpu.vcpu.N` / `cpu.core.N`; `memory.assigned|host|guest_used` (VM),
  `memory.used|total` (host); `disk.<dev>.{read_bytes,write_bytes,read_iops,write_iops}`;
  `net.<index>.{rx,tx}` for VMs (definition order, because `vnetN` changes every start) and
  `net.<nic>.{rx,tx}` for host physical NICs, bonds and bridges; `load.{1,5,15}`. Keys ending
  `.total` are never stacked.
- Range picker: Last 10 minutes, Last 24 hours, Last 30 days, Last year.
- **Stacked** toggle: stacks per-vCPU/per-core CPU, per-interface network and per-disk series
  within each chart. Off shows overlapping lines, as today's reference layout does.
- Synced crosshair across the charts, with a value tooltip on the chart under the pointer; drag to
  zoom, double-click to reset.
- Legend: a row of coloured chips under each chart (no values); a chip toggles its series by
  click, Enter or Space, and hidden series stay hidden across refreshes. Stacked sums leave hidden
  series out.
- Live refresh: every 5 s at 10 minutes and every 60 s at 24 hours, only while the tab is
  visible. Longer ranges refresh on demand.
- Gaps are drawn as gaps. Units scale automatically (%, B/s → GiB/s, IOPS); a chart mixing byte rates and IOPS gets a
  second axis on full-size charts (compact realtime charts keep IOPS on an unlabelled scale so the
  time axis stays readable). Stacked CPU weights each vCPU/core by 1/N so the stack top is total
  CPU; hosts keep at most 64 per-core series.
- States: connecting, host stale (last history still shown with a stale banner if already
  loaded), no data yet, capability missing, error with retry. Keyboard focus reaches the range
  picker and toggle. The app has no dark theme yet; series colors are CSS variables on `.stats`.

## Realtime drawer, owner request 2026-09-16

Updated placement, owner request 2026-09-17: VM **Realtime** is beneath the console display,
in both the Console tab and its separate window, so operators can watch both together.
Host **Realtime** remains at the top of Stats; VM Stats contains historical charts only.
The existing component and host-owned sampling lease are reused. **API impact: none** — the existing
`GET /api/v1/vms/{id}/stats?range=live` contract is unchanged; no new durable state or sampling behavior.
**Guide impact: updated** in the usage-history and console guides. See the
[placement checks](../changes/2026-09-17-console-realtime-stats.json). Decided design:

- `range=live` on the existing stats endpoints (step 1 s, 120 points). A live query starts or
  renews a 2-minute **host-owned lease** for 1-second sampling of that one VM or host into a
  memory-only buffer. Nothing is written to disk or merged into stored history; the 5 s sampler
  remains the only source of stored history. When queries stop, the lease expires on the host,
  so a closed browser or a lost Controller needs no clean-up request. **State owner: none** (the
  buffer is transient observed state).
- At most 8 live objects per host; beyond that the host answers 429 and the Controller relays it.
- Per-second VM samples use single-domain `virsh domstats --nowait --state` (BUG-0213 rule) with a
  per-tick deadline, so a slow answer skips a second instead of piling up. Only `state.state=1`
  (running) is sampled: libvirt still returns stale CPU and balloon counters for a shut-off domain
  (BUG-0255).
- UI: drawer collapsed and unchecked by default and never remembered; polls once a second
  without overlap; pauses while the page is hidden; closing the drawer or leaving the tab turns it
  off; turns itself off 10 minutes after it was turned on (Retry does not extend that) with a
  Resume button; shows 429 and other errors inline; only state changes are announced to screen
  readers.

## Delivery order and acceptance

1. **Done.** Core sampler and ring store with unit tests: rate arithmetic, counter reset, unknown vs
   zero, tier consolidation, series add, in-place slot writes, reload, file caps.
2. **Done.** Protocol capability, report `stats_channels` request, agent upgrade endpoint and query
   handler; Controller relay endpoints. Contract tests: auth, invalid range, unknown object, stale
   host, missing capability, channel never dialled, query timeout, dead reused channel, oversized
   reply, 429 relay.
3. **Done.** Stats tabs and Realtime drawer with uPlot; UI typecheck, tests and build; mocked-browser
   review of loading, empty, error, stale, stacked, hover, zoom, legend, keyboard, narrow and
   realtime states.
4. **Done on a simulated host.** Disposable-host verification: VM running, stopped (gap), agent
   restart, Controller replacement showing the same history, VM deletion removing its file, host
   stats, realtime for VM, host and stopped VM, lease expiry, and the UI in a real browser.
5. **Done.** `docs/API.md`, OpenAPI contract and generated reference, user guide page
   `docs/guide/usage-history.md`, change record, uPlot licence notice, bug ledger (BUG-0230 to
   BUG-0233, BUG-0255), roadmap board.

API impact: **new contract** — stats query endpoints (including `range=live`), agent stats channel,
report response `stats_channels`, `usage-history-v1` capability; existing `vms[].usage` contract
unchanged in shape.
Guide impact: **updated** — new page for the Stats tabs and Realtime drawer.

## Review fixes after the disposable-host run, 2026-09-16

Unit tests cover each fix. The final host run (2026-09-17) used a build that includes them, but did
not create their specific conditions (a backwards clock step, a VM idle across a day boundary, more
than 256 VMs).

- A stored tier (24h, 30d, 1y) ignores samples behind its current step instead of resetting the
  ring, so a backwards clock step no longer wipes stored history. Only the memory-only 5 s tier
  restarts.
- An idle object stays in memory until no tier holds samples for an unfinished step, so a stopped
  VM keeps its last day in `1y`. The 256-VM cap counts only objects sampled in the last hour, so
  stopped VMs held for that reason do not block new ones.
- The Controller relay requires every series to have exactly the range's point count and names
  `live` in its invalid-range message.
- Stacked charts leave hidden series out of the running sums and pair each band with the nearest
  visible series below it.
- The layout comment now states that in-place slot writes are not fsynced and a crash between slot
  and index writes can briefly expose values one ring-length old until the next sample.

## Clock corrections, decided 2026-09-17

A sample more than one step older than a stored tier's newest step is ignored rather than
resetting that tier, so a clock that ran ahead and is corrected backwards never wipes stored
history; the tier resumes when real time passes its newest step (BUG-0231). To make a wrong boot
clock rare, the agent unit is ordered after `time-sync.target`. This is ordering only: it adds no
dependency, so an agent on a host without time synchronisation or network still starts.
Decision: project owner delegated the choice; chosen as the no-data-loss option.

## More than 64 cores, decided 2026-09-17

Core keeps at most 64 per-core CPU series per host so the fixed series (total CPU, memory, network,
disk, load) always fit the 128-series cap. On a larger host the stacked CPU chart covers the first
64 cores and does not reach total CPU use; `cpu.total` is still recorded and charted. The limit is
documented in the guide. Decision: project owner delegated the choice; kept as the smallest change.

Amended 2026-09-22: host temperature series share the same 128-series cap. Up to 24 of them are
registered ahead of the per-core CPU series, so a large host drops cores rather than sensors. See
the [hardware and sensors plan](2026-09-22-host-hardware-and-sensors.md).

## Remaining work

- Walk the [guide](../guide/usage-history.md) through as written on a Nex Core host with the default
  10 s heartbeat (first-query latency was measured only with a 1 s heartbeat).
- The 8-view realtime cap (429) through a real Controller: unit tests only; a host run needs nine
  objects the Controller knows.
- 30-day and 1-year tiers, one-year retention pruning and in-place writes over hours: unit tests
  only.
- Rates under guest load, physical hardware, hosted CI runs, and several concurrent viewers.
- Changes made after the final host run, covered by unit, packaging or mocked-browser checks only:
  `After=time-sync.target` ordering in `packaging/systemd/nex-core-agent.service`, and the compact
  realtime Disk chart time labels.
- Cap overflow (256 VMs, 128 series) is logged only; nothing shows it to operators.
- Live migration, when built, must carry the VM's history file.
- The app has no dark theme, so the charts have no dark variant.
