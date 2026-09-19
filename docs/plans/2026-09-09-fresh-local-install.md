# Fresh local Core and Controller installation

Date: 2026-09-09
Status: Historical fresh-installation and runtime checks passed with independent review.

## Scope

project owner requested a local installation and test of the latest Core and Controller.
Build matching current-source artifacts, install the branded ISO onto a new
disposable regular-file disk under KVM, and deploy the bundled Controller through
Core's local interface. Use isolated QEMU networking and loopback access; do not
change workstation disks, bridges, services, or previous lab installations.

Exercise the [first managed VM workflow](2026-09-09-first-managed-vm.md) through
the installed Controller and embedded Core agent. Locally starting the exact
disposable guest for boot verification does not introduce central power controls.
Normal guests are not configured to autostart by this creation slice; reboot
acceptance requires preserved identity, definition and disk, not automatic restart.

API impact: **existing contract** — test local authentication, appliance deployment,
Controller enrollment/adoption, pool resource registration, ISO transfer, VM
creation, and agent commands/results/observations without changing interfaces.
The later [pool model correction](2026-09-09-pool-master-model.md) changed
enrollment/bootstrap contracts; the appliance host now becomes the pool master of
an automatically created pool and its re-verification is tracked there.
Guide impact: **updated** — record actual packaged-install walkthrough evidence
and correct any instructions contradicted by that walkthrough.

## Acceptance

- [x] Retain isolated build inputs, matching Core/Controller source provenance,
      artifact digests and existing development signing identity.
- [x] Install through the actual installer onto a fresh selected disposable disk;
      inspect the logo and boot the installed system without its ISO.
- [x] Authenticate to Core, deploy the bundled Controller, and verify automatic
      enrollment/adoption using the installed embedded agent.
- [x] Register storage/network, upload an ISO, and create a stopped VM through
      the browser; inspect the real libvirt definition and guest boot.
- [x] Exercise relevant failure/recovery paths, Controller outage and host reboot;
      record actual outcomes and any untested limits.
- [x] Update guide verification and scoped assessment, run relevant checks, and
      obtain independent evidence review.

This is disposable virtual-machine verification, not physical hardware testing,
external release certification, or completion of the remaining Phase 2 lifecycle.
It does not cover the later ZFS-enabled `0.3.5` internal QA image.

## Executed evidence

The [fresh installation report](../qa/fresh-install/checks.md) identifies matching
artifacts, blank-disk installation, inspected logo, installed embedded-agent
behavior, GUI deployment/creation, guest boot, real Controller outage and host
reboot. The lab remains available through private loopback forwards. No product
binaries or interfaces required corrections during this run.
