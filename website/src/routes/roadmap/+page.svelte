<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
  import "../../app.css";

  const statuses = {
    shipped: { label: "Built", mark: "●" },
    testing: { label: "In testing", mark: "◐" },
    next: { label: "Next", mark: "○" },
    later: { label: "Later", mark: "○" }
  };

  type Status = keyof typeof statuses;

  const milestones: {
    status: Status;
    title: string;
    summary: string;
    done: string[];
    open: string[];
  }[] = [
    {
      status: "shipped",
      title: "The host foundation",
      summary:
        "Debian, KVM/QEMU and libvirt running a guest we can start, reach and reboot.",
      done: [
        "Debian 13 host with libvirt and QEMU from Debian's own packages",
        "An accelerated guest boots, gets networking and keeps its data across a host reboot",
        "Local console and serial recovery access to the host"
      ],
      open: []
    },
    {
      status: "shipped",
      title: "One host, managed from a browser",
      summary:
        "A Controller that enrolls a host, and everything needed to run a VM on it. Phase 2 closed 2026-09-17.",
      done: [
        "Administrator login, sites, and automatic pools — each enrolled host becomes the master of its own pool",
        "Host enrollment by short-lived token, or by address and root password after you confirm the host's fingerprint; revoke and re-enroll without losing identity",
        "Reported host inventory: CPU, memory, virtualization features, network interfaces, disks, health checks and how fresh each observation is",
        "Durable tasks with a history of who did what, honest stale and degraded states, and reconciliation after a reconnect",
        "Pool-level storage registration, and pool networks on a VLAN or an existing bridge",
        "ISO upload that keeps running in the background while you close the dialog, or a public URL download to selected host storage",
        "Full VM creation: name and guest OS, vCPU count and topology, CPU model, a dedicated or maximum memory mode, several disks, several network interfaces, firmware, machine type, boot order, start after creation and autostart",
        "Editing a VM in place, applying the changes a running guest can accept and saying which ones need a stop",
        "Power control: start, graceful shutdown, restart and an explicit force stop kept visually apart from it",
        "A browser console for VMs and for hosts, with key combinations, display scaling, clipboard both ways, typing into installers, screenshots and CD/DVD insert and eject",
        "Guest tools reporting: agent state, operating system, hostname, addresses and clipboard readiness, with install advice when no agent answers",
        "Per-VM CPU, memory and disk usage from the host, shown on VM details with honest unknown values rather than zeros",
        "VM and host usage history kept on each host for up to a year, with charts, stacking, zoom and an on-demand 1-second realtime view — shipped in 0.3.0",
        "Windows-ready firmware choices: Secure Boot, an optional TPM 2.0, and a second CD drive that seeds Fedora virtio-win 0.1.302 from Core media — shipped in 0.3.2 with retained Fedora corresponding source",
        "Deleting a stopped VM after typing its name to confirm, with a separate choice about keeping or removing its disks, and completion if Nex Core is interrupted part-way — including retries after an interrupted delete, shipped in 0.3.0",
        "Phase 2 gate closed on 2026-09-17: create/install/console/reboot/persist and Controller outage recovery on gate5; BUG-0256 and admission re-checks on 0.3.1; VirtIO media on 0.3.2"
      ],
      open: []
    },
    {
      status: "testing",
      title: "An installable host that brings up its own Controller",
      summary:
        "One empty server, one ISO, a browser — and a working Controller at the end of it.",
      done: [
        "A Nex Core ISO with our own graphical installer: welcome, host identity, per-interface networking with VLAN and static or DHCP addressing, an explicit disk choice, and a review page whose erase confirmation names the disk",
        "Installation with no internet connection at all",
        "Nex Core's own version and identity, which survive base-package updates",
        "A small web interface on every installed host for setup, host and VM status, consoles and recovery",
        "Deploying the Controller as a virtual appliance from that local interface, which then creates the first site, enrolls the host and adopts the appliance on its own",
        "A signed Nex package repository, with update checks and updates through the ordinary package manager",
        "Releases made by tagging: the ISO and both packages are built and published together, and the build refuses to run if the tag and the changelog disagree",
        "A documented export of the Controller's database and identity, and a restore onto a replacement appliance",
        "A Controller package that starts: packages up to 0.2.9 were built without their database driver and stopped at launch; fixed from 0.3.0 and confirmed starting in the published 0.3.1 package"
      ],
      open: [
        "Secure Boot installation of the host — UEFI installation has so far passed only in an emulated virtual machine",
        "Real Intel and AMD hardware — every installation so far has been a disposable virtual machine",
        "Machines with several disks, and the safety of that choice",
        "Connecting a host to an existing Controller from the host's own interface; today the Controller reaches out and adds the host",
        "Replacing the Controller appliance on the same host that runs it, which is still being decided; today a replacement Controller runs on another machine and takes the pool back from its hosts"
      ]
    },
    {
      status: "testing",
      title: "More than one host, and shared storage",
      summary:
        "A pool that spans hosts, with storage they can all reach and honest placement rules.",
      done: [
        "Joining another host to an existing pool as a member under its master; the master keeps the membership, and a join with a lost reply is safe to retry",
        "Storage repositories owned by Nex Core: local directories, existing OpenZFS datasets and single-host iSCSI LUNs stay with the host that owns them, while NFS and SMB3 storage is shared across the pool",
        "Registering shared NFS or SMB3 storage once at pool level, with access, write permission, QEMU access and disk locking verified separately on every member before a VM there can use it",
        "Choosing which host a new VM is placed on, with only the storage, networks and ISOs that host has validated on offer",
        "A fixed CPU definition discovered from every current pool member and saved with each VM, with manual CPU choices still available — shipped in 0.3.1; native runtime and simulated browser checks passed, physical mixed-host testing and live migration remain open",
        "VMs on several hosts writing to the same NFS or SMB3 storage at once, with disk locks refusing a second writer of the same disk — passed in bounded three-host lab tests with no injected faults"
      ],
      open: [
        "A Controller screen for joining a host to a pool; today the join runs through each host's local interface",
        "Changing which host is the master, and ejecting a member — one authority handover design with its own safety checks",
        "Every member keeps a signed, versioned copy of the master's pool records, so a surviving member can become master without losing VM ownership, membership or storage settings",
        "Automatic master succession when the master is lost: only with a majority of the pool and proof the old master is powered off, never from a missed heartbeat; two-host pools promote manually",
        "The master, not the Controller, approves which host a VM is placed on",
        "Clustered iSCSI storage shared by several hosts with fencing: built for development and not yet for real use",
        "Fault testing that proves a host which stopped reporting never leads to a second writer: failed fencing, network partitions, lost quorum, larger pools, long-running load and physical storage",
        "Copying pool networks to member hosts, mapping them to host bridges and VLANs with compatibility checks, applying remote network changes in stages, and rolling them back automatically if management access is lost",
        "Maintenance mode, which blocks new placements and reports what is still running on the host"
      ]
    },
    {
      status: "next",
      title: "Snapshots, backups and proven restores",
      summary:
        "Getting a VM back — from a rollback point, and from a backup that lives somewhere else.",
      done: [],
      open: [
        "Snapshots: create, list, revert and delete, covering every supported disk and the matching VM configuration, and recovering cleanly when a snapshot or its consolidation is interrupted",
        "VM disk snapshots through QEMU/libvirt on validated disk-format and storage combinations, without requiring ZFS or Btrfs",
        "Backup repositories on storage separate from the VMs they protect, with their own credentials",
        "Full backups of stopped guests first, then running guests through libvirt's backup support, each with checksums and a manifest that can stand on its own",
        "Schedules, retention, bandwidth limits and per-VM overrides, with missed runs recorded rather than hidden",
        "Restoring as a new VM by default, on an isolated network, and replacing an original only with explicit confirmation and proven ownership",
        "Rebuilding the backup catalog from the repository itself, so backups outlive the Controller database"
      ]
    },
    {
      status: "next",
      title: "Moving VMs between hosts",
      summary: "Relocation and live migration, with exactly one host ever running the VM.",
      done: [],
      open: [
        "Moving a stopped VM, transferring its disks when the storage is local to one host",
        "Manual recovery after a host dies: install a replacement Core, select surviving VMs on NFS or SMB storage and boot them after restoring their configuration and proving the old host cannot still write; iSCSI requires an exclusive LUN handover",
        "Recovering when the failed host was the pool master by promoting a member from its copy of the pool records, and when it was the only Core from protected recovery records that survive on network storage, never depending on the old host or Controller",
        "Live migration for guests on shared storage, once CPU model, firmware, machine type, networking and versions have all been checked",
        "Draining a host for maintenance and returning it to service afterwards",
        "Recovering correctly when the migration network drops mid-transfer — a timeout is never treated as a finished move",
        "Moving a VM to a different site managed by the same Controller"
      ]
    },
    {
      status: "next",
      title: "A release we would support",
      summary: "Upgrades, roles and recovery procedures that have actually been rehearsed.",
      done: [
        "Checksums, release notes, a matching source bundle and a component manifest, published with each release since 0.2.9"
      ],
      open: [
        "Upgrading hosts that already have VMs on them, with schema migrations and a stated Controller and host compatibility window",
        "Operator and viewer roles, scoped across sites and pools, with host enrollment and credentials kept to administrators",
        "Written and rehearsed procedures for host replacement, Controller loss, storage loss and certificate recovery",
        "Rehearsing replacement-Core VM recovery separately on NFS, SMB and supported exclusive-handover iSCSI targets, including stale old-host return and refusal when safe ownership cannot be proven",
        "Surfacing the things that matter: failed tasks, ageing backups, low capacity, degraded storage, lost hosts and expiring certificates, plus diagnostic bundles with secrets stripped out",
        "A published hardware list and a stated range of supported versions"
      ]
    },
    {
      status: "later",
      title: "After the first supported release",
      summary: "Agreed directions that follow the work above, in roughly this order.",
      done: [],
      open: [
        "High availability per pool, with real quorum and fencing — a missing heartbeat will never be enough on its own to restart a VM somewhere else",
        "HA and migration controlled by the active pool master, building on the earlier master succession; returning VMs to a preferred host only when requested or explicitly enabled, with stability checks to prevent repeated moves",
        "Repeated disaster-recovery, fencing and failback tests under partitions, master loss and stale-host return, with independent safety review before any supported reliability claim",
        "Container Services: Docker Swarm inside Nex-managed node VMs, deployed and operated from Controller rather than over SSH",
        "Guided passthrough of physical hardware to a VM",
        "Templates, clones, cloud-init and creating several VMs at once",
        "Ceph storage, once the NFS path is solid",
        "Changed-block tracking (CBT) and incremental backups after full restores are proven, with safe full-backup fallback if tracking is lost and retention that preserves complete restore chains",
        "Storage-native ZFS and Btrfs snapshot integration, with separate lifecycle and recovery tests; evaluate native send/receive for off-host copies",
        "An optional, off-by-default MCP server so an authorized AI agent can use the same Controller operations a person would"
      ]
    }
  ];

  const tally = (status: Status) => milestones.filter((m) => m.status === status).length;
  const counts = [
    { value: tally("shipped"), label: "Built", status: "shipped" as Status },
    { value: tally("testing"), label: "In testing", status: "testing" as Status },
    { value: tally("next") + tally("later"), label: "Ahead", status: "next" as Status }
  ];
</script>

<svelte:head>
  <title>Roadmap — Nex</title>
  <meta
    name="description"
    content="What is built in Nex today, what is still being tested, and what comes next — from the host foundation through pools, backups, migration and a supported release."
  />
</svelte:head>

<a class="skip" href="#main">Skip to content</a>
<div class="preview-bar">
  <span class="preview-dot" aria-hidden="true"></span> Private preview
  <span class="preview-divider">/</span> In development
</div>
<header class="site-header wrap">
  <a class="brand" href="/" aria-label="Nex home">
    <span class="wordmark" aria-hidden="true"></span>
    <span class="brand-divider"></span>
    <span class="brand-caption">Virtualization,<br />built together.</span>
  </a>
  <nav aria-label="Main navigation">
    <a href="/">Home</a>
    <a href="/#platform">Platform</a>
    <a href="/#approach">Approach</a>
    <a href="/roadmap/" aria-current="page">Roadmap</a>
    <a href="/gallery/">Gallery</a>
    <a class="nav-release" href="/#releases">Releases <span aria-hidden="true">↗</span></a>
  </nav>
</header>

<main id="main" class="wrap">
  <section class="roadmap-intro" aria-labelledby="roadmap-title">
    <div class="intro-copy">
      <p class="eyebrow"><span class="small-line"></span> Where the project stands</p>
      <h1 id="roadmap-title">Roadmap.</h1>
      <p class="roadmap-description">
        Built in order — no milestone counts as done until it works end to end. No
        dates. Ticked items are in published releases through 0.3.1 unless a note says
        otherwise.
      </p>
    </div>
    <dl class="counts">
      {#each counts as count}
        <div class={`count count-${count.status}`}>
          <dt>{count.value}</dt>
          <dd>{count.label}</dd>
        </div>
      {/each}
    </dl>
  </section>

  <section class="legend" aria-label="What the marks mean">
    <dl>
      <div>
        <dt><span class="mark shipped" aria-hidden="true">●</span> Built</dt>
        <dd>Shipped, checks recorded</dd>
      </div>
      <div>
        <dt><span class="mark testing" aria-hidden="true">◐</span> In testing</dt>
        <dd>Usable, checks unfinished</dd>
      </div>
      <div>
        <dt><span class="mark next" aria-hidden="true">○</span> Next</dt>
        <dd>Specified, not yet built</dd>
      </div>
    </dl>
    <p class="legend-note">Bars count the items listed here — not effort or time.</p>
  </section>

  <ol class="timeline">
    {#each milestones as milestone, index}
      <li class={`milestone milestone-${milestone.status}`}>
        <div class="card-top">
          <span class="card-number">{index < 7 ? `0${index + 1}` : "—"}</span>
          <span class={`status status-${milestone.status}`}>
            <span class="mark" aria-hidden="true">{statuses[milestone.status].mark}</span>
            {statuses[milestone.status].label}
          </span>
        </div>
        <h2>{milestone.title}</h2>
        <p class="milestone-summary">{milestone.summary}</p>
        <div class="progress">
          <div
            class="meter"
            role="img"
            aria-label={`${milestone.done.length} of ${milestone.done.length + milestone.open.length} listed items built`}
          >
            {#each Array.from({ length: milestone.done.length + milestone.open.length }) as _, slot}
              <span class={slot < milestone.done.length ? "seg seg-on" : "seg"}></span>
            {/each}
          </div>
          <span class="progress-text">
            {milestone.done.length}<span class="of">/{milestone.done.length + milestone.open.length}</span>
          </span>
        </div>
        <details>
          <summary>
            {milestone.done.length > 0 ? "See the detail" : "See what this covers"}
            <span aria-hidden="true">+</span>
          </summary>
          <div class="detail">
            {#if milestone.done.length > 0}
              <h3>Built</h3>
              <ul class="list done-list">
                {#each milestone.done as item}
                  <li>{item}</li>
                {/each}
              </ul>
            {/if}
            {#if milestone.open.length > 0}
              <h3>{milestone.done.length > 0 ? "Still to do" : "What this covers"}</h3>
              <ul class="list open-list">
                {#each milestone.open as item}
                  <li>{item}</li>
                {/each}
              </ul>
            {/if}
          </div>
        </details>
      </li>
    {/each}
  </ol>

  <section class="closing" aria-labelledby="closing-title">
    <h2 id="closing-title">Two things we will not shortcut.</h2>
    <div class="closing-grid">
      <article>
        <h3>A snapshot is not a backup.</h3>
        <p>
          Same storage, so it dies with it. And a finished backup job proves nothing until
          a restore is tested.
        </p>
      </article>
      <article>
        <h3>An unreachable host is not a stopped host.</h3>
        <p>
          Its VMs may still be writing. Nothing starts a second copy on a guess, and HA
          waits for fencing that can prove it.
        </p>
      </article>
    </div>
  </section>
</main>

<footer class="wrap">
  <div class="footer-top">
    <a class="brand" href="/" aria-label="Nex home"><span class="wordmark" aria-hidden="true"></span></a>
    <p>Community-driven.<br />Built together.</p>
    <a class="text-link" href="/">Back to the project <span aria-hidden="true">→</span></a>
  </div>
  <div class="footer-bottom">
    <span>© 2026 Nex.</span>
    <span>Debian · KVM · libvirt</span>
  </div>
</footer>

<style>
  .site-header nav { flex-wrap: wrap; justify-content: flex-end; gap: 20px; }
  [aria-current="page"] { color: var(--blue); text-decoration: underline; text-underline-offset: 6px; }

  .roadmap-intro {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 34px;
    padding-block: 62px 40px;
  }
  h1 { font-size: clamp(44px, 6vw, 72px); }
  .roadmap-description { max-width: 42ch; margin-top: 18px; color: var(--muted); font-size: 15px; }

  .counts { display: flex; gap: 10px; margin: 0; }
  .count {
    min-width: 104px;
    padding: 16px 18px 14px;
    border: 1px solid var(--line);
    border-top-width: 3px;
    background: #fff;
  }
  .count dt { font-size: 34px; font-weight: 500; letter-spacing: -0.05em; line-height: 1; }
  .count dd {
    margin: 8px 0 0;
    font: 10px var(--mono);
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
  }
  .count-shipped { border-top-color: #1c7a53; }
  .count-testing { border-top-color: #c08a24; }
  .count-next { border-top-color: #aab3c4; }

  .legend {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 12px 32px;
    padding-block: 18px;
    border-block: 1px solid var(--line);
  }
  .legend dl { display: flex; flex-wrap: wrap; gap: 10px 32px; margin: 0; }
  .legend dt { display: flex; align-items: baseline; gap: 8px; font-size: 13px; }
  .legend dd { display: inline; margin: 0 0 0 8px; color: var(--muted); font-size: 12px; }
  .legend dd::before { content: "— "; }
  .legend-note { font: 10px var(--mono); letter-spacing: 0.04em; color: var(--muted); }

  .mark { font-size: 10px; line-height: 1; }
  .shipped { color: #1c7a53; }
  .testing { color: #9a6414; }
  .next { color: #7d879b; }

  .timeline {
    list-style: none;
    margin: 0;
    padding: 40px 0 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(330px, 1fr));
    gap: 20px;
  }
  .milestone {
    position: relative;
    display: flex;
    flex-direction: column;
    min-width: 0;
    padding: 22px 24px 20px;
    border: 1px solid var(--line);
    border-top: 3px solid #aab3c4;
    background: #fff;
  }
  .milestone-shipped { border-top-color: #1c7a53; }
  .milestone-testing { border-top-color: #c08a24; }

  .card-top { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
  .card-number {
    font: 11px var(--mono);
    letter-spacing: 0.08em;
    color: var(--muted);
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 5px 10px;
    border: 1px solid var(--line);
    font: 9px var(--mono);
    letter-spacing: 0.07em;
    text-transform: uppercase;
    white-space: nowrap;
  }
  .status-shipped { border-color: #b6dcc9; background: #f1f9f5; color: #17553c; }
  .status-shipped .mark { color: #1c7a53; }
  .status-testing { border-color: #e6d3ab; background: #fdf8ef; color: #6e4a10; }
  .status-testing .mark { color: #9a6414; }
  .status-next .mark, .status-later .mark { color: #7d879b; }

  .milestone h2 { margin-top: 18px; font-size: 22px; }
  .milestone-summary {
    margin-top: 10px;
    color: var(--muted);
    font-size: 13.5px;
    line-height: 1.55;
    flex-grow: 1;
  }

  .progress { display: flex; align-items: center; gap: 12px; margin-top: 20px; }
  .meter { display: flex; gap: 3px; flex: 1; min-width: 0; }
  .seg { flex: 1; height: 6px; min-width: 3px; background: #e4e8f1; }
  .seg-on { background: #1c7a53; }
  .milestone-testing .seg-on { background: #c08a24; }
  .progress-text { font: 11px var(--mono); letter-spacing: 0.03em; }
  .of { color: var(--muted); }

  details { margin-top: 18px; border-top: 1px solid var(--line); padding-top: 14px; }
  summary {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    cursor: pointer;
    list-style: none;
    font: 10px var(--mono);
    letter-spacing: 0.07em;
    text-transform: uppercase;
    color: var(--muted);
  }
  summary::-webkit-details-marker { display: none; }
  summary:hover { color: var(--blue); }
  details[open] summary { color: #17233b; }
  details[open] summary span { transform: rotate(45deg); }
  summary span { font-size: 13px; transition: transform 0.15s; }

  .detail h3 {
    margin-top: 18px;
    font: 10px var(--mono);
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
    font-weight: 400;
  }
  .list { margin: 12px 0 0; padding: 0; list-style: none; display: grid; gap: 10px; }
  .list li { position: relative; padding-left: 22px; font-size: 13px; line-height: 1.6; }
  .list li::before { position: absolute; left: 0; top: 0; font-size: 10px; line-height: 1.95; }
  .done-list li::before { content: "✓"; color: #1c7a53; }
  .open-list li::before { content: "○"; color: #7d879b; font-size: 8px; }

  /* Hover preview: float the panel over the cards below instead of pushing them down.
     Layered on top of <details>, never replacing it — clicking still opens the panel in
     flow, which is what keyboard, touch and older browsers rely on. Gated on a real
     hovering pointer so a tap cannot leave a touch device stuck in a hover state, and
     :focus-within is inside the same gate so Tab previews it the same way a mouse does.
     ::details-content is what makes a closed panel paintable; where it is unsupported
     the card simply stays click-to-open. */
  @media (hover: hover) and (pointer: fine) {
    .milestone:hover,
    .milestone:focus-within { z-index: 5; }
    .milestone:hover details:not([open])::details-content,
    .milestone:focus-within details:not([open])::details-content {
      content-visibility: visible;
    }
    .milestone:hover details:not([open]) .detail,
    .milestone:focus-within details:not([open]) .detail {
      position: absolute;
      left: -1px;
      right: -1px;
      top: 100%;
      max-height: 58vh;
      overflow-y: auto;
      overscroll-behavior: contain;
      margin-top: 0;
      padding: 4px 24px 22px;
      border: 1px solid var(--line);
      border-top: 0;
      background: #fff;
      box-shadow: 0 18px 34px -16px rgba(23, 35, 59, 0.28);
    }
    .milestone:hover details:not([open]) summary,
    .milestone:focus-within details:not([open]) summary { color: var(--blue); }
  }
  @media (hover: hover) and (pointer: fine) and (prefers-reduced-motion: no-preference) {
    .milestone:hover details:not([open]) .detail,
    .milestone:focus-within details:not([open]) .detail {
      animation: slide-down 0.16s ease-out;
    }
    @keyframes slide-down {
      from { opacity: 0; transform: translateY(-6px); }
      to { opacity: 1; transform: translateY(0); }
    }
  }

  .closing { margin-top: 56px; border-top: 1px solid var(--line); padding-block: 40px 72px; }
  .closing h2 { font-size: 26px; }
  .closing-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 28px 64px;
    margin-top: 26px;
  }
  .closing h3 { font-size: 17px; }
  .closing p { margin-top: 10px; color: var(--muted); font-size: 13.5px; }

  @media (prefers-reduced-motion: reduce) {
    summary span { transition: none; }
  }
  @media (max-width: 800px) {
    .site-header { flex-wrap: wrap; padding-block: 24px; gap: 24px; }
    .site-header nav { justify-content: flex-start; }
  }
  @media (max-width: 580px) {
    .roadmap-intro { padding-block: 40px 28px; gap: 26px; }
    .counts { width: 100%; }
    .count { flex: 1; min-width: 0; padding: 13px 12px 11px; }
    .count dt { font-size: 27px; }
    .timeline { padding-top: 28px; grid-template-columns: minmax(0, 1fr); }
    .milestone { padding: 20px 18px 18px; }
    .closing { margin-top: 40px; padding-block: 32px 48px; }
    .closing h2 { font-size: 23px; }
  }
</style>
