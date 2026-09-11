# First managed VM creation

Date: 2026-09-09
Status: Bounded creation workflow implemented and verified in a disposable nested-KVM lab; full Phase 2 remains open.

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

The [logo correction](../qa/iso/2026-09-09-logo-cleanup.md) now has native
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

The requested host ISO view and final single-dropdown site selector are now
implemented and [verified in the selected local Controller](../qa/host-isos/checks.md).
Final browser checks cover transfer/status isolation, the searchable 500-site
fixture, keyboard focus and eight selector groups; all 16 existing Controller
walkthrough groups also passed. The local UI/source update preserves the backend
binary and is not a rebuilt installation/release image.
