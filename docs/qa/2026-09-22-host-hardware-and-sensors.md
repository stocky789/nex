# Host hardware and temperature sensors verification

Date: 2026-09-22. Scope: slices 1 and 2 of
[host hardware inventory and temperature sensors](../plans/2026-09-22-host-hardware-and-sensors.md)
— the hwmon temperature series, `GET /api/v1/hosts/{id}/hardware`, and the host **Stats** tab's
Hardware panel, gauge row and Temperature chart.

**Everything below is simulated.** Fixture sysfs trees, stub inventory tools and mocked host
replies only. No physical machine, no lab host, no real `hwmon`, `dmidecode`, `lspci`, `lsblk` or
`drivetemp` output was observed, and no sensor label or threshold in this report or in any fixture
is a real-machine value. The real-hardware walkthrough belongs to the project owner (decision:
project owner, 2026-09-22) and is outstanding; this report is not acceptance of it.

## Build under test

Working tree on 2026-09-22 (uncommitted, alongside unrelated in-flight work from other sessions).
Go toolchain from the repository's usual `/usr/bin/go`; the UI bundled with the repository's
esbuild options into a scratch output directory, so `web/dist` was not written.

## Executed checks

### Core and Controller

- `go vet ./internal/agent/ ./internal/controller/ ./internal/protocol/` — passed, no diagnostics.
- `go test ./internal/agent/ ./internal/controller/ ./internal/protocol/ -count=1` — passed.
  Temperature sampling: keys stable when the kernel renumbers hwmon indexes, keys unique when two
  chips share a name, labels per driver, unreadable and implausible readings recorded as gaps, and
  a 64-core host with more disks and sensors than the series budget keeping all 24 temperature
  series and every fixed series while the per-core series are the ones dropped.
  `readHardware`: every section from a fixture sysfs/procfs tree with stub tools on an isolated
  `PATH`, a missing tool, a failing tool, and a machine with no DMI table or `/proc/cpuinfo`; the
  stats channel still serving `/stats` and now also `/hardware`.
  Relay: no session (401), unknown host (404), stale host (409), a Core without
  `host-hardware-v1` (409), no channel within the wait (503), a reply past the PCI bound (502), a
  reply past the byte bound (502), a transparent retry on a channel the agent had closed, and a
  host that stops answering (504).
- `bash installer/check.sh` — passed, including the package-input assertions covering the added
  `dmidecode` and `pciutils`.

### Web unit checks

- `npx tsc --noEmit` in `web/` — passed, no diagnostics.
- `node src/stats.test.mjs` and the other ten suites — passed. Covers the hardware summary line
  with and without firmware module data, the per-section note for rows / unavailable with a reason
  / unavailable without one / an empty section / a missing section, sensor thresholds keyed by
  series key including a sensor that publishes none, a gauge scale taken from a sensor's `crit`,
  warm and critical band selection, the three-step staleness window, `celsius` formatting, the
  temperature group's ordering and non-stacking, and VM exclusion.

### Browser review — simulated replies

Chromium through Playwright against the bundled UI served from a throwaway local HTTP server, with
every `/api/v1/**` request intercepted and answered from fixtures. No Controller, agent or lab host
was contacted. 16 checks passed and all 16 captures were inspected. The harness and captures were
written to untracked scratch (see [Limits](#limits)).

Exercised: both panels loading and releasing; a host with four sensors; a host with no sensors at
all; a section returned unavailable with the host's own reason; an older Core hiding the panel with
the hardware request count asserted at zero; a stale host (409) and recovery through **Retry**; a
502 keeping the last loaded report on screen; warm and critical gauges; a sensor with no
thresholds; a sensor whose newest points are gaps reading "—"; keyboard operation of the
expandable sections and Tab order; the accessibility tree of the gauge row (`role="meter"` names,
`aria-valuetext`, `aria-valuemax` from the host's own `crit`); 390 px width with a measured
horizontal-overflow assertion; the VM Stats tab having no panel, no gauges and no hardware request;
and the computed colour of the gauge value for the contrast rule.

## Defects found and fixed

All were found by the browser review and fixed in `web/src/stats.tsx` and `web/src/stats.css`; the
harness was rerun and recaptured after each.

1. **Hardware could never be reread.** **Retry** existed only inside the stale and error banners,
   which appear only when the first load failed, so the plan's "stale host: last loaded, marked
   stale, Retry" state was unreachable. **Retry** now sits in the panel heading, disabled while
   loading, like **Refresh** on Usage history.
2. **66 px of horizontal page scroll at 390 px** — the hardware tables cannot shrink below their
   content. The section now scrolls inside its own card instead of pushing the page.
3. **Identifiers wrapped mid-token** — a PCI ID rendered across two lines reads as two values.
   Identifier pills no longer wrap.
4. **Words split inside table cells** ("coretemp" as "coretem p"). Cell wrapping now breaks only
   between words, still breaking a value too long for its column.
5. **A gauge with no current reading was painted in the "normal range" accent**, a claim an unknown
   value cannot make. An unknown gauge is drawn in the track grey.

## Limits

- No hardware verification of any kind, as stated above. The simulated runs prove parsing, the
  empty, degraded and failure states, layout, keyboard and accessibility behaviour only.
- The browser harness, its bundle and its captures are untracked scratch, so this report is the
  only tracked evidence of the browser review; the run is not reproducible from the repository.
  Promoting the harness to `tests/integration/` is outstanding.
- The five UI fixes above are render- and CSS-level and have no unit check; `stats.test.mjs`
  exercises exported helpers only. The browser harness is their only check.
- The per-tool and whole-report hardware timeouts have no automated check; reproducing them needs a
  stub tool that sleeps past the budget.
- The guide's own hardware and temperature steps have not been walked through as written.
- The plan's outage gate (stop the Controller for ten minutes, restart it, replace it) was not
  exercised for the temperature series; the existing usage-history evidence covers the same file
  and sampler but predates these series.
