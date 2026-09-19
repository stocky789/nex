# Nex user guide

This living guide covers implemented development workflows for **Nex Core**, the
Debian-based host distribution and local management service, and **Nex Controller**,
the separate management service and web UI. An installed host reports one version,
`Nex Core <version>`. Use disposable environments; each page states the build and
verification limits that apply to its workflow.

1. [Install Nex Core in a disposable VM](install-nex-core.md).
2. [Verify the installed Nex Core host](verify-nex-core.md).
3. [Set up Nex Controller and enroll a host](manage-controller.md).
4. [Deploy the Controller appliance from a Core host](deploy-controller-appliance.md)
   (bundled appliance and Core-local deployment workflow; development acceptance limits apply).
5. [Secure Controller and host web access](secure-web-access.md)
   (security development changes and verification limits).
6. [Explore the private HTTPS website](website-preview.md)
   (product information only; separate from Controller management).
7. [Operate the private website](operate-website.md)
   (authorized maintainers; nginx releases, monitoring and rollback).
8. [Add VM storage repositories](storage-repositories.md)
   (local directories, NFSv4, SMB3, iSCSI and OpenZFS; new ZFS provisioning is restricted
   to internal disposable labs).
9. [Join an enrolled Nex Core to a pool](join-a-pool.md)
   (development local-API workflow; per-host shared-storage access and guest-network/ISO
   convergence, with native acceptance limits).
10. [Register resources and create a virtual machine](create-virtual-machine.md)
11. [Rename or remove a pool VLAN network or an ISO](remove-pool-network-or-iso.md)
   (pool-wide removal on the master with typed confirmation; members converge, and in-use
   resources are refused).
12. [Edit a virtual machine](edit-virtual-machine.md)
   (change a VM in place on its General tab; live changes where the guest can take them).
13. [Delete a virtual machine](delete-virtual-machine.md)
   (remove a stopped guest; keep or permanently delete disk files).
14. [Control VM power](vm-power.md)
   (start, shut down, reboot, force stop, and the offered eject of install media after an
   install).
15. [Open a VM or host console](vm-console.md)
   (toolbar with send keys, display scaling and resize, clipboard, typing, screenshots,
   CD/DVD insert/eject on VMs, and a Console tab on hosts).
16. [Install guest tools and read guest information](guest-tools.md)
   (QEMU guest agent and spice-vdagent; OS, hostname, addresses and clipboard readiness).
17. [Read VM and host usage history](usage-history.md)
   (Stats tab charts up to a year back, with Stacked, zoom and legend toggles).
18. [Check and apply software updates](software-updates.md)
   (Controller and host-local package updates from a configured repo).
19. [Publish a Nex Core release](publish-a-release.md)
   (authorized maintainers; tag-driven ISO and package build, and website publication).

Each procedure states its prerequisites, expected outcome, troubleshooting, and verification
limits. A documented procedure is not automatically a tested procedure: check its Verification
section for the exact evidence and outstanding checks.

Pools are created automatically: each enrolled host becomes the pool master of its own pool.
Managed snapshots/backups/restores, migrations, HA, and Docker Container Services
remain **planned and unavailable**; see the [roadmap](../plans/2026-09-08-nex-platform.md).

The [API reference](../API.md) tracks implemented Nex interfaces separately. Build developers
should use the [ISO build instructions](../../installer/README.md) or
[Controller development instructions](../development.md).
