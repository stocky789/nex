# First managed VM creation

Date: 2026-09-09
Status: Bounded creation workflow implemented and verified in a disposable nested-KVM lab; full Phase 2 closed 2026-09-17 ([close record](../qa/2026-09-17-phase2-gate/CLOSED.md)).

## Scope and decisions

project owner authorized the complete second item of the project progression: register
host-local storage and an existing guest network, upload an ISO, and create a
general VM with selected CPU, memory, disk, network and firmware. This is a bounded
part of [Phase 2](2026-09-08-nex-platform.md#2-one-host-vm-management), not completion
of the entire phase. Central power controls, browser consoles, stopped-VM editing
and deletion remain the next slice. Creation leaves a persistent VM stopped;
the UI must state that result explicitly.

project owner also confirmed that the installer installs correctly and looks good, with a
remaining distorted/rippled logo. Record this as operator feedback, without
inventing the installation environment, artifact digest or a fresh agent-run
walkthrough. A separate worker owns the logo correction. Website, repositories
and release/download work remain with their existing owners.

The logo correction (its QA record was removed; see git history) had native
before/after evidence: replace the textured reference-board crop with a dedicated
transparent lockup while preserving header allocation. API impact: **none** for
this local GTK presentation change; guide impact: **none** because installation
steps and recovery controls are unchanged. The final GTK initrd was tested; the
existing ISO remains unchanged until the next normal image build.

## End-to-end contract

API impact: **new contract** — authenticated browser storage/network registration,
ISO upload and VM creation requests become durable Controller operations; the
enrolled Core agent validates existing host resources, retrieves authorized ISO
content, and creates the libvirt definition and owned local disk. Extend the
existing command/result and observation contracts with capability negotiation,
resource verification and creation results. Preserve pool ownership, separate
host assignment, stable VM UUIDs, scoped host credentials and protected Controller
appliance identity. Agents retain outbound authenticated HTTPS. Since the
[pool model correction](2026-09-09-pool-master-model.md), the owning pool is the
one created automatically for the enrolled host, which is its pool master.

Use existing sessions, CSRF checks, enrollment, operation/audit records, libvirt
execution helpers and native storage/network capabilities. Registration never
formats disks or changes host networking. A pool registration grants access only
on the explicitly verified host. Interrupted uploads are unavailable; interrupted
creation reconciles existing identity and ownership before any repeated effects.
Unsupported agents and stale/incompatible resources fail closed. Long operations
must not block independent health reporting.

Guide impact: **updated** — document pool resource registration, media upload,
creation choices, asynchronous results, stopped outcome, errors and recovery with
the actual UI labels. Update the API schemas and generated reference alongside
implementation, and record scoped change assessments and executed QA evidence.

## Acceptance

- [x] Register existing local storage and an existing guest network for one
      enrolled host; show pending, failed and usable verification honestly.
- [x] Upload an ISO with authenticated transfer, bounded size/space, validated
      content/path handling and safe interrupted-upload/retry behavior.
- [x] Create a pool-owned persistent VM with stable identity and the selected
      CPU, memory, independent disk, network and supported firmware.
- [x] Reject unauthorized, wrong-host/pool, incompatible-agent, stale-resource,
      invalid-input, insufficient-capacity and conflicting/replayed requests.
- [x] Reconcile process restarts and lost responses without duplicate definitions
      or disks; leave existing VMs and appliance recovery unaffected.
- [x] Exercise the full Controller/agent/libvirt flow in a disposable lab and
      locally boot the resulting disposable guest to verify the generated domain
      and ISO attachment. Local QA power commands are not central power features.
- [x] Exercise actual desktop, narrow-screen and keyboard browser flows, including
      loading, empty, failed, stale and interrupted states; inspect captures.
- [x] Update the guide, API reference and scoped assessments; run appropriate Go,
      UI, contract, integration and documentation checks, with matching CI coverage.
- [x] Obtain independent implementation and evidence review, resolve findings and
      verify corrections before accepting this slice.

Full guest installation through a browser console, central lifecycle operations,
guest-data persistence and the complete Phase 2 outage gate remain required in
the following slice. This plan does not claim hardware, release, or hosted CI proof.

## Delivered evidence

[Executed checks and independent review](../qa/managed-vm/checks.md) link the real
Controller/agent/libvirt runs, BIOS/UEFI boot captures, browser walkthroughs,
selected-resource verification and final reporting-recovery supplement. Browser
presentation failures and secondary-host capability reports are explicitly
simulated. Both CI providers run portable regression checks; no hosted execution
or release artifact rebuild is claimed. The installed ISO and already deployed
Controller/Core components do not change until matching builds are deployed.

Scoped documentation coverage, changed-guide checks and the final combined
structural check passed. An earlier combined check was blocked while the concurrent
website guide's QA file was still being written; its owner completed that work.
See the QA report for actual checks and scope. No hosted CI execution is claimed.

## Host storage ISO visibility

project owner requested that each host's Storage view show its dispatched ISO images.
Display that host's pool-scoped media records with destination storage, size and
transfer result. A dispatched transfer is not yet available; distinguish successful
imports from pending, failed or uncertain outcomes. Qualify availability when host
state is stale, access revoked or refreshing data fails. The records describe Nex
imports, not a filesystem scan or continuous proof files remain unchanged.

API impact: **existing contract** — reuse authenticated media/storage/host reads;
the existing Controller import operation and Core validation/result flow already
record target host, storage, size and status. No command or API schema change.
Guide impact: **updated** — document host Storage navigation, availability labels
and the limits of last-confirmed import information.

Acceptance: implement the host view; run typecheck/build and focused browser
checks for host filtering, transfer states, empty/stale/error recovery and narrow
layout; independently review; verify the updated UI in the selected local lab.
This adds visibility without expanding media distribution or guest lifecycle scope.

Status: implemented and [verified](../qa/host-isos/checks.md); see the
[desktop layout plan](2026-09-09-controller-desktop-layout.md#site-selection-at-scale--2026-09-09).

## Public URL ISO downloads — 2026-09-11

The project owner requested a public URL alternative to selecting a local ISO in
Controller. Keep both sources in **ISO library → Upload ISO**, with an explicit
validated storage destination. The selected Nex Core host downloads public HTTP(S)
media itself after Controller submits the request; Controller does not become a
required download worker. This adds a source to the existing host-local import,
not automatic distribution to every pool host.

API impact: **new contract** — authenticated, CSRF-protected
`POST /api/v1/media/download` accepts `request_id`, `storage_id`, `name`, and `url`.
The existing `iso-import` command carries an optional URL, gated by the host's
`iso-download-v1` capability. Core validates public destinations at connection time
and on redirects, bounds transfer time and storage consumption, validates ISO
content, computes SHA-256, and publishes only completed media. Existing operation
results and managed inventory report completion and the measured size/hash.
Uncertain submissions retain their request identity; a terminal failure requires a
new request after correcting its cause. Downloads continue across Controller
outages; Core restart reconciles its durable journal before reporting a result.
Upgrade Controller before Core for the extended report schema.

Durable state owner: **core** — the pool master's managed-VM journal records ISO
identity, name, storage and resolved size/hash; the image lives in the selected
storage's `nex-media` directory. Managed inventory and enrollment/report adoption
recover these on a fresh Controller. Current pools contain only their master;
future member distribution remains Phase 4 work.

### URL download progress — 2026-09-25 bug fix

Core samples received bytes and the public server's positive `Content-Length` during an
active URL import. Its existing managed journal owns the observation; ordinary authenticated
reports carry `iso_downloads` after Controller support is acknowledged. Controller validates
the operation and host, mirrors only the latest observation in host inventory and renders it
in **Storage** and **Tasks**. Unknown total size gives an indeterminate bar, stale host data
is not live progress, and task success still requires complete ISO validation. Progress clears
after a terminal or interrupted import; Controller neither downloads nor schedules it.

API impact: **changed contract** — additive `ManagedState.iso_downloads` on the Core report
and `GET /api/v1/hosts` inventory, plus a Controller acknowledgement for old-version
compatibility. The `iso-import` command, submission, result and retry rules are unchanged.
Guide impact: **updated** — the VM creation guide explains the bar and its limits.
State owner: **core** — the host managed journal records bounded byte samples for an active
import; a fresh Controller adopts the completed ISO from Core inventory and ignores progress
for an operation it never submitted. No new Controller-owned state.
See [progress checks](../qa/2026-09-25-iso-url-progress.md) for executed evidence and limits.

Guide impact: **updated** — document both source choices, host internet access,
public-only URL restrictions, task results, and safe retry behavior in the
[VM creation guide](../guide/create-virtual-machine.md).

Acceptance requires focused Core and Controller checks for validation, redirects,
size/content failures, replay, completed metadata and recovery; UI typecheck/build
and browser success/error/keyboard/stale checks; API reference and documentation
checks; independent review. Actual results and any limits belong in
[URL ISO verification](../qa/iso-url/checks.md). This slice does not close the
remaining Phase 2 gate or rebuild installed packages or installation media.

URL source implementation, focused Core/Controller checks, browser verification with
mocked APIs, documentation gates and independent review passed; see the
[executed evidence and limits](../qa/iso-url/checks.md). The source slice is complete.
A real public Internet ISO transfer, packaged upgrade and physical-host validation
were not run, and no installation image or live deployment was changed.


### Queue power behind pending tasks — 2026-09-11

Start and other power requests can be accepted behind pending power/edit/media tasks on
Core advertising vm-power-queue-v1. Core owns durable admission order and same-VM prerequisite
checks; Controller sends one API request and displays results. No frontend retry loop and
no automatic retry of real failures. ACPI task completion remains distinct from guest state.

API impact: changed contract — optional VMPowerRequest.after_operation_id, queue capability,
FIFO report dispatch and queued/terminal managed-outcome adoption. Existing auth, replay and
outage safety remain. Old Core retains its busy guard; upgrade together for queue behavior.
Guide impact: updated in the VM power guide. State owner: Core agent state.json command
journal (order and local prerequisite) plus managed-vms execution journal; inventory outcomes
recover the queue in a fresh Controller without redispatch. Core receipt establishes host
persistence; HTTP acceptance alone only establishes pending delivery.
Evidence and limits: [power queue QA](../qa/power-queue/checks.md).
