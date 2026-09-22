# Read VM and host usage history, hardware and temperatures

## Status

Development build. Each VM and host page in Nex Controller has a **Stats** tab that charts CPU,
memory, network and disk use over time, and load average for hosts. History is recorded and kept by
the Nex Core host itself, up to one year back, so replacing or restarting Nex Controller does not
lose it. The **General** tab still shows the current usage summary.

A host's **Stats** tab also shows what the machine is and how hot it is running: a **Hardware**
panel above the charts, a row of **gauges** for CPU, memory and each temperature sensor, and a
**Temperature** chart beside the others. The host reads its hardware fresh each time the tab is
opened and stores none of it; temperatures are recorded in the same history as everything else.
VM Stats tabs have no Hardware panel or gauges: a VM has no hardware of its own.

Known limits:

- Temperatures come from the sensors the Linux kernel exposes. A virtual machine acting as a host
  usually has none, which is normal and not a fault.
- SATA and SAS disk temperatures need the kernel `drivetemp` module, which Nex Core loads. NVMe,
  CPU and GPU sensors load themselves. Board and fan sensors are not read yet.
- CPUs report one temperature per package rather than per core, and at most 24 sensors are kept
  per host. Over that limit the CPU package and NVMe temperatures are kept ahead of individual
  disk temperatures. A sensor that stops answering never holds up the rest of the
  sampling: its last reading stands in for up to two samples, and after that its chart shows a
  gap until the sensor answers again.
- The warm and critical bands come from the chip's own thresholds. A sensor that publishes none
  shows a plain number with no band; the number is not judged against an invented limit.
- Gauges read the **Last 10 minutes** range only, so they are hidden on the longer ranges rather
  than presenting an hourly or daily average as the current reading.
- Memory module details need the host's firmware tables; some machines, and most virtual ones,
  report a total but no per-slot table.
- Detail gets coarser with age: 5-second points for the last 10 minutes, 1-minute points for 24
  hours, hourly points for 30 days and daily points for a year. Older points are averages, so a
  short spike is flattened.
- The 5-second detail is kept in memory only; restarting the host agent clears the last 10
  minutes of fine detail.
- Guest-used memory appears only for VMs whose guest runs the virtio balloon driver, and only after
  the VM definition has been saved on this Nex version.
- A VM's history stays on the host that recorded it.
- Realtime (1-second) stats are kept only in the host's memory for the last two minutes and are not
  added to the history.
- If the host clock was ahead and is then corrected (for example a hardware clock holding local
  time, fixed by time sync after boot), the 24-hour, 30-day and 1-year charts store nothing for as
  long as the clock was ahead, then resume by themselves. Stored history is kept, not cleared, when
  the clock moves back. Use time sync on hosts. The 10-minute chart continues immediately.
- A host records history for at most 256 VMs sampled in the last hour; VMs that stopped earlier no
  longer take those places.
- With **Stacked** on, a host's CPU chart covers at most its first 64 cores, so on a larger host
  the top of the stack leaves out the remaining cores.

## Prerequisites

- Administrator access to [Nex Controller](manage-controller.md).
- The VM's host, or the host itself, freshly connected and running a Nex Core version that
  supports usage history. An older host shows **Upgrade Nex Core to see usage history** instead
  of charts.
- History exists only for time the host was running this Nex Core version; a VM records history
  only while it runs.
- For the **Hardware** panel, a host running a Nex Core version that reports its hardware. An
  older host simply has no panel.

## Steps

1. Select a VM or host in **Resources** and open its **Stats** tab.
2. Wait for the charts. The first load for a host can take up to 30 seconds while
   **Connecting to host…** is shown; later loads are immediate.
3. Choose a period in **Range**: **Last 10 minutes**, **Last 24 hours**, **Last 30 days** or
   **Last year**. The two shorter ranges refresh on their own while the tab is visible; use
   **Refresh** for the longer ones.
4. Turn on **Stacked** to stack per-vCPU (or per-core) CPU, network and disk series. Each vCPU
   or core keeps its own 0–100%, so the stacked CPU axis runs to 100% per vCPU: four fully busy
   vCPUs fill a 400% chart. Memory is never stacked.
5. Hover over a chart to read its values at that time in a small tooltip; the other charts show
   the same moment with a crosshair.
6. Drag across a chart to zoom all charts to that period; double-click to zoom back out.
7. Select a series name below a chart to hide or show that series (a hidden series is dimmed and
   struck through). Hidden series stay hidden while you refresh.
8. On a host, read the **Hardware** panel above the charts. A **System disk mirror** block at
   the top of the panel shows whether the system disk is mirrored and offers **Replace disk…**
   when a member has failed ([Replace a failed system disk](replace-a-failed-system-disk.md)).
   Below that, a summary line names the server, its CPU, installed memory and BIOS version.
   Select a section — **System**, **CPU**, **Memory**, **Disks**, **PCI devices** or
   **Temperature sensors** — to expand its table; collapsed sections that have rows show a
   count. Select **Retry** in the panel if the host could not be reached.
9. On a host with the **Last 10 minutes** range selected, read the gauges above the charts: CPU
   and memory as a percentage, then one gauge per temperature sensor showing its reading in °C.
   A sensor at or above its warm threshold is marked **Warm**, and at or above its critical
   threshold **Critical**. A dash means no recent reading.
10. Read the **Temperature** chart with the others to see how a sensor moved over the range.
11. For a VM, open its **Console** tab (or its separate console window), expand **Realtime**
   beneath the display and tick **Show realtime stats (1-second samples)**. The console stays
   visible above the charts; **Stacked** in the drawer controls the live chart stacking. For a
   host, **Realtime** remains at the top of its **Stats** tab. Compact charts of the last two
   minutes update every second, with a **Live** marker and how long ago they updated. Untick
   the box, close the drawer, or leave the console or host Stats tab to stop. Realtime pauses
   while the browser page is hidden and turns itself off after 10 minutes; select **Resume**
   to continue.

## Expected result

Each chart shows lines for the chosen range, with the time axis ending at the present. Gaps in a
line mean no sample: the VM was stopped, the host was off, or the host could not read that value.
A gap is not zero use. CPU is a percentage of the VM's allocated vCPUs, or of the host's cores;
network and disk are throughput per second, and disk also shows operations per second.

## Troubleshooting

- **Upgrade Nex Core to see usage history** — the host runs an older Nex Core. Update the host
  ([software updates](software-updates.md)); history starts from the upgrade. Nex removes any
  previously loaded chart because that Core cannot serve it.
- **The host connection is stale** — the host has not reported recently. Nex does not assume the
  VM stopped. If a chart was already loaded, it stays visible but may be out of date; reconnect
  Nex Core and select **Retry**. Without a chart, reconnect Nex Core before retrying.
- **"the host did not open a stats connection in time; check the host connection and retry"**, **"the
  host did not answer the usage history query in time; retry"** or **"the host stats connection
  failed; retry"** — select **Retry**. If this repeats, check the host's connection in **Resources**
  and its agent service.
- **"the host has no usage history for this VM"** or **No history recorded yet** — the VM has not
  run, or the host has not yet completed its first samples since the upgrade. Wait a minute and
  refresh.
- **Gaps in the 24-hour, 30-day and 1-year charts after a clock correction** — see the clock limit
  under Status. The host agent log reports that stored history is ahead of the clock.
- **Guest-used memory is missing** — edit and save the VM once, and install the virtio balloon
  driver in the guest (included in the Linux kernel; part of the virtio driver disc for Windows).
- **"too many realtime views on this host; close one and retry"** — at most 8 VMs or hosts on one host can show realtime
  stats at once, counted across all administrators. Close realtime on another page; the host stops sampling a view about two minutes
  after its last viewer stops.
- **Collecting realtime samples…** stays for a second or two after ticking the box; a VM that is
  stopped shows no realtime samples.
- If a refresh fails after charts loaded, the last charts stay visible with *Could not refresh;
  showing the last loaded history.* followed by the error.
- **A hidden series in Stacked mode** is removed from the stack: the visible series stack directly
  on each other.
- **No Hardware panel on a host** — that host runs an older Nex Core that cannot report its
  hardware. Update the host ([software updates](software-updates.md)); the charts and the CPU and
  memory gauges still work.
- **A section reads "dmidecode failed" or "lsblk failed"** — only that section is missing; the rest
  of the report is what the host just read. Check the tool on the host.
- **"the host is not freshly connected; its hardware cannot be read now"** — the host has not
  reported recently. Reconnect Nex Core, then select **Retry**. A report already on screen stays,
  marked as no longer current.
- **No temperature gauges or chart on a host** — the host exposes no sensors. Virtual hosts
  normally do not. On a physical host, missing disk temperatures usually mean the `drivetemp`
  module is not loaded.
- **The gauges disappear when you change Range** — they are deliberately shown only for **Last 10
  minutes**, because longer ranges store averages rather than current readings.

## Verification

The console placement is checked separately in the [console realtime assessment](../changes/2026-09-17-console-realtime-stats.json), using a simulated API and guest display; this is not new host-sampling evidence.

2026-09-16: the Stats tab and Realtime drawer were exercised in a real browser against a mocked
Controller API (loading, empty, error, stale, stacked, hover, zoom, legend chips, keyboard, narrow
layouts, realtime polling, pause while hidden, 10-minute auto-off, drawer close and 429). Go tests
cover the host sampler, history files, realtime sampling, query channel and Controller relay.

2026-09-17: a [disposable-host run](../qa/2026-09-16-usage-history.md) on a simulated nested-KVM
host charted a running VM and the host at the 10-minute and 24-hour ranges, kept history across a
host agent restart and a Controller replacement, showed a stopped VM as a gap in history and in
realtime, removed a deleted VM's history, and exercised the Realtime drawer, tooltip and legend
chips in a browser. Realtime sampling stopped on the host after its two-minute lease.

Not verified on a host: this page's steps walked through as written, the default 10-second report
interval (the run used 1 second), the 30-day and 1-year ranges, the 8-view realtime limit, rates
under guest load, and physical hardware. The compact realtime Disk chart labels and the agent's
start ordering after clock sync changed after that run and have only automated checks.

2026-09-17: automated web checks distinguish the older-Core and stale-host states. Their rendered
stale-to-success and capability transitions were not browser- or component-tested.

2026-09-22: the Hardware panel, gauges and Temperature chart were checked against
[simulated hosts only](../qa/2026-09-22-host-hardware-and-sensors.md) — Go unit tests against
fixture sysfs trees and stub tools, Controller relay tests for the refusals above, web checks of
the summary, per-section states, sensor thresholds and gauge bands, and a browser review against
mocked host replies covering loading, a host with no sensors, an unavailable section, an older
Core, stale and failed reads with **Retry**, warm and critical gauges, keyboard use of the
sections, the gauges' screen-reader reading and a narrow layout. **No part of the hardware and
temperature steps above has been walked through on a real machine**, and no sensor label,
threshold, `drivetemp`, `dmidecode` or `lspci` output here was observed on one; that walkthrough
belongs to the project owner (decision: project owner, 2026-09-22) and its results will be written
up under `docs/qa/`.
