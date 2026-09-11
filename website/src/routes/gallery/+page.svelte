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
</script>

<svelte:head>
  <title>Gallery — Nex Controller</title>
  <meta name="description" content="Explore the Nex Controller interface: hosts, virtual machines, networking, storage and activity, shown with fictional demo data." />
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
  .gallery-intro { padding-block: 70px 48px; }
  .gallery-description { max-width: 600px; margin-top: 24px; color: var(--muted); font-size: 16px; }
  .gallery-note { max-width: 740px; margin-top: 24px; padding-left: 16px; border-left: 2px solid var(--blue); color: var(--muted); font-size: 12px; }
  .gallery-grid { display: grid; gap: 52px; padding-bottom: 72px; }
  .gallery-card { min-width: 0; }
  .screenshot-link { display: block; border: 1px solid var(--line); background: #fff; }
  img { display: block; width: 100%; height: auto; }
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
