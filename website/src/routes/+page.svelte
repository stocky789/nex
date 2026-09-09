<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
  import "../app.css";
</script>

<svelte:head>
  <title>Nex — Virtualization, built together</title>
  <meta
    name="description"
    content="A community-driven virtualization project. Help build Nex Core and Nex Controller on Debian, KVM and libvirt."
  />
</svelte:head>

<a class="skip" href="#main">Skip to content</a>
<div class="preview-bar">
  <span class="preview-dot" aria-hidden="true"></span> Private preview
  <span class="preview-divider">/</span> In development
</div>
<header class="site-header wrap">
  <a class="brand" href="#main" aria-label="Nex home"
    ><span class="wordmark" aria-hidden="true"></span
    ><span class="brand-divider"></span
    ><span class="brand-caption">Virtualization,<br />built together.</span></a
  >
  <nav aria-label="Main navigation">
    <a href="#platform">Platform</a><a href="#approach">Approach</a
    ><a href="#questions">Questions</a><a href="/gallery/">Gallery</a
    ><a class="nav-release" href="#releases"
      >Releases <span aria-hidden="true">↗</span></a
    >
  </nav>
</header>

<main id="main">
  <section class="hero wrap" aria-labelledby="hero-title">
    <div class="hero-copy">
      <p class="eyebrow"><span class="small-line"></span> A community-driven project</p>
      <h1 id="hero-title">
        Virtualization.<br /><span>Built together.</span>
      </h1>
      <p class="hero-description">
        A Debian-based host OS and web interface for managing virtual machines.
        Built on KVM and libvirt, shaped by the people who use them.
      </p>
      <div class="hero-actions">
        <a class="button" href="#platform"
          >What we’re building <span aria-hidden="true">↗</span></a
        ><a class="text-link" href="#releases"
          >Development status <span aria-hidden="true">→</span></a
        >
      </div>
      <p class="development-note">
        <span aria-hidden="true">◌</span> Early development. Public builds are not available yet.
      </p>
    </div>
    <figure class="architecture">
      <div class="diagram-index">
        <span>NX / SYSTEM OVERVIEW</span><span>01 — 02</span>
      </div>
      <div class="architecture-drawing">
        <div class="orbit orbit-one"></div>
        <div class="orbit orbit-two"></div>
        <div class="control-slab">
          <div class="slab-top">
            <span class="diagram-icon" aria-hidden="true">⌘</span
            ><span class="slab-number">01 / CONTROL PLANE</span>
          </div>
          <h2>Nex Controller</h2>
          <p>Manage your hosts and virtual machines.</p>
          <div class="slab-footer">
            <span>Inventory</span><span>Operations</span><span>Visibility</span>
          </div>
        </div>
        <div class="diagram-connector">
          <span>Commands + host status</span>
        </div>
        <div class="core-slab">
          <div class="slab-top">
            <span class="host-icon" aria-hidden="true"><i></i><i></i></span
            ><span class="slab-number">02 / HOST FOUNDATION</span>
          </div>
          <h2>Nex Core</h2>
          <p>Where your virtual machines run.</p>
          <div class="slab-footer">
            <span>Debian</span><span>KVM / QEMU</span><span>libvirt</span>
          </div>
        </div>
      </div>
      <figcaption>
        <span class="crosshair" aria-hidden="true">+</span> Core + Controller <span>Architecture overview</span>
      </figcaption>
    </figure>
  </section>

  <div class="foundation-strip">
    <div class="wrap foundation-inner">
      <p>Established foundations.<br /><strong>One shared project.</strong></p>
      <span>Debian<span class="foundation-tag">OPERATING SYSTEM</span></span
      ><span>KVM / QEMU<span class="foundation-tag">VIRTUALIZATION</span></span
      ><span>libvirt<span class="foundation-tag">HOST MANAGEMENT</span></span>
    </div>
  </div>

  <section
    class="platform wrap section-space"
    id="platform"
    aria-labelledby="platform-title"
  >
    <div class="section-heading">
      <p class="eyebrow">01 / The platform</p>
      <div>
        <h2 id="platform-title">Two parts.<br />One platform.</h2>
        <p>
          Nex Core runs on your hosts. Nex Controller gives you a web interface
          to manage them.
        </p>
      </div>
    </div>
    <div class="product-grid">
      <article class="product core-product">
        <div class="product-top">
          <span class="product-number">01</span
          ><span class="label">The host</span>
        </div>
        <h3>
          Nex Core<span class="product-symbol" aria-hidden="true">↳</span>
        </h3>
        <p>
          A Debian-based host OS using KVM/QEMU and libvirt to run your VMs.
          A local agent connects each host to Nex Controller.
        </p>
        <ul>
          <li>KVM/QEMU virtual machines</li>
          <li>Local management and recovery access</li>
          <li>Standard Debian packages and services</li>
        </ul>
        <div class="product-bottom">
          Runs on your hosts
          <span aria-hidden="true">↓</span>
        </div>
      </article>
      <article class="product controller-product">
        <div class="product-top">
          <span class="product-number">02</span
          ><span class="label">The control plane</span>
        </div>
        <h3>
          Nex Controller<span class="product-symbol" aria-hidden="true">↗</span>
        </h3>
        <p>
          A web interface and API for managing your infrastructure.
          View hosts, follow tasks and organize resources from a dedicated VM.
        </p>
        <ul>
          <li>Central inventory and host enrollment</li>
          <li>Task history and reported host status</li>
          <li>A dedicated Controller virtual appliance</li>
        </ul>
        <div class="product-bottom">
          Runs in a dedicated VM <span aria-hidden="true">↑</span>
        </div>
      </article>
    </div>
    <p class="scope-note">
      Features are under development.
      <a href="#releases">Read the release status →</a>
    </p>
  </section>

  <section class="approach" id="approach" aria-labelledby="approach-title">
    <div class="wrap approach-inner">
      <div class="approach-heading">
        <p class="eyebrow">02 / Our approach</p>
        <h2 id="approach-title">Build it.<br />Try it. Improve it.</h2>
        <p>
          Practical tools, shared knowledge and contributions of every kind.
        </p>
        <div class="principle-mark" aria-hidden="true">
          <span></span><span></span><span></span>
        </div>
      </div>
      <div class="principles">
        <article>
          <span>01</span>
          <div>
            <h3>Use what already works.</h3>
            <p>
              Use Debian, KVM and libvirt for the work they already do well.
              Put our effort into making them easier to manage together.
            </p>
          </div>
        </article>
        <article>
          <span>02</span>
          <div>
            <h3>Make room for contributions.</h3>
            <p>
              Help with code, design, documentation, testing or bug reports.
              Every contribution helps shape the project.
            </p>
          </div>
        </article>
        <article>
          <span>03</span>
          <div>
            <h3>Test the things that can go wrong.</h3>
            <p>
              Check failure and recovery in disposable environments.
              Test that backups can actually be restored.
            </p>
          </div>
        </article>
      </div>
    </div>
  </section>

  <section
    class="releases wrap section-space"
    id="releases"
    aria-labelledby="releases-title"
  >
    <div class="section-heading">
      <p class="eyebrow">03 / Development</p>
      <div>
        <h2 id="releases-title">
          Development<br />status.
        </h2>
        <p>
          The next step is a development build the community can test.
        </p>
      </div>
    </div>
    <div class="release-panel">
      <div class="release-summary">
        <span class="status-label"
          ><span aria-hidden="true"></span> In development</span
        >
        <h3>The foundation<br />is taking shape.</h3>
        <p>
          The installer and early Controller have been tested in a disposable environment.
          VM workflows, recovery and updates still need work before a release.
        </p>
      </div>
      <div class="release-details">
        <div>
          <span class="release-kicker">Public downloads</span
          ><strong>Not available yet</strong>
          <p>
            Images and packages will be shared when testing and
            redistribution requirements are met.
          </p>
        </div>
        <div>
          <span class="release-kicker">Next milestones</span
          ><strong>VM lifecycle, recovery and updates</strong>
          <p>
            Create and manage VMs, restore their data and update
            existing installations safely.
          </p>
        </div>
        <div class="release-footnote">
          A release date has not been set.
        </div>
      </div>
    </div>
  </section>

  <section
    class="questions wrap"
    id="questions"
    aria-labelledby="questions-title"
  >
    <div>
      <p class="eyebrow">04 / Questions</p>
      <h2 id="questions-title">Good to know.</h2>
    </div>
    <div class="faq-list">
      <details>
        <summary>
          Is Nex a new hypervisor?<span aria-hidden="true">+</span>
        </summary>
        <p>
          No. Nex Core uses KVM/QEMU and libvirt on Debian. Nex adds a host
          distribution and management experience around those established
          components.
        </p>
      </details>
      <details>
        <summary>
          How do Core and Controller fit together?<span aria-hidden="true"
            >+</span
          >
        </summary>
        <p>
          Core runs on your virtualization hosts. Controller is a separate
          management service and web interface. It organizes hosts and VMs
          into sites and pools.
        </p>
      </details>
      <details>
        <summary>
          Can I download or use Nex today?<span aria-hidden="true">+</span>
        </summary>
        <p>
          Public downloads are not available yet. Installation, upgrades and
          recovery are being tested before the first release.
        </p>
      </details>
      <details>
        <summary>
          How can I contribute?<span aria-hidden="true">+</span>
        </summary>
        <p>
          Code, testing, design, documentation and bug reports are welcome.
          We’ll link the repository and contribution guide here when they’re public.
        </p>
      </details>
      <details>
        <summary>Is Nex open source?<span aria-hidden="true">+</span></summary>
        <p>
          Original Nex code is licensed under AGPL-3.0-only. Debian and other
          third-party components retain their own licenses. The Nex name and
          artwork have separate terms; a complete ISO is not relicensed as AGPL.
        </p>
      </details>
    </div>
  </section>
</main>

<footer class="wrap">
  <div class="footer-top">
    <a class="brand" href="#main" aria-label="Nex home"
      ><span class="wordmark" aria-hidden="true"></span
    ></a>
    <p>Community-driven.<br />Built together.</p>
    <a class="text-link" href="#main"
      >Back to top <span aria-hidden="true">↑</span></a
    >
  </div>
  <div class="footer-bottom">
    <span>© 2026 Nex.</span
    ><span>Debian · KVM · libvirt</span>
  </div>
</footer>
