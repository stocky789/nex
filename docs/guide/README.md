# Nex user guide

This living guide covers implemented operator workflows: **Nex Core** (development alpha; an
installed host reports one version, `Nex Core <version>`), a standalone Debian-derived host, and the separate **Phase 1 Controller/agent development
build**. Both are for local lab evaluation; the installer does not yet deliver agent enrollment.

1. [Install Nex Core in a disposable VM](install-nex-core.md).
2. [Verify the installed Nex Core host](verify-nex-core.md).
3. [Set up Nex Controller and enroll a host](manage-controller.md).
4. [Deploy the Controller appliance from a Core host](deploy-controller-appliance.md)
   (separate development package).
5. [Secure Controller and host web access](secure-web-access.md)
   (security development changes and verification limits).
6. [Explore the private HTTPS website](website-preview.md)
   (product information only; separate from Controller management).
7. [Operate the private website](operate-website.md)
   (authorized maintainers; nginx releases, monitoring and rollback).
8. [Register resources and create a virtual machine](create-virtual-machine.md)
9. [Control VM power](vm-power.md)
10. [Open a VM or host console](vm-console.md)
   (toolbar with send keys, display scaling and resize, clipboard, typing, screenshots,
   CD/DVD insert/eject on VMs, and a Console tab on hosts).
11. [Install guest tools and read guest information](guest-tools.md)
   (QEMU guest agent and spice-vdagent; OS, hostname, addresses and clipboard readiness).
12. [Check and apply software updates](software-updates.md)
   (Controller and host-local package updates from a configured repo).
13. [Publish a Nex Core release](publish-a-release.md)
   (authorized maintainers; tag-driven ISO and package build, and website publication).

Each procedure states its prerequisites, expected outcome, troubleshooting, and verification
limits. A documented procedure is not automatically a tested procedure: check its Verification
section for the exact evidence and outstanding checks.

Controller's development build includes its web GUI, administrator login, sites,
enrollment (**Add host** by address and root password, or a manual token), observed host
health and inventory, and read-only inventory refresh tasks.
Pools are created automatically: each enrolled host becomes the pool master of its
own pool, so there is no pool-creation step.
The separate appliance slice adds local deployment and restricted VM power recovery.
The managed-VM creation slice adds pool-owned local storage, VLAN or existing guest networks,
ISO upload and creation of a stopped VM with multiple disks and network interfaces, either
memory mode, an explicit boot order and optional autostart, with separate verification evidence.
Editing and deleting a VM, managed snapshots/backups/restores,
migrations, HA, and Docker Container Services remain **planned and unavailable**.
Their intended behavior belongs in the
[roadmap](../plans/2026-09-08-nex-platform.md).

The [API reference](../API.md) tracks implemented Nex interfaces separately. Build developers
should use the [ISO build instructions](../../installer/README.md) or
[Controller development instructions](../development.md).

These are ordinary Markdown pages with relative links and stable filenames, ready for a future
documentation website. Keep this index current when adding or retiring a procedure; preserve
versioned release copies when publishing release documentation.
