# Host-local Controller appliance bootstrap

Date: 2026-09-08
Status: Bounded appliance development slice accepted in disposable nested KVM, including clean package deployment, separate-host manual recovery and replacement-host reboot; Core installation-media integration remains open.

## Authorized scope

Matt requested a lightweight web UI on each Nex Core host, basic local VM controls,
and a Deploy Controller action that automatically provisions a Debian virtual
appliance and reports progress. Controller has no installation ISO. Matt authorized
parallel implementation and delegated the appliance mechanism and resource defaults.

This prioritizes a bounded appliance bootstrap slice from Phase 3, including its
required local VM execution foundation, before the rest of Phase 2. It does not
mark general VM creation, uploaded installation media, browser consoles, shared
storage, migration, or either complete phase as delivered. The
[platform safety and recovery requirements](2026-09-08-nex-platform.md#host-local-ui-and-controller-appliance-bootstrap)
remain applicable. Existing standalone installer work is preserved.

## Implementation decisions

- Prepare a Debian 13 genericcloud qcow2 image with Controller, its GUI and matching
  source, cloud-init, and the guest agent already installed. Deployments boot this
  image with per-instance NoCloud configuration rather than running Debian Installer.
  Debian's image named `nocloud` is different and does not run cloud-init.
  See [Debian cloud images](https://cloud.debian.org/images/cloud/) and
  [cloud-init NoCloud](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html).
- Start with 2 vCPUs, 4 GiB RAM, and a 32 GiB independent local disk, subject to
  capacity checks and host reserves. These are Nex development defaults, to be
  verified in the lab, not a universal sizing guarantee.
- Select existing usable local storage and an existing guest-capable bridge;
  support DHCP or explicit static IPv4 configuration. DHCP discovery uses the
  owned appliance guest agent and management MAC; no address is guessed. Missing bridge or capacity blocks deployment; the
  local GUI does not edit a live management network.
- Verify a Nex-signed appliance manifest and image digest against a separately
  provisioned trusted key. No embedded signing private key or shared credentials.
  Upstream Debian cloud-image transport/checksums and Nex artifact signatures are
  separate checks; neither establishes complete redistribution evidence.
- Persist request identity, VM UUID and progress before mutation. Refresh/retry
  resumes or reports the existing operation. Unknown outcomes require reconciliation,
  never another writable VM. Protect owned disks and retain partial results for recovery.
- Initialize Controller credentials and TLS per deployment, then create the first
  Site/Pool, enroll this host, and adopt the original appliance UUID. Remove bootstrap
  material after successful handoff. A running VM alone is not readiness.
  **Note, 2026-09-09:** bootstrap now creates the Site only; the pool is created at
  enrollment with this host as its pool master, and the deploy request carries no pool
  name. See the [pool master model](2026-09-09-pool-master-model.md).
- Local authenticated HTTPS remains independent of Controller. Restrict power
  actions to proven local ownership and serialize through the same VM engine.
  Controller power actions and general VM lifecycle remain separate Phase 2 work.
- Deliver a manual quiesced Controller state/identity export and replacement import;
  require exclusion of the original before activation. Same-host snapshots are not
  recovery evidence. No automatic failover or concurrent Controllers.

## API and guide impact

API impact: **new contract** — browser calls authenticated host-local session,
inventory, appliance deployment/status, and local power APIs; local operations drive
libvirt and report durable observations. Pinned HTTPS bootstrap uses Controller
administrator authorization, existing enrollment/report contracts, and new idempotent
bootstrap/adoption operations. VM identity belongs to its Pool with separate host
assignment. Machine-readable local and central references, authorization, validation,
retry/reconnect semantics and runnable checks ship with implementation.

Guide impact: **updated** — document actual host setup, trust, resource/network
selection, deployment progress, first Controller login, local recovery and export/import.
Keep development and simulation limits explicit. No invented successful walkthroughs.

## Acceptance evidence

- [x] Signed image verification and resource/network blockers exercised.
- [x] Local login, authorization, CSRF, invalid input and secret handling checked.
- [x] Real Debian appliance boot and service initialization in a disposable KVM lab.
- [x] Browser deployment reaches verified Controller and enrolled/adopted inventory.
- [x] Refresh, lost response and local-service restart preserve one VM/operation; replacement-host reboot autostarts the restored VM and restores login/reconnection.
- [x] Controller outage retains local access; safe local power recovery works.
- [x] Bootstrap material removed without breaking subsequent boots.
- [x] Quiesced export and isolated replacement recovery verified, including existing-host reconnection.
- [x] Desktop/narrow/keyboard and loading/error/unknown states captured and reviewed.
- [x] API references, focused tests, guide walkthrough and documentation checks pass.
- [x] Independent implementation and evidence review findings resolved.
- [x] Host Debian package installation and automatic appliance deployment verified end to end.
- [ ] Bundle the host package and signed appliance into Nex Core installation media
  (source hooks land `nex-core-local` in the offline repo; rebuilt-ISO boot verification remains).

Evidence: [clean deployment and recovery](../qa/appliance/lab.md),
[package installation](../qa/appliance/package.md), [browser states](../qa/appliance/browser.md),
and [checks/review](../qa/appliance/checks.md). Simulated lost-response/error checks
are distinguished from real KVM boot, power and service-restart results.

Real physical hardware, whole-image redistribution clearance, hosted CI execution,
and full Phase 2/3 acceptance remain separate evidence gates.

## Console access URL

The local UI package publishes its configured/discovered HTTPS management address and actual
port (9443) to a native pre-login issue fragment, refreshing waiting login prompts. It does
not expose credentials. Initialization failure or service stop removes the advertised URL.
Automatic selection refreshes on service restart; live DHCP rebinding is not introduced.
API impact: none — existing local service configuration drives console presentation; no
Controller operation, agent command/result or client-facing API changes. Guide impact: updated.
The Core ISO build bundles `nex-core-local` from `dist/core-local/` (or `CORE_LOCAL_DEB`)
into the offline install repository and enables the service on first boot.

## Host-local root login — 2026-09-08

Matt requested one host credential: the local web UI authenticates `root` against native
PAM using the password set by the installer. Remove generated web credentials; password
changes apply to new logins, while established sessions retain their existing expiry.
Locked/expired accounts and unavailable authentication fail closed. Controller appliance
credentials remain separate. API impact: changed contract — local session POST/GET now
require/return root; request shape, session protections and agent commands are unchanged.
Guide impact: updated — root login, password changes, upgrade cleanup and recovery.
Validation and delivery limits are recorded in [root authentication evidence](../qa/appliance/root-auth.md).

## Installer management bridge — 2026-09-08

Matt requested that choosing the GUI management NIC produce a deployment-ready network.
Create one native ifupdown `br0`, with the selected MAC-bound NIC or VLAN as its port;
assign management addressing to br0. Preserve secondary NIC policy and offline mode.
Apply only to staged fresh-install configuration; do not migrate live hosts remotely.
API impact: existing contract — local service discovery already recognizes the default-route
Linux bridge and reports it through existing preflight/deployment contracts. No Controller
operation or agent command changes. Guide impact: updated — automatic bridge availability
and host IP placement. Native validation: [bridge QA](../qa/iso/2026-09-08-management-bridge.md).

## Deployment usability — 2026-09-08

Matt requested easier Controller deployment: DHCP as an option, “Network settings”
label, removal of the sixteen-character password minimum, removal of typed host-name
confirmation for local VM power actions, and removal of the routine Observed timestamp badge.
New UI drafts default to DHCP and reveal static address/router/DNS only when selected.
Keep explicit VM/action confirmation and force-stop consequences, authenticated API
authorization, ownership checks and stale/disconnected action protection. Passwords
remain nonempty with existing safe transport/input bounds; no complexity requirement.
This intentionally supersedes the prior sixteen-character bootstrap policy.

API impact: changed contract — local deployment/recovery accepts `network_mode`,
with omission preserving static behavior; async DHCP address discovery feeds existing
Controller URL/results. Persist a trusted TLS server name for agent connections to
DHCP addresses without bypassing certificate verification. Existing Controller bootstrap
accepts short passwords. Local power calls no longer require typed `confirm_host`.
No new Controller command or resource hierarchy. Guide impact: updated.
Matched new appliance firstboot/Controller binary and host package are required before
shipping this behavior; an old bundled appliance retains the old password minimum.
Evidence and unexecuted limits: [deployment usability QA](../qa/appliance/deployment-usability.md).
