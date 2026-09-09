All notable changes to **Nex** are recorded here in clear, non-technical language suitable for release notes and internal comms.

This project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Release tags use a `v` prefix (for example `v1.0.0`). Release sections use the same four buckets throughout: **Major Feature**, **Minor Feature**, **Bug Fixes**, and **Miscellaneous**.

> **Version history note:** Nex is a pre-release alpha and has never been git-tagged. Only two
> version numbers were ever stamped into a build: `0.1.0` (from the controller/agent foundation)
> and the current `0.1.5~alpha1`. The sections below reconstruct the development **stages** from
> the commit history and number them across that range; the intermediate versions (0.0.x and
> 0.1.1–0.1.4) map real stages of work but were not individually built or released. Dates are the
> commit dates. The current build stamps `0.1.5~alpha1` (Debian package form of `0.1.5-alpha1`).

## [Unreleased]

> **Maintainers:** Leave this section empty. Append new entries under the **latest numbered version** heading below (the first `## [x.y.z]` block after this one) until that version is tagged and published.

## [0.1.5-alpha1] — 2026-09-09

Console quality-of-life features, guest tools observation, and the collapse to a single Nex
Core version. This is the version the packages currently build as (`0.1.5~alpha1`).

### Minor Feature

- Extended the VM console: send key combinations, fit/actual/resize-to-window display modes, quality presets, view-only mode, screenshots, reconnect, full screen with the toolbar and a console-only browser window.
- Added console clipboard in both directions for guests running the guest tools, plus **Type into VM** for installers and text consoles; new VMs are defined with the clipboard channel, an absolute pointer and a resizable VirtIO display.
- Added a **Guest tools** panel showing the guest agent state, operating system, hostname, IP addresses and clipboard readiness reported by the host. When a running VM has no responding agent it shows an install advisory with the exact commands for Linux and Windows guests.
- Nex Core reports one version: the host page shows `Nex Core <version>` with no separate agent version, and the installed identity, console banner and ISO name follow the Nex Core package version through updates.

### Bug Fixes

- Fixed the continuous-integration update tests so they reach the public APT repo, an OpenSSL 3.0 TLS-match check, and a private-IP fixture in the Controller capture harness (BUG-0040, BUG-0041).

### Miscellaneous

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

- Rebuilt the Controller shell as a compact XenCenter-style desktop layout: a menu and toolbar, a searchable collapsible resource tree, General/Networking/Storage/Activity tabs, and a bottom task drawer.

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
The last ISO actually built and tested is `nex-core-0.1.0-alpha1-amd64.iso`; the `0.1.5~alpha1`
build default has not yet been built into media. Physical hardware, external redistribution
requirements and full phase acceptance remain open. Shared storage, backups, migration, HA and
Container Services remain roadmap work.
