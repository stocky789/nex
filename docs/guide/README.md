# Nex user guide

This living guide covers implemented development workflows for **Nex Core**, the
Debian-based host distribution and local management service, and **Nex Controller**,
the separate management service and web UI. An installed host reports one version,
`Nex Core <version>`. Use disposable environments; each page states the build and
verification limits that apply to its workflow.

1. [Install Nex Core in a disposable VM](install-nex-core.md).
2. [Verify the installed Nex Core host](verify-nex-core.md).
3. [Set up Nex Controller and enroll a host](manage-controller.md).
4. [Navigate the Controller console](navigate-the-controller-console.md)
   (rail, scope menu, search, activity pill, lists with slide-over panels, Tasks, Events and
   Settings; phase-gated screens and per-browser conveniences).
5. [Deploy the Controller appliance from a Core host](deploy-controller-appliance.md)
   (bundled appliance and Core-local deployment workflow; development acceptance limits apply).
6. [Secure Controller and host web access](secure-web-access.md)
   (security development changes and verification limits).
7. [Explore the private HTTPS website](website-preview.md)
   (product information only; separate from Controller management).
8. [Operate the private website](operate-website.md)
   (authorized maintainers; nginx releases, monitoring and rollback).
9. [Add VM storage repositories](storage-repositories.md)
   (local directories, NFSv4, SMB3, iSCSI and OpenZFS; new ZFS provisioning is restricted
   to internal disposable labs).
10. [Join a host to a pool](join-a-pool.md)
   (add a host from the pool's **Settings** → **Members**, with the host-local procedure as the
   recovery path; per-host shared-storage access and guest-network/ISO convergence, with native
   acceptance limits).
11. [Put a pool host into maintenance](host-maintenance-mode.md)
   (stop new placements, starts and resumes on one host, including guests on its own local storage
   and guests that would start themselves after a reboot; running guests are untouched).
12. [Pool authority and recovery](pool-authority-and-recovery.md)
   (read the copy of the pool master's authority each member holds; change which host is the pool
   master; eject a host from a pool; let each host's own Nex Core configure its management
   controller for fencing, or set BMC access by hand where it cannot; remove that account again;
   read each host's **Fencing** check, which says whether each host could be shut out and by
   which other hosts — the hosts check each other's controllers continuously and never power
   anything off; promote a surviving member when the pool master is lost; resume a master change whose target
   died; what a restricted host is and how it clears. Core can automatically promote an eligible two-host pool only with Host fencing enabled and a recorded usable BMC fence profile; otherwise manual promotion remains available when its fence proof can be established. A promotion may power off more than one host. Watchdog-based promotion is supported only through the manual workflow in pools of three or more hosts; storage-lease takeover and witness-based promotion remain planned, while Controller-hosted witness registration and status are available in development).
13. [Change a host's management network](change-management-network.md)
   (stage, apply and confirm a host's own addressing, with the timed rollback the host performs
   itself; console recovery and native verification limits apply).
14. [Register resources and create a virtual machine](create-virtual-machine.md)
15. [Rename or remove a pool VLAN network or an ISO](remove-pool-network-or-iso.md)
   (pool-wide removal on the master with typed confirmation; members converge, and in-use
   resources are refused).
16. [Edit a virtual machine](edit-virtual-machine.md)
   (change a VM in place from its panel's **Configuration** tab; live changes where the guest
   can take them).
17. [Tag virtual machines](tag-virtual-machines.md)
   (label VMs with tags kept on the pool master; filter and search the VM list by tag).
18. [Delete a virtual machine](delete-virtual-machine.md)
   (remove a stopped guest; keep or permanently delete disk files).
19. [Control VM power](vm-power.md)
   (start, shut down, reboot, force stop, and the offered eject of install media after an
   install).
20. [Open a VM or host console](vm-console.md)
   (full-screen VM console with send keys, display scaling and resize, clipboard, typing,
   screenshots and CD/DVD insert/eject, and a host panel **Console** tab).
21. [Install guest tools and read guest information](guest-tools.md)
   (QEMU guest agent and spice-vdagent; OS, hostname, addresses and clipboard readiness).
22. [Read VM and host usage history, hardware and temperatures](usage-history.md)
   (**Performance** tab charts up to a year back, with Stacked, zoom and legend toggles, and a
   host's **Hardware** tab, gauges and temperature chart).
23. [Replace a failed system disk](replace-a-failed-system-disk.md)
   (rebuild a host's mirrored system disk from its panel's **Storage** tab while the host keeps
   running).
24. [Check and apply software updates](software-updates.md)
   (Controller and host-local package updates from a configured repo).
25. [Publish a Nex Core release](publish-a-release.md)
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
