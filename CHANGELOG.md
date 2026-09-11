All notable changes to **Nex** are recorded here in clear, non-technical language suitable for release notes and internal comms.

This project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Release tags use a `v` prefix (for example `v1.0.0`). Release sections use the same four buckets throughout: **Major Feature**, **Minor Feature**, **Bug Fixes**, and **Miscellaneous**.

> **Version history note:** Nex is a pre-release alpha and has never been git-tagged. Only two
> version numbers were ever stamped into a build: `0.1.0` (from the controller/agent foundation)
> and the current `0.1.5~alpha1`. The sections below reconstruct the development **stages** from
> the commit history and number them across that range; the intermediate versions (0.0.x and
> 0.1.1–0.1.4) map real stages of work but were not individually built or released. Dates are the
> commit dates. The current build stamps from the newest changelog heading (`0.1.8` →
> Debian `0.1.8`).

## [Unreleased]

> **Maintainers:** Leave this section empty. Append new entries under the **latest numbered version** heading below (the first `## [x.y.z]` block after this one) until that version is tagged and published.

## [0.1.8] — 2026-09-11

### Bug Fixes

- Fixed leftover iSCSI sessions after a refused undersized storage registration.
- Fixed concurrent SMB storage marker creation failing under race detection.
- Fixed Controller gallery captures failing when Create VM had no healthy storage option.
- Fixed managed VM browser checks still looking for the old Register local storage control.
- Fixed release builds dropping mid-download of the Debian installer ISO.
- Fixed registration forms following you between pool views. Opening **Register network**,
  **Upload ISO** or **Create VM** and then selecting another view left the form sitting above
  the new content until you clicked **Close**. Each form now stays on its own view, and your
  entries and wizard progress are still there when you come back to it.
- Fixed Controller deployment and VM creation failing when libvirt denied execution of the
  console's custom emulator wrapper. New definitions use the standard host emulator;
  console quality presets currently do not enable lossy JPEG image detail changes.

## [0.1.7-alpha1] — 2026-09-11

### Bug Fixes

- Fixed VM edits stuck on *resource ID already reserved by another operation* after a previous
  edit needed attention (libvirt CPU check mismatch). Retries can run again, and host-passthrough
  definitions match what libvirt stores.
- Fixed the console image quality control so Low / Balanced / Best actually change how the VM
  display is compressed. Existing VMs pick this up the next time their definition is applied
  and the guest is restarted.
- Fixed the VM CPU model list so it only shows named models this host can actually run, and
  named models start under KVM instead of failing on common baselines such as qemu64.
- Fixed Controller updates for appliances that were never installed as a Debian package:
  Update now runs a normal apt install (not --only-upgrade), and Available no longer says
  Up to date when the check failed.
- Fixed the Core package build omitting the VNC lossy wrapper script from the source archive,
  which broke release packaging.

## [0.1.6-alpha1] — 2026-09-11

### Bug Fixes

- Fixed Controller software-update checks in appliance deployments. The appliance now includes
  the signed Nex archive configuration and keyring, and its unprivileged service uses its own
  signed APT index cache instead of failing to write the system package cache.

## [0.1.5-alpha1] — 2026-09-09

Console quality-of-life features, guest tools observation, and the collapse to a single Nex
Core version. This is the version the packages currently build as (`0.1.5~alpha1`).

### Major Feature

- Replaced VM creation with the full specification: a six-step wizard covering name, description and guest OS; vCPU count, sockets/cores/threads and CPU model; a dedicated or maximum memory mode; **multiple disks** with their own storage, name, size, bus and thin/thick provisioning; **multiple network interfaces** with adapter model and automatic or manual MAC; firmware, machine type, install ISO and a per-device boot order you can reorder; and start-after-creation and autostart. VM details now show every disk and interface. Creation still never starts a guest: **Start after creation** issues a separate start task once creation succeeds. Existing lab VMs must be recreated — this alpha has no upgrade path for the old single-disk shape.

### Minor Feature

- Added VM editing in place on the VM's General tab: name, description, guest OS, CPU, memory, disks and network interfaces (add and remove), firmware, machine type, boot order, install ISO and autostart. Changes a running guest can take — adding or removing VirtIO/SCSI disks and network interfaces, swapping or ejecting the ISO, description, guest OS and autostart — apply while it runs; the rest waits for a stop and the form says so per field. Every VM now has a CD drive (empty without an ISO), removed disks are kept on the host rather than deleted, and the host journal keeps every revision of a VM's definition.
- Hosts have a **Console** tab that uses the same browser console as VMs (a root shell on the host).
- From a VM console you can insert or eject an ISO on the CD/DVD tray without leaving the console.

- Controller deploy shows **Local storage** as a fixed, non-editable host path instead of a one-item dropdown.
- ISO upload to Controller can be closed and continues in the background while the UI stays open; progress, rate and Cancel appear as the first row of Recent tasks (BUG-0048). Removed the fixed 8 GiB ISO size cap — staging is limited by free disk space (256 MiB reserved).
- **Check** and **Update** now use the system package manager against the signed Nex repository, instead of downloading a single package file and installing it directly. Updates that need a new supporting component now install cleanly rather than failing part-way, and every package is signature-checked. Progress is reported in steps rather than as a download percentage, and the available-version check refreshes at most once every five minutes.
- Nex Core is now installed only from its own ISO. Installing the Nex package onto an ordinary Debian system is refused with an explanation: Nex Core is a distribution, and the installer establishes the storage, networking, console identity and recovery paths that a supported host depends on. Existing hosts continue to update normally.
- The Controller appliance now carries the Nex repository signing key and repository entry, which it previously lacked entirely.
- Releases are now made by tagging. Pushing a version tag builds the ISO and both packages at that version and publishes them to the website automatically, and refuses to build at all if the tag and the changelog disagree. ISO downloads keep the newest three releases.
- Controller default administrator username is **root** (was admin), so it can match Core host credentials. The deploy password unlocks the appliance console as **root** too.
- Every enrolled host gets a default local storage registration named **ISO** at `/var/lib/libvirt/images` (created on install), so ISO upload and VM disks work without manually registering storage.
- Extended the VM console: send key combinations, fit/actual/resize-to-window display modes, quality presets, view-only mode, screenshots, reconnect, full screen with the toolbar and a console-only browser window.
- Added console clipboard in both directions for guests running the guest tools, plus **Type into VM** for installers and text consoles; new VMs are defined with the clipboard channel, an absolute pointer and a resizable VirtIO display.
- Added a **Guest tools** panel showing the guest agent state, operating system, hostname, IP addresses and clipboard readiness reported by the host. When a running VM has no responding agent it shows an install advisory with the exact commands for Linux and Windows guests.
- Nex Core reports one version: the host page shows `Nex Core <version>` with no separate agent version, and the installed identity, console banner and ISO name follow the Nex Core package version through updates.

### Bug Fixes

- Dropdown menus in the Controller and the host deploy page now open inside the page instead of as a separate desktop popup, so they no longer close by themselves on desktops that dismiss such popups (Wayland compositors such as Hyprland with Chromium-based browsers). Browsers without this capability keep their usual menus (BUG-0064).

- Hardened the host console: it no longer opens a local socket that any process on the host could reach, idle consoles no longer stream full frames continuously, Ctrl+C/D/Z and arrow keys work, tab stops render correctly, and a Controller refuses the host console on an older Nex Core immediately instead of hanging.
- Fixed VM media changes: an interrupted insert no longer reports the ISO as inserted with an empty tray, eject keeps the boot order the host actually holds and an ejected VM still re-adopts into a replacement Controller, ejecting an already-empty tray succeeds, and a media change no longer breaks idempotent replay of the original create request.
- Fixed Create VM so reopening a draft in which every network interface had been removed no longer adds one back and blocks Next.
- Fixed Create VM so the first disk name defaults from the pool and VM name, instead of staying as system and ignoring the name typed on the first step.
- Fixed Create VM CPU topology so blank sockets no longer look like 1 while staying unset; filling cores or threads now starts from the default layout instead of rejecting 0 × cores × threads.
- Fixed Create VM dropdowns (Bus and the rest) closing as soon as they opened.
- Fixed local Core service shutdown so the embedded agent stops before the process exits, which had left CI TempDir cleanup racing agent state writes.
- Shortened the notice after a VM power request so it no longer tells you to follow the task for the next host report.
- Fixed a fresh Nex Core install having no Nex package repository: the host now ships the Nex archive signing key and configures `/etc/apt/sources.list.d/nex.sources`, so `apt update` lists the Nex repo out of the box (BUG-0042).
- Fixed the continuous-integration update tests so they reach the public APT repo, an OpenSSL 3.0 TLS-match check, and a private-IP fixture in the Controller capture harness (BUG-0040, BUG-0041).

### Miscellaneous

- Every push to `main` now builds the ISO and both packages at the changelog version and publishes them as the repository release for that version's tag, replacing the previous release of the same version. Releases no longer wait for a hand-pushed tag, and the build works on a runner that executes jobs inside a container.
- ISO builds always rebuild Core and Controller together from the current source tree, and refuse a package whose embedded Controller is behind the package version, so media never reuses a stale package.

## [0.1.4-alpha1] — 2026-09-09

Update reliability and corresponding-source hygiene.

### Bug Fixes

- Fixed Controller package updates that could stop the service and report success when restart failed; apply now stops the service before dpkg, verifies it restarts with correct credential ownership, and fails loudly with logs.
- Fixed Core updates that could leave progress stuck on **Installing** after a successful upgrade; the Core postinst no longer restarts mid-apply, and stale "installing" state clears once the installed version already matches available.
- Fixed **Updates** to always show the repo's available version instead of hiding it behind **Up to date**, making stuck states easier to diagnose.

### Miscellaneous

- ISO builds always regenerate the corresponding AGPL source and inject it into the packaged Core, so media never ships a stale source snapshot; the installer check rejects the old installer-only source archive.
- Bumped the published package version past earlier per-feature development tags so affected hosts see an available update again.
- Deployed Controller appliance VMs with host-passthrough CPU.
- Cleaned up the Core local sidebar navigation so Overview and Updates read as quiet nav items instead of mismatched form buttons.

## [0.1.3-alpha1] — 2026-09-09

Public update repository and self-sufficient appliances.

### Minor Feature

- Software updates default to the public package repo at `https://nexvirtualization.com/apt`; old private HTTP repo URLs are remapped automatically.
- Controller appliances include the update helper so **Check**/**Update** work without a manual package install, and new Controller deploys from Core inject the helper at first boot.

### Miscellaneous

- Added private APT repository publish tooling for the website host (`/apt/`), including signing bootstrap and package metadata.

## [0.1.2-alpha1] — 2026-09-09

First managed VM lifecycle, packaged updates, and the pool-master model.

### Major Feature

- Added Controller/Core support for creating and running managed VMs: pool-owned local storage and guest-network registration, ISO upload, stopped-VM creation with selected CPU, memory, disk, network and firmware, a browser console over an authenticated WebSocket, and central VM power actions (start, shut down, reboot, force stop).
- Added packaged software-update delivery for both Controller and Core, including a Debian `nex-controller` package that upgrades appliance installs in place, with download/install progress and error status in both UIs.

### Minor Feature

- Pools are now created automatically: administrators create sites only, and every enrolled Nex Core host becomes the **pool master** of its own pool, named after its hostname. Enrollment tokens are bound to a site. The **Create pool** action and its API (`POST /api/v1/pools`) are removed; existing pools gain a master on upgrade. Joining a member host, changing the master and ejecting remain planned.
- Added **Add host** by address and root password: Controller shows the host's certificate fingerprint for confirmation, then enrolls the host over its local HTTPS API without storing or logging the password. The manual enrollment token remains available for hosts Controller cannot reach.
- Simplified local VM power confirmations so operators do not have to type a hostname, while retaining target confirmation and ownership checks.

### Miscellaneous

- Added the managed-VM and console protocol contracts, with documentation, QA evidence, and rebuilt Controller UI/desktop appliance artifacts.

## [0.1.1-alpha1] — 2026-09-09

Controller desktop shell.

### Minor Feature

- Rebuilt the Controller shell as a compact desktop layout: a menu and toolbar, a searchable collapsible resource tree, General/Networking/Storage/Activity tabs, and a bottom task drawer.

### Bug Fixes

- Fixed the mobile Help menu clipping past the left edge at 360px, with a regression bounds check in the browser suite (BUG-0026).

## [0.1.0-alpha1] — 2026-09-09

Phase 1 Controller and Core foundation. First version stamped into a build.

### Major Feature

- Added the Nex Controller development foundation: administrator sign-in, Sites → Pools → Hosts, secure host enrollment, inventory, health reporting, refresh tasks and activity history.
- Added a built-in host web interface for Controller appliance deployment and local recovery, including ownership-aware VM power controls when Controller is unavailable.
- Added a prepared Controller virtual appliance with signed bundle verification, first-boot setup and host enrollment, plus manual Controller state and identity export and recovery on a replacement host.
- Built a custom Nex Core graphical installer with Nex branding, fullscreen pages, step navigation, host setup, network configuration, disk selection and a final installation review.

### Minor Feature

- Added installer timezone dropdowns, keyboard layout selection, password visibility controls and shared hostname editing on the Host and Network pages, plus a welcome introduction and a reminder to deploy at least one Nex Controller.
- Added detected network adapters with MAC addresses and a green link indicator, per-adapter DHCP or static IPv4, VLAN settings, and an explicit management adapter selection.
- DHCP selection now shows that it is looking for an address, then displays the received IP, with clear failure feedback and Retry. This previews the lease; the installed host requests its own address.
- Added disk details and a single erase confirmation naming the selected target, replacing typed disk-name confirmation.
- Added consistent Nex installation progress, a completion screen with **Restart now**, and failure messages with an installation log viewer.
- The host console shows the local web interface URL using the management address and configured web port; the host-local interface uses **root** and the password set during installation.
- Controller deployment defaults to DHCP, with static IPv4 available under **Network settings**, and can rediscover the appliance address on startup or explicit retry while preserving its identity. Removed the deployment password minimum, retaining input size and encoding checks.
- Added host credential rotation, revocation and deliberate re-enrollment, with persistent identity and task state across service restarts.

### Bug Fixes

- Fixed the graphical installer leaving unused screen margins and improved page sizing and navigation on smaller displays.
- Replaced difficult typed timezone selection with dropdowns and removed the confusing keyboard-test field and startup keyboard warning.
- Corrected hostname validation to accept valid short and fully qualified names, and removed the lengthy hostname hover text.
- Corrected NIC status to report detected link instead of treating an administratively disabled adapter as a disconnected cable.
- Fixed valid Xen virtual installation disks being rejected at the final review.
- Fixed management adapter configuration to create the bridge needed by Controller deployment, preserving DHCP, static IP and VLAN settings.
- Removed routine debconf-loading interruptions and fixed transitions between custom progress and the stock Debian progress bar.
- Removed the redundant console certificate fingerprint and login instructions, and the host web interface's routine Observed timestamp badge; stale-state warnings remain.
- Fixed DHCP preview text leaking into static address editing and ensured canceled or outdated requests cannot update the wrong adapter.
- Fixed DHCP appliance restart ordering so address discovery happens before agent connection, while preserving certificate verification.
- Fixed appliance first-boot service ordering and permissions, and made deployment readiness wait for the UI and bootstrap cleanup.
- Corrected host health reporting so stalled probes and fresh heartbeats do not imply fresh, healthy inventory.
- Fixed login admission starvation from incomplete requests and inherited process locks that prevented clean service restarts.

### Miscellaneous

- Hardened web sessions, cookies, request validation, login throttling and browser security headers; rebuilt application binaries with an updated Go compiler and checked their dependencies.
- Added checks to the GitHub and Gitea workflows. Local checks and disposable VM/container walkthroughs are recorded; hosted CI execution is not claimed.
- Expanded the operator guides, API reference, bug ledger and verification records.

## [0.0.4-alpha1] — 2026-09-08

### Miscellaneous

- Recorded the installer handoff and verification status, updating the activity log and change assessments.

## [0.0.3-alpha1] — 2026-09-08

### Minor Feature

- Made installer setup require root only, and added verified user documentation.

### Miscellaneous

- Added the documentation checker (`scripts/check_docs.py`) and its test, and a first GitHub workflow.

## [0.0.2-alpha1] — 2026-09-08

First publishable standalone installer alpha.

### Major Feature

- Added the standalone Nex Core installer recipe with branding and smoke checks, producing a Debian 13-based installation ISO with graphical, minimal text and serial installation options, keeping KVM/QEMU and libvirt as the virtualization platform.

### Miscellaneous

- Applied the AGPL-3.0-only licensing decision to original Nex source while preserving third-party licensing and tracking source-delivery and artwork requirements.
- Added the contributor rules, platform plans and initial QA evidence.

## [0.0.1-alpha1] — 2026-09-08

### Miscellaneous

- Initialized the repository.

---

Verification and remaining limits (current alpha): see the
[Controller foundation checks](docs/qa/phase1/checks.md),
[appliance acceptance](docs/plans/2026-09-08-controller-appliance.md),
[security checks](docs/qa/security/checks.md), [appliance rebuild](docs/qa/appliance/usability-rebuild.md),
[latest installer DHCP checks](docs/qa/iso/2026-09-09-installer-dhcp.md),
[pool master lab checks](docs/qa/pool-master/lab/checks.md),
[add host lab checks](docs/qa/simple-host-enrollment/lab/checks.md) and the
[console features checks](docs/qa/console-features/checks.md).
The `0.1.5~alpha1` media has now been built from the current source
(`dist/nex-core-0.1.5~alpha1-amd64.iso`, SHA-256 `5b4a4367…`) but not yet install-tested; the
prior tested media was `nex-core-0.1.0-alpha1-amd64.iso`. Physical hardware, external redistribution
requirements and full phase acceptance remain open. Shared storage, backups, migration, HA and
Container Services remain roadmap work.
