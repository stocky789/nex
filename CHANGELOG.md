# Changelog

All notable changes to **Nex** are recorded here in clear, non-technical language suitable for release notes and internal comms.

This project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Release tags use a `v` prefix (for example `v1.0.0`). Release sections use the same four buckets throughout: **Major Feature**, **Minor Feature**, **Bug Fixes**, and **Miscellaneous**.

> **Version history note:** Early sections reconstruct development **stages** from commit
> history; a section alone does not establish that a version was published. Nex remains
> development software. Builds use the newest numbered changelog heading (`0.4.10` →
> Debian `0.4.10`).

## [Unreleased]

> **Maintainers:** Leave this section empty. Append new entries under the **latest numbered version** heading below (the first `## [x.y.z]` block after this one) until that version is tagged and published.

## [0.4.10] — 2026-09-24

### Minor Feature

- **Set BMC access** has a **Detect** button that checks whether Redfish or IPMI answers and
  fills in the system ID.

### Bug Fixes

- BMC Detect removes its temporary password from Nex Core after a probe and expires it if the
  probe never runs.
- Fence profiles accept Redfish system IDs with dots, such as `System.Embedded.1`.
- Host Networking shows the current addressing, VLAN, gateway and DNS for Nex-managed
  interfaces; its Refresh button, interface labels and Addressing controls are clearer.
- Host console text uses normal mixed-case letters, and terminal colour codes no longer
  appear as junk such as `[32m`.
- Host overview has one **Interfaces & VLANs** button for Networking.
- VM and host usage sparklines use thinner, smoother lines for busy disk and network traces.

### Miscellaneous

- Documented the planned pool VLAN host-uplink mapping; it is not implemented yet.

## [0.4.9] — 2026-09-24

### Minor Feature

- VM and host side panels can be dragged wider or narrower from the left edge; the width is
  remembered in this browser.
- The top bar's activity pill opens a short task list instead of jumping straight to **Tasks**;
  **Open Tasks** still reaches the full page.
- ISO upload progress, rate and Cancel show on Storage (upload form or ISO library) as well as
  on Tasks.

### Bug Fixes

- Containers rail item active/hover fill covers the full label (and matches other rail items'
  width).
- A fresh pool whose management NIC used a VLAN tag now shows that management bridge as
  **nex0 untagged** (or the matching uplink) on **Networks** and in the create-VM network list,
  instead of staying empty.
- Installer disk summary and review state what the selection installs: one system-disk size on
  all selected drives as RAID1 copies (not pooled storage), and how many disk failures it survives.
- Install progress shows **Still working…** and a live **Installer activity** log while a step
  is quiet, so a long preseed or package stage does not look hung.

## [0.4.8] — 2026-09-23

> **Development working-tree record, not a published release.** The console redesign below was
> checked with unit and Go tests, the phase 1 browser lab, the CI browser checks and captures
> against a labelled simulated inventory ([QA checks](docs/qa/controller-console-redesign/checks.md)).
> No real host was exercised, and the tags Controller-replacement lab check has not run.

### Minor Feature

- Controller web UI design tokens: one token sheet with a dark theme that follows the system
  setting (with a toggle), flat surfaces on the brand palette, operator labels instead of internal
  identifiers, relative times, and a grouped console toolbar with **Focus mode**. Status badges
  keep their square dot-and-text form.
- Controller console redesign: a labelled icon rail (Overview, VMs with Containers under it,
  Hosts, Storage, Networks, Backups, Tasks, Events, Settings), a top bar with the site and pool
  scope menu, global search and a running-tasks indicator, a pool overview with usage, placement
  and a **Needs attention** list, filterable VM and host lists whose details open in a side panel,
  bulk VM start and shut down, and pool settings. **Storage** and **Networks** show each
  repository's and network's health with a light per host, and the VM list can be filtered by
  network. Overview sections can be collapsed, and the usage chart can be read point by point
  (longer ranges show averages). Pool members show short status checks with details on request,
  VM guest tools show a compact status grid, and long explanations sit behind **About** links.
  VMs can carry tags, kept on the pool master (needs Nex Core 0.4.8 or later there). Hosts
  show uptime and network link speed where Nex Core reports them, and the pool members report says when each host joined. Backups,
  containers, snapshots, migration and high availability appear in their place but stay
  unavailable until their phase lands. The toolbar, resource tree and **Tasks** strip are gone;
  see [Navigate the Controller console](docs/guide/navigate-the-controller-console.md). Upgrade
  Nex Controller before Nex Core: an older Controller cannot read a 0.4.8 host's reports and
  shows it as stale.

### Bug Fixes

- Create VM keeps the wizard open when you open Storage or Networks from a side link, so returning
  to VMs resumes the form instead of showing the list.
- Saving VM tags re-reads the VM first so a stale list cannot rewrite disks or networks; tag edits
  no longer reset while you type; clearing tags still works if the host lost tag support.
- Host and VM panels refuse deep links outside the current site or pool scope. Host stats refresh
  in one batch, and the host Performance tab reuses the list's recent history.
- Large VM lists scroll without drawing every row at once. Overview and Hosts share one host-stats
  fetch, and console screens load in smaller pieces instead of one large download.

## [0.4.7] — 2026-09-23

### Bug Fixes

- Pool members now take on large shared ISOs, such as a Windows installer, instead of
  reporting `pool-media-reconciliation deadline` forever (BUG-0541).
- A pool master no longer reports its members' authority replica as stale a minute after
  every successful push (BUG-0542).

## [0.4.6] — 2026-09-23

### Bug Fixes

- A host that has never run a VM can report its VMs to the pool, and a VM creation it refuses
  is recorded so the pool master frees the name (BUG-0539).
- A Controller deployment interrupted while copying the appliance disk now starts again on
  **Retry deployment** or after the host service restarts, instead of staying stuck with a
  half-copied disk (BUG-0540).

### Miscellaneous

- The installable host and its Controller bootstrap passed the whole chain in disposable
  virtual machines, including a Controller deployment interrupted part-way, and master-owned
  VM placement passed its fault and recovery checks. Real Intel and AMD hardware remains
  untested.

## [0.4.5] — 2026-09-23

### Minor Feature

- **Add host to pool** fills in the joining host's address by itself: each host reports the
  address a pool master can reach it at, and the dialog shows only that address, prefilled.
  The `https://` and the port are never typed; change the address only if the master must
  reach the host another way.

### Bug Fixes

- The installer's disk list spreads its columns across the available width, and dragging a
  column divider now actually resizes the column instead of doing nothing (BUG-0529).
- Both installer wizards now show and record the serial of SATA, SAS, SCSI and virtio disks;
  previously only NVMe disks had one, so the erase review and the last-moment "serial changed"
  guard were blank for most hardware (BUG-0530).
- **Add host** now works against a bundled Controller deployed with DHCP when the Controller
  address is given by IP: the host is told the hostname the Controller's certificate carries,
  instead of failing with "cannot validate certificate ... doesn't contain any IP SANs". The
  manual `nex-core-agent enroll` gains `--server-name` for the same case (BUG-0531).
- **Add host to pool** no longer looks like the place to add a brand-new host: it explains that
  it joins a host already enrolled in the site, offers **Enroll a new host…** when there is none,
  and asks for the peer address only after a host is chosen (BUG-0532).
- The ISO builds again: the pinned UEFI firmware package is fetched from a fixed Debian snapshot
  instead of Debian's moving development suite, which had dropped that version (BUG-0533).
- A freshly installed host can upload ISOs to its **Local storage** again; the media folder the
  host created for the bundled driver disc was unreadable to the hypervisor (BUG-0534). Hosts
  installed from 0.3.0 onward can be repaired with `chmod 0711 /var/lib/libvirt/images/nex-media`.
- After a host joins a pool, its own untagged network is shown in that pool instead of under the
  removed single-host pool (BUG-0535).
- A VM creation interrupted before any disk was made now ends as a plain failure that frees the
  name, instead of leaving a VM that could neither be used nor deleted and that blocked removing
  its host from the pool (BUG-0536).
- An edit that lost the pool master's reply, and a VM deleted while such an edit was unresolved,
  no longer leave the pool master holding that VM for ever (BUG-0537, BUG-0538).

## [0.4.4] — 2026-09-22

> **Development working-tree record, not a published release.** Mirrored system-disk install,
> system-disk and ZFS disk replace, hot spares, the host Version light, and the staged-review
> fixes below were checked with runnable harnesses, nested labs and simulated browser review
> where noted in the QA records. Physical multi-disk install, ESP-detached boot, and live
> failed-member replacement on real hardware remain outstanding where those records say so.

### Minor Feature

- The Nex Core installer can put the system disk on a **Linux md RAID1 mirror** with an **ext4**
  root: tick two or more disks at install time. The host keeps running if one of those disks
  fails; replace the failed disk from the host's **Stats** tab.
- **Replace disk** on a degraded ZFS repository (or a degraded system mirror) clears a spare and
  rebuilds onto it from Nex Controller. ZFS pools may also take hot spares and a three-disk mirror
  or RAIDZ3 when every selected host supports disk replace.
- A host's **Version** light shows whether its Nex Core matches the newest release in the update
  archive (green), has an update (orange) or a new major release (red).

### Bug Fixes

- Installer wizard fits BMC/IPMI consoles better at 800×600 and 1024×768: no clipped disk
  summary, no nested sideways scrollbars, tighter chrome on small displays.
- A ZFS repository whose pool is still serving after a disk failure stays readable and writable
  and shows as degraded, instead of being taken out of service until the disk is replaced
  (BUG-0443).
- The host Version line now names the state in text (up to date, update available, or major
  release behind) beside the colour light, so the meaning does not depend on hover or colour
  alone (BUG-0524).
- Checking for updates on an offline Controller no longer runs the same failing archive refresh
  twice when it also looks up the newest Nex Core (BUG-0523).
- Install and replace-system-disk guide pages now agree on ESP `nofail`: this build mounts with it;
  older and hand-built mirrors may still need the console steps (BUG-0519).
- The ZFS replace guide no longer says an ONLINE disk appears in the failed-disk list; those disks
  are not offered (BUG-0520).
- The host Version light no longer pulses green for plain Debian (or other non-Nex-Core) OS
  strings, and no longer misreads apt epoch or package revisions. A pre-release host against the
  matching final release now shows an update available, and a pre-release archive candidate is not
  offered as an upgrade over that final. Signing out clears the light so a non-administrator does
  not keep the previous admin's archive answer (BUG-0470, BUG-0471, BUG-0472).
- On a host Stats tab, the system disk mirror now keeps following a rebuild and a submitted
  replacement on its own. The progress percentage and the success or failure notice used to freeze
  until Retry was pressed by hand, because each history refresh restarted the follow timers
  (BUG-0448).
- When Replace disk… finds no usable spare, the dialog now says every disk is in use. It used to
  claim every spare was too small even when size was never the problem; that sentence is reserved
  for when every offered disk really is undersized (BUG-0449).
- Replace disk… is hidden while a system array is rebuilding, and Nex Core refuses a second
  replacement until recover, resync or reshape finishes (BUG-0473).
- Healthy system arrays are summarised by their RAID level instead of always as a mirror, and
  Replace disk… is offered only for RAID1 (BUG-0474).
- Temperature gauges announce Warm or Critical in the meter value, not only as coloured text
  (BUG-0475).
- The system disk panel no longer calls an older Core or an unavailable md report a single
  unmirrored disk; it shows the upgrade or host reason instead (BUG-0476).
- Hardware table headers keep their underline, the system disk block matches the other Hardware
  cards, and the Healthy state meets text contrast on the Stats canvas (BUG-0477, BUG-0478).
- Replace disk… stays hidden until the host also supports storage discovery, so the dialog does
  not open into an immediate discovery refusal (BUG-0479).
- Hot spares and widened ZFS layouts (three-disk mirror, RAIDZ3) are refused unless every
  selected host and the pool master support disk replace; the wizard hides Spare and those
  layouts without that capability (BUG-0491).
- Pool detail on ZFS repositories is withheld from reports until the Controller acknowledges
  disk-replace support, so an older Controller no longer rejects every host report (BUG-0492).
- Replace disk… on a system mirror now uses the same disk table as ZFS replace, with an extra Fit
  column for disks that are too small (BUG-0481).
- Replacing a system disk and replacing a ZFS pool disk now take the same host lock, so two
  replacements cannot wipe the same spare at once (BUG-0482).
- A system-disk replacement that failed while installing the bootloader can be retried after the
  mirror finishes rebuilding; it no longer refuses the new disk as already in sync (BUG-0483).
- Choosing a disk that still shows as a faulty mirror member is refused instead of reported as a
  successful repair (BUG-0484).
- On UEFI hosts, when `/boot/efi` is empty because the failed disk held that mount, the replacement
  copies boot files from the surviving disk's EFI system partition (BUG-0485).
- Replacing a failed ZFS disk shows the replacement's by-id name beside the erase confirmation,
  so the name to type is visible without hovering the serial (BUG-0487).
- The guide for replacing a failed ZFS disk now points at **ZFS storage by host** and its
  **Action** column, where **Replace disk** actually appears (BUG-0486).
- A registered ZFS dataset now shows the same pool detail line (failed disk, resilver,
  error counts) under Host access instead of only the word degraded (BUG-0488).
- A running ZFS resilver percentage now matches the size figures beside it (BUG-0489).
- A ZFS pool that is still ONLINE but has leaf read/write/checksum errors, or a ZFS status
  sentence, is flagged for attention with the offending device named (BUG-0490).
- Replace disk on a ZFS pool is hidden while a resilver is running or the failed leaf is already
  being replaced, and Nex Core refuses a nested replace or an already-healthy member before wiping
  another disk (BUG-0493, BUG-0494, BUG-0495).
- A ZFS replacement that is smaller than the failed disk is refused before wipe, and a resume after
  wipe-started still requires erase confirmation if the disk now holds data (BUG-0496, BUG-0497).
- A ZFS hot spare that needs erase is wiped with the data disks, an ineligible spare is refused
  before wipe, and `zpool create` turns on automatic spare engagement when spares are selected
  (BUG-0498, BUG-0499).
- A timed-out storage probe no longer keeps the previous ZFS pool detail, and **Replace disk** on
  **ZFS storage by host** stays hidden until the host connection and pool observation are both
  current; a stale pool line is marked **Last report** (BUG-0500).
- A ZFS hot spare can no longer be left on a single data disk by unticking after Spare; the wizard
  and payload both re-apply the two-data-disk rule (BUG-0501).
- ZFS pool status and action sentences are visible under the summary line, and a long degraded
  summary wraps instead of stretching the Storage table (BUG-0502).
- **Replace disk** follows the live pool observation while open, so a leaf that came back online
  reaches the empty state instead of a stale failed list (BUG-0503).
- ZFS replace guide: resilvering spelling, erase-button placeholder, and post-wipe Needs attention
  outcomes (BUG-0504, BUG-0505).
- On a mirrored install, losing the disk whose EFI partition fstab named no longer leaves GRUB
  upgrades writing into an empty `/boot/efi` directory: the host remounts a surviving member's
  ESP, rewrites that fstab UUID, and keeps copying boot files to the other members. The ESP mount
  also uses `nofail` with a short device timeout so a missing ESP cannot drop a healthy mirror
  into emergency mode (BUG-0506).
- Minimal (text) install now preselects a lone eligible disk, matching the graphical installer
  (BUG-0508).
- A single-disk install on a machine that still has an old software RAID on unselected disks no
  longer records those foreign arrays in the new host's mdadm.conf or initramfs (BUG-0509).
- The pool master refuses hot spares and widened ZFS layouts on a member that lacks disk-replace
  support before posting the selection, instead of forwarding an opaque peer refusal (BUG-0510).
- Replacing a ZFS disk on a member waits long enough for wipe and `zpool replace`, and a dropped
  peer connection no longer cancels work already started on that host (BUG-0512).
- Minimal install no longer walks every network adapter by default. After you pick the
  management adapter, a Yes/No asks whether to configure more; Enter means no and leaves the
  rest unconfigured. Choosing yes still offers **Disabled**, **dhcp** or **static** on each leftover
  adapter (BUG-0444).
- Graphical and text installers now share the same disk and network wording: mirror capacity uses
  the smallest disk size rather than calling it usable, disks under 16 GiB are omitted from both
  lists, leftover adapters say **Disabled** in both frontends, and the text disk list names each
  serial (BUG-0514–BUG-0518).
- A failed ESP copy on a mirrored host no longer leaves a member's EFI partition mounted after
  apt (BUG-0519).
- Disk selection refusals use one shared 10% mirror rule sentence, and CI pins the 5% accept /
  over-10% refuse boundary for both frontends (BUG-0520, BUG-0525).

## [0.4.3] — 2026-09-22

> **Development working-tree record, not a published release.** Nothing below has been seen on a
> physical server. The hardware panel was checked against simulated hosts only — fixture sensor
> trees, stub inventory tools and mocked replies in a browser — and the hardware walkthrough is
> still to come. Host fencing was settled in a nested virtual laboratory and by runnable checks
> only: no physical management controller has been contacted, no host has been powered off, and
> no physical watchdog has been read, armed or expired. Every timing quoted for it comes from
> nested runs. Everything below was reviewed independently before commit; the twenty-three defects
> that review found in the unreleased work are fixed and recorded as BUG-0418 to BUG-0440 in the
> bug ledger rather than listed here, because no build ever carried them.

### Minor Feature

- A host's **Stats** tab now shows what the machine is and how hot it is running. A **Hardware**
  panel above the charts lists the server and its firmware, processor, memory modules, disks, PCI
  devices and temperature sensors; the host reads all of it fresh when the tab is opened and keeps
  none of it, so a replacement Nex Controller shows the same thing. The disk list holds installed
  disks only, never compressed-memory, loop or optical devices. A section the host could not
  read says so, with its own reason, and the rest still appears.
- A row of **gauges** shows current CPU and memory use and a dial for every temperature sensor.
  Warm and critical are the chip's own limits, not a number chosen by Nex; a sensor that publishes
  no limits shows a plain reading, and a reading that has stopped arriving shows a dash instead of
  a stale number. Gauges appear on the **Last 10 minutes** range only.
- A **Temperature** chart joins the CPU, memory, network, disk and load charts, with a year of
  history kept by the host like the rest. A host keeps up to 24 sensors, CPU package and NVMe
  readings ahead of individual drives, so a machine full of disks still charts its processor. A
  sensor that stops answering shows a gap after two samples and never holds up the rest of the
  host's usage sampling. Hosts with no sensors, which includes most virtual ones, say so rather
  than showing an empty chart.
- Both **Stats** tabs were restyled: a lighter page with white cards and a clearer set of chart
  colours, shared by the charts and the new gauges; every plotted line keeps at least 3:1
  contrast against its card. VM Stats tabs have no hardware panel or gauges, because a VM has no
  hardware of its own.
- **You no longer record fence devices.** Each host's own Nex Core finds its management
  controller, creates a power-control-only account for the pool on it, pins the controller's
  certificate and reports what it found on the host's row, where the account can also be
  withdrawn again. Where a controller cannot be configured this way, **Set BMC access…** still
  takes credentials you supply, and a device you record yourself always wins. A controller that
  refuses the account is retried every five minutes, not on every pass, so its settings memory is
  not worn down. The identities, accounts and pinned certificates are kept by the pool master, so
  a replacement Nex Controller sees the same fencing.
- A **Fencing** column on the pool's **Hosts** tab answers one question per host: if this host
  were lost, could another host shut it out? Every few minutes each host signs in to every *other*
  host's management controller and checks which machine answers — a check can never power
  anything off. A host that could not be shut out says why, **What each host found** shows each
  checker's own result, and the pool line reads *Fencing: ready for N of M hosts*.
- A pool-wide **Host fencing** switch under the members table, **on** for every pool unless you
  turn it off. Where the pool's prerequisites hold — three to five hosts, no clustered storage,
  and a hardware watchdog on the host — Nex Core forms one cluster for the pool and arms each
  host's watchdog, one host at a time, so a host that loses the pool's majority is reset by its
  own hardware and its virtual machines stop. That consequence is shown beside the switch whether
  or not you open it, and every host's row says in plain words what it is doing or what is
  stopping it. Pools of one or two hosts, pools running clustered storage and pools larger than
  five arm nothing and report why; two-host pools are shut out through their management
  controllers. A host entering maintenance puts its own watchdog down before it leaves the pool's
  cluster, and turning the switch off releases that cluster so clustered storage can be created
  afterwards. A refused change to the switch is shown beside it, not as a failed page load.
  Promoting a survivor after master loss now names the method it will use for each host,
  including a lost host's own armed watchdog after its waiting period, and still refuses until
  the evidence exists; a master that its own watchdog reset stays restricted on its next boot
  exactly as a master shut out through its controller does.
- **Add host to pool** no longer asks for the pool master's address. The master works out the
  address a joining host should use from its own pool-peer listener and records it itself, so a
  join needs only the host you are adding; an address you supply is still accepted as an
  override.
- The pool's **Hosts** tab was reorganised. Each row's actions — maintenance, fence device,
  promotion, master change and eject — are in one **Actions** menu in a trailing column, the
  master's row no longer offers actions that can never apply to it, and every column but
  Convergence is sized to its content.

### Bug Fixes

- A replacement Nex Controller can now recover a pool whose master is gone for good. Previously
  the first survivor to re-enroll was recorded as the pool master, every other member's
  enrollment was held, and promoting the survivor was refused with "this host is already the pool
  master". A host's own report naming a different master now corrects the record (BUG-0414).
- Update comparison follows Debian's own version ordering, so the upgrades a host offers match
  what APT would install. A version with an explicit revision, such as `0.1.0-dev`, is no longer
  treated as older than `0.1.0`, and a failed comparison never reports an update as available or
  as successfully installed (BUG-0416).

### Miscellaneous

- Detaching SMB or iSCSI storage now removes its credentials from the host. The unused-credentials
  clean-up panel is gone from the Storage page.
- Forge releases no longer attach standalone `nex-core-local` or `nex-controller` `.deb`
  downloads. Install from the ISO; enrolled hosts still update through the signed APT archive.
- Publishing a release no longer skips the website download tree or APT archive just because
  the ISO includes ZFS. With deploy secrets set, one Release run updates APT, downloads, and
  the static website pages.
- Removed obsolete manifest-based rules that blocked website or APT publication when the ISO
  listed storage packages. Release publish depends on deploy secrets only.
- A Release run now checks its deploy secrets and builds the website before it creates the forge
  release or moves the version tag, so a broken website build stops the run before anything is
  published. Each website publish gets its own release id, so a second run at the same version
  and a prerelease version both publish cleanly.
- Nex Core now installs `dmidecode` and `pciutils`, which it uses to read memory modules and PCI
  devices, and loads the kernel's `drivetemp` module so SATA and SAS disks report a temperature.
- A host tells Nex Controller whether it can report its hardware (`host-hardware-v1`). A host
  running an older Nex Core simply shows no Hardware panel; its charts and usage history are
  unaffected.

## [0.4.2] — 2026-09-21

> **Development working-tree record, not a published release.** Installer fix only; verified
> with simulated disks and not yet installed on physical hardware.

### Bug Fixes

- Installing onto a disk that was used before, for example one member of a software RAID or LVM
  storage from a previous hypervisor, no longer stops at the Disk step with "No root file
  system". The installer now clears the old partitions, RAID membership and LVM from the selected
  disk only. The other disks are not written; an array they still form is left degraded. The
  installation log view now includes the partitioning log (BUG-0413).
- A newly installed host no longer imports ZFS pools left on disks that were not selected for
  installation. Pools Nex Core creates are imported as before; an old pool can still be
  imported deliberately. The install guide now covers hardware RAID controllers and internal
  SD-card boot modules.

## [0.4.1] — 2026-09-21

> **Development working-tree record, not a published release.** Installer work only; the
> Phase 4 acceptance scope and limits recorded under 0.4.0 are unchanged. No 0.4.1 ISO has
> been built or installed, so both entries below are verified at source level only.

### Minor Feature

- **Minimal install - Nex Core (text)** now runs the same setup as the graphical installer,
  in a keyboard-only interface. It asks the same questions, applies the same password and
  disk rules, offers the same Go Back navigation and the same named erase confirmation, and
  installs the same host, including the `br0` management bridge, MAC-bound `nexN` interface
  names and the chosen VLAN and DNS. Previously this mode ran Debian's own installation
  questions and produced a host with different networking. The serial console entry still
  runs Debian's questions.

  It offers the same **Offline (no management network)** choice as the graphical installer, and
  a machine with no network adapter is told so and installed offline instead of setup refusing
  to start. The keyboard layout must actually be applied to the console before the root password
  is requested; a layout this console cannot load is reported and asked again. The VLAN question
  is prefilled with `0`, so pressing Enter leaves an adapter untagged. The final review lists
  every adapter's mode, address, gateway and VLAN alongside the disk. Answering yes checks the
  complete settings before anything is written: a rejection names its own reason, erases nothing
  and returns to the questions with every answer still filled in. Hardware detection now runs
  before the questions start, so the detection steps no longer interrupt a question on screen.
  Both installers list network adapters in the same order, so a machine gets the same `nexN`
  interface names whichever mode installs it.

### Bug Fixes

- Installation no longer fails part way through with "nex-core-local : Depends: vlan but it
  is not installable". The installation media now carries the `vlan` package that Nex Core's
  tagged management networking needs, and the build refuses to produce media that is missing
  any package Nex Core depends on (BUG-0406).

## [0.4.0] — 2026-09-20

> **Development working-tree record, not a published release.** Phase 4 multi-host pools are
> accepted for disposable virtual-lab scope (2026-09-20); OD-6 strict unreachable-member rule
> confirmed. Physical/BMC, five-host, package-install and authentic owner-conflict limits remain
> outside this acceptance.

### Minor Feature

- Add a host to a pool from Nex Controller: the pool's new **Hosts** tab shows the membership the
  pool master reports and offers **Add host to pool** with a compatibility preview. The joining
  host performs the join itself and reports its own progress, so closing the browser or losing
  Controller does not interrupt or undo it. The host-local procedure remains available for
  recovery.
- Change which host is a pool's master, and eject a host from a pool, from the pool's **Hosts**
  tab. Both are typed confirmations and both are run by the hosts themselves: the pool, its
  networks, its storage, its ISO library and its virtual machines keep their identities and their
  names, virtual machines that are running keep running, and closing the browser or losing
  Controller entirely does not interrupt or undo the change. While a master change runs, other pool
  changes are refused with a message asking you to retry rather than queued, and the change can be
  cancelled until the authority has actually been handed over. A host that is not reachable can
  still be ejected; it finds out when it returns and becomes the master of its own new single-host
  pool, keeping its identity, its local storage and its local virtual machines. There is still no
  automatic succession: a master change needs the current pool master to be working.
- Put a pool host into maintenance from the pool's **Hosts** tab: the pool master then places no
  new virtual machine on that host and refuses starting one there. Guests that are already running
  are untouched, and stopping or shutting one down is never blocked. The pool master records the
  flag, so it survives a Controller restart and does not need the drained host to be reachable.
- Each pool member now keeps a signed, verified copy of the pool master's own records, and the
  **Hosts** tab shows whether that copy is current. Members never act on the copy by themselves;
  it is what the host you choose activates during a master change or a promotion, and it exists so
  the pool's records remain recoverable.
- Promote a surviving member to pool master when the pool master is **lost**. You choose the host
  and Nex Core proves the whole thing itself: it checks its copy of the pool records, checks that
  no other host holds a newer one, checks that every virtual machine is recorded the same way by
  the host that owns it, then powers the lost master off through a **fence device** you recorded
  earlier and verifies that it is off before it takes over. You see exactly what blocks before you
  confirm, and you confirm by typing the pool name and acknowledging the power-off separately.
  Limits, stated plainly: a fence device must be recorded **while the pool master still works**;
  only a `virsh` fence device can be recorded in this development build, so only a disposable laboratory pool
  can be promoted this way; there is still **no automatic succession**, and there is no force,
  override or "declare this host lost" anywhere, because none of them would be evidence. Every
  ready member of a pool with fence devices can power off the hosts those devices name — the same
  trust the pool's certificate authority already places in a member — so admit only hosts you trust.
- A host that cannot prove it holds its pool's current authority now says so and refuses to act:
  a restarted pool master that might have been fenced, and a returning old master that was, both
  show as **Restricted** and start nothing and change nothing about the pool until they have
  reconciled. Stopping a virtual machine, its console, local recovery access and the host's own
  reporting keep working, and the refusal holds with no Nex Controller connected. A virtual machine
  a returning host still defines that the pool now records elsewhere is kept, never started twice
  and never deleted.
- Change a host's management network from its **Networking** tab: stage the new addressing, review
  it against what the host reports now, apply it by typing the host's name, and confirm it once the
  host is back. The host owns the whole change — if it is not confirmed within the rollback window
  you chose, or if it reboots first, the host restores its previous configuration by itself, with
  no Controller involved. Renaming interfaces and editing guest networking are not part of this;
  running virtual machines are not affected.
- Record a **fence device** for a host in a pool, from the pool's **Hosts** tab, so a surviving
  member can be promoted if that host is ever lost. The credentials are handed to Nex Core once,
  over an encrypted delivery, and are never stored by Nex Controller.

### Miscellaneous

- Phase 4 multi-host pools accepted for disposable virtual-lab scope; OD-6 strict
  unreachable-member rule confirmed.
- Manual starts on a host in maintenance are refused on every pool host, including members.

### Bug Fixes

- Controller foundation CI and Release no longer run at the same time on the shared
  Gitea runner, which had been starving browser checks during ISO builds (BUG-0400).
- Release builds pin the offline package download to the same Debian snapshots as the
  source archive, so a new security package cannot land on the ISO without its source
  (BUG-0401).
- ISO installer GUI rebuilds fetch cdebconf from those same pinned snapshots, so the
  source step cannot run without deb-src configured (BUG-0402).
- Controller foundation CI no longer starts five heavy jobs at once on the shared Gitea
  runner, which had been killing runs mid-check and falsely failing Installer after a clean
  pass when Docker cleanup timed out.
- Controller foundation CI runs the browser lane alone before Go race, so stalled-probe
  heartbeats are not starved on the shared runner.
- Storage registration clears the password field as soon as credentials are handed off, so a
  slow reload cannot leave the secret in the open form (BUG-0397).
- Host ISO availability checks no longer treat a brief reconnect as missing transfer success
  during Controller foundation CI (BUG-0398).
- Controller foundation CI no longer runs push and pull-request labs at once on the
  shared runner, and certificate-rotation reporting gets a longer wait under load (BUG-0399).
- Release credential checks no longer crash on an unset version when only deploy secrets are
  under test.
- Cross-site pool join refusal and management-network cancel wording stay aligned with the
  live handlers in the API contract checks.
- Virtual machines on a pool master's local storage now start themselves again after the master
  reboots. Previously the request was made too early in the boot, while the host was still
  confirming it is the pool master, and nothing tried again (BUG-0392).
- Putting a host into maintenance now succeeds when that host is switched off or unreachable,
  instead of finishing as *needs attention*. The drain is recorded by the pool master either way,
  and the host picks it up when it returns (BUG-0393).
- A host put into maintenance is replicated to members straight away, and a member's libvirt
  start hook also reads that replica, so a drained host cannot start guests from a stale local
  membership copy (BUG-0387).
- Authority replica pushes no longer skip a member just because a GET looked healthy; only a
  prior successful POST ack short-circuits a re-push (BUG-0388).
- A join invitation that expired only by a small Controller clock skew is still collected;
  Nex Core remains the authority for the real deadline (BUG-0389).
- A returned pool master that cannot reach a ready peer now names that peer in its restriction,
  and can clear only when a fresh fence proof already excludes that peer — silence still never
  counts as agreement (BUG-0390).
- A master-change activation that fails after VM ownership was adopted rolls that adoption back,
  so a host is not left with half-applied authority (BUG-0391).
- A master change or eject now freezes pool authority as soon as the transfer is recorded, so
  other pool changes cannot slip in before the freeze finishes (BUG-0382).
- Add-host preview no longer blocks a host just because an earlier VM create or storage
  registration failed, and joining across sites is refused (BUG-0383, BUG-0384).
- A failed pool promotion only asks to “resolve and check again” when the refusal is a real
  conflict, not for every failure (BUG-0385).
- Maintenance, master-change and eject actions wait for a fresh membership generation before
  submit, and a restricted-host banner refreshes while it is still shown (BUG-0386).
- A pool network or storage registration the pool master refused can now be discarded. Previously
  it stayed in the list for ever with no action that could clear it, and a refused storage
  registration could not even be addressed (BUG-0325).
- Creating a virtual machine on a host whose access to a required shared repository, network or
  ISO the pool master last reported as failed or stale now returns a warning with the accepted
  request, and the task carries it while it waits. Nothing is refused and the pool master still
  decides; the warning only tells a request aimed at a problem apart from one that is merely slow,
  and it clears by itself (BUG-0326).
- **Roll back now** on an applied management network change now actually rolls it back. It used to
  submit a step Nex Core refused, so the only way out was to wait for the rollback deadline. A
  rollback you asked for is reported as such and no longer raises a failing health check
  (BUG-0327).
- A pool member whose VLAN failed to apply now re-checks the cause on every pass instead of only
  when the clock hour changed, so fixing the uplink converges within seconds rather than within an
  hour; and it no longer re-reports an old failure as if it had just been observed (BUG-0328).
- Adding a pool member to a replacement Nex Controller before its pool master has reported it now
  reads as the wait it is, with a one-click retry, instead of an error that looked like a broken
  host (BUG-0329).
- A clustered-storage request whose client disconnects is no longer answered as a success
  (BUG-0330).
- Cancelling an eject is now refused once the membership change is already written, and the eject
  completes instead of stopping half way. Nex Core reports whether a change can still be cancelled
  rather than leaving that to be guessed (BUG-0331).
- Fence devices now reach the host that would use them, so a promotion can actually fence
  (BUG-0332).
- **Found by independent review of this development work and fixed:**
  two surviving hosts that could not see each other could both be promoted, leaving one pool with
  two masters. A promotion now refuses while a ready host is unreachable unless that host is
  powered off by the same promotion, and the confirmation names every host that will be powered
  off — not only the old pool master (BUG-0333).
- A promotion could have powered off a pool master that was reachable and perfectly healthy, and
  two hosts could run promotions of the same pool at once. Both are refused (BUG-0333).
- The pool's virtual machine ownership records could still change while a master change was in
  progress, so the records handed over were not the ones that had been agreed. They are now
  deferred and settled afterwards (BUG-0334).
- A master change handed to a host that then died left the pool permanently without a master. The
  outgoing master can now resume, by powering that host off with your explicit confirmation
  (BUG-0335).
- A pool member could take over the pool in Nex Controller's view simply by claiming to be its
  master, and separately, connecting a member to a replacement Controller before its master could
  leave that pool stuck for ever. Both are fixed, and either connection order now works
  (BUG-0336, BUG-0339).
- A join invitation could outlive its fifteen minutes in the Controller database after a restart,
  and an expired one could be re-sent empty. The window is now swept once a minute, and an
  invitation nobody collected ends the task with a message instead (BUG-0337, BUG-0338).
- A host's own management network rollback no longer depends on the clock. A host cut off by its
  own change usually loses its time source with it, which is exactly when the rollback has to
  fire; it now measures the window from its own boot instead (BUG-0340).
- Applying or rolling back a management network change no longer takes the management bridge down
  and back up, which used to cut off every virtual machine attached to it until its domain was
  restarted. The bridge is reconfigured in place (BUG-0341).
- A management network change could be kept on evidence that never crossed it — a connection from
  the host to itself, or a report that left over a different network card. Only traffic that
  actually terminates on the management bridge now counts (BUG-0342).
- A management network rollback that started and could not finish now reports a failure, a spec
  that names an interface the host does not have is refused before anything is staged, and
  re-staging the same change with different settings is refused instead of silently replaying the
  old one (BUG-0343, BUG-0344, BUG-0345).
- **Maintenance mode now does its job for the commonest kind of guest.** Starting a virtual
  machine on a drained host's own local storage was not blocked at all, nor was resuming a
  suspended one. Both are refused now, on local and shared storage alike and whether or not the
  pool master is reachable, and a drained host no longer lets guests start themselves after a
  reboot (BUG-0346, BUG-0347).
- A join could report **failed** while the pool master already held the host as a ready member,
  which left no way to recover. Only the stages before the master can have admitted the host now
  report a failure (BUG-0348).
- The host-local join path no longer discards a join Nex Controller started, and the invitation is
  cleared from the joining host once it has been redeemed (BUG-0349, BUG-0350).
- A clustered-storage change is now refused while a pool master change is in progress, with the
  same retry message every other pool change gives (BUG-0351).
- A cancellation Nex Core actually carried out is now shown as the success it is, rather than as a
  failed master change, and each member's fence device line says whether its credentials have been
  replicated (BUG-0352).
- Nex Core's services now restart automatically after any exit, not only after a failure, so a
  management network rollback is not left unarmed by a crash (BUG-0353).
- A removed ISO no longer blocks detaching its repository, and a repository already detached from
  Nex no longer blocks joining a pool (BUG-0354, BUG-0355).
- An ejected host now releases pool resources in dependency order and resumes a blocked leave by
  itself once its named local dependency is cleared; it keeps its local VMs and never changes their
  networking to force the leave through (BUG-0356).
- Joining a pool now rehomes all copies of a member's retained local storage and media records, so
  local storage remains usable after a join (BUG-0357).
- An ejected membership row no longer prevents shared VM ownership from settling (BUG-0358).
- An ejected host can rejoin its former pool, and a verified local-only VM stays with that host's
  new pool; stale inventory still blocks ejecting when Core cannot prove shared VM safety
  (BUG-0363, BUG-0365).
- A VM deleted before an eject remains deleted when that host rejoins its former pool, and the old
  pool removes its stale ownership record (BUG-0368).
- A host rejoining its former pool can safely converge its existing shared networks, repositories
  and ISO records without manually removing and re-adding them (BUG-0369).
- The VM creation form now waits for the pool master's maintenance report before offering a host
  as a placement target (BUG-0370).
- The management-network form no longer creates horizontal overflow at a narrow viewport, while
  keeping its hidden field labels available to assistive technology (BUG-0371).
- A restricted host now refuses direct libvirt starts of managed local-storage VMs as well as
  API-initiated starts; independent appliance domains and shared-storage permit handling are
  unchanged (BUG-0374).

## [0.3.6] — 2026-09-19

### Bug Fixes

- Fixed Release builds failing after a successful ISO build when OpenZFS packages are present;
  forge releases publish again.

## [0.3.5] — 2026-09-19

### Minor Feature

- Cleaner CPU selection: the pool baseline is marked **(Recommended)** and detailed feature lists are hidden.
- Pool members automatically receive shared VLAN and ISO library configuration.
- Pool VLAN networks can now be renamed or removed, and imported ISOs removed, from the pool
  master; members apply the change themselves, and anything a VM still uses is refused.
- Experimental ZFS setup is in development.
- Simpler ZFS setup: choose hosts and disks on one screen, then review and erase; the Storage page lists repositories first.

### Bug Fixes

- Fixed VM creation incorrectly rejecting registered SMB or NFS storage.

## [0.3.4] — 2026-09-18

### Bug Fixes

- Final release verification checks download headers without transferring the entire
  source archive and avoids a false missing-version error from an early-closing
  package-index check (BUG-0294).

- Clustered storage browser fixtures use controlled time so a slow runner cannot
  expire their healthy report; an explicit expiry check still verifies that stale
  storage is excluded from VM placement (BUG-0293).

- Storage browser checks allow asynchronous results the same bounded time as browser
  actions and match each inspected request to its trigger, preventing false failures
  when a valid folder listing arrives after five seconds (BUG-0292).

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
