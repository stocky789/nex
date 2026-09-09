All notable changes to **Nex** are recorded here in clear, non-technical language suitable for release notes and internal comms.

This project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Release tags use a `v` prefix (for example `v1.0.0`). Release sections use the same four buckets throughout: **Major Feature**, **Minor Feature**, **Bug Fixes**, and **Miscellaneous**.

## [Unreleased]

> **Maintainers:** Leave this section empty. Append new entries under the **latest numbered version** heading below (the first `## [x.y.z]` block after this one) until that version is tagged and published.

## [0.1.0-alpha1]

Development work from 8–9 September 2026. This remains a lab-tested alpha; the entries
below describe implemented work, not completion of the full platform roadmap.

### Major Feature

- Added a Debian 13-based Nex Core installation ISO with graphical, minimal text and serial installation options, keeping KVM/QEMU and libvirt as the virtualization platform.
- Built a custom Nex Core graphical installer with Nex branding, fullscreen pages, step navigation, host setup, network configuration, disk selection and a final installation review.
- Added the Nex Controller development foundation: administrator sign-in, Sites → Pools → Hosts, secure host enrollment, inventory, health reporting, refresh tasks and activity history.
- Added a built-in host web interface for Controller appliance deployment and local recovery, including ownership-aware VM power controls when Controller is unavailable.
- Added a prepared Controller virtual appliance with signed bundle verification, first-boot setup and host enrollment, plus manual Controller state and identity export and recovery on a replacement host.

### Minor Feature

- Added installer timezone dropdowns, keyboard layout selection, password visibility controls and shared hostname editing on the Host and Network pages.
- Added a welcome introduction and a reminder to deploy at least one Nex Controller.
- Added detected network adapters with MAC addresses and a green link indicator, per-adapter DHCP or static IPv4, VLAN settings, and an explicit management adapter selection.
- DHCP selection now shows that it is looking for an address, then displays the received IP, with clear failure feedback and Retry. This previews the lease; the installed host requests its own address.
- Added disk details and a single erase confirmation naming the selected target, replacing typed disk-name confirmation.
- Added consistent Nex installation progress, a completion screen with **Restart now**, and failure messages with an installation log viewer.
- The host console shows the local web interface URL using the management address and configured web port.
- The host-local web interface uses **root** and the password set during installation, following the host's account policy and subsequent password changes.
- Controller deployment now defaults to DHCP, with static IPv4 available under **Network settings**. Startup and explicit retry can rediscover the appliance address while preserving its identity.
- Removed the Controller deployment password minimum; short nonempty passwords are accepted, with input size and encoding checks retained.
- Simplified local VM power confirmations so operators do not have to type a hostname, while retaining target confirmation and ownership checks.
- Added host credential rotation, revocation and deliberate re-enrollment, with persistent identity and task state across service restarts.
- Pools are now created automatically: administrators create sites only, and every enrolled Nex Core host becomes the **pool master** of its own pool, named after its hostname. Enrollment tokens are bound to a site. The **Create pool** action and its API (`POST /api/v1/pools`) are removed; existing pools gain a master on upgrade. Controller and Core must be upgraded together. Joining a member host, changing the master and ejecting remain planned.
- Added **Add host** by address and root password: Controller shows the host's certificate fingerprint for confirmation, then enrolls the host over its local HTTPS API without storing or logging the password. The manual enrollment token remains available for hosts Controller cannot reach.
- Added software update checks and apply flows for Controller and Core local packages, including a Debian `nex-controller` package that upgrades appliance installs in place, with download/install progress and error status in both UIs.
- Software updates default to the public package repo at `https://nexvirtualization.com/apt`.
- Removed private host addresses from website and update documentation.
- Controller appliances include the update helper so Check/Update work without a manual package install; old lab repo URLs are ignored automatically.
- New Controller deploys from Core also inject the update helper at first boot.
- Cleaned up the Core local sidebar navigation so Overview and Updates read as quiet nav items instead of mismatched form buttons.

### Bug Fixes

- Fixed the graphical installer leaving unused screen margins and improved page sizing and navigation on smaller displays.
- Replaced difficult typed timezone selection with dropdowns and removed the confusing keyboard-test field and startup keyboard warning.
- Corrected hostname validation to accept valid short hostnames and fully qualified names, and removed the lengthy hostname hover text.
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

- Added private APT repository publish tooling for the website host (`/apt/`), including signing bootstrap and package metadata.
- Hardened web sessions, cookies, request validation, login throttling and browser security headers; rebuilt application binaries with an updated Go compiler and checked their dependencies.
- Rebuilt the Controller appliance, host-local Debian package and Core ISO with matching Nex source snapshots; the latest ISO includes installer DHCP feedback.
- Expanded the operator guides, API reference, bug ledger and verification records, with regression checks for installer, networking, authentication, appliance deployment and recovery.
- Added checks to the GitHub and Gitea workflows. Local checks and disposable VM/container walkthroughs are recorded; hosted CI execution is not claimed.
- Recorded AGPL-3.0-only licensing for original Nex code while preserving third-party licensing and tracking source-delivery and artwork requirements.

Verification and remaining limits: see the [Controller foundation checks](docs/qa/phase1/checks.md),
[appliance acceptance](docs/plans/2026-09-08-controller-appliance.md),
[security checks](docs/qa/security/checks.md), [appliance rebuild](docs/qa/appliance/usability-rebuild.md),
[latest installer DHCP checks](docs/qa/iso/2026-09-09-installer-dhcp.md),
[pool master lab checks](docs/qa/pool-master/lab/checks.md) and
[add host lab checks](docs/qa/simple-host-enrollment/lab/checks.md).
The packaged appliance image still ships the previous Controller; deploying it with the
updated host package stops at needs attention until the image is rebuilt.
The latest ISO has not had a complete fresh installation walkthrough. Physical hardware,
external redistribution requirements and full phase acceptance remain open. General
Controller VM management and consoles, shared storage, backups, migration, HA and
Container Services remain roadmap work.
