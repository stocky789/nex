# Nex Controller console design guide

This is the visual and interaction contract for the Nex Controller web UI. Read it before changing
anything under `web/src/`. Open [reference.html](reference.html) in a browser next to it: the
reference is the approved look, and this page names the rules behind it so new screens match
without copying markup.

Decision: project owner, 2026-09-23 — this console layout and styling is the default for all
Controller UI work from now on. The delivery plan is
[2026-09-23-controller-console-redesign.md](../../plans/2026-09-23-controller-console-redesign.md).

The [brand guide](../../brand/README.md) remains the colour authority; the tokens below are drawn
from it. The Nex Core host-local page (`web/src/local.tsx`) and the installer are outside this guide.

## The layout in one sentence

A dark labelled **icon rail** on the left picks the section, a slim **top bar** carries scope
(site / pool), global search and live task activity, each section is a **dense filterable table**
with an optional **filter sidebar**, and selecting a row opens a **slide-over panel** on the right
so the list stays in view.

```
┌────┬──────────────────────────────────────────────────────────────────────────────┐
│ n  │ ● Site / Pool ▾        [ Jump to a VM, host, IP or setting   / ]   ● 1 task ◐ ☺ │
├────┼──────────────┬────────────────────────────────────────────┬──────────────────┤
│ ▣  │ search       │ All 15 · Running 11 · Needs attention 2    │ db-primary [Run] │
│VMs │ Group by ─○  │ ☐ Name     State    Pool  Host  IP  …      │ Console Start …  │
│ ▤  │ Status       │ ☐ ● web-01 Running  …                      │ Overview Perf …  │
│Host│ ☐ Running 11 │ ☐ ● db-pri Running  …   (selected row)     │ ┌──────────────┐ │
│ …  │ Pool …       │                                            │ │ console      │ │
│    │ Host …       │                                            │ └──────────────┘ │
│ ⚙  │ Clear filters│ 1–15 of 15                     [Create VM] │ properties …     │
└────┴──────────────┴────────────────────────────────────────────┴──────────────────┘
 rail  filter sidebar            table                              slide-over panel
```

## Tokens

Every colour, radius, shadow and font comes from `web/src/tokens.css`. No literal colour appears in
any other stylesheet; `web/src/tokens.test.mjs` enforces that and checks contrast (≥ 4.5:1 for every
text/background pair it lists). The theme follows the operator's system setting, and the top-bar
toggle overrides it per browser.

| Token | Light | Dark | Use |
|---|---|---|---|
| `--canvas` | `#F6F7FC` | `#111820` | page ground |
| `--panel` | `#FFFFFF` | `#18222C` | tiles, sections, panel cards, inputs |
| `--sunken` | `#EDF0F7` | `#1F2B36` | search fields, hover rows, meters' track, pills |
| `--line` / `--line-soft` | `#C6CEE4` / `#E1E5F0` | `#2C3B48` / `#22303C` | control borders / row dividers, tile borders |
| `--text` / `--muted` | `#17233B` / `#5B6478` | `#E5E8F3` / `#9AA6B8` | body / secondary text |
| `--accent` / `--accent-hover` | `#2456D9` / `#3478FF` | `#5B93FF` / `#7DAAFF` | links, primary buttons, active tab underline |
| `--on-accent` | `#FFFFFF` | `#0B1220` | text on accent |
| `--select` | `#E4ECFF` | `#1C3350` | selected table row |
| `--rail` / `--on-rail` / `--rail-hi` | `#17233B` / `#AAB5C8` / `#FFFFFF` | `#0B1419` / `#7F8C9E` / `#E5E8F3` | the rail and logo cell; the rail is dark in both themes |
| `--spark` | `#7A9A12` | `#B5D43B` | "live" only: the running-task pulse. Never a status, never decoration |
| `--ok`, `--warn`, `--bad`, `--info`, `--neutral` (+ `-bg`) | see `tokens.css` | see `tokens.css` | status only, always with text |
| `--s1`…`--s4` | `#2456D9 #7C5CC4 #0E7490 #7A9A12` | `#5B93FF #B79CF2 #5EC4D6 #B5D43B` | chart series: CPU, memory, storage I/O, network |
| `--radius-control` / `--radius-panel` | `2px` / `3px` | same | controls, badges, chips / tiles, sections, panel cards |
| `--shadow-pop` | one soft shadow | darker | slide-over panel, menus, dialogs only |
| `--font` / `--font-mono` | system UI stack / system mono stack | same | no web fonts |

Brand lime (`#B5D43B`) is a small accent by brand rule; here it marks only live activity.

## Type and density

- 13 px base, 12 px table headers and meta, 11 px badges, tile labels and axis text; 14–16 px panel
  titles; KPI values 22 px. Weight 600 for headings, row leads and KPI values; 400 elsewhere.
- `font-variant-numeric: tabular-nums` on every number column, KPI and meter label.
- Monospace for IP addresses, MACs, paths and identifiers, nowhere else.
- Uppercase with 0.06 em letter-spacing only for KPI tile labels.
- Sentence case for every label and button ("Create VM", "Shut down", not "Create Vm" or "SHUT DOWN").
- Table rows ≈ 34 px, rail items 52 px with 10 px labels, top bar 48 px.

## Rules that keep it looking designed

These come straight from the owner's review. Treat a violation as a defect.

1. **No coloured side stripes.** Never put a coloured left (or right) border, bar or rail on a chip,
   list item, card, table row, alert or selected nav item. Show state with a status dot, an icon, a
   text badge, a fill change or a full thin border.
2. **Tight corners.** `2px` for controls, badges, chips and pills; `3px` for tiles, sections and
   cards. Only status dots, the pulse and avatars are round. No pills with `999px` radius.
3. **No gradients** except the diagonal hatch that marks a stale host in the placement map.
4. **One shadow**, `--shadow-pop`, and only on things that float: the slide-over, menus, dialogs.
   Tiles and sections use a `--line-soft` border instead.
5. **No emoji, no decorative numbering, no centred page layouts.** Icons are the outline set in
   `web/src/icons.tsx` (24 px grid, 1.6 stroke, round caps).
6. **Colour never carries meaning alone.** Every status has text (`Running`, `Stale`, `Unknown`,
   `Failed`), and status badges keep the square dot-and-text form (decision: project owner,
   2026-09-23).
7. **Honest state.** A VM on a stale host is `Unknown`, never `Stopped`. Disabled controls stay
   visible and say why (tooltip or the panel's warning block). Destructive actions keep their
   explicit-target confirmation dialogs.
8. **Not-yet-available features look finished and say so.** Elements whose Nex Core phase has not
   landed render with the real layout, disabled controls and one line naming what is missing, for
   example "Available with snapshots (Phase 5)". Never fake numbers for them.

## Components

Each item lists where the reference shows it. Build new screens from these before inventing more.

- **Rail** (every screen). Logo cell (the dark Nex Controller symbol,
  `nex-controller-symbol-dark.svg`, 32 px) above it. Items: icon over a 10 px label; hover lightens
  the fill; the current section gets a fill only, with no bar.
  A small amber dot on Events means unseen warnings. Order: Overview, VMs, Containers, Hosts,
  Storage, Networks, Backups, then pinned to the bottom: Tasks, Events, Settings. **Containers is a
  sub-item of VMs** (container services run in Nex-managed VMs): it sits directly under VMs,
  centred like every rail item, and is marked as a sub-item only by being smaller (16 px icon,
  9 px label, 38 px tall), never by an indent, a connector line or a stripe. Global search also
  finds rail pages by name.
- **Top bar**. Left: site status dot + site name, `/`, pool name + host-count badge + chevron (the
  scope switcher menu, which also holds Create site and Add host). Centre: global search (max
  460 px, `/` focuses it) that jumps to VMs, hosts, IPs and settings sections. Right: the activity
  pill (pulse + "N tasks running", opens a short task list; **Open Tasks** reaches the full page), theme toggle, account menu (username, Sign out).
- **Filter sidebar** (VMs, 250 px). Search, "Group by host" switch, facet groups (checkbox, value,
  count in muted parentheses) with collapse chevrons, then Clear filters and Customize columns at
  the bottom. Collapses behind a Filters button under 900 px.
- **View tabs** above a table: text with a count pill; the warning pill turns amber.
- **Bulk bar**: appears when rows are checked, `--select` fill, "N selected", action buttons, Clear.
- **Table**: sticky 12 px header, `--line-soft` row dividers, hover `--sunken`, selected `--select`.
  First column carries the status dot and name. Links (host, parent) in accent. Utilisation cells
  use the 60 px mini bar plus the percentage; the bar turns `--warn` above 80 %. Row actions (`.row-actions`) stack as one column of equal-width small buttons in the last
  cell, danger included, with no extra offset, so every row's buttons line up.
- **Slide-over panel** (420 px default, wider when a tab needs it, full width on phones). Drag the
  left edge to resize; the chosen width is kept in this browser (`nex.slideover.width`). Double-click
  the handle, or focus it and press Home, to reset. Width eases when it changes from a tab, keyboard
  step or reset; it tracks the pointer directly while dragging. Header: icon, name, state badge,
  close. Sub-line: parent links. Quick actions row (primary first). Text tabs with an accent
  underline. Body: cards (`--panel`, `--line-soft` border, 3 px radius) holding a console preview,
  key/value lists (label muted left, value right), meters, sparkline rows, tables. Escape closes it;
  the list stays interactive.
- **Warning block** (`.why`): `--warn` text on `--warn-bg`, full rectangle, no stripe.
- **KPI tile**: every tile has the same four rows so a row of tiles lines up: uppercase label
  (+ optional badge), large value with muted unit, a thin bar, a muted foot line. Hosts and
  Virtual machines use a **segmented status bar** (Hosts: one block per host, tooltip
  "host: Online/Stale/Revoked/Unknown"; VMs: stacked running/paused/stopped/unknown) with a legend
  of dot + count so colour never carries the state alone. CPU, Memory and Storage use a capacity
  bar and a change label in the header ("▲ 4% 1h", "▼ 2% 24h", "▲ 1% since opened" or
  "— steady"); no sparkline or trend line — the utilisation chart below draws the trend.
- **Chart**: faint horizontal grid, muted axis text, area fill under the first series only, end
  dots, dashed `--warn` reference line with a right-aligned label, legend of square swatches,
  range segmented control (1H 1D 1W 1M). Lines are monotone-smoothed (no overshoot past a real
  sample; gaps stay gaps). **Hover**: a vertical crosshair, a dot per series and a tooltip with
  the time and each value; the plot is focusable, Left/Right step through points, Escape hides the
  reading, and a tap does the same on touch. On 1D, 1W and 1M, samples are **averaged into
  buckets** and the tooltip says "Average of N samples", so a point never pretends to be a single
  reading.
- **Collapsible widget**: every Overview tile has a chevron icon button at the right of its header
  (`aria-expanded`, "Collapse …" / "Expand …"). Collapsed, the tile keeps its header and one muted
  line of summary (for example "CPU 42% · memory 61%", "3 items · 1 failure"). Tiles start
  expanded; the collapsed set is a per-browser convenience in `localStorage`
  (`nex.overview.collapsed`), never Controller state.
- **Needs attention list**: status icon (triangle, cross, info) coloured by severity, title, muted
  detail line, chevron; each row links to the thing it is about.
- **Placement map**: one column per host (status dot, name, role badge, memory bar, muted meta),
  VM chips with a status dot and a thin border, width proportional to memory; stopped VMs dashed;
  stale host hatched; Unknown VMs in `--warn` text. At most 12 chips per host, then a "+N more"
  chip linking to the host.
- **Status-check strip**: a row of checks, each a status dot + short label + one word
  ("Connection OK", "Fencing Stale", "Authority copy Unknown"). The dot only repeats the word; the
  full sentence is the check's tooltip and sits behind a "Details" expander on the row, never
  inline. Used for Members rows (Connection, Membership, Maintenance, Authority copy, Convergence,
  Fencing), the pool strip above them (Authority generation, Fencing, Host fencing, Watchdog
  cluster; its "About" holds the explanations and the Host fencing switch) and the VM guest tools
  grid (Guest agent, Clipboard, Display resize, each with a "Why?" expander).
- **Info disclosure**: a long explanation goes behind a closed "About …" disclosure (for example
  "About storage", "About removing ISOs", "About changing networks", "How to enable guest tools")
  at the end of the section it explains, not in a footnote paragraph under every table.
- **Per-host access lights**: one small light per pool host with the host name, linking to the
  host; tooltip and accessible name give "host: state. detail"; a muted count line follows
  ("2 accessible · 1 unknown"). A host that is not reporting gets a **hollow** light labelled
  Unknown, never a filled one, so missing evidence never looks like access or its absence.
- **Operational screens versus Settings.** Storage and Networks are operational rail screens: a
  table of what the hosts report now (Storage: Type, Scope, Capacity, Health, Host access, Last
  check, Manage, then the ISO library; Networks: Name, VLAN, Uplink, Host access, VMs, Status,
  Actions, with the VM count linking to the VM list filtered by that network). Settings holds pool
  configuration only: Overview, Members, High availability, Backup repositories, Updates. Old
  settings links for storage, ISO and networks redirect to the operational screens.
- **Settings section**: icon, title, count badge, action buttons on the right of the header
  (primary first), then a table. A secondary nav (230 px) with search lists the sections and ends
  with the pool name, its master and "Settings are stored on the pool master."
- **Port tiles** (host panel): 30 px squares coloured by link speed with a legend; no-link tiles
  use `--line`. **Disk tiles**: name, size/type, role in `--ok`.
- **Buttons**: 30 px (26 px `.sm`), 2 px radius, `--line` border; primary is filled accent; danger
  is red text; disabled is 45 % opacity with a reason.

## Which parts of the reference are illustrative

The reference uses fictional data. Pieces tied to later Nex Core phases (snapshots, backups and
restore tests, migration, HA restart and "survives loss of one host", container services) are
built in the UI with the honest not-yet-available state from rule 8 until their phase lands. The
hardware model name and fencing method in the host panel come from the host's own report.

## Checking your work

- `npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build`.
- Look at the real screen in a browser at 1440×900, 1024×768 and 390×844, in both themes, and
  compare it with the reference side by side. Inspect the screenshots; a passing build is not a
  review.
- Search your diff for `border-left`, `border-right`, `999px`, `gradient`, and radius values above
  3 px before handing it off.
