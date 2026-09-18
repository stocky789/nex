# Changelog

All notable changes to **Nex** are recorded here in clear, non-technical language suitable for release notes and internal comms.

This project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Release tags use a `v` prefix (for example `v1.0.0`). Release sections use the same four buckets throughout: **Major Feature**, **Minor Feature**, **Bug Fixes**, and **Miscellaneous**.

> **Version history note:** Early sections reconstruct development **stages** from commit
> history; a section alone does not establish that a version was published. Nex remains
> development software. Builds use the newest numbered changelog heading (`0.3.4` →
> Debian `0.3.4`).

## [Unreleased]

> **Maintainers:** Leave this section empty. Append new entries under the **latest numbered version** heading below (the first `## [x.y.z]` block after this one) until that version is tagged and published.

## [0.3.4] — 2026-09-18

### Bug Fixes

- Storage browser checks wait for the actual credential or discovery request before
  testing delayed replies, avoiding false CI failures on busy runners (BUG-0291).
- Installation media now compresses the complete Windows driver ISO and avoids
  redundant source payloads and stale cached packages; offline installation and
  installed drivers remain unchanged (BUG-0289, BUG-0290).
- Core packages share one identical Nex source archive between the host and bundled
  Controller appliance, preserving both installed source-access paths.

### Miscellaneous

- Local development ISO measurements fell from 3.54 GB to 2.40 GB (32% smaller).
  These measurements compare the recorded 0.3.2 and optimized 0.3.3 builds;
  a 0.3.4 artifact has not yet been built or published.
- GitHub and Gitea installer checks cover compressed driver restoration and source
  retention; release workflows report artifact sizes. Offline installation, first
  boot, nested KVM and reboot passed in a disposable VM with no network interface.

## [0.3.3] — 2026-09-18

### Minor Feature

- VM creation, edits and persistent media changes now receive pool-master approval
  using current Core resource checks, with rejections shown in Task history.
  Upgrade Controller, then Core on the selected host and pool master; older Core
  versions block these changes rather than bypassing approval.

### Bug Fixes

- Gitea releases are published only after every asset finishes uploading and passes its
  reported-size check; handled upload failures remove the incomplete draft (BUG-0281).
- Browser acceptance checks allow normal page loading and inventory refresh times
  on busy runners without lengthening ordinary control checks (BUG-0282, BUG-0283).

- Retrying a completed storage registration returns the original repository and
  task instead of incorrectly reporting changed input (BUG-0279).
- VM startup checks the most specific matching storage repository and rejects
  ambiguous ownership, preventing a broader registration from bypassing checks
  on a nested repository (BUG-0280).
- Default-resource tests no longer depend on the build machine's libvirt
  directories (BUG-0278).

### Miscellaneous

- Both release workflows now start the actual Controller package and verify its
  database, bundled UI/source and administrator login before publication. Push
  and pull-request workflows also check that packages without working SQLite
  support are rejected.
- Updated API references, operator guidance and roadmap with bounded package,
  appliance, offline installation and native two-host placement evidence. Those
  labs used recorded 0.3.2 development snapshots, not rebuilt 0.3.3 artifacts;
  Phase 3, Phase 4 and final release acceptance remain open.

## [0.3.2] — 2026-09-17

### Minor Feature

- The Core ISO now bundles Fedora virtio-win 0.1.302 with its matching Fedora
  corresponding-source RPM and notices, so a fresh host seeds the Windows driver
  disc automatically. Earlier releases left that media out on purpose.

### Bug Fixes

- Fixed the enrollment browser check failing when Create VM opened slowly after New VM
  from a host view, and stopped Core from recording a failed default storage registration
  when `/var/lib/libvirt/images` is not there yet.
- Host inventory under load starts the libvirt domain list sooner so a short probe budget
  cannot skip it, and Add host gives enrollment enough time to finish under busy CI.
- A Core host update that is still installing is no longer reported as finished, and shutting
  Core down waits for an update it started instead of leaving the install running behind it
  (BUG-0274).
- Publishing a release survives a connection dropped part-way through uploading the ISO: each
  file is resent instead of failing the whole release, and a file the forge stored short is
  rejected rather than published as a broken download (BUG-0275).
- Browser checks give a busy build machine enough time to start the Controller, and print the
  Controller's own log when it never answers (BUG-0276). Two host-agent checks now measure the
  behaviour they describe instead of how fast the build machine is (BUG-0277).
- A guest that put itself to sleep (Windows Sleep, for example) no longer locks up its VM
  and every other VM on the same host. The VM is shown as asleep, **Start** wakes it, Shut
  down and Reboot wake it first, and Force stop and Force reboot work directly. New and
  re-saved VM definitions remove ACPI sleep and hibernate from the guest, so guests no longer
  offer them (BUG-0273).

### Miscellaneous

- Phase 2 (one-host VM management) is closed: create/install/console/reboot/persist and
  Controller outage recovery proven earlier; MAC hot-plug and admission re-checks on 0.3.1;
  VirtIO media on this release.
- Licensing and release checks require the virtio-win source tree whenever the
  ISO is staged; the old refuse-without-source gate is replaced by that retention
  check.

## [0.3.1] — 2026-09-17

### Minor Feature

- VM realtime stats now expand beneath the console, including its separate window, so the
  guest display and live usage charts can be watched together. Historical charts stay in Stats.
- New VMs default to a fixed CPU definition shared by the current pool members, discovered
  by Nex Core through libvirt. The selected model and features stay with the VM when the
  pool changes. Manual CPU choices, including `qemu64`, remain available; there are no
  operating-system-specific CPU rules. Upgrade Controller first, then every Core.

### Bug Fixes

- Explicit CPU feature definitions are checked against the host runtime before creation,
  stopped edits and Core-mediated starts. Unavailable pool members prevent a new common
  baseline from being offered; existing VM definitions remain unchanged (BUG-0272).
- Stacked CPU charts now give each visible vCPU or host core its own 100% share of the axis,
  so four fully busy CPUs fill a 400% chart.

### Miscellaneous

- Reconcile the platform, VM creation and public roadmap with the native CPU baseline work.
  Native runtime checks and simulated browser tests pass; heterogeneous physical pools,
  live migration and the original installer boot-loop diagnosis remain unverified.
- Public roadmap wording now matches published 0.3.0/0.3.1: usage history, delete retries and
  the Controller package start fix are marked built. Phase 2 closed later the same day under
  0.3.2 after the VirtIO ISO and focused lab close-out.

## [0.3.0] — 2026-09-17

### Minor Feature

- VMs and hosts have a **Stats** tab with CPU, memory, network and disk charts (and load for hosts)
  from the last 10 minutes back to a year, recorded by each Nex Core host so a replacement
  Controller shows the same history. Charts offer a **Stacked** toggle, hover values, drag-to-zoom
  and series toggles, and a **Realtime** drawer shows 1-second samples on request. Upgrade Nex
  Controller before Nex Core.
- Pushes that change the public website now build and publish the static site when website
  deploy secrets are configured, separate from ISO and APT release publication.

### Bug Fixes

- Right-click **Delete…** on a VM now opens the confirmation dialog again (BUG-0218).
- Inserting or ejecting the driver disc from the VM console no longer fails with "invalid JSON
  request", and the same ISO can no longer be placed in both CD drives (BUG-0235).
- Creating or editing a VM now refuses a driver disc the host cannot attach or has not imported
  (BUG-0228).
- An interrupted change from UEFI to BIOS now finishes on recovery instead of leaving the VM
  without a definition (BUG-0219).
- Changing a VM from UEFI to BIOS now warns that its UEFI variables will be deleted and needs
  confirmation before saving (BUG-0225).
- One save that renames a stopped UEFI VM and changes Secure Boot no longer ends in
  "needs attention" (BUG-0220).
- A VM edit that removes or reorders network interfaces no longer changes the wrong interface
  (BUG-0226).
- An interrupted VM delete now finishes when retried, and retrying after changing the disk choice
  no longer fails with "request_id already used" (BUG-0221, BUG-0227).
- Disks permanently deleted with their VM no longer block detaching the storage (BUG-0222).
- A queued storage, network or ISO row no longer lingers after it succeeds (BUG-0236).
- A failed VM creation now shows the failure and a link to its task instead of "Object not found"
  (BUG-0237).
- The VM Activity tab now lists the VM's own console, power, media, edit and delete actions
  (BUG-0238).
- **Add host** errors such as "already enrolled" now stay visible in the form (BUG-0239).
- Power confirmation buttons keep their warning and primary styling (BUG-0240).
- A VM on an unreachable host now shows its last reported state (BUG-0241).
- **Open console** is fully visible on narrow screens in the host-local page (BUG-0242).
- Console clipboard and display resize no longer report ready before a guest OS is running
  (BUG-0243).
- Healthy host checks now show when they last succeeded (BUG-0244).
- Deleting a VM from its page now shows the "Delete queued" notice (BUG-0245).
- The `nex-controller` package now contains a Controller that starts. Packages from 0.1.6~alpha1
  through 0.2.9 were built without the database driver and exit at start (BUG-0246).
- An interrupted VM delete that resumes no longer briefly shows "needs attention" (BUG-0247).
- A VM that is still being created shows its creation progress, not Edit and Delete (BUG-0248).
- The "Start queued" notice now disappears once the task finishes (BUG-0249).
- The Guest tools badge shows the agent's state instead of "stale" (BUG-0250).
- Starting a Controller appliance now asks for confirmation and warns against running two
  Controllers (BUG-0251).
- The delete dialog's disk checkbox sits next to its label again (BUG-0252).
- VMs on an unreachable host are greyed in the resource tree instead of looking running
  (BUG-0253).
- Removing a network interface and adding a new one in the same VM edit now saves, and the new
  interface always gets its own MAC address instead of the removed one's (BUG-0254).
- Adding a network interface to a running VM no longer reuses the MAC address of an interface the
  guest has not yet released. Nex Core now refuses that change until the guest releases the old
  interface or the VM is stopped, and new interfaces no longer get a recently removed address
  (BUG-0256).

### Miscellaneous

- Tidied the VM right-click menu layout, icons and disabled-state colours.
- Removed the sample age row from the VM usage panel.

## [0.2.9] — 2026-09-16

### Minor Feature

- Per-VM CPU, memory and disk usage reported by the host is now displayed in the VM details
  panel, including sample age and explicit unknown values when a metric was not reported.
- Failed ISO imports now offer a confirmed **Discard failed import** action in the ISO library.
  The failed task and audit history remain available after its library row is removed.

### Bug Fixes

- UEFI firmware variables now stay with a VM when it is renamed. Changing a stopped VM from
  UEFI to BIOS removes its old variable file safely, including after an interrupted change.
- Turning Secure Boot off and back on now preserves the VM's boot entries and enrolled keys.
- A VM creation rejected by Nex Core no longer leaves an unknown Controller-only VM in the
  inventory. Its failed task remains available and the same request can still be replayed safely
  (BUG-0217).
- New installations include the current OVMF Secure Boot certificate template, with the 2023
  Microsoft UEFI and Windows UEFI certificate authorities verified during installation checks.

### Miscellaneous

- Release builds now include a matching source bundle and compliance manifest with checksums,
  and leave six unreviewed firmware packages out of the ISO.
- The public ISO does not bundle virtio-win driver media. Import matching driver media into the
  host ISO library before selecting it in a Windows VM's driver disc drive.

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
