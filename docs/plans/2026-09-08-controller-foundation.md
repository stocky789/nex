# Phase 1: Controller foundation and host enrollment

Date: 2026-09-08
Status: Implemented; Phase 1 development acceptance passed. The full active
working-tree documentation check remains blocked by concurrent installer evidence.

## Authorized scope

Matt authorized completing Phase 1 with GPT-6 Astra agents at medium effort while
another agent finishes the installer. This permits concurrent Controller/agent
development, superseding the installer-first sequencing for this slice. The
[platform plan](2026-09-08-nex-platform.md#1-controller-foundation-and-host-enrollment)
still defines the product model and acceptance requirements. Installer files and
its existing working-tree changes belong to the installer workstream.

Deliver a Go Controller backed by local SQLite, a Go Core agent, and a React and
TypeScript UI: administrator bootstrap/login, Sites → Pools → Hosts, secure host
enrollment and certificate lifecycle, observed inventory, independent bounded
health reporting, and durable operations with safe retry and reconnect behavior.
Use a read-only inventory refresh command to exercise command delivery and result
reconciliation. VM lifecycle commands and browser consoles remain Phase 2.

API impact: **new contract** — authenticated browser/API clients create inventory
and enrollment intent; agents enroll over verified HTTPS, then use scoped host
certificates for outbound heartbeats, observations, command retrieval and results.
Implement both ends, compatibility rules, OpenAPI, offline generated reference,
authorization/validation and failure/retry checks together.

Guide impact: **updated** — add real development-version Controller setup, host
enrollment, monitoring and recovery workflows with actual walkthrough evidence.

## Implementation decisions

- Keep SQLite on local disk with one Controller process. Use stdlib Go HTTP/TLS
  and certificate support, with a reviewed SQLite driver.
- Phase 1 may use bounded, read-only `virsh` subprocesses for libvirt observations;
  process isolation permits deadlines for stalled native calls. This is a scoped
  change from the proposed Go bindings, not a new virtualization implementation.
  Reassess bindings when Phase 2 introduces VM operations.
- Test with disposable local application state, explicitly simulated failure
  inputs and a separately named disposable Debian container with actual libvirt.
  Packages and service stop/stall tests are confined to that container; do not
  install services, modify networks, erase disks or reboot live infrastructure.
  Real hardware and full Phase 0 proof remain separate gates.
- Keep the approved light/blue UI, visible freshness and capability status,
  keyboard access, narrow-screen layouts and genuine durable task outcomes.

## Acceptance checklist

- [x] Bootstrap administrator without a shared default; reject unauthorized calls.
- [x] Create site/pool, enroll, rotate, revoke and re-enroll a uniquely identified host.
- [x] Restart either process without losing identity or duplicating operations.
- [x] Interrupt connectivity and recover fresh inventory and reconciled results.
- [x] Distinguish stale connectivity, libvirt failure and a stalled probe while
      other observations continue; monitoring never restarts guests.
- [x] Verify API schemas, implemented route coverage, version compatibility,
      validation, host isolation and retry behavior with runnable checks.
- [x] Exercise desktop, 360px and keyboard UI flows; inspect captures for empty,
      loading, failed, stale and confirmation states.
- [x] Walk the guide against the actual development build and record evidence.
- [x] Run focused Go, UI, integration, API and documentation checks; configure
      matching GitHub/Gitea push and pull-request checks without claiming hosted
      execution until actual runs have been inspected.
- [x] Obtain independent review, correct findings and verify corrections.

**Note, 2026-09-09:** manual pool creation in the checklist above was removed by the
[pool master model correction](2026-09-09-pool-master-model.md); administrators create
sites only and each enrolling host becomes the master of its automatically created pool.

Acceptance is supported by [final checks and build evidence](../qa/phase1/checks.md),
[process/browser walkthroughs](../qa/phase1/acceptance.md), [native Debian/libvirt
checks](../qa/phase1/debian.md), and [independent review](../qa/phase1/review.md).
The scoped documentation range passed for 69 changed paths. The complete active
working tree still has the installer agent's missing wizard-QA link; this slice
neither removes that requirement nor claims a green combined installer change.

The Controller and agent are development builds. Real systemd deployment,
physical-host and guest-persistence proof, installer enrollment, hosted CI and
redistribution certification remain outside this accepted development slice.
Phase 2 adds VM creation/lifecycle and browser consoles using these foundations.
