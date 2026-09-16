# Changelog

All notable changes to **Nex** are recorded here in clear, non-technical language suitable for release notes and internal comms.

This project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Release tags use a `v` prefix (for example `v1.0.0`). Release sections use the same four buckets throughout: **Major Feature**, **Minor Feature**, **Bug Fixes**, and **Miscellaneous**.

> **Version history note:** Early sections reconstruct development **stages** from commit
> history; a section alone does not establish that a version was published. Nex remains
> development software. Builds use the newest numbered changelog heading (`0.2.8` →
> Debian `0.2.8`).

## [Unreleased]

> **Maintainers:** Leave this section empty. Append new entries under the **latest numbered version** heading below (the first `## [x.y.z]` block after this one) until that version is tagged and published.

## [0.2.8] — 2026-09-16

### Major Feature

- **Windows 11 now installs.** Virtual machines can be given a TPM 2.0, which Windows 11 requires
  and Nex could not previously provide, so only the relaxed IoT editions would install. Standard
  editions now install normally. The Create VM wizard turns the TPM on by default when you choose
  Windows. A TPM cannot be removed from a VM afterwards: anything the guest sealed to it, such as
  a BitLocker key, would be lost (BUG-0196).
- **Secure Boot is now shown and can be changed.** It was always on and invisible. Existing VMs
  keep exactly the firmware they were installed with, so nothing already running is affected. You
  can turn it off for unsigned boot media or drivers, and a host that cannot provide the right
  firmware now says so in those words instead of reporting an unexplained failure
  (BUG-0199, BUG-0215).
- **Every VM now has a second CD drive for a driver disc.** A Windows guest installing to a VirtIO
  disk needs the VirtIO driver ISO available at the same time as the Windows ISO. Previously there
  was one drive, so the discs had to be swapped part-way through Setup. Both can now be inserted
  at once, and either can be changed while the VM is running (BUG-0198).

### Minor Feature

- Start a VM from inside its console. Opening the console first means you are already watching
  when the guest boots, so the brief *Press any key to boot from CD or DVD* prompt is not missed
  (BUG-0197).
- After an installation finishes, Nex offers to eject the install disc, which otherwise keeps
  re-offering the installer on every start (BUG-0202).
- Remove a failed ISO download from the ISO library. An ISO already stored on a host still cannot
  be removed and now says so plainly, rather than appearing to work (BUG-0203).
- Per-VM CPU, memory and disk usage is now measured on the host and reported through the API.
  Nothing displays it yet.

### Bug Fixes

- Shutting down a VM no longer reports *libvirt refused the shutdown request* when the guest
  actually shut down. Windows guests often take longer to stop than the old one-size timeout
  allowed, so a successful shutdown was reported as needing attention (BUG-0213).
- Typing into a Windows console no longer loses capitals and symbols. `Nex-Test_Upper D:\Path`
  arrived as `nex-test-upper d;\path`, which made passwords, paths and commands impossible to
  type (BUG-0212).
- *Resize guest to window* is now offered only when the guest can actually do it, instead of
  whenever the display hardware supports it, and explains what to install when it cannot
  (BUG-0201).
- A Windows 11 guest is reported as Windows 11, not Windows 10 (BUG-0211).
- Windows guests now keep the correct clock. The guest read the host's UTC time as local time
  until it reached a time server. Windows guests also get the standard Hyper-V settings
  (BUG-0210).
- A download link that redirects to an insecure address now says so and suggests using a direct
  secure link, instead of reporting a connection failure (BUG-0200).
- The host's built-in storage is now called **Local storage**, not **ISO**, since it holds VM
  disks as well. Hosts that already registered it keep the name they recorded (BUG-0205).
- The ISO upload banner no longer says a transfer is queued after it has succeeded (BUG-0204).
- A new VM with dedicated memory no longer shows upgrade instructions meant for older VMs
  (BUG-0206).
- The console's *Open in new window* link is no longer clipped, and the guest display stays usable
  when the clipboard panel is open (BUG-0207).
- The sign-in page no longer logs an error in the browser console before you sign in (BUG-0208).
- The installer's progress and completion screens now use the same steps as the rest of the
  installer, and the completion screen no longer offers **Go Back** (BUG-0209).

### Miscellaneous

- Deleting a VM now removes its TPM state and firmware variables, including when an earlier edit
  removed the device. A VM whose firmware was switched from UEFI to BIOS can still leave its
  firmware variable file behind; this is recorded and will be fixed with the firmware variable
  store work (BUG-0214).
- A VM's firmware can be changed while it is stopped. The guide previously said it could not,
  which was wrong. An operating system already installed under one firmware will usually not boot
  under the other, so choose it before installing.
- Running the test suite on a workstation with virtualisation tools installed no longer issues
  commands to that machine's own hypervisor, which raised repeated password prompts (BUG-0216).
- The API reference now documents the TPM, Secure Boot, driver disc and usage fields, and the
  media delete operation.

## [0.2.7] — 2026-09-15

### Minor Feature

- Delete a stopped guest VM from **General** or by right-clicking it in **Resources**. You can keep
  the disk files on the host or permanently delete them.
- Right-click a VM in **Resources** for Start, Shut down, Reboot, Force reboot, Force stop, Open
  console, and Delete.

### Bug Fixes

- Creating a VM, or changing a stopped one, no longer fails with *insufficient available memory
  including 2 GiB host reserve* just because the host is short of free memory at that moment.
  Free memory is now checked when the VM starts, so it is created and stays stopped, and starting
  it explains that there is not enough memory available (BUG-0195).

## [0.2.6] — 2026-09-15

### Minor Feature

- Stopped VMs show muted in the **Resources** tree so they are easier to tell apart from running ones.

### Bug Fixes

- Updating the Nex Controller from **Updates** no longer gets stuck on *Update requested…* after an
  earlier update attempt failed. A failed update used to leave its request behind, so the update
  service retried in a loop until the system stopped starting it and ignored every later click;
  the failure and its message now also show on the page. A Controller already stuck needs a short
  manual upgrade first, described in the software updates guide (BUG-0193).

## [0.2.5] — 2026-09-15

### Bug Fixes

- A storage credential request that Nex Core refuses — for example one that reuses a request ID
  already used for a different credential — no longer stops the Nex Core agent. The refusal was
  treated as a fatal error, so the agent restarted repeatedly for several seconds; it now skips
  that one request, which reports that it was not acknowledged, and keeps running (BUG-0189).
- NFS and SMB storage that could not be mounted when Nex Core started, for example because the
  NAS booted later, now comes back by itself within about a minute, and **Recheck** retries it
  immediately. Storage that is not mounted says *remote storage is not mounted* (BUG-0190).
- Changing a VM no longer fails with *other domain disk cannot be inspected for exclusive
  ownership* just because a different VM's storage is temporarily unmounted (BUG-0191).
- A VM change on shared storage that was interrupted, including a console CD/DVD insert or eject,
  now settles by itself about a minute after its storage is healthy. Until then only that VM is
  held back: other VMs in the pool can still start, stop and change media (BUG-0192).

## [0.2.4] — 2026-09-15

### Minor Feature

- When creating a VM, install media is first in the boot order by default.
- ISO library URL downloads prefill the ISO name from the filename in the download link.

### Bug Fixes

- Ejecting a disc from a running VM works again. On a VM that boots from its install media —
  the default for VMs created with an ISO — **Eject** failed with *stop the VM first to change
  the boot order*. An eject no longer touches the boot order at all: the empty CD drive keeps
  its place, exactly as the host's own definition does, so the disc comes out while the guest
  keeps running (BUG-0188).
- Software updates work again on a host that has storage registered. Updating Nex Core or a
  Controller failed after the download with *refusing APT downgrade while version-2 storage
  state is retained*, even though the new package was newer and carried the required marker:
  the safeguard misread the package manager's own install steps. Downgrades to a package
  without that marker are still refused (BUG-0187).
- Updating past this needs the fixed safeguard on the host first. A host still on 0.2.3 or
  earlier shows the same message until it is reinstalled, or the hook is moved aside for one
  upgrade as described in the software updates guide.

## [0.2.3] — 2026-09-15

### Minor Feature

- Added Force reboot for a hard guest reset from the Controller toolbar.

### Bug Fixes

- Local console no longer shows systemd AF_VSOCK SSH generator errors on hosts without vsock.
- The console CD/DVD control now shows when the guest ejected the disc itself, as a Debian
  installer does after it copies the base system, and offers the same ISO so you can put it
  back without stopping the VM (BUG-0184).
- Ejecting a disc the guest already removed also clears it from the VM's saved settings, so it
  is not attached again at the next start (BUG-0184).
- Eject and insert from the VM console work on VMs using pool-shared storage instead of failing
  with *shared VM media changes require a durable pool definition transition*; the change is
  recorded on the pool master like any other VM setting (BUG-0185).

## [0.2.2] — 2026-09-14

### Bug Fixes

- A single-host pool now records its host as the pool master, so pool storage and VM operations
  work there exactly as on a pool with members (BUG-0183).
- A VM on NFS or SMB storage can start on a single-host pool instead of failing with *pool
  membership is unavailable* (BUG-0182).
- Registering NFS, SMB or clustered iSCSI storage no longer asks for a host; the form names the
  pool master, and every host in the pool receives the repository.

## [0.2.1] — 2026-09-14

### Bug Fixes

- VM creation and ISO insert work with an ISO stored on SMB storage instead of failing with *ISO
  size or immutable permissions changed* (BUG-0181).
- A VM can attach an install ISO from any storage on its host, not only the repository that
  holds the VM's disks (BUG-0180).

## [0.2.0] — 2026-09-14

### Bug Fixes

- A single-host pool can register NFS and SMB storage again instead of failing with *pool
  membership is unavailable for remote storage operation* (BUG-0178).

## [0.1.9] — 2026-09-14

### Major Feature

- Added pool-wide NFS and SMB repositories: members join through the Core-local API and receive
  the master's storage configuration, which survives Controller replacement (BUG-0094).

### Minor Feature

- Added guided discovery for existing local directories, ZFS datasets, NFS exports, SMB shares
  and iSCSI targets; manual entry remains available (BUG-0095).
- Added an optional SMB domain/workgroup with repository credentials, and marked existing
  repository folders with **E** in the share picker.
- Clustered iSCSI setup also offers target discovery; LUN, WWID and filesystem UUID stay explicit.
- Clustered iSCSI, NFS and SMB passed multi-host concurrent-write lab scenarios; clustered iSCSI
  is not accepted for production (BUG-0138, BUG-0143).

### Bug Fixes

- SMB shared storage no longer reports false cross-host lock failures when several hosts check at once (BUG-0173).
- Core peer routes return correct status codes for a wrong method and an empty fence-proof list (BUG-0174, BUG-0175).
- Restored usable CPU-model inventory (BUG-0154).
- Member storage-reconciliation health now shows a safe failure stage and outcome (BUG-0155).
- Disk-selection explanations stay wrapped at the installer's 800×600 resolution (BUG-0153).
- Clustered-storage controls report a retryable negotiation state after reconnect instead of
  calling a new Core unsupported (BUG-0151).
- Task history labels Core-reported historical outcomes with their re-observation time (BUG-0152).
- Fixed local management restarting on clean Core installs (BUG-0149).
- Core enrollment recovery keeps the working credential until a replacement is accepted (BUG-0148).
- Periodic reconciliation leaves already-correct cluster configuration and services untouched
  (BUG-0146).
- Storage setup distinguishes unavailable master status from an empty configuration (BUG-0147).
- Disposable cluster-host definitions now boot with all assigned CPUs (BUG-0145).
- Shared-VM shutdown and force stop work while clustered storage or the master is unavailable
  (BUG-0144).
- Storage rows are selected by repository identity when names are identical.
- Disposable workload verification refuses unknown cleanup state (BUG-0142).
- Core negotiates extended clustered-storage reports, staying compatible with older Controllers
  (BUG-0141).
- Source archives omit the build account identity from tar metadata (BUG-0140).
- Offline storage shows cached access as a last report and withholds actions until its Core
  reconnects (BUG-0139).
- Shared VM edits keep the master's ownership record in sync with safe retries (BUG-0131).
- VM ownership survives disk-placement changes and stale reports from other hosts (BUG-0132/0133).
- Separated initial cluster setup from degraded runtime readiness (BUG-0130).
- Clustered storage rejects incompatible DLM fencing libraries (BUG-0128).
- GFS2 monitoring uses its supported timeout (BUG-0127).
- Clustered fencing explicitly powers hosts off, including DLM requests (BUG-0125/0134).
- Cluster status requires fresh proof-aware reports after a fence (BUG-0137).
- Clustered iSCSI verifies its session and filesystem before Pacemaker starts (BUG-0121).
- Uncertain shared-storage VM creation can recover without recreating or starting the VM
  (BUG-0122).
- Shared-VM autostart attempts survive agent restarts and stay tied to the host boot (BUG-0123).
- A slow repository check no longer exhausts later checks' deadlines (BUG-0119).
- Late discovery replies no longer reopen a cancelled folder picker (BUG-0120).
- Lost fencing responses now require Core-status reconciliation, not automatic retry (BUG-0108).
- Added a Controller compatibility handshake for newer clustered-storage reports (BUG-0109).
- Corrected certificate identity checks for delegated fencing (BUG-0110).
- GFS2 resources stay disabled until DLM ordering is verified (BUG-0111).
- Fixed generated Corosync quorum syntax (BUG-0112).
- Fixed cluster health-check and Pacemaker change handling (BUG-0113, BUG-0114).
- Clustered-storage reports keep member host identities (BUG-0115).
- Corrected GFS2 repository observation and member mount paths (BUG-0116).
- Shared-repository selection covers clustered GFS2; uncoordinated updates/detach are blocked
  (BUG-0117).
- Fixed shared VM operations blocked by healthy checks and inventory not converging after a join
  (BUG-0101, BUG-0102).
- Fixed unchanged iSCSI configuration treated as credential rotation, and tightened fencing
  controls (BUG-0103, BUG-0104).
- Fixed interrupted cluster setup recovery and fence-request replay (BUG-0105).
- Fixed SSH fence-pin recovery and virsh fencing integration (BUG-0106, BUG-0107).
- Shared VM starts require the pool master's ownership approval (BUG-0098).
- Shared-storage checks test QEMU-compatible byte-range locking (BUG-0100).
- Fixed cluster status replies discarded for valid states (BUG-0099).
- Installed Core hosts register their default **ISO** library automatically again (BUG-0093).
- Host and pool health summaries show affected checks and reasons (BUG-0096).
- Fixed pool storage recovery after member outages, missed updates and access failures.

### Miscellaneous

- Verified bug-ledger fixes in a disposable lab; the nested UEFI crash (BUG-0097) remains open.
- Documented the internal Core-to-Core pool peer API and enforced reference coverage for every peer route and agent command.

## [0.1.8] — 2026-09-11

### Bug Fixes

- Fixed leftover iSCSI sessions after a refused undersized registration.
- Fixed concurrent SMB storage marker creation.
- Fixed Controller gallery captures when Create VM had no healthy storage.
- Fixed managed VM browser checks looking for an old control.
- Fixed simulated storage registrations never reporting healthy in browser CI.
- Fixed ISO URL browser mocks missing healthy storage access.
- Split Controller foundation CI into parallel lanes.
- Fixed Core package install in the installer when AppArmor could not reload policy.
- Fixed release builds dropping mid-download of the Debian installer ISO.
- Registration forms no longer follow you between pool views; entries are kept.
- Fixed Controller deployment and VM creation when libvirt denied the console emulator wrapper;
  console quality presets no longer change lossy JPEG detail.

## [0.1.7-alpha1] — 2026-09-11

### Bug Fixes

- Fixed VM edits stuck on *resource ID already reserved by another operation* after an edit
  needed attention.
- Console Low / Balanced / Best quality now changes display compression; existing VMs apply it
  after their definition is reapplied and the guest restarts.
- The VM CPU model list shows only models the host can run under KVM.
- Fixed Controller updates on appliances not installed as a Debian package, and **Up to date**
  shown after a failed check.
- Fixed the Core source archive omitting the VNC wrapper script.

## [0.1.6-alpha1] — 2026-09-11

### Bug Fixes

- Fixed Controller software-update checks in appliance deployments.

## [0.1.5-alpha1] — 2026-09-09

Console quality-of-life features, guest tools observation, and the collapse to a single Nex
Core version. This stage used Debian package version `0.1.5~alpha1`.

### Major Feature

- Replaced VM creation with a six-step wizard: CPU topology and model, memory mode, **multiple disks**
  and **network interfaces**, firmware, boot order and autostart. Existing lab VMs must be recreated.

### Minor Feature

- Added VM editing on the General tab; live-capable changes apply while running, the rest after a
  stop. Removed disks are kept on the host.
- Hosts have a **Console** tab that uses the same browser console as VMs (a root shell on the host).
- From a VM console you can insert or eject an ISO on the CD/DVD tray without leaving the console.

- Controller deploy shows **Local storage** as a fixed, non-editable host path instead of a one-item dropdown.
- ISO upload continues in the background with progress in Recent tasks (BUG-0048); the 8 GiB cap
  is replaced by a free-space limit.
- **Check** and **Update** use the system package manager against the signed Nex repository,
  reporting progress in steps.
- Nex Core installs only from its own ISO; installing the package on ordinary Debian is refused.
  Existing hosts update normally.
- The Controller appliance now carries the Nex repository signing key and repository entry, which it previously lacked entirely.
- Pushing a version tag builds and publishes the ISO and packages; the build refuses a tag that
  disagrees with the changelog. Downloads keep the newest three releases.
- Controller default administrator username is **root** (was admin), so it can match Core host credentials. The deploy password unlocks the appliance console as **root** too.
- Every enrolled host gets a default local storage registration named **ISO** at `/var/lib/libvirt/images` (created on install), so ISO upload and VM disks work without manually registering storage.
- Extended the VM console: key combinations, display modes, quality presets, view-only,
  screenshots, reconnect, full screen and a console-only window.
- Added two-way console clipboard for guests with guest tools, plus **Type into VM**.
- Added a **Guest tools** panel with agent state, OS, hostname, IPs and install guidance.
- Nex Core reports one version, `Nex Core <version>`, with no separate agent version.

### Bug Fixes

- Dropdown menus open inside the page so they no longer close by themselves on some Wayland
  desktops (BUG-0064).

- Hardened the host console: no host-reachable local socket, no idle frame streaming, working
  control/arrow keys, and immediate refusal on an older Nex Core.
- Fixed VM media changes: interrupted inserts, eject boot order and re-adoption, empty-tray eject
  and create-request replay.
- Fixed Create VM so reopening a draft in which every network interface had been removed no longer adds one back and blocks Next.
- Fixed Create VM so the first disk name defaults from the pool and VM name, instead of staying as system and ignoring the name typed on the first step.
- Fixed Create VM CPU topology handling of blank sockets.
- Fixed Create VM dropdowns (Bus and the rest) closing as soon as they opened.
- The embedded agent now stops before the local Core service exits.
- Shortened the notice after a VM power request so it no longer tells you to follow the task for the next host report.
- Fresh Nex Core installs configure the signed Nex package repository (BUG-0042).
- Fixed continuous-integration update and capture tests (BUG-0040, BUG-0041).

### Miscellaneous

- Every push to `main` builds and publishes the ISO and packages as the release for the changelog
  version, replacing an earlier release of that version.
- ISO builds rebuild Core and Controller together and refuse a stale embedded Controller.

## [0.1.4-alpha1] — 2026-09-09

Update reliability and corresponding-source hygiene.

### Bug Fixes

- Controller package updates now fail with logs when the service does not restart.
- Fixed Core update progress stuck on **Installing** after a successful upgrade.
- **Updates** always shows the repository's available version.

### Miscellaneous

- ISO builds always regenerate and embed the corresponding AGPL source.
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

- Added managed VMs: pool storage and network registration, ISO upload, VM creation, a browser
  console and central power actions.
- Added packaged software updates for Controller and Core with progress and error status.

### Minor Feature

- Pools are created automatically: each enrolled host becomes **pool master** of its own pool.
  **Create pool** and `POST /api/v1/pools` are removed.
- Added **Add host** by address and root password with certificate fingerprint confirmation; the
  password is not stored. Manual enrollment tokens remain.
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
- Controller deployment defaults to DHCP (static IPv4 under **Network settings**) and rediscovers
  the appliance address while preserving its identity.
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
- Added checks to the GitHub and Gitea workflows.
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

- Added the Debian 13-based Nex Core installation ISO with graphical, text and serial options.

### Miscellaneous

- Applied the AGPL-3.0-only licensing decision to original Nex source while preserving third-party licensing and tracking source-delivery and artwork requirements.
- Added the contributor rules, platform plans and initial QA evidence.

## [0.0.1-alpha1] — 2026-09-08

### Miscellaneous

- Initialized the repository.

---

Verification and remaining limits: see the
[Controller foundation checks](docs/qa/phase1/checks.md),
[appliance acceptance](docs/plans/2026-09-08-controller-appliance.md),
[security checks](docs/qa/security/checks.md), [appliance rebuild](docs/qa/appliance/usability-rebuild.md),
[latest installer DHCP checks](docs/qa/iso/2026-09-09-installer-dhcp.md),
[pool master lab checks](docs/qa/pool-master/lab/checks.md),
[add host lab checks](docs/qa/simple-host-enrollment/lab/checks.md) and the
[console features checks](docs/qa/console-features/checks.md).
