# Host hardware inventory and temperature sensors

Status: slices 1 and 2 implemented, hardware walkthrough outstanding, 2026-09-22. Simulated
verification only ([QA record](../qa/2026-09-22-host-hardware-and-sensors.md)); the project owner's
real-hardware walkthrough is still to come. Slice 3 is not authorized.

## Goal

Nex Core knows the machine it runs on. Each host discovers its own hardware (system, firmware,
CPU, memory modules, disks, PCI devices) and reads the temperature of every component the kernel
exposes a sensor for. It records temperatures in its own usage history and serves both over the
host API. On a host's **Stats** tab, above **Usage history**, the operator sees:

- a **Hardware** summary: what this server is;
- a row of **gauges**: CPU, memory and one gauge per detected temperature sensor;
- a **Temperature** chart alongside the existing CPU, memory, network, disk and load charts.

This is a distribution capability first. The discovery, sampling, thresholds and history ship in
the Nex Core image and work with no Controller connected; the Controller only relays and draws.

## Architecture invariant

**State owner: core.** Hardware facts are read live from the host on request and stored nowhere.
Temperature history is observed state and joins the host's existing history file.

- Record: temperature series in `<state-dir>/usage/host.rrd` on each host. Hardware has no record.
- Recovery: a fresh Controller sees the same hardware, gauges and temperature history as soon as
  the host reports the capabilities. The Controller keeps no copy, cache, threshold or schedule.
- Replacement test: nothing to lose. Delete the Controller and every value comes back from hosts.

## Core behavior

Design order: Core behavior → host-side state → host API → outage checks → UI.

### Temperatures

The kernel's hwmon class is the single source. The existing 5 s host sampler
(`readHostPoints` in `internal/agent/usage_history_sample.go`) also walks `/sys/class/hwmon/*`
and records each `tempN_input` as a gauge series. No new loop, daemon or dependency; the realtime
(1 s) view gets temperatures through the same function.

- Series: group `temperature`, unit `celsius`, key `temp.<chip>-<device>.<n>` built from the
  hwmon `name` and the device's sysfs basename. hwmon index numbers change across boots and are
  never part of a key.
- Label: the component, derived from the hwmon device link, then `tempN_label`:
  `coretemp`/`k10temp` → "CPU 0 package"; `nvme` → "nvme0 (model)"; `drivetemp` → "sda";
  GPU and NIC drivers → the PCI device name; `acpitz` and others → the chip name.
- CPU: one series per package (`Package id N`, `Tctl`/`Tdie`), not per core. Per-core
  temperatures would exhaust the 128-series cap on large hosts and add little.
- Budget: at most 24 temperature series per host, registered ahead of per-core CPU series so the
  [64-core decision](2026-09-16-usage-history.md#more-than-64-cores-decided-2026-09-17) cannot
  starve them. Extra sensors are dropped with the existing one-time series warning.
- Unknown is never zero: an unreadable, absent or implausible reading (below −40 °C or above
  150 °C, which some chips return for an unconnected input) records nothing and charts as a gap.
- SATA/SAS disk temperatures need the kernel `drivetemp` module. The installer and package ship
  `/etc/modules-load.d/nex-sensors.conf` loading it. NVMe, CPU and GPU sensors load on their own.
  The agent never loads modules (`ProtectKernelModules=yes` stays).
- A host with no sensors (every nested-KVM lab host) reports none. That is a normal state.

### Hardware

One function, `readHardware(ctx, root)`, shaped like `readBlockDisks(ctx, root)` so tests run
against a fixture tree. Read on request with the existing capped-output, deadline-bound exec
helper; nothing is cached or persisted.

| Section | Source |
| --- | --- |
| System, board, chassis, BIOS vendor/version/date, serial | `/sys/class/dmi/id/*` |
| CPU model, sockets, cores, threads | `/proc/cpuinfo`, `/sys/devices/system/cpu/*/topology` |
| Memory modules: slot, size, type, speed, manufacturer, part | `dmidecode -t memory` |
| Disks: model, serial, size, rotational, transport | `lsblk -J` (util-linux, already shipped) |
| PCI devices: GPUs, NICs, storage and RAID controllers | `lspci -mm -nn -D` |
| Sensors: series key, label, `max` and `crit` thresholds | the same hwmon walk |

The sensor list carries each sensor's series key with its hwmon `tempN_max`/`tempN_crit`, so a
gauge gets its scale and warning band from the hardware, not from a number invented in the UI.
A section whose tool fails or is missing is reported as unavailable with a reason; the rest of
the report still returns.

### Packaging

Add `dmidecode` and `pciutils` (with its `pci.ids` data) to `installer/packages.txt`. Both are
separate GPL-2.0-or-later programs the agent executes, not code linked into Nex; record them in
the component manifest and retained source per [LICENSING](../LICENSING.md) before any ISO ships.

## Host API

API impact: **changed contract** (stats) and **new contract** (hardware).

- **Stats, changed (additive):** host `StatsHistory` gains group `temperature` with unit
  `celsius`. No new field, endpoint or capability; an older UI ignores the unknown group.
- **Hardware, new:** `GET /api/v1/hosts/{id}/hardware`, admin-authenticated, relayed over the
  existing outbound stats channel as `GET /hardware`. It is not added to the heartbeat report:
  the data is static, several kilobytes, and the report is strictly decoded, so a report change
  would force a Controller-before-Core upgrade for no benefit.
- Capability `host-hardware-v1`. Without it the Controller answers 409 "upgrade Nex Core" and the
  UI hides the panel; charts and gauges for CPU and memory still work from `usage-history-v1`.
- Errors reuse the stats relay: 404 unknown host; 409 revoked, stale or no capability; 503 no
  channel within 20 s; 504 timeout; 502 reply outside the contract. Read-only, so every retry is
  safe. The reply is bounded (at most 64 memory modules, 128 disks, 256 PCI devices, 24 sensors).
- Serial numbers are returned to authenticated administrators only and are never logged or
  written to diagnostic bundles.
- Update `docs/API.md`, the OpenAPI contract and the generated reference in the same slice.

## Controller UI

Host **Stats** tab only, above **Usage history**, in this order:

1. **Hardware** panel: one summary line (vendor and model, CPU, memory, BIOS version), with an
   expandable table per section. States: loading, unavailable section with reason, older Core
   (hidden), stale host (last loaded, marked stale, Retry).
2. **Gauges**: CPU %, memory %, then each temperature sensor. A small SVG arc with
   `role="meter"` and a text value; the scale ends at the sensor's `crit`, the warning band starts
   at `max`, and a sensor without thresholds shows a plain value with no band. Values are the last
   known point of the 10-minute history the tab already refreshes every 5 s, so gauges add no
   request. A newest point older than three steps shows "—", not a frozen number.
3. **Temperature** chart: a sixth group in `charts()`, `celsius` added to `formatValue` and
   `formatTicks`. Not stackable. No sensors: "No temperature sensors detected on this host."

VM Stats tabs gain no panel or gauge; a VM has no hardware of its own.

### Visual style

Decision: project owner, 2026-09-22 — the Stats tab follows a styling reference the owner
supplied: a soft, light surface with vivid accent colours, not the current muted palette. The
reference image is not committed. A later plain-stats restyle (2026-09-22) dropped the
accent-left-border cards, tinted backgrounds, half-circle gauge arcs and `--accent-*` aliases;
the live tokens below are what `web/src/stats.css` actually defines.

| Token | Value | Use |
| --- | --- | --- |
| (canvas) | `#f7f8fa` | Stats tab background behind the cards (`section.panel.stats`) |
| card fill / `--stats-border` | `#ffffff` / `#dde3ea` | panels, gauges and charts: white cards, 1 px border, 6 px radius |
| `--stats-muted` | `#67778d` | captions, units, row counts |
| `--stats-line` | `#eef1f5` | gauge track and table row rules |
| `--stats-ink` | `#1f2937` | headings and values |
| `--series-1` | `#3273dc` | CPU |
| `--series-2` | `#0e7490` | memory |
| `--series-3` | `#1179b5` | network |
| `--series-4` | `#0f8a5f` | disk |
| `--series-5` | `#9a6700` | load / warm readings |
| `--series-6` | `#7c5cff` | additional series |
| `--series-7` | `#ff3860` | critical / error series |
| `--series-8` | `#5b6570` | muted / inactive series |

- **Cards.** Each Hardware section, gauge and chart is a white card with a quiet border on the
  `#f7f8fa` canvas. No accent left border or tinted card background.
- **Gauges.** A short horizontal fill bar over `--stats-line`, the value in large text, and the
  label above in `--stats-muted`. Warm and Critical are words beside the value in darkened amber
  or red; there is no half-circle arc and no pill badge.
- **Hardware summary.** Section headings are ordinary weight text; expandable cards show a row
  count when collapsed. Tables use a quiet muted header row.
- **Charts.** `--series-1` to `--series-7` are the chart colours, scoped to `.stats`, so the
  existing charts, the temperature chart and the gauges read as one system. `--series-8`
  stays grey.
- **Contrast is not traded away.** Series 2–5 meet the 3:1 non-text minimum against white.
  Every gauge also shows its number and a state word when Warm or Critical. Colour alone never
  carries a state.
- **Scope.** The tokens are defined under `.stats` in `web/src/stats.css` and restyle the Stats
  tabs only (host and VM, so the two do not diverge). Restyling the rest of the Controller UI is
  a separate decision and is not part of this plan.

## Slices

1. **Temperatures.** hwmon walk, series, budget, `drivetemp` module file; temperature chart and
   gauges; guide page update; QA record. *Implemented, hardware walkthrough outstanding.*
2. **Hardware.** `readHardware`, `/hardware` on the stats channel, Controller relay, capability,
   packages and licensing manifest, Hardware panel, API reference, guide.
   *Implemented, hardware walkthrough outstanding.*
3. **Later, not authorized by this plan:** fan, voltage and power readings (same hwmon walk);
   board Super-I/O sensors, which need a probing step at install time; BMC sensors through
   `ipmitool`, after the [fencing plan](2026-09-22-automatic-host-fencing.md) settles its
   packaging; over-temperature alerts.

### What the implementation settled differently

Decisions: project owner delegated; taken as the smallest working change, 2026-09-22.

- **Series key.** `temp.<chip>-<device>.<n>` needs the hwmon `device` link; a chip without one
  (`acpitz` is the common case) keys as `temp.<chip>.<n>`. Two such chips sharing a name would
  then share a key, so repeats are numbered `-2`, `-3` and keep their own series.
- **Sensor budget.** Over 24 sensors the cut is by priority — CPU packages, then NVMe, then
  other devices, then per-drive sensors — and the survivors are re-sorted by key, so the same 24
  survive the budget from one boot to the next rather than whichever the kernel probed first.
  They are still added ahead of the per-core CPU series, as planned.
- **Guide.** The existing [usage history page](../guide/usage-history.md) was extended with the
  hardware, gauge and temperature material instead of adding a second page; the operator reaches
  all of it from one **Stats** tab.
- **Fixtures.** Sysfs and procfs fixture trees are built in `t.TempDir` with stub tools on an
  isolated `PATH`, not committed under `testdata`, so no fictional vendor, serial or sensor value
  is carried in the repository as though it were a capture.
- **Plain stats styling.** The accent-coloured left borders, tinted card backgrounds, half-circle
  gauge arcs, uppercase captions, pill badges and `--accent-*` / `--stats-bg` / `--stats-track`
  tokens were removed in favour of white cards on `#f7f8fa`, horizontal fill bars and the
  `--series-*` table above. Follow `web/src/stats.css`, not the original reference palette.

## Acceptance gates

- Unit checks against fixture sysfs trees: key stability when hwmon indexes are renumbered;
  labels per driver; implausible and unreadable readings become gaps; a 64-core host with 30
  sensors keeps 24 temperature series and every fixed series; `readHardware` with a missing or
  failing tool returns the other sections.
- Contract checks for `/hardware`: authorization, unknown host, no capability, stale host, channel
  timeout, oversized reply refused, retry on a closed channel.
- Outage: stop the Controller for ten minutes, restart it, and the temperature chart shows no gap.
  Replace the Controller and the hardware panel, gauges and history match the old one.
- **Hardware verification belongs to the project owner.** Decision: project owner, 2026-09-22 —
  the owner runs the real-hardware walkthrough on a spare bare-metal server after slices 1 and 2
  are both implemented. Implementation work does not wait for it, does not stand up a hardware
  lab, and never reports sensor labels, thresholds, `drivetemp` or `dmidecode` output as verified.
  Simulated hosts prove only the empty and failure states. The slices are recorded as
  "implemented, hardware walkthrough outstanding" until the owner's results are written up under
  `docs/qa/` with sanitized serials; the guide walkthrough evidence comes from that run.
- Browser review against mocked host replies: loading, empty, stale, older-Core and error states,
  warm and critical gauges, keyboard and screen-reader reading of the gauges, narrow-width layout,
  and the [visual style](#visual-style) including its contrast rules.
- Documentation, API reference, change record (`state.owner: core`) and privacy gates pass.

## Open decision for the project owner

- Slice 3 board sensors: detecting Super-I/O chips means probing I/O ports at install time, which
  the upstream tooling itself warns can misbehave on rare hardware. Decide before slice 3 whether
  Nex probes automatically, offers it as an installer option, or leaves it to the operator.
