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
    notes?: string[];
  }[] = [
    {
      status: "shipped",
      title: "The host foundation",
      summary:
        "Phase 0 closed by the project owner. Debian, KVM/QEMU and libvirt run a guest in a bounded nested lab.",
      done: [
        "Debian 13 host with libvirt and QEMU from Debian's own packages",
        "A nested KVM guest boots, and its definition and host marker persist across a reboot",
        "Local console and serial recovery access to the host in the disposable lab"
      ],
      open: [],
      notes: [
        "Full guest-network and written-data reboot acceptance was not run; later problems are tracked as bugs"
      ]
    },
    {
      status: "shipped",
      title: "One host, managed from a browser",
      summary:
        "Phases 1–2 closed: a Controller that enrolls a host and manages a VM through its lifecycle.",
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
      status: "shipped",
      title: "An installable host that brings up its own Controller",
      summary:
        "Phase 3 closed by the project owner after the disposable install-to-first-VM lab chain.",
      done: [
        "A Nex Core ISO with our own graphical installer: welcome, host identity, per-interface networking with VLAN and static or DHCP addressing, an explicit disk choice, and a review page whose erase confirmation names the disk",
        "Installation with no internet connection at all; the local development 0.3.2 snapshot passed offline BIOS installation, first boot and reboot in a disposable VM",
        "Nex Core's own version and identity, which survive base-package updates",
        "A small web interface on every installed host for setup, host and VM status, consoles and recovery",
        "Deploying the Controller as a virtual appliance from that local interface, which then creates the first site, enrolls the host and adopts the appliance on its own",
        "A signed Nex package repository, with update checks and updates through the ordinary package manager",
        "One changelog version for the ISO and both Debian packages, built together for release with their declared versions checked before publication",
        "A documented export of the Controller's database and identity, and a restore onto a replacement appliance",
        "A Controller package that starts: fixed from 0.3.0 and checked in published 0.3.1; the local development 0.3.2 snapshot also passed installation, crash recovery, same-version reinstall and reboot with saved site identity",
        "Local development 0.3.2 snapshot: matching package/ISO source verified, Core service restart/reboot checked, and the bundled appliance booted separately with DHCP, verified TLS and bootstrap-secret cleanup",
        "The whole chain from a 0.4.5 development ISO in disposable virtual machines: offline installation, eject and reboot, bundled Controller deployment, a second host added and joined, and a first managed VM, with recovery access while the Controller was stopped",
        "Installation under enforcing Secure Boot in emulation, and on machines with several disks: one disk chosen with the other left untouched, or two disks as a mirror",
        "An installation with the network cable unplugged, and a power cut during installation followed by a clean reinstall",
        "A Controller deployment interrupted part-way on a freshly installed host, which now starts again and finishes"
      ],
      open: [],
      notes: [
        "The published 0.4.12 network installer fails graphical UEFI installation after its network preflight; a corrected temporary overlay passed, but a rebuilt release ISO still needs verification",
        "Fresh signed Windows/Linux guest boot under Debian stable OVMF, and existing guest firmware state across the package switch, still need disposable-lab verification",
        "Real Intel and AMD hardware — every installation so far has been a disposable virtual machine",
        "Connecting a host to an existing Controller from the host's own interface; today the Controller reaches out and adds the host",
        "Replacing the Controller appliance on the same host that runs it, which is still being decided; today a replacement Controller runs on another machine and takes the pool back from its hosts"
      ]
    },
    {
      status: "shipped",
      title: "More than one host, and shared storage",
      summary:
        "Phase 4 closed by the project owner. The earlier multi-host acceptance remains limited to its recorded virtual-lab scope; fencing testing stays open.",
      done: [
        "Joining another host to an existing pool as a member under its master; the master keeps the membership, and a join with a lost reply is safe to retry",
        "Storage repositories owned by Nex Core: local directories, existing OpenZFS datasets and single-host iSCSI LUNs stay with the host that owns them, while NFS and SMB3 storage is shared across the pool",
        "Registering shared NFS or SMB3 storage once at pool level, with access, write permission, QEMU access and disk locking verified separately on every member before a VM there can use it",
        "Choosing which host a new VM is placed on, with only the storage, networks and ISOs that host has validated on offer",
        "A fixed CPU definition discovered from every current pool member and saved with each VM, with manual CPU choices still available — shipped in 0.3.1; native runtime and simulated browser checks passed, physical mixed-host testing and live migration remain open",
        "VMs on several hosts writing to the same NFS or SMB3 storage at once, with disk locks refusing a second writer of the same disk — passed in bounded three-host lab tests with no injected faults",
        "Joining a host to a pool from Nex Controller — reviewed G1 evidence covers repeated join/eject lifecycle, missing-uplink failure, autonomous recovery and cleanup (disposable lab, 2026-09-20)",
        "After join, members pull the master's pool-wide guest VLANs and ISO library entries, materialize them locally and report per-host status — bounded missing-uplink recovery evidence",
        "Changing the pool master and ejecting a member, including one that cannot be reached — scoped native and browser evidence; identities preserved",
        "Every member keeps a signed, versioned copy of the master's pool records — reviewed lifecycle evidence",
        "Manual master recovery through fence-verified promotion — scoped disposable-lab evidence; OD-6 strict unreachable-member rule confirmed",
        "Restricted hosts: boot-time refusal, autostart suppression, rejoin and post-clear start/stop — reviewed S6-14 evidence",
        "Remote management-network edits with staged apply and rollback — reviewed G3 and S3 subset evidence",
        "Maintenance mode: enter, stop, refused start and exit — reviewed G2 browser evidence",
        "Phase 4 gate closed for disposable virtual-lab scope on 2026-09-20",
        "Master-owned VM placement: lost replies, storage loss, membership races, interrupted changes and Controller replacement passed in a disposable multi-host lab (2026-09-23)"
      ],
      open: [],
      notes: [
        "Per-host uplink maps for pool VLANs: decided, not implemented; a member with differently named uplinks can still join with failed network convergence",
        "Physical/BMC fencing and five-host scale — not claimed for disposable virtual-lab acceptance",
        "Package-installed libvirt hook and AppArmor enforcement — lab overlay only so far",
        "Authentic owner-conflict S6-12/16 fixture — unavailable; contradictory-owner subcase not exercised",
        "Broader duplicate-writer, workload-continuity and fault-matrix evidence — permanent limits for this lab class",
        "Automatic master succession: a narrow two-host BMC path has simulated checks; storage lease, witness and physical fence proof remain open, separate from VM restart policies",
        "Clustered iSCSI storage shared by several hosts with fencing: built for development, not yet for real use",
        "ZFS provisioning from selected disks: in development; external image delivery remains open",
        "G2 maintenance reboot/autostart walkthrough and remaining browser coverage for transfer/recovery views"
      ]
    },
    {
      status: "shipped",
      title: "VM snapshots and rollback",
      summary:
        "Phase 5 closed 2026-09-27 for disposable virtual-lab scope; Core-owned snapshots with Controller UI; permanent physical/production and product-on-every-backend limits remain documented.",
      done: [
        "Bounded disposable-lab native feasibility for disk snapshots and paired firmware state",
        "Core-owned stopped snapshot creation and catalog reporting, with bounded exact-retry, interruption, Controller replacement and pool promotion/rejoin evidence",
        "Corrected stopped revert recovery for a disposable two-disk UEFI guest: exact same-operation retry settled without a second native revert",
        "Stopped leaf delete with public no-delete blockcommit, catalog metadata rename and Controller Snapshots tab create/list/rename/revert/delete",
        "Running BIOS crash-consistent and quiesced create through Controller, with honest UEFI and missing-agent refusals",
        "Browser Snapshots walkthrough: crash create, rename, matching-disk revert and leaf delete",
        "VMs-list bulk Snapshot for selected shut-off guests with shared name and per-VM create tasks",
        "Native multi-backend stopped snapshot lifecycle and midcommit consolidation on local, NFS, SMB and exclusive iSCSI (lab only; not product backup)",
        "Phase 5 gate closed on 2026-09-27 for disposable virtual-lab scope (forge CI on ac92f407; independent checklist and Core state-ownership review)"
      ],
      open: [
        "Product Controller/Core snapshot paths not certified on every S0 backend (NFS/SMB/exclusive iSCSI); S0 native evidence is not product-on-all-backends certification",
        "Physical and production storage and hardware fencing remain untested (disposable nested lab only)",
        "Memory snapshots are later work, out of Phase 5 scope"
      ]
    },
    {
      status: "testing",
      title: "Backups and proven restores",
      summary:
        "Phase 6 had a bounded disposable-lab close on 2026-09-27. The expanded destination candidate has passed selected native recovery gates; final acceptance remains open.",
      done: [
        "Core NFS backup repository registration, stopped full backup and restore-as-new APIs",
        "Controller relay and Backups UI for repositories, recovery points, Back up now and restore as new",
        "Online full backup (crash-consistent) for running BIOS guests via libvirt push-mode; quiesced and UEFI online refused",
        "Core-owned backup policies, schedules and retention with Controller/UI relay",
        "Restore replace exclusive cutover, protect/pins, test restore, catalog import/rebuild and Core-local backup status",
        "Operator guides for repositories, Back up now, policies, restore-as-new/replace, catalog rebuild and Controller-absent recovery",
        "Disposable-lab product NFS stopped backup → disks aside → restore-as-new (nex-core-local 0.4.12 + Phase 6 overlay)",
        "Bounded nested Campaign A (Controller-absent Run now/restore + fresh Controller adoption)",
        "Bounded nested Campaign B re-run after promote backup-authority fix (continuity, restore-as-new, adoption)",
        "Browser Backups rail walkthrough; settlement bugs fixed in code; hermetic Phase 6 backup CI regression",
        "Phase 6 gate closed on 2026-09-27 for disposable virtual-lab scope",
        "Retained history beyond 64 MiB and signed backup-authority takeover passed bounded native multi-host checks on an earlier candidate"
      ],
      open: [
        "Backup repositories beyond NFS: SMB3, S3-compatible and SFTP are implemented in the current candidate, but native/browser/forge acceptance of that tree remains open (earlier destination lab evidence predates later review fixes); FTPS and opt-in plaintext FTP are still out of scope",
        "Nested disposable lab is not physical failure-domain certification",
        "Quiesced online and UEFI online backup remain refused until proven",
        "Enabled destination build needs a live browser walkthrough of setup, credentials, policy, restore and error flows",
        "The final Core-down missed-schedule wall-clock span and current-candidate checks remain open",
        "Full three-host simultaneous join and schedule wall-clock span not fully proven",
        "2026-09-29 review fixes have focused unit tests only; lab and forge CI evidence is outstanding",
        "Lab used package 0.4.12 plus binary overlay — not ISO install acceptance",
        "Forge CI on a published push not yet inspected (owner push residual)"
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
        "An application catalogue fed by a signed official Git repository and optional community repositories, fetched by the pool master, covering container applications and VM appliances with a simple install wizard and an Advanced view of the underlying stack",
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
    <span class="wordmark" aria-hidden="true"><img src="/nex-symbol-small.svg" alt="" width="38" height="38" /><img src="/nex-wordmark.svg" alt="" width="81" height="28" /></span>
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
        Phases 0–4 are closed by project-owner decision. Phase 5 snapshots closed for disposable-lab scope; Phase 6 backups are in testing.
        Earlier test limits and known issues remain visible; fencing still needs
        proper safety testing. Development checks do not establish publication.
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
        <dd>Completed or accepted by the project owner; see evidence limits</dd>
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
            {#if milestone.notes?.length}
              <h3>Known issues and separate gates</h3>
              <ul class="list open-list">
                {#each milestone.notes as item}
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
    <a class="brand" href="/" aria-label="Nex home"><span class="wordmark" aria-hidden="true"><img src="/nex-symbol-small.svg" alt="" width="38" height="38" /><img src="/nex-wordmark.svg" alt="" width="81" height="28" /></span></a>
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
