# Nex Controller UI modernization

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Agents never commit: leave each task's changes unstaged for the project owner and report the suggested commit message.

Date: 2026-09-23
Status: implemented — Tasks 1–13 complete and reviewed, awaiting the project owner's commit; evidence in the [QA checks](../qa/controller-ui-modernization/checks.md). Owner: project owner.

**Goal:** Replace the Controller web UI's dated visual layer and vertical layout with a token-driven, flat, brand-correct design that keeps every workflow, the desktop-console navigation model and the honest state handling exactly as they are.

**Architecture:** Presentation only. The React components keep their data flow, API calls, confirmations and error handling; three stylesheets are rewritten on top of one new token sheet, a few small components (Badge, Banner, label maps, task-drawer helpers) replace repeated inline patterns, and the shell loses its menubar, gains a header Actions menu, a collapsed task strip and an off-canvas tree. Nothing durable moves: the only new persistence is per-browser `localStorage` for the drawer and tree preferences, which is browser convenience, not Controller state.

**Tech Stack:** React 19, TypeScript, esbuild, plain CSS custom properties, uPlot, noVNC. No new dependencies, no framework change, no web fonts.

## Authorized change

The project owner reviewed captures of every Controller screen on 2026-09-23 (sign-in, first run, workspace, site, all seven pool tabs, all six host tabs, all four VM tabs, every inline form and dialog, the wizard, menus, Task history, Updates, stale/degraded/error states, and 390/1024/1440 px widths) and asked for a complete modernization. Decisions recorded from that review:

- **Decision: project owner, 2026-09-23** — the square, dot-and-text status badges stay as they are (shape, dot, text). Only their colours move onto tokens. Nothing in this plan rounds them into pills.
- **Decision: project owner, 2026-09-23** — everything else in the reviewed proposal is approved, including the five layout moves that go beyond restyling: remove the File/View/Host/Help menubar and move host actions to a header **Actions** menu; collapse the bottom task drawer to a strip by default; underline tabs instead of boxed gradient tabs; an off-canvas resource tree and icon-only toolbar under 900 px; a console focus mode that hides the shell chrome.

This evolves the [desktop layout plan](2026-09-09-controller-desktop-layout.md) and its [fixed command toolbar](2026-09-09-controller-desktop-layout.md#fixed-command-toolbar--2026-09-10): the toolbar, left tree, resource header, horizontal detail tabs, dense properties and bottom task area all remain; the menubar does not. The [brand guide](../brand/README.md) is the colour and type authority. The Nex Core host-local page (`web/src/local.tsx`) and the installer are outside this plan.

## What the review found

Numbers from the working tree on 2026-09-23:

| Measure | Value |
|---|---|
| Distinct hex colours across `style.css`, `stats.css`, `console.css` | 146 |
| CSS custom properties in use | 6 |
| Gradient backgrounds on chrome surfaces | 13 |
| Font-size declarations that are 11 px or 12 px | 63 of 80 |
| Content height left at 1440×900 after chrome and the open task drawer | about 430 px |
| Content height left at 1024×768 | about 300 px |

Priority issues, in order: content gets under half the screen (P0); the visual language is a decade old, with gradients, 1–2 px radii, a grey canvas and a Windows font stack that falls back on Linux and macOS (P1); no token system, so nothing can be changed once and dark mode is impossible (P1); the menubar duplicates the toolbar and sidebar and hides the only unique host actions (P1); internal identifiers and enums are rendered as labels (`vm-power`, `op-md-4`, `needs_attention`, `host · connection · stale`) (P2); the pool Hosts table is nine columns of paragraphs with its Actions column clipped at 1440 px (P2); narrow and tablet widths are tolerated, not designed (P2); Updates is one card on an empty page, the console stacks two amber bars for one condition, timestamps are long locale strings, tree dots are 6 px and colour-only (P3).

What must not be lost: the honest freshness model and its copy; greyed controls with their reasons; the confirmation dialogs; the density and consistency of property tables; the Stats page, which is already the visual bar the rest of the app is brought up to.

## Design system

### Tokens

Light values are the brand's primary palette plus derived status pairs. Every pair below was checked at ≥ 4.5:1 on 2026-09-23; `tokens.test.mjs` (Task 2) re-checks them on every test run.

| Token | Light | Dark | Role |
|---|---|---|---|
| `--canvas` | `#F6F7FC` porcelain | `#202B3B` midnight navy | page ground |
| `--panel` | `#FFFFFF` | `#26324A` | panels, dialogs, table bodies |
| `--sunken` | `#E5E8F3` platinum | `#2A3649` | section headings, table headers, strips |
| `--line` | `#C6CEE4` silver blue | `#3A4860` | panel and control borders |
| `--line-soft` | `#DDE2EE` | `#2F3D55` | row dividers |
| `--text` | `#17233B` graphite | `#E5E8F3` | body text |
| `--muted` | `#5B6478` | `#AAB5C8` silver | secondary text (5.55:1 on canvas, 4.85:1 on sunken) |
| `--accent` | `#2456D9` action blue | `#7BA7D9` mist blue | links, primary buttons, selected tab |
| `--accent-hover` | `#3478FF` electric blue | `#9CC0EA` | hover |
| `--on-accent` | `#FFFFFF` | `#17233B` | text on accent |
| `--select` | `#E4ECFF` | `#2C3F63` | selected tree row, menu hover, row hover |
| `--topbar` / `--on-topbar` | `#17233B` / `#E5E8F3` | same | the one dark band |
| `--focus` | `#2456D9` | `#9CC0EA` | focus ring |
| `--danger` / `--on-danger` | `#B42318` / `#FFFFFF` | `#F1A8A2` / `#17233B` | destructive controls |
| `--ok` / `--ok-bg` | `#176B42` / `#DDF5E6` | `#7FD8A4` / `#1E3A2C` | fresh, healthy, succeeded |
| `--warn` / `--warn-bg` | `#8A4B00` / `#FFF1D6` | `#F2C067` / `#3D2F14` | stale, degraded, pending, needs attention |
| `--bad` / `--bad-bg` | `#B42318` / `#FDE2E1` | `#F1A8A2` / `#452224` | failed, revoked, blocked |
| `--info` / `--info-bg` | `#2456D9` / `#E4ECFF` | `#9CC0EA` / `#23375A` | queued, uploading, pool master |
| `--neutral` / `--neutral-bg` | `#5B6478` / `#E5E8F3` | `#AAB5C8` / `#2A3649` | unknown, member, cancelled |
| `--series-1..8` | `#2456D9 #0E7490 #7C3AED #0F8A5F #9A6700 #B42318 #5B6478 #3478FF` | same | chart series |
| `--radius-control` / `--radius-panel` | `4px` / `8px` | same | controls / panels, dialogs, menus |
| `--shadow-pop` | `0 8px 24px rgba(23,35,59,.16), 0 1px 2px rgba(23,35,59,.08)` | `0 8px 24px rgba(0,0,0,.5)` | menus, dialogs, popovers only |
| `--font` | `system-ui, -apple-system, "Segoe UI", sans-serif` | same | brand guide stack |
| `--font-mono` | `ui-monospace, "SFMono-Regular", Menlo, Consolas, monospace` | same | identifiers, MACs, paths |

### Type scale

14 px body, 13 px tables and property lists, 12 px meta and footnotes, 11 px only inside badges and axis labels; 20 px page title, 15 px panel titles; weight 600 for headings and row leads; `font-variant-numeric: tabular-nums` on every numeric column. Line height 1.45.

### Surfaces and chrome

No gradients anywhere. Panels are `--panel` on `--canvas` with one `--line` border and `--radius-panel`; controls `--radius-control`, 32 px tall; one shadow, `--shadow-pop`, on menus and dialogs only. Top bar 40 px `--topbar`; toolbar 40 px `--panel` with icon-and-label buttons, disabled at 40% opacity, primary filled `--accent`; page header 56 px with an eyebrow breadcrumb, icon, name, status badges and an **Actions** menu; underline tabs 36 px; task strip 32 px collapsed. Tree rows 28 px with an 8 px connection dot plus a text badge when not fresh, selected row as a `--select` block with a 2 px accent left edge.

### Status system

One `Badge` (square, dot + text, five tones) and one `Banner` (info, warning, danger; left accent bar) used everywhere the app now hand-writes `<span className="badge …">` or `.alert/.notice/.warning`. Never colour alone: every tone has text.

## Global constraints

- Architecture invariant: no new Controller table, cache or file; no derived state. `localStorage` keys `nex.dock` and `nex.tree` hold per-browser preferences only and every read and write is wrapped in try/catch.
- Brand: light presentation is the default; the dark block is `prefers-color-scheme` only; the n tile is never a status or a navigation icon after Task 12; no other product's naming, artwork or wording.
- Accessibility: keyboard reach for every control that exists today, visible focus (`outline: 2px solid var(--focus)`), `aria-current` on tabs and tree rows, text on every status, no text under 11 px, no colour pair under 4.5:1.
- Privacy: fixtures use fictional names, RFC 5737 addresses and `example.net`; captures are not committed.
- No new npm dependency. `npm --prefix web run typecheck`, `npm --prefix web test` and `npm --prefix web run build` pass after every task.
- API impact: **none** for every task. Guide impact: **updated** in Tasks 5, 6, 10 and 13.
- Do not touch `web/src/local.tsx`, `web/src/local.css` or `web/public/local.html`.

## File structure

| Path | Responsibility |
|---|---|
| `web/src/tokens.css` (new) | every colour, radius, shadow and font the UI uses; light block, then the dark block |
| `web/src/style.css` | shell, tree, header, tabs, tables, forms, dialogs; references tokens only |
| `web/src/stats.css`, `web/src/console.css` | unchanged responsibilities, token references only |
| `web/src/ui.tsx` (new) | `Badge`, `Banner`, `badgeTone` |
| `web/src/labels.ts` (new) | `operationLabel`, `statusLabel`, `checkLabel`, `auditLabel`, `relativeTime` |
| `web/src/dock.ts` (new) | `taskCounts`, `newFailures`, `readPreference`, `writePreference` |
| `web/src/main.tsx` | shell: topbar, toolbar, header with Actions menu, tree, task strip, Tasks and Activity tables |
| `web/src/managed.tsx`, `web/src/pool-members.tsx`, `web/src/pool-promotion.tsx`, `web/src/storage.tsx`, `web/src/console.tsx`, `web/src/stats.tsx` | replace inline badge/banner markup with the shared components; small structural changes named per task |
| `web/src/tokens.test.mjs`, `web/src/ui.test.mjs`, `web/src/labels.test.mjs`, `web/src/dock.test.mjs` (new) | runnable checks picked up by `npm test` |
| `tests/integration/ui_review_browser.py` (new) | labeled-fixture capture of every view against a running Controller, for before/after evidence |
| `tests/integration/phase1_browser.py` | menu selectors move from the menubar to the header Actions menu; width checks added |
| `docs/guide/manage-controller.md`, `docs/guide/vm-power.md`, `docs/guide/vm-console.md` | navigation and label wording |
| `docs/qa/controller-ui-modernization/checks.md` (new) | executed evidence |
| `CHANGELOG.md`, `docs/changes/2026-09-23-controller-ui-modernization.json` | record |

Task order matters: Task 1 captures the baseline before anything changes; Tasks 2–4 are the foundation every later task references; Tasks 5–12 can be split across workers by file ownership after Task 4 lands; Task 13 closes.

---

### Task 1: Baseline capture harness

**Files:**
- Create: `tests/integration/ui_review_browser.py`
- Create: `tests/integration/fixtures/ui-review.json`

**Interfaces:**
- Produces: `python3 tests/integration/ui_review_browser.py --url URL --password-file FILE --captures DIR [--views all|shell|pool|host|vm|dialogs]` writing `NN-name.png` files; reused unchanged by Task 13 for the after-captures.

The earlier toolbar QA used labeled browser route stubs for records the lab lacked; this harness does the same for a whole inventory so every tab renders with data. Reads (`GET /api/v1/...`) are answered from the fixture; the sign-in, session, shell and every write go to the real Controller.

- [ ] **Step 1: Write the fixture**

Create `tests/integration/fixtures/ui-review.json` with these records (all fictional; timestamps are placeholders the script rewrites relative to capture time, see step 2):

```json
{
  "sites": [{"id": "site-ams", "name": "Amsterdam lab"}, {"id": "site-ber", "name": "Berlin edge"}],
  "pools": [{"id": "pool-a", "name": "lab-node-01", "site_id": "site-ams", "master_host_id": "host-1"},
            {"id": "pool-b", "name": "edge-node-01", "site_id": "site-ber", "master_host_id": "host-4"}],
  "hosts": [
    {"id": "host-1", "pool_id": "pool-a", "hostname": "lab-node-01", "connection": "fresh", "last_seen": "@-3s", "revoked": false,
     "capabilities": ["managed-vm-v1", "console-v1", "host-console-v1", "iso-download-v1", "storage-repositories-v1", "storage-discovery-v1", "storage-zfs-provision-v1", "storage-zfs-replace-v1", "storage-clustered-iscsi-v2", "vm-cpu-baseline-v1", "vm-delete-v1", "vm-media-v1", "vm-media-drivers-drive-v1", "vm-power-v1", "vm-update-v1", "host-hardware-v1", "host-system-disk-replace-v1", "pool-resource-removal-v1"],
     "inventory": {"observed_at": "@-4s", "hostname": "lab-node-01", "cpu_model": "AMD EPYC 7313P 16-Core Processor", "cpu_count": 32, "memory_bytes": 274877906944, "virtualization": ["kvm"], "os_version": "Nex Core 0.4.4 (Debian 13)", "agent_version": "0.4.4",
       "nics": [{"name": "eno1", "mac": "02:00:5e:00:53:01", "kind": "physical", "master": "br0", "addresses": [], "state": "up"}, {"name": "eno2", "mac": "02:00:5e:00:53:02", "kind": "physical", "addresses": [], "state": "down"}, {"name": "br0", "mac": "02:00:5e:00:53:01", "kind": "bridge", "addresses": ["192.0.2.11/24"], "state": "up"}, {"name": "br0.20", "mac": "02:00:5e:00:53:01", "kind": "vlan", "parent": "br0", "addresses": [], "state": "up"}],
       "disks": [{"name": "nvme0n1", "size_bytes": 1030792151040}, {"name": "sda", "size_bytes": 4294967296000}, {"name": "sdb", "size_bytes": 4294967296000}],
       "checks": [{"name": "libvirt", "status": "healthy", "freshness": "fresh", "observed_at": "@-4s", "last_success": "@-4s"}, {"name": "kvm", "status": "healthy", "freshness": "fresh", "observed_at": "@-4s", "last_success": "@-4s"}, {"name": "management-network", "status": "healthy", "freshness": "fresh", "observed_at": "@-4s", "last_success": "@-4s"}, {"name": "pool-storage-reconciliation", "status": "healthy", "freshness": "fresh", "observed_at": "@-4s", "last_success": "@-4s"}],
       "vms": [{"id": "vm-web", "state": "running"}, {"id": "vm-build", "state": "shut off"}, {"id": "vm-ctl", "state": "running"}]}},
    {"id": "host-2", "pool_id": "pool-a", "hostname": "lab-node-02", "connection": "fresh", "last_seen": "@-5s", "revoked": false, "capabilities": "@same-as-host-1",
     "inventory": {"observed_at": "@-5s", "hostname": "lab-node-02", "cpu_model": "AMD EPYC 7313P 16-Core Processor", "cpu_count": 32, "memory_bytes": 274877906944, "virtualization": ["kvm"], "os_version": "Nex Core 0.4.4 (Debian 13)", "agent_version": "0.4.4", "nics": "@same-as-host-1", "disks": "@same-as-host-1",
       "checks": [{"name": "libvirt", "status": "healthy", "freshness": "fresh", "observed_at": "@-5s", "last_success": "@-5s"}, {"name": "kvm", "status": "healthy", "freshness": "fresh", "observed_at": "@-5s", "last_success": "@-5s"}, {"name": "zfs-pool", "status": "degraded", "freshness": "fresh", "observed_at": "@-5s", "last_success": "@-2d", "error": "zpool tank is DEGRADED: sdb FAULTED (too many errors)"}],
       "vms": [{"id": "vm-db", "state": "running"}]}},
    {"id": "host-3", "pool_id": "pool-a", "hostname": "lab-node-03", "connection": "stale", "last_seen": "@-7m", "revoked": false, "capabilities": "@same-as-host-1",
     "inventory": {"observed_at": "@-6m", "hostname": "lab-node-03", "cpu_model": "AMD EPYC 7313P 16-Core Processor", "cpu_count": 16, "memory_bytes": 137438953472, "virtualization": ["kvm"], "os_version": "Nex Core 0.4.4 (Debian 13)", "agent_version": "0.4.4", "nics": "@same-as-host-1", "disks": "@same-as-host-1",
       "checks": [{"name": "libvirt", "status": "healthy", "freshness": "stale", "observed_at": "@-6m"}, {"name": "kvm", "status": "healthy", "freshness": "stale", "observed_at": "@-6m"}],
       "vms": [{"id": "vm-legacy", "state": "running"}]}},
    {"id": "host-4", "pool_id": "pool-b", "hostname": "edge-node-01", "connection": "fresh", "last_seen": "@-2s", "revoked": false, "capabilities": "@same-as-host-1",
     "inventory": {"observed_at": "@-2s", "hostname": "edge-node-01", "cpu_model": "Intel Xeon E-2388G", "cpu_count": 8, "memory_bytes": 68719476736, "virtualization": ["kvm"], "os_version": "Nex Core 0.4.4 (Debian 13)", "agent_version": "0.4.4", "nics": [{"name": "eno1", "mac": "02:00:5e:00:53:41", "kind": "physical", "master": "br0", "addresses": [], "state": "up"}, {"name": "br0", "mac": "02:00:5e:00:53:41", "kind": "bridge", "addresses": ["198.51.100.21/24"], "state": "up"}], "disks": [{"name": "nvme0n1", "size_bytes": 512110190592}],
       "checks": [{"name": "libvirt", "status": "healthy", "freshness": "fresh", "observed_at": "@-2s", "last_success": "@-2s"}], "vms": [{"id": "vm-edge", "state": "running"}]}}
  ],
  "vms": [
    {"id": "vm-web", "pool_id": "pool-a", "host_id": "host-1", "name": "web-01", "operation_id": "op-vm-web", "status": "succeeded", "state": "running", "freshness": "fresh", "role": "guest", "protected": false,
     "spec": {"revision": 3, "description": "Public web frontend", "guest_os": "linux", "cpu": {"count": 4, "sockets": 1, "cores": 4, "threads": 1, "model": "baseline"}, "memory": {"mode": "maximum", "size_mib": 8192}, "disks": [{"storage_id": "st-nfs", "name": "web-01-root", "size_gib": 40, "bus": "virtio", "thin": true, "boot_order": 1}], "nics": [{"network_id": "net-2", "model": "virtio", "mac": "02:00:5e:00:53:a1", "mtu": 1500}], "boot": {"firmware": "uefi", "machine": "q35", "media_boot_order": 0, "secure_boot": "enrolled", "tpm": "none"}, "media_id": "md-debian", "start_after_create": true, "autostart": true},
     "guest": {"agent": "connected", "os": "Debian GNU/Linux 13 (trixie)", "hostname": "web-01", "addresses": [{"interface": "enp1s0", "address": "192.0.2.41"}], "clipboard": "supported", "resize": true, "media": "empty"},
     "usage": {"observed_at": "@-4s", "sample_window_ms": 5000, "cpu_percent": 12.4, "memory_assigned_bytes": 8589934592, "memory_maximum_bytes": 8589934592, "memory_host_bytes": 9019431321, "disk_used_bytes": 12025908428, "disk_capacity_bytes": 42949672960}},
    {"id": "vm-db", "pool_id": "pool-a", "host_id": "host-2", "name": "db-01", "operation_id": "op-vm-db", "status": "succeeded", "state": "running", "freshness": "fresh", "role": "guest", "protected": false,
     "spec": {"revision": 2, "description": "PostgreSQL primary", "guest_os": "linux", "cpu": {"count": 8, "sockets": 1, "cores": 8, "threads": 1, "model": "baseline"}, "memory": {"mode": "dedicated", "size_mib": 32768}, "disks": [{"storage_id": "st-nfs", "name": "db-01-root", "size_gib": 40, "bus": "virtio", "thin": true, "boot_order": 1}, {"storage_id": "st-zfs", "name": "db-01-data", "size_gib": 500, "bus": "virtio", "thin": true, "boot_order": 0}], "nics": [{"network_id": "net-2", "model": "virtio", "mac": "02:00:5e:00:53:a2", "mtu": 1500}], "boot": {"firmware": "uefi", "machine": "q35", "media_boot_order": 0, "secure_boot": "enrolled", "tpm": "none"}, "media_id": "", "start_after_create": true, "autostart": true},
     "guest": {"agent": "connected", "os": "Debian GNU/Linux 13 (trixie)", "hostname": "db-01", "addresses": [{"interface": "enp1s0", "address": "192.0.2.42"}], "clipboard": "supported", "resize": true},
     "usage": {"observed_at": "@-5s", "sample_window_ms": 5000, "cpu_percent": 63.8, "memory_assigned_bytes": 34359738368, "memory_maximum_bytes": 34359738368, "memory_host_bytes": 36077725286, "disk_used_bytes": 332859965440, "disk_capacity_bytes": 579820584960}},
    {"id": "vm-build", "pool_id": "pool-a", "host_id": "host-1", "name": "build-runner-01", "operation_id": "op-vm-build", "status": "succeeded", "state": "shut off", "freshness": "fresh", "role": "guest", "protected": false,
     "spec": {"revision": 1, "description": "CI build runner", "guest_os": "linux", "cpu": {"count": 4, "sockets": 1, "cores": 4, "threads": 1, "model": "baseline"}, "memory": {"mode": "maximum", "size_mib": 8192}, "disks": [{"storage_id": "st-local-1", "name": "build-runner-01-root", "size_gib": 120, "bus": "virtio", "thin": true, "boot_order": 1}], "nics": [{"network_id": "net-2", "model": "virtio", "mac": "02:00:5e:00:53:a3", "mtu": 1500}], "boot": {"firmware": "uefi", "machine": "q35", "media_boot_order": 0, "secure_boot": "enrolled", "tpm": "none"}, "media_id": "", "start_after_create": false, "autostart": false},
     "guest": {"agent": "disconnected", "clipboard": "unknown", "resize": false}, "usage": null},
    {"id": "vm-win", "pool_id": "pool-a", "host_id": "host-1", "name": "win-ad-01", "operation_id": "op-vm-win", "status": "succeeded", "state": "running", "freshness": "fresh", "role": "guest", "protected": false,
     "spec": {"revision": 1, "description": "Windows Server 2025 domain controller", "guest_os": "windows", "cpu": {"count": 4, "sockets": 1, "cores": 4, "threads": 1, "model": "baseline"}, "memory": {"mode": "dedicated", "size_mib": 16384}, "disks": [{"storage_id": "st-nfs", "name": "win-ad-01-c", "size_gib": 100, "bus": "virtio", "thin": true, "boot_order": 1}], "nics": [{"network_id": "net-2", "model": "virtio", "mac": "02:00:5e:00:53:a4", "mtu": 1500}], "boot": {"firmware": "uefi", "machine": "q35", "media_boot_order": 2, "secure_boot": "enrolled", "tpm": "emulated"}, "media_id": "md-win", "drivers_media_id": "md-virtio", "start_after_create": true, "autostart": true},
     "guest": {"agent": "no-channel", "clipboard": "unknown", "resize": false},
     "usage": {"observed_at": "@-4s", "sample_window_ms": 5000, "cpu_percent": 4.1, "memory_assigned_bytes": 17179869184, "memory_maximum_bytes": 17179869184, "memory_host_bytes": 18038862643, "disk_used_bytes": 40802189312, "disk_capacity_bytes": 107374182400}},
    {"id": "vm-legacy", "pool_id": "pool-a", "host_id": "host-3", "name": "legacy-app-01", "operation_id": "op-vm-legacy", "status": "succeeded", "state": "running", "freshness": "stale", "role": "guest", "protected": false,
     "spec": {"revision": 1, "description": "Vendor appliance, BIOS boot", "guest_os": "linux", "cpu": {"count": 4, "sockets": 1, "cores": 4, "threads": 1, "model": "baseline"}, "memory": {"mode": "maximum", "size_mib": 8192}, "disks": [{"storage_id": "st-nfs", "name": "legacy-app-01-root", "size_gib": 80, "bus": "virtio", "thin": true, "boot_order": 1}], "nics": [{"network_id": "net-2", "model": "virtio", "mac": "02:00:5e:00:53:a5", "mtu": 1500}], "boot": {"firmware": "bios", "machine": "pc", "media_boot_order": 0}, "media_id": "", "start_after_create": true, "autostart": true},
     "guest": {"agent": "unresponsive", "clipboard": "unknown", "resize": false}, "usage": null},
    {"id": "vm-ctl", "pool_id": "pool-a", "host_id": "host-1", "name": "nex-controller", "operation_id": "", "status": "succeeded", "state": "running", "freshness": "fresh", "role": "appliance", "protected": true,
     "spec": {"revision": 1, "description": "Nex Controller appliance", "guest_os": "linux", "cpu": {"count": 2, "sockets": 1, "cores": 2, "threads": 1, "model": "baseline"}, "memory": {"mode": "maximum", "size_mib": 4096}, "disks": [{"storage_id": "st-local-1", "name": "nex-controller-root", "size_gib": 32, "bus": "virtio", "thin": true, "boot_order": 1}], "nics": [{"network_id": "net-1", "model": "virtio", "mac": "02:00:5e:00:53:c0", "mtu": 1500}], "boot": {"firmware": "uefi", "machine": "q35", "media_boot_order": 0, "secure_boot": "enrolled", "tpm": "none"}, "media_id": "", "start_after_create": true, "autostart": true},
     "guest": {"agent": "connected", "os": "Debian GNU/Linux 13 (trixie)", "hostname": "nex-controller", "addresses": [{"interface": "enp1s0", "address": "192.0.2.10"}], "clipboard": "supported", "resize": true},
     "usage": {"observed_at": "@-4s", "sample_window_ms": 5000, "cpu_percent": 2.2, "memory_assigned_bytes": 4294967296, "memory_maximum_bytes": 4294967296, "memory_host_bytes": 4509715660, "disk_used_bytes": 6979321856, "disk_capacity_bytes": 34359738368}},
    {"id": "vm-edge", "pool_id": "pool-b", "host_id": "host-4", "name": "edge-proxy-01", "operation_id": "op-vm-edge", "status": "succeeded", "state": "running", "freshness": "fresh", "role": "guest", "protected": false,
     "spec": {"revision": 1, "description": "Edge reverse proxy", "guest_os": "linux", "cpu": {"count": 2, "sockets": 1, "cores": 2, "threads": 1, "model": "baseline"}, "memory": {"mode": "maximum", "size_mib": 4096}, "disks": [{"storage_id": "st-local-4", "name": "edge-proxy-01-root", "size_gib": 20, "bus": "virtio", "thin": true, "boot_order": 1}], "nics": [{"network_id": "net-3", "model": "virtio", "mac": "02:00:5e:00:53:b1", "mtu": 1500}], "boot": {"firmware": "uefi", "machine": "q35", "media_boot_order": 0, "secure_boot": "enrolled", "tpm": "none"}, "media_id": "", "start_after_create": true, "autostart": true},
     "guest": {"agent": "connected", "os": "Debian GNU/Linux 13 (trixie)", "hostname": "edge-proxy-01", "addresses": [{"interface": "enp1s0", "address": "198.51.100.31"}], "clipboard": "supported", "resize": true},
     "usage": {"observed_at": "@-2s", "sample_window_ms": 5000, "cpu_percent": 7.9, "memory_assigned_bytes": 4294967296, "memory_maximum_bytes": 4294967296, "memory_host_bytes": 4509715660, "disk_used_bytes": 5476083302, "disk_capacity_bytes": 21474836480}}
  ],
  "storage": [
    {"id": "st-local-1", "pool_id": "pool-a", "host_id": "host-1", "name": "local-images", "operation_id": "op-st-1", "status": "succeeded", "path": "/var/lib/libvirt/images", "kind": "local", "generation": 1, "capabilities": {"vm_disk": true, "iso": true, "thin": true, "thick": true, "shared": false}, "access": {"status": "ready", "readable": true, "writable": true, "available_bytes": 442381631488, "total_bytes": 944892805120, "observed_at": "@-4s"}},
    {"id": "st-nfs", "pool_id": "pool-a", "host_id": "host-1", "name": "nfs-shared", "operation_id": "op-st-2", "status": "succeeded", "path": "/mnt/nex/nfs-shared", "kind": "nfs", "source": {"nfs": {"server": "nfs.example.net", "export": "/export/nex"}}, "generation": 7, "capabilities": {"vm_disk": true, "iso": true, "thin": true, "thick": false, "shared": true, "eligible_host_ids": ["host-1", "host-2", "host-3"]}, "access": {"status": "ready", "readable": true, "writable": true, "available_bytes": 2373024415744, "total_bytes": 8589934592000, "observed_at": "@-4s"},
     "member_access": [{"repository_id": "st-nfs", "host_id": "host-1", "generation": 7, "identity": "nfs:nfs.example.net:/export/nex", "readable": true, "writable": true, "qemu_writable": true, "locking": true, "observed_at": "@-4s"}, {"repository_id": "st-nfs", "host_id": "host-2", "generation": 7, "identity": "nfs:nfs.example.net:/export/nex", "readable": true, "writable": true, "qemu_writable": true, "locking": true, "observed_at": "@-5s"}, {"repository_id": "st-nfs", "host_id": "host-3", "generation": 6, "identity": "nfs:nfs.example.net:/export/nex", "readable": false, "writable": false, "qemu_writable": false, "locking": false, "observed_at": "@-7m", "error": "host not reporting"}]},
    {"id": "st-zfs", "pool_id": "pool-a", "host_id": "host-2", "name": "tank", "operation_id": "op-st-3", "status": "succeeded", "path": "/tank/nex", "kind": "zfs", "zfs_provisioned": true, "zfs_scope": "host", "source": {"zfs": {"pool": "tank", "dataset": "tank/nex", "mount_path": "/tank/nex"}}, "generation": 2, "capabilities": {"vm_disk": true, "iso": false, "thin": true, "thick": true, "shared": false},
     "access": {"status": "ready", "readable": true, "writable": true, "available_bytes": 3328599654400, "total_bytes": 3865470566400, "observed_at": "@-5s", "zfs_pool": {"name": "tank", "state": "DEGRADED", "status": "One or more devices has been faulted in response to persistent errors.", "action": "Replace the faulted device, or use 'zpool clear' to mark the device repaired.", "scan": {"function": "scrub", "state": "completed", "errors": "0"}, "devices": [{"name": "tank", "state": "DEGRADED"}, {"name": "mirror-0", "state": "DEGRADED", "parent": "tank"}, {"name": "sda", "path": "/dev/disk/by-id/ata-EXAMPLE_4TB_A1", "state": "ONLINE", "parent": "mirror-0", "read_errors": "0", "write_errors": "0", "checksum_errors": "0"}, {"name": "sdb", "path": "/dev/disk/by-id/ata-EXAMPLE_4TB_B2", "state": "FAULTED", "parent": "mirror-0", "read_errors": "0", "write_errors": "312", "checksum_errors": "0"}]}}},
    {"id": "st-iscsi", "pool_id": "pool-a", "host_id": "host-1", "name": "san-lun0", "operation_id": "op-st-4", "status": "succeeded", "path": "/mnt/nex/san-lun0", "kind": "iscsi", "source": {"iscsi": {"portal": "203.0.113.5:3260", "target_iqn": "iqn.2026-01.net.example:san.lun0", "lun": 0, "wwid": "36001405example0001", "filesystem_uuid": "3f1c2a9e-0000-4000-8000-000000000001", "exclusive_target": true, "mode": "clustered_gfs2", "cluster_id": "nexgfs1", "lock_table": "nexgfs1:lun0", "eligible_host_ids": ["host-1", "host-2"]}}, "generation": 3, "capabilities": {"vm_disk": true, "iso": false, "thin": false, "thick": true, "shared": true, "eligible_host_ids": ["host-1", "host-2"]}, "access": {"status": "ready", "readable": true, "writable": true, "available_bytes": 1932735283200, "total_bytes": 2147483648000, "observed_at": "@-4s"}},
    {"id": "st-local-4", "pool_id": "pool-b", "host_id": "host-4", "name": "local-images", "operation_id": "op-st-5", "status": "succeeded", "path": "/var/lib/libvirt/images", "kind": "local", "generation": 1, "capabilities": {"vm_disk": true, "iso": true, "thin": true, "thick": true, "shared": false}, "access": {"status": "ready", "readable": true, "writable": true, "available_bytes": 193273528320, "total_bytes": 472446402560, "observed_at": "@-2s"}}
  ],
  "networks": [
    {"id": "net-1", "pool_id": "pool-a", "host_id": "host-1", "name": "management", "operation_id": "op-net-1", "status": "succeeded", "kind": "bridge", "source": "br0", "generation": 2, "member_access": [{"network_id": "net-1", "host_id": "host-1", "generation": 2, "bridge": "br0", "uplink_present": true, "observed_at": "@-4s"}, {"network_id": "net-1", "host_id": "host-2", "generation": 2, "bridge": "br0", "uplink_present": true, "observed_at": "@-5s"}, {"network_id": "net-1", "host_id": "host-3", "generation": 1, "bridge": "br0", "uplink_present": false, "observed_at": "@-7m", "error": "host not reporting"}]},
    {"id": "net-2", "pool_id": "pool-a", "host_id": "host-1", "name": "guest-vlan20", "operation_id": "op-net-2", "status": "succeeded", "kind": "vlan", "source": "br0", "uplink": "eno1", "vlan": 20, "generation": 2, "member_access": [{"network_id": "net-2", "host_id": "host-1", "generation": 2, "bridge": "br0.20", "uplink_present": true, "observed_at": "@-4s"}, {"network_id": "net-2", "host_id": "host-2", "generation": 2, "bridge": "br0.20", "uplink_present": true, "observed_at": "@-5s"}, {"network_id": "net-2", "host_id": "host-3", "generation": 1, "bridge": "br0.20", "uplink_present": false, "observed_at": "@-7m", "error": "host not reporting"}]},
    {"id": "net-3", "pool_id": "pool-b", "host_id": "host-4", "name": "management", "operation_id": "op-net-3", "status": "succeeded", "kind": "bridge", "source": "br0", "generation": 1, "member_access": [{"network_id": "net-3", "host_id": "host-4", "generation": 1, "bridge": "br0", "uplink_present": true, "observed_at": "@-2s"}]}
  ],
  "media": [
    {"id": "md-debian", "pool_id": "pool-a", "host_id": "host-1", "storage_id": "st-nfs", "name": "debian-13.1.0-amd64-netinst.iso", "operation_id": "op-md-1", "status": "succeeded", "size_bytes": 736100352, "sha256": "2a9b5c7e0f4d1b6a8c3e9f0d7b2a4c6e8f1d3b5a7c9e0f2d4b6a8c0e2f4d6b8a", "member_access": [{"media_id": "md-debian", "host_id": "host-1", "storage_id": "st-nfs", "sha256": "2a9b5c7e", "observed_at": "@-4s"}]},
    {"id": "md-virtio", "pool_id": "pool-a", "host_id": "host-1", "storage_id": "st-nfs", "name": "virtio-win-0.1.271.iso", "operation_id": "op-md-2", "status": "succeeded", "size_bytes": 662700032, "sha256": "9c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d", "member_access": []},
    {"id": "md-win", "pool_id": "pool-a", "host_id": "host-1", "storage_id": "st-nfs", "name": "windows-server-2025-eval.iso", "operation_id": "op-md-3", "status": "succeeded", "size_bytes": 5798205849, "sha256": "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0", "member_access": []},
    {"id": "md-ubuntu", "pool_id": "pool-a", "host_id": "host-1", "storage_id": "st-local-1", "name": "ubuntu-26.04-live-server-amd64.iso", "operation_id": "op-md-4", "status": "pending", "size_bytes": 3113851289, "sha256": "", "member_access": []}
  ],
  "operations": [
    {"id": "op-md-4", "host_id": "host-1", "kind": "iso-import", "request_id": "req-op-md-4", "status": "pending", "created_at": "@-2m", "updated_at": "@-2m", "actor": "root"},
    {"id": "op-power-1", "host_id": "host-1", "kind": "vm-power", "request_id": "req-op-power-1", "status": "failed", "created_at": "@-9m", "updated_at": "@-9m", "actor": "root", "error": "Core rejected the shutdown: the guest agent is unresponsive. Use Force stop if the guest is hung."},
    {"id": "op-refresh-3", "host_id": "host-3", "kind": "refresh-inventory", "request_id": "req-op-refresh-3", "status": "needs_attention", "created_at": "@-12m", "updated_at": "@-12m", "actor": "root", "error": "The host did not answer within 30 s; its last inventory is 7 minutes old."},
    {"id": "op-vm-win", "host_id": "host-1", "kind": "vm-create", "request_id": "req-op-vm-win", "status": "succeeded", "created_at": "@-3h", "updated_at": "@-3h", "actor": "root"},
    {"id": "op-st-4", "host_id": "host-1", "kind": "storage-register", "request_id": "req-op-st-4", "status": "succeeded", "created_at": "@-5h", "updated_at": "@-5h", "actor": "root"},
    {"id": "op-refresh-1", "host_id": "host-1", "kind": "refresh-inventory", "request_id": "req-op-refresh-1", "status": "succeeded", "created_at": "@-6h", "updated_at": "@-6h", "actor": "root"},
    {"id": "op-vm-db", "host_id": "host-2", "kind": "vm-create", "request_id": "req-op-vm-db", "status": "succeeded", "created_at": "@-2d", "updated_at": "@-2d", "actor": "root"},
    {"id": "op-vm-web", "host_id": "host-1", "kind": "vm-create", "request_id": "req-op-vm-web", "status": "succeeded", "created_at": "@-2d", "updated_at": "@-2d", "actor": "root"},
    {"id": "op-st-3", "host_id": "host-2", "kind": "storage-zfs-provision", "request_id": "req-op-st-3", "status": "succeeded", "created_at": "@-3d", "updated_at": "@-3d", "actor": "root"}
  ],
  "audit": [
    {"id": 41, "actor": "root", "action": "operation.create", "resource_id": "vm-legacy", "created_at": "@-9m", "detail": "vm-power shutdown rejected by Core: guest agent unresponsive"},
    {"id": 40, "actor": "root", "action": "operation.create", "resource_id": "md-ubuntu", "created_at": "@-2m", "detail": "iso-import ubuntu-26.04-live-server-amd64.iso to local-images"},
    {"id": 39, "actor": "system", "action": "host.connection.stale", "resource_id": "host-3", "created_at": "@-6m", "detail": "lab-node-03 stopped reporting"},
    {"id": 38, "actor": "system", "action": "host.check.degraded", "resource_id": "host-2/zfs-pool", "created_at": "@-2d", "detail": "zpool tank is DEGRADED"},
    {"id": 37, "actor": "root", "action": "operation.create", "resource_id": "vm-win", "created_at": "@-3h", "detail": "vm-create win-ad-01 on lab-node-01"},
    {"id": 36, "actor": "root", "action": "host.add", "resource_id": "host-3", "created_at": "@-5d", "detail": "lab-node-03 joined lab-node-01"},
    {"id": 35, "actor": "root", "action": "host.add", "resource_id": "host-1", "created_at": "@-7d", "detail": "lab-node-01 became master of its pool"},
    {"id": 34, "actor": "root", "action": "site.create", "resource_id": "site-ams", "created_at": "@-7d", "detail": "Amsterdam lab"}
  ],
  "updates": {"package": "nex-controller", "installed_version": "0.4.4", "available_version": "0.4.5", "update_available": true, "core_available_version": "0.4.5", "repo_url": "https://apt.example.net/nex"},
  "hardware": {"observed_at": "@-4s",
    "system": {"vendor": "Example Systems", "model": "ES-1U-EPYC", "version": "2.1", "serial": "ES1U-000123", "board_vendor": "Example Systems", "board_name": "ES-MB-7313", "board_version": "1.0", "chassis_vendor": "Example Systems", "chassis_type": "Rack Mount Chassis", "bios_vendor": "Example Firmware", "bios_version": "3.4.1", "bios_date": "2026-03-11"},
    "cpu": {"model": "AMD EPYC 7313P 16-Core Processor", "sockets": 1, "cores": 16, "threads": 32},
    "memory": {"total_bytes": 274877906944, "modules": [{"slot": "DIMM_A1", "size_bytes": 34359738368, "type": "DDR4", "speed_mts": 3200, "manufacturer": "Example Memory", "part": "EM32G3200R"}, {"slot": "DIMM_B1", "size_bytes": 34359738368, "type": "DDR4", "speed_mts": 3200, "manufacturer": "Example Memory", "part": "EM32G3200R"}, {"slot": "DIMM_C1", "size_bytes": 34359738368, "type": "DDR4", "speed_mts": 3200, "manufacturer": "Example Memory", "part": "EM32G3200R"}, {"slot": "DIMM_D1", "size_bytes": 34359738368, "type": "DDR4", "speed_mts": 3200, "manufacturer": "Example Memory", "part": "EM32G3200R"}, {"slot": "DIMM_E1", "size_bytes": 34359738368, "type": "DDR4", "speed_mts": 3200, "manufacturer": "Example Memory", "part": "EM32G3200R"}, {"slot": "DIMM_F1", "size_bytes": 34359738368, "type": "DDR4", "speed_mts": 3200, "manufacturer": "Example Memory", "part": "EM32G3200R"}, {"slot": "DIMM_G1", "size_bytes": 34359738368, "type": "DDR4", "speed_mts": 3200, "manufacturer": "Example Memory", "part": "EM32G3200R"}, {"slot": "DIMM_H1", "size_bytes": 34359738368, "type": "DDR4", "speed_mts": 3200, "manufacturer": "Example Memory", "part": "EM32G3200R"}]},
    "disks": {"devices": [{"name": "nvme0n1", "model": "EXAMPLE NVMe 960GB", "serial": "EXN0001", "size_bytes": 1030792151040, "rotational": false, "transport": "nvme"}, {"name": "sda", "model": "EXAMPLE HDD 4TB", "serial": "EXH0001", "size_bytes": 4294967296000, "rotational": true, "transport": "sata"}, {"name": "sdb", "model": "EXAMPLE HDD 4TB", "serial": "EXH0002", "size_bytes": 4294967296000, "rotational": true, "transport": "sata"}]},
    "pci": {"devices": [{"address": "0000:01:00.0", "class": "Ethernet controller", "class_id": "0200", "vendor": "Example Networks", "vendor_id": "1af4", "device": "10GbE dual-port", "device_id": "1000"}, {"address": "0000:02:00.0", "class": "Non-Volatile memory controller", "class_id": "0108", "vendor": "Example Storage", "vendor_id": "1b36", "device": "NVMe SSD", "device_id": "0010"}]},
    "sensors": {"temperatures": [{"key": "temperature.k10temp.tctl", "chip": "k10temp", "label": "Tctl", "max_celsius": 90, "crit_celsius": 95}, {"key": "temperature.nvme.composite", "chip": "nvme", "label": "Composite", "max_celsius": 70, "crit_celsius": 80}]},
    "md_arrays": {"arrays": [{"name": "md0", "device": "/dev/md0", "level": "raid1", "raid_disks": 2, "degraded": 0, "members": [{"name": "nvme0n1p2", "device": "/dev/nvme0n1p2", "slot": "0", "state": "in_sync"}, {"name": "nvme1n1p2", "device": "/dev/nvme1n1p2", "slot": "1", "state": "in_sync"}]}]}},
  "members": {"pool_id": "pool-a", "pool_name": "lab-node-01", "master_host_id": "host-1", "master_connection": "fresh", "master_seen_at": "@-3s", "generation": 42, "authoritative": true, "maintenance_supported": true, "replica_supported": true, "transfer_supported": true, "host_fencing_supported": true, "fence": null, "authority": {"generation": 42, "digest": "sha256:5f2a…c91e", "observed_at": "@-3s"}, "transfer": null, "last_transfer": null, "promotion": null, "last_promotion": null,
    "members": [
      {"host_id": "host-1", "name": "lab-node-01", "role": "master", "status": "healthy", "connection": "fresh", "checks": [{"name": "storage", "status": "healthy"}, {"name": "networks", "status": "healthy"}, {"name": "media", "status": "healthy"}], "access": {"healthy": 4, "pending": 0, "failed": 0, "stale": 0}, "maintenance": false, "guests": {"known": true, "total": 3, "running": 2, "names": ["web-01", "build-runner-01", "nex-controller"]}, "replica": null, "transfer_supported": true},
      {"host_id": "host-2", "name": "lab-node-02", "role": "member", "status": "healthy", "connection": "fresh", "checks": [{"name": "storage", "status": "healthy"}, {"name": "networks", "status": "healthy"}, {"name": "media", "status": "healthy"}], "access": {"healthy": 4, "pending": 0, "failed": 0, "stale": 0}, "maintenance": false, "guests": {"known": true, "total": 1, "running": 1, "names": ["db-01"]}, "replica": {"generation": 42, "digest": "sha256:5f2a…c91e", "verified": true, "received_at": "@-3s", "acknowledged_generation": 42, "acknowledged_at": "@-3s", "state": "current"}, "transfer_supported": true},
      {"host_id": "host-3", "name": "lab-node-03", "role": "member", "status": "needs_attention", "error": "The host has not reported for 7 minutes.", "connection": "stale", "checks": [{"name": "storage", "status": "unknown", "detail": "last report 7 minutes old"}, {"name": "networks", "status": "unknown"}, {"name": "media", "status": "unknown"}], "access": {"healthy": 0, "pending": 0, "failed": 0, "stale": 4}, "maintenance": false, "guests": {"known": false, "total": 1, "running": 1, "names": ["legacy-app-01"]}, "replica": {"generation": 40, "digest": "sha256:11ab…77de", "verified": true, "received_at": "@-9m", "acknowledged_generation": 40, "acknowledged_at": "@-9m", "state": "behind"}, "transfer_supported": true}
    ]},
  "join_candidates": {"candidates": [{"host_id": "host-4", "name": "edge-node-01", "connection": "fresh", "eligible": false, "rows": [{"check": "CPU baseline", "subject": "EPYC-Milan", "result": "will_not_converge", "detail": "edge-node-01 reports an Intel CPU; the pool baseline is EPYC-Milan."}, {"check": "Shared storage", "subject": "nfs-shared", "result": "compatible"}, {"check": "Guest networks", "subject": "guest-vlan20", "result": "refuses_join", "detail": "no uplink named eno1"}]}]}
}
```

`"@-3s"`, `"@-7m"`, `"@-3h"`, `"@-2d"` are rewritten to ISO timestamps relative to capture time; `"@same-as-host-1"` copies the named field from `hosts[0]`. Host network and usage-history series are generated in code (step 2) because they are shape, not content.

- [ ] **Step 2: Write the harness**

```python
#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Capture every Controller view against a running Controller with a labeled simulated inventory.

Reads are answered from tests/integration/fixtures/ui-review.json; the sign-in, session, shell
and every write go to the real Controller. Console tabs therefore report "not found": no host
exists behind the fixture. Fictional names and RFC 5737 addresses only.
"""
import argparse, json, math, re, time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = json.loads((ROOT / 'tests/integration/fixtures/ui-review.json').read_text())
UNITS = {'s': 1, 'm': 60, 'h': 3600, 'd': 86400}
NOW = datetime.now(timezone.utc)


def resolve(value, hosts):
    """Rewrite "@-7m" timestamps and "@same-as-host-1" references."""
    if isinstance(value, str):
        m = re.fullmatch(r'@-(\d+)([smhd])', value)
        if m:
            return (NOW - timedelta(seconds=int(m[1]) * UNITS[m[2]])).isoformat().replace('+00:00', 'Z')
        return value
    if isinstance(value, list):
        return [resolve(v, hosts) for v in value]
    if isinstance(value, dict):
        out = {}
        for k, v in value.items():
            if v == '@same-as-host-1':
                out[k] = hosts[0][k] if k in hosts[0] else hosts[0]['inventory'][k]
            else:
                out[k] = resolve(v, hosts)
        return out
    return value


def series(obj, id_, rng):
    n, step = {'live': (24, 5), '10m': (120, 5), '24h': (288, 300), '30d': (720, 3600), '1y': (365, 86400)}.get(rng, (120, 5))
    def wave(base, amp, period):
        return [max(0.0, base + amp * math.sin(i / period) + amp * 0.15 * math.sin(i * 1.7)) for i in range(n)]
    def s(key, group, label, unit, values):
        return {'key': key, 'group': group, 'label': label, 'unit': unit, 'values': values}
    gib, mib = 1024 ** 3, 1024 ** 2
    out = []
    if obj == 'host':
        out += [s(f'cpu.core{c}', 'cpu', f'Core {c}', 'percent', wave(8 + c * 3, 6, 9 + c)) for c in range(4)]
        out += [s('cpu.total', 'cpu', 'Total', 'percent', wave(14, 8, 11)),
                s('memory.used', 'memory', 'Used', 'bytes', wave(150 * gib, 12 * gib, 30)),
                s('memory.total', 'memory', 'Total', 'bytes', [256 * gib] * n),
                s('network.eno1.rx', 'network', 'eno1 receive', 'bytes_per_second', wave(42 * mib, 30 * mib, 7)),
                s('network.eno1.tx', 'network', 'eno1 transmit', 'bytes_per_second', wave(18 * mib, 12 * mib, 8)),
                s('disk.nvme0n1.read', 'disk', 'nvme0n1 read', 'bytes_per_second', wave(25 * mib, 20 * mib, 5)),
                s('disk.nvme0n1.write', 'disk', 'nvme0n1 write', 'bytes_per_second', wave(9 * mib, 8 * mib, 6)),
                s('load.1', 'load', '1 min', 'load', wave(3.2, 1.1, 10)), s('load.5', 'load', '5 min', 'load', wave(3.0, 0.6, 14)),
                s('load.15', 'load', '15 min', 'load', wave(2.8, 0.3, 20)),
                s('temperature.k10temp.tctl', 'temperature', 'Tctl', 'celsius', wave(58, 6, 12)),
                s('temperature.nvme.composite', 'temperature', 'NVMe composite', 'celsius', wave(41, 3, 15))]
    else:
        out += [s(f'cpu.vcpu{c}', 'cpu', f'vCPU {c}', 'percent', wave(10 + c * 4, 8, 9 + c)) for c in range(4)]
        out += [s('cpu.total', 'cpu', 'Total', 'percent', wave(14, 8, 11)),
                s('memory.used', 'memory', 'Used', 'bytes', wave(5.5 * gib, 0.8 * gib, 30)),
                s('network.rx', 'network', 'Receive', 'bytes_per_second', wave(4 * mib, 3 * mib, 7)),
                s('network.tx', 'network', 'Transmit', 'bytes_per_second', wave(9 * mib, 6 * mib, 8)),
                s('disk.read', 'disk', 'Read', 'bytes_per_second', wave(2 * mib, 2 * mib, 5)),
                s('disk.write', 'disk', 'Write', 'bytes_per_second', wave(1 * mib, 1 * mib, 6))]
    if id_ in ('host-3', 'vm-legacy'):  # a stale host stops producing samples
        for x in out:
            x['values'] = [v if i < n - 80 else None for i, v in enumerate(x['values'])]
    start = (NOW - timedelta(seconds=n * step)).isoformat().replace('+00:00', 'Z')
    return {'object': obj, 'id': id_, 'range': rng, 'step_seconds': step, 'start': start, 'series': out}


def host_network(host):
    br0 = next(n for n in host['inventory']['nics'] if n['name'] == 'br0')
    address = br0['addresses'][0]
    return {'host_id': host['id'], 'hostname': host['hostname'], 'connection': host['connection'], 'supported': True, 'state': 'confirmed',
            'management_network': {'revision': 3, 'stage': 'none', 'spec': {'interfaces': [{'name': 'br0', 'mac': br0['mac'], 'mode': 'static', 'address': address, 'gateway': address.rsplit('.', 1)[0] + '.1', 'management': True}], 'dns': ['192.0.2.2', '192.0.2.3']}, 'observed_at': host['inventory']['observed_at']},
            'nics': host['inventory']['nics'], 'check': {'name': 'management-network', 'status': 'healthy', 'detail': 'default route via br0'}, 'last_operation': None}


def install(page, fx):
    lists = {k: fx[k] for k in ('sites', 'pools', 'hosts', 'vms', 'storage', 'networks', 'media', 'operations', 'audit')}

    def handle(route):
        req = route.request
        if req.method != 'GET':
            return route.continue_()
        url = req.url
        path = url.split('/api/v1/', 1)[1].split('?', 1)[0]
        query = dict(p.split('=', 1) for p in url.split('?', 1)[1].split('&')) if '?' in url else {}
        def send(body):
            route.fulfill(status=200, content_type='application/json', body=json.dumps(body))
        if path in lists:
            return send(lists[path])
        if path == 'updates':
            return send(fx['updates'])
        if path == 'storage/credentials':
            return send([])
        if path == 'storage/cluster':
            return send({'status': 'succeeded', 'pool_id': query.get('pool_id'), 'observed_at': NOW.isoformat(), 'operations': []})
        m = re.fullmatch(r'hosts/([^/]+)/hardware', path)
        if m:
            return send({**fx['hardware'], 'host_id': m[1]})
        m = re.fullmatch(r'hosts/([^/]+)/network', path)
        if m:
            host = next((h for h in fx['hosts'] if h['id'] == m[1]), None)
            return send(host_network(host)) if host else route.continue_()
        m = re.fullmatch(r'(hosts|vms)/([^/]+)/stats', path)
        if m:
            return send(series('host' if m[1] == 'hosts' else 'vm', m[2], query.get('range', '10m')))
        m = re.fullmatch(r'pools/([^/]+)/members', path)
        if m:
            return send(fx['members'] if m[1] == 'pool-a' else {**fx['members'], 'pool_id': 'pool-b', 'pool_name': 'edge-node-01', 'master_host_id': 'host-4', 'generation': 5, 'members': fx['members']['members'][:1]})
        m = re.fullmatch(r'pools/([^/]+)/join-candidates', path)
        if m:
            return send(fx['join_candidates'])
        return route.continue_()

    page.route(re.compile(r'/api/v1/'), handle)


VIEWS = [  # (name, route, tab or action, viewport)
    ('workspace-overview', '/', None, 'desktop'), ('site-overview', '/sites/site-ams', None, 'desktop'),
    ('pool-general', '/pools/pool-a', None, 'desktop'), ('pool-hosts', '/pools/pool-a', 'Hosts', 'tall'),
    ('pool-virtual-machines', '/pools/pool-a', 'Virtual machines', 'desktop'), ('pool-storage', '/pools/pool-a', 'Storage', 'tall'),
    ('pool-networking', '/pools/pool-a', 'Networking', 'desktop'), ('pool-iso-library', '/pools/pool-a', 'ISO library', 'desktop'),
    ('pool-activity', '/pools/pool-a', 'Activity', 'desktop'),
    ('dialog-add-storage', '/pools/pool-a', 'Storage>Register storage', 'tall'), ('dialog-add-network', '/pools/pool-a', 'Networking>Register network', 'tall'),
    ('dialog-iso-upload', '/pools/pool-a', 'ISO library>Upload ISO', 'tall'), ('dialog-new-vm', '/pools/pool-a', '>New VM', 'tall'), ('dialog-add-host', '/pools/pool-a', '>Add host', 'tall'),
    ('host-general', '/hosts/host-1', None, 'tall'), ('host-stats', '/hosts/host-1', 'Stats', 'tall'), ('host-console', '/hosts/host-1', 'Console', 'desktop'),
    ('host-networking', '/hosts/host-1', 'Networking', 'tall'), ('host-storage', '/hosts/host-1', 'Storage', 'desktop'), ('host-activity', '/hosts/host-1', 'Activity', 'desktop'),
    ('host-stale', '/hosts/host-3', None, 'desktop'), ('host-degraded', '/hosts/host-2', None, 'tall'),
    ('vm-general', '/vms/vm-web', None, 'tall'), ('vm-stats', '/vms/vm-web', 'Stats', 'tall'), ('vm-console', '/vms/vm-web', 'Console', 'tall'),
    ('vm-activity', '/vms/vm-web', 'Activity', 'desktop'), ('vm-edit', '/vms/vm-web', '>Edit', 'tall'), ('dialog-power-confirm', '/vms/vm-web', '>Force stop', 'desktop'),
    ('vm-shut-off', '/vms/vm-build', None, 'desktop'), ('vm-stale', '/vms/vm-legacy', None, 'desktop'), ('vm-windows-console', '/vms/vm-win', 'Console', 'desktop'),
    ('task-history', '/tasks', None, 'tall'), ('updates', '/updates', None, 'desktop'), ('invalid-object', '/vms/does-not-exist', None, 'desktop'),
    ('narrow-overview', '/', None, 'narrow'), ('narrow-pool', '/pools/pool-a', None, 'narrow'), ('narrow-vm', '/vms/vm-web', None, 'narrow'),
    ('mid-host', '/hosts/host-1', None, 'mid'),
]
VIEWPORTS = {'desktop': (1440, 900), 'tall': (1440, 2000), 'mid': (1024, 768), 'narrow': (390, 844)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--url', required=True)
    ap.add_argument('--username', default='root')
    ap.add_argument('--password-file', required=True)
    ap.add_argument('--captures', required=True)
    ap.add_argument('--views', default='all')
    args = ap.parse_args()
    out = Path(args.captures); out.mkdir(parents=True, exist_ok=True)
    fx = resolve(FIXTURE, FIXTURE['hosts'])
    password = Path(args.password_file).read_text().strip()
    with sync_playwright() as pw:
        browser = pw.chromium.launch()
        ctx = browser.new_context(ignore_https_errors=True, viewport={'width': 1440, 'height': 900})
        page = ctx.new_page()
        page.set_default_timeout(8000)
        errors = []
        page.on('pageerror', lambda e: errors.append(str(e)))
        page.goto(args.url + '/')
        page.screenshot(path=str(out / '00-login.png'))
        page.get_by_label(re.compile('username', re.I)).fill(args.username)
        page.get_by_label(re.compile('password', re.I)).fill(password)
        page.get_by_role('button', name=re.compile('sign in', re.I)).click()
        page.wait_for_timeout(1200)
        page.screenshot(path=str(out / '01-first-run.png'))
        install(page, fx)
        for n, (name, route, action, vp) in enumerate(VIEWS, start=2):
            if args.views != 'all' and not name.startswith(args.views):
                continue
            w, h = VIEWPORTS[vp]
            page.set_viewport_size({'width': w, 'height': h})
            page.goto(args.url + '/#' + route); page.reload(); page.wait_for_timeout(900)
            if action:
                tab, _, button = action.partition('>')
                if tab:
                    page.get_by_role('navigation', name='Resource views').get_by_role('button', name=tab, exact=True).click()
                if button:
                    page.get_by_role('button', name=button, exact=True).first.click()
                page.wait_for_timeout(1500 if tab == 'Stats' else 700)
            page.screenshot(path=str(out / f'{n:02d}-{name}.png'))
            print('captured', name)
        browser.close()
    if errors:
        print('page errors:'); print('\n'.join(errors))
        raise SystemExit(1)


if __name__ == '__main__':
    main()
```

- [ ] **Step 3: Run it against a disposable local Controller and inspect the baseline**

Build and start a Controller from the working tree as `docs/development.md` describes (`make binaries`, `make ui`, a self-signed certificate, an administrator password file, a private data directory, any free loopback port; the `-source-archive` flag only needs an existing file). Then:

```sh
python3 tests/integration/ui_review_browser.py --url https://127.0.0.1:PORT --password-file /path/to/admin-password --captures /tmp/ui-before
```

Expected: 40 PNG files, `captured …` for each view, no `page errors` line, exit 0. Open a dozen and confirm every tab shows fixture data rather than an empty state. Keep `/tmp/ui-before` for Task 13; it is not committed.

- [ ] **Step 4: Run the repository checks**

```sh
python3 -m unittest discover -s tests -p 'test_repository_privacy.py'
python3 tests/test_managed_vm_browser_modes.py
```

Expected: both pass (the fixture uses only fictional identifiers).

- [ ] **Step 5: Hand off**

Leave the two new files unstaged. Suggested commit: `tests: add labeled-fixture capture harness for Controller UI review`.

---

### Task 2: Token sheet, gradient removal, base typography

**Files:**
- Create: `web/src/tokens.css`
- Create: `web/src/tokens.test.mjs`
- Modify: `web/src/style.css` (`:root` block lines 2–14; every literal colour; every `linear-gradient`; `border-radius` values; font stacks at lines 3 and 50)
- Modify: `web/src/stats.css` (`.stats` custom properties lines 3–15, every literal colour, `.stats-id` font at line 68)
- Modify: `web/src/console.css` (every literal colour; `.console-page` font at line 52)
- Modify: `web/src/main.tsx:10` (import order: `tokens.css` before `style.css`)
- Modify: `web/public/index.html:7` (`theme-color` stays `#F6F7FC`; confirm it matches `--canvas`)

**Interfaces:**
- Produces: the token names in the Design system table above. Every later task styles through them and adds no literal colour to the three stylesheets.
- Removes: `--blue` (use `--accent`) and `--surface` (use `--panel`).

- [ ] **Step 1: Write the failing test**

`web/src/tokens.test.mjs`:

```js
// SPDX-License-Identifier: AGPL-3.0-only
// The UI's colours live in tokens.css alone, no chrome surface is a gradient, and every
// text/background token pair meets WCAG AA. run with: node src/tokens.test.mjs
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const read = (f) => readFileSync(new URL(`./${f}`, import.meta.url), "utf8");
const tokens = read("tokens.css");
for (const file of ["style.css", "stats.css", "console.css"]) {
  const css = read(file);
  assert.equal((css.match(/linear-gradient/g) || []).length, 0, `${file}: no gradients`);
  const literals = css.match(/#[0-9a-f]{3,8}\b/gi) || [];
  assert.deepEqual(literals, [], `${file}: colours come from tokens.css; found ${literals.join(" ")}`);
}

const [light, dark = ""] = tokens.split("@media (prefers-color-scheme: dark)");
const hex = (block, name) => {
  const m = block.match(new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})`));
  assert.ok(m, `token --${name} is defined`);
  return m[1];
};
const luminance = (h) => {
  const c = [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
const ratio = (a, b) => { const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
const pairs = [["text", "canvas"], ["text", "panel"], ["text", "sunken"], ["muted", "canvas"], ["muted", "panel"], ["muted", "sunken"], ["accent", "panel"], ["accent", "canvas"], ["on-accent", "accent"], ["on-topbar", "topbar"], ["on-danger", "danger"], ["ok", "ok-bg"], ["warn", "warn-bg"], ["bad", "bad-bg"], ["info", "info-bg"], ["neutral", "neutral-bg"]];
for (const [fg, bg] of pairs) {
  assert.ok(ratio(hex(light, fg), hex(light, bg)) >= 4.5, `light ${fg} on ${bg}`);
  if (dark) assert.ok(ratio(hex(dark, fg), hex(dark, bg)) >= 4.5, `dark ${fg} on ${bg}`);
}
console.log("tokens ok");
```

- [ ] **Step 2: Run it to verify it fails**

Run: `node web/src/tokens.test.mjs`
Expected: FAIL, `ENOENT … tokens.css`.

- [ ] **Step 3: Write `web/src/tokens.css`**

```css
/* SPDX-License-Identifier: AGPL-3.0-only */
/* Every colour, radius, shadow and font the Controller UI uses. style.css, stats.css and
   console.css reference these and never a literal colour (tokens.test.mjs enforces it).
   Values are the brand's primary palette (docs/brand/README.md) plus derived status pairs,
   each checked at 4.5:1 or better. */
:root {
  color-scheme: light;
  --canvas: #F6F7FC;
  --panel: #FFFFFF;
  --sunken: #E5E8F3;
  --line: #C6CEE4;
  --line-soft: #DDE2EE;
  --text: #17233B;
  --muted: #5B6478;
  --accent: #2456D9;
  --accent-hover: #3478FF;
  --on-accent: #FFFFFF;
  --select: #E4ECFF;
  --topbar: #17233B;
  --on-topbar: #E5E8F3;
  --focus: #2456D9;
  --danger: #B42318;
  --on-danger: #FFFFFF;
  --ok: #176B42; --ok-bg: #DDF5E6;
  --warn: #8A4B00; --warn-bg: #FFF1D6;
  --bad: #B42318; --bad-bg: #FDE2E1;
  --info: #2456D9; --info-bg: #E4ECFF;
  --neutral: #5B6478; --neutral-bg: #E5E8F3;
  --console-bg: #000000;
  --series-1: #2456D9; --series-2: #0E7490; --series-3: #7C3AED; --series-4: #0F8A5F;
  --series-5: #9A6700; --series-6: #B42318; --series-7: #5B6478; --series-8: #3478FF;
  --radius-control: 4px;
  --radius-panel: 8px;
  --shadow-pop: 0 8px 24px rgba(23, 35, 59, .16), 0 1px 2px rgba(23, 35, 59, .08);
  --font: system-ui, -apple-system, "Segoe UI", sans-serif;
  --font-mono: ui-monospace, "SFMono-Regular", Menlo, Consolas, monospace;
}
```

(The dark block is Task 12.)

- [ ] **Step 4: Rewrite the base of `style.css`**

Replace lines 1–14 with:

```css
/* SPDX-License-Identifier: AGPL-3.0-only */
:root { font: 14px/1.45 var(--font); color: var(--text); background: var(--canvas); font-synthesis: none; }
```

Then, through the whole file: replace every `var(--blue)` with `var(--accent)` and `var(--surface)` with `var(--panel)`; replace every `linear-gradient(...)` background with the flat token the surface maps to (`.topbar` → `var(--topbar)`; `.toolbar`, `.section-heading`, `th`, `.page-heading`, `.nav-title`, `.statusbar`, `.detail-tabs button`, `.activity-dock > summary`, `.pool-row-actions > summary`, `button` → `var(--panel)` or `var(--sunken)` per the Design system); replace every literal colour with the nearest token (`#202020`/`#242424`/`#262626` → `var(--text)`; greys used as borders → `var(--line)` or `var(--line-soft)`; `#d4e3ff`/`#dbe7ff`/`#e8f0ff`/`#eef4ff`/`#e3edff` → `var(--select)`; `#10306b`/`#1d4ed8`/`#3560b8` → `var(--accent)`; `#b91c1c` → `var(--danger)`; badge and notice colours → the status pairs); `border-radius: 1px|2px|3px` → `var(--radius-control)`, `6px` → `var(--radius-panel)`; `Consolas, monospace` → `var(--font-mono)`. Set `button`, `input`, `select`, `textarea` to `min-height: 32px; border: 1px solid var(--line); border-radius: var(--radius-control); background: var(--panel); color: var(--text)`; `button:hover` → `background: var(--select); border-color: var(--accent)`; `button.primary` → `background: var(--accent); color: var(--on-accent); border-color: var(--accent)`; `button.danger` → `color: var(--danger); border-color: var(--danger)`; `:focus-visible` → `outline: 2px solid var(--focus); outline-offset: 2px`.

The same substitution rules apply to `stats.css` (delete the `.stats` custom-property block and use `--line-soft`, `--muted`, `--text`, `--series-N`, `--ok`, `--warn`, `--bad`, `--warn-bg`; `.stats-id` uses `var(--font-mono)`) and `console.css` (`.console-screen` background `var(--console-bg)`, `.console-status` → `var(--warn-bg)`/`var(--warn)`, `.console-page` font `var(--font)`, `.console-toolbar-toggle:focus-visible` outline `var(--focus)`).

- [ ] **Step 5: Import order and font sizes**

In `web/src/main.tsx` line 10 insert `import "./tokens.css";` before `import "./style.css";`. In `style.css` raise: `table` and `.details` from 12 px to 13 px; `h1` to 20 px; `h2` to 15 px; `.breadcrumbs`, `.statusbar`, `small`, `.panel-note` to 12 px; keep `.badge` at 11 px. Delete `.metric .tiny-label { font-size: 10px }` in the 700 px block (11 px minimum).

- [ ] **Step 6: Run the checks**

```sh
node web/src/tokens.test.mjs
npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build
```

Expected: `tokens ok`; typecheck, all `src/*.test.mjs` and the build pass.

- [ ] **Step 7: Look**

Start the Controller with the rebuilt `web/dist` and run `python3 tests/integration/ui_review_browser.py --views pool --captures /tmp/ui-task2 …`. Expected: no gradient anywhere, porcelain canvas, white panels, one border tone, badges still square. Fix anything that lost contrast before moving on.

- [ ] **Step 8: Hand off**

Suggested commit: `ui: introduce design tokens, remove gradients and literal colours`.

---

### Task 3: Badge and Banner components

**Files:**
- Create: `web/src/ui.tsx`
- Create: `web/src/ui.test.mjs`
- Modify: `web/src/main.tsx:178-180` (delete the local `Badge`; import from `./ui`)
- Modify: `web/src/managed.tsx:35` (delete `status`; import `Badge` and use `<Badge value={…} />`)
- Modify: every `className={\`badge …\`}` / `className="badge …"` in `web/src/*.tsx` (find them with `grep -n 'className={`badge\|className="badge' web/src/*.tsx`) to render `<Badge value={…} label={…} />`
- Modify: `web/src/style.css` `.badge` rules (currently the block starting `.badge { display: inline-flex …` and the four colour rules that follow) and `.alert, .notice, .warning`

**Interfaces:**
- Consumes: `statusLabel` from Task 4 (until Task 4 lands, `Badge` renders `value.replaceAll("-", " ").replaceAll("_", " ")`; Task 4 swaps in `statusLabel`).
- Produces:

```ts
export type Tone = "ok" | "warn" | "bad" | "info" | "neutral";
export function badgeTone(value: string): Tone;
export function Badge(props: { value: string; label?: string }): JSX.Element;   // square, dot + text, class "badge <tone>"
export function Banner(props: { tone: "info" | "warn" | "bad"; role?: "alert" | "status"; children: ReactNode }): JSX.Element;  // class "banner <tone>"
```

- [ ] **Step 1: Write the failing test**

`web/src/ui.test.mjs`:

```js
// SPDX-License-Identifier: AGPL-3.0-only
// One status vocabulary: every value the Controller and Core report maps to one of five tones,
// and the square dot-and-text badge is the only badge. run with: node src/ui.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";

const { badgeTone, Badge, Banner } = await import(bundleTestModule(new URL("./ui.tsx", import.meta.url), "ui"));

for (const v of ["fresh", "healthy", "succeeded", "current", "available", "confirmed", "connected", "ready", "in_sync", "passed", "verified", "up"]) assert.equal(badgeTone(v), "ok", v);
for (const v of ["stale", "degraded", "pending", "dispatched", "needs_attention", "behind", "unverified", "uncertain", "warning", "disconnected", "unresponsive", "timeout", "awaiting_fencing", "staged", "applied"]) assert.equal(badgeTone(v), "warn", v);
for (const v of ["failed", "error", "revoked", "blocked", "down", "faulted", "rejected"]) assert.equal(badgeTone(v), "bad", v);
for (const v of ["queued", "uploading", "master", "Pool master", "provisioned"]) assert.equal(badgeTone(v), "info", v);
for (const v of ["unknown", "", "cancelled", "member", "not reported", "something-new"]) assert.equal(badgeTone(v), "neutral", v);

const html = renderToStaticMarkup(createElement(Badge, { value: "needs_attention" }));
assert.match(html, /class="badge warn"/);
assert.match(html, />Needs attention</, "the badge text is the operator label, never the raw enum");
assert.match(renderToStaticMarkup(createElement(Badge, { value: "master", label: "Pool master" })), />Pool master</);
assert.match(renderToStaticMarkup(createElement(Banner, { tone: "warn", role: "status" }, "x")), /class="banner warn" role="status"/);
console.log("ui ok");
```

- [ ] **Step 2: Run it to verify it fails**

Run: `node web/src/ui.test.mjs`
Expected: FAIL, cannot resolve `./ui.tsx`.

- [ ] **Step 3: Write `web/src/ui.tsx`**

```tsx
// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { statusLabel } from "./labels";

export type Tone = "ok" | "warn" | "bad" | "info" | "neutral";
const tones: [Tone, string[]][] = [
  ["ok", ["fresh", "healthy", "ok", "succeeded", "up", "verified", "passed", "current", "available", "confirmed", "connected", "ready", "in_sync", "running", "online", "met"]],
  ["warn", ["stale", "degraded", "timeout", "dispatched", "warning", "pending", "awaiting_fencing", "needs_attention", "unverified", "uncertain", "behind", "disconnected", "unresponsive", "staged", "applied", "unmet", "arming"]],
  ["bad", ["failed", "error", "revoked", "down", "blocked", "faulted", "rejected", "unavailable", "offline", "critical"]],
  ["info", ["queued", "uploading", "master", "pool master", "provisioned", "connecting"]],
];
export function badgeTone(value: string): Tone {
  const key = value.trim().toLowerCase();
  return tones.find(([, values]) => values.includes(key))?.[0] ?? "neutral";
}
// The square dot-and-text badge. The dot is drawn by CSS; the text is always present.
export function Badge({ value, label }: { value: string; label?: string }) {
  return <span className={`badge ${badgeTone(value)}`}>{label ?? statusLabel(value)}</span>;
}
export function Banner({ tone, role, children }: { tone: "info" | "warn" | "bad"; role?: "alert" | "status"; children: ReactNode }) {
  return <p className={`banner ${tone}`} role={role}>{children}</p>;
}
```

Until Task 4 exists, create `web/src/labels.ts` with only `export const statusLabel = (v: string) => v ? v.charAt(0).toUpperCase() + v.slice(1).replaceAll(/[-_]/g, " ") : "Unknown";` Task 4 replaces the file.

- [ ] **Step 4: Replace the CSS**

In `style.css` replace the `.badge` colour rules with:

```css
/* The square status badge: a dot and a word, never colour alone. Shape is deliberate. */
.badge { display: inline-flex; align-items: center; gap: 5px; padding: 1px 6px; font-size: 11px; font-weight: 600; line-height: 1.6; white-space: nowrap; color: var(--neutral); background: var(--neutral-bg); }
.badge::before { content: ""; width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.badge.ok { color: var(--ok); background: var(--ok-bg); }
.badge.warn { color: var(--warn); background: var(--warn-bg); }
.badge.bad { color: var(--bad); background: var(--bad-bg); }
.badge.info { color: var(--info); background: var(--info-bg); }
.banner, .alert, .notice, .warning { margin: 0 0 12px; padding: 9px 12px; border-left: 3px solid var(--info); border-radius: 0 var(--radius-control) var(--radius-control) 0; background: var(--info-bg); color: var(--text); line-height: 1.5; overflow-wrap: anywhere; }
.banner.warn, .warning { border-left-color: var(--warn); background: var(--warn-bg); }
.banner.bad, .alert { border-left-color: var(--bad); background: var(--bad-bg); }
.notice { border-left-color: var(--ok); background: var(--ok-bg); }
```

Delete `.badge.master` and the old value-keyed colour rules (`.badge.fresh, .badge.healthy …`, `.badge.stale …`, `.badge.failed …`, `.badge.queued …`, `.badge.cancelled`).

- [ ] **Step 5: Route every badge through the component**

`grep -n 'className={`badge\|className="badge' web/src/*.tsx` lists each site (in `main.tsx`, `managed.tsx`, `pool-members.tsx`, `pool-promotion.tsx`, `pool-authority.tsx`, `storage.tsx`, `host-network.tsx`, `console.tsx`, `stats.tsx`, `zfs.tsx`, `md.tsx`). Replace each with `<Badge value={theValue} />`, or `<Badge value={theValue} label={theText} />` where the site shows a different text than the value (for example `main.tsx:1784` becomes `<Badge value={master ? "master" : "member"} label={master ? "Pool master" : "Member"} />`). Where a site rendered `role="status"` around a badge, keep the wrapper.

- [ ] **Step 6: Run the checks**

```sh
node web/src/ui.test.mjs && npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build
grep -c 'className={`badge\|className="badge' web/src/*.tsx
```

Expected: `ui ok`; all pass; the grep reports 0 for every file except `ui.tsx`.

- [ ] **Step 7: Hand off**

Suggested commit: `ui: one Badge and one Banner component with five tones`.

---

### Task 4: Operator labels and relative time

**Files:**
- Create: `web/src/labels.ts` (replaces the stub from Task 3)
- Create: `web/src/labels.test.mjs`
- Modify: `web/src/main.tsx` (Tasks table ~1946–1950: `o.kind`; Activity table ~2021–2030: `entry.action`; `date()` helper used by both tables; `HostDetails` health check names; the header `powerStatus` line "Observed running · fresh")
- Modify: `web/src/managed.tsx` (`stateText` callers keep `stateText`; the pool Virtual machines table's "Observed state" cell wraps the text in `<Badge value={v.state} label={stateText(v.state)} />`)
- Modify: `web/src/pool-members.tsx` (member `status`, `replica.state`, check names)

**Interfaces:**
- Produces:

```ts
export function operationLabel(kind: string): string;      // "vm-power" → "VM power"
export function statusLabel(value: string): string;        // "needs_attention" → "Needs attention", "" → "Unknown"
export function checkLabel(name: string): string;          // "pool-storage-reconciliation" → "Pool storage reconciliation"
export function auditLabel(action: string): string;        // "host.connection.stale" → "Host connection stale"
export function relativeTime(iso: string, now?: number): string;  // "3 min ago"; falls back to the input when unparsable
```

- [ ] **Step 1: Write the failing test**

`web/src/labels.test.mjs`:

```js
// SPDX-License-Identifier: AGPL-3.0-only
// Identifiers Core reports are shown to operators as words. run with: node src/labels.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";

const { operationLabel, statusLabel, checkLabel, auditLabel, relativeTime } = await import(bundleTestModule(new URL("./labels.ts", import.meta.url), "labels"));

assert.equal(operationLabel("vm-power"), "VM power");
assert.equal(operationLabel("iso-import"), "Import ISO");
assert.equal(operationLabel("refresh-inventory"), "Refresh inventory");
assert.equal(operationLabel("storage-zfs-provision"), "Provision ZFS storage");
assert.equal(operationLabel("pool-transfer"), "Change pool master");
assert.equal(operationLabel("some-future-kind"), "Some future kind", "a newer Core never shows a blank");

assert.equal(statusLabel("needs_attention"), "Needs attention");
assert.equal(statusLabel("in_sync"), "In sync");
assert.equal(statusLabel("shut off"), "Shut off");
assert.equal(statusLabel("fresh"), "Fresh");
assert.equal(statusLabel(""), "Unknown");

assert.equal(checkLabel("pool-storage-reconciliation"), "Pool storage reconciliation");
assert.equal(checkLabel("kvm"), "KVM");
assert.equal(checkLabel("libvirt"), "libvirt");
assert.equal(checkLabel("management-network"), "Management network");

assert.equal(auditLabel("site.create"), "Site created");
assert.equal(auditLabel("host.revoke"), "Host access revoked");
assert.equal(auditLabel("host.connection.stale"), "Host connection stale");
assert.equal(auditLabel("update.apply"), "Update applied");

const now = Date.parse("2026-09-23T12:00:00Z");
assert.equal(relativeTime("2026-09-23T11:59:50Z", now), "just now");
assert.equal(relativeTime("2026-09-23T11:57:00Z", now), "3 min ago");
assert.equal(relativeTime("2026-09-23T09:00:00Z", now), "3 h ago");
assert.equal(relativeTime("2026-09-20T12:00:00Z", now), "3 d ago");
assert.equal(relativeTime("2026-08-01T12:00:00Z", now), "2026-08-01");
assert.equal(relativeTime("not a date", now), "not a date");
console.log("labels ok");
```

- [ ] **Step 2: Run it to verify it fails**

Run: `node web/src/labels.test.mjs`
Expected: FAIL, `operationLabel is not a function`.

- [ ] **Step 3: Write `web/src/labels.ts`**

```ts
// SPDX-License-Identifier: AGPL-3.0-only
// Operator-facing names for the identifiers Core and the Controller report. Unknown values fall
// back to a readable form of the raw string, so a newer Core never renders as a blank label;
// the raw identifier stays available beside the label where an operator may need to grep it.
const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const humanize = (s: string) => capitalize(s.replaceAll(/[-_.]+/g, " ").trim());

const operations: Record<string, string> = {
  "refresh-inventory": "Refresh inventory", "vm-create": "Create VM", "vm-update": "Edit VM", "vm-delete": "Delete VM",
  "vm-power": "VM power", "vm-start": "Start VM", "start-vm": "Start VM", "vm-media": "Change VM media",
  "iso-import": "Import ISO", "media-remove": "Remove ISO",
  "storage-register": "Register storage", "storage-update": "Update storage", "storage-recheck": "Recheck storage",
  "storage-detach": "Detach storage", "storage-zfs-provision": "Provision ZFS storage", "storage-zfs-replace": "Replace ZFS disk",
  "storage-credential-retire": "Retire storage credential",
  "network-register": "Register network", "network-update": "Update network", "network-remove": "Remove network",
  "management-network": "Change management network", "host-system-disk-replace": "Replace system disk",
  "pool-join": "Join pool", "pool-maintenance": "Maintenance mode", "pool-transfer": "Change pool master",
  "pool-promote": "Promote pool master", "pool-fence-profile": "Fence profile", "pool-host-fencing": "Host fencing",
  "pool-bmc-provisioning": "BMC provisioning", enroll: "Enroll host",
};
export function operationLabel(kind: string): string { return operations[kind] ?? humanize(kind); }

const statuses: Record<string, string> = { needs_attention: "Needs attention", in_sync: "In sync", awaiting_fencing: "Awaiting fencing", will_not_converge: "Will not converge", refuses_join: "Refuses join", no_peer: "No peer", not_applicable: "Not applicable", not_ready: "Not ready" };
export function statusLabel(value: string): string {
  if (!value) return "Unknown";
  return statuses[value] ?? humanize(value);
}

const checks: Record<string, string> = { libvirt: "libvirt", kvm: "KVM", "management-network": "Management network", "pool-storage-reconciliation": "Pool storage reconciliation", "storage-credentials": "Storage credentials", system: "System", web: "Web service", guest: "Guest agent", "zfs-pool": "ZFS pool" };
export function checkLabel(name: string): string { return checks[name] ?? humanize(name); }

const audits: Record<string, string> = {
  "site.create": "Site created", "host.add": "Host added", "host.revoke": "Host access revoked", "console.open": "VM console opened",
  "host.console.open": "Host console opened", "update.apply": "Update applied", "enrollment-token.create": "Enrollment token created",
  "appliance.adopt": "Appliance adopted", "appliance.bootstrap": "Appliance bootstrapped", "media.discard": "ISO discarded",
  "operation.create": "Task requested", "pool.promote": "Pool master promoted", "pool.host-fencing": "Host fencing changed",
  "pool.fence-profile": "Fence profile changed", "pool.bmc-provisioning": "BMC provisioning changed",
};
export function auditLabel(action: string): string { return audits[action] ?? humanize(action); }

// Relative for the last week, an ISO date after that; the absolute value belongs in a title attribute.
export function relativeTime(iso: string, now = Date.now()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return iso;
  const s = Math.round((now - t) / 1000);
  if (s < 45) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  if (s < 7 * 86400) return `${Math.round(s / 86400)} d ago`;
  return new Date(t).toISOString().slice(0, 10);
}
```

- [ ] **Step 4: Apply the labels**

- `main.tsx` Tasks table: `<strong>{operationLabel(o.kind)}</strong>` and keep `<small className="mono">{o.id}</small>`; the Updated cell renders `<time dateTime={timestamp.iso} title={timestamp.value}>{relativeTime(timestamp.iso)}</time>` (extend `taskTimestamp` at line 136 to also return the ISO string it formatted).
- `main.tsx` Activity table: `<strong>{auditLabel(entry.action)}</strong>` and `<td><time dateTime={entry.created_at} title={date(entry.created_at)}>{relativeTime(entry.created_at)}</time></td>`.
- `main.tsx` `HostDetails` health checks: `<strong>{checkLabel(check.name)}</strong>`; keep `check.name` as a `small.mono` line only when it differs from the label.
- `main.tsx` VM header line: `Observed ${stateText(vm.state)} · ${statusLabel(vm.freshness).toLowerCase()}` stays a sentence; the freshness badge in the panel heading uses `<Badge value={vm.freshness} />`.
- `pool-members.tsx`: member status, replica state and check names go through `Badge`/`statusLabel`/`checkLabel`.

- [ ] **Step 5: Run the checks**

```sh
node web/src/labels.test.mjs && npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build
```

Expected: `labels ok`; all pass.

- [ ] **Step 6: Hand off**

Suggested commit: `ui: show operator labels and relative times instead of raw identifiers`.

---

### Task 5: Shell chrome — remove the menubar, header Actions menu, underline tabs

**Files:**
- Modify: `web/src/main.tsx:929-951` (topbar and menubar), `:952-967` (toolbar), `:997-1050` (breadcrumbs and page heading), `:1051-1054` (tabs), `:236-244` (`Menu` gains an optional `align` prop)
- Modify: `web/src/style.css` (`.topbar`, `.menubar`, `.menu`, `.menu-items`, `.toolbar*`, `.breadcrumbs`, `.page-heading`, `.eyebrow`, `.detail-tabs`, `.statusbar`)
- Modify: `tests/integration/phase1_browser.py:79`, `:174`, `:548`, `:666`
- Modify: `docs/guide/manage-controller.md:66-94`, `:162-163`, `:195-197`

**Interfaces:**
- Produces: the header `Actions` menu (a `Menu` with `label="Actions"`) that carries **Refresh inventory**, **Re-enroll host**, **Revoke access** on host pages; the status bar carries **API reference** and **Nex source code**; `.eyebrow` is the breadcrumb.

- [ ] **Step 1: Update the browser lab first**

In `tests/integration/phase1_browser.py`:
- line 79: replace the four-entry loop with a check that, on the workspace page, no element with `aria-label="Application menus"` exists and the status bar has links named **API reference** and **Nex source code**; on a host page, `menu_item('Actions', 'Refresh inventory')` is visible.
- lines 174, 548, 666: `menu_item('Host', …)` → `menu_item('Actions', …)`.

Run `python3 tests/test_managed_vm_browser_modes.py` (parses the browser modules) — expected PASS. The lab itself runs in Task 13.

- [ ] **Step 2: Remove the menubar and move its unique items**

In `main.tsx` delete the `<div className="menubar" …>` block (lines 946–951). In the status bar (search `className="statusbar"`), add `<a href="/api/docs/" target="_blank" rel="noreferrer">API reference</a>` before the existing source link. In the page heading's `.actions` container render, when `host && !vm`:

```tsx
<Menu label="Actions" align="end">
  <button disabled={busy || host.revoked} onClick={refreshInventory}>Refresh inventory</button>
  <button disabled={busy || !host.revoked} onClick={() => open("enroll")}>Re-enroll host</button>
  <button className="danger" disabled={busy || host.revoked} onClick={() => open("revoke")}>Revoke access</button>
</Menu>
```

`Menu` gets `align?: "start" | "end"` and adds class `menu-end` when `end`; `.menu-end .menu-items { left: auto; right: 0; }`.

- [ ] **Step 3: Breadcrumb into the header, underline tabs**

Move the breadcrumb markup (lines 998–1013) inside `.page-heading` as the first child with `className="eyebrow"` (the existing `.eyebrow` paragraph that prints "HOST"/"POOL" is deleted). CSS:

```css
.topbar { min-height: 40px; padding: 4px 14px; background: var(--topbar); color: var(--on-topbar); border-bottom: 0; }
.toolbar { min-height: 40px; padding: 4px 10px; background: var(--panel); border-bottom: 1px solid var(--line); }
.toolbar-group > button, .toolbar > button, .toolbar > a { min-height: 30px; padding: 4px 10px; border-radius: var(--radius-control); font-weight: 500; }
.toolbar-group > button:disabled, .toolbar > button:disabled { opacity: .4; }
.page-heading { min-height: 56px; padding: 8px 16px 0; background: var(--panel); border-bottom: 1px solid var(--line); }
.eyebrow { display: block; margin: 0 0 2px; font-size: 12px; color: var(--muted); }
.page-heading h1 { font-size: 20px; }
.detail-tabs { gap: 20px; padding: 0 16px; background: var(--panel); border-bottom: 1px solid var(--line); }
.detail-tabs button { min-height: 36px; padding: 8px 2px; border: 0; border-bottom: 2px solid transparent; border-radius: 0; background: transparent; color: var(--muted); font-weight: 500; }
.detail-tabs button:hover { color: var(--text); }
.detail-tabs button[aria-current="page"] { color: var(--accent); border-bottom-color: var(--accent); font-weight: 600; }
.menu-items { padding: 4px; border: 1px solid var(--line); border-radius: var(--radius-panel); background: var(--panel); box-shadow: var(--shadow-pop); }
.menu-items button, .menu-items a { border-radius: var(--radius-control); }
.menu-items button:hover, .menu-items a:hover { background: var(--select); }
```

Delete `.menubar`, `.menu > summary` styles that assumed the menubar, and `.breadcrumbs`.

- [ ] **Step 4: Guide**

`docs/guide/manage-controller.md` lines 66–94: describe the top bar, the fixed toolbar, the **Resources** pane and the selected resource's header with its breadcrumb, badges and **Actions** menu; remove the File/View/Host/Help sentences; keep the keyboard paragraph (Tab reaches the Actions menu, Enter opens it, Escape closes it). Lines 162–163 and 195–197: **Host** menu → **Actions** menu in the host header. Line 89–93 mention of **API reference** → the status bar.

- [ ] **Step 5: Run the checks**

```sh
npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build
python3 scripts/check_docs.py --worktree
```

Expected: pass (the docs gate needs the change record from Task 13; until then it reports the missing record for the guide page, which is expected and noted in the hand-off).

- [ ] **Step 6: Look**

`ui_review_browser.py --views host`. Expected: five chrome bands (topbar, toolbar, header, tabs, content), the Actions menu opens on Enter and closes on Escape, tabs are underlined, no menubar.

- [ ] **Step 7: Hand off**

Suggested commit: `ui: drop the menubar; host actions move to the header, tabs become underline tabs`.

---

### Task 6: Task strip and failure notices

**Files:**
- Create: `web/src/dock.ts`, `web/src/dock.test.mjs`
- Modify: `web/src/main.tsx:1646-1649` (the `activity-dock` details), the `Tasks` component heading (~1925–1930), the effect that receives new `data.operations` (inside the reload path around line 629)
- Modify: `web/src/style.css` `.activity-dock*`
- Modify: `docs/guide/vm-power.md:42`, `:53`, `:83`; `docs/guide/vm-console.md:75`; `docs/guide/manage-controller.md:163`

**Interfaces:**
- Produces:

```ts
export function taskCounts(ops: { status: string }[]): { running: number; failed: number };
export function newFailures(previous: { id: string; status: string }[], next: { id: string; status: string }[]): { id: string; status: string }[];
export function readPreference(key: string, fallback: boolean): boolean;   // localStorage, try/catch, "1"/"0"
export function writePreference(key: string, value: boolean): void;
```

- [ ] **Step 1: Write the failing test**

`web/src/dock.test.mjs`:

```js
// SPDX-License-Identifier: AGPL-3.0-only
// The task strip counts what is running and what failed, and a failure that appears between
// polls is announced once. run with: node src/dock.test.mjs
import { bundleTestModule } from "./test-helpers.mjs";
import assert from "node:assert/strict";

const { taskCounts, newFailures, readPreference, writePreference } = await import(bundleTestModule(new URL("./dock.ts", import.meta.url), "dock"));

assert.deepEqual(taskCounts([{ status: "pending" }, { status: "dispatched" }, { status: "failed" }, { status: "needs_attention" }, { status: "succeeded" }]), { running: 2, failed: 2 });
assert.deepEqual(taskCounts([]), { running: 0, failed: 0 });

const before = [{ id: "a", status: "pending" }, { id: "b", status: "failed" }];
assert.deepEqual(newFailures(before, [{ id: "a", status: "failed" }, { id: "b", status: "failed" }]), [{ id: "a", status: "failed" }], "only the transition is announced");
assert.deepEqual(newFailures(before, [{ id: "c", status: "needs_attention" }]), [{ id: "c", status: "needs_attention" }], "a task created and failed between polls is announced");
assert.deepEqual(newFailures([], [{ id: "b", status: "failed" }]), [], "the first load announces nothing");

globalThis.localStorage = undefined;
assert.equal(readPreference("nex.dock", true), true, "no storage: the fallback");
writePreference("nex.dock", false);
const store = new Map();
globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
writePreference("nex.dock", false);
assert.equal(readPreference("nex.dock", true), false);
globalThis.localStorage = { getItem: () => { throw new Error("blocked"); }, setItem: () => { throw new Error("blocked"); } };
assert.equal(readPreference("nex.dock", true), true, "a throwing store is the fallback");
writePreference("nex.dock", true);
console.log("dock ok");
```

- [ ] **Step 2: Run it to verify it fails**

Run: `node web/src/dock.test.mjs` — Expected: FAIL, cannot resolve `./dock.ts`.

- [ ] **Step 3: Write `web/src/dock.ts`**

```ts
// SPDX-License-Identifier: AGPL-3.0-only
// The bottom task strip: counts for its collapsed line, the failures to announce, and the
// per-browser open/closed preference. localStorage is a convenience: any failure to read or
// write it leaves the default in place.
const running = new Set(["pending", "dispatched"]);
const failed = new Set(["failed", "needs_attention"]);

export function taskCounts(ops: { status: string }[]): { running: number; failed: number } {
  return { running: ops.filter(o => running.has(o.status)).length, failed: ops.filter(o => failed.has(o.status)).length };
}

export function newFailures<T extends { id: string; status: string }>(previous: T[], next: T[]): T[] {
  if (!previous.length) return [];
  const before = new Map(previous.map(o => [o.id, o.status]));
  return next.filter(o => failed.has(o.status) && before.get(o.id) !== o.status);
}

export function readPreference(key: string, fallback: boolean): boolean {
  try { const v = localStorage.getItem(key); return v === null ? fallback : v === "1"; } catch { return fallback; }
}
export function writePreference(key: string, value: boolean): void {
  try { localStorage.setItem(key, value ? "1" : "0"); } catch { /* preference not kept */ }
}
```

- [ ] **Step 4: The strip**

In `main.tsx` add state `const [dockOpen, setDockOpen] = useState(() => readPreference("nex.dock", false));` and replace the `activity-dock` block:

```tsx
{!loading && kind !== "tasks" && kind !== "updates" && (() => {
  const scoped = data.operations.filter((o) => scopedHosts.some((h) => h.id === o.host_id));
  const counts = taskCounts(scoped);
  return <details className="activity-dock" open={dockOpen} onToggle={(e) => { setDockOpen(e.currentTarget.open); writePreference("nex.dock", e.currentTarget.open); }}>
    <summary>
      <span>Tasks</span>
      <span className="dock-counts">{counts.running ? <Badge value="pending" label={`${counts.running} running`} /> : null}{counts.failed ? <Badge value="failed" label={`${counts.failed} failed`} /> : null}{!counts.running && !counts.failed && <span className="muted">{scoped.length} recent</span>}</span>
      <a href="#/tasks" onClick={(e) => e.stopPropagation()}>Task history</a>
    </summary>
    <Tasks operations={scoped.slice(0, 5)} hosts={data.hosts} recent upload={upload} onCancelUpload={cancelUpload} />
  </details>;
})()}
```

Where `reload()` stores the fetched operations, keep the previous list in a ref and call `setNotice` for each of `newFailures(previousRef.current, operations)`: `${operationLabel(o.kind)} on ${hostname} ${o.status === "failed" ? "failed" : "needs attention"}. ${o.error ?? ""}` with the operation ID so the existing notice bar links to it.

CSS:

```css
.activity-dock { flex-shrink: 0; border-top: 1px solid var(--line); background: var(--panel); }
.activity-dock > summary { display: flex; align-items: center; gap: 12px; min-height: 32px; padding: 4px 16px; font-size: 13px; font-weight: 600; cursor: pointer; list-style: none; }
.activity-dock > summary::-webkit-details-marker { display: none; }
.activity-dock > summary::before { content: ""; width: 0; height: 0; border-left: 4px solid transparent; border-right: 4px solid transparent; border-top: 5px solid var(--muted); }
.activity-dock[open] > summary::before { transform: rotate(180deg); }
.dock-counts { display: flex; gap: 6px; }
.activity-dock > summary > a { margin-left: auto; font-weight: 400; font-size: 12px; }
.activity-dock > .panel { margin: 0; border: 0; border-top: 1px solid var(--line-soft); max-height: 220px; overflow: auto; }
```

- [ ] **Step 5: Guide**

Where the guide says "under **Recent tasks**" (`vm-power.md` 42, 53, 83; `vm-console.md` 75; `manage-controller.md` 163) say "in the **Tasks** strip at the bottom of the page (select it to expand; it shows the last five tasks and a count of running and failed ones)". Add one sentence to `manage-controller.md` after line 163: "A task that fails between refreshes is also announced in the notice bar with a link to it."

- [ ] **Step 6: Run the checks**

`node web/src/dock.test.mjs && npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build` — Expected: `dock ok`; all pass.

- [ ] **Step 7: Look**

`ui_review_browser.py --views pool`. Expected: the strip is 32 px closed, opens on click and on Enter, remembers its state after a reload, the notice bar appears when the fixture's failed task is first seen after a poll (temporarily edit the fixture's `op-power-1` status to `pending`, capture, restore it to `failed`, refresh: the notice appears).

- [ ] **Step 8: Hand off**

Suggested commit: `ui: collapse the task drawer to a strip with counts; announce new failures`.

---

### Task 7: Resource tree, off-canvas under 900 px, icon-only toolbar

**Files:**
- Modify: `web/src/main.tsx:968-995` (aside and tree), `:952-967` (toolbar labels wrapped in `<span className="toolbar-label">`), header (a **Resources** toggle button rendered before the eyebrow under 900 px)
- Modify: `web/src/managed.tsx:782` (`VMPower` buttons wrap their text in `<span className="toolbar-label">`)
- Modify: `web/src/style.css` (`aside`, `.nav-title`, `aside nav a`, `.tree*`, `.connection-dot`, `.sidebar-foot`, the `@media (max-width: 700px)` block, the `@media (max-width: 1050px)` block)

**Interfaces:**
- Produces: `body.tree-open` while the off-canvas tree is shown; `.toolbar-label` for every toolbar button text.

- [ ] **Step 1: Tree markup**

In the host row (line 987) render the connection as `<span className={`connection-dot ${h.connection}`} aria-hidden="true" />` followed by `{h.connection !== "fresh" && <Badge value={h.connection} />}`; keep the `aria-label` on the row link instead: `aria-label={`${h.hostname}, connection ${statusLabel(h.connection)}`}`. Add a `useState` `treeOpen` (default from `readPreference("nex.tree", false)` only below 900 px; always false above), a header button:

```tsx
<button type="button" className="tree-toggle" aria-expanded={treeOpen} aria-controls="resources" onClick={() => setTreeOpen(o => !o)}>Resources</button>
```

`aside` gets `id="resources"`; `document.body.classList.toggle("tree-open", treeOpen)` in an effect; close on `hashchange` and on Escape; focus moves to the resource search input on open and back to the toggle on close.

- [ ] **Step 2: CSS**

```css
.shell { grid-template-columns: 248px minmax(0, 1fr); }
aside { border-right: 1px solid var(--line); background: var(--panel); }
.nav-title { padding: 8px 12px; font-size: 12px; font-weight: 600; letter-spacing: .04em; text-transform: uppercase; color: var(--muted); background: var(--panel); border-bottom: 1px solid var(--line-soft); }
aside nav a { min-height: 28px; padding: 4px 8px; gap: 8px; border: 0; border-left: 2px solid transparent; border-radius: 0; color: var(--text); }
aside nav a:hover { background: var(--select); }
aside nav a.selected { background: var(--select); border-left-color: var(--accent); color: var(--accent); font-weight: 600; }
.tree { margin-left: 12px; padding-left: 6px; border-left: 1px solid var(--line-soft); }
.tree .host-link { font-size: 13px; }
.connection-dot { width: 8px; height: 8px; margin-left: auto; background: var(--neutral); box-shadow: none; }
.connection-dot.fresh { background: var(--ok); } .connection-dot.stale { background: var(--warn); } .connection-dot.revoked { background: var(--bad); }
.sidebar-foot { border-top: 1px solid var(--line-soft); }
.sidebar-foot a.selected { background: var(--select); color: var(--accent); }
.tree-toggle { display: none; }
@media (max-width: 900px) {
  .shell { grid-template-columns: minmax(0, 1fr); }
  .tree-toggle { display: inline-flex; margin-bottom: 6px; }
  aside { position: fixed; inset: 0 auto 0 0; z-index: 30; width: min(320px, 85vw); transform: translateX(-100%); transition: transform .18s ease-out; box-shadow: var(--shadow-pop); }
  body.tree-open aside { transform: none; }
  .toolbar-label { position: absolute; width: 1px; height: 1px; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); }
  .toolbar-group > button, .toolbar > button { padding: 4px 8px; }
}
@media (prefers-reduced-motion: reduce) { aside { transition: none; } }
```

Delete the 700 px rules that stacked the tree above the content (`aside { max-height: 190px … }`, `.shell { display: block }`, `.sidebar-foot { display: none }`) and the `aside { max-height: 300px }` at the end of the file. Keep `.desktop-app { height: auto }` under 700 px.

- [ ] **Step 3: Run the checks and look**

`npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build`, then `ui_review_browser.py --views narrow` and `--views mid`. Expected at 390 px: the toolbar is one row of icons, content starts under the header, the tree opens over the content and closes on Escape; at 1024 px the tree is inline and content has about 560 px. Confirm `document.documentElement.scrollWidth <= window.innerWidth` on the narrow pool page from the browser console.

- [ ] **Step 4: Hand off**

Suggested commit: `ui: restyle the resource tree; off-canvas tree and icon-only toolbar under 900px`.

---

### Task 8: Tables, panels, pool Hosts table

**Files:**
- Modify: `web/src/style.css` (`.panel`, `.section-heading`, `table`, `th`, `td`, `tbody tr:hover`, `.table-scroll`, `.details`, `.check`, `.metrics`, `.metric`, `.pool-members-table*`, `.pool-fencing-cell`, `.pool-row-actions`)
- Modify: `web/src/pool-members.tsx` (fencing cell, authority timestamps, the per-row fencing sentence)
- Modify: `web/src/managed.tsx:489` (Virtual machines table: state badge; the **Create VM** button moves into the panel's `.section-heading`)
- Modify: `web/src/main.tsx` Hosts table "Last report" cell → `relativeTime` with `title`

- [ ] **Step 1: CSS**

```css
.panel { border: 1px solid var(--line); border-radius: var(--radius-panel); background: var(--panel); overflow: hidden; }
.section-heading { padding: 8px 14px; background: var(--panel); border-bottom: 1px solid var(--line-soft); }
.section-heading h2 { font-size: 15px; }
table { font-size: 13px; }
th { position: sticky; top: 0; z-index: 1; padding: 7px 12px; background: var(--sunken); color: var(--muted); font-size: 12px; font-weight: 600; border-right: 0; border-bottom: 1px solid var(--line); }
td { padding: 8px 12px; border-bottom: 1px solid var(--line-soft); }
td.num, th.num { text-align: right; font-variant-numeric: tabular-nums; }
tbody tr:hover { background: var(--select); }
.details { font-size: 13px; }
.details dt, .details dd { border-bottom: 1px solid var(--line-soft); }
.metrics { gap: 24px; padding: 10px 0 12px; border-bottom: 1px solid var(--line-soft); }
.metric { padding: 0; border: 0; }
.metric strong { font-size: 20px; color: var(--text); }
.pool-members-table .pool-actions-cell { position: sticky; right: 0; background: var(--panel); box-shadow: -8px 0 8px -8px rgba(23, 35, 59, .2); }
.pool-row-actions > summary { border: 1px solid var(--line); border-radius: var(--radius-control); background: var(--panel); }
.pool-row-actions > div { border-radius: var(--radius-panel); box-shadow: var(--shadow-pop); background: var(--panel); border-color: var(--line); }
```

Remove `td:last-child { color: var(--muted) }` and instead add `className="muted"` on the cells that were meant to be muted (Updated/Recorded/Last report), so the sticky actions cell is not greyed.

- [ ] **Step 2: Pool Hosts table**

In `pool-members.tsx`: the Fencing cell renders `<Badge value={fenceState} />` plus a `<details className="fold"><summary>Why</summary><p>{explanation}</p></details>`; when every member's fence report is missing (the "not reported" case), render the explanation once as `<Banner tone="info">` above the table and only the badge in each row. "Received … · Acknowledged …" timestamps use `<time title={iso}>{relativeTime(iso)}</time>`. Add `className="num"` to numeric cells.

- [ ] **Step 3: Run the checks and look**

`npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build`, then `ui_review_browser.py --views pool`. Expected: the pool Hosts table's Actions column stays visible at 1440 px, one fencing notice above the table, relative times, sticky header while the pane scrolls.

- [ ] **Step 4: Hand off**

Suggested commit: `ui: flat panels and tables; pool Hosts table keeps its actions on screen`.

---

### Task 9: Forms, dialogs, editor, sign-in

**Files:**
- Modify: `web/src/style.css` (`label`, `fieldset`, `legend`, `input`, `select`, `textarea`, `::picker(select)`, `.form-panel*`, `.confirm-dialog*`, `.vm-editor`, `.vm-wizard`, `.steps`, `.login*`, `.iso-drop`, `.boot-order`)
- Modify: `web/src/main.tsx` (VM wizard `.steps` list rendered above the wizard form when it is not already; confirm dialog buttons `className="danger"` keep their class)

- [ ] **Step 1: CSS**

```css
label { gap: 4px; font-size: 13px; font-weight: 600; }
input, select, textarea { min-height: 32px; padding: 5px 10px; border: 1px solid var(--line); border-radius: var(--radius-control); background: var(--panel); color: var(--text); }
input::placeholder { color: var(--muted); }
fieldset { margin: 0; padding: 12px 14px 14px; border: 1px solid var(--line-soft); border-radius: var(--radius-panel); }
legend { padding: 0 6px; font-size: 13px; font-weight: 600; color: var(--muted); }
.form-panel { padding: 0 16px 16px; border-color: var(--line); border-top: 3px solid var(--accent); background: var(--panel); }
.form-panel > .section-heading { margin: 0 -16px 14px; padding: 8px 16px; background: var(--sunken); }
.form-panel form { max-width: 560px; }
.steps { gap: 4px 20px; padding: 10px 16px; background: var(--sunken); border-bottom: 1px solid var(--line-soft); }
.steps li[aria-current="step"] { color: var(--accent); }
.confirm-dialog { border: 1px solid var(--line); border-radius: var(--radius-panel); box-shadow: var(--shadow-pop); background: var(--panel); color: var(--text); padding: 18px 20px; }
.confirm-dialog::backdrop { background: rgba(23, 35, 59, .45); }
.confirm-dialog button.danger { background: var(--danger); color: var(--on-danger); border-color: var(--danger); }
.vm-editor > .actions { position: sticky; bottom: 0; margin: 12px -10px -12px; padding: 10px; background: var(--panel); border-top: 1px solid var(--line-soft); }
.login { background: var(--canvas); }
.login-card { border: 1px solid var(--line); border-radius: var(--radius-panel); box-shadow: none; }
.login > .brand { padding: 10px 14px; border-radius: var(--radius-panel); background: #F6F7FC; }
.iso-drop { border: 1px dashed var(--line); border-radius: var(--radius-panel); background: var(--canvas); }
::picker(select) { border: 1px solid var(--line); border-radius: var(--radius-panel); box-shadow: var(--shadow-pop); background: var(--panel); color: var(--text); }
select option:hover, select option:focus-visible { background: var(--select); }
```

The one literal in `.login > .brand` is the raster lockup's own porcelain, kept so the sprite reads on the dark canvas in Task 12; `tokens.test.mjs` allows it by naming it `--brand-plate: #F6F7FC` in `tokens.css` and using `var(--brand-plate)` here.

On the VM details panel, separate **Edit** and **Delete VM**: `.actions > .danger { margin-left: 12px; }`.

- [ ] **Step 2: Run the checks and look**

`npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build`, then `ui_review_browser.py --views dialog` and `--views vm`. Expected: fieldsets read as grouped cards, the wizard shows its step strip, the confirm dialog has a filled red confirm button and Cancel first in DOM order (unchanged), the editor's Save bar stays visible while scrolling, the sign-in card sits on porcelain.

- [ ] **Step 3: Hand off**

Suggested commit: `ui: restyle forms, dialogs, the VM editor and sign-in on the token sheet`.

---

### Task 10: Stats, console, Updates

**Files:**
- Modify: `web/src/stats.css` (series colours are `--series-N`; gauge bar colours `--accent`/`--warn`/`--bad`; `.stats-chart`, `.stats-gauge`, `.stats-hw-section` radius `--radius-panel`)
- Modify: `web/src/console.tsx` (the two status paragraphs become one `Banner tone="warn"` with both sentences; a **Focus mode** toggle button next to **Full screen**)
- Modify: `web/src/console.css` (`.console-toolbar` groups: `.console-keys`, `.console-media`, `.console-view` with a `gap: 16px` and a 1 px `--line-soft` separator; `body.console-focus .topbar, body.console-focus .toolbar, body.console-focus aside, body.console-focus .activity-dock, body.console-focus .statusbar, body.console-focus .detail-tabs, body.console-focus .page-heading { display: none; }`)
- Modify: `web/src/main.tsx` Updates page: a `Badge` from `versionLight(\`Nex Controller ${update.installed_version}\`, update.available_version)` beside the version, the archive URL as text, "Last checked <time>" from a `checkedAt` state set when `updates` is fetched, and the existing **Check**/**Update** buttons
- Modify: `docs/guide/vm-console.md` (add **Focus mode** after line 44: hides the Controller chrome around the console; Escape or the button restores it)

- [ ] **Step 1: Console focus mode**

In `console.tsx` add `const [focus, setFocus] = useState(false);` with an effect that toggles `document.body.classList.toggle("console-focus", focus)` and removes it on unmount; Escape while focused (`keydown` on `document`, only when the console screen does not have keyboard focus so guest keys are not intercepted) sets it false; the button reads **Focus mode** / **Leave focus mode** with `aria-pressed`.

- [ ] **Step 2: Run the checks and look**

`npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build`, then `ui_review_browser.py --views host` (Stats) and `--views vm` (Console). Expected: chart series use the brand series colours, the console toolbar reads as three groups with one status banner, Focus mode leaves only the console panel on screen, Updates shows the version light and the last check time.

- [ ] **Step 3: Hand off**

Suggested commit: `ui: token colours for charts, grouped console toolbar with focus mode, fuller Updates page`.

---

### Task 11: Icons

**Files:**
- Modify: `web/src/icons.tsx`
- Modify: `web/src/main.tsx` (`Icon kind="home"` in the topbar stays; the workspace and updates page headings use `kind="workspace"` and `kind="updates"`)

The brand guide says the n tile does not indicate navigation or state. The toolbar keeps its coloured resource icons (site, pool, host, vm, power) because they identify resource kinds; the two uses of the n tile as a page icon are replaced.

- [ ] **Step 1: Add two icons**

In `icons.tsx` extend `IconKind` with `"workspace" | "updates"` and add:

```tsx
workspace: <><rect x="2" y="3" width="16" height="14" rx="2" fill="#a8caff" stroke="#1d4ed8" /><path d="M2 8h16M8 8v9" stroke="#1d4ed8" /></>,
updates: <><circle cx="10" cy="10" r="8.5" fill="#dbeafe" stroke="#2563eb" /><path d="M10 14V6M6.5 9.5 10 6l3.5 3.5" stroke="#2563eb" strokeWidth="2" /></>,
```

Change the two `h1` icon expressions in `main.tsx` from `"home"` to `kind === "updates" ? "updates" : "workspace"` for the workspace and updates headings.

- [ ] **Step 2: Run the checks**

`npm --prefix web run typecheck && npm --prefix web run build` — Expected: pass.

- [ ] **Step 3: Hand off**

Suggested commit: `ui: workspace and updates page icons; the n tile is brand only`.

---

### Task 12: Dark theme

**Files:**
- Modify: `web/src/tokens.css` (append the dark block)
- Modify: `web/src/tokens.test.mjs` (already checks the dark block when present)
- Modify: `web/src/icons.tsx` (no change: the icons carry their own fills and read on both grounds)

- [ ] **Step 1: Append to `tokens.css`**

```css
/* The brand's supporting dark palette, applied only when the operator's system asks for it.
   Delete this block to make the Controller light-only. */
@media (prefers-color-scheme: dark) {
  :root {
    color-scheme: dark;
    --canvas: #202B3B;
    --panel: #26324A;
    --sunken: #2A3649;
    --line: #3A4860;
    --line-soft: #2F3D55;
    --text: #E5E8F3;
    --muted: #AAB5C8;
    --accent: #7BA7D9;
    --accent-hover: #9CC0EA;
    --on-accent: #17233B;
    --select: #2C3F63;
    --topbar: #17233B;
    --on-topbar: #E5E8F3;
    --focus: #9CC0EA;
    --danger: #F1A8A2;
    --on-danger: #17233B;
    --ok: #7FD8A4; --ok-bg: #1E3A2C;
    --warn: #F2C067; --warn-bg: #3D2F14;
    --bad: #F1A8A2; --bad-bg: #452224;
    --info: #9CC0EA; --info-bg: #23375A;
    --neutral: #AAB5C8; --neutral-bg: #2A3649;
    --shadow-pop: 0 8px 24px rgba(0, 0, 0, .5);
  }
}
```

- [ ] **Step 2: Run the checks and look**

`node web/src/tokens.test.mjs` (now checks 16 dark pairs too), then capture three views with the harness after adding `color_scheme='dark'` to `browser.new_context(...)` temporarily. Expected: navy canvas, readable text, the badges still square, the brand plate keeps the lockup legible, the console screen stays black.

- [ ] **Step 3: Hand off**

Suggested commit: `ui: dark theme from the token sheet under prefers-color-scheme`.

---

### Task 13: Evidence, guide, changelog, change record

**Files:**
- Create: `docs/qa/controller-ui-modernization/checks.md`
- Modify: `docs/guide/manage-controller.md` **Verification** section; `docs/guide/vm-power.md`, `docs/guide/vm-console.md` **Verification** sections (one sentence each pointing at the new QA record)
- Modify: `CHANGELOG.md` (under the latest numbered version, **Minor Feature**)
- Create: `docs/changes/2026-09-23-controller-ui-modernization.json`

- [ ] **Step 1: After-captures and the browser lab**

```sh
python3 tests/integration/ui_review_browser.py --url https://127.0.0.1:PORT --password-file /path/to/admin-password --captures /tmp/ui-after
GO=/path/to/go NEX_QA_CAPTURES=/tmp/ui-lab python3 tests/integration/phase1.py --browser --evidence docs/qa/controller-ui-modernization/results.json
```

Expected: 40 after-captures with no page errors; the phase1 browser run exits 0 with every result group completed, including the moved Actions-menu groups and the 360 px groups.

- [ ] **Step 2: Inspect and record**

Open every before/after pair side by side. Write `docs/qa/controller-ui-modernization/checks.md` with: environment (working-tree build, headless Chromium, the four viewports, labeled simulated inventory, no real host), the commands above and their exit status, the result groups, the captures inspected by name with what was checked (chrome height at 1440×900 and 1024×768 measured from the captures; badge shape unchanged; contrast test output; keyboard operation of the Actions menu, task strip, off-canvas tree and Focus mode; no horizontal page scroll at 390 px), and limits (console tabs never connected; no real hardware; scores are a reviewer's pass). Captures are not committed.

- [ ] **Step 3: Changelog**

Under the latest numbered version's **Minor Feature** list add: "Controller web UI modernization: one design-token sheet with a dark theme, flat surfaces on the brand palette, a slimmer header with an **Actions** menu replacing the menubar, a collapsed **Tasks** strip that announces failures, an off-canvas resource tree and icon toolbar on narrow screens, operator labels instead of internal identifiers, relative times, a grouped console toolbar with **Focus mode**, and a fuller Updates page. Status badges keep their square dot-and-text form."

- [ ] **Step 4: Change record**

```json
{
  "summary": "Controller web UI modernization: token sheet, flat brand surfaces, header Actions menu replacing the menubar, collapsed task strip, off-canvas tree, operator labels, console focus mode, dark theme.",
  "paths": ["web/src/tokens.css", "web/src/tokens.test.mjs", "web/src/style.css", "web/src/stats.css", "web/src/console.css", "web/src/ui.tsx", "web/src/ui.test.mjs", "web/src/labels.ts", "web/src/labels.test.mjs", "web/src/dock.ts", "web/src/dock.test.mjs", "web/src/icons.tsx", "web/src/main.tsx", "web/src/managed.tsx", "web/src/pool-members.tsx", "web/src/pool-promotion.tsx", "web/src/storage.tsx", "web/src/console.tsx", "web/src/stats.tsx", "web/public/index.html", "tests/integration/ui_review_browser.py", "tests/integration/fixtures/ui-review.json", "tests/integration/phase1_browser.py", "docs/guide/manage-controller.md", "docs/guide/vm-power.md", "docs/guide/vm-console.md", "docs/qa/controller-ui-modernization/checks.md", "docs/qa/controller-ui-modernization/results.json", "CHANGELOG.md", "docs/plans/2026-09-23-controller-ui-modernization.md", "docs/changes/2026-09-23-controller-ui-modernization.json"],
  "guide": {"impact": "updated", "reason": "Navigation chrome, the Actions menu, the Tasks strip, Focus mode and status labels changed what operators see and where.", "pages": ["docs/guide/manage-controller.md", "docs/guide/vm-power.md", "docs/guide/vm-console.md"]},
  "api": {"impact": "none", "reason": "Presentation only: the same client-facing reads and writes, identities, request IDs and results; no Core command or contract changes.", "pages": []},
  "roadmap": {"impact": "none", "reason": "No phase or acceptance-gate status changes; the desktop layout plan is evolved by the linked plan.", "pages": []},
  "state": {"owner": "none", "reason": "No Controller or Core record changes. The dock and tree open/closed preferences live in the browser's localStorage as a per-browser convenience with a fixed default; losing them changes nothing an operator must re-enter."},
  "verification": {"method": "npm --prefix web run typecheck; npm --prefix web test (tokens, ui, labels, dock and existing suites); npm --prefix web run build; python3 scripts/check_docs.py --worktree; python3 -m unittest discover -s tests -p 'test_repository_privacy.py'; tests/integration/phase1.py --browser in a disposable lab; tests/integration/ui_review_browser.py before/after captures at 1440x900, 1440x2000, 1024x768 and 390x844 with a labeled simulated inventory, inspected.", "evidence": ["docs/qa/controller-ui-modernization/checks.md", "docs/qa/controller-ui-modernization/results.json", "web/src/tokens.test.mjs", "web/src/ui.test.mjs", "web/src/labels.test.mjs", "web/src/dock.test.mjs"]}
}
```

- [ ] **Step 5: Gates**

```sh
npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build
python3 scripts/check_docs.py --worktree
python3 -m unittest discover -s tests -p 'test_repository_privacy.py'
python3 scripts/generate_api.py --check
```

Expected: all pass; `generate_api.py --check` is unchanged because no API changed.

- [ ] **Step 6: Hand off**

Suggested commit: `docs: record the Controller UI modernization walkthrough and change record`. Report the net line change of the whole batch (expected: negative in `style.css`, positive overall from the harness and tests).

---

## API and guide impact

API impact: **none**. Every task calls the same `GET /api/v1/{sites,pools,hosts,vms,storage,networks,media,operations,audit,updates}`, `hosts/{id}/{hardware,network,stats}`, `vms/{id}/stats`, `pools/{id}/members` and the same write requests with the same identities, request IDs, confirmations and results. The [API reference](../API.md) does not change; `scripts/generate_api.py --check` stays green.

Guide impact: **updated** in Tasks 5, 6, 10 and 13 (`manage-controller.md`, `vm-power.md`, `vm-console.md`), walked through in the browser lab and the fixture harness and recorded in `docs/qa/controller-ui-modernization/checks.md`.

State: **none**. See the change record's reason for the two `localStorage` preferences.

## Acceptance

- Every task's unit checks pass; `tokens.test.mjs` proves no gradients, no literal colours outside `tokens.css`, and ≥ 4.5:1 on every token pair in both themes.
- Measured from after-captures: content height ≥ 640 px at 1440×900 and ≥ 520 px at 1024×768 with the task strip closed; no horizontal page scroll at 390 px; the pool Hosts table's Actions column visible at 1440 px.
- Status badges are visibly the same square dot-and-text form as before; a side-by-side pair is named in the QA record.
- Keyboard: Actions menu, tabs, tree rows, task strip, off-canvas tree toggle, Focus mode and every dialog reachable and closable with Tab/Enter/Escape, with visible focus.
- `phase1.py --browser` passes with the moved menu groups; the guide's manage-controller, vm-power and vm-console pages describe the delivered chrome.
- Independent review of the batch against scope, behaviour, accessibility, privacy and the actual evidence, with corrections verified.
- Left explicitly open: no vector brand lockup exists yet, so the sign-in and top-bar marks remain crops of the approved raster board; Console connections are not exercised by the fixture harness (the console QA reports cover them); no real hardware or storage is touched by any of this.

## Remaining work

Open minor findings from review: `storage.tsx` `ZFSPoolLines` renders a raw `badge warn` span outside `Badge`; the "Creation ${status}" sentence in `main.tsx` and `HostHealthSummary` show raw values, and `pool-member-join` and `pool-storage-reconcile` use the fallback label; the multi-failure notice logic in `reload()` has no unit test and no test pins the delete-VM notice text quoted by `docs/guide/delete-virtual-machine.md`; `ui_review_browser.py` does not URL-decode query values; at 390 px the icon toolbar takes two rows; console status text built from an API error can lack its full stop.

## Out of scope

Nex Core host-local page and installer; keyboard shortcuts and bulk actions; table column sorting and Task history filters; a global search; any change to the API, the data model or the desktop-console navigation model beyond the five approved moves.
