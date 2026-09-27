# Navigate the Controller console

## Status

Development build 0.4.15 of Nex Controller. The console replaces the earlier desktop layout
(toolbar, resource tree, detail tabs and bottom task strip) with a labelled rail, a top bar,
filterable lists with slide-over panels, a pool **Overview**, operational **Storage** and
**Networks** screens and pool **Settings**. Every workflow reachable before remains reachable;
the other guide pages name the screens used here.

Some screens show features whose Nex Core phase has not landed. They keep their real layout with
disabled controls and say which phase brings them: restore tests (later
Phase 6), live migration (Phase 7), high availability (Phase 9) and container services
(Phase 10). Snapshot controls and the S4 backup repository / Back up now / restore-as-new
flows are available in matching development builds.

## Prerequisites

- An administrator session in Nex Controller (see
  [Set up Nex Controller and enroll a host](manage-controller.md)).
- A current browser. The layout works from phone width upwards; see **Narrow screens** in the
  steps.

## Steps

### The rail

1. Use the labelled icon rail on the left to move between screens. From the top:
   - **Overview** (`#/`) — the dashboard for the scope chosen in the top bar.
   - **VMs** (`#/vms`) — the virtual machine list; selecting a row opens that VM's panel
     (`#/vms/<id>`).
   - **Containers** (`#/containers`), a smaller item directly under **VMs** because container
     services run in Nex-managed VMs — not available yet: every control is disabled and the screen reads
     *Available with container services (Phase 10).*
   - **Hosts** (`#/hosts`) — the host list shows each host's reported management IPv4 address;
     selecting a row opens that host's panel (`#/hosts/<id>`). If Core has not reported that
     address, the list shows **—**.
   - **Storage** (`#/storage`) — the pool's storage repositories with **Type**, **Scope**,
     **Capacity**, **Health**, **Host access**, **Last check** and **Manage**, then the
     **ISO library**. **Host access** has one light per pool host with its name; a hollow
     **Unknown** light means that host is not reporting current access, never that it has none.
   - **Networks** (`#/networks`) — the pool's guest networks with **Name**, **VLAN**, **Uplink**,
     **Host access**, **VMs**, **Status** and **Actions**. The **VMs** count links to the VM list
     filtered to that network (`#/vms?network=<name>`).
   - **Backups** (`#/backups`) — recovery points with VM/repository filters, **Back up now**,
     **Restore as new**, **Replace**, **Protect**, **Test restore**, and **Policies**.
     covers schedules and retention; test-restore recording remains gated. Register destinations under
     **Settings** → **Backup repositories**.
   - Pinned at the bottom: **Tasks** (`#/tasks`), **Events** (`#/events`) and **Settings**
     (`#/settings`, which opens its **Overview** section).

   Longer explanations on these screens sit behind **About …** disclosures (for example
   **About storage**, **About removing ISOs** and **About changing networks**); select one to
   read it.

   The current screen's rail item is highlighted. **Events** shows a small dot when events
   newer than the last time this browser opened **Events** exist; opening **Events** clears it.

### The top bar

2. Open the scope menu at the left of the top bar. It shows a status dot, the site name, the
   pool name (or **All pools**) and the host count. The menu lists each site as
   `<site> · all pools`, followed by that site's pools, then **Create site** and **Add host**.
   Choose **All pools** to see site-wide lists and Overview, or a pool to narrow every list,
   Overview and pool **Settings** to it. If no pool is selected, or a selected pool disappears,
   the first pool in that site's list is selected automatically. **Add host** adds to the chosen
   site and is disabled until a site exists.
3. Use the search field, *Jump to a VM, host, IP or setting*, to go straight to a VM, a host
   (by name or reported IP address), a rail page (for example **Storage**) or a settings
   section. Press `/` anywhere outside a text field to focus it. Use Up and Down to choose a
   result, Enter to open it and Escape to clear the field. It shows up to eight results, or
   **No matches**.
4. Read the activity pill: **N tasks running** with a pulse while tasks are in progress
   (including an ISO upload from this browser), otherwise **No tasks running**. Select it to
   open a short task list; use **Open Tasks** at the bottom for the full **Tasks** page (also on
   the rail).
5. Select the theme button to cycle this browser between the system theme, light and dark.
6. Open the account menu (the person icon). It shows **Signed in as** with your account and
   links to the **API reference** and **Nex source code**, and holds **Sign out**.

### Overview

7. Select **Overview**. For the chosen pool (or every pool of the site with
   `<site> · all pools`) it shows:
   - KPI tiles for **Hosts** (online of total, a stale count when a host is not reporting, and
     one bar block per host — point at a block for that host's state), **Virtual machines**
     (running of total, with a bar and counts for running, paused, stopped and unknown), **CPU**,
     **Memory** and **Storage** (a capacity bar and, in the tile header, the change over the last
     hour — or 24 hours when the chart shows **1W** or **1M**, and since the page opened for
     **Storage** — or **— steady**).
   - A pool utilisation chart of CPU, memory and storage I/O drawn from each host's own history,
     with **1H**, **1D**, **1W** and **1M** ranges. Point at the chart, or tap it, to read the
     values at that time; with the chart focused, Left and Right step through the points and
     Escape hides the reading. On **1D**, **1W** and **1M** each point is the average of the
     samples in its interval, and the reading says *Average of N samples*.
   - **Needs attention**: unhealthy or stale hosts, failed tasks, storage faults, busy VMs and
     available Nex Core updates, each linking to where it can be dealt with.
   - **Placement**: one column per host, with its role and each VM as a block sized by memory.
     A host with more than twelve VMs shows the first twelve and a **+N more** link to its panel.
   - **Recent tasks**, with a **History** link to **Tasks**.
   - **Backups, last 14 nights** — overview chart remains gated; use **Backups** for recovery
     points and **Settings** → **Backup repositories** for destinations.
   - **Resilience**: **Survives loss of one host** is not available yet (Phase 9); **Fencing**,
     **Shared storage** and **Master** are reported now.

   Each of these sections has a chevron button in its header (**Collapse** / **Expand**).
   A collapsed section keeps its header and a one-line summary, such as the current CPU and
   memory for the chart or the item count for **Needs attention**. Every section starts
   expanded; which ones you collapse is remembered in this browser only.

   With no site, **Overview** shows **Start with a site** and a **Create site** button; with a
   site but no hosts, it shows **Add your first host** and an **Add host** button.

### Lists and slide-over panels

8. On **VMs** or **Hosts**, select a row to open its slide-over panel. The panel header shows
   the name, status and where it runs; quick actions sit below it and tabs below those. Drag the
   left edge of the panel to make it wider or narrower; double-click the edge (or focus it and press
   Home) to restore the default width. Press Escape or select the close button (×) to close the
   panel; the list stays where it was and, on **VMs**, the keyboard focus returns to the row. A
   panel link can be copied and opened directly.
9. On **VMs**, use the filter sidebar: search by name, IP address or tag, the **Group by host**
   switch, the **Status**, **Pool**, **Host**, **Guest OS**, **Network** and **Tags** facets with counts,
   **Clear filters**, and **Customize columns**. The view buttons **All**, **Running**,
   **Needs attention** and **Stopped** show their counts. See [Control VM power](vm-power.md)
   for the checkbox bulk actions and [Tag virtual machines](tag-virtual-machines.md) for tags.
10. Open a VM or host console from its panel: a VM's **Console** quick action opens the
    full-screen console (`#/console/<id>`), also for a stopped VM, whose console page offers
    **Start VM** so you can watch it boot; a host's **Console** tab can open
    `#/host-console/<id>` in a new window. The **Nex Controller** link at the top of the
    console page returns to the panel. See [Open a VM or host console](vm-console.md).

### Tasks and Events

11. Select **Tasks** for every task with **Task**, **By**, **When**, **Result** and
    **Progress**. An ISO upload from this browser appears as an **Upload ISO** row with its
    progress and a **Cancel** button while it runs. There is no retry button: a failed task links
    to where its action can be sent again (*Open <VM>*, **Open Storage**, **Open Networks**,
    **Open the ISO library** or the host it ran on). Notices after a request carry a **Tasks**
    link, and **Overview** → **Recent tasks** has **History**.
12. Select **Events** for recorded changes and health transitions with **Event**, **Source**,
    **When** and **Result**. Each panel's **Activity** tab shows the same history for that one
    VM or host.

### Settings

13. Select **Settings**. Settings holds pool configuration only; storage and networks are on
    their own rail screens. The secondary navigation lists **Overview**, **Members**,
    **High availability**, **Backup repositories** and **Updates**
    (`#/settings/<overview|members|ha|backups|updates>`), with a
    **Search settings** field. Below the list it reads `<pool> · master <host>` and
    *Settings are stored on the pool master.* Pool sections need a pool chosen in the scope
    menu; otherwise they read *Choose a pool in the scope menu to see its …*. **Overview** stacks
    the pool summary with the other sections. **Backup repositories** (Phase 6) and
    **High availability** (Phase 9) are not available yet. **Members** holds joining hosts,
    master changes, eject, fencing and maintenance (see [Join a host to a pool](join-a-pool.md)
    and [Pool authority and recovery](pool-authority-and-recovery.md)); **Updates** holds
    **Check** and **Update** (see [Check and apply software updates](software-updates.md)).
14. Older links keep working: `#/sites/<id>` and `#/pools/<id>` choose that scope and show
    **Overview**, `#/pools/<id>/members` opens that pool's **Members** settings, `#/updates`
    opens **Settings** → **Updates**, `#/settings/storage` and `#/settings/iso` open **Storage**,
    and `#/settings/networks` opens **Networks**.

### Narrow screens

15. Below about 900 px wide the search field folds into a **Search** button that opens it over
    the top bar, the activity pill shows only the pulse and the count of running tasks (still
    opens the short task list), the settings navigation becomes a scrolling strip of sections above the
    content, and **VMs** hides its filter sidebar behind a **Filters** button. Below about
    600 px a slide-over panel takes the full width. The rail stays visible at every width.

## Expected result

Every screen is one rail item or one search away, the scope menu decides which site and pool the
lists and **Overview** show, and every VM and host opens in a panel over its list without losing
the list. Phase-gated screens and controls say which phase brings them and never show invented
figures.

The theme, the chosen scope, the last time **Events** was opened, the hidden VM columns, the
collapsed **Overview** sections and the slide-over panel width are stored in this browser only. They are conveniences: they are not Controller state, other browsers
and administrators do not see them, and a replacement Controller does not need them. Clearing site
data resets them to the defaults (system theme, first site and its first pool, every column shown,
every section expanded, default panel width).

## Troubleshooting

- **A pool screen says *Choose a pool in the scope menu to see its …*:** the site has no pool,
  or **All pools** was explicitly chosen while several pools exist. Choose one pool in the menu.
- **Create VM is disabled with *Choose a pool in the scope menu first.*:** choose a pool; a VM
  is created in one pool.
- **The Events dot does not clear on another computer:** the seen time is kept per browser.
- **A link says *Object not found* or *VM not found*:** the VM, host or section is not known to
  this Controller, or inventory is still loading. Use **Retry** on the inventory alert when the
  Controller was unreachable; while a panel is open, the alert and its **Retry** are inside the
  panel.
- **Search finds nothing for an IP address:** search only matches addresses the guest agent or
  host has reported; a VM without guest tools reports none.
- **Values look old:** the console reloads inventory every ten seconds. A stale host keeps its last
  reported values, marked stale; a stale host does not mean its VMs have stopped.

## Verification

Written for the Controller console redesign against the development source (development build
0.4.9); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). This page was written from
the source code and was not walked through separately by its author. Slide-over resize was checked
against the Panel implementation and unit tests; a live browser drag walkthrough was not repeated
for this page.
The first-pool default, explicit **All pools** and removed-pool fallback were exercised in a
[simulated browser regression](../qa/2026-09-25-default-pool-scope.md); no real enrollment
or host join was performed for that check.
The Hosts address display was exercised with simulated Core inventory in a
[browser regression](../qa/2026-09-25-host-management-ip.md); it was not checked on a live host.
