<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
  import "../../app.css";

  const screenshots = [
    {
      file: "desktop-shell.png",
      title: "Hosts at a glance",
      description: "The resource tree, host overview and recent tasks share one workspace.",
      alt: "Nex Controller General tab with the site tree showing the host as pool master of its automatic pool, host hardware details, health checks and recent tasks."
    },
    {
      file: "vms-desktop.png",
      title: "Virtual machines in the pool",
      description: "Every VM in the pool with its assigned host, observed state and creation task.",
      alt: "Nex Controller pool Virtual machines tab listing three fictional demo VMs — app-01, db-01 and web-01 — each assigned to the demo host, observed running with fresh state and a succeeded creation task."
    },
    {
      file: "vm-general-desktop.png",
      title: "A virtual machine in detail",
      description: "Configuration, observed state and the task that created it, with power controls in the toolbar.",
      alt: "Nex Controller VM details for the fictional web-01 guest showing observed running state, CPU topology, memory, disk, network adapter, install ISO, firmware and its succeeded creation task, with toolbar power controls enabled."
    },
    {
      file: "create-vm-desktop.png",
      title: "Create a VM, reviewed first",
      description: "A six-step wizard that summarises the whole guest before anything is created.",
      alt: "Nex Controller Create VM wizard on step 6 of 6, Review, summarising the fictional build-01 guest's name, CPU, memory, disk, network, firmware, install ISO and boot order above the lifecycle options and the Create stopped VM button."
    },
    {
      file: "networking-desktop.png",
      title: "Host networking",
      description: "The Networking tab shows observed interfaces, addresses and connection state.",
      alt: "Nex Controller Networking tab listing two fictional host interfaces, example addresses and their observed up state."
    },
    {
      file: "storage-desktop.png",
      title: "Storage on the host",
      description: "The Storage tab lists local disks and their reported capacity.",
      alt: "Nex Controller Storage tab showing local disk paths and observed capacities."
    },
    {
      file: "activity-desktop.png",
      title: "Activity history",
      description: "The Activity tab records changes, who made them and when they happened.",
      alt: "Nex Controller Activity tab showing site creation, enrollment token, automatic pool creation and host enrollment records."
    }
  ];

  const installer = [
    {
      file: "installer-boot-menu.png", width: 640, height: 480,
      title: "Boot the installer",
      description: "Choose the graphical wizard, a minimal text install or a serial console install.",
      alt: "Nex Core installation media boot menu offering GUI install, minimal text install, serial console install and Debian rescue mode."
    },
    {
      file: "installer-welcome.png", width: 1024, height: 768,
      title: "A guided host installation",
      description: "Five steps from welcome to review, with nothing written until you confirm.",
      alt: "Nex Core installer welcome page with the Welcome, Host, Network, Disk and Review steps listed beside an introduction to Nex Core and Nex Controller."
    },
    {
      file: "installer-host.png", width: 1024, height: 768,
      title: "Name the host",
      description: "Hostname, keyboard, timezone dropdowns and a confirmed root password.",
      alt: "Nex Core installer Host page with the fictional hostname nex-core-01.example.com, a US English keyboard, the Australia/Sydney timezone and masked matching root passwords."
    },
    {
      file: "installer-network.png", width: 1024, height: 768,
      title: "Configure every adapter",
      description: "Live link state, MAC and driver per NIC, with static or DHCP addressing and an optional VLAN.",
      alt: "Nex Core installer Network page showing adapter ens2 with link detected, a static documentation-range address, gateway and DNS servers, selected as the management adapter."
    },
    {
      file: "installer-network-dhcp.png", width: 1024, height: 768,
      title: "Preview a DHCP lease",
      description: "The installer asks for a lease on the chosen adapter and shows the address it would receive.",
      alt: "Nex Core installer Network page with the second adapter ens3 set to automatic DHCP, showing the previewed DHCP address and a Retry button."
    },
    {
      file: "installer-disk.png", width: 1024, height: 768,
      title: "Pick the installation disk",
      description: "Select one whole disk explicitly; other disks are left untouched.",
      alt: "Nex Core installer Disk page with a single 32 GiB virtual disk selected."
    },
    {
      file: "installer-review.png", width: 1024, height: 768,
      title: "Review before erasing",
      description: "Every setting on one page, and an erase checkbox naming the disk before install starts.",
      alt: "Nex Core installer Review page summarising the host, network bridge and static address, and the whole-disk ext4 installation, with the erase confirmation for the named disk checked."
    },
    {
      file: "installer-progress.png", width: 1024, height: 768,
      title: "Installation progress",
      description: "Progress, the current task and any installer prompts in the same layout.",
      alt: "Nex Core installer progress page installing the base system, with a progress bar and the current package step."
    },
    {
      file: "installer-complete.png", width: 1024, height: 768,
      title: "Ready to restart",
      description: "The installer waits for you to remove the media and choose Restart now.",
      alt: "Nex Core installation complete page confirming Nex Core is ready, with instructions to remove the installation media and a Restart now button."
    },
    {
      file: "installer-first-boot.png", width: 1024, height: 768,
      title: "First boot",
      description: "The installed host shows its version, name and local web UI address at the console.",
      alt: "Installed Nex Core 0.1.9 console showing the host name nex-core-01, the local web UI address https://192.0.2.20:9443 and a login prompt."
    }
  ];
</script>

<svelte:head>
  <title>Gallery — Nex Controller</title>
  <meta name="description" content="Explore the Nex Controller interface and the Nex Core installer: hosts, virtual machines, networking, storage, activity and host installation, shown with fictional demo data." />
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
    <a href="/#questions">Questions</a>
    <a href="/roadmap/">Roadmap</a>
    <a href="/gallery/" aria-current="page">Gallery</a>
    <a class="nav-release" href="/#releases">Releases <span aria-hidden="true">↗</span></a>
  </nav>
</header>

<main id="main" class="wrap">
  <section class="gallery-intro" aria-labelledby="gallery-title">
    <p class="eyebrow"><span class="small-line"></span> Inside Nex Controller</p>
    <h1 id="gallery-title">Nex Controller.<br /><span>A closer look.</span></h1>
    <p class="gallery-description">Hosts, virtual machines, networking, storage and activity in one workspace.</p>
    <p class="gallery-note">Development screenshots with fictional demo data. Captured 11 September 2026 from a real Controller build; the host inventory and its virtual machines are a simulated demo fixture, not a running production system.</p>
  </section>

  <section class="gallery-grid" aria-label="Controller screenshots">
    {#each screenshots as screenshot, index}
      <figure class="gallery-card">
        <a class="screenshot-link" href={`/gallery/${screenshot.file}`} aria-label={`Open full-size screenshot: ${screenshot.title}`}>
          <img src={`/gallery/${screenshot.file}`} alt={screenshot.alt} width="1440" height="1000" loading={index === 0 ? "eager" : "lazy"} />
        </a>
        <figcaption>
          <div>
            <p class="eyebrow">0{index + 1} / Controller</p>
            <h2>{screenshot.title}</h2>
            <p class="caption-description">{screenshot.description}</p>
          </div>
          <a class="text-link" href={`/gallery/${screenshot.file}`}>View full size <span aria-hidden="true">↗</span></a>
        </figcaption>
      </figure>
    {/each}
  </section>

  <section class="gallery-intro" aria-labelledby="installer-title">
    <p class="eyebrow"><span class="small-line"></span> Inside the Nex Core installer</p>
    <h2 id="installer-title" class="section-title">Nex Core installer.<br /><span>From boot to host.</span></h2>
    <p class="gallery-description">A graphical wizard that sets up the host, its network adapters and the installation disk, then shows everything before it erases anything.</p>
    <p class="gallery-note">Captured 14 September 2026 from the Nex Core 0.1.9 ISO installing into a disposable virtual machine with example host and network values.</p>
  </section>

  <section class="gallery-grid" aria-label="Installer screenshots">
    {#each installer as screenshot, index}
      <figure class="gallery-card">
        <a class="screenshot-link" href={`/gallery/${screenshot.file}`} aria-label={`Open full-size screenshot: ${screenshot.title}`}>
          <img src={`/gallery/${screenshot.file}`} alt={screenshot.alt} width={screenshot.width} height={screenshot.height} loading="lazy" />
        </a>
        <figcaption>
          <div>
            <p class="eyebrow">0{index + 1} / Installer</p>
            <h2>{screenshot.title}</h2>
            <p class="caption-description">{screenshot.description}</p>
          </div>
          <a class="text-link" href={`/gallery/${screenshot.file}`}>View full size <span aria-hidden="true">↗</span></a>
        </figcaption>
      </figure>
    {/each}
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
  .gallery-intro { padding-block: 70px 48px; }
  .gallery-description { max-width: 600px; margin-top: 24px; color: var(--muted); font-size: 16px; }
  .gallery-note { max-width: 740px; margin-top: 24px; padding-left: 16px; border-left: 2px solid var(--blue); color: var(--muted); font-size: 12px; }
  .section-title { font-size: clamp(38px, 4.7vw, 69px); line-height: 1.08; margin-top: 30px; letter-spacing: -0.068em; }
  .section-title > span { color: var(--blue); }
  .gallery-grid { display: grid; gap: 52px; padding-bottom: 72px; }
  .gallery-card { min-width: 0; }
  .screenshot-link { display: block; border: 1px solid var(--line); background: #fff; }
  .screenshot-link img { display: block; width: 100%; height: auto; }
  figcaption { display: flex; align-items: flex-start; justify-content: space-between; gap: 24px; padding: 22px 0 0; border-top: 0; font-family: inherit; color: #17233b; }
  figcaption h2 { margin-top: 10px; font-size: 28px; }
  .caption-description { margin-top: 10px; color: var(--muted); font-size: 14px; }
  figcaption .text-link { white-space: nowrap; font-size: 12px; margin-top: 24px; }
  @media (max-width: 800px) {
    .site-header { flex-wrap: wrap; padding-block: 24px; gap: 24px; }
    .site-header nav { justify-content: flex-start; }
  }
  @media (max-width: 580px) {
    .gallery-intro { padding-block: 42px 32px; }
    .gallery-grid { gap: 36px; padding-bottom: 48px; }
    figcaption { flex-direction: column; gap: 14px; }
    figcaption .text-link { margin-top: 0; }
    figcaption h2 { font-size: 25px; }
  }
</style>
