# Phase 3 installation chain and placement fault/recovery lab — 2026-09-23

## Status

Disposable nested-KVM lab campaign on one workstation, run against the 0.4.5 working tree.
It closes the master-owned placement fault/recovery matrix and all of the Phase 3 integration
chain except the interrupted appliance deployment, for **disposable virtual-lab scope**. Six defects were found and fixed (BUG-0533 to BUG-0538).
Physical Intel/AMD hardware, a physical BMC or watchdog, and release publication are not claimed.

## Environment and method

- Hosts are QEMU/KVM guests with nested KVM. All of them sit on one isolated, uplink-free Linux
  bridge on the workstation (`10.0.2.0/24`, no NAT, no route to any other network). A lab-only
  DHCP server on that bridge hands out fixed addresses: host A `10.0.2.15`, host B `10.0.2.16`,
  host C `10.0.2.17`, NFS node `10.0.2.30`. The workstation reaches them directly and through
  loopback forwards.
- Hosts were installed from an unmodified ISO built from the working tree by `installer/build.sh`,
  through the graphical wizard (`tests/integration/fresh_install_gui.py` plus manual QMP input
  where noted), with no preseed or disk injection.
  - First build: `nex-core-0.4.5-amd64.iso`, SHA-256 `fa6e11bd2f769eaf…81069a832`.
  - The lab later ran rebuilt `nex-core-local` binaries with the BUG-0536 to BUG-0538 fixes,
    swapped in place of `/usr/lib/nex/nex-core-local`. That is a development deployment, not a
    package upgrade.
- The NFS node is a Debian 13 cloud guest exporting one directory with `nfs-kernel-server`.
- Controller: the bundled appliance that host A deployed. For the replacement case, a fresh
  Controller built from the working tree ran on the workstation's bridge address.
- API checks used authenticated sessions with CSRF tokens. Raw captures, credentials and logs
  stay in ignored local evidence.

## Phase 3: install, eject, reboot, enroll, create a VM

| Gate item | Method | Result |
| --- | --- | --- |
| Offline install under UEFI with enforcing **Secure Boot** | Host A: OVMF secure-boot firmware with the Microsoft keys enrolled, SMM on, network isolated | GRUB and the installer booted under Secure Boot. After installing, `mokutil --sb-state` read `SecureBoot enabled`, before and after a later reboot. |
| Installer refuses without erase confirmation | Clicked install with the erase box unticked | The wizard showed its confirmation error; both target files were still zero in their first MiB. |
| **Multiple disks**, install to one | Host A, two blank disks, only `/dev/vda` ticked | Installed to `vda`. The second disk kept zero allocated bytes, and `lsblk` showed it unpartitioned. |
| Multiple disks, **mirrored** system disk | Host B, BIOS, both disks ticked | Review showed a RAID1 mirror. After install, `/proc/mdstat` showed `md0 raid1 [2/2] [UU]` and `/` on `md0`. |
| **Absent network** during install | Host B's NIC link held down through QMP for the whole install | The wizard showed "No link — connect this adapter" and still accepted DHCP for the management NIC. The install completed. On first boot with the link up, the host took `10.0.2.16` by DHCP on `br0`. |
| **Interrupted installation** and recovery | Host B's power cut mid-install (kernel configuring, RAID already created) | The half-installed disks did not boot; SeaBIOS stopped at "Booting from Hard Disk". The ISO was booted again: the Disk step listed both disks as "Has partitions". The reinstall completed, with no leftover arrays and a clean `[UU]` mirror. |
| Late interruption | Host C's install was cut by a workstation freeze late in the base install | The disk booted plain "Debian GNU/Linux 13" with no Nex Core services and no management network. That is honest, since it claims to be nothing more, but it is not a usable host; reinstall it. |
| Media eject, reboot, first boot | "Restart now" (QEMU exits on reboot), then boot from disk | Console banner `Nex Core 0.4.5`, local web UI address shown, `nex-core-local` active. |
| **Real management networking** | Bridge plus DHCP server instead of QEMU user networking | DHCP leases, peer traffic between hosts, NFS and the appliance all ran on the shared L2 segment. |
| Local Controller bootstrap | `fresh_install_browser.py --stage deploy` (Chromium) | Static-address appliance reached `ready`, and the host was enrolled. |
| **Enroll a second host** | Add host by IP through the Controller API | The probe fingerprint matched the served certificate. A well-formed wrong fingerprint got 502. The retry got 409 "already enrolled". |
| **First managed VM** | `--stage managed` (six-step GUI wizard) | Passed after BUG-0534 was worked around (see Defects). The VM was created, started, and its definition matched. |
| **Recovery access** during a Controller outage | `fresh_install_recovery.py` `controller-stop`, then browser `--stage offline`, then `controller-start` | Root login and the local VM inventory stayed available, and the guest and appliance kept running. |
| Host reboot | `--stage reboot`/`verify-reboot` | Identity, domain XML and disks were kept. Autostart VMs came back running and the others shut off. Pool membership stayed fresh. |
| **Interrupted bootstrap** | Host C: `nex-core-local` SIGKILLed during a DHCP appliance deploy | **Not yet run** — see the [lab handover](../plans/2026-09-23-lab-handover.md). |

The first "restart after ready" kill on host A landed one second after the deployment reached
`ready`, so it counts only as a post-ready restart, not an interrupted bootstrap.

## Placement fault/recovery (`tests/integration/placement_faults.py`)

Pool: master A, member B, shared NFS repository, pool VLAN network. Every case checks one
durable owner and no second writer: `virsh` on both hosts, plus the master's `pool-vms.json`
record with no pending update.

| Case | Result |
| --- | --- |
| `lost-reply` | An exact create or update retry after a dropped HTTP reply returned the same VM and operation. A changed payload under the same `request_id` got 409. One domain, one owner. |
| `shared-edit` | A stopped definition edit, then ISO insert and eject on a member-placed VM on shared storage. The master record and the member's libvirt definition agreed each time; no pending update was left. |
| `storage-loss` (NFS server stopped) | A create on the lost repository failed with no domain. A start stayed `dispatched` and nothing ran it. The running VM was never reported stopped. |
| `storage-loss` (export identity/fsid swapped) | The create and the start both failed with no domain; the running VM stayed running. |
| `membership` | Both race orders were observed. When the create won, the eject was refused ("still owns pool VM records"). When the eject won, the create failed with "master refused pool storage reservation". After emptying B and ejecting it, a create targeting B failed closed. B then rejoined. |
| `interrupt`, target killed during create | After the fix the create settled as `failed` ("interrupted before any VM resource was created"): no domain, no master record, and an exact retry replays it. Before the fix see BUG-0536. |
| `interrupt`, master killed during update | In both runs after the fix, the kill landed after the reply was delivered: the update resolved `succeeded` and the record matched libvirt. The lost-reply ordering (BUG-0537) was observed once before the fix and is covered by unit tests; it was not re-observed in the lab. |
| `replacement` | The old appliance was disabled and shut off. A fresh Controller re-adopted both hosts, the pool name and master, every VM with its ID, name and host, both local storages, the shared NFS repository, the VLAN, both untagged networks under the joined pool, and all media. The retry of an earlier create did not duplicate the VM. |

## Defects found and fixed

- **BUG-0533:** the ISO build failed because it fetched a pinned OVMF version from Debian's moving
  sid suite.
- **BUG-0534:** on a fresh host, `nex-media` was created 0700 under the service umask, so every ISO
  upload to Local storage failed. In the lab this was worked around with `chmod 0711`; the fix has
  a unit test, and its fresh-install check is still open (see the handover).
- **BUG-0535:** a joined member's untagged network stayed projected under the removed auto-pool.
- **BUG-0536:** an interrupted or refused create left a phantom VM, a reserved name and a master
  record, which blocked ejecting the host.
- **BUG-0537:** the master's lost reply to an update reservation left a `pending_update` that
  nothing could settle.
- **BUG-0538:** a deleted VM kept its master record while such an update was pending.

After the fixed `nex-core-local` was deployed, the three stuck master records in the lab cleared
within about a minute, without any operator action.

## Other observations

- After a disk is ticked, the installer's Disk step keeps showing the red "Select the disk…" error
  until Continue is pressed. This is cosmetic.
- The lab harnesses assumed the old wizard coordinates, `-boot d`, a fixed hostname, and
  single-host user networking. They now support the current layout, a shared tap bridge,
  enforcing Secure Boot, and reinstalling onto used disks.

## Limits

Nested virtualization only. Not claimed:
- representative physical Intel/AMD hardware, or a real BMC or watchdog;
- Secure Boot with a vendor key database other than OVMF's Microsoft template;
- a package-upgrade path for the fixes (they were deployed as binaries);
- the master-side lost-reply ordering in the lab after the fix;
- hosted CI (signed off by the project owner, 2026-09-23).
